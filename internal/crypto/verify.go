package crypto

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
)

// WalletVerifier verifies that a wallet address signed a given message.
type WalletVerifier interface {
	Verify(address, message, signatureBase64 string) error
}

// VerifyBTCMessage verifies a Bitcoin signed message (legacy format, all common address types).
// Supports P2PKH (1...), P2WPKH (bc1...).
func VerifyBTCMessage(address, message, sigBase64 string) error {
	return verifyMessage(address, message, sigBase64, "Bitcoin Signed Message:\n", &chaincfg.MainNetParams)
}

// VerifyLTCMessage verifies a Litecoin signed message (same algorithm, different magic + network).
func VerifyLTCMessage(address, message, sigBase64 string) error {
	return verifyMessage(address, message, sigBase64, "Litecoin Signed Message:\n", ltcMainNetParams)
}

// ltcMainNetParams defines the minimal Litecoin mainnet address parameters.
// btcd does not bundle Litecoin, so we declare just what we need.
var ltcMainNetParams = &chaincfg.Params{
	Name:             "ltc-mainnet",
	PubKeyHashAddrID: 0x30, // 'L' addresses
	ScriptHashAddrID: 0x32, // 'M' addresses
	Bech32HRPSegwit:  "ltc",
}

// verifyMessage is the shared implementation for BTC and LTC.
func verifyMessage(address, message, sigBase64, magic string, params *chaincfg.Params) error {
	sigBytes, err := base64.StdEncoding.DecodeString(sigBase64)
	if err != nil {
		return errors.New("invalid signature encoding: must be base64")
	}
	if len(sigBytes) != 65 {
		return fmt.Errorf("invalid signature length: got %d, want 65", len(sigBytes))
	}

	hash := bitcoinMessageHash(magic, message)

	pubKey, compressed, err := ecdsa.RecoverCompact(sigBytes, hash)
	if err != nil {
		return fmt.Errorf("signature recovery failed: %w", err)
	}

	// Try all address types the recovered key can produce and see if any matches.
	candidates, err := addressCandidates(pubKey.SerializeCompressed(), pubKey.SerializeUncompressed(), compressed, params)
	if err != nil {
		return fmt.Errorf("address derivation failed: %w", err)
	}

	for _, candidate := range candidates {
		if candidate == address {
			return nil
		}
	}
	return errors.New("signature does not match address")
}

// addressCandidates returns all Bitcoin-style addresses the recovered key could map to.
func addressCandidates(compressed, uncompressed []byte, isCompressed bool, params *chaincfg.Params) ([]string, error) {
	var out []string

	// P2PKH compressed
	if addr, err := p2pkhAddress(compressed, params); err == nil {
		out = append(out, addr)
	}

	// P2PKH uncompressed (only if the signature indicated uncompressed key)
	if !isCompressed {
		if addr, err := p2pkhAddress(uncompressed, params); err == nil {
			out = append(out, addr)
		}
	}

	// P2WPKH native segwit (bc1... / ltc1...) — only compressed keys
	if params.Bech32HRPSegwit != "" {
		if addr, err := p2wpkhAddress(compressed, params); err == nil {
			out = append(out, addr)
		}
	}

	return out, nil
}

func p2pkhAddress(pubKeyBytes []byte, params *chaincfg.Params) (string, error) {
	hash160 := btcutil.Hash160(pubKeyBytes)
	addr, err := btcutil.NewAddressPubKeyHash(hash160, params)
	if err != nil {
		return "", err
	}
	return addr.EncodeAddress(), nil
}

func p2wpkhAddress(pubKeyBytes []byte, params *chaincfg.Params) (string, error) {
	hash160 := btcutil.Hash160(pubKeyBytes)
	addr, err := btcutil.NewAddressWitnessPubKeyHash(hash160, params)
	if err != nil {
		return "", err
	}
	return addr.EncodeAddress(), nil
}

// bitcoinMessageHash computes the double-SHA256 hash used for Bitcoin-style message signing.
// Format: varint(len(magic)) + magic + varint(len(message)) + message
func bitcoinMessageHash(magic, message string) []byte {
	payload := appendVarString(nil, magic)
	payload = appendVarString(payload, message)
	h1 := sha256.Sum256(payload)
	h2 := sha256.Sum256(h1[:])
	return h2[:]
}

// appendVarString appends a Bitcoin-varint-prefixed string to dst.
func appendVarString(dst []byte, s string) []byte {
	n := len(s)
	switch {
	case n < 0xfd:
		dst = append(dst, byte(n))
	case n <= 0xffff:
		dst = append(dst, 0xfd, byte(n), byte(n>>8))
	case n <= 0xffffffff:
		dst = append(dst, 0xfe, byte(n), byte(n>>8), byte(n>>16), byte(n>>24))
	default:
		dst = append(dst, 0xff,
			byte(n), byte(n>>8), byte(n>>16), byte(n>>24),
			byte(n>>32), byte(n>>40), byte(n>>48), byte(n>>56))
	}
	return append(dst, s...)
}
