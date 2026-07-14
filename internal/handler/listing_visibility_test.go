package handler

// Tests for listing visibility model (MTask 2026-07-12):
//   - Listings stay 'active' when first chat opens (NOT set to 'matched').
//   - Board shows listing even while first chat is active.
//   - Board hides listing only after opened_chats_count reaches 2 (status='closed').
//   - RenewListing: free renewal while opened_chats_count < 2; blocked at count=2 or wrong wallet.
//   - ResumeChat: fallback returns owner's expired listing with can_renew field.
//   - CloseChat: first close does not change listing status; second close sets 'closed'.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	_ "modernc.org/sqlite"

	ncrypto "naroom/internal/crypto"
	"naroom/internal/middleware"
)

// visHashKey is the HMAC key used by all vis tests (matches visServer setup).
var visHashKey = []byte("testkey")

// visHash computes the wallet HMAC hash the same way dev-mode middleware does.
func visHash(walletAddr string) string {
	return ncrypto.WalletHash(visHashKey, walletAddr)
}

// ── schema & helpers ─────────────────────────────────────────────────────────

func openVisTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`CREATE TABLE sessions (
			token_hash   TEXT PRIMARY KEY,
			wallet_hash  TEXT NOT NULL,
			currency     TEXT, role TEXT,
			created_at   INTEGER, expires_at INTEGER, revoked_at INTEGER,
			last_seen_at INTEGER, revoked_by TEXT,
			principal_id TEXT
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
			renewal_count       INTEGER NOT NULL DEFAULT 0,
			is_sample           INTEGER NOT NULL DEFAULT 0,
			first_activated_at  INTEGER,
			owner_principal_id  TEXT
		)`,
		`CREATE TABLE chat_rooms (
			id                     TEXT PRIMARY KEY,
			listing_id             TEXT,
			response_id            TEXT,
			client_hash            TEXT NOT NULL,
			counselor_hash         TEXT NOT NULL,
			client_pubkey          TEXT NOT NULL DEFAULT '',
			counselor_pubkey       TEXT NOT NULL DEFAULT '',
			started_at             INTEGER NOT NULL DEFAULT 0,
			expires_at             INTEGER NOT NULL DEFAULT 0,
			closed_at              INTEGER,
			closed_by              TEXT,
			peer_left_at           INTEGER,
			client_left_at         INTEGER,
			listing_counted        INTEGER NOT NULL DEFAULT 0,
			status                 TEXT NOT NULL DEFAULT 'active',
			client_principal_id    TEXT,
			counselor_principal_id TEXT
		)`,
		`CREATE TABLE responses (
			id                      TEXT PRIMARY KEY,
			listing_id              TEXT NOT NULL,
			counselor_hash          TEXT NOT NULL,
			counselor_pubkey        TEXT NOT NULL DEFAULT '',
			status                  TEXT NOT NULL DEFAULT 'pending',
			created_at              INTEGER NOT NULL DEFAULT 0,
			counselor_principal_id  TEXT
		)`,
		`CREATE TABLE reputation (
			counselor_hash     TEXT PRIMARY KEY,
			sessions_total     INTEGER NOT NULL DEFAULT 0,
			sessions_completed INTEGER NOT NULL DEFAULT 0,
			sessions_early_exit INTEGER NOT NULL DEFAULT 0,
			thumbs_up          INTEGER NOT NULL DEFAULT 0,
			thumbs_down        INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE review_tokens (
			token          TEXT PRIMARY KEY,
			counselor_hash TEXT NOT NULL,
			is_paid        INTEGER NOT NULL DEFAULT 0,
			used           INTEGER NOT NULL DEFAULT 0,
			created_at     INTEGER NOT NULL DEFAULT 0,
			expires_at     INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE encrypted_messages (
			id           TEXT PRIMARY KEY,
			room_id      TEXT NOT NULL,
			sender_pubkey TEXT NOT NULL,
			nonce        TEXT NOT NULL,
			ciphertext   TEXT NOT NULL,
			msg_type     TEXT DEFAULT 'text',
			created_at   INTEGER NOT NULL
		)`,
		`CREATE TABLE invoices (
			id                  TEXT PRIMARY KEY,
			type                TEXT NOT NULL,
			address             TEXT NOT NULL DEFAULT '',
			amount_usd          REAL NOT NULL DEFAULT 0,
			amount_crypto       TEXT NOT NULL DEFAULT '0',
			currency            TEXT NOT NULL DEFAULT 'BTC',
			payer_address       TEXT,
			txid                TEXT,
			status              TEXT NOT NULL DEFAULT 'pending',
			listing_id          TEXT,
			response_id         TEXT,
			client_pubkey       TEXT,
			chat_room_id        TEXT,
			payment_detected_at INTEGER,
			price_at_creation   REAL,
			payer_principal_id  TEXT,
			created_at          INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE helper_board_subscriptions (
			id               TEXT PRIMARY KEY,
			telegram_chat_id TEXT NOT NULL,
			counselor_hash   TEXT,
			city             TEXT,
			language         TEXT,
			problem          TEXT,
			help_type        TEXT,
			urgency          TEXT,
			created_at       INTEGER NOT NULL,
			expires_at       INTEGER NOT NULL,
			active           BOOLEAN DEFAULT TRUE
		)`,
		`CREATE TABLE client_listing_notifications (
			id         TEXT PRIMARY KEY,
			listing_id TEXT NOT NULL,
			active     BOOLEAN DEFAULT TRUE,
			expires_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE principals (
			id            TEXT PRIMARY KEY,
			recovery_hash TEXT NOT NULL,
			role          TEXT NOT NULL,
			wallet_hash   TEXT,
			currency      TEXT,
			created_at    INTEGER NOT NULL DEFAULT 0,
			last_seen     INTEGER
		)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return db
}

func visServer(t *testing.T, db *sql.DB) *httptest.Server {
	t.Helper()
	h := &Handler{DB: db, DevMode: true, ListingTTL: 86400}
	requireSess := middleware.RequireSession(db, true, []byte("testkey"))
	r := chi.NewRouter()
	r.With(requireSess).Get("/board/{city}", h.Board)
	r.With(requireSess).Post("/listing/{id}/renew", h.RenewListing)
	r.With(requireSess).Get("/resume", h.ResumeChat)
	r.With(requireSess).Post("/chat/{room_id}/close", h.CloseChat)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func seedVisSession(t *testing.T, db *sql.DB, tokenHash, walletHash, role string) {
	t.Helper()
	seedVisSessionWithPrincipal(t, db, tokenHash, walletHash, role, "")
}

// seedVisSessionWithPrincipal inserts a session row with optional principal_id.
func seedVisSessionWithPrincipal(t *testing.T, db *sql.DB, tokenHash, walletHash, role, principalID string) {
	t.Helper()
	now := time.Now().Unix()
	var err error
	if principalID != "" {
		_, err = db.Exec(`INSERT INTO sessions (token_hash, wallet_hash, role, created_at, expires_at, principal_id)
			VALUES (?, ?, ?, ?, ?, ?)`, tokenHash, walletHash, role, now, now+86400, principalID)
	} else {
		_, err = db.Exec(`INSERT INTO sessions (token_hash, wallet_hash, role, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?)`, tokenHash, walletHash, role, now, now+86400)
	}
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

// seedVisPrincipal inserts a principal row and returns its ID.
func seedVisPrincipal(t *testing.T, db *sql.DB, id, walletHash, role string) string {
	t.Helper()
	now := time.Now().Unix()
	_, err := db.Exec(`INSERT INTO principals (id, recovery_hash, role, wallet_hash, created_at)
		VALUES (?, ?, ?, ?, ?)`, id, "testhash-"+id, role, walletHash, now)
	if err != nil {
		t.Fatalf("seed principal: %v", err)
	}
	return id
}

// seedVisListing inserts a listing. walletAddr is the raw wallet address; it is hashed internally.
// Also creates a principal and links owner_principal_id so strict auth checks pass.
func seedVisListing(t *testing.T, db *sql.DB, id, walletAddr, status string, openedChats int, visibleUntil int64) {
	t.Helper()
	now := time.Now().Unix()
	hash := visHash(walletAddr)
	principalID := "prn-vis-" + id
	seedVisPrincipal(t, db, principalID, hash, "client")
	_, err := db.Exec(`INSERT INTO listings
		(id, wallet_hash, owner_principal_id, status, opened_chats_count, visible_until, created_at, first_activated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, hash, principalID, status, openedChats, visibleUntil, now-3600, now-3600)
	if err != nil {
		t.Fatalf("seed listing: %v", err)
	}
}

// seedVisListingWithToken seeds a listing and returns a Bearer token that includes principal_id.
// Use this for tests that call RenewListing via doPost (dev-mode wallet headers won't set principal_id).
func seedVisListingWithToken(t *testing.T, db *sql.DB, id, walletAddr, status string, openedChats int, visibleUntil int64) string {
	t.Helper()
	seedVisListing(t, db, id, walletAddr, status, openedChats, visibleUntil)
	hash := visHash(walletAddr)
	principalID := "prn-vis-" + id
	tokenRaw := "vis-bearer-token-" + id
	tokenHash := middleware.HashToken(tokenRaw)
	seedVisSessionWithPrincipal(t, db, tokenHash, hash, "client", principalID)
	return tokenRaw
}

func seedVisChatRoom(t *testing.T, db *sql.DB, roomID, listingID, clientHash, counselorHash string) {
	t.Helper()
	now := time.Now().Unix()
	_, err := db.Exec(`INSERT INTO chat_rooms
		(id, listing_id, client_hash, counselor_hash, started_at, expires_at, status)
		VALUES (?, ?, ?, ?, ?, ?, 'active')`,
		roomID, listingID, clientHash, counselorHash, now, now+86400)
	if err != nil {
		t.Fatalf("seed chat room: %v", err)
	}
}

func seedVisResponse(t *testing.T, db *sql.DB, respID, listingID, counselorHash string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO responses (id, listing_id, counselor_hash, status, created_at)
		VALUES (?, ?, ?, 'accepted', ?)`, respID, listingID, counselorHash, time.Now().Unix())
	if err != nil {
		t.Fatalf("seed response: %v", err)
	}
}

func devAuthHeader(walletAddr, role string) string {
	// Dev-mode bearer is empty; we use X-Dev-Wallet + X-Dev-Role instead.
	// But RequireSession checks for Bearer first. Use a fake token + dev wallet header combo.
	_ = walletAddr
	_ = role
	return ""
}

// doGet sends a GET request with dev-mode wallet headers.
func doGet(t *testing.T, srv *httptest.Server, path, walletAddr, role string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("X-Dev-Wallet", walletAddr)
	req.Header.Set("X-Dev-Role", role)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// doPost sends a POST request with dev-mode wallet headers and optional body.
func doPost(t *testing.T, srv *httptest.Server, path, walletAddr, role, body string) *http.Response {
	t.Helper()
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("{}")
	}
	req, _ := http.NewRequest("POST", srv.URL+path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dev-Wallet", walletAddr)
	req.Header.Set("X-Dev-Role", role)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// doPostWithBearer sends a POST request using a Bearer token (for principal-aware auth).
func doPostWithBearer(t *testing.T, srv *httptest.Server, path, bearerToken, body string) *http.Response {
	t.Helper()
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("{}")
	}
	req, _ := http.NewRequest("POST", srv.URL+path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// ── Board visibility tests ────────────────────────────────────────────────────

// VIS-1: Listing stays visible on the board even while first chat is active.
// Previously the NOT EXISTS filter would hide it.
func TestBoard_ShowsListingWithFirstChatActive(t *testing.T) {
	db := openVisTestDB(t)
	srv := visServer(t, db)

	now := time.Now().Unix()
	// Active listing with 1 opened chat (still has room for second peer)
	seedVisListing(t, db, "vis-list-1", "client-1", "active", 1, now+3600)
	seedVisChatRoom(t, db, "vis-room-1", "vis-list-1", "client-1", "counselor-1")

	resp := doGet(t, srv, "/board/tbilisi", "anyuser", "client")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var listings []map[string]any
	json.NewDecoder(resp.Body).Decode(&listings)
	for _, l := range listings {
		if l["id"] == "vis-list-1" {
			return // found
		}
	}
	t.Fatal("VIS-1 FAIL: listing with active chat should appear on board, but was not found")
}

// VIS-2: Listing disappears from board after second chat (status='closed').
func TestBoard_HidesListingAfterSecondChat(t *testing.T) {
	db := openVisTestDB(t)
	srv := visServer(t, db)

	now := time.Now().Unix()
	seedVisListing(t, db, "vis-list-2", "client-2", "closed", 2, now+3600)

	resp := doGet(t, srv, "/board/tbilisi", "anyuser", "client")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var listings []map[string]any
	json.NewDecoder(resp.Body).Decode(&listings)
	for _, l := range listings {
		if l["id"] == "vis-list-2" {
			t.Fatal("VIS-2 FAIL: closed listing (2 chats) should NOT appear on board")
		}
	}
}

// VIS-3: Board hides listings with opened_chats_count=2 even if status='active' (safety guard).
func TestBoard_HidesListingWithCount2ActiveStatus(t *testing.T) {
	db := openVisTestDB(t)
	srv := visServer(t, db)

	now := time.Now().Unix()
	// Edge case: status='active' but count=2 (should not happen in practice after this fix)
	seedVisListing(t, db, "vis-list-guard", "client-guard", "active", 2, now+3600)

	resp := doGet(t, srv, "/board/tbilisi", "anyuser", "client")
	defer resp.Body.Close()
	var listings []map[string]any
	json.NewDecoder(resp.Body).Decode(&listings)
	for _, l := range listings {
		if l["id"] == "vis-list-guard" {
			t.Fatal("VIS-3 FAIL: listing with opened_chats_count=2 should not appear on board")
		}
	}
}

// ── RenewListing tests ────────────────────────────────────────────────────────

// VIS-4: Renewal allowed when opened_chats_count=0 (no chats yet).
func TestRenew_AllowedWhenCount0(t *testing.T) {
	db := openVisTestDB(t)
	srv := visServer(t, db)

	now := time.Now().Unix()
	token := seedVisListingWithToken(t, db, "ren-list-0", "owner-wallet-0", "expired", 0, now-100)

	resp := doPostWithBearer(t, srv, "/listing/ren-list-0/renew", token, "")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("VIS-4 FAIL: expected 200, got %d", resp.StatusCode)
	}
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if result["status"] != "renewed" {
		t.Fatalf("expected status=renewed, got %v", result["status"])
	}
}

// VIS-5: Renewal allowed when opened_chats_count=1 (one chat still in progress).
func TestRenew_AllowedWhenCount1(t *testing.T) {
	db := openVisTestDB(t)
	srv := visServer(t, db)

	now := time.Now().Unix()
	token := seedVisListingWithToken(t, db, "ren-list-1", "owner-wallet-1", "expired", 1, now-100)

	resp := doPostWithBearer(t, srv, "/listing/ren-list-1/renew", token, "")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("VIS-5 FAIL: expected 200, got %d", resp.StatusCode)
	}
}

// VIS-6: Renewal blocked when opened_chats_count=2 (listing fully consumed).
func TestRenew_BlockedWhenCount2(t *testing.T) {
	db := openVisTestDB(t)
	srv := visServer(t, db)

	now := time.Now().Unix()
	token := seedVisListingWithToken(t, db, "ren-list-2", "owner-wallet-2", "expired", 2, now-100)

	resp := doPostWithBearer(t, srv, "/listing/ren-list-2/renew", token, "")
	defer resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("VIS-6 FAIL: expected 409 for count=2, got %d", resp.StatusCode)
	}
}

// VIS-7: Renewal blocked for wrong wallet (different principal).
func TestRenew_BlockedWrongWallet(t *testing.T) {
	db := openVisTestDB(t)
	srv := visServer(t, db)

	now := time.Now().Unix()
	// Seed the listing with "true-owner" principal
	seedVisListingWithToken(t, db, "ren-list-w", "true-owner", "expired", 0, now-100)

	// Seed a different principal for "wrong-wallet"
	wrongHash := visHash("wrong-wallet")
	wrongPrincipalID := "prn-vis-wrong"
	seedVisPrincipal(t, db, wrongPrincipalID, wrongHash, "client")
	wrongToken := "vis-bearer-token-wrong"
	seedVisSessionWithPrincipal(t, db, middleware.HashToken(wrongToken), wrongHash, "client", wrongPrincipalID)

	resp := doPostWithBearer(t, srv, "/listing/ren-list-w/renew", wrongToken, "")
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("VIS-7 FAIL: expected 403 for wrong wallet, got %d", resp.StatusCode)
	}
}

// ── ResumeChat fallback tests ─────────────────────────────────────────────────

// VIS-8: /resume returns expired renewable listing when no active chat exists.
func TestResumeChat_FindsExpiredRenewableListing(t *testing.T) {
	db := openVisTestDB(t)
	srv := visServer(t, db)

	now := time.Now().Unix()
	principalID := "prn-vis-8-owner"
	ownerHash := visHash("resume-owner-8")
	// Insert principal and listing with owner_principal_id
	_, _ = db.Exec(`INSERT OR IGNORE INTO principals (id, recovery_hash, role, wallet_hash, created_at) VALUES (?, ?, 'client', ?, ?)`,
		principalID, "rh-vis-8", ownerHash, now)
	_, err := db.Exec(`INSERT INTO listings
		(id, wallet_hash, owner_principal_id, status, opened_chats_count, visible_until, created_at, first_activated_at)
		VALUES ('resume-list-1', ?, ?, 'expired', 0, ?, ?, ?)`,
		ownerHash, principalID, now-100, now-86400, now-86400)
	if err != nil {
		t.Fatalf("insert listing: %v", err)
	}
	// Session with principal_id
	tokenRaw := "resume-token-vis-8"
	seedVisSessionWithPrincipal(t, db, middleware.HashToken(tokenRaw), ownerHash, "client", principalID)

	req, _ := http.NewRequest("GET", srv.URL+"/resume", nil)
	req.Header.Set("Authorization", "Bearer "+tokenRaw)
	resp, err2 := http.DefaultClient.Do(req)
	if err2 != nil {
		t.Fatalf("GET /resume: %v", err2)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("VIS-8 FAIL: expected 200, got %d", resp.StatusCode)
	}
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if result["listing_id"] != "resume-list-1" {
		t.Fatalf("VIS-8 FAIL: expected listing_id=resume-list-1, got %v", result["listing_id"])
	}
	if result["listing_status"] != "expired" {
		t.Fatalf("VIS-8 FAIL: expected listing_status=expired, got %v", result["listing_status"])
	}
	if result["can_renew"] != true {
		t.Fatalf("VIS-8 FAIL: expected can_renew=true, got %v", result["can_renew"])
	}
}

// VIS-9: /resume active chat takes priority over expired listing.
func TestResumeChat_ActiveChatTakesPriority(t *testing.T) {
	db := openVisTestDB(t)
	srv := visServer(t, db)

	now := time.Now().Unix()
	clientPrincipalID := "prn-vis-9-client"
	peerPrincipalID := "prn-vis-9-peer"
	ownerHash2 := visHash("resume-owner-9")
	peerHash2 := visHash("peer-9")

	_, _ = db.Exec(`INSERT OR IGNORE INTO principals (id, recovery_hash, role, wallet_hash, created_at) VALUES (?, 'rh-vis-9c', 'client', ?, ?)`, clientPrincipalID, ownerHash2, now)
	_, _ = db.Exec(`INSERT OR IGNORE INTO principals (id, recovery_hash, role, wallet_hash, created_at) VALUES (?, 'rh-vis-9p', 'peer', ?, ?)`, peerPrincipalID, peerHash2, now)

	// Both an expired listing and an active chat room exist
	_, err := db.Exec(`INSERT INTO listings
		(id, wallet_hash, owner_principal_id, status, opened_chats_count, visible_until, created_at, first_activated_at)
		VALUES ('resume-list-2', ?, ?, 'expired', 0, ?, ?, ?)`,
		ownerHash2, clientPrincipalID, now-100, now-86400, now-86400)
	if err != nil {
		t.Fatalf("insert listing: %v", err)
	}
	// Chat room with client_principal_id so ResumeChat finds it
	_, _ = db.Exec(`INSERT INTO chat_rooms
		(id, listing_id, client_hash, counselor_hash, started_at, expires_at, status, client_principal_id, counselor_principal_id)
		VALUES ('resume-room-2', 'resume-list-2', ?, ?, ?, ?, 'active', ?, ?)`,
		ownerHash2, peerHash2, now, now+86400, clientPrincipalID, peerPrincipalID)

	tokenRaw := "resume-token-vis-9"
	seedVisSessionWithPrincipal(t, db, middleware.HashToken(tokenRaw), ownerHash2, "client", clientPrincipalID)

	req, _ := http.NewRequest("GET", srv.URL+"/resume", nil)
	req.Header.Set("Authorization", "Bearer "+tokenRaw)
	resp, err2 := http.DefaultClient.Do(req)
	if err2 != nil {
		t.Fatalf("GET /resume: %v", err2)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("VIS-9 FAIL: expected 200, got %d", resp.StatusCode)
	}
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if result["room_id"] != "resume-room-2" {
		t.Fatalf("VIS-9 FAIL: expected room_id=resume-room-2 (chat priority), got %v", result)
	}
}

// ── CloseChat listing status tests ───────────────────────────────────────────

// VIS-10: Closing the first chat leaves listing status unchanged (stays 'active').
// Room starts as 'peer_left' so the client's close triggers the full-close path
// (not just first-departure). With count=1, listing must remain 'active'.
func TestCloseChat_ListingStaysActive_OnFirstClose(t *testing.T) {
	db := openVisTestDB(t)
	now := time.Now().Unix()

	clientHash1 := visHash("close-client-1")
	peerHash1 := visHash("close-peer-1")
	clientPrnID1 := "prn-close-client-1"
	peerPrnID1 := "prn-close-peer-1"

	_, _ = db.Exec(`INSERT OR IGNORE INTO principals (id, recovery_hash, role, wallet_hash, created_at) VALUES (?, 'rh-c1', 'client', ?, ?)`, clientPrnID1, clientHash1, now)
	_, _ = db.Exec(`INSERT OR IGNORE INTO principals (id, recovery_hash, role, wallet_hash, created_at) VALUES (?, 'rh-p1', 'peer', ?, ?)`, peerPrnID1, peerHash1, now)

	_, _ = db.Exec(`INSERT INTO listings
		(id, wallet_hash, status, opened_chats_count, visible_until, created_at)
		VALUES ('close-list-1', ?, 'active', 1, ?, ?)`, clientHash1, now+3600, now-3600)
	_, _ = db.Exec(`INSERT INTO responses (id, listing_id, counselor_hash, status, created_at)
		VALUES ('close-resp-1', 'close-list-1', ?, 'accepted', ?)`, peerHash1, now)
	// Peer already left; client closing is the final departure (full-close path)
	_, _ = db.Exec(`INSERT INTO chat_rooms
		(id, listing_id, response_id, client_hash, counselor_hash, started_at, expires_at, status, peer_left_at,
		 client_principal_id, counselor_principal_id)
		VALUES ('close-room-1', 'close-list-1', 'close-resp-1', ?, ?, ?, ?, 'peer_left', ?, ?, ?)`,
		clientHash1, peerHash1, now-200, now+3600, now-100, clientPrnID1, peerPrnID1)
	_, _ = db.Exec(`INSERT INTO reputation (counselor_hash) VALUES (?)`, peerHash1)

	seedVisSessionWithPrincipal(t, db, middleware.HashToken("close-token-1"), clientHash1, "client", clientPrnID1)
	srv := visServer(t, db)

	req, _ := http.NewRequest("POST", srv.URL+"/chat/close-room-1/close", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer close-token-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST close: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("VIS-10: expected 200 from CloseChat, got %d", resp.StatusCode)
	}

	var status string
	db.QueryRow(`SELECT status FROM listings WHERE id = 'close-list-1'`).Scan(&status)
	if status != "active" {
		t.Fatalf("VIS-10 FAIL: expected listing status='active' after first chat close, got %q", status)
	}
}

// VIS-11: Closing the second chat permanently closes the listing.
// Room starts as 'peer_left' so the client's close is the final (second) departure.
func TestCloseChat_ListingClosed_OnSecondClose(t *testing.T) {
	db := openVisTestDB(t)
	now := time.Now().Unix()

	clientHash2 := visHash("close-client-2")
	peerHash2 := visHash("close-peer-2")
	clientPrnID2 := "prn-close-client-2"
	peerPrnID2 := "prn-close-peer-2"

	_, _ = db.Exec(`INSERT OR IGNORE INTO principals (id, recovery_hash, role, wallet_hash, created_at) VALUES (?, 'rh-c2', 'client', ?, ?)`, clientPrnID2, clientHash2, now)
	_, _ = db.Exec(`INSERT OR IGNORE INTO principals (id, recovery_hash, role, wallet_hash, created_at) VALUES (?, 'rh-p2', 'peer', ?, ?)`, peerPrnID2, peerHash2, now)

	_, _ = db.Exec(`INSERT INTO listings
		(id, wallet_hash, status, opened_chats_count, visible_until, created_at)
		VALUES ('close-list-2', ?, 'active', 2, ?, ?)`, clientHash2, now+3600, now-3600)
	_, _ = db.Exec(`INSERT INTO responses (id, listing_id, counselor_hash, status, created_at)
		VALUES ('close-resp-2', 'close-list-2', ?, 'accepted', ?)`, peerHash2, now)
	// Peer already left (peer_left) — client closing is the final departure
	_, _ = db.Exec(`INSERT INTO chat_rooms
		(id, listing_id, response_id, client_hash, counselor_hash, started_at, expires_at, status, peer_left_at,
		 client_principal_id, counselor_principal_id)
		VALUES ('close-room-2', 'close-list-2', 'close-resp-2', ?, ?, ?, ?, 'peer_left', ?, ?, ?)`,
		clientHash2, peerHash2, now-200, now+3600, now-100, clientPrnID2, peerPrnID2)
	_, _ = db.Exec(`INSERT INTO reputation (counselor_hash) VALUES (?)`, peerHash2)

	seedVisSessionWithPrincipal(t, db, middleware.HashToken("close-token-2"), clientHash2, "client", clientPrnID2)
	srv := visServer(t, db)

	req, _ := http.NewRequest("POST", srv.URL+"/chat/close-room-2/close", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer close-token-2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST close: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("VIS-11: expected 200 from CloseChat, got %d", resp.StatusCode)
	}

	var status string
	db.QueryRow(`SELECT status FROM listings WHERE id = 'close-list-2'`).Scan(&status)
	if status != "closed" {
		t.Fatalf("VIS-11 FAIL: expected listing status='closed' after second chat close, got %q", status)
	}
}

// ── Helpers for extended renewal tests ───────────────────────────────────────

// mockTelegramSender records how many times SendHelperMessage is called.
type mockTelegramSender struct {
	mu    sync.Mutex
	calls int
}

func (m *mockTelegramSender) SendHelperMessage(_ context.Context, _, _ string) error {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return nil
}

func (m *mockTelegramSender) SendClientMessage(_ context.Context, _, _ string) error { return nil }

func (m *mockTelegramSender) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// visServerFull is like visServer but accepts a Telegram sender for notification tests.
func visServerFull(t *testing.T, db *sql.DB, tg interface {
	SendClientMessage(ctx context.Context, chatID, text string) error
	SendHelperMessage(ctx context.Context, chatID, text string) error
}) *httptest.Server {
	t.Helper()
	h := &Handler{DB: db, DevMode: true, ListingTTL: 86400, Telegram: tg}
	requireSess := middleware.RequireSession(db, true, []byte("testkey"))
	r := chi.NewRouter()
	r.With(requireSess).Post("/listing/{id}/renew", h.RenewListing)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// ── Extended renewal tests ────────────────────────────────────────────────────

// seedVisListingRaw inserts a listing directly with a principal and returns a bearer token.
func seedVisListingRaw(t *testing.T, db *sql.DB, id, walletAddr, status string, openedChats int, visibleUntil, createdAt, firstActivatedAt int64) string {
	t.Helper()
	hash := visHash(walletAddr)
	principalID := "prn-raw-" + id
	seedVisPrincipal(t, db, principalID, hash, "client")
	_, err := db.Exec(`INSERT INTO listings
		(id, wallet_hash, owner_principal_id, status, opened_chats_count, visible_until, created_at, first_activated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, hash, principalID, status, openedChats, visibleUntil, createdAt, firstActivatedAt)
	if err != nil {
		t.Fatalf("seedVisListingRaw: %v", err)
	}
	tokenRaw := "vis-raw-bearer-" + id
	tokenHash := middleware.HashToken(tokenRaw)
	seedVisSessionWithPrincipal(t, db, tokenHash, hash, "client", principalID)
	return tokenRaw
}

// VIS-12: A listing activated more than 30 days ago with count=0 can still renew.
// Proves the old 30-day cutoff has been removed.
func TestRenew_OlderThan30Days_AllowedAtCount0(t *testing.T) {
	db := openVisTestDB(t)
	srv := visServer(t, db)
	now := time.Now().Unix()

	// first_activated_at = 60 days ago; visible_until already expired
	token := seedVisListingRaw(t, db, "old-list-0", "old-owner-0", "expired", 0, now-100, now-86400*60, now-86400*60)

	resp := doPostWithBearer(t, srv, "/listing/old-list-0/renew", token, "")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("VIS-12 FAIL: expected 200 for 60-day-old listing (no 30-day cutoff), got %d", resp.StatusCode)
	}
}

// VIS-13: A listing activated more than 30 days ago with count=1 can still renew.
func TestRenew_OlderThan30Days_AllowedAtCount1(t *testing.T) {
	db := openVisTestDB(t)
	srv := visServer(t, db)
	now := time.Now().Unix()

	token := seedVisListingRaw(t, db, "old-list-1", "old-owner-1", "expired", 1, now-100, now-86400*45, now-86400*45)

	resp := doPostWithBearer(t, srv, "/listing/old-list-1/renew", token, "")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("VIS-13 FAIL: expected 200 for 45-day-old listing with count=1, got %d", resp.StatusCode)
	}
}

// VIS-14: Early renewal is blocked when listing is active with > 1h visibility left.
func TestRenew_EarlyRenewalBlocked(t *testing.T) {
	db := openVisTestDB(t)
	srv := visServer(t, db)
	now := time.Now().Unix()

	// visible_until = now + 7200 (2 hours left) → more than the 1h threshold
	token := seedVisListingWithToken(t, db, "early-list", "early-owner", "active", 0, now+7200)

	resp := doPostWithBearer(t, srv, "/listing/early-list/renew", token, "")
	defer resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("VIS-14 FAIL: expected 409 (active listing has >1h left), got %d", resp.StatusCode)
	}
}

// VIS-15: Immediate duplicate renewal returns 409.
// After the first renewal (which extends visible_until by 24h), a second
// immediate call is blocked because the listing now has much more than 1h left.
func TestRenew_DuplicateRenewal(t *testing.T) {
	db := openVisTestDB(t)
	srv := visServer(t, db)
	now := time.Now().Unix()

	// Start expired so first renewal succeeds
	token := seedVisListingWithToken(t, db, "dup-list", "dup-owner", "expired", 0, now-100)

	// First renewal: must succeed
	r1 := doPostWithBearer(t, srv, "/listing/dup-list/renew", token, "")
	r1.Body.Close()
	if r1.StatusCode != 200 {
		t.Fatalf("VIS-15 FAIL: first renewal expected 200, got %d", r1.StatusCode)
	}

	// Second renewal immediately after: must return 409 (listing now fresh)
	r2 := doPostWithBearer(t, srv, "/listing/dup-list/renew", token, "")
	defer r2.Body.Close()
	if r2.StatusCode != 409 {
		t.Fatalf("VIS-15 FAIL: duplicate renewal expected 409, got %d", r2.StatusCode)
	}
}

// VIS-16: Successful renewal: asserts zero invoices, count unchanged, renewal_count+1, visible_until≈now+86400.
func TestRenew_Assertions(t *testing.T) {
	db := openVisTestDB(t)
	srv := visServer(t, db)
	now := time.Now().Unix()

	// Expired listing with count=1 (still renewable)
	hash := visHash("assert-owner")
	principalID := "prn-assert"
	seedVisPrincipal(t, db, principalID, hash, "client")
	_, err := db.Exec(`INSERT INTO listings
		(id, wallet_hash, owner_principal_id, status, opened_chats_count, renewal_count, visible_until, created_at, first_activated_at)
		VALUES ('assert-list', ?, ?, 'expired', 1, 2, ?, ?, ?)`,
		hash, principalID, now-100, now-86400, now-86400)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	tokenRaw := "vis-assert-token"
	seedVisSessionWithPrincipal(t, db, middleware.HashToken(tokenRaw), hash, "client", principalID)

	before := time.Now().Unix()
	resp := doPostWithBearer(t, srv, "/listing/assert-list/renew", tokenRaw, "")
	defer resp.Body.Close()
	after := time.Now().Unix()

	if resp.StatusCode != 200 {
		t.Fatalf("VIS-16: expected 200, got %d", resp.StatusCode)
	}

	// Parse response body
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)

	// renewal_count increments exactly once (was 2, now must be 3)
	if rc, _ := body["renewal_count"].(float64); int(rc) != 3 {
		t.Fatalf("VIS-16 FAIL: expected renewal_count=3, got %v", body["renewal_count"])
	}

	// visible_until ≈ now + 86400 (within a few seconds)
	if vu, _ := body["visible_until"].(float64); vu < float64(before+86400-5) || vu > float64(after+86400+5) {
		t.Fatalf("VIS-16 FAIL: visible_until=%v, want ≈ %d", vu, before+86400)
	}

	// opened_chats_count unchanged (still 1)
	var count int
	db.QueryRow(`SELECT opened_chats_count FROM listings WHERE id = 'assert-list'`).Scan(&count)
	if count != 1 {
		t.Fatalf("VIS-16 FAIL: opened_chats_count should still be 1, got %d", count)
	}

	// Zero invoices created by renewal
	var invoiceCount int
	db.QueryRow(`SELECT COUNT(*) FROM invoices`).Scan(&invoiceCount)
	if invoiceCount != 0 {
		t.Fatalf("VIS-16 FAIL: renewal must create zero invoices, got %d", invoiceCount)
	}
}

// VIS-17: Telegram matching-helper notification fires exactly once after a successful renewal.
// A duplicate (blocked) renewal call does not dispatch an additional notification.
func TestRenew_TelegramNotifiedOnce(t *testing.T) {
	db := openVisTestDB(t)
	mock := &mockTelegramSender{}
	srv := visServerFull(t, db, mock)
	now := time.Now().Unix()

	// Insert a matching helper subscription so NotifyMatchingHelpers actually sends
	_, err := db.Exec(`INSERT INTO helper_board_subscriptions
		(id, telegram_chat_id, city, problem, help_type, urgency, created_at, expires_at, active)
		VALUES ('sub-1', 'chat-123', 'tbilisi', 'alcohol', 'crisis', 'can_wait', ?, ?, TRUE)`,
		now, now+86400)
	if err != nil {
		t.Fatalf("insert subscription: %v", err)
	}

	// Expired listing in tbilisi matching the subscription
	tgHash := visHash("tg-owner")
	tgPrincipalID := "prn-tg"
	seedVisPrincipal(t, db, tgPrincipalID, tgHash, "client")
	_, err = db.Exec(`INSERT INTO listings
		(id, wallet_hash, owner_principal_id, status, opened_chats_count, visible_until, created_at, first_activated_at,
		 city, dependency_type, help_type, urgency, languages)
		VALUES ('tg-list', ?, ?, 'expired', 0, ?, ?, ?, 'tbilisi', 'alcohol', 'crisis', 'can_wait', '["en"]')`,
		tgHash, tgPrincipalID, now-100, now-86400, now-86400)
	if err != nil {
		t.Fatalf("insert listing: %v", err)
	}
	tgTokenRaw := "vis-tg-token"
	seedVisSessionWithPrincipal(t, db, middleware.HashToken(tgTokenRaw), tgHash, "client", tgPrincipalID)

	// First renewal → 200 → goroutine dispatched
	r1 := doPostWithBearer(t, srv, "/listing/tg-list/renew", tgTokenRaw, "")
	r1.Body.Close()
	if r1.StatusCode != 200 {
		t.Fatalf("VIS-17: first renewal expected 200, got %d", r1.StatusCode)
	}
	// Give the goroutine time to complete
	time.Sleep(150 * time.Millisecond)

	if c := mock.count(); c != 1 {
		t.Fatalf("VIS-17 FAIL: expected 1 Telegram call after first renewal, got %d", c)
	}

	// Second renewal immediately → 409 → no goroutine dispatched
	r2 := doPostWithBearer(t, srv, "/listing/tg-list/renew", tgTokenRaw, "")
	r2.Body.Close()
	if r2.StatusCode != 409 {
		t.Fatalf("VIS-17: duplicate renewal expected 409, got %d", r2.StatusCode)
	}
	time.Sleep(50 * time.Millisecond)

	if c := mock.count(); c != 1 {
		t.Fatalf("VIS-17 FAIL: expected still 1 Telegram call after blocked renewal, got %d", c)
	}
}
