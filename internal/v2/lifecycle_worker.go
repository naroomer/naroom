package v2

// lifecycle_worker.go — periodic V2 maintenance loops.
//
// LifecycleWorker bundles four mandatory production lifecycle operations
// that must run on bounded intervals independent of user HTTP traffic:
//
//  1. ListingService.NormalizeExpired   — hides listings whose daily window closed;
//     marks finished when the 5-day entitlement expires.
//  2. HelperPurchaseService.NormalizeHelperExpired — expires stale helper invoices/purchases.
//  3. TelegramTransport.NormalizeExpiredAttempts   — deletes expired pending/processing
//     Telegram link attempts.
//  4. TelegramTransport.SendPendingReviewNotifications — delivers buffered review prompts.
//
// Safety:
//   - Each tick runs synchronously; the next tick only starts after the previous one
//     completes, preventing overlap even if a tick is slow.
//   - Transient errors are logged (with [internal] redaction) but do not stop
//     subsequent ticks.
//   - Goroutines exit cleanly on context cancellation.
//   - No tokens, wallet addresses, chat IDs, or PII appear in logs or errors.

import (
	"context"
	"log/slog"
	"time"
)

// NormalizeInterval is the recommended interval for running expiry normalization in production.
const NormalizeInterval = 60 * time.Second

// ReviewInterval is the recommended interval for running review-notification delivery in production.
const ReviewInterval = 30 * time.Second

// LifecycleWorker runs the four mandatory V2 periodic maintenance operations.
type LifecycleWorker struct {
	listingSvc *ListingService
	helperSvc  *HelperPurchaseService
	telegramTr *TelegramTransport
	now        func() time.Time

	// Operation seams — set to the real implementations by NewLifecycleWorker.
	// Tests can replace individual seams to inject errors or observe calls.
	// Each seam receives the current time (or ignores it for review delivery).
	opNormalizeExpired  func(now time.Time) error
	opNormalizeHelper   func(now time.Time) error
	opNormalizeAttempts func(now time.Time) error
	opSendReviews       func() error
}

// NewLifecycleWorker creates a LifecycleWorker. All arguments must be non-nil.
func NewLifecycleWorker(
	listingSvc *ListingService,
	helperSvc *HelperPurchaseService,
	telegramTr *TelegramTransport,
	now func() time.Time,
) *LifecycleWorker {
	w := &LifecycleWorker{
		listingSvc: listingSvc,
		helperSvc:  helperSvc,
		telegramTr: telegramTr,
		now:        now,
	}
	// Wire the default (real) implementations.
	w.opNormalizeExpired = listingSvc.NormalizeExpired
	w.opNormalizeHelper = helperSvc.NormalizeHelperExpired
	w.opNormalizeAttempts = telegramTr.NormalizeExpiredAttempts
	w.opSendReviews = func() error { return telegramTr.SendPendingReviewNotifications() }
	return w
}

// NormalizeOnce runs the three expiry-normalization operations once.
// Errors are logged with [internal] and do not propagate to the caller.
// Returns the first non-nil error for test assertions; in production the
// caller (Run loop) ignores the return value and continues on the next tick.
//
// Each sub-operation is called through an injectable seam (opNormalize*)
// set by NewLifecycleWorker. Tests can replace individual seams to inject
// errors without replacing the entire worker.
func (w *LifecycleWorker) NormalizeOnce() error {
	now := w.now()
	var firstErr error
	if err := w.opNormalizeExpired(now); err != nil {
		slog.Error("v2: lifecycle: normalize listings", "err", "[internal]")
		if firstErr == nil {
			firstErr = err
		}
	}
	if err := w.opNormalizeHelper(now); err != nil {
		slog.Error("v2: lifecycle: normalize helper purchases", "err", "[internal]")
		if firstErr == nil {
			firstErr = err
		}
	}
	if err := w.opNormalizeAttempts(now); err != nil {
		slog.Error("v2: lifecycle: normalize telegram attempts", "err", "[internal]")
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ReviewDeliveryOnce runs the review-notification delivery once.
// Errors are logged with [internal].
func (w *LifecycleWorker) ReviewDeliveryOnce() error {
	if err := w.opSendReviews(); err != nil {
		slog.Error("v2: lifecycle: send review notifications", "err", "[internal]")
		return err
	}
	return nil
}

// Run starts the lifecycle loops. Blocks until ctx is cancelled.
//
//   - normalizeInterval: how often to run expiry normalization (recommended: 60s).
//   - reviewInterval:    how often to attempt review delivery (recommended: 30s).
//
// Each operation runs to completion before the next tick is processed
// (non-overlapping).
func (w *LifecycleWorker) Run(ctx context.Context, normalizeInterval, reviewInterval time.Duration) {
	normTicker := time.NewTicker(normalizeInterval)
	reviewTicker := time.NewTicker(reviewInterval)
	defer normTicker.Stop()
	defer reviewTicker.Stop()

	// Run both immediately at startup before waiting for the first tick.
	w.NormalizeOnce()      //nolint:errcheck
	w.ReviewDeliveryOnce() //nolint:errcheck

	for {
		select {
		case <-ctx.Done():
			return
		case <-normTicker.C:
			w.NormalizeOnce() //nolint:errcheck
		case <-reviewTicker.C:
			w.ReviewDeliveryOnce() //nolint:errcheck
		}
	}
}
