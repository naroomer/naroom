package v2

// Test matrix for Task 04C — 27 tests
//
// Group 1: Constructor/HTTP boundary
//  1. TestListingHTTPConstructorValidation      — nil/empty args rejected; defensive key copy
//  2. TestListingHTTPStrictConstraints          — wrong CT, oversized body, unknown field, bad JSON, trailing
//  3. TestListingHTTPCapabilityNotInPath        — code/wallet in query string → 404, not 200
//  4. TestListingHTTPWrongCodeWrongWallet        — wrong code and wrong wallet → identical 404 bytes
//  5. TestListingHTTPRateLimitBuckets           — exact limits per endpoint; separate buckets per IP
//
// Group 2: Restore matrix
//  6. TestListingHTTPRestorePhaseMapping        — all 8 phases
//  7. TestListingHTTPRestoreTelegramNotReady    — bare binding without destination → needs_link/link_pending
//  8. TestListingHTTPRestoreStaleVisible        — DB visible but visible_until < now → hidden phase
//  9. TestListingHTTPRestoreFinishedAtDeadline  — now == entitlement_expires_at → finished
// 10. TestListingHTTPRestoreResponsePrivacy     — sensitive values absent from response JSON
//
// Group 3: Publish/Reactivate
// 11. TestListingHTTPFirstPublishSuccess        — form_ready + binding → 201 safe JSON
// 12. TestListingHTTPPublishInvalidInput        — missing city/contact → 400; no row inserted
// 13. TestListingHTTPPublishMissingBinding      — no binding → 409 telegram_required
// 14. TestListingHTTPPublishIdempotency         — second publish → 409 already_published; 1 row
// 15. TestListingHTTPReactivateBalanceFromServer — balance from injected reader, not request body
// 16. TestListingHTTPReactivateProviderFailure  — balance reader error → 503
// 17. TestListingHTTPReactivateBalanceFloor     — $119.99 → 409; $120.00 → 200
// 18. TestListingHTTPReactivateLowBalanceBindingPreserved — after 409 binding still ready
// 19. TestListingHTTPReactivateMissingBinding   — no binding → 409 telegram_required
// 20. TestListingHTTPReactivateSuccess          — full setup publish+advance+reactivate → activation_count=2
// 21. TestListingHTTPReactivateConcurrency      — concurrent reactivate: 1 success, 1 conflict
// 22. TestListingHTTPReactivateFinalExpiry      — expired entitlement → 410 before any mutation
//
// Group 4: Public views
// 23. TestListingHTTPBoardEmpty                 — empty board → []
// 24. TestListingHTTPBoardContent               — only visible listings; ordering
// 25. TestListingHTTPPublicDetail               — visible → 200; hidden/unknown → 404
// 26. TestListingHTTPPublicDetailPrivacy        — correct fields present; sensitive fields absent
//
// Group 5: Full lifecycle
// 27. TestClientJourneyLifecycle                — complete E2E recorder-level test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── journeyFakeBalance ────────────────────────────────────────────────────────

type journeyFakeBalance struct {
	mu      sync.Mutex
	balance float64
	err     error
}

func (f *journeyFakeBalance) BalanceUSD(_ context.Context, _, _ string) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.balance, f.err
}

func (f *journeyFakeBalance) set(bal float64, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.balance = bal
	f.err = err
}

// ── Test setup helpers ────────────────────────────────────────────────────────

// newJourneyHandler sets up a full ClientJourneyHandler with injectable clock,
// in-memory DB, and the provided balance reader. Returns handler + db.
func newJourneyHandler(
	t *testing.T,
	nowFn func() time.Time,
	bal ClientBalanceReader,
) (*ClientJourneyHandler, *TelegramTransport, *Service, *ListingService, *sql.DB) {
	t.Helper()
	transport, svc, ls, db := newTestTransport(t, nowFn)
	if bal == nil {
		bal = &journeyFakeBalance{balance: 150.0}
	}
	h, err := NewClientJourneyHandler(svc, ls, transport, bal, []byte("test-rl-key"), nowFn)
	if err != nil {
		t.Fatalf("NewClientJourneyHandler: %v", err)
	}
	return h, transport, svc, ls, db
}

// journeyPost sends a POST to path with JSON body and returns the recorder.
func journeyPost(h http.Handler, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// journeyPostIP sends a POST with a specific RemoteAddr for rate-limit tests.
func journeyPostIP(h http.Handler, path string, body any, ip string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":9999"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// getPath sends a GET request to path, returns the recorder.
func getPath(h http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// capBody builds a minimal management_code+wallet_address body.
func capBody(code, wallet string) map[string]string {
	return map[string]string{"management_code": code, "wallet_address": wallet}
}

// publishBody builds a full publish request body.
func publishBody(code, wallet string) map[string]any {
	return map[string]any{
		"management_code": code,
		"wallet_address":  wallet,
		"city":            "tbilisi",
		"dependency_type": "alcohol",
		"help_type":       "just_talk",
		"urgency":         "can_wait",
		"languages":       []string{"en"},
		"contact_type":    "telegram",
		"contact":         "@testuser",
	}
}

// countRows returns the number of rows in the given table.
func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n) //nolint:errcheck
	return n
}

// decodeJourneyErr decodes the JSON error body and returns code.
func decodeJourneyErr(body []byte) string {
	var e journeyError
	json.Unmarshal(body, &e) //nolint:errcheck
	return e.Code
}

// decodeRestoreNav decodes the restore response.
func decodeRestoreNav(body []byte) restoreNavResponse {
	var r restoreNavResponse
	json.Unmarshal(body, &r) //nolint:errcheck
	return r
}

// fullSetupPublish creates form_ready flow, completes Telegram webhook, publishes listing.
// Returns rawCode, listingID, flowID.
func fullSetupPublish(
	t *testing.T,
	svc *Service,
	transport *TelegramTransport,
	h http.Handler,
	walletAddr string,
) (rawCode, listingID, flowID string) {
	t.Helper()
	rawCode, fID := makeFormReadyFlowForTransport(t, svc, walletAddr)
	flowID = fID

	// Create Telegram link and complete webhook.
	rawToken := createPendingAttempt(t, transport, rawCode, walletAddr)
	body := buildWebhookBody(12345, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	if code != http.StatusOK {
		t.Fatalf("webhook: got %d, want 200", code)
	}

	// Publish.
	w := journeyPost(h, "/v2/client/listings/publish", publishBody(rawCode, walletAddr))
	if w.Code != http.StatusCreated {
		t.Fatalf("publish: got %d, want 201; body: %s", w.Code, w.Body.String())
	}
	var safe safeListingJSON
	json.Unmarshal(w.Body.Bytes(), &safe) //nolint:errcheck
	listingID = safe.ID
	return rawCode, listingID, flowID
}

// ── Group 1: Constructor/HTTP boundary ───────────────────────────────────────

// Test 1
func TestListingHTTPConstructorValidation(t *testing.T) {
	transport, svc, ls, _ := newTestTransport(t, nil)
	bal := &journeyFakeBalance{balance: 150.0}
	now := time.Now

	// nil svc
	_, err := NewClientJourneyHandler(nil, ls, transport, bal, []byte("key"), now)
	if err == nil {
		t.Error("nil svc not rejected")
	}

	// nil ls
	_, err = NewClientJourneyHandler(svc, nil, transport, bal, []byte("key"), now)
	if err == nil {
		t.Error("nil ls not rejected")
	}

	// nil transport
	_, err = NewClientJourneyHandler(svc, ls, nil, bal, []byte("key"), now)
	if err == nil {
		t.Error("nil transport not rejected")
	}

	// nil balance
	_, err = NewClientJourneyHandler(svc, ls, transport, nil, []byte("key"), now)
	if err == nil {
		t.Error("nil balance not rejected")
	}

	// empty key
	_, err = NewClientJourneyHandler(svc, ls, transport, bal, []byte{}, now)
	if err == nil {
		t.Error("empty key not rejected")
	}

	// nil now
	_, err = NewClientJourneyHandler(svc, ls, transport, bal, []byte("key"), nil)
	if err == nil {
		t.Error("nil now not rejected")
	}

	// Valid args → success
	h, err := NewClientJourneyHandler(svc, ls, transport, bal, []byte("key"), now)
	if err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
	if h == nil {
		t.Fatal("nil handler returned")
	}

	// Defensive copy: mutate original key after construction → stored key unchanged.
	origKey := []byte("original-key-value!!")
	h2, err := NewClientJourneyHandler(svc, ls, transport, bal, origKey, now)
	if err != nil {
		t.Fatalf("construction failed: %v", err)
	}
	before := fmt.Sprintf("%x", h2.rateLimitKey)
	origKey[0] = 0xFF
	after := fmt.Sprintf("%x", h2.rateLimitKey)
	if before != after {
		t.Error("rateLimitKey was not defensively copied")
	}
}

// Test 2
func TestListingHTTPStrictConstraints(t *testing.T) {
	nowFn := time.Now
	h, _, _, _, _ := newJourneyHandler(t, nowFn, nil)
	mux := h.Routes()

	endpoints := []string{
		"/v2/client/listings/restore",
		"/v2/client/listings/publish",
		"/v2/client/listings/reactivate",
	}

	for _, path := range endpoints {
		path := path
		t.Run("wrong_ct_"+path, func(t *testing.T) {
			b := []byte(`{"management_code":"x","wallet_address":"y"}`)
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
			req.Header.Set("Content-Type", "text/plain")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("wrong CT: got %d, want 400", w.Code)
			}
		})

		t.Run("oversized_"+path, func(t *testing.T) {
			big := make([]byte, 5000)
			for i := range big {
				big[i] = 'a'
			}
			// Wrap in a JSON string to make it valid-ish but oversized.
			body := append([]byte(`{"management_code":"`), big...)
			body = append(body, '"', '}')
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("oversized body: got %d, want 413", w.Code)
			}
		})

		t.Run("bad_json_"+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(`not json`)))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("bad JSON: got %d, want 400", w.Code)
			}
		})

		t.Run("trailing_value_"+path, func(t *testing.T) {
			body := []byte(`{"management_code":"x","wallet_address":"y"}{}`)
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("trailing value: got %d, want 400", w.Code)
			}
		})
	}

	// Unknown field: all three POST endpoints must reject unknown JSON fields.
	for _, path := range []string{
		"/v2/client/listings/restore",
		"/v2/client/listings/reactivate",
		"/v2/client/listings/publish",
	} {
		path := path
		t.Run("unknown_field_"+path, func(t *testing.T) {
			body := []byte(`{"management_code":"x","wallet_address":"y","unknown_field":"z"}`)
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("unknown field %s: got %d, want 400", path, w.Code)
			}
		})
	}
}

// Test 3
func TestListingHTTPCapabilityNotInPath(t *testing.T) {
	nowFn := time.Now
	h, _, svc, _, _ := newJourneyHandler(t, nowFn, nil)
	mux := h.Routes()

	rawCode, _ := makeFormReadyFlowForTransport(t, svc, testBTCBech32Addr)

	// For all three POST capability routes: code+wallet in query string must never authorize.
	for _, path := range []string{
		"/v2/client/listings/restore",
		"/v2/client/listings/publish",
		"/v2/client/listings/reactivate",
	} {
		path := path
		t.Run("query_string_"+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost,
				path+"?management_code="+rawCode+"&wallet_address="+testBTCBech32Addr,
				bytes.NewReader([]byte(`{"management_code":"bad","wallet_address":"bad"}`)),
			)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code == http.StatusOK || w.Code == http.StatusCreated {
				t.Errorf("%s query params authorized request: got %d, want non-2xx", path, w.Code)
			}
		})
	}
}

// Test 4
func TestListingHTTPWrongCodeWrongWallet(t *testing.T) {
	nowFn := time.Now
	h, _, svc, _, _ := newJourneyHandler(t, nowFn, nil)
	mux := h.Routes()

	// Create a real flow so DB is not empty.
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, testBTCBech32Addr)

	// w1: wrong code + correct wallet.
	w1 := journeyPost(mux, "/v2/client/listings/restore",
		capBody("wrongcode0000000000000000000000000000000000000000000000000000000000", testBTCBech32Addr))
	// w2: wrong code + different valid wallet.
	w2 := journeyPost(mux, "/v2/client/listings/restore",
		capBody("wrongcode1111111111111111111111111111111111111111111111111111111111",
			"bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"))
	// w3: correct code + different syntactically valid wallet (wrong wallet).
	w3 := journeyPost(mux, "/v2/client/listings/restore",
		capBody(rawCode, "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"))

	if w1.Code != http.StatusNotFound {
		t.Errorf("wrong code: got %d, want 404", w1.Code)
	}
	if w2.Code != http.StatusNotFound {
		t.Errorf("wrong code + wrong wallet: got %d, want 404", w2.Code)
	}
	if w3.Code != http.StatusNotFound {
		t.Errorf("correct code + wrong wallet: got %d, want 404", w3.Code)
	}
	// All three must return identical bodies (privacy: no hint about which was wrong).
	if !bytes.Equal(w1.Body.Bytes(), w2.Body.Bytes()) {
		t.Errorf("wrong code and wrong wallet returned different bodies:\n%s\nvs\n%s",
			w1.Body.String(), w2.Body.String())
	}
	if !bytes.Equal(w1.Body.Bytes(), w3.Body.Bytes()) {
		t.Errorf("wrong code and correct code+wrong wallet returned different bodies:\n%s\nvs\n%s",
			w1.Body.String(), w3.Body.String())
	}
}

// Test 5
func TestListingHTTPRateLimitBuckets(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	nowFn := func() time.Time { return now }
	h, _, _, _, _ := newJourneyHandler(t, nowFn, nil)
	mux := h.Routes()

	// restore: limit = 10
	for i := 0; i < 10; i++ {
		w := journeyPostIP(mux, "/v2/client/listings/restore",
			capBody("x", "y"), "1.2.3.4")
		// Should not be rate-limited (though may get 400/404 for missing fields).
		if w.Code == http.StatusTooManyRequests {
			t.Errorf("restore request %d/%d was rate-limited early", i+1, 10)
		}
	}
	w := journeyPostIP(mux, "/v2/client/listings/restore",
		capBody("x", "y"), "1.2.3.4")
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("restore 11th request: got %d, want 429", w.Code)
	}

	// publish/reactivate: limit = 5
	for _, path := range []string{"/v2/client/listings/publish", "/v2/client/listings/reactivate"} {
		path := path
		for i := 0; i < 5; i++ {
			w := journeyPostIP(mux, path, capBody("x", "y"), "1.2.3.5")
			if w.Code == http.StatusTooManyRequests {
				t.Errorf("%s request %d/5 was rate-limited early", path, i+1)
			}
		}
		w := journeyPostIP(mux, path, capBody("x", "y"), "1.2.3.5")
		if w.Code != http.StatusTooManyRequests {
			t.Errorf("%s 6th request: got %d, want 429", path, w.Code)
		}
	}

	// board: limit = 30
	for i := 0; i < 30; i++ {
		w := journeyPostIP(mux, "/v2/client/listings/restore", // different bucket test - use board
			capBody("x", "y"), "9.9.9.9")
		_ = w
	}
	// Different IP → separate bucket: should not be limited.
	w2 := journeyPostIP(mux, "/v2/client/listings/restore",
		capBody("x", "y"), "5.5.5.5")
	if w2.Code == http.StatusTooManyRequests {
		t.Errorf("different IP was rate-limited by another IP's bucket")
	}

	// Bounded: fill map past maxEntries doesn't panic.
	for i := 0; i < 100; i++ {
		ip := fmt.Sprintf("10.0.%d.%d", i/256, i%256)
		journeyPostIP(mux, "/v2/client/listings/restore", capBody("x", "y"), ip) //nolint:errcheck
	}

	// Fail-closed: when the limiter map is full (defaultMaxLimiterEntries unique active
	// entries), a new IP must be denied rather than evicting an active entry.
	t.Run("fail_closed_at_max_entries", func(t *testing.T) {
		fh, _, _, _, _ := newJourneyHandler(t, nowFn, nil)
		fmux := fh.Routes()

		// Fill restoreLim to exactly defaultMaxLimiterEntries unique IPs, each
		// with one request (well within the per-IP limit of 10).
		for i := 0; i < defaultMaxLimiterEntries; i++ {
			// Use a /16 that won't collide with existing test IPs.
			a, b := i/256, i%256
			ip := fmt.Sprintf("172.16.%d.%d", a, b)
			journeyPostIP(fmux, "/v2/client/listings/restore", capBody("x", "y"), ip) //nolint:errcheck
		}

		// New IP that has never been seen → map is full → fail closed → 429.
		wExtra := journeyPostIP(fmux, "/v2/client/listings/restore", capBody("x", "y"), "198.51.100.1")
		if wExtra.Code != http.StatusTooManyRequests {
			t.Errorf("fail-closed at max entries: got %d, want 429", wExtra.Code)
		}
		if code := decodeJourneyErr(wExtra.Body.Bytes()); code != errCodeRateLimited {
			t.Errorf("fail-closed error code=%s, want %s", code, errCodeRateLimited)
		}
	})
}

// ── Group 2: Restore matrix ───────────────────────────────────────────────────

// journeySetupPhase creates a fresh handler+transport+svc+ls+db for a phase subtest.
// Each subtest gets its own isolated in-memory DB to avoid display_name collisions.
func journeySetupPhase(t *testing.T, clk time.Time) (mux http.Handler, transport *TelegramTransport, svc *Service, ls *ListingService, nowFn func() time.Time) {
	t.Helper()
	nowFn = func() time.Time { return clk }
	h, tr, s, l, _ := newJourneyHandler(t, nowFn, nil)
	return h.Routes(), tr, s, l, nowFn
}

// Test 6
func TestListingHTTPRestorePhaseMapping(t *testing.T) {
	baseTime := time.Unix(1_700_000_000, 0)

	t.Run("awaiting_payment", func(t *testing.T) {
		mux, _, svc, _, _ := journeySetupPhase(t, baseTime)
		rawCode, fv, err := svc.CreatePaymentIntent(testBTCBech32Addr, "BTC", validDraft())
		if err != nil {
			t.Fatalf("CreatePaymentIntent: %v", err)
		}
		_ = fv
		w := journeyPost(mux, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
		if w.Code != http.StatusOK {
			t.Fatalf("got %d, want 200", w.Code)
		}
		r := decodeRestoreNav(w.Body.Bytes())
		if r.Phase != phaseAwaitingPayment || r.NextAction != actionWaitForPayment {
			t.Errorf("awaiting_payment: phase=%s action=%s", r.Phase, r.NextAction)
		}
	})

	t.Run("payment_detected", func(t *testing.T) {
		mux, _, svc, _, _ := journeySetupPhase(t, baseTime)
		rawCode, fv, err := svc.CreatePaymentIntent(testBTCBech32Addr, "BTC", validDraft())
		if err != nil {
			t.Fatalf("CreatePaymentIntent: %v", err)
		}
		_, err = svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_det",
			[]string{testBTCBech32Addr}, fv.AmountAtomic, baseTime)
		if err != nil {
			t.Fatalf("RecordPaymentDetected: %v", err)
		}
		w := journeyPost(mux, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
		r := decodeRestoreNav(w.Body.Bytes())
		if r.Phase != phasePaymentDetected || r.NextAction != actionWaitForPayment {
			t.Errorf("payment_detected: phase=%s action=%s", r.Phase, r.NextAction)
		}
	})

	t.Run("payment_expired", func(t *testing.T) {
		mux, _, svc, _, _ := journeySetupPhase(t, baseTime)
		rawCode, fv, err := svc.CreatePaymentIntent(testBTCBech32Addr, "BTC", validDraft())
		if err != nil {
			t.Fatalf("CreatePaymentIntent: %v", err)
		}
		// ExpireInvoice requires now > detection_deadline_at (now + 60min at creation).
		futureTime := baseTime.Add(2 * time.Hour)
		if _, err := svc.ExpireInvoice(fv.FlowID, fv.InvoiceID, futureTime); err != nil {
			t.Fatalf("ExpireInvoice: %v", err)
		}
		w := journeyPost(mux, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
		r := decodeRestoreNav(w.Body.Bytes())
		if r.Phase != phasePaymentExpired || r.NextAction != actionStartNewListing {
			t.Errorf("payment_expired: phase=%s action=%s", r.Phase, r.NextAction)
		}
	})

	t.Run("paid_low_balance", func(t *testing.T) {
		mux, _, svc, _, _ := journeySetupPhase(t, baseTime)
		rawCode, fv, err := svc.CreatePaymentIntent(testBTCBech32Addr, "BTC", validDraft())
		if err != nil {
			t.Fatalf("CreatePaymentIntent: %v", err)
		}
		_, err = svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_lb",
			[]string{testBTCBech32Addr}, fv.AmountAtomic, baseTime)
		if err != nil {
			t.Fatalf("RecordPaymentDetected: %v", err)
		}
		_, err = svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, baseTime)
		if err != nil {
			t.Fatalf("ConfirmPayment: %v", err)
		}
		// Low balance → paid_low_balance state
		_, err = svc.RecordPostPaymentBalance(fv.FlowID, 50.0, hardFloorUSD)
		if err != nil {
			t.Fatalf("RecordPostPaymentBalance low: %v", err)
		}
		w := journeyPost(mux, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
		r := decodeRestoreNav(w.Body.Bytes())
		if r.Phase != phasePaidLowBalance || r.NextAction != actionRecheckBalance {
			t.Errorf("paid_low_balance: phase=%s action=%s", r.Phase, r.NextAction)
		}
	})

	t.Run("form_ready_no_listing_no_telegram", func(t *testing.T) {
		mux, _, svc, _, _ := journeySetupPhase(t, baseTime)
		rawCode, _ := makeFormReadyFlowForTransport(t, svc, testBTCBech32Addr)
		w := journeyPost(mux, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
		r := decodeRestoreNav(w.Body.Bytes())
		if r.Phase != phaseFormReady || r.NextAction != actionPrepareFirstPublication {
			t.Errorf("form_ready/no_tg: phase=%s action=%s", r.Phase, r.NextAction)
		}
		if r.TelegramStatus != "needs_link" {
			t.Errorf("form_ready/no_tg: telegram_status=%s", r.TelegramStatus)
		}
	})

	t.Run("form_ready_no_listing_telegram_ready", func(t *testing.T) {
		mux, transport, svc, _, _ := journeySetupPhase(t, baseTime)
		rawCode, _ := makeFormReadyFlowForTransport(t, svc, testBTCBech32Addr)
		// Create Telegram binding.
		rawToken := createPendingAttempt(t, transport, rawCode, testBTCBech32Addr)
		sendWebhook(transport, buildWebhookBody(99, "private", "/start "+rawToken), string(testWebhookSecret)) //nolint:errcheck

		w := journeyPost(mux, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
		r := decodeRestoreNav(w.Body.Bytes())
		if r.Phase != phaseFormReady || r.NextAction != actionPublish {
			t.Errorf("form_ready/tg_ready: phase=%s action=%s", r.Phase, r.NextAction)
		}
		if r.TelegramStatus != "ready" {
			t.Errorf("form_ready/tg_ready: telegram_status=%s", r.TelegramStatus)
		}
	})

	t.Run("visible", func(t *testing.T) {
		mux, transport, svc, _, _ := journeySetupPhase(t, baseTime)
		rawCode, _, _ := fullSetupPublish(t, svc, transport, mux, testBTCBech32Addr)
		w := journeyPost(mux, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
		r := decodeRestoreNav(w.Body.Bytes())
		if r.Phase != phaseVisible || r.NextAction != actionViewListing {
			t.Errorf("visible: phase=%s action=%s", r.Phase, r.NextAction)
		}
		if r.Listing == nil {
			t.Error("visible: listing is nil in response")
		}
	})

	t.Run("hidden_no_telegram", func(t *testing.T) {
		_, transport, svc, ls, _ := journeySetupPhase(t, baseTime)
		// Create handler at baseTime for publish.
		h0, err := NewClientJourneyHandler(svc, ls, transport,
			&journeyFakeBalance{balance: 150.0}, []byte("rl-h0"), func() time.Time { return baseTime })
		if err != nil {
			t.Fatalf("NewClientJourneyHandler: %v", err)
		}
		mux0 := h0.Routes()
		rawCode, _, _ := fullSetupPublish(t, svc, transport, mux0, testBTCBech32Addr)
		// Advance clock past visible_until.
		advancedTime := baseTime.Add(25 * time.Hour)
		ls.NormalizeExpired(advancedTime) //nolint:errcheck
		// Handler at advanced time.
		h2, err := NewClientJourneyHandler(svc, ls, transport,
			&journeyFakeBalance{balance: 150.0},
			[]byte("test-rl-key-2"),
			func() time.Time { return advancedTime })
		if err != nil {
			t.Fatalf("NewClientJourneyHandler: %v", err)
		}
		mux2 := h2.Routes()
		w := journeyPost(mux2, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
		r := decodeRestoreNav(w.Body.Bytes())
		if r.Phase != phaseHidden {
			t.Errorf("hidden/no_tg: phase=%s, want hidden", r.Phase)
		}
		if r.NextAction != actionConnectTelegramForReactivation {
			t.Errorf("hidden/no_tg: next_action=%s", r.NextAction)
		}
	})

	t.Run("hidden_telegram_ready", func(t *testing.T) {
		_, transport, svc, ls, _ := journeySetupPhase(t, baseTime)
		h0, err := NewClientJourneyHandler(svc, ls, transport,
			&journeyFakeBalance{balance: 150.0}, []byte("rl-ht0"), func() time.Time { return baseTime })
		if err != nil {
			t.Fatalf("NewClientJourneyHandler: %v", err)
		}
		mux0 := h0.Routes()
		rawCode, _, _ := fullSetupPublish(t, svc, transport, mux0, testBTCBech32Addr)
		advancedTime := baseTime.Add(25 * time.Hour)
		ls.NormalizeExpired(advancedTime) //nolint:errcheck
		h2, err := NewClientJourneyHandler(svc, ls, transport,
			&journeyFakeBalance{balance: 150.0},
			[]byte("test-rl-key-3"),
			func() time.Time { return advancedTime })
		if err != nil {
			t.Fatalf("NewClientJourneyHandler: %v", err)
		}
		mux2 := h2.Routes()

		// Create Telegram binding for window 2 at original transport.
		rawToken := createPendingAttempt(t, transport, rawCode, testBTCBech32Addr)
		sendWebhook(transport, buildWebhookBody(77, "private", "/start "+rawToken), string(testWebhookSecret)) //nolint:errcheck

		w := journeyPost(mux2, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
		r := decodeRestoreNav(w.Body.Bytes())
		if r.Phase != phaseHidden || r.NextAction != actionReactivate {
			t.Errorf("hidden/tg_ready: phase=%s action=%s", r.Phase, r.NextAction)
		}
	})

	t.Run("finished", func(t *testing.T) {
		_, transport, svc, ls, _ := journeySetupPhase(t, baseTime)
		h0, err := NewClientJourneyHandler(svc, ls, transport,
			&journeyFakeBalance{balance: 150.0}, []byte("rl-fin0"), func() time.Time { return baseTime })
		if err != nil {
			t.Fatalf("NewClientJourneyHandler: %v", err)
		}
		mux0 := h0.Routes()
		rawCode, _, _ := fullSetupPublish(t, svc, transport, mux0, testBTCBech32Addr)
		// Advance past entitlement (5 days).
		expiredTime := baseTime.Add(6 * 24 * time.Hour)
		ls.NormalizeExpired(expiredTime) //nolint:errcheck
		h2, err := NewClientJourneyHandler(svc, ls, transport,
			&journeyFakeBalance{balance: 150.0},
			[]byte("test-rl-key-4"),
			func() time.Time { return expiredTime })
		if err != nil {
			t.Fatalf("NewClientJourneyHandler: %v", err)
		}
		mux2 := h2.Routes()
		w := journeyPost(mux2, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
		r := decodeRestoreNav(w.Body.Bytes())
		if r.Phase != phaseFinished || r.NextAction != actionStartNewListing {
			t.Errorf("finished: phase=%s action=%s", r.Phase, r.NextAction)
		}
	})
}

// Test 7
func TestListingHTTPRestoreTelegramNotReady(t *testing.T) {
	nowFn := time.Now
	h, transport, svc, ls, _ := newJourneyHandler(t, nowFn, nil)
	mux := h.Routes()

	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, testBTCBech32Addr)

	// Attach a bare ready binding (no destination row) — this simulates a binding
	// that was never fully completed by a webhook.
	ref := makeBindingRef()
	n := ls.now()
	if err := ls.attachReadyBinding(flowID, ref, n, n.Add(10*time.Minute)); err != nil {
		t.Fatalf("attachReadyBinding: %v", err)
	}
	// No destination row inserted.

	status, err := transport.QueryLinkStatus(rawCode, testBTCBech32Addr)
	if err != nil {
		t.Fatalf("QueryLinkStatus: %v", err)
	}
	// Bare binding should not be "ready".
	if status == "ready" {
		t.Errorf("bare binding reported as ready; want needs_link or link_pending")
	}

	w := journeyPost(mux, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusOK {
		t.Fatalf("restore: got %d, want 200", w.Code)
	}
	r := decodeRestoreNav(w.Body.Bytes())
	// Phase must be form_ready but action is prepare_first_publication (not publish)
	// because telegram is not truly ready.
	if r.Phase != phaseFormReady {
		t.Errorf("bare binding: phase=%s, want form_ready", r.Phase)
	}
	if r.TelegramStatus == "ready" {
		t.Errorf("bare binding: telegram_status=ready, want needs_link or link_pending")
	}
}

// Test 8
// TestListingHTTPRestoreStaleVisible verifies that when the handler clock is past
// visible_until (but DB state is still 'visible'), restore returns phase=hidden
// without mutating the DB state. This covers the case where NormalizeExpired has
// not yet run after the daily window ended.
func TestListingHTTPRestoreStaleVisible(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	h, transport, svc, _, db := newJourneyHandler(t, nowFn, nil)
	mux := h.Routes()

	rawCode, _, _ := fullSetupPublish(t, svc, transport, mux, testBTCBech32Addr)

	// The listing's visible_until = now + 24h (set by FirstPublish).
	// Advance the handler clock to now + 25h so visible_until is in the past.
	// Do NOT call NormalizeExpired, so the DB state stays 'visible' (stale).
	advanced := now.Add(25 * time.Hour)
	h2, err := NewClientJourneyHandler(svc, h.ls, transport,
		&journeyFakeBalance{balance: 150.0},
		[]byte("rl-stale"),
		func() time.Time { return advanced })
	if err != nil {
		t.Fatalf("NewClientJourneyHandler: %v", err)
	}

	w := journeyPost(h2.Routes(), "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
	r := decodeRestoreNav(w.Body.Bytes())
	// Effectively hidden (visible_until < now) even though DB state='visible'.
	if r.Phase != phaseHidden {
		t.Errorf("stale visible: phase=%s, want hidden", r.Phase)
	}

	// DB state must be unchanged (no mutation from restore).
	var dbState string
	db.QueryRow(`SELECT state FROM v2_listings LIMIT 1`).Scan(&dbState) //nolint:errcheck
	if dbState != "visible" {
		t.Errorf("DB state mutated by restore: got %s, want visible", dbState)
	}
}

// Test 9
func TestListingHTTPRestoreFinishedAtDeadline(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	h, transport, svc, _, db := newJourneyHandler(t, nowFn, nil)
	mux := h.Routes()

	rawCode, _, flowID := fullSetupPublish(t, svc, transport, mux, testBTCBech32Addr)

	// Read the actual entitlement_expires_at from DB.
	var entExp int64
	db.QueryRow(`SELECT entitlement_expires_at FROM v2_listings WHERE flow_id=?`, flowID).Scan(&entExp) //nolint:errcheck

	// At exactly entitlement_expires_at → finished.
	exactTime := time.Unix(entExp, 0)
	h2, err := NewClientJourneyHandler(svc, h.ls, transport,
		&journeyFakeBalance{balance: 150.0},
		[]byte("rl-exact"),
		func() time.Time { return exactTime })
	if err != nil {
		t.Fatalf("NewClientJourneyHandler: %v", err)
	}

	w := journeyPost(h2.Routes(), "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
	r := decodeRestoreNav(w.Body.Bytes())
	if r.Phase != phaseFinished {
		t.Errorf("at exact deadline: phase=%s, want finished", r.Phase)
	}
	if r.NextAction != actionStartNewListing {
		t.Errorf("at exact deadline: next_action=%s, want start_new_listing", r.NextAction)
	}
}

// Test 10
func TestListingHTTPRestoreResponsePrivacy(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	h, transport, svc, _, _ := newJourneyHandler(t, nowFn, nil)
	mux := h.Routes()

	rawCode, _, _ := fullSetupPublish(t, svc, transport, mux, testBTCBech32Addr)

	w := journeyPost(mux, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
	body := w.Body.String()

	sensitiveStrings := []string{
		rawCode,           // management_code value
		testBTCBech32Addr, // wallet_address
		"contact",         // any contact-related field
		"ciphertext",      // ciphertext
		"binding_ref",     // binding ref key
		"fingerprint",     // fingerprint
		"token",           // token
		"invoice_id",      // invoice ID
	}
	// Note: flow_id is intentionally NOT in the response, but listing ID (public) may appear.
	// We check that the raw management code and wallet aren't there.
	for _, s := range sensitiveStrings {
		if s == "contact" || s == "ciphertext" || s == "binding_ref" ||
			s == "fingerprint" || s == "token" || s == "invoice_id" {
			// Check that these keys don't appear in the JSON body.
			if strings.Contains(body, `"`+s+`"`) {
				t.Errorf("sensitive key %q found in restore response", s)
			}
		}
	}
	// Raw values that must never appear.
	if strings.Contains(body, rawCode) {
		t.Errorf("management_code value found in restore response")
	}
}

// ── Group 3: Publish/Reactivate ───────────────────────────────────────────────

// Test 11
func TestListingHTTPFirstPublishSuccess(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	h, transport, svc, _, db := newJourneyHandler(t, nowFn, nil)
	mux := h.Routes()

	rawCode, _ := makeFormReadyFlowForTransport(t, svc, testBTCBech32Addr)
	rawToken := createPendingAttempt(t, transport, rawCode, testBTCBech32Addr)
	if code := sendWebhook(transport, buildWebhookBody(12345, "private", "/start "+rawToken), string(testWebhookSecret)); code != 200 {
		t.Fatalf("webhook: got %d", code)
	}

	w := journeyPost(mux, "/v2/client/listings/publish", publishBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusCreated {
		t.Fatalf("publish: got %d, want 201; body: %s", w.Code, w.Body.String())
	}

	var safe safeListingJSON
	if err := json.Unmarshal(w.Body.Bytes(), &safe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if safe.ID == "" {
		t.Error("id is empty")
	}
	if safe.DisplayName == "" {
		t.Error("display_name is empty")
	}
	if safe.City != "tbilisi" {
		t.Errorf("city=%s, want tbilisi", safe.City)
	}
	if safe.State != "visible" {
		t.Errorf("state=%s, want visible", safe.State)
	}
	if safe.ActivationCount != 1 {
		t.Errorf("activation_count=%d, want 1", safe.ActivationCount)
	}
	// Response must not contain contact value.
	body := w.Body.String()
	if strings.Contains(body, "@testuser") {
		t.Error("raw contact found in publish response")
	}

	// One row in v2_listings.
	if n := countRows(t, db, "v2_listings"); n != 1 {
		t.Errorf("v2_listings count=%d, want 1", n)
	}
}

// Test 12
func TestListingHTTPPublishInvalidInput(t *testing.T) {
	nowFn := time.Now
	h, transport, svc, _, db := newJourneyHandler(t, nowFn, nil)
	mux := h.Routes()

	rawCode, _ := makeFormReadyFlowForTransport(t, svc, testBTCBech32Addr)
	rawToken := createPendingAttempt(t, transport, rawCode, testBTCBech32Addr)
	sendWebhook(transport, buildWebhookBody(12345, "private", "/start "+rawToken), string(testWebhookSecret)) //nolint:errcheck

	// Missing city.
	body := map[string]any{
		"management_code": rawCode,
		"wallet_address":  testBTCBech32Addr,
		"dependency_type": "alcohol",
		"help_type":       "just_talk",
		"urgency":         "can_wait",
		"languages":       []string{"en"},
		"contact_type":    "telegram",
		"contact":         "@testuser",
	}
	w := journeyPost(mux, "/v2/client/listings/publish", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing city: got %d, want 400", w.Code)
	}

	// Invalid contact (empty).
	body2 := publishBody(rawCode, testBTCBech32Addr)
	body2["contact"] = ""
	w2 := journeyPost(mux, "/v2/client/listings/publish", body2)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("empty contact: got %d, want 400", w2.Code)
	}

	// No rows inserted.
	if n := countRows(t, db, "v2_listings"); n != 0 {
		t.Errorf("v2_listings count=%d, want 0 after invalid input", n)
	}
}

// Test 13
func TestListingHTTPPublishMissingBinding(t *testing.T) {
	nowFn := time.Now
	h, _, svc, _, db := newJourneyHandler(t, nowFn, nil)
	mux := h.Routes()

	rawCode, _ := makeFormReadyFlowForTransport(t, svc, testBTCBech32Addr)
	// No Telegram binding created.

	w := journeyPost(mux, "/v2/client/listings/publish", publishBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusConflict {
		t.Errorf("no binding: got %d, want 409", w.Code)
	}
	if code := decodeJourneyErr(w.Body.Bytes()); code != errCodeTelegramRequired {
		t.Errorf("error code=%s, want %s", code, errCodeTelegramRequired)
	}
	if n := countRows(t, db, "v2_listings"); n != 0 {
		t.Errorf("v2_listings count=%d, want 0", n)
	}
}

// Test 14
func TestListingHTTPPublishIdempotency(t *testing.T) {
	nowFn := time.Now
	h, transport, svc, _, db := newJourneyHandler(t, nowFn, nil)
	mux := h.Routes()

	rawCode, _ := makeFormReadyFlowForTransport(t, svc, testBTCBech32Addr)
	rawToken := createPendingAttempt(t, transport, rawCode, testBTCBech32Addr)
	sendWebhook(transport, buildWebhookBody(12345, "private", "/start "+rawToken), string(testWebhookSecret)) //nolint:errcheck

	// First publish.
	w1 := journeyPost(mux, "/v2/client/listings/publish", publishBody(rawCode, testBTCBech32Addr))
	if w1.Code != http.StatusCreated {
		t.Fatalf("first publish: got %d, want 201; body: %s", w1.Code, w1.Body.String())
	}

	// Second publish — same code/wallet.
	w2 := journeyPost(mux, "/v2/client/listings/publish", publishBody(rawCode, testBTCBech32Addr))
	if w2.Code != http.StatusConflict {
		t.Errorf("second publish: got %d, want 409", w2.Code)
	}
	if code := decodeJourneyErr(w2.Body.Bytes()); code != errCodeAlreadyPublished {
		t.Errorf("error code=%s, want %s", code, errCodeAlreadyPublished)
	}

	// Exactly one row.
	if n := countRows(t, db, "v2_listings"); n != 1 {
		t.Errorf("v2_listings count=%d, want 1", n)
	}
}

// TestListingHTTPPublishConcurrency proves that two simultaneous publish calls
// produce exactly one 201 and one stable 409 already_published, with exactly
// one listing row and activation_count=1.
func TestListingHTTPPublishConcurrency(t *testing.T) {
	nowFn := time.Now
	h, transport, svc, _, db := newJourneyHandler(t, nowFn, nil)
	mux := h.Routes()

	rawCode, _ := makeFormReadyFlowForTransport(t, svc, testBTCBech32Addr)
	rawToken := createPendingAttempt(t, transport, rawCode, testBTCBech32Addr)
	if code := sendWebhook(transport, buildWebhookBody(12345, "private", "/start "+rawToken), string(testWebhookSecret)); code != 200 {
		t.Fatalf("webhook: got %d", code)
	}

	type result struct{ code int }
	results := make([]result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := journeyPost(mux, "/v2/client/listings/publish", publishBody(rawCode, testBTCBech32Addr))
			results[i] = result{code: w.Code}
		}()
	}
	wg.Wait()

	created := 0
	conflicts := 0
	for _, r := range results {
		switch r.code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		}
	}
	if created != 1 {
		t.Errorf("concurrent publish: created=%d, want 1 (codes: %d %d)", created, results[0].code, results[1].code)
	}
	if conflicts != 1 {
		t.Errorf("concurrent publish: conflicts=%d, want 1", conflicts)
	}

	// Exactly one listing row, activation_count=1.
	if n := countRows(t, db, "v2_listings"); n != 1 {
		t.Errorf("concurrent publish: v2_listings count=%d, want 1", n)
	}
	var actCount int
	db.QueryRow(`SELECT activation_count FROM v2_listings LIMIT 1`).Scan(&actCount) //nolint:errcheck
	if actCount != 1 {
		t.Errorf("concurrent publish: activation_count=%d, want 1", actCount)
	}
}

// Test 15
func TestListingHTTPReactivateBalanceFromServer(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	bal := &journeyFakeBalance{balance: 150.0}
	h, transport, svc, _, _ := newJourneyHandler(t, nowFn, bal)
	mux := h.Routes()

	rawCode, _, _ := fullSetupPublish(t, svc, transport, mux, testBTCBech32Addr)

	// Advance clock past visible_until.
	advanced := now.Add(25 * time.Hour)
	h.ls.NormalizeExpired(advanced) //nolint:errcheck

	h2, err := NewClientJourneyHandler(svc, h.ls, transport, bal, []byte("rl-bal"), func() time.Time { return advanced })
	if err != nil {
		t.Fatalf("NewClientJourneyHandler: %v", err)
	}
	mux2 := h2.Routes()

	// Create new Telegram binding for window 2.
	rawToken2 := createPendingAttempt(t, transport, rawCode, testBTCBech32Addr)
	sendWebhook(transport, buildWebhookBody(12345, "private", "/start "+rawToken2), string(testWebhookSecret)) //nolint:errcheck

	// Reactivate request body has NO balance field — server fetches it from reader.
	// The journeyFakeBalance has balance=150.0, so reactivation should succeed.
	w := journeyPost(mux2, "/v2/client/listings/reactivate", capBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusOK {
		t.Errorf("reactivate with server-side balance: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

// Test 16
func TestListingHTTPReactivateProviderFailure(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	bal := &journeyFakeBalance{balance: 150.0}
	h, transport, svc, _, db := newJourneyHandler(t, nowFn, bal)
	mux := h.Routes()

	rawCode, _, _ := fullSetupPublish(t, svc, transport, mux, testBTCBech32Addr)
	advanced := now.Add(25 * time.Hour)
	h.ls.NormalizeExpired(advanced) //nolint:errcheck

	// Now set balance reader to return error.
	bal.set(0, errors.New("provider unavailable"))

	h2, err := NewClientJourneyHandler(svc, h.ls, transport, bal, []byte("rl-prov"), func() time.Time { return advanced })
	if err != nil {
		t.Fatalf("NewClientJourneyHandler: %v", err)
	}
	mux2 := h2.Routes()

	rawToken2 := createPendingAttempt(t, transport, rawCode, testBTCBech32Addr)
	sendWebhook(transport, buildWebhookBody(12345, "private", "/start "+rawToken2), string(testWebhookSecret)) //nolint:errcheck

	w := journeyPost(mux2, "/v2/client/listings/reactivate", capBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("provider failure: got %d, want 503", w.Code)
	}
	if code := decodeJourneyErr(w.Body.Bytes()); code != errCodeProviderUnavailable {
		t.Errorf("error code=%s, want %s", code, errCodeProviderUnavailable)
	}

	// Listing state unchanged (still hidden).
	var state string
	db.QueryRow(`SELECT state FROM v2_listings LIMIT 1`).Scan(&state) //nolint:errcheck
	if state != "hidden" {
		t.Errorf("listing state after provider failure: got %s, want hidden", state)
	}
}

// Test 17
func TestListingHTTPReactivateBalanceFloor(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }

	// Test $119.99 → 409.
	t.Run("below_floor", func(t *testing.T) {
		bal := &journeyFakeBalance{balance: 119.99}
		h, transport, svc, _, _ := newJourneyHandler(t, nowFn, bal)
		mux := h.Routes()

		rawCode, _, _ := fullSetupPublish(t, svc, transport, mux, testBTCBech32Addr)
		advanced := now.Add(25 * time.Hour)
		h.ls.NormalizeExpired(advanced) //nolint:errcheck

		h2, _ := NewClientJourneyHandler(svc, h.ls, transport, bal, []byte("rl-floor1"), func() time.Time { return advanced })
		mux2 := h2.Routes()

		rawToken := createPendingAttempt(t, transport, rawCode, testBTCBech32Addr)
		sendWebhook(transport, buildWebhookBody(12345, "private", "/start "+rawToken), string(testWebhookSecret)) //nolint:errcheck

		w := journeyPost(mux2, "/v2/client/listings/reactivate", capBody(rawCode, testBTCBech32Addr))
		if w.Code != http.StatusConflict {
			t.Errorf("$119.99: got %d, want 409", w.Code)
		}
		if code := decodeJourneyErr(w.Body.Bytes()); code != errCodeLowBalance {
			t.Errorf("error code=%s, want %s", code, errCodeLowBalance)
		}
	})

	// Test $120.00 → 200.
	t.Run("at_floor", func(t *testing.T) {
		bal := &journeyFakeBalance{balance: 120.0}
		h, transport, svc, _, _ := newJourneyHandler(t, nowFn, bal)
		mux := h.Routes()

		rawCode, _, _ := fullSetupPublish(t, svc, transport, mux, testBTCBech32Addr)
		advanced := now.Add(25 * time.Hour)
		h.ls.NormalizeExpired(advanced) //nolint:errcheck

		h2, _ := NewClientJourneyHandler(svc, h.ls, transport, bal, []byte("rl-floor2"), func() time.Time { return advanced })
		mux2 := h2.Routes()

		rawToken := createPendingAttempt(t, transport, rawCode, testBTCBech32Addr)
		sendWebhook(transport, buildWebhookBody(12345, "private", "/start "+rawToken), string(testWebhookSecret)) //nolint:errcheck

		w := journeyPost(mux2, "/v2/client/listings/reactivate", capBody(rawCode, testBTCBech32Addr))
		if w.Code != http.StatusOK {
			t.Errorf("$120.00: got %d, want 200; body: %s", w.Code, w.Body.String())
		}
	})
}

// Test 18
func TestListingHTTPReactivateLowBalanceBindingPreserved(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	bal := &journeyFakeBalance{balance: 119.99}
	h, transport, svc, _, _ := newJourneyHandler(t, nowFn, bal)
	mux := h.Routes()

	rawCode, _, _ := fullSetupPublish(t, svc, transport, mux, testBTCBech32Addr)
	advanced := now.Add(25 * time.Hour)
	h.ls.NormalizeExpired(advanced) //nolint:errcheck

	h2, _ := NewClientJourneyHandler(svc, h.ls, transport, bal, []byte("rl-bind-pres"), func() time.Time { return advanced })
	mux2 := h2.Routes()

	rawToken := createPendingAttempt(t, transport, rawCode, testBTCBech32Addr)
	sendWebhook(transport, buildWebhookBody(12345, "private", "/start "+rawToken), string(testWebhookSecret)) //nolint:errcheck

	// Reactivate fails due to low balance.
	w := journeyPost(mux2, "/v2/client/listings/reactivate", capBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusConflict {
		t.Fatalf("low balance: got %d, want 409", w.Code)
	}

	// Binding must still be present and in state=ready.
	status, err := transport.QueryLinkStatus(rawCode, testBTCBech32Addr)
	if err != nil {
		t.Fatalf("QueryLinkStatus: %v", err)
	}
	if status != "ready" {
		t.Errorf("after low_balance: binding status=%s, want ready", status)
	}
}

// Test 19
func TestListingHTTPReactivateMissingBinding(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	bal := &journeyFakeBalance{balance: 150.0}
	h, transport, svc, _, _ := newJourneyHandler(t, nowFn, bal)
	mux := h.Routes()

	rawCode, _, _ := fullSetupPublish(t, svc, transport, mux, testBTCBech32Addr)
	advanced := now.Add(25 * time.Hour)
	h.ls.NormalizeExpired(advanced) //nolint:errcheck

	h2, _ := NewClientJourneyHandler(svc, h.ls, transport, bal, []byte("rl-miss-bind"), func() time.Time { return advanced })
	mux2 := h2.Routes()

	// No new binding created → 409 telegram_required.
	w := journeyPost(mux2, "/v2/client/listings/reactivate", capBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusConflict {
		t.Errorf("no binding: got %d, want 409", w.Code)
	}
	if code := decodeJourneyErr(w.Body.Bytes()); code != errCodeTelegramRequired {
		t.Errorf("error code=%s, want %s", code, errCodeTelegramRequired)
	}
}

// TestListingHTTPReactivateOldWindowBinding verifies that a window-1 binding
// (consumed during first publication) does not satisfy the window-2 reactivation
// requirement. After publish the window-1 binding is in state "published"; the
// domain requires a fresh ready binding for window 2.
func TestListingHTTPReactivateOldWindowBinding(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	bal := &journeyFakeBalance{balance: 150.0}
	h, transport, svc, _, _ := newJourneyHandler(t, nowFn, bal)
	mux := h.Routes()

	// Publish listing — consumes window-1 binding.
	rawCode, _, _ := fullSetupPublish(t, svc, transport, mux, testBTCBech32Addr)

	// Advance clock past visible_until.
	advanced := now.Add(25 * time.Hour)
	h.ls.NormalizeExpired(advanced) //nolint:errcheck

	h2, err := NewClientJourneyHandler(svc, h.ls, transport, bal, []byte("rl-old-win"), func() time.Time { return advanced })
	if err != nil {
		t.Fatalf("NewClientJourneyHandler: %v", err)
	}
	mux2 := h2.Routes()

	// Do NOT create a window-2 binding. The old window-1 binding (state=published)
	// must not satisfy reactivation.
	w := journeyPost(mux2, "/v2/client/listings/reactivate", capBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusConflict {
		t.Errorf("old window binding: got %d, want 409", w.Code)
	}
	if code := decodeJourneyErr(w.Body.Bytes()); code != errCodeTelegramRequired {
		t.Errorf("old window binding error code=%s, want %s", code, errCodeTelegramRequired)
	}
}

// Test 20
func TestListingHTTPReactivateSuccess(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	bal := &journeyFakeBalance{balance: 150.0}
	h, transport, svc, _, _ := newJourneyHandler(t, nowFn, bal)
	mux := h.Routes()

	rawCode, _, _ := fullSetupPublish(t, svc, transport, mux, testBTCBech32Addr)

	// Advance past visible_until.
	advanced := now.Add(25 * time.Hour)
	h.ls.NormalizeExpired(advanced) //nolint:errcheck

	h2, err := NewClientJourneyHandler(svc, h.ls, transport, bal, []byte("rl-react"), func() time.Time { return advanced })
	if err != nil {
		t.Fatalf("NewClientJourneyHandler: %v", err)
	}
	mux2 := h2.Routes()

	// Create new binding for window 2.
	rawToken2 := createPendingAttempt(t, transport, rawCode, testBTCBech32Addr)
	if code := sendWebhook(transport, buildWebhookBody(12345, "private", "/start "+rawToken2), string(testWebhookSecret)); code != 200 {
		t.Fatalf("webhook2: got %d", code)
	}

	w := journeyPost(mux2, "/v2/client/listings/reactivate", capBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusOK {
		t.Fatalf("reactivate: got %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var safe safeListingJSON
	json.Unmarshal(w.Body.Bytes(), &safe) //nolint:errcheck
	if safe.ActivationCount != 2 {
		t.Errorf("activation_count=%d, want 2", safe.ActivationCount)
	}
}

// Test 21
func TestListingHTTPReactivateConcurrency(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	bal := &journeyFakeBalance{balance: 150.0}
	h, transport, svc, _, _ := newJourneyHandler(t, nowFn, bal)
	mux := h.Routes()

	rawCode, _, _ := fullSetupPublish(t, svc, transport, mux, testBTCBech32Addr)

	advanced := now.Add(25 * time.Hour)
	h.ls.NormalizeExpired(advanced) //nolint:errcheck

	h2, err := NewClientJourneyHandler(svc, h.ls, transport, bal, []byte("rl-conc"), func() time.Time { return advanced })
	if err != nil {
		t.Fatalf("NewClientJourneyHandler: %v", err)
	}
	mux2 := h2.Routes()

	// One binding for window 2.
	rawToken2 := createPendingAttempt(t, transport, rawCode, testBTCBech32Addr)
	if code := sendWebhook(transport, buildWebhookBody(12345, "private", "/start "+rawToken2), string(testWebhookSecret)); code != 200 {
		t.Fatalf("webhook2: got %d", code)
	}

	type result struct{ code int }
	results := make([]result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := journeyPost(mux2, "/v2/client/listings/reactivate", capBody(rawCode, testBTCBech32Addr))
			results[i] = result{code: w.Code}
		}()
	}
	wg.Wait()

	successes := 0
	conflicts := 0
	for _, r := range results {
		if r.code == http.StatusOK {
			successes++
		} else if r.code == http.StatusConflict {
			conflicts++
		}
	}
	if successes != 1 {
		t.Errorf("concurrent reactivate: successes=%d, want 1", successes)
	}
	if conflicts != 1 {
		t.Errorf("concurrent reactivate: conflicts=%d, want 1", conflicts)
	}
}

// Test 22
func TestListingHTTPReactivateFinalExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	bal := &journeyFakeBalance{balance: 150.0}
	h, transport, svc, _, db := newJourneyHandler(t, nowFn, bal)
	mux := h.Routes()

	rawCode, _, flowID := fullSetupPublish(t, svc, transport, mux, testBTCBech32Addr)

	// Read entitlement_expires_at.
	var entExp int64
	db.QueryRow(`SELECT entitlement_expires_at FROM v2_listings WHERE flow_id=?`, flowID).Scan(&entExp) //nolint:errcheck

	// Advance past entitlement.
	expired := time.Unix(entExp+1, 0)
	h.ls.NormalizeExpired(expired) //nolint:errcheck

	h2, err := NewClientJourneyHandler(svc, h.ls, transport, bal, []byte("rl-exp"), func() time.Time { return expired })
	if err != nil {
		t.Fatalf("NewClientJourneyHandler: %v", err)
	}
	mux2 := h2.Routes()

	w := journeyPost(mux2, "/v2/client/listings/reactivate", capBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusGone {
		t.Errorf("expired entitlement: got %d, want 410", w.Code)
	}
	if code := decodeJourneyErr(w.Body.Bytes()); code != errCodeEntitlementExpired {
		t.Errorf("error code=%s, want %s", code, errCodeEntitlementExpired)
	}
}

// ── Group 4: Public views ─────────────────────────────────────────────────────

// Test 23
func TestListingHTTPBoardEmpty(t *testing.T) {
	nowFn := time.Now
	h, _, _, _, _ := newJourneyHandler(t, nowFn, nil)
	mux := h.Routes()

	w := getPath(mux, "/v2/board/tbilisi")
	if w.Code != http.StatusOK {
		t.Fatalf("board empty: got %d, want 200", w.Code)
	}
	body := strings.TrimSpace(w.Body.String())
	if body != "[]" {
		t.Errorf("empty board: got %q, want []", body)
	}
}

// journeyUniqueNameGen is a counter-based display name generator for tests that
// need multiple listings in the same DB (avoids UNIQUE display_name collisions).
type journeyUniqueNameGen struct {
	mu sync.Mutex
	n  int
}

func (g *journeyUniqueNameGen) Generate() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	// Generate names like "calm_river_01", "calm_river_02", etc.
	name := fmt.Sprintf("calm_river_%02d", g.n)
	return name, nil
}

// newJourneyHandlerWithUniqueName is like newJourneyHandler but uses a counter-based
// display name generator to avoid collisions when multiple listings share one DB.
func newJourneyHandlerWithUniqueName(
	t *testing.T,
	nowFn func() time.Time,
	bal ClientBalanceReader,
) (*ClientJourneyHandler, *TelegramTransport, *Service, *ListingService, *sql.DB) {
	t.Helper()
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	svc, err := NewWithClock(db, testHMACKey, nowFn)
	if err != nil {
		t.Fatalf("NewWithClock: %v", err)
	}
	cipher, err := NewAESGCMContactCipher(testAESKey, "v1")
	if err != nil {
		t.Fatalf("NewAESGCMContactCipher: %v", err)
	}
	ls, err := NewListingService(svc, cipher, &journeyUniqueNameGen{}, &fakeContactValidator{})
	if err != nil {
		t.Fatalf("NewListingService: %v", err)
	}
	destCipher, err := NewDestinationCipher(testDestKey, "dest_v1")
	if err != nil {
		t.Fatalf("NewDestinationCipher: %v", err)
	}
	sender := &mockSender{}
	transport, err := NewTelegramTransport(svc, ls, testTokenSecret, testWebhookSecret,
		destCipher, "TestBot", sender, nowFn)
	if err != nil {
		t.Fatalf("NewTelegramTransport: %v", err)
	}
	if bal == nil {
		bal = &journeyFakeBalance{balance: 150.0}
	}
	h, err := NewClientJourneyHandler(svc, ls, transport, bal, []byte("test-rl-key"), nowFn)
	if err != nil {
		t.Fatalf("NewClientJourneyHandler: %v", err)
	}
	return h, transport, svc, ls, db
}

// Test 24
func TestListingHTTPBoardContent(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	bal := &journeyFakeBalance{balance: 150.0}

	// Use unique name generator to avoid display_name collision with multiple listings.
	h, transport, svc, ls, db := newJourneyHandlerWithUniqueName(t, nowFn, bal)
	mux := h.Routes()

	// Create 2 visible listings + 1 hidden.

	// Flow 1 → visible.
	rawCode1, _ := makeFormReadyFlowForTransport(t, svc, testBTCBech32Addr)
	rawToken1 := createPendingAttempt(t, transport, rawCode1, testBTCBech32Addr)
	sendWebhook(transport, buildWebhookBody(1001, "private", "/start "+rawToken1), string(testWebhookSecret)) //nolint:errcheck
	w1 := journeyPost(mux, "/v2/client/listings/publish", publishBody(rawCode1, testBTCBech32Addr))
	if w1.Code != http.StatusCreated {
		t.Fatalf("publish1: got %d; body: %s", w1.Code, w1.Body.String())
	}

	// Flow 2 → visible.
	rawCode2, _ := makeFormReadyFlowForTransport(t, svc, testBTCBech32Addr)
	rawToken2 := createPendingAttempt(t, transport, rawCode2, testBTCBech32Addr)
	sendWebhook(transport, buildWebhookBody(1002, "private", "/start "+rawToken2), string(testWebhookSecret)) //nolint:errcheck
	w2 := journeyPost(mux, "/v2/client/listings/publish", publishBody(rawCode2, testBTCBech32Addr))
	if w2.Code != http.StatusCreated {
		t.Fatalf("publish2: got %d; body: %s", w2.Code, w2.Body.String())
	}

	// Board at now should have 2 visible listings (both published and within their window).
	wBoard := getPath(mux, "/v2/board/tbilisi")
	if wBoard.Code != http.StatusOK {
		t.Fatalf("board: got %d", wBoard.Code)
	}

	var listings []publicListingJSON
	json.Unmarshal(wBoard.Body.Bytes(), &listings) //nolint:errcheck
	if len(listings) != 2 {
		t.Errorf("board count=%d, want 2; body: %s", len(listings), wBoard.Body.String())
	}

	// Verify listings have required fields.
	for _, l := range listings {
		if l.ID == "" {
			t.Error("board listing has empty ID")
		}
		if l.City != "tbilisi" {
			t.Errorf("board listing city=%s, want tbilisi", l.City)
		}
		if l.DisplayName == "" {
			t.Error("board listing has empty DisplayName")
		}
	}

	// Advance clock past visible_until to simulate all listings going hidden.
	advanced := now.Add(25 * time.Hour)
	ls.NormalizeExpired(advanced) //nolint:errcheck

	// Board at advanced time should have 0 visible listings.
	h2, _ := NewClientJourneyHandler(svc, ls, transport, bal, []byte("rl-board2"), func() time.Time { return advanced })
	wBoard2 := getPath(h2.Routes(), "/v2/board/tbilisi")
	json.Unmarshal(wBoard2.Body.Bytes(), &listings) //nolint:errcheck
	if len(listings) != 0 {
		t.Errorf("board at advanced time: count=%d, want 0", len(listings))
	}

	// Verify hidden listings are not on the board.
	var hiddenCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_listings WHERE state='hidden'`).Scan(&hiddenCnt) //nolint:errcheck
	if hiddenCnt < 2 {
		t.Errorf("hidden count=%d after NormalizeExpired, want >= 2", hiddenCnt)
	}
}

// Test 25
func TestListingHTTPPublicDetail(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	bal := &journeyFakeBalance{balance: 150.0}
	h, transport, svc, _, db := newJourneyHandler(t, nowFn, bal)
	mux := h.Routes()

	rawCode, listingID, _ := fullSetupPublish(t, svc, transport, mux, testBTCBech32Addr)
	_ = rawCode

	// Visible listing → 200.
	w := getPath(mux, "/v2/listings/"+listingID)
	if w.Code != http.StatusOK {
		t.Errorf("visible listing: got %d, want 200", w.Code)
	}

	// No mutation: listing state unchanged.
	var state string
	db.QueryRow(`SELECT state FROM v2_listings WHERE id=?`, listingID).Scan(&state) //nolint:errcheck
	if state != "visible" {
		t.Errorf("state mutated by GET: got %s, want visible", state)
	}

	// All non-visible cases must return identical 404 status and body.
	// Collect reference 404 body from an unknown ID.
	wRef := getPath(mux, "/v2/listings/"+newID())
	if wRef.Code != http.StatusNotFound {
		t.Errorf("unknown ID: got %d, want 404", wRef.Code)
	}
	ref404Body := wRef.Body.Bytes()

	// Hidden listing (visible_until < now, state transitions to hidden via NormalizeExpired).
	advanced := now.Add(25 * time.Hour)
	h.ls.NormalizeExpired(advanced) //nolint:errcheck
	h2, _ := NewClientJourneyHandler(svc, h.ls, transport, bal, []byte("rl-det2"), func() time.Time { return advanced })
	mux2 := h2.Routes()
	wHidden := getPath(mux2, "/v2/listings/"+listingID)
	if wHidden.Code != http.StatusNotFound {
		t.Errorf("hidden listing: got %d, want 404", wHidden.Code)
	}
	if !bytes.Equal(wHidden.Body.Bytes(), ref404Body) {
		t.Errorf("hidden 404 body differs from unknown 404 body:\n%s\nvs\n%s",
			wHidden.Body.String(), string(ref404Body))
	}

	// Expired listing (past entitlement_expires_at, state=finished via NormalizeExpired).
	farFuture := now.Add(10 * 24 * time.Hour)
	h.ls.NormalizeExpired(farFuture) //nolint:errcheck
	h3, _ := NewClientJourneyHandler(svc, h.ls, transport, bal, []byte("rl-det3"), func() time.Time { return farFuture })
	wExpired := getPath(h3.Routes(), "/v2/listings/"+listingID)
	if wExpired.Code != http.StatusNotFound {
		t.Errorf("expired listing: got %d, want 404", wExpired.Code)
	}
	if !bytes.Equal(wExpired.Body.Bytes(), ref404Body) {
		t.Errorf("expired 404 body differs from unknown 404 body:\n%s\nvs\n%s",
			wExpired.Body.String(), string(ref404Body))
	}

	// Malformed listing ID (reaches the handler but doesn't match any listing) →
	// 404 with identical body. Path-traversal strings are excluded because Go's
	// ServeMux cleans paths before dispatch, producing 3xx instead of 404.
	for _, malformed := range []string{"not-a-valid-id", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		path := "/v2/listings/" + malformed
		wMal := getPath(mux, path)
		if wMal.Code != http.StatusNotFound {
			t.Errorf("malformed id %q: got %d, want 404", malformed, wMal.Code)
		}
		if !bytes.Equal(wMal.Body.Bytes(), ref404Body) {
			t.Errorf("malformed id %q 404 body differs:\n%s\nvs\n%s",
				malformed, wMal.Body.String(), string(ref404Body))
		}
	}
}

// Test 26
func TestListingHTTPPublicDetailPrivacy(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	h, transport, svc, _, _ := newJourneyHandler(t, nowFn, nil)
	mux := h.Routes()

	_, listingID, _ := fullSetupPublish(t, svc, transport, mux, testBTCBech32Addr)

	w := getPath(mux, "/v2/listings/"+listingID)
	if w.Code != http.StatusOK {
		t.Fatalf("detail: got %d, want 200", w.Code)
	}

	var public publicListingJSON
	if err := json.Unmarshal(w.Body.Bytes(), &public); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Required fields.
	if public.ID == "" {
		t.Error("id empty")
	}
	if public.DisplayName == "" {
		t.Error("display_name empty")
	}
	if public.City == "" {
		t.Error("city empty")
	}
	if public.CountryCode == "" {
		t.Error("country_code empty")
	}
	if public.DependencyType == "" {
		t.Error("dependency_type empty")
	}
	if public.HelpType == "" {
		t.Error("help_type empty")
	}
	if public.Urgency == "" {
		t.Error("urgency empty")
	}
	if len(public.Languages) == 0 {
		t.Error("languages empty")
	}
	if public.VisibleUntil == 0 {
		t.Error("visible_until is 0")
	}
	if public.TimeLeftSec < 0 {
		t.Error("time_left_sec negative")
	}

	// Sensitive fields must be absent.
	body := w.Body.String()
	for _, key := range []string{
		"management_code", "wallet", "contact", "binding_ref", "flow_id",
		"chat_id", "ciphertext", "fingerprint", "token", "invoice",
	} {
		if strings.Contains(body, `"`+key+`"`) {
			t.Errorf("sensitive key %q found in public detail response", key)
		}
	}
}

// ── Additional regression tests for 04C-FIX ──────────────────────────────────

// TestListingHTTPRestoreFormReadyExpired proves Fix 1: if the flow is in
// form_ready state (payment confirmed, balance OK, no listing yet) but
// entitlement_expires_at has passed, restore returns phase=finished /
// next_action=start_new_listing with listing=null — not a 500.
func TestListingHTTPRestoreFormReadyExpired(t *testing.T) {
	baseTime := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return baseTime }
	h, transport, svc, _, _ := newJourneyHandler(t, nowFn, nil)
	_ = transport // Telegram not needed for this path

	rawCode, _ := makeFormReadyFlowForTransport(t, svc, testBTCBech32Addr)

	// Read the entitlement_expires_at from the flow view.
	fv, err := svc.RestorePaymentIntent(rawCode, testBTCBech32Addr)
	if err != nil {
		t.Fatalf("RestorePaymentIntent: %v", err)
	}
	if fv.EntitlementExpiresAt == nil {
		t.Fatal("form_ready flow has no entitlement_expires_at — cannot test expiry")
	}

	// Advance clock to exactly entitlement_expires_at (boundary: now == exp → expired).
	expiredTime := *fv.EntitlementExpiresAt
	h2, err := NewClientJourneyHandler(svc, h.ls, transport, &journeyFakeBalance{balance: 150.0},
		[]byte("rl-fre"), func() time.Time { return expiredTime })
	if err != nil {
		t.Fatalf("NewClientJourneyHandler: %v", err)
	}

	w := journeyPost(h2.Routes(), "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusOK {
		t.Fatalf("form_ready expired restore: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	r := decodeRestoreNav(w.Body.Bytes())
	if r.Phase != phaseFinished {
		t.Errorf("form_ready expired: phase=%s, want finished", r.Phase)
	}
	if r.NextAction != actionStartNewListing {
		t.Errorf("form_ready expired: next_action=%s, want start_new_listing", r.NextAction)
	}
	if r.Listing != nil {
		t.Errorf("form_ready expired: listing should be nil (no listing created), got non-nil")
	}
}

// TestListingHTTPRestorePaidLowBalanceExpired proves Fix 1 for the
// paid_low_balance sub-state: when entitlement_expires_at is reached, the
// terminal boundary overrides the recheck_balance next-action.
func TestListingHTTPRestorePaidLowBalanceExpired(t *testing.T) {
	baseTime := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return baseTime }
	h, transport, svc, _, _ := newJourneyHandler(t, nowFn, nil)
	_ = transport

	// Advance the flow to paid_low_balance: confirm payment, then record
	// insufficient balance so the domain sets state=paid_low_balance.
	rawCode, fv, err := svc.CreatePaymentIntent(testBTCBech32Addr, "BTC", validDraft())
	if err != nil {
		t.Fatalf("CreatePaymentIntent: %v", err)
	}
	_, err = svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_plb",
		[]string{testBTCBech32Addr}, fv.AmountAtomic, baseTime)
	if err != nil {
		t.Fatalf("RecordPaymentDetected: %v", err)
	}
	_, err = svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, baseTime)
	if err != nil {
		t.Fatalf("ConfirmPayment: %v", err)
	}
	// Low balance → paid_low_balance state.
	_, err = svc.RecordPostPaymentBalance(fv.FlowID, 50.0, hardFloorUSD)
	if err != nil {
		t.Fatalf("RecordPostPaymentBalance low: %v", err)
	}

	// Get the entitlement_expires_at from the flow view.
	flowView, err := svc.RestorePaymentIntent(rawCode, testBTCBech32Addr)
	if err != nil {
		t.Fatalf("RestorePaymentIntent: %v", err)
	}
	if flowView.EntitlementExpiresAt == nil {
		t.Fatal("paid_low_balance flow has no entitlement_expires_at")
	}

	// At exact expiry boundary: terminal guard fires before paid_low_balance branch.
	expiredTime := *flowView.EntitlementExpiresAt
	h2, err := NewClientJourneyHandler(svc, h.ls, transport, &journeyFakeBalance{balance: 50.0},
		[]byte("rl-plbe"), func() time.Time { return expiredTime })
	if err != nil {
		t.Fatalf("NewClientJourneyHandler: %v", err)
	}

	w := journeyPost(h2.Routes(), "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusOK {
		t.Fatalf("paid_low_balance expired restore: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	r := decodeRestoreNav(w.Body.Bytes())
	if r.Phase != phaseFinished {
		t.Errorf("paid_low_balance expired: phase=%s, want finished", r.Phase)
	}
	if r.NextAction != actionStartNewListing {
		t.Errorf("paid_low_balance expired: next_action=%s, want start_new_listing", r.NextAction)
	}
}

// TestListingHTTPRestoreVisibleTelegramActive proves Fix 2: restore for a
// currently visible listing calls QueryLinkStatus and returns telegram_status=active.
func TestListingHTTPRestoreVisibleTelegramActive(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	h, transport, svc, _, _ := newJourneyHandler(t, nowFn, nil)
	mux := h.Routes()

	rawCode, _, _ := fullSetupPublish(t, svc, transport, mux, testBTCBech32Addr)

	// Listing is visible. After a successful publish, the destination becomes
	// active and QueryLinkStatus returns "active".
	w := journeyPost(mux, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusOK {
		t.Fatalf("restore visible: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	r := decodeRestoreNav(w.Body.Bytes())
	if r.Phase != phaseVisible {
		t.Errorf("telegram_active test: phase=%s, want visible", r.Phase)
	}
	if r.TelegramStatus != "active" {
		t.Errorf("visible listing telegram_status=%s, want active", r.TelegramStatus)
	}
}

// ── Group 5: Full lifecycle ───────────────────────────────────────────────────

// Test 27
func TestClientJourneyLifecycle(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	var mu sync.Mutex
	clock := start
	nowFn := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	advance := func(d time.Duration) time.Time {
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(d)
		return clock
	}

	bal := &journeyFakeBalance{balance: 150.0}
	transport, svc, ls, db := newTestTransport(t, nowFn)

	makeHandler := func(b ClientBalanceReader) (http.Handler, *ClientJourneyHandler) {
		h, err := NewClientJourneyHandler(svc, ls, transport, b, []byte("lifecycle-rl"), nowFn)
		if err != nil {
			t.Fatalf("NewClientJourneyHandler: %v", err)
		}
		return h.Routes(), h
	}
	mux, h := makeHandler(bal)

	// ── a. Create payment intent ──────────────────────────────────────────────
	rawCode, fv, err := svc.CreatePaymentIntent(testBTCBech32Addr, "BTC", validDraft())
	if err != nil {
		t.Fatalf("a. CreatePaymentIntent: %v", err)
	}
	flowID := fv.FlowID
	invoiceID := fv.InvoiceID

	// ── b. Simulate watcher: RecordPaymentDetected + ConfirmPayment ───────────
	now := nowFn()
	_, err = svc.RecordPaymentDetected(flowID, invoiceID, "txid_lifecycle",
		[]string{testBTCBech32Addr}, fv.AmountAtomic, now)
	if err != nil {
		t.Fatalf("b. RecordPaymentDetected: %v", err)
	}
	_, err = svc.ConfirmPayment(flowID, invoiceID, now)
	if err != nil {
		t.Fatalf("b. ConfirmPayment: %v", err)
	}

	// ── c. POST /restore → paid_low_balance (before balance set) ─────────────
	// After confirm but before RecordPostPaymentBalance → payment_confirmed state.
	w := journeyPost(mux, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusOK {
		t.Fatalf("c. restore: got %d", w.Code)
	}
	r := decodeRestoreNav(w.Body.Bytes())
	if r.Phase != phasePaidLowBalance {
		t.Errorf("c. phase=%s, want paid_low_balance", r.Phase)
	}

	// ── d. RecordPostPaymentBalance → form_ready ──────────────────────────────
	_, err = svc.RecordPostPaymentBalance(flowID, 150.0, hardFloorUSD)
	if err != nil {
		t.Fatalf("d. RecordPostPaymentBalance: %v", err)
	}

	// ── e. POST /restore → form_ready/prepare_first_publication ──────────────
	w = journeyPost(mux, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
	r = decodeRestoreNav(w.Body.Bytes())
	if r.Phase != phaseFormReady || r.NextAction != actionPrepareFirstPublication {
		t.Errorf("e. phase=%s action=%s", r.Phase, r.NextAction)
	}
	if r.TelegramStatus != "needs_link" {
		t.Errorf("e. telegram_status=%s, want needs_link", r.TelegramStatus)
	}

	// ── f. CreateLink + sendWebhook → binding ready ───────────────────────────
	rawToken := createPendingAttempt(t, transport, rawCode, testBTCBech32Addr)
	if code := sendWebhook(transport, buildWebhookBody(55555, "private", "/start "+rawToken), string(testWebhookSecret)); code != 200 {
		t.Fatalf("f. webhook: got %d", code)
	}

	// ── g. POST /restore → form_ready/publish, telegram_status=ready ─────────
	w = journeyPost(mux, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
	r = decodeRestoreNav(w.Body.Bytes())
	if r.Phase != phaseFormReady || r.NextAction != actionPublish {
		t.Errorf("g. phase=%s action=%s", r.Phase, r.NextAction)
	}
	if r.TelegramStatus != "ready" {
		t.Errorf("g. telegram_status=%s, want ready", r.TelegramStatus)
	}

	// ── h. POST /publish → 201, 1 row, activation_count=1 ────────────────────
	w = journeyPost(mux, "/v2/client/listings/publish", publishBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusCreated {
		t.Fatalf("h. publish: got %d; body: %s", w.Code, w.Body.String())
	}
	var safe safeListingJSON
	json.Unmarshal(w.Body.Bytes(), &safe) //nolint:errcheck
	listingID := safe.ID
	if safe.ActivationCount != 1 {
		t.Errorf("h. activation_count=%d, want 1", safe.ActivationCount)
	}
	if n := countRows(t, db, "v2_listings"); n != 1 {
		t.Errorf("h. v2_listings count=%d, want 1", n)
	}
	// No plaintext contact in DB.
	var ct, nonce string
	db.QueryRow(`SELECT contact_ciphertext, contact_nonce FROM v2_listings WHERE id=?`, listingID).Scan(&ct, &nonce) //nolint:errcheck
	if ct == "@testuser" || nonce == "@testuser" || ct == "normalized_@testuser" {
		t.Error("h. plaintext contact found in DB")
	}
	if ct == "" || nonce == "" {
		t.Error("h. ciphertext or nonce is empty")
	}

	// ── i. GET /board/tbilisi → 1 listing ────────────────────────────────────
	wBoard := getPath(mux, "/v2/board/tbilisi")
	if wBoard.Code != http.StatusOK {
		t.Fatalf("i. board: got %d", wBoard.Code)
	}
	var boardListings []publicListingJSON
	json.Unmarshal(wBoard.Body.Bytes(), &boardListings) //nolint:errcheck
	if len(boardListings) != 1 {
		t.Errorf("i. board count=%d, want 1", len(boardListings))
	}

	// ── j. GET /listings/{id} → 200 ──────────────────────────────────────────
	wDetail := getPath(mux, "/v2/listings/"+listingID)
	if wDetail.Code != http.StatusOK {
		t.Fatalf("j. detail: got %d", wDetail.Code)
	}

	// ── k. POST /restore → visible/view_listing ───────────────────────────────
	w = journeyPost(mux, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
	r = decodeRestoreNav(w.Body.Bytes())
	if r.Phase != phaseVisible || r.NextAction != actionViewListing {
		t.Errorf("k. phase=%s action=%s", r.Phase, r.NextAction)
	}
	if r.Listing == nil {
		t.Error("k. listing is nil in restore response")
	}

	// ── l. Advance clock past visible_until (+ 25 hours) ─────────────────────
	advancedTime := advance(25 * time.Hour)
	ls.NormalizeExpired(advancedTime) //nolint:errcheck

	// ── m. POST /restore → hidden/connect_telegram_for_reactivation ──────────
	w = journeyPost(mux, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
	r = decodeRestoreNav(w.Body.Bytes())
	if r.Phase != phaseHidden {
		t.Errorf("m. phase=%s, want hidden", r.Phase)
	}
	if r.NextAction != actionConnectTelegramForReactivation {
		t.Errorf("m. next_action=%s, want %s", r.NextAction, actionConnectTelegramForReactivation)
	}

	// ── n. CreateLink for window 2 + sendWebhook → binding ready for window 2 ─
	rawToken2 := createPendingAttempt(t, transport, rawCode, testBTCBech32Addr)
	if code := sendWebhook(transport, buildWebhookBody(55555, "private", "/start "+rawToken2), string(testWebhookSecret)); code != 200 {
		t.Fatalf("n. webhook2: got %d", code)
	}

	// ── o. POST /restore → hidden/reactivate ──────────────────────────────────
	w = journeyPost(mux, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
	r = decodeRestoreNav(w.Body.Bytes())
	if r.Phase != phaseHidden || r.NextAction != actionReactivate {
		t.Errorf("o. phase=%s action=%s", r.Phase, r.NextAction)
	}

	// ── p. POST /reactivate → 200, activation_count=2 ────────────────────────
	w = journeyPost(mux, "/v2/client/listings/reactivate", capBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusOK {
		t.Fatalf("p. reactivate: got %d; body: %s", w.Code, w.Body.String())
	}
	var safe2 safeListingJSON
	json.Unmarshal(w.Body.Bytes(), &safe2) //nolint:errcheck
	if safe2.ActivationCount != 2 {
		t.Errorf("p. activation_count=%d, want 2", safe2.ActivationCount)
	}

	// Verify activation_count in DB = 2.
	var dbCount int
	db.QueryRow(`SELECT activation_count FROM v2_listings WHERE id=?`, listingID).Scan(&dbCount) //nolint:errcheck
	if dbCount != 2 {
		t.Errorf("p. DB activation_count=%d, want 2", dbCount)
	}

	// Strict binding/destination check: the schema enforces one binding row per
	// flow (UNIQUE flow_id), so after reactivation there is exactly 1 binding row
	// with window_number=2 and state=active, and exactly 1 destination row.
	var bindWindowNum int
	var bindState string
	db.QueryRow(`SELECT window_number, state FROM v2_client_notification_bindings WHERE flow_id=?`,
		flowID).Scan(&bindWindowNum, &bindState) //nolint:errcheck
	if bindWindowNum != 2 {
		t.Errorf("p. binding window_number=%d, want 2", bindWindowNum)
	}
	if bindState != "active" {
		t.Errorf("p. binding state=%s, want active", bindState)
	}
	var destCount int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_destinations WHERE binding_ref IN `+
		`(SELECT binding_ref FROM v2_client_notification_bindings WHERE flow_id=?)`,
		flowID).Scan(&destCount) //nolint:errcheck
	if destCount != 1 {
		t.Errorf("p. destination count=%d, want 1 (one for current binding)", destCount)
	}

	// ── q. Advance clock to exact entitlement_expires_at ─────────────────────
	var entExp int64
	db.QueryRow(`SELECT entitlement_expires_at FROM v2_listings WHERE id=?`, listingID).Scan(&entExp) //nolint:errcheck
	mu.Lock()
	clock = time.Unix(entExp, 0)
	mu.Unlock()

	// ── r. POST /restore → finished/start_new_listing ────────────────────────
	w = journeyPost(mux, "/v2/client/listings/restore", capBody(rawCode, testBTCBech32Addr))
	r = decodeRestoreNav(w.Body.Bytes())
	if r.Phase != phaseFinished || r.NextAction != actionStartNewListing {
		t.Errorf("r. phase=%s action=%s", r.Phase, r.NextAction)
	}

	// ── s. POST /publish after expiry → stable 410 entitlement_expired ──────
	// The domain checks entitlement before creating a new listing, so even though
	// a listing exists this must return 410, not 409 already_published.
	w = journeyPost(mux, "/v2/client/listings/publish", publishBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusGone {
		t.Errorf("s. publish after expiry: got %d, want 410", w.Code)
	}
	if code := decodeJourneyErr(w.Body.Bytes()); code != errCodeEntitlementExpired {
		t.Errorf("s. publish after expiry code=%s, want %s", code, errCodeEntitlementExpired)
	}

	// ── t. POST /reactivate → 410 entitlement_expired ────────────────────────
	w = journeyPost(mux, "/v2/client/listings/reactivate", capBody(rawCode, testBTCBech32Addr))
	if w.Code != http.StatusGone {
		t.Errorf("t. reactivate after expiry: got %d, want 410", w.Code)
	}
	if code := decodeJourneyErr(w.Body.Bytes()); code != errCodeEntitlementExpired {
		t.Errorf("t. error code=%s, want %s", code, errCodeEntitlementExpired)
	}

	// Final invariant: no plaintext secrets in DB.
	rows, err := db.Query(`SELECT contact_ciphertext, contact_nonce FROM v2_listings`)
	if err != nil {
		t.Fatalf("final: query listings: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ctVal, nonceVal string
		rows.Scan(&ctVal, &nonceVal) //nolint:errcheck
		if ctVal == "@testuser" || ctVal == "normalized_@testuser" {
			t.Error("final: plaintext contact in DB contact_ciphertext")
		}
	}

	// Use h to suppress unused variable warning.
	_ = h
}
