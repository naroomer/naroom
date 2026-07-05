package middleware

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
)

// RequireNotBanned rejects requests from wallets whose banned_until > now.
// Must be applied AFTER RequireSession (needs wallet_hash in context).
// Banned wallets get 403 with {"error":"account banned","banned_until":<unix_ts>}.
// If no session (public route) or wallet not in abuse_counters, passes through.
func RequireNotBanned(db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			walletHash := SessionWalletHash(r.Context())
			if walletHash == "" {
				// No session context — public route, not our concern.
				next.ServeHTTP(w, r)
				return
			}

			now := time.Now().Unix()
			var bannedUntil int64
			err := db.QueryRow(
				`SELECT banned_until FROM abuse_counters WHERE client_hash = ? AND banned_until > ?`,
				walletHash, now,
			).Scan(&bannedUntil)
			if err == nil {
				// Active ban found.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]any{
					"error":        "account banned",
					"banned_until": bannedUntil,
				})
				return
			}
			// sql.ErrNoRows or any other error → not banned, pass through.
			next.ServeHTTP(w, r)
		})
	}
}
