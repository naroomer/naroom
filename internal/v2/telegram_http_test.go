package v2

// Tests 30-34 for TelegramLinkHandler HTTP endpoints.
// Test 30: privacy scan (full DB scan).
// Test 31: HTTPBotAPISender classification.
// Tests 32-34: HTTP endpoint validation.
// All tests: injectable clock, in-memory SQLite, injected RoundTripper, no TCP, no t.Skip.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── Helper: create a TelegramLinkHandler ──────────────────────────────────

func newTestTelegramLinkHandler(t *testing.T, now func() time.Time) (*TelegramLinkHandler, *TelegramTransport, *Service, *ListingService) {
	t.Helper()
	transport, svc, ls, _ := newTestTransport(t, now)
	rateLimitKey := []byte("test-rate-limit-key-32-bytes!!")
	h, err := NewTelegramLinkHandler(svc, transport, rateLimitKey, now)
	if err != nil {
		t.Fatalf("NewTelegramLinkHandler: %v", err)
	}
	return h, transport, svc, ls
}

// postTelegramJSON is a test helper that sends a POST with JSON body.
func postTelegramJSON(handler http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

// ── Test 30: TestFullDBScanNoRawTokenOrChatID ─────────────────────────────

func TestFullDBScanNoRawTokenOrChatID(t *testing.T) {
	transport, svc, _, db := newTestTransport(t, nil)

	// Create a flow.
	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	_ = flowID

	// CreateLink (generates pending attempt with HMAC hash only).
	result, err := transport.CreateLink(rawCode, "bc1qtest")
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	// Extract raw token.
	const prefix = "?start="
	idx := strings.Index(result.BotURL, prefix)
	rawToken := result.BotURL[idx+len(prefix):]

	// Simulate webhook (creates binding+destination, deletes attempt).
	body := buildWebhookBody(9876543210, "private", "/start "+rawToken)
	sendWebhook(transport, body, string(testWebhookSecret))
	// (May succeed or fail depending on state — we don't care for this test's purpose)

	// Scan ALL v2_* tables for:
	// - raw token (43-char base64url string)
	// - plaintext chat_id (numeric string "9876543210")
	// - management code (rawCode)
	// - wallet address
	sensitiveValues := []string{
		rawToken,
		"9876543210",
		rawCode,
		"bc1qtest",
	}

	tables := []struct {
		name    string
		columns []string
	}{
		{"v2_client_flows", []string{"id", "wallet_fingerprint", "currency", "management_code_hash", "state"}},
		{"v2_invoices", []string{"id", "flow_id", "status", "payment_address", "detected_txid", "payment_txid"}},
		{"v2_client_notification_bindings", []string{"id", "flow_id", "binding_ref", "state"}},
		{"v2_listings", []string{"id", "flow_id", "city", "display_name", "contact_type", "contact_ciphertext", "contact_nonce", "contact_key_version", "state"}},
		{"v2_telegram_link_attempts", []string{"id", "flow_id", "token_hash", "state"}},
		{"v2_telegram_destinations", []string{"binding_ref", "chat_id_ciphertext", "chat_id_nonce", "key_version"}},
	}

	for _, tbl := range tables {
		colList := strings.Join(tbl.columns, ", ")
		rows, err := db.Query(fmt.Sprintf("SELECT %s FROM %s", colList, tbl.name))
		if err != nil {
			t.Fatalf("query %s: %v", tbl.name, err)
		}
		defer rows.Close()
		for rows.Next() {
			vals := make([]any, len(tbl.columns))
			ptrs := make([]any, len(tbl.columns))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err = rows.Scan(ptrs...); err != nil {
				t.Fatalf("scan %s: %v", tbl.name, err)
			}
			for ci, col := range tbl.columns {
				v := fmt.Sprintf("%v", vals[ci])
				for _, sensitive := range sensitiveValues {
					if sensitive != "" && strings.Contains(v, sensitive) {
						t.Errorf("table %s col %s: sensitive value %q found in %q", tbl.name, col, sensitive, v)
					}
				}
			}
		}
	}
}

// ── Test 31: TestHTTPBotAPISenderClassification ───────────────────────────

type mockRoundTripper struct {
	fn func(*http.Request) (*http.Response, error)
}

func (m *mockRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return m.fn(r)
}

func makeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestHTTPBotAPISenderClassification(t *testing.T) {
	cases := []struct {
		name       string
		transport  func(*http.Request) (*http.Response, error)
		wantPerm   bool // true = ErrPermanentDelivery
		wantNil    bool // true = nil error
		wantNonNil bool // true = some error
	}{
		{
			name: "200_ok_true",
			transport: func(*http.Request) (*http.Response, error) {
				return makeResponse(200, `{"ok":true}`), nil
			},
			wantNil: true,
		},
		{
			name: "429_retryable",
			transport: func(*http.Request) (*http.Response, error) {
				return makeResponse(429, `{"ok":false,"description":"Too Many Requests"}`), nil
			},
			wantNonNil: true,
		},
		{
			name: "500_retryable",
			transport: func(*http.Request) (*http.Response, error) {
				return makeResponse(500, `{"ok":false}`), nil
			},
			wantNonNil: true,
		},
		{
			name: "403_permanent",
			transport: func(*http.Request) (*http.Response, error) {
				return makeResponse(403, `{"ok":false,"description":"Forbidden"}`), nil
			},
			wantPerm: true,
		},
		{
			name: "malformed_json_permanent",
			transport: func(*http.Request) (*http.Response, error) {
				return makeResponse(200, `not-json`), nil
			},
			wantPerm: true,
		},
		{
			name: "context_cancelled",
			transport: func(*http.Request) (*http.Response, error) {
				return nil, context.Canceled
			},
			wantNonNil: true,
		},
		{
			name: "ok_false_permanent",
			transport: func(*http.Request) (*http.Response, error) {
				return makeResponse(200, `{"ok":false}`), nil
			},
			wantPerm: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: &mockRoundTripper{fn: tc.transport}}
			sender, err := NewHTTPBotAPISender("https://api.telegram.org", "testtoken", client)
			if err != nil {
				t.Fatalf("NewHTTPBotAPISender: %v", err)
			}

			ctx := context.Background()
			if tc.name == "context_cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel() // pre-cancel
			}

			err = sender.SendMessage(ctx, 123, "test")
			switch {
			case tc.wantNil:
				if err != nil {
					t.Errorf("want nil, got %v", err)
				}
			case tc.wantPerm:
				if !errors.Is(err, ErrPermanentDelivery) {
					t.Errorf("want ErrPermanentDelivery, got %v", err)
				}
			case tc.wantNonNil:
				if err == nil {
					t.Error("want non-nil error, got nil")
				}
				if errors.Is(err, ErrPermanentDelivery) {
					t.Errorf("want retryable error, got ErrPermanentDelivery: %v", err)
				}
			}
		})
	}

	// 429 specifically must NOT be ErrPermanentDelivery.
	t.Run("429_not_permanent", func(t *testing.T) {
		client := &http.Client{Transport: &mockRoundTripper{fn: func(*http.Request) (*http.Response, error) {
			return makeResponse(429, ``), nil
		}}}
		sender, _ := NewHTTPBotAPISender("https://api.telegram.org", "tok", client)
		err := sender.SendMessage(context.Background(), 1, "test")
		if errors.Is(err, ErrPermanentDelivery) {
			t.Error("429 must not be ErrPermanentDelivery")
		}
		if err == nil {
			t.Error("429 must return non-nil error")
		}
	})
}

// ── Test 32: TestTelegramLinkHTTPStrictBody ───────────────────────────────

func TestTelegramLinkHTTPStrictBody(t *testing.T) {
	h, _, _, _ := newTestTelegramLinkHandler(t, nil)
	handler := h.Routes()

	// Wrong Content-Type → 400.
	t.Run("wrong_content_type", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/client/telegram-links", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "text/plain")
		req.RemoteAddr = "127.0.0.1:1"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("wrong content-type: got %d, want 400", w.Code)
		}
	})

	// Unknown fields → 400.
	t.Run("unknown_fields", func(t *testing.T) {
		w := postTelegramJSON(handler, "/v2/client/telegram-links", `{"management_code":"x","wallet_address":"y","unknown_field":"z"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("unknown fields: got %d, want 400", w.Code)
		}
	})

	// Body too large → 413.
	t.Run("body_too_large", func(t *testing.T) {
		largeBody := `{"management_code":"` + strings.Repeat("x", 5000) + `","wallet_address":"y"}`
		req := httptest.NewRequest(http.MethodPost, "/v2/client/telegram-links", strings.NewReader(largeBody))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "127.0.0.1:1"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("body too large: got %d, want 413", w.Code)
		}
	})

	// Missing management_code → 400.
	t.Run("missing_management_code", func(t *testing.T) {
		w := postTelegramJSON(handler, "/v2/client/telegram-links", `{"wallet_address":"bc1qtest"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("missing management_code: got %d, want 400", w.Code)
		}
	})

	// Missing wallet_address → 400.
	t.Run("missing_wallet_address", func(t *testing.T) {
		w := postTelegramJSON(handler, "/v2/client/telegram-links", `{"management_code":"code"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("missing wallet_address: got %d, want 400", w.Code)
		}
	})
}

// ── Test 33: TestTelegramLinkRateLimit ────────────────────────────────────

func TestTelegramLinkRateLimit(t *testing.T) {
	h, _, _, _ := newTestTelegramLinkHandler(t, nil)
	handler := h.Routes()

	// linkLim: 5 requests/minute. 6th request should be 429.
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v2/client/telegram-links", strings.NewReader(`{"management_code":"x","wallet_address":"y"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "10.0.0.1:9999"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		// These will return 400/404 due to invalid data but not 429.
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("unexpected 429 on request %d", i+1)
		}
	}

	// 6th request → 429.
	req := httptest.NewRequest(http.MethodPost, "/v2/client/telegram-links", strings.NewReader(`{"management_code":"x","wallet_address":"y"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.1:9999"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("6th request: got %d, want 429", w.Code)
	}
}

// ── Test 34: TestLinkStatusHTTPMatrix ─────────────────────────────────────

func TestLinkStatusHTTPMatrix(t *testing.T) {
	h, transport, svc, ls := newTestTelegramLinkHandler(t, nil)
	handler := h.Routes()

	// No flow → 404.
	t.Run("no_flow", func(t *testing.T) {
		w := postTelegramJSON(handler, "/v2/client/telegram-links/status", `{"management_code":"nonexistent","wallet_address":"bc1qtest"}`)
		if w.Code != http.StatusNotFound {
			t.Errorf("no flow: got %d, want 404", w.Code)
		}
	})

	// Create a flow but don't confirm it.
	t.Run("flow_not_ready", func(t *testing.T) {
		_, fv, err := svc.CreatePaymentIntent("bc1qnotready", "BTC", validDraft())
		if err != nil {
			t.Fatalf("CreatePaymentIntent: %v", err)
		}
		// flow is awaiting_payment, not confirmed.
		rawCode2, _, _ := func() (string, string, error) {
			// We need the rawCode; use a hack to get it.
			// Actually, re-read from service test pattern.
			return "", fv.FlowID, nil
		}()
		_ = rawCode2
		// The raw code was returned from CreatePaymentIntent but we didn't capture it.
		// Let's just use a separate flow creation.
		rawCodeNR, fvNR, errNR := svc.CreatePaymentIntent("bc1qnotready2", "BTC", validDraft())
		if errNR != nil {
			t.Fatalf("CreatePaymentIntent 2: %v", errNR)
		}
		_ = fvNR
		w := postTelegramJSON(handler, "/v2/client/telegram-links/status",
			fmt.Sprintf(`{"management_code":%q,"wallet_address":"bc1qnotready2"}`, rawCodeNR))
		if w.Code != http.StatusConflict {
			t.Errorf("flow not ready: got %d, want 409", w.Code)
		}
	})

	// Form-ready flow → needs_link.
	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, "bc1qmatrix")

	t.Run("needs_link", func(t *testing.T) {
		w := postTelegramJSON(handler, "/v2/client/telegram-links/status",
			fmt.Sprintf(`{"management_code":%q,"wallet_address":"bc1qmatrix"}`, rawCode))
		if w.Code != http.StatusOK {
			t.Fatalf("needs_link: got %d, want 200", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "needs_link") {
			t.Errorf("needs_link: body %q missing 'needs_link'", body)
		}
	})

	// Create pending attempt → link_pending.
	t.Run("link_pending", func(t *testing.T) {
		if _, err := transport.CreateLink(rawCode, "bc1qmatrix"); err != nil {
			t.Fatalf("CreateLink: %v", err)
		}
		w := postTelegramJSON(handler, "/v2/client/telegram-links/status",
			fmt.Sprintf(`{"management_code":%q,"wallet_address":"bc1qmatrix"}`, rawCode))
		if w.Code != http.StatusOK {
			t.Fatalf("link_pending: got %d, want 200", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "link_pending") {
			t.Errorf("link_pending: body %q missing 'link_pending'", body)
		}
	})

	// Attach ready binding → ready.
	t.Run("ready", func(t *testing.T) {
		// Delete existing attempt.
		svc.db.Exec(`DELETE FROM v2_telegram_link_attempts WHERE flow_id=?`, flowID) //nolint:errcheck

		now := ls.now()
		ref := makeBindingRef()
		if err := ls.attachReadyBinding(flowID, ref, now, now.Add(10*time.Minute)); err != nil {
			t.Fatalf("attachReadyBinding: %v", err)
		}
		// Insert stub destination so QueryLinkStatus INNER JOIN finds it.
		if _, err := svc.db.Exec(`INSERT INTO v2_telegram_destinations
			(binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at)
			VALUES (?, 'stub_ct', 'stub_nonce', 'test_v1', ?, ?)`,
			ref, now.Unix(), now.Add(10*time.Minute).Unix()); err != nil {
			t.Fatalf("insert stub destination: %v", err)
		}

		w := postTelegramJSON(handler, "/v2/client/telegram-links/status",
			fmt.Sprintf(`{"management_code":%q,"wallet_address":"bc1qmatrix"}`, rawCode))
		if w.Code != http.StatusOK {
			t.Fatalf("ready: got %d, want 200", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, `"ready"`) {
			t.Errorf("ready: body %q missing 'ready'", body)
		}
	})

	// Make binding active → active.
	t.Run("active", func(t *testing.T) {
		// Force binding to active state via direct DB update.
		// The destination row was inserted in the "ready" sub-test above; just update the binding.
		now := ls.now()
		svc.db.Exec(`UPDATE v2_client_notification_bindings SET state='active', activated_at=?, valid_until=? WHERE flow_id=?`, //nolint:errcheck
			now.Unix(), now.Add(10*time.Minute).Unix(), flowID)
		// Also update the destination expires_at to keep it live.
		svc.db.Exec(`UPDATE v2_telegram_destinations SET expires_at=? WHERE binding_ref IN (SELECT binding_ref FROM v2_client_notification_bindings WHERE flow_id=?)`, //nolint:errcheck
			now.Add(10*time.Minute).Unix(), flowID)

		w := postTelegramJSON(handler, "/v2/client/telegram-links/status",
			fmt.Sprintf(`{"management_code":%q,"wallet_address":"bc1qmatrix"}`, rawCode))
		if w.Code != http.StatusOK {
			t.Fatalf("active: got %d, want 200", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, `"active"`) {
			t.Errorf("active: body %q missing 'active'", body)
		}
	})
}

// ── Extra: TestNewTelegramLinkHandlerValidation ────────────────────────────

func TestNewTelegramLinkHandlerValidation(t *testing.T) {
	transport, svc, _, _ := newTestTransport(t, nil)

	// nil svc.
	_, err := NewTelegramLinkHandler(nil, transport, []byte("key"), nil)
	if err == nil {
		t.Error("nil svc not rejected")
	}

	// nil transport.
	_, err = NewTelegramLinkHandler(svc, nil, []byte("key"), nil)
	if err == nil {
		t.Error("nil transport not rejected")
	}

	// empty rateLimitKey.
	_, err = NewTelegramLinkHandler(svc, transport, nil, nil)
	if err == nil {
		t.Error("empty rateLimitKey not rejected")
	}
}

// ── Extra: test that webhook endpoint is wired ─────────────────────────────

func TestTelegramLinkHandlerWebhookWired(t *testing.T) {
	h, _, svc, _ := newTestTelegramLinkHandler(t, nil)
	handler := h.Routes()
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	_ = rawCode

	// A webhook POST with wrong secret to the wired endpoint.
	body := bytes.NewReader(buildWebhookBody(1, "private", "/start abc"))
	req := httptest.NewRequest(http.MethodPost, "/v2/telegram/client/webhook", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "wrong")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wired webhook: got %d, want 401", w.Code)
	}
}

// ── Task 04B-FIX HTTP sender tests ───────────────────────────────────────────

// TestHTTPBotAPISenderRejectsNonHTTPSBaseURL verifies that NewHTTPBotAPISender
// rejects non-HTTPS base URLs.
func TestHTTPBotAPISenderRejectsNonHTTPSBaseURL(t *testing.T) {
	client := &http.Client{Transport: &mockRoundTripper{fn: func(*http.Request) (*http.Response, error) {
		return makeResponse(200, `{"ok":true}`), nil
	}}}

	// http:// must be rejected.
	_, err := NewHTTPBotAPISender("http://api.telegram.org", "token", client)
	if err == nil {
		t.Error("http:// URL not rejected")
	}

	// Empty scheme must be rejected.
	_, err = NewHTTPBotAPISender("api.telegram.org", "token", client)
	if err == nil {
		t.Error("no-scheme URL not rejected")
	}

	// https:// must be accepted.
	sender, err := NewHTTPBotAPISender("https://api.telegram.org", "token", client)
	if err != nil {
		t.Errorf("https:// URL rejected: %v", err)
	}
	_ = sender
}

// TestHTTPBotAPISenderOversizedResponseRejected verifies that a response body
// exceeding 64 KiB is classified as ErrPermanentDelivery.
func TestHTTPBotAPISenderOversizedResponseRejected(t *testing.T) {
	// Body slightly over 64 KiB.
	oversizedBody := strings.Repeat("a", 64*1024+1)

	client := &http.Client{Transport: &mockRoundTripper{fn: func(*http.Request) (*http.Response, error) {
		body := `{"ok":true,"result":"` + oversizedBody + `"}`
		return makeResponse(200, body), nil
	}}}

	sender, err := NewHTTPBotAPISender("https://api.telegram.org", "token", client)
	if err != nil {
		t.Fatalf("NewHTTPBotAPISender: %v", err)
	}

	err = sender.SendMessage(context.Background(), 1, "test")
	if err == nil {
		t.Error("oversized response accepted without error")
	}
	// Must be classified as permanent (not retryable).
	if !errors.Is(err, ErrPermanentDelivery) {
		t.Errorf("oversized response: want ErrPermanentDelivery, got %v", err)
	}
}

// ── Task 04B-FIX2 HTTP sender tests ──────────────────────────────────────────

// TestHTTPBotAPISenderOversized429RemainsRetryable verifies that an oversized body
// with HTTP 429 stays retryable (not ErrPermanentDelivery). Status takes priority.
func TestHTTPBotAPISenderOversized429RemainsRetryable(t *testing.T) {
	oversizedBody := strings.Repeat("x", 64*1024+1)
	client := &http.Client{Transport: &mockRoundTripper{fn: func(*http.Request) (*http.Response, error) {
		return makeResponse(429, oversizedBody), nil
	}}}
	sender, err := NewHTTPBotAPISender("https://api.telegram.org", "token", client)
	if err != nil {
		t.Fatalf("NewHTTPBotAPISender: %v", err)
	}
	err = sender.SendMessage(context.Background(), 1, "test")
	if err == nil {
		t.Error("oversized 429: want non-nil error")
	}
	if errors.Is(err, ErrPermanentDelivery) {
		t.Errorf("oversized 429: must NOT be ErrPermanentDelivery, got %v", err)
	}
}

// TestHTTPBotAPISenderOversized500RemainsRetryable verifies that an oversized body
// with HTTP 500 stays retryable (not ErrPermanentDelivery). Status takes priority.
func TestHTTPBotAPISenderOversized500RemainsRetryable(t *testing.T) {
	oversizedBody := strings.Repeat("y", 64*1024+1)
	client := &http.Client{Transport: &mockRoundTripper{fn: func(*http.Request) (*http.Response, error) {
		return makeResponse(500, oversizedBody), nil
	}}}
	sender, err := NewHTTPBotAPISender("https://api.telegram.org", "token", client)
	if err != nil {
		t.Fatalf("NewHTTPBotAPISender: %v", err)
	}
	err = sender.SendMessage(context.Background(), 1, "test")
	if err == nil {
		t.Error("oversized 500: want non-nil error")
	}
	if errors.Is(err, ErrPermanentDelivery) {
		t.Errorf("oversized 500: must NOT be ErrPermanentDelivery, got %v", err)
	}
}
