package crypto

import (
	"encoding/base64"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
)

// signMessage signs a message the same way Bitcoin wallets do (compact ECDSA).
func signMessage(privKey *btcec.PrivateKey, magic, message string) string {
	hash := bitcoinMessageHash(magic, message)
	sig, _ := ecdsa.SignCompact(privKey, hash, true) // true = compressed
	return base64.StdEncoding.EncodeToString(sig)
}

func TestVerifyBTCMessage_P2PKH(t *testing.T) {
	// Generate a deterministic test key
	privKey, _ := btcec.PrivKeyFromBytes(make([]byte, 31)) // 31 zero bytes + implied structure
	// Use a proper 32-byte key
	keyBytes := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	privKey, pubKey := btcec.PrivKeyFromBytes(keyBytes[:])
	_ = pubKey

	// Derive P2PKH address
	hash160 := btcutil.Hash160(privKey.PubKey().SerializeCompressed())
	addr, _ := btcutil.NewAddressPubKeyHash(hash160, &chaincfg.MainNetParams)
	address := addr.EncodeAddress()

	message := "test message for signing"
	sig := signMessage(privKey, "Bitcoin Signed Message:\n", message)

	if err := VerifyBTCMessage(address, message, sig); err != nil {
		t.Fatalf("P2PKH verify failed: %v", err)
	}
}

func TestVerifyBTCMessage_P2WPKH(t *testing.T) {
	keyBytes := [32]byte{10, 20, 30, 40, 50}
	privKey, _ := btcec.PrivKeyFromBytes(keyBytes[:])

	hash160 := btcutil.Hash160(privKey.PubKey().SerializeCompressed())
	addr, _ := btcutil.NewAddressWitnessPubKeyHash(hash160, &chaincfg.MainNetParams)
	address := addr.EncodeAddress()

	message := "segwit test message"
	sig := signMessage(privKey, "Bitcoin Signed Message:\n", message)

	if err := VerifyBTCMessage(address, message, sig); err != nil {
		t.Fatalf("P2WPKH verify failed: %v", err)
	}
}

func TestVerifyBTCMessage_WrongAddress(t *testing.T) {
	keyBytes := [32]byte{99}
	privKey, _ := btcec.PrivKeyFromBytes(keyBytes[:])

	// Sign with one key but verify against a different address
	wrongKey := [32]byte{100}
	wrongPriv, _ := btcec.PrivKeyFromBytes(wrongKey[:])
	hash160 := btcutil.Hash160(wrongPriv.PubKey().SerializeCompressed())
	wrongAddr, _ := btcutil.NewAddressPubKeyHash(hash160, &chaincfg.MainNetParams)

	message := "tampered"
	sig := signMessage(privKey, "Bitcoin Signed Message:\n", message)

	if err := VerifyBTCMessage(wrongAddr.EncodeAddress(), message, sig); err == nil {
		t.Fatal("expected error for wrong address, got nil")
	}
}

func TestVerifyBTCMessage_WrongMessage(t *testing.T) {
	keyBytes := [32]byte{55}
	privKey, _ := btcec.PrivKeyFromBytes(keyBytes[:])

	hash160 := btcutil.Hash160(privKey.PubKey().SerializeCompressed())
	addr, _ := btcutil.NewAddressPubKeyHash(hash160, &chaincfg.MainNetParams)

	sig := signMessage(privKey, "Bitcoin Signed Message:\n", "original message")

	if err := VerifyBTCMessage(addr.EncodeAddress(), "tampered message", sig); err == nil {
		t.Fatal("expected error for wrong message, got nil")
	}
}

func TestVerifyLTCMessage_P2PKH(t *testing.T) {
	keyBytes := [32]byte{77, 88, 99}
	privKey, _ := btcec.PrivKeyFromBytes(keyBytes[:])

	hash160 := btcutil.Hash160(privKey.PubKey().SerializeCompressed())
	addr, _ := btcutil.NewAddressPubKeyHash(hash160, ltcMainNetParams)
	address := addr.EncodeAddress()

	message := "litecoin test message"
	sig := signMessage(privKey, "Litecoin Signed Message:\n", message)

	if err := VerifyLTCMessage(address, message, sig); err != nil {
		t.Fatalf("LTC P2PKH verify failed: %v", err)
	}
}

func TestVerifyBTCMessage_InvalidBase64(t *testing.T) {
	if err := VerifyBTCMessage("1abc", "msg", "not-valid-base64!!!"); err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestVerifyBTCMessage_ShortSignature(t *testing.T) {
	shortSig := base64.StdEncoding.EncodeToString([]byte("short"))
	if err := VerifyBTCMessage("1abc", "msg", shortSig); err == nil {
		t.Fatal("expected error for short signature")
	}
}
