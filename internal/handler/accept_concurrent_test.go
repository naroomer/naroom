package handler

// Tests for the second-concurrent-chat entitlement in AcceptResponse.
//
// Approved model:
//   - While opened_chats_count=1, client may accept Helper B while Chat A is active.
//   - An accepted response tied to an existing chat_room is NOT an unpaid reservation.
//   - At most one accepted response with a pending unpaid invoice (no room) at a time.
//   - Pending responses remain pending after the first Helper is accepted.
//   - Count=2 → listing fully consumed, no further acceptances.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	ncrypto "naroom/internal/crypto"
	"naroom/internal/middleware"
)

// ── test fixtures ─────────────────────────────────────────────────────────────

var acceptEncKey = make([]byte, 32) // all-zero 32-byte AES key for tests

func openAcceptTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`CREATE TABLE sessions (
			token_hash TEXT PRIMARY KEY,
			wallet_hash TEXT NOT NULL,
			currency TEXT, role TEXT,
			created_at INTEGER, expires_at INTEGER,
			revoked_at INTEGER, last_seen_at INTEGER
		)`,
		`CREATE TABLE listings (
			id                  TEXT PRIMARY KEY,
			city                TEXT NOT NULL DEFAULT 'tbilisi',
			dependency_type     TEXT NOT NULL DEFAULT 'alcohol',
			help_type           TEXT NOT NULL DEFAULT 'crisis',
			urgency             TEXT NOT NULL DEFAULT 'can_wait',
			languages           TEXT NOT NULL DEFAULT '["en"]',
			wallet_hash         TEXT NOT NULL DEFAULT '',
			visible_until       INTEGER NOT NULL DEFAULT 0,
			created_at          INTEGER NOT NULL DEFAULT 0,
			status              TEXT NOT NULL DEFAULT 'active',
			opened_chats_count  INTEGER NOT NULL DEFAULT 0,
			is_sample           INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE responses (
			id               TEXT PRIMARY KEY,
			listing_id       TEXT NOT NULL,
			counselor_hash   TEXT NOT NULL,
			counselor_pubkey TEXT NOT NULL DEFAULT '',
			status           TEXT NOT NULL DEFAULT 'pending',
			created_at       INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE chat_rooms (
			id               TEXT PRIMARY KEY,
			listing_id       TEXT,
			response_id      TEXT,
			client_hash      TEXT NOT NULL,
			counselor_hash   TEXT NOT NULL,
			client_pubkey    TEXT NOT NULL DEFAULT '',
			counselor_pubkey TEXT NOT NULL DEFAULT '',
			started_at       INTEGER NOT NULL DEFAULT 0,
			expires_at       INTEGER NOT NULL DEFAULT 0,
			closed_at        INTEGER,
			closed_by        TEXT,
			peer_left_at     INTEGER,
			client_left_at   INTEGER,
			listing_counted  INTEGER NOT NULL DEFAULT 0,
			status           TEXT NOT NULL DEFAULT 'active'
		)`,
		`CREATE TABLE wallet_sessions (
			wallet_hash        TEXT PRIMARY KEY,
			wallet_address_enc TEXT NOT NULL DEFAULT '',
			currency           TEXT NOT NULL DEFAULT 'BTC',
			role               TEXT NOT NULL,
			balance_status     TEXT DEFAULT 'ok',
			min_required_usd   REAL NOT NULL DEFAULT 1000,
			balance_usd        REAL DEFAULT 0,
			last_checked_at    INTEGER,
			low_since          INTEGER,
			verified           BOOLEAN DEFAULT FALSE,
			first_seen         INTEGER NOT NULL DEFAULT 0,
			created_at         INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE invoices (
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
		)`,
		`CREATE TABLE invoice_index (
			currency    TEXT PRIMARY KEY,
			next_index  INTEGER NOT NULL DEFAULT 0
		)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("accept schema: %v\nSQL: %s", err, q)
		}
	}
	return db
}

func acceptServer(t *testing.T, db *sql.DB) *httptest.Server {
	t.Helper()
	prices := ncrypto.NewPriceCache(5 * time.Minute)
	prices.SetDevPrices(100000.0, 100.0)
	wallet, err := ncrypto.NewHDWallet(db, "", "") // dev mode — placeholder addresses
	if err != nil {
		t.Fatalf("new wallet: %v", err)
	}
	h := &Handler{
		DB:           db,
		DevMode:      true,
		HashKey:      visHashKey,
		WalletEncKey: acceptEncKey,
		Prices:       prices,
		Wallet:       wallet,
	}
	requireSess := middleware.RequireSession(db, true, visHashKey)
	r := chi.NewRouter()
	r.With(requireSess).Post("/response/{id}/accept", h.AcceptResponse)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// seedAcceptCounselor inserts a wallet_sessions row for a counselor with encrypted address.
func seedAcceptCounselor(t *testing.T, db *sql.DB, walletAddr string) string {
	t.Helper()
	hash := visHash(walletAddr)
	enc, err := ncrypto.EncryptAddress(acceptEncKey, walletAddr)
	if err != nil {
		t.Fatalf("encrypt counselor addr: %v", err)
	}
	now := time.Now().Unix()
	_, err = db.Exec(`INSERT INTO wallet_sessions
		(wallet_hash, wallet_address_enc, currency, role, min_required_usd, first_seen, created_at)
		VALUES (?, ?, 'BTC', 'peer', 2000, ?, ?)`, hash, enc, now, now)
	if err != nil {
		t.Fatalf("seed counselor session: %v", err)
	}
	return hash
}

// seedClientSession inserts wallet_sessions row for a client and returns hash + session token.
func seedClientSession(t *testing.T, db *sql.DB, walletAddr string) (hash, token string) {
	t.Helper()
	hash = visHash(walletAddr)
	enc, err := ncrypto.EncryptAddress(acceptEncKey, walletAddr)
	if err != nil {
		t.Fatalf("encrypt client addr: %v", err)
	}
	now := time.Now().Unix()
	_, err = db.Exec(`INSERT INTO wallet_sessions
		(wallet_hash, wallet_address_enc, currency, role, min_required_usd, first_seen, created_at)
		VALUES (?, ?, 'BTC', 'client', 150, ?, ?)`, hash, enc, now, now)
	if err != nil {
		t.Fatalf("seed client session: %v", err)
	}
	token = "accept-test-client-token-" + walletAddr
	seedVisSession(t, db, middleware.HashToken(token), hash, "client")
	return hash, token
}

func doAccept(t *testing.T, srv *httptest.Server, responseID, token, walletAddr, role string) *http.Response {
	t.Helper()
	body := `{"client_pubkey":"deadbeef0102030405060708090a0b0c0d0e0f10"}`
	req, _ := http.NewRequest("POST", srv.URL+"/response/"+responseID+"/accept", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if walletAddr != "" {
		req.Header.Set("X-Dev-Wallet", walletAddr)
		req.Header.Set("X-Dev-Role", role)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST accept: %v", err)
	}
	return resp
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// ACC-1: Client may accept Helper B while Chat A is active (count=1, response_A has a room).
func TestAccept_AllowsSecondHelperWhileFirstChatActive(t *testing.T) {
	db := openAcceptTestDB(t)
	srv := acceptServer(t, db)
	now := time.Now().Unix()

	clientHash, clientToken := seedClientSession(t, db, "acc1-client")
	counselorA := seedAcceptCounselor(t, db, "acc1-peer-A")
	counselorB := seedAcceptCounselor(t, db, "acc1-peer-B")

	// Listing with 1 chat already opened
	_, _ = db.Exec(`INSERT INTO listings (id, wallet_hash, status, opened_chats_count, visible_until, created_at)
		VALUES ('acc-list-1', ?, 'active', 1, ?, ?)`, clientHash, now+3600, now)

	// response_A: accepted, HAS a chat room (not an unpaid reservation)
	_, _ = db.Exec(`INSERT INTO responses (id, listing_id, counselor_hash, status, created_at)
		VALUES ('acc-resp-A', 'acc-list-1', ?, 'accepted', ?)`, counselorA, now)
	_, _ = db.Exec(`INSERT INTO chat_rooms (id, listing_id, response_id, client_hash, counselor_hash, started_at, expires_at, status)
		VALUES ('acc-room-A', 'acc-list-1', 'acc-resp-A', ?, ?, ?, ?, 'active')`,
		clientHash, counselorA, now-100, now+3600)

	// response_B: pending, waiting to be accepted
	_, _ = db.Exec(`INSERT INTO responses (id, listing_id, counselor_hash, status, created_at)
		VALUES ('acc-resp-B', 'acc-list-1', ?, 'pending', ?)`, counselorB, now)

	resp := doAccept(t, srv, "acc-resp-B", clientToken, "", "")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("ACC-1 FAIL: expected 200 (second helper accepted while chat A active), got %d", resp.StatusCode)
	}

	// Verify invoice was created for response_B
	var invoiceCount int
	db.QueryRow(`SELECT COUNT(*) FROM invoices WHERE response_id = 'acc-resp-B'`).Scan(&invoiceCount)
	if invoiceCount != 1 {
		t.Fatalf("ACC-1 FAIL: expected 1 invoice for response_B, got %d", invoiceCount)
	}
}

// ACC-2: Acceptance blocked when another accepted response has an unpaid invoice (no chat room yet).
func TestAccept_BlockedByUnpaidInvoice(t *testing.T) {
	db := openAcceptTestDB(t)
	srv := acceptServer(t, db)
	now := time.Now().Unix()

	clientHash, clientToken := seedClientSession(t, db, "acc2-client")
	counselorA := seedAcceptCounselor(t, db, "acc2-peer-A")
	counselorB := seedAcceptCounselor(t, db, "acc2-peer-B")

	_, _ = db.Exec(`INSERT INTO listings (id, wallet_hash, status, opened_chats_count, visible_until, created_at)
		VALUES ('acc-list-2', ?, 'active', 0, ?, ?)`, clientHash, now+3600, now)

	// response_A: accepted, NO chat room yet (unpaid reservation in flight)
	_, _ = db.Exec(`INSERT INTO responses (id, listing_id, counselor_hash, status, created_at)
		VALUES ('acc-resp-A2', 'acc-list-2', ?, 'accepted', ?)`, counselorA, now)

	// response_B: pending
	_, _ = db.Exec(`INSERT INTO responses (id, listing_id, counselor_hash, status, created_at)
		VALUES ('acc-resp-B2', 'acc-list-2', ?, 'pending', ?)`, counselorB, now)

	resp := doAccept(t, srv, "acc-resp-B2", clientToken, "", "")
	defer resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("ACC-2 FAIL: expected 409 (unpaid reservation blocks), got %d", resp.StatusCode)
	}
}

// ACC-3: Pending responses are preserved after the first Helper is accepted.
// Previously all pending responses were rejected immediately on any acceptance.
func TestAccept_PreservesPendingResponses(t *testing.T) {
	db := openAcceptTestDB(t)
	srv := acceptServer(t, db)
	now := time.Now().Unix()

	clientHash, clientToken := seedClientSession(t, db, "acc3-client")
	counselorA := seedAcceptCounselor(t, db, "acc3-peer-A")
	counselorB := seedAcceptCounselor(t, db, "acc3-peer-B")

	_, _ = db.Exec(`INSERT INTO listings (id, wallet_hash, status, opened_chats_count, visible_until, created_at)
		VALUES ('acc-list-3', ?, 'active', 0, ?, ?)`, clientHash, now+3600, now)
	_, _ = db.Exec(`INSERT INTO responses (id, listing_id, counselor_hash, status, created_at)
		VALUES ('acc-resp-A3', 'acc-list-3', ?, 'pending', ?)`, counselorA, now)
	_, _ = db.Exec(`INSERT INTO responses (id, listing_id, counselor_hash, status, created_at)
		VALUES ('acc-resp-B3', 'acc-list-3', ?, 'pending', ?)`, counselorB, now)

	// Accept response_A
	resp := doAccept(t, srv, "acc-resp-A3", clientToken, "", "")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("ACC-3 FAIL: expected 200 on accept, got %d", resp.StatusCode)
	}

	// response_B must still be 'pending' (not rejected)
	var status string
	db.QueryRow(`SELECT status FROM responses WHERE id = 'acc-resp-B3'`).Scan(&status)
	if status != "pending" {
		t.Fatalf("ACC-3 FAIL: response_B should still be 'pending', got %q", status)
	}
}

// ACC-4: Acceptance blocked when listing is at count=2 (fully consumed).
func TestAccept_BlockedWhenListingAtCount2(t *testing.T) {
	db := openAcceptTestDB(t)
	srv := acceptServer(t, db)
	now := time.Now().Unix()

	clientHash, clientToken := seedClientSession(t, db, "acc4-client")
	counselorC := seedAcceptCounselor(t, db, "acc4-peer-C")

	_, _ = db.Exec(`INSERT INTO listings (id, wallet_hash, status, opened_chats_count, visible_until, created_at)
		VALUES ('acc-list-4', ?, 'closed', 2, ?, ?)`, clientHash, now+3600, now)
	_, _ = db.Exec(`INSERT INTO responses (id, listing_id, counselor_hash, status, created_at)
		VALUES ('acc-resp-C4', 'acc-list-4', ?, 'pending', ?)`, counselorC, now)

	resp := doAccept(t, srv, "acc-resp-C4", clientToken, "", "")
	defer resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("ACC-4 FAIL: expected 409 (count=2 blocks further acceptance), got %d", resp.StatusCode)
	}
}

// ACC-5: Client with active chat in a DIFFERENT listing is blocked from accepting in this one.
func TestAccept_BlockedByActiveChatInOtherListing(t *testing.T) {
	db := openAcceptTestDB(t)
	srv := acceptServer(t, db)
	now := time.Now().Unix()

	clientHash, clientToken := seedClientSession(t, db, "acc5-client")
	counselorD := seedAcceptCounselor(t, db, "acc5-peer-D")

	// Listing 1: has active chat (client is in it)
	_, _ = db.Exec(`INSERT INTO listings (id, wallet_hash, status, opened_chats_count, visible_until, created_at)
		VALUES ('acc-other-list', ?, 'active', 1, ?, ?)`, clientHash, now+3600, now)
	_, _ = db.Exec(`INSERT INTO chat_rooms (id, listing_id, client_hash, counselor_hash, started_at, expires_at, status)
		VALUES ('acc-other-room', 'acc-other-list', ?, ?, ?, ?, 'active')`,
		clientHash, counselorD, now-100, now+3600)

	// Listing 2: new listing, client trying to accept here too
	_, _ = db.Exec(`INSERT INTO listings (id, wallet_hash, status, opened_chats_count, visible_until, created_at)
		VALUES ('acc-list-5', ?, 'active', 0, ?, ?)`, clientHash, now+3600, now)
	_, _ = db.Exec(`INSERT INTO responses (id, listing_id, counselor_hash, status, created_at)
		VALUES ('acc-resp-D5', 'acc-list-5', ?, 'pending', ?)`, counselorD, now)

	resp := doAccept(t, srv, "acc-resp-D5", clientToken, "", "")
	defer resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("ACC-5 FAIL: expected 409 (active chat in different listing blocks), got %d", resp.StatusCode)
	}
}
