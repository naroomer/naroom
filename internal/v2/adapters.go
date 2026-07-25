package v2

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	ncrypto "naroom/internal/crypto"
)

// V2TxResult is the normalized form of a blockchain transaction relevant to a V2 invoice.
// AmountAtomic is the sum of all outputs from this transaction to the queried invoice address.
type V2TxResult struct {
	Txid           string
	Confirmations  int      // 0 = unconfirmed (in mempool)
	AmountAtomic   int64    // satoshis (BTC) or litoshis (LTC)
	InputAddresses []string // de-duplicated input addresses
}

// V2ChainClient fetches transaction data for a specific blockchain address.
// It must include unconfirmed transactions (0 confirmations).
// V2 watcher uses this — it must NOT call V1 FindPayment (99% tolerance, no unconfirmed support).
type V2ChainClient interface {
	GetInvoiceTxs(ctx context.Context, address string) ([]V2TxResult, error)
}

// V2PriceSource returns the current USD price per coin (1 BTC or 1 LTC in USD).
type V2PriceSource interface {
	PricePerCoin(ctx context.Context, currency string) (float64, error)
}

// V2AddressAllocator issues unique receive addresses for invoices.
// Production: backed by HDAllocatorAdapter which shares the V1 invoice_index
// counter for the same xpub — intentionally correct to prevent index collisions.
type V2AddressAllocator interface {
	AllocateAddress(ctx context.Context, currency string) (string, error)
}

// ── MempoolV2Adapter ─────────────────────────────────────────────────────────

// MempoolV2Adapter fetches BTC V2 invoice transactions directly from the
// mempool.space API. It owns its own HTTP client so that tests can inject a
// transport without touching V1 code or opening a TCP listener.
//
// It uses the /address/{addr}/txs endpoint (includes unconfirmed) instead of
// V1 FindPayment (which skips unconfirmed and applies 99% tolerance).
type MempoolV2Adapter struct {
	baseURL    string
	httpClient *http.Client
}

// NewMempoolV2Adapter creates a production MempoolV2Adapter pointed at baseURL.
func NewMempoolV2Adapter(baseURL string) *MempoolV2Adapter {
	return &MempoolV2Adapter{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// newMempoolV2AdapterWithClient creates a MempoolV2Adapter with a caller-supplied
// http.Client. Used only in tests to inject an in-memory transport.
func newMempoolV2AdapterWithClient(baseURL string, c *http.Client) *MempoolV2Adapter {
	return &MempoolV2Adapter{baseURL: baseURL, httpClient: c}
}

// mempoolRawTx is the JSON schema for a mempool.space /address/{addr}/txs entry.
type mempoolRawTx struct {
	TxID   string `json:"txid"`
	Status struct {
		Confirmed bool `json:"confirmed"`
	} `json:"status"`
	Vout []struct {
		ScriptPubkeyAddress string `json:"scriptpubkey_address"`
		Value               int64  `json:"value"` // satoshis
	} `json:"vout"`
	Vin []struct {
		Prevout struct {
			ScriptPubkeyAddress string `json:"scriptpubkey_address"`
		} `json:"prevout"`
	} `json:"vin"`
}

func (a *MempoolV2Adapter) GetInvoiceTxs(ctx context.Context, address string) ([]V2TxResult, error) {
	url := fmt.Sprintf("%s/address/%s/txs", a.baseURL, address)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("v2: mempool GetInvoiceTxs: %w", err)
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("v2: mempool GetInvoiceTxs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("v2: mempool GetInvoiceTxs: status %d", resp.StatusCode)
	}
	var txs []mempoolRawTx
	if err := json.NewDecoder(resp.Body).Decode(&txs); err != nil {
		return nil, fmt.Errorf("v2: mempool GetInvoiceTxs: decode: %w", err)
	}
	results := make([]V2TxResult, 0, len(txs))
	for _, tx := range txs {
		var total int64
		for _, vout := range tx.Vout {
			if vout.ScriptPubkeyAddress == address {
				total += vout.Value
			}
		}
		if total == 0 {
			continue
		}
		confs := 0
		if tx.Status.Confirmed {
			confs = 1 // mempool.space: confirmed=true → at least 1 confirmation
		}
		seen := map[string]bool{}
		var inputs []string
		for _, vin := range tx.Vin {
			if a := vin.Prevout.ScriptPubkeyAddress; a != "" && !seen[a] {
				seen[a] = true
				inputs = append(inputs, a)
			}
		}
		results = append(results, V2TxResult{
			Txid:           tx.TxID,
			Confirmations:  confs,
			AmountAtomic:   total,
			InputAddresses: inputs,
		})
	}
	return results, nil
}

// ── BlockcypherV2Adapter ──────────────────────────────────────────────────────

// BlockcypherV2Adapter fetches LTC V2 invoice transactions directly from the
// blockcypher API. It owns its own HTTP client so that tests can inject a
// transport without touching V1 code or opening a TCP listener.
type BlockcypherV2Adapter struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewBlockcypherV2Adapter creates a production BlockcypherV2Adapter pointed at baseURL.
func NewBlockcypherV2Adapter(baseURL, token string) *BlockcypherV2Adapter {
	return &BlockcypherV2Adapter{
		baseURL:    baseURL,
		token:      token,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// newBlockcypherV2AdapterWithClient creates a BlockcypherV2Adapter with a caller-supplied
// http.Client. Used only in tests to inject an in-memory transport.
func newBlockcypherV2AdapterWithClient(baseURL string, c *http.Client) *BlockcypherV2Adapter {
	return &BlockcypherV2Adapter{baseURL: baseURL, httpClient: c}
}

func (a *BlockcypherV2Adapter) tokenParam() string {
	if a.token == "" {
		return ""
	}
	return "?token=" + a.token
}

// bcRawAddrTxs is the JSON schema for a blockcypher /addrs/{addr}/full response.
type bcRawAddrTxs struct {
	Txs []struct {
		Hash          string `json:"hash"`
		Confirmations int    `json:"confirmations"`
		Outputs       []struct {
			Addresses []string `json:"addresses"`
			Value     int64    `json:"value"` // litoshis
		} `json:"outputs"`
		Inputs []struct {
			Addresses []string `json:"addresses"`
		} `json:"inputs"`
	} `json:"txs"`
}

func (a *BlockcypherV2Adapter) GetInvoiceTxs(ctx context.Context, address string) ([]V2TxResult, error) {
	url := fmt.Sprintf("%s/addrs/%s/full?limit=10%s", a.baseURL, address,
		strings.Replace(a.tokenParam(), "?", "&", 1))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("v2: blockcypher GetInvoiceTxs: %w", err)
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("v2: blockcypher GetInvoiceTxs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("v2: blockcypher GetInvoiceTxs: status %d", resp.StatusCode)
	}
	var data bcRawAddrTxs
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("v2: blockcypher GetInvoiceTxs: decode: %w", err)
	}
	results := make([]V2TxResult, 0, len(data.Txs))
	for _, tx := range data.Txs {
		var total int64
		for _, out := range tx.Outputs {
			for _, addr := range out.Addresses {
				if addr == address {
					total += out.Value
				}
			}
		}
		if total == 0 {
			continue
		}
		seen := map[string]bool{}
		var inputs []string
		for _, inp := range tx.Inputs {
			for _, addr := range inp.Addresses {
				if addr != "" && !seen[addr] {
					seen[addr] = true
					inputs = append(inputs, addr)
				}
			}
		}
		results = append(results, V2TxResult{
			Txid:           tx.Hash,
			Confirmations:  tx.Confirmations,
			AmountAtomic:   total,
			InputAddresses: inputs,
		})
	}
	return results, nil
}

// PriceCacheV2Adapter wraps *crypto.PriceCache to implement V2PriceSource.
type PriceCacheV2Adapter struct {
	pc *ncrypto.PriceCache
}

func NewPriceCacheV2Adapter(pc *ncrypto.PriceCache) *PriceCacheV2Adapter {
	return &PriceCacheV2Adapter{pc: pc}
}

func (a *PriceCacheV2Adapter) PricePerCoin(ctx context.Context, currency string) (float64, error) {
	switch currency {
	case "BTC":
		return a.pc.BTCPrice()
	case "LTC":
		return a.pc.LTCPrice()
	default:
		return 0, fmt.Errorf("v2: PriceCacheV2Adapter: unsupported currency %q", currency)
	}
}

// V2AtomicBalanceReader returns confirmed balance in atomic units (sat/litoshi).
type V2AtomicBalanceReader interface {
	GetBalanceAtomic(ctx context.Context, address string) (int64, error)
}

// MempoolBalanceAdapter adapts *crypto.MempoolClient for balance reading.
type MempoolBalanceAdapter struct{ client *ncrypto.MempoolClient }

func NewMempoolBalanceAdapter(c *ncrypto.MempoolClient) *MempoolBalanceAdapter {
	return &MempoolBalanceAdapter{c}
}

func (a *MempoolBalanceAdapter) GetBalanceAtomic(ctx context.Context, address string) (int64, error) {
	bal, err := a.client.GetBalance(address)
	if err != nil {
		return 0, fmt.Errorf("v2: mempool balance: %w", err)
	}
	return bal, nil
}

// BlockcypherBalanceAdapter adapts *crypto.BlockcypherClient for balance reading.
type BlockcypherBalanceAdapter struct{ client *ncrypto.BlockcypherClient }

func NewBlockcypherBalanceAdapter(c *ncrypto.BlockcypherClient) *BlockcypherBalanceAdapter {
	return &BlockcypherBalanceAdapter{c}
}

func (a *BlockcypherBalanceAdapter) GetBalanceAtomic(ctx context.Context, address string) (int64, error) {
	bal, err := a.client.GetBalance(address)
	if err != nil {
		return 0, fmt.Errorf("v2: blockcypher balance: %w", err)
	}
	return bal, nil
}

// V2BalanceReader implements ClientBalanceReader using a V2AtomicBalanceReader and V2PriceSource.
// It gets the balance in atomic units and converts to USD.
type V2BalanceReader struct {
	btcBalance V2AtomicBalanceReader
	ltcBalance V2AtomicBalanceReader
	price      V2PriceSource
}

// NewV2BalanceReader creates a ClientBalanceReader backed by chain clients and a price source.
func NewV2BalanceReader(btcBal, ltcBal V2AtomicBalanceReader, price V2PriceSource) *V2BalanceReader {
	return &V2BalanceReader{btcBalance: btcBal, ltcBalance: ltcBal, price: price}
}

func (r *V2BalanceReader) BalanceUSD(ctx context.Context, walletAddress, currency string) (float64, error) {
	var atomic int64
	var err error
	switch currency {
	case "BTC":
		atomic, err = r.btcBalance.GetBalanceAtomic(ctx, walletAddress)
	case "LTC":
		atomic, err = r.ltcBalance.GetBalanceAtomic(ctx, walletAddress)
	default:
		return 0, fmt.Errorf("v2: V2BalanceReader: unsupported currency %q", currency)
	}
	if err != nil {
		return 0, err
	}
	price, err := r.price.PricePerCoin(ctx, currency)
	if err != nil {
		return 0, err
	}
	if price <= 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return 0, fmt.Errorf("v2: V2BalanceReader: invalid price %v for %s", price, currency)
	}
	return float64(atomic) / 1e8 * price, nil
}

// V2InvoiceIssuer implements ClientInvoiceIssuer backed by a V2AddressAllocator and V2PriceSource.
// It calculates amount_atomic using ceiling division so the invoice is never under $5.
type V2InvoiceIssuer struct {
	alloc V2AddressAllocator
	price V2PriceSource
}

func NewV2InvoiceIssuer(alloc V2AddressAllocator, price V2PriceSource) *V2InvoiceIssuer {
	return &V2InvoiceIssuer{alloc: alloc, price: price}
}

func (iss *V2InvoiceIssuer) CreateClientInvoice(ctx context.Context, currency string) (InvoiceDraft, error) {
	addr, err := iss.alloc.AllocateAddress(ctx, currency)
	if err != nil {
		return InvoiceDraft{}, fmt.Errorf("v2: V2InvoiceIssuer: allocate address: %w", err)
	}
	price, err := iss.price.PricePerCoin(ctx, currency)
	if err != nil {
		return InvoiceDraft{}, fmt.Errorf("v2: V2InvoiceIssuer: price: %w", err)
	}
	if price <= 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return InvoiceDraft{}, fmt.Errorf("v2: V2InvoiceIssuer: invalid price %v for %s", price, currency)
	}
	// Ceiling division: ceil($5 / pricePerCoin * 1e8)
	// = ceil(5.0 / price * 1e8)
	const usdAmount = 5.0
	atomic := int64(math.Ceil(usdAmount / price * 1e8))
	if atomic <= 0 {
		return InvoiceDraft{}, fmt.Errorf("v2: V2InvoiceIssuer: computed atomic amount %d is non-positive", atomic)
	}
	return InvoiceDraft{
		PaymentAddress: addr,
		AmountAtomic:   atomic,
		AmountUSDCents: clientFlowUSDCents,
	}, nil
}

// HDAllocatorAdapter wraps crypto.HDWallet for V2 address allocation.
//
// SHARED ATOMIC COUNTER (intentionally correct):
// crypto.HDWallet.NextBTCAddress/NextLTCAddress use the V1 `invoice_index` table
// as a shared atomic derivation counter. For the same xpub/account namespace,
// this is the SAFE design:
//
//   - Same xpub + one shared atomic counter → unique derivation indexes across
//     V1 and V2, guaranteed by the atomicity of the counter.
//   - A separate V2 counter for the same xpub would re-derive indexes already
//     used by V1, creating real address collisions — that pattern is UNSAFE.
//   - Elevated index consumption (indexes skipped by V2) is not a collision
//     and is acceptable.
//
// Two safe designs exist:
//  1. Same xpub/account namespace + shared atomic counter (this adapter: safe).
//  2. Separate V2 xpub/account derivation namespace + separate V2 counter (safe).
//
// V2 production wiring must NOT introduce a separate counter for the same xpub.
// V2 production DB wiring is not yet complete; tests use a stub V2AddressAllocator
// with synthetic addresses until wiring is confirmed.
type HDAllocatorAdapter struct {
	wallet *ncrypto.HDWallet
}

func NewHDAllocatorAdapter(w *ncrypto.HDWallet) *HDAllocatorAdapter {
	return &HDAllocatorAdapter{wallet: w}
}

func (a *HDAllocatorAdapter) AllocateAddress(ctx context.Context, currency string) (string, error) {
	switch currency {
	case "BTC":
		addr, _, err := a.wallet.NextBTCAddress()
		return addr, err
	case "LTC":
		addr, _, err := a.wallet.NextLTCAddress()
		return addr, err
	default:
		return "", fmt.Errorf("v2: HDAllocatorAdapter: unsupported currency %q", currency)
	}
}

// ensure V2InvoiceIssuer implements ClientInvoiceIssuer at compile time.
var _ ClientInvoiceIssuer = (*V2InvoiceIssuer)(nil)

// ensure V2BalanceReader implements ClientBalanceReader at compile time.
var _ ClientBalanceReader = (*V2BalanceReader)(nil)

// ensure adapters implement their interfaces.
var _ V2ChainClient = (*MempoolV2Adapter)(nil)
var _ V2ChainClient = (*BlockcypherV2Adapter)(nil)
var _ V2PriceSource = (*PriceCacheV2Adapter)(nil)
var _ V2AtomicBalanceReader = (*MempoolBalanceAdapter)(nil)
var _ V2AtomicBalanceReader = (*BlockcypherBalanceAdapter)(nil)
var _ V2AddressAllocator = (*HDAllocatorAdapter)(nil)

// ── V2HelperInvoiceIssuer ────────────────────────────────────────────────────

// V2HelperInvoiceIssuer issues $10 helper invoices using the shared HD wallet.
// Address allocation uses the same NextBTCAddress/NextLTCAddress counter as
// the client invoice issuer, so index collision is impossible.
type V2HelperInvoiceIssuer struct {
	alloc V2AddressAllocator
	price V2PriceSource
}

// NewV2HelperInvoiceIssuer creates a production helper invoice issuer.
func NewV2HelperInvoiceIssuer(alloc V2AddressAllocator, price V2PriceSource) *V2HelperInvoiceIssuer {
	return &V2HelperInvoiceIssuer{alloc: alloc, price: price}
}

// CreateHelperInvoice allocates a fresh HD-wallet address and calculates the
// atomic amount for a $10 helper fee (ceiling division for safety).
func (iss *V2HelperInvoiceIssuer) CreateHelperInvoice(ctx context.Context, currency string) (HelperInvoiceDraft, error) {
	addr, err := iss.alloc.AllocateAddress(ctx, currency)
	if err != nil {
		return HelperInvoiceDraft{}, fmt.Errorf("v2: V2HelperInvoiceIssuer: allocate address: %w", err)
	}
	priceUSD, err := iss.price.PricePerCoin(ctx, currency)
	if err != nil {
		return HelperInvoiceDraft{}, fmt.Errorf("v2: V2HelperInvoiceIssuer: get price: %w", err)
	}
	if priceUSD <= 0 {
		return HelperInvoiceDraft{}, fmt.Errorf("v2: V2HelperInvoiceIssuer: invalid price %v", priceUSD)
	}
	const usdAmount = 10.0 // $10 helper fee
	atomic := int64(math.Ceil(usdAmount / priceUSD * 1e8))
	return HelperInvoiceDraft{
		PaymentAddress: addr,
		AmountAtomic:   atomic,
		AmountUSDCents: helperInvoiceUSDCents,
	}, nil
}

// ensure V2HelperInvoiceIssuer implements HelperInvoiceIssuerHTTP at compile time.
var _ HelperInvoiceIssuerHTTP = (*V2HelperInvoiceIssuer)(nil)
