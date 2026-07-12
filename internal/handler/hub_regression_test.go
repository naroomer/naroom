package handler

// Regression tests for ChatHub single-active-browser policy.
//
// Policy (implemented 2026-07-11):
//   - Exactly one active WS connection per (room, wallet).
//   - Same session token + reconnect (browser refresh) → old conn cancelled, new takes over.
//   - Different session token (second browser) → rejected with {type:"system",event:"chat_already_open"}.
//   - After original connection closes the slot is free; any session may connect.
//
// Production bug that motivated this:
//   A second browser for the same wallet would silently overwrite the hub entry, then on
//   close remove it entirely — stopping message delivery for the remaining browser.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	_ "modernc.org/sqlite"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"naroom/internal/middleware"
)

// ── schema & seed ────────────────────────────────────────────────────────────

func openHubTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// Force a single connection so all goroutines share the same in-memory database.
	// Without this, database/sql can open additional connections, each of which
	// SQLite treats as a completely separate :memory: database — causing queries
	// from concurrent goroutines (WS handler + HTTP handler) to see empty DBs.
	db.SetMaxOpenConns(1)
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
			t.Fatalf("schema: %v\nSQL: %s", err, q)
		}
	}
	return db
}

const (
	testRoomID       = "room_regression_test"
	testClientHash   = "clienthash_regression"
	testHelperHash   = "helperhash_regression"
	testClientPub    = "clientpubkey_regression"
	testHelperPub    = "helperpubkey_regression"
	testClientToken  = "clienttoken_regression_aaa"   // session A (first browser)
	testClientToken2 = "clienttoken_regression_bbb"   // session B (second browser, same wallet)
	testHelperToken  = "helpertoken_regression_ccc"
)

func seedHubRoom(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	exp := now + 3600
	db.Exec(`INSERT INTO chat_rooms
		(id, client_hash, counselor_hash, client_pubkey, counselor_pubkey, started_at, expires_at, status)
		VALUES (?,?,?,?,?,?,?,'active')`,
		testRoomID, testClientHash, testHelperHash, testClientPub, testHelperPub, now, exp)
	// Session A — first browser
	db.Exec(`INSERT INTO sessions (token_hash, wallet_hash, role, created_at, expires_at) VALUES (?,?,'client',?,?)`,
		middleware.HashToken(testClientToken), testClientHash, now, exp)
	// Session B — second browser (same wallet, different token)
	db.Exec(`INSERT INTO sessions (token_hash, wallet_hash, role, created_at, expires_at) VALUES (?,?,'client',?,?)`,
		middleware.HashToken(testClientToken2), testClientHash, now, exp)
	// Helper session
	db.Exec(`INSERT INTO sessions (token_hash, wallet_hash, role, created_at, expires_at) VALUES (?,?,'peer',?,?)`,
		middleware.HashToken(testHelperToken), testHelperHash, now, exp)
}

// ── WS helpers ───────────────────────────────────────────────────────────────

// dialWS connects and returns a channel of typed chat messages.
func dialWS(t *testing.T, ctx context.Context, url, token string) (*websocket.Conn, <-chan wsOutMessage) {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{token}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ch := make(chan wsOutMessage, 50)
	go func() {
		defer close(ch)
		for {
			var m wsOutMessage
			if err := wsjson.Read(ctx, conn, &m); err != nil {
				return
			}
			ch <- m
		}
	}()
	return conn, ch
}

// dialWSRaw connects and returns a channel of raw JSON objects (any message type).
func dialWSRaw(t *testing.T, ctx context.Context, url, token string) (*websocket.Conn, <-chan map[string]any) {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{token}})
	if err != nil {
		t.Fatalf("dialWSRaw: %v", err)
	}
	ch := make(chan map[string]any, 20)
	go func() {
		defer close(ch)
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(raw, &m) == nil {
				ch <- m
			}
		}
	}()
	return conn, ch
}

func sendWS(t *testing.T, ctx context.Context, conn *websocket.Conn, nonce, cipher string) {
	t.Helper()
	if err := wsjson.Write(ctx, conn, wsMessage{Nonce: nonce, Ciphertext: cipher, MsgType: "text"}); err != nil {
		t.Fatalf("sendWS: %v", err)
	}
}

func waitMsg(t *testing.T, ch <-chan wsOutMessage, label string) wsOutMessage {
	t.Helper()
	select {
	case m, ok := <-ch:
		if !ok {
			t.Fatalf("waitMsg(%s): channel closed before message", label)
		}
		return m
	case <-time.After(2 * time.Second):
		t.Fatalf("waitMsg(%s): timeout", label)
	}
	panic("unreachable")
}

func waitRaw(t *testing.T, ch <-chan map[string]any, label string) map[string]any {
	t.Helper()
	select {
	case m, ok := <-ch:
		if !ok {
			t.Fatalf("waitRaw(%s): channel closed", label)
		}
		return m
	case <-time.After(2 * time.Second):
		t.Fatalf("waitRaw(%s): timeout", label)
	}
	panic("unreachable")
}

func hubServer(t *testing.T, db *sql.DB, hub *ChatHub) (*httptest.Server, string) {
	t.Helper()
	h := &Handler{DB: db, Hub: hub}
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/ws", h.ChatWS(hub))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	url := "ws" + srv.URL[4:] + "/chat/ws?room_id=" + testRoomID
	return srv, url
}

func hubCount(hub *ChatHub) int {
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	return len(hub.rooms[testRoomID])
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestChatHub_SameSession_Refresh_Reconnects verifies that reconnecting with
// the same session token (browser refresh/reload) is allowed and takes over cleanly.
func TestChatHub_SameSession_Refresh_Reconnects(t *testing.T) {
	db := openHubTestDB(t)
	defer db.Close()
	seedHubRoom(t, db)

	hub := NewChatHub()
	_, url := hubServer(t, db, hub)
	ctx := context.Background()

	helperConn, helperMsgs := dialWS(t, ctx, url, testHelperToken)
	defer helperConn.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond)

	// First connection with session A.
	conn1, _ := dialWS(t, ctx, url, testClientToken)
	defer conn1.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond)
	if c := hubCount(hub); c != 2 {
		t.Fatalf("after conn1: want 2 hub entries, got %d", c)
	}

	// Reconnect with the SAME session token (refresh): should succeed, old conn cancelled.
	conn2, conn2Msgs := dialWS(t, ctx, url, testClientToken)
	defer conn2.Close(websocket.StatusNormalClosure, "")
	time.Sleep(50 * time.Millisecond)

	// Hub still has exactly 2 entries (client + helper, no duplicates).
	if c := hubCount(hub); c != 2 {
		t.Errorf("after refresh: want 2 hub entries, got %d", c)
	}

	// conn2 → helper: verifies conn2 is the active client connection.
	sendWS(t, ctx, conn2, "nonce_c2h", "cipher_c2h")
	got := waitMsg(t, helperMsgs, "helper receives from refreshed client")
	if got.Nonce != "nonce_c2h" {
		t.Errorf("nonce: got %q, want nonce_c2h", got.Nonce)
	}

	// helper → conn2: verifies conn2 receives broadcasts.
	sendWS(t, ctx, helperConn, "nonce_h2c", "cipher_h2c")
	got2 := waitMsg(t, conn2Msgs, "conn2 receives from helper")
	if got2.Nonce != "nonce_h2c" {
		t.Errorf("conn2 nonce: got %q, want nonce_h2c", got2.Nonce)
	}
}

// TestChatHub_DifferentSession_SecondBrowser_Rejected verifies that a second
// browser (different session token, same wallet) receives chat_already_open and
// is disconnected. The original connection continues unaffected.
func TestChatHub_DifferentSession_SecondBrowser_Rejected(t *testing.T) {
	db := openHubTestDB(t)
	defer db.Close()
	seedHubRoom(t, db)

	hub := NewChatHub()
	_, url := hubServer(t, db, hub)
	ctx := context.Background()

	helperConn, helperMsgs := dialWS(t, ctx, url, testHelperToken)
	defer helperConn.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond)

	// First browser: session A.
	conn1, conn1Msgs := dialWS(t, ctx, url, testClientToken)
	defer conn1.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond)

	// Second browser: session B (different token, same walletHash).
	conn2, conn2Raw := dialWSRaw(t, ctx, url, testClientToken2)
	defer conn2.Close(websocket.StatusNormalClosure, "")

	// Second browser must receive chat_already_open.
	msg := waitRaw(t, conn2Raw, "second browser gets rejection")
	if msg["type"] != "system" || msg["event"] != "chat_already_open" {
		t.Errorf("expected {type:system,event:chat_already_open}, got %v", msg)
	}

	// conn2 should be closed by server after rejection.
	select {
	case _, ok := <-conn2Raw:
		if ok {
			t.Error("expected conn2 to be closed after rejection, but got more messages")
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("timeout: conn2 was not closed after rejection")
	}

	// Hub still has only 2 entries (client session A + helper).
	time.Sleep(30 * time.Millisecond)
	if c := hubCount(hub); c != 2 {
		t.Errorf("hub count after rejection: want 2, got %d", c)
	}

	// Verify session A is still the registered connection (by token hash).
	hub.mu.RLock()
	registered := hub.rooms[testRoomID][testClientHash]
	hub.mu.RUnlock()
	if registered == nil {
		t.Error("client slot unexpectedly empty after rejection")
	} else if registered.sessionTokenHash != middleware.HashToken(testClientToken) {
		t.Errorf("hub has wrong session token hash: got %q, want session A hash", registered.sessionTokenHash)
	}

	// Original connection still receives messages.
	sendWS(t, ctx, helperConn, "nonce_after_rejection", "cipher_after_rejection")
	m := waitMsg(t, conn1Msgs, "conn1 receives after rejection")
	if m.Nonce != "nonce_after_rejection" {
		t.Errorf("nonce: got %q, want nonce_after_rejection", m.Nonce)
	}

	// Helper also unaffected.
	sendWS(t, ctx, conn1, "nonce_client_to_helper", "cipher_c2h")
	mh := waitMsg(t, helperMsgs, "helper receives from conn1 after rejection")
	if mh.Nonce != "nonce_client_to_helper" {
		t.Errorf("helper nonce: got %q", mh.Nonce)
	}
}

// TestChatHub_NoPublickeyReplacement verifies that a second-browser rejection
// leaves the registered session token hash unchanged (no pubkey/session takeover).
func TestChatHub_NoPublickeyReplacement(t *testing.T) {
	db := openHubTestDB(t)
	defer db.Close()
	seedHubRoom(t, db)

	hub := NewChatHub()
	_, url := hubServer(t, db, hub)
	ctx := context.Background()

	helperConn, _ := dialWS(t, ctx, url, testHelperToken)
	defer helperConn.Close(websocket.StatusNormalClosure, "")

	conn1, _ := dialWS(t, ctx, url, testClientToken)
	defer conn1.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond)

	// Record registered session hash before second-browser attempt.
	hub.mu.RLock()
	beforeHash := hub.rooms[testRoomID][testClientHash].sessionTokenHash
	hub.mu.RUnlock()

	// Second browser tries to connect.
	conn2, conn2Raw := dialWSRaw(t, ctx, url, testClientToken2)
	waitRaw(t, conn2Raw, "rejection received")
	conn2.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond)

	// Session token hash must be unchanged.
	hub.mu.RLock()
	afterHash := hub.rooms[testRoomID][testClientHash].sessionTokenHash
	hub.mu.RUnlock()
	if beforeHash != afterHash {
		t.Errorf("session token hash changed after rejection: before=%q after=%q", beforeHash, afterHash)
	}
	if afterHash != middleware.HashToken(testClientToken) {
		t.Errorf("wrong session hash after rejection: got %q, want hash of testClientToken", afterHash)
	}
}

// TestChatHub_SlotFreeAfterClose_NewSessionCanConnect verifies that once the
// original connection closes the slot is freed and a new session may connect.
func TestChatHub_SlotFreeAfterClose_NewSessionCanConnect(t *testing.T) {
	db := openHubTestDB(t)
	defer db.Close()
	seedHubRoom(t, db)

	hub := NewChatHub()
	_, url := hubServer(t, db, hub)
	ctx := context.Background()

	helperConn, helperMsgs := dialWS(t, ctx, url, testHelperToken)
	defer helperConn.Close(websocket.StatusNormalClosure, "")

	// Session A connects then closes.
	conn1, _ := dialWS(t, ctx, url, testClientToken)
	time.Sleep(30 * time.Millisecond)
	conn1.Close(websocket.StatusNormalClosure, "leaving")
	time.Sleep(60 * time.Millisecond) // let deregister run

	// Slot should be free: exactly 1 entry (helper only).
	if c := hubCount(hub); c != 1 {
		t.Fatalf("after close: want 1 hub entry, got %d", c)
	}

	// Session B (different token) can now connect.
	conn2, _ := dialWS(t, ctx, url, testClientToken2)
	defer conn2.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond)

	if c := hubCount(hub); c != 2 {
		t.Fatalf("after session B connects: want 2, got %d", c)
	}

	// Verify it's session B in the hub.
	hub.mu.RLock()
	registered := hub.rooms[testRoomID][testClientHash]
	hub.mu.RUnlock()
	if registered.sessionTokenHash != middleware.HashToken(testClientToken2) {
		t.Error("hub should have session B's token hash after reconnect")
	}

	// Session B can send to helper.
	sendWS(t, ctx, conn2, "nonce_from_sessionB", "cipher_from_b")
	mh := waitMsg(t, helperMsgs, "helper receives from session B")
	if mh.Nonce != "nonce_from_sessionB" {
		t.Errorf("got %q, want nonce_from_sessionB", mh.Nonce)
	}
}

// TestChatHub_MessageDelivery_AfterSameSessionReconnect verifies messages flow
// correctly after a refresh (same session reconnect).
func TestChatHub_MessageDelivery_AfterSameSessionReconnect(t *testing.T) {
	db := openHubTestDB(t)
	defer db.Close()
	seedHubRoom(t, db)

	hub := NewChatHub()
	_, url := hubServer(t, db, hub)
	ctx := context.Background()

	helperConn, helperMsgs := dialWS(t, ctx, url, testHelperToken)
	defer helperConn.Close(websocket.StatusNormalClosure, "")

	conn1, _ := dialWS(t, ctx, url, testClientToken)
	time.Sleep(30 * time.Millisecond)

	// Send before refresh.
	sendWS(t, ctx, conn1, "nonce_pre_refresh", "cipher_pre")
	waitMsg(t, helperMsgs, "helper gets pre-refresh msg")

	// Refresh: same session reconnects.
	conn1.Close(websocket.StatusNormalClosure, "refresh")
	time.Sleep(50 * time.Millisecond)
	conn2, conn2Msgs := dialWS(t, ctx, url, testClientToken)
	defer conn2.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond)

	// Drain history replayed on connect.
	drainCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
drain:
	for {
		select {
		case <-conn2Msgs:
		case <-drainCtx.Done():
			break drain
		}
	}

	// Send after refresh.
	sendWS(t, ctx, conn2, "nonce_post_refresh", "cipher_post")
	m := waitMsg(t, helperMsgs, "helper gets post-refresh msg")
	if m.Nonce != "nonce_post_refresh" {
		t.Errorf("nonce: got %q, want nonce_post_refresh", m.Nonce)
	}

	// Helper sends to client — arrives on conn2.
	sendWS(t, ctx, helperConn, "nonce_h_to_c", "cipher_h2c")
	mc := waitMsg(t, conn2Msgs, "conn2 receives from helper")
	if mc.Nonce != "nonce_h_to_c" {
		t.Errorf("client nonce: got %q, want nonce_h_to_c", mc.Nonce)
	}

	// DB has 3 messages: pre-refresh (conn1→helper), post-refresh (conn2→helper), helper→conn2.
	// No duplicates — each message stored exactly once regardless of refresh.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM encrypted_messages WHERE room_id = ?`, testRoomID).Scan(&count)
	if count != 3 {
		t.Errorf("expected 3 DB messages (no duplicates), got %d", count)
	}
}

// TestChatHub_OpenedChatsCount_Unchanged verifies the hub policy does not affect
// opened_chats_count or room status.
func TestChatHub_OpenedChatsCount_Unchanged(t *testing.T) {
	db := openHubTestDB(t)
	defer db.Close()
	seedHubRoom(t, db)

	// Manually set a fake listing row to verify count isn't touched.
	db.Exec(`CREATE TABLE IF NOT EXISTS listings (
		id TEXT PRIMARY KEY, opened_chats_count INTEGER DEFAULT 0, status TEXT DEFAULT 'active',
		wallet_hash TEXT, city TEXT, dependency_type TEXT, help_type TEXT,
		urgency TEXT, languages TEXT, visible_until INTEGER, created_at INTEGER,
		is_sample INTEGER DEFAULT 0, renewal_count INTEGER DEFAULT 0, first_activated_at INTEGER
	)`)
	db.Exec(`INSERT INTO listings (id, opened_chats_count, status) VALUES ('lst_test_hub', 1, 'matched')`)
	db.Exec(`UPDATE chat_rooms SET listing_id='lst_test_hub' WHERE id=?`, testRoomID)

	hub := NewChatHub()
	_, url := hubServer(t, db, hub)
	ctx := context.Background()

	helperConn, _ := dialWS(t, ctx, url, testHelperToken)
	defer helperConn.Close(websocket.StatusNormalClosure, "")
	conn1, _ := dialWS(t, ctx, url, testClientToken)
	defer conn1.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond)

	// Second browser rejected.
	conn2, conn2Raw := dialWSRaw(t, ctx, url, testClientToken2)
	waitRaw(t, conn2Raw, "rejection")
	conn2.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond)

	// opened_chats_count still 1.
	var count int
	db.QueryRow(`SELECT opened_chats_count FROM listings WHERE id='lst_test_hub'`).Scan(&count)
	if count != 1 {
		t.Errorf("opened_chats_count: got %d, want 1", count)
	}

	// Room status still active.
	var status string
	db.QueryRow(`SELECT status FROM chat_rooms WHERE id=?`, testRoomID).Scan(&status)
	if status != "active" {
		t.Errorf("room status: got %q, want active", status)
	}
}

// TestChatHub_CloseRace verifies no panic or deadlock when multiple
// connections close concurrently.
func TestChatHub_CloseRace(t *testing.T) {
	db := openHubTestDB(t)
	defer db.Close()
	seedHubRoom(t, db)

	hub := NewChatHub()
	_, url := hubServer(t, db, hub)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	helperConn, _ := dialWS(t, ctx, url, testHelperToken)
	clientConn, _ := dialWS(t, ctx, url, testClientToken)
	time.Sleep(30 * time.Millisecond)

	// Close both concurrently while sending — no panic.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			wsjson.Write(ctx, clientConn, wsMessage{Nonce: "rn", Ciphertext: "rc", MsgType: "text"})
		}
		clientConn.Close(websocket.StatusNormalClosure, "")
	}()
	go func() {
		defer wg.Done()
		helperConn.Close(websocket.StatusNormalClosure, "")
	}()
	wg.Wait()
	time.Sleep(100 * time.Millisecond)

	// Hub should be empty after both close.
	hub.mu.RLock()
	_, exists := hub.rooms[testRoomID]
	hub.mu.RUnlock()
	if exists {
		t.Error("room entry should be removed after all connections close")
	}
}

// TestChatHub_ExactlyOneSlotPerWallet verifies hub count semantics.
func TestChatHub_ExactlyOneSlotPerWallet(t *testing.T) {
	db := openHubTestDB(t)
	defer db.Close()
	seedHubRoom(t, db)

	hub := NewChatHub()
	_, url := hubServer(t, db, hub)
	ctx := context.Background()

	// Connect helper (1 slot).
	helperConn, _ := dialWS(t, ctx, url, testHelperToken)
	defer helperConn.Close(websocket.StatusNormalClosure, "")
	time.Sleep(20 * time.Millisecond)
	if c := hubCount(hub); c != 1 {
		t.Fatalf("after helper: want 1, got %d", c)
	}

	// Connect client session A (2 slots).
	conn1, _ := dialWS(t, ctx, url, testClientToken)
	time.Sleep(20 * time.Millisecond)
	if c := hubCount(hub); c != 2 {
		t.Fatalf("after client A: want 2, got %d", c)
	}

	// Reconnect with same session (refresh) → still 2 slots.
	conn2, _ := dialWS(t, ctx, url, testClientToken)
	time.Sleep(50 * time.Millisecond)
	if c := hubCount(hub); c != 2 {
		t.Fatalf("after refresh: want 2, got %d", c)
	}

	// Second browser (session B) → rejected, still 2 slots.
	conn3, conn3Raw := dialWSRaw(t, ctx, url, testClientToken2)
	waitRaw(t, conn3Raw, "rejection")
	conn3.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond)
	if c := hubCount(hub); c != 2 {
		t.Fatalf("after rejection: want 2, got %d", c)
	}

	conn1.Close(websocket.StatusNormalClosure, "")
	conn2.Close(websocket.StatusNormalClosure, "")
}

// TestChatHub_SecondBrowser_CloseCode_Is4000 verifies that when a second browser
// (different session token, same wallet) is rejected, the server:
//  1. sends the JSON system event first (so onmessage fires if it arrives first),
//  2. closes the connection with WebSocket close code 4000, reason "chat_already_open".
//
// Close code 4000 is the reliable rejection signal: the browser's onclose(event)
// always receives event.code regardless of message/close frame ordering.
// This prevents the infinite "Reconnecting…" loop seen in production.
func TestChatHub_SecondBrowser_CloseCode_Is4000(t *testing.T) {
	db := openHubTestDB(t)
	defer db.Close()
	seedHubRoom(t, db)

	hub := NewChatHub()
	_, url := hubServer(t, db, hub)
	ctx := context.Background()

	// First browser establishes the session slot.
	conn1, _ := dialWS(t, ctx, url, testClientToken)
	defer conn1.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond)

	// Second browser dials with a different session token (same wallet).
	conn2, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		Subprotocols: []string{testClientToken2},
	})
	if err != nil {
		t.Fatalf("dial second browser: %v", err)
	}
	defer conn2.Close(websocket.StatusNormalClosure, "")

	// First frame: JSON system event {type:"system", event:"chat_already_open"}.
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, rawMsg, err := conn2.Read(readCtx)
	if err != nil {
		t.Fatalf("expected system JSON message before close, got error: %v", err)
	}
	var sysMsg map[string]any
	if jsonErr := json.Unmarshal(rawMsg, &sysMsg); jsonErr != nil {
		t.Fatalf("unmarshal system message: %v", jsonErr)
	}
	if sysMsg["type"] != "system" || sysMsg["event"] != "chat_already_open" {
		t.Errorf("system message: want {type:system,event:chat_already_open}, got %v", sysMsg)
	}

	// Second read: must return a CloseError with code 4000.
	readCtx2, cancel2 := context.WithTimeout(ctx, 2*time.Second)
	defer cancel2()
	_, _, err = conn2.Read(readCtx2)
	if err == nil {
		t.Fatal("expected close error from server, got nil")
	}
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("expected websocket.CloseError, got: %T: %v", err, err)
	}
	if closeErr.Code != websocket.StatusCode(4000) {
		t.Errorf("close code: got %d, want 4000", closeErr.Code)
	}
	if closeErr.Reason != "chat_already_open" {
		t.Errorf("close reason: got %q, want \"chat_already_open\"", closeErr.Reason)
	}

	// First browser's slot must still be in the hub, with session A's token hash.
	time.Sleep(30 * time.Millisecond)
	hub.mu.RLock()
	rc := hub.rooms[testRoomID][testClientHash]
	hub.mu.RUnlock()
	if rc == nil {
		t.Error("first browser slot should still be in hub after second-browser rejection")
	} else if rc.sessionTokenHash != middleware.HashToken(testClientToken) {
		t.Error("first browser slot should still hold session A's token hash after rejection")
	}
}

// hubServerFull creates a test HTTP server with both the WS endpoint and the
// UpdateChatPubkey endpoint (with requireSession middleware), using chi routing
// so that chi.URLParam works inside the handler.
//
// WebSocket upgrade is a GET request, so we register /chat/ws with r.Get.
// The pubkey endpoint is POST-only, registered with requireSession middleware.
func hubServerFull(t *testing.T, db *sql.DB, hub *ChatHub) (*httptest.Server, string) {
	t.Helper()
	h := &Handler{DB: db, Hub: hub}
	requireSess := middleware.RequireSession(db, false, []byte("testhashkey"))

	r := chi.NewRouter()
	r.Get("/chat/ws", h.ChatWS(hub)) // WebSocket upgrade is always GET
	r.With(requireSess).Post("/chat/{room_id}/pubkey", h.UpdateChatPubkey)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	wsURL := "ws" + srv.URL[4:] + "/chat/ws?room_id=" + testRoomID
	return srv, wsURL
}

// TestChatHub_ThreeBrowser_ABH_FullSequence is the three-browser regression test.
//
// Sequence:
//   A (session A) + H (helper) are in an active chat.
//   B (session B, same wallet as A) attempts to enter the same room.
//
// Expected invariants:
//   1. A↔H message delivery works before B arrives.
//   2. B's pubkey POST is rejected (409) — DB client_pubkey is unchanged.
//   3. B's WS connection is rejected with {type:system, event:chat_already_open}.
//   4. Hub still holds exactly 2 entries (A + H) after B's attempt.
//   5. A↔H message delivery continues uninterrupted after B is rejected.
//   6. A's hub slot still holds session A's token hash (not replaced by B).
func TestChatHub_ThreeBrowser_ABH_FullSequence(t *testing.T) {
	db := openHubTestDB(t)
	defer db.Close()
	seedHubRoom(t, db)

	hub := NewChatHub()
	srv, wsURL := hubServerFull(t, db, hub)
	ctx := context.Background()

	// ── 1. A and H connect ────────────────────────────────────────────────
	helperConn, helperMsgs := dialWS(t, ctx, wsURL, testHelperToken)
	defer helperConn.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond)

	conn1, conn1Msgs := dialWS(t, ctx, wsURL, testClientToken)
	defer conn1.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond)

	if c := hubCount(hub); c != 2 {
		t.Fatalf("initial hub count: want 2, got %d", c)
	}

	// ── 2. Verify A↔H messaging works before B ───────────────────────────
	sendWS(t, ctx, conn1, "pre_b_a2h_n", "pre_b_a2h_c")
	pre := waitMsg(t, helperMsgs, "H receives A's message before B")
	if pre.Nonce != "pre_b_a2h_n" {
		t.Errorf("pre-B A→H nonce: got %q, want pre_b_a2h_n", pre.Nonce)
	}

	sendWS(t, ctx, helperConn, "pre_b_h2a_n", "pre_b_h2a_c")
	pre2 := waitMsg(t, conn1Msgs, "A receives H's message before B")
	if pre2.Nonce != "pre_b_h2a_n" {
		t.Errorf("pre-B H→A nonce: got %q, want pre_b_h2a_n", pre2.Nonce)
	}

	// ── 3. B attempts pubkey update — must be denied (409) ───────────────
	// This simulates Browser B calling loadRoom() which fires the pubkey POST
	// fire-and-forget. Without the hub check in UpdateChatPubkey, this would
	// overwrite client_pubkey with B's key and break A↔H encryption silently.
	pubkeyReq, _ := http.NewRequest("POST",
		srv.URL+"/chat/"+testRoomID+"/pubkey",
		strings.NewReader(`{"pubkey":"B_fake_pubkey_hex"}`))
	pubkeyReq.Header.Set("Content-Type", "application/json")
	pubkeyReq.Header.Set("Authorization", "Bearer "+testClientToken2) // session B
	pubkeyResp, err := http.DefaultClient.Do(pubkeyReq)
	if err != nil {
		t.Fatalf("pubkey POST failed: %v", err)
	}
	pubkeyResp.Body.Close()
	if pubkeyResp.StatusCode != 409 {
		t.Errorf("pubkey POST with active A session: got %d, want 409", pubkeyResp.StatusCode)
	}

	// DB client_pubkey must be unchanged.
	var storedPubkey string
	db.QueryRow(`SELECT client_pubkey FROM chat_rooms WHERE id = ?`, testRoomID).Scan(&storedPubkey)
	if storedPubkey != testClientPub {
		t.Errorf("client_pubkey after B's POST: got %q, want %q (unchanged)", storedPubkey, testClientPub)
	}

	// ── 4. B attempts WS connection — must be rejected ────────────────────
	conn2, conn2Raw := dialWSRaw(t, ctx, wsURL, testClientToken2)
	rejection := waitRaw(t, conn2Raw, "B receives rejection system message")
	if rejection["type"] != "system" || rejection["event"] != "chat_already_open" {
		t.Errorf("B rejection message: got %v, want {type:system,event:chat_already_open}", rejection)
	}
	// Channel closes when server closes B's WS.
	select {
	case _, ok := <-conn2Raw:
		if ok {
			t.Error("expected B's channel closed after rejection, got more messages")
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("timeout: B's WS not closed after rejection")
	}
	conn2.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond)

	// ── 5. Hub count still 2 (A + H), A's slot unchanged ─────────────────
	if c := hubCount(hub); c != 2 {
		t.Errorf("hub count after B: want 2, got %d", c)
	}
	hub.mu.RLock()
	rc := hub.rooms[testRoomID][testClientHash]
	hub.mu.RUnlock()
	if rc == nil {
		t.Error("A's hub slot missing after B's attempt")
	} else if rc.sessionTokenHash != middleware.HashToken(testClientToken) {
		t.Errorf("A's hub slot has wrong session hash after B's attempt")
	}

	// ── 6. A↔H messaging continues uninterrupted after B's attempt ────────
	sendWS(t, ctx, conn1, "post_b_a2h_n", "post_b_a2h_c")
	post := waitMsg(t, helperMsgs, "H receives A's message AFTER B rejected")
	if post.Nonce != "post_b_a2h_n" {
		t.Errorf("post-B A→H nonce: got %q, want post_b_a2h_n", post.Nonce)
	}

	sendWS(t, ctx, helperConn, "post_b_h2a_n", "post_b_h2a_c")
	post2 := waitMsg(t, conn1Msgs, "A receives H's message AFTER B rejected")
	if post2.Nonce != "post_b_h2a_n" {
		t.Errorf("post-B H→A nonce: got %q, want post_b_h2a_n", post2.Nonce)
	}
}

// TestChatHub_UpdatePubkey_Allowed_WhenNoActiveSession verifies that UpdateChatPubkey
// succeeds (200) when no WS session is currently active for the wallet — the
// legitimate reconnect-after-session-loss path.
func TestChatHub_UpdatePubkey_Allowed_WhenNoActiveSession(t *testing.T) {
	db := openHubTestDB(t)
	defer db.Close()
	seedHubRoom(t, db)

	hub := NewChatHub()
	srv, _ := hubServerFull(t, db, hub)

	// No WS connections — hub is empty. Session B may update the pubkey freely.
	req, _ := http.NewRequest("POST",
		srv.URL+"/chat/"+testRoomID+"/pubkey",
		strings.NewReader(`{"pubkey":"reconnected_pubkey"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testClientToken2)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("pubkey POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("pubkey update with no active session: got %d, want 200", resp.StatusCode)
	}

	var storedPubkey string
	db.QueryRow(`SELECT client_pubkey FROM chat_rooms WHERE id = ?`, testRoomID).Scan(&storedPubkey)
	if storedPubkey != "reconnected_pubkey" {
		t.Errorf("pubkey not updated: got %q, want reconnected_pubkey", storedPubkey)
	}
}

// TestChatHub_UpdatePubkey_Allowed_SameSession verifies that UpdateChatPubkey
// succeeds (200) when the same session token owns the active WS slot — the
// normal same-browser page-reload path.
func TestChatHub_UpdatePubkey_Allowed_SameSession(t *testing.T) {
	db := openHubTestDB(t)
	defer db.Close()
	seedHubRoom(t, db)

	hub := NewChatHub()
	srv, wsURL := hubServerFull(t, db, hub)
	ctx := context.Background()

	// Session A holds the WS slot.
	conn1, _ := dialWS(t, ctx, wsURL, testClientToken)
	defer conn1.Close(websocket.StatusNormalClosure, "")
	time.Sleep(30 * time.Millisecond)

	// Same session (A) updates pubkey — must be allowed.
	req, _ := http.NewRequest("POST",
		srv.URL+"/chat/"+testRoomID+"/pubkey",
		strings.NewReader(`{"pubkey":"updated_by_same_session"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testClientToken) // same token as WS
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("pubkey POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("same-session pubkey update: got %d, want 200", resp.StatusCode)
	}

	var storedPubkey string
	db.QueryRow(`SELECT client_pubkey FROM chat_rooms WHERE id = ?`, testRoomID).Scan(&storedPubkey)
	if storedPubkey != "updated_by_same_session" {
		t.Errorf("pubkey not updated: got %q", storedPubkey)
	}
}
