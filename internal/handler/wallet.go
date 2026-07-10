package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"naroom/internal/crypto"
)

// issueSession creates a new session token, stores the hash, and returns the raw token.
// walletHash must be pre-computed with crypto.WalletHash — plain address is never stored in sessions.
func (h *Handler) issueSession(walletHash, role, currency string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hashBytes := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(hashBytes[:])

	now := time.Now().Unix()
	expiresAt := now + 86400 // 24h

	_, err := h.DB.Exec(`
		INSERT INTO sessions (token_hash, wallet_hash, currency, role, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, tokenHash, walletHash, currency, role, now, expiresAt)
	if err != nil {
		return "", err
	}
	return token, nil
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
			role               = excluded.role,
			balance_status     = 'ok',
			min_required_usd   = excluded.min_required_usd,
			last_checked_at    = excluded.last_checked_at,
			verified           = TRUE
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
