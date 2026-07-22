// Package v2 — HTTP handler layer for the V2 Informer endpoints.
//
// Privacy invariants:
//   - wallet_address is validated and used for balance check; never stored or logged.
//   - raw_token never appears in logs or error responses (except returned on access).
//   - chat_id and sub_ref never appear in any API response.
//   - Error responses use stable code strings, not raw error messages.
package v2

import (
	"context"
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

// InformerBalanceReader returns USD balance for a wallet address.
type InformerBalanceReader interface {
	BalanceUSD(ctx context.Context, walletAddress, currency string) (float64, error)
}

// InformerHandler handles /v2/informer/* HTTP endpoints.
type InformerHandler struct {
	svc          *InformerService
	balance      InformerBalanceReader
	botUsername  string
	rateLimitKey []byte
	lim          *fixedWindowLimiter
}

// NewInformerHandler creates an InformerHandler.
func NewInformerHandler(
	svc *InformerService,
	balance InformerBalanceReader,
	botUsername string,
	rateLimitKey []byte,
	now func() time.Time,
) (*InformerHandler, error) {
	if svc == nil {
		return nil, errors.New("v2: NewInformerHandler: svc must not be nil")
	}
	if balance == nil {
		return nil, errors.New("v2: NewInformerHandler: balance must not be nil")
	}
	if botUsername == "" {
		return nil, errors.New("v2: NewInformerHandler: botUsername must not be empty")
	}
	if len(rateLimitKey) == 0 {
		return nil, errors.New("v2: NewInformerHandler: rateLimitKey must not be empty")
	}
	keyCopy := make([]byte, len(rateLimitKey))
	copy(keyCopy, rateLimitKey)
	if now == nil {
		now = time.Now
	}
	return &InformerHandler{
		svc:          svc,
		balance:      balance,
		botUsername:  botUsername,
		rateLimitKey: keyCopy,
		lim:          newFixedWindowLimiter(10, time.Minute, defaultMaxLimiterEntries, now),
	}, nil
}

// Routes returns an http.Handler for Informer HTTP endpoints.
func (h *InformerHandler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/informer/access", h.handleAccess)
	mux.HandleFunc("POST /v2/informer/status", h.handleStatus)
	return mux
}

// informerError writes {"error":"...","code":"..."} JSON.
func informerError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, private")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(struct { //nolint:errcheck
		Error string `json:"error"`
		Code  string `json:"code"`
	}{Error: msg, Code: code})
}

// informerIPKey derives a rate-limit key from an IP string using HMAC.
func informerIPKeyHMAC(rateLimitKey []byte, host string) string {
	mac := hmac.New(sha256.New, rateLimitKey)
	mac.Write([]byte(rateLimitDomain))
	mac.Write([]byte(host))
	return hex.EncodeToString(mac.Sum(nil))
}

// remoteHost extracts the canonical host from r.RemoteAddr.
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return host
}

// handleAccess handles POST /v2/informer/access.
// Body: {"wallet_address":"...","city":"..."}
// Returns: {"bot_url":"...","expires_at":"...","city":"...","raw_token":"..."}
func (h *InformerHandler) handleAccess(w http.ResponseWriter, r *http.Request) {
	// Rate limit by IP.
	ipKey := informerIPKeyHMAC(h.rateLimitKey, remoteHost(r))
	if !h.lim.Allow(ipKey) {
		informerError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}

	var req struct {
		WalletAddress string `json:"wallet_address"`
		City          string `json:"city"`
	}
	if !decodeStrict(w, r, &req) {
		return
	}

	req.WalletAddress = strings.TrimSpace(req.WalletAddress)
	req.City = strings.ToLower(strings.TrimSpace(req.City))

	if req.WalletAddress == "" {
		informerError(w, http.StatusBadRequest, "missing_wallet", "wallet_address required")
		return
	}
	if req.City == "" {
		informerError(w, http.StatusBadRequest, "missing_city", "city required")
		return
	}

	// Validate address and detect currency.
	_, currency, valErr := validateAndNormalizeAddress(req.WalletAddress)
	if valErr != nil {
		informerError(w, http.StatusBadRequest, "invalid_address", "invalid wallet address")
		return
	}

	// Validate city.
	if !isInformerCity(req.City) {
		informerError(w, http.StatusBadRequest, "invalid_city", "city not supported")
		return
	}

	// Balance check. Wallet address used here and never stored.
	balanceUSD, balErr := h.balance.BalanceUSD(r.Context(), req.WalletAddress, currency)
	if balErr != nil {
		informerError(w, http.StatusServiceUnavailable, "provider_error", "balance check unavailable")
		return
	}
	if balanceUSD < InformerMinBalanceUSD {
		informerError(w, http.StatusPaymentRequired, "low_balance", "balance below $1000 floor")
		return
	}

	// Create token. City and balance threshold determine eligibility; wallet is discarded.
	rawToken, expiresAt, tokErr := h.svc.CreateAccess(req.City, balanceUSD)
	if tokErr != nil {
		if errors.Is(tokErr, ErrInformerLowBalance) {
			informerError(w, http.StatusPaymentRequired, "low_balance", "balance below $1000 floor")
			return
		}
		if errors.Is(tokErr, ErrInformerInvalidCity) {
			informerError(w, http.StatusBadRequest, "invalid_city", "city not supported")
			return
		}
		informerError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}

	botURL := "https://t.me/" + h.botUsername + "?start=" + rawToken
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, private")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"bot_url":    botURL,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
		"city":       req.City,
		"raw_token":  rawToken,
	})
}

// handleStatus handles POST /v2/informer/status.
// Body: {"raw_token":"..."}
// Returns: {"city":"...","state":"pending"|"claimed"}
func (h *InformerHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RawToken string `json:"raw_token"`
	}
	if !decodeStrict(w, r, &req) {
		return
	}
	if req.RawToken == "" {
		informerError(w, http.StatusBadRequest, "missing_token", "raw_token required")
		return
	}

	city, state, statusErr := h.svc.QueryStatus(req.RawToken)
	if errors.Is(statusErr, ErrInformerTokenNotFound) {
		informerError(w, http.StatusNotFound, "token_not_found", "token not found")
		return
	}
	if errors.Is(statusErr, ErrInformerTokenExpired) {
		informerError(w, http.StatusGone, "token_expired", "token expired")
		return
	}
	if statusErr != nil {
		informerError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, private")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{"city": city, "state": state}) //nolint:errcheck
}
