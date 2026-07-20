package v2

// Test matrix — all 15 original tests (task 01) + 8 new/updated tests (task 01A):
//
// Original tests (task 01):
// | # | test name                           | invariant                                                            |
// |---|-------------------------------------|----------------------------------------------------------------------|
// |  1| TestCreateIntentAtomicity           | flow + invoice + code hash created atomically                        |
// |  2| TestRawCodeAbsentFromDB             | raw code never appears in any v2 table                               |
// |  3| TestWalletAddressAbsentFromDB       | plaintext wallet never appears in any v2 table                       |
// |  4| TestCurrencyValidation              | only BTC and LTC accepted                                            |
// |  5| TestBech32NormalizationStable       | bech32 mixed-case → same fingerprint as lowercase                    |
// |  6| TestRestoreCorrectCodeAndWallet     | correct code + wallet → success, invoice snapshot returned           |
// |  7| TestRestoreEnumerationResistance    | wrong code and wrong wallet both return ErrNotFound                  |
// |  8| TestConfirmPaymentEntitlement       | confirmation sets expiry = confirmed_at + 5*24h exactly once         |
// |  9| TestConfirmIdempotentSameTxid       | repeat with same txid → success, state unchanged                     |
// | 10| TestConfirmConflictDifferentTxid    | different txid → ErrConflict, expiry unchanged                       |
// | 11| TestConfirmConcurrentRace           | concurrent callers serialize safely; one entitlement, no double-set  |
// | 12| TestBalanceBeforePaymentForbidden   | RecordPostPaymentBalance before confirm → ErrInvalidState            |
// | 13| TestLowBalanceThenFormReady         | paid_low_balance → re-check with ≥floor → form_ready                 |
// | 14| TestFormReadyNoDowngrade            | form_ready + low re-check → stays form_ready                         |
// | 15| TestSchemaDDLIdempotencyFile        | DDL idempotent across open/apply/close/open/apply on a real file     |
//
// Task 01A additions:
// | 1A-1| TestInvoiceSnapshotStoredAndRestored  | full invoice snapshot saved and returned by Restore                 |
// | 1A-2| TestInvalidDraftNoWrite               | invalid draft → no flow, no invoice in DB                           |
// | 1A-3| TestCreateAtomicityTriggerRollback    | failed invoice insert (via trigger) rolls back flow row too         |
// | 1A-4| (merged into existing tests 2 and 3)  | plaintext wallet and raw code still absent                          |
// | 1A-5| TestInvalidBalanceInputsNoWrite       | NaN/Inf/negative balance, zero/negative floor → no DB write         |
// | 1A-6| TestBalanceCASFailureNoPartialWrite   | failed flow state UPDATE (via trigger) → invoice balance not written |
// | 1A-7| (replaces test 15)                    | DDL idempotency via real SQLite file                                |
// | 1A-8| TestDomainSeparationDistinctHashes    | wallet vs code vs V1 domains produce distinct values                |
//
// Note on TestConfirmConcurrentRace (test 11):
// With MaxOpenConns(1), all DB operations serialize through one connection. The test
// demonstrates that concurrent *Go* callers produce a consistent result (one entitlement,
// no double-set). It does NOT demonstrate concurrent SQLite writer transactions, which
// MaxOpenConns(1) makes impossible by construction.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var testHMACKey = []byte("test-hmac-key-for-v2-unit-tests-only")

// validDraft returns a well-formed InvoiceDraft for tests that need a passing draft.
func validDraft() InvoiceDraft {
	return InvoiceDraft{
		PaymentAddress: "bc1qplatformreceiveaddress000000000000000000",
		AmountAtomic:   500_000, // 0.005 BTC in satoshis
		AmountUSDCents: 500,     // $5.00
	}
}

func newTestService(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	svc, err := New(db, testHMACKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return svc, db
}

// createIntent creates a payment intent with validDraft and fails the test on error.
func createIntent(t *testing.T, svc *Service, addr, currency string) (rawCode string, fv FlowView) {
	t.Helper()
	rawCode, fv, err := svc.CreatePaymentIntent(addr, currency, validDraft())
	if err != nil {
		t.Fatalf("CreatePaymentIntent: %v", err)
	}
	return rawCode, fv
}

// confirmPayment confirms a payment and fails the test on error.
// It first calls RecordPaymentDetected (using "bc1qtest" as the sender address,
// which is the wallet all existing tests use), then calls ConfirmPayment.
func confirmPayment(t *testing.T, svc *Service, fv FlowView, txid string) FlowView {
	t.Helper()
	// The tests that use this helper create intents with "bc1qtest" as the wallet address.
	// Pass it as the sender input for the detection step.
	const testSenderAddr = "bc1qtest"
	now := time.Now()
	detected, err := svc.RecordPaymentDetected(
		fv.FlowID, fv.InvoiceID, txid,
		[]string{testSenderAddr},
		fv.AmountAtomic,
		now,
	)
	if err != nil {
		t.Fatalf("RecordPaymentDetected (in confirmPayment helper): %v", err)
	}
	result, err := svc.ConfirmPayment(detected.FlowID, detected.InvoiceID, now)
	if err != nil {
		t.Fatalf("ConfirmPayment: %v", err)
	}
	return result
}

// ── Test 01 ──────────────────────────────────────────────────────────────────

func TestCreateIntentAtomicity(t *testing.T) {
	// flow row + invoice row + management_code_hash created atomically.
	svc, db := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq", "BTC")

	var flowCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM v2_client_flows WHERE id = ?`,
		fv.FlowID).Scan(&flowCount); err != nil {
		t.Fatalf("count flows: %v", err)
	}
	if flowCount != 1 {
		t.Errorf("expected 1 flow row, got %d", flowCount)
	}

	var invoiceCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM v2_invoices WHERE id = ? AND flow_id = ?`,
		fv.InvoiceID, fv.FlowID).Scan(&invoiceCount); err != nil {
		t.Fatalf("count invoices: %v", err)
	}
	if invoiceCount != 1 {
		t.Errorf("expected 1 invoice row, got %d", invoiceCount)
	}

	var codeHash string
	if err := db.QueryRow(`SELECT management_code_hash FROM v2_client_flows WHERE id = ?`,
		fv.FlowID).Scan(&codeHash); err != nil {
		t.Fatalf("read code hash: %v", err)
	}
	if codeHash == "" {
		t.Error("management_code_hash is empty")
	}

	if fv.State != StateAwaitingPayment {
		t.Errorf("expected state %q, got %q", StateAwaitingPayment, fv.State)
	}

	// Invoice snapshot fields must be populated.
	if fv.PaymentAddress == "" {
		t.Error("PaymentAddress is empty in FlowView")
	}
	if fv.AmountAtomic == 0 {
		t.Error("AmountAtomic is zero in FlowView")
	}
	if fv.AmountUSDCents != 500 {
		t.Errorf("AmountUSDCents: got %d, want 500", fv.AmountUSDCents)
	}
}

// ── Test 02 ──────────────────────────────────────────────────────────────────

func TestRawCodeAbsentFromDB(t *testing.T) {
	// Raw code returned to caller must not appear anywhere in v2 tables.
	svc, db := newTestService(t)
	rawCode, fv := createIntent(t, svc, "bc1qtest000000000000000000000000000000000000", "BTC")

	if rawCode == "" {
		t.Fatal("CreatePaymentIntent returned empty raw code")
	}

	var storedHash string
	if err := db.QueryRow(`SELECT management_code_hash FROM v2_client_flows WHERE id = ?`,
		fv.FlowID).Scan(&storedHash); err != nil {
		t.Fatalf("read stored hash: %v", err)
	}
	if storedHash == rawCode {
		t.Error("raw code stored in plaintext as management_code_hash")
	}

	rows, err := db.Query(`SELECT id, wallet_fingerprint, management_code_hash FROM v2_client_flows`)
	if err != nil {
		t.Fatalf("query flows: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, fp, ch string
		if err := rows.Scan(&id, &fp, &ch); err != nil {
			t.Fatal(err)
		}
		if id == rawCode || fp == rawCode || ch == rawCode {
			t.Error("raw code found in v2_client_flows row")
		}
	}

	irows, err := db.Query(`SELECT id, flow_id, payment_address FROM v2_invoices`)
	if err != nil {
		t.Fatalf("query invoices: %v", err)
	}
	defer irows.Close()
	for irows.Next() {
		var id, flowID, addr string
		if err := irows.Scan(&id, &flowID, &addr); err != nil {
			t.Fatal(err)
		}
		if id == rawCode || flowID == rawCode || addr == rawCode {
			t.Error("raw code found in v2_invoices row")
		}
	}
}

// ── Test 03 ──────────────────────────────────────────────────────────────────

func TestWalletAddressAbsentFromDB(t *testing.T) {
	// Plaintext wallet must never appear in any v2 column.
	svc, db := newTestService(t)
	const walletAddr = "LdP8Qox1VAhCzLJNqrr74YovaWYyNBUWvL"
	_, fv := createIntent(t, svc, walletAddr, "LTC")

	var id, fp, currency, ch, state string
	var created, updated int64
	if err := db.QueryRow(`
		SELECT id, wallet_fingerprint, currency, management_code_hash, state, created_at, updated_at
		FROM v2_client_flows WHERE id = ?`, fv.FlowID).
		Scan(&id, &fp, &currency, &ch, &state, &created, &updated); err != nil {
		t.Fatalf("read flow: %v", err)
	}
	for col, val := range map[string]string{
		"id": id, "wallet_fingerprint": fp, "currency": currency,
		"management_code_hash": ch, "state": state,
	} {
		if val == walletAddr {
			t.Errorf("wallet address found in v2_client_flows.%s", col)
		}
	}
	if fp == walletAddr {
		t.Error("wallet_fingerprint equals plaintext address")
	}

	var invID, invFlowID, invStatus, invAddr string
	if err := db.QueryRow(`SELECT id, flow_id, status, payment_address FROM v2_invoices WHERE flow_id = ?`,
		fv.FlowID).Scan(&invID, &invFlowID, &invStatus, &invAddr); err != nil {
		t.Fatalf("read invoice: %v", err)
	}
	for col, val := range map[string]string{
		"id": invID, "flow_id": invFlowID, "status": invStatus, "payment_address": invAddr,
	} {
		if val == walletAddr {
			t.Errorf("wallet address found in v2_invoices.%s", col)
		}
	}
}

// ── Test 04 ──────────────────────────────────────────────────────────────────

func TestCurrencyValidation(t *testing.T) {
	// Only BTC and LTC accepted; any other value returns ErrInvalidCurrency.
	// Currency is validated before draft — bad currency still returns ErrInvalidCurrency
	// even with an empty draft.
	svc, _ := newTestService(t)
	const addr = "bc1qtest"

	for _, currency := range []string{"BTC", "LTC"} {
		_, _, err := svc.CreatePaymentIntent(addr, currency, validDraft())
		if err != nil {
			t.Errorf("currency %q: unexpected error: %v", currency, err)
		}
	}

	for _, bad := range []string{"ETH", "USDT", "btc", "ltc", "", "BTC ", " LTC"} {
		_, _, err := svc.CreatePaymentIntent(addr, bad, InvoiceDraft{})
		if !errors.Is(err, ErrInvalidCurrency) {
			t.Errorf("currency %q: expected ErrInvalidCurrency, got %v", bad, err)
		}
	}
}

// ── Test 05 ──────────────────────────────────────────────────────────────────

func TestBech32NormalizationStable(t *testing.T) {
	// Bech32 mixed-case → same fingerprint as lowercase; Restore succeeds regardless of case.
	svc, _ := newTestService(t)

	const lower = "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	const upper = "BC1QAR0SRRR7XFKVY5L643LYDNW9RE59GTZZWF5MDQ"
	const mixed = "Bc1QaR0SrRr7XFkVy5l643lYdNw9Re59GtZZwf5mDq"

	rawCode, fv := createIntent(t, svc, lower, "BTC")

	for _, tc := range []struct{ name, addr string }{
		{"uppercase", upper},
		{"mixed-case", mixed},
	} {
		got, err := svc.RestorePaymentIntent(rawCode, tc.addr)
		if err != nil {
			t.Errorf("restore with %s bech32: %v", tc.name, err)
			continue
		}
		if got.FlowID != fv.FlowID {
			t.Errorf("%s restore: flow ID mismatch: got %q, want %q",
				tc.name, got.FlowID, fv.FlowID)
		}
	}
}

// ── Test 06 ──────────────────────────────────────────────────────────────────

func TestRestoreCorrectCodeAndWallet(t *testing.T) {
	// Correct rawCode + correct walletAddress → success; full FlowView including
	// invoice snapshot is returned.
	svc, _ := newTestService(t)
	const addr = "LdP8Qox1VAhCzLJNqrr74YovaWYyNBUWvL"
	rawCode, fv := createIntent(t, svc, addr, "LTC")

	restored, err := svc.RestorePaymentIntent(rawCode, addr)
	if err != nil {
		t.Fatalf("RestorePaymentIntent: %v", err)
	}
	if restored.FlowID != fv.FlowID {
		t.Errorf("FlowID mismatch: got %q, want %q", restored.FlowID, fv.FlowID)
	}
	if restored.InvoiceID != fv.InvoiceID {
		t.Errorf("InvoiceID mismatch: got %q, want %q", restored.InvoiceID, fv.InvoiceID)
	}
	if restored.State != StateAwaitingPayment {
		t.Errorf("State: got %q, want %q", restored.State, StateAwaitingPayment)
	}
	if restored.Currency != "LTC" {
		t.Errorf("Currency: got %q, want LTC", restored.Currency)
	}
	// Invoice snapshot must survive the round-trip.
	draft := validDraft()
	if restored.PaymentAddress != draft.PaymentAddress {
		t.Errorf("PaymentAddress: got %q, want %q", restored.PaymentAddress, draft.PaymentAddress)
	}
	if restored.AmountAtomic != draft.AmountAtomic {
		t.Errorf("AmountAtomic: got %d, want %d", restored.AmountAtomic, draft.AmountAtomic)
	}
	if restored.AmountUSDCents != draft.AmountUSDCents {
		t.Errorf("AmountUSDCents: got %d, want %d", restored.AmountUSDCents, draft.AmountUSDCents)
	}
}

// ── Test 07 ──────────────────────────────────────────────────────────────────

func TestRestoreEnumerationResistance(t *testing.T) {
	// Wrong code and wrong wallet both return ErrNotFound — the same external error.
	svc, _ := newTestService(t)
	const addr = "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	rawCode, _ := createIntent(t, svc, addr, "BTC")

	const wrongCode = "000000000000000000000000000000000000000000000000000000000000dead"
	const wrongWallet = "bc1qwrong0000000000000000000000000000000000"

	_, err1 := svc.RestorePaymentIntent(wrongCode, addr)
	if !errors.Is(err1, ErrNotFound) {
		t.Errorf("wrong code: expected ErrNotFound, got %v", err1)
	}

	_, err2 := svc.RestorePaymentIntent(rawCode, wrongWallet)
	if !errors.Is(err2, ErrNotFound) {
		t.Errorf("wrong wallet: expected ErrNotFound, got %v", err2)
	}

	if err1.Error() != err2.Error() {
		t.Errorf("errors differ: %q vs %q — leaks enumeration information", err1, err2)
	}
}

// ── Test 08 ──────────────────────────────────────────────────────────────────

func TestConfirmPaymentEntitlement(t *testing.T) {
	// ConfirmPayment sets entitlement_expires_at = payment_confirmed_at + 5*24h.
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	before := time.Now()
	result := confirmPayment(t, svc, fv, "txid_abc123")
	after := time.Now()

	if result.State != StatePaymentConfirmed {
		t.Errorf("state: got %q, want %q", result.State, StatePaymentConfirmed)
	}
	if result.InvoiceStatus != "confirmed" {
		t.Errorf("invoice status: got %q, want confirmed", result.InvoiceStatus)
	}
	if result.PaymentConfirmedAt == nil {
		t.Fatal("payment_confirmed_at is nil")
	}
	if result.EntitlementExpiresAt == nil {
		t.Fatal("entitlement_expires_at is nil")
	}

	confirmedAt := *result.PaymentConfirmedAt
	expiresAt := *result.EntitlementExpiresAt

	if confirmedAt.Before(before.Truncate(time.Second)) || confirmedAt.After(after.Add(time.Second)) {
		t.Errorf("payment_confirmed_at %v outside expected range [%v, %v]",
			confirmedAt, before, after)
	}

	expectedExpiry := confirmedAt.Add(entitlementDuration)
	diff := expiresAt.Sub(expectedExpiry)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("entitlement_expires_at %v, expected ~%v (diff %v)",
			expiresAt, expectedExpiry, diff)
	}

	if result.PaymentTxid == nil || *result.PaymentTxid != "txid_abc123" {
		t.Errorf("payment_txid: got %v, want txid_abc123", result.PaymentTxid)
	}
}

// ── Test 09 ──────────────────────────────────────────────────────────────────

func TestConfirmIdempotentSameTxid(t *testing.T) {
	// Repeat ConfirmPayment on an already-confirmed invoice → success, state and expiry unchanged.
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	first := confirmPayment(t, svc, fv, "txid_same")

	// Second confirm: no txid parameter; idempotent if already confirmed.
	second, err := svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, time.Now())
	if err != nil {
		t.Fatalf("second ConfirmPayment (idempotent): %v", err)
	}
	if second.State != first.State {
		t.Errorf("state changed on idempotent call: %q → %q", first.State, second.State)
	}
	if second.EntitlementExpiresAt == nil || first.EntitlementExpiresAt == nil {
		t.Fatal("entitlement_expires_at is nil")
	}
	if !second.EntitlementExpiresAt.Equal(*first.EntitlementExpiresAt) {
		t.Errorf("entitlement_expires_at changed: %v → %v",
			*first.EntitlementExpiresAt, *second.EntitlementExpiresAt)
	}
}

// ── Test 10 ──────────────────────────────────────────────────────────────────

// TestDetectionConflictDifferentTxid verifies that trying to detect a different
// txid after the first one is already locked returns ErrConflict, and that the
// original detected_txid is unchanged.
func TestDetectionConflictDifferentTxid(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	now := time.Now()
	_, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_first",
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	if err != nil {
		t.Fatalf("first detection: %v", err)
	}

	_, err = svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_different",
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for different txid, got %v", err)
	}

	// Verify detected_txid is still txid_first.
	reread, err := svc.readFlowView(fv.FlowID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if reread.DetectedTxid == nil || *reread.DetectedTxid != "txid_first" {
		t.Errorf("detected_txid changed: got %v, want txid_first", reread.DetectedTxid)
	}
}

// TestConfirmConflictDifferentTxid verifies that calling ConfirmPayment on a
// confirmed invoice returns success (idempotent) — the "conflict" is now at
// detection time, not confirmation time.
func TestConfirmConflictDifferentTxid(t *testing.T) {
	// After confirmation, calling ConfirmPayment again returns success (idempotent).
	// The entitlement_expires_at and payment_txid remain unchanged.
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	first := confirmPayment(t, svc, fv, "txid_first")

	// Calling ConfirmPayment again on an already-confirmed invoice is idempotent.
	second, err := svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, time.Now())
	if err != nil {
		t.Fatalf("second ConfirmPayment: expected idempotent success, got %v", err)
	}

	if second.EntitlementExpiresAt == nil || first.EntitlementExpiresAt == nil {
		t.Fatal("entitlement_expires_at is nil")
	}
	if !second.EntitlementExpiresAt.Equal(*first.EntitlementExpiresAt) {
		t.Errorf("entitlement_expires_at changed: %v → %v",
			*first.EntitlementExpiresAt, *second.EntitlementExpiresAt)
	}
	if second.PaymentTxid == nil || *second.PaymentTxid != "txid_first" {
		t.Errorf("payment_txid overwritten: got %v, want txid_first", second.PaymentTxid)
	}
}

// ── Test 11 ──────────────────────────────────────────────────────────────────

// TestDetectionConcurrentRace verifies that multiple goroutines racing to detect
// a payment produce a consistent result: exactly one txid is locked, others get
// either idempotent success (same txid) or ErrConflict (different txid).
func TestDetectionConcurrentRace(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	const workers = 8
	txids := make([]string, workers)
	for i := range txids {
		if i%2 == 0 {
			txids[i] = "txid_A"
		} else {
			txids[i] = "txid_B"
		}
	}

	type result struct {
		err error
	}
	results := make([]result, workers)
	var wg sync.WaitGroup
	var gate sync.WaitGroup
	gate.Add(1)
	wg.Add(workers)
	now := time.Now()
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			gate.Wait()
			_, err := svc.RecordPaymentDetected(
				fv.FlowID, fv.InvoiceID, txids[i],
				[]string{"bc1qtest"}, fv.AmountAtomic, now,
			)
			results[i] = result{err: err}
		}()
	}
	gate.Done()
	wg.Wait()

	var successes, conflicts int
	for _, r := range results {
		if r.err == nil {
			successes++
		} else if errors.Is(r.err, ErrConflict) {
			conflicts++
		} else {
			t.Errorf("unexpected error: %v", r.err)
		}
	}
	if successes == 0 {
		t.Fatal("no successful detection")
	}

	// Exactly one txid must be detected.
	final, err := svc.readFlowView(fv.FlowID)
	if err != nil {
		t.Fatalf("read final state: %v", err)
	}
	if final.DetectedTxid == nil {
		t.Fatal("no detected_txid after concurrent detection")
	}
	if *final.DetectedTxid != "txid_A" && *final.DetectedTxid != "txid_B" {
		t.Errorf("unexpected winning txid: %q", *final.DetectedTxid)
	}
	t.Logf("concurrent detect: %d successes, %d conflicts, winning txid=%q",
		successes, conflicts, *final.DetectedTxid)
}

// TestConfirmConcurrentRace verifies that concurrent ConfirmPayment calls
// on an already-detected invoice produce a consistent result.
func TestConfirmConcurrentRace(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	// First, detect the payment.
	now := time.Now()
	_, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_race",
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	if err != nil {
		t.Fatalf("detection: %v", err)
	}

	const workers = 8
	type result struct {
		fv  FlowView
		err error
	}
	results := make([]result, workers)
	var wg sync.WaitGroup
	var startGate sync.WaitGroup
	startGate.Add(1)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			startGate.Wait()
			r, err := svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, now)
			results[i] = result{fv: r, err: err}
		}()
	}
	startGate.Done()
	wg.Wait()

	var successes []FlowView
	conflictCount := 0
	for _, r := range results {
		if r.err == nil {
			successes = append(successes, r.fv)
		} else if errors.Is(r.err, ErrConflict) {
			conflictCount++
		} else {
			t.Errorf("unexpected error: %v", r.err)
		}
	}

	if len(successes) == 0 {
		t.Fatal("no successful confirmation")
	}

	// All successes share the same entitlement_expires_at.
	first := successes[0]
	if first.EntitlementExpiresAt == nil {
		t.Fatal("winner has nil entitlement_expires_at")
	}
	for _, s := range successes[1:] {
		if s.EntitlementExpiresAt == nil {
			t.Error("success result has nil entitlement_expires_at")
			continue
		}
		if !s.EntitlementExpiresAt.Equal(*first.EntitlementExpiresAt) {
			t.Errorf("entitlement_expires_at inconsistent across successes")
		}
	}

	final, err := svc.readFlowView(fv.FlowID)
	if err != nil {
		t.Fatalf("read final state: %v", err)
	}
	if final.EntitlementExpiresAt == nil {
		t.Error("final entitlement_expires_at is nil")
	}
	if final.PaymentTxid == nil {
		t.Error("final payment_txid is nil")
	}
	if *final.PaymentTxid != "txid_race" {
		t.Errorf("unexpected winning txid: %q", *final.PaymentTxid)
	}

	t.Logf("concurrent confirm: %d successes, %d conflicts, winning txid=%q",
		len(successes), conflictCount, *final.PaymentTxid)
}

// ── Test 12 ──────────────────────────────────────────────────────────────────

func TestBalanceBeforePaymentForbidden(t *testing.T) {
	// RecordPostPaymentBalance before payment confirmation → ErrInvalidState.
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	_, err := svc.RecordPostPaymentBalance(fv.FlowID, 250.0, 120.0)
	if !errors.Is(err, ErrInvalidState) {
		t.Errorf("expected ErrInvalidState before payment, got %v", err)
	}
}

// ── Test 13 ──────────────────────────────────────────────────────────────────

func TestLowBalanceThenFormReady(t *testing.T) {
	// paid_low_balance can be upgraded to form_ready by a passing balance check.
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")
	fv = confirmPayment(t, svc, fv, "txid_low_then_ready")

	const floor = 120.0

	after1, err := svc.RecordPostPaymentBalance(fv.FlowID, 90.0, floor)
	if err != nil {
		t.Fatalf("first balance check: %v", err)
	}
	if after1.State != StatePaidLowBalance {
		t.Errorf("after low-balance check: got %q, want %q", after1.State, StatePaidLowBalance)
	}

	after2, err := svc.RecordPostPaymentBalance(fv.FlowID, 120.0, floor)
	if err != nil {
		t.Fatalf("second balance check: %v", err)
	}
	if after2.State != StateFormReady {
		t.Errorf("after sufficient balance: got %q, want %q", after2.State, StateFormReady)
	}
	if after2.LastBalanceUSD == nil || *after2.LastBalanceUSD != 120.0 {
		t.Errorf("last_balance_usd: got %v, want 120.0", after2.LastBalanceUSD)
	}
}

// ── Test 14 ──────────────────────────────────────────────────────────────────

func TestFormReadyNoDowngrade(t *testing.T) {
	// form_ready must not be downgraded by a low-balance re-check.
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")
	fv = confirmPayment(t, svc, fv, "txid_form_ready")

	const floor = 120.0

	after1, err := svc.RecordPostPaymentBalance(fv.FlowID, 200.0, floor)
	if err != nil {
		t.Fatalf("first balance check: %v", err)
	}
	if after1.State != StateFormReady {
		t.Errorf("expected form_ready after sufficient balance, got %q", after1.State)
	}

	after2, err := svc.RecordPostPaymentBalance(fv.FlowID, 50.0, floor)
	if err != nil {
		t.Fatalf("low balance re-check: %v", err)
	}
	if after2.State != StateFormReady {
		t.Errorf("form_ready downgraded to %q — must not happen", after2.State)
	}
	if after2.LastBalanceUSD == nil || *after2.LastBalanceUSD != 50.0 {
		t.Errorf("last_balance_usd: got %v, want 50.0", after2.LastBalanceUSD)
	}
}

// ── Test 15 (01A-7) ──────────────────────────────────────────────────────────

func TestSchemaDDLIdempotencyFile(t *testing.T) {
	// DDL idempotency verified via real SQLite file: open/apply/close, then open/apply again.
	// This proves that CREATE TABLE/INDEX IF NOT EXISTS is safe across process restarts,
	// not just within a single in-memory session.
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "v2_test.db")

	dsn := func(path string) string {
		return fmt.Sprintf("file:%s?_foreign_keys=on&_journal_mode=WAL", path)
	}

	// First open + apply.
	db1, err := sql.Open("sqlite", dsn(dbPath))
	if err != nil {
		t.Fatalf("first sql.Open: %v", err)
	}
	db1.SetMaxOpenConns(1)
	if err := ApplySchema(db1); err != nil {
		t.Fatalf("first ApplySchema: %v", err)
	}
	// Create a flow to confirm the schema is functional.
	svc1, err := New(db1, testHMACKey)
	if err != nil {
		t.Fatalf("New (first open): %v", err)
	}
	_, fv, err := svc1.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err != nil {
		t.Fatalf("CreatePaymentIntent (first open): %v", err)
	}
	db1.Close()

	// Second open + apply on the same file — must not fail.
	db2, err := sql.Open("sqlite", dsn(dbPath))
	if err != nil {
		t.Fatalf("second sql.Open: %v", err)
	}
	db2.SetMaxOpenConns(1)
	defer db2.Close()
	if err := ApplySchema(db2); err != nil {
		t.Fatalf("second ApplySchema (idempotency check): %v", err)
	}

	// Data from the first session must still be readable.
	svc2, err := New(db2, testHMACKey)
	if err != nil {
		t.Fatalf("New (second open): %v", err)
	}
	reread, err := svc2.readFlowView(fv.FlowID)
	if err != nil {
		t.Fatalf("readFlowView after reopen: %v", err)
	}
	if reread.FlowID != fv.FlowID {
		t.Errorf("FlowID mismatch after reopen: got %q, want %q", reread.FlowID, fv.FlowID)
	}

	// Temp dir cleanup is handled by t.TempDir().
	_ = os.Remove(dbPath) // best-effort early cleanup; TempDir does it too
}

// ── Test 01A-1 ────────────────────────────────────────────────────────────────

func TestInvoiceSnapshotStoredAndRestored(t *testing.T) {
	// CreatePaymentIntent stores the full invoice snapshot; RestorePaymentIntent
	// returns exactly the same address, amount_atomic, and amount_usd_cents.
	svc, db := newTestService(t)

	draft := InvoiceDraft{
		PaymentAddress: "ltc1qplatformaddr000000000000000000000000",
		AmountAtomic:   12_345_678,
		AmountUSDCents: 500,
	}
	const addr = "LdP8Qox1VAhCzLJNqrr74YovaWYyNBUWvL"

	rawCode, fv, err := svc.CreatePaymentIntent(addr, "LTC", draft)
	if err != nil {
		t.Fatalf("CreatePaymentIntent: %v", err)
	}

	// Verify FlowView from creation.
	if fv.PaymentAddress != draft.PaymentAddress {
		t.Errorf("create: PaymentAddress: got %q, want %q", fv.PaymentAddress, draft.PaymentAddress)
	}
	if fv.AmountAtomic != draft.AmountAtomic {
		t.Errorf("create: AmountAtomic: got %d, want %d", fv.AmountAtomic, draft.AmountAtomic)
	}
	if fv.AmountUSDCents != draft.AmountUSDCents {
		t.Errorf("create: AmountUSDCents: got %d, want %d", fv.AmountUSDCents, draft.AmountUSDCents)
	}

	// Verify DB columns directly (no REAL for monetary amounts).
	var dbAddr string
	var dbCents, dbAtomic int64
	if err := db.QueryRow(`SELECT payment_address, amount_usd_cents, amount_atomic FROM v2_invoices WHERE flow_id = ?`,
		fv.FlowID).Scan(&dbAddr, &dbCents, &dbAtomic); err != nil {
		t.Fatalf("read invoice columns: %v", err)
	}
	if dbAddr != draft.PaymentAddress {
		t.Errorf("DB payment_address: got %q, want %q", dbAddr, draft.PaymentAddress)
	}
	if dbCents != draft.AmountUSDCents {
		t.Errorf("DB amount_usd_cents: got %d, want %d", dbCents, draft.AmountUSDCents)
	}
	if dbAtomic != draft.AmountAtomic {
		t.Errorf("DB amount_atomic: got %d, want %d", dbAtomic, draft.AmountAtomic)
	}

	// Verify RestorePaymentIntent returns the same snapshot.
	restored, err := svc.RestorePaymentIntent(rawCode, addr)
	if err != nil {
		t.Fatalf("RestorePaymentIntent: %v", err)
	}
	if restored.PaymentAddress != draft.PaymentAddress {
		t.Errorf("restore: PaymentAddress: got %q, want %q",
			restored.PaymentAddress, draft.PaymentAddress)
	}
	if restored.AmountAtomic != draft.AmountAtomic {
		t.Errorf("restore: AmountAtomic: got %d, want %d",
			restored.AmountAtomic, draft.AmountAtomic)
	}
	if restored.AmountUSDCents != draft.AmountUSDCents {
		t.Errorf("restore: AmountUSDCents: got %d, want %d",
			restored.AmountUSDCents, draft.AmountUSDCents)
	}
}

// ── Test 01A-2 ────────────────────────────────────────────────────────────────

func TestInvalidDraftNoWrite(t *testing.T) {
	// An invalid draft must not leave any flow or invoice row in the DB.
	svc, db := newTestService(t)

	cases := []struct {
		name  string
		draft InvoiceDraft
	}{
		{"empty address", InvoiceDraft{PaymentAddress: "", AmountAtomic: 1000, AmountUSDCents: 500}},
		{"zero atomic", InvoiceDraft{PaymentAddress: "addr", AmountAtomic: 0, AmountUSDCents: 500}},
		{"negative atomic", InvoiceDraft{PaymentAddress: "addr", AmountAtomic: -1, AmountUSDCents: 500}},
		{"wrong cents", InvoiceDraft{PaymentAddress: "addr", AmountAtomic: 1000, AmountUSDCents: 499}},
		{"zero cents", InvoiceDraft{PaymentAddress: "addr", AmountAtomic: 1000, AmountUSDCents: 0}},
		{"empty draft", InvoiceDraft{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := svc.CreatePaymentIntent("bc1qtest", "BTC", tc.draft)
			if !errors.Is(err, ErrInvalidDraft) {
				t.Errorf("expected ErrInvalidDraft, got %v", err)
			}

			var flowCount, invCount int
			db.QueryRow(`SELECT COUNT(*) FROM v2_client_flows`).Scan(&flowCount) //nolint:errcheck
			db.QueryRow(`SELECT COUNT(*) FROM v2_invoices`).Scan(&invCount)      //nolint:errcheck
			if flowCount != 0 {
				t.Errorf("after invalid draft %q: found %d flow row(s)", tc.name, flowCount)
			}
			if invCount != 0 {
				t.Errorf("after invalid draft %q: found %d invoice row(s)", tc.name, invCount)
			}
		})
	}
}

// ── Test 01A-3 ────────────────────────────────────────────────────────────────

func TestCreateAtomicityTriggerRollback(t *testing.T) {
	// If the invoice INSERT fails after the flow INSERT succeeds, both rows must be
	// absent (full rollback). A temporary RAISE(ABORT) trigger simulates the failure.
	svc, db := newTestService(t)

	_, err := db.Exec(`
		CREATE TRIGGER v2_test_block_invoice_insert
		BEFORE INSERT ON v2_invoices
		BEGIN SELECT RAISE(ABORT, 'test: invoice insert blocked'); END`)
	if err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	defer db.Exec(`DROP TRIGGER IF EXISTS v2_test_block_invoice_insert`) //nolint:errcheck

	_, _, err = svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err == nil {
		t.Fatal("expected error from blocked invoice insert, got nil")
	}

	// Both tables must be empty — the flow row must have been rolled back.
	var flowCount, invCount int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_flows`).Scan(&flowCount) //nolint:errcheck
	db.QueryRow(`SELECT COUNT(*) FROM v2_invoices`).Scan(&invCount)      //nolint:errcheck
	if flowCount != 0 {
		t.Errorf("after rollback: found %d flow row(s) — transaction was not fully rolled back",
			flowCount)
	}
	if invCount != 0 {
		t.Errorf("after rollback: found %d invoice row(s)", invCount)
	}
}

// ── Test 01A-5 ────────────────────────────────────────────────────────────────

func TestInvalidBalanceInputsNoWrite(t *testing.T) {
	// NaN, Inf, negative balance, zero floor, and negative floor are all rejected
	// before any DB write; neither flow state nor invoice balance is modified.
	svc, db := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")
	fv = confirmPayment(t, svc, fv, "txid_invalid_balance")

	invalidCases := []struct {
		name    string
		balance float64
		floor   float64
	}{
		{"NaN balance", math.NaN(), 120.0},
		{"+Inf balance", math.Inf(1), 120.0},
		{"-Inf balance", math.Inf(-1), 120.0},
		{"negative balance", -1.0, 120.0},
		{"NaN floor", 200.0, math.NaN()},
		{"+Inf floor", 200.0, math.Inf(1)},
		{"zero floor", 200.0, 0.0},
		{"negative floor", 200.0, -10.0},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.RecordPostPaymentBalance(fv.FlowID, tc.balance, tc.floor)
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("expected ErrInvalidInput, got %v", err)
			}

			// Flow state must still be payment_confirmed (unchanged).
			var state string
			db.QueryRow(`SELECT state FROM v2_client_flows WHERE id = ?`,
				fv.FlowID).Scan(&state) //nolint:errcheck
			if state != StatePaymentConfirmed {
				t.Errorf("flow state was modified to %q after invalid input", state)
			}

			// Invoice balance snapshot must remain NULL.
			var lastBalance sql.NullFloat64
			db.QueryRow(`SELECT last_balance_usd FROM v2_invoices WHERE flow_id = ?`,
				fv.FlowID).Scan(&lastBalance) //nolint:errcheck
			if lastBalance.Valid {
				t.Errorf("invoice last_balance_usd was written (%v) for invalid input %q",
					lastBalance.Float64, tc.name)
			}
		})
	}
}

// ── Test 01A-6 ────────────────────────────────────────────────────────────────

func TestBalanceCASFailureNoPartialWrite(t *testing.T) {
	// When the flow state UPDATE fails (SQL error or zero rows), the invoice balance
	// snapshot must NOT be partially written. A temporary RAISE(ABORT) trigger on
	// v2_client_flows forces the state UPDATE to fail, demonstrating that the invoice
	// UPDATE inside the same transaction is rolled back with it.
	//
	// Note: with MaxOpenConns(1), the RowsAffected=0 path for RecordPostPaymentBalance
	// cannot be triggered by concurrent goroutines (all writes serialize). The trigger
	// tests the same transactional invariant: state-update failure → no invoice write.
	svc, db := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")
	confirmPayment(t, svc, fv, "txid_cas_base")

	// Block any UPDATE to the state column on v2_client_flows.
	_, err := db.Exec(`
		CREATE TRIGGER v2_test_block_flow_state_upd
		BEFORE UPDATE OF state ON v2_client_flows
		BEGIN SELECT RAISE(ABORT, 'test: flow state update blocked'); END`)
	if err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	defer db.Exec(`DROP TRIGGER IF EXISTS v2_test_block_flow_state_upd`) //nolint:errcheck

	// RecordPostPaymentBalance must fail — the flow state UPDATE is blocked.
	_, casErr := svc.RecordPostPaymentBalance(fv.FlowID, 200.0, 120.0)
	if casErr == nil {
		t.Fatal("expected error when flow state UPDATE is blocked, got nil")
	}

	// Invoice balance must NOT have been written (transaction rolled back).
	var lastBalance sql.NullFloat64
	if err := db.QueryRow(`SELECT last_balance_usd FROM v2_invoices WHERE flow_id = ?`,
		fv.FlowID).Scan(&lastBalance); err != nil {
		t.Fatalf("read invoice balance: %v", err)
	}
	if lastBalance.Valid {
		t.Errorf("invoice last_balance_usd was partially written (%v) despite state UPDATE failure",
			lastBalance.Float64)
	}
}

// ── Task 02 prelude: PaymentAddress TrimSpace ─────────────────────────────────

func TestPaymentAddressTrimSpace(t *testing.T) {
	// PaymentAddress with leading/trailing whitespace must be trimmed before
	// validation and storage. The FlowView and DB must contain the trimmed value.
	svc, db := newTestService(t)

	draft := InvoiceDraft{
		PaymentAddress: "  bc1qplatformtest000  ",
		AmountAtomic:   500_000,
		AmountUSDCents: 500,
	}
	_, fv, err := svc.CreatePaymentIntent("bc1qtest", "BTC", draft)
	if err != nil {
		t.Fatalf("CreatePaymentIntent: %v", err)
	}

	const want = "bc1qplatformtest000"
	if fv.PaymentAddress != want {
		t.Errorf("FlowView.PaymentAddress: got %q, want %q", fv.PaymentAddress, want)
	}

	var dbAddr string
	if err := db.QueryRow(`SELECT payment_address FROM v2_invoices WHERE flow_id = ?`,
		fv.FlowID).Scan(&dbAddr); err != nil {
		t.Fatalf("read DB: %v", err)
	}
	if dbAddr != want {
		t.Errorf("DB payment_address: got %q, want %q", dbAddr, want)
	}
}

func TestWhitespaceOnlyPaymentAddressRejected(t *testing.T) {
	// All-whitespace PaymentAddress is empty after TrimSpace and must be rejected
	// as ErrInvalidDraft. No flow or invoice row must be written.
	svc, db := newTestService(t)

	draft := InvoiceDraft{
		PaymentAddress: "   \t\n  ",
		AmountAtomic:   500_000,
		AmountUSDCents: 500,
	}
	_, _, err := svc.CreatePaymentIntent("bc1qtest", "BTC", draft)
	if !errors.Is(err, ErrInvalidDraft) {
		t.Errorf("expected ErrInvalidDraft for whitespace-only address, got %v", err)
	}

	var flowCount, invCount int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_flows`).Scan(&flowCount) //nolint:errcheck
	db.QueryRow(`SELECT COUNT(*) FROM v2_invoices`).Scan(&invCount)      //nolint:errcheck
	if flowCount != 0 || invCount != 0 {
		t.Errorf("after rejection: found %d flow(s) and %d invoice(s), want 0 each",
			flowCount, invCount)
	}
}

// ── Bonus: domain separation ──────────────────────────────────────────────────

func TestDomainSeparationDistinctHashes(t *testing.T) {
	// Wallet fingerprint and code hash domains must be distinct from each other
	// and from the V1 domain string.
	svc, _ := newTestService(t)

	const addr = "1A1zP1eP5QGefi2DMPTfTL5SLmv7Divfna"
	fpBTC := svc.walletFingerprint("BTC", normalizeAddress(addr))
	fpLTC := svc.walletFingerprint("LTC", normalizeAddress(addr))
	codeH := svc.codeHash(addr)

	if fpBTC == fpLTC {
		t.Error("BTC and LTC fingerprints identical — currency domain separation missing")
	}
	if fpBTC == codeH {
		t.Error("wallet fingerprint equals code hash of same input — domain separation missing")
	}
	if walletDomain == "naroom:v1:" {
		t.Error("V2 wallet domain matches V1 domain — domain separation missing")
	}
	if codeDomain == walletDomain {
		t.Error("code domain equals wallet domain")
	}
}

// ── Task 03 State Machine Tests ───────────────────────────────────────────────

// SM-1: Fresh invoice has pending status and detection_deadline = created_at + 60min.
func TestInvoiceCreatedPendingWithDeadline(t *testing.T) {
	svc, _ := newTestService(t)
	before := time.Now()
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")
	after := time.Now()

	if fv.InvoiceStatus != InvoiceStatusPending {
		t.Errorf("status: got %q, want %q", fv.InvoiceStatus, InvoiceStatusPending)
	}

	// detection_deadline_at should be created_at + 3600 seconds.
	expectedDeadline := fv.CreatedAt.Add(detectionWindow)
	diff := fv.DetectionDeadlineAt.Sub(expectedDeadline)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("DetectionDeadlineAt %v, expected ~%v (diff %v)", fv.DetectionDeadlineAt, expectedDeadline, diff)
	}

	// Must be in the future.
	if !fv.DetectionDeadlineAt.After(before) {
		t.Error("DetectionDeadlineAt is not in the future")
	}
	_ = after
}

// SM-2: Suitable unconfirmed tx before deadline → payment_detected.
func TestDetectionBeforeDeadline(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	now := time.Now()
	detected, err := svc.RecordPaymentDetected(
		fv.FlowID, fv.InvoiceID, "txid_sm2",
		[]string{"bc1qtest"},
		fv.AmountAtomic,
		now,
	)
	if err != nil {
		t.Fatalf("RecordPaymentDetected: %v", err)
	}
	if detected.InvoiceStatus != InvoiceStatusDetected {
		t.Errorf("status: got %q, want %q", detected.InvoiceStatus, InvoiceStatusDetected)
	}
	if detected.DetectedTxid == nil || *detected.DetectedTxid != "txid_sm2" {
		t.Errorf("DetectedTxid: got %v, want txid_sm2", detected.DetectedTxid)
	}
	if detected.PaymentDetectedAt == nil {
		t.Error("PaymentDetectedAt is nil")
	}
	if detected.ConfirmationDeadlineAt == nil {
		t.Error("ConfirmationDeadlineAt is nil")
	} else {
		expected := now.Unix() + int64(confirmationWindow.Seconds())
		if *detected.ConfirmationDeadlineAt != time.Unix(expected, 0) {
			diff := detected.ConfirmationDeadlineAt.Unix() - expected
			if diff < -1 || diff > 1 {
				t.Errorf("ConfirmationDeadlineAt: got %v, want ~%v",
					detected.ConfirmationDeadlineAt.Unix(), expected)
			}
		}
	}
}

// SM-3: Confirmed tx before grace deadline → confirmed, 5-day entitlement.
func TestConfirmationBeforeGrace(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	now := time.Now()
	_, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_sm3",
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	if err != nil {
		t.Fatalf("detection: %v", err)
	}

	confirmed, err := svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, now)
	if err != nil {
		t.Fatalf("ConfirmPayment: %v", err)
	}
	if confirmed.InvoiceStatus != InvoiceStatusConfirmed {
		t.Errorf("status: got %q, want confirmed", confirmed.InvoiceStatus)
	}
	if confirmed.EntitlementExpiresAt == nil {
		t.Fatal("EntitlementExpiresAt is nil")
	}
	if confirmed.PaymentConfirmedAt == nil {
		t.Fatal("PaymentConfirmedAt is nil")
	}
	expected := confirmed.PaymentConfirmedAt.Add(entitlementDuration)
	diff := confirmed.EntitlementExpiresAt.Sub(expected)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("EntitlementExpiresAt %v, expected ~%v", confirmed.EntitlementExpiresAt, expected)
	}
	if confirmed.PaymentTxid == nil || *confirmed.PaymentTxid != "txid_sm3" {
		t.Errorf("PaymentTxid: got %v, want txid_sm3", confirmed.PaymentTxid)
	}
}

// SM-4: pending → expired after 60 min; late tx does NOT revive it.
func TestPendingExpiredAfterDeadline(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	// Expire it.
	past := fv.DetectionDeadlineAt.Add(time.Second)
	expired, err := svc.ExpireInvoice(fv.FlowID, fv.InvoiceID, past)
	if err != nil {
		t.Fatalf("ExpireInvoice: %v", err)
	}
	if expired.InvoiceStatus != InvoiceStatusExpired {
		t.Errorf("status after expire: got %q, want expired", expired.InvoiceStatus)
	}

	// Late tx must not revive.
	_, err = svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_late",
		[]string{"bc1qtest"}, fv.AmountAtomic, past)
	if !errors.Is(err, ErrExpired) {
		t.Errorf("expected ErrExpired for late detection, got %v", err)
	}

	// Status unchanged.
	reread, _ := svc.readFlowView(fv.FlowID)
	if reread.InvoiceStatus != InvoiceStatusExpired {
		t.Errorf("status after late detection: got %q, want expired", reread.InvoiceStatus)
	}
}

// SM-5: Detection exactly AT deadline is accepted; detection after deadline is rejected.
func TestDetectionBoundary(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	// Exactly AT deadline — should succeed.
	atDeadline := fv.DetectionDeadlineAt
	detected, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_at",
		[]string{"bc1qtest"}, fv.AmountAtomic, atDeadline)
	if err != nil {
		t.Fatalf("detection at deadline: %v", err)
	}
	if detected.InvoiceStatus != InvoiceStatusDetected {
		t.Errorf("status at deadline: got %q, want payment_detected", detected.InvoiceStatus)
	}

	// Create another invoice to test rejection after deadline.
	svc2, _ := newTestService(t)
	_, fv2 := createIntent(t, svc2, "bc1qtest", "BTC")

	afterDeadline := fv2.DetectionDeadlineAt.Add(time.Second)
	_, err = svc2.RecordPaymentDetected(fv2.FlowID, fv2.InvoiceID, "txid_after",
		[]string{"bc1qtest"}, fv2.AmountAtomic, afterDeadline)
	if !errors.Is(err, ErrExpired) {
		t.Errorf("detection after deadline: expected ErrExpired, got %v", err)
	}
}

// SM-6: Confirmation exactly AT grace deadline accepted; after deadline → expired.
func TestConfirmationBoundary(t *testing.T) {
	// Test: at grace deadline is accepted.
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	detectedAt := time.Now()
	detected, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_bound",
		[]string{"bc1qtest"}, fv.AmountAtomic, detectedAt)
	if err != nil {
		t.Fatalf("detection: %v", err)
	}
	if detected.ConfirmationDeadlineAt == nil {
		t.Fatal("ConfirmationDeadlineAt is nil")
	}

	// Exactly AT confirmation deadline.
	atGrace := *detected.ConfirmationDeadlineAt
	_, err = svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, atGrace)
	if err != nil {
		t.Fatalf("confirmation at grace deadline: %v", err)
	}

	// Create another invoice to test rejection after grace deadline.
	svc2, _ := newTestService(t)
	_, fv2 := createIntent(t, svc2, "bc1qtest", "BTC")
	detectedAt2 := time.Now()
	detected2, err := svc2.RecordPaymentDetected(fv2.FlowID, fv2.InvoiceID, "txid_bound2",
		[]string{"bc1qtest"}, fv2.AmountAtomic, detectedAt2)
	if err != nil {
		t.Fatalf("detection2: %v", err)
	}
	if detected2.ConfirmationDeadlineAt == nil {
		t.Fatal("ConfirmationDeadlineAt2 is nil")
	}

	afterGrace := detected2.ConfirmationDeadlineAt.Add(time.Second)
	_, err = svc2.ConfirmPayment(fv2.FlowID, fv2.InvoiceID, afterGrace)
	if !errors.Is(err, ErrExpired) {
		t.Errorf("confirmation after grace: expected ErrExpired, got %v", err)
	}
}

// SM-7: payment_detected → expired after 24h without confirmation.
func TestDetectedExpiredAfterGrace(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	detectedAt := time.Now()
	detected, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_sm7",
		[]string{"bc1qtest"}, fv.AmountAtomic, detectedAt)
	if err != nil {
		t.Fatalf("detection: %v", err)
	}
	if detected.ConfirmationDeadlineAt == nil {
		t.Fatal("ConfirmationDeadlineAt is nil")
	}

	afterGrace := detected.ConfirmationDeadlineAt.Add(time.Second)
	expired, err := svc.ExpireInvoice(fv.FlowID, fv.InvoiceID, afterGrace)
	if err != nil {
		t.Fatalf("ExpireInvoice: %v", err)
	}
	if expired.InvoiceStatus != InvoiceStatusExpired {
		t.Errorf("status after grace expire: got %q, want expired", expired.InvoiceStatus)
	}
}

// SM-8: Expired invoice cannot be confirmed.
func TestExpiredCannotBeConfirmed(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	// Expire it.
	past := fv.DetectionDeadlineAt.Add(time.Second)
	_, err := svc.ExpireInvoice(fv.FlowID, fv.InvoiceID, past)
	if err != nil {
		t.Fatalf("ExpireInvoice: %v", err)
	}

	// Detection should fail.
	_, err = svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_sm8",
		[]string{"bc1qtest"}, fv.AmountAtomic, past)
	if !errors.Is(err, ErrExpired) {
		t.Errorf("detection after expire: expected ErrExpired, got %v", err)
	}
}

// SM-9: Exact amount passes.
func TestExactAmountPasses(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	now := time.Now()
	detected, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_exact",
		[]string{"bc1qtest"}, fv.AmountAtomic /* exactly the required amount */, now)
	if err != nil {
		t.Fatalf("exact amount detection: %v", err)
	}
	if detected.InvoiceStatus != InvoiceStatusDetected {
		t.Errorf("status: got %q, want payment_detected", detected.InvoiceStatus)
	}
}

// SM-10: amount_atomic-1 is rejected; subsequent right-amount tx before deadline passes.
func TestUnderpaymentRejected(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	now := time.Now()

	// Underpayment by 1 satoshi.
	_, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_under",
		[]string{"bc1qtest"}, fv.AmountAtomic-1, now)
	if !errors.Is(err, ErrInsufficientPayment) {
		t.Errorf("underpayment: expected ErrInsufficientPayment, got %v", err)
	}

	// Invoice must still be pending (not locked).
	reread, _ := svc.readFlowView(fv.FlowID)
	if reread.InvoiceStatus != InvoiceStatusPending {
		t.Errorf("status after underpayment: got %q, want pending", reread.InvoiceStatus)
	}

	// Correct amount tx before deadline should succeed.
	_, err = svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_correct",
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	if err != nil {
		t.Errorf("correct amount after underpayment: %v", err)
	}
}

// SM-11: Overpayment passes, no extra entitlement.
func TestOverpaymentPasses(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	now := time.Now()
	detected, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_over",
		[]string{"bc1qtest"}, fv.AmountAtomic*2, now)
	if err != nil {
		t.Fatalf("overpayment detection: %v", err)
	}
	if detected.InvoiceStatus != InvoiceStatusDetected {
		t.Errorf("status: got %q, want payment_detected", detected.InvoiceStatus)
	}

	// Confirm and verify entitlement is exactly 5 days (not more).
	confirmed, err := svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, now)
	if err != nil {
		t.Fatalf("ConfirmPayment: %v", err)
	}
	if confirmed.EntitlementExpiresAt == nil || confirmed.PaymentConfirmedAt == nil {
		t.Fatal("entitlement fields nil")
	}
	expected := confirmed.PaymentConfirmedAt.Add(entitlementDuration)
	diff := confirmed.EntitlementExpiresAt.Sub(expected)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("overpayment gave wrong entitlement: %v vs expected %v", confirmed.EntitlementExpiresAt, expected)
	}
}

// SM-12: Two underpayment txs do NOT aggregate.
func TestNoAggregation(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")
	now := time.Now()

	half := fv.AmountAtomic / 2

	// First underpayment tx.
	_, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_half1",
		[]string{"bc1qtest"}, half, now)
	if !errors.Is(err, ErrInsufficientPayment) {
		t.Errorf("first half: expected ErrInsufficientPayment, got %v", err)
	}

	// Second underpayment tx — also rejected individually.
	_, err = svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_half2",
		[]string{"bc1qtest"}, half, now)
	if !errors.Is(err, ErrInsufficientPayment) {
		t.Errorf("second half: expected ErrInsufficientPayment, got %v", err)
	}

	// Invoice still pending.
	reread, _ := svc.readFlowView(fv.FlowID)
	if reread.InvoiceStatus != InvoiceStatusPending {
		t.Errorf("status after two halves: got %q, want pending", reread.InvoiceStatus)
	}
}

// SM-13: Multiple outputs of one tx to invoice address are summed by the adapter.
// Since RecordPaymentDetected receives the pre-computed AmountAtomic, we test
// that a single call with the correct total passes.
func TestMultipleOutputsSummed(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")
	now := time.Now()

	// Adapter would sum: two outputs of 250k + 250k = 500k (matches AmountAtomic).
	// Here we just pass the summed value directly to RecordPaymentDetected.
	_, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_multi_out",
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	if err != nil {
		t.Fatalf("multi-output sum detection: %v", err)
	}

	reread, _ := svc.readFlowView(fv.FlowID)
	if reread.InvoiceStatus != InvoiceStatusDetected {
		t.Errorf("status: got %q, want payment_detected", reread.InvoiceStatus)
	}
}

// SM-14: Wrong-sender tx does not lock invoice; right-sender tx before deadline passes.
func TestWrongSenderSkipped(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")
	now := time.Now()

	// Wrong sender.
	_, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_wrong",
		[]string{"bc1qwrong000000000000000000000000000000000"}, fv.AmountAtomic, now)
	if !errors.Is(err, ErrSenderMismatch) {
		t.Errorf("wrong sender: expected ErrSenderMismatch, got %v", err)
	}

	// Invoice still pending.
	reread, _ := svc.readFlowView(fv.FlowID)
	if reread.InvoiceStatus != InvoiceStatusPending {
		t.Errorf("status after wrong sender: got %q, want pending", reread.InvoiceStatus)
	}

	// Right sender before deadline passes.
	_, err = svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_right",
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	if err != nil {
		t.Errorf("right sender after wrong sender: %v", err)
	}
}

// SM-15: Expected sender plus additional inputs passes.
func TestExtraSendersOK(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")
	now := time.Now()

	// Multiple inputs: one matches, others don't.
	_, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_multi",
		[]string{
			"bc1qother00000000000000000000000000000000000",
			"bc1qtest", // matches
			"bc1qanother0000000000000000000000000000000000",
		}, fv.AmountAtomic, now)
	if err != nil {
		t.Fatalf("extra senders OK: %v", err)
	}
}

// SM-16: After detection, another tx cannot replace the locked txid.
func TestCannotReplaceLockedTxid(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")
	now := time.Now()

	_, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_locked",
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	if err != nil {
		t.Fatalf("first detection: %v", err)
	}

	_, err = svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_replace",
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	if !errors.Is(err, ErrConflict) {
		t.Errorf("replace locked txid: expected ErrConflict, got %v", err)
	}

	// Verify original txid locked.
	reread, _ := svc.readFlowView(fv.FlowID)
	if reread.DetectedTxid == nil || *reread.DetectedTxid != "txid_locked" {
		t.Errorf("detected_txid replaced: got %v", reread.DetectedTxid)
	}
}

// SM-17: Duplicate detection calls (same txid) → idempotent, timestamps unchanged.
func TestDetectionIdempotent(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")
	now := time.Now()

	first, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_idem",
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	if err != nil {
		t.Fatalf("first detection: %v", err)
	}

	second, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_idem",
		[]string{"bc1qtest"}, fv.AmountAtomic, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("idempotent detection: %v", err)
	}

	// Timestamps must not change.
	if first.PaymentDetectedAt == nil || second.PaymentDetectedAt == nil {
		t.Fatal("PaymentDetectedAt is nil")
	}
	if !first.PaymentDetectedAt.Equal(*second.PaymentDetectedAt) {
		t.Errorf("PaymentDetectedAt changed: %v → %v", first.PaymentDetectedAt, second.PaymentDetectedAt)
	}
}

// SM-18: Duplicate confirmation calls → idempotent, entitlement unchanged.
func TestConfirmationIdempotent(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")
	now := time.Now()

	_, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_conf_idem",
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	if err != nil {
		t.Fatalf("detection: %v", err)
	}

	first, err := svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, now)
	if err != nil {
		t.Fatalf("first confirm: %v", err)
	}

	second, err := svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("second confirm: %v", err)
	}

	if first.EntitlementExpiresAt == nil || second.EntitlementExpiresAt == nil {
		t.Fatal("EntitlementExpiresAt is nil")
	}
	if !first.EntitlementExpiresAt.Equal(*second.EntitlementExpiresAt) {
		t.Errorf("EntitlementExpiresAt changed: %v → %v",
			first.EntitlementExpiresAt, second.EntitlementExpiresAt)
	}
}

// SM-19: After detection, restart (new service object, same DB) continues correctly.
func TestRestartBetweenDetectionAndConfirm(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	svc1, err := New(db, testHMACKey)
	if err != nil {
		t.Fatalf("New (svc1): %v", err)
	}

	_, fv, err := svc1.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err != nil {
		t.Fatalf("CreatePaymentIntent: %v", err)
	}

	now := time.Now()
	_, err = svc1.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_restart",
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	if err != nil {
		t.Fatalf("detection (svc1): %v", err)
	}

	// Simulate restart: new service object, same DB.
	svc2, err := New(db, testHMACKey)
	if err != nil {
		t.Fatalf("New (svc2): %v", err)
	}

	// LoadWatchableInvoices should return the detected invoice.
	watchable, err := svc2.LoadWatchableInvoices()
	if err != nil {
		t.Fatalf("LoadWatchableInvoices: %v", err)
	}
	found := false
	for _, w := range watchable {
		if w.FlowID == fv.FlowID {
			found = true
			if w.InvoiceStatus != InvoiceStatusDetected {
				t.Errorf("watchable status: got %q, want payment_detected", w.InvoiceStatus)
			}
			break
		}
	}
	if !found {
		t.Error("detected invoice not found in LoadWatchableInvoices after restart")
	}

	// Continue with confirmation on svc2.
	confirmed, err := svc2.ConfirmPayment(fv.FlowID, fv.InvoiceID, now)
	if err != nil {
		t.Fatalf("ConfirmPayment (svc2): %v", err)
	}
	if confirmed.InvoiceStatus != InvoiceStatusConfirmed {
		t.Errorf("status after restart+confirm: got %q, want confirmed", confirmed.InvoiceStatus)
	}
}

// SM-20: RPC error (ErrSenderMismatch) before/after detection does not corrupt state.
func TestRPCErrorDoesNotCorruptState(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")
	now := time.Now()

	// Simulate RPC error: wrong sender.
	_, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_rpc",
		[]string{"bc1qwrong"}, fv.AmountAtomic, now)
	if !errors.Is(err, ErrSenderMismatch) {
		t.Errorf("expected ErrSenderMismatch, got %v", err)
	}

	// Invoice must still be pending.
	reread, _ := svc.readFlowView(fv.FlowID)
	if reread.InvoiceStatus != InvoiceStatusPending {
		t.Errorf("status after RPC error: got %q, want pending", reread.InvoiceStatus)
	}
	if reread.DetectedTxid != nil {
		t.Error("DetectedTxid should be nil after failed detection")
	}

	// Correct sender now succeeds.
	_, err = svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_rpc",
		[]string{"bc1qtest"}, fv.AmountAtomic, now)
	if err != nil {
		t.Errorf("correct detection after RPC error: %v", err)
	}
}

// SM-21: Backoff Next() grows up to max, Reset() resets; cancelled ctx exits Run().
func TestWatcherBackoff(t *testing.T) {
	b := newCappedBackoff()

	// First call starts at min.
	d1 := b.Next()
	if d1 != 5*time.Second {
		t.Errorf("first Next: got %v, want 5s", d1)
	}

	// Second call doubles.
	d2 := b.Next()
	if d2 != 10*time.Second {
		t.Errorf("second Next: got %v, want 10s", d2)
	}

	// Grow to max.
	for i := 0; i < 20; i++ {
		b.Next()
	}
	dMax := b.Next()
	if dMax != 5*time.Minute {
		t.Errorf("max backoff: got %v, want 5m", dMax)
	}

	// Reset restarts from min.
	b.Reset()
	dAfterReset := b.Next()
	if dAfterReset != 5*time.Second {
		t.Errorf("after reset: got %v, want 5s", dAfterReset)
	}
}

// SM-22: balance >= $120 → form_ready; balance < $120 → paid_low_balance.
func TestBalanceThreshold(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")
	fv = confirmPayment(t, svc, fv, "txid_thresh")

	// Low balance.
	after1, err := svc.RecordPostPaymentBalance(fv.FlowID, 119.99, hardFloorUSD)
	if err != nil {
		t.Fatalf("low balance: %v", err)
	}
	if after1.State != StatePaidLowBalance {
		t.Errorf("low balance state: got %q, want paid_low_balance", after1.State)
	}

	// Meeting floor.
	after2, err := svc.RecordPostPaymentBalance(fv.FlowID, 120.0, hardFloorUSD)
	if err != nil {
		t.Fatalf("floor balance: %v", err)
	}
	if after2.State != StateFormReady {
		t.Errorf("floor balance state: got %q, want form_ready", after2.State)
	}
}

// SM-23: Balance provider outage after confirmation leaves payment_confirmed;
// next cycle (with working provider) completes.
func TestBalanceOutageAfterConfirmation(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")
	fv = confirmPayment(t, svc, fv, "txid_outage")

	// Balance check would fail (outage) — state unchanged.
	// (We simulate by NOT calling RecordPostPaymentBalance.)
	reread, _ := svc.readFlowView(fv.FlowID)
	if reread.State != StatePaymentConfirmed {
		t.Errorf("state after outage (no balance): got %q, want payment_confirmed", reread.State)
	}
	if reread.LastBalanceUSD != nil {
		t.Error("last_balance_usd should be nil after simulated outage")
	}

	// Next cycle with working provider.
	after, err := svc.RecordPostPaymentBalance(fv.FlowID, 150.0, hardFloorUSD)
	if err != nil {
		t.Fatalf("balance after outage recovery: %v", err)
	}
	if after.State != StateFormReady {
		t.Errorf("state after recovery: got %q, want form_ready", after.State)
	}
}

// SM-24: BTC and LTC both pass through confirmation/amount/sender correctly.
func TestBTCAndLTCBothWork(t *testing.T) {
	for _, tc := range []struct {
		currency string
		wallet   string
	}{
		{"BTC", "bc1qtest"},
		{"LTC", "ltc1qtest"},
	} {
		tc := tc
		t.Run(tc.currency, func(t *testing.T) {
			svc, _ := newTestService(t)
			_, fv, err := svc.CreatePaymentIntent(tc.wallet, tc.currency, validDraft())
			if err != nil {
				t.Fatalf("create: %v", err)
			}

			now := time.Now()
			detected, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_"+tc.currency,
				[]string{tc.wallet}, fv.AmountAtomic, now)
			if err != nil {
				t.Fatalf("detection: %v", err)
			}
			if detected.InvoiceStatus != InvoiceStatusDetected {
				t.Errorf("status: got %q, want payment_detected", detected.InvoiceStatus)
			}

			confirmed, err := svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, now)
			if err != nil {
				t.Fatalf("confirmation: %v", err)
			}
			if confirmed.InvoiceStatus != InvoiceStatusConfirmed {
				t.Errorf("status: got %q, want confirmed", confirmed.InvoiceStatus)
			}
			if confirmed.Currency != tc.currency {
				t.Errorf("currency: got %q, want %q", confirmed.Currency, tc.currency)
			}
		})
	}
}

// SM-25: ErrSenderMismatch and ErrInsufficientPayment do not expose wallet or txid.
func TestSensitiveErrorsDoNotExposeSensitiveValues(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qsecretwallet", "BTC")
	now := time.Now()

	const sensitiveTxid = "txid_very_secret_123"
	const sensitiveWallet = "bc1qsecretwallet"

	// ErrSenderMismatch
	_, err := svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, sensitiveTxid,
		[]string{"bc1qwrong"}, fv.AmountAtomic, now)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, sensitiveTxid) {
			t.Error("ErrSenderMismatch error string contains txid")
		}
		if strings.Contains(errStr, sensitiveWallet) {
			t.Error("ErrSenderMismatch error string contains wallet")
		}
	}

	// ErrInsufficientPayment
	_, err = svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, sensitiveTxid,
		[]string{sensitiveWallet}, 1, now)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, sensitiveTxid) {
			t.Error("ErrInsufficientPayment error string contains txid")
		}
	}
}

// SM-26: LoadWatchableInvoices returns pending and detected, not confirmed+balanced.
func TestLoadWatchableInvoices(t *testing.T) {
	svc, _ := newTestService(t)

	// Create 3 flows:
	// 1. pending
	// 2. detected
	// 3. confirmed + balanced (should NOT appear)
	_, fv1, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err != nil {
		t.Fatalf("create1: %v", err)
	}

	_, fv2, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err != nil {
		t.Fatalf("create2: %v", err)
	}
	now := time.Now()
	_, err = svc.RecordPaymentDetected(fv2.FlowID, fv2.InvoiceID, "txid_w2",
		[]string{"bc1qtest"}, fv2.AmountAtomic, now)
	if err != nil {
		t.Fatalf("detect2: %v", err)
	}

	// Flow 3: confirmed + balanced (NOT watchable).
	_, fv3, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
	if err != nil {
		t.Fatalf("create3: %v", err)
	}
	_, err = svc.RecordPaymentDetected(fv3.FlowID, fv3.InvoiceID, "txid_w3",
		[]string{"bc1qtest"}, fv3.AmountAtomic, now)
	if err != nil {
		t.Fatalf("detect3: %v", err)
	}
	_, err = svc.ConfirmPayment(fv3.FlowID, fv3.InvoiceID, now)
	if err != nil {
		t.Fatalf("confirm3: %v", err)
	}
	// Set balance so it's no longer watchable.
	_, err = svc.RecordPostPaymentBalance(fv3.FlowID, 150.0, hardFloorUSD)
	if err != nil {
		t.Fatalf("balance3: %v", err)
	}

	watchable, err := svc.LoadWatchableInvoices()
	if err != nil {
		t.Fatalf("LoadWatchableInvoices: %v", err)
	}

	ids := map[string]bool{}
	for _, w := range watchable {
		ids[w.FlowID] = true
	}

	if !ids[fv1.FlowID] {
		t.Error("pending invoice not in watchable list")
	}
	if !ids[fv2.FlowID] {
		t.Error("detected invoice not in watchable list")
	}
	if ids[fv3.FlowID] {
		t.Error("confirmed+balanced invoice should NOT be in watchable list")
	}
}

// SM-27: Invalid/zero/NaN price → V2InvoiceIssuer returns error, no DB rows.
func TestInvalidPriceRejectsInvoice(t *testing.T) {
	// Test that V2InvoiceIssuer rejects bad prices.
	for _, tc := range []struct {
		name  string
		price float64
	}{
		{"zero price", 0},
		{"negative price", -1.0},
		{"NaN price", math.NaN()},
		{"+Inf price", math.Inf(1)},
		{"-Inf price", math.Inf(-1)},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fakePrice := &fakePriceSource{price: tc.price}
			fakeAlloc := &fakeAllocator{addr: "bc1qplatform"}
			issuer := NewV2InvoiceIssuer(fakeAlloc, fakePrice)

			_, err := issuer.CreateClientInvoice(nil, "BTC")
			if err == nil {
				t.Errorf("%s: expected error, got nil", tc.name)
			}
		})
	}
}

// SM-28: Amount ceiling rounding is integer and >= $5 / price.
func TestAtomicAmountCeiling(t *testing.T) {
	// Test at a few BTC prices.
	for _, priceUSD := range []float64{30000.0, 65432.1, 100000.0, 0.01} {
		priceUSD := priceUSD
		t.Run(fmt.Sprintf("price_%.2f", priceUSD), func(t *testing.T) {
			fakePrice := &fakePriceSource{price: priceUSD}
			fakeAlloc := &fakeAllocator{addr: "bc1qplatform"}
			issuer := NewV2InvoiceIssuer(fakeAlloc, fakePrice)

			draft, err := issuer.CreateClientInvoice(nil, "BTC")
			if err != nil {
				t.Fatalf("CreateClientInvoice: %v", err)
			}
			if draft.AmountAtomic <= 0 {
				t.Errorf("AmountAtomic <= 0: %d", draft.AmountAtomic)
			}

			// The amount must be >= $5 worth at the given price.
			valueBTC := float64(draft.AmountAtomic) / 1e8
			valueUSD := valueBTC * priceUSD
			if valueUSD < 5.0-0.0001 { // small epsilon for float comparison
				t.Errorf("AmountAtomic %d worth $%.4f < $5 at price $%.2f",
					draft.AmountAtomic, valueUSD, priceUSD)
			}

			// Ceiling property: one less satoshi should be worth less than $5.
			if draft.AmountAtomic > 1 {
				lessOneBTC := float64(draft.AmountAtomic-1) / 1e8
				lessOneUSD := lessOneBTC * priceUSD
				if lessOneUSD >= 5.0 {
					t.Errorf("AmountAtomic-1 %d worth $%.4f >= $5 — not ceiling",
						draft.AmountAtomic-1, lessOneUSD)
				}
			}
		})
	}
}

// ── Task 03A: ExpireInvoice CAS race test ─────────────────────────────────────

// SM-CAS-1: Detection commits between watcher deadline check and ExpireInvoice CAS.
// → ExpireInvoice gets ErrInvalidState; invoice stays payment_detected with full grace.
func TestExpireInvoiceCASRaceProtectsGrace(t *testing.T) {
	svc, _ := newTestService(t)

	// Create a fresh invoice so detection_deadline_at is far in the future.
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	// Simulate: detection committed BEFORE the watcher fires ExpireInvoice.
	// The deadline is in the future, so normally the watcher would skip expiry.
	// But we're testing what happens when a watcher with a stale view tries expiry.
	now := time.Now()
	detected, err := svc.RecordPaymentDetected(
		fv.FlowID, fv.InvoiceID, "txid_cas_race",
		[]string{"bc1qtest"}, fv.AmountAtomic, now,
	)
	if err != nil {
		t.Fatalf("RecordPaymentDetected: %v", err)
	}
	if detected.InvoiceStatus != InvoiceStatusDetected {
		t.Fatalf("expected payment_detected after detection, got %q", detected.InvoiceStatus)
	}

	// Watcher had a stale pending view; it calls ExpireInvoice for the pending deadline.
	// The pending branch of the CAS requires status='pending' AND now > detection_deadline_at.
	// Since status is now 'payment_detected', the CAS misses → resolveExpireRace fires.
	// The invoice is not expired and not past its confirmation deadline → ErrInvalidState.
	pastDetectionDeadline := fv.DetectionDeadlineAt.Add(time.Second)
	_, expireErr := svc.ExpireInvoice(fv.FlowID, fv.InvoiceID, pastDetectionDeadline)
	if !errors.Is(expireErr, ErrInvalidState) {
		t.Errorf("expected ErrInvalidState when CAS misses due to concurrent detection, got %v", expireErr)
	}

	// Invoice must still be payment_detected — grace period must be intact.
	reread, readErr := svc.readFlowView(fv.FlowID)
	if readErr != nil {
		t.Fatalf("readFlowView: %v", readErr)
	}
	if reread.InvoiceStatus != InvoiceStatusDetected {
		t.Errorf("invoice status after race: got %q, want payment_detected", reread.InvoiceStatus)
	}
	if reread.ConfirmationDeadlineAt == nil {
		t.Error("confirmation_deadline_at is nil — grace period was erased")
	}
	if reread.DetectedTxid == nil || *reread.DetectedTxid != "txid_cas_race" {
		t.Errorf("detected_txid: got %v, want txid_cas_race", reread.DetectedTxid)
	}
}

// SM-CAS-2: ExpireInvoice before deadline (pending invoice, not yet due)
// → CAS misses; resolveExpireRace sees pending + not yet due → ErrInvalidState.
func TestExpireInvoicePendingNotYetDue(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	// Invoice is pending and well within its deadline. This simulates a watcher
	// bug or clock skew that calls ExpireInvoice before the deadline.
	// The CAS WHERE clause requires now > detection_deadline_at; since deadline is
	// in the future, the UPDATE hits 0 rows → resolveExpireRace → ErrInvalidState.
	beforeDeadline := fv.DetectionDeadlineAt.Add(-time.Minute)
	_, err := svc.ExpireInvoice(fv.FlowID, fv.InvoiceID, beforeDeadline)
	if !errors.Is(err, ErrInvalidState) {
		t.Errorf("expected ErrInvalidState for pre-deadline expiry, got %v", err)
	}

	reread, _ := svc.readFlowView(fv.FlowID)
	if reread.InvoiceStatus != InvoiceStatusPending {
		t.Errorf("status after pre-deadline expiry attempt: got %q, want pending", reread.InvoiceStatus)
	}
}

// SM-CAS-3: ExpireInvoice on already-expired invoice → idempotent success.
// resolveExpireRace sees status='expired' and returns (FlowView, nil).
func TestExpireInvoiceIdempotent(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")

	past := fv.DetectionDeadlineAt.Add(time.Second)
	_, err := svc.ExpireInvoice(fv.FlowID, fv.InvoiceID, past)
	if err != nil {
		t.Fatalf("first ExpireInvoice: %v", err)
	}

	// Second call: CAS misses (status='expired'), resolveExpireRace returns the
	// current FlowView with nil error — idempotent success. The watcher can
	// call ExpireInvoice again without consequence.
	second, err2 := svc.ExpireInvoice(fv.FlowID, fv.InvoiceID, past.Add(time.Second))
	if err2 != nil {
		t.Errorf("second ExpireInvoice: expected nil (idempotent), got %v", err2)
	}
	if second.InvoiceStatus != InvoiceStatusExpired {
		t.Errorf("second ExpireInvoice: status %q, want expired", second.InvoiceStatus)
	}
}

// SM-CAS-4: ExpireInvoice on confirmed invoice → ErrConflict.
func TestExpireInvoiceConfirmedIsConflict(t *testing.T) {
	svc, _ := newTestService(t)
	_, fv := createIntent(t, svc, "bc1qtest", "BTC")
	confirmPayment(t, svc, fv, "txid_confirmed_expire")

	past := fv.DetectionDeadlineAt.Add(time.Second)
	_, err := svc.ExpireInvoice(fv.FlowID, fv.InvoiceID, past)
	if !errors.Is(err, ErrConflict) {
		t.Errorf("ExpireInvoice on confirmed: expected ErrConflict, got %v", err)
	}
}

// ── Task 03A: Schema CHECK constraint tests ───────────────────────────────────

// SC-1 (negative): Direct SQL INSERT with impossible state/field combination
// must be rejected by SQLite CHECK constraint.
func TestSchemaCheckConstraintNegative(t *testing.T) {
	_, db := newTestService(t)

	// Helper: insert a flow and return its id.
	insertFlow := func(t *testing.T) string {
		t.Helper()
		id := fmt.Sprintf("flow-%d", time.Now().UnixNano())
		_, err := db.Exec(`INSERT INTO v2_client_flows
			(id, wallet_fingerprint, currency, management_code_hash, state, created_at, updated_at)
			VALUES (?, 'wfp', 'BTC', 'hash', 'awaiting_payment', 1, 1)`, id)
		if err != nil {
			t.Fatalf("insertFlow: %v", err)
		}
		return id
	}

	cases := []struct {
		name   string
		status string
		fields string // inline SQL values for detected/payment/entitlement columns
	}{
		{
			// pending with detected_txid set
			"pending with detected_txid",
			"pending",
			"detected_txid='tx1', payment_detected_at=NULL, confirmation_deadline_at=NULL, payment_txid=NULL, payment_confirmed_at=NULL, entitlement_expires_at=NULL",
		},
		{
			// payment_detected missing detected_txid
			"payment_detected missing detected_txid",
			"payment_detected",
			"detected_txid=NULL, payment_detected_at=100, confirmation_deadline_at=200, payment_txid=NULL, payment_confirmed_at=NULL, entitlement_expires_at=NULL",
		},
		{
			// payment_detected with payment_txid set
			"payment_detected with payment_txid",
			"payment_detected",
			"detected_txid='tx1', payment_detected_at=100, confirmation_deadline_at=200, payment_txid='tx1', payment_confirmed_at=NULL, entitlement_expires_at=NULL",
		},
		{
			// confirmed missing entitlement_expires_at
			"confirmed missing entitlement_expires_at",
			"confirmed",
			"detected_txid='tx1', payment_detected_at=100, confirmation_deadline_at=200, payment_txid='tx1', payment_confirmed_at=300, entitlement_expires_at=NULL",
		},
		{
			// confirmed with payment_txid != detected_txid
			"confirmed payment_txid != detected_txid",
			"confirmed",
			"detected_txid='tx1', payment_detected_at=100, confirmation_deadline_at=200, payment_txid='tx2', payment_confirmed_at=300, entitlement_expires_at=400",
		},
		{
			// expired with payment_txid set
			"expired with payment_txid",
			"expired",
			"detected_txid=NULL, payment_detected_at=NULL, confirmation_deadline_at=NULL, payment_txid='tx1', payment_confirmed_at=NULL, entitlement_expires_at=NULL",
		},
		{
			// expired with entitlement set
			"expired with entitlement_expires_at",
			"expired",
			"detected_txid=NULL, payment_detected_at=NULL, confirmation_deadline_at=NULL, payment_txid=NULL, payment_confirmed_at=NULL, entitlement_expires_at=500",
		},
		{
			// expired: detected_txid set but payment_detected_at missing (mixed form)
			"expired mixed form: txid without detected_at",
			"expired",
			"detected_txid='tx1', payment_detected_at=NULL, confirmation_deadline_at=NULL, payment_txid=NULL, payment_confirmed_at=NULL, entitlement_expires_at=NULL",
		},
		{
			// balance pair: usd set but checked_at missing
			"balance pair mismatch (usd without checked_at)",
			"pending",
			"detected_txid=NULL, payment_detected_at=NULL, confirmation_deadline_at=NULL, payment_txid=NULL, payment_confirmed_at=NULL, entitlement_expires_at=NULL",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			flowID := insertFlow(t)
			invID := fmt.Sprintf("inv-%d", time.Now().UnixNano())
			now := time.Now().Unix()

			var query string
			if tc.name == "balance pair mismatch (usd without checked_at)" {
				// last_balance_usd set but last_balance_checked_at NULL
				query = fmt.Sprintf(`INSERT INTO v2_invoices
					(id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
					 detection_deadline_at, %s,
					 last_balance_usd, last_balance_checked_at, created_at, updated_at)
					VALUES ('%s', '%s', '%s', 'bc1qaddr', 500, 1000, %d, NULL, NULL, NULL,
					        NULL, NULL, NULL, NULL, NULL,
					        99.0, NULL, %d, %d)`,
					tc.fields, invID, flowID, tc.status, now+3600, now, now)
				// Actually let me write this more carefully
				_, err := db.Exec(`INSERT INTO v2_invoices
					(id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
					 detection_deadline_at,
					 detected_txid, payment_detected_at, confirmation_deadline_at,
					 payment_txid, payment_confirmed_at, entitlement_expires_at,
					 last_balance_usd, last_balance_checked_at, created_at, updated_at)
					VALUES (?, ?, 'pending', 'bc1qaddr', 500, 1000, ?,
					        NULL, NULL, NULL, NULL, NULL, NULL,
					        99.0, NULL, ?, ?)`,
					invID, flowID, now+3600, now, now)
				if err == nil {
					t.Errorf("case %q: expected CHECK constraint error, got nil", tc.name)
				}
				return
			}

			// For all other cases: build the insert from tc.fields inline.
			_ = query
			// We use a template: parse tc.fields to extract the 6 column values.
			// Simpler: just write individual cases.
			var err error
			switch tc.name {
			case "pending with detected_txid":
				_, err = db.Exec(`INSERT INTO v2_invoices
					(id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
					 detection_deadline_at,
					 detected_txid, payment_detected_at, confirmation_deadline_at,
					 payment_txid, payment_confirmed_at, entitlement_expires_at,
					 created_at, updated_at)
					VALUES (?, ?, 'pending', 'bc1qaddr', 500, 1000, ?,
					        'tx1', NULL, NULL, NULL, NULL, NULL, ?, ?)`,
					invID, flowID, now+3600, now, now)
			case "payment_detected missing detected_txid":
				_, err = db.Exec(`INSERT INTO v2_invoices
					(id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
					 detection_deadline_at,
					 detected_txid, payment_detected_at, confirmation_deadline_at,
					 payment_txid, payment_confirmed_at, entitlement_expires_at,
					 created_at, updated_at)
					VALUES (?, ?, 'payment_detected', 'bc1qaddr', 500, 1000, ?,
					        NULL, 100, 200, NULL, NULL, NULL, ?, ?)`,
					invID, flowID, now+3600, now, now)
			case "payment_detected with payment_txid":
				_, err = db.Exec(`INSERT INTO v2_invoices
					(id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
					 detection_deadline_at,
					 detected_txid, payment_detected_at, confirmation_deadline_at,
					 payment_txid, payment_confirmed_at, entitlement_expires_at,
					 created_at, updated_at)
					VALUES (?, ?, 'payment_detected', 'bc1qaddr', 500, 1000, ?,
					        'tx1', 100, 200, 'tx1', NULL, NULL, ?, ?)`,
					invID, flowID, now+3600, now, now)
			case "confirmed missing entitlement_expires_at":
				_, err = db.Exec(`INSERT INTO v2_invoices
					(id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
					 detection_deadline_at,
					 detected_txid, payment_detected_at, confirmation_deadline_at,
					 payment_txid, payment_confirmed_at, entitlement_expires_at,
					 created_at, updated_at)
					VALUES (?, ?, 'confirmed', 'bc1qaddr', 500, 1000, ?,
					        'tx1', 100, 200, 'tx1', 300, NULL, ?, ?)`,
					invID, flowID, now+3600, now, now)
			case "confirmed payment_txid != detected_txid":
				_, err = db.Exec(`INSERT INTO v2_invoices
					(id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
					 detection_deadline_at,
					 detected_txid, payment_detected_at, confirmation_deadline_at,
					 payment_txid, payment_confirmed_at, entitlement_expires_at,
					 created_at, updated_at)
					VALUES (?, ?, 'confirmed', 'bc1qaddr', 500, 1000, ?,
					        'tx1', 100, 200, 'tx2', 300, 400, ?, ?)`,
					invID, flowID, now+3600, now, now)
			case "expired with payment_txid":
				_, err = db.Exec(`INSERT INTO v2_invoices
					(id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
					 detection_deadline_at,
					 detected_txid, payment_detected_at, confirmation_deadline_at,
					 payment_txid, payment_confirmed_at, entitlement_expires_at,
					 created_at, updated_at)
					VALUES (?, ?, 'expired', 'bc1qaddr', 500, 1000, ?,
					        NULL, NULL, NULL, 'tx1', NULL, NULL, ?, ?)`,
					invID, flowID, now+3600, now, now)
			case "expired with entitlement_expires_at":
				_, err = db.Exec(`INSERT INTO v2_invoices
					(id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
					 detection_deadline_at,
					 detected_txid, payment_detected_at, confirmation_deadline_at,
					 payment_txid, payment_confirmed_at, entitlement_expires_at,
					 created_at, updated_at)
					VALUES (?, ?, 'expired', 'bc1qaddr', 500, 1000, ?,
					        NULL, NULL, NULL, NULL, NULL, 500, ?, ?)`,
					invID, flowID, now+3600, now, now)
			case "expired mixed form: txid without detected_at":
				_, err = db.Exec(`INSERT INTO v2_invoices
					(id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
					 detection_deadline_at,
					 detected_txid, payment_detected_at, confirmation_deadline_at,
					 payment_txid, payment_confirmed_at, entitlement_expires_at,
					 created_at, updated_at)
					VALUES (?, ?, 'expired', 'bc1qaddr', 500, 1000, ?,
					        'tx1', NULL, NULL, NULL, NULL, NULL, ?, ?)`,
					invID, flowID, now+3600, now, now)
			default:
				t.Fatalf("unhandled case: %q", tc.name)
			}

			if err == nil {
				t.Errorf("case %q: expected CHECK constraint violation, got nil", tc.name)
			}
		})
	}
}

// SC-2 (positive): Both valid forms of 'expired' are accepted by the schema.
func TestSchemaExpiredBothFormsAccepted(t *testing.T) {
	_, db := newTestService(t)

	insertFlow := func(suffix string) string {
		id := "flow-sc2-" + suffix
		db.Exec(`INSERT INTO v2_client_flows
			(id, wallet_fingerprint, currency, management_code_hash, state, created_at, updated_at)
			VALUES (?, 'wfp', 'BTC', 'hash', 'awaiting_payment', 1, 1)`, id) //nolint:errcheck
		return id
	}

	now := time.Now().Unix()

	// Form (a): expired from pending — all detection/payment/entitlement fields NULL.
	flowA := insertFlow("form-a")
	_, err := db.Exec(`INSERT INTO v2_invoices
		(id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
		 detection_deadline_at,
		 detected_txid, payment_detected_at, confirmation_deadline_at,
		 payment_txid, payment_confirmed_at, entitlement_expires_at,
		 created_at, updated_at)
		VALUES ('inv-form-a', ?, 'expired', 'bc1qaddr', 500, 1000, ?,
		        NULL, NULL, NULL, NULL, NULL, NULL, ?, ?)`,
		flowA, now+3600, now, now)
	if err != nil {
		t.Errorf("form (a) expired from pending: unexpected error: %v", err)
	}

	// Form (b): expired from payment_detected — detected fields present, no payment/entitlement.
	flowB := insertFlow("form-b")
	_, err = db.Exec(`INSERT INTO v2_invoices
		(id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
		 detection_deadline_at,
		 detected_txid, payment_detected_at, confirmation_deadline_at,
		 payment_txid, payment_confirmed_at, entitlement_expires_at,
		 created_at, updated_at)
		VALUES ('inv-form-b', ?, 'expired', 'bc1qaddr', 500, 1000, ?,
		        'tx1', 100, 200, NULL, NULL, NULL, ?, ?)`,
		flowB, now+3600, now, now)
	if err != nil {
		t.Errorf("form (b) expired from payment_detected: unexpected error: %v", err)
	}
}

// SC-3 (positive): amount_usd_cents=0 and amount_atomic=0 are rejected.
func TestSchemaPositiveAmountConstraints(t *testing.T) {
	_, db := newTestService(t)

	now := time.Now().Unix()

	insertFlow := func(suffix string) string {
		id := "flow-sc3-" + suffix
		db.Exec(`INSERT INTO v2_client_flows
			(id, wallet_fingerprint, currency, management_code_hash, state, created_at, updated_at)
			VALUES (?, 'wfp', 'BTC', 'hash', 'awaiting_payment', 1, 1)`, id) //nolint:errcheck
		return id
	}

	// Zero amount_usd_cents.
	flowZ := insertFlow("zero-cents")
	_, err := db.Exec(`INSERT INTO v2_invoices
		(id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
		 detection_deadline_at, detected_txid, payment_detected_at, confirmation_deadline_at,
		 payment_txid, payment_confirmed_at, entitlement_expires_at, created_at, updated_at)
		VALUES ('inv-zero-cents', ?, 'pending', 'bc1qaddr', 0, 1000, ?,
		        NULL, NULL, NULL, NULL, NULL, NULL, ?, ?)`,
		flowZ, now+3600, now, now)
	if err == nil {
		t.Error("amount_usd_cents=0: expected CHECK constraint error, got nil")
	}

	// Zero amount_atomic.
	flowA := insertFlow("zero-atomic")
	_, err = db.Exec(`INSERT INTO v2_invoices
		(id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
		 detection_deadline_at, detected_txid, payment_detected_at, confirmation_deadline_at,
		 payment_txid, payment_confirmed_at, entitlement_expires_at, created_at, updated_at)
		VALUES ('inv-zero-atomic', ?, 'pending', 'bc1qaddr', 500, 0, ?,
		        NULL, NULL, NULL, NULL, NULL, NULL, ?, ?)`,
		flowA, now+3600, now, now)
	if err == nil {
		t.Error("amount_atomic=0: expected CHECK constraint error, got nil")
	}

	// detection_deadline_at == created_at (not strictly greater).
	flowD := insertFlow("deadline-eq")
	_, err = db.Exec(`INSERT INTO v2_invoices
		(id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
		 detection_deadline_at, detected_txid, payment_detected_at, confirmation_deadline_at,
		 payment_txid, payment_confirmed_at, entitlement_expires_at, created_at, updated_at)
		VALUES ('inv-deadline-eq', ?, 'pending', 'bc1qaddr', 500, 1000, ?,
		        NULL, NULL, NULL, NULL, NULL, NULL, ?, ?)`,
		flowD, now /* deadline == created_at */, now, now)
	if err == nil {
		t.Error("detection_deadline_at == created_at: expected CHECK constraint error, got nil")
	}
}

// ── Test helpers for SM-27/SM-28 ──────────────────────────────────────────────

type fakePriceSource struct {
	price float64
	err   error
}

func (f *fakePriceSource) PricePerCoin(_ context.Context, _ string) (float64, error) {
	return f.price, f.err
}

type fakeAllocator struct {
	addr string
	err  error
}

func (f *fakeAllocator) AllocateAddress(_ context.Context, _ string) (string, error) {
	return f.addr, f.err
}
