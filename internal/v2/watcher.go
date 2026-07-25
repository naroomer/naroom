// Package v2 — V2 payment intent watcher.
//
// V2Watcher is isolated from internal/worker/invoice_watcher.go.
// It does NOT call V1 FindPayment (99% tolerance, skips unconfirmed).
// All blockchain, price, clock, and sleep dependencies are injectable for tests.
package v2

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"time"
)

// cappedBackoff implements capped exponential backoff for watcher retry.
// Start: 5s. Factor: 2. Cap: 5m. Resets after success.
type cappedBackoff struct {
	current time.Duration
	min     time.Duration
	max     time.Duration
	factor  float64
}

func newCappedBackoff() *cappedBackoff {
	return &cappedBackoff{
		min:    5 * time.Second,
		max:    5 * time.Minute,
		factor: 2.0,
	}
}

func (b *cappedBackoff) Next() time.Duration {
	if b.current == 0 {
		b.current = b.min
		return b.current
	}
	next := time.Duration(float64(b.current) * b.factor)
	if next > b.max {
		next = b.max
	}
	b.current = next
	return b.current
}

func (b *cappedBackoff) Reset() {
	b.current = 0
}

// CycleResult reports the outcome of one ProcessOnce cycle.
// Run uses this to decide whether to back off and by how much.
type CycleResult struct {
	// ProviderCallMade is true if at least one chain/balance API call was attempted.
	ProviderCallMade bool
	// HasProviderError is true if any provider, config, or DB error occurred in
	// this cycle. A single failing invoice makes the whole cycle failed — success
	// of other invoices in the same cycle does not hide the failure.
	HasProviderError bool
}

// defaultPollInterval is the normal sleep between successful or empty watcher cycles.
// Failure cycles use cappedBackoff instead.
const defaultPollInterval = 5 * time.Second

// V2Watcher processes V2 payment intents through the invoice state machine.
// All dependencies are injected — no direct blockchain or price API calls.
//
// Privacy: txids are used internally but never logged in full. Wallet addresses
// are never stored; they exist only transiently in MatchSenderAddress results.
type V2Watcher struct {
	svc          *Service
	clients      map[string]V2ChainClient // currency → chain client
	balance      ClientBalanceReader
	now          func() time.Time
	sleep        func(context.Context, time.Duration) // injectable for tests; cancellation-aware
	pollInterval time.Duration                        // normal sleep after success/empty cycle
	backoff      *cappedBackoff
	policy       V2BalancePolicy
}

// NewV2Watcher creates a V2Watcher.
// clients must have entries for all currencies this watcher is expected to process ("BTC", "LTC").
// pollInterval is the normal sleep between successful or empty cycles; 0 means defaultPollInterval (5s).
func NewV2Watcher(
	svc *Service,
	clients map[string]V2ChainClient,
	balance ClientBalanceReader,
	now func() time.Time,
	sleep func(context.Context, time.Duration),
	pollInterval time.Duration,
) (*V2Watcher, error) {
	if svc == nil {
		return nil, errors.New("v2: NewV2Watcher: svc must not be nil")
	}
	if len(clients) == 0 {
		return nil, errors.New("v2: NewV2Watcher: clients must not be empty")
	}
	if balance == nil {
		return nil, errors.New("v2: NewV2Watcher: balance must not be nil")
	}
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) {
			select {
			case <-ctx.Done():
			case <-time.After(d):
			}
		}
	}
	if pollInterval < 0 {
		return nil, errors.New("v2: NewV2Watcher: pollInterval must not be negative")
	}
	if pollInterval == 0 {
		pollInterval = defaultPollInterval
	}
	return &V2Watcher{
		svc:          svc,
		clients:      clients,
		balance:      balance,
		now:          now,
		sleep:        sleep,
		pollInterval: pollInterval,
		backoff:      newCappedBackoff(),
		policy:       DefaultV2BalancePolicy(),
	}, nil
}

// SetPolicy replaces the balance policy on this V2Watcher.
func (w *V2Watcher) SetPolicy(p V2BalancePolicy) { w.policy = p }

// ProcessOnce runs one full cycle: loads all watchable invoices and processes each.
// It is safe to call concurrently — database CAS prevents double transitions and
// ProcessOnce no longer mutates shared watcher state (backoff is owned by Run).
// Returns a CycleResult that Run uses to update backoff; see Run for details.
func (w *V2Watcher) ProcessOnce(ctx context.Context) CycleResult {
	invoices, err := w.svc.LoadWatchableInvoices()
	if err != nil {
		slog.Error("v2 watcher: load watchable invoices", "err", "[internal]")
		return CycleResult{HasProviderError: true}
	}

	var result CycleResult
	for _, fv := range invoices {
		if ctx.Err() != nil {
			return result
		}
		var callMade, hasErr bool
		switch fv.InvoiceStatus {
		case InvoiceStatusPending:
			callMade, hasErr = w.processPending(ctx, fv)
		case InvoiceStatusDetected:
			callMade, hasErr = w.processDetected(ctx, fv)
		case InvoiceStatusConfirmed:
			callMade, hasErr = w.processConfirmedBalance(ctx, fv)
		}
		if callMade {
			result.ProviderCallMade = true
		}
		if hasErr {
			result.HasProviderError = true
			// Do not return early: other invoices must still be processed.
		}
	}
	return result
}

// Run polls in a loop until ctx is cancelled. Every cycle ends with a sleep:
//
//   - Failed cycle (HasProviderError=true): sleep backoff.Next() (5s→10s→20s→…→5m).
//     The failure progression grows until a successful cycle resets it.
//   - Successful or empty cycle: reset failure progression; sleep pollInterval (default 5s).
//
// This prevents tight-looping on both empty queues and successful provider calls.
// Context cancellation terminates any in-progress sleep immediately.
// Backoff is managed here exactly once per cycle; ProcessOnce is stateless w.r.t. backoff.
func (w *V2Watcher) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		result := w.ProcessOnce(ctx)
		if result.HasProviderError {
			delay := w.backoff.Next()
			w.sleep(ctx, delay)
		} else {
			w.backoff.Reset()
			w.sleep(ctx, w.pollInterval)
		}
	}
}

func (w *V2Watcher) chainClient(currency string) (V2ChainClient, bool) {
	c, ok := w.clients[currency]
	return c, ok
}

// processPending handles a pending invoice: expire if past deadline, otherwise
// poll the chain for a matching payment. Returns (callMade, hasError).
func (w *V2Watcher) processPending(ctx context.Context, fv FlowView) (callMade, hasErr bool) {
	now := w.now()

	// Check if detection deadline has passed (boundary: == is not expired).
	if now.Unix() > fv.DetectionDeadlineAt.Unix() {
		_, err := w.svc.ExpireInvoice(fv.FlowID, fv.InvoiceID, now)
		if err != nil {
			if errors.Is(err, ErrExpired) || errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalidState) {
				// Controlled outcomes: already expired, confirmed concurrently,
				// or deadline not yet due (CAS race) — no backoff needed.
				return false, false
			}
			slog.Error("v2 watcher: expire pending invoice", "err", "[internal]")
			return false, true // unexpected DB error → trigger backoff
		}
		return false, false // expiry is a DB-only operation, not a provider call
	}

	client, ok := w.chainClient(fv.Currency)
	if !ok {
		slog.Error("v2 watcher: no chain client for currency", "currency", fv.Currency)
		return false, true // config error counts as a provider error for backoff
	}

	txs, err := client.GetInvoiceTxs(ctx, fv.PaymentAddress)
	if err != nil {
		slog.Error("v2 watcher: get invoice txs", "err", "[internal]")
		return true, true // provider call made, provider error
	}

	for _, tx := range txs {
		if tx.AmountAtomic < fv.AmountAtomic {
			continue // underpayment — keep looking
		}
		_, detErr := w.svc.RecordPaymentDetected(
			fv.FlowID, fv.InvoiceID, tx.Txid,
			tx.InputAddresses,
			tx.AmountAtomic,
			now,
		)
		if detErr == nil {
			return true, false // successfully detected
		}
		if errors.Is(detErr, ErrSenderMismatch) {
			continue // wrong sender, try next tx
		}
		if errors.Is(detErr, ErrExpired) || errors.Is(detErr, ErrConflict) {
			return true, false // deadline passed or concurrent handler — not a provider error
		}
		slog.Error("v2 watcher: record payment detected", "err", "[internal]")
		return true, true // provider call succeeded but DB error
	}
	return true, false // provider responded successfully, no suitable tx yet
}

// processDetected handles a payment_detected invoice: expire if grace passed,
// otherwise poll the chain for confirmation. Returns (callMade, hasError).
func (w *V2Watcher) processDetected(ctx context.Context, fv FlowView) (callMade, hasErr bool) {
	now := w.now()

	// Check if confirmation grace period has passed.
	if fv.ConfirmationDeadlineAt != nil && now.Unix() > fv.ConfirmationDeadlineAt.Unix() {
		_, err := w.svc.ExpireInvoice(fv.FlowID, fv.InvoiceID, now)
		if err != nil {
			if errors.Is(err, ErrExpired) || errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalidState) {
				return false, false
			}
			slog.Error("v2 watcher: expire detected invoice", "err", "[internal]")
			return false, true // unexpected DB error → trigger backoff
		}
		return false, false
	}

	if fv.DetectedTxid == nil {
		return false, false // should not happen — detected invoice always has a txid
	}

	client, ok := w.chainClient(fv.Currency)
	if !ok {
		return false, true
	}

	txs, err := client.GetInvoiceTxs(ctx, fv.PaymentAddress)
	if err != nil {
		slog.Error("v2 watcher: get invoice txs (confirmation check)", "err", "[internal]")
		return true, true // provider call made, provider error
	}

	for _, tx := range txs {
		if tx.Txid != *fv.DetectedTxid {
			continue
		}
		if tx.Confirmations < 1 {
			return true, false // not yet confirmed — success, keep watching
		}
		// Confirmed: transition to confirmed state.
		_, confErr := w.svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, now)
		if confErr != nil && !errors.Is(confErr, ErrConflict) {
			slog.Error("v2 watcher: confirm payment", "err", "[internal]")
			return true, true // provider call succeeded but DB error
		}

		// After confirmation, do balance check.
		// We need the sender address — find it from this tx's inputs.
		balCallMade, balErr := w.doBalanceCheck(ctx, fv.FlowID, fv.Currency, tx.InputAddresses)
		return true, balErr && balCallMade
	}
	return true, false // provider responded, tx not found or not confirmed yet
}

// processConfirmedBalance handles a confirmed invoice that still needs its
// post-payment balance check. Returns (callMade, hasError).
func (w *V2Watcher) processConfirmedBalance(ctx context.Context, fv FlowView) (callMade, hasErr bool) {
	if fv.DetectedTxid == nil {
		return false, false
	}
	client, ok := w.chainClient(fv.Currency)
	if !ok {
		return false, true
	}
	txs, err := client.GetInvoiceTxs(ctx, fv.PaymentAddress)
	if err != nil {
		slog.Error("v2 watcher: get txs for balance recheck", "err", "[internal]")
		return true, true // provider call made, provider error
	}
	for _, tx := range txs {
		if tx.Txid != *fv.DetectedTxid {
			continue
		}
		balCallMade, balErr := w.doBalanceCheck(ctx, fv.FlowID, fv.Currency, tx.InputAddresses)
		return balCallMade, balErr
	}
	return true, false // provider responded, tx not found (unusual but not an error)
}

// doBalanceCheck looks up the sender address via HMAC fingerprint, reads its
// USD balance, and records it. Returns (callMade, hasError).
func (w *V2Watcher) doBalanceCheck(ctx context.Context, flowID, currency string, inputAddresses []string) (callMade, hasErr bool) {
	// Find the sender address matching the wallet fingerprint.
	senderAddr, err := w.svc.MatchSenderAddress(flowID, inputAddresses)
	if err != nil {
		slog.Error("v2 watcher: match sender address", "err", "[internal]")
		return false, true
	}

	// Check USD balance.
	balanceUSD, err := w.balance.BalanceUSD(ctx, senderAddr, currency)
	if err != nil {
		slog.Error("v2 watcher: balance check", "err", "[internal]")
		return true, true // provider call made, provider error
	}

	if math.IsNaN(balanceUSD) || math.IsInf(balanceUSD, 0) || balanceUSD < 0 {
		slog.Error("v2 watcher: invalid balance value")
		return true, true
	}

	_, err = w.svc.RecordPostPaymentBalance(flowID, balanceUSD, w.policy.ClientHardFloorUSD)
	if err != nil && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrInvalidState) {
		slog.Error("v2 watcher: record post-payment balance", "err", "[internal]")
		return true, true
	}
	return true, false
}
