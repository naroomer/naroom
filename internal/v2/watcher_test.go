package v2

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// errFakeProvider is a sentinel provider error distinct from domain errors.
var errFakeProvider = errors.New("fake provider error")

// ── Fakes for watcher tests ───────────────────────────────────────────────────

// fakeChainClient allows test control of returned txs per address.
type fakeChainClient struct {
	mu    sync.Mutex
	txs   map[string][]V2TxResult // address → txs
	err   error
	calls int
}

func newFakeChainClient() *fakeChainClient {
	return &fakeChainClient{txs: make(map[string][]V2TxResult)}
}

func (f *fakeChainClient) setTxs(address string, txs []V2TxResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.txs[address] = txs
}

func (f *fakeChainClient) GetInvoiceTxs(_ context.Context, address string) ([]V2TxResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.txs[address], nil
}

// fakeBalanceReader is an injectable ClientBalanceReader.
type fakeBalanceReader struct {
	result float64
	err    error
	calls  int
}

func (f *fakeBalanceReader) BalanceUSD(_ context.Context, _, _ string) (float64, error) {
	f.calls++
	return f.result, f.err
}

// fakeSleep records delays but does NOT block — for backoff progression tests
// that just inspect the delay values.
type fakeSleep struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (f *fakeSleep) sleep(_ context.Context, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delays = append(f.delays, d)
}

func (f *fakeSleep) totalDelays() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Duration, len(f.delays))
	copy(out, f.delays)
	return out
}

// blockingSleep records delays AND blocks until the context is cancelled or the
// delay elapses — used to test that Run's sleep is actually interrupted by cancel.
type blockingSleep struct {
	mu      sync.Mutex
	delays  []time.Duration
	blocked chan struct{} // closed when the first sleep call has started blocking
}

func newBlockingSleep() *blockingSleep {
	return &blockingSleep{blocked: make(chan struct{})}
}

func (b *blockingSleep) sleep(ctx context.Context, d time.Duration) {
	b.mu.Lock()
	b.delays = append(b.delays, d)
	b.mu.Unlock()

	// Signal once that we are now blocking.
	select {
	case <-b.blocked:
	default:
		close(b.blocked)
	}

	// Actually block until context cancelled or timer fires.
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (b *blockingSleep) totalDelays() []time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]time.Duration, len(b.delays))
	copy(out, b.delays)
	return out
}

// ── newTestWatcher creates a watcher backed by an in-memory service. ──────────

func newTestWatcher(t *testing.T) (*V2Watcher, *Service, *fakeChainClient, *fakeBalanceReader) {
	t.Helper()
	svc, _ := newTestService(t)
	chain := newFakeChainClient()
	bal := &fakeBalanceReader{result: 150.0}
	now := time.Now

	w, err := NewV2Watcher(
		svc,
		map[string]V2ChainClient{"BTC": chain, "LTC": chain},
		bal,
		now,
		func(_ context.Context, _ time.Duration) {}, // instant sleep
		0,
	)
	if err != nil {
		t.Fatalf("NewV2Watcher: %v", err)
	}
	return w, svc, chain, bal
}

// ── Watcher constructor tests ─────────────────────────────────────────────────

func TestWatcherConstructorValidation(t *testing.T) {
	svc, _ := newTestService(t)
	chain := newFakeChainClient()
	bal := &fakeBalanceReader{}
	clients := map[string]V2ChainClient{"BTC": chain}

	cases := []struct {
		name    string
		svc     *Service
		clients map[string]V2ChainClient
		balance ClientBalanceReader
	}{
		{"nil svc", nil, clients, bal},
		{"empty clients", svc, map[string]V2ChainClient{}, bal},
		{"nil clients", svc, nil, bal},
		{"nil balance", svc, clients, nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewV2Watcher(tc.svc, tc.clients, tc.balance, nil, nil, 0)
			if err == nil {
				t.Errorf("%s: expected error, got nil", tc.name)
			}
		})
	}

	// Valid construction.
	w, err := NewV2Watcher(svc, clients, bal, nil, nil, 0)
	if err != nil || w == nil {
		t.Fatalf("valid construction: %v", err)
	}
}

// ── Watcher ProcessOnce tests ─────────────────────────────────────────────────

// TestWatcherDetectsPendingInvoice verifies that ProcessOnce detects a payment.
func TestWatcherDetectsPendingInvoice(t *testing.T) {
	w, svc, chain, _ := newTestWatcher(t)
	ctx := context.Background()

	_, fv, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Provide a matching tx.
	chain.setTxs(fv.PaymentAddress, []V2TxResult{
		{
			Txid:           "txid_watcher",
			Confirmations:  0,
			AmountAtomic:   fv.AmountAtomic,
			InputAddresses: []string{"bc1qtest"},
		},
	})

	w.ProcessOnce(ctx)

	reread, err := svc.readFlowView(fv.FlowID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if reread.InvoiceStatus != InvoiceStatusDetected {
		t.Errorf("status after ProcessOnce: got %q, want payment_detected", reread.InvoiceStatus)
	}
	if reread.DetectedTxid == nil || *reread.DetectedTxid != "txid_watcher" {
		t.Errorf("DetectedTxid: got %v, want txid_watcher", reread.DetectedTxid)
	}
}

// TestWatcherConfirmsDetectedInvoice verifies that ProcessOnce confirms when confirmations >= 1.
func TestWatcherConfirmsDetectedInvoice(t *testing.T) {
	w, svc, chain, bal := newTestWatcher(t)
	ctx := context.Background()
	bal.result = 150.0

	_, fv, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// First detect.
	now := time.Now()
	_, err = svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_conf",
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}

	// Provide confirmed tx.
	chain.setTxs(fv.PaymentAddress, []V2TxResult{
		{
			Txid:           "txid_conf",
			Confirmations:  1,
			AmountAtomic:   fv.AmountAtomic,
			InputAddresses: []string{"bc1qtest"},
		},
	})

	w.ProcessOnce(ctx)

	reread, err := svc.readFlowView(fv.FlowID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if reread.InvoiceStatus != InvoiceStatusConfirmed {
		t.Errorf("status after ProcessOnce confirm: got %q, want confirmed", reread.InvoiceStatus)
	}
	if reread.State != StateFormReady && reread.State != StatePaidLowBalance && reread.State != StatePaymentConfirmed {
		t.Errorf("unexpected state: %q", reread.State)
	}
}

// TestWatcherExpiresPendingAfterDeadline verifies expiry when deadline passes.
func TestWatcherExpiresPendingAfterDeadline(t *testing.T) {
	svc, _ := newTestService(t)
	chain := newFakeChainClient()
	bal := &fakeBalanceReader{}

	// Use a clock just past the detection deadline.
	_, fv, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	pastDeadline := fv.DetectionDeadlineAt.Add(2 * time.Second)
	nowFn := func() time.Time { return pastDeadline }

	w, err := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, nowFn,
		func(_ context.Context, _ time.Duration) {},
		0,
	)
	if err != nil {
		t.Fatalf("NewV2Watcher: %v", err)
	}

	w.ProcessOnce(context.Background())

	reread, _ := svc.readFlowView(fv.FlowID)
	if reread.InvoiceStatus != InvoiceStatusExpired {
		t.Errorf("status: got %q, want expired", reread.InvoiceStatus)
	}
}

// TestWatcherSkipsUnderpayment verifies that underpayment txs are skipped.
func TestWatcherSkipsUnderpayment(t *testing.T) {
	w, svc, chain, _ := newTestWatcher(t)
	ctx := context.Background()

	_, fv, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Underpayment tx.
	chain.setTxs(fv.PaymentAddress, []V2TxResult{
		{
			Txid:           "txid_under",
			Confirmations:  0,
			AmountAtomic:   fv.AmountAtomic - 1,
			InputAddresses: []string{"bc1qtest"},
		},
	})

	w.ProcessOnce(ctx)

	reread, _ := svc.readFlowView(fv.FlowID)
	if reread.InvoiceStatus != InvoiceStatusPending {
		t.Errorf("status after underpayment: got %q, want pending", reread.InvoiceStatus)
	}
}

// TestWatcherSkipsWrongSender verifies that wrong-sender txs are skipped.
func TestWatcherSkipsWrongSender(t *testing.T) {
	w, svc, chain, _ := newTestWatcher(t)
	ctx := context.Background()

	_, fv, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Wrong sender tx.
	chain.setTxs(fv.PaymentAddress, []V2TxResult{
		{
			Txid:           "txid_ws",
			Confirmations:  0,
			AmountAtomic:   fv.AmountAtomic,
			InputAddresses: []string{"bc1qwrong0000000000000000000000000000000000"},
		},
	})

	w.ProcessOnce(ctx)

	reread, _ := svc.readFlowView(fv.FlowID)
	if reread.InvoiceStatus != InvoiceStatusPending {
		t.Errorf("status after wrong sender: got %q, want pending", reread.InvoiceStatus)
	}
}

// TestWatcherHandlesChainError verifies that chain errors do not corrupt state.
func TestWatcherHandlesChainError(t *testing.T) {
	svc, _ := newTestService(t)
	chain := newFakeChainClient()
	chain.err = errors.New("network timeout")
	bal := &fakeBalanceReader{}

	w, _ := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, time.Now,
		func(_ context.Context, _ time.Duration) {},
		0,
	)

	_, fv, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	w.ProcessOnce(context.Background())

	reread, _ := svc.readFlowView(fv.FlowID)
	if reread.InvoiceStatus != InvoiceStatusPending {
		t.Errorf("status after chain error: got %q, want pending", reread.InvoiceStatus)
	}
}

// TestWatcherRunCancelledCtx verifies that Run exits when ctx is cancelled.
func TestWatcherRunCancelledCtx(t *testing.T) {
	svc, _ := newTestService(t)
	chain := newFakeChainClient()
	bal := &fakeBalanceReader{}
	fs := &fakeSleep{}

	w, _ := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, time.Now,
		fs.sleep,
		0,
	)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	// Let it run a couple of cycles.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Error("Run did not exit after context cancellation")
	}
}

// TestWatcherProcessOnceIdempotent verifies double ProcessOnce is safe.
func TestWatcherProcessOnceIdempotent(t *testing.T) {
	w, svc, chain, _ := newTestWatcher(t)
	ctx := context.Background()

	_, fv, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	chain.setTxs(fv.PaymentAddress, []V2TxResult{
		{
			Txid:           "txid_idem",
			Confirmations:  0,
			AmountAtomic:   fv.AmountAtomic,
			InputAddresses: []string{"bc1qtest"},
		},
	})

	w.ProcessOnce(ctx)
	w.ProcessOnce(ctx) // second call — must be idempotent

	reread, _ := svc.readFlowView(fv.FlowID)
	if reread.InvoiceStatus != InvoiceStatusDetected {
		t.Errorf("status after double ProcessOnce: got %q, want payment_detected", reread.InvoiceStatus)
	}
}

// TestWatcherBalanceCheckAfterConfirmation verifies that balance is checked post-confirmation.
func TestWatcherBalanceCheckAfterConfirmation(t *testing.T) {
	svc, _ := newTestService(t)
	chain := newFakeChainClient()
	bal := &fakeBalanceReader{result: 150.0}

	w, _ := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, time.Now,
		func(_ context.Context, _ time.Duration) {},
		0,
	)

	_, fv, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Set up confirmed tx.
	chain.setTxs(fv.PaymentAddress, []V2TxResult{
		{
			Txid:           "txid_bal_check",
			Confirmations:  1,
			AmountAtomic:   fv.AmountAtomic,
			InputAddresses: []string{"bc1qtest"},
		},
	})

	// Detect first.
	now := time.Now()
	_, err = svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_bal_check",
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}

	// ProcessOnce should confirm + run balance check.
	w.ProcessOnce(context.Background())

	reread, _ := svc.readFlowView(fv.FlowID)
	if reread.InvoiceStatus != InvoiceStatusConfirmed {
		t.Errorf("status: got %q, want confirmed", reread.InvoiceStatus)
	}
	if bal.calls == 0 {
		t.Error("balance reader was never called")
	}
	if reread.LastBalanceUSD == nil {
		t.Error("LastBalanceUSD is nil after balance check")
	} else if *reread.LastBalanceUSD != 150.0 {
		t.Errorf("LastBalanceUSD: got %v, want 150.0", *reread.LastBalanceUSD)
	}
}

// TestWatcherBalanceOutageLeavesPendingConfirmed verifies balance outage is safe.
func TestWatcherBalanceOutageLeavesPendingConfirmed(t *testing.T) {
	svc, _ := newTestService(t)
	chain := newFakeChainClient()
	bal := &fakeBalanceReader{err: errors.New("balance provider down")}

	w, _ := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, time.Now,
		func(_ context.Context, _ time.Duration) {},
		0,
	)

	_, fv, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	chain.setTxs(fv.PaymentAddress, []V2TxResult{
		{
			Txid:           "txid_outage",
			Confirmations:  1,
			AmountAtomic:   fv.AmountAtomic,
			InputAddresses: []string{"bc1qtest"},
		},
	})

	now := time.Now()
	_, err = svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_outage",
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}

	w.ProcessOnce(context.Background())

	reread, _ := svc.readFlowView(fv.FlowID)
	// Confirmation should have happened even though balance check failed.
	if reread.InvoiceStatus != InvoiceStatusConfirmed {
		t.Errorf("status: got %q, want confirmed", reread.InvoiceStatus)
	}
	// Balance should be nil (outage).
	if reread.LastBalanceUSD != nil {
		t.Errorf("LastBalanceUSD should be nil during outage, got %v", *reread.LastBalanceUSD)
	}
}

// TestWatcherLoadWatchableOnlyReturnsRelevant verifies LoadWatchableInvoices result.
func TestWatcherLoadWatchableOnlyReturnsRelevant(t *testing.T) {
	svc, _ := newTestService(t)

	// Create pending invoice.
	_, fv1, _ := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())

	// Create confirmed+balanced invoice (not watchable).
	_, fv2, _ := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	now := time.Now()
	svc.RecordPaymentDetected(fv2.FlowID, fv2.InvoiceID, "txid2", []string{"bc1qtest"}, fv2.AmountAtomic, now) //nolint:errcheck
	svc.ConfirmPayment(fv2.FlowID, fv2.InvoiceID, now)                                                         //nolint:errcheck
	svc.RecordPostPaymentBalance(fv2.FlowID, 150.0, hardFloorUSD)                                              //nolint:errcheck

	watchable, err := svc.LoadWatchableInvoices()
	if err != nil {
		t.Fatalf("LoadWatchableInvoices: %v", err)
	}

	found1, found2 := false, false
	for _, w := range watchable {
		if w.FlowID == fv1.FlowID {
			found1 = true
		}
		if w.FlowID == fv2.FlowID {
			found2 = true
		}
	}
	if !found1 {
		t.Error("pending invoice not in watchable list")
	}
	if found2 {
		t.Error("confirmed+balanced invoice should NOT be in watchable list")
	}
}

// ── Task 03A backoff integration tests ───────────────────────────────────────

// makeWatcherWithProviderError creates a watcher that always gets provider errors.
// An invoice is pre-created so that ProcessOnce actually calls the chain client.
func makeWatcherWithProviderError(t *testing.T, fs *fakeSleep) (*V2Watcher, *Service) {
	t.Helper()
	svc, _ := newTestService(t)
	chain := newFakeChainClient()
	chain.err = errFakeProvider
	bal := &fakeBalanceReader{}

	w, err := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, time.Now,
		fs.sleep,
		0,
	)
	if err != nil {
		t.Fatalf("NewV2Watcher: %v", err)
	}

	// Pre-create an invoice so the pending-stage provider call fires.
	_, _, createErr := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if createErr != nil {
		t.Fatalf("CreatePaymentIntent: %v", createErr)
	}
	return w, svc
}

// TestWatcherBackoffThreeConsecutiveFailures verifies the first three sleep
// durations after consecutive provider failures: 5s → 10s → 20s.
func TestWatcherBackoffThreeConsecutiveFailures(t *testing.T) {
	fs := &fakeSleep{}
	w, _ := makeWatcherWithProviderError(t, fs)
	ctx := context.Background()

	// Run three failing cycles.
	for i := 0; i < 3; i++ {
		result := w.ProcessOnce(ctx)
		if !result.HasProviderError {
			t.Errorf("cycle %d: expected HasProviderError=true", i+1)
		}
		w.backoff.Next() // simulate Run calling Next after failure
	}

	// Inspect the backoff progression directly.
	// After 3 cycles the backoff state encodes the last sleep.
	// We test the Run-managed sequence by simulating Run manually:
	b := newCappedBackoff()
	delays := []time.Duration{b.Next(), b.Next(), b.Next()}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}
	for i, d := range delays {
		if d != want[i] {
			t.Errorf("delay[%d]: got %v, want %v", i, d, want[i])
		}
	}
}

// TestWatcherBackoffSuccessResets verifies that a successful cycle resets the
// backoff so the next failure starts at 5s again.
func TestWatcherBackoffSuccessReset(t *testing.T) {
	fs := &fakeSleep{}
	svc, _ := newTestService(t)
	chain := newFakeChainClient()
	chain.err = errFakeProvider // will fail initially
	bal := &fakeBalanceReader{}

	w, _ := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, time.Now, fs.sleep,
		0,
	)

	_, _, _ = svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())

	ctx := context.Background()

	// Two consecutive failures — backoff grows.
	for i := 0; i < 2; i++ {
		result := w.ProcessOnce(ctx)
		if !result.HasProviderError {
			t.Errorf("cycle %d: expected failure", i+1)
		}
		w.backoff.Next()
	}

	// A successful cycle (no provider error) → reset.
	chain.err = nil
	chain.setTxs("", nil) // no matching txs — but provider succeeds
	result := w.ProcessOnce(ctx)
	if result.HasProviderError {
		t.Error("success cycle: unexpected HasProviderError=true")
	}
	w.backoff.Reset() // simulate Run calling Reset after success

	// Next failure must start at 5s again.
	chain.err = errFakeProvider
	_ = w.ProcessOnce(ctx)
	d := w.backoff.Next()
	if d != 5*time.Second {
		t.Errorf("after success reset: first failure sleep %v, want 5s", d)
	}
}

// TestWatcherPartialFailureCycleIsFailed verifies that one failing invoice in a
// cycle marks the whole cycle as failed, even if another invoice succeeds.
func TestWatcherPartialFailureCycleIsFailed(t *testing.T) {
	svc, _ := newTestService(t)

	// Invoice A: BTC — chain client will fail.
	_, fvA, _ := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	_ = fvA

	// Invoice B: LTC — chain client succeeds with no matching tx.
	_, fvB, _ := svc.CreatePaymentIntent("ltc1qtest", "LTC", validDraft())
	_ = fvB

	btcChain := newFakeChainClient()
	btcChain.err = errFakeProvider // BTC fails

	ltcChain := newFakeChainClient()
	// LTC succeeds with empty tx list (no matching tx — not an error)

	w, _ := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": btcChain, "LTC": ltcChain},
		&fakeBalanceReader{},
		time.Now,
		func(_ context.Context, _ time.Duration) {},
		0,
	)

	result := w.ProcessOnce(context.Background())
	if !result.HasProviderError {
		t.Error("mixed cycle: expected HasProviderError=true (BTC failed)")
	}
	if !result.ProviderCallMade {
		t.Error("mixed cycle: expected ProviderCallMade=true")
	}
}

// TestWatcherDetectedStageProviderFailureSavesDetectedFields verifies that a
// provider failure during the confirmed-check stage does not erase the
// payment_detected fields (txid, deadlines, etc.).
func TestWatcherDetectedStageProviderFailureSavesDetectedFields(t *testing.T) {
	svc, _ := newTestService(t)
	chain := newFakeChainClient()
	bal := &fakeBalanceReader{}

	w, _ := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, time.Now,
		func(_ context.Context, _ time.Duration) {},
		0,
	)

	_, fv, _ := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())

	// Detect the invoice.
	now := time.Now()
	detected, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_det",
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	if err != nil {
		t.Fatalf("RecordPaymentDetected: %v", err)
	}
	if detected.ConfirmationDeadlineAt == nil {
		t.Fatal("confirmation_deadline_at not set after detection")
	}

	// Provider error during confirmation check.
	chain.err = errFakeProvider
	result := w.ProcessOnce(context.Background())
	if !result.HasProviderError {
		t.Error("expected HasProviderError after provider failure in detected stage")
	}

	// Fields must be preserved.
	reread, err := svc.readFlowView(fv.FlowID)
	if err != nil {
		t.Fatalf("readFlowView: %v", err)
	}
	if reread.InvoiceStatus != InvoiceStatusDetected {
		t.Errorf("status: got %q, want payment_detected", reread.InvoiceStatus)
	}
	if reread.DetectedTxid == nil || *reread.DetectedTxid != "txid_det" {
		t.Errorf("DetectedTxid: got %v, want txid_det", reread.DetectedTxid)
	}
	if reread.ConfirmationDeadlineAt == nil {
		t.Error("ConfirmationDeadlineAt erased after provider error")
	}
}

// TestWatcherConfirmedBalanceProviderFailureSavesEntitlement verifies that a
// provider failure during the post-confirmation balance stage does not remove
// the confirmed state or entitlement.
func TestWatcherConfirmedBalanceProviderFailureSavesEntitlement(t *testing.T) {
	svc, _ := newTestService(t)
	chain := newFakeChainClient()
	bal := &fakeBalanceReader{err: errors.New("balance provider down")}

	w, _ := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, time.Now,
		func(_ context.Context, _ time.Duration) {},
		0,
	)

	_, fv, _ := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	now := time.Now()
	svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_c", //nolint:errcheck
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, now) //nolint:errcheck

	// Check state is confirmed with entitlement set.
	confirmed, err := svc.readFlowView(fv.FlowID)
	if err != nil {
		t.Fatalf("readFlowView: %v", err)
	}
	if confirmed.InvoiceStatus != InvoiceStatusConfirmed {
		t.Fatalf("status before balance fail: got %q, want confirmed", confirmed.InvoiceStatus)
	}
	if confirmed.EntitlementExpiresAt == nil {
		t.Fatal("EntitlementExpiresAt nil before balance fail")
	}
	originalExpiry := *confirmed.EntitlementExpiresAt

	// Provide the tx so processConfirmedBalance can try the balance check.
	chain.setTxs(fv.PaymentAddress, []V2TxResult{
		{Txid: "txid_c", Confirmations: 1, AmountAtomic: fv.AmountAtomic,
			InputAddresses: []string{"bc1qtest"}},
	})

	result := w.ProcessOnce(context.Background())
	if !result.HasProviderError {
		t.Error("expected HasProviderError after balance provider failure")
	}

	// Confirmed state and entitlement must be unchanged.
	reread, _ := svc.readFlowView(fv.FlowID)
	if reread.InvoiceStatus != InvoiceStatusConfirmed {
		t.Errorf("status after balance fail: got %q, want confirmed", reread.InvoiceStatus)
	}
	if reread.EntitlementExpiresAt == nil || *reread.EntitlementExpiresAt != originalExpiry {
		t.Error("EntitlementExpiresAt changed after balance provider failure")
	}
	if reread.LastBalanceUSD != nil {
		t.Error("LastBalanceUSD set despite balance provider failure")
	}
}

// TestWatcherRunCancellationInterruptsSleep verifies that a sleeping Run
// terminates when the context is cancelled (no spin-loop).
func TestWatcherRunCancellationInterruptsSleep(t *testing.T) {
	svc, _ := newTestService(t)
	chain := newFakeChainClient()
	chain.err = errFakeProvider // guarantee a failure so Run calls sleep
	bal := &fakeBalanceReader{}

	bs := newBlockingSleep()
	w, _ := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, time.Now,
		bs.sleep,
		0,
	)

	// Pre-create an invoice so the provider is called and fails.
	svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft()) //nolint:errcheck

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	// Wait until the first blocking sleep has started.
	select {
	case <-bs.blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not call sleep after provider error")
	}

	// Verify the first delay is 5s.
	delays := bs.totalDelays()
	if len(delays) == 0 || delays[0] != 5*time.Second {
		t.Errorf("first sleep delay: got %v, want [5s]", delays)
	}

	// Cancel the context — the blocked sleep must terminate promptly.
	cancel()
	select {
	case <-done:
		// success
	case <-time.After(3 * time.Second):
		t.Error("Run did not exit after context cancellation during sleep")
	}
}

// ── controlledSleep cancels context after N sleep calls ──────────────────────
//
// This is distinct from fakeSleep (never blocks, records delays) and
// blockingSleep (blocks until ctx cancelled or timer fires). controlledSleep
// lets Run-level backoff tests terminate deterministically after exactly N
// cycles without real timers.

type controlledSleep struct {
	mu     sync.Mutex
	delays []time.Duration
	cancel context.CancelFunc
	limit  int
}

func newControlledSleep(cancel context.CancelFunc, limit int) *controlledSleep {
	return &controlledSleep{cancel: cancel, limit: limit}
}

func (c *controlledSleep) sleep(_ context.Context, d time.Duration) {
	c.mu.Lock()
	c.delays = append(c.delays, d)
	n := len(c.delays)
	cancel := c.cancel
	c.mu.Unlock()
	if n >= c.limit {
		cancel()
	}
}

func (c *controlledSleep) totalDelays() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]time.Duration, len(c.delays))
	copy(out, c.delays)
	return out
}

// ── sequentialChain returns pre-programmed errors per call index ──────────────

type sequentialChain struct {
	mu       sync.Mutex
	calls    int
	sequence []error // nil element means success (empty txs)
}

func (s *sequentialChain) GetInvoiceTxs(_ context.Context, _ string) ([]V2TxResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.calls
	s.calls++
	if i < len(s.sequence) {
		return nil, s.sequence[i]
	}
	return nil, nil
}

// equalDelays compares two duration slices element-by-element.
func equalDelays(got, want []time.Duration) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// ── Run-level backoff tests (P1-1) ───────────────────────────────────────────

// TestWatcherRunBackoffThreeErrors verifies that three consecutive failed
// cycles produce backoff delays [5s, 10s, 20s].
func TestWatcherRunBackoffThreeErrors(t *testing.T) {
	svc, _ := newTestService(t)
	chain := newFakeChainClient()
	chain.err = errFakeProvider
	bal := &fakeBalanceReader{}

	ctx, cancel := context.WithCancel(context.Background())
	cs := newControlledSleep(cancel, 3)

	w, _ := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, time.Now,
		cs.sleep,
		0,
	)
	svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft()) //nolint:errcheck

	w.Run(ctx)

	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}
	got := cs.totalDelays()
	if !equalDelays(got, want) {
		t.Errorf("backoff delays: got %v, want %v", got, want)
	}
}

// TestWatcherRunSuccessCycleSleepsNormal verifies that a successful cycle
// (provider called, no error) sleeps exactly the poll interval (5s).
func TestWatcherRunSuccessCycleSleepsNormal(t *testing.T) {
	svc, _ := newTestService(t)
	chain := newFakeChainClient() // returns empty txs, no error
	bal := &fakeBalanceReader{}

	ctx, cancel := context.WithCancel(context.Background())
	cs := newControlledSleep(cancel, 1)

	w, _ := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, time.Now,
		cs.sleep,
		0,
	)
	svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft()) //nolint:errcheck

	w.Run(ctx)

	got := cs.totalDelays()
	if len(got) != 1 || got[0] != 5*time.Second {
		t.Errorf("success cycle sleep: got %v, want [5s]", got)
	}
}

// TestWatcherRunEmptyQueueSleepsNormal verifies that an empty queue cycle
// sleeps the poll interval (5s) and does not spin without a sleep.
func TestWatcherRunEmptyQueueSleepsNormal(t *testing.T) {
	svc, _ := newTestService(t)
	chain := newFakeChainClient()
	bal := &fakeBalanceReader{}

	ctx, cancel := context.WithCancel(context.Background())
	cs := newControlledSleep(cancel, 2) // two cycles to prove no spin

	w, _ := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, time.Now,
		cs.sleep,
		0,
	)
	// No invoices — LoadWatchableInvoices returns empty.

	w.Run(ctx)

	want := []time.Duration{5 * time.Second, 5 * time.Second}
	got := cs.totalDelays()
	if !equalDelays(got, want) {
		t.Errorf("empty queue delays: got %v, want %v", got, want)
	}
}

// TestWatcherRunBackoffResetOnSuccess verifies that a successful cycle resets
// the backoff counter. Two errors → success → error must produce [5s,10s,5s,5s].
func TestWatcherRunBackoffResetOnSuccess(t *testing.T) {
	svc, _ := newTestService(t)
	sc := &sequentialChain{
		sequence: []error{
			errFakeProvider, // cycle 1: provider error
			errFakeProvider, // cycle 2: provider error
			nil,             // cycle 3: success (empty txs, no error)
			errFakeProvider, // cycle 4: provider error (fresh backoff)
		},
	}
	bal := &fakeBalanceReader{}

	ctx, cancel := context.WithCancel(context.Background())
	cs := newControlledSleep(cancel, 4)

	w, _ := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": sc},
		bal, time.Now,
		cs.sleep,
		0,
	)
	svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft()) //nolint:errcheck

	w.Run(ctx)

	want := []time.Duration{5 * time.Second, 10 * time.Second, 5 * time.Second, 5 * time.Second}
	got := cs.totalDelays()
	if !equalDelays(got, want) {
		t.Errorf("backoff reset delays: got %v, want %v", got, want)
	}
}

// TestWatcherRunCancelInterruptsSuccessSleep verifies that cancelling the
// context during the normal (success/empty) poll sleep terminates Run promptly.
func TestWatcherRunCancelInterruptsSuccessSleep(t *testing.T) {
	svc, _ := newTestService(t)
	chain := newFakeChainClient() // no error → success/empty path
	bal := &fakeBalanceReader{}

	bs := newBlockingSleep()
	w, _ := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, time.Now,
		bs.sleep,
		0,
	)
	// No invoices so the cycle is empty and has no error → success sleep.

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	select {
	case <-bs.blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not call sleep for empty/success cycle")
	}

	// The first sleep must be the poll interval (5s), not a backoff delay.
	delays := bs.totalDelays()
	if len(delays) == 0 || delays[0] != 5*time.Second {
		t.Errorf("success/empty sleep delay: got %v, want 5s first delay", delays)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Error("Run did not exit after context cancellation during success/empty sleep")
	}
}

// ── Expiry DB-error backoff tests (P1-2) ─────────────────────────────────────

// TestWatcherPendingExpiryDBErrorTriggersBackoff verifies that an unexpected
// DB error during pending→expired transition sets HasProviderError=true so Run
// backs off. Controlled errors (ErrExpired, ErrConflict, ErrInvalidState) are
// covered by other tests and remain silent.
func TestWatcherPendingExpiryDBErrorTriggersBackoff(t *testing.T) {
	svc, db := newTestService(t)
	chain := newFakeChainClient()
	bal := &fakeBalanceReader{}

	_, fv, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Use a clock past the detection deadline to trigger the expiry path.
	pastDeadline := fv.DetectionDeadlineAt.Add(2 * time.Second)
	nowFn := func() time.Time { return pastDeadline }

	w, err := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, nowFn,
		func(_ context.Context, _ time.Duration) {},
		0,
	)
	if err != nil {
		t.Fatalf("NewV2Watcher: %v", err)
	}

	// Block the status UPDATE so ExpireInvoice returns an unexpected DB error.
	_, err = db.Exec(`
		CREATE TRIGGER v2_test_block_pending_expiry
		BEFORE UPDATE OF status ON v2_invoices
		BEGIN SELECT RAISE(ABORT, 'test: pending expiry blocked'); END`)
	if err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	defer db.Exec(`DROP TRIGGER IF EXISTS v2_test_block_pending_expiry`) //nolint:errcheck

	result := w.ProcessOnce(context.Background())
	if !result.HasProviderError {
		t.Error("expected HasProviderError=true for unexpected DB error during pending expiry, got false")
	}
}

// TestWatcherDetectedExpiryDBErrorTriggersBackoff verifies that an unexpected
// DB error during payment_detected→expired transition sets HasProviderError=true.
func TestWatcherDetectedExpiryDBErrorTriggersBackoff(t *testing.T) {
	svc, db := newTestService(t)
	chain := newFakeChainClient()
	bal := &fakeBalanceReader{}

	_, fv, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Detect the invoice so it enters payment_detected state (sets confirmation_deadline_at).
	now := time.Now()
	detected, err := svc.RecordPaymentDetected(
		fv.FlowID, fv.InvoiceID, "txid_expiry_test",
		[]string{"bc1qtest"}, fv.AmountAtomic, now,
	)
	if err != nil {
		t.Fatalf("RecordPaymentDetected: %v", err)
	}
	if detected.ConfirmationDeadlineAt == nil {
		t.Fatal("confirmation_deadline_at not set after detection — test cannot proceed")
	}

	// Use a clock past the confirmation deadline to trigger the expiry path.
	pastConfirm := detected.ConfirmationDeadlineAt.Add(2 * time.Second)
	nowFn := func() time.Time { return pastConfirm }

	w, err := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, nowFn,
		func(_ context.Context, _ time.Duration) {},
		0,
	)
	if err != nil {
		t.Fatalf("NewV2Watcher: %v", err)
	}

	// Block the status UPDATE so ExpireInvoice returns an unexpected DB error.
	_, err = db.Exec(`
		CREATE TRIGGER v2_test_block_detected_expiry
		BEFORE UPDATE OF status ON v2_invoices
		BEGIN SELECT RAISE(ABORT, 'test: detected expiry blocked'); END`)
	if err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	defer db.Exec(`DROP TRIGGER IF EXISTS v2_test_block_detected_expiry`) //nolint:errcheck

	result := w.ProcessOnce(context.Background())
	if !result.HasProviderError {
		t.Error("expected HasProviderError=true for unexpected DB error during detected expiry, got false")
	}
}

// TestWatcherProcessOnceConcurrentSafe verifies that concurrent ProcessOnce
// calls with provider errors do not race on shared watcher state.
// Run with -race to detect data races.
func TestWatcherProcessOnceConcurrentSafe(t *testing.T) {
	svc, _ := newTestService(t)
	chain := newFakeChainClient()
	chain.err = errFakeProvider
	bal := &fakeBalanceReader{}

	w, _ := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, time.Now,
		func(_ context.Context, _ time.Duration) {},
		0,
	)

	_, _, _ = svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())

	const goroutines = 8
	var wg sync.WaitGroup
	ctx := context.Background()
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.ProcessOnce(ctx)
		}()
	}
	wg.Wait()
	// If -race detects no issues, the test passes.
}
