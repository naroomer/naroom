package v2

// Tests 15-24 for webhook handling.
// All tests: injectable clock, in-memory SQLite, mock sender, no time.Sleep, no t.Skip.

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildWebhookBody constructs a minimal Telegram webhook JSON for /start.
func buildWebhookBody(chatID int64, chatType, text string) []byte {
	return []byte(fmt.Sprintf(`{"message":{"chat":{"id":%d,"type":%q},"text":%q}}`, chatID, chatType, text))
}

// sendWebhook sends a webhook request to the transport handler and returns the status code.
func sendWebhook(transport *TelegramTransport, body []byte, secret string) int {
	req := httptest.NewRequest(http.MethodPost, "/v2/telegram/client/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	}
	w := httptest.NewRecorder()
	transport.HandleWebhook(w, req)
	return w.Code
}

// createPendingAttempt creates a pending link attempt and returns the rawToken.
func createPendingAttempt(t *testing.T, transport *TelegramTransport, rawCode, wallet string) string {
	t.Helper()
	result, err := transport.CreateLink(rawCode, wallet)
	if err != nil {
		t.Fatalf("createPendingAttempt: CreateLink: %v", err)
	}
	const prefix = "?start="
	idx := len("https://t.me/TestBot") + len(prefix)
	return result.BotURL[idx:]
}

// ── Test 15: TestWebhookWrongSecretRejected ───────────────────────────────

func TestWebhookWrongSecretRejected(t *testing.T) {
	transport, svc, _, _ := newTestTransport(t, nil)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")

	sender := transport.sender.(*mockSender)
	sender.setCalls(nil)

	body := buildWebhookBody(42, "private", "/start "+rawToken)

	// Wrong secret → 401, sender NOT called.
	code := sendWebhook(transport, body, "wrong-secret")
	if code != http.StatusUnauthorized {
		t.Errorf("wrong secret: got %d, want 401", code)
	}
	if sender.callCount() != 0 {
		t.Error("sender was called with wrong secret")
	}

	// No secret → 401.
	code = sendWebhook(transport, body, "")
	if code != http.StatusUnauthorized {
		t.Errorf("no secret: got %d, want 401", code)
	}
}

// ── Test 16: TestWebhookNonPrivateChatIgnored ─────────────────────────────

func TestWebhookNonPrivateChatIgnored(t *testing.T) {
	transport, svc, _, db := newTestTransport(t, nil)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")

	body := buildWebhookBody(42, "group", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	if code != http.StatusOK {
		t.Errorf("non-private chat: got %d, want 200", code)
	}

	// DB must not be mutated (attempt still pending, no binding).
	var state string
	db.QueryRow(`SELECT state FROM v2_telegram_link_attempts WHERE token_hash=?`, //nolint:errcheck
		computeTokenHash(testTokenSecret, rawToken)).Scan(&state)
	if state != "pending" {
		t.Errorf("attempt state changed: got %q, want pending", state)
	}

	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Errorf("binding created for non-private chat: got %d", cnt)
	}
}

// ── Test 17: TestWebhookValidStartCreatesBindingAndDestination ───────────

func TestWebhookValidStartCreatesBindingAndDestination(t *testing.T) {
	transport, svc, _, db := newTestTransport(t, nil)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")

	body := buildWebhookBody(12345, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	if code != http.StatusOK {
		t.Errorf("valid start: got %d, want 200", code)
	}

	// Binding row must exist with state=ready.
	var bindingState string
	db.QueryRow(`SELECT state FROM v2_client_notification_bindings LIMIT 1`).Scan(&bindingState) //nolint:errcheck
	if bindingState != "ready" {
		t.Errorf("binding state: got %q, want ready", bindingState)
	}

	// Destination row must exist.
	var destCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_destinations`).Scan(&destCnt) //nolint:errcheck
	if destCnt != 1 {
		t.Errorf("destination count: got %d, want 1", destCnt)
	}

	// Attempt must be deleted.
	var attCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts`).Scan(&attCnt) //nolint:errcheck
	if attCnt != 0 {
		t.Errorf("attempt not deleted: got %d", attCnt)
	}
}

// ── Test 18: TestWebhookStartAtBotNameWorks ───────────────────────────────

func TestWebhookStartAtBotNameWorks(t *testing.T) {
	transport, svc, _, db := newTestTransport(t, nil)

	// Test /start@TestBot <token> (case-insensitive).
	rawCode1, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest1btc")
	rawToken1 := createPendingAttempt(t, transport, rawCode1, "bc1qtest1btc")

	body := buildWebhookBody(100, "private", "/start@testbot "+rawToken1)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	if code != http.StatusOK {
		t.Errorf("/start@testbot: got %d, want 200", code)
	}

	var bindCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&bindCnt) //nolint:errcheck
	if bindCnt != 1 {
		t.Errorf("/start@testbot: binding count %d, want 1", bindCnt)
	}

	// Wrong bot name → 200 neutral, no binding.
	rawCode2, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest2btc")
	rawToken2 := createPendingAttempt(t, transport, rawCode2, "bc1qtest2btc")
	body2 := buildWebhookBody(200, "private", "/start@wrongbot "+rawToken2)
	code = sendWebhook(transport, body2, string(testWebhookSecret))
	if code != http.StatusOK {
		t.Errorf("wrong bot name: got %d, want 200", code)
	}

	// No new binding for wrong bot name.
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&bindCnt) //nolint:errcheck
	if bindCnt != 1 {
		t.Errorf("wrong bot name created binding: count %d, want 1", bindCnt)
	}
}

// ── Test 19: TestWebhookExpiredTokenNeutral ───────────────────────────────

func TestWebhookExpiredTokenNeutral(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	var clockNow = now
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, _, _ := newTestTransport(t, getNow)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")

	sender := transport.sender.(*mockSender)
	sender.setCalls(nil)

	// Advance past token expiry.
	mu.Lock()
	clockNow = now.Add(20 * time.Minute) // past 15min TTL
	mu.Unlock()

	body := buildWebhookBody(99, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	if code != http.StatusOK {
		t.Errorf("expired token: got %d, want 200", code)
	}
	if sender.callCount() != 0 {
		t.Error("sender called with expired token")
	}
}

// ── Test 20: TestWebhookConcurrentExactlyOne ──────────────────────────────

func TestWebhookConcurrentExactlyOne(t *testing.T) {
	transport, svc, _, db := newTestTransport(t, nil)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")

	body := buildWebhookBody(55555, "private", "/start "+rawToken)

	const goroutines = 10
	statuses := make([]int, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i] = sendWebhook(transport, bytes.Clone(body), string(testWebhookSecret))
		}()
	}
	wg.Wait()

	// Exactly 1 binding+destination.
	var bindCnt, destCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&bindCnt) //nolint:errcheck
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_destinations`).Scan(&destCnt)        //nolint:errcheck
	if bindCnt != 1 {
		t.Errorf("concurrent webhook: binding count %d, want 1", bindCnt)
	}
	if destCnt != 1 {
		t.Errorf("concurrent webhook: destination count %d, want 1", destCnt)
	}

	// All statuses must be 200 or 503.
	for i, s := range statuses {
		if s != http.StatusOK && s != http.StatusServiceUnavailable {
			t.Errorf("goroutine %d: unexpected status %d", i, s)
		}
	}

	// At least one 200.
	found200 := false
	for _, s := range statuses {
		if s == http.StatusOK {
			found200 = true
			break
		}
	}
	if !found200 {
		t.Error("no successful webhook response")
	}

	// Sender must have been called exactly once (only one goroutine won the CAS).
	senderMock := transport.sender.(*mockSender)
	if senderMock.callCount() != 1 {
		t.Errorf("sender call count: got %d, want 1", senderMock.callCount())
	}
}

// ── Test 21: TestWebhookRetryableSendFailureReturns503 ────────────────────

func TestWebhookRetryableSendFailureReturns503(t *testing.T) {
	transport, svc, _, db := newTestTransport(t, nil)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")

	sender := transport.sender.(*mockSender)
	sender.setCalls(errors.New("retryable network error")) // retryable (not ErrPermanentDelivery)

	body := buildWebhookBody(77, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	if code != http.StatusServiceUnavailable {
		t.Errorf("retryable send failure: got %d, want 503", code)
	}

	// Attempt must be reset to pending.
	var state string
	db.QueryRow(`SELECT state FROM v2_telegram_link_attempts WHERE token_hash=?`, //nolint:errcheck
		computeTokenHash(testTokenSecret, rawToken)).Scan(&state)
	if state != "pending" {
		t.Errorf("attempt not reset to pending: got %q", state)
	}

	// Retry should now succeed.
	sender.setCalls(nil) // success
	code2 := sendWebhook(transport, bytes.Clone(body), string(testWebhookSecret))
	if code2 != http.StatusOK {
		t.Errorf("retry after retryable failure: got %d, want 200", code2)
	}
	var bindCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&bindCnt) //nolint:errcheck
	if bindCnt != 1 {
		t.Errorf("retry: binding count %d, want 1", bindCnt)
	}
}

// ── Test 22: TestWebhookPermanentSendFailureReturns200 ────────────────────

func TestWebhookPermanentSendFailureReturns200(t *testing.T) {
	transport, svc, _, db := newTestTransport(t, nil)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")

	sender := transport.sender.(*mockSender)
	sender.setCalls(fmt.Errorf("%w: bot blocked", ErrPermanentDelivery))

	body := buildWebhookBody(88, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	if code != http.StatusOK {
		t.Errorf("permanent send failure: got %d, want 200", code)
	}

	// Attempt must be reset to pending (so client can retry with new link).
	var state string
	db.QueryRow(`SELECT state FROM v2_telegram_link_attempts WHERE token_hash=?`, //nolint:errcheck
		computeTokenHash(testTokenSecret, rawToken)).Scan(&state)
	if state != "pending" {
		t.Errorf("attempt not reset to pending: got %q", state)
	}

	// No binding or destination created.
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Errorf("binding created on permanent failure: got %d", cnt)
	}

	// After permanent failure, a DIFFERENT positive private chat ID retries with the SAME
	// rawToken (same flow). The attempt was reset to pending, so the same token can be used
	// again. This verifies that permanent failure returns the attempt to pending so the
	// existing link (same token) can be retried from a different chat.
	sender.setCalls(nil)                                         // reset to success
	body2 := buildWebhookBody(99, "private", "/start "+rawToken) // same rawToken, different chat.id
	code2 := sendWebhook(transport, body2, string(testWebhookSecret))
	if code2 != http.StatusOK {
		t.Errorf("same-token second chat after permanent failure: got %d, want 200", code2)
	}
	// Exactly one binding+destination for the original flow.
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&cnt) //nolint:errcheck
	if cnt != 1 {
		t.Errorf("same-token retry: binding count %d, want 1", cnt)
	}
	var destCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_destinations`).Scan(&destCnt) //nolint:errcheck
	if destCnt != 1 {
		t.Errorf("same-token retry: destination count %d, want 1", destCnt)
	}
	// Attempt must be deleted (finalization complete).
	var attCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts`).Scan(&attCnt) //nolint:errcheck
	if attCnt != 0 {
		t.Errorf("same-token retry: attempt not deleted: count %d", attCnt)
	}
}

// ── Test 23: TestWebhookDBFailureAfterSuccessfulSendRecoversAfterLease ───────

func TestWebhookDBFailureAfterSuccessfulSendRecoversAfterLease(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	var clockNow = now
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, _, db := newTestTransport(t, getNow)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")

	sender := transport.sender.(*mockSender)
	sender.setCalls(nil) // send succeeds

	// Install hook: installs a real SQLite BEFORE INSERT trigger on v2_telegram_destinations
	// that raises ABORT, then returns nil (no Go-level error). The hook simulates a real
	// DB failure inside the finalization transaction: binding inserts ok, then the destination
	// insert hits RAISE(ABORT, ...) and the entire transaction rolls back.
	triggerInstalled := false
	transport._testAfterSendHook = func() error {
		if !triggerInstalled {
			triggerInstalled = true
			_, err := db.Exec(`CREATE TRIGGER IF NOT EXISTS _test_dest_fail
				BEFORE INSERT ON v2_telegram_destinations
				BEGIN SELECT RAISE(ABORT, 'forced destination failure'); END`)
			if err != nil {
				// If trigger creation fails, propagate as Go error so the test fails clearly.
				return fmt.Errorf("hook: create trigger: %w", err)
			}
		}
		return nil // hook returns nil — let finalization proceed into the trigger
	}

	body := buildWebhookBody(42, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	if code != http.StatusServiceUnavailable {
		t.Errorf("DB failure after send: got %d, want 503", code)
	}

	// Sender called exactly once in first attempt.
	if sender.callCount() != 1 {
		t.Errorf("sender call count after failure: got %d, want 1", sender.callCount())
	}

	// No binding or destination created (transaction was rolled back).
	var bindCnt, destCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&bindCnt) //nolint:errcheck
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_destinations`).Scan(&destCnt)        //nolint:errcheck
	if bindCnt != 0 {
		t.Errorf("binding created on DB failure: count %d", bindCnt)
	}
	if destCnt != 0 {
		t.Errorf("destination created on DB failure: count %d", destCnt)
	}

	// Attempt remains 'processing' with live lease.
	tokenHash := computeTokenHash(testTokenSecret, rawToken)
	var state string
	var leaseUntil sql.NullInt64
	db.QueryRow(`SELECT state, lease_until FROM v2_telegram_link_attempts WHERE token_hash=?`, tokenHash). //nolint:errcheck
														Scan(&state, &leaseUntil)
	if state != "processing" {
		t.Errorf("attempt state after failure: got %q, want processing", state)
	}
	if !leaseUntil.Valid {
		t.Error("lease_until is NULL after processing claim")
	}

	// Before lease expiry: retry webhook must NOT send again (CAS won't match; lease still live).
	code = sendWebhook(transport, bytes.Clone(body), string(testWebhookSecret))
	if code != http.StatusOK {
		t.Errorf("pre-lease retry status: got %d, want 200 (neutral, CAS blocked)", code)
	}
	if sender.callCount() != 1 {
		t.Errorf("sender called before lease expiry: got %d, want 1", sender.callCount())
	}

	// Remove the trigger so the retry can succeed.
	db.Exec(`DROP TRIGGER IF EXISTS _test_dest_fail`) //nolint:errcheck

	// Advance clock past lease expiry.
	mu.Lock()
	clockNow = now.Add(leaseDuration + time.Second)
	mu.Unlock()

	// Post-lease retry must succeed: stale processing → re-claim → send → finalize.
	code = sendWebhook(transport, bytes.Clone(body), string(testWebhookSecret))
	if code != http.StatusOK {
		t.Errorf("post-lease retry: got %d, want 200", code)
	}

	// Sender called exactly once more (total 2).
	if sender.callCount() != 2 {
		t.Errorf("sender call count after recovery: got %d, want 2", sender.callCount())
	}

	// Final state: 1 binding, 1 destination, attempt deleted.
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&bindCnt) //nolint:errcheck
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_destinations`).Scan(&destCnt)        //nolint:errcheck
	if bindCnt != 1 {
		t.Errorf("final binding count: got %d, want 1", bindCnt)
	}
	if destCnt != 1 {
		t.Errorf("final destination count: got %d, want 1", destCnt)
	}
	var attCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts`).Scan(&attCnt) //nolint:errcheck
	if attCnt != 0 {
		t.Errorf("attempt not deleted after recovery: count %d", attCnt)
	}
}

// ── Test 24: TestWebhookReplayedTokenNeutral ──────────────────────────────

func TestWebhookReplayedTokenNeutral(t *testing.T) {
	transport, svc, _, db := newTestTransport(t, nil)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")

	body := buildWebhookBody(111, "private", "/start "+rawToken)

	// First webhook — succeeds.
	code := sendWebhook(transport, body, string(testWebhookSecret))
	if code != http.StatusOK {
		t.Fatalf("first webhook: got %d, want 200", code)
	}

	sender := transport.sender.(*mockSender)
	callsBefore := sender.callCount()

	// Second webhook (replay) — should be neutral.
	code = sendWebhook(transport, body, string(testWebhookSecret))
	if code != http.StatusOK {
		t.Errorf("replayed token: got %d, want 200", code)
	}
	if sender.callCount() != callsBefore {
		t.Error("sender called on replayed token")
	}

	// Still only 1 binding.
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&cnt) //nolint:errcheck
	if cnt != 1 {
		t.Errorf("binding count after replay: got %d, want 1", cnt)
	}
}

// ── Task 04B-FIX webhook tests ────────────────────────────────────────────────

// TestWebhookFinalizeUsesFreshClock verifies that HandleWebhook uses t.now()
// captured AFTER SendMessage for re-verification. When the token has permanently
// expired by finalizeNow, the exact claimed attempt is deleted and 200 is returned
// (per FIX2 §2: expired token after send → neutral 200 + attempt cleanup).
func TestWebhookFinalizeUsesFreshClock(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	var clockNow = now
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, _, db := newTestTransport(t, getNow)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")

	// Hook: after send, advance clock past the token TTL (15 min).
	transport._testAfterSendHook = func() error {
		mu.Lock()
		clockNow = now.Add(16 * time.Minute) // past telegramLinkTTL
		mu.Unlock()
		return nil // don't fail — let finalization proceed with the new clock
	}

	body := buildWebhookBody(99, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	// Token now appears permanently expired at finalizeNow → neutral 200 + attempt cleanup.
	if code != http.StatusOK {
		t.Errorf("post-expiry finalize: got %d, want 200 (neutral)", code)
	}

	// No binding or destination must be created.
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Errorf("binding created with expired token: count %d", cnt)
	}
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_destinations`).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Errorf("destination created with expired token: count %d", cnt)
	}

	// The expired attempt must have been deleted (not left in processing state).
	var attCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts`).Scan(&attCnt) //nolint:errcheck
	if attCnt != 0 {
		t.Errorf("expired attempt not cleaned up: count %d", attCnt)
	}
}

// TestWebhookInitialBindingDestinationExpiryEqualNearEntitlement verifies that
// when entitlementExpiresAt < finalizeNow+15min, both binding.valid_until AND
// destination.expires_at equal entitlementExpiresAt (single validUntil value).
func TestWebhookInitialBindingDestinationExpiryEqualNearEntitlement(t *testing.T) {
	// Start clock at a base time for flow creation.
	flowTime := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	var clockNow = flowTime
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, _, db := newTestTransport(t, getNow)
	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, "bc1qtest")

	// Read entitlement from DB.
	var entUnix int64
	svc.db.QueryRow(`SELECT entitlement_expires_at FROM v2_invoices WHERE flow_id=?`, flowID).Scan(&entUnix) //nolint:errcheck

	// Advance to 10 minutes before entitlement (less than 15min TTL).
	nearEnd := time.Unix(entUnix, 0).Add(-10 * time.Minute)
	mu.Lock()
	clockNow = nearEnd
	mu.Unlock()

	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")
	body := buildWebhookBody(555, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	if code != http.StatusOK {
		t.Fatalf("near entitlement: got %d, want 200", code)
	}

	// binding.valid_until must equal entitlementExpiresAt.
	var bindingValidUntil int64
	db.QueryRow(`SELECT valid_until FROM v2_client_notification_bindings WHERE flow_id=?`, flowID).Scan(&bindingValidUntil) //nolint:errcheck

	// destination.expires_at must equal entitlementExpiresAt.
	var destExpiresAt int64
	db.QueryRow(`SELECT d.expires_at FROM v2_telegram_destinations d
		INNER JOIN v2_client_notification_bindings b ON b.binding_ref = d.binding_ref
		WHERE b.flow_id=?`, flowID).Scan(&destExpiresAt) //nolint:errcheck

	if bindingValidUntil != entUnix {
		t.Errorf("binding.valid_until: got %d, want %d (entitlement)", bindingValidUntil, entUnix)
	}
	if destExpiresAt != entUnix {
		t.Errorf("destination.expires_at: got %d, want %d (entitlement)", destExpiresAt, entUnix)
	}
	if bindingValidUntil != destExpiresAt {
		t.Errorf("binding.valid_until (%d) != destination.expires_at (%d)", bindingValidUntil, destExpiresAt)
	}
}

// TestWebhookTrailingJSONNeutralNoMutation verifies that a webhook with trailing
// JSON after the first value returns 200 without calling sender or mutating DB.
func TestWebhookTrailingJSONNeutralNoMutation(t *testing.T) {
	transport, svc, _, db := newTestTransport(t, nil)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")

	sender := transport.sender.(*mockSender)
	sender.setCalls(nil)

	// Build body with trailing JSON object after the main webhook payload.
	mainBody := buildWebhookBody(42, "private", "/start "+rawToken)
	trailing := append(mainBody, []byte(`{"extra":"trailing"}`)...)

	req := httptest.NewRequest(http.MethodPost, "/v2/telegram/client/webhook", bytes.NewReader(trailing))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", string(testWebhookSecret))
	w := httptest.NewRecorder()
	transport.HandleWebhook(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("trailing JSON: got %d, want 200", w.Code)
	}

	// Sender must NOT be called.
	if sender.callCount() != 0 {
		t.Errorf("sender called with trailing JSON: count %d", sender.callCount())
	}

	// No DB mutation.
	tokenHash := computeTokenHash(testTokenSecret, rawToken)
	var state string
	db.QueryRow(`SELECT state FROM v2_telegram_link_attempts WHERE token_hash=?`, tokenHash).Scan(&state) //nolint:errcheck
	if state != "pending" {
		t.Errorf("attempt state changed: got %q, want pending", state)
	}

	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Errorf("binding created on trailing JSON: count %d", cnt)
	}
}

// ── Task 04B-FIX2 webhook regression tests ────────────────────────────────────

// TestWebhookFinalizeRejectsLeaseAtExactBoundary verifies the half-open lease interval:
// when finalizeNow == lease_until, the finalizer must NOT commit (returns 503 for reclaim).
func TestWebhookFinalizeRejectsLeaseAtExactBoundary(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	var clockNow = now
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, _, db := newTestTransport(t, getNow)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")

	// Install hook: advance clock so that finalizeNow == exactly lease_until.
	// Claim happens at 'now', lease_until = now + 30s. After send, advance to exactly lease_until.
	transport._testAfterSendHook = func() error {
		mu.Lock()
		clockNow = now.Add(leaseDuration) // exactly at lease boundary
		mu.Unlock()
		return nil
	}

	body := buildWebhookBody(42, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	// At exact boundary (finalizeNow == lease_until), finalizer must be rejected → 503.
	if code != http.StatusServiceUnavailable {
		t.Errorf("exact lease boundary: got %d, want 503", code)
	}

	// No binding or destination must be created.
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Errorf("binding created at exact lease boundary: count %d", cnt)
	}
}

// TestWebhookCollisionRetryRefreshesFinalizeClock verifies that finalizeNow is
// refreshed before each collision-retry transaction attempt.
// TestWebhookCollisionRetryRefreshesFinalizeClock verifies that the finalization clock is
// refreshed AFTER each binding-ref generation, not at the start of the retry iteration.
//
// Scenario:
//   - Initial ref (step 9) collides: retry=0 fails with errBindingRefCollision.
//   - At retry=1, bindingRefGen advances the clock to exactly newLease (the lease boundary).
//   - Fresh finalizeNow == newLease → txLeaseUntil.Int64 <= finalizeNowUnix → 503.
//   - Sender was called exactly once (before the retry loop).
//   - No binding or destination created; attempt stays processing with original lease.
func TestWebhookCollisionRetryRefreshesFinalizeClock(t *testing.T) {
	baseTime := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	clockNow := baseTime
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, ls, _ := newTestTransport(t, getNow)
	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")

	// Pre-insert a colliding binding on a different flow.
	collidingRef := "bnd_" + strings.Repeat("dd", 16)
	rawCode2, flowID2 := makeFormReadyFlowForTransport(t, svc, "bc1qtest2")
	_ = rawCode2
	if err := ls.attachReadyBinding(flowID2, collidingRef, getNow(), getNow().Add(10*time.Minute)); err != nil {
		t.Fatalf("pre-insert collision binding: %v", err)
	}
	_ = flowID

	// newLease for our attempt = baseTime.Unix() + 30.
	newLeaseUnix := baseTime.Unix() + int64(leaseDuration.Seconds())

	genCalls := 0
	transport.bindingRefGen = func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		genCalls++
		if genCalls == 1 {
			// First call (step 9, initial ref): return colliding ref → collision at retry=0.
			return collidingRef, nil
		}
		// Second call (retry=1): advance clock to exact lease boundary so fresh finalizeNow == newLease.
		clockNow = time.Unix(newLeaseUnix, 0)
		return defaultBindingRefGen()
	}

	body := buildWebhookBody(77, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	// Fresh clock at retry=1 equals newLease → half-open lease boundary → 503.
	if code != http.StatusServiceUnavailable {
		t.Errorf("collision with clock at lease boundary: got %d, want 503", code)
	}

	// Sender called exactly once (before retry loop).
	if n := transport.sender.(*mockSender).callCount(); n != 1 {
		t.Errorf("sender call count: got %d, want 1", n)
	}

	// No binding or destination created.
	tokenHash := computeTokenHash(testTokenSecret, rawToken)
	db := transport.svc.db
	var bindCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings WHERE flow_id=?`, flowID).Scan(&bindCnt) //nolint:errcheck
	if bindCnt != 0 {
		t.Errorf("binding created despite clock boundary: count %d", bindCnt)
	}
	var destCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_destinations`).Scan(&destCnt) //nolint:errcheck
	if destCnt != 0 {
		t.Errorf("destination created despite clock boundary: count %d", destCnt)
	}

	// Attempt stays processing with original newLease (not deleted or reset to pending).
	var state string
	var leaseUntil int64
	db.QueryRow(`SELECT state, lease_until FROM v2_telegram_link_attempts WHERE token_hash=?`, tokenHash).Scan(&state, &leaseUntil) //nolint:errcheck
	if state != "processing" {
		t.Errorf("attempt state: got %q, want processing", state)
	}
	if leaseUntil != newLeaseUnix {
		t.Errorf("attempt lease_until: got %d, want %d", leaseUntil, newLeaseUnix)
	}
}

// TestWebhookTokenExpiresDuringSendReturnsNeutralAndCleansAttempt verifies that
// when the exact token expires during the send (finalizeNow >= token.expires_at),
// the handler returns 200 and atomically deletes the exact claimed attempt.
func TestWebhookTokenExpiresDuringSendReturnsNeutralAndCleansAttempt(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	var clockNow = now
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, _, db := newTestTransport(t, getNow)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")

	// Hook: advance clock past token TTL (16 min) after send succeeds.
	transport._testAfterSendHook = func() error {
		mu.Lock()
		clockNow = now.Add(16 * time.Minute) // past 15-min token TTL
		mu.Unlock()
		return nil
	}

	body := buildWebhookBody(55, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	// Token permanently expired during send → neutral 200.
	if code != http.StatusOK {
		t.Errorf("token expired during send: got %d, want 200", code)
	}

	// Sender was called exactly once.
	if n := transport.sender.(*mockSender).callCount(); n != 1 {
		t.Errorf("sender call count: got %d, want 1", n)
	}

	// No binding or destination must be created.
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Errorf("binding created on expired token: count %d", cnt)
	}

	// The exact attempt must be deleted (not left in processing or pending).
	var attCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts`).Scan(&attCnt) //nolint:errcheck
	if attCnt != 0 {
		t.Errorf("expired attempt not deleted: count %d", attCnt)
	}
}

// TestWebhookEntitlementExpiresDuringSendReturnsNeutralAndCleansAttempt verifies
// that when the entitlement expires during the send (but the token itself is still live),
// the handler returns neutral 200 and deletes the exact claimed attempt.
func TestWebhookEntitlementExpiresDuringSendReturnsNeutralAndCleansAttempt(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	var clockNow = now
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, _, db := newTestTransport(t, getNow)
	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, "bc1qtest")

	// Read the entitlement expiry (set at flow-creation time = now + 5 days).
	var entUnix int64
	svc.db.QueryRow(`SELECT entitlement_expires_at FROM v2_invoices WHERE flow_id=?`, flowID).Scan(&entUnix) //nolint:errcheck

	// Advance clock to 20 seconds BEFORE entitlement expiry.
	// lease_until = (entUnix-20) + 30s = entUnix+10s — the lease outlasts the entitlement.
	// Token TTL = 15min, so token expires at (entUnix-20) + 900s = entUnix+880s (still alive).
	// This means: at finalizeNow = entUnix+1, entitlement expired but token+lease are alive.
	mu.Lock()
	clockNow = time.Unix(entUnix-20, 0) // 20 seconds before entitlement expiry
	mu.Unlock()

	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")

	// Hook: after send, advance clock 1 second past entitlement expiry.
	// finalizeNow = entUnix+1:
	//   - entitlement expired (entUnix+1 > entUnix) ✓
	//   - lease_until = entUnix+10s, so lease still alive (entUnix+1 < entUnix+10) ✓
	//   - token expires at entUnix+880s, so token still alive ✓
	transport._testAfterSendHook = func() error {
		mu.Lock()
		clockNow = time.Unix(entUnix+1, 0)
		mu.Unlock()
		return nil
	}

	body := buildWebhookBody(66, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	// Entitlement permanently expired during send → neutral 200.
	if code != http.StatusOK {
		t.Errorf("entitlement expired during send: got %d, want 200 (neutral)", code)
	}

	// Sender called exactly once.
	if n := transport.sender.(*mockSender).callCount(); n != 1 {
		t.Errorf("sender call count: got %d, want 1", n)
	}

	// No binding or destination must be created.
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Errorf("binding created on expired entitlement: count %d", cnt)
	}

	// The exact attempt must be deleted (neutral cleanup, not retry loop).
	var attCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts`).Scan(&attCnt) //nolint:errcheck
	if attCnt != 0 {
		t.Errorf("attempt not deleted after entitlement expiry: count %d", attCnt)
	}
}

// TestWebhookNegativePrivateChatIDNeutralNoMutation verifies that a negative
// private chat ID returns 200 without calling sender or mutating DB.
func TestWebhookNegativePrivateChatIDNeutralNoMutation(t *testing.T) {
	transport, svc, _, db := newTestTransport(t, nil)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")

	sender := transport.sender.(*mockSender)
	sender.setCalls(nil)

	// Negative chat ID (e.g. group/supergroup) in a "private" type message.
	body := buildWebhookBody(-12345, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	if code != http.StatusOK {
		t.Errorf("negative chatID: got %d, want 200", code)
	}
	if sender.callCount() != 0 {
		t.Errorf("sender called with negative chatID: count %d", sender.callCount())
	}

	// No DB mutation — attempt must remain pending.
	tokenHash := computeTokenHash(testTokenSecret, rawToken)
	var state string
	db.QueryRow(`SELECT state FROM v2_telegram_link_attempts WHERE token_hash=?`, tokenHash).Scan(&state) //nolint:errcheck
	if state != "pending" {
		t.Errorf("attempt state changed: got %q, want pending", state)
	}
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Errorf("binding created for negative chatID: count %d", cnt)
	}
}

// TestWebhookOldFinalizerCannotUseReclaimedLease verifies that a stale handler A cannot
// finalize using a lease that has been superseded by handler B's reclaim.
//
// Scenario:
//  1. Handler A claims attempt with lease L1.
//  2. After-send hook: advance clock to L1 (boundary) and simulate handler B by directly
//     updating DB to assign a new processing lease L2 = L1+30.
//  3. Handler A's exact-lease re-verify (WHERE lease_until=L1) finds no row → neutral 200.
//  4. Sender called once; no binding or destination; attempt stays processing with L2.
func TestWebhookOldFinalizerCannotUseReclaimedLease(t *testing.T) {
	baseTime := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	clockNow := baseTime
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, _, _ := newTestTransport(t, getNow)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")
	tokenHash := computeTokenHash(testTokenSecret, rawToken)

	db := transport.svc.db
	newLeaseA := baseTime.Unix() + int64(leaseDuration.Seconds()) // L1 = 1_000_030
	newLeaseB := newLeaseA + int64(leaseDuration.Seconds())       // L2 = 1_000_060

	transport._testAfterSendHook = func() error {
		mu.Lock()
		clockNow = time.Unix(newLeaseA, 0) // advance to exact lease boundary
		mu.Unlock()
		// Simulate handler B taking over: update DB with new lease L2.
		_, err := db.Exec(
			`UPDATE v2_telegram_link_attempts SET lease_until=? WHERE token_hash=? AND state='processing'`,
			newLeaseB, tokenHash,
		)
		return err
	}

	body := buildWebhookBody(42, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	// Exact-lease predicate (WHERE lease_until=L1) returns no row → superseded → neutral 200.
	if code != http.StatusOK {
		t.Errorf("superseded finalizer: got %d, want 200", code)
	}

	// Sender called exactly once (handler A's send before finalization).
	if n := transport.sender.(*mockSender).callCount(); n != 1 {
		t.Errorf("sender call count: got %d, want 1", n)
	}

	// No binding or destination created by handler A.
	var bindCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings`).Scan(&bindCnt) //nolint:errcheck
	if bindCnt != 0 {
		t.Errorf("binding created by superseded handler: count %d", bindCnt)
	}
	var destCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_destinations`).Scan(&destCnt) //nolint:errcheck
	if destCnt != 0 {
		t.Errorf("destination created by superseded handler: count %d", destCnt)
	}

	// Attempt remains processing with handler B's lease L2 (not deleted, not reset).
	var state string
	var leaseUntil int64
	db.QueryRow(`SELECT state, lease_until FROM v2_telegram_link_attempts WHERE token_hash=?`, tokenHash).Scan(&state, &leaseUntil) //nolint:errcheck
	if state != "processing" {
		t.Errorf("attempt state: got %q, want processing", state)
	}
	if leaseUntil != newLeaseB {
		t.Errorf("attempt lease_until: got %d, want %d (handler B's lease L2)", leaseUntil, newLeaseB)
	}
}

// TestWebhookOldFinalizerDoesNotDeleteReplacementAttempt verifies that a superseded handler A
// does not delete token B when token A has been replaced by a new CreateLink call.
//
// Scenario:
//  1. Handler A claims attempt (token A) with lease L1.
//  2. After-send hook: advance clock past token A's TTL, then CreateLink replaces token A
//     with a new pending attempt (token B) for the same flow.
//  3. Handler A's exact-lease re-verify (WHERE token_hash=tokenHashA) finds no row → neutral 200.
//  4. Token B remains pending and untouched; no binding or destination.
func TestWebhookOldFinalizerDoesNotDeleteReplacementAttempt(t *testing.T) {
	baseTime := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	clockNow := baseTime
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, _, _ := newTestTransport(t, getNow)
	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")
	tokenHashA := computeTokenHash(testTokenSecret, rawToken)
	db := transport.svc.db

	var rawTokenB string
	transport._testAfterSendHook = func() error {
		// Advance clock past token A's 15-min TTL so CreateLink can replace it.
		mu.Lock()
		clockNow = baseTime.Add(16 * time.Minute)
		mu.Unlock()
		// CreateLink deletes the expired processing attempt (token A) and inserts token B.
		res, err := transport.CreateLink(rawCode, "bc1qtest")
		if err != nil {
			return err
		}
		const prefix = "?start="
		idx := len("https://t.me/TestBot") + len(prefix)
		rawTokenB = res.BotURL[idx:]
		return nil
	}

	body := buildWebhookBody(42, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	// Token A deleted by CreateLink → exact-lease re-verify ErrNoRows → neutral 200.
	if code != http.StatusOK {
		t.Errorf("superseded handler after token replacement: got %d, want 200", code)
	}

	// Sender called exactly once.
	if n := transport.sender.(*mockSender).callCount(); n != 1 {
		t.Errorf("sender call count: got %d, want 1", n)
	}

	// No binding or destination created.
	var bindCnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings WHERE flow_id=?`, flowID).Scan(&bindCnt) //nolint:errcheck
	if bindCnt != 0 {
		t.Errorf("binding created by superseded handler: count %d", bindCnt)
	}

	// Token A must be gone (deleted by CreateLink).
	var attACnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts WHERE token_hash=?`, tokenHashA).Scan(&attACnt) //nolint:errcheck
	if attACnt != 0 {
		t.Errorf("token A attempt still exists after replacement: count %d", attACnt)
	}

	// Token B must be the sole attempt, in pending state.
	tokenHashB := computeTokenHash(testTokenSecret, rawTokenB)
	var state string
	db.QueryRow(`SELECT state FROM v2_telegram_link_attempts WHERE token_hash=?`, tokenHashB).Scan(&state) //nolint:errcheck
	if state != "pending" {
		t.Errorf("token B state: got %q, want pending", state)
	}
	var total int
	db.QueryRow(`SELECT COUNT(*) FROM v2_telegram_link_attempts`).Scan(&total) //nolint:errcheck
	if total != 1 {
		t.Errorf("attempt count: got %d, want 1 (only token B)", total)
	}
}

// TestWebhookTokenExpiryCleanupDeleteFailureReturns503 verifies that when the DELETE inside
// cleanupExpiredAttemptTx fails (injected via SQLite BEFORE DELETE trigger), the handler
// returns 503 and the attempt row is left in processing state.
func TestWebhookTokenExpiryCleanupDeleteFailureReturns503(t *testing.T) {
	baseTime := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	clockNow := baseTime
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, _, _ := newTestTransport(t, getNow)
	rawCode, _ := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")
	tokenHash := computeTokenHash(testTokenSecret, rawToken)
	db := transport.svc.db

	// Inject BEFORE DELETE trigger to make every DELETE on v2_telegram_link_attempts fail.
	if _, err := db.Exec(`
		CREATE TRIGGER block_token_cleanup_delete
		BEFORE DELETE ON v2_telegram_link_attempts BEGIN
		  SELECT RAISE(ABORT, 'injected token expiry DELETE failure');
		END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	// Hook: advance clock past token TTL so the token-expiry cleanup path fires.
	transport._testAfterSendHook = func() error {
		mu.Lock()
		clockNow = baseTime.Add(16 * time.Minute) // past 15-min token TTL
		mu.Unlock()
		return nil
	}

	body := buildWebhookBody(55, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	// DELETE inside cleanupExpiredAttemptTx fails → 503.
	if code != http.StatusServiceUnavailable {
		t.Errorf("token cleanup DELETE failure: got %d, want 503", code)
	}

	// Attempt must still exist (DELETE was blocked by trigger).
	var state string
	db.QueryRow(`SELECT state FROM v2_telegram_link_attempts WHERE token_hash=?`, tokenHash).Scan(&state) //nolint:errcheck
	if state != "processing" {
		t.Errorf("attempt state after DELETE failure: got %q, want processing", state)
	}
}

// TestWebhookEntitlementExpiryCleanupDeleteFailureReturns503 verifies that when the DELETE
// inside cleanupExpiredAttemptTx fails on the entitlement-expiry path (injected via SQLite
// BEFORE DELETE trigger), the handler returns 503 and the attempt row is preserved.
func TestWebhookEntitlementExpiryCleanupDeleteFailureReturns503(t *testing.T) {
	baseTime := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	clockNow := baseTime
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	transport, svc, _, _ := newTestTransport(t, getNow)
	rawCode, flowID := makeFormReadyFlowForTransport(t, svc, "bc1qtest")
	db := transport.svc.db

	// Read entitlement expiry (flow created at baseTime; entitlement = baseTime + 5 days).
	var entUnix int64
	svc.db.QueryRow(`SELECT entitlement_expires_at FROM v2_invoices WHERE flow_id=?`, flowID).Scan(&entUnix) //nolint:errcheck

	// Create the attempt 20 seconds before entitlement expiry.
	// lease_until = (entUnix-20) + 30 = entUnix+10; token expires at (entUnix-20)+900 = entUnix+880.
	mu.Lock()
	clockNow = time.Unix(entUnix-20, 0)
	mu.Unlock()
	rawToken := createPendingAttempt(t, transport, rawCode, "bc1qtest")
	tokenHash := computeTokenHash(testTokenSecret, rawToken)

	// Inject BEFORE DELETE trigger after the attempt is created.
	if _, err := db.Exec(`
		CREATE TRIGGER block_ent_cleanup_delete
		BEFORE DELETE ON v2_telegram_link_attempts BEGIN
		  SELECT RAISE(ABORT, 'injected entitlement expiry DELETE failure');
		END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	// Hook: advance clock 1 second past entitlement expiry.
	// finalizeNow = entUnix+1: entitlement expired, lease still live (entUnix+10), token alive (entUnix+880).
	transport._testAfterSendHook = func() error {
		mu.Lock()
		clockNow = time.Unix(entUnix+1, 0)
		mu.Unlock()
		return nil
	}

	body := buildWebhookBody(66, "private", "/start "+rawToken)
	code := sendWebhook(transport, body, string(testWebhookSecret))
	// DELETE inside cleanupExpiredAttemptTx (entitlement path) fails → 503.
	if code != http.StatusServiceUnavailable {
		t.Errorf("entitlement cleanup DELETE failure: got %d, want 503", code)
	}

	// Sender called exactly once.
	if n := transport.sender.(*mockSender).callCount(); n != 1 {
		t.Errorf("sender call count: got %d, want 1", n)
	}

	// Attempt must still exist (DELETE was blocked by trigger).
	var state string
	db.QueryRow(`SELECT state FROM v2_telegram_link_attempts WHERE token_hash=?`, tokenHash).Scan(&state) //nolint:errcheck
	if state != "processing" {
		t.Errorf("attempt state after DELETE failure: got %q, want processing", state)
	}
}

// ── Telegram malformed callback matrix (Category 11) ──────────────────────────

// buildCallbackBody constructs a callback_query webhook JSON.
func buildCallbackBody(queryID string, chatID int64, chatType string, messageID int64, data string) []byte {
	return []byte(fmt.Sprintf(
		`{"callback_query":{"id":%q,"data":%q,"message":{"message_id":%d,"chat":{"id":%d,"type":%q}}}}`,
		queryID, data, messageID, chatID, chatType,
	))
}

// validCallbackData returns a valid rv: callback data for testing.
func validCallbackData() string {
	return "rv:" + strings.Repeat("a", 32) + ":p"
}

func TestWebhookCallbackMalformedMatrix(t *testing.T) {
	transport, svc, _, db := newTestTransport(t, nil)
	rs, err := NewReviewService(db, testHMACKey, time.Now)
	if err != nil {
		t.Fatalf("NewReviewService: %v", err)
	}
	stubSender := &stubReviewSender{}
	transport.SetReviewService(rs, stubSender)

	_ = svc // keep reference

	sendCB := func(body []byte) int {
		req := httptest.NewRequest(http.MethodPost, "/v2/telegram/client/webhook", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", string(testWebhookSecret))
		w := httptest.NewRecorder()
		transport.HandleWebhook(w, req)
		return w.Code
	}

	// Count consumed reviews before test.
	countReviews := func() int {
		var n int
		db.QueryRow(`SELECT COUNT(*) FROM v2_review_entitlements WHERE consumed_at IS NOT NULL`).Scan(&n) //nolint:errcheck
		return n
	}
	before := countReviews()

	cases := []struct {
		name string
		body []byte
	}{
		{
			"empty_query_id",
			buildCallbackBody("", 123, "private", 1, validCallbackData()),
		},
		{
			"nil_message",
			[]byte(`{"callback_query":{"id":"qid","data":"` + validCallbackData() + `"}}`),
		},
		{
			"nil_chat",
			[]byte(`{"callback_query":{"id":"qid","data":"` + validCallbackData() + `","message":{"message_id":1}}}`),
		},
		{
			"non_private_group",
			buildCallbackBody("qid", 123, "group", 1, validCallbackData()),
		},
		{
			"non_private_channel",
			buildCallbackBody("qid", 123, "channel", 1, validCallbackData()),
		},
		{
			"zero_chat_id",
			buildCallbackBody("qid", 0, "private", 1, validCallbackData()),
		},
		{
			"negative_chat_id",
			buildCallbackBody("qid", -1, "private", 1, validCallbackData()),
		},
		{
			"zero_message_id",
			buildCallbackBody("qid", 123, "private", 0, validCallbackData()),
		},
		{
			"negative_message_id",
			buildCallbackBody("qid", 123, "private", -1, validCallbackData()),
		},
		{
			"malformed_data_no_prefix",
			buildCallbackBody("qid", 123, "private", 1, "bad:data:here"),
		},
		{
			"malformed_data_uppercase_hex",
			buildCallbackBody("qid", 123, "private", 1, "rv:"+strings.Repeat("A", 32)+":p"),
		},
		{
			"malformed_data_wrong_length",
			buildCallbackBody("qid", 123, "private", 1, "rv:"+strings.Repeat("a", 16)+":p"),
		},
		{
			"malformed_data_invalid_action",
			buildCallbackBody("qid", 123, "private", 1, "rv:"+strings.Repeat("a", 32)+":x"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code := sendCB(tc.body)
			if code != http.StatusOK {
				t.Errorf("%s: want 200, got %d", tc.name, code)
			}
			after := countReviews()
			if after != before {
				t.Errorf("%s: want 0 new reviews, got %d new rows", tc.name, after-before)
			}
		})
	}
}

// ── Mock sender with context ───────────────────────────────────────────────

// mockContextSender records context cancellation.
type mockContextSender struct {
	mu         sync.Mutex
	cancelFunc context.CancelFunc
	called     bool
	returnErr  error
}

func (m *mockContextSender) SendMessage(ctx context.Context, _ int64, _ string) error {
	m.mu.Lock()
	m.called = true
	m.mu.Unlock()
	return m.returnErr
}
