package v2

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// ContactCipher encrypts and decrypts raw contact handles for storage.
// The ciphertext uses AES-256-GCM with per-listing AAD so that a ciphertext
// from one listing cannot be replayed into another.
type ContactCipher interface {
	Encrypt(plaintext, listingID, flowID, contactType string) (ciphertextHex, nonceHex, keyVersion string, err error)
	Decrypt(ciphertextHex, nonceHex, keyVersion, listingID, flowID, contactType string) (plaintext string, err error)
}

// AESGCMContactCipher implements ContactCipher using AES-256-GCM.
// Key must be exactly 32 bytes. keyVersion is stored alongside ciphertext
// so that key rotation can be performed without immediate re-encryption.
type AESGCMContactCipher struct {
	key        []byte
	keyVersion string
}

// NewAESGCMContactCipher creates an AESGCMContactCipher.
// key must be exactly 32 bytes; keyVersion must not be empty.
func NewAESGCMContactCipher(key []byte, keyVersion string) (*AESGCMContactCipher, error) {
	if len(key) != 32 {
		return nil, errors.New("v2: AESGCMContactCipher: key must be exactly 32 bytes for AES-256")
	}
	if keyVersion == "" {
		return nil, errors.New("v2: AESGCMContactCipher: keyVersion must not be empty")
	}
	k := make([]byte, 32)
	copy(k, key)
	return &AESGCMContactCipher{key: k, keyVersion: keyVersion}, nil
}

// aad builds the additional authenticated data string for a specific listing+flow+type.
// AAD binds the ciphertext to its listing so cross-listing blob swaps are detected.
func (c *AESGCMContactCipher) aad(listingID, flowID, contactType string) []byte {
	return []byte("naroom:v2:contact-aad:" + c.keyVersion + ":" + listingID + ":" + flowID + ":" + contactType)
}

// Encrypt encrypts plaintext using AES-256-GCM and returns hex-encoded ciphertext,
// nonce, and key version. A fresh random nonce is generated for every call.
func (c *AESGCMContactCipher) Encrypt(plaintext, listingID, flowID, contactType string) (string, string, string, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", "", "", fmt.Errorf("v2: encrypt: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", "", fmt.Errorf("v2: encrypt gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", "", "", fmt.Errorf("v2: encrypt nonce: %w", err)
	}
	aad := c.aad(listingID, flowID, contactType)
	ct := gcm.Seal(nil, nonce, []byte(plaintext), aad)
	return hex.EncodeToString(ct), hex.EncodeToString(nonce), c.keyVersion, nil
}

// Decrypt decrypts a hex-encoded AES-GCM ciphertext and verifies its AAD.
// Returns an error if the key version is unknown or authentication fails.
func (c *AESGCMContactCipher) Decrypt(ciphertextHex, nonceHex, keyVersion, listingID, flowID, contactType string) (string, error) {
	if keyVersion != c.keyVersion {
		return "", errors.New("v2: decrypt: unknown key version")
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", fmt.Errorf("v2: decrypt: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("v2: decrypt gcm: %w", err)
	}
	ct, err := hex.DecodeString(ciphertextHex)
	if err != nil {
		return "", errors.New("v2: decrypt: malformed ciphertext")
	}
	nonce, err := hex.DecodeString(nonceHex)
	if err != nil {
		return "", errors.New("v2: decrypt: malformed nonce")
	}
	aad := c.aad(listingID, flowID, contactType)
	pt, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return "", errors.New("v2: decrypt: authentication failed")
	}
	return string(pt), nil
}
