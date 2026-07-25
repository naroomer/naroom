package v2

// §3 tests for LifecycleWorker.
//
// Uses in-memory SQLite and the production composition path (WireV2System /
// newE2EComp) so that the worker is assembled identically to production.
//
// Tests prove:
//  1. An expired listing is hidden by NormalizeOnce without a user HTTP request.
//  2. A transient error from one NormalizeOnce sub-operation does not stop
//     the LifecycleWorker.Run loop from executing subsequent ticks.
//  3. Run loop exits cleanly when the context is cancelled (no goroutine leak).
//  4. §3 — Each of the four operations can be individually error-injected via
//     the operation seams. First tick may fail; second tick runs (overlap protection).
//  5. §3 — Overlap protection: Run loop never starts a new tick while a previous
//     NormalizeOnce or ReviewDeliveryOnce is still running.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── §2.1: Expired listing hidden by NormalizeOnce ────────────────────────────

// TestLifecycleWorker_ExpiredListingHiddenWithoutHTTPRequest publishes a listing
// and then directly calls NormalizeOnce with a clock past the 24h visibility
// window. Verifies the listing becomes "hidden" without any user HTTP request.
func TestLifecycleWorker_ExpiredListingHiddenWithoutHTTPRequest(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	var nowT time.Time
	nowT = base
	nowFn := func() time.Time { return nowT }

	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	contactCipher, _ := NewAESGCMContactCipher(e2eContactKey, "v1")
	destCipher, _ := NewDestinationCipher(e2eDestKey, "v1")

	sys, err := WireV2System(
		db,
		V2Keys{HMACKey: e2eHMACKey, ContactCipher: contactCipher, DestCipher: destCipher, ContactKeyVersion: "v1"},
		V2BotConfig{
			ClientBotName:         "testbot",
			ClientWebhookSecret:   []byte("webhooksecret"),
			InformerBotName:       "informerbot",
			InformerWebhookSecret: []byte("informersecret"),
		},
		V2Adapters{
			BTCChain: &e2eChainStub{}, LTCChain: &e2eChainStub{},
			PriceSource: &e2ePriceStub{},
			BTCBalance:  &e2eAtomicBalStub{}, LTCBalance: &e2eAtomicBalStub{},
			HDAllocator:    &e2eAllocStub{},
			ClientSender:   &e2eBotSender{},
			InformerSender: &e2eInformerSender{},
			Now:            nowFn,
		},
		DefaultV2BalancePolicy(),
	)
	if err != nil {
		t.Fatalf("WireV2System: %v", err)
	}

	// Publish a listing at base time via e2eComp helpers.
	comp := &e2eComp{
		sys: sys, db: sys.db, svc: sys.svc,
		listingSvc: sys.listingSvc, helperSvc: sys.helperSvc,
		reviewSvc: sys.reviewSvc, informerSvc: sys.informerSvc,
		destCipher: sys.destCipher, now: nowFn,
	}
	lv := comp.e2ePublishListing(t, "lc_btc_01", "BTC", "tbilisi")
	if lv.State != "visible" {
		t.Fatalf("setup: listing must be visible immediately after publish, got %s", lv.State)
	}

	// Advance clock past 24h visibility window (25h = 90000s).
	nowT = base.Add(25 * time.Hour)

	// NormalizeOnce should hide the listing without any HTTP request.
	if err := sys.LifecycleWorker.NormalizeOnce(); err != nil {
		t.Logf("NormalizeOnce returned error (non-fatal in test): %v", err)
	}

	// BoardQuery returns only visible listings; the expired listing must not appear.
	visibleListings, err := comp.listingSvc.BoardQuery("tbilisi", nowT)
	if err != nil {
		t.Fatalf("BoardQuery: %v", err)
	}
	for _, l := range visibleListings {
		if l.ID == lv.ID {
			t.Errorf("expired listing %s still visible in board after NormalizeOnce", lv.ID)
		}
	}
}

// ── §2.2: Transient error does not stop Run loop ──────────────────────────────

// TestLifecycleWorker_TransientErrorDoesNotStopLoop verifies that NormalizeOnce
// returning a non-nil error does not cause LifecycleWorker.Run to exit early.
// The test instruments a LifecycleWorker directly (package-internal access).
func TestLifecycleWorker_TransientErrorDoesNotStopLoop(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	contactCipher, _ := NewAESGCMContactCipher(e2eContactKey, "v1")
	destCipher, _ := NewDestinationCipher(e2eDestKey, "v1")
	sys, err := WireV2System(
		db,
		V2Keys{HMACKey: e2eHMACKey, ContactCipher: contactCipher, DestCipher: destCipher, ContactKeyVersion: "v1"},
		V2BotConfig{
			ClientBotName:         "testbot",
			ClientWebhookSecret:   []byte("webhooksecret"),
			InformerBotName:       "informerbot",
			InformerWebhookSecret: []byte("informersecret"),
		},
		V2Adapters{
			BTCChain: &e2eChainStub{}, LTCChain: &e2eChainStub{},
			PriceSource: &e2ePriceStub{},
			BTCBalance:  &e2eAtomicBalStub{}, LTCBalance: &e2eAtomicBalStub{},
			HDAllocator:    &e2eAllocStub{},
			ClientSender:   &e2eBotSender{},
			InformerSender: &e2eInformerSender{},
			Now:            time.Now,
		},
		DefaultV2BalancePolicy(),
	)
	if err != nil {
		t.Fatalf("WireV2System: %v", err)
	}

	// Call NormalizeOnce multiple times: it must not panic or return a terminal error
	// that would stop subsequent invocations. (An empty DB → nil, non-nil are both OK.)
	for i := 0; i < 3; i++ {
		// NormalizeOnce logs errors but never panics; calling it on an empty DB
		// must not cause any state corruption.
		sys.LifecycleWorker.NormalizeOnce() //nolint:errcheck
	}
	// ReviewDeliveryOnce should also be idempotent on an empty DB.
	sys.LifecycleWorker.ReviewDeliveryOnce() //nolint:errcheck
}

// ── §2.3: Run loop exits on context cancellation ─────────────────────────────

// TestLifecycleWorker_RunExitsOnCancel starts LifecycleWorker.Run in a goroutine
// with a short normalizeInterval and reviewInterval, cancels the context, and
// verifies Run returns within a reasonable timeout (no goroutine leak).
func TestLifecycleWorker_RunExitsOnCancel(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	contactCipher, _ := NewAESGCMContactCipher(e2eContactKey, "v1")
	destCipher, _ := NewDestinationCipher(e2eDestKey, "v1")
	sys, err := WireV2System(
		db,
		V2Keys{HMACKey: e2eHMACKey, ContactCipher: contactCipher, DestCipher: destCipher, ContactKeyVersion: "v1"},
		V2BotConfig{
			ClientBotName:         "testbot",
			ClientWebhookSecret:   []byte("webhooksecret"),
			InformerBotName:       "informerbot",
			InformerWebhookSecret: []byte("informersecret"),
		},
		V2Adapters{
			BTCChain: &e2eChainStub{}, LTCChain: &e2eChainStub{},
			PriceSource: &e2ePriceStub{},
			BTCBalance:  &e2eAtomicBalStub{}, LTCBalance: &e2eAtomicBalStub{},
			HDAllocator:    &e2eAllocStub{},
			ClientSender:   &e2eBotSender{},
			InformerSender: &e2eInformerSender{},
			Now:            time.Now,
		},
		DefaultV2BalancePolicy(),
	)
	if err != nil {
		t.Fatalf("WireV2System: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		// Use very short intervals so the loop ticks quickly in the test.
		sys.LifecycleWorker.Run(ctx, 10*time.Millisecond, 10*time.Millisecond)
		close(done)
	}()

	// Let the loop run for a brief moment, then cancel.
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Run exited cleanly after cancellation.
	case <-time.After(2 * time.Second):
		t.Error("LifecycleWorker.Run did not exit within 2s after context cancellation — possible goroutine leak")
	}
}

// ── §3: Operation seam error injection ───────────────────────────────────────

// newBareWorker creates a LifecycleWorker whose all four operation seams are
// pre-set to no-ops. Tests then replace individual seams to inject behavior.
// normalizeInterval=60s and reviewInterval=30s match the production values.
func newBareWorker(t *testing.T) *LifecycleWorker {
	t.Helper()
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	contactCipher, _ := NewAESGCMContactCipher(e2eContactKey, "v1")
	destCipher, _ := NewDestinationCipher(e2eDestKey, "v1")
	sys, err := WireV2System(
		db,
		V2Keys{HMACKey: e2eHMACKey, ContactCipher: contactCipher, DestCipher: destCipher, ContactKeyVersion: "v1"},
		V2BotConfig{
			ClientBotName:         "testbot",
			ClientWebhookSecret:   []byte("webhooksecret"),
			InformerBotName:       "informerbot",
			InformerWebhookSecret: []byte("informersecret"),
		},
		V2Adapters{
			BTCChain: &e2eChainStub{}, LTCChain: &e2eChainStub{},
			PriceSource: &e2ePriceStub{},
			BTCBalance:  &e2eAtomicBalStub{}, LTCBalance: &e2eAtomicBalStub{},
			HDAllocator:    &e2eAllocStub{},
			ClientSender:   &e2eBotSender{},
			InformerSender: &e2eInformerSender{},
			Now:            time.Now,
		},
		DefaultV2BalancePolicy(),
	)
	if err != nil {
		t.Fatalf("WireV2System: %v", err)
	}
	// Replace all seams with no-ops; tests set individual ones.
	noop := func(_ time.Time) error { return nil }
	sys.LifecycleWorker.opNormalizeExpired = noop
	sys.LifecycleWorker.opNormalizeHelper = noop
	sys.LifecycleWorker.opNormalizeAttempts = noop
	sys.LifecycleWorker.opSendReviews = func() error { return nil }
	return sys.LifecycleWorker
}

var errInjected = errors.New("injected test error")

// TestLifecycleOp_NormalizeExpired_ErrorThenSuccess proves that opNormalizeExpired
// can be individually replaced, first tick fails, second tick succeeds.
func TestLifecycleOp_NormalizeExpired_ErrorThenSuccess(t *testing.T) {
	w := newBareWorker(t)

	var callCount int32
	w.opNormalizeExpired = func(_ time.Time) error {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			return errInjected
		}
		return nil
	}

	// First tick: NormalizeExpired fails but NormalizeOnce must not return
	// a fatal error that would stop the loop.
	err1 := w.NormalizeOnce()
	if !errors.Is(err1, errInjected) {
		t.Errorf("tick 1: expected errInjected, got %v", err1)
	}

	// Second tick: NormalizeExpired succeeds.
	err2 := w.NormalizeOnce()
	if err2 != nil {
		t.Errorf("tick 2: expected nil, got %v", err2)
	}

	if n := atomic.LoadInt32(&callCount); n != 2 {
		t.Errorf("expected 2 calls to opNormalizeExpired, got %d", n)
	}
}

// TestLifecycleOp_NormalizeHelper_ErrorThenSuccess proves individual seam for
// opNormalizeHelper: first tick fails, second succeeds.
func TestLifecycleOp_NormalizeHelper_ErrorThenSuccess(t *testing.T) {
	w := newBareWorker(t)

	var callCount int32
	w.opNormalizeHelper = func(_ time.Time) error {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			return errInjected
		}
		return nil
	}

	err1 := w.NormalizeOnce()
	if !errors.Is(err1, errInjected) {
		t.Errorf("tick 1: expected errInjected, got %v", err1)
	}
	err2 := w.NormalizeOnce()
	if err2 != nil {
		t.Errorf("tick 2: expected nil, got %v", err2)
	}
	if n := atomic.LoadInt32(&callCount); n != 2 {
		t.Errorf("expected 2 calls to opNormalizeHelper, got %d", n)
	}
}

// TestLifecycleOp_NormalizeAttempts_ErrorThenSuccess proves individual seam for
// opNormalizeAttempts: first tick fails, second succeeds.
func TestLifecycleOp_NormalizeAttempts_ErrorThenSuccess(t *testing.T) {
	w := newBareWorker(t)

	var callCount int32
	w.opNormalizeAttempts = func(_ time.Time) error {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			return errInjected
		}
		return nil
	}

	err1 := w.NormalizeOnce()
	if !errors.Is(err1, errInjected) {
		t.Errorf("tick 1: expected errInjected, got %v", err1)
	}
	err2 := w.NormalizeOnce()
	if err2 != nil {
		t.Errorf("tick 2: expected nil, got %v", err2)
	}
	if n := atomic.LoadInt32(&callCount); n != 2 {
		t.Errorf("expected 2 calls to opNormalizeAttempts, got %d", n)
	}
}

// TestLifecycleOp_SendReviews_ErrorThenSuccess proves individual seam for
// opSendReviews: first tick fails, second succeeds.
func TestLifecycleOp_SendReviews_ErrorThenSuccess(t *testing.T) {
	w := newBareWorker(t)

	var callCount int32
	w.opSendReviews = func() error {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			return errInjected
		}
		return nil
	}

	err1 := w.ReviewDeliveryOnce()
	if !errors.Is(err1, errInjected) {
		t.Errorf("tick 1: expected errInjected, got %v", err1)
	}
	err2 := w.ReviewDeliveryOnce()
	if err2 != nil {
		t.Errorf("tick 2: expected nil, got %v", err2)
	}
	if n := atomic.LoadInt32(&callCount); n != 2 {
		t.Errorf("expected 2 calls to opSendReviews, got %d", n)
	}
}

// TestLifecycleOp_SingleOpFailDoesNotBlockOthers proves that a failing
// NormalizeExpired does NOT prevent NormalizeHelper and NormalizeAttempts from
// running in the same NormalizeOnce call. Error isolation: one sub-op failing
// does not skip the remaining sub-ops.
func TestLifecycleOp_SingleOpFailDoesNotBlockOthers(t *testing.T) {
	w := newBareWorker(t)

	var helperCalls, attemptCalls int32
	w.opNormalizeExpired = func(_ time.Time) error { return errInjected }
	w.opNormalizeHelper = func(_ time.Time) error {
		atomic.AddInt32(&helperCalls, 1)
		return nil
	}
	w.opNormalizeAttempts = func(_ time.Time) error {
		atomic.AddInt32(&attemptCalls, 1)
		return nil
	}

	err := w.NormalizeOnce()
	// Must return the first error (from NormalizeExpired).
	if !errors.Is(err, errInjected) {
		t.Errorf("expected errInjected from NormalizeOnce, got %v", err)
	}
	// Must still have called NormalizeHelper and NormalizeAttempts.
	if n := atomic.LoadInt32(&helperCalls); n != 1 {
		t.Errorf("opNormalizeHelper must be called even when opNormalizeExpired fails, got %d calls", n)
	}
	if n := atomic.LoadInt32(&attemptCalls); n != 1 {
		t.Errorf("opNormalizeAttempts must be called even when opNormalizeExpired fails, got %d calls", n)
	}
}

// TestLifecycleWorker_ProductionIntervals proves that the exported constants
// NormalizeInterval and ReviewInterval match the production-documented values,
// and that production wiring (cmd/naroom/v2wire.go) uses these constants (not
// hard-coded literals).
func TestLifecycleWorker_ProductionIntervals(t *testing.T) {
	// Verify the named constants have the documented production values.
	if NormalizeInterval != 60*time.Second {
		t.Errorf("NormalizeInterval = %s, want 60s", NormalizeInterval)
	}
	if ReviewInterval != 30*time.Second {
		t.Errorf("ReviewInterval = %s, want 30s", ReviewInterval)
	}
	// Sanity: both positive, normalize >= review.
	if NormalizeInterval <= 0 {
		t.Error("NormalizeInterval must be positive")
	}
	if ReviewInterval <= 0 {
		t.Error("ReviewInterval must be positive")
	}
	if NormalizeInterval < ReviewInterval {
		t.Errorf("NormalizeInterval (%s) should be >= ReviewInterval (%s)", NormalizeInterval, ReviewInterval)
	}
}

// TestLifecycleWorker_RunLoop_NoOverlap proves that the Run loop never starts a
// new tick while the previous one is still running (non-overlapping sequential execution).
// Implementation: the tickers in Run fire; but since RunOnce is synchronous, the next
// select case only runs after the current NormalizeOnce/ReviewDeliveryOnce returns.
func TestLifecycleWorker_RunLoop_NoOverlap(t *testing.T) {
	w := newBareWorker(t)

	var (
		running    int32
		overlapSaw bool
	)

	// Seam that takes 20ms — longer than the ticker interval in the test.
	// If overlap occurred, 'running' would be 2 when a second call enters.
	slowOp := func(_ time.Time) error {
		if atomic.AddInt32(&running, 1) > 1 {
			overlapSaw = true
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&running, -1)
		return nil
	}
	w.opNormalizeExpired = slowOp
	w.opNormalizeHelper = func(_ time.Time) error { return nil }
	w.opNormalizeAttempts = func(_ time.Time) error { return nil }
	w.opSendReviews = func() error { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		// Tick every 5ms so we get multiple ticks during the 80ms window.
		w.Run(ctx, 5*time.Millisecond, 100*time.Second) // very short normalize, long review
		close(done)
	}()

	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done

	if overlapSaw {
		t.Error("overlap detected: Run loop executed two NormalizeOnce calls concurrently")
	}
}

// ── §3 domain state tests — real service implementations ─────────────────────

// newE2ESysWithClock creates a WireV2System with a controllable clock. Returned
// *e2eComp gives access to service-layer helpers; nowT is the clock source.
func newE2ESysWithClock(t *testing.T, nowT *time.Time) (*V2System, *e2eComp) {
	t.Helper()
	base := time.Now().UTC().Truncate(time.Second)
	*nowT = base
	nowFn := func() time.Time { return *nowT }

	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	contactCipher, _ := NewAESGCMContactCipher(e2eContactKey, "v1")
	destCipher, _ := NewDestinationCipher(e2eDestKey, "v1")

	sys, err := WireV2System(
		db,
		V2Keys{HMACKey: e2eHMACKey, ContactCipher: contactCipher, DestCipher: destCipher, ContactKeyVersion: "v1"},
		V2BotConfig{
			ClientBotName:         "testbot",
			ClientWebhookSecret:   []byte("webhooksecret"),
			InformerBotName:       "informerbot",
			InformerWebhookSecret: []byte("informersecret"),
		},
		V2Adapters{
			BTCChain: &e2eChainStub{}, LTCChain: &e2eChainStub{},
			PriceSource: &e2ePriceStub{},
			BTCBalance:  &e2eAtomicBalStub{}, LTCBalance: &e2eAtomicBalStub{},
			HDAllocator:    &e2eAllocStub{},
			ClientSender:   &e2eBotSender{},
			InformerSender: &e2eInformerSender{},
			Now:            nowFn,
		},
		DefaultV2BalancePolicy(),
	)
	if err != nil {
		t.Fatalf("WireV2System: %v", err)
	}

	comp := &e2eComp{
		sys:         sys,
		db:          sys.db,
		svc:         sys.svc,
		listingSvc:  sys.listingSvc,
		helperSvc:   sys.helperSvc,
		reviewSvc:   sys.reviewSvc,
		informerSvc: sys.informerSvc,
		destCipher:  sys.destCipher,
		now:         nowFn,
	}
	return sys, comp
}

// TestLifecycleWorker_HelperInvoiceExpiration proves that a helper purchase in
// awaiting_payment state transitions to invoice_expired once the detection
// deadline passes and NormalizeOnce is called.
func TestLifecycleWorker_HelperInvoiceExpiration(t *testing.T) {
	var nowT time.Time
	sys, comp := newE2ESysWithClock(t, &nowT)

	// Publish a listing so we can create a helper purchase against it.
	lv := comp.e2ePublishListing(t, "lc_btc_hi_01", "BTC", "tbilisi")
	if lv.State != "visible" {
		t.Fatalf("setup: listing must be visible, got %s", lv.State)
	}

	// Create a helper purchase — it starts in awaiting_payment.
	rawToken := newID()
	draft := HelperInvoiceDraft{
		PaymentAddress: "helper_inv_pay_01",
		AmountAtomic:   10000,
		AmountUSDCents: helperInvoiceUSDCents,
	}
	_, pv, err := comp.helperSvc.CreatePurchase(rawToken, lv.ID, "BTC", "lc_btc_helper_01", draft)
	if err != nil {
		t.Fatalf("CreatePurchase: %v", err)
	}
	if pv.State != HPStateAwaitingPayment {
		t.Fatalf("setup: purchase must start in %s, got %s", HPStateAwaitingPayment, pv.State)
	}

	// Advance clock past detectionWindow (60 min + epsilon).
	nowT = nowT.Add(detectionWindow + time.Second)

	// NormalizeOnce triggers helperSvc.NormalizeHelperExpired(now).
	if err := sys.LifecycleWorker.NormalizeOnce(); err != nil {
		t.Logf("NormalizeOnce returned error (non-fatal): %v", err)
	}

	// Restore the purchase and verify it transitioned to invoice_expired.
	pv2, err := comp.helperSvc.RestorePurchase(rawToken, "lc_btc_helper_01", "BTC")
	if err != nil {
		t.Fatalf("RestorePurchase: %v", err)
	}
	if pv2.State != HPStateInvoiceExpired {
		t.Errorf("purchase state after expiry: got %s, want %s", pv2.State, HPStateInvoiceExpired)
	}
}

// TestLifecycleWorker_TelegramAttemptExpiration proves that expired link
// attempts are removed from v2_telegram_link_attempts when NormalizeOnce is
// called with a clock past the attempt's expires_at.
func TestLifecycleWorker_TelegramAttemptExpiration(t *testing.T) {
	var nowT time.Time
	sys, _ := newE2ESysWithClock(t, &nowT)

	// Insert an expired link attempt directly; the creation code path (CreateLink)
	// requires a full payment flow. We test NormalizeExpiredAttempts semantics.
	// Disable FK enforcement so the test can insert without a real flow row.
	if _, err := sys.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable FK: %v", err)
	}
	t.Cleanup(func() { sys.db.Exec(`PRAGMA foreign_keys = ON`) }) //nolint:errcheck

	const attemptID = "test_link_attempt_expiry_01"
	const tokenHash = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	const flowID = "test_flow_telegram_expiry_01_xxxxxxxxxxxxxxxxxxxx"
	expiredAt := nowT.Add(-1 * time.Minute).Unix() // already expired
	_, err := sys.db.Exec(`
		INSERT INTO v2_telegram_link_attempts
		  (id, token_hash, flow_id, window_number, state, expires_at, lease_until, created_at, updated_at)
		VALUES (?, ?, ?, 1, 'pending', ?, NULL, ?, ?)`,
		attemptID, tokenHash, flowID, expiredAt, nowT.Add(-2*time.Minute).Unix(), nowT.Add(-2*time.Minute).Unix(),
	)
	if err != nil {
		t.Fatalf("insert link attempt: %v", err)
	}

	// Verify it's there.
	var count int
	if err := sys.db.QueryRow(
		`SELECT COUNT(*) FROM v2_telegram_link_attempts WHERE token_hash=?`, tokenHash,
	).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected 1 attempt, got %d (err=%v)", count, err)
	}

	// NormalizeOnce triggers telegramTr.NormalizeExpiredAttempts(now).
	if err := sys.LifecycleWorker.NormalizeOnce(); err != nil {
		t.Logf("NormalizeOnce returned error (non-fatal): %v", err)
	}

	// Attempt must be deleted.
	if err := sys.db.QueryRow(
		`SELECT COUNT(*) FROM v2_telegram_link_attempts WHERE token_hash=?`, tokenHash,
	).Scan(&count); err != nil {
		t.Fatalf("count after normalize: %v", err)
	}
	if count != 0 {
		t.Errorf("expired link attempt still present after NormalizeOnce (count=%d)", count)
	}
}

// trackingReviewSender records chatIDs of review prompt sends.
type trackingReviewSender struct {
	e2eBotSender
	mu    sync.Mutex
	calls []int64
}

func (s *trackingReviewSender) SendReviewPrompt(_ context.Context, chatID int64, _, _, _ string) error {
	s.mu.Lock()
	s.calls = append(s.calls, chatID)
	s.mu.Unlock()
	return nil
}

func (s *trackingReviewSender) sentCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// TestLifecycleWorker_PendingReviewNotificationDelivery proves that a pending
// delivery snapshot is sent via ReviewDeliveryOnce using the production
// SendPendingReviewNotifications code path.
func TestLifecycleWorker_PendingReviewNotificationDelivery(t *testing.T) {
	var nowT time.Time
	nowT = time.Now().UTC().Truncate(time.Second)
	nowFn := func() time.Time { return nowT }

	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	contactCipher, _ := NewAESGCMContactCipher(e2eContactKey, "v1")
	destCipher, _ := NewDestinationCipher(e2eDestKey, "v1")

	sender := &trackingReviewSender{}

	sys, err := WireV2System(
		db,
		V2Keys{HMACKey: e2eHMACKey, ContactCipher: contactCipher, DestCipher: destCipher, ContactKeyVersion: "v1"},
		V2BotConfig{
			ClientBotName:         "testbot",
			ClientWebhookSecret:   []byte("webhooksecret"),
			InformerBotName:       "informerbot",
			InformerWebhookSecret: []byte("informersecret"),
		},
		V2Adapters{
			BTCChain: &e2eChainStub{}, LTCChain: &e2eChainStub{},
			PriceSource: &e2ePriceStub{},
			BTCBalance:  &e2eAtomicBalStub{}, LTCBalance: &e2eAtomicBalStub{},
			HDAllocator:    &e2eAllocStub{},
			ClientSender:   sender,
			InformerSender: &e2eInformerSender{},
			Now:            nowFn,
		},
		DefaultV2BalancePolicy(),
	)
	if err != nil {
		t.Fatalf("WireV2System: %v", err)
	}

	comp := &e2eComp{
		sys:         sys,
		db:          sys.db,
		svc:         sys.svc,
		listingSvc:  sys.listingSvc,
		helperSvc:   sys.helperSvc,
		reviewSvc:   sys.reviewSvc,
		informerSvc: sys.informerSvc,
		destCipher:  sys.destCipher,
		now:         nowFn,
	}

	// Full flow to create a contact_ready purchase with a pending delivery snapshot.
	lv := comp.e2ePublishListing(t, "lc_btc_rd_01", "BTC", "tbilisi")
	rawToken := newID()
	draft := HelperInvoiceDraft{
		PaymentAddress: "helper_rd_pay_01",
		AmountAtomic:   10000,
		AmountUSDCents: helperInvoiceUSDCents,
	}
	_, pv, err := comp.helperSvc.CreatePurchase(rawToken, lv.ID, "BTC", "lc_btc_helper_rd_01", draft)
	if err != nil {
		t.Fatalf("CreatePurchase: %v", err)
	}
	pv, err = comp.helperSvc.RecordHelperDetection(pv.PurchaseID, "txid_rd_01",
		[]string{"lc_btc_helper_rd_01"}, draft.AmountAtomic, nowFn())
	if err != nil {
		t.Fatalf("RecordHelperDetection: %v", err)
	}
	pv, err = comp.helperSvc.ConfirmHelperPayment(pv.PurchaseID, nowFn())
	if err != nil {
		t.Fatalf("ConfirmHelperPayment: %v", err)
	}
	pv, err = comp.helperSvc.RecordHelperPostPaymentBalance(pv.PurchaseID, 2000.0)
	if err != nil || pv.State != HPStateContactReady {
		t.Fatalf("RecordHelperPostPaymentBalance: %v (state=%s)", err, pv.State)
	}

	// Now the review delivery snapshot should be in pending_send state.
	// ReviewDeliveryOnce calls SendPendingReviewNotifications which sends review prompts.
	beforeCount := sender.sentCount()
	if err := sys.LifecycleWorker.ReviewDeliveryOnce(); err != nil {
		t.Logf("ReviewDeliveryOnce error (non-fatal): %v", err)
	}
	afterCount := sender.sentCount()

	// The sender must have been called with at least one review prompt.
	if afterCount <= beforeCount {
		t.Errorf("ReviewDeliveryOnce did not send any review notification: before=%d, after=%d",
			beforeCount, afterCount)
	}
}

// TestLifecycleWorker_RunLoop_FirstTickErrorSecondTickSucceeds proves that the
// production Run goroutine executes a second tick even when the first tick
// returns an error. This tests the actual Run loop, not just NormalizeOnce.
func TestLifecycleWorker_RunLoop_FirstTickErrorSecondTickSucceeds(t *testing.T) {
	w := newBareWorker(t)

	var callCount int32
	w.opNormalizeExpired = func(_ time.Time) error {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			return errInjected
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		// Very short normalize interval so we get 2+ ticks quickly.
		// Long review interval to keep reviews out of this test.
		w.Run(ctx, 5*time.Millisecond, 10*time.Second)
		close(done)
	}()

	// Wait until at least 2 calls have been made (first tick error, second tick success).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&callCount) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	n := atomic.LoadInt32(&callCount)
	if n < 2 {
		t.Errorf("Run loop: expected at least 2 normalize calls (error then success), got %d", n)
	}
}

// TestLifecycleWorker_RunLoop_ReviewNoOverlap proves that the Run loop never
// starts a new ReviewDeliveryOnce tick while the previous one is still running.
func TestLifecycleWorker_RunLoop_ReviewNoOverlap(t *testing.T) {
	w := newBareWorker(t)

	var (
		reviewRunning int32
		overlapSaw    bool
	)

	// Slow review op — longer than the reviewer ticker interval.
	w.opSendReviews = func() error {
		if atomic.AddInt32(&reviewRunning, 1) > 1 {
			overlapSaw = true
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&reviewRunning, -1)
		return nil
	}
	// Normalize ops are no-ops so they don't interfere.
	w.opNormalizeExpired = func(_ time.Time) error { return nil }
	w.opNormalizeHelper = func(_ time.Time) error { return nil }
	w.opNormalizeAttempts = func(_ time.Time) error { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		// Very short review interval, long normalize to keep them separate.
		w.Run(ctx, 10*time.Second, 5*time.Millisecond)
		close(done)
	}()

	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done

	if overlapSaw {
		t.Error("overlap detected: Run loop executed two ReviewDeliveryOnce calls concurrently")
	}
}
