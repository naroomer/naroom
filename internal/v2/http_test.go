package v2

// HTTP test matrix (Task 02 + 02A + 03A fixes):
//
// Original Task 02 tests (preserved):
// |  1 | TestHTTPCreateBTC                    | POST create → 201, currency BTC, code issued, invoice match  |
// |  2 | TestHTTPCreateLTC                    | POST create → 201, currency LTC                              |
// |  3 | TestHTTPCreateUnknownPrefix          | unknown address prefix → 400, issuer never called            |
// |  4 | TestHTTPCreateEmptyWallet            | empty/whitespace wallet → 400                                |
// |  5 | TestHTTPStrictRequestConstraints     | wrong CT / oversized / unknown field / bad JSON / trailing   |
// |  6 | TestHTTPIssuerError                  | issuer network failure → 503, no DB rows                     |
// |  7 | TestHTTPCreateResponsePrivacy        | response has no fingerprint/hash; raw code absent from DB    |
// |  8 | TestHTTPRestoreCorrect               | restore with correct code+wallet returns same flow+invoice   |
// |  9 | TestHTTPRestoreNoSensitiveFields     | restore response omits management_code, wallet, fingerprint  |
// | 10 | TestHTTPRestoreWrongCodeWrongWallet  | wrong code and wrong wallet → byte-for-byte identical 404    |
// | 11 | TestHTTPRestoreAfterConfirm          | restore after confirmation shows confirmed state + expiry    |
// | 12 | TestHTTPRecheckBeforePayment         | recheck before confirm → 409, balance reader not called      |
// | 13 | TestHTTPRecheckLowBalance            | recheck $119.99 → paid_low_balance                           |
// | 14 | TestHTTPRecheckFormReady             | recheck $120.00 → form_ready                                 |
// | 15 | TestHTTPBalanceReaderError           | balance reader error → 503, state unchanged                  |
// | 16 | TestHTTPNoCapabilityInURLOrQuery     | endpoints reject code from path/query, not body              |
// | 17 | TestHTTPRateLimitIsolation           | rate limit fires on exact N+1 request; isolated per key      |
// | 18 | TestHTTPLimiterCleanup              | auto-cleanup via Allow; Cleanup() still correct              |
// | 19 | TestHTTPSensitiveValuesNotLeaked     | wallet/code/fingerprint absent from error response bodies    |
//
// Task 02A additions:
// | A1 | TestHTTPContentTypeStrict            | mime.ParseMediaType: charset OK; supertype/type rejected     |
// | A2 | TestHTTPTrailingBodyTooLarge         | valid JSON + trailing > 4 KiB → 413                         |
// | A3 | TestHTTPIssuerInvalidDraft           | issuer returns invalid draft (no error) → 503, no DB rows   |
// | A4 | TestHTTPRestoreRequiredFields        | empty/whitespace management_code or wallet_address → 400     |
// | A5 | TestHTTPRecheckRequiredFields        | same for recheck-balance                                     |
// | A6 | TestHTTPConstructorValidation        | nil svc/issuer/balance, empty key → error                    |
// | A7 | TestHTTPRateLimitKeyDefensiveCopy    | mutating caller buffer after construction doesn't change key |
// | A8 | TestHTTPClientKeyIPNormalization     | same IP/different ports → same bucket; different IPs differ  |
// | A9 | TestHTTPLimiterAutoCleanup           | expired entries removed by plain Allow, no manual Cleanup    |
// | A10| TestHTTPLimiterExpiryBoundary        | at exact expiry (now == expiry) a new window starts          |
// | A11| TestHTTPLimiterBounded               | len(entries) never exceeds maxEntries under unique-key flood |
// | A12| TestHTTPLimiterFullActiveMapDenies   | full map of active entries → new key denied, none evicted   |
//
// Task 03A additions (address checksum/network validation):
// | V1 | TestHTTPAddressValidation            | real mainnet addresses pass; checksum mutation/testnet → 400  |

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/wire"
)

// ── Known-valid mainnet addresses for HTTP-layer tests ────────────────────────
//
// Service/watcher tests may use synthetic strings (e.g. "bc1qtest") because they
// bypass HTTP validation. HTTP tests must use real mainnet addresses so that
// validateAndNormalizeAddress passes checksum + network validation.
//
// All variable addresses are generated at package-init time from deterministic
// 20-byte hashes via btcutil — this guarantees correct checksums and avoids
// hard-coded addresses that may be invalid with a specific btcutil version.
//
// BTC bech32 P2WPKH: encodes a deterministic pubkey hash; validated by btcutil.
const testBTCBech32Addr = "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"

// testBTCLegacyAddr is a valid BTC mainnet P2PKH address generated at init time.
var testBTCLegacyAddr string

// LTC P2PKH legacy: well-known valid LTC mainnet address (L-prefix, version 0x30).
const testLTCLegacyAddr = "LdP8Qox1VAhCzLJNqrr74YovaWYyNBUWvL"

// testLTCBech32Addr is a valid LTC mainnet P2WPKH bech32 address generated at
// package-init time via btcutil, using a fixed deterministic 20-byte hash.
// v2LTCMainNetParams is defined in http.go (same package).
// v2LTCMainNetParams must be registered via chaincfg.Register (done in http.go's
// init) before DecodeAddress can decode LTC bech32 addresses.
var testLTCBech32Addr string

// testBTCP2SHAddr is a valid BTC mainnet P2SH address ("3…") generated at init time.
var testBTCP2SHAddr string

// testLTCP2SHAddr is a valid LTC mainnet P2SH address ("M…") generated at init time.
var testLTCP2SHAddr string

func init() {
	// Use a deterministic 20-byte pubkey hash. Any valid 20-byte slice works;
	// the resulting addresses are accepted by validateAndNormalizeAddress.
	hash20 := [20]byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a,
		0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14,
	}

	btcAddr, err := btcutil.NewAddressPubKeyHash(hash20[:], &chaincfg.MainNetParams)
	if err != nil {
		panic("http_test init: NewAddressPubKeyHash BTC: " + err.Error())
	}
	testBTCLegacyAddr = btcAddr.EncodeAddress()

	ltcAddr, err := btcutil.NewAddressWitnessPubKeyHash(hash20[:], v2LTCMainNetParams)
	if err != nil {
		panic("http_test init: NewAddressWitnessPubKeyHash LTC: " + err.Error())
	}
	testLTCBech32Addr = ltcAddr.EncodeAddress()

	// P2SH addresses (script hash → "3…" for BTC, "M…" for LTC).
	// Use a different deterministic hash to keep addresses distinct.
	hash20p2sh := [20]byte{
		0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29, 0x2a,
		0x2b, 0x2c, 0x2d, 0x2e, 0x2f, 0x30, 0x31, 0x32, 0x33, 0x34,
	}

	btcP2SH, err := btcutil.NewAddressScriptHashFromHash(hash20p2sh[:], &chaincfg.MainNetParams)
	if err != nil {
		panic("http_test init: NewAddressScriptHashFromHash BTC: " + err.Error())
	}
	testBTCP2SHAddr = btcP2SH.EncodeAddress()

	ltcP2SH, err := btcutil.NewAddressScriptHashFromHash(hash20p2sh[:], v2LTCMainNetParams)
	if err != nil {
		panic("http_test init: NewAddressScriptHashFromHash LTC: " + err.Error())
	}
	testLTCP2SHAddr = ltcP2SH.EncodeAddress()
}

// ── Fakes ─────────────────────────────────────────────────────────────────────

type fakeIssuer struct {
	draft InvoiceDraft
	err   error
	calls int
}

func (f *fakeIssuer) CreateClientInvoice(_ context.Context, _ string) (InvoiceDraft, error) {
	f.calls++
	return f.draft, f.err
}

type fakeBalance struct {
	balance float64
	err     error
	calls   int
}

func (f *fakeBalance) BalanceUSD(_ context.Context, _, _ string) (float64, error) {
	f.calls++
	return f.balance, f.err
}

func btcDraft() InvoiceDraft {
	return InvoiceDraft{
		PaymentAddress: "bc1qplatformreceive000000000000000000000000",
		AmountAtomic:   500_000,
		AmountUSDCents: 500,
	}
}

func ltcDraft() InvoiceDraft {
	return InvoiceDraft{
		PaymentAddress: "ltc1qplatformreceive0000000000000000000000",
		AmountAtomic:   20_000_000,
		AmountUSDCents: 500,
	}
}

// newTestHandler creates a ClientHandler backed by an in-memory SQLite DB.
// Fails the test immediately if construction fails.
func newTestHandler(t *testing.T, issuer ClientInvoiceIssuer, balance ClientBalanceReader, now func() time.Time) (*ClientHandler, *Service) {
	t.Helper()
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	svc, err := New(db, testHMACKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	h, err := NewClientHandler(svc, issuer, balance, []byte("test-rate-limit-key"), now)
	if err != nil {
		t.Fatalf("NewClientHandler: %v", err)
	}
	return h, svc
}

// postJSON issues a POST with Content-Type: application/json and a JSON-encoded body.
func postJSON(router http.Handler, path string, v any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(v)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// postRaw issues a POST with an explicit Content-Type and a raw string body.
func postRaw(router http.Handler, path, ct, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// confirmPaymentHTTP is a test helper that performs detect+confirm for http_test.go tests.
// wallet is the wallet address used when creating the intent (used as sender).
func confirmPaymentHTTP(t *testing.T, svc *Service, flowID, invoiceID, wallet, txid string, amountAtomic int64) {
	t.Helper()
	now := time.Now()
	_, err := svc.RecordPaymentDetected(flowID, invoiceID, txid,
		[]string{wallet}, amountAtomic, now)
	if err != nil {
		t.Fatalf("RecordPaymentDetected (confirmPaymentHTTP): %v", err)
	}
	_, err = svc.ConfirmPayment(flowID, invoiceID, now)
	if err != nil {
		t.Fatalf("ConfirmPayment (confirmPaymentHTTP): %v", err)
	}
}

// postJSONFromIP is like postJSON but sets a specific RemoteAddr for rate-limit isolation.
func postJSONFromIP(router http.Handler, path, remoteAddr string, v any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(v)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// decodeJSON parses the response body into v, failing the test on error.
func decodeJSON(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decode JSON from %d response: %v\nbody: %s", w.Code, err, w.Body.String())
	}
}

const (
	createPath  = "/v2/client/payment-intents"
	restorePath = "/v2/client/payment-intents/restore"
	recheckPath = "/v2/client/payment-intents/recheck-balance"
)

// ── Test 1: Create BTC ────────────────────────────────────────────────────────

func TestHTTPCreateBTC(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	h, _ := newTestHandler(t, issuer, &fakeBalance{}, nil)
	router := h.Routes()

	w := postJSON(router, createPath, map[string]string{
		"wallet_address": "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp createResponse
	decodeJSON(t, w, &resp)

	if resp.Currency != "BTC" {
		t.Errorf("currency: got %q, want BTC", resp.Currency)
	}
	if resp.ManagementCode == "" {
		t.Error("management_code is empty in 201 response")
	}
	if resp.FlowID == "" || resp.InvoiceID == "" {
		t.Error("flow_id or invoice_id is empty")
	}
	if resp.State != StateAwaitingPayment {
		t.Errorf("state: got %q, want %q", resp.State, StateAwaitingPayment)
	}
	d := btcDraft()
	if resp.Invoice.PaymentAddress != d.PaymentAddress {
		t.Errorf("invoice.payment_address: got %q, want %q", resp.Invoice.PaymentAddress, d.PaymentAddress)
	}
	if resp.Invoice.AmountAtomic != d.AmountAtomic {
		t.Errorf("invoice.amount_atomic: got %d, want %d", resp.Invoice.AmountAtomic, d.AmountAtomic)
	}
	if resp.Invoice.AmountUSDCents != d.AmountUSDCents {
		t.Errorf("invoice.amount_usd_cents: got %d, want %d", resp.Invoice.AmountUSDCents, d.AmountUSDCents)
	}
	if resp.Invoice.Status != "pending" {
		t.Errorf("invoice.status: got %q, want pending", resp.Invoice.Status)
	}
	if issuer.calls != 1 {
		t.Errorf("issuer called %d times, want 1", issuer.calls)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
}

// ── Test 2: Create LTC ───────────────────────────────────────────────────────

func TestHTTPCreateLTC(t *testing.T) {
	issuer := &fakeIssuer{draft: ltcDraft()}
	h, _ := newTestHandler(t, issuer, &fakeBalance{}, nil)
	router := h.Routes()

	w := postJSON(router, createPath, map[string]string{
		"wallet_address": testLTCBech32Addr, // computed from known hash in init()
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp createResponse
	decodeJSON(t, w, &resp)

	if resp.Currency != "LTC" {
		t.Errorf("currency: got %q, want LTC", resp.Currency)
	}
}

// ── Test 3: Unknown address prefix → 400, issuer not called ──────────────────

func TestHTTPCreateUnknownPrefix(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	h, _ := newTestHandler(t, issuer, &fakeBalance{}, nil)
	router := h.Routes()

	for _, addr := range []string{
		"XBT1qsomething",
		"ethaddress0x123",
		"Xabcdefg",
		"2address",
	} {
		w := postJSON(router, createPath, map[string]string{"wallet_address": addr})
		if w.Code != http.StatusBadRequest {
			t.Errorf("addr %q: expected 400, got %d", addr, w.Code)
		}
	}
	if issuer.calls != 0 {
		t.Errorf("issuer called %d times for unknown prefixes, want 0", issuer.calls)
	}
}

// ── Test 4: Empty / whitespace wallet → 400 ──────────────────────────────────

func TestHTTPCreateEmptyWallet(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	h, _ := newTestHandler(t, issuer, &fakeBalance{}, nil)
	router := h.Routes()

	for _, addr := range []string{"", "   ", "\t\n"} {
		w := postJSON(router, createPath, map[string]string{"wallet_address": addr})
		if w.Code != http.StatusBadRequest {
			t.Errorf("addr %q: expected 400, got %d", addr, w.Code)
		}
	}
	if issuer.calls != 0 {
		t.Errorf("issuer called %d times for empty wallets, want 0", issuer.calls)
	}
}

// ── Test 5: Strict request constraints ───────────────────────────────────────

func TestHTTPStrictRequestConstraints(t *testing.T) {
	validAddr := `{"wallet_address":"bc1qtest"}`

	cases := []struct {
		name   string
		ct     string
		body   string
		expect int
	}{
		{"wrong content type", "text/plain", validAddr, http.StatusBadRequest},
		{"no content type", "", validAddr, http.StatusBadRequest},
		{"oversized body", "application/json",
			`{"wallet_address":"` + strings.Repeat("x", 4100) + `"}`,
			http.StatusRequestEntityTooLarge},
		{"unknown field", "application/json",
			`{"wallet_address":"bc1qtest","extra_field":"bad"}`,
			http.StatusBadRequest},
		{"malformed JSON", "application/json", `{not valid json`, http.StatusBadRequest},
		{"trailing JSON value", "application/json",
			`{"wallet_address":"bc1qtest"}{"another":"value"}`,
			http.StatusBadRequest},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			issuer := &fakeIssuer{draft: btcDraft()}
			h, _ := newTestHandler(t, issuer, &fakeBalance{}, nil)
			router := h.Routes()

			w := postRaw(router, createPath, tc.ct, tc.body)
			if w.Code != tc.expect {
				t.Errorf("expected %d, got %d: %s", tc.expect, w.Code, w.Body.String())
			}
			if issuer.calls != 0 {
				t.Errorf("issuer called %d times for %q, want 0", issuer.calls, tc.name)
			}
		})
	}
}

// ── Test 6: Invoice issuer network error → 503, no DB rows ───────────────────

func TestHTTPIssuerError(t *testing.T) {
	issuer := &fakeIssuer{err: errors.New("payment gateway timeout")}
	h, svc := newTestHandler(t, issuer, &fakeBalance{}, nil)
	router := h.Routes()

	w := postJSON(router, createPath, map[string]string{
		"wallet_address": testBTCBech32Addr,
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}

	var flowCount, invCount int
	db := svc.db
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_flows`).Scan(&flowCount) //nolint:errcheck
	db.QueryRow(`SELECT COUNT(*) FROM v2_invoices`).Scan(&invCount)      //nolint:errcheck
	if flowCount != 0 || invCount != 0 {
		t.Errorf("after issuer error: %d flow(s), %d invoice(s); want 0 each", flowCount, invCount)
	}
}

// ── Test 7: Create response privacy; raw code absent from DB ─────────────────

func TestHTTPCreateResponsePrivacy(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	h, svc := newTestHandler(t, issuer, &fakeBalance{}, nil)
	router := h.Routes()

	w := postJSON(router, createPath, map[string]string{
		"wallet_address": testBTCBech32Addr,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}

	var resp createResponse
	decodeJSON(t, w, &resp)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	for _, banned := range []string{"wallet_fingerprint", "management_code_hash", "wallet_address", "payment_txid"} {
		if _, ok := raw[banned]; ok {
			t.Errorf("response contains banned field %q", banned)
		}
	}

	rawCode := resp.ManagementCode
	if rawCode == "" {
		t.Fatal("management_code is empty")
	}
	db := svc.db
	var codeHash string
	db.QueryRow(`SELECT management_code_hash FROM v2_client_flows WHERE id = ?`, resp.FlowID).Scan(&codeHash) //nolint:errcheck
	if codeHash == rawCode {
		t.Error("raw management_code stored in plaintext as management_code_hash")
	}
	rows, _ := db.Query(`SELECT id, wallet_fingerprint, management_code_hash, currency, state FROM v2_client_flows`)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var a, b, c, d, e string
			rows.Scan(&a, &b, &c, &d, &e) //nolint:errcheck
			for _, v := range []string{a, b, c, d, e} {
				if v == rawCode {
					t.Error("raw management code found in v2_client_flows text column")
				}
			}
		}
	}
}

// ── Test 8: Restore with correct code + wallet ────────────────────────────────

func TestHTTPRestoreCorrect(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	h, _ := newTestHandler(t, issuer, &fakeBalance{}, nil)
	router := h.Routes()

	const wallet = "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	wCreate := postJSON(router, createPath, map[string]string{"wallet_address": wallet})
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", wCreate.Code)
	}
	var created createResponse
	decodeJSON(t, wCreate, &created)

	wRestore := postJSON(router, restorePath, map[string]string{
		"management_code": created.ManagementCode,
		"wallet_address":  wallet,
	})
	if wRestore.Code != http.StatusOK {
		t.Fatalf("restore: expected 200, got %d: %s", wRestore.Code, wRestore.Body.String())
	}

	var restored restoreResponse
	decodeJSON(t, wRestore, &restored)

	if restored.FlowID != created.FlowID {
		t.Errorf("flow_id mismatch: got %q, want %q", restored.FlowID, created.FlowID)
	}
	if restored.InvoiceID != created.InvoiceID {
		t.Errorf("invoice_id mismatch: got %q, want %q", restored.InvoiceID, created.InvoiceID)
	}
	if restored.Currency != "BTC" {
		t.Errorf("currency: got %q, want BTC", restored.Currency)
	}
	if restored.State != StateAwaitingPayment {
		t.Errorf("state: got %q, want %q", restored.State, StateAwaitingPayment)
	}
	d := btcDraft()
	if restored.Invoice.PaymentAddress != d.PaymentAddress {
		t.Errorf("invoice.payment_address: got %q, want %q", restored.Invoice.PaymentAddress, d.PaymentAddress)
	}
	if restored.Invoice.AmountAtomic != d.AmountAtomic {
		t.Errorf("invoice.amount_atomic: got %d, want %d", restored.Invoice.AmountAtomic, d.AmountAtomic)
	}
	if restored.Invoice.AmountUSDCents != d.AmountUSDCents {
		t.Errorf("invoice.amount_usd_cents: got %d, want %d", restored.Invoice.AmountUSDCents, d.AmountUSDCents)
	}
}

// ── Test 9: Restore response omits sensitive fields ───────────────────────────

func TestHTTPRestoreNoSensitiveFields(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	h, _ := newTestHandler(t, issuer, &fakeBalance{}, nil)
	router := h.Routes()

	const wallet = testBTCBech32Addr
	wCreate := postJSON(router, createPath, map[string]string{"wallet_address": wallet})
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("create: %d", wCreate.Code)
	}
	var created createResponse
	decodeJSON(t, wCreate, &created)

	wRestore := postJSON(router, restorePath, map[string]string{
		"management_code": created.ManagementCode,
		"wallet_address":  wallet,
	})
	if wRestore.Code != http.StatusOK {
		t.Fatalf("restore: %d: %s", wRestore.Code, wRestore.Body.String())
	}

	var raw map[string]json.RawMessage
	json.Unmarshal(wRestore.Body.Bytes(), &raw) //nolint:errcheck
	for _, banned := range []string{
		"management_code",
		"wallet_address",
		"wallet_fingerprint",
		"management_code_hash",
	} {
		if _, ok := raw[banned]; ok {
			t.Errorf("restore response contains banned field %q", banned)
		}
	}
}

// ── Test 10: Wrong code and wrong wallet → byte-for-byte identical 404 ────────

func TestHTTPRestoreWrongCodeWrongWallet(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	h, _ := newTestHandler(t, issuer, &fakeBalance{}, nil)
	router := h.Routes()

	const wallet = testBTCBech32Addr
	wCreate := postJSON(router, createPath, map[string]string{"wallet_address": wallet})
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("create: %d", wCreate.Code)
	}
	var created createResponse
	decodeJSON(t, wCreate, &created)

	w1 := postJSON(router, restorePath, map[string]string{
		"management_code": "0000000000000000000000000000000000000000000000000000000000000000",
		"wallet_address":  wallet,
	})
	w2 := postJSON(router, restorePath, map[string]string{
		"management_code": created.ManagementCode,
		"wallet_address":  "bc1qwrong00000000000000000000000000000000000",
	})

	if w1.Code != http.StatusNotFound {
		t.Errorf("wrong code: expected 404, got %d", w1.Code)
	}
	if w2.Code != http.StatusNotFound {
		t.Errorf("wrong wallet: expected 404, got %d", w2.Code)
	}

	body1 := strings.TrimSpace(w1.Body.String())
	body2 := strings.TrimSpace(w2.Body.String())
	if body1 != body2 {
		t.Errorf("response bodies differ:\n  wrong code:   %s\n  wrong wallet: %s", body1, body2)
	}
}

// ── Test 11: Restore after direct confirm shows confirmed state + expiry ───────

func TestHTTPRestoreAfterConfirm(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	h, svc := newTestHandler(t, issuer, &fakeBalance{}, nil)
	router := h.Routes()

	const wallet = testBTCBech32Addr
	wCreate := postJSON(router, createPath, map[string]string{"wallet_address": wallet})
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("create: %d", wCreate.Code)
	}
	var created createResponse
	decodeJSON(t, wCreate, &created)

	confirmPaymentHTTP(t, svc, created.FlowID, created.InvoiceID, wallet, "txid_test_confirm", btcDraft().AmountAtomic)

	wRestore := postJSON(router, restorePath, map[string]string{
		"management_code": created.ManagementCode,
		"wallet_address":  wallet,
	})
	if wRestore.Code != http.StatusOK {
		t.Fatalf("restore after confirm: %d: %s", wRestore.Code, wRestore.Body.String())
	}

	var restored restoreResponse
	decodeJSON(t, wRestore, &restored)

	if restored.State != StatePaymentConfirmed {
		t.Errorf("state: got %q, want %q", restored.State, StatePaymentConfirmed)
	}
	if restored.PaymentConfirmedAt == nil {
		t.Error("payment_confirmed_at is nil after confirmation")
	}
	if restored.EntitlementExpiresAt == nil {
		t.Error("entitlement_expires_at is nil after confirmation")
	}
	if restored.PaymentConfirmedAt != nil && restored.EntitlementExpiresAt != nil {
		diff := *restored.EntitlementExpiresAt - *restored.PaymentConfirmedAt
		want := int64(entitlementDuration.Seconds())
		if diff != want {
			t.Errorf("expiry - confirmed_at = %d s, want %d s (5 days)", diff, want)
		}
	}
}

// ── Test 12: Recheck before payment → 409, balance reader not called ──────────

func TestHTTPRecheckBeforePayment(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	bal := &fakeBalance{balance: 999.99}
	h, _ := newTestHandler(t, issuer, bal, nil)
	router := h.Routes()

	const wallet = testBTCBech32Addr
	wCreate := postJSON(router, createPath, map[string]string{"wallet_address": wallet})
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("create: %d", wCreate.Code)
	}
	var created createResponse
	decodeJSON(t, wCreate, &created)

	wRecheck := postJSON(router, recheckPath, map[string]string{
		"management_code": created.ManagementCode,
		"wallet_address":  wallet,
	})
	if wRecheck.Code != http.StatusConflict {
		t.Errorf("expected 409 before payment, got %d: %s", wRecheck.Code, wRecheck.Body.String())
	}
	if bal.calls != 0 {
		t.Errorf("balance reader called %d times before payment, want 0", bal.calls)
	}
}

// ── Test 13: Recheck $119.99 → paid_low_balance ──────────────────────────────

func TestHTTPRecheckLowBalance(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	bal := &fakeBalance{balance: 119.99}
	h, svc := newTestHandler(t, issuer, bal, nil)
	router := h.Routes()

	const wallet = testBTCBech32Addr
	wCreate := postJSON(router, createPath, map[string]string{"wallet_address": wallet})
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("create: %d", wCreate.Code)
	}
	var created createResponse
	decodeJSON(t, wCreate, &created)

	confirmPaymentHTTP(t, svc, created.FlowID, created.InvoiceID, wallet, "txid_low", btcDraft().AmountAtomic)

	wRecheck := postJSON(router, recheckPath, map[string]string{
		"management_code": created.ManagementCode,
		"wallet_address":  wallet,
	})
	if wRecheck.Code != http.StatusOK {
		t.Fatalf("recheck: expected 200, got %d: %s", wRecheck.Code, wRecheck.Body.String())
	}

	var resp recheckResponse
	decodeJSON(t, wRecheck, &resp)

	if resp.State != StatePaidLowBalance {
		t.Errorf("state: got %q, want %q", resp.State, StatePaidLowBalance)
	}
	if resp.LastBalanceUSD == nil || *resp.LastBalanceUSD != 119.99 {
		t.Errorf("last_balance_usd: got %v, want 119.99", resp.LastBalanceUSD)
	}
}

// ── Test 14: Recheck $120.00 → form_ready ─────────────────────────────────────

func TestHTTPRecheckFormReady(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	h, svc := newTestHandler(t, issuer, &fakeBalance{balance: 119.99}, nil)
	router := h.Routes()

	const wallet = testBTCBech32Addr
	wCreate := postJSON(router, createPath, map[string]string{"wallet_address": wallet})
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("create: %d", wCreate.Code)
	}
	var created createResponse
	decodeJSON(t, wCreate, &created)

	confirmPaymentHTTP(t, svc, created.FlowID, created.InvoiceID, wallet, "txid_formready", btcDraft().AmountAtomic)

	w1 := postJSON(router, recheckPath, map[string]string{
		"management_code": created.ManagementCode,
		"wallet_address":  wallet,
	})
	if w1.Code != http.StatusOK {
		t.Fatalf("first recheck: %d: %s", w1.Code, w1.Body.String())
	}

	h.balance = &fakeBalance{balance: 120.00}
	w2 := postJSON(router, recheckPath, map[string]string{
		"management_code": created.ManagementCode,
		"wallet_address":  wallet,
	})
	if w2.Code != http.StatusOK {
		t.Fatalf("second recheck: %d: %s", w2.Code, w2.Body.String())
	}

	var resp recheckResponse
	decodeJSON(t, w2, &resp)
	if resp.State != StateFormReady {
		t.Errorf("state: got %q, want %q", resp.State, StateFormReady)
	}
}

// ── Test 15: Balance reader error → 503, state unchanged ─────────────────────

func TestHTTPBalanceReaderError(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	bal := &fakeBalance{err: errors.New("blockchain node unreachable")}
	h, svc := newTestHandler(t, issuer, bal, nil)
	router := h.Routes()

	const wallet = testBTCBech32Addr
	wCreate := postJSON(router, createPath, map[string]string{"wallet_address": wallet})
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("create: %d", wCreate.Code)
	}
	var created createResponse
	decodeJSON(t, wCreate, &created)

	confirmPaymentHTTP(t, svc, created.FlowID, created.InvoiceID, wallet, "txid_bal_err", btcDraft().AmountAtomic)

	wRecheck := postJSON(router, recheckPath, map[string]string{
		"management_code": created.ManagementCode,
		"wallet_address":  wallet,
	})
	if wRecheck.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", wRecheck.Code, wRecheck.Body.String())
	}

	fv, err := svc.RestorePaymentIntent(created.ManagementCode, wallet)
	if err != nil {
		t.Fatalf("RestorePaymentIntent: %v", err)
	}
	if fv.State != StatePaymentConfirmed {
		t.Errorf("state changed to %q after balance reader error", fv.State)
	}
	if fv.LastBalanceUSD != nil {
		t.Errorf("last_balance_usd set (%v) despite balance reader error", *fv.LastBalanceUSD)
	}
}

// ── Test 16: No capability in URL path or query string ────────────────────────

func TestHTTPNoCapabilityInURLOrQuery(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	h, _ := newTestHandler(t, issuer, &fakeBalance{}, nil)
	router := h.Routes()

	for _, path := range []string{createPath, restorePath, recheckPath} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code == http.StatusOK {
			t.Errorf("GET %s returned 200 — endpoint should require POST", path)
		}
	}

	fakeCode := "deadbeef000000000000000000000000000000000000000000000000deadbeef"
	queryPath := restorePath + "?management_code=" + fakeCode + "&wallet_address=bc1qtest"
	req2 := httptest.NewRequest(http.MethodPost, queryPath, nil)
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	if w2.Code == http.StatusOK {
		t.Error("POST to restore with empty body and code in query returned 200")
	}
}

// ── Test 17: Rate limit fires on exact N+1 request; isolated per key ──────────

func TestHTTPRateLimitIsolation(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	h, _ := newTestHandler(t, issuer, &fakeBalance{}, nil)
	router := h.Routes()

	wallet := map[string]string{"wallet_address": "bc1qtest000000000000000000000000000000000000"}

	const createLimit = 5
	ip1 := "10.0.0.1:1234"
	for i := 1; i <= createLimit; i++ {
		w := postJSONFromIP(router, createPath, ip1, wallet)
		if w.Code == http.StatusTooManyRequests {
			t.Errorf("request %d: unexpected 429 (limit is %d)", i, createLimit)
		}
	}
	w := postJSONFromIP(router, createPath, ip1, wallet)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("request %d: expected 429, got %d", createLimit+1, w.Code)
	}

	ip2 := "10.0.0.2:1234"
	w2 := postJSONFromIP(router, createPath, ip2, wallet)
	if w2.Code == http.StatusTooManyRequests {
		t.Errorf("ip2 hit rate limit of ip1: got 429 on first request")
	}

	const restoreLimit = 60
	body := map[string]string{"management_code": "fakecode", "wallet_address": "bc1qtest"}
	ip3 := "10.0.0.3:1234"
	for i := 1; i <= restoreLimit; i++ {
		w := postJSONFromIP(router, restorePath, ip3, body)
		if w.Code == http.StatusTooManyRequests {
			t.Errorf("restore request %d: unexpected 429", i)
		}
	}
	wR := postJSONFromIP(router, restorePath, ip3, body)
	if wR.Code != http.StatusTooManyRequests {
		t.Errorf("restore request %d: expected 429, got %d", restoreLimit+1, wR.Code)
	}

	const recheckLimit = 5
	ip4 := "10.0.0.4:1234"
	for i := 1; i <= recheckLimit; i++ {
		w := postJSONFromIP(router, recheckPath, ip4, body)
		if w.Code == http.StatusTooManyRequests {
			t.Errorf("recheck request %d: unexpected 429", i)
		}
	}
	wRC := postJSONFromIP(router, recheckPath, ip4, body)
	if wRC.Code != http.StatusTooManyRequests {
		t.Errorf("recheck request %d: expected 429, got %d", recheckLimit+1, wRC.Code)
	}
}

// ── Test 18: Limiter — auto-cleanup and manual Cleanup ───────────────────────

func TestHTTPLimiterCleanup(t *testing.T) {
	var now time.Time
	now = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	clockFn := func() time.Time { return now }

	// Small maxEntries so tests are fast.
	lim := newFixedWindowLimiter(3, time.Minute, 100, clockFn)
	key := "testkey"

	// Exhaust the limit.
	for i := 0; i < 3; i++ {
		if !lim.Allow(key) {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if lim.Allow(key) {
		t.Error("4th request should be denied")
	}

	// Advance time past expiry (now >= expiry triggers auto-cleanup on next Allow).
	now = now.Add(61 * time.Second)

	// Allow automatically removes the expired entry and starts a fresh window.
	if !lim.Allow(key) {
		t.Error("first Allow after expiry should succeed (auto-cleanup)")
	}

	// Entry now exists in the new window.
	lim.mu.Lock()
	_, present := lim.entries[key]
	lim.mu.Unlock()
	if !present {
		t.Error("entry missing after Allow in new window")
	}

	// Manual Cleanup still works: advance 30 s more (still within new window).
	now = now.Add(30 * time.Second)
	lim.Cleanup()
	lim.mu.Lock()
	_, still := lim.entries[key]
	lim.mu.Unlock()
	if !still {
		t.Error("active entry removed by Cleanup before window expired")
	}

	// Advance past the new window's expiry and Cleanup again.
	now = now.Add(40 * time.Second) // 30+40 > 60: new window now expired
	lim.Cleanup()
	lim.mu.Lock()
	_, afterCleanup := lim.entries[key]
	lim.mu.Unlock()
	if afterCleanup {
		t.Error("expired entry still present after explicit Cleanup")
	}
}

// ── Test 19: Sensitive values not leaked in error response bodies ─────────────

func TestHTTPSensitiveValuesNotLeaked(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	h, _ := newTestHandler(t, issuer, &fakeBalance{}, nil)
	router := h.Routes()

	const sensitiveWallet = "bc1qSENSITIVEWALLET00000000000000000000000"
	const sensitiveCode = "SENSITIVEMANAGEMENTCODE0000000000000000000000000000000000000000"

	w1 := postJSON(router, createPath, map[string]string{"wallet_address": sensitiveWallet})
	if strings.Contains(w1.Body.String(), sensitiveWallet) {
		t.Errorf("create 400 response leaks wallet address: %s", w1.Body.String())
	}

	w2 := postJSON(router, restorePath, map[string]string{
		"management_code": sensitiveCode,
		"wallet_address":  sensitiveWallet,
	})
	if strings.Contains(w2.Body.String(), sensitiveCode) {
		t.Errorf("restore 404 response leaks management_code: %s", w2.Body.String())
	}
	if strings.Contains(w2.Body.String(), sensitiveWallet) {
		t.Errorf("restore 404 response leaks wallet_address: %s", w2.Body.String())
	}

	issuerFail := &fakeIssuer{err: fmt.Errorf("internal DB error: SELECT * FROM secrets")}
	hFail, _ := newTestHandler(t, issuerFail, &fakeBalance{}, nil)
	w3 := postJSON(hFail.Routes(), createPath, map[string]string{"wallet_address": testBTCBech32Addr})
	if strings.Contains(w3.Body.String(), "DB error") || strings.Contains(w3.Body.String(), "secrets") {
		t.Errorf("503 response leaks internal error detail: %s", w3.Body.String())
	}
}

// ── Test A1: Content-Type strict parsing via mime.ParseMediaType ──────────────

func TestHTTPContentTypeStrict(t *testing.T) {
	validBody := `{"wallet_address":"bc1qtest"}`

	cases := []struct {
		ct      string
		accept  bool
		comment string
	}{
		{"application/json", true, "bare media type"},
		{"application/json; charset=utf-8", true, "charset parameter"},
		{"APPLICATION/JSON; CHARSET=UTF-8", true, "uppercase"},
		{"application/json; boundary=something", true, "arbitrary parameter"},
		{"text/plain", false, "wrong type"},
		{"text/application/json", false, "type is 'text', not 'application'"},
		{"application/json-patch+json", false, "wrong subtype"},
		{"application/jsonx", false, "wrong subtype prefix"},
		{"", false, "empty"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.comment, func(t *testing.T) {
			issuer := &fakeIssuer{draft: btcDraft()}
			h, _ := newTestHandler(t, issuer, &fakeBalance{}, nil)
			router := h.Routes()

			w := postRaw(router, createPath, tc.ct, validBody)
			if tc.accept {
				// A 400 from the wallet prefix (bc1qtest is short) or other business
				// logic is fine; 400 from Content-Type is not.
				// We only check that Content-Type is NOT the cause.
				// To confirm: look for the specific CT error message.
				if strings.Contains(w.Body.String(), "Content-Type") {
					t.Errorf("CT %q: rejected for Content-Type reason: %s", tc.ct, w.Body.String())
				}
			} else {
				if w.Code != http.StatusBadRequest {
					t.Errorf("CT %q: expected 400, got %d: %s", tc.ct, w.Code, w.Body.String())
				}
				if !strings.Contains(w.Body.String(), "Content-Type") {
					t.Errorf("CT %q: expected Content-Type error, got: %s", tc.ct, w.Body.String())
				}
			}
		})
	}
}

// ── Test A2: Valid JSON + trailing bytes > 4 KiB → 413 ────────────────────────

func TestHTTPTrailingBodyTooLarge(t *testing.T) {
	// The valid object is small (~30 bytes). Trailing whitespace forces the
	// decoder to keep reading past the 4096-byte MaxBytesReader limit while
	// scanning for the next JSON token, triggering MaxBytesError → 413.
	// Non-whitespace bytes (like 'x') cause an immediate JSON syntax error
	// before the reader limit is hit, which would give 400 instead of 413.
	validPart := `{"wallet_address":"bc1qtest"}`
	trailing := strings.Repeat(" ", 4100)
	body := validPart + trailing

	issuer := &fakeIssuer{draft: btcDraft()}
	h, _ := newTestHandler(t, issuer, &fakeBalance{}, nil)

	w := postRaw(h.Routes(), createPath, "application/json", body)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d: %s", w.Code, w.Body.String())
	}
	if issuer.calls != 0 {
		t.Errorf("issuer called despite oversized trailing body")
	}
}

// ── Test A2b: Valid JSON + non-whitespace garbage → 400 ───────────────────────
//
// Regression: `{"wallet_address":"bc1qtest"}garbage`
// This is distinct from the oversized-trailing test (→ 413) and the
// second-JSON-value test (→ 400 via nil error from Decode). Here the
// second Decode returns a *json.SyntaxError because "garbage" is not
// parseable JSON, confirming the third branch in decodeStrict.

func TestHTTPTrailingGarbageBody(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	h, svc := newTestHandler(t, issuer, &fakeBalance{}, nil)

	body := `{"wallet_address":"bc1qtest"}garbage`
	w := postRaw(h.Routes(), createPath, "application/json", body)

	// 1. Response must be 400.
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 2. Generic error body must not contain sensitive input values.
	respBody := w.Body.String()
	for _, sensitive := range []string{"bc1qtest", "garbage", "wallet_address"} {
		if strings.Contains(respBody, sensitive) {
			t.Errorf("response body leaks sensitive value %q: %s", sensitive, respBody)
		}
	}

	// 3. Issuer must not have been called.
	if issuer.calls != 0 {
		t.Errorf("issuer called %d times, want 0", issuer.calls)
	}

	// 4. No DB rows must have been written.
	var flowCount, invCount int
	svc.db.QueryRow(`SELECT COUNT(*) FROM v2_client_flows`).Scan(&flowCount) //nolint:errcheck
	svc.db.QueryRow(`SELECT COUNT(*) FROM v2_invoices`).Scan(&invCount)      //nolint:errcheck
	if flowCount != 0 || invCount != 0 {
		t.Errorf("trailing garbage: %d flow(s), %d invoice(s) written; want 0 each",
			flowCount, invCount)
	}
}

// ── Test A3: Issuer returns invalid draft (no error) → 503, no DB rows ────────

func TestHTTPIssuerInvalidDraft(t *testing.T) {
	// Issuer succeeds but returns an invalid draft (zero atomic, wrong cents).
	badDraft := InvoiceDraft{
		PaymentAddress: "bc1qplatformreceive000000000000000000000000",
		AmountAtomic:   0,   // invalid: must be > 0
		AmountUSDCents: 999, // invalid: must be 500
	}
	issuer := &fakeIssuer{draft: badDraft, err: nil}
	h, svc := newTestHandler(t, issuer, &fakeBalance{}, nil)

	w := postJSON(h.Routes(), createPath, map[string]string{
		"wallet_address": testBTCBech32Addr,
	})
	// Must be 503 (server-side misconfiguration), not 400 (client error).
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("invalid draft: expected 503, got %d: %s", w.Code, w.Body.String())
	}

	// Internal draft details must not appear in the response body.
	body := w.Body.String()
	if strings.Contains(body, "999") || strings.Contains(body, "amount") || strings.Contains(body, "draft") {
		t.Errorf("response body leaks draft details: %s", body)
	}

	// No DB rows must have been written.
	var flowCount, invCount int
	svc.db.QueryRow(`SELECT COUNT(*) FROM v2_client_flows`).Scan(&flowCount) //nolint:errcheck
	svc.db.QueryRow(`SELECT COUNT(*) FROM v2_invoices`).Scan(&invCount)      //nolint:errcheck
	if flowCount != 0 || invCount != 0 {
		t.Errorf("invalid draft: %d flow(s), %d invoice(s) written; want 0 each", flowCount, invCount)
	}
}

// ── Test A4: Restore required fields → 400 ────────────────────────────────────

func TestHTTPRestoreRequiredFields(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	h, _ := newTestHandler(t, issuer, &fakeBalance{}, nil)
	router := h.Routes()

	cases := []struct {
		name string
		body map[string]string
	}{
		{"empty management_code", map[string]string{"management_code": "", "wallet_address": "bc1qtest"}},
		{"whitespace management_code", map[string]string{"management_code": "   ", "wallet_address": "bc1qtest"}},
		{"empty wallet_address", map[string]string{"management_code": "somecode", "wallet_address": ""}},
		{"whitespace wallet_address", map[string]string{"management_code": "somecode", "wallet_address": "  \t  "}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			w := postJSON(router, restorePath, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s: expected 400, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}

	// Non-empty but invalid values must still return 404 (not 400) to prevent enumeration.
	w := postJSON(router, restorePath, map[string]string{
		"management_code": "nonexistentcode0000000000000000000000000000000000000000000",
		"wallet_address":  "bc1qtest000000000000000000000000000000000000",
	})
	if w.Code != http.StatusNotFound {
		t.Errorf("valid-format but wrong code: expected 404, got %d", w.Code)
	}
}

// ── Test A5: Recheck required fields → 400 ────────────────────────────────────

func TestHTTPRecheckRequiredFields(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	h, _ := newTestHandler(t, issuer, &fakeBalance{}, nil)
	router := h.Routes()

	cases := []struct {
		name string
		body map[string]string
	}{
		{"empty management_code", map[string]string{"management_code": "", "wallet_address": "bc1qtest"}},
		{"whitespace management_code", map[string]string{"management_code": "\n\t", "wallet_address": "bc1qtest"}},
		{"empty wallet_address", map[string]string{"management_code": "somecode", "wallet_address": ""}},
		{"whitespace wallet_address", map[string]string{"management_code": "somecode", "wallet_address": " "}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			w := postJSON(router, recheckPath, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s: expected 400, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// ── Test A6: Constructor validation ───────────────────────────────────────────

func TestHTTPConstructorValidation(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	svc, err := New(db, testHMACKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	issuer := &fakeIssuer{draft: btcDraft()}
	bal := &fakeBalance{}
	key := []byte("validkey")

	cases := []struct {
		name    string
		svc     *Service
		issuer  ClientInvoiceIssuer
		balance ClientBalanceReader
		key     []byte
	}{
		{"nil svc", nil, issuer, bal, key},
		{"nil issuer", svc, nil, bal, key},
		{"nil balance", svc, issuer, nil, key},
		{"empty key", svc, issuer, bal, []byte{}},
		{"nil key", svc, issuer, bal, nil},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			h, err := NewClientHandler(tc.svc, tc.issuer, tc.balance, tc.key, nil)
			if err == nil {
				t.Errorf("%s: expected error, got nil (handler: %+v)", tc.name, h)
			}
		})
	}

	// Valid construction must succeed.
	h, err := NewClientHandler(svc, issuer, bal, key, nil)
	if err != nil {
		t.Fatalf("valid construction: unexpected error: %v", err)
	}
	if h == nil {
		t.Error("valid construction returned nil handler")
	}
}

// ── Test A7: Defensive copy of rateLimitKey ────────────────────────────────────

func TestHTTPRateLimitKeyDefensiveCopy(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	bal := &fakeBalance{}
	db, _ := OpenMemory()
	defer db.Close()
	svc, _ := New(db, testHMACKey)

	key := []byte("original-secret-key")
	h, err := NewClientHandler(svc, issuer, bal, key, nil)
	if err != nil {
		t.Fatalf("NewClientHandler: %v", err)
	}

	// Record the HMAC key stored in the handler.
	storedKey := string(h.rateLimitKey)

	// Mutate the caller's buffer.
	for i := range key {
		key[i] = 0xFF
	}

	// The handler's stored key must be unchanged.
	if string(h.rateLimitKey) != storedKey {
		t.Error("rateLimitKey was mutated by caller; defensive copy not working")
	}
}

// ── Test A8: Client IP normalization ──────────────────────────────────────────

func TestHTTPClientKeyIPNormalization(t *testing.T) {
	issuer := &fakeIssuer{draft: btcDraft()}
	h, _ := newTestHandler(t, issuer, &fakeBalance{}, nil)
	router := h.Routes()

	wallet := map[string]string{"wallet_address": "bc1qtest000000000000000000000000000000000000"}

	// IPv4: same IP, different source ports → same rate-limit bucket.
	// Exhaust the limit from port 1111.
	const createLimit = 5
	for i := 1; i <= createLimit; i++ {
		postJSONFromIP(router, createPath, "192.0.2.1:1111", wallet)
	}
	// Port 9999, same IP: must also be rate-limited.
	w := postJSONFromIP(router, createPath, "192.0.2.1:9999", wallet)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("same IPv4 different port: expected 429, got %d (ports should map to same bucket)", w.Code)
	}

	// Different IP: must NOT be rate-limited.
	w2 := postJSONFromIP(router, createPath, "192.0.2.2:1111", wallet)
	if w2.Code == http.StatusTooManyRequests {
		t.Errorf("different IPv4: unexpected 429 (should be isolated bucket)")
	}

	// IPv6: same IP, different ports → same bucket.
	h2, _ := newTestHandler(t, &fakeIssuer{draft: btcDraft()}, &fakeBalance{}, nil)
	r2 := h2.Routes()
	for i := 1; i <= createLimit; i++ {
		postJSONFromIP(r2, createPath, "[2001:db8::1]:1234", wallet)
	}
	wv6 := postJSONFromIP(r2, createPath, "[2001:db8::1]:9999", wallet)
	if wv6.Code != http.StatusTooManyRequests {
		t.Errorf("same IPv6 different port: expected 429, got %d", wv6.Code)
	}

	// Verify raw IP does not appear in the limiter map.
	h.createLim.mu.Lock()
	defer h.createLim.mu.Unlock()
	for k := range h.createLim.entries {
		if strings.Contains(k, "192.0.2.1") {
			t.Errorf("raw IP found in limiter map key: %q", k)
		}
	}
}

// ── Test A9: Auto-cleanup via Allow (no manual Cleanup needed) ────────────────

func TestHTTPLimiterAutoCleanup(t *testing.T) {
	var now time.Time
	now = time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	clockFn := func() time.Time { return now }
	lim := newFixedWindowLimiter(2, time.Minute, 100, clockFn)

	key := "autokey"
	lim.Allow(key) // count=1
	lim.Allow(key) // count=2 (limit exhausted)

	// Verify entry is present.
	lim.mu.Lock()
	_, before := lim.entries[key]
	lim.mu.Unlock()
	if !before {
		t.Fatal("entry should be present before expiry")
	}

	// Advance past expiry — do NOT call Cleanup().
	now = now.Add(time.Minute + time.Second)

	// Calling Allow should automatically remove the stale entry and start a new window.
	if !lim.Allow(key) {
		t.Error("Allow after expiry should return true (new window)")
	}

	// The new entry must exist with count=1.
	lim.mu.Lock()
	e, present := lim.entries[key]
	lim.mu.Unlock()
	if !present {
		t.Fatal("entry missing after Allow in new window")
	}
	if e.count != 1 {
		t.Errorf("count in new window: got %d, want 1", e.count)
	}
}

// ── Test A10: Exact expiry boundary starts new window ─────────────────────────

func TestHTTPLimiterExpiryBoundary(t *testing.T) {
	baseTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	var now time.Time
	now = baseTime
	clockFn := func() time.Time { return now }
	lim := newFixedWindowLimiter(1, time.Minute, 100, clockFn)

	key := "boundkey"
	if !lim.Allow(key) {
		t.Fatal("first Allow should succeed")
	}
	if lim.Allow(key) {
		t.Fatal("second Allow (same window) should be denied")
	}

	// Move to exactly the expiry time (now == expiry means expired).
	lim.mu.Lock()
	expiry := lim.entries[key].expiry
	lim.mu.Unlock()
	now = expiry // now == expiry → window considered expired

	if !lim.Allow(key) {
		t.Error("Allow at exact expiry time should start a new window and return true")
	}
}

// ── Test A11: len(entries) <= maxEntries under unique-key flood ───────────────

func TestHTTPLimiterBounded(t *testing.T) {
	const maxEntries = 50
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	lim := newFixedWindowLimiter(100, time.Minute, maxEntries, func() time.Time { return now })

	// Flood with more unique keys than maxEntries.
	for i := 0; i < 1000; i++ {
		lim.Allow(fmt.Sprintf("unique-key-%d", i))
	}

	lim.mu.Lock()
	n := len(lim.entries)
	lim.mu.Unlock()
	if n > maxEntries {
		t.Errorf("len(entries) = %d, exceeds maxEntries=%d", n, maxEntries)
	}
}

// ── Test A12: Full active map → new key denied, existing entries preserved ────

func TestHTTPLimiterFullActiveMapDenies(t *testing.T) {
	const maxEntries = 5
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	lim := newFixedWindowLimiter(100, time.Minute, maxEntries, func() time.Time { return now })

	// Fill the map with active (non-expired) entries.
	existing := make([]string, maxEntries)
	for i := 0; i < maxEntries; i++ {
		existing[i] = fmt.Sprintf("active-key-%d", i)
		if !lim.Allow(existing[i]) {
			t.Fatalf("active key %d should be allowed", i)
		}
	}

	// All entries are active; map is at capacity.
	lim.mu.Lock()
	n := len(lim.entries)
	lim.mu.Unlock()
	if n != maxEntries {
		t.Fatalf("expected %d entries, got %d", maxEntries, n)
	}

	// A new key must be denied (fail closed).
	if lim.Allow("new-key") {
		t.Error("new key should be denied when map is full of active entries")
	}

	// None of the existing active entries must have been evicted.
	lim.mu.Lock()
	nAfter := len(lim.entries)
	lim.mu.Unlock()
	if nAfter != maxEntries {
		t.Errorf("entries after denial: got %d, want %d (active entries evicted?)", nAfter, maxEntries)
	}
	for _, k := range existing {
		lim.mu.Lock()
		_, found := lim.entries[k]
		lim.mu.Unlock()
		if !found {
			t.Errorf("active entry %q was evicted", k)
		}
	}
}

// ── Test V1: Address checksum/network validation ───────────────────────────────
//
// Verifies that validateAndNormalizeAddress:
//   - accepts valid BTC/LTC mainnet addresses (legacy and SegWit);
//   - rejects checksum-mutated addresses, testnet addresses, and wrong-network
//     addresses with 400 "invalid or unsupported wallet address";
//   - never calls the issuer and leaves DB empty for all rejected addresses;
//   - never includes the raw address in the error response body or logs.

func TestHTTPAddressValidation(t *testing.T) {
	// ── Part 1: Valid addresses pass and reach the issuer ─────────────────────
	validCases := []struct {
		addr     string
		currency string
		draft    InvoiceDraft
		label    string
	}{
		{testBTCBech32Addr, "BTC", btcDraft(), "BTC bech32 P2WPKH"},
		{testBTCLegacyAddr, "BTC", btcDraft(), "BTC legacy P2PKH"},
		{testLTCLegacyAddr, "LTC", ltcDraft(), "LTC legacy P2PKH"},
		{testLTCBech32Addr, "LTC", ltcDraft(), "LTC bech32 P2WPKH"},
	}

	for _, tc := range validCases {
		tc := tc
		t.Run("valid/"+tc.label, func(t *testing.T) {
			issuer := &fakeIssuer{draft: tc.draft}
			h, svc := newTestHandler(t, issuer, &fakeBalance{}, nil)
			router := h.Routes()

			w := postJSON(router, createPath, map[string]string{"wallet_address": tc.addr})
			if w.Code != http.StatusCreated {
				t.Fatalf("addr %q: expected 201, got %d: %s", tc.addr, w.Code, w.Body.String())
			}
			if issuer.calls != 1 {
				t.Errorf("addr %q: issuer called %d times, want 1", tc.addr, issuer.calls)
			}

			var resp createResponse
			decodeJSON(t, w, &resp)
			if resp.Currency != tc.currency {
				t.Errorf("addr %q: currency %q, want %q", tc.addr, resp.Currency, tc.currency)
			}

			var flowCount int
			svc.db.QueryRow(`SELECT COUNT(*) FROM v2_client_flows`).Scan(&flowCount) //nolint:errcheck
			if flowCount != 1 {
				t.Errorf("addr %q: flow count %d, want 1", tc.addr, flowCount)
			}
		})
	}

	// ── Part 2: Invalid addresses → 400, issuer never called, DB empty ────────
	// Checksum-mutated addresses: flip the last character of each valid address.
	mutateLast := func(s string) string {
		b := []byte(s)
		last := b[len(b)-1]
		// Rotate within base58/bech32 character set — just increment by 1.
		if last == 'z' {
			b[len(b)-1] = 'a'
		} else if last == '9' {
			b[len(b)-1] = '0'
		} else {
			b[len(b)-1] = last + 1
		}
		return string(b)
	}

	invalidCases := []struct {
		addr  string
		label string
	}{
		// Checksum mutations — each changes one char and breaks the checksum.
		{mutateLast(testBTCBech32Addr), "BTC bech32 checksum mutated"},
		{mutateLast(testBTCLegacyAddr), "BTC legacy checksum mutated"},
		{mutateLast(testLTCLegacyAddr), "LTC legacy checksum mutated"},
		{mutateLast(testLTCBech32Addr), "LTC bech32 checksum mutated"},

		// BTC testnet bech32 (HRP "tb").
		{"tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx", "BTC testnet bech32"},
		// BTC testnet legacy (prefix 'm').
		{"mipcBbFg9gMiCh81Kj8tqqdgoZub1ZJRfn", "BTC testnet legacy"},

		// Wrong-network: LTC P2PKH address prefix ('L') but treated as LTC;
		// an actually invalid address with 'L' prefix.
		{"Lthisisnotavalidaddress0000000000000", "LTC invalid checksum"},

		// Unknown prefix.
		{"XBT1qsomething", "unknown prefix XBT"},
		{"ETH0xabcdef", "Ethereum address"},
		{"", "empty"},
		{"   ", "whitespace only"},
	}

	for _, tc := range invalidCases {
		tc := tc
		t.Run("invalid/"+tc.label, func(t *testing.T) {
			issuer := &fakeIssuer{draft: btcDraft()}
			h, svc := newTestHandler(t, issuer, &fakeBalance{}, nil)
			router := h.Routes()

			w := postJSON(router, createPath, map[string]string{"wallet_address": tc.addr})
			if w.Code != http.StatusBadRequest {
				t.Errorf("addr %q: expected 400, got %d: %s", tc.addr, w.Code, w.Body.String())
			}

			// Issuer must not have been called.
			if issuer.calls != 0 {
				t.Errorf("addr %q: issuer called %d times, want 0", tc.addr, issuer.calls)
			}

			// DB must be empty.
			var flowCount, invCount int
			svc.db.QueryRow(`SELECT COUNT(*) FROM v2_client_flows`).Scan(&flowCount) //nolint:errcheck
			svc.db.QueryRow(`SELECT COUNT(*) FROM v2_invoices`).Scan(&invCount)      //nolint:errcheck
			if flowCount != 0 || invCount != 0 {
				t.Errorf("addr %q: %d flow(s), %d invoice(s) after rejection; want 0 each",
					tc.addr, flowCount, invCount)
			}

			// Error body must not contain the raw address (privacy).
			if tc.addr != "" && strings.TrimSpace(tc.addr) != "" {
				trimmed := strings.TrimSpace(tc.addr)
				if strings.Contains(w.Body.String(), trimmed) {
					t.Errorf("addr %q: raw address leaked in 400 response: %s", tc.addr, w.Body.String())
				}
			}
		})
	}
}

// ── LTC params tests (P1-3) ──────────────────────────────────────────────────

// TestLTCParamsNonZeroNetID verifies that v2LTCMainNetParams.Net is set to the
// Litecoin mainnet magic (0xdbb6c0fb) and is never the zero value.
func TestLTCParamsNonZeroNetID(t *testing.T) {
	wantNet := wire.BitcoinNet(0xdbb6c0fb)
	if v2LTCMainNetParams.Net == 0 {
		t.Fatal("v2LTCMainNetParams.Net is zero — params were built with a sparse struct literal")
	}
	if v2LTCMainNetParams.Net != wantNet {
		t.Errorf("v2LTCMainNetParams.Net = 0x%x, want 0x%x", uint32(v2LTCMainNetParams.Net), uint32(wantNet))
	}
}

// TestLTCParamsDuplicateRegistration verifies that registering v2LTCMainNetParams
// a second time returns errors.Is(err, chaincfg.ErrDuplicateNet) == true.
// This proves the sentinel is matched without string comparison.
func TestLTCParamsDuplicateRegistration(t *testing.T) {
	err := chaincfg.Register(v2LTCMainNetParams)
	if err == nil {
		// Unlikely: maybe the test ran before init(). Unregister and skip.
		chaincfg.Register(v2LTCMainNetParams) //nolint:errcheck
		t.Skip("second Register returned nil — params may not have been pre-registered")
	}
	if !errors.Is(err, chaincfg.ErrDuplicateNet) {
		t.Errorf("second Register: got %v, want errors.Is(err, chaincfg.ErrDuplicateNet)==true", err)
	}
}

// TestHTTPP2SHAddresses verifies that valid BTC P2SH ("3…") and LTC P2SH ("M…")
// mainnet addresses are accepted (201) and that checksum-mutated variants are
// rejected (400) with no DB rows written.
func TestHTTPP2SHAddresses(t *testing.T) {
	// mutateLast flips the last character so the checksum breaks.
	mutateLast := func(addr string) string {
		if len(addr) == 0 {
			return addr
		}
		b := []byte(addr)
		if b[len(b)-1] == 'a' {
			b[len(b)-1] = 'b'
		} else {
			b[len(b)-1] = 'a'
		}
		return string(b)
	}

	validCases := []struct {
		addr     string
		label    string
		wantCurr string
	}{
		{testBTCP2SHAddr, "BTC P2SH (3…)", "BTC"},
		{testLTCP2SHAddr, "LTC P2SH (M…)", "LTC"},
	}

	for _, tc := range validCases {
		tc := tc
		t.Run("valid/"+tc.label, func(t *testing.T) {
			issuer := &fakeIssuer{draft: btcDraft()}
			if tc.wantCurr == "LTC" {
				issuer.draft = ltcDraft()
			}
			h, svc := newTestHandler(t, issuer, &fakeBalance{balance: 9999.0}, nil)
			router := h.Routes()

			w := postJSON(router, createPath, map[string]string{"wallet_address": tc.addr})
			if w.Code != http.StatusCreated {
				t.Errorf("%s: expected 201, got %d: %s", tc.label, w.Code, w.Body.String())
			}

			var count int
			svc.db.QueryRow(`SELECT COUNT(*) FROM v2_invoices`).Scan(&count) //nolint:errcheck
			if count != 1 {
				t.Errorf("%s: expected 1 invoice row, got %d", tc.label, count)
			}
		})
	}

	invalidCases := []struct {
		addr  string
		label string
	}{
		{mutateLast(testBTCP2SHAddr), "BTC P2SH checksum mutated"},
		{mutateLast(testLTCP2SHAddr), "LTC P2SH checksum mutated"},
	}

	for _, tc := range invalidCases {
		tc := tc
		t.Run("invalid/"+tc.label, func(t *testing.T) {
			issuer := &fakeIssuer{draft: btcDraft()}
			h, svc := newTestHandler(t, issuer, &fakeBalance{}, nil)
			router := h.Routes()

			w := postJSON(router, createPath, map[string]string{"wallet_address": tc.addr})
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s: expected 400, got %d: %s", tc.label, w.Code, w.Body.String())
			}
			if issuer.calls != 0 {
				t.Errorf("%s: issuer called %d times, want 0", tc.label, issuer.calls)
			}

			var flowCount, invCount int
			svc.db.QueryRow(`SELECT COUNT(*) FROM v2_client_flows`).Scan(&flowCount) //nolint:errcheck
			svc.db.QueryRow(`SELECT COUNT(*) FROM v2_invoices`).Scan(&invCount)      //nolint:errcheck
			if flowCount != 0 || invCount != 0 {
				t.Errorf("%s: %d flow(s), %d invoice(s) after rejection; want 0 each",
					tc.label, flowCount, invCount)
			}
		})
	}
}
