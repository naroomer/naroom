package handler

import (
	"net/http"

	"naroom/internal/crypto"
)

type walletRegisterReq struct {
	WalletAddress string `json:"wallet_address"`
	Currency      string `json:"currency"`
	Role          string `json:"role"`
}

// WalletRegister handles POST /wallet/register.
// Checks that the address has ≥$1000 balance and issues a session token.
// No signature required — proof of ownership happens at payment time.
func (h *Handler) WalletRegister(w http.ResponseWriter, r *http.Request) {
	var req walletRegisterReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}
	if req.WalletAddress == "" || req.Currency == "" || req.Role == "" {
		writeError(w, 400, "wallet_address, currency, role required")
		return
	}
	if req.Role != "client" && req.Role != "peer" {
		writeError(w, 400, "role must be client or peer")
		return
	}
	if req.Currency != "BTC" && req.Currency != "LTC" {
		writeError(w, 400, "currency must be BTC or LTC")
		return
	}

	// ── Dev mode: skip balance check ─────────────────────────────────────────
	if h.DevMode {
		if err := h.upsertWalletSession(req.WalletAddress, req.Role, req.Currency); err != nil {
			writeError(w, 500, "db error")
			return
		}
		walletHash := crypto.WalletHash(h.HashKey, req.WalletAddress)
		token, err := h.issueSession(walletHash, req.Role, req.Currency)
		if err != nil {
			writeError(w, 500, "session creation failed")
			return
		}
		writeJSON(w, 200, map[string]any{
			"status":        "ok",
			"session_token": token,
			"expires_in":    86400,
		})
		return
	}

	// ── Check balance ─────────────────────────────────────────────────────────
	var minUSD float64
	switch req.Role {
	case "client":
		minUSD = 150.0
	default: // peer
		minUSD = 1000.0
	}

	balanceUSD, err := h.checkBalanceUSD(req.WalletAddress, req.Currency)
	if err != nil {
		writeError(w, 502, "balance check failed: "+err.Error())
		return
	}
	if balanceUSD < minUSD {
		writeJSON(w, 402, map[string]any{
			"error":       "insufficient balance",
			"balance_usd": balanceUSD,
			"required_usd": minUSD,
		})
		return
	}

	// ── Issue session ─────────────────────────────────────────────────────────
	if err := h.upsertWalletSession(req.WalletAddress, req.Role, req.Currency); err != nil {
		writeError(w, 500, "db error")
		return
	}
	walletHash := crypto.WalletHash(h.HashKey, req.WalletAddress)
	token, err := h.issueSession(walletHash, req.Role, req.Currency)
	if err != nil {
		writeError(w, 500, "session creation failed")
		return
	}
	writeJSON(w, 200, map[string]any{
		"status":        "ok",
		"balance_usd":   balanceUSD,
		"session_token": token,
		"expires_in":    86400,
	})
}

// checkBalanceUSD returns the USD value of the wallet balance.
func (h *Handler) checkBalanceUSD(address, currency string) (float64, error) {
	switch currency {
	case "BTC":
		satoshis, err := h.Mempool.GetBalance(address)
		if err != nil {
			return 0, err
		}
		btc := float64(satoshis) / 1e8
		price, err := h.Prices.BTCPrice()
		if err != nil {
			return 0, err
		}
		return btc * price, nil

	case "LTC":
		litoshis, err := h.Blockcypher.GetBalance(address)
		if err != nil {
			return 0, err
		}
		ltc := float64(litoshis) / 1e8
		price, err := h.Prices.LTCPrice()
		if err != nil {
			return 0, err
		}
		return ltc * price, nil
	}
	return 0, nil
}
