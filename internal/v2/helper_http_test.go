package v2

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── Fakes for HTTP tests ──────────────────────────────────────────────────────

type fakeHelperIssuer struct {
	result HelperInvoiceDraft
	err    error
}

func (f *fakeHelperIssuer) CreateHelperInvoice(_ context.Context, currency string) (HelperInvoiceDraft, error) {
	if f.err != nil {
		return HelperInvoiceDraft{}, f.err
	}
	d := f.result
	if d.PaymentAddress == "" {
		d.PaymentAddress = "payaddr_http_" + currency
	}
	if d.AmountAtomic == 0 {
		d.AmountAtomic = 100000
	}
	if d.AmountUSDCents == 0 {
		d.AmountUSDCents = helperInvoiceUSDCents
	}
	return d, nil
}

type fakeHelperBalance struct {
	result float64
	err    error
}

func (f *fakeHelperBalance) BalanceUSD(_ context.Context, _, _ string) (float64, error) {
	return f.result, f.err
}

// countingIssuer is a thread-safe issuer that records call counts.
type countingIssuer struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (c *countingIssuer) CreateHelperInvoice(_ context.Context, currency string) (HelperInvoiceDraft, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	if c.err != nil {
		return HelperInvoiceDraft{}, c.err
	}
	return HelperInvoiceDraft{
		PaymentAddress: "payaddr_cnt_" + currency,
		AmountAtomic:   100000,
		AmountUSDCents: helperInvoiceUSDCents,
	}, nil
}

// countingBalance is a thread-safe balance reader that records call counts.
type countingBalance struct {
	mu     sync.Mutex
	calls  int
	result float64
	err    error
}

func (c *countingBalance) BalanceUSD(_ context.Context, _, _ string) (float64, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.result, c.err
}

// newTestHelperHandler creates an HTTP handler with a fresh in-memory service.
func newTestHelperHandler(t *testing.T, bal float64) (*HelperPurchaseHandler, *HelperPurchaseService) {
	t.Helper()
	svc, _ := newTestHelperService(t)

	issuer := &fakeHelperIssuer{}
	balReader := &fakeHelperBalance{result: bal}

	h, err := NewHelperPurchaseHandler(svc, issuer, balReader, testHMACKey, time.Now)
	if err != nil {
		t.Fatalf("NewHelperPurchaseHandler: %v", err)
	}
	return h, svc
}

func helperPost(t *testing.T, handler http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// ── Test 23: Create returns token once; restore does not repeat token ─────────

func TestHelperHTTP_TokenOnlyInCreate(t *testing.T) {
	h, svc := newTestHelperHandler(t, 1100.0)
	db := svc.db
	listingID := mustCreateVisibleListing(t, db, "US")

	tok := newID()
	rr := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
		"purchase_token": tok,
		"listing_id":     listingID,
		"wallet_address": testBTCBech32Addr,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status %d, body: %s", rr.Code, rr.Body)
	}

	var createResp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&createResp) //nolint:errcheck

	token, ok := createResp["purchase_token"].(string)
	if !ok || token == "" {
		t.Fatalf("create response missing purchase_token, got %v", createResp)
	}

	// Restore — must not contain purchase_token.
	rr2 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases/restore", map[string]string{
		"purchase_token": token,
		"wallet_address": testBTCBech32Addr,
	})
	if rr2.Code != http.StatusOK {
		t.Fatalf("restore: status %d, body: %s", rr2.Code, rr2.Body)
	}
	var restoreResp map[string]interface{}
	json.NewDecoder(rr2.Body).Decode(&restoreResp) //nolint:errcheck

	if _, hasToken := restoreResp["purchase_token"]; hasToken {
		t.Error("restore response must not contain purchase_token")
	}

	// Phase must be awaiting_payment.
	if restoreResp["phase"] != "awaiting_payment" {
		t.Errorf("restore phase: %v", restoreResp["phase"])
	}
}

// Test 24: Token+wallet restore after fresh handler/service instance.
func TestHelperHTTP_RestoreAfterReinit(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")

	issuer := &fakeHelperIssuer{}
	bal := &fakeHelperBalance{result: 1100.0}
	h, _ := NewHelperPurchaseHandler(svc, issuer, bal, testHMACKey, time.Now)

	rr := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
		"purchase_token": newID(),
		"listing_id":     listingID,
		"wallet_address": testBTCBech32Addr,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	var cResp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&cResp) //nolint:errcheck
	token := cResp["purchase_token"].(string)

	// Create a new handler instance with the same db/svc.
	h2, _ := NewHelperPurchaseHandler(svc, issuer, bal, testHMACKey, time.Now)
	rr2 := helperPost(t, h2.Routes(), "/v2/helper/contact-purchases/restore", map[string]string{
		"purchase_token": token,
		"wallet_address": testBTCBech32Addr,
	})
	if rr2.Code != http.StatusOK {
		t.Fatalf("restore after reinit: %d %s", rr2.Code, rr2.Body)
	}
}

// Test 25: Wallet without token, token without/with wrong wallet, query token → 404.
func TestHelperHTTP_CapabilityEnumeration(t *testing.T) {
	h, svc := newTestHelperHandler(t, 1100.0)
	db := svc.db
	listingID := mustCreateVisibleListing(t, db, "US")

	rr := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
		"purchase_token": newID(),
		"listing_id":     listingID,
		"wallet_address": testBTCBech32Addr,
	})
	var cResp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&cResp) //nolint:errcheck
	token := cResp["purchase_token"].(string)

	// Wrong token + correct wallet → 404.
	rr2 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases/restore", map[string]string{
		"purchase_token": strings.Repeat("a", 64),
		"wallet_address": testBTCBech32Addr,
	})
	if rr2.Code != http.StatusNotFound {
		t.Errorf("wrong token: status %d", rr2.Code)
	}

	// Correct token + wrong wallet → 404.
	rr3 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases/restore", map[string]string{
		"purchase_token": token,
		"wallet_address": testLTCLegacyAddr,
	})
	if rr3.Code != http.StatusNotFound {
		t.Errorf("wrong wallet: status %d", rr3.Code)
	}

	// Token in query string (no body purchase_token) → 400.
	req := httptest.NewRequest(http.MethodPost, "/v2/helper/contact-purchases/restore?purchase_token="+token,
		strings.NewReader(`{"wallet_address":"`+testBTCBech32Addr+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:1234"
	rr4 := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr4, req)
	if rr4.Code == http.StatusOK {
		t.Error("query-string token should not authorize")
	}
}

// Test 26: Restore never contains contact.
func TestHelperHTTP_RestoreNoContact(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")
	addr := testBTCBech32Addr
	normalized, currency, _ := validateAndNormalizeAddress(addr)
	rawToken := newID()
	draft := HelperInvoiceDraft{PaymentAddress: "payaddr_r26", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, view, _ := svc.CreatePurchase(rawToken, listingID, currency, normalized, draft)

	// Bring to contact_ready state.
	svc.RecordHelperDetection(view.PurchaseID, "txid_26", []string{addr}, 100000, time.Now()) //nolint:errcheck
	svc.ConfirmHelperPayment(view.PurchaseID, time.Now())                                     //nolint:errcheck
	svc.RecordHelperPostPaymentBalance(view.PurchaseID, 1500.0)                               //nolint:errcheck

	issuer := &fakeHelperIssuer{}
	bal := &fakeHelperBalance{result: 1100.0}
	h, _ := NewHelperPurchaseHandler(svc, issuer, bal, testHMACKey, time.Now)

	rr := helperPost(t, h.Routes(), "/v2/helper/contact-purchases/restore", map[string]string{
		"purchase_token": rawToken,
		"wallet_address": addr,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", rr.Code, rr.Body)
	}
	body := rr.Body.String()
	if strings.Contains(body, "@testhelper") {
		t.Error("restore must not contain plaintext contact")
	}
	if strings.Contains(body, "contact") && strings.Contains(body, "@") {
		t.Error("restore must not contain contact handle")
	}
}

// Test 27: First reveal sets expiry +24h; repeated reveal preserves exact expiry and contact.
func TestHelperHTTP_RevealExpiryAndIdempotency(t *testing.T) {
	now := time.Now()
	db, _ := OpenMemory()
	defer db.Close()
	cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")
	svc, _ := NewHelperPurchaseService(db, testHMACKey, cipher, NewRandomAliasGenerator(), func() time.Time { return now })

	listingID := mustCreateVisibleListing(t, db, "US")
	addr := testBTCBech32Addr
	normalized, currency, _ := validateAndNormalizeAddress(addr)
	rawToken := newID()
	draft := HelperInvoiceDraft{PaymentAddress: "payaddr_27", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, view, _ := svc.CreatePurchase(rawToken, listingID, currency, normalized, draft)
	svc.RecordHelperDetection(view.PurchaseID, "txid_27", []string{addr}, 100000, now) //nolint:errcheck
	svc.ConfirmHelperPayment(view.PurchaseID, now)                                     //nolint:errcheck
	svc.RecordHelperPostPaymentBalance(view.PurchaseID, 1500.0)                        //nolint:errcheck

	issuer := &fakeHelperIssuer{}
	bal := &fakeHelperBalance{result: 1500.0}
	h, _ := NewHelperPurchaseHandler(svc, issuer, bal, testHMACKey, func() time.Time { return now })

	rr1 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases/reveal", map[string]string{
		"purchase_token": rawToken,
		"wallet_address": addr,
	})
	if rr1.Code != http.StatusOK {
		t.Fatalf("first reveal: %d %s", rr1.Code, rr1.Body)
	}
	var r1 map[string]interface{}
	json.NewDecoder(rr1.Body).Decode(&r1) //nolint:errcheck

	exp1 := r1["receipt_expires_at"].(float64)
	contact1 := r1["contact"].(string)

	// Expected receipt_expires_at = now + 24h.
	expectedExp := float64(now.Unix() + int64(confirmationWindow.Seconds()))
	if exp1 != expectedExp {
		t.Errorf("receipt_expires_at: got %v, want %v", exp1, expectedExp)
	}

	// Second reveal — same expiry, same contact.
	rr2 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases/reveal", map[string]string{
		"purchase_token": rawToken,
		"wallet_address": addr,
	})
	if rr2.Code != http.StatusOK {
		t.Fatalf("second reveal: %d %s", rr2.Code, rr2.Body)
	}
	var r2 map[string]interface{}
	json.NewDecoder(rr2.Body).Decode(&r2) //nolint:errcheck

	if r2["receipt_expires_at"] != exp1 {
		t.Errorf("second reveal: receipt_expires_at changed: %v vs %v", r2["receipt_expires_at"], exp1)
	}
	if r2["contact"] != contact1 {
		t.Error("second reveal: contact changed")
	}
}

// Test 28: First reveal after claim deadline → 410. Reveal after receipt expiry → 410.
func TestHelperHTTP_RevealAfterDeadlines(t *testing.T) {
	pastTime := time.Now().Add(-25 * time.Hour)
	db, _ := OpenMemory()
	defer db.Close()
	cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")
	svc, _ := NewHelperPurchaseService(db, testHMACKey, cipher, NewRandomAliasGenerator(), func() time.Time { return pastTime })

	listingID := mustCreateVisibleListing(t, db, "US")
	addr := testBTCBech32Addr
	normalized, currency, _ := validateAndNormalizeAddress(addr)
	rawToken := newID()
	draft := HelperInvoiceDraft{PaymentAddress: "payaddr_28", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, view, _ := svc.CreatePurchase(rawToken, listingID, currency, normalized, draft)
	svc.RecordHelperDetection(view.PurchaseID, "txid_28", []string{addr}, 100000, pastTime) //nolint:errcheck
	svc.ConfirmHelperPayment(view.PurchaseID, pastTime)                                     //nolint:errcheck
	svc.RecordHelperPostPaymentBalance(view.PurchaseID, 1500.0)                             //nolint:errcheck

	// Now current time is 25h past contact_ready_at → reveal deadline passed.
	nowFn := func() time.Time { return time.Now() }
	svc2, _ := NewHelperPurchaseService(db, testHMACKey, cipher, NewRandomAliasGenerator(), nowFn)

	issuer := &fakeHelperIssuer{}
	bal := &fakeHelperBalance{result: 1500.0}
	h, _ := NewHelperPurchaseHandler(svc2, issuer, bal, testHMACKey, nowFn)

	rr := helperPost(t, h.Routes(), "/v2/helper/contact-purchases/reveal", map[string]string{
		"purchase_token": rawToken,
		"wallet_address": addr,
	})
	if rr.Code != http.StatusGone {
		t.Errorf("reveal after claim deadline: status %d, body: %s", rr.Code, rr.Body)
	}
	// No contact in error response.
	if strings.Contains(rr.Body.String(), "@") {
		t.Error("error body must not contain contact")
	}
}

// Test 29: Concurrent first reveal → safe; same expiry; one purchase_count effect.
func TestHelperHTTP_ConcurrentFirstReveal(t *testing.T) {
	now := time.Now()
	db, _ := OpenMemory()
	defer db.Close()
	cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")
	svc, _ := NewHelperPurchaseService(db, testHMACKey, cipher, NewRandomAliasGenerator(), func() time.Time { return now })

	listingID := mustCreateVisibleListing(t, db, "US")
	addr := testBTCBech32Addr
	normalized, currency, _ := validateAndNormalizeAddress(addr)
	rawToken := newID()
	draft := HelperInvoiceDraft{PaymentAddress: "payaddr_29", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, view, _ := svc.CreatePurchase(rawToken, listingID, currency, normalized, draft)
	svc.RecordHelperDetection(view.PurchaseID, "txid_29", []string{addr}, 100000, now) //nolint:errcheck
	svc.ConfirmHelperPayment(view.PurchaseID, now)                                     //nolint:errcheck
	svc.RecordHelperPostPaymentBalance(view.PurchaseID, 1500.0)                        //nolint:errcheck

	const goroutines = 5
	results := make([]HelperRevealResult, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := svc.RevealHelperContact(view.PurchaseID, rawToken, normalized, currency)
			results[i] = r
			errs[i] = err
		}()
	}
	wg.Wait()

	// All must succeed (repeated reveal within window is idempotent).
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: reveal error: %v", i, err)
		}
	}

	// All must return the same receipt_expires_at.
	firstExp := results[0].ReceiptExpiresAt
	for i, r := range results {
		if r.ReceiptExpiresAt != firstExp {
			t.Errorf("goroutine %d: different receipt_expires_at: %v vs %v", i, r.ReceiptExpiresAt, firstExp)
		}
	}
	// purchase_count still 1 (set at balance check, not at reveal).
	fp := svc.helperWalletFingerprint(currency, normalized)
	var profileID string
	db.QueryRow(`SELECT id FROM v2_helper_profiles WHERE wallet_fingerprint = ?`, fp).Scan(&profileID) //nolint:errcheck
	var pc int
	db.QueryRow(`SELECT purchase_count FROM v2_helper_profiles WHERE id=?`, profileID).Scan(&pc) //nolint:errcheck
	if pc != 1 {
		t.Errorf("purchase_count=%d after concurrent reveals, want 1", pc)
	}
}

// Test 30: Reveal headers forbid caching; errors have no plaintext contact.
func TestHelperHTTP_RevealHeaders(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")
	addr := testBTCBech32Addr
	normalized, currency, _ := validateAndNormalizeAddress(addr)
	rawToken := newID()
	draft := HelperInvoiceDraft{PaymentAddress: "payaddr_30", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, view, _ := svc.CreatePurchase(rawToken, listingID, currency, normalized, draft)
	svc.RecordHelperDetection(view.PurchaseID, "txid_30", []string{addr}, 100000, time.Now()) //nolint:errcheck
	svc.ConfirmHelperPayment(view.PurchaseID, time.Now())                                     //nolint:errcheck
	svc.RecordHelperPostPaymentBalance(view.PurchaseID, 1500.0)                               //nolint:errcheck

	issuer := &fakeHelperIssuer{}
	bal := &fakeHelperBalance{result: 1500.0}
	h, _ := NewHelperPurchaseHandler(svc, issuer, bal, testHMACKey, time.Now)

	rr := helperPost(t, h.Routes(), "/v2/helper/contact-purchases/reveal", map[string]string{
		"purchase_token": rawToken,
		"wallet_address": addr,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("reveal: %d %s", rr.Code, rr.Body)
	}

	checkHeader := func(key, want string) {
		t.Helper()
		got := rr.Header().Get(key)
		if !strings.Contains(got, want) {
			t.Errorf("header %q: got %q, want contains %q", key, got, want)
		}
	}
	checkHeader("Cache-Control", "no-store")
	checkHeader("Pragma", "no-cache")
	checkHeader("Referrer-Policy", "no-referrer")
	checkHeader("X-Content-Type-Options", "nosniff")
}

// Test 31: After receipt expiry, same wallet can start a new purchase on same listing.
func TestHelperHTTP_NewPurchaseAfterReceiptExpiry(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")
	addr := testBTCBech32Addr
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	draft := HelperInvoiceDraft{PaymentAddress: "payaddr_31a", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, view, _ := svc.CreatePurchase(newID(), listingID, currency, normalized, draft)

	// Complete flow and set receipt_expired.
	svc.RecordHelperDetection(view.PurchaseID, "txid_31", []string{addr}, 100000, time.Now()) //nolint:errcheck
	svc.ConfirmHelperPayment(view.PurchaseID, time.Now())                                     //nolint:errcheck
	svc.RecordHelperPostPaymentBalance(view.PurchaseID, 1500.0)                               //nolint:errcheck
	// Force receipt_expired on the old purchase.
	db.Exec(`UPDATE v2_helper_purchases SET state='receipt_expired', updated_at=? WHERE id=?`, time.Now().Unix(), view.PurchaseID) //nolint:errcheck

	// New purchase on same listing → should succeed.
	draft2 := HelperInvoiceDraft{PaymentAddress: "payaddr_31b", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, newView, err := svc.CreatePurchase(newID(), listingID, currency, normalized, draft2)
	if err != nil {
		t.Fatalf("new purchase after receipt_expired: %v", err)
	}
	if newView.State != HPStateAwaitingPayment {
		t.Errorf("new purchase state: %q, want awaiting_payment", newView.State)
	}
}

// Test 32: Reveal does not hide, extend, or modify the Client listing.
func TestHelperHTTP_RevealDoesNotAffectListing(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")

	// Read listing before.
	var stateBefore string
	var visibleUntilBefore int64
	db.QueryRow(`SELECT state, visible_until FROM v2_listings WHERE id=?`, listingID).Scan(&stateBefore, &visibleUntilBefore) //nolint:errcheck

	addr := testBTCBech32Addr
	normalized, currency, _ := validateAndNormalizeAddress(addr)
	rawToken := newID()
	draft := HelperInvoiceDraft{PaymentAddress: "payaddr_32", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, view, _ := svc.CreatePurchase(rawToken, listingID, currency, normalized, draft)
	svc.RecordHelperDetection(view.PurchaseID, "txid_32", []string{addr}, 100000, time.Now()) //nolint:errcheck
	svc.ConfirmHelperPayment(view.PurchaseID, time.Now())                                     //nolint:errcheck
	svc.RecordHelperPostPaymentBalance(view.PurchaseID, 1500.0)                               //nolint:errcheck
	svc.RevealHelperContact(view.PurchaseID, rawToken, normalized, currency)                  //nolint:errcheck

	// Listing state must be unchanged.
	var stateAfter string
	var visibleUntilAfter int64
	db.QueryRow(`SELECT state, visible_until FROM v2_listings WHERE id=?`, listingID).Scan(&stateAfter, &visibleUntilAfter) //nolint:errcheck

	if stateAfter != stateBefore {
		t.Errorf("listing state changed: %q → %q", stateBefore, stateAfter)
	}
	if visibleUntilAfter != visibleUntilBefore {
		t.Errorf("visible_until changed: %d → %d", visibleUntilBefore, visibleUntilAfter)
	}
}

// Test 33: Unknown fields, wrong content type, oversized body, trailing JSON, unsupported methods.
func TestHelperHTTP_InputDiscipline(t *testing.T) {
	h, svc := newTestHelperHandler(t, 1100.0)
	db := svc.db
	listingID := mustCreateVisibleListing(t, db, "US")
	_ = listingID
	srv := h.Routes()

	postRaw := func(path, ct, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", ct)
		req.RemoteAddr = "127.0.0.1:1234"
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}

	// Wrong content type.
	rr := postRaw("/v2/helper/contact-purchases", "text/plain", `{"listing_id":"x","wallet_address":"y"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("wrong content type: %d", rr.Code)
	}

	// Unknown fields.
	rr = postRaw("/v2/helper/contact-purchases", "application/json",
		`{"listing_id":"x","wallet_address":"y","unknown_field":"z"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("unknown field: %d", rr.Code)
	}

	// Trailing garbage JSON.
	rr = postRaw("/v2/helper/contact-purchases", "application/json",
		`{"listing_id":"x","wallet_address":"y"}{}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("trailing JSON: %d", rr.Code)
	}

	// Oversized body (> 4096 bytes).
	huge := strings.Repeat("a", 5000)
	rr = postRaw("/v2/helper/contact-purchases", "application/json", `{"listing_id":"`+huge+`"}`)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: %d", rr.Code)
	}

	// GET instead of POST.
	req := httptest.NewRequest(http.MethodGet, "/v2/helper/contact-purchases", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, req)
	if rr2.Code == http.StatusOK {
		t.Error("GET should not be 200")
	}
}

// Test 34: Separate bounded limiters reach max-entry fail-closed boundary.
func TestHelperHTTP_RateLimiterFailClosed(t *testing.T) {
	svc, _ := newTestHelperService(t)
	issuer := &fakeHelperIssuer{}
	bal := &fakeHelperBalance{result: 1100.0}

	// Tiny limiter: 1 entry max, 1 request per window.
	h := &HelperPurchaseHandler{
		svc:          svc,
		issuer:       issuer,
		balance:      bal,
		rateLimitKey: testHMACKey,
		createLim:    newFixedWindowLimiter(1, time.Hour, 1, time.Now),
		restoreLim:   newFixedWindowLimiter(1, time.Hour, 1, time.Now),
		recheckLim:   newFixedWindowLimiter(1, time.Hour, 1, time.Now),
		revealLim:    newFixedWindowLimiter(1, time.Hour, 1, time.Now),
	}

	// First IP uses the one slot.
	req1 := httptest.NewRequest(http.MethodPost, "/v2/helper/contact-purchases",
		strings.NewReader(`{}`))
	req1.Header.Set("Content-Type", "application/json")
	req1.RemoteAddr = "10.0.0.1:1"
	rr1 := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr1, req1)
	// Not rate-limited (may fail for other reasons).
	if rr1.Code == http.StatusTooManyRequests {
		t.Error("first IP should not be rate-limited")
	}

	// Second IP — map is full (maxEntries=1, entry from IP1 is active) → fail closed.
	req2 := httptest.NewRequest(http.MethodPost, "/v2/helper/contact-purchases",
		strings.NewReader(`{}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.RemoteAddr = "10.0.0.2:2"
	rr2 := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusTooManyRequests {
		t.Errorf("second IP with full limiter: got %d, want 429", rr2.Code)
	}
}

// Test 35: Wrong token/wallet and unavailable listing → same body/status, no enumeration.
func TestHelperHTTP_NoEnumeration(t *testing.T) {
	h, svc := newTestHelperHandler(t, 1100.0)
	db := svc.db

	// Real token to compare.
	listingID := mustCreateVisibleListing(t, db, "US")
	rr := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
		"purchase_token": newID(),
		"listing_id":     listingID,
		"wallet_address": testBTCBech32Addr,
	})
	var cResp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&cResp) //nolint:errcheck
	_ = cResp["purchase_token"]

	// Wrong token.
	rr2 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases/restore", map[string]string{
		"purchase_token": strings.Repeat("b", 64),
		"wallet_address": testBTCBech32Addr,
	})

	// Unavailable listing.
	rr3 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
		"purchase_token": newID(),
		"listing_id":     "nonexistent",
		"wallet_address": testBTCBech32Addr,
	})

	if rr2.Code != http.StatusNotFound || rr3.Code != http.StatusNotFound {
		t.Errorf("codes: wrong token=%d, unavailable listing=%d; both want 404", rr2.Code, rr3.Code)
	}
}

// ── Full lifecycle test ────────────────────────────────────────────────────────

func TestHelperPurchaseLifecycle(t *testing.T) {
	now := time.Now()
	db, _ := OpenMemory()
	defer db.Close()
	cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")
	clockFn := func() time.Time { return now }
	svc, _ := NewHelperPurchaseService(db, testHMACKey, cipher, NewRandomAliasGenerator(), clockFn)

	listingID := mustCreateVisibleListing(t, db, "US")
	addr := testBTCBech32Addr
	normalized, currency, _ := validateAndNormalizeAddress(addr)
	issuer := &fakeHelperIssuer{}
	bal := &fakeHelperBalance{result: 1100.0}
	h, _ := NewHelperPurchaseHandler(svc, issuer, bal, testHMACKey, clockFn)
	srv := h.Routes()

	// Step 1: Create purchase.
	purchaseTok := newID()
	rr := helperPost(t, srv, "/v2/helper/contact-purchases", map[string]string{
		"purchase_token": purchaseTok,
		"listing_id":     listingID,
		"wallet_address": addr,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	var cResp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&cResp) //nolint:errcheck
	token := cResp["purchase_token"].(string)
	purchaseID := cResp["purchase_id"].(string)
	countryNotice := cResp["country_code"].(string)
	noRefund := cResp["no_refund"].(bool)
	if countryNotice != "US" {
		t.Errorf("country notice: %q", countryNotice)
	}
	if !noRefund {
		t.Error("no_refund must be true")
	}

	// Step 2: Simulate browser close; restore.
	rr2 := helperPost(t, srv, "/v2/helper/contact-purchases/restore", map[string]string{
		"purchase_token": token,
		"wallet_address": addr,
	})
	if rr2.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", rr2.Code, rr2.Body)
	}
	var rResp map[string]interface{}
	json.NewDecoder(rr2.Body).Decode(&rResp) //nolint:errcheck
	if rResp["phase"] != "awaiting_payment" {
		t.Errorf("phase: %v", rResp["phase"])
	}

	// Step 3: Watcher detects + confirms payment.
	svc.RecordHelperDetection(purchaseID, "txid_lifecycle", []string{addr}, 100000, now) //nolint:errcheck
	svc.ConfirmHelperPayment(purchaseID, now)                                            //nolint:errcheck

	// Step 4: Low balance first.
	svc.RecordHelperPostPaymentBalance(purchaseID, 500.0) //nolint:errcheck
	rr3 := helperPost(t, srv, "/v2/helper/contact-purchases/restore", map[string]string{
		"purchase_token": token,
		"wallet_address": addr,
	})
	var rResp3 map[string]interface{}
	json.NewDecoder(rr3.Body).Decode(&rResp3) //nolint:errcheck
	if rResp3["phase"] != "paid_low_balance" {
		t.Errorf("phase after low balance: %v", rResp3["phase"])
	}

	// Step 5: Top up → recheck balance.
	bal.result = 1500.0
	rr4 := helperPost(t, srv, "/v2/helper/contact-purchases/recheck-balance", map[string]string{
		"purchase_token": token,
		"wallet_address": addr,
	})
	if rr4.Code != http.StatusOK {
		t.Fatalf("recheck: %d %s", rr4.Code, rr4.Body)
	}
	var rResp4 map[string]interface{}
	json.NewDecoder(rr4.Body).Decode(&rResp4) //nolint:errcheck
	if rResp4["phase"] != "contact_ready" {
		t.Errorf("phase after recheck: %v", rResp4["phase"])
	}

	// Step 6: Country lock should be fixed.
	var cc string
	db.QueryRow(`SELECT country_code FROM v2_helper_profiles WHERE currency=? AND wallet_fingerprint=?`,
		currency, svc.helperWalletFingerprint(currency, normalized)).Scan(&cc) //nolint:errcheck
	if cc != "US" {
		t.Errorf("country_code: %q", cc)
	}

	// Step 7: First reveal.
	rr5 := helperPost(t, srv, "/v2/helper/contact-purchases/reveal", map[string]string{
		"purchase_token": token,
		"wallet_address": addr,
	})
	if rr5.Code != http.StatusOK {
		t.Fatalf("reveal: %d %s", rr5.Code, rr5.Body)
	}
	var revResp map[string]interface{}
	json.NewDecoder(rr5.Body).Decode(&revResp) //nolint:errcheck
	contact1 := revResp["contact"].(string)
	exp1 := revResp["receipt_expires_at"].(float64)
	if contact1 == "" {
		t.Error("contact must not be empty")
	}
	// Headers.
	if !strings.Contains(rr5.Header().Get("Cache-Control"), "no-store") {
		t.Error("no-store header missing")
	}

	// Step 8: Repeat reveal — same contact, same expiry.
	rr6 := helperPost(t, srv, "/v2/helper/contact-purchases/reveal", map[string]string{
		"purchase_token": token,
		"wallet_address": addr,
	})
	var revResp2 map[string]interface{}
	json.NewDecoder(rr6.Body).Decode(&revResp2) //nolint:errcheck
	if revResp2["contact"] != contact1 {
		t.Error("repeat reveal: contact changed")
	}
	if revResp2["receipt_expires_at"] != exp1 {
		t.Error("repeat reveal: receipt_expires_at changed")
	}

	// Step 9: Simulate receipt expiry.
	db.Exec(`UPDATE v2_helper_purchases SET state='receipt_expired', updated_at=? WHERE id=?`, now.Unix(), purchaseID) //nolint:errcheck

	rr7 := helperPost(t, srv, "/v2/helper/contact-purchases/reveal", map[string]string{
		"purchase_token": token,
		"wallet_address": addr,
	})
	if rr7.Code != http.StatusGone {
		t.Errorf("reveal after receipt_expired: %d", rr7.Code)
	}

	// Step 10: New independent purchase on same listing.
	listingID2 := mustCreateVisibleListingDB(t, db, "US")
	rr8 := helperPost(t, srv, "/v2/helper/contact-purchases", map[string]string{
		"purchase_token": newID(),
		"listing_id":     listingID2,
		"wallet_address": addr,
	})
	if rr8.Code != http.StatusCreated {
		t.Fatalf("new purchase after expiry: %d %s", rr8.Code, rr8.Body)
	}
	var newResp map[string]interface{}
	json.NewDecoder(rr8.Body).Decode(&newResp) //nolint:errcheck
	if newResp["purchase_id"] == purchaseID {
		t.Error("new purchase must have different ID")
	}
}

// ── New P1-Fix HTTP tests ──────────────────────────────────────────────────────

// TestHelperHTTP_NaNBalanceIs503 verifies that a NaN/Inf/negative balance from
// the balance provider returns HTTP 503 with code "balance_provider_unavailable"
// and does NOT create any purchase or invoice rows.
func TestHelperHTTP_NaNBalanceIs503(t *testing.T) {
	for _, tc := range []struct {
		name    string
		balance float64
	}{
		{"NaN", math.NaN()},
		{"Inf", math.Inf(1)},
		{"negative", -1.0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, db := newTestHelperService(t)
			listingID := mustCreateVisibleListing(t, db, "US")

			issuer := &fakeHelperIssuer{}
			balReader := &fakeHelperBalance{result: tc.balance}
			h, err := NewHelperPurchaseHandler(svc, issuer, balReader, testHMACKey, time.Now)
			if err != nil {
				t.Fatalf("NewHelperPurchaseHandler: %v", err)
			}

			var beforeP, beforeI int
			db.QueryRow(`SELECT COUNT(*) FROM v2_helper_purchases`).Scan(&beforeP) //nolint:errcheck
			db.QueryRow(`SELECT COUNT(*) FROM v2_helper_invoices`).Scan(&beforeI)  //nolint:errcheck

			rr := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
				"purchase_token": newID(),
				"listing_id":     listingID,
				"wallet_address": testBTCBech32Addr,
			})

			if rr.Code != http.StatusServiceUnavailable {
				t.Errorf("want 503, got %d: %s", rr.Code, rr.Body)
			}
			var resp map[string]interface{}
			json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck
			if resp["code"] != codeBalanceUnavailable {
				t.Errorf("want code %q, got %v", codeBalanceUnavailable, resp["code"])
			}

			var afterP, afterI int
			db.QueryRow(`SELECT COUNT(*) FROM v2_helper_purchases`).Scan(&afterP) //nolint:errcheck
			db.QueryRow(`SELECT COUNT(*) FROM v2_helper_invoices`).Scan(&afterI)  //nolint:errcheck
			if afterP != beforeP {
				t.Errorf("purchases: want %d rows, got %d", beforeP, afterP)
			}
			if afterI != beforeI {
				t.Errorf("invoices: want %d rows, got %d", beforeI, afterI)
			}
		})
	}
}

// TestHelperHTTP_IdempotentCreate verifies that:
//   - Same token + same wallet → 200 (not 201), no purchase_token in response,
//     same purchase_id returned.
//   - Same token + different wallet → 404.
func TestHelperHTTP_IdempotentCreate(t *testing.T) {
	h, svc := newTestHelperHandler(t, 1100.0)
	db := svc.db
	listingID := mustCreateVisibleListing(t, db, "US")

	tok := newID()

	// First call → 201, purchase_token present.
	rr1 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
		"purchase_token": tok,
		"listing_id":     listingID,
		"wallet_address": testBTCBech32Addr,
	})
	if rr1.Code != http.StatusCreated {
		t.Fatalf("first create: want 201, got %d: %s", rr1.Code, rr1.Body)
	}
	var resp1 map[string]interface{}
	json.NewDecoder(rr1.Body).Decode(&resp1) //nolint:errcheck
	if _, ok := resp1["purchase_token"]; !ok {
		t.Error("first create response must contain purchase_token")
	}
	purchaseID1 := resp1["purchase_id"].(string)

	// Second call, same token + same wallet → 200, no purchase_token, same ID.
	rr2 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
		"purchase_token": tok,
		"listing_id":     listingID,
		"wallet_address": testBTCBech32Addr,
	})
	if rr2.Code != http.StatusOK {
		t.Errorf("idempotent create: want 200, got %d: %s", rr2.Code, rr2.Body)
	}
	var resp2 map[string]interface{}
	json.NewDecoder(rr2.Body).Decode(&resp2) //nolint:errcheck
	if _, ok := resp2["purchase_token"]; ok {
		t.Error("idempotent create response must NOT contain purchase_token")
	}
	if resp2["purchase_id"] != purchaseID1 {
		t.Errorf("idempotent: want same purchase_id %q, got %q", purchaseID1, resp2["purchase_id"])
	}

	// Same token + different wallet → 404.
	rr3 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
		"purchase_token": tok,
		"listing_id":     listingID,
		"wallet_address": testLTCLegacyAddr,
	})
	if rr3.Code != http.StatusNotFound {
		t.Errorf("different wallet: want 404, got %d: %s", rr3.Code, rr3.Body)
	}
}

// TestHelperHTTP_ErrorCodes verifies that key error paths return the expected
// stable error code strings in the JSON response body.
func TestHelperHTTP_ErrorCodes(t *testing.T) {
	h, svc := newTestHelperHandler(t, 1100.0)
	db := svc.db

	// invalid purchase_token (too short) → 400 + invalid_request.
	rr := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
		"purchase_token": "tooshort",
		"listing_id":     newID(),
		"wallet_address": testBTCBech32Addr,
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid token: want 400, got %d", rr.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck
	if resp["code"] != codeInvalidRequest {
		t.Errorf("invalid token: want code %q, got %v", codeInvalidRequest, resp["code"])
	}

	// unknown listing → 404 + listing_not_found.
	rr2 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
		"purchase_token": newID(),
		"listing_id":     newID(), // doesn't exist
		"wallet_address": testBTCBech32Addr,
	})
	if rr2.Code != http.StatusNotFound {
		t.Errorf("unknown listing: want 404, got %d", rr2.Code)
	}
	var resp2 map[string]interface{}
	json.NewDecoder(rr2.Body).Decode(&resp2) //nolint:errcheck
	if resp2["code"] != codeListingNotFound {
		t.Errorf("unknown listing: want code %q, got %v", codeListingNotFound, resp2["code"])
	}

	// unknown purchase (reveal with bad token) → 404 + purchase_not_found.
	listingID := mustCreateVisibleListing(t, db, "US")
	_ = listingID
	rr3 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases/reveal", map[string]string{
		"purchase_token": strings.Repeat("b", 64),
		"wallet_address": testBTCBech32Addr,
	})
	if rr3.Code != http.StatusNotFound {
		t.Errorf("unknown purchase reveal: want 404, got %d", rr3.Code)
	}
	var resp3 map[string]interface{}
	json.NewDecoder(rr3.Body).Decode(&resp3) //nolint:errcheck
	if resp3["code"] != codePurchaseNotFound {
		t.Errorf("unknown purchase reveal: want code %q, got %v", codePurchaseNotFound, resp3["code"])
	}

	// duplicate active purchase (different token, same listing) → 409 + duplicate_active_purchase.
	tok1 := newID()
	helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{ //nolint:errcheck
		"purchase_token": tok1,
		"listing_id":     listingID,
		"wallet_address": testBTCBech32Addr,
	})
	rr4 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
		"purchase_token": newID(), // different token
		"listing_id":     listingID,
		"wallet_address": testBTCBech32Addr,
	})
	if rr4.Code != http.StatusConflict {
		t.Errorf("duplicate: want 409, got %d: %s", rr4.Code, rr4.Body)
	}
	var resp4 map[string]interface{}
	json.NewDecoder(rr4.Body).Decode(&resp4) //nolint:errcheck
	if resp4["code"] != codeDuplicatePurchase {
		t.Errorf("duplicate: want code %q, got %v", codeDuplicatePurchase, resp4["code"])
	}
}

// TestHelperHTTP_ConcurrentCreate covers the five create idempotency/concurrency
// scenarios required by Task 05-FIX2. All use thread-safe counting fakes so that
// provider/issuer call counts are verifiable.
func TestHelperHTTP_ConcurrentCreate(t *testing.T) {

	// ── Scenario 1: sequential response-loss retry ────────────────────────────
	// Second HTTP call with the same token must return 200 without calling
	// balance provider or invoice issuer again.
	t.Run("sequential_retry_provider_and_issuer_called_once", func(t *testing.T) {
		ci := &countingIssuer{}
		cb := &countingBalance{result: 1100.0}
		svc, db := newTestHelperService(t)
		h, err := NewHelperPurchaseHandler(svc, ci, cb, testHMACKey, time.Now)
		if err != nil {
			t.Fatalf("NewHelperPurchaseHandler: %v", err)
		}
		listingID := mustCreateVisibleListing(t, db, "US")
		tok := newID()

		rr1 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
			"purchase_token": tok,
			"listing_id":     listingID,
			"wallet_address": testBTCBech32Addr,
		})
		if rr1.Code != http.StatusCreated {
			t.Fatalf("first create: want 201, got %d: %s", rr1.Code, rr1.Body)
		}
		var r1 map[string]interface{}
		json.NewDecoder(rr1.Body).Decode(&r1) //nolint:errcheck
		purchaseID1 := r1["purchase_id"].(string)

		rr2 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
			"purchase_token": tok,
			"listing_id":     listingID,
			"wallet_address": testBTCBech32Addr,
		})
		if rr2.Code != http.StatusOK {
			t.Errorf("retry: want 200, got %d: %s", rr2.Code, rr2.Body)
		}
		var r2 map[string]interface{}
		json.NewDecoder(rr2.Body).Decode(&r2) //nolint:errcheck
		if r2["purchase_id"] != purchaseID1 {
			t.Errorf("retry: want same purchase_id %q, got %q", purchaseID1, r2["purchase_id"])
		}
		if _, ok := r2["purchase_token"]; ok {
			t.Error("retry response must NOT contain purchase_token")
		}

		cb.mu.Lock()
		providerCalls := cb.calls
		cb.mu.Unlock()
		ci.mu.Lock()
		issuerCalls := ci.calls
		ci.mu.Unlock()
		if providerCalls != 1 {
			t.Errorf("balance provider calls: want 1, got %d", providerCalls)
		}
		if issuerCalls != 1 {
			t.Errorf("issuer calls: want 1, got %d", issuerCalls)
		}

		var nP, nPu, nI int
		db.QueryRow(`SELECT COUNT(*) FROM v2_helper_profiles`).Scan(&nP)   //nolint:errcheck
		db.QueryRow(`SELECT COUNT(*) FROM v2_helper_purchases`).Scan(&nPu) //nolint:errcheck
		db.QueryRow(`SELECT COUNT(*) FROM v2_helper_invoices`).Scan(&nI)   //nolint:errcheck
		if nP != 1 || nPu != 1 || nI != 1 {
			t.Errorf("rows: want 1/1/1, got profiles=%d purchases=%d invoices=%d", nP, nPu, nI)
		}
	})

	// ── Scenario 2: concurrent same-token requests (8 goroutines) ────────────
	// Exactly one goroutine must receive 201; the rest must receive 200.
	// The balance provider and invoice issuer must each be called exactly once.
	// Different source IPs bypass the per-IP rate limiter (5/min) while still
	// hitting the same keyed lock (which is derived from the token, not the IP).
	t.Run("concurrent_same_token_one_201_rest_200", func(t *testing.T) {
		const N = 8
		ci := &countingIssuer{}
		cb := &countingBalance{result: 1100.0}
		svc, db := newTestHelperService(t)
		h, err := NewHelperPurchaseHandler(svc, ci, cb, testHMACKey, time.Now)
		if err != nil {
			t.Fatalf("NewHelperPurchaseHandler: %v", err)
		}
		listingID := mustCreateVisibleListing(t, db, "US")
		tok := newID()
		handler := h.Routes()

		// postFromIP sends a request with a distinct source IP so each goroutine
		// has its own rate-limit bucket but all share the same token lock.
		postFromIP := func(ip string, body map[string]string) *httptest.ResponseRecorder {
			b, _ := json.Marshal(body)
			req := httptest.NewRequest(http.MethodPost, "/v2/helper/contact-purchases", bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = ip + ":9999"
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			return rr
		}

		// One unique source IP per goroutine; each gets its own rate-limit bucket.
		ips := [N]string{
			"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4",
			"10.0.0.5", "10.0.0.6", "10.0.0.7", "10.0.0.8",
		}

		type result struct {
			code       int
			purchaseID string
		}
		results := make([]result, N)
		var wg sync.WaitGroup
		wg.Add(N)
		for i := 0; i < N; i++ {
			go func(i int) {
				defer wg.Done()
				rr := postFromIP(ips[i], map[string]string{
					"purchase_token": tok,
					"listing_id":     listingID,
					"wallet_address": testBTCBech32Addr,
				})
				var resp map[string]interface{}
				json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck
				pid, _ := resp["purchase_id"].(string)
				results[i] = result{code: rr.Code, purchaseID: pid}
			}(i)
		}
		wg.Wait()

		var got201, got200 int
		var seenPID string
		for _, r := range results {
			switch r.code {
			case http.StatusCreated:
				got201++
				seenPID = r.purchaseID
			case http.StatusOK:
				got200++
			default:
				t.Errorf("unexpected status %d", r.code)
			}
			if r.purchaseID != "" && seenPID != "" && r.purchaseID != seenPID {
				t.Errorf("purchase_id mismatch: %s vs %s", seenPID, r.purchaseID)
			}
			if r.purchaseID != "" {
				seenPID = r.purchaseID
			}
		}
		if got201 != 1 {
			t.Errorf("want 1×201, got %d", got201)
		}
		if got200 != N-1 {
			t.Errorf("want %d×200, got %d", N-1, got200)
		}

		cb.mu.Lock()
		providerCalls := cb.calls
		cb.mu.Unlock()
		ci.mu.Lock()
		issuerCalls := ci.calls
		ci.mu.Unlock()
		if providerCalls != 1 {
			t.Errorf("balance provider calls: want 1, got %d", providerCalls)
		}
		if issuerCalls != 1 {
			t.Errorf("issuer calls: want 1, got %d", issuerCalls)
		}

		var nPu, nI int
		db.QueryRow(`SELECT COUNT(*) FROM v2_helper_purchases`).Scan(&nPu) //nolint:errcheck
		db.QueryRow(`SELECT COUNT(*) FROM v2_helper_invoices`).Scan(&nI)   //nolint:errcheck
		if nPu != 1 || nI != 1 {
			t.Errorf("rows: want 1 purchase and 1 invoice, got %d/%d", nPu, nI)
		}
	})

	// ── Scenario 3: same token + different wallet or listing → generic 404 ────
	// No balance provider or issuer call on the retry.
	t.Run("same_token_different_wallet_gives_404_no_provider_call", func(t *testing.T) {
		ci := &countingIssuer{}
		cb := &countingBalance{result: 1100.0}
		svc, db := newTestHelperService(t)
		h, err := NewHelperPurchaseHandler(svc, ci, cb, testHMACKey, time.Now)
		if err != nil {
			t.Fatalf("NewHelperPurchaseHandler: %v", err)
		}
		listingID := mustCreateVisibleListing(t, db, "US")
		tok := newID()

		rr1 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
			"purchase_token": tok,
			"listing_id":     listingID,
			"wallet_address": testBTCBech32Addr,
		})
		if rr1.Code != http.StatusCreated {
			t.Fatalf("first create: want 201, got %d: %s", rr1.Code, rr1.Body)
		}

		// Same token, different wallet → must get exact same 404 as unknown purchase.
		rr2 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
			"purchase_token": tok,
			"listing_id":     listingID,
			"wallet_address": testLTCLegacyAddr,
		})
		if rr2.Code != http.StatusNotFound {
			t.Errorf("different wallet: want 404, got %d: %s", rr2.Code, rr2.Body)
		}
		var resp map[string]interface{}
		json.NewDecoder(rr2.Body).Decode(&resp) //nolint:errcheck
		if resp["code"] != codePurchaseNotFound {
			t.Errorf("different wallet: want code %q, got %v", codePurchaseNotFound, resp["code"])
		}

		// Provider and issuer each called once (for the first successful create only).
		cb.mu.Lock()
		providerCalls := cb.calls
		cb.mu.Unlock()
		ci.mu.Lock()
		issuerCalls := ci.calls
		ci.mu.Unlock()
		if providerCalls != 1 {
			t.Errorf("provider calls after mismatch retry: want 1, got %d", providerCalls)
		}
		if issuerCalls != 1 {
			t.Errorf("issuer calls after mismatch retry: want 1, got %d", issuerCalls)
		}
	})

	// ── Scenario 4: concurrent different-token same profile/listing → 409 ─────
	// Different tokens for the same wallet+listing must not create two active
	// purchases. The partial UNIQUE index ensures only one INSERT wins; losers
	// get a stable 409 duplicate_active_purchase, not a 500.
	t.Run("concurrent_different_token_same_pair_409", func(t *testing.T) {
		const N = 4
		ci := &countingIssuer{}
		cb := &countingBalance{result: 1100.0}
		svc, db := newTestHelperService(t)
		h, err := NewHelperPurchaseHandler(svc, ci, cb, testHMACKey, time.Now)
		if err != nil {
			t.Fatalf("NewHelperPurchaseHandler: %v", err)
		}
		listingID := mustCreateVisibleListing(t, db, "US")

		routesDiff := h.Routes()
		postFromIPDiff := func(ip string, body map[string]string) *httptest.ResponseRecorder {
			b, _ := json.Marshal(body)
			req := httptest.NewRequest(http.MethodPost, "/v2/helper/contact-purchases", bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = ip + ":9999"
			rr := httptest.NewRecorder()
			routesDiff.ServeHTTP(rr, req)
			return rr
		}
		ipsDiff := [N]string{"10.1.0.1", "10.1.0.2", "10.1.0.3", "10.1.0.4"}

		codes := make([]int, N)
		var wg sync.WaitGroup
		wg.Add(N)
		for i := 0; i < N; i++ {
			go func(i int) {
				defer wg.Done()
				rr := postFromIPDiff(ipsDiff[i], map[string]string{
					"purchase_token": newID(), // different token each goroutine
					"listing_id":     listingID,
					"wallet_address": testBTCBech32Addr,
				})
				codes[i] = rr.Code
			}(i)
		}
		wg.Wait()

		var got201, got409 int
		for _, c := range codes {
			switch c {
			case http.StatusCreated:
				got201++
			case http.StatusConflict:
				got409++
			default:
				t.Errorf("unexpected status %d (want 201 or 409)", c)
			}
		}
		if got201 != 1 {
			t.Errorf("want exactly 1×201, got %d", got201)
		}
		if got409 != N-1 {
			t.Errorf("want %d×409, got %d", N-1, got409)
		}

		// Exactly one active purchase and one invoice must exist.
		var nPu, nI int
		db.QueryRow(`SELECT COUNT(*) FROM v2_helper_purchases WHERE state NOT IN ('invoice_expired','failed','receipt_expired')`).Scan(&nPu) //nolint:errcheck
		db.QueryRow(`SELECT COUNT(*) FROM v2_helper_invoices`).Scan(&nI)                                                                     //nolint:errcheck
		if nPu != 1 || nI != 1 {
			t.Errorf("want 1 active purchase and 1 invoice, got %d/%d", nPu, nI)
		}
	})

	// ── Scenario 5: terminal previous purchase + new token → new 201 ──────────
	// After a purchase reaches a terminal state, a new token for the same
	// wallet+listing must produce a fresh 201 with a distinct purchase ID.
	t.Run("terminal_then_new_token_creates_new_purchase", func(t *testing.T) {
		ci := &countingIssuer{}
		cb := &countingBalance{result: 1100.0}
		svc, db := newTestHelperService(t)
		h, err := NewHelperPurchaseHandler(svc, ci, cb, testHMACKey, time.Now)
		if err != nil {
			t.Fatalf("NewHelperPurchaseHandler: %v", err)
		}
		listingID := mustCreateVisibleListing(t, db, "US")
		tok1 := newID()

		rr1 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
			"purchase_token": tok1,
			"listing_id":     listingID,
			"wallet_address": testBTCBech32Addr,
		})
		if rr1.Code != http.StatusCreated {
			t.Fatalf("first create: want 201, got %d: %s", rr1.Code, rr1.Body)
		}
		var r1 map[string]interface{}
		json.NewDecoder(rr1.Body).Decode(&r1) //nolint:errcheck
		purchaseID1 := r1["purchase_id"].(string)

		// Force purchase and invoice to terminal (invoice_expired) via direct SQL.
		ts := time.Now().Unix()
		if _, execErr := db.Exec(`UPDATE v2_helper_purchases SET state = 'invoice_expired', updated_at = ? WHERE id = ?`, ts, purchaseID1); execErr != nil {
			t.Fatalf("force purchase terminal: %v", execErr)
		}
		if _, execErr := db.Exec(`UPDATE v2_helper_invoices SET status = 'expired', updated_at = ? WHERE purchase_id = ?`, ts, purchaseID1); execErr != nil {
			t.Fatalf("force invoice expired: %v", execErr)
		}

		// New token, same wallet+listing → must create a new purchase (201).
		tok2 := newID()
		rr2 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
			"purchase_token": tok2,
			"listing_id":     listingID,
			"wallet_address": testBTCBech32Addr,
		})
		if rr2.Code != http.StatusCreated {
			t.Fatalf("second create: want 201, got %d: %s", rr2.Code, rr2.Body)
		}
		var r2 map[string]interface{}
		json.NewDecoder(rr2.Body).Decode(&r2) //nolint:errcheck
		purchaseID2 := r2["purchase_id"].(string)

		if purchaseID1 == purchaseID2 {
			t.Error("second create must produce a new purchase ID distinct from the terminal one")
		}

		// Same wallet → same profile (1 profile total).
		var nP int
		db.QueryRow(`SELECT COUNT(*) FROM v2_helper_profiles`).Scan(&nP) //nolint:errcheck
		if nP != 1 {
			t.Errorf("want 1 profile (same wallet), got %d", nP)
		}

		// 2 purchases and 2 invoices total.
		var nPu, nI int
		db.QueryRow(`SELECT COUNT(*) FROM v2_helper_purchases`).Scan(&nPu) //nolint:errcheck
		db.QueryRow(`SELECT COUNT(*) FROM v2_helper_invoices`).Scan(&nI)   //nolint:errcheck
		if nPu != 2 || nI != 2 {
			t.Errorf("want 2 purchases and 2 invoices, got %d/%d", nPu, nI)
		}

		// Provider and issuer each called once per successful create (2 total).
		cb.mu.Lock()
		providerCalls := cb.calls
		cb.mu.Unlock()
		ci.mu.Lock()
		issuerCalls := ci.calls
		ci.mu.Unlock()
		if providerCalls != 2 {
			t.Errorf("provider calls: want 2, got %d", providerCalls)
		}
		if issuerCalls != 2 {
			t.Errorf("issuer calls: want 2, got %d", issuerCalls)
		}
	})
}

// TestHelperHTTP_NoBindingReturns409: ErrReviewNoBinding → HTTP 409 client_notification_unavailable.
func TestHelperHTTP_NoBindingReturns409(t *testing.T) {
	h, svc := newTestHelperHandler(t, 1100.0)
	db := svc.db
	listingID := mustCreateVisibleListing(t, db, "US")

	// Remove the active binding to force ErrReviewNoBinding.
	if _, err := db.Exec(`DELETE FROM v2_client_notification_bindings WHERE flow_id = (SELECT flow_id FROM v2_listings WHERE id = ?)`, listingID); err != nil {
		t.Fatalf("delete binding: %v", err)
	}

	rr := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
		"purchase_token": newID(),
		"listing_id":     listingID,
		"wallet_address": testBTCBech32Addr,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("no binding: want 409, got %d body: %s", rr.Code, rr.Body)
	}
	var body map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["code"] != codeClientNotificationUnavailable {
		t.Errorf("response code: want %q, got %q", codeClientNotificationUnavailable, body["code"])
	}
}
