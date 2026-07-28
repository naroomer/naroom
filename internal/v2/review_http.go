// Package v2 — HTTP handler layer for the V2 Helper review endpoints.
//
// Routes are test-only and must NOT be registered in cmd/naroom/main.go.
// Privacy: review tokens, wallet addresses, fingerprints never appear in logs
// or error bodies. Wrong token and wrong wallet return byte-identical 404.
// Both routes set Cache-Control: no-store, private unconditionally.
package v2

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// ── Error codes ───────────────────────────────────────────────────────────────

const (
	codeReviewNotFound        = "review_not_found"
	codeReviewExpired         = "review_expired"
	codeReviewAlreadyConsumed = "review_already_consumed"
	codeReviewInvalidRating   = "invalid_rating"
	codeReviewNotYetAvailable = "review_not_yet_available"
	// codeInternalError is shared with helper_http.go in package v2.
)

// reviewError writes {"error":"...","code":"..."} and sets no-store headers.
func reviewError(w http.ResponseWriter, status int, msg, code string) {
	setNoStoreHeaders(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(struct { //nolint:errcheck
		Error string `json:"error"`
		Code  string `json:"code"`
	}{Error: msg, Code: code})
}

// ── HelperReviewHandler ───────────────────────────────────────────────────────

// HelperReviewHandler serves the two Helper review endpoints.
type HelperReviewHandler struct {
	svc           *ReviewService
	rateLimitKey  []byte
	capabilityLim *fixedWindowLimiter
	reviewLim     *fixedWindowLimiter
}

// NewHelperReviewHandler constructs a HelperReviewHandler.
func NewHelperReviewHandler(
	svc *ReviewService,
	rateLimitKey []byte,
	now func() time.Time,
) (*HelperReviewHandler, error) {
	if svc == nil {
		return nil, errors.New("v2: NewHelperReviewHandler: svc must not be nil")
	}
	if len(rateLimitKey) == 0 {
		return nil, errors.New("v2: NewHelperReviewHandler: rateLimitKey must not be empty")
	}
	keyCopy := make([]byte, len(rateLimitKey))
	copy(keyCopy, rateLimitKey)
	if now == nil {
		now = time.Now
	}
	const maxE = defaultMaxLimiterEntries
	return &HelperReviewHandler{
		svc:           svc,
		rateLimitKey:  keyCopy,
		capabilityLim: newFixedWindowLimiter(10, time.Minute, maxE, now),
		reviewLim:     newFixedWindowLimiter(10, time.Minute, maxE, now),
	}, nil
}

// Routes returns an http.Handler with the two Helper review endpoints.
// This handler is test-only and must NOT be mounted in cmd/naroom/main.go.
func (h *HelperReviewHandler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/helper/reviews/capability", h.handleCapability)
	mux.HandleFunc("POST /v2/helper/reviews", h.handleSubmit)
	return mux
}

// clientKeyReview derives a rate-limit bucket key from the remote IP.
func (h *HelperReviewHandler) clientKeyReview(r *http.Request) string {
	ch := &ClientHandler{rateLimitKey: h.rateLimitKey}
	return ch.clientKey(r)
}

// ── POST /v2/helper/reviews/capability ───────────────────────────────────────
//
// Authenticates the Helper by purchase_id + purchase_token + wallet_address
// (same auth as RestorePurchase). Returns the deterministic review token plus
// the Client's safe reputation view.
//
// Wrong purchase_token OR wrong wallet_address → byte-identical 404.
// Exact retry → same deterministic token (idempotent).

type helperReviewCapabilityRequest struct {
	PurchaseID    string `json:"purchase_id"`
	PurchaseToken string `json:"purchase_token"`
	WalletAddress string `json:"wallet_address"`
}

type clientReputationJSON struct {
	MemberSince   int64 `json:"member_since"`
	PositiveCount int   `json:"positive_count"`
	NegativeCount int   `json:"negative_count"`
}

type helperReviewCapabilityResponse struct {
	ReviewToken       string               `json:"review_token"`
	ExpiresAt         int64                `json:"expires_at"`
	ClientReputation  clientReputationJSON `json:"client_reputation"`
	ClientDisplayName string               `json:"client_display_name"`
}

func (h *HelperReviewHandler) handleCapability(w http.ResponseWriter, r *http.Request) {
	// Set no-store headers unconditionally — review tokens must not be cached.
	setNoStoreHeaders(w)

	key := h.clientKeyReview(r)
	if !h.capabilityLim.Allow(key) {
		reviewError(w, http.StatusTooManyRequests, "rate limit exceeded", codeRateLimited)
		return
	}

	var req helperReviewCapabilityRequest
	if !decodeStrict(w, r, &req) {
		return
	}

	if strings.TrimSpace(req.PurchaseID) == "" {
		reviewError(w, http.StatusBadRequest, "purchase_id is required", codeInvalidRequest)
		return
	}
	if strings.TrimSpace(req.PurchaseToken) == "" {
		reviewError(w, http.StatusBadRequest, "purchase_token is required", codeInvalidRequest)
		return
	}
	if strings.TrimSpace(req.WalletAddress) == "" {
		reviewError(w, http.StatusBadRequest, "wallet_address is required", codeInvalidRequest)
		return
	}

	// Validate and normalize wallet address.
	normalized, currency, err := validateAndNormalizeAddress(req.WalletAddress)
	if err != nil {
		// Invalid address format → byte-identical 404.
		reviewError(w, http.StatusNotFound, "review capability not found", codeReviewNotFound)
		return
	}

	result, err := h.svc.GetHelperReviewCapability(req.PurchaseID, req.PurchaseToken, normalized, currency)
	if err != nil {
		var gated *ErrReviewGated
		switch {
		case errors.Is(err, ErrReviewCapabilityNotFound):
			reviewError(w, http.StatusNotFound, "review capability not found", codeReviewNotFound)
		case errors.Is(err, ErrReviewExpired):
			reviewError(w, http.StatusGone, "review window has expired", codeReviewExpired)
		case errors.As(err, &gated):
			// Return 403 with available_at so the frontend can show a countdown.
			setNoStoreHeaders(w)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(struct { //nolint:errcheck
				Error       string `json:"error"`
				Code        string `json:"code"`
				AvailableAt int64  `json:"available_at"`
			}{"review not yet available", codeReviewNotYetAvailable, gated.AvailableAt})
		default:
			reviewError(w, http.StatusInternalServerError, "internal error", codeInternalError)
		}
		return
	}

	resp := helperReviewCapabilityResponse{
		ReviewToken: result.ReviewToken,
		ExpiresAt:   result.ExpiresAt.Unix(),
		ClientReputation: clientReputationJSON{
			MemberSince:   result.ClientReputation.MemberSince.Unix(),
			PositiveCount: result.ClientReputation.PositiveCount,
			NegativeCount: result.ClientReputation.NegativeCount,
		},
		ClientDisplayName: result.ClientDisplayName,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

// ── POST /v2/helper/reviews ───────────────────────────────────────────────────
//
// Accepts the one-time review token and a rating of "positive" or "negative".
// Atomically consumes the Helper entitlement and increments the Client aggregate.
//
// Exact repeat of the same rating → idempotent 200 (no second increment).
// Different rating after first consume → 409 review_already_consumed.
// Wrong token → 404. Expired → 410. Unknown rating → 400.

type helperReviewSubmitRequest struct {
	ReviewToken string `json:"review_token"`
	Rating      string `json:"rating"` // "positive" or "negative"
}

type helperReviewSubmitResponse struct {
	Accepted bool `json:"accepted"`
}

func (h *HelperReviewHandler) handleSubmit(w http.ResponseWriter, r *http.Request) {
	// Set no-store headers unconditionally.
	setNoStoreHeaders(w)

	key := h.clientKeyReview(r)
	if !h.reviewLim.Allow(key) {
		reviewError(w, http.StatusTooManyRequests, "rate limit exceeded", codeRateLimited)
		return
	}

	var req helperReviewSubmitRequest
	if !decodeStrict(w, r, &req) {
		return
	}

	if strings.TrimSpace(req.ReviewToken) == "" {
		reviewError(w, http.StatusBadRequest, "review_token is required", codeInvalidRequest)
		return
	}
	if req.Rating != "positive" && req.Rating != "negative" {
		reviewError(w, http.StatusBadRequest, "rating must be 'positive' or 'negative'", codeReviewInvalidRating)
		return
	}

	err := h.svc.SubmitHelperReview(req.ReviewToken, req.Rating)
	if err != nil {
		var gated *ErrReviewGated
		switch {
		case errors.Is(err, ErrReviewNotFound):
			reviewError(w, http.StatusNotFound, "review not found", codeReviewNotFound)
		case errors.Is(err, ErrReviewExpired):
			reviewError(w, http.StatusGone, "review window has expired", codeReviewExpired)
		case errors.Is(err, ErrReviewAlreadyConsumed):
			reviewError(w, http.StatusConflict, "review already submitted", codeReviewAlreadyConsumed)
		case errors.As(err, &gated):
			// Return 403 with available_at so the frontend can show a countdown,
			// the same stable domain response handleCapability returns.
			setNoStoreHeaders(w)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(struct { //nolint:errcheck
				Error       string `json:"error"`
				Code        string `json:"code"`
				AvailableAt int64  `json:"available_at"`
			}{"review not yet available", codeReviewNotYetAvailable, gated.AvailableAt})
		default:
			reviewError(w, http.StatusInternalServerError, "internal error", codeInternalError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(helperReviewSubmitResponse{Accepted: true}) //nolint:errcheck
}
