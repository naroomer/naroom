package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	ncrypto "naroom/internal/crypto"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// mockPrices is a test double for PriceFetcher.
type mockPrices struct {
	btc    float64
	ltc    float64
	btcErr error
	ltcErr error
}

func (m *mockPrices) BTCPrice() (float64, error) { return m.btc, m.btcErr }
func (m *mockPrices) LTCPrice() (float64, error) { return m.ltc, m.ltcErr }

// openTestDB creates a temporary SQLite database with the invoices AND listings tables.
// Both are needed: invoices for all tests, listings to prove confirmInvoice side-effects.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	f, err := os.CreateTemp("", "naroom-iw-test-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	name := f.Name()
	f.Close()
	t.Cleanup(func() { os.Remove(name) })

	db, err := sql.Open("sqlite", name)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE invoices (
			id                  TEXT PRIMARY KEY,
			type                TEXT NOT NULL,
			address             TEXT NOT NULL DEFAULT '',
			amount_usd          REAL NOT NULL DEFAULT 0,
			amount_crypto       TEXT NOT NULL DEFAULT '0',
			currency            TEXT NOT NULL,
			payer_address       TEXT,
			txid                TEXT,
			status              TEXT NOT NULL DEFAULT 'pending',
			listing_id          TEXT,
			response_id         TEXT,
			client_pubkey       TEXT,
			chat_room_id        TEXT,
			payment_detected_at INTEGER,
			price_at_creation   REAL,
			created_at          INTEGER NOT NULL
		);
		CREATE TABLE listings (
			id            TEXT PRIMARY KEY,
			city          TEXT NOT NULL DEFAULT 'tbilisi',
			dependency_type TEXT NOT NULL DEFAULT 'alcohol',
			help_type     TEXT NOT NULL DEFAULT 'crisis',
			urgency       TEXT NOT NULL DEFAULT 'urgent',
			languages     TEXT NOT NULL DEFAULT 'en',
			wallet_hash   TEXT NOT NULL DEFAULT 'test-hash',
			visible_until INTEGER NOT NULL DEFAULT 0,
			created_at    INTEGER NOT NULL DEFAULT 0,
			status        TEXT NOT NULL DEFAULT 'pending'
		)
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}

// insertInvoice inserts a minimal invoice row for testing.
func insertInvoice(t *testing.T, db *sql.DB, id, currency, payerAddress, status string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO invoices (id, type, address, amount_crypto, currency, payer_address, status, created_at)
		 VALUES (?, 'listing', 'test-addr', '0', ?, ?, ?, ?)`,
		id, currency, payerAddress, status, time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("insert invoice %s: %v", id, err)
	}
}

// invoiceStatus reads the current status of an invoice from the test DB.
func invoiceStatus(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM invoices WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatalf("read invoice status %s: %v", id, err)
	}
	return status
}

// newMempoolServer returns an httptest.Server that simulates mempool.space /address/:addr
// responding with the given confirmed balance in satoshis.
func newMempoolServer(t *testing.T, balanceSat int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"chain_stats": map[string]any{
				"funded_txo_sum": balanceSat,
				"spent_txo_sum":  int64(0),
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newErrorServer returns an httptest.Server that always responds with HTTP 503.
func newErrorServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newEmptyTxServer returns an httptest.Server that returns an empty tx list —
// simulating an address with no blockchain activity.
func newEmptyTxServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

const testHashKey = "test-hash-key-for-invoice-watcher"

// ── verifySenderAndBalance tests (DevMode=false) ─────────────────────────────

// IN-3: Empty payer_address → invoice immediately rejected, no blockchain call.
func TestVerify_EmptyPayerAddress(t *testing.T) {
	db := openTestDB(t)
	insertInvoice(t, db, "inv-empty-payer", "BTC", "", "pending")

	iw := &InvoiceWatcher{
		DB: db, HashKey: []byte(testHashKey), DevMode: false,
	}

	got := iw.verifySenderAndBalance("inv-empty-payer", "listing", "BTC", "", []string{"some-addr"}, 0)
	if got {
		t.Fatal("expected false for empty payer_address")
	}
	if s := invoiceStatus(t, db, "inv-empty-payer"); s != "rejected" {
		t.Fatalf("expected status=rejected, got %q", s)
	}
}

// IN-3: No senders in tx inputs → invoice rejected.
func TestVerify_NoSenders(t *testing.T) {
	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "addr-A")
	insertInvoice(t, db, "inv-no-senders", "BTC", payerHash, "pending")

	iw := &InvoiceWatcher{
		DB: db, HashKey: key, DevMode: false,
	}

	got := iw.verifySenderAndBalance("inv-no-senders", "listing", "BTC", payerHash, []string{}, 0)
	if got {
		t.Fatal("expected false for empty senders list")
	}
	if s := invoiceStatus(t, db, "inv-no-senders"); s != "rejected" {
		t.Fatalf("expected status=rejected, got %q", s)
	}
}

// IN-3: Sender hash does not match registered wallet → invoice rejected.
func TestVerify_WrongWallet(t *testing.T) {
	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "addr-A")
	insertInvoice(t, db, "inv-wrong-wallet", "BTC", payerHash, "pending")

	iw := &InvoiceWatcher{
		DB: db, HashKey: key, DevMode: false,
	}

	got := iw.verifySenderAndBalance("inv-wrong-wallet", "listing", "BTC", payerHash, []string{"addr-B"}, 0)
	if got {
		t.Fatal("expected false: sender addr-B does not match registered addr-A")
	}
	if s := invoiceStatus(t, db, "inv-wrong-wallet"); s != "rejected" {
		t.Fatalf("expected status=rejected, got %q", s)
	}
}

// IN-3: Multi-input tx — only one of multiple senders matches → accepted.
func TestVerify_MultiInputOneMatches(t *testing.T) {
	mempoolSrv := newMempoolServer(t, 10_000_000_000) // 100 BTC — well above $135 threshold

	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "addr-A")
	insertInvoice(t, db, "inv-multi-input", "BTC", payerHash, "pending")

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: key,
		Mempool: ncrypto.NewMempoolClient(mempoolSrv.URL),
		Prices:  &mockPrices{btc: 50_000},
		DevMode: false,
	}

	// addr-B first (no match), addr-A second (matches). Any match → accept.
	got := iw.verifySenderAndBalance("inv-multi-input", "listing", "BTC", payerHash,
		[]string{"addr-B", "addr-A"}, 50_000)
	if !got {
		t.Fatal("expected true: addr-A is a valid registered sender")
	}
	// verifySenderAndBalance returns true without setting 'confirmed' — that is confirmInvoice's job.
	if s := invoiceStatus(t, db, "inv-multi-input"); s != "pending" {
		t.Fatalf("expected status=pending after successful verify, got %q", s)
	}
}

// IN-5, IN-6: Balance API returns 503 → false returned, invoice stays 'pending' (not 'rejected').
// API outage must not permanently fail a payment that was already found on-chain.
func TestVerify_APIError_LeavesPending(t *testing.T) {
	errSrv := newErrorServer(t)

	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "addr-A")
	insertInvoice(t, db, "inv-api-error", "BTC", payerHash, "pending")

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: key,
		Mempool: ncrypto.NewMempoolClient(errSrv.URL),
		Prices:  &mockPrices{btc: 50_000},
		DevMode: false,
	}

	got := iw.verifySenderAndBalance("inv-api-error", "listing", "BTC", payerHash,
		[]string{"addr-A"}, 50_000)
	if got {
		t.Fatal("expected false: API error should not confirm the invoice")
	}
	// MUST be 'pending', not 'rejected'. Rejected is permanent; pending allows retry.
	if s := invoiceStatus(t, db, "inv-api-error"); s != "pending" {
		t.Fatalf("expected status=pending after API error, got %q", s)
	}
}

// ── IN-4: Double-confirm guard ────────────────────────────────────────────────

// IN-4: confirmInvoice on an already-confirmed invoice must be a complete no-op.
// Proves:
//  1. txid is not overwritten (WHERE status='pending' guard)
//  2. Linked listing is NOT activated (switch block never entered)
//
// Limitation: does not test chat room side-effects (would require full DB schema).
// Chat path uses the same RowsAffected=0 early-return, so the guard is structural,
// not type-specific. Documented as ⚠️ partial in TEST_MATRIX.md.
func TestDoubleConfirmGuard(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().Unix()

	// Insert a listing in 'pending' state — it should NOT become 'active'.
	_, err := db.Exec(`INSERT INTO listings (id, status, visible_until, created_at)
		VALUES ('list-1', 'pending', 0, ?)`, now)
	if err != nil {
		t.Fatalf("insert listing: %v", err)
	}

	// Invoice is already confirmed — a previous watcher tick confirmed it.
	_, err = db.Exec(`INSERT INTO invoices
		(id, type, address, amount_crypto, currency, status, txid, listing_id, created_at)
		VALUES ('inv-dupe', 'listing', 'test-addr', '0', 'BTC', 'confirmed', 'original-txid', 'list-1', ?)`,
		now)
	if err != nil {
		t.Fatalf("insert pre-confirmed invoice: %v", err)
	}

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: []byte(testHashKey),
		DevMode: false,
		// Prices=nil → balance check skipped. We never reach it anyway (RowsAffected=0 returns early).
	}

	// Simulate a second watcher tick trying to confirm the same invoice.
	iw.confirmInvoice("inv-dupe", "listing", "duplicate-txid", 0, "list-1", "", "")

	// 1. txid must not be overwritten.
	var txid sql.NullString
	db.QueryRow(`SELECT txid FROM invoices WHERE id = 'inv-dupe'`).Scan(&txid) //nolint:errcheck
	if txid.String == "duplicate-txid" {
		t.Fatal("IN-4 FAIL: txid overwritten despite WHERE status='pending' guard")
	}
	if txid.String != "original-txid" {
		t.Fatalf("unexpected txid %q", txid.String)
	}

	// 2. Invoice status must remain 'confirmed'.
	if s := invoiceStatus(t, db, "inv-dupe"); s != "confirmed" {
		t.Fatalf("expected status=confirmed, got %q", s)
	}

	// 3. Listing must NOT have been activated — the side-effect switch block was never entered.
	var listingStatus string
	db.QueryRow(`SELECT status FROM listings WHERE id = 'list-1'`).Scan(&listingStatus) //nolint:errcheck
	if listingStatus == "active" {
		t.Fatal("IN-4 FAIL: listing was activated by a duplicate confirm — switch block entered despite RowsAffected=0")
	}
	if listingStatus != "pending" {
		t.Fatalf("unexpected listing status %q (expected pending)", listingStatus)
	}
}

// ── IN-5: Balance math threshold tests ───────────────────────────────────────
//
// Formula: minUSD = minHold - invoiceCost - 10
//   listing: 150 - 5  - 10 = $135  (client must have at least $135 remaining)
//   chat:    1000 - 15 - 10 = $975  (peer must have at least $975 remaining)
//
// Threshold is strict: balance < minUSD → rejected; balance >= minUSD → true (caller confirms).
// Test price: $100,000/BTC. Satoshi conversions: $135 = 135000 sat, $134.999 = 134999 sat.

// IN-5: listing invoice; sender balance exactly at threshold ($135) → passes.
func TestBalanceThreshold_ListingPassesAt135(t *testing.T) {
	const btcPrice = 100_000.0
	// $135 = 135000 satoshis at $100k/BTC
	const sat135 = int64(135_000)

	mempoolSrv := newMempoolServer(t, sat135)
	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "addr-exact")
	insertInvoice(t, db, "inv-balance-pass", "BTC", payerHash, "pending")

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: key,
		Mempool: ncrypto.NewMempoolClient(mempoolSrv.URL),
		Prices:  &mockPrices{btc: btcPrice},
		DevMode: false,
	}

	got := iw.verifySenderAndBalance("inv-balance-pass", "listing", "BTC", payerHash,
		[]string{"addr-exact"}, btcPrice)
	if !got {
		t.Fatalf("IN-5 FAIL: expected true at exactly $135 (balance=minUSD), got false")
	}
	// verifySenderAndBalance returns true but does not confirm — status stays pending.
	if s := invoiceStatus(t, db, "inv-balance-pass"); s != "pending" {
		t.Fatalf("unexpected status after verify-pass: %q", s)
	}
}

// IN-5: listing invoice; sender balance one cent below threshold ($134.999) → rejected.
func TestBalanceThreshold_ListingFailsAt134(t *testing.T) {
	const btcPrice = 100_000.0
	// $134.999 = 134999 satoshis at $100k/BTC (1 sat below $135 threshold)
	const sat134_999 = int64(134_999)

	mempoolSrv := newMempoolServer(t, sat134_999)
	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "addr-low")
	insertInvoice(t, db, "inv-balance-fail", "BTC", payerHash, "pending")

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: key,
		Mempool: ncrypto.NewMempoolClient(mempoolSrv.URL),
		Prices:  &mockPrices{btc: btcPrice},
		DevMode: false,
	}

	got := iw.verifySenderAndBalance("inv-balance-fail", "listing", "BTC", payerHash,
		[]string{"addr-low"}, btcPrice)
	if got {
		t.Fatalf("IN-5 FAIL: expected false at $134.999 (1 sat below $135 threshold), got true")
	}
	if s := invoiceStatus(t, db, "inv-balance-fail"); s != "rejected" {
		t.Fatalf("IN-5 FAIL: expected status=rejected, got %q", s)
	}
}

// IN-5: chat invoice; sender balance exactly at threshold ($975) → passes.
func TestBalanceThreshold_ChatPassesAt975(t *testing.T) {
	const btcPrice = 100_000.0
	// $975 = 975000 satoshis at $100k/BTC
	const sat975 = int64(975_000)

	mempoolSrv := newMempoolServer(t, sat975)
	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "peer-addr-exact")

	// Chat invoice — must use type='chat' to trigger 1000/15 thresholds
	_, err := db.Exec(
		`INSERT INTO invoices (id, type, address, amount_crypto, currency, payer_address, status, created_at)
		 VALUES ('inv-chat-pass', 'chat', 'test-addr', '0', 'BTC', ?, 'pending', ?)`,
		payerHash, time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("insert chat invoice: %v", err)
	}

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: key,
		Mempool: ncrypto.NewMempoolClient(mempoolSrv.URL),
		Prices:  &mockPrices{btc: btcPrice},
		DevMode: false,
	}

	got := iw.verifySenderAndBalance("inv-chat-pass", "chat", "BTC", payerHash,
		[]string{"peer-addr-exact"}, btcPrice)
	if !got {
		t.Fatalf("IN-5 FAIL: expected true at exactly $975 (chat threshold), got false")
	}
}

// IN-5: chat invoice; sender balance one cent below threshold ($974.999) → rejected.
func TestBalanceThreshold_ChatFailsAt974(t *testing.T) {
	const btcPrice = 100_000.0
	// $974.999 = 974999 satoshis (1 sat below $975 threshold)
	const sat974_999 = int64(974_999)

	mempoolSrv := newMempoolServer(t, sat974_999)
	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "peer-addr-low")

	_, err := db.Exec(
		`INSERT INTO invoices (id, type, address, amount_crypto, currency, payer_address, status, created_at)
		 VALUES ('inv-chat-fail', 'chat', 'test-addr', '0', 'BTC', ?, 'pending', ?)`,
		payerHash, time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("insert chat invoice: %v", err)
	}

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: key,
		Mempool: ncrypto.NewMempoolClient(mempoolSrv.URL),
		Prices:  &mockPrices{btc: btcPrice},
		DevMode: false,
	}

	got := iw.verifySenderAndBalance("inv-chat-fail", "chat", "BTC", payerHash,
		[]string{"peer-addr-low"}, btcPrice)
	if got {
		t.Fatalf("IN-5 FAIL: expected false at $974.999 (1 sat below $975 chat threshold), got true")
	}
	if s := invoiceStatus(t, db, "inv-chat-fail"); s != "rejected" {
		t.Fatalf("IN-5 FAIL: expected status=rejected, got %q", s)
	}
}

// ── IN-6: Grace-window expiry tests ──────────────────────────────────────────
//
// These tests exercise the expiry logic inside watch(), not verifySenderAndBalance.
// watch() applies the deadline BEFORE calling any blockchain API.
//
// Deadline rules (from invoice_watcher.go):
//   expiryDeadline = created_at + 3600           (1-hour normal TTL)
//   if payment_detected_at valid:
//       grace = payment_detected_at + 86400       (24-hour grace from detection)
//       expiryDeadline = max(expiryDeadline, grace)
//   if now > expiryDeadline → mark 'expired' and continue

// IN-6a: Normal TTL has passed, but payment was detected and grace window is still open.
// Invoice must NOT be expired.
func TestGraceWindow_NotExpiredWithinGrace(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().Unix()

	// Timestamps:
	//   created_at         = now - 7200  →  normal deadline = now - 7200 + 3600 = now - 3600 (expired 1h ago)
	//   payment_detected_at = now - 1800  →  grace deadline  = now - 1800 + 86400 = now + 84600 (active for 23.5h)
	// Expected: max(now-3600, now+84600) = now+84600 → NOT expired
	createdAt := now - 7200
	detectedAt := now - 1800

	_, err := db.Exec(`INSERT INTO invoices
		(id, type, address, amount_crypto, currency, payer_address, status, payment_detected_at, created_at)
		VALUES ('inv-grace-active', 'listing', 'btc-addr', '0', 'BTC', 'some-hash', 'pending', ?, ?)`,
		detectedAt, createdAt)
	if err != nil {
		t.Fatalf("insert invoice: %v", err)
	}

	// Mock mempool: /address/:addr/txs returns empty list (no new payment to process).
	// watch() will: check expiry (not expired) → call FindPayment → tx=nil → skip.
	txSrv := newEmptyTxServer(t)

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: []byte(testHashKey),
		Mempool: ncrypto.NewMempoolClient(txSrv.URL),
		Prices:  nil,
		DevMode: false,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	iw.watch(ctx)

	// Invoice must still be 'pending' — not expired, not confirmed (no tx found).
	if s := invoiceStatus(t, db, "inv-grace-active"); s != "pending" {
		t.Fatalf("IN-6 FAIL: expected pending (within grace window), got %q", s)
	}
}

// IN-6b: Both normal TTL and grace window have passed.
// Invoice must be marked 'expired'.
func TestGraceWindow_ExpiredAfterGrace(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().Unix()

	// Timestamps:
	//   created_at          = now - 90000  →  normal deadline = now - 90000 + 3600  = now - 86400 (past)
	//   payment_detected_at = now - 87000  →  grace deadline  = now - 87000 + 86400 = now - 600   (10 min ago, past)
	// Expected: max(now-86400, now-600) = now-600 → EXPIRED (600s ago)
	createdAt := now - 90000
	detectedAt := now - 87000

	_, err := db.Exec(`INSERT INTO invoices
		(id, type, address, amount_crypto, currency, payer_address, status, payment_detected_at, created_at)
		VALUES ('inv-grace-expired', 'listing', 'btc-addr', '0', 'BTC', 'some-hash', 'pending', ?, ?)`,
		detectedAt, createdAt)
	if err != nil {
		t.Fatalf("insert invoice: %v", err)
	}

	// No mock needed for blockchain: expiry check fires before FindPayment.
	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: []byte(testHashKey),
		DevMode: false,
		// Mempool=nil is safe: watch() marks expired via `continue` before reaching FindPayment.
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	iw.watch(ctx)

	if s := invoiceStatus(t, db, "inv-grace-expired"); s != "expired" {
		t.Fatalf("IN-6 FAIL: expected expired (grace window passed), got %q", s)
	}
}
