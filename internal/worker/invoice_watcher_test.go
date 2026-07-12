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

// openTestDBFull creates a temporary SQLite database with the full schema needed
// for chat-related confirmInvoice tests (invoices, listings, responses, chat_rooms, wallet_sessions).
func openTestDBFull(t *testing.T) *sql.DB {
	t.Helper()
	f, err := os.CreateTemp("", "naroom-iw-full-test-*.db")
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
			id                 TEXT PRIMARY KEY,
			city               TEXT NOT NULL DEFAULT 'tbilisi',
			dependency_type    TEXT NOT NULL DEFAULT 'alcohol',
			help_type          TEXT NOT NULL DEFAULT 'crisis',
			urgency            TEXT NOT NULL DEFAULT 'urgent',
			languages          TEXT NOT NULL DEFAULT 'en',
			wallet_hash        TEXT NOT NULL DEFAULT 'test-hash',
			visible_until      INTEGER NOT NULL DEFAULT 0,
			created_at         INTEGER NOT NULL DEFAULT 0,
			status             TEXT NOT NULL DEFAULT 'pending',
			opened_chats_count INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE responses (
			id               TEXT PRIMARY KEY,
			listing_id       TEXT NOT NULL,
			counselor_hash   TEXT NOT NULL,
			counselor_pubkey TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE chat_rooms (
			id               TEXT PRIMARY KEY,
			listing_id       TEXT NOT NULL,
			response_id      TEXT NOT NULL,
			client_hash      TEXT NOT NULL,
			counselor_hash   TEXT NOT NULL,
			client_pubkey    TEXT NOT NULL DEFAULT '',
			counselor_pubkey TEXT NOT NULL DEFAULT '',
			started_at       INTEGER NOT NULL DEFAULT 0,
			expires_at       INTEGER NOT NULL DEFAULT 0,
			status           TEXT NOT NULL DEFAULT 'active',
			listing_counted  INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE wallet_sessions (
			wallet_hash      TEXT PRIMARY KEY,
			min_required_usd REAL
		);
	`)
	if err != nil {
		t.Fatalf("create full schema: %v", err)
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

// ── resolveSender tests (DevMode=false) ──────────────────────────────────────

// IN-3: Empty payer_address → invoice immediately rejected, no blockchain call.
func TestVerify_EmptyPayerAddress(t *testing.T) {
	db := openTestDB(t)
	insertInvoice(t, db, "inv-empty-payer", "BTC", "", "pending")

	iw := &InvoiceWatcher{
		DB: db, HashKey: []byte(testHashKey), DevMode: false,
	}

	got, ok := iw.resolveSender("inv-empty-payer", "listing", "BTC", "", []string{"some-addr"}, 0)
	if ok {
		t.Fatal("expected ok=false for empty payer_address")
	}
	if got != "" {
		t.Fatalf("expected empty hash on failure, got %q", got)
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

	got, ok := iw.resolveSender("inv-no-senders", "listing", "BTC", payerHash, []string{}, 0)
	if ok {
		t.Fatal("expected ok=false for empty senders list")
	}
	if got != "" {
		t.Fatalf("expected empty hash on failure, got %q", got)
	}
	if s := invoiceStatus(t, db, "inv-no-senders"); s != "rejected" {
		t.Fatalf("expected status=rejected, got %q", s)
	}
}

// TestResolve_DifferentSender_Accepted: payment from unregistered wallet B
// → accepted, returns hash(B).
func TestResolve_DifferentSender_Accepted(t *testing.T) {
	mempoolSrv := newMempoolServer(t, 10_000_000_000) // ample balance
	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "addr-A")
	insertInvoice(t, db, "inv-rebind", "BTC", payerHash, "pending")

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: key,
		Mempool: ncrypto.NewMempoolClient(mempoolSrv.URL),
		Prices:  &mockPrices{btc: 50_000},
		DevMode: false,
	}

	expectedHash := ncrypto.WalletHash(key, "addr-B")
	got, ok := iw.resolveSender("inv-rebind", "listing", "BTC", payerHash,
		[]string{"addr-B"}, 50_000)
	if !ok {
		t.Fatal("expected ok=true: different sender with good balance should be accepted")
	}
	if got != expectedHash {
		t.Fatalf("expected hash(addr-B)=%q, got %q", expectedHash, got)
	}
	// Invoice must NOT be rejected
	if s := invoiceStatus(t, db, "inv-rebind"); s == "rejected" {
		t.Fatal("invoice was rejected for different sender — should be accepted under new model")
	}
}

// TestResolve_RegisteredWalletPreferred: when registered wallet is among multiple senders,
// prefer it (return registered hash, no rebind).
func TestResolve_RegisteredWalletPreferred(t *testing.T) {
	mempoolSrv := newMempoolServer(t, 10_000_000_000) // ample balance
	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "addr-A") // registered wallet
	insertInvoice(t, db, "inv-prefer-registered", "BTC", payerHash, "pending")

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: key,
		Mempool: ncrypto.NewMempoolClient(mempoolSrv.URL),
		Prices:  &mockPrices{btc: 50_000},
		DevMode: false,
	}

	// senders = [addr-B, addr-A] — addr-A is registered, should be preferred
	got, ok := iw.resolveSender("inv-prefer-registered", "listing", "BTC", payerHash,
		[]string{"addr-B", "addr-A"}, 50_000)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != payerHash {
		t.Fatalf("expected registered wallet hash to be preferred, got %q", got)
	}
}

// IN-3: Multi-input tx — only one of multiple senders matches registered wallet → accepted with registered hash.
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

	// addr-B first (no match), addr-A second (matches). Registered wallet preferred.
	got, ok := iw.resolveSender("inv-multi-input", "listing", "BTC", payerHash,
		[]string{"addr-B", "addr-A"}, 50_000)
	if !ok {
		t.Fatal("expected ok=true: addr-A is a valid registered sender")
	}
	if got != payerHash {
		t.Fatalf("expected registered wallet hash %q, got %q", payerHash, got)
	}
	// resolveSender returns hash without confirming — status stays pending.
	if s := invoiceStatus(t, db, "inv-multi-input"); s != "pending" {
		t.Fatalf("expected status=pending after successful resolve, got %q", s)
	}
}

// IN-5, IN-6: Balance API returns 503 → ("", false) returned, invoice stays 'pending' (not 'rejected').
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

	got, ok := iw.resolveSender("inv-api-error", "listing", "BTC", payerHash,
		[]string{"addr-A"}, 50_000)
	if ok {
		t.Fatal("expected ok=false: API error should not confirm the invoice")
	}
	if got != "" {
		t.Fatalf("expected empty hash on API error, got %q", got)
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
	iw.confirmInvoice("inv-dupe", "listing", "duplicate-txid", 0, "list-1", "", "", "")

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
//
//	listing: 150 - 5  - 10 = $135  (client must have at least $135 remaining)
//	chat:    1000 - 15 - 10 = $975  (peer must have at least $975 remaining)
//
// Threshold is strict: balance < minUSD → rejected; balance >= minUSD → ("hash", true).
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

	got, ok := iw.resolveSender("inv-balance-pass", "listing", "BTC", payerHash,
		[]string{"addr-exact"}, btcPrice)
	if !ok {
		t.Fatalf("IN-5 FAIL: expected ok=true at exactly $135 (balance=minUSD), got false")
	}
	if got != payerHash {
		t.Fatalf("expected hash of addr-exact (%q), got %q", payerHash, got)
	}
	// resolveSender returns hash but does not confirm — status stays pending.
	if s := invoiceStatus(t, db, "inv-balance-pass"); s != "pending" {
		t.Fatalf("unexpected status after resolve-pass: %q", s)
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

	got, ok := iw.resolveSender("inv-balance-fail", "listing", "BTC", payerHash,
		[]string{"addr-low"}, btcPrice)
	if ok {
		t.Fatalf("IN-5 FAIL: expected ok=false at $134.999 (1 sat below $135 threshold), got true")
	}
	if got != "" {
		t.Fatalf("expected empty hash on failure, got %q", got)
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

	got, ok := iw.resolveSender("inv-chat-pass", "chat", "BTC", payerHash,
		[]string{"peer-addr-exact"}, btcPrice)
	if !ok {
		t.Fatalf("IN-5 FAIL: expected ok=true at exactly $975 (chat threshold), got false")
	}
	if got != payerHash {
		t.Fatalf("expected hash of peer-addr-exact (%q), got %q", payerHash, got)
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

	got, ok := iw.resolveSender("inv-chat-fail", "chat", "BTC", payerHash,
		[]string{"peer-addr-low"}, btcPrice)
	if ok {
		t.Fatalf("IN-5 FAIL: expected ok=false at $974.999 (1 sat below $975 chat threshold), got true")
	}
	if got != "" {
		t.Fatalf("expected empty hash on failure, got %q", got)
	}
	if s := invoiceStatus(t, db, "inv-chat-fail"); s != "rejected" {
		t.Fatalf("IN-5 FAIL: expected status=rejected, got %q", s)
	}
}

// TestResolve_DifferentSender_InsufficientBalance: payment from unregistered wallet B
// with insufficient balance → rejected.
func TestResolve_DifferentSender_InsufficientBalance(t *testing.T) {
	const btcPrice = 100_000.0
	const sat134_999 = int64(134_999) // $134.999 — below $135 listing threshold
	mempoolSrv := newMempoolServer(t, sat134_999)
	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "addr-A")
	insertInvoice(t, db, "inv-rebind-lowbal", "BTC", payerHash, "pending")

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: key,
		Mempool: ncrypto.NewMempoolClient(mempoolSrv.URL),
		Prices:  &mockPrices{btc: btcPrice},
		DevMode: false,
	}

	got, ok := iw.resolveSender("inv-rebind-lowbal", "listing", "BTC", payerHash,
		[]string{"addr-B"}, btcPrice)
	if ok {
		t.Fatalf("expected ok=false: addr-B has insufficient balance, got hash=%q", got)
	}
	if got != "" {
		t.Fatalf("expected empty hash on failure, got %q", got)
	}
	if s := invoiceStatus(t, db, "inv-rebind-lowbal"); s != "rejected" {
		t.Fatalf("expected status=rejected for insufficient balance, got %q", s)
	}
}

// ── IN-6: Grace-window expiry tests ──────────────────────────────────────────
//
// These tests exercise the expiry logic inside watch(), not resolveSender.
// watch() applies the deadline BEFORE calling any blockchain API.
//
// Deadline rules (from invoice_watcher.go):
//
//	expiryDeadline = created_at + 3600           (1-hour normal TTL)
//	if payment_detected_at valid:
//	    grace = payment_detected_at + 86400       (24-hour grace from detection)
//	    expiryDeadline = max(expiryDeadline, grace)
//	if now > expiryDeadline → mark 'expired' and continue

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

// ── Test D: Double-confirm chat — opened_chats_count idempotency ──────────────

// TestDoubleConfirm_OpenedChatsCountNotDoubled (Test D):
// Simulates a watcher retry — confirmInvoice called twice for the same chat invoice.
// The first call creates the chat room and increments opened_chats_count.
// The second call must be a no-op: opened_chats_count stays at 1.
func TestDoubleConfirm_OpenedChatsCountNotDoubled(t *testing.T) {
	db := openTestDBFull(t)
	now := time.Now().Unix()

	// Insert counselor wallet session
	_, err := db.Exec(`INSERT INTO wallet_sessions (wallet_hash, min_required_usd) VALUES ('counselor-hash-1', 2000)`)
	if err != nil {
		t.Fatalf("insert wallet_session: %v", err)
	}

	// Insert listing for the client
	_, err = db.Exec(`INSERT INTO listings (id, wallet_hash, status, visible_until, created_at, opened_chats_count)
		VALUES ('list-chat-1', 'client-hash-1', 'active', ?, ?, 0)`, now+86400, now)
	if err != nil {
		t.Fatalf("insert listing: %v", err)
	}

	// Insert response from counselor
	_, err = db.Exec(`INSERT INTO responses (id, listing_id, counselor_hash, counselor_pubkey)
		VALUES ('resp-1', 'list-chat-1', 'counselor-hash-1', 'counselor-pubkey-1')`)
	if err != nil {
		t.Fatalf("insert response: %v", err)
	}

	// Insert pending chat invoice
	_, err = db.Exec(`INSERT INTO invoices
		(id, type, address, amount_crypto, currency, payer_address, status, response_id, client_pubkey, created_at)
		VALUES ('inv-chat-double', 'chat', 'test-addr', '0', 'BTC', 'counselor-hash-1', 'pending', 'resp-1', 'client-pubkey-1', ?)`,
		now)
	if err != nil {
		t.Fatalf("insert chat invoice: %v", err)
	}

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: []byte(testHashKey),
		DevMode: true, // DevMode so amount check is skipped
	}

	// First call: should create chat room and increment opened_chats_count to 1
	iw.confirmInvoice("inv-chat-double", "chat", "txid-1", 1000000, "", "resp-1", "client-pubkey-1", "")

	// Verify first call worked: opened_chats_count should be 1
	var count int
	if err := db.QueryRow(`SELECT opened_chats_count FROM listings WHERE id = 'list-chat-1'`).Scan(&count); err != nil {
		t.Fatalf("read opened_chats_count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected opened_chats_count=1 after first confirm, got %d", count)
	}

	// Verify invoice is confirmed and has chat_room_id
	var chatRoomID sql.NullString
	if err := db.QueryRow(`SELECT chat_room_id FROM invoices WHERE id = 'inv-chat-double'`).Scan(&chatRoomID); err != nil {
		t.Fatalf("read chat_room_id: %v", err)
	}
	if !chatRoomID.Valid || chatRoomID.String == "" {
		t.Fatal("expected chat_room_id to be set after first confirm")
	}

	// Second call: invoice is now 'confirmed', must be a no-op
	iw.confirmInvoice("inv-chat-double", "chat", "txid-2", 1000000, "", "resp-1", "client-pubkey-1", "")

	// opened_chats_count must still be 1 — not doubled
	if err := db.QueryRow(`SELECT opened_chats_count FROM listings WHERE id = 'list-chat-1'`).Scan(&count); err != nil {
		t.Fatalf("read opened_chats_count after second confirm: %v", err)
	}
	if count != 1 {
		t.Fatalf("Test D FAIL: opened_chats_count doubled to %d after duplicate confirmInvoice", count)
	}

	// chat_rooms must have exactly one row for this invoice
	var roomCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chat_rooms WHERE response_id = 'resp-1'`).Scan(&roomCount); err != nil {
		t.Fatalf("count chat_rooms: %v", err)
	}
	if roomCount != 1 {
		t.Fatalf("Test D FAIL: expected 1 chat room, got %d", roomCount)
	}
}

// ── VIS-W: Listing status after first and second chat ────────────────────────

// VIS-W1: After first chat invoice confirmed, listing stays 'active' (NOT 'matched').
func TestListingStaysActive_AfterFirstChat(t *testing.T) {
	db := openTestDBFull(t)
	now := time.Now().Unix()

	_, _ = db.Exec(`INSERT INTO wallet_sessions (wallet_hash, min_required_usd) VALUES ('peer-vis-1', 2000)`)
	_, _ = db.Exec(`INSERT INTO listings (id, wallet_hash, status, visible_until, created_at, opened_chats_count)
		VALUES ('vis-list-1', 'client-vis-1', 'active', ?, ?, 0)`, now+86400, now)
	_, _ = db.Exec(`INSERT INTO responses (id, listing_id, counselor_hash, counselor_pubkey)
		VALUES ('vis-resp-1', 'vis-list-1', 'peer-vis-1', 'peer-pub-1')`)
	_, _ = db.Exec(`INSERT INTO invoices
		(id, type, address, amount_crypto, currency, payer_address, status, response_id, client_pubkey, created_at)
		VALUES ('vis-inv-1', 'chat', 'addr', '0', 'BTC', 'peer-vis-1', 'pending', 'vis-resp-1', 'client-pub-1', ?)`, now)

	iw := &InvoiceWatcher{DB: db, HashKey: []byte(testHashKey), DevMode: true}
	iw.confirmInvoice("vis-inv-1", "chat", "txid-1", 1000000, "", "vis-resp-1", "client-pub-1", "")

	var status string
	db.QueryRow(`SELECT status FROM listings WHERE id = 'vis-list-1'`).Scan(&status)
	if status != "active" {
		t.Fatalf("VIS-W1 FAIL: after first chat, listing status must be 'active', got %q", status)
	}

	var count int
	db.QueryRow(`SELECT opened_chats_count FROM listings WHERE id = 'vis-list-1'`).Scan(&count)
	if count != 1 {
		t.Fatalf("VIS-W1 FAIL: opened_chats_count must be 1, got %d", count)
	}
}

// VIS-W2: After second chat invoice confirmed, listing becomes 'closed'.
func TestListingClosed_AfterSecondChat(t *testing.T) {
	db := openTestDBFull(t)
	now := time.Now().Unix()

	_, _ = db.Exec(`INSERT INTO wallet_sessions (wallet_hash, min_required_usd) VALUES ('peer-vis-2a', 2000)`)
	_, _ = db.Exec(`INSERT INTO wallet_sessions (wallet_hash, min_required_usd) VALUES ('peer-vis-2b', 2000)`)
	_, _ = db.Exec(`INSERT INTO listings (id, wallet_hash, status, visible_until, created_at, opened_chats_count)
		VALUES ('vis-list-2', 'client-vis-2', 'active', ?, ?, 1)`, now+86400, now)
	_, _ = db.Exec(`INSERT INTO responses (id, listing_id, counselor_hash, counselor_pubkey)
		VALUES ('vis-resp-2b', 'vis-list-2', 'peer-vis-2b', 'peer-pub-2b')`)
	_, _ = db.Exec(`INSERT INTO invoices
		(id, type, address, amount_crypto, currency, payer_address, status, response_id, client_pubkey, created_at)
		VALUES ('vis-inv-2b', 'chat', 'addr', '0', 'BTC', 'peer-vis-2b', 'pending', 'vis-resp-2b', 'client-pub-2', ?)`, now)

	iw := &InvoiceWatcher{DB: db, HashKey: []byte(testHashKey), DevMode: true}
	iw.confirmInvoice("vis-inv-2b", "chat", "txid-2b", 1000000, "", "vis-resp-2b", "client-pub-2", "")

	var status string
	db.QueryRow(`SELECT status FROM listings WHERE id = 'vis-list-2'`).Scan(&status)
	if status != "closed" {
		t.Fatalf("VIS-W2 FAIL: after second chat, listing status must be 'closed', got %q", status)
	}

	var count int
	db.QueryRow(`SELECT opened_chats_count FROM listings WHERE id = 'vis-list-2'`).Scan(&count)
	if count != 2 {
		t.Fatalf("VIS-W2 FAIL: opened_chats_count must be 2, got %d", count)
	}
}

// openTestDBFullWithStatus is like openTestDBFull but adds status to responses
// and adds wallet_sessions.wallet_address_enc (not needed here but keeps schema consistent).
func openTestDBFullWithStatus(t *testing.T) *sql.DB {
	t.Helper()
	f, err := os.CreateTemp("", "naroom-iw-status-test-*.db")
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
			id                 TEXT PRIMARY KEY,
			city               TEXT NOT NULL DEFAULT 'tbilisi',
			dependency_type    TEXT NOT NULL DEFAULT 'alcohol',
			help_type          TEXT NOT NULL DEFAULT 'crisis',
			urgency            TEXT NOT NULL DEFAULT 'urgent',
			languages          TEXT NOT NULL DEFAULT 'en',
			wallet_hash        TEXT NOT NULL DEFAULT 'test-hash',
			visible_until      INTEGER NOT NULL DEFAULT 0,
			created_at         INTEGER NOT NULL DEFAULT 0,
			status             TEXT NOT NULL DEFAULT 'pending',
			opened_chats_count INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE responses (
			id               TEXT PRIMARY KEY,
			listing_id       TEXT NOT NULL,
			counselor_hash   TEXT NOT NULL,
			counselor_pubkey TEXT NOT NULL DEFAULT '',
			status           TEXT NOT NULL DEFAULT 'pending'
		);
		CREATE TABLE chat_rooms (
			id               TEXT PRIMARY KEY,
			listing_id       TEXT NOT NULL,
			response_id      TEXT NOT NULL,
			client_hash      TEXT NOT NULL,
			counselor_hash   TEXT NOT NULL,
			client_pubkey    TEXT NOT NULL DEFAULT '',
			counselor_pubkey TEXT NOT NULL DEFAULT '',
			started_at       INTEGER NOT NULL DEFAULT 0,
			expires_at       INTEGER NOT NULL DEFAULT 0,
			status           TEXT NOT NULL DEFAULT 'active',
			listing_counted  INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE wallet_sessions (
			wallet_hash      TEXT PRIMARY KEY,
			min_required_usd REAL
		);
	`)
	if err != nil {
		t.Fatalf("create full schema with status: %v", err)
	}
	return db
}

// ── Atomic guard tests ───────────────────────────────────────────────────────

// ATOM-1: confirmInvoice on a listing already at count=2 must not create a chat room.
// This tests the CAS guard: UPDATE … WHERE count < 2 returns RowsAffected=0 → abort.
func TestAtomicGuard_ThirdRoomImpossible(t *testing.T) {
	db := openTestDBFullWithStatus(t)
	now := time.Now().Unix()

	_, _ = db.Exec(`INSERT INTO wallet_sessions (wallet_hash, min_required_usd) VALUES ('counselor-atom-1', 2000)`)
	_, _ = db.Exec(`INSERT INTO listings (id, wallet_hash, status, visible_until, created_at, opened_chats_count)
		VALUES ('atom-list-1', 'client-atom-1', 'closed', ?, ?, 2)`, now+86400, now)
	_, _ = db.Exec(`INSERT INTO responses (id, listing_id, counselor_hash, counselor_pubkey, status)
		VALUES ('atom-resp-c', 'atom-list-1', 'counselor-atom-1', 'pub-c', 'pending')`)
	_, _ = db.Exec(`INSERT INTO invoices
		(id, type, address, amount_crypto, currency, payer_address, status, response_id, client_pubkey, created_at)
		VALUES ('atom-inv-c', 'chat', 'addr', '0', 'BTC', 'counselor-atom-1', 'pending', 'atom-resp-c', 'client-pub-c', ?)`, now)

	iw := &InvoiceWatcher{DB: db, HashKey: []byte(testHashKey), DevMode: true}
	iw.confirmInvoice("atom-inv-c", "chat", "txid-c", 1000000, "", "atom-resp-c", "client-pub-c", "")

	// No chat room must have been created
	var roomCount int
	db.QueryRow(`SELECT COUNT(*) FROM chat_rooms WHERE listing_id = 'atom-list-1'`).Scan(&roomCount)
	if roomCount != 0 {
		t.Fatalf("ATOM-1 FAIL: expected 0 chat rooms (CAS guard should have blocked), got %d", roomCount)
	}

	// Invoice must have no chat_room_id
	var chatRoomID sql.NullString
	db.QueryRow(`SELECT chat_room_id FROM invoices WHERE id = 'atom-inv-c'`).Scan(&chatRoomID)
	if chatRoomID.Valid && chatRoomID.String != "" {
		t.Fatalf("ATOM-1 FAIL: invoice chat_room_id should be NULL, got %q", chatRoomID.String)
	}

	// Listing count must remain at 2
	var count int
	db.QueryRow(`SELECT opened_chats_count FROM listings WHERE id = 'atom-list-1'`).Scan(&count)
	if count != 2 {
		t.Fatalf("ATOM-1 FAIL: opened_chats_count should remain 2, got %d", count)
	}
}

// ATOM-2: second payment confirms → listing becomes 'closed', pending response rejected.
func TestSecondPayment_ClosesListingAndRejectsPending(t *testing.T) {
	db := openTestDBFullWithStatus(t)
	now := time.Now().Unix()

	_, _ = db.Exec(`INSERT INTO wallet_sessions (wallet_hash, min_required_usd) VALUES ('counselor-atom-2b', 2000)`)
	_, _ = db.Exec(`INSERT INTO listings (id, wallet_hash, status, visible_until, created_at, opened_chats_count)
		VALUES ('atom-list-2', 'client-atom-2', 'active', ?, ?, 1)`, now+86400, now)
	_, _ = db.Exec(`INSERT INTO responses (id, listing_id, counselor_hash, counselor_pubkey, status)
		VALUES ('atom-resp-b', 'atom-list-2', 'counselor-atom-2b', 'pub-b', 'accepted')`)
	_, _ = db.Exec(`INSERT INTO responses (id, listing_id, counselor_hash, counselor_pubkey, status)
		VALUES ('atom-resp-c2', 'atom-list-2', 'counselor-atom-x', 'pub-x', 'pending')`)
	_, _ = db.Exec(`INSERT INTO invoices
		(id, type, address, amount_crypto, currency, payer_address, status, response_id, client_pubkey, created_at)
		VALUES ('atom-inv-b', 'chat', 'addr', '0', 'BTC', 'counselor-atom-2b', 'pending', 'atom-resp-b', 'client-pub-2', ?)`, now)

	iw := &InvoiceWatcher{DB: db, HashKey: []byte(testHashKey), DevMode: true}
	iw.confirmInvoice("atom-inv-b", "chat", "txid-b", 1000000, "", "atom-resp-b", "client-pub-2", "")

	// Listing must be 'closed'
	var listingStatus string
	db.QueryRow(`SELECT status FROM listings WHERE id = 'atom-list-2'`).Scan(&listingStatus)
	if listingStatus != "closed" {
		t.Fatalf("ATOM-2 FAIL: listing must be 'closed' after second payment, got %q", listingStatus)
	}

	// opened_chats_count must be 2
	var count int
	db.QueryRow(`SELECT opened_chats_count FROM listings WHERE id = 'atom-list-2'`).Scan(&count)
	if count != 2 {
		t.Fatalf("ATOM-2 FAIL: opened_chats_count must be 2, got %d", count)
	}

	// Pending response must be 'rejected'
	var respStatus string
	db.QueryRow(`SELECT status FROM responses WHERE id = 'atom-resp-c2'`).Scan(&respStatus)
	if respStatus != "rejected" {
		t.Fatalf("ATOM-2 FAIL: pending response must be 'rejected', got %q", respStatus)
	}
}
