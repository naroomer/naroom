package middleware

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	ncrypto "naroom/internal/crypto"
)

type contextKey int

const (
	ctxWalletHash contextKey = iota
	ctxWalletRole
	ctxSessionTokenHash
)

// SessionWalletHash returns the HMAC wallet hash stored in ctx after session validation, or "".
// Handlers use this directly as the identity key for all DB queries — no plain address needed.
func SessionWalletHash(ctx context.Context) string {
	v, _ := ctx.Value(ctxWalletHash).(string)
	return v
}

// SessionRole returns the role ("client" or "peer") stored in ctx, or "".
func SessionRole(ctx context.Context) string {
	v, _ := ctx.Value(ctxWalletRole).(string)
	return v
}

// SessionTokenHash returns the SHA-256 hex digest of the session token stored in ctx, or "".
// Set by RequireSession for Bearer-token auth; empty in dev-mode wallet-bypass path.
func SessionTokenHash(ctx context.Context) string {
	v, _ := ctx.Value(ctxSessionTokenHash).(string)
	return v
}

// HashToken returns the SHA-256 hex digest of a raw session token.
func HashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// RequireSession is middleware that validates the Bearer token in the Authorization header.
// On success it stores wallet_hash and role in the request context.
// Skipped when devMode is true and the Authorization header is absent — the wallet address
// from X-Dev-Wallet is then hashed with hashKey and stored as wallet_hash (dev only).
func RequireSession(db *sql.DB, devMode bool, hashKey []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")

			// ── Dev mode shortcut ────────────────────────────────────────────
			if devMode && authHeader == "" {
				wallet := r.Header.Get("X-Dev-Wallet")
				role := r.Header.Get("X-Dev-Role")
				if wallet != "" && role != "" {
					walletHash := ncrypto.WalletHash(hashKey, wallet)
					ctx := context.WithValue(r.Context(), ctxWalletHash, walletHash)
					ctx = context.WithValue(ctx, ctxWalletRole, role)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				// Fall through to normal auth — still require a token in dev mode
				// if neither header is set (e.g., direct API calls).
			}

			// ── Parse Bearer token ────────────────────────────────────────────
			if !strings.HasPrefix(authHeader, "Bearer ") {
				http.Error(w, `{"error":"authorization required"}`, http.StatusUnauthorized)
				return
			}
			rawToken := strings.TrimPrefix(authHeader, "Bearer ")
			if rawToken == "" {
				http.Error(w, `{"error":"empty token"}`, http.StatusUnauthorized)
				return
			}

			tokenHash := HashToken(rawToken)
			now := time.Now().Unix()

			var walletHash, role string
			err := db.QueryRow(`
				SELECT wallet_hash, role FROM sessions
				WHERE token_hash = ? AND expires_at > ? AND revoked_at IS NULL
			`, tokenHash, now).Scan(&walletHash, &role)
			if err != nil {
				http.Error(w, `{"error":"invalid or expired session"}`, http.StatusUnauthorized)
				return
			}

			// Update last_seen_at asynchronously (non-critical)
			go db.Exec(`UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?`, now, tokenHash)

			ctx := context.WithValue(r.Context(), ctxWalletHash, walletHash)
			ctx = context.WithValue(ctx, ctxWalletRole, role)
			ctx = context.WithValue(ctx, ctxSessionTokenHash, tokenHash)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
