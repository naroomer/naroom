// Package v2 — HTTP handler layer for the V2 client payment intent API.
//
// This file contains the closed HTTP contract for V2 client flows. The handler
// is NOT registered in cmd/naroom/main.go and has no connection to V1 routes.
// It exists solely as a testable object; production wiring will be a separate step.
//
// Privacy invariants enforced here:
//   - Request bodies, wallet addresses, management codes, fingerprints, and txids
//     are never written to logs or error responses.
//   - All internal errors surface as generic messages without SQL/RPC details.
//   - Wrong management code and wrong wallet address return identical 404 responses.
//   - Management code is never accepted from URL path or query string.
package v2

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/wire"
)

// ── External dependency interfaces ────────────────────────────────────────────

// ClientInvoiceIssuer prepares a $5 invoice draft for the given currency.
// The caller owns all real network/HD-wallet operations; this package only
// validates and stores the returned snapshot.
type ClientInvoiceIssuer interface {
	CreateClientInvoice(ctx context.Context, currency string) (InvoiceDraft, error)
}

// ClientBalanceReader returns the current USD balance for the given wallet.
// The caller owns all blockchain/price-oracle calls.
type ClientBalanceReader interface {
	BalanceUSD(ctx context.Context, walletAddress, currency string) (float64, error)
}

// ── Address / currency validation ────────────────────────────────────────────

// v2LTCMainNetParams is a copy of chaincfg.MainNetParams with Litecoin-specific
// address and network fields overridden. Using a full struct copy ensures HD key
// derivation fields and all other chaincfg fields are non-zero and coherent —
// only the five LTC-specific fields differ from the BTC mainnet baseline.
//
// Supported LTC mainnet address types:
//   - P2PKH legacy (version byte 0x30 → addresses starting with 'L')
//   - P2SH  new-format (version byte 0x32 → addresses starting with 'M')
//   - Native SegWit bech32 v0 (HRP "ltc" → addresses starting with "ltc1")
//
// Note: Old LTC P2SH addresses use version byte 0x05 (same as BTC P2SH → '3'
// prefix). Those are ambiguous with BTC P2SH and are treated as BTC by this
// validator. Users must use the M… format for LTC P2SH.
var v2LTCMainNetParams = func() *chaincfg.Params {
	p := chaincfg.MainNetParams // value copy; chaincfg.Params has no mutex fields
	p.Name = "ltc-v2-mainnet"
	p.Net = wire.BitcoinNet(0xdbb6c0fb) // Litecoin mainnet magic bytes
	p.PubKeyHashAddrID = 0x30           // P2PKH 'L…' prefix
	p.ScriptHashAddrID = 0x32           // P2SH  'M…' prefix (new format)
	p.Bech32HRPSegwit = "ltc"           // native SegWit HRP
	return &p
}()

func init() {
	// Register v2LTCMainNetParams so that btcutil.DecodeAddress can decode LTC bech32
	// addresses. DecodeAddress calls chaincfg.IsBech32SegwitPrefix internally, which
	// only recognises registered networks.
	//
	// Duplicate-registration is expected when multiple test packages in the same
	// binary share this package. It is safe: the same pointer is already registered.
	// We detect it via the sentinel errors.Is check — no string comparison.
	if err := chaincfg.Register(v2LTCMainNetParams); err != nil {
		if !errors.Is(err, chaincfg.ErrDuplicateNet) {
			panic("v2: chaincfg.Register(v2LTCMainNetParams): " + err.Error())
		}
	}
}

// ErrInvalidWalletAddress is returned by validateAndNormalizeAddress when the
// provided address fails checksum validation, belongs to an unsupported network,
// or is otherwise malformed. The error message is generic — the raw address is
// never included to avoid leaking it into logs or responses.
var ErrInvalidWalletAddress = errors.New("v2: invalid or unsupported wallet address")

// validateAndNormalizeAddress validates addr against BTC or LTC mainnet using
// cryptographic checksum verification (btcutil.DecodeAddress). It returns the
// normalized address and the detected currency, or ErrInvalidWalletAddress for
// any invalid, wrong-network, malformed, or unsupported input.
//
// Supported address types:
//
//	BTC mainnet: P2PKH (1…), P2SH (3…), native SegWit bech32 (bc1…)
//	LTC mainnet: P2PKH (L…), P2SH new-format (M…), native SegWit bech32 (ltc1…)
//
// Testnet, regtest, and all other address types are rejected.
// The raw address is never included in any returned error.
func validateAndNormalizeAddress(addr string) (normalized, currency string, err error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", "", ErrInvalidWalletAddress
	}
	lower := strings.ToLower(addr)

	switch {
	case strings.HasPrefix(lower, "bc1") || addr[0] == '1' || addr[0] == '3':
		// Likely BTC. Validate checksum and network.
		decoded, decErr := btcutil.DecodeAddress(addr, &chaincfg.MainNetParams)
		if decErr != nil || !decoded.IsForNet(&chaincfg.MainNetParams) {
			return "", "", ErrInvalidWalletAddress
		}
		// bech32 addresses are case-insensitive; normalize to lowercase.
		norm := addr
		if strings.HasPrefix(lower, "bc1") {
			norm = lower
		}
		return norm, "BTC", nil

	case strings.HasPrefix(lower, "ltc1") || addr[0] == 'L' || addr[0] == 'M':
		// Likely LTC. Validate checksum and network.
		decoded, decErr := btcutil.DecodeAddress(addr, v2LTCMainNetParams)
		if decErr != nil || !decoded.IsForNet(v2LTCMainNetParams) {
			return "", "", ErrInvalidWalletAddress
		}
		// bech32 addresses are case-insensitive; normalize to lowercase.
		norm := addr
		if strings.HasPrefix(lower, "ltc1") {
			norm = lower
		}
		return norm, "LTC", nil

	default:
		return "", "", ErrInvalidWalletAddress
	}
}

// ── Rate limiter ─────────────────────────────────────────────────────────────

// rateLimitDomain is the HMAC input prefix for rate-limit client keys.
// Raw IP addresses are never stored or logged; only the keyed digest is used.
const rateLimitDomain = "naroom:v2:ratelimit:"

// defaultMaxLimiterEntries is the hard cap on the per-limiter entry map.
// Allow sweeps expired entries when the map is full; if still full after the
// sweep, the new key is denied (fail closed). Active entries are never evicted.
const defaultMaxLimiterEntries = 10_000

type windowEntry struct {
	count  int
	expiry time.Time
}

// isExpiredAt reports whether the entry's window has ended (now >= expiry).
func isExpiredAt(e *windowEntry, now time.Time) bool {
	return !now.Before(e.expiry) // now >= expiry
}

// fixedWindowLimiter is a bounded fixed-window per-key rate limiter.
//   - The map is capped at maxEntries to prevent unbounded growth.
//   - Opportunistic cleanup runs inside Allow when the current key is found
//     expired, or when the map is at capacity and a new key must be added.
//   - The clock is injectable for deterministic testing.
type fixedWindowLimiter struct {
	mu         sync.Mutex
	limit      int
	window     time.Duration
	maxEntries int
	now        func() time.Time
	entries    map[string]*windowEntry
}

func newFixedWindowLimiter(limit int, window time.Duration, maxEntries int, now func() time.Time) *fixedWindowLimiter {
	return &fixedWindowLimiter{
		limit:      limit,
		window:     window,
		maxEntries: maxEntries,
		now:        now,
		entries:    make(map[string]*windowEntry),
	}
}

// Allow returns true if the key is within the rate limit for the current window.
//
// Opportunistic cleanup:
//   - If the key's existing entry is expired, it is removed and a fresh window
//     starts — no manual Cleanup call required.
//   - If the map is at capacity and a new entry is needed, all expired entries
//     are swept first. If the map is still at capacity after the sweep (all
//     entries are active), the new key is denied (fail closed).
func (l *fixedWindowLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	e, ok := l.entries[key]
	if ok && isExpiredAt(e, now) {
		// Opportunistic cleanup: remove expired entry for this key.
		delete(l.entries, key)
		ok = false
	}

	if !ok {
		// New entry needed. Enforce capacity.
		if len(l.entries) >= l.maxEntries {
			// Sweep all expired entries across the map.
			for k, v := range l.entries {
				if isExpiredAt(v, now) {
					delete(l.entries, k)
				}
			}
			// If still at capacity, fail closed — no active entry eviction.
			if len(l.entries) >= l.maxEntries {
				return false
			}
		}
		l.entries[key] = &windowEntry{count: 1, expiry: now.Add(l.window)}
		return true
	}

	// Entry is active.
	if e.count >= l.limit {
		return false
	}
	e.count++
	return true
}

// Cleanup removes all expired entries. Retained for testing convenience;
// normal operation does not require explicit calls — Allow handles cleanup
// opportunistically when capacity is needed or when a key's window has expired.
func (l *fixedWindowLimiter) Cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, e := range l.entries {
		if isExpiredAt(e, now) {
			delete(l.entries, k)
		}
	}
}

// ── ClientHandler ─────────────────────────────────────────────────────────────

// ClientHandler holds the dependencies for the three V2 client HTTP endpoints.
// Construct it with NewClientHandler; do not create it directly.
type ClientHandler struct {
	svc          *Service
	issuer       ClientInvoiceIssuer
	balance      ClientBalanceReader
	rateLimitKey []byte
	createLim    *fixedWindowLimiter
	restoreLim   *fixedWindowLimiter
	recheckLim   *fixedWindowLimiter
}

// NewClientHandler constructs a ClientHandler and validates all required
// dependencies. rateLimitKey is copied defensively so later mutations of the
// caller's buffer do not affect the stored key.
//
// Returns an error if any dependency is nil or if rateLimitKey is empty.
func NewClientHandler(
	svc *Service,
	issuer ClientInvoiceIssuer,
	balance ClientBalanceReader,
	rateLimitKey []byte,
	now func() time.Time,
) (*ClientHandler, error) {
	if svc == nil {
		return nil, errors.New("v2: NewClientHandler: svc must not be nil")
	}
	if issuer == nil {
		return nil, errors.New("v2: NewClientHandler: issuer must not be nil")
	}
	if balance == nil {
		return nil, errors.New("v2: NewClientHandler: balance must not be nil")
	}
	if len(rateLimitKey) == 0 {
		return nil, errors.New("v2: NewClientHandler: rateLimitKey must not be empty")
	}
	// Defensive copy: isolate from caller mutations.
	keyCopy := make([]byte, len(rateLimitKey))
	copy(keyCopy, rateLimitKey)

	if now == nil {
		now = time.Now
	}
	const maxEntries = defaultMaxLimiterEntries
	return &ClientHandler{
		svc:          svc,
		issuer:       issuer,
		balance:      balance,
		rateLimitKey: keyCopy,
		createLim:    newFixedWindowLimiter(5, time.Minute, maxEntries, now),
		restoreLim:   newFixedWindowLimiter(10, time.Minute, maxEntries, now),
		recheckLim:   newFixedWindowLimiter(5, time.Minute, maxEntries, now),
	}, nil
}

// Routes returns an http.Handler with the three V2 client endpoints registered.
// This handler is test-only and must NOT be mounted in cmd/naroom/main.go.
func (h *ClientHandler) Routes() http.Handler {
	mux := http.NewServeMux()
	// Go 1.22+ method-prefixed patterns.
	mux.HandleFunc("POST /v2/client/payment-intents", h.handleCreate)
	mux.HandleFunc("POST /v2/client/payment-intents/restore", h.handleRestore)
	mux.HandleFunc("POST /v2/client/payment-intents/recheck-balance", h.handleRecheckBalance)
	return mux
}

// clientKey derives a rate-limit bucket key from the request's remote IP.
// The IP is extracted via net.SplitHostPort (with a safe fallback for bare
// addresses) and canonicalized via net.ParseIP to collapse format variants.
// The raw IP is never stored; only its HMAC digest enters the limiter map.
func (h *ClientHandler) clientKey(r *http.Request) string {
	addr := r.RemoteAddr
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// RemoteAddr without port — use as-is.
		host = addr
	}
	// Canonicalize: collapse IPv4/IPv6 format variants (e.g. "::1" vs "0:0:…:1").
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	mac := hmac.New(sha256.New, h.rateLimitKey)
	mac.Write([]byte(rateLimitDomain))
	mac.Write([]byte(host))
	return hex.EncodeToString(mac.Sum(nil))
}

// ── JSON helpers ──────────────────────────────────────────────────────────────

const maxBodyBytes = 4096

// jsonError writes a JSON error response. Internal details are never included.
func jsonError(w http.ResponseWriter, statusCode int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	b, _ := json.Marshal(map[string]string{"error": msg})
	w.Write(b) //nolint:errcheck
}

// decodeStrict enforces all request body constraints:
//   - Content-Type is parsed via mime.ParseMediaType; the media type must be
//     exactly "application/json" (case-insensitive); parameters (e.g. charset)
//     are permitted.
//   - Body must not exceed maxBodyBytes (4 KiB); a MaxBytesError from either
//     Decode call returns 413.
//   - Unknown JSON fields are rejected (400).
//   - The body must contain exactly one JSON value; the second Decode must
//     return io.EOF. Any other result (second value or garbage bytes) → 400.
//
// Returns true on success; on failure it writes the appropriate response and
// returns false.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	ct := r.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		jsonError(w, http.StatusBadRequest, "Content-Type must be application/json")
		return false
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			jsonError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return false
	}

	// Second Decode must return exactly io.EOF — no trailing values or garbage.
	var extra json.RawMessage
	switch trailingErr := dec.Decode(&extra); {
	case errors.Is(trailingErr, io.EOF):
		// Exactly one value: success.
		return true
	case trailingErr == nil:
		// A second complete JSON value was present.
		jsonError(w, http.StatusBadRequest, "request body must contain exactly one JSON value")
		return false
	default:
		var maxErr *http.MaxBytesError
		if errors.As(trailingErr, &maxErr) {
			jsonError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		// Garbage trailing bytes or other decode error.
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
}

// ── Shared response types ─────────────────────────────────────────────────────

type invoiceSnapshotJSON struct {
	Status         string `json:"status"`
	PaymentAddress string `json:"payment_address"`
	AmountAtomic   int64  `json:"amount_atomic"`
	AmountUSDCents int64  `json:"amount_usd_cents"`
}

// ── POST /v2/client/payment-intents ──────────────────────────────────────────

type createRequest struct {
	WalletAddress string `json:"wallet_address"`
}

// createResponse is the 201 body. management_code is returned only here and
// only once. No fingerprint, code hash, or wallet is included.
type createResponse struct {
	ManagementCode string              `json:"management_code"`
	FlowID         string              `json:"flow_id"`
	InvoiceID      string              `json:"invoice_id"`
	Currency       string              `json:"currency"`
	State          string              `json:"state"`
	Invoice        invoiceSnapshotJSON `json:"invoice"`
}

func (h *ClientHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	key := h.clientKey(r)
	if !h.createLim.Allow(key) {
		jsonError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	var req createRequest
	if !decodeStrict(w, r, &req) {
		return
	}

	normalized, currency, err := validateAndNormalizeAddress(req.WalletAddress)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid or unsupported wallet address")
		return
	}

	// Call the injected invoice issuer. Network/service failure → 503, no DB write.
	draft, err := h.issuer.CreateClientInvoice(r.Context(), currency)
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "payment service unavailable")
		return
	}

	rawCode, fv, err := h.svc.CreatePaymentIntent(normalized, currency, draft)
	if err != nil {
		if errors.Is(err, ErrInvalidDraft) {
			// The issuer returned a draft that failed domain validation.
			// This is a server-side misconfiguration, not a client error: 503.
			jsonError(w, http.StatusServiceUnavailable, "payment service unavailable")
			return
		}
		jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := createResponse{
		ManagementCode: rawCode,
		FlowID:         fv.FlowID,
		InvoiceID:      fv.InvoiceID,
		Currency:       fv.Currency,
		State:          fv.State,
		Invoice: invoiceSnapshotJSON{
			Status:         fv.InvoiceStatus,
			PaymentAddress: fv.PaymentAddress,
			AmountAtomic:   fv.AmountAtomic,
			AmountUSDCents: fv.AmountUSDCents,
		},
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

// ── POST /v2/client/payment-intents/restore ───────────────────────────────────

type restoreRequest struct {
	ManagementCode string `json:"management_code"`
	WalletAddress  string `json:"wallet_address"`
}

// restoreResponse omits management_code, wallet_address, fingerprint, code hash,
// and full txid. Confirmation and balance fields are included only when present.
// Invoice status now includes "payment_detected" and "expired" in addition to
// "pending" and "confirmed".
type restoreResponse struct {
	FlowID    string              `json:"flow_id"`
	InvoiceID string              `json:"invoice_id"`
	Currency  string              `json:"currency"`
	State     string              `json:"state"`
	Invoice   invoiceSnapshotJSON `json:"invoice"`

	// Existing confirmation fields.
	PaymentConfirmedAt   *int64   `json:"payment_confirmed_at,omitempty"`
	EntitlementExpiresAt *int64   `json:"entitlement_expires_at,omitempty"`
	LastBalanceUSD       *float64 `json:"last_balance_usd,omitempty"`
	LastBalanceCheckedAt *int64   `json:"last_balance_checked_at,omitempty"`

	// Task 03 fields: detection timeline.
	DetectionDeadlineAt    int64  `json:"detection_deadline_at"` // always present
	PaymentDetectedAt      *int64 `json:"payment_detected_at,omitempty"`
	ConfirmationDeadlineAt *int64 `json:"confirmation_deadline_at,omitempty"`
}

func (h *ClientHandler) handleRestore(w http.ResponseWriter, r *http.Request) {
	key := h.clientKey(r)
	if !h.restoreLim.Allow(key) {
		jsonError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	var req restoreRequest
	if !decodeStrict(w, r, &req) {
		return
	}

	// Validate required fields before calling the domain service.
	// Empty/whitespace values → 400. Non-empty but invalid values → 404 (via domain).
	if strings.TrimSpace(req.ManagementCode) == "" {
		jsonError(w, http.StatusBadRequest, "management_code is required")
		return
	}
	if strings.TrimSpace(req.WalletAddress) == "" {
		jsonError(w, http.StatusBadRequest, "wallet_address is required")
		return
	}

	// Wrong code and wrong wallet both return the identical 404 — no enumeration.
	// We do NOT run detectCurrency on wallet_address here to avoid creating a
	// timing or response difference between wrong-code and wrong-wallet paths.
	fv, err := h.svc.RestorePaymentIntent(req.ManagementCode, req.WalletAddress)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			jsonError(w, http.StatusNotFound, "payment intent not found")
			return
		}
		jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := toRestoreResponse(fv)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

func toRestoreResponse(fv FlowView) restoreResponse {
	resp := restoreResponse{
		FlowID:    fv.FlowID,
		InvoiceID: fv.InvoiceID,
		Currency:  fv.Currency,
		State:     fv.State,
		Invoice: invoiceSnapshotJSON{
			Status:         fv.InvoiceStatus,
			PaymentAddress: fv.PaymentAddress,
			AmountAtomic:   fv.AmountAtomic,
			AmountUSDCents: fv.AmountUSDCents,
		},
		LastBalanceUSD:      fv.LastBalanceUSD,
		DetectionDeadlineAt: fv.DetectionDeadlineAt.Unix(),
	}
	if fv.PaymentConfirmedAt != nil {
		u := fv.PaymentConfirmedAt.Unix()
		resp.PaymentConfirmedAt = &u
	}
	if fv.EntitlementExpiresAt != nil {
		u := fv.EntitlementExpiresAt.Unix()
		resp.EntitlementExpiresAt = &u
	}
	if fv.LastBalanceCheckedAt != nil {
		u := fv.LastBalanceCheckedAt.Unix()
		resp.LastBalanceCheckedAt = &u
	}
	if fv.PaymentDetectedAt != nil {
		u := fv.PaymentDetectedAt.Unix()
		resp.PaymentDetectedAt = &u
	}
	if fv.ConfirmationDeadlineAt != nil {
		u := fv.ConfirmationDeadlineAt.Unix()
		resp.ConfirmationDeadlineAt = &u
	}
	return resp
}

// ── POST /v2/client/payment-intents/recheck-balance ──────────────────────────

type recheckRequest struct {
	ManagementCode string `json:"management_code"`
	WalletAddress  string `json:"wallet_address"`
}

// recheckResponse returns only safe balance fields. No management code, wallet,
// fingerprint, or txid is included.
type recheckResponse struct {
	FlowID               string   `json:"flow_id"`
	InvoiceID            string   `json:"invoice_id"`
	Currency             string   `json:"currency"`
	State                string   `json:"state"`
	LastBalanceUSD       *float64 `json:"last_balance_usd,omitempty"`
	LastBalanceCheckedAt *int64   `json:"last_balance_checked_at,omitempty"`
}

// hardFloorUSD is the API-level hard floor passed to the domain service.
// The public requirement is $150; the API floor is $120 per task spec.
const hardFloorUSD = 120.0

func (h *ClientHandler) handleRecheckBalance(w http.ResponseWriter, r *http.Request) {
	key := h.clientKey(r)
	if !h.recheckLim.Allow(key) {
		jsonError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	var req recheckRequest
	if !decodeStrict(w, r, &req) {
		return
	}

	// Validate required fields before calling the domain service.
	if strings.TrimSpace(req.ManagementCode) == "" {
		jsonError(w, http.StatusBadRequest, "management_code is required")
		return
	}
	if strings.TrimSpace(req.WalletAddress) == "" {
		jsonError(w, http.StatusBadRequest, "wallet_address is required")
		return
	}

	// Step 1: Restore confirms capability and retrieves currency.
	fv, err := h.svc.RestorePaymentIntent(req.ManagementCode, req.WalletAddress)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			jsonError(w, http.StatusNotFound, "payment intent not found")
			return
		}
		jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Step 2: Balance recheck requires a confirmed payment (invoice status = confirmed).
	if fv.InvoiceStatus != InvoiceStatusConfirmed {
		if fv.InvoiceStatus == InvoiceStatusExpired {
			jsonError(w, http.StatusGone, "payment intent expired")
			return
		}
		jsonError(w, http.StatusConflict, "payment not yet confirmed")
		return
	}

	// Normalize wallet for the balance reader. validateAndNormalizeAddress succeeds
	// here because RestorePaymentIntent already verified the code+wallet pair and
	// we know the wallet was valid when the flow was created.
	normalized, _, err := validateAndNormalizeAddress(req.WalletAddress)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid wallet address")
		return
	}

	// Step 3: Read balance. Failure returns 503; state is not modified.
	balanceUSD, err := h.balance.BalanceUSD(r.Context(), normalized, fv.Currency)
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "balance service unavailable")
		return
	}

	// Step 4: Record balance with hard floor = $120.
	fv, err = h.svc.RecordPostPaymentBalance(fv.FlowID, balanceUSD, hardFloorUSD)
	if err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalidState) {
			jsonError(w, http.StatusConflict, "operation not permitted in current state")
			return
		}
		jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := recheckResponse{
		FlowID:         fv.FlowID,
		InvoiceID:      fv.InvoiceID,
		Currency:       fv.Currency,
		State:          fv.State,
		LastBalanceUSD: fv.LastBalanceUSD,
	}
	if fv.LastBalanceCheckedAt != nil {
		u := fv.LastBalanceCheckedAt.Unix()
		resp.LastBalanceCheckedAt = &u
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}
