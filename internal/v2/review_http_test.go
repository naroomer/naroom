// Package v2 — HTTP handler tests for the V2 Helper review endpoints.
package v2

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── Handler scaffolding ───────────────────────────────────────────────────────

func newTestReviewHandler(t *testing.T) (*HelperReviewHandler, *ReviewService, *sql.DB) {
	t.Helper()
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	rs, err := NewReviewService(db, testHMACKey, time.Now)
	if err != nil {
		t.Fatalf("NewReviewService: %v", err)
	}
	h, err := NewHelperReviewHandler(rs, testHMACKey, time.Now)
	if err != nil {
		t.Fatalf("NewHelperReviewHandler: %v", err)
	}
	return h, rs, db
}

func reviewPost(t *testing.T, handler http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:5555"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// ── POST /v2/helper/reviews/capability ───────────────────────────────────────

func TestReviewHTTP_CapabilitySuccess(t *testing.T) {
	h, _, db := newTestReviewHandler(t)
	purchaseID, _, _, rawToken := mustCreateContactReadyPurchase(t, db)

	rr := reviewPost(t, h.Routes(), "/v2/helper/reviews/capability", map[string]string{
		"purchase_id":    purchaseID,
		"purchase_token": rawToken,
		"wallet_address": testBTCBech32Addr,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("capability: status %d, body: %s", rr.Code, rr.Body)
	}

	var resp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck

	if _, ok := resp["review_token"].(string); !ok || resp["review_token"] == "" {
		t.Error("response missing review_token")
	}
	if _, ok := resp["expires_at"].(float64); !ok {
		t.Error("response missing expires_at")
	}
	if _, ok := resp["client_reputation"].(map[string]interface{}); !ok {
		t.Error("response missing client_reputation")
	}
	if _, ok := resp["client_display_name"].(string); !ok {
		t.Error("response missing client_display_name")
	}
}

func TestReviewHTTP_CapabilityNoStoreHeaders(t *testing.T) {
	h, _, db := newTestReviewHandler(t)
	purchaseID, _, _, rawToken := mustCreateContactReadyPurchase(t, db)

	rr := reviewPost(t, h.Routes(), "/v2/helper/reviews/capability", map[string]string{
		"purchase_id":    purchaseID,
		"purchase_token": rawToken,
		"wallet_address": testBTCBech32Addr,
	})

	cc := rr.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control must contain no-store, got %q", cc)
	}
}

func TestReviewHTTP_CapabilityWrongToken(t *testing.T) {
	h, _, db := newTestReviewHandler(t)
	purchaseID, _, _, _ := mustCreateContactReadyPurchase(t, db)

	rr := reviewPost(t, h.Routes(), "/v2/helper/reviews/capability", map[string]string{
		"purchase_id":    purchaseID,
		"purchase_token": newID(), // wrong token
		"wallet_address": testBTCBech32Addr,
	})
	if rr.Code != http.StatusNotFound {
		t.Errorf("wrong token: expected 404, got %d", rr.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck
	if resp["code"] != codeReviewNotFound {
		t.Errorf("wrong token: expected code %q, got %v", codeReviewNotFound, resp["code"])
	}
}

func TestReviewHTTP_CapabilityWrongWallet(t *testing.T) {
	h, _, db := newTestReviewHandler(t)
	purchaseID, _, _, rawToken := mustCreateContactReadyPurchase(t, db)

	rr := reviewPost(t, h.Routes(), "/v2/helper/reviews/capability", map[string]string{
		"purchase_id":    purchaseID,
		"purchase_token": rawToken,
		"wallet_address": "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdp", // different
	})
	if rr.Code != http.StatusNotFound {
		t.Errorf("wrong wallet: expected 404, got %d", rr.Code)
	}
}

func TestReviewHTTP_CapabilityByteIdenticalErrors(t *testing.T) {
	h, _, db := newTestReviewHandler(t)
	purchaseID, _, _, rawToken := mustCreateContactReadyPurchase(t, db)

	rrWrongToken := reviewPost(t, h.Routes(), "/v2/helper/reviews/capability", map[string]string{
		"purchase_id":    purchaseID,
		"purchase_token": newID(),
		"wallet_address": testBTCBech32Addr,
	})
	rrWrongWallet := reviewPost(t, h.Routes(), "/v2/helper/reviews/capability", map[string]string{
		"purchase_id":    purchaseID,
		"purchase_token": rawToken,
		"wallet_address": "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdp",
	})

	if rrWrongToken.Code != rrWrongWallet.Code {
		t.Errorf("wrong token and wrong wallet should return same HTTP status: %d vs %d",
			rrWrongToken.Code, rrWrongWallet.Code)
	}

	var respT, respW map[string]interface{}
	json.NewDecoder(rrWrongToken.Body).Decode(&respT)  //nolint:errcheck
	json.NewDecoder(rrWrongWallet.Body).Decode(&respW) //nolint:errcheck
	if respT["code"] != respW["code"] {
		t.Errorf("wrong token vs wrong wallet: different error codes: %v vs %v", respT["code"], respW["code"])
	}
}

func TestReviewHTTP_CapabilityMissingFields(t *testing.T) {
	h, _, _ := newTestReviewHandler(t)

	cases := []struct {
		name string
		body map[string]string
	}{
		{"no purchase_id", map[string]string{"purchase_token": "tok", "wallet_address": "addr"}},
		{"no purchase_token", map[string]string{"purchase_id": "id", "wallet_address": "addr"}},
		{"no wallet_address", map[string]string{"purchase_id": "id", "purchase_token": "tok"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := reviewPost(t, h.Routes(), "/v2/helper/reviews/capability", tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s: expected 400, got %d", tc.name, rr.Code)
			}
		})
	}
}

func TestReviewHTTP_CapabilityUnknownField(t *testing.T) {
	h, _, _ := newTestReviewHandler(t)
	b := []byte(`{"purchase_id":"x","purchase_token":"y","wallet_address":"z","extra_field":"bad"}`)
	req := httptest.NewRequest(http.MethodPost, "/v2/helper/reviews/capability", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:5555"
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("unknown field: expected 400, got %d", rr.Code)
	}
}

// ── POST /v2/helper/reviews ───────────────────────────────────────────────────

func TestReviewHTTP_SubmitSuccess(t *testing.T) {
	h, _, db := newTestReviewHandler(t)
	// Helper reviews the client → increments client profile, not helper profile.
	purchaseID, _, clientProfileID, _ := mustCreateContactReadyPurchase(t, db)
	tok := helperTokenFromPurchase(t, db, purchaseID)

	rr := reviewPost(t, h.Routes(), "/v2/helper/reviews", map[string]string{
		"review_token": tok,
		"rating":       "positive",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("submit: status %d, body: %s", rr.Code, rr.Body)
	}

	var resp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck
	if resp["accepted"] != true {
		t.Errorf("submit: expected accepted=true, got %v", resp["accepted"])
	}

	// Helper reviewing client → client's positive_count increments.
	var pos int
	db.QueryRow(`SELECT positive_count FROM v2_client_profiles WHERE id = ?`, clientProfileID).Scan(&pos) //nolint:errcheck
	if pos != 1 {
		t.Errorf("submit positive: client positive_count=%d, want 1", pos)
	}
}

func TestReviewHTTP_SubmitNoStoreHeaders(t *testing.T) {
	h, _, db := newTestReviewHandler(t)
	purchaseID, _, _, _ := mustCreateContactReadyPurchase(t, db)
	tok := helperTokenFromPurchase(t, db, purchaseID)

	rr := reviewPost(t, h.Routes(), "/v2/helper/reviews", map[string]string{
		"review_token": tok,
		"rating":       "positive",
	})
	cc := rr.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control must contain no-store, got %q", cc)
	}
}

func TestReviewHTTP_SubmitWrongToken(t *testing.T) {
	h, _, _ := newTestReviewHandler(t)

	rr := reviewPost(t, h.Routes(), "/v2/helper/reviews", map[string]string{
		"review_token": "invalid.token",
		"rating":       "positive",
	})
	if rr.Code != http.StatusNotFound {
		t.Errorf("wrong token: expected 404, got %d", rr.Code)
	}
}

func TestReviewHTTP_SubmitInvalidRating(t *testing.T) {
	h, _, _ := newTestReviewHandler(t)

	rr := reviewPost(t, h.Routes(), "/v2/helper/reviews", map[string]string{
		"review_token": "sometoken",
		"rating":       "neutral",
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid rating: expected 400, got %d", rr.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck
	if resp["code"] != codeReviewInvalidRating {
		t.Errorf("invalid rating: expected code %q, got %v", codeReviewInvalidRating, resp["code"])
	}
}

func TestReviewHTTP_SubmitIdempotent(t *testing.T) {
	h, _, db := newTestReviewHandler(t)
	purchaseID, _, _, _ := mustCreateContactReadyPurchase(t, db)
	tok := helperTokenFromPurchase(t, db, purchaseID)

	// First submit.
	rr1 := reviewPost(t, h.Routes(), "/v2/helper/reviews", map[string]string{
		"review_token": tok,
		"rating":       "positive",
	})
	if rr1.Code != http.StatusOK {
		t.Fatalf("first submit: status %d", rr1.Code)
	}

	// Exact repeat: idempotent 200.
	rr2 := reviewPost(t, h.Routes(), "/v2/helper/reviews", map[string]string{
		"review_token": tok,
		"rating":       "positive",
	})
	if rr2.Code != http.StatusOK {
		t.Errorf("idempotent repeat: expected 200, got %d", rr2.Code)
	}
}

func TestReviewHTTP_SubmitAlreadyConsumed(t *testing.T) {
	h, _, db := newTestReviewHandler(t)
	purchaseID, _, _, _ := mustCreateContactReadyPurchase(t, db)
	tok := helperTokenFromPurchase(t, db, purchaseID)

	// First submit: positive.
	reviewPost(t, h.Routes(), "/v2/helper/reviews", map[string]string{
		"review_token": tok,
		"rating":       "positive",
	})

	// Different rating → 409.
	rr := reviewPost(t, h.Routes(), "/v2/helper/reviews", map[string]string{
		"review_token": tok,
		"rating":       "negative",
	})
	if rr.Code != http.StatusConflict {
		t.Errorf("already consumed: expected 409, got %d", rr.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck
	if resp["code"] != codeReviewAlreadyConsumed {
		t.Errorf("already consumed: expected code %q, got %v", codeReviewAlreadyConsumed, resp["code"])
	}
}

func TestReviewHTTP_SubmitMissingFields(t *testing.T) {
	h, _, _ := newTestReviewHandler(t)

	cases := []struct {
		name string
		body map[string]string
	}{
		{"no review_token", map[string]string{"rating": "positive"}},
		{"no rating", map[string]string{"review_token": "tok"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := reviewPost(t, h.Routes(), "/v2/helper/reviews", tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s: expected 400, got %d", tc.name, rr.Code)
			}
		})
	}
}

func TestReviewHTTP_SubmitUnknownField(t *testing.T) {
	h, _, _ := newTestReviewHandler(t)
	b := []byte(`{"review_token":"x","rating":"positive","extra":"bad"}`)
	req := httptest.NewRequest(http.MethodPost, "/v2/helper/reviews", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:5555"
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("unknown field: expected 400, got %d", rr.Code)
	}
}

func TestReviewHTTP_SubmitRateLimited(t *testing.T) {
	h, _, _ := newTestReviewHandler(t)
	// 11 requests from same IP should trigger rate limit (limit is 10/min).
	var lastCode int
	for i := 0; i < 12; i++ {
		rr := reviewPost(t, h.Routes(), "/v2/helper/reviews", map[string]string{
			"review_token": "tok",
			"rating":       "positive",
		})
		lastCode = rr.Code
	}
	if lastCode != http.StatusTooManyRequests {
		t.Errorf("rate limit: expected 429, got %d", lastCode)
	}
}
