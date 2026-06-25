package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

// EncryptAddress encrypts a wallet address with AES-256-GCM.
// Returns a base64url-encoded string: nonce (12 bytes) + GCM ciphertext + auth tag.
// Each call produces different output (random nonce) — safe for storage.
func EncryptAddress(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("encrypt: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("encrypt: new gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("encrypt: nonce: %w", err)
	}
	// Seal appends ciphertext+tag to nonce → result is nonce||ciphertext||tag
	out := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawURLEncoding.EncodeToString(out), nil
}

// DecryptAddress reverses EncryptAddress. Returns an error on key mismatch or tampered data
// (GCM authentication tag protects integrity).
func DecryptAddress(key []byte, encoded string) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decrypt: base64: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("decrypt: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("decrypt: new gcm: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize+gcm.Overhead() {
		return "", fmt.Errorf("decrypt: ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: authentication failed (wrong key or tampered data)")
	}
	return string(plaintext), nil
}

// IsLikelyBTC returns true if the address looks like a Bitcoin address (1…, 3…, bc1…).
// Used when currency is not stored explicitly (e.g. migration of old rows).
func IsLikelyBTC(addr string) bool {
	if len(addr) == 0 {
		return true
	}
	return addr[0] == '1' || addr[0] == '3' || (len(addr) > 3 && addr[:3] == "bc1")
}

// PrepareEncKey normalises a raw key string to exactly 32 bytes via SHA-256.
// In dev mode: if rawKey is empty, derives a key from serverSalt (so dev doesn't need extra config).
// In production: rawKey must be non-empty; hard-fails otherwise.
func PrepareEncKey(rawKey, serverSalt string, devMode bool) ([]byte, error) {
	if rawKey != "" {
		h := sha256.Sum256([]byte(rawKey))
		return h[:], nil
	}
	if devMode {
		// Dev-only fallback: derive from SERVER_SALT with a fixed domain separator.
		// This produces a stable key for local testing without requiring extra config.
		h := sha256.Sum256(append([]byte("naroom-wallet-enc:"), []byte(serverSalt)...))
		return h[:], nil
	}
	return nil, fmt.Errorf("WALLET_ENC_KEY is required in production (DEV_MODE is not set)")
}
