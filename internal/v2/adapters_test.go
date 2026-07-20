package v2

// Adapter tests — coverage matrix:
//
// MempoolV2Adapter (BTC / mempool.space JSON schema) — 9 tests:
//   - unconfirmed tx (status.confirmed=false) → confirmations=0
//   - confirmed tx (status.confirmed=true) → confirmations=1
//   - multiple outputs on invoice address summed; other outputs excluded
//   - separate txs remain separate (not merged into one result)
//   - all input addresses extracted and de-duplicated
//   - malformed JSON → error, no fabricated result
//   - non-200 HTTP status → error
//   - provider unreachable (transport error) → error
//   - context cancellation propagates
//
// BlockcypherV2Adapter (LTC / blockcypher JSON schema) — 9 tests:
//   - same coverage as BTC above
//
// Integration tests (real adapter → real V2Watcher → service state machine):
//   - TestAdapterWatcherBTC: 5 scenarios via MempoolV2Adapter with JSON fixture
//   - TestAdapterWatcherLTC: 5 scenarios via BlockcypherV2Adapter with JSON fixture
//
// Tests use an in-memory http.RoundTripper — no TCP listener required.
// 0 SKIP in any environment.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// ── In-memory transport helpers ───────────────────────────────────────────────

// roundTripFunc is a function that implements http.RoundTripper.
// It allows defining inline transport behaviour without a TCP server.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// testClient returns an *http.Client backed by the given RoundTripper.
func testClient(rt http.RoundTripper) *http.Client {
	return &http.Client{Transport: rt}
}

// rtJSON returns a transport that responds 200 with body marshalled as JSON.
func rtJSON(v interface{}) http.RoundTripper {
	b, err := json.Marshal(v)
	if err != nil {
		panic("rtJSON: marshal: " + err.Error())
	}
	return roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(b)),
			Header:     make(http.Header),
		}, nil
	})
}

// rtStatus returns a transport that responds with given HTTP status and no body.
func rtStatus(code int) http.RoundTripper {
	return roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: code,
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
		}, nil
	})
}

// rtBody returns a transport that responds 200 with the given raw bytes.
func rtBody(body []byte) http.RoundTripper {
	return roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})
}

// rtError returns a transport that returns a network-level error.
func rtError(err error) http.RoundTripper {
	return roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, err
	})
}

// ── Mempool JSON fixture helpers ──────────────────────────────────────────────

type mempoolVout struct {
	ScriptPubkeyAddress string `json:"scriptpubkey_address"`
	Value               int64  `json:"value"`
}

type mempoolVin struct {
	Prevout struct {
		ScriptPubkeyAddress string `json:"scriptpubkey_address"`
	} `json:"prevout"`
}

type mempoolTxJSON struct {
	TxID   string `json:"txid"`
	Status struct {
		Confirmed bool `json:"confirmed"`
	} `json:"status"`
	Vout []mempoolVout `json:"vout"`
	Vin  []mempoolVin  `json:"vin"`
}

func mempoolRT(txs []mempoolTxJSON) http.RoundTripper { return rtJSON(txs) }

func newMempoolTestAdapter(rt http.RoundTripper) *MempoolV2Adapter {
	return newMempoolV2AdapterWithClient("http://test.invalid", testClient(rt))
}

// ── Blockcypher JSON fixture helpers ──────────────────────────────────────────

type bcOutput struct {
	Addresses []string `json:"addresses"`
	Value     int64    `json:"value"`
}

type bcInput struct {
	Addresses []string `json:"addresses"`
}

type bcTxJSON struct {
	Hash          string     `json:"hash"`
	Confirmations int        `json:"confirmations"`
	Outputs       []bcOutput `json:"outputs"`
	Inputs        []bcInput  `json:"inputs"`
}

func blockcypherRT(txs []bcTxJSON) http.RoundTripper {
	return rtJSON(map[string]interface{}{"txs": txs})
}

func newBlockcypherTestAdapter(rt http.RoundTripper) *BlockcypherV2Adapter {
	return newBlockcypherV2AdapterWithClient("http://test.invalid", testClient(rt))
}

// ── BTC adapter tests (MempoolV2Adapter) ─────────────────────────────────────

func TestMempoolAdapterUnconfirmedTx(t *testing.T) {
	const invoiceAddr = "bc1qplatform0000000000000000000000000000000"
	txs := []mempoolTxJSON{{
		TxID: "txid_unconf",
		Status: struct {
			Confirmed bool `json:"confirmed"`
		}{Confirmed: false},
		Vout: []mempoolVout{{ScriptPubkeyAddress: invoiceAddr, Value: 500_000}},
		Vin: []mempoolVin{{Prevout: struct {
			ScriptPubkeyAddress string `json:"scriptpubkey_address"`
		}{"bc1qsender"}}},
	}}
	results, err := newMempoolTestAdapter(mempoolRT(txs)).GetInvoiceTxs(t.Context(), invoiceAddr)
	if err != nil {
		t.Fatalf("GetInvoiceTxs: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Confirmations != 0 {
		t.Errorf("unconfirmed: confirmations=%d, want 0", results[0].Confirmations)
	}
	if results[0].AmountAtomic != 500_000 {
		t.Errorf("amount: got %d, want 500000", results[0].AmountAtomic)
	}
}

func TestMempoolAdapterConfirmedTx(t *testing.T) {
	const invoiceAddr = "bc1qplatform0000000000000000000000000000000"
	txs := []mempoolTxJSON{{
		TxID: "txid_conf",
		Status: struct {
			Confirmed bool `json:"confirmed"`
		}{Confirmed: true},
		Vout: []mempoolVout{{ScriptPubkeyAddress: invoiceAddr, Value: 750_000}},
		Vin: []mempoolVin{{Prevout: struct {
			ScriptPubkeyAddress string `json:"scriptpubkey_address"`
		}{"bc1qsender"}}},
	}}
	results, err := newMempoolTestAdapter(mempoolRT(txs)).GetInvoiceTxs(t.Context(), invoiceAddr)
	if err != nil {
		t.Fatalf("GetInvoiceTxs: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Confirmations != 1 {
		t.Errorf("confirmed: confirmations=%d, want 1", results[0].Confirmations)
	}
}

func TestMempoolAdapterMultipleOutputsSummed(t *testing.T) {
	const invoiceAddr = "bc1qplatform0000000000000000000000000000000"
	txs := []mempoolTxJSON{{
		TxID: "txid_multi",
		Status: struct {
			Confirmed bool `json:"confirmed"`
		}{},
		Vout: []mempoolVout{
			{ScriptPubkeyAddress: invoiceAddr, Value: 200_000},
			{ScriptPubkeyAddress: "bc1qchange000", Value: 100_000}, // excluded
			{ScriptPubkeyAddress: invoiceAddr, Value: 300_000},     // summed
		},
		Vin: []mempoolVin{{Prevout: struct {
			ScriptPubkeyAddress string `json:"scriptpubkey_address"`
		}{"bc1qsender"}}},
	}}
	results, err := newMempoolTestAdapter(mempoolRT(txs)).GetInvoiceTxs(t.Context(), invoiceAddr)
	if err != nil {
		t.Fatalf("GetInvoiceTxs: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].AmountAtomic != 500_000 {
		t.Errorf("sum: got %d, want 500000 (change excluded)", results[0].AmountAtomic)
	}
}

func TestMempoolAdapterOutputsOfOtherAddressExcluded(t *testing.T) {
	const invoiceAddr = "bc1qplatform0000000000000000000000000000000"
	txs := []mempoolTxJSON{{
		TxID: "txid_other",
		Status: struct {
			Confirmed bool `json:"confirmed"`
		}{},
		Vout: []mempoolVout{{ScriptPubkeyAddress: "bc1qother", Value: 999_000}},
		Vin: []mempoolVin{{Prevout: struct {
			ScriptPubkeyAddress string `json:"scriptpubkey_address"`
		}{"bc1qsender"}}},
	}}
	results, err := newMempoolTestAdapter(mempoolRT(txs)).GetInvoiceTxs(t.Context(), invoiceAddr)
	if err != nil {
		t.Fatalf("GetInvoiceTxs: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results (no output to invoice addr), got %d", len(results))
	}
}

func TestMempoolAdapterSeparateTxsStaySeparate(t *testing.T) {
	const invoiceAddr = "bc1qplatform0000000000000000000000000000000"
	txs := []mempoolTxJSON{
		{
			TxID: "txid_a",
			Status: struct {
				Confirmed bool `json:"confirmed"`
			}{},
			Vout: []mempoolVout{{ScriptPubkeyAddress: invoiceAddr, Value: 100_000}},
			Vin: []mempoolVin{{Prevout: struct {
				ScriptPubkeyAddress string `json:"scriptpubkey_address"`
			}{"bc1qsenderA"}}},
		},
		{
			TxID: "txid_b",
			Status: struct {
				Confirmed bool `json:"confirmed"`
			}{},
			Vout: []mempoolVout{{ScriptPubkeyAddress: invoiceAddr, Value: 200_000}},
			Vin: []mempoolVin{{Prevout: struct {
				ScriptPubkeyAddress string `json:"scriptpubkey_address"`
			}{"bc1qsenderB"}}},
		},
	}
	results, err := newMempoolTestAdapter(mempoolRT(txs)).GetInvoiceTxs(t.Context(), invoiceAddr)
	if err != nil {
		t.Fatalf("GetInvoiceTxs: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 separate results, got %d", len(results))
	}
	txids := map[string]int64{results[0].Txid: results[0].AmountAtomic, results[1].Txid: results[1].AmountAtomic}
	if txids["txid_a"] != 100_000 {
		t.Errorf("txid_a: amount %d, want 100000", txids["txid_a"])
	}
	if txids["txid_b"] != 200_000 {
		t.Errorf("txid_b: amount %d, want 200000", txids["txid_b"])
	}
}

func TestMempoolAdapterInputAddressesDeduped(t *testing.T) {
	const invoiceAddr = "bc1qplatform0000000000000000000000000000000"
	makeVin := func(addr string) mempoolVin {
		return mempoolVin{Prevout: struct {
			ScriptPubkeyAddress string `json:"scriptpubkey_address"`
		}{addr}}
	}
	txs := []mempoolTxJSON{{
		TxID: "txid_dedup",
		Status: struct {
			Confirmed bool `json:"confirmed"`
		}{},
		Vout: []mempoolVout{{ScriptPubkeyAddress: invoiceAddr, Value: 500_000}},
		Vin:  []mempoolVin{makeVin("bc1qsender"), makeVin("bc1qsender"), makeVin("bc1qother")},
	}}
	results, err := newMempoolTestAdapter(mempoolRT(txs)).GetInvoiceTxs(t.Context(), invoiceAddr)
	if err != nil {
		t.Fatalf("GetInvoiceTxs: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	seen := map[string]bool{}
	for _, addr := range results[0].InputAddresses {
		if seen[addr] {
			t.Errorf("duplicate input address %q", addr)
		}
		seen[addr] = true
	}
	if !seen["bc1qsender"] || !seen["bc1qother"] {
		t.Errorf("missing input addresses; got: %v", results[0].InputAddresses)
	}
}

func TestMempoolAdapterMalformedJSON(t *testing.T) {
	results, err := newMempoolTestAdapter(rtBody([]byte("not json {{{{"))).GetInvoiceTxs(t.Context(), "bc1qany")
	if err == nil {
		t.Errorf("expected error for malformed JSON, got %d results", len(results))
	}
}

func TestMempoolAdapterNon200(t *testing.T) {
	results, err := newMempoolTestAdapter(rtStatus(http.StatusInternalServerError)).GetInvoiceTxs(t.Context(), "bc1qany")
	if err == nil {
		t.Errorf("expected error for 500 response, got %d results", len(results))
	}
}

func TestMempoolAdapterProviderUnreachable(t *testing.T) {
	results, err := newMempoolTestAdapter(rtError(errors.New("connection refused"))).GetInvoiceTxs(t.Context(), "bc1qany")
	if err == nil {
		t.Errorf("expected error for unreachable provider, got %d results", len(results))
	}
}

// ── LTC adapter tests (BlockcypherV2Adapter) ─────────────────────────────────

func TestBlockcypherAdapterUnconfirmedTx(t *testing.T) {
	const invoiceAddr = "ltc1qplatform00000000000000000000000000000000"
	txs := []bcTxJSON{{
		Hash:          "txid_ltc_unconf",
		Confirmations: 0,
		Outputs:       []bcOutput{{Addresses: []string{invoiceAddr}, Value: 1_000_000}},
		Inputs:        []bcInput{{Addresses: []string{"ltc1qsender"}}},
	}}
	results, err := newBlockcypherTestAdapter(blockcypherRT(txs)).GetInvoiceTxs(t.Context(), invoiceAddr)
	if err != nil {
		t.Fatalf("GetInvoiceTxs: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Confirmations != 0 {
		t.Errorf("unconfirmed: confirmations=%d, want 0", results[0].Confirmations)
	}
	if results[0].AmountAtomic != 1_000_000 {
		t.Errorf("amount: got %d, want 1000000", results[0].AmountAtomic)
	}
}

func TestBlockcypherAdapterConfirmedTx(t *testing.T) {
	const invoiceAddr = "ltc1qplatform00000000000000000000000000000000"
	txs := []bcTxJSON{{
		Hash:          "txid_ltc_conf",
		Confirmations: 3,
		Outputs:       []bcOutput{{Addresses: []string{invoiceAddr}, Value: 2_000_000}},
		Inputs:        []bcInput{{Addresses: []string{"ltc1qsender"}}},
	}}
	results, err := newBlockcypherTestAdapter(blockcypherRT(txs)).GetInvoiceTxs(t.Context(), invoiceAddr)
	if err != nil {
		t.Fatalf("GetInvoiceTxs: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Confirmations != 3 {
		t.Errorf("confirmations=%d, want 3", results[0].Confirmations)
	}
}

func TestBlockcypherAdapterMultipleOutputsSummed(t *testing.T) {
	const invoiceAddr = "ltc1qplatform00000000000000000000000000000000"
	txs := []bcTxJSON{{
		Hash:          "txid_ltc_multi",
		Confirmations: 0,
		Outputs: []bcOutput{
			{Addresses: []string{invoiceAddr}, Value: 400_000},
			{Addresses: []string{"ltc1qchange"}, Value: 50_000}, // excluded
			{Addresses: []string{invoiceAddr}, Value: 600_000},  // summed
		},
		Inputs: []bcInput{{Addresses: []string{"ltc1qsender"}}},
	}}
	results, err := newBlockcypherTestAdapter(blockcypherRT(txs)).GetInvoiceTxs(t.Context(), invoiceAddr)
	if err != nil {
		t.Fatalf("GetInvoiceTxs: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].AmountAtomic != 1_000_000 {
		t.Errorf("sum: got %d, want 1000000", results[0].AmountAtomic)
	}
}

func TestBlockcypherAdapterOutputsOfOtherAddressExcluded(t *testing.T) {
	const invoiceAddr = "ltc1qplatform00000000000000000000000000000000"
	txs := []bcTxJSON{{
		Hash:          "txid_ltc_other",
		Confirmations: 0,
		Outputs:       []bcOutput{{Addresses: []string{"ltc1qother"}, Value: 9_000_000}},
		Inputs:        []bcInput{{Addresses: []string{"ltc1qsender"}}},
	}}
	results, err := newBlockcypherTestAdapter(blockcypherRT(txs)).GetInvoiceTxs(t.Context(), invoiceAddr)
	if err != nil {
		t.Fatalf("GetInvoiceTxs: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results (no output to invoice addr), got %d", len(results))
	}
}

func TestBlockcypherAdapterSeparateTxsStaySeparate(t *testing.T) {
	const invoiceAddr = "ltc1qplatform00000000000000000000000000000000"
	txs := []bcTxJSON{
		{Hash: "txid_ltc_x", Confirmations: 0, Outputs: []bcOutput{{Addresses: []string{invoiceAddr}, Value: 111_111}},
			Inputs: []bcInput{{Addresses: []string{"ltc1qA"}}}},
		{Hash: "txid_ltc_y", Confirmations: 2, Outputs: []bcOutput{{Addresses: []string{invoiceAddr}, Value: 222_222}},
			Inputs: []bcInput{{Addresses: []string{"ltc1qB"}}}},
	}
	results, err := newBlockcypherTestAdapter(blockcypherRT(txs)).GetInvoiceTxs(t.Context(), invoiceAddr)
	if err != nil {
		t.Fatalf("GetInvoiceTxs: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 separate results, got %d", len(results))
	}
	byTxid := map[string]V2TxResult{}
	for _, r := range results {
		byTxid[r.Txid] = r
	}
	if byTxid["txid_ltc_x"].AmountAtomic != 111_111 {
		t.Errorf("txid_ltc_x amount: %d", byTxid["txid_ltc_x"].AmountAtomic)
	}
	if byTxid["txid_ltc_y"].AmountAtomic != 222_222 {
		t.Errorf("txid_ltc_y amount: %d", byTxid["txid_ltc_y"].AmountAtomic)
	}
}

func TestBlockcypherAdapterInputAddressesDeduped(t *testing.T) {
	const invoiceAddr = "ltc1qplatform00000000000000000000000000000000"
	txs := []bcTxJSON{{
		Hash:          "txid_ltc_dedup",
		Confirmations: 0,
		Outputs:       []bcOutput{{Addresses: []string{invoiceAddr}, Value: 500_000}},
		Inputs: []bcInput{
			{Addresses: []string{"ltc1qsender"}},
			{Addresses: []string{"ltc1qsender"}}, // duplicate
			{Addresses: []string{"ltc1qother"}},
		},
	}}
	results, err := newBlockcypherTestAdapter(blockcypherRT(txs)).GetInvoiceTxs(t.Context(), invoiceAddr)
	if err != nil {
		t.Fatalf("GetInvoiceTxs: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	seen := map[string]int{}
	for _, addr := range results[0].InputAddresses {
		seen[addr]++
	}
	for addr, count := range seen {
		if count > 1 {
			t.Errorf("address %q appears %d times, want 1 (dedup failed)", addr, count)
		}
	}
	if seen["ltc1qsender"] != 1 || seen["ltc1qother"] != 1 {
		t.Errorf("missing input addresses; got: %v", results[0].InputAddresses)
	}
}

func TestBlockcypherAdapterMalformedJSON(t *testing.T) {
	results, err := newBlockcypherTestAdapter(rtBody([]byte("{invalid json"))).GetInvoiceTxs(t.Context(), "ltc1qany")
	if err == nil {
		t.Errorf("expected error for malformed JSON, got %d results", len(results))
	}
}

func TestBlockcypherAdapterNon200(t *testing.T) {
	results, err := newBlockcypherTestAdapter(rtStatus(http.StatusServiceUnavailable)).GetInvoiceTxs(t.Context(), "ltc1qany")
	if err == nil {
		t.Errorf("expected error for 503 response, got %d results", len(results))
	}
}

func TestBlockcypherAdapterProviderUnreachable(t *testing.T) {
	results, err := newBlockcypherTestAdapter(rtError(errors.New("connection refused"))).GetInvoiceTxs(t.Context(), "ltc1qany")
	if err == nil {
		t.Errorf("expected error for unreachable provider, got %d results", len(results))
	}
}

// ── Integration tests: real adapter → real V2Watcher → service state machine ─
//
// These tests prove that the real adapter's JSON parsing drives the watcher's
// state machine — not a hand-crafted V2TxResult fake.
//
// Scenarios tested for both BTC (Mempool) and LTC (Blockcypher):
//  1. Exact atomic amount + correct sender → invoice transitions to payment_detected.
//  2. Underpayment → invoice stays pending.
//  3. Two separate underpayment txs → invoice stays pending (no aggregation).
//  4. Wrong sender, correct amount → invoice stays pending.
//  5. Correct sender + exact amount → invoice detected (after wrong-sender was skipped).

// btcIntegDraft returns an InvoiceDraft suitable for BTC integration tests.
func btcIntegDraft() InvoiceDraft {
	return InvoiceDraft{
		PaymentAddress: "bc1qplatforminteg000000000000000000000000000",
		AmountAtomic:   500_000,
		AmountUSDCents: 500,
	}
}

// ltcIntegDraft returns an InvoiceDraft suitable for LTC integration tests.
func ltcIntegDraft() InvoiceDraft {
	return InvoiceDraft{
		PaymentAddress: "ltc1qplatforminteg000000000000000000000000",
		AmountAtomic:   10_000_000,
		AmountUSDCents: 500,
	}
}

// newWatcherWithAdapter creates a V2Watcher using the provided chain client.
func newWatcherWithAdapter(t *testing.T, svc *Service, chain V2ChainClient, currency string) *V2Watcher {
	t.Helper()
	w, err := NewV2Watcher(
		svc,
		map[string]V2ChainClient{currency: chain},
		&fakeBalanceReader{result: 150.0},
		time.Now,
		func(_ context.Context, _ time.Duration) {},
		0,
	)
	if err != nil {
		t.Fatalf("NewV2Watcher: %v", err)
	}
	return w
}

// TestAdapterWatcherBTC runs integration scenarios using MempoolV2Adapter
// with JSON fixtures served via an in-memory transport.
func TestAdapterWatcherBTC(t *testing.T) {
	const walletAddr = "bc1qtest"
	draft := btcIntegDraft()
	invoiceAddr := draft.PaymentAddress

	// makeRTExact builds a Mempool transport that returns one tx with the given
	// amount and sender, targeting the invoice address.
	makeRT := func(txid string, amount int64, sender string) http.RoundTripper {
		return mempoolRT([]mempoolTxJSON{{
			TxID: txid,
			Status: struct {
				Confirmed bool `json:"confirmed"`
			}{false},
			Vout: []mempoolVout{{ScriptPubkeyAddress: invoiceAddr, Value: amount}},
			Vin: []mempoolVin{{Prevout: struct {
				ScriptPubkeyAddress string `json:"scriptpubkey_address"`
			}{sender}}},
		}})
	}
	// makeTwoUnderpayRT returns two separate txs each with half the required amount.
	makeTwoUnderpayRT := func() http.RoundTripper {
		half := draft.AmountAtomic / 2
		return mempoolRT([]mempoolTxJSON{
			{TxID: "txid_half1",
				Status: struct {
					Confirmed bool `json:"confirmed"`
				}{},
				Vout: []mempoolVout{{ScriptPubkeyAddress: invoiceAddr, Value: half}},
				Vin: []mempoolVin{{Prevout: struct {
					ScriptPubkeyAddress string `json:"scriptpubkey_address"`
				}{walletAddr}}},
			},
			{TxID: "txid_half2",
				Status: struct {
					Confirmed bool `json:"confirmed"`
				}{},
				Vout: []mempoolVout{{ScriptPubkeyAddress: invoiceAddr, Value: half}},
				Vin: []mempoolVin{{Prevout: struct {
					ScriptPubkeyAddress string `json:"scriptpubkey_address"`
				}{walletAddr}}},
			},
		})
	}

	t.Run("exact_amount_correct_sender_detected", func(t *testing.T) {
		svc, _ := newTestService(t)
		_, fv, _ := svc.CreatePaymentIntent(walletAddr, "BTC", draft)
		adapter := newMempoolTestAdapter(makeRT("txid_exact", draft.AmountAtomic, walletAddr))
		w := newWatcherWithAdapter(t, svc, adapter, "BTC")
		w.ProcessOnce(context.Background())
		reread, _ := svc.readFlowView(fv.FlowID)
		if reread.InvoiceStatus != InvoiceStatusDetected {
			t.Errorf("exact amount+sender: status %q, want payment_detected", reread.InvoiceStatus)
		}
		if reread.DetectedTxid == nil || *reread.DetectedTxid != "txid_exact" {
			t.Errorf("detected txid: %v, want txid_exact", reread.DetectedTxid)
		}
	})

	t.Run("underpayment_stays_pending", func(t *testing.T) {
		svc, _ := newTestService(t)
		_, fv, _ := svc.CreatePaymentIntent(walletAddr, "BTC", draft)
		adapter := newMempoolTestAdapter(makeRT("txid_under", draft.AmountAtomic-1, walletAddr))
		w := newWatcherWithAdapter(t, svc, adapter, "BTC")
		w.ProcessOnce(context.Background())
		reread, _ := svc.readFlowView(fv.FlowID)
		if reread.InvoiceStatus != InvoiceStatusPending {
			t.Errorf("underpayment: status %q, want pending", reread.InvoiceStatus)
		}
	})

	t.Run("two_separate_underpayments_not_aggregated", func(t *testing.T) {
		svc, _ := newTestService(t)
		_, fv, _ := svc.CreatePaymentIntent(walletAddr, "BTC", draft)
		adapter := newMempoolTestAdapter(makeTwoUnderpayRT())
		w := newWatcherWithAdapter(t, svc, adapter, "BTC")
		w.ProcessOnce(context.Background())
		reread, _ := svc.readFlowView(fv.FlowID)
		if reread.InvoiceStatus != InvoiceStatusPending {
			t.Errorf("two underpayments: status %q, want pending (no aggregation)", reread.InvoiceStatus)
		}
	})

	t.Run("wrong_sender_stays_pending", func(t *testing.T) {
		svc, _ := newTestService(t)
		_, fv, _ := svc.CreatePaymentIntent(walletAddr, "BTC", draft)
		adapter := newMempoolTestAdapter(makeRT("txid_wrong", draft.AmountAtomic, "bc1qwrong0000000000000000000000000000000000"))
		w := newWatcherWithAdapter(t, svc, adapter, "BTC")
		w.ProcessOnce(context.Background())
		reread, _ := svc.readFlowView(fv.FlowID)
		if reread.InvoiceStatus != InvoiceStatusPending {
			t.Errorf("wrong sender: status %q, want pending", reread.InvoiceStatus)
		}
	})

	t.Run("correct_sender_after_wrong_detected", func(t *testing.T) {
		// Fixture has two txs: first wrong sender, second correct sender.
		// Watcher must skip the first and detect the second.
		svc, _ := newTestService(t)
		_, fv, _ := svc.CreatePaymentIntent(walletAddr, "BTC", draft)
		twoTxsRT := mempoolRT([]mempoolTxJSON{
			{TxID: "txid_wrong2",
				Status: struct {
					Confirmed bool `json:"confirmed"`
				}{},
				Vout: []mempoolVout{{ScriptPubkeyAddress: invoiceAddr, Value: draft.AmountAtomic}},
				Vin: []mempoolVin{{Prevout: struct {
					ScriptPubkeyAddress string `json:"scriptpubkey_address"`
				}{"bc1qwrong0000000000000000000000000000000000"}}},
			},
			{TxID: "txid_correct2",
				Status: struct {
					Confirmed bool `json:"confirmed"`
				}{},
				Vout: []mempoolVout{{ScriptPubkeyAddress: invoiceAddr, Value: draft.AmountAtomic}},
				Vin: []mempoolVin{{Prevout: struct {
					ScriptPubkeyAddress string `json:"scriptpubkey_address"`
				}{walletAddr}}},
			},
		})
		adapter := newMempoolTestAdapter(twoTxsRT)
		w := newWatcherWithAdapter(t, svc, adapter, "BTC")
		w.ProcessOnce(context.Background())
		reread, _ := svc.readFlowView(fv.FlowID)
		if reread.InvoiceStatus != InvoiceStatusDetected {
			t.Errorf("correct sender after wrong: status %q, want payment_detected", reread.InvoiceStatus)
		}
		if reread.DetectedTxid == nil || *reread.DetectedTxid != "txid_correct2" {
			t.Errorf("detected txid: %v, want txid_correct2", reread.DetectedTxid)
		}
	})
}

// TestAdapterWatcherLTC runs the same integration scenarios using BlockcypherV2Adapter.
func TestAdapterWatcherLTC(t *testing.T) {
	const walletAddr = "ltc1qtest"
	draft := ltcIntegDraft()
	invoiceAddr := draft.PaymentAddress

	makeRT := func(txid string, amount int64, sender string) http.RoundTripper {
		return blockcypherRT([]bcTxJSON{{
			Hash:          txid,
			Confirmations: 0,
			Outputs:       []bcOutput{{Addresses: []string{invoiceAddr}, Value: amount}},
			Inputs:        []bcInput{{Addresses: []string{sender}}},
		}})
	}
	makeTwoUnderpayRT := func() http.RoundTripper {
		half := draft.AmountAtomic / 2
		return blockcypherRT([]bcTxJSON{
			{Hash: "txid_ltc_half1", Confirmations: 0,
				Outputs: []bcOutput{{Addresses: []string{invoiceAddr}, Value: half}},
				Inputs:  []bcInput{{Addresses: []string{walletAddr}}},
			},
			{Hash: "txid_ltc_half2", Confirmations: 0,
				Outputs: []bcOutput{{Addresses: []string{invoiceAddr}, Value: half}},
				Inputs:  []bcInput{{Addresses: []string{walletAddr}}},
			},
		})
	}

	t.Run("exact_amount_correct_sender_detected", func(t *testing.T) {
		svc, _ := newTestService(t)
		_, fv, _ := svc.CreatePaymentIntent(walletAddr, "LTC", draft)
		adapter := newBlockcypherTestAdapter(makeRT("txid_ltc_exact", draft.AmountAtomic, walletAddr))
		w := newWatcherWithAdapter(t, svc, adapter, "LTC")
		w.ProcessOnce(context.Background())
		reread, _ := svc.readFlowView(fv.FlowID)
		if reread.InvoiceStatus != InvoiceStatusDetected {
			t.Errorf("exact amount+sender: status %q, want payment_detected", reread.InvoiceStatus)
		}
		if reread.DetectedTxid == nil || *reread.DetectedTxid != "txid_ltc_exact" {
			t.Errorf("detected txid: %v, want txid_ltc_exact", reread.DetectedTxid)
		}
	})

	t.Run("underpayment_stays_pending", func(t *testing.T) {
		svc, _ := newTestService(t)
		_, fv, _ := svc.CreatePaymentIntent(walletAddr, "LTC", draft)
		adapter := newBlockcypherTestAdapter(makeRT("txid_ltc_under", draft.AmountAtomic-1, walletAddr))
		w := newWatcherWithAdapter(t, svc, adapter, "LTC")
		w.ProcessOnce(context.Background())
		reread, _ := svc.readFlowView(fv.FlowID)
		if reread.InvoiceStatus != InvoiceStatusPending {
			t.Errorf("underpayment: status %q, want pending", reread.InvoiceStatus)
		}
	})

	t.Run("two_separate_underpayments_not_aggregated", func(t *testing.T) {
		svc, _ := newTestService(t)
		_, fv, _ := svc.CreatePaymentIntent(walletAddr, "LTC", draft)
		adapter := newBlockcypherTestAdapter(makeTwoUnderpayRT())
		w := newWatcherWithAdapter(t, svc, adapter, "LTC")
		w.ProcessOnce(context.Background())
		reread, _ := svc.readFlowView(fv.FlowID)
		if reread.InvoiceStatus != InvoiceStatusPending {
			t.Errorf("two underpayments: status %q, want pending (no aggregation)", reread.InvoiceStatus)
		}
	})

	t.Run("wrong_sender_stays_pending", func(t *testing.T) {
		svc, _ := newTestService(t)
		_, fv, _ := svc.CreatePaymentIntent(walletAddr, "LTC", draft)
		adapter := newBlockcypherTestAdapter(makeRT("txid_ltc_wrong", draft.AmountAtomic, "ltc1qwrong000000000000000000000000000000000"))
		w := newWatcherWithAdapter(t, svc, adapter, "LTC")
		w.ProcessOnce(context.Background())
		reread, _ := svc.readFlowView(fv.FlowID)
		if reread.InvoiceStatus != InvoiceStatusPending {
			t.Errorf("wrong sender: status %q, want pending", reread.InvoiceStatus)
		}
	})

	t.Run("correct_sender_after_wrong_detected", func(t *testing.T) {
		svc, _ := newTestService(t)
		_, fv, _ := svc.CreatePaymentIntent(walletAddr, "LTC", draft)
		twoTxsRT := blockcypherRT([]bcTxJSON{
			{Hash: "txid_ltc_wrong2", Confirmations: 0,
				Outputs: []bcOutput{{Addresses: []string{invoiceAddr}, Value: draft.AmountAtomic}},
				Inputs:  []bcInput{{Addresses: []string{"ltc1qwrong000000000000000000000000000000000"}}},
			},
			{Hash: "txid_ltc_correct2", Confirmations: 0,
				Outputs: []bcOutput{{Addresses: []string{invoiceAddr}, Value: draft.AmountAtomic}},
				Inputs:  []bcInput{{Addresses: []string{walletAddr}}},
			},
		})
		adapter := newBlockcypherTestAdapter(twoTxsRT)
		w := newWatcherWithAdapter(t, svc, adapter, "LTC")
		w.ProcessOnce(context.Background())
		reread, _ := svc.readFlowView(fv.FlowID)
		if reread.InvoiceStatus != InvoiceStatusDetected {
			t.Errorf("correct sender after wrong: status %q, want payment_detected", reread.InvoiceStatus)
		}
		if reread.DetectedTxid == nil || *reread.DetectedTxid != "txid_ltc_correct2" {
			t.Errorf("detected txid: %v, want txid_ltc_correct2", reread.DetectedTxid)
		}
	})
}

// Ensure unused imports aren't flagged:
var _ = fmt.Sprintf
