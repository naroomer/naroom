package v2_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	v2 "naroom/internal/v2"
)

// ── Stub balance readers ──────────────────────────────────────────────────────

type stubBalanceReader struct {
	usd float64
	err error
}

func (s *stubBalanceReader) BalanceUSD(_ context.Context, _, _ string) (float64, error) {
	return s.usd, s.err
}

// ── Handler factory ───────────────────────────────────────────────────────────

func newInformerHandler(t *testing.T, bal *stubBalanceReader) (*v2.InformerHandler, *v2.InformerService) {
	t.Helper()
	db, err := v2.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	destCipher, _ := v2.NewDestinationCipher(make([]byte, 32), "test_v1")
	svc, _ := v2.NewInformerService(
		db,
		[]byte("test-hmac-key-32-bytes-12345678!"),
		[]byte("test-token-secret-32-bytes-12345"),
		destCipher,
		nil,
	)
	h, err := v2.NewInformerHandler(
		svc, bal, "naroom_informer_testbot",
		[]byte("test-rate-limit-key-32-bytes-123"),
		time.Now,
	)
	if err != nil {
		t.Fatalf("NewInformerHandler: %v", err)
	}
	return h, svc
}

func informerPost(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func decodeInformerResp(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, rr.Body.String())
	}
	return out
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestInformerAccess_Success(t *testing.T) {
	h, _ := newInformerHandler(t, &stubBalanceReader{usd: 1500.0})
	rr := informerPost(t, h.Routes(), "/v2/informer/access", map[string]any{
		"wallet_address": "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
		"city":           "tbilisi",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	resp := decodeInformerResp(t, rr)
	botURL, _ := resp["bot_url"].(string)
	if botURL == "" {
		t.Error("expected non-empty bot_url")
	}
	rawToken, _ := resp["raw_token"].(string)
	if rawToken == "" {
		t.Error("expected non-empty raw_token")
	}
}

func TestInformerAccess_LowBalance(t *testing.T) {
	h, _ := newInformerHandler(t, &stubBalanceReader{usd: 500.0})
	rr := informerPost(t, h.Routes(), "/v2/informer/access", map[string]any{
		"wallet_address": "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
		"city":           "tbilisi",
	})
	if rr.Code != http.StatusPaymentRequired {
		t.Errorf("expected 402, got %d", rr.Code)
	}
	resp := decodeInformerResp(t, rr)
	if code, _ := resp["code"].(string); code != "low_balance" {
		t.Errorf("expected code=low_balance, got %q", code)
	}
}

func TestInformerAccess_ProviderError(t *testing.T) {
	h, _ := newInformerHandler(t, &stubBalanceReader{err: errors.New("network timeout")})
	rr := informerPost(t, h.Routes(), "/v2/informer/access", map[string]any{
		"wallet_address": "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
		"city":           "tbilisi",
	})
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
	resp := decodeInformerResp(t, rr)
	if code, _ := resp["code"].(string); code != "provider_error" {
		t.Errorf("expected code=provider_error, got %q", code)
	}
}

func TestInformerAccess_InvalidAddress(t *testing.T) {
	h, _ := newInformerHandler(t, &stubBalanceReader{usd: 5000.0})
	rr := informerPost(t, h.Routes(), "/v2/informer/access", map[string]any{
		"wallet_address": "not-a-real-wallet-address",
		"city":           "tbilisi",
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
	resp := decodeInformerResp(t, rr)
	if code, _ := resp["code"].(string); code != "invalid_address" {
		t.Errorf("expected code=invalid_address, got %q", code)
	}
}

func TestInformerAccess_InvalidCity(t *testing.T) {
	h, _ := newInformerHandler(t, &stubBalanceReader{usd: 5000.0})
	rr := informerPost(t, h.Routes(), "/v2/informer/access", map[string]any{
		"wallet_address": "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
		"city":           "atlantis",
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
	resp := decodeInformerResp(t, rr)
	if code, _ := resp["code"].(string); code != "invalid_city" {
		t.Errorf("expected code=invalid_city, got %q", code)
	}
}

func TestInformerAccess_WrongContentType(t *testing.T) {
	h, _ := newInformerHandler(t, &stubBalanceReader{usd: 5000.0})
	req := httptest.NewRequest(http.MethodPost, "/v2/informer/access",
		bytes.NewBufferString(`{"wallet_address":"bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq","city":"tbilisi"}`))
	req.Header.Set("Content-Type", "text/plain")
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for wrong content-type, got %d", rr.Code)
	}
}

func TestInformerAccess_MissingWallet(t *testing.T) {
	h, _ := newInformerHandler(t, &stubBalanceReader{usd: 5000.0})
	rr := informerPost(t, h.Routes(), "/v2/informer/access", map[string]any{"city": "tbilisi"})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestInformerAccess_MissingCity(t *testing.T) {
	h, _ := newInformerHandler(t, &stubBalanceReader{usd: 5000.0})
	rr := informerPost(t, h.Routes(), "/v2/informer/access", map[string]any{
		"wallet_address": "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestInformerStatus_Pending(t *testing.T) {
	h, svc := newInformerHandler(t, &stubBalanceReader{usd: 2000.0})
	// Create token via handler.
	accessRR := informerPost(t, h.Routes(), "/v2/informer/access", map[string]any{
		"wallet_address": "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
		"city":           "tbilisi",
	})
	if accessRR.Code != 200 {
		t.Fatalf("access failed: %d %s", accessRR.Code, accessRR.Body.String())
	}
	resp := decodeInformerResp(t, accessRR)
	rawToken, _ := resp["raw_token"].(string)

	// Status → pending.
	statusRR := informerPost(t, h.Routes(), "/v2/informer/status", map[string]any{"raw_token": rawToken})
	if statusRR.Code != 200 {
		t.Fatalf("status failed: %d %s", statusRR.Code, statusRR.Body.String())
	}
	statusResp := decodeInformerResp(t, statusRR)
	if state, _ := statusResp["state"].(string); state != "pending" {
		t.Errorf("expected pending, got %q", state)
	}
	_ = svc
}

func TestInformerStatus_Claimed(t *testing.T) {
	h, svc := newInformerHandler(t, &stubBalanceReader{usd: 2000.0})
	accessRR := informerPost(t, h.Routes(), "/v2/informer/access", map[string]any{
		"wallet_address": "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
		"city":           "tbilisi",
	})
	resp := decodeInformerResp(t, accessRR)
	rawToken, _ := resp["raw_token"].(string)

	// Subscribe (claim) directly.
	if err := svc.Subscribe(9999, rawToken); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	statusRR := informerPost(t, h.Routes(), "/v2/informer/status", map[string]any{"raw_token": rawToken})
	statusResp := decodeInformerResp(t, statusRR)
	if state, _ := statusResp["state"].(string); state != "claimed" {
		t.Errorf("expected claimed, got %q", state)
	}
}

func TestInformerStatus_TokenNotFound(t *testing.T) {
	h, _ := newInformerHandler(t, &stubBalanceReader{usd: 2000.0})
	rr := informerPost(t, h.Routes(), "/v2/informer/status", map[string]any{
		"raw_token": "unknown-token-that-does-not-exist-1234",
	})
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
	resp := decodeInformerResp(t, rr)
	if code, _ := resp["code"].(string); code != "token_not_found" {
		t.Errorf("expected code=token_not_found, got %q", code)
	}
}

func TestInformerStatus_TokenExpired(t *testing.T) {
	base := time.Now()
	var nowT time.Time
	nowT = base

	db, _ := v2.OpenMemory()
	defer db.Close()
	destCipher, _ := v2.NewDestinationCipher(make([]byte, 32), "test_v1")
	svc, _ := v2.NewInformerService(
		db,
		[]byte("test-hmac-key-32-bytes-12345678!"),
		[]byte("test-token-secret-32-bytes-12345"),
		destCipher,
		func() time.Time { return nowT },
	)
	h, _ := v2.NewInformerHandler(svc, &stubBalanceReader{usd: 2000.0}, "testbot",
		[]byte("test-rate-limit-key-32-bytes-123"), func() time.Time { return nowT })

	rawToken, _, _ := svc.CreateAccess("tbilisi", 2000.0)
	nowT = base.Add(15*time.Minute + time.Second) // expired

	rr := informerPost(t, h.Routes(), "/v2/informer/status", map[string]any{"raw_token": rawToken})
	if rr.Code != http.StatusGone {
		t.Errorf("expected 410, got %d", rr.Code)
	}
	resp := decodeInformerResp(t, rr)
	if code, _ := resp["code"].(string); code != "token_expired" {
		t.Errorf("expected code=token_expired, got %q", code)
	}
}

func TestInformerWebhookHandler_Routes(t *testing.T) {
	svc := newInformerTestSvc(t, nil)
	transport, _ := v2.NewInformerTransport(svc, []byte(informerTestSecret), "testbot", nil)

	rawToken, _, _ := svc.CreateAccess("tbilisi", 2000.0)
	body := `{"update_id":1,"message":{"message_id":1,"chat":{"id":1234,"type":"private"},"text":"/start ` + rawToken + `"}}`
	req := httptest.NewRequest(http.MethodPost, "/v2/telegram/informer/webhook", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", informerTestSecret)
	rr := httptest.NewRecorder()
	transport.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	ok, city, _ := svc.HasActiveSubscription(1234)
	if !ok || city != "tbilisi" {
		t.Errorf("expected tbilisi subscription via routes, got ok=%v city=%s", ok, city)
	}
}
