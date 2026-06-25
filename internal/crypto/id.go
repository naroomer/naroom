package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// NewID generates a cryptographically random ID with prefix.
func NewID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return prefix + "_" + hex.EncodeToString(b)
}

// WalletHash returns HMAC-SHA256(key, "naroom:v1:" + NormalizeAddress(address)).
// This is the canonical keyed hash for all wallet addresses stored in the database.
// Use this instead of Hash() for any wallet address — HMAC is the correct construction
// for keyed hashing and avoids length-extension attacks from plain SHA256 concatenation.
func WalletHash(key []byte, address string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("naroom:v1:"))
	mac.Write([]byte(NormalizeAddress(address)))
	return hex.EncodeToString(mac.Sum(nil))
}

// NormalizeAddress canonicalizes a wallet address before hashing.
// Bech32 addresses (bc1..., ltc1...) are lowercased — they are case-insensitive
// by spec. Legacy addresses (1..., 3..., L..., M...) are case-sensitive and
// returned unchanged. Surrounding whitespace is always trimmed.
func NormalizeAddress(addr string) string {
	addr = strings.TrimSpace(addr)
	lower := strings.ToLower(addr)
	if strings.HasPrefix(lower, "bc1") || strings.HasPrefix(lower, "ltc1") {
		return lower
	}
	return addr
}

// Hash returns SHA256 hex of inputs concatenated. Use for non-wallet, non-keyed
// hashing only (e.g. pair deduplication from already-hashed values, token hashing
// in middleware which is handled separately). For wallet addresses always use WalletHash.
func Hash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprint(h, p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// RandomToken generates a random token for reviews etc.
func RandomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
