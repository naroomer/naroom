// Package v2 — HTTP handler layer for Telegram link/webhook endpoints.
//
// Privacy invariants:
//   - management_code and wallet_address are never logged.
//   - Internal error details are never returned to callers.
package v2

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// TelegramLinkHandler handles POST /v2/client/telegram-links, /status, and /webhook.
type TelegramLinkHandler struct {
	svc          *Service
	transport    *TelegramTransport
	linkLim      *fixedWindowLimiter
	statusLim    *fixedWindowLimiter
	rateLimitKey []byte
}

// NewTelegramLinkHandler constructs a TelegramLinkHandler.
// svc, transport, rateLimitKey must not be nil/empty.
// linkLim: 5 requests/minute; statusLim: 10 requests/minute.
func NewTelegramLinkHandler(svc *Service, transport *TelegramTransport, rateLimitKey []byte, now func() time.Time) (*TelegramLinkHandler, error) {
	if svc == nil {
		return nil, errors.New("v2: NewTelegramLinkHandler: svc must not be nil")
	}
	if transport == nil {
		return nil, errors.New("v2: NewTelegramLinkHandler: transport must not be nil")
	}
	if len(rateLimitKey) == 0 {
		return nil, errors.New("v2: NewTelegramLinkHandler: rateLimitKey must not be empty")
	}
	keyCopy := make([]byte, len(rateLimitKey))
	copy(keyCopy, rateLimitKey)
	if now == nil {
		now = time.Now
	}
	const maxEntries = defaultMaxLimiterEntries
	return &TelegramLinkHandler{
		svc:          svc,
		transport:    transport,
		rateLimitKey: keyCopy,
		linkLim:      newFixedWindowLimiter(5, time.Minute, maxEntries, now),
		statusLim:    newFixedWindowLimiter(10, time.Minute, maxEntries, now),
	}, nil
}

// Routes returns an http.Handler. Must NOT be mounted in cmd/naroom/main.go.
func (h *TelegramLinkHandler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/client/telegram-links", h.handleCreateLink)
	mux.HandleFunc("POST /v2/client/telegram-links/status", h.handleLinkStatus)
	mux.HandleFunc("POST /v2/telegram/client/webhook", h.transport.HandleWebhook)
	return mux
}

// telegramClientKey derives a rate-limit bucket key from the request's real
// client IP (see RealClientIP).
func (h *TelegramLinkHandler) telegramClientKey(r *http.Request) string {
	host := RealClientIP(r)
	mac := hmac.New(sha256.New, h.rateLimitKey)
	mac.Write([]byte(rateLimitDomain))
	mac.Write([]byte(host))
	return hex.EncodeToString(mac.Sum(nil))
}

// telegramLinkRequest is the request body for both link and status endpoints.
type telegramLinkRequest struct {
	ManagementCode string `json:"management_code"`
	WalletAddress  string `json:"wallet_address"`
}

// telegramLinkResponse is the response for createLink.
type telegramLinkResponse struct {
	BotURL    string `json:"bot_url"`
	ExpiresAt int64  `json:"expires_at"` // unix seconds
}

// telegramLinkStatusResponse is the response for linkStatus.
type telegramLinkStatusResponse struct {
	Status string `json:"status"` // needs_link|link_pending|ready|active
}

func (h *TelegramLinkHandler) handleCreateLink(w http.ResponseWriter, r *http.Request) {
	key := h.telegramClientKey(r)
	if !h.linkLim.Allow(key) {
		jsonError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	var req telegramLinkRequest
	if !decodeStrict(w, r, &req) {
		return
	}

	if strings.TrimSpace(req.ManagementCode) == "" {
		jsonError(w, http.StatusBadRequest, "management_code is required")
		return
	}
	if strings.TrimSpace(req.WalletAddress) == "" {
		jsonError(w, http.StatusBadRequest, "wallet_address is required")
		return
	}

	result, err := h.transport.CreateLink(req.ManagementCode, req.WalletAddress)
	if err != nil {
		switch {
		case errors.Is(err, ErrListingCapabilityNotFound):
			jsonError(w, http.StatusNotFound, "not found")
		case errors.Is(err, ErrFormNotReady):
			jsonError(w, http.StatusConflict, "flow not ready")
		case errors.Is(err, ErrEntitlementExpired):
			jsonError(w, http.StatusGone, "entitlement expired")
		case errors.Is(err, ErrAlreadyVisible):
			jsonError(w, http.StatusConflict, "already visible")
		case errors.Is(err, ErrConflict):
			jsonError(w, http.StatusConflict, "conflict")
		default:
			jsonError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	resp := telegramLinkResponse{
		BotURL:    result.BotURL,
		ExpiresAt: result.ExpiresAt.Unix(),
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

func (h *TelegramLinkHandler) handleLinkStatus(w http.ResponseWriter, r *http.Request) {
	key := h.telegramClientKey(r)
	if !h.statusLim.Allow(key) {
		jsonError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	var req telegramLinkRequest
	if !decodeStrict(w, r, &req) {
		return
	}

	if strings.TrimSpace(req.ManagementCode) == "" {
		jsonError(w, http.StatusBadRequest, "management_code is required")
		return
	}
	if strings.TrimSpace(req.WalletAddress) == "" {
		jsonError(w, http.StatusBadRequest, "wallet_address is required")
		return
	}

	status, err := h.transport.QueryLinkStatus(req.ManagementCode, req.WalletAddress)
	if err != nil {
		switch {
		case errors.Is(err, ErrListingCapabilityNotFound):
			jsonError(w, http.StatusNotFound, "not found")
		case errors.Is(err, ErrFormNotReady):
			jsonError(w, http.StatusConflict, "flow not ready")
		default:
			jsonError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	resp := telegramLinkStatusResponse{Status: status}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}
