package v2

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// newTestHelperWatcher creates a watcher backed by an in-memory service.
func newTestHelperWatcher(t *testing.T) (*HelperPurchaseWatcher, *HelperPurchaseService, *fakeChainClient, *fakeBalanceReader) {
	t.Helper()
	svc, _ := newTestHelperService(t)
	chain := newFakeChainClient()
	bal := &fakeBalanceReader{result: 1500.0}

	w, err := NewHelperPurchaseWatcher(
		svc,
		map[string]V2ChainClient{"BTC": chain, "LTC": chain},
		bal,
		time.Now,
		func(_ context.Context, _ time.Duration) {},
		0,
	)
	if err != nil {
		t.Fatalf("NewHelperPurchaseWatcher: %v", err)
	}
	return w, svc, chain, bal
}

// fullPurchaseSetup creates a purchase in awaiting_payment state.
func fullPurchaseSetup(t *testing.T, svc *HelperPurchaseService, currency, walletAddr, payAddr, country string) (purchaseID, rawToken string) {
	t.Helper()

	listingID := mustCreateVisibleListing(t, svc.db, country)
	normalized, _, _ := validateAndNormalizeAddress(walletAddr)

	draft := HelperInvoiceDraft{
		PaymentAddress: payAddr,
		AmountAtomic:   100000,
		AmountUSDCents: helperInvoiceUSDCents,
	}
	rawToken = newID()
	_, view, err := svc.CreatePurchase(rawToken, listingID, currency, normalized, draft)
	if err != nil {
		t.Fatalf("CreatePurchase: %v", err)
	}
	return view.PurchaseID, rawToken
}

// Test 11: BTC: pending → detected → confirmed → contact_ready.
func TestHelperWatcher_BTCFullFlow(t *testing.T) {
	w, svc, chain, bal := newTestHelperWatcher(t)
	bal.result = 1500.0

	btcWallet := testBTCBech32Addr
	purchaseID, _ := fullPurchaseSetup(t, svc, "BTC", btcWallet, "payaddr_btc", "US")

	chain.setTxs("payaddr_btc", []V2TxResult{
		{
			Txid:           "btc_txid_1",
			Confirmations:  0,
			AmountAtomic:   100000,
			InputAddresses: []string{btcWallet},
		},
	})

	ctx := context.Background()

	// Cycle 1: pending → payment_detected.
	w.ProcessOnce(ctx)
	view, _ := svc.readPurchaseView(purchaseID)
	if view.InvoiceStatus != HPInvoiceDetected {
		t.Errorf("after detection cycle: invoice status = %q, want %q", view.InvoiceStatus, HPInvoiceDetected)
	}

	// Cycle 2: 0 confirmations → stays detected.
	w.ProcessOnce(ctx)
	view, _ = svc.readPurchaseView(purchaseID)
	if view.State != HPStatePaymentDetected {
		t.Errorf("still detected: state = %q", view.State)
	}

	// Tx gets confirmed.
	chain.setTxs("payaddr_btc", []V2TxResult{
		{
			Txid:           "btc_txid_1",
			Confirmations:  1,
			AmountAtomic:   100000,
			InputAddresses: []string{btcWallet},
		},
	})

	// Cycle 3: confirmed → contact_ready.
	w.ProcessOnce(ctx)
	view, _ = svc.readPurchaseView(purchaseID)
	if view.State != HPStateContactReady {
		t.Errorf("after confirm cycle: state = %q, want contact_ready", view.State)
	}
}

// Test 12: 999.99 → paid_low_balance, contact not ready, purchase_count unchanged.
func TestHelperWatcher_LowBalance(t *testing.T) {
	w, svc, chain, bal := newTestHelperWatcher(t)
	bal.result = 999.99

	btcWallet := testBTCBech32Addr
	purchaseID, _ := fullPurchaseSetup(t, svc, "BTC", btcWallet, "payaddr_lb", "US")

	chain.setTxs("payaddr_lb", []V2TxResult{
		{Txid: "tx_lb", Confirmations: 1, AmountAtomic: 100000, InputAddresses: []string{btcWallet}},
	})

	ctx := context.Background()
	w.ProcessOnce(ctx) // detect
	w.ProcessOnce(ctx) // confirm + balance check

	view, _ := svc.readPurchaseView(purchaseID)
	if view.State != HPStatePaidLowBalance {
		t.Errorf("state = %q, want paid_low_balance", view.State)
	}
	if view.ContactReadyAt != nil {
		t.Error("contact_ready_at must be nil when balance insufficient")
	}

	var pc int
	svc.db.QueryRow(`SELECT purchase_count FROM v2_helper_profiles WHERE id=?`, view.ProfileID).Scan(&pc) //nolint:errcheck
	if pc != 0 {
		t.Errorf("purchase_count must be 0 after low balance, got %d", pc)
	}
}

// Test 13: Top-up same wallet to >= 1000 before retry deadline → contact_ready.
func TestHelperWatcher_TopUpToContactReady(t *testing.T) {
	now := time.Now()
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")
	svc, _ := NewHelperPurchaseService(db, testHMACKey, cipher, NewRandomDisplayNameGenerator(), func() time.Time { return now })

	btcWallet := testBTCBech32Addr
	listingID := mustCreateVisibleListing(t, db, "US")
	normalized, currency, _ := validateAndNormalizeAddress(btcWallet)

	draft := HelperInvoiceDraft{PaymentAddress: "payaddr_topup", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, view, _ := svc.CreatePurchase(newID(), listingID, currency, normalized, draft)
	purchaseID := view.PurchaseID

	svc.RecordHelperDetection(purchaseID, "txid_topup", []string{btcWallet}, 100000, now) //nolint:errcheck
	svc.ConfirmHelperPayment(purchaseID, now)                                             //nolint:errcheck
	svc.RecordHelperPostPaymentBalance(purchaseID, 500.0)                                 //nolint:errcheck

	v, _ := svc.readPurchaseView(purchaseID)
	if v.State != HPStatePaidLowBalance {
		t.Fatalf("expected paid_low_balance, got %q", v.State)
	}

	retryDeadline := v.BalanceRetryDeadlineAt
	if retryDeadline == nil {
		t.Fatal("balance_retry_deadline_at must be set")
	}

	// Recheck at exactly the deadline (equality = still allowed).
	updated, err := svc.RecheckHelperBalance(purchaseID, 1500.0, *retryDeadline)
	if err != nil {
		t.Fatalf("RecheckHelperBalance at deadline: %v", err)
	}
	if updated.State != HPStateContactReady {
		t.Errorf("state = %q, want contact_ready", updated.State)
	}
}

// Test 14: Retry after deadline → ErrHelperRetryDeadlinePassed; wrong wallet → ErrHelperNotFound.
func TestHelperWatcher_RetryAfterDeadline(t *testing.T) {
	now := time.Now()
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")
	svc, _ := NewHelperPurchaseService(db, testHMACKey, cipher, NewRandomDisplayNameGenerator(), func() time.Time { return now })

	btcWallet := testBTCBech32Addr
	listingID := mustCreateVisibleListing(t, db, "US")
	normalized, currency, _ := validateAndNormalizeAddress(btcWallet)

	rawToken := newID()
	draft := HelperInvoiceDraft{PaymentAddress: "payaddr_late", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, view, _ := svc.CreatePurchase(rawToken, listingID, currency, normalized, draft)
	svc.RecordHelperDetection(view.PurchaseID, "txid_late", []string{btcWallet}, 100000, now) //nolint:errcheck
	svc.ConfirmHelperPayment(view.PurchaseID, now)                                            //nolint:errcheck
	svc.RecordHelperPostPaymentBalance(view.PurchaseID, 500.0)                                //nolint:errcheck

	v, _ := svc.readPurchaseView(view.PurchaseID)
	retryDeadline := v.BalanceRetryDeadlineAt
	afterDeadline := retryDeadline.Add(time.Second)

	_, retryErr := svc.RecheckHelperBalance(view.PurchaseID, 1500.0, afterDeadline)
	if !errors.Is(retryErr, ErrHelperRetryDeadlinePassed) {
		t.Errorf("expected ErrHelperRetryDeadlinePassed, got %v", retryErr)
	}

	// Wrong wallet → same 404.
	otherAddr := "bc1q34aq5pa62push4zhekkzr29s3jkl9p05j6h3h3"
	otherNorm, otherCur, _ := validateAndNormalizeAddress(otherAddr)
	_, restoreErr := svc.RestorePurchase(rawToken, otherNorm, otherCur)
	if !errors.Is(restoreErr, ErrHelperNotFound) {
		t.Errorf("wrong wallet: expected ErrHelperNotFound, got %v", restoreErr)
	}
}

// Test 15: Wrong sender, underpayment, split transactions.
func TestHelperWatcher_SenderAndPaymentChecks(t *testing.T) {
	w, svc, chain, _ := newTestHelperWatcher(t)

	btcWallet := testBTCBech32Addr
	purchaseID, _ := fullPurchaseSetup(t, svc, "BTC", btcWallet, "payaddr_checks", "US")
	ctx := context.Background()

	// Wrong sender — no detection.
	chain.setTxs("payaddr_checks", []V2TxResult{
		{Txid: "wrong_tx", Confirmations: 0, AmountAtomic: 100000, InputAddresses: []string{"bc1q34aq5pa62push4zhekkzr29s3jkl9p05j6h3h3"}},
	})
	w.ProcessOnce(ctx)
	view, _ := svc.readPurchaseView(purchaseID)
	if view.InvoiceStatus != HPInvoicePending {
		t.Error("wrong sender must not trigger detection")
	}

	// Underpayment — no detection.
	chain.setTxs("payaddr_checks", []V2TxResult{
		{Txid: "under_tx", Confirmations: 0, AmountAtomic: 50000, InputAddresses: []string{btcWallet}},
	})
	w.ProcessOnce(ctx)
	view, _ = svc.readPurchaseView(purchaseID)
	if view.InvoiceStatus != HPInvoicePending {
		t.Error("underpayment must not trigger detection")
	}

	// Split txs (each under required amount) — no detection.
	chain.setTxs("payaddr_checks", []V2TxResult{
		{Txid: "split1", Confirmations: 0, AmountAtomic: 60000, InputAddresses: []string{btcWallet}},
		{Txid: "split2", Confirmations: 0, AmountAtomic: 60000, InputAddresses: []string{btcWallet}},
	})
	w.ProcessOnce(ctx)
	view, _ = svc.readPurchaseView(purchaseID)
	if view.InvoiceStatus != HPInvoicePending {
		t.Error("split underpayment must not trigger detection")
	}

	// Correct amount + correct sender → detection.
	chain.setTxs("payaddr_checks", []V2TxResult{
		{Txid: "correct_tx", Confirmations: 0, AmountAtomic: 100000, InputAddresses: []string{btcWallet}},
	})
	w.ProcessOnce(ctx)
	view, _ = svc.readPurchaseView(purchaseID)
	if view.InvoiceStatus != HPInvoiceDetected {
		t.Errorf("correct tx: expected payment_detected, got %q", view.InvoiceStatus)
	}
}

// Test 16: Provider outage preserves state; duplicate cycles → one ready transition.
func TestHelperWatcher_ProviderOutageAndDuplicateCycles(t *testing.T) {
	w, svc, chain, bal := newTestHelperWatcher(t)
	bal.result = 1500.0

	btcWallet := testBTCBech32Addr
	purchaseID, _ := fullPurchaseSetup(t, svc, "BTC", btcWallet, "payaddr_outage", "US")
	ctx := context.Background()

	// Provider outage.
	chain.err = errFakeProvider
	result := w.ProcessOnce(ctx)
	if !result.HasProviderError {
		t.Error("expected HasProviderError=true on provider failure")
	}
	view, _ := svc.readPurchaseView(purchaseID)
	if view.InvoiceStatus != HPInvoicePending {
		t.Errorf("state changed during outage: %q", view.InvoiceStatus)
	}

	// Recovery.
	chain.err = nil
	chain.setTxs("payaddr_outage", []V2TxResult{
		{Txid: "outage_tx", Confirmations: 1, AmountAtomic: 100000, InputAddresses: []string{btcWallet}},
	})

	// Multiple cycles — only one ready transition.
	for i := 0; i < 3; i++ {
		w.ProcessOnce(ctx)
	}

	view, _ = svc.readPurchaseView(purchaseID)
	if view.State != HPStateContactReady {
		t.Errorf("after recovery: state=%q, want contact_ready", view.State)
	}

	var pc int
	svc.db.QueryRow(`SELECT purchase_count FROM v2_helper_profiles WHERE id=?`, view.ProfileID).Scan(&pc) //nolint:errcheck
	if pc != 1 {
		t.Errorf("purchase_count=%d, want 1", pc)
	}
}

// Test 17: Payment detected while listing visible; listing hidden later → confirm still works.
func TestHelperWatcher_LateConfirmAfterListingHidden(t *testing.T) {
	now := time.Now()
	svc, db := newTestHelperService(t)

	btcWallet := testBTCBech32Addr
	listingID := mustCreateVisibleListing(t, db, "US")
	normalized, currency, _ := validateAndNormalizeAddress(btcWallet)

	draft := HelperInvoiceDraft{PaymentAddress: "payaddr_late17", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, view, _ := svc.CreatePurchase(newID(), listingID, currency, normalized, draft)

	svc.RecordHelperDetection(view.PurchaseID, "txid_17", []string{btcWallet}, 100000, now) //nolint:errcheck

	// Listing becomes hidden.
	db.Exec(`UPDATE v2_listings SET state='hidden', visible_until=NULL, updated_at=? WHERE id=?`, now.Unix(), listingID) //nolint:errcheck

	_, err := svc.ConfirmHelperPayment(view.PurchaseID, now)
	if err != nil {
		t.Fatalf("ConfirmHelperPayment after listing hidden: %v", err)
	}

	_, err = svc.RecordHelperPostPaymentBalance(view.PurchaseID, 1500.0)
	if err != nil {
		t.Fatalf("RecordHelperPostPaymentBalance: %v", err)
	}

	v, _ := svc.readPurchaseView(view.PurchaseID)
	if v.State != HPStateContactReady {
		t.Errorf("state=%q, want contact_ready", v.State)
	}
}

// Test 18: Invoice creation after listing hidden/finished → ErrNotFound.
func TestHelperWatcher_NoInvoiceAfterListingGone(t *testing.T) {
	svc, db := newTestHelperService(t)

	addr := testBTCBech32Addr
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	listingID := mustCreateVisibleListing(t, db, "US")
	db.Exec(`UPDATE v2_listings SET state='hidden', visible_until=NULL WHERE id=?`, listingID) //nolint:errcheck

	draft := HelperInvoiceDraft{PaymentAddress: "payaddr18", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, _, err := svc.CreatePurchase(newID(), listingID, currency, normalized, draft)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("hidden listing: expected ErrNotFound, got %v", err)
	}

	listingID2 := mustCreateVisibleListing(t, db, "US")
	db.Exec(`UPDATE v2_listings SET state='finished', visible_until=NULL WHERE id=?`, listingID2) //nolint:errcheck

	_, _, err = svc.CreatePurchase(newID(), listingID2, currency, normalized, draft)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("finished listing: expected ErrNotFound, got %v", err)
	}
}

// Watcher Run cancels cleanly.
func TestHelperWatcher_RunCancellation(t *testing.T) {
	svc, _ := newTestHelperService(t)
	chain := newFakeChainClient()
	bal := &fakeBalanceReader{result: 1500.0}
	sleep := newBlockingSleep()

	w, err := NewHelperPurchaseWatcher(
		svc,
		map[string]V2ChainClient{"BTC": chain},
		bal,
		time.Now,
		sleep.sleep,
		0,
	)
	if err != nil {
		t.Fatalf("NewHelperPurchaseWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.Run(ctx)
	}()

	select {
	case <-sleep.blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not start sleeping within 2s")
	}

	cancel()
	wg.Wait()
}
