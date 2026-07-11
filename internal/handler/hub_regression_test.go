package handler

// Regression tests for ChatHub multi-connection support.
//
// Reproduction sequence (production bug, 2026-07-11):
//   1. Client and Helper have an active chat (one WS each).
//   2. Client opens the same room in a second browser tab → BEFORE fix: overwrote the first
//      tab's hub entry, orphaning it.
//   3. Client closes the second tab → BEFORE fix: deleted the hub entry entirely, stopping
//      all message delivery for that room.
//   4. Messages from Helper stopped arriving on the remaining Client tab.
//   5. Refreshing Client/Helper pages (reconnecting WS) restored delivery.
//
// Root cause: hub keyed connections by wallet_hash, so a second tab for the same wallet
// silently replaced the first connection.  On close it removed the only hub entry.
//
// Fix: each connection gets a unique connID; deregistration removes only that connID.

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"naroom/internal/middleware"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func openHubTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE sessions (
			token_hash TEXT PRIMARY KEY,
			wallet_hash TEXT NOT NULL,
			currency TEXT, role TEXT,
			created_at INTEGER, expires_at INTEGER, revoked_at INTEGER
		)`,
		`CREATE TABLE chat_rooms (
			id TEXT PRIMARY KEY,
			listing_id TEXT, response_id TEXT,
			client_hash TEXT NOT NULL,
			counselor_hash TEXT NOT NULL,
			client_pubkey TEXT NOT NULL,
			counselor_pubkey TEXT NOT NULL,
			started_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			closed_at INTEGER, closed_by TEXT,
			peer_left_at INTEGER, client_left_at INTEGER,
			listing_counted INTEGER DEFAULT 0,
			status TEXT DEFAULT 'active'
		)`,
		`CREATE TABLE encrypted_messages (
			id TEXT PRIMARY KEY,
			room_id TEXT NOT NULL,
			sender_pubkey TEXT NOT NULL,
			nonce TEXT NOT NULL,
			ciphertext TEXT NOT NULL,
			msg_type TEXT DEFAULT 'text',
			created_at INTEGER NOT NULL
		)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("schema: %v: %s", err, q)
		}
	}
	return db
}

const (
	testRoomID      = "room_regression_test"
	testClientHash  = "clienthash_regression"
	testHelperHash  = "helperhash_regression"
	testClientPub   = "clientpubkey_regression"
	testHelperPub   = "helperpubkey_regression"
	testClientToken = "clienttoken_regression_abc123"
	testHelperToken = "helpertoken_regression_def456"
)

func seedHubRoom(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	exp := now + 3600
	db.Exec(`INSERT INTO chat_rooms
		(id, client_hash, counselor_hash, client_pubkey, counselor_pubkey, started_at, expires_at, status)
		VALUES (?,?,?,?,?,?,?,'active')`,
		testRoomID, testClientHash, testHelperHash, testClientPub, testHelperPub, now, exp)
	db.Exec(`INSERT INTO sessions (token_hash, wallet_hash, role, created_at, expires_at) VALUES (?,?,'client',?,?)`,
		middleware.HashToken(testClientToken), testClientHash, now, exp)
	db.Exec(`INSERT INTO sessions (token_hash, wallet_hash, role, created_at, expires_at) VALUES (?,?,'peer',?,?)`,
		middleware.HashToken(testHelperToken), testHelperHash, now, exp)
}

// dialWS connects to the test server and returns the connection plus a channel
// that receives all incoming messages (background goroutine, closed on disconnect).
func dialWS(t *testing.T, ctx context.Context, url, token string) (*websocket.Conn, <-chan wsOutMessage) {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		Subprotocols: []string{token},
	})
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	msgs := make(chan wsOutMessage, 50)
	go func() {
		defer close(msgs)
		for {
			var m wsOutMessage
			if err := wsjson.Read(ctx, conn, &m); err != nil {
				return
			}
			msgs <- m
		}
	}()
	return conn, msgs
}

// sendWS sends a message over the WS and returns immediately.
func sendWS(t *testing.T, ctx context.Context, conn *websocket.Conn, nonce, cipher string) {
	t.Helper()
	err := wsjson.Write(ctx, conn, wsMessage{Nonce: nonce, Ciphertext: cipher, MsgType: "text"})
	if err != nil {
		t.Fatalf("sendWS: %v", err)
	}
}

// waitMsg waits up to 2 s for a message on ch.
func waitMsg(t *testing.T, ch <-chan wsOutMessage, label string) wsOutMessage {
	t.Helper()
	select {
	case m, ok := <-ch:
		if !ok {
			t.Fatalf("waitMsg(%s): channel closed", label)
		}
		return m
	case <-time.After(2 * time.Second):
		t.Fatalf("waitMsg(%s): timeout", label)
	}
	panic("unreachable")
}

// newHubServer builds a minimal httptest server that only serves /chat/ws.
func newHubServer(t *testing.T, db *sql.DB, hub *ChatHub) *httptest.Server {
	t.Helper()
	h := &Handler{DB: db, Hub: hub}
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/ws", h.ChatWS(hub))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func wsURL(srv *httptest.Server) string {
	return "ws" + srv.URL[4:] + "/chat/ws?room_id=" + testRoomID
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestChatHub_MultipleTabsSameWallet_NoMessageDrop is the exact production
// reproduction sequence: second tab opens, closes, messages must still flow.
func TestChatHub_MultipleTabsSameWallet_NoMessageDrop(t *testing.T) {
	db := openHubTestDB(t)
	defer db.Close()
	seedHubRoom(t, db)

	hub := NewChatHub()
	srv := newHubServer(t, db, hub)
	url := wsURL(srv)
	ctx := context.Background()

	// Step 1: Helper connects.
	helperConn, helperMsgs := dialWS(t, ctx, url, testHelperToken)
	defer helperConn.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond) // let registration settle

	// Step 2: Client tab 1 connects.
	clientConn1, _ := dialWS(t, ctx, url, testClientToken)
	time.Sleep(30 * time.Millisecond)

	// Step 3: Client tab 2 opens (same wallet) — BEFORE fix this overwrites tab 1.
	clientConn2, _ := dialWS(t, ctx, url, testClientToken)
	time.Sleep(30 * time.Millisecond)

	// Step 4: Client tab 2 closes — BEFORE fix this deletes the hub entry entirely.
	clientConn2.Close(websocket.StatusNormalClosure, "closing second tab")
	time.Sleep(50 * time.Millisecond) // let close propagate through hub deregister

	// Step 5: Client tab 1 sends a message — Helper must still receive it.
	sendWS(t, ctx, clientConn1, "nonce_multitab_1", "cipher_multitab_1")
	m := waitMsg(t, helperMsgs, "helper receives after tab2 close")
	if m.Nonce != "nonce_multitab_1" {
		t.Errorf("got nonce %q, want nonce_multitab_1", m.Nonce)
	}
	if m.SenderPubkey != testClientPub {
		t.Errorf("got sender_pubkey %q, want %q", m.SenderPubkey, testClientPub)
	}

	// Verify DB: exactly 1 message row (no duplicates from multi-tab).
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM encrypted_messages WHERE room_id = ?`, testRoomID).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 DB message, got %d", count)
	}

	// Verify hub: client tab1 still registered, helper still registered.
	hub.mu.RLock()
	hubCount := len(hub.rooms[testRoomID])
	hub.mu.RUnlock()
	if hubCount < 2 {
		t.Errorf("expected ≥2 connections in hub (client tab1 + helper), got %d", hubCount)
	}

	clientConn1.Close(websocket.StatusNormalClosure, "")
}

// TestChatHub_Reconnect_DeliveryRestored verifies that disconnect + reconnect
// (browser refresh) correctly restores message delivery.
func TestChatHub_Reconnect_DeliveryRestored(t *testing.T) {
	db := openHubTestDB(t)
	defer db.Close()
	seedHubRoom(t, db)

	hub := NewChatHub()
	srv := newHubServer(t, db, hub)
	url := wsURL(srv)
	ctx := context.Background()

	// Initial connections.
	helperConn, helperMsgs := dialWS(t, ctx, url, testHelperToken)
	defer helperConn.Close(websocket.StatusNormalClosure, "")
	clientConn, _ := dialWS(t, ctx, url, testClientToken)
	time.Sleep(30 * time.Millisecond)

	// Send first message — proves initial delivery works.
	sendWS(t, ctx, clientConn, "nonce_before_refresh", "cipher_before_refresh")
	waitMsg(t, helperMsgs, "initial delivery")

	// Client disconnects (simulates page refresh / network drop).
	clientConn.Close(websocket.StatusNormalClosure, "refreshing")
	time.Sleep(50 * time.Millisecond)

	// Client reconnects.
	clientConn2, _ := dialWS(t, ctx, url, testClientToken)
	defer clientConn2.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond)

	// Drain history messages that sendHistory replays on connect.
	drainCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
drain:
	for {
		select {
		case <-helperMsgs:
		case <-drainCtx.Done():
			break drain
		}
	}

	// Send after reconnect — Helper must still receive.
	sendWS(t, ctx, clientConn2, "nonce_after_refresh", "cipher_after_refresh")
	m := waitMsg(t, helperMsgs, "delivery after reconnect")
	if m.Nonce != "nonce_after_refresh" {
		t.Errorf("got nonce %q, want nonce_after_refresh", m.Nonce)
	}
}

// TestChatHub_CloseRace verifies that concurrent close of one connection while
// another is broadcasting does not panic or deadlock.
func TestChatHub_CloseRace(t *testing.T) {
	db := openHubTestDB(t)
	defer db.Close()
	seedHubRoom(t, db)

	hub := NewChatHub()
	srv := newHubServer(t, db, hub)
	url := wsURL(srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	helperConn, _ := dialWS(t, ctx, url, testHelperToken)
	defer helperConn.Close(websocket.StatusNormalClosure, "")

	// Open 3 client tabs to stress the race.
	clientConns := make([]*websocket.Conn, 3)
	for i := range clientConns {
		c, _ := dialWS(t, ctx, url, testClientToken)
		clientConns[i] = c
	}
	time.Sleep(50 * time.Millisecond)

	// Close tabs 1 and 2 concurrently while tab 0 keeps sending.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); clientConns[1].Close(websocket.StatusNormalClosure, "") }()
	go func() { defer wg.Done(); clientConns[2].Close(websocket.StatusNormalClosure, "") }()
	go func() {
		for i := 0; i < 5; i++ {
			wsjson.Write(ctx, clientConns[0], wsMessage{Nonce: "rnonce", Ciphertext: "rcipher", MsgType: "text"})
		}
	}()
	wg.Wait()
	time.Sleep(100 * time.Millisecond) // let sends finish

	// No panic = race handled. Verify hub is still consistent.
	hub.mu.RLock()
	for _, rc := range hub.rooms[testRoomID] {
		if rc == nil {
			t.Error("nil roomConn in hub after race")
		}
	}
	hub.mu.RUnlock()

	clientConns[0].Close(websocket.StatusNormalClosure, "")
}

// TestChatHub_HubCount_TracksExactConnections verifies registration and
// deregistration counts are exact (no stale entries, no missing entries).
func TestChatHub_HubCount_TracksExactConnections(t *testing.T) {
	db := openHubTestDB(t)
	defer db.Close()
	seedHubRoom(t, db)

	hub := NewChatHub()
	srv := newHubServer(t, db, hub)
	url := wsURL(srv)
	ctx := context.Background()

	count := func() int {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return len(hub.rooms[testRoomID])
	}

	// Open 3 connections (1 helper + 2 client tabs).
	helperConn, _ := dialWS(t, ctx, url, testHelperToken)
	time.Sleep(20 * time.Millisecond)
	if c := count(); c != 1 {
		t.Fatalf("after helper connect: want 1, got %d", c)
	}

	c1, _ := dialWS(t, ctx, url, testClientToken)
	time.Sleep(20 * time.Millisecond)
	if c := count(); c != 2 {
		t.Fatalf("after client tab1: want 2, got %d", c)
	}

	c2, _ := dialWS(t, ctx, url, testClientToken)
	time.Sleep(20 * time.Millisecond)
	if c := count(); c != 3 {
		t.Fatalf("after client tab2: want 3, got %d", c)
	}

	// Close tab 2 → count drops by exactly 1.
	c2.Close(websocket.StatusNormalClosure, "")
	time.Sleep(50 * time.Millisecond)
	if c := count(); c != 2 {
		t.Fatalf("after tab2 close: want 2, got %d", c)
	}

	// Close tab 1 → count drops by exactly 1.
	c1.Close(websocket.StatusNormalClosure, "")
	time.Sleep(50 * time.Millisecond)
	if c := count(); c != 1 {
		t.Fatalf("after tab1 close: want 1, got %d", c)
	}

	// Close helper → room entry cleaned up entirely.
	helperConn.Close(websocket.StatusNormalClosure, "")
	time.Sleep(50 * time.Millisecond)
	hub.mu.RLock()
	_, exists := hub.rooms[testRoomID]
	hub.mu.RUnlock()
	if exists {
		t.Error("room entry should be removed after all connections close")
	}
}
