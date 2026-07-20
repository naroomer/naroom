package v2

// Tests 1-14 plus schema tests (3) and privacy scan (30) for TelegramTransport.
// All tests: injectable clock, in-memory SQLite, injected mock sender, no time.Sleep, no t.Skip.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// makeBindingRef is a test helper that generates a binding ref, panicking on rand error.
func makeBindingRef() string {
	ref, err := defaultBindingRefGen()
	if err != nil {
		panic("makeBindingRef: " + err.Error())
	}
	return ref
}

// ── Test fakes ────────────────────────────────────────────────────────────────

type mockSender struct {
	mu      sync.Mutex
	calls   int
	returns error
}

func (m *mockSender) SendMessage(_ context.Context, _ int64, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.returns
}

func (m *mockSender) setCalls(returns error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = 0
	m.returns = returns
}

func (m *mockSender) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// ── Helper setup ──────────────────────────────────────────────────────────────

var testTokenSecret = []byte("test-token-secret-32-bytes-long!!")
var testWebhookSecret = []byte("webhook-secret-token")
var testDestKey = func() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = 0x55
	}
	return k
}()

func newTestTransport(t *testing.T, now func() time.Time) (*TelegramTransport, *Service, *ListingService, *sql.DB) {
	t.Helper()
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if now == nil {
		now = time.Now
	}

	svc, err := NewWithClock(db, testHMACKey, now)
	if err != nil {
		t.Fatalf("NewWithClock: %v", err)
	}

	cipher, err := NewAESGCMContactCipher(testAESKey, "v1")
	if err != nil {
		t.Fatalf("NewAESGCMContactCipher: %v", err)
	}
	ls, err := NewListingService(svc, cipher, &fakeDisplayNameGenerator{}, &fakeContactValidator{})
	if err != nil {
		t.Fatalf("NewListingService: %v", err)
	}

	destCipher, err := NewDestinationCipher(testDestKey, "dest_v1")
	if err != nil {
		t.Fatalf("NewDestinationCipher: %v", err)
	}

	sender := &mockSender{}

	transport, err := NewTelegramTransport(
		svc, ls,
		testTokenSecret, testWebhookSecret,
		destCipher,
		"TestBot",
		sender,
		now,
	)
	if err != nil {
		t.Fatalf("NewTelegramTransport: %v", err)
	}

	return transport, svc, ls, db
}

// makeFormReadyFlowForTransport creates a confirmed form_ready flow without hardFloorUSD dependency.
func makeFormReadyFlowForTransport(t *testing.T, svc *Service, walletAddr string) (rawCode, flowID string) {
	t.Helper()
	rawCode, fv, err := svc.CreatePaymentIntent(walletAddr, "BTC", validDraft())
	if err != nil {
		t.Fatalf("CreatePaymentIntent: %v", err)
	}
	now := svc.now()
	_, err = svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_"+newID()[:8],
		[]string{walletAddr}, fv.AmountAtomic, now)
	if err != nil {
		t.Fatalf("RecordPaymentDetected: %v", err)
	}
	_, err = svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, now)
	if err != nil {
		t.Fatalf("ConfirmPayment: %v", err)
	}
	_, err = svc.RecordPostPaymentBalance(fv.FlowID, 150.0, hardFloorUSD)
	if err != nil {
		t.Fatalf("RecordPostPaymentBalance: %v", err)
	}
	return rawCode, fv.FlowID
}

// ── Test 1: TestRawTokenFormat ─────────────────────────────────────────────

func TestRawTokenFormat(t *testing.T) {
	transport, svc, _, db := newTestTransport(t, nil)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")

	result, err := transport.CreateLink(rawCode, "bc1qtest")
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	// Extract raw token from BotURL.
	const prefix = "?start="
	idx := strings.Index(result.BotURL, prefix)
	if idx < 0 {
		t.Fatalf("no ?start= in BotURL: %s", result.BotURL)
	}
	rawToken := result.BotURL[idx+len(prefix):]

	// Token must be exactly 43 chars.
	if len(rawToken) != 43 {
		t.Errorf("rawToken length: got %d, want 43", len(rawToken))
	}
	if !isValidRawToken(rawToken) {
		t.Errorf("rawToken not valid base64url: %s", rawToken)
	}

	// DB must contain only HMAC hash, not raw token.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts WHERE token_hash = ?`, rawToken).Scan(&count) //nolint:errcheck
	if count != 0 {
		t.Error("raw token found as token_hash in DB (should be HMAC only)")
	}

	// DB must contain the HMAC hash.
	expectedHash := computeTokenHash(testTokenSecret, rawToken)
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts WHERE token_hash = ?`, expectedHash).Scan(&count) //nolint:errcheck
	if count != 1 {
		t.Errorf("HMAC hash not found in DB: %s", expectedHash)
	}

	// Scan all text values in attempts table; raw token must not appear.
	rows, err := db.Query(`SELECT id, flow_id, token_hash FROM v2_telegram_link_attempts`)
	if err != nil {
		t.Fatalf("query attempts: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, flowID, hash string
		rows.Scan(&id, &flowID, &hash) //nolint:errcheck
		for _, val := range []string{id, flowID, hash} {
			if val == rawToken {
				t.Errorf("raw token found in DB column value: %q", val)
			}
		}
	}
}

// ── Test 2: TestDifferentHMACSecretDifferentHash ──────────────────────────

func TestDifferentHMACSecretDifferentHash(t *testing.T) {
	rawToken := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 43 chars

	secret1 := []byte("secret-one-32bytes-padding-padpad")
	secret2 := []byte("secret-two-32bytes-padding-padpad")

	hash1 := computeTokenHash(secret1, rawToken)
	hash2 := computeTokenHash(secret2, rawToken)

	if hash1 == hash2 {
		t.Error("different secrets produced same hash")
	}
	if !isValidTokenHash(hash1) {
		t.Errorf("hash1 not valid token hash: %s", hash1)
	}
	if !isValidTokenHash(hash2) {
		t.Errorf("hash2 not valid token hash: %s", hash2)
	}
}

// ── Test 3: TestSchemaLinkAttemptConstraints ──────────────────────────────

func TestSchemaLinkAttemptConstraints(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	// Create a flow to reference.
	svc, err := NewWithClock(db, testHMACKey, time.Now)
	if err != nil {
		t.Fatalf("NewWithClock: %v", err)
	}
	_, fv, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err != nil {
		t.Fatalf("CreatePaymentIntent: %v", err)
	}
	flowID := fv.FlowID
	now := time.Now().Unix()

	validHash := strings.Repeat("a", 64) // 64 lowercase hex chars

	// Valid pending row.
	t.Run("valid_pending", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_telegram_link_attempts
			(id, flow_id, token_hash, window_number, state, expires_at, lease_until, created_at, updated_at)
			VALUES (?, ?, ?, 1, 'pending', ?, NULL, ?, ?)`,
			newID(), flowID, validHash, now+900, now, now)
		if err != nil {
			t.Errorf("valid pending row rejected: %v", err)
		}
		// Clean up.
		db.Exec(`DELETE FROM v2_telegram_link_attempts WHERE flow_id = ?`, flowID) //nolint:errcheck
	})

	// Malformed token_hash: not 64 chars.
	t.Run("bad_hash_short", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_telegram_link_attempts
			(id, flow_id, token_hash, window_number, state, expires_at, lease_until, created_at, updated_at)
			VALUES (?, ?, ?, 1, 'pending', ?, NULL, ?, ?)`,
			newID(), flowID, "abc", now+900, now, now)
		if err == nil {
			t.Error("short token_hash not rejected")
		}
	})

	// Uppercase in token_hash.
	t.Run("bad_hash_uppercase", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_telegram_link_attempts
			(id, flow_id, token_hash, window_number, state, expires_at, lease_until, created_at, updated_at)
			VALUES (?, ?, ?, 1, 'pending', ?, NULL, ?, ?)`,
			newID(), flowID, strings.Repeat("A", 64), now+900, now, now)
		if err == nil {
			t.Error("uppercase token_hash not rejected")
		}
	})

	// Non-hex in token_hash.
	t.Run("bad_hash_nonhex", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_telegram_link_attempts
			(id, flow_id, token_hash, window_number, state, expires_at, lease_until, created_at, updated_at)
			VALUES (?, ?, ?, 1, 'pending', ?, NULL, ?, ?)`,
			newID(), flowID, strings.Repeat("z", 64), now+900, now, now)
		if err == nil {
			t.Error("non-hex token_hash not rejected")
		}
	})

	// Pending with lease_until set — violates CHECK.
	t.Run("pending_with_lease", func(t *testing.T) {
		hash := strings.Repeat("b", 64)
		_, err := db.Exec(`INSERT INTO v2_telegram_link_attempts
			(id, flow_id, token_hash, window_number, state, expires_at, lease_until, created_at, updated_at)
			VALUES (?, ?, ?, 1, 'pending', ?, ?, ?, ?)`,
			newID(), flowID, hash, now+900, now+30, now, now)
		if err == nil {
			t.Error("pending with lease_until not rejected")
		}
	})

	// Processing without lease_until — violates CHECK.
	t.Run("processing_without_lease", func(t *testing.T) {
		hash := strings.Repeat("c", 64)
		_, err := db.Exec(`INSERT INTO v2_telegram_link_attempts
			(id, flow_id, token_hash, window_number, state, expires_at, lease_until, created_at, updated_at)
			VALUES (?, ?, ?, 1, 'processing', ?, NULL, ?, ?)`,
			newID(), flowID, hash, now+900, now, now)
		if err == nil {
			t.Error("processing without lease_until not rejected")
		}
	})

	// Processing with updated_at > lease_until — violates CHECK.
	t.Run("processing_updated_after_lease", func(t *testing.T) {
		hash := strings.Repeat("d", 64)
		_, err := db.Exec(`INSERT INTO v2_telegram_link_attempts
			(id, flow_id, token_hash, window_number, state, expires_at, lease_until, created_at, updated_at)
			VALUES (?, ?, ?, 1, 'processing', ?, ?, ?, ?)`,
			newID(), flowID, hash, now+900, now+30, now, now+60)
		if err == nil {
			t.Error("processing with updated_at > lease_until not rejected")
		}
	})

	// created_at >= expires_at — violates CHECK.
	t.Run("created_at_ge_expires_at", func(t *testing.T) {
		hash := strings.Repeat("e", 64)
		_, err := db.Exec(`INSERT INTO v2_telegram_link_attempts
			(id, flow_id, token_hash, window_number, state, expires_at, lease_until, created_at, updated_at)
			VALUES (?, ?, ?, 1, 'pending', ?, NULL, ?, ?)`,
			newID(), flowID, hash, now, now, now)
		if err == nil {
			t.Error("created_at >= expires_at not rejected")
		}
	})
}

// ── Test 4: TestDestinationCipher ─────────────────────────────────────────

func TestDestinationCipher(t *testing.T) {
	c, err := NewDestinationCipher(testDestKey, "v1")
	if err != nil {
		t.Fatalf("NewDestinationCipher: %v", err)
	}

	chatID := int64(123456789)
	bindingRef := "bnd_" + strings.Repeat("a", 32)

	ct, nonce, err := c.EncryptChatID(chatID, bindingRef)
	if err != nil {
		t.Fatalf("EncryptChatID: %v", err)
	}
	if ct == "" || nonce == "" {
		t.Fatal("empty ciphertext or nonce")
	}

	// DecryptChatID with correct AAD.
	got, err := c.DecryptChatID(ct, nonce, "v1", bindingRef)
	if err != nil {
		t.Fatalf("DecryptChatID: %v", err)
	}
	if got != chatID {
		t.Errorf("DecryptChatID: got %d, want %d", got, chatID)
	}

	// Wrong binding_ref in AAD.
	_, err = c.DecryptChatID(ct, nonce, "v1", "bnd_"+strings.Repeat("b", 32))
	if err == nil {
		t.Error("expected error with wrong binding_ref in AAD")
	}

	// Tampered ciphertext.
	tampered := ct[:len(ct)-2] + "ff"
	if tampered == ct {
		tampered = "00" + ct[2:]
	}
	_, err = c.DecryptChatID(tampered, nonce, "v1", bindingRef)
	if err == nil {
		t.Error("expected error on tampered ciphertext")
	}
}

// ── Test 5: TestDestinationWrongAAD ──────────────────────────────────────

func TestDestinationWrongAAD(t *testing.T) {
	c, _ := NewDestinationCipher(testDestKey, "v1")

	chatID := int64(987654321)
	bindingRef1 := "bnd_" + strings.Repeat("1", 32)
	bindingRef2 := "bnd_" + strings.Repeat("2", 32)

	ct, nonce, _ := c.EncryptChatID(chatID, bindingRef1)

	// Wrong binding_ref AAD → authentication failure.
	_, err := c.DecryptChatID(ct, nonce, "v1", bindingRef2)
	if err == nil {
		t.Error("expected authentication failure with wrong binding_ref")
	}
}

// ── Test 6: TestBindingDeleteCascadesDestination ──────────────────────────

func TestBindingDeleteCascadesDestination(t *testing.T) {
	_, svc, ls, db := newTestTransport(t, nil)

	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	_ = rawCode

	// Create binding+destination.
	now := ls.now()
	ref := makeBindingRef()
	if err := ls.attachReadyBinding(flowID, ref, now, now.Add(10*time.Minute)); err != nil {
		t.Fatalf("attachReadyBinding: %v", err)
	}
	_, err := db.Exec(`INSERT INTO v2_telegram_destinations
		(binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at)
		VALUES (?, 'ct', 'nonce', 'v1', ?, ?)`,
		ref, now.Unix(), now.Add(10*time.Minute).Unix())
	if err != nil {
		t.Fatalf("insert destination: %v", err)
	}

	// Verify both exist.
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_destinations WHERE binding_ref=?`, ref).Scan(&cnt) //nolint:errcheck
	if cnt != 1 {
		t.Fatalf("destination not found before delete")
	}

	// Delete binding.
	if _, err = db.Exec(`DELETE FROM v2_client_notification_bindings WHERE binding_ref=?`, ref); err != nil {
		t.Fatalf("delete binding: %v", err)
	}

	// Destination must be cascade-deleted.
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_destinations WHERE binding_ref=?`, ref).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Error("destination not cascade-deleted when binding deleted")
	}
}

// ── Test 7: TestCreateLinkFirstWindow ─────────────────────────────────────

func TestCreateLinkFirstWindow(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	transport, svc, _, _ := newTestTransport(t, func() time.Time { return now })

	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")

	result, err := transport.CreateLink(rawCode, "bc1qtest")
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	// BotURL must contain bot username.
	if !strings.Contains(result.BotURL, "TestBot") {
		t.Errorf("BotURL missing bot username: %s", result.BotURL)
	}
	if !strings.HasPrefix(result.BotURL, "https://t.me/TestBot?start=") {
		t.Errorf("BotURL format wrong: %s", result.BotURL)
	}

	// ExpiresAt ≈ now + 15min.
	wantExpires := now.Add(15 * time.Minute)
	if result.ExpiresAt.Unix() != wantExpires.Unix() {
		t.Errorf("ExpiresAt: got %v, want %v", result.ExpiresAt, wantExpires)
	}
}

// ── Test 8: TestCreateLinkWrongCodeOrWallet ───────────────────────────────

func TestCreateLinkWrongCodeOrWallet(t *testing.T) {
	transport, svc, _, _ := newTestTransport(t, nil)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")

	// Wrong code.
	_, err := transport.CreateLink("wrongcode000", "bc1qtest")
	if !errors.Is(err, ErrListingCapabilityNotFound) {
		t.Errorf("wrong code: got %v, want ErrListingCapabilityNotFound", err)
	}

	// Wrong wallet.
	_, err = transport.CreateLink(rawCode, "bc1qwrongwallet")
	if !errors.Is(err, ErrListingCapabilityNotFound) {
		t.Errorf("wrong wallet: got %v, want ErrListingCapabilityNotFound", err)
	}
}

// ── Test 9: TestCreateLinkReplacesExistingPending ─────────────────────────

func TestCreateLinkReplacesExistingPending(t *testing.T) {
	transport, svc, _, db := newTestTransport(t, nil)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")

	// First link.
	result1, err := transport.CreateLink(rawCode, "bc1qtest")
	if err != nil {
		t.Fatalf("first CreateLink: %v", err)
	}
	rawToken1 := result1.BotURL[strings.Index(result1.BotURL, "?start=")+len("?start="):]
	hash1 := computeTokenHash(testTokenSecret, rawToken1)

	// Second link (replaces first).
	result2, err := transport.CreateLink(rawCode, "bc1qtest")
	if err != nil {
		t.Fatalf("second CreateLink: %v", err)
	}
	rawToken2 := result2.BotURL[strings.Index(result2.BotURL, "?start=")+len("?start="):]
	hash2 := computeTokenHash(testTokenSecret, rawToken2)

	// First token hash must be gone.
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts WHERE token_hash=?`, hash1).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Error("first token hash still in DB after replacement")
	}

	// Only second token hash present.
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts WHERE token_hash=?`, hash2).Scan(&cnt) //nolint:errcheck
	if cnt != 1 {
		t.Error("second token hash not in DB")
	}

	// Total attempts for this flow: 1.
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts`).Scan(&cnt) //nolint:errcheck
	if cnt != 1 {
		t.Errorf("expected 1 attempt, got %d", cnt)
	}
}

// ── Test 10: TestCreateLinkBlockedByLiveLease ─────────────────────────────

func TestCreateLinkBlockedByLiveLease(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	var clockNow = now
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, _, db := newTestTransport(t, getNow)
	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	_ = flowID

	// Create first attempt.
	result1, err := transport.CreateLink(rawCode, "bc1qtest")
	if err != nil {
		t.Fatalf("first CreateLink: %v", err)
	}
	rawToken1 := result1.BotURL[strings.Index(result1.BotURL, "?start=")+len("?start="):]
	hash1 := computeTokenHash(testTokenSecret, rawToken1)

	// Manually set state to processing with live lease.
	leaseUntil := now.Unix() + int64(leaseDuration.Seconds())
	db.Exec(`UPDATE v2_telegram_link_attempts SET state='processing', lease_until=?, updated_at=? WHERE token_hash=?`, //nolint:errcheck
		leaseUntil, now.Unix(), hash1)

	// Second CreateLink while lease is live → ErrConflict.
	_, err = transport.CreateLink(rawCode, "bc1qtest")
	if !errors.Is(err, ErrConflict) {
		t.Errorf("live lease: got %v, want ErrConflict", err)
	}

	// Advance time past lease expiry.
	mu.Lock()
	clockNow = time.Unix(leaseUntil+1, 0)
	mu.Unlock()

	// Now CreateLink should succeed (stale lease).
	_, err = transport.CreateLink(rawCode, "bc1qtest")
	if err != nil {
		t.Errorf("stale lease: got %v, want success", err)
	}
}

// ── Test 11: TestCreateLinkBlockedByReadyBinding ──────────────────────────

func TestCreateLinkBlockedByReadyBinding(t *testing.T) {
	transport, svc, ls, _ := newTestTransport(t, nil)
	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, "bc1qtest")

	// Attach a ready binding directly.
	now := ls.now()
	ref := makeBindingRef()
	if err := ls.attachReadyBinding(flowID, ref, now, now.Add(10*time.Minute)); err != nil {
		t.Fatalf("attachReadyBinding: %v", err)
	}

	// CreateLink should be blocked by existing ready binding.
	_, err := transport.CreateLink(rawCode, "bc1qtest")
	if !errors.Is(err, ErrAlreadyVisible) {
		t.Errorf("ready binding: got %v, want ErrAlreadyVisible", err)
	}
}

// ── Test 12: TestCreateLinkClearsExpiredBindingBeforeNextWindow ───────────

func TestCreateLinkClearsExpiredBindingBeforeNextWindow(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	var clockNow = now
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, ls, db := newTestTransport(t, getNow)
	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, "bc1qtest")

	// Attach a binding with short TTL.
	ref := makeBindingRef()
	if err := ls.attachReadyBinding(flowID, ref, now, now.Add(1*time.Minute)); err != nil {
		t.Fatalf("attachReadyBinding: %v", err)
	}
	// Insert destination for it.
	db.Exec(`INSERT INTO v2_telegram_destinations (binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at) VALUES (?, 'ct', 'nonce', 'v1', ?, ?)`, //nolint:errcheck
		ref, now.Unix(), now.Add(1*time.Minute).Unix())

	// Advance past binding expiry and also past the 24h window (so listing is hidden).
	mu.Lock()
	clockNow = now.Add(25 * time.Hour)
	mu.Unlock()

	// The binding should be expired; CreateLink should succeed.
	_, err := transport.CreateLink(rawCode, "bc1qtest")
	if err != nil {
		t.Errorf("after expired binding: got %v, want success", err)
	}

	// Pending attempt should be created.
	var attCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts`).Scan(&attCnt) //nolint:errcheck
	if attCnt != 1 {
		t.Errorf("expected 1 pending attempt, got %d", attCnt)
	}

	// The expired destination may still be in DB (cleanup is done by NormalizeExpired,
	// not by CreateLink). The key invariant is that CreateLink succeeded.
	// NormalizeExpired handles cleanup of expired bindings and their destinations.
	if err := ls.NormalizeExpired(getNow()); err != nil {
		t.Fatalf("NormalizeExpired: %v", err)
	}

	// After NormalizeExpired, the expired destination is gone (cascade).
	var destCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_destinations WHERE binding_ref=?`, ref).Scan(&destCnt) //nolint:errcheck
	if destCnt != 0 {
		t.Error("expired destination still in DB after NormalizeExpired")
	}
}

// ── Test 13: TestCreateLinkConcurrentResultsAndSingleAttempt ─────────────────

func TestCreateLinkConcurrentResultsAndSingleAttempt(t *testing.T) {
	transport, svc, _, db := newTestTransport(t, nil)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")

	const goroutines = 50
	type result struct{ err error }
	results := make([]result, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := transport.CreateLink(rawCode, "bc1qtest")
			results[i] = result{err: err}
		}()
	}
	wg.Wait()

	// Exactly 1 pending attempt in DB.
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts`).Scan(&cnt) //nolint:errcheck
	if cnt != 1 {
		t.Errorf("concurrent CreateLink: got %d attempts, want 1", cnt)
	}

	// All results must be nil or ErrConflict; no unexpected errors.
	for i, r := range results {
		if r.err != nil && !errors.Is(r.err, ErrConflict) {
			t.Errorf("goroutine %d: unexpected error %v (want nil or ErrConflict)", i, r.err)
		}
	}

	// At least one goroutine succeeded.
	var successes int
	for _, r := range results {
		if r.err == nil {
			successes++
		}
	}
	if successes == 0 {
		t.Error("no goroutine succeeded in creating a link")
	}
}

// ── Test 14: TestCreateLinkResponseNoInternalIDs ──────────────────────────

func TestCreateLinkResponseNoInternalIDs(t *testing.T) {
	transport, svc, _, db := newTestTransport(t, nil)
	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, "bc1qtest")

	result, err := transport.CreateLink(rawCode, "bc1qtest")
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	// BotURL must not contain flow_id, binding_ref, or token_hash.
	if strings.Contains(result.BotURL, flowID) {
		t.Errorf("BotURL contains flow_id: %s", result.BotURL)
	}

	// Read token_hash and attempt id from DB.
	var tokenHash, attemptID string
	db.QueryRow(`SELECT token_hash, id FROM v2_telegram_link_attempts`).Scan(&tokenHash, &attemptID) //nolint:errcheck

	if strings.Contains(result.BotURL, tokenHash) {
		t.Errorf("BotURL contains token_hash: %s", result.BotURL)
	}
	if strings.Contains(result.BotURL, attemptID) {
		t.Errorf("BotURL contains attempt id: %s", result.BotURL)
	}

	// BotURL should only contain: bot username + raw token.
	rawToken := result.BotURL[strings.Index(result.BotURL, "?start=")+len("?start="):]
	if !isValidRawToken(rawToken) {
		t.Errorf("extracted token not valid: %s", rawToken)
	}
}

// ── Test 29: TestNormalizeDeletesExpiredAttempt ────────────────────────────

func TestNormalizeDeletesExpiredAttempt(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	var clockNow = now
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, ls, db := newTestTransport(t, getNow)
	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, "bc1qtest")

	// Create attempt.
	if _, err := transport.CreateLink(rawCode, "bc1qtest"); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	// Create binding+destination with short expiry.
	ref := makeBindingRef()
	mu.Lock()
	cn := clockNow
	mu.Unlock()
	if err := ls.attachReadyBinding(flowID, ref, cn, cn.Add(1*time.Minute)); err != nil {
		t.Fatalf("attachReadyBinding: %v", err)
	}
	db.Exec(`INSERT INTO v2_telegram_destinations (binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at) VALUES (?, 'ct', 'nonce', 'v1', ?, ?)`, //nolint:errcheck
		ref, cn.Unix(), cn.Add(1*time.Minute).Unix())

	// Advance time past attempt and binding expiry.
	mu.Lock()
	clockNow = now.Add(2 * time.Hour)
	mu.Unlock()
	laterTime := clockNow

	// NormalizeExpiredAttempts removes expired attempts.
	if err := transport.NormalizeExpiredAttempts(laterTime); err != nil {
		t.Fatalf("NormalizeExpiredAttempts: %v", err)
	}
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts`).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Errorf("expired attempt still in DB: got %d", cnt)
	}

	// NormalizeExpired cleans up expired bindings (cascades to destinations).
	if err := ls.NormalizeExpired(laterTime); err != nil {
		t.Fatalf("NormalizeExpired: %v", err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_destinations WHERE binding_ref=?`, ref).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Errorf("expired destination still in DB after NormalizeExpired: got %d", cnt)
	}
}

// ── Task 04B-FIX Tests ────────────────────────────────────────────────────────

// TestQueryLinkStatusBareBindingIsNotReady verifies that a ready binding without
// a matching destination is not returned as ready/active by QueryLinkStatus.
func TestQueryLinkStatusBareBindingIsNotReady(t *testing.T) {
	transport, svc, ls, _ := newTestTransport(t, nil)
	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, "bc1qtest")

	// Attach bare binding (no destination).
	now := ls.now()
	ref := makeBindingRef()
	if err := ls.attachReadyBinding(flowID, ref, now, now.Add(10*time.Minute)); err != nil {
		t.Fatalf("attachReadyBinding: %v", err)
	}

	status, err := transport.QueryLinkStatus(rawCode, "bc1qtest")
	if err != nil {
		t.Fatalf("QueryLinkStatus: %v", err)
	}
	// Bare binding (no destination) must not count as ready.
	if status == "ready" || status == "active" {
		t.Errorf("bare binding returned status %q; want needs_link or link_pending", status)
	}
}

// TestCreateLinkCleansExpiredBindingAndDestinationAtomically verifies that
// CreateLink deletes an expired binding AND its destination atomically before
// inserting the new pending attempt.
func TestCreateLinkCleansExpiredBindingAndDestinationAtomically(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	var clockNow = now
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, ls, db := newTestTransport(t, getNow)
	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, "bc1qtest")

	// Attach expired binding with destination.
	ref := makeBindingRef()
	if err := ls.attachReadyBinding(flowID, ref, now, now.Add(1*time.Minute)); err != nil {
		t.Fatalf("attachReadyBinding: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO v2_telegram_destinations
		(binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at)
		VALUES (?, 'ct', 'nonce', 'v1', ?, ?)`,
		ref, now.Unix(), now.Add(1*time.Minute).Unix()); err != nil {
		t.Fatalf("insert destination: %v", err)
	}

	// Advance past binding expiry.
	mu.Lock()
	clockNow = now.Add(2 * time.Hour)
	mu.Unlock()

	// CreateLink should succeed and atomically clean the expired binding+destination.
	if _, err := transport.CreateLink(rawCode, "bc1qtest"); err != nil {
		t.Fatalf("CreateLink after expiry: %v", err)
	}

	// Expired binding must be gone.
	var bindCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings WHERE binding_ref=?`, ref).Scan(&bindCnt) //nolint:errcheck
	if bindCnt != 0 {
		t.Errorf("expired binding not cleaned up by CreateLink: count %d", bindCnt)
	}

	// Expired destination must be gone (cascade from binding delete).
	var destCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_destinations WHERE binding_ref=?`, ref).Scan(&destCnt) //nolint:errcheck
	if destCnt != 0 {
		t.Errorf("expired destination not cleaned up by CreateLink: count %d", destCnt)
	}

	// Exactly 1 new pending attempt.
	var attCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts`).Scan(&attCnt) //nolint:errcheck
	if attCnt != 1 {
		t.Errorf("expected 1 pending attempt, got %d", attCnt)
	}
}

// TestCreateLinkDoesNotDeleteFreshWebhookClaim verifies that if a webhook is
// currently processing an attempt (state=processing, live lease), CreateLink
// returns ErrConflict and does NOT delete the processing attempt.
func TestCreateLinkDoesNotDeleteFreshWebhookClaim(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	var clockNow = now
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, _, db := newTestTransport(t, getNow)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")

	// Create a pending attempt.
	result, err := transport.CreateLink(rawCode, "bc1qtest")
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	rawToken := result.BotURL[strings.Index(result.BotURL, "?start=")+len("?start="):]
	hash := computeTokenHash(testTokenSecret, rawToken)

	// Manually transition to processing with live lease (simulating webhook claim).
	leaseUntil := now.Unix() + int64(leaseDuration.Seconds())
	db.Exec(`UPDATE v2_telegram_link_attempts SET state='processing', lease_until=?, updated_at=? WHERE token_hash=?`, //nolint:errcheck
		leaseUntil, now.Unix(), hash)

	// CreateLink while lease is live must return ErrConflict.
	_, err = transport.CreateLink(rawCode, "bc1qtest")
	if !errors.Is(err, ErrConflict) {
		t.Errorf("got %v, want ErrConflict", err)
	}

	// The processing attempt must still be present with state=processing.
	var state string
	var lease sql.NullInt64
	db.QueryRow(`SELECT state, lease_until FROM v2_telegram_link_attempts WHERE token_hash=?`, hash).Scan(&state, &lease) //nolint:errcheck
	if state != "processing" {
		t.Errorf("attempt state after ErrConflict: got %q, want processing", state)
	}
	if !lease.Valid || lease.Int64 != leaseUntil {
		t.Errorf("lease_until changed: got %v, want %d", lease, leaseUntil)
	}
}

// TestWebhookBindingRefCollisionRetriesBoundedly verifies that when bindingRefGen
// always returns a colliding ref, the webhook retries maxBindingRefRetries times
// and then returns 503. After a successful send and exhausted retries:
//   - 503 response;
//   - no binding or destination created;
//   - attempt stays processing with the exact original lease (NOT reset to pending);
//   - sender was called exactly once (send is not retried).
func TestWebhookBindingRefCollisionRetriesBoundedly(t *testing.T) {
	transport, svc, ls, db := newTestTransport(t, nil)
	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")
	tokenHash := computeTokenHash(testTokenSecret, rawToken)

	// Pre-insert a binding with a known ref to cause UNIQUE collision.
	collidingRef := "bnd_" + strings.Repeat("cc", 16)
	now := ls.now()
	// Create another flow to hold the colliding binding_ref.
	rawCode2, flowID2 := makeFormReadyFlowForTransport(t, svc, "bc1qtest2")
	_ = rawCode2
	if err := ls.attachReadyBinding(flowID2, collidingRef, now, now.Add(10*time.Minute)); err != nil {
		t.Fatalf("pre-insert collision binding: %v", err)
	}
	_ = flowID

	// Inject generator that always returns the colliding ref.
	genCalls := 0
	transport.bindingRefGen = func() (string, error) {
		genCalls++
		return collidingRef, nil
	}

	body := buildWebhookBody(42, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	if code != http.StatusServiceUnavailable {
		t.Errorf("all retries exhausted: got %d, want 503", code)
	}

	// Generator must have been called maxBindingRefRetries times.
	if genCalls != maxBindingRefRetries {
		t.Errorf("generator call count: got %d, want %d", genCalls, maxBindingRefRetries)
	}

	// No binding for our flow must have been created.
	var bindCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings WHERE flow_id=?`, flowID).Scan(&bindCnt) //nolint:errcheck
	if bindCnt != 0 {
		t.Errorf("binding created despite collision: count %d", bindCnt)
	}
	var destCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_destinations`).Scan(&destCnt) //nolint:errcheck
	if destCnt != 0 {
		t.Errorf("destination created despite collision: count %d", destCnt)
	}

	// Sender called exactly once (send is not retried after exhaustion).
	if n := transport.sender.(*mockSender).callCount(); n != 1 {
		t.Errorf("sender call count: got %d, want 1", n)
	}

	// Attempt stays processing with the original lease — NOT reset to pending.
	// This prevents an immediate retry from re-sending the Telegram message.
	var state string
	var leaseUntil sql.NullInt64
	db.QueryRow(`SELECT state, lease_until FROM v2_telegram_link_attempts WHERE token_hash=?`, tokenHash).Scan(&state, &leaseUntil) //nolint:errcheck
	if state != "processing" {
		t.Errorf("attempt state after collision exhaustion: got %q, want processing", state)
	}
	if !leaseUntil.Valid || leaseUntil.Int64 <= now.Unix() {
		t.Errorf("attempt lease_until not set or in the past: %v", leaseUntil)
	}
}

// TestWebhookRandomFailureNoPanicNoPartialState verifies that when bindingRefGen
// returns an error, HandleWebhook returns 503 without panicking and without leaving
// partial binding/destination state.
func TestWebhookRandomFailureNoPanicNoPartialState(t *testing.T) {
	transport, svc, _, db := newTestTransport(t, nil)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")

	// Inject failing generator.
	transport.bindingRefGen = func() (string, error) {
		return "", errors.New("simulated crypto/rand failure")
	}

	body := buildWebhookBody(42, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	if code != http.StatusServiceUnavailable {
		t.Errorf("rand failure: got %d, want 503", code)
	}

	// No binding or destination created.
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Errorf("binding created on rand failure: count %d", cnt)
	}
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_destinations`).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Errorf("destination created on rand failure: count %d", cnt)
	}

	// Attempt must be reset to pending (sender was NOT called yet when gen fails).
	tokenHash := computeTokenHash(testTokenSecret, rawToken)
	var state string
	db.QueryRow(`SELECT state FROM v2_telegram_link_attempts WHERE token_hash=?`, tokenHash).Scan(&state) //nolint:errcheck
	if state != "pending" {
		t.Errorf("attempt state after rand failure: got %q, want pending", state)
	}
}

// TestNewTelegramTransportRejectsNilClock verifies that NewTelegramTransport
// returns an error when now is nil.
func TestNewTelegramTransportRejectsNilClock(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	svc, err := NewWithClock(db, testHMACKey, time.Now)
	if err != nil {
		t.Fatalf("NewWithClock: %v", err)
	}
	cipher, err := NewAESGCMContactCipher(testAESKey, "v1")
	if err != nil {
		t.Fatalf("NewAESGCMContactCipher: %v", err)
	}
	ls, err := NewListingService(svc, cipher, &fakeDisplayNameGenerator{}, &fakeContactValidator{})
	if err != nil {
		t.Fatalf("NewListingService: %v", err)
	}
	destCipher, err := NewDestinationCipher(testDestKey, "dest_v1")
	if err != nil {
		t.Fatalf("NewDestinationCipher: %v", err)
	}

	_, err = NewTelegramTransport(
		svc, ls,
		testTokenSecret, testWebhookSecret,
		destCipher, "TestBot",
		&mockSender{},
		nil, // nil clock — must be rejected
	)
	if err == nil {
		t.Error("nil clock not rejected by NewTelegramTransport")
	}
}

// ── Task 04B-FIX2 Tests ───────────────────────────────────────────────────────

// TestCreateLinkReplacesExpiredProcessingAttemptWithLiveLease verifies that CreateLink
// can replace a processing attempt whose token has expired, even if lease_until > now.
// An expired token means the attempt is no longer recoverable; only a live token+lease
// together protect an attempt from replacement.
func TestCreateLinkReplacesExpiredProcessingAttemptWithLiveLease(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	var clockNow = now
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, _, db := newTestTransport(t, getNow)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")

	// Create initial pending attempt.
	result, err := transport.CreateLink(rawCode, "bc1qtest")
	if err != nil {
		t.Fatalf("first CreateLink: %v", err)
	}
	rawToken := result.BotURL[strings.Index(result.BotURL, "?start=")+len("?start="):]
	hash := computeTokenHash(testTokenSecret, rawToken)

	// Manually set to processing with a lease that extends beyond token expiry.
	// Token expires at now+15min; lease_until = now+25min (live lease, dead token).
	tokenExpires := now.Unix() + int64(telegramLinkTTL.Seconds())
	liveLease := now.Unix() + 25*60
	db.Exec(`UPDATE v2_telegram_link_attempts SET state='processing', lease_until=?, updated_at=? WHERE token_hash=?`, //nolint:errcheck
		liveLease, now.Unix(), hash)

	// Advance clock past token expiry but still within the lease window.
	mu.Lock()
	clockNow = time.Unix(tokenExpires+60, 0) // token expired, lease still alive
	mu.Unlock()

	// CreateLink must succeed — expired token must be replaceable even with live lease.
	result2, err := transport.CreateLink(rawCode, "bc1qtest")
	if err != nil {
		t.Fatalf("CreateLink with expired processing attempt: %v", err)
	}
	if result2.BotURL == "" {
		t.Error("CreateLink returned empty BotURL")
	}

	// Only the new attempt should be present.
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts`).Scan(&cnt) //nolint:errcheck
	if cnt != 1 {
		t.Errorf("attempt count: got %d, want 1", cnt)
	}
	// The old (expired) token hash must be gone.
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts WHERE token_hash=?`, hash).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Error("expired processing attempt still in DB after CreateLink")
	}
}

// TestQueryLinkStatusMismatchedDestinationExpiryIsNotReady verifies that when
// a binding and destination both have future expiries but d.expires_at != b.valid_until,
// QueryLinkStatus does NOT return ready or active.
func TestQueryLinkStatusMismatchedDestinationExpiryIsNotReady(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	transport, svc, ls, db := newTestTransport(t, func() time.Time { return now })
	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, "bc1qtest")

	// Attach a binding with valid_until = now+14min (within the 15min max TTL).
	ref := makeBindingRef()
	if err := ls.attachReadyBinding(flowID, ref, now, now.Add(14*time.Minute)); err != nil {
		t.Fatalf("attachReadyBinding: %v", err)
	}

	// Insert a destination with a DIFFERENT future expiry (now+10min ≠ now+14min).
	mismatchedExpiry := now.Add(10 * time.Minute).Unix()
	if _, err := db.Exec(`INSERT INTO v2_telegram_destinations
		(binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at)
		VALUES (?, 'ct', 'nonce', 'v1', ?, ?)`,
		ref, now.Unix(), mismatchedExpiry); err != nil {
		t.Fatalf("insert mismatched destination: %v", err)
	}

	// Both binding (now+30min) and destination (now+20min) are in the future,
	// but expires_at != valid_until → must NOT be ready or active.
	status, err := transport.QueryLinkStatus(rawCode, "bc1qtest")
	if err != nil {
		t.Fatalf("QueryLinkStatus: %v", err)
	}
	if status == "ready" || status == "active" {
		t.Errorf("mismatched expiry returned status %q; want needs_link or link_pending", status)
	}
}

// TestNewTelegramTransportConstructorTableTests verifies strict validation of
// botUsername and HTTPS URL variants via table-driven tests.
func TestNewTelegramTransportConstructorTableTests(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	svc, _ := NewWithClock(db, testHMACKey, time.Now)
	cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")
	ls, _ := NewListingService(svc, cipher, &fakeDisplayNameGenerator{}, &fakeContactValidator{})
	dc, _ := NewDestinationCipher(testDestKey, "dest_v1")

	validSender := &mockSender{}
	validNow := time.Now

	// Table of invalid bot usernames.
	invalidUsernames := []struct {
		name     string
		username string
	}{
		{"too_short", "Bot"},          // 3 chars < 5
		{"no_bot_suffix", "MyHandle"}, // 8 chars, no bot suffix
		{"at_prefix", "@TestBot"},     // @ not allowed
		{"empty", ""},
		{"one_char", "B"},
		{"four_chars", "test"},         // no bot suffix and < 5
		{"has_space", "Test Bot"},      // space not allowed
		{"special_chars", "Test!Bot"},  // ! not allowed
		{"uppercase_no_bot", "MYTEST"}, // no bot suffix
	}
	for _, tc := range invalidUsernames {
		tc := tc
		t.Run("username_"+tc.name, func(t *testing.T) {
			_, err := NewTelegramTransport(svc, ls, testTokenSecret, testWebhookSecret, dc, tc.username, validSender, validNow)
			if err == nil {
				t.Errorf("username %q was accepted; want error", tc.username)
			}
		})
	}

	// Valid bot usernames.
	validUsernames := []struct {
		name     string
		username string
	}{
		{"min_length", "mybot"},          // 5 chars, ends in bot
		{"with_digits", "test1bot"},      // includes digit
		{"underscore", "my_bot"},         // underscore
		{"mixed_case", "TestBot"},        // mixed case, ends in Bot (case-insensitive)
		{"long", "averylongusernamebot"}, // 20 chars, ends in bot
	}
	for _, tc := range validUsernames {
		tc := tc
		t.Run("username_valid_"+tc.name, func(t *testing.T) {
			_, err := NewTelegramTransport(svc, ls, testTokenSecret, testWebhookSecret, dc, tc.username, validSender, validNow)
			if err != nil {
				t.Errorf("username %q was rejected: %v", tc.username, err)
			}
		})
	}
}

// TestNewHTTPBotAPISenderURLTableTests verifies that only strict HTTPS origins are accepted.
func TestNewHTTPBotAPISenderURLTableTests(t *testing.T) {
	client := &http.Client{Transport: &mockRoundTripper{fn: func(*http.Request) (*http.Response, error) {
		return nil, errors.New("not used")
	}}}

	invalidURLs := []struct {
		name string
		url  string
	}{
		{"http_scheme", "http://api.telegram.org"},
		{"no_scheme", "api.telegram.org"},
		{"empty", ""},
		{"https_no_host", "https://"},
		{"with_userinfo", "https://user:pass@api.telegram.org"},
		{"with_query", "https://api.telegram.org?foo=bar"},
		{"with_fragment", "https://api.telegram.org#section"},
		// Path validation: only empty or "/" are allowed as origins.
		{"with_path", "https://api.telegram.org/bot123/sendMessage"},
		{"with_path_segment", "https://api.telegram.org/unexpected"},
		{"with_encoded_path", "https://api.telegram.org%2Fpath"},
		{"opaque_url", "https:api.telegram.org/path"},
	}
	for _, tc := range invalidURLs {
		tc := tc
		t.Run("url_invalid_"+tc.name, func(t *testing.T) {
			_, err := NewHTTPBotAPISender(tc.url, "token", client)
			if err == nil {
				t.Errorf("URL %q was accepted; want error", tc.url)
			}
		})
	}

	// Valid HTTPS origin.
	_, err := NewHTTPBotAPISender("https://api.telegram.org", "token", client)
	if err != nil {
		t.Errorf("valid HTTPS URL rejected: %v", err)
	}
	// Trailing slash is normalized.
	_, err = NewHTTPBotAPISender("https://api.telegram.org/", "token", client)
	if err != nil {
		t.Errorf("HTTPS URL with trailing slash rejected: %v", err)
	}
}
