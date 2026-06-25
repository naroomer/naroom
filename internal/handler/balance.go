package handler

import (
	"net/http"

	"naroom/internal/crypto"
)

// BalanceStatus handles GET /api/balance-status?wallet=xxx.
// Looks up by HMAC hash — plain address is never stored.
func (h *Handler) BalanceStatus(w http.ResponseWriter, r *http.Request) {
	wallet := r.URL.Query().Get("wallet")
	if wallet == "" {
		writeError(w, 400, "wallet parameter required")
		return
	}

	walletHash := crypto.WalletHash(h.HashKey, wallet)

	var status, role string
	var minRequired float64
	var lastChecked *int64

	err := h.DB.QueryRow(`
		SELECT balance_status, role, min_required_usd, last_checked_at
		FROM wallet_sessions WHERE wallet_hash = ?
	`, walletHash).Scan(&status, &role, &minRequired, &lastChecked)
	if err != nil {
		writeError(w, 404, "wallet not found")
		return
	}

	writeJSON(w, 200, map[string]any{
		"status":           status,
		"role":             role,
		"min_required_usd": minRequired,
		"last_checked_at":  lastChecked,
	})
}
