package v2_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	v2 "naroom/internal/v2"
)

// ── Test helpers ──────────────────────────────────────────────────────────────

func newInformerTestSvc(t *testing.T, clock func() time.Time) *v2.InformerService {
	t.Helper()
	db, err := v2.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	destCipher, err := v2.NewDestinationCipher(make([]byte, 32), "test_v1")
	if err != nil {
		t.Fatalf("NewDestinationCipher: %v", err)
	}
	svc, err := v2.NewInformerService(
		db,
		[]byte("test-hmac-key-32-bytes-12345678!"),
		[]byte("test-token-secret-32-bytes-12345"),
		destCipher,
		clock,
	)
	if err != nil {
		t.Fatalf("NewInformerService: %v", err)
	}
	return svc
}

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func postInformerWebhook(t *testing.T, transport *v2.InformerTransport, body, secret string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v2/telegram/informer/webhook", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	rr := httptest.NewRecorder()
	transport.HandleWebhook(rr, req)
	return rr
}

const informerTestSecret = "correct-secret-1234567890abcdef!!"

func newInformerTransport(t *testing.T, svc *v2.InformerService) *v2.InformerTransport {
	t.Helper()
	tr, err := v2.NewInformerTransport(svc, []byte(informerTestSecret), "testbot", nil)
	if err != nil {
		t.Fatalf("NewInformerTransport: %v", err)
	}
	return tr
}

// ── 1. BTC eligibility success ────────────────────────────────────────────────

func TestInformerCreateAccess_Success(t *testing.T) {
	svc := newInformerTestSvc(t, nil)
	rawToken, exp, err := svc.CreateAccess("tbilisi", 1500.0)
	if err != nil {
		t.Fatalf("CreateAccess: %v", err)
	}
	if rawToken == "" {
		t.Fatal("expected non-empty raw token")
	}
	if exp.IsZero() {
		t.Fatal("expected non-zero expires_at")
	}
	city, state, qErr := svc.QueryStatus(rawToken)
	if qErr != nil {
		t.Fatalf("QueryStatus after create: %v", qErr)
	}
	if state != "pending" || city != "tbilisi" {
		t.Errorf("want pending/tbilisi, got %s/%s", state, city)
	}
}

// ── 2. Low balance ─────────────────────────────────────────────────────────────

func TestInformerCreateAccess_LowBalance(t *testing.T) {
	svc := newInformerTestSvc(t, nil)
	_, _, err := svc.CreateAccess("tbilisi", 500.0)
	if !errors.Is(err, v2.ErrInformerLowBalance) {
		t.Errorf("expected ErrInformerLowBalance, got: %v", err)
	}
}

// ── 3. Invalid city ────────────────────────────────────────────────────────────

func TestInformerCreateAccess_InvalidCity(t *testing.T) {
	svc := newInformerTestSvc(t, nil)
	_, _, err := svc.CreateAccess("atlantis", 2000.0)
	if !errors.Is(err, v2.ErrInformerInvalidCity) {
		t.Errorf("expected ErrInformerInvalidCity, got: %v", err)
	}
}

// ── 4. Exact $1000 boundary ────────────────────────────────────────────────────

func TestInformerCreateAccess_ExactFloor(t *testing.T) {
	svc := newInformerTestSvc(t, nil)
	_, _, err := svc.CreateAccess("batumi", 1000.0)
	if err != nil {
		t.Errorf("expected success at exactly $1000, got: %v", err)
	}
	_, _, err = svc.CreateAccess("batumi", 999.99)
	if !errors.Is(err, v2.ErrInformerLowBalance) {
		t.Errorf("expected ErrInformerLowBalance at $999.99, got: %v", err)
	}
}

// ── 5. Raw wallet NOT passed to service ───────────────────────────────────────
// The InformerService.CreateAccess signature does not accept wallet_address.
// This is a contract test: we verify the function signature is correct.

func TestInformerServiceSignatureNoWallet(t *testing.T) {
	// CreateAccess(city string, balanceUSD float64) — no wallet parameter.
	// This test simply confirms the function exists with the expected signature.
	svc := newInformerTestSvc(t, nil)
	tok, _, err := svc.CreateAccess("tbilisi", 2000.0)
	if err != nil || tok == "" {
		t.Errorf("unexpected: %v", err)
	}
}

// ── 6. Token expiry equality boundary ─────────────────────────────────────────

func TestInformerTokenExpiry(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	var nowT time.Time
	nowT = base
	clock := func() time.Time { return nowT }

	svc := newInformerTestSvc(t, clock)
	rawToken, _, err := svc.CreateAccess("tbilisi", 2000.0)
	if err != nil {
		t.Fatalf("CreateAccess: %v", err)
	}

	// At T+15m exactly → NOT expired (task spec: boundary inclusive = alive).
	nowT = base.Add(15 * time.Minute)
	_, _, err = svc.QueryStatus(rawToken)
	if err != nil {
		t.Errorf("expected alive at T+15m boundary, got: %v", err)
	}

	// At T+15m+1s → expired.
	nowT = base.Add(15*time.Minute + time.Second)
	_, _, err = svc.QueryStatus(rawToken)
	if !errors.Is(err, v2.ErrInformerTokenExpired) {
		t.Errorf("expected ErrInformerTokenExpired at T+15m+1s, got: %v", err)
	}
}

// ── 7. Replay (same token used twice) ─────────────────────────────────────────

func TestInformerTokenReplay(t *testing.T) {
	svc := newInformerTestSvc(t, nil)
	rawToken, _, _ := svc.CreateAccess("tbilisi", 2000.0)

	if err := svc.Subscribe(1001, rawToken); err != nil {
		t.Fatalf("first Subscribe: %v", err)
	}
	err := svc.Subscribe(1002, rawToken)
	if !errors.Is(err, v2.ErrInformerTokenClaimed) {
		t.Errorf("expected ErrInformerTokenClaimed on replay, got: %v", err)
	}
}

// ── 8. Concurrent claim simulation ────────────────────────────────────────────

func TestInformerConcurrentClaim(t *testing.T) {
	db, err := v2.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	destCipher, _ := v2.NewDestinationCipher(make([]byte, 32), "test_v1")
	svc, _ := v2.NewInformerService(
		db,
		[]byte("test-hmac-key-32-bytes-12345678!"),
		[]byte("test-token-secret-32-bytes-12345"),
		destCipher,
		nil,
	)
	rawToken, _, _ := svc.CreateAccess("batumi", 2000.0)

	// Directly mark token claimed in DB to simulate race.
	_, _ = db.Exec(`UPDATE v2_informer_tokens SET state='claimed'`)

	err = svc.Subscribe(1003, rawToken)
	if !errors.Is(err, v2.ErrInformerTokenClaimed) {
		t.Errorf("expected ErrInformerTokenClaimed on concurrent claim, got: %v", err)
	}
}

// ── 9. Wrong webhook secret → 403 ─────────────────────────────────────────────

func TestInformerWebhook_WrongSecret(t *testing.T) {
	svc := newInformerTestSvc(t, nil)
	transport := newInformerTransport(t, svc)

	body := `{"update_id":1,"message":{"message_id":1,"chat":{"id":123,"type":"private"},"text":"/stop"}}`
	rr := postInformerWebhook(t, transport, body, "wrong-secret")
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

// ── 10. Non-private chat → no subscription created ────────────────────────────

func TestInformerWebhook_NonPrivateChat(t *testing.T) {
	svc := newInformerTestSvc(t, nil)
	transport := newInformerTransport(t, svc)

	rawToken, _, _ := svc.CreateAccess("tbilisi", 2000.0)
	body := `{"update_id":1,"message":{"message_id":1,"chat":{"id":123,"type":"group"},"text":"/start ` + rawToken + `"}}`
	rr := postInformerWebhook(t, transport, body, informerTestSecret)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for group chat, got %d", rr.Code)
	}
	ok, _, _ := svc.HasActiveSubscription(123)
	if ok {
		t.Error("subscription must NOT be created for group chat")
	}
}

// ── 11. Valid /start through webhook → subscription created ──────────────────

func TestInformerWebhook_ValidStart(t *testing.T) {
	svc := newInformerTestSvc(t, nil)
	transport := newInformerTransport(t, svc)

	rawToken, _, _ := svc.CreateAccess("tbilisi", 2000.0)
	body := `{"update_id":1,"message":{"message_id":1,"chat":{"id":999,"type":"private"},"text":"/start ` + rawToken + `"}}`
	rr := postInformerWebhook(t, transport, body, informerTestSecret)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	ok, city, _ := svc.HasActiveSubscription(999)
	if !ok || city != "tbilisi" {
		t.Errorf("expected tbilisi subscription, got ok=%v city=%s", ok, city)
	}
}

// ── 12. One-chat/one-city replacement ────────────────────────────────────────

func TestInformerOneChatOneCityReplacement(t *testing.T) {
	svc := newInformerTestSvc(t, nil)
	const chatID int64 = 500

	tokenA, _, _ := svc.CreateAccess("tbilisi", 2000.0)
	_ = svc.Subscribe(chatID, tokenA)
	ok, city, _ := svc.HasActiveSubscription(chatID)
	if !ok || city != "tbilisi" {
		t.Fatalf("expected tbilisi, got ok=%v city=%s", ok, city)
	}

	tokenB, _, _ := svc.CreateAccess("batumi", 2000.0)
	_ = svc.Subscribe(chatID, tokenB)
	ok, city, _ = svc.HasActiveSubscription(chatID)
	if !ok || city != "batumi" {
		t.Fatalf("expected batumi after replace, got ok=%v city=%s", ok, city)
	}

	refs, _ := svc.LoadActiveSubscribersForCity("tbilisi")
	if len(refs) != 0 {
		t.Errorf("tbilisi subscription should be gone, found %d refs", len(refs))
	}
}

// ── 13. /stop deletion ────────────────────────────────────────────────────────

func TestInformerStopDeletion(t *testing.T) {
	svc := newInformerTestSvc(t, nil)
	const chatID int64 = 600

	tok, _, _ := svc.CreateAccess("yerevan", 2000.0)
	_ = svc.Subscribe(chatID, tok)
	_ = svc.Unsubscribe(chatID)

	ok, _, _ := svc.HasActiveSubscription(chatID)
	if ok {
		t.Error("subscription still active after Unsubscribe")
	}
	refs, _ := svc.LoadActiveSubscribersForCity("yerevan")
	if len(refs) != 0 {
		t.Errorf("yerevan subscribers should be empty after /stop, got %d", len(refs))
	}
}

func TestInformerStopIdempotent(t *testing.T) {
	svc := newInformerTestSvc(t, nil)
	if err := svc.Unsubscribe(601); err != nil {
		t.Errorf("Unsubscribe on non-existent must not error, got: %v", err)
	}
}

// ── 14. Isolation from Client/Helper tables ───────────────────────────────────

func TestInformerIsolation(t *testing.T) {
	db, err := v2.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	destCipher, _ := v2.NewDestinationCipher(make([]byte, 32), "test_v1")
	svc, _ := v2.NewInformerService(
		db,
		[]byte("test-hmac-key-32-bytes-12345678!"),
		[]byte("test-token-secret-32-bytes-12345"),
		destCipher,
		nil,
	)

	tok, _, _ := svc.CreateAccess("almaty", 2000.0)
	_ = svc.Subscribe(700, tok)
	_ = svc.Unsubscribe(700)

	tables := []string{
		"v2_client_flows", "v2_client_profiles", "v2_helper_profiles",
		"v2_helper_purchases", "v2_helper_invoices", "v2_review_entitlements",
	}
	for _, tbl := range tables {
		var count int
		if scanErr := db.QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&count); scanErr != nil {
			continue
		}
		if count != 0 {
			t.Errorf("informer operations wrote %d rows to %s — must be isolated", count, tbl)
		}
	}
}

// ── 15. First publish creates exactly ONE outbox event ────────────────────────

func TestInformerFirstPublish_OneEvent(t *testing.T) {
	svc := newInformerTestSvc(t, nil)
	err := svc.NotifyFirstPublish("listing-001", "tbilisi", "Alex", "crisis", "alcohol", "urgent", time.Now())
	if err != nil {
		t.Fatalf("NotifyFirstPublish: %v", err)
	}
	entries, _ := svc.LoadPendingOutbox()
	if len(entries) != 1 {
		t.Fatalf("expected 1 outbox entry, got %d", len(entries))
	}
	if entries[0].ListingID != "listing-001" || entries[0].City != "tbilisi" {
		t.Errorf("unexpected entry: %+v", entries[0])
	}
}

// ── 16. Duplicate event_key (daily reactivation) → ErrInformerDuplicateEvent ─

func TestInformerFirstPublish_DuplicateKey(t *testing.T) {
	svc := newInformerTestSvc(t, nil)
	_ = svc.NotifyFirstPublish("listing-002", "batumi", "Bob", "motivation", "cannabis", "soon", time.Now())

	err := svc.NotifyFirstPublish("listing-002", "batumi", "Bob", "motivation", "cannabis", "soon", time.Now())
	if !errors.Is(err, v2.ErrInformerDuplicateEvent) {
		t.Errorf("expected ErrInformerDuplicateEvent on duplicate, got: %v", err)
	}
	entries, _ := svc.LoadPendingOutbox()
	if len(entries) != 1 {
		t.Errorf("expected 1 outbox row, got %d", len(entries))
	}
}

// ── 17. Worker: matching city → notified; other city → not ───────────────────

type fakeInformerSender struct {
	msgs []int64
}

func (f *fakeInformerSender) SendInformerNotification(_ context.Context, chatID int64, _ string) error {
	f.msgs = append(f.msgs, chatID)
	return nil
}

func TestInformerWorker_MatchingCity(t *testing.T) {
	svc := newInformerTestSvc(t, nil)

	tokA, _, _ := svc.CreateAccess("tbilisi", 2000.0)
	_ = svc.Subscribe(1001, tokA)

	tokB, _, _ := svc.CreateAccess("batumi", 2000.0)
	_ = svc.Subscribe(1002, tokB)

	_ = svc.NotifyFirstPublish("listing-tbilisi-01", "tbilisi", "Dana", "crisis", "opioids", "urgent", time.Now())

	sender := &fakeInformerSender{}
	worker := v2.NewInformerWorker(svc, sender)
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if len(sender.msgs) != 1 || sender.msgs[0] != 1001 {
		t.Errorf("expected only chatID=1001 notified, got %v", sender.msgs)
	}
	entries, _ := svc.LoadPendingOutbox()
	if len(entries) != 0 {
		t.Errorf("expected outbox cleared after delivery, got %d pending", len(entries))
	}
}

func TestInformerWorker_NonMatchingCity(t *testing.T) {
	svc := newInformerTestSvc(t, nil)

	tokB, _, _ := svc.CreateAccess("batumi", 2000.0)
	_ = svc.Subscribe(1002, tokB) // subscribed to batumi

	_ = svc.NotifyFirstPublish("listing-tbilisi-02", "tbilisi", "Eve", "just_talk", "gambling", "can_wait", time.Now())

	sender := &fakeInformerSender{}
	worker := v2.NewInformerWorker(svc, sender)
	_ = worker.RunOnce(context.Background())

	if len(sender.msgs) != 0 {
		t.Errorf("expected zero notifications for non-matching city, got %v", sender.msgs)
	}
}

// ── 18. Publish commit independent of Telegram delivery ──────────────────────

type failingInformerSender struct{}

func (f *failingInformerSender) SendInformerNotification(_ context.Context, _ int64, _ string) error {
	return errors.New("telegram delivery failed")
}

func TestInformerWorker_PublishIndependentOfDelivery(t *testing.T) {
	svc := newInformerTestSvc(t, nil)

	tok, _, _ := svc.CreateAccess("tbilisi", 2000.0)
	_ = svc.Subscribe(1001, tok)
	_ = svc.NotifyFirstPublish("listing-indep-01", "tbilisi", "Frank", "recovery_plan", "stimulants", "urgent", time.Now())

	// Sender always fails — RunOnce must not return error (publish is already committed).
	worker := v2.NewInformerWorker(svc, &failingInformerSender{})
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Errorf("RunOnce must not return error on individual send failure, got: %v", err)
	}
}

// ── 19–23. Retryable/permanent delivery lifecycle ─────────────────────────────

// permanentInformerSender always returns ErrInformerNotificationPermanent.
type permanentInformerSender struct{}

func (p *permanentInformerSender) SendInformerNotification(_ context.Context, _ int64, _ string) error {
	return v2.ErrInformerNotificationPermanent
}

// TestInformerRetryableErrorKeepsPending: one retryable failure leaves the
// entry pending with attempt incremented — event is not lost.
func TestInformerRetryableErrorKeepsPending(t *testing.T) {
	svc := newInformerTestSvc(t, nil)

	tok, _, _ := svc.CreateAccess("tbilisi", 2000.0)
	_ = svc.Subscribe(2001, tok)
	_ = svc.NotifyFirstPublish("listing-retry-01", "tbilisi", "G", "crisis", "alcohol", "urgent", time.Now())

	worker := v2.NewInformerWorker(svc, &failingInformerSender{})
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce must not return infra error, got: %v", err)
	}

	// Entry must still be pending (retryable — not lost).
	entries, listErr := svc.LoadPendingOutbox()
	if listErr != nil {
		t.Fatalf("LoadPendingOutbox: %v", listErr)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 pending entry after retryable failure, got %d", len(entries))
	}
	if entries[0].Attempt != 1 {
		t.Errorf("expected attempt=1 after one retryable failure, got %d", entries[0].Attempt)
	}
}

// TestInformerSuccessfulRunAfterRetry: after one retryable failure, a subsequent
// successful run delivers the notification and marks the entry done.
func TestInformerSuccessfulRunAfterRetry(t *testing.T) {
	svc := newInformerTestSvc(t, nil)

	tok, _, _ := svc.CreateAccess("tbilisi", 2000.0)
	_ = svc.Subscribe(2002, tok)
	_ = svc.NotifyFirstPublish("listing-retry-02", "tbilisi", "H", "motivation", "alcohol", "urgent", time.Now())

	// First run: retryable failure — entry stays pending.
	failWorker := v2.NewInformerWorker(svc, &failingInformerSender{})
	_ = failWorker.RunOnce(context.Background())

	// Second run: success — notification delivered, entry marked done.
	sender := &fakeInformerSender{}
	succWorker := v2.NewInformerWorker(svc, sender)
	if err := succWorker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce on success run: %v", err)
	}

	if len(sender.msgs) != 1 {
		t.Errorf("expected 1 notification on success run, got %d", len(sender.msgs))
	}
	pending, _ := svc.LoadPendingOutbox()
	if len(pending) != 0 {
		t.Errorf("expected 0 pending entries after successful delivery, got %d", len(pending))
	}
	state, _ := svc.QueryOutboxState("listing-retry-02")
	if state != "done" {
		t.Errorf("expected state=done after successful delivery, got %q", state)
	}
}

// TestInformerPermanentFailureTerminal: a permanent send error marks the entry
// failed immediately (no retry, no pending entry after the first run).
func TestInformerPermanentFailureTerminal(t *testing.T) {
	svc := newInformerTestSvc(t, nil)

	tok, _, _ := svc.CreateAccess("tbilisi", 2000.0)
	_ = svc.Subscribe(2003, tok)
	_ = svc.NotifyFirstPublish("listing-perm-01", "tbilisi", "I", "crisis", "alcohol", "urgent", time.Now())

	worker := v2.NewInformerWorker(svc, &permanentInformerSender{})
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce must not return infra error on permanent failure, got: %v", err)
	}

	// Entry must be in terminal 'failed' state (not pending).
	pending, _ := svc.LoadPendingOutbox()
	if len(pending) != 0 {
		t.Errorf("expected 0 pending entries after permanent failure, got %d", len(pending))
	}
	state, _ := svc.QueryOutboxState("listing-perm-01")
	if state != "failed" {
		t.Errorf("expected state=failed after permanent failure, got %q", state)
	}
}

// TestInformerMaxAttemptsTerminal: MaxOutboxAttempts consecutive retryable
// failures exhaust the retry budget and mark the entry failed.
func TestInformerMaxAttemptsTerminal(t *testing.T) {
	svc := newInformerTestSvc(t, nil)

	tok, _, _ := svc.CreateAccess("tbilisi", 2000.0)
	_ = svc.Subscribe(2004, tok)
	_ = svc.NotifyFirstPublish("listing-maxattempt-01", "tbilisi", "J", "crisis", "alcohol", "urgent", time.Now())

	worker := v2.NewInformerWorker(svc, &failingInformerSender{})

	// Run exactly MaxOutboxAttempts times; last run should mark the entry failed.
	for i := 0; i < v2.MaxOutboxAttempts; i++ {
		if err := worker.RunOnce(context.Background()); err != nil {
			t.Fatalf("RunOnce iteration %d: %v", i, err)
		}
	}

	pending, _ := svc.LoadPendingOutbox()
	if len(pending) != 0 {
		t.Errorf("expected 0 pending entries after %d attempts, got %d", v2.MaxOutboxAttempts, len(pending))
	}
	state, _ := svc.QueryOutboxState("listing-maxattempt-01")
	if state != "failed" {
		t.Errorf("expected state=failed after max attempts exhausted, got %q", state)
	}
}
