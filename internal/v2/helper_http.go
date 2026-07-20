// Package v2 — HTTP handler layer for the V2 Helper contact purchase API.
//
// This handler is NOT registered in cmd/naroom/main.go.
// Privacy: wallet, token, fingerprint, contact, and txid never appear in logs or error bodies.
package v2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"
)

// HelperInvoiceIssuerHTTP is the issuer interface used by the HTTP handler.
// Separated from the domain package to avoid import cycles.
type HelperInvoiceIssuerHTTP interface {
	CreateHelperInvoice(ctx context.Context, currency string) (HelperInvoiceDraft, error)
}

// HelperPurchaseHandler holds dependencies for the four V2 Helper HTTP endpoints.
type HelperPurchaseHandler struct {
	svc          *HelperPurchaseService
	issuer       HelperInvoiceIssuerHTTP
	balance      ClientBalanceReader
	rateLimitKey []byte
	createLim    *fixedWindowLimiter
	restoreLim   *fixedWindowLimiter
	recheckLim   *fixedWindowLimiter
	revealLim    *fixedWindowLimiter
}

// Stable error code constants for all Helper routes.
const (
	codeInvalidRequest      = "invalid_request"
	codeRateLimited         = "rate_limited"
	codeListingNotFound     = "listing_not_found"
	codePurchaseNotFound    = "purchase_not_found"
	codeInsufficientBalance = "insufficient_pre_balance"
	codeBalanceUnavailable  = "balance_provider_unavailable"
	codeInvoiceUnavailable  = "invoice_provider_unavailable"
	codeCountryConflict     = "country_conflict"
	codeStateConflict       = "state_conflict"
	codeRetryExpired        = "balance_retry_expired"
	codeReceiptExpired      = "receipt_expired"
	codeDuplicatePurchase   = "duplicate_active_purchase"
	codeInternalError       = "internal_error"
)

// helperError writes {"error":"...","code":"..."} with the given HTTP status.
// All Helper routes must use this instead of the generic jsonError.
func helperError(w http.ResponseWriter, status int, msg, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(struct { //nolint:errcheck
		Error string `json:"error"`
		Code  string `json:"code"`
	}{Error: msg, Code: code})
}

// NewHelperPurchaseHandler constructs a HelperPurchaseHandler.
func NewHelperPurchaseHandler(
	svc *HelperPurchaseService,
	issuer HelperInvoiceIssuerHTTP,
	balance ClientBalanceReader,
	rateLimitKey []byte,
	now func() time.Time,
) (*HelperPurchaseHandler, error) {
	if svc == nil {
		return nil, errors.New("v2: NewHelperPurchaseHandler: svc must not be nil")
	}
	if issuer == nil {
		return nil, errors.New("v2: NewHelperPurchaseHandler: issuer must not be nil")
	}
	if balance == nil {
		return nil, errors.New("v2: NewHelperPurchaseHandler: balance must not be nil")
	}
	if len(rateLimitKey) == 0 {
		return nil, errors.New("v2: NewHelperPurchaseHandler: rateLimitKey must not be empty")
	}
	keyCopy := make([]byte, len(rateLimitKey))
	copy(keyCopy, rateLimitKey)
	if now == nil {
		now = time.Now
	}
	const maxE = defaultMaxLimiterEntries
	return &HelperPurchaseHandler{
		svc:          svc,
		issuer:       issuer,
		balance:      balance,
		rateLimitKey: keyCopy,
		createLim:    newFixedWindowLimiter(5, time.Minute, maxE, now),
		restoreLim:   newFixedWindowLimiter(10, time.Minute, maxE, now),
		recheckLim:   newFixedWindowLimiter(5, time.Minute, maxE, now),
		revealLim:    newFixedWindowLimiter(5, time.Minute, maxE, now),
	}, nil
}

// Routes returns an http.Handler with the four Helper endpoints.
// This handler is test-only and must NOT be mounted in cmd/naroom/main.go.
func (h *HelperPurchaseHandler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/helper/contact-purchases", h.handleCreate)
	mux.HandleFunc("POST /v2/helper/contact-purchases/restore", h.handleRestore)
	mux.HandleFunc("POST /v2/helper/contact-purchases/recheck-balance", h.handleRecheckBalance)
	mux.HandleFunc("POST /v2/helper/contact-purchases/reveal", h.handleReveal)
	return mux
}

// clientKeyHelper derives a rate-limit key from the remote IP (same logic as ClientHandler).
func (h *HelperPurchaseHandler) clientKeyHelper(r *http.Request) string {
	// Reuse the ClientHandler helper by constructing a temporary handler.
	ch := &ClientHandler{rateLimitKey: h.rateLimitKey}
	return ch.clientKey(r)
}

// ── Shared response types ─────────────────────────────────────────────────────

type helperInvoiceJSON struct {
	Status                 string `json:"status"`
	PaymentAddress         string `json:"payment_address"`
	AmountAtomic           int64  `json:"amount_atomic"`
	AmountUSDCents         int64  `json:"amount_usd_cents"`
	DetectionDeadlineAt    int64  `json:"detection_deadline_at"`
	ConfirmationDeadlineAt *int64 `json:"confirmation_deadline_at,omitempty"`
}

func toHelperInvoiceJSON(v HelperPurchaseView) helperInvoiceJSON {
	inv := helperInvoiceJSON{
		Status:              v.InvoiceStatus,
		PaymentAddress:      v.PaymentAddress,
		AmountAtomic:        v.AmountAtomic,
		AmountUSDCents:      v.AmountUSDCents,
		DetectionDeadlineAt: v.DetectionDeadlineAt.Unix(),
	}
	if v.ConfirmationDeadlineAt != nil {
		u := v.ConfirmationDeadlineAt.Unix()
		inv.ConfirmationDeadlineAt = &u
	}
	return inv
}

// ── POST /v2/helper/contact-purchases ────────────────────────────────────────

type helperCreateRequest struct {
	PurchaseToken string `json:"purchase_token"` // 64 lowercase hex, browser-generated
	ListingID     string `json:"listing_id"`
	WalletAddress string `json:"wallet_address"`
}

type helperCreateResponse struct {
	PurchaseToken string            `json:"purchase_token,omitempty"` // raw token — returned ONCE (omitted on idempotent)
	PurchaseID    string            `json:"purchase_id"`
	Currency      string            `json:"currency"`
	PublicName    string            `json:"public_name"`
	Invoice       helperInvoiceJSON `json:"invoice"`
	// Notice fields — informational only.
	CountryCode                   string `json:"country_code"`
	InvoiceUSDCents               int    `json:"invoice_usd_cents"`
	RequiredPostPaymentBalanceUSD int    `json:"required_post_payment_balance_usd"`
	NoRefund                      bool   `json:"no_refund"`
	SameExpectedWalletRequired    bool   `json:"same_expected_wallet_required"`
}

func (h *HelperPurchaseHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	key := h.clientKeyHelper(r)
	if !h.createLim.Allow(key) {
		helperError(w, http.StatusTooManyRequests, "rate limit exceeded", codeRateLimited)
		return
	}

	var req helperCreateRequest
	if !decodeStrict(w, r, &req) {
		return
	}

	// Validate purchase_token: exact 64 lowercase hex.
	if !validatePurchaseToken(req.PurchaseToken) {
		helperError(w, http.StatusBadRequest, "purchase_token must be 64 lowercase hex", codeInvalidRequest)
		return
	}

	if strings.TrimSpace(req.ListingID) == "" {
		helperError(w, http.StatusBadRequest, "listing_id is required", codeInvalidRequest)
		return
	}
	if strings.TrimSpace(req.WalletAddress) == "" {
		helperError(w, http.StatusBadRequest, "wallet_address is required", codeInvalidRequest)
		return
	}

	// Validate and normalize address.
	normalized, currency, err := validateAndNormalizeAddress(req.WalletAddress)
	if err != nil {
		helperError(w, http.StatusBadRequest, "invalid or unsupported wallet address", codeInvalidRequest)
		return
	}

	now := h.svc.now()

	// Verify listing effective visibility and get country (read-only early check for fast 404).
	listingCountry, err := h.svc.GetListingForPurchase(req.ListingID, now)
	if err != nil {
		helperError(w, http.StatusNotFound, "listing not found", codeListingNotFound)
		return
	}

	// Read-only profile country pre-check (SELECT only, no insert).
	fp := h.svc.helperWalletFingerprint(currency, normalized)
	var existingCountry sql.NullString
	dbErr := h.svc.db.QueryRow(
		`SELECT country_code FROM v2_helper_profiles WHERE wallet_fingerprint = ?`, fp,
	).Scan(&existingCountry)
	if dbErr != nil && !errors.Is(dbErr, sql.ErrNoRows) {
		helperError(w, http.StatusInternalServerError, "internal error", codeInternalError)
		return
	}
	if dbErr == nil && existingCountry.Valid && existingCountry.String != listingCountry {
		helperError(w, http.StatusConflict, "profile locked to a different country", codeCountryConflict)
		return
	}

	// Server-side balance check.
	balanceUSD, err := h.balance.BalanceUSD(r.Context(), normalized, currency)
	if err != nil {
		helperError(w, http.StatusServiceUnavailable, "balance service unavailable", codeBalanceUnavailable)
		return
	}
	// NaN/Inf/negative → 503.
	if math.IsNaN(balanceUSD) || math.IsInf(balanceUSD, 0) || balanceUSD < 0 {
		helperError(w, http.StatusServiceUnavailable, "balance service returned invalid value", codeBalanceUnavailable)
		return
	}
	if balanceUSD < helperPreInvoiceFloorUSD {
		helperError(w, http.StatusPaymentRequired, "insufficient balance for purchase", codeInsufficientBalance)
		return
	}

	// Get $10 invoice from issuer.
	draft, err := h.issuer.CreateHelperInvoice(r.Context(), currency)
	if err != nil {
		helperError(w, http.StatusServiceUnavailable, "payment service unavailable", codeInvoiceUnavailable)
		return
	}
	if draft.AmountUSDCents != helperInvoiceUSDCents || draft.AmountAtomic <= 0 || draft.PaymentAddress == "" {
		helperError(w, http.StatusServiceUnavailable, "payment service unavailable", codeInvoiceUnavailable)
		return
	}

	// Atomic: re-verify listing + country + create purchase + invoice.
	isNew, view, err := h.svc.CreatePurchase(req.PurchaseToken, req.ListingID, currency, normalized, draft)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			helperError(w, http.StatusNotFound, "listing not found", codeListingNotFound)
			return
		}
		if errors.Is(err, ErrHelperNotFound) {
			helperError(w, http.StatusNotFound, "purchase not found", codePurchaseNotFound)
			return
		}
		if errors.Is(err, ErrHelperCountryMismatch) {
			helperError(w, http.StatusConflict, "profile locked to a different country", codeCountryConflict)
			return
		}
		if errors.Is(err, ErrConflict) {
			helperError(w, http.StatusConflict, "active purchase already exists for this listing", codeDuplicatePurchase)
			return
		}
		helperError(w, http.StatusInternalServerError, "internal error", codeInternalError)
		return
	}

	resp := helperCreateResponse{
		PurchaseID:                    view.PurchaseID,
		Currency:                      view.Currency,
		PublicName:                    view.PublicName,
		Invoice:                       toHelperInvoiceJSON(view),
		CountryCode:                   view.CountryCode,
		InvoiceUSDCents:               helperInvoiceUSDCents,
		RequiredPostPaymentBalanceUSD: int(helperPostPaymentFloorUSD),
		NoRefund:                      true,
		SameExpectedWalletRequired:    true,
	}
	if isNew {
		resp.PurchaseToken = req.PurchaseToken
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if isNew {
		w.WriteHeader(http.StatusCreated)
	}
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

// ── POST /v2/helper/contact-purchases/restore ─────────────────────────────────

type helperRestoreRequest struct {
	PurchaseToken string `json:"purchase_token"`
	WalletAddress string `json:"wallet_address"`
}

type helperRestoreResponse struct {
	PurchaseID string            `json:"purchase_id"`
	Phase      string            `json:"phase"`
	NextAction string            `json:"next_action"`
	Currency   string            `json:"currency"`
	PublicName string            `json:"public_name"`
	Invoice    helperInvoiceJSON `json:"invoice"`

	BalanceRetryDeadlineAt *int64   `json:"balance_retry_deadline_at,omitempty"`
	LastBalanceUSD         *float64 `json:"last_balance_usd,omitempty"`
	ContactReadyAt         *int64   `json:"contact_ready_at,omitempty"`
	ReceiptExpiresAt       *int64   `json:"receipt_expires_at,omitempty"`
}

func helperPurchasePhase(state string) (phase, nextAction string) {
	switch state {
	case HPStateAwaitingPayment:
		return "awaiting_payment", "wait_for_payment"
	case HPStatePaymentDetected:
		return "payment_detected", "wait_for_payment"
	case HPStateInvoiceExpired:
		return "payment_expired", "start_new_purchase"
	case HPStatePaymentConfirmed:
		return "payment_confirmed", "wait_for_balance_check"
	case HPStatePaidLowBalance:
		return "paid_low_balance", "top_up_and_recheck"
	case HPStateContactReady:
		return "contact_ready", "reveal_contact"
	case HPStateFailed:
		return "failed", "start_new_purchase"
	case HPStateReceiptExpired:
		return "receipt_expired", "finished"
	default:
		return "unknown", "start_new_purchase"
	}
}

func (h *HelperPurchaseHandler) handleRestore(w http.ResponseWriter, r *http.Request) {
	key := h.clientKeyHelper(r)
	if !h.restoreLim.Allow(key) {
		helperError(w, http.StatusTooManyRequests, "rate limit exceeded", codeRateLimited)
		return
	}

	var req helperRestoreRequest
	if !decodeStrict(w, r, &req) {
		return
	}

	if strings.TrimSpace(req.PurchaseToken) == "" {
		helperError(w, http.StatusBadRequest, "purchase_token is required", codeInvalidRequest)
		return
	}
	if strings.TrimSpace(req.WalletAddress) == "" {
		helperError(w, http.StatusBadRequest, "wallet_address is required", codeInvalidRequest)
		return
	}

	normalized, currency, err := validateAndNormalizeAddress(req.WalletAddress)
	if err != nil {
		// Wrong wallet format → same 404 as wrong token.
		helperError(w, http.StatusNotFound, "purchase not found", codePurchaseNotFound)
		return
	}

	view, err := h.svc.RestorePurchase(req.PurchaseToken, normalized, currency)
	if err != nil {
		if errors.Is(err, ErrHelperNotFound) {
			helperError(w, http.StatusNotFound, "purchase not found", codePurchaseNotFound)
			return
		}
		helperError(w, http.StatusInternalServerError, "internal error", codeInternalError)
		return
	}

	phase, nextAction := helperPurchasePhase(view.State)
	resp := helperRestoreResponse{
		PurchaseID: view.PurchaseID,
		Phase:      phase,
		NextAction: nextAction,
		Currency:   view.Currency,
		PublicName: view.PublicName,
		Invoice:    toHelperInvoiceJSON(view),
	}
	if view.BalanceRetryDeadlineAt != nil {
		u := view.BalanceRetryDeadlineAt.Unix()
		resp.BalanceRetryDeadlineAt = &u
	}
	if view.LastBalanceUSD != nil {
		resp.LastBalanceUSD = view.LastBalanceUSD
	}
	if view.ContactReadyAt != nil {
		u := view.ContactReadyAt.Unix()
		resp.ContactReadyAt = &u
	}
	if view.ReceiptExpiresAt != nil {
		u := view.ReceiptExpiresAt.Unix()
		resp.ReceiptExpiresAt = &u
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

// ── POST /v2/helper/contact-purchases/recheck-balance ────────────────────────

type helperRecheckRequest struct {
	PurchaseToken string `json:"purchase_token"`
	WalletAddress string `json:"wallet_address"`
}

type helperRecheckResponse struct {
	PurchaseID     string   `json:"purchase_id"`
	Phase          string   `json:"phase"`
	NextAction     string   `json:"next_action"`
	LastBalanceUSD *float64 `json:"last_balance_usd,omitempty"`
}

func (h *HelperPurchaseHandler) handleRecheckBalance(w http.ResponseWriter, r *http.Request) {
	key := h.clientKeyHelper(r)
	if !h.recheckLim.Allow(key) {
		helperError(w, http.StatusTooManyRequests, "rate limit exceeded", codeRateLimited)
		return
	}

	var req helperRecheckRequest
	if !decodeStrict(w, r, &req) {
		return
	}

	if strings.TrimSpace(req.PurchaseToken) == "" {
		helperError(w, http.StatusBadRequest, "purchase_token is required", codeInvalidRequest)
		return
	}
	if strings.TrimSpace(req.WalletAddress) == "" {
		helperError(w, http.StatusBadRequest, "wallet_address is required", codeInvalidRequest)
		return
	}

	normalized, currency, err := validateAndNormalizeAddress(req.WalletAddress)
	if err != nil {
		helperError(w, http.StatusNotFound, "purchase not found", codePurchaseNotFound)
		return
	}

	// Verify capability.
	view, err := h.svc.RestorePurchase(req.PurchaseToken, normalized, currency)
	if err != nil {
		if errors.Is(err, ErrHelperNotFound) {
			helperError(w, http.StatusNotFound, "purchase not found", codePurchaseNotFound)
			return
		}
		helperError(w, http.StatusInternalServerError, "internal error", codeInternalError)
		return
	}

	// Only allowed in payment_confirmed or paid_low_balance.
	if view.State != HPStatePaymentConfirmed && view.State != HPStatePaidLowBalance {
		if view.State == HPStateFailed {
			helperError(w, http.StatusGone, "balance retry deadline has passed", codeRetryExpired)
			return
		}
		helperError(w, http.StatusConflict, "recheck not permitted in current state", codeStateConflict)
		return
	}

	// Check retry deadline before provider call.
	now := h.svc.now()
	if view.BalanceRetryDeadlineAt != nil && now.Unix() > view.BalanceRetryDeadlineAt.Unix() {
		helperError(w, http.StatusGone, "balance retry deadline has passed", codeRetryExpired)
		return
	}

	// Server-side balance check.
	balanceUSD, err := h.balance.BalanceUSD(r.Context(), normalized, currency)
	if err != nil {
		helperError(w, http.StatusServiceUnavailable, "balance service unavailable", codeBalanceUnavailable)
		return
	}

	updated, err := h.svc.RecheckHelperBalance(view.PurchaseID, balanceUSD, now)
	if err != nil {
		if errors.Is(err, ErrHelperRetryDeadlinePassed) {
			helperError(w, http.StatusGone, "balance retry deadline has passed", codeRetryExpired)
			return
		}
		if errors.Is(err, ErrHelperCountryMismatch) {
			helperError(w, http.StatusConflict, "profile locked to a different country", codeCountryConflict)
			return
		}
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalidState) {
			helperError(w, http.StatusConflict, "operation not permitted in current state", codeStateConflict)
			return
		}
		helperError(w, http.StatusInternalServerError, "internal error", codeInternalError)
		return
	}

	phase, nextAction := helperPurchasePhase(updated.State)
	resp := helperRecheckResponse{
		PurchaseID:     updated.PurchaseID,
		Phase:          phase,
		NextAction:     nextAction,
		LastBalanceUSD: updated.LastBalanceUSD,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

// ── POST /v2/helper/contact-purchases/reveal ─────────────────────────────────

type helperRevealRequest struct {
	PurchaseToken string `json:"purchase_token"`
	WalletAddress string `json:"wallet_address"`
}

type helperRevealResponse struct {
	PurchaseID       string `json:"purchase_id"`
	ContactType      string `json:"contact_type"`
	Contact          string `json:"contact"`
	ReceiptExpiresAt int64  `json:"receipt_expires_at"`
}

func (h *HelperPurchaseHandler) handleReveal(w http.ResponseWriter, r *http.Request) {
	key := h.clientKeyHelper(r)
	if !h.revealLim.Allow(key) {
		helperError(w, http.StatusTooManyRequests, "rate limit exceeded", codeRateLimited)
		return
	}

	var req helperRevealRequest
	if !decodeStrict(w, r, &req) {
		return
	}

	// Set no-store headers immediately (before capability check).
	setNoStoreHeaders(w)

	if strings.TrimSpace(req.PurchaseToken) == "" {
		helperError(w, http.StatusBadRequest, "purchase_token is required", codeInvalidRequest)
		return
	}
	if strings.TrimSpace(req.WalletAddress) == "" {
		helperError(w, http.StatusBadRequest, "wallet_address is required", codeInvalidRequest)
		return
	}

	normalized, currency, err := validateAndNormalizeAddress(req.WalletAddress)
	if err != nil {
		helperError(w, http.StatusNotFound, "purchase not found", codePurchaseNotFound)
		return
	}

	// Capability check: restore first to get purchase ID.
	view, err := h.svc.RestorePurchase(req.PurchaseToken, normalized, currency)
	if err != nil {
		if errors.Is(err, ErrHelperNotFound) {
			helperError(w, http.StatusNotFound, "purchase not found", codePurchaseNotFound)
			return
		}
		helperError(w, http.StatusInternalServerError, "internal error", codeInternalError)
		return
	}

	result, err := h.svc.RevealHelperContact(view.PurchaseID, req.PurchaseToken, normalized, currency)
	if err != nil {
		if errors.Is(err, ErrHelperNotFound) {
			helperError(w, http.StatusNotFound, "purchase not found", codePurchaseNotFound)
			return
		}
		if errors.Is(err, ErrHelperReceiptExpired) || errors.Is(err, ErrHelperRevealDeadlinePassed) {
			helperError(w, http.StatusGone, "contact receipt has expired", codeReceiptExpired)
			return
		}
		if errors.Is(err, ErrInvalidState) {
			helperError(w, http.StatusConflict, "contact not yet ready", codeStateConflict)
			return
		}
		helperError(w, http.StatusInternalServerError, "internal error", codeInternalError)
		return
	}

	resp := helperRevealResponse{
		PurchaseID:       result.PurchaseID,
		ContactType:      result.ContactType,
		Contact:          result.Contact,
		ReceiptExpiresAt: result.ReceiptExpiresAt.Unix(),
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

// setNoStoreHeaders sets cache prevention headers on the response writer.
func setNoStoreHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
