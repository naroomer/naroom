// Package v2 — HelperPurchaseWatcher: processes Helper purchases through the state machine.
//
// Privacy: raw txids, wallet addresses, fingerprints, inputs and tokens are never logged.
package v2

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"time"
)

// HelperPurchaseWatcher processes Helper contact purchases through their invoice state machine.
// Architecture mirrors V2Watcher; dependencies are fully injectable for tests.
type HelperPurchaseWatcher struct {
	svc          *HelperPurchaseService
	clients      map[string]V2ChainClient // currency → chain client
	balance      ClientBalanceReader
	now          func() time.Time
	sleep        func(context.Context, time.Duration)
	pollInterval time.Duration
	backoff      *cappedBackoff
}

// NewHelperPurchaseWatcher creates a HelperPurchaseWatcher.
func NewHelperPurchaseWatcher(
	svc *HelperPurchaseService,
	clients map[string]V2ChainClient,
	balance ClientBalanceReader,
	now func() time.Time,
	sleep func(context.Context, time.Duration),
	pollInterval time.Duration,
) (*HelperPurchaseWatcher, error) {
	if svc == nil {
		return nil, errors.New("v2: NewHelperPurchaseWatcher: svc must not be nil")
	}
	if len(clients) == 0 {
		return nil, errors.New("v2: NewHelperPurchaseWatcher: clients must not be empty")
	}
	if balance == nil {
		return nil, errors.New("v2: NewHelperPurchaseWatcher: balance must not be nil")
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
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	return &HelperPurchaseWatcher{
		svc:          svc,
		clients:      clients,
		balance:      balance,
		now:          now,
		sleep:        sleep,
		pollInterval: pollInterval,
		backoff:      newCappedBackoff(),
	}, nil
}

// ProcessOnce runs one full cycle: loads all watchable purchases and processes each.
// Returns CycleResult for backoff decisions.
func (w *HelperPurchaseWatcher) ProcessOnce(ctx context.Context) CycleResult {
	purchases, err := w.svc.LoadWatchablePurchases()
	if err != nil {
		slog.Error("v2 helper watcher: load watchable purchases", "err", "[internal]")
		return CycleResult{HasProviderError: true}
	}

	var result CycleResult
	for _, p := range purchases {
		if ctx.Err() != nil {
			return result
		}
		var callMade, hasErr bool
		switch p.InvoiceStatus {
		case HPInvoicePending:
			callMade, hasErr = w.processHelperPending(ctx, p)
		case HPInvoiceDetected:
			callMade, hasErr = w.processHelperDetected(ctx, p)
		case HPInvoiceConfirmed:
			callMade, hasErr = w.processHelperConfirmedBalance(ctx, p)
		}
		if callMade {
			result.ProviderCallMade = true
		}
		if hasErr {
			result.HasProviderError = true
		}
	}
	return result
}

// Run polls in a loop until ctx is cancelled.
func (w *HelperPurchaseWatcher) Run(ctx context.Context) {
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

func (w *HelperPurchaseWatcher) chainClient(currency string) (V2ChainClient, bool) {
	c, ok := w.clients[currency]
	return c, ok
}

// processHelperPending: expire if past deadline, otherwise poll for payment.
func (w *HelperPurchaseWatcher) processHelperPending(ctx context.Context, p HelperWatchableView) (callMade, hasErr bool) {
	now := w.now()

	if now.Unix() > p.DetectionDeadlineAt.Unix() {
		_, err := w.svc.ExpireHelperInvoice(p.PurchaseID, now)
		if err != nil {
			if errors.Is(err, ErrExpired) || errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalidState) {
				return false, false
			}
			slog.Error("v2 helper watcher: expire pending invoice", "err", "[internal]")
			return false, true
		}
		return false, false
	}

	client, ok := w.chainClient(p.Currency)
	if !ok {
		slog.Error("v2 helper watcher: no chain client", "currency", p.Currency)
		return false, true
	}

	txs, err := client.GetInvoiceTxs(ctx, p.PaymentAddress)
	if err != nil {
		slog.Error("v2 helper watcher: get invoice txs", "err", "[internal]")
		return true, true
	}

	for _, tx := range txs {
		if tx.AmountAtomic < p.AmountAtomic {
			continue
		}
		_, detErr := w.svc.RecordHelperDetection(
			p.PurchaseID, tx.Txid,
			tx.InputAddresses,
			tx.AmountAtomic,
			now,
		)
		if detErr == nil {
			return true, false
		}
		if errors.Is(detErr, ErrSenderMismatch) {
			continue
		}
		if errors.Is(detErr, ErrExpired) || errors.Is(detErr, ErrConflict) {
			return true, false
		}
		slog.Error("v2 helper watcher: record detection", "err", "[internal]")
		return true, true
	}
	return true, false
}

// processHelperDetected: expire if past confirmation deadline, otherwise check for confirmation.
func (w *HelperPurchaseWatcher) processHelperDetected(ctx context.Context, p HelperWatchableView) (callMade, hasErr bool) {
	now := w.now()

	if p.ConfirmationDeadlineAt != nil && now.Unix() > p.ConfirmationDeadlineAt.Unix() {
		_, err := w.svc.ExpireHelperInvoice(p.PurchaseID, now)
		if err != nil {
			if errors.Is(err, ErrExpired) || errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalidState) {
				return false, false
			}
			slog.Error("v2 helper watcher: expire detected invoice", "err", "[internal]")
			return false, true
		}
		return false, false
	}

	if !p.DetectedTxidHashPresent {
		return false, false
	}

	client, ok := w.chainClient(p.Currency)
	if !ok {
		return false, true
	}

	txs, err := client.GetInvoiceTxs(ctx, p.PaymentAddress)
	if err != nil {
		slog.Error("v2 helper watcher: get txs for confirmation", "err", "[internal]")
		return true, true
	}

	// We need to find the tx whose hash matches the stored txid_hash.
	// We compute the hash of each candidate tx.
	for _, tx := range txs {
		txHash := w.svc.HelperTxidHash(tx.Txid)
		// Read the stored hash from the DB.
		var storedHash string
		qErr := w.svc.db.QueryRow(`
			SELECT detected_txid_hash FROM v2_helper_invoices WHERE purchase_id = ?`, p.PurchaseID,
		).Scan(&storedHash)
		if qErr != nil || storedHash == "" {
			continue
		}
		if txHash != storedHash {
			continue
		}

		if tx.Confirmations < 1 {
			return true, false // not yet confirmed
		}

		_, confErr := w.svc.ConfirmHelperPayment(p.PurchaseID, now)
		if confErr != nil && !errors.Is(confErr, ErrConflict) {
			slog.Error("v2 helper watcher: confirm payment", "err", "[internal]")
			return true, true
		}

		// After confirmation, do balance check.
		balCallMade, balErr := w.doHelperBalanceCheck(ctx, p.PurchaseID, tx.InputAddresses)
		return true, balErr && balCallMade
	}
	return true, false
}

// processHelperConfirmedBalance: purchase confirmed but balance not yet checked.
func (w *HelperPurchaseWatcher) processHelperConfirmedBalance(ctx context.Context, p HelperWatchableView) (callMade, hasErr bool) {
	if !p.DetectedTxidHashPresent {
		return false, false
	}

	client, ok := w.chainClient(p.Currency)
	if !ok {
		return false, true
	}

	txs, err := client.GetInvoiceTxs(ctx, p.PaymentAddress)
	if err != nil {
		slog.Error("v2 helper watcher: get txs for balance recheck", "err", "[internal]")
		return true, true
	}

	// Find tx whose hash matches stored hash.
	var storedHash string
	if qErr := w.svc.db.QueryRow(`
		SELECT detected_txid_hash FROM v2_helper_invoices WHERE purchase_id = ?`, p.PurchaseID,
	).Scan(&storedHash); qErr != nil || storedHash == "" {
		return false, false
	}

	for _, tx := range txs {
		if w.svc.HelperTxidHash(tx.Txid) != storedHash {
			continue
		}
		balCallMade, balErr := w.doHelperBalanceCheck(ctx, p.PurchaseID, tx.InputAddresses)
		return balCallMade, balErr
	}
	return true, false
}

// doHelperBalanceCheck: finds sender address, reads balance, records it.
func (w *HelperPurchaseWatcher) doHelperBalanceCheck(ctx context.Context, purchaseID string, inputAddresses []string) (callMade, hasErr bool) {
	senderAddr, err := w.svc.MatchHelperSenderAddress(purchaseID, inputAddresses)
	if err != nil {
		slog.Error("v2 helper watcher: match sender address", "err", "[internal]")
		return false, true
	}

	// We need the currency — read from profile.
	var currency string
	if qErr := w.svc.db.QueryRow(`
		SELECT hp.currency
		FROM v2_helper_purchases p
		JOIN v2_helper_profiles hp ON hp.id = p.helper_profile_id
		WHERE p.id = ?`, purchaseID,
	).Scan(&currency); qErr != nil {
		slog.Error("v2 helper watcher: read currency", "err", "[internal]")
		return false, true
	}

	balanceUSD, err := w.balance.BalanceUSD(ctx, senderAddr, currency)
	if err != nil {
		slog.Error("v2 helper watcher: balance check", "err", "[internal]")
		return true, true
	}

	if math.IsNaN(balanceUSD) || math.IsInf(balanceUSD, 0) || balanceUSD < 0 {
		slog.Error("v2 helper watcher: invalid balance value")
		return true, true
	}

	_, err = w.svc.RecordHelperPostPaymentBalance(purchaseID, balanceUSD)
	if err != nil && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrInvalidState) && !errors.Is(err, ErrHelperCountryMismatch) {
		slog.Error("v2 helper watcher: record post-payment balance", "err", "[internal]")
		return true, true
	}
	return true, false
}
