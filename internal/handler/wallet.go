package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"naroom/internal/crypto"
)

// issueSession creates a new session token, stores the hash, and returns the raw token.
// walletHash must be pre-computed with crypto.WalletHash — plain address is never stored in sessions.
// principalID may be "" for legacy sessions; use createPrincipal first for new sessions.
func (h *Handler) issueSession(principalID, walletHash, role, currency string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hashBytes := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(hashBytes[:])

	now := time.Now().Unix()
	expiresAt := now + 86400 // 24h

	var nullPrincipal sql.NullString
	if principalID != "" {
		nullPrincipal = sql.NullString{String: principalID, Valid: true}
	}

	_, err := h.DB.Exec(`
		INSERT INTO sessions (token_hash, wallet_hash, currency, role, created_at, expires_at, principal_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, tokenHash, walletHash, currency, role, now, expiresAt, nullPrincipal)
	if err != nil {
		return "", err
	}
	return token, nil
}

// createPrincipal creates a new principal and returns (principalID, rawRecoveryCode, error).
// The raw recovery code is returned ONCE and must be shown to the user immediately.
func (h *Handler) createPrincipal(role string) (string, string, error) {
	principalID := crypto.NewID("prn")
	// Generate 32-byte random recovery code
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	rawRecovery := hex.EncodeToString(raw)
	recoveryHash := crypto.WalletHash(h.HashKey, rawRecovery) // HMAC(HASH_KEY, raw)
	now := time.Now().Unix()
	_, err := h.DB.Exec(`
		INSERT INTO principals (id, recovery_hash, role, created_at)
		VALUES (?, ?, ?, ?)
	`, principalID, recoveryHash, role, now)
	if err != nil {
		return "", "", err
	}
	return principalID, rawRecovery, nil
}

// linkWalletToPrincipal sets the billing wallet for a principal and updates all active sessions.
func (h *Handler) linkWalletToPrincipal(principalID, walletHash, currency string) error {
	now := time.Now().Unix()
	_, err := h.DB.Exec(`
		UPDATE principals SET wallet_hash = ?, currency = ?, last_seen = ?
		WHERE id = ?
	`, walletHash, currency, now, principalID)
	if err != nil {
		return err
	}
	// Update active sessions so middleware returns real wallet_hash
	_, err = h.DB.Exec(`
		UPDATE sessions SET wallet_hash = ?
		WHERE principal_id = ? AND revoked_at IS NULL AND expires_at > ?
	`, walletHash, principalID, now)
	return err
}

func (h *Handler) upsertWalletSession(walletAddress, role, currency string) error {
	now := time.Now().Unix()

	var minRequired float64
	switch role {
	case "client":
		minRequired = h.clientMinBalance()
	default: // peer
		minRequired = h.peerMinBalance()
	}

	walletHash := crypto.WalletHash(h.HashKey, walletAddress)

	// Encrypt the plain address before writing — plain address must never be stored in wallet_sessions.
	addrEnc, err := crypto.EncryptAddress(h.WalletEncKey, walletAddress)
	if err != nil {
		return fmt.Errorf("upsertWalletSession: encrypt: %w", err)
	}

	_, err = h.DB.Exec(`
		INSERT INTO wallet_sessions (wallet_hash, wallet_address_enc, currency, role, balance_status, min_required_usd, balance_usd, last_checked_at, verified, first_seen, created_at)
		VALUES (?, ?, ?, ?, 'ok', ?, ?, ?, TRUE, ?, ?)
		ON CONFLICT(wallet_hash) DO UPDATE SET
			wallet_address_enc = excluded.wallet_address_enc,
			currency           = excluded.currency,
			balance_status     = 'ok',
			min_required_usd   = excluded.min_required_usd,
			last_checked_at    = excluded.last_checked_at,
			verified           = TRUE
			-- role is intentionally NOT updated to prevent role overwrite
	`, walletHash, addrEnc, currency, role, minRequired, minRequired, now, now, now, now)
	if err != nil {
		return err
	}

	// Ensure reputation entry exists for counselors
	if role == "peer" {
		counselorHash := crypto.WalletHash(h.HashKey, walletAddress)
		h.DB.Exec(`
			INSERT OR IGNORE INTO reputation (counselor_hash, region, first_seen)
			VALUES (?, '', ?)
		`, counselorHash, now)
	}
	return nil
}
