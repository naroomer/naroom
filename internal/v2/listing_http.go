// Package v2 — HTTP handler layer for V2 client listing journey.
//
// ClientJourneyHandler orchestrates the full HTTP Client path:
//
//	POST /v2/client/listings/restore    — composite navigation response
//	POST /v2/client/listings/publish    — first publication
//	POST /v2/client/listings/reactivate — daily reactivation
//	GET  /v2/board/{city}               — public board
//	GET  /v2/listings/{listing_id}      — public listing detail
//
// Routes() is test-only. Must NOT be registered in cmd/naroom/main.go.
//
// Privacy invariants:
//   - management_code and wallet_address are never logged, returned in errors,
//     or accepted from URL path or query string.
//   - Contact value/ciphertext, flow ID, binding ref, wallet fingerprint,
//     code hash, and Telegram chat ID are never included in any response.
//   - Wrong code and wrong wallet return identical 404 responses.
//   - balanceUSD is never accepted from the request body.
//   - No global/default network client is used.
package v2

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

// ── Stable machine-readable error codes ───────────────────────────────────────

const (
	errCodeInvalidRequest        = "invalid_request"
	errCodeNotFound              = "not_found"
	errCodeNotReady              = "not_ready"
	errCodeTelegramRequired      = "telegram_required"
	errCodeAlreadyVisible        = "already_visible"
	errCodeProfileAlreadyVisible = "profile_already_visible"
	errCodeAlreadyPublished      = "already_published"
	errCodeLowBalance            = "low_balance"
	errCodeEntitlementExpired    = "entitlement_expired"
	errCodeProviderUnavailable   = "provider_unavailable"
	errCodeConflict              = "conflict"
	errCodeInternalError         = "internal_error"
	errCodeRateLimited           = "rate_limited"
)

// ── Phase constants ───────────────────────────────────────────────────────────

const (
	phaseAwaitingPayment = "awaiting_payment"
	phasePaymentDetected = "payment_detected"
	phasePaymentExpired  = "payment_expired"
	phasePaidLowBalance  = "paid_low_balance"
	phaseFormReady       = "form_ready"
	phaseVisible         = "visible"
	phaseHidden          = "hidden"
	phaseFinished        = "finished"
)

// ── Next-action constants ─────────────────────────────────────────────────────

const (
	actionWaitForPayment                 = "wait_for_payment"
	actionRecheckBalance                 = "recheck_balance"
	actionPrepareFirstPublication        = "prepare_first_publication"
	actionPublish                        = "publish"
	actionViewListing                    = "view_listing"
	actionConnectTelegramForReactivation = "connect_telegram_for_reactivation"
	actionReactivate                     = "reactivate"
	actionStartNewListing                = "start_new_listing"
)

// ── Response types ────────────────────────────────────────────────────────────

// journeyError is the JSON error body for all client journey routes.
// Format: {"error":"...","code":"..."}
type journeyError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// safeListingJSON is the listing snapshot returned to authenticated callers.
// Excludes: contact value/ciphertext, flow ID, binding ref, wallet fingerprint,
// management code/hash, Telegram destination, and internal IDs beyond the public listing ID.
type safeListingJSON struct {
	ID                   string   `json:"id"`
	City                 string   `json:"city"`
	CountryCode          string   `json:"country_code"`
	DependencyType       string   `json:"dependency_type"`
	HelpType             string   `json:"help_type"`
	Urgency              string   `json:"urgency"`
	Languages            []string `json:"languages"`
	DisplayName          string   `json:"display_name"`
	ContactType          string   `json:"contact_type"` // type only, never the contact value
	State                string   `json:"state"`
	VisibleUntil         *int64   `json:"visible_until"` // unix seconds; null when not visible
	FirstPublishedAt     int64    `json:"first_published_at"`
	LastActivatedAt      int64    `json:"last_activated_at"`
	EntitlementExpiresAt int64    `json:"entitlement_expires_at"`
	ActivationCount      int      `json:"activation_count"`
	CreatedAt            int64    `json:"created_at"`
	UpdatedAt            int64    `json:"updated_at"`
}

// listingViewToSafe converts a ListingView to the safe JSON representation.
// ListingView already excludes ciphertext and capability fields.
func listingViewToSafe(lv ListingView) safeListingJSON {
	s := safeListingJSON{
		ID:                   lv.ID,
		City:                 lv.City,
		CountryCode:          lv.CountryCode,
		DependencyType:       lv.DependencyType,
		HelpType:             lv.HelpType,
		Urgency:              lv.Urgency,
		Languages:            lv.Languages,
		DisplayName:          lv.DisplayName,
		ContactType:          lv.ContactType,
		State:                lv.State,
		FirstPublishedAt:     lv.FirstPublishedAt.Unix(),
		LastActivatedAt:      lv.LastActivatedAt.Unix(),
		EntitlementExpiresAt: lv.EntitlementExpiresAt.Unix(),
		ActivationCount:      lv.ActivationCount,
		CreatedAt:            lv.CreatedAt.Unix(),
		UpdatedAt:            lv.UpdatedAt.Unix(),
	}
	if lv.VisibleUntil != nil {
		u := lv.VisibleUntil.Unix()
		s.VisibleUntil = &u
	}
	return s
}

// restoreNavResponse is the composite navigation response for POST /v2/client/listings/restore.
// It provides all information a future frontend needs to determine the next screen.
type restoreNavResponse struct {
	Phase                string           `json:"phase"`
	NextAction           string           `json:"next_action"`
	InvoiceStatus        string           `json:"invoice_status"`
	TelegramStatus       string           `json:"telegram_status"`
	EntitlementExpiresAt int64            `json:"entitlement_expires_at"` // unix seconds; 0 if not yet set
	Listing              *safeListingJSON `json:"listing"`
}

// publicListingJSON is the board and detail public shape.
// Contains no contact data, no capability, no payment, no Telegram, no internal IDs.
type publicListingJSON struct {
	ID               string               `json:"id"`
	DisplayName      string               `json:"display_name"`
	City             string               `json:"city"`
	CountryCode      string               `json:"country_code"`
	DependencyType   string               `json:"dependency_type"`
	HelpType         string               `json:"help_type"`
	Urgency          string               `json:"urgency"`
	Languages        []string             `json:"languages"`
	VisibleUntil     int64                `json:"visible_until"` // unix seconds
	TimeLeftSec      int64                `json:"time_left_sec"` // non-negative
	ClientReputation clientReputationJSON `json:"client_reputation"`
}

func publicViewToJSON(p PublicListingView) publicListingJSON {
	tls := p.TimeLeftSec
	if tls < 0 {
		tls = 0
	}
	return publicListingJSON{
		ID:             p.ID,
		DisplayName:    p.DisplayName,
		City:           p.City,
		CountryCode:    p.CountryCode,
		DependencyType: p.DependencyType,
		HelpType:       p.HelpType,
		Urgency:        p.Urgency,
		Languages:      p.Languages,
		VisibleUntil:   p.VisibleUntil.Unix(),
		TimeLeftSec:    tls,
		ClientReputation: clientReputationJSON{
			MemberSince:   p.ClientReputation.MemberSince.Unix(),
			PositiveCount: p.ClientReputation.PositiveCount,
			NegativeCount: p.ClientReputation.NegativeCount,
		},
	}
}

// ── ClientJourneyHandler ──────────────────────────────────────────────────────

// ClientJourneyHandler holds the dependencies for the five V2 client listing endpoints.
// Construct it with NewClientJourneyHandler; do not create it directly.
type ClientJourneyHandler struct {
	svc           *Service
	ls            *ListingService
	transport     *TelegramTransport
	balance       ClientBalanceReader
	rateLimitKey  []byte
	now           func() time.Time
	restoreLim    *fixedWindowLimiter
	publishLim    *fixedWindowLimiter
	reactivateLim *fixedWindowLimiter
	boardLim      *fixedWindowLimiter
	detailLim     *fixedWindowLimiter
}

// NewClientJourneyHandler constructs a ClientJourneyHandler.
// All dependencies must be non-nil; rateLimitKey must not be empty; now must not be nil.
// rateLimitKey is copied defensively so later mutations of the caller's buffer do not
// affect the stored key.
func NewClientJourneyHandler(
	svc *Service,
	ls *ListingService,
	transport *TelegramTransport,
	balance ClientBalanceReader,
	rateLimitKey []byte,
	now func() time.Time,
) (*ClientJourneyHandler, error) {
	if svc == nil {
		return nil, errors.New("v2: NewClientJourneyHandler: svc must not be nil")
	}
	if ls == nil {
		return nil, errors.New("v2: NewClientJourneyHandler: ls must not be nil")
	}
	if transport == nil {
		return nil, errors.New("v2: NewClientJourneyHandler: transport must not be nil")
	}
	if balance == nil {
		return nil, errors.New("v2: NewClientJourneyHandler: balance must not be nil")
	}
	if len(rateLimitKey) == 0 {
		return nil, errors.New("v2: NewClientJourneyHandler: rateLimitKey must not be empty")
	}
	if now == nil {
		return nil, errors.New("v2: NewClientJourneyHandler: now must not be nil")
	}
	keyCopy := make([]byte, len(rateLimitKey))
	copy(keyCopy, rateLimitKey)
	const maxE = defaultMaxLimiterEntries
	return &ClientJourneyHandler{
		svc:           svc,
		ls:            ls,
		transport:     transport,
		balance:       balance,
		rateLimitKey:  keyCopy,
		now:           now,
		restoreLim:    newFixedWindowLimiter(10, time.Minute, maxE, now),
		publishLim:    newFixedWindowLimiter(5, time.Minute, maxE, now),
		reactivateLim: newFixedWindowLimiter(5, time.Minute, maxE, now),
		boardLim:      newFixedWindowLimiter(30, time.Minute, maxE, now),
		detailLim:     newFixedWindowLimiter(30, time.Minute, maxE, now),
	}, nil
}

// Routes returns an http.Handler for the five V2 client listing endpoints.
// Test-only: must NOT be mounted in cmd/naroom/main.go.
func (h *ClientJourneyHandler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/client/listings/restore", h.handleRestore)
	mux.HandleFunc("POST /v2/client/listings/publish", h.handlePublish)
	mux.HandleFunc("POST /v2/client/listings/reactivate", h.handleReactivate)
	mux.HandleFunc("GET /v2/board/{city}", h.handleBoard)
	mux.HandleFunc("GET /v2/listings/{listing_id}", h.handleListingDetail)
	mux.HandleFunc("POST /v2/listings/{id}/owner-view", h.handleOwnerView)
	return mux
}

// journeyKey derives a rate-limit bucket key from the request remote IP.
// The raw IP is never stored; only the HMAC digest enters the limiter map.
func (h *ClientJourneyHandler) journeyKey(r *http.Request) string {
	addr := r.RemoteAddr
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	mac := hmac.New(sha256.New, h.rateLimitKey)
	mac.Write([]byte(rateLimitDomain))
	mac.Write([]byte(host))
	return hex.EncodeToString(mac.Sum(nil))
}

// jsonErrCode writes a JSON error with error text and a stable machine-readable code.
func jsonErrCode(w http.ResponseWriter, statusCode int, msg, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	b, _ := json.Marshal(journeyError{Error: msg, Code: code})
	w.Write(b) //nolint:errcheck
}

// ── Request types ─────────────────────────────────────────────────────────────

// journeyCapRequest is the base body for restore and reactivate (capability only).
type journeyCapRequest struct {
	ManagementCode string `json:"management_code"`
	WalletAddress  string `json:"wallet_address"`
}

// journeyPublishRequest is the publish body: capability + listing form.
// Idempotency contract: duplicate calls after a lost response return 409 already_published.
type journeyPublishRequest struct {
	ManagementCode string   `json:"management_code"`
	WalletAddress  string   `json:"wallet_address"`
	City           string   `json:"city"`
	DependencyType string   `json:"dependency_type"`
	HelpType       string   `json:"help_type"`
	Urgency        string   `json:"urgency"`
	Languages      []string `json:"languages"`
	ContactType    string   `json:"contact_type"`
	Contact        string   `json:"contact"`
}

// decodeCap decodes the capability body and validates required fields.
func decodeCap(w http.ResponseWriter, r *http.Request, req *journeyCapRequest) bool {
	if !decodeStrict(w, r, req) {
		return false
	}
	if strings.TrimSpace(req.ManagementCode) == "" {
		jsonErrCode(w, http.StatusBadRequest, "management_code is required", errCodeInvalidRequest)
		return false
	}
	if strings.TrimSpace(req.WalletAddress) == "" {
		jsonErrCode(w, http.StatusBadRequest, "wallet_address is required", errCodeInvalidRequest)
		return false
	}
	return true
}

// capNotFound returns the identical 404 for both wrong code and wrong wallet.
func capNotFound(w http.ResponseWriter) {
	jsonErrCode(w, http.StatusNotFound, "not found", errCodeNotFound)
}

// ── POST /v2/client/listings/restore ─────────────────────────────────────────

func (h *ClientJourneyHandler) handleRestore(w http.ResponseWriter, r *http.Request) {
	if !h.restoreLim.Allow(h.journeyKey(r)) {
		jsonErrCode(w, http.StatusTooManyRequests, "rate limit exceeded", errCodeRateLimited)
		return
	}
	var req journeyCapRequest
	if !decodeCap(w, r, &req) {
		return
	}
	fv, err := h.svc.RestorePaymentIntent(req.ManagementCode, req.WalletAddress)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			capNotFound(w)
			return
		}
		jsonErrCode(w, http.StatusInternalServerError, "internal error", errCodeInternalError)
		return
	}
	resp, err := h.buildRestoreNav(fv, req.ManagementCode, req.WalletAddress)
	if err != nil {
		jsonErrCode(w, http.StatusInternalServerError, "internal error", errCodeInternalError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

// buildRestoreNav derives the phase/next_action composite response.
// rawCode and walletAddress are used only for Telegram status queries and
// are never included in the returned response.
//
// Normative phase mapping:
//  1. invoice pending          → awaiting_payment  / wait_for_payment
//  2. invoice payment_detected → payment_detected  / wait_for_payment
//  3. invoice expired          → payment_expired   / start_new_listing
//  4. state paid_low_balance or payment_confirmed → paid_low_balance / recheck_balance
//  5. form_ready, no listing:  telegram ready      → form_ready / publish
//     otherwise               → form_ready / prepare_first_publication
//  6. listing visible (effective) → visible / view_listing
//  7. listing hidden, entitlement alive: telegram ready → hidden / reactivate
//     otherwise               → hidden / connect_telegram_for_reactivation
//  8. entitlement expired or listing finished → finished / start_new_listing
func (h *ClientJourneyHandler) buildRestoreNav(fv FlowView, rawCode, walletAddress string) (restoreNavResponse, error) {
	now := h.now()
	resp := restoreNavResponse{
		InvoiceStatus:  fv.InvoiceStatus,
		TelegramStatus: "needs_link", // default; overridden only when relevant
	}
	if fv.EntitlementExpiresAt != nil {
		resp.EntitlementExpiresAt = fv.EntitlementExpiresAt.Unix()
	}

	// Cases 1-3: determined by invoice status alone.
	switch fv.InvoiceStatus {
	case InvoiceStatusPending:
		resp.Phase, resp.NextAction = phaseAwaitingPayment, actionWaitForPayment
		return resp, nil
	case InvoiceStatusDetected:
		resp.Phase, resp.NextAction = phasePaymentDetected, actionWaitForPayment
		return resp, nil
	case InvoiceStatusExpired:
		resp.Phase, resp.NextAction = phasePaymentExpired, actionStartNewListing
		return resp, nil
	}

	// Terminal boundary: entitlement has expired. This guard is evaluated after
	// invoice terminal statuses (pending/detected/expired return early above) but
	// before paid_low_balance, Telegram, and listing lookups, so an expired
	// entitlement always overrides any other non-terminal flow state.
	if fv.EntitlementExpiresAt != nil && !now.Before(*fv.EntitlementExpiresAt) {
		resp.Phase, resp.NextAction = phaseFinished, actionStartNewListing
		return resp, nil
	}

	// Invoice is confirmed. Case 4: balance not yet checked.
	if fv.State == StatePaidLowBalance || fv.State == StatePaymentConfirmed {
		resp.Phase, resp.NextAction = phasePaidLowBalance, actionRecheckBalance
		return resp, nil
	}

	// State is form_ready. Look up listing by flow ID.
	lv, lvErr := h.ls.scanListingView(fv.FlowID)
	if lvErr != nil && !errors.Is(lvErr, ErrNotFound) {
		return restoreNavResponse{}, lvErr
	}

	if errors.Is(lvErr, ErrNotFound) {
		// Case 5: form_ready, no listing yet.
		tgStatus, err := h.transport.QueryLinkStatus(rawCode, walletAddress)
		if err != nil {
			return restoreNavResponse{}, err
		}
		resp.TelegramStatus = tgStatus
		if tgStatus == "ready" {
			resp.Phase, resp.NextAction = phaseFormReady, actionPublish
		} else {
			resp.Phase, resp.NextAction = phaseFormReady, actionPrepareFirstPublication
		}
		return resp, nil
	}

	// Listing found. Attach safe view.
	safe := listingViewToSafe(lv)
	resp.Listing = &safe

	// Case 8 (checked first): terminal states.
	if lv.State == "finished" || !now.Before(lv.EntitlementExpiresAt) {
		resp.Phase, resp.NextAction = phaseFinished, actionStartNewListing
		return resp, nil
	}

	// Case 6: effectively visible (timestamps dominate DB state label).
	// QueryLinkStatus is called here so the composite response carries an
	// accurate telegram_status (expected "active" after a successful publish).
	if lv.State == "visible" && lv.VisibleUntil != nil && now.Before(*lv.VisibleUntil) {
		tgStatus, err := h.transport.QueryLinkStatus(rawCode, walletAddress)
		if err != nil {
			return restoreNavResponse{}, err
		}
		resp.TelegramStatus = tgStatus
		resp.Phase, resp.NextAction = phaseVisible, actionViewListing
		return resp, nil
	}

	// Case 7: hidden with live entitlement.
	tgStatus, err := h.transport.QueryLinkStatus(rawCode, walletAddress)
	if err != nil {
		return restoreNavResponse{}, err
	}
	resp.TelegramStatus = tgStatus
	if tgStatus == "ready" {
		resp.Phase, resp.NextAction = phaseHidden, actionReactivate
	} else {
		resp.Phase, resp.NextAction = phaseHidden, actionConnectTelegramForReactivation
	}
	return resp, nil
}

// ── POST /v2/client/listings/publish ─────────────────────────────────────────

// Idempotency: a retry after a lost HTTP response returns stable 409 already_published
// (not a new listing). The domain enforces UNIQUE(flow_id) on v2_listings.
func (h *ClientJourneyHandler) handlePublish(w http.ResponseWriter, r *http.Request) {
	if !h.publishLim.Allow(h.journeyKey(r)) {
		jsonErrCode(w, http.StatusTooManyRequests, "rate limit exceeded", errCodeRateLimited)
		return
	}
	var req journeyPublishRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ManagementCode) == "" {
		jsonErrCode(w, http.StatusBadRequest, "management_code is required", errCodeInvalidRequest)
		return
	}
	if strings.TrimSpace(req.WalletAddress) == "" {
		jsonErrCode(w, http.StatusBadRequest, "wallet_address is required", errCodeInvalidRequest)
		return
	}

	lv, err := h.ls.FirstPublish(req.ManagementCode, req.WalletAddress, ListingInput{
		City:           req.City,
		DependencyType: req.DependencyType,
		HelpType:       req.HelpType,
		Urgency:        req.Urgency,
		Languages:      req.Languages,
		ContactType:    req.ContactType,
		RawContact:     req.Contact,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrListingCapabilityNotFound):
			capNotFound(w)
		case errors.Is(err, ErrInvalidListingInput):
			jsonErrCode(w, http.StatusBadRequest, "invalid listing or contact input", errCodeInvalidRequest)
		case errors.Is(err, ErrFormNotReady):
			jsonErrCode(w, http.StatusConflict, "flow not ready for publication", errCodeNotReady)
		case errors.Is(err, ErrEntitlementExpired):
			jsonErrCode(w, http.StatusGone, "entitlement expired", errCodeEntitlementExpired)
		case errors.Is(err, ErrListingAlreadyExists):
			// Stable 409 on retry — no new listing is ever created.
			jsonErrCode(w, http.StatusConflict, "listing already published", errCodeAlreadyPublished)
		case errors.Is(err, ErrAlreadyVisible):
			jsonErrCode(w, http.StatusConflict, "listing already visible", errCodeAlreadyVisible)
		case errors.Is(err, ErrProfileAlreadyVisible):
			jsonErrCode(w, http.StatusConflict, "another listing from this wallet is already visible", errCodeProfileAlreadyVisible)
		case errors.Is(err, ErrBindingRequired), errors.Is(err, ErrDestinationMissing):
			jsonErrCode(w, http.StatusConflict, "telegram binding required", errCodeTelegramRequired)
		default:
			jsonErrCode(w, http.StatusInternalServerError, "internal error", errCodeInternalError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(listingViewToSafe(lv)) //nolint:errcheck
}

// ── POST /v2/client/listings/reactivate ──────────────────────────────────────

// Reactivate opens a new 24-hour visibility window.
// balanceUSD is fetched server-side; the browser cannot supply or override it.
// Concurrent or retried calls serialize via the domain CAS UPDATE and produce
// exactly one successful activation per window.
func (h *ClientJourneyHandler) handleReactivate(w http.ResponseWriter, r *http.Request) {
	if !h.reactivateLim.Allow(h.journeyKey(r)) {
		jsonErrCode(w, http.StatusTooManyRequests, "rate limit exceeded", errCodeRateLimited)
		return
	}
	var req journeyCapRequest
	if !decodeCap(w, r, &req) {
		return
	}

	// Step 1: Verify capability.
	fv, err := h.svc.RestorePaymentIntent(req.ManagementCode, req.WalletAddress)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			capNotFound(w)
			return
		}
		jsonErrCode(w, http.StatusInternalServerError, "internal error", errCodeInternalError)
		return
	}

	// Step 2: Reject non-confirmed or expired invoice before any provider call.
	if fv.InvoiceStatus != InvoiceStatusConfirmed {
		if fv.InvoiceStatus == InvoiceStatusExpired {
			jsonErrCode(w, http.StatusGone, "entitlement expired", errCodeEntitlementExpired)
			return
		}
		jsonErrCode(w, http.StatusConflict, "payment not confirmed", errCodeNotReady)
		return
	}
	if fv.EntitlementExpiresAt == nil || !h.now().Before(*fv.EntitlementExpiresAt) {
		jsonErrCode(w, http.StatusGone, "entitlement expired", errCodeEntitlementExpired)
		return
	}

	// Step 3: Normalize wallet for the balance reader.
	// validateAndNormalizeAddress validates checksum; the wallet was valid at creation,
	// so failure here is an unexpected internal error.
	normalized, _, err := validateAndNormalizeAddress(req.WalletAddress)
	if err != nil {
		jsonErrCode(w, http.StatusInternalServerError, "internal error", errCodeInternalError)
		return
	}

	// Step 4: Server-side balance check. The browser cannot supply or override this.
	balanceUSD, err := h.balance.BalanceUSD(r.Context(), normalized, fv.Currency)
	if err != nil {
		// Step 5: Provider failure → 503; no listing or binding transition occurs.
		jsonErrCode(w, http.StatusServiceUnavailable, "balance service unavailable", errCodeProviderUnavailable)
		return
	}

	// Steps 6-7: Domain atomically verifies fresh ready binding for exact next window
	// and checks balance floor ($120). On success, activation_count increments once.
	lv, err := h.ls.Reactivate(req.ManagementCode, req.WalletAddress, balanceUSD)
	if err != nil {
		switch {
		case errors.Is(err, ErrListingCapabilityNotFound):
			capNotFound(w)
		case errors.Is(err, ErrLowBalance):
			// Step 8: $119.99 → 409 low_balance; ready binding is not consumed.
			jsonErrCode(w, http.StatusConflict, "balance below required floor", errCodeLowBalance)
		case errors.Is(err, ErrEntitlementExpired):
			jsonErrCode(w, http.StatusGone, "entitlement expired", errCodeEntitlementExpired)
		case errors.Is(err, ErrBindingRequired), errors.Is(err, ErrDestinationMissing):
			jsonErrCode(w, http.StatusConflict, "telegram binding required for next window", errCodeTelegramRequired)
		case errors.Is(err, ErrAlreadyVisible):
			jsonErrCode(w, http.StatusConflict, "listing already visible", errCodeAlreadyVisible)
		case errors.Is(err, ErrProfileAlreadyVisible):
			jsonErrCode(w, http.StatusConflict, "another listing from this wallet is already visible", errCodeProfileAlreadyVisible)
		case errors.Is(err, ErrFormNotReady):
			jsonErrCode(w, http.StatusConflict, "listing not ready for reactivation", errCodeNotReady)
		case errors.Is(err, ErrConflict):
			jsonErrCode(w, http.StatusConflict, "conflict", errCodeConflict)
		default:
			jsonErrCode(w, http.StatusInternalServerError, "internal error", errCodeInternalError)
		}
		return
	}

	// Step 9: HTTP 200 safe ListingView.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(listingViewToSafe(lv)) //nolint:errcheck
}

// ── GET /v2/board/{city} ──────────────────────────────────────────────────────

// handleBoard returns effectively visible listings for a city.
// Unknown city → 404. Empty result → []. No mutation of lifecycle state.
// Order: newest last_activated_at first, then listing ID ascending (deterministic).
func (h *ClientJourneyHandler) handleBoard(w http.ResponseWriter, r *http.Request) {
	if !h.boardLim.Allow(h.journeyKey(r)) {
		jsonErrCode(w, http.StatusTooManyRequests, "rate limit exceeded", errCodeRateLimited)
		return
	}
	city := r.PathValue("city")
	listings, err := h.ls.BoardQuery(city, h.now())
	if err != nil {
		if errors.Is(err, ErrInvalidListingInput) {
			// Contract: unknown city → 404.
			jsonErrCode(w, http.StatusNotFound, "unknown city", errCodeNotFound)
			return
		}
		jsonErrCode(w, http.StatusInternalServerError, "internal error", errCodeInternalError)
		return
	}
	// Return empty JSON array, not null.
	result := make([]publicListingJSON, 0, len(listings))
	for _, p := range listings {
		result = append(result, publicViewToJSON(p))
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(result) //nolint:errcheck
}

// ── GET /v2/listings/{listing_id} ────────────────────────────────────────────

// handleListingDetail returns a single effectively visible listing.
// Hidden, expired, finished, unknown, and malformed IDs all return identical 404.
// No lifecycle state mutation occurs.
func (h *ClientJourneyHandler) handleListingDetail(w http.ResponseWriter, r *http.Request) {
	if !h.detailLim.Allow(h.journeyKey(r)) {
		jsonErrCode(w, http.StatusTooManyRequests, "rate limit exceeded", errCodeRateLimited)
		return
	}
	listingID := r.PathValue("listing_id")
	p, err := h.ls.GetPublicListing(listingID, h.now())
	if err != nil {
		// All non-visible cases: identical 404.
		jsonErrCode(w, http.StatusNotFound, "not found", errCodeNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(publicViewToJSON(p)) //nolint:errcheck
}

// ── POST /v2/listings/{id}/owner-view ────────────────────────────────────────

// ownerViewRequest carries the management_code in the POST body. The code is a
// bearer-style secret and must never travel as a URL query parameter (it would
// otherwise land in access logs, browser history, and Referer headers — the
// same class of leak the cross-device handoff fix addresses for purchase tokens).
type ownerViewRequest struct {
	ManagementCode string `json:"management_code"`
}

// handleOwnerView returns an owner-only summary for a listing, authenticated via management_code.
// POST /v2/listings/{id}/owner-view  Body: {"management_code": "..."}
// Wrong code, wrong listing ID, or missing parameters all return identical 404.
// storage (localStorage) is never treated as an authorization basis by itself — the
// frontend may read a locally-saved code, but this endpoint is the sole authority.
func (h *ClientJourneyHandler) handleOwnerView(w http.ResponseWriter, r *http.Request) {
	if !h.detailLim.Allow(h.journeyKey(r)) {
		jsonErrCode(w, http.StatusTooManyRequests, "rate limit exceeded", errCodeRateLimited)
		return
	}
	id := r.PathValue("id")
	var req ownerViewRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	code := req.ManagementCode
	if strings.TrimSpace(id) == "" || strings.TrimSpace(code) == "" {
		jsonErrCode(w, http.StatusNotFound, "not found", errCodeNotFound)
		return
	}

	view, err := h.ls.GetListingOwnerView(id, code)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			jsonErrCode(w, http.StatusNotFound, "not found", errCodeNotFound)
			return
		}
		jsonErrCode(w, http.StatusInternalServerError, "internal error", errCodeInternalError)
		return
	}

	resp := map[string]any{
		"listing_id":     view.ListingID,
		"state":          view.State,
		"city":           view.City,
		"country_code":   view.CountryCode,
		"display_name":   view.DisplayName,
		"urgency":        view.Urgency,
		"languages":      view.Languages,
		"dep_type":       view.DepType,
		"help_type":      view.HelpType,
		"telegram_ready": view.TelegramReady,
	}
	if view.VisibleUntil != nil {
		resp["visible_until"] = view.VisibleUntil.Unix()
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}
