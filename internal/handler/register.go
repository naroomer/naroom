package handler

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"naroom/internal/crypto"
)

// detectCurrencyFromAddress returns "BTC" or "LTC" based on address prefix.
// ltc1…, L…, M… → LTC; bc1…, 1…, 3… → BTC.
// Returns "" if unknown (frontend-supplied value is used as fallback).
func detectCurrencyFromAddress(addr string) string {
	switch {
	case strings.HasPrefix(addr, "ltc1"), strings.HasPrefix(addr, "LTC1"),
		strings.HasPrefix(addr, "L"), strings.HasPrefix(addr, "M"):
		return "LTC"
	case strings.HasPrefix(addr, "bc1"), strings.HasPrefix(addr, "BC1"),
		strings.HasPrefix(addr, "1"), strings.HasPrefix(addr, "3"):
		return "BTC"
	}
	return ""
}

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
	// Auto-correct currency from address prefix — frontend may send wrong value
	// due to caching or race conditions with auto-detect.
	if detected := detectCurrencyFromAddress(req.WalletAddress); detected != "" {
		req.Currency = detected
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
		minUSD = h.clientMinBalance()
	default: // peer
		minUSD = h.peerMinBalance()
	}

	// Use cached balance if wallet was verified within the last 5 minutes.
	// Avoids hammering BlockCypher/Mempool on repeated register calls.
	walletHash := crypto.WalletHash(h.HashKey, req.WalletAddress)
	var cachedBalance float64
	cacheHit := false
	if err := h.DB.QueryRow(`
		SELECT balance_usd FROM wallet_sessions
		WHERE wallet_hash = ? AND balance_status = 'ok' AND last_checked_at > strftime('%s','now') - 300
		LIMIT 1
	`, walletHash).Scan(&cachedBalance); err == nil {
		cacheHit = true
	}

	var balanceUSD float64
	if cacheHit {
		balanceUSD = cachedBalance
	} else {
		var err error
		balanceUSD, err = h.checkBalanceUSD(req.WalletAddress, req.Currency)
		if err != nil {
			// If all balance APIs are unavailable (rate-limited, blocked), let the user
			// through. The actual crypto payment at listing/chat time will enforce funds.
			// This avoids blocking legitimate users when external APIs fail.
			log.Printf("balance check unavailable for %s (%s): %v — allowing through", req.Currency, req.Role, err)
			balanceUSD = minUSD // treat as passing
		}
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
			// Fallback to Blockchair when BlockCypher is rate-limited or unavailable.
			log.Printf("BlockCypher failed (%v), falling back to Blockchair", err)
			litoshis, err = h.Blockchair.GetLTCBalance(address)
			if err != nil {
				return 0, fmt.Errorf("LTC balance unavailable: %v", err)
			}
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
