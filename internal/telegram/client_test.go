package telegram_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"naroom/internal/telegram"
)

// mockSender records sent messages for assertions.
type mockSender struct {
	clientChatIDs []string
	clientMsgs    []string
	helperChatIDs []string
	helperMsgs    []string
}

func (m *mockSender) SendClientMessage(_ context.Context, chatID, text string) error {
	m.clientChatIDs = append(m.clientChatIDs, chatID)
	m.clientMsgs = append(m.clientMsgs, text)
	return nil
}

func (m *mockSender) SendHelperMessage(_ context.Context, chatID, text string) error {
	m.helperChatIDs = append(m.helperChatIDs, chatID)
	m.helperMsgs = append(m.helperMsgs, text)
	return nil
}

// openTestDB returns an in-memory SQLite DB with the minimal Telegram tables.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	_, err = db.Exec(`
		CREATE TABLE client_listing_notifications (
			id TEXT PRIMARY KEY,
			listing_id TEXT NOT NULL,
			telegram_chat_id TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			active BOOLEAN DEFAULT TRUE
		);
		CREATE TABLE helper_board_subscriptions (
			id TEXT PRIMARY KEY,
			telegram_chat_id TEXT NOT NULL,
			counselor_hash TEXT,
			city TEXT, language TEXT, problem TEXT, help_type TEXT, urgency TEXT,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			active BOOLEAN DEFAULT TRUE
		);
	`)
	if err != nil {
		t.Fatalf("create tables: %v", err)
	}
	return db
}

func insertClientBinding(t *testing.T, db *sql.DB, listingID, chatID string, ttlSec int64) {
	t.Helper()
	now := time.Now().Unix()
	_, err := db.Exec(`
		INSERT INTO client_listing_notifications (id, listing_id, telegram_chat_id, created_at, expires_at, active)
		VALUES (?, ?, ?, ?, ?, TRUE)
	`, "cln-"+chatID, listingID, chatID, now, now+ttlSec)
	if err != nil {
		t.Fatalf("insert client binding: %v", err)
	}
}

func insertHelperSubscription(t *testing.T, db *sql.DB, chatID, counselorHash string, ttlSec int64) {
	t.Helper()
	now := time.Now().Unix()
	var nullHash sql.NullString
	if counselorHash != "" {
		nullHash = sql.NullString{String: counselorHash, Valid: true}
	}
	_, err := db.Exec(`
		INSERT INTO helper_board_subscriptions (id, telegram_chat_id, counselor_hash, created_at, expires_at, active)
		VALUES (?, ?, ?, ?, ?, TRUE)
	`, "hbs-"+chatID, chatID, nullHash, now, now+ttlSec)
	if err != nil {
		t.Fatalf("insert helper subscription: %v", err)
	}
}

// ── TestNotifyClientListingActivated ─────────────────────────────────────────

func TestNotifyClientListingActivated_SendsOnce(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	insertClientBinding(t, db, "lst-1", "chat-111", 3600)

	s := &mockSender{}
	if err := telegram.NotifyClientListingActivated(context.Background(), db, s, "lst-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(s.clientMsgs) != 1 {
		t.Fatalf("expected 1 client message, got %d", len(s.clientMsgs))
	}
	if s.clientMsgs[0] != telegram.ClientListingActivatedMessage {
		t.Errorf("wrong message: %q", s.clientMsgs[0])
	}
	if s.clientChatIDs[0] != "chat-111" {
		t.Errorf("wrong chat_id: %q", s.clientChatIDs[0])
	}
	if len(s.helperMsgs) != 0 {
		t.Errorf("expected no helper messages, got %d", len(s.helperMsgs))
	}
}

func TestNotifyClientListingActivated_ExpiredBinding_NoSend(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	insertClientBinding(t, db, "lst-exp", "chat-999", -1) // already expired

	s := &mockSender{}
	_ = telegram.NotifyClientListingActivated(context.Background(), db, s, "lst-exp")

	if len(s.clientMsgs) != 0 {
		t.Errorf("expected no messages for expired binding, got %d", len(s.clientMsgs))
	}
}

func TestNotifyClientListingActivated_NilSender_NoPanic(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	insertClientBinding(t, db, "lst-nil", "chat-000", 3600)

	// Must not panic
	if err := telegram.NotifyClientListingActivated(context.Background(), db, nil, "lst-nil"); err != nil {
		t.Fatalf("nil sender returned error: %v", err)
	}
}

// ── TestNotifyChatOpened ─────────────────────────────────────────────────────

func TestNotifyChatOpened_SendsToClient(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	insertClientBinding(t, db, "lst-chat", "client-chat-42", 3600)

	s := &mockSender{}
	if err := telegram.NotifyChatOpened(context.Background(), db, s, "lst-chat", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(s.clientMsgs) != 1 {
		t.Fatalf("expected 1 client message, got %d", len(s.clientMsgs))
	}
	if s.clientMsgs[0] != telegram.ChatOpenedClientMessage {
		t.Errorf("wrong client message: %q", s.clientMsgs[0])
	}
	if len(s.helperMsgs) != 0 {
		t.Errorf("expected no helper messages when no counselor_hash given, got %d", len(s.helperMsgs))
	}
}

func TestNotifyChatOpened_SendsToHelper_WhenBindingExists(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	insertClientBinding(t, db, "lst-both", "client-chat-77", 3600)
	insertHelperSubscription(t, db, "helper-chat-88", "counselor-hash-abc", 3600)

	s := &mockSender{}
	if err := telegram.NotifyChatOpened(context.Background(), db, s, "lst-both", "counselor-hash-abc"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(s.clientMsgs) != 1 {
		t.Fatalf("expected 1 client message, got %d", len(s.clientMsgs))
	}
	if s.clientMsgs[0] != telegram.ChatOpenedClientMessage {
		t.Errorf("wrong client message: %q", s.clientMsgs[0])
	}
	if len(s.helperMsgs) != 1 {
		t.Fatalf("expected 1 helper message, got %d", len(s.helperMsgs))
	}
	if s.helperMsgs[0] != telegram.ChatOpenedHelperMessage {
		t.Errorf("wrong helper message: %q", s.helperMsgs[0])
	}
	if s.helperChatIDs[0] != "helper-chat-88" {
		t.Errorf("wrong helper chat_id: %q", s.helperChatIDs[0])
	}
}

func TestNotifyChatOpened_NoHelperSend_WhenNoBindingExists(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	insertClientBinding(t, db, "lst-nohelper", "client-chat-55", 3600)
	// No helper subscription inserted

	s := &mockSender{}
	_ = telegram.NotifyChatOpened(context.Background(), db, s, "lst-nohelper", "counselor-hash-xyz")

	if len(s.helperMsgs) != 0 {
		t.Errorf("expected no helper message when no subscription exists, got %d", len(s.helperMsgs))
	}
}

func TestNotifyChatOpened_NilSender_NoPanic(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	insertClientBinding(t, db, "lst-nil2", "chat-nil", 3600)
	insertHelperSubscription(t, db, "helper-nil", "hash-nil", 3600)

	// Must not panic with nil sender
	if err := telegram.NotifyChatOpened(context.Background(), db, nil, "lst-nil2", "hash-nil"); err != nil {
		t.Fatalf("nil sender returned error: %v", err)
	}
}

// ── TestMessageTextSafety ────────────────────────────────────────────────────
// Verifies that no message constant contains sensitive data: wallet address,
// txid format, invoice address, session token, or pubkey format.

func TestMessageTextSafety(t *testing.T) {
	msgs := []struct {
		name string
		text string
	}{
		{"ClientListingActivatedMessage", telegram.ClientListingActivatedMessage},
		{"ChatOpenedClientMessage", telegram.ChatOpenedClientMessage},
		{"ChatOpenedHelperMessage", telegram.ChatOpenedHelperMessage},
		{"ClientReplyMessage", telegram.ClientReplyMessage},
		{"ClientConfirmText", telegram.ClientConfirmText},
		{"HelperConfirmText", telegram.HelperConfirmText},
	}

	// Patterns that must NOT appear in any message text.
	// These are representative formats of sensitive data.
	forbidden := []struct {
		label   string
		pattern string
	}{
		// Bitcoin/Litecoin address patterns (base58 starting with 1, 3, L, M, bc1, ltc1)
		{"wallet address (starts with 1)", "1A1zP"},
		{"wallet address (bc1 bech32)", "bc1q"},
		{"wallet address (ltc1 bech32)", "ltc1q"},
		// TXID format: 64 hex chars
		{"txid", strings.Repeat("a", 64)},
		// Session token: starts with "tok_" per naroom crypto.NewID
		{"session token prefix", "tok_"},
		// Invoice address: these are HD wallet derived so we can't hard-code them,
		// but the messages shouldn't include "addr" or "address:" with a value.
		{"raw address label", "invoice address"},
		// PEM/raw pubkey
		{"pubkey prefix", "-----BEGIN"},
	}

	for _, m := range msgs {
		for _, f := range forbidden {
			if strings.Contains(m.text, f.pattern) {
				t.Errorf("message %q contains forbidden pattern %q: %q", m.name, f.label, m.text)
			}
		}
	}
}
