package handler

import (
	"net/http"
	"strings"
	"time"

	"naroom/internal/middleware"
)

// SessionRefresh handles POST /session/refresh — rotates the session token.
// Returns a new token; the old one is revoked.
func (h *Handler) SessionRefresh(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		writeError(w, 401, "authorization required")
		return
	}
	rawToken := strings.TrimPrefix(authHeader, "Bearer ")
	oldHash := middleware.HashToken(rawToken)

	now := time.Now().Unix()

	var walletHash, role, currency string
	err := h.DB.QueryRow(`
		SELECT wallet_hash, role, currency FROM sessions
		WHERE token_hash = ? AND expires_at > ? AND revoked_at IS NULL
	`, oldHash, now).Scan(&walletHash, &role, &currency)
	if err != nil {
		writeError(w, 401, "invalid or expired session")
		return
	}

	// Issue new token
	newToken, err := h.issueSession(walletHash, role, currency)
	if err != nil {
		writeError(w, 500, "session creation failed")
		return
	}

	// Revoke old token
	h.DB.Exec(`UPDATE sessions SET revoked_at = ? WHERE token_hash = ?`, now, oldHash)

	writeJSON(w, 200, map[string]any{
		"token":      newToken,
		"expires_at": now + 86400,
	})
}

// SessionRevoke handles POST /session/revoke — invalidates the current session.
func (h *Handler) SessionRevoke(w http.ResponseWriter, r *http.Request) {
	wallet := middleware.SessionWalletHash(r.Context())
	if wallet == "" {
		writeError(w, 401, "authorization required")
		return
	}
	authHeader := r.Header.Get("Authorization")
	rawToken := strings.TrimPrefix(authHeader, "Bearer ")
	tokenHash := middleware.HashToken(rawToken)
	h.DB.Exec(`UPDATE sessions SET revoked_at = ? WHERE token_hash = ?`, time.Now().Unix(), tokenHash)
	writeJSON(w, 200, map[string]string{"status": "revoked"})
}
