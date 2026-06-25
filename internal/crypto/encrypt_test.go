package crypto

import (
	"strings"
	"testing"
)

// Test matrix:
// | test                      | invariant                                      | bug it catches                                   |
// |---------------------------|------------------------------------------------|--------------------------------------------------|
// | TestEncryptDecryptRoundTrip | encrypt→decrypt returns original address      | broken cipher, wrong key derivation              |
// | TestDecryptWrongKey        | wrong key → error (not silent garbage)        | key confusion, accidental plain-text fallback    |
// | TestDecryptTamperedData    | bit-flip in ciphertext → auth error           | missing GCM integrity check                      |
// | TestEncryptProducesUnique  | same input → different ciphertext each time   | deterministic nonce (catastrophic GCM failure)   |
// | TestPrepareEncKeyDev       | dev mode without key → derives stable key     | dev mode hard-failing when key not set           |
// | TestPrepareEncKeyProd      | prod mode without key → hard error            | silent plain-text fallback in production         |
// | TestDecryptTooShort        | short ciphertext → error, not panic           | out-of-bounds read on malformed input            |

func testKey() []byte {
	key, _ := PrepareEncKey("test-key-for-unit-tests-only-32b", "", false)
	return key
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	// Invariant: encrypt(key, addr) → decrypt(key, result) == addr
	addresses := []string{
		"1A1zP1eP5QGefi2DMPTfTL5SLmv7Divfna",
		"bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
		"ltc1q3w3pzrh3vs6g87kxpn9a8jdmm5nj60e8w7hnp",
		"LQ3Khqf5HRyRpZXiKrNe1qdQCHxiomKXbV",
	}
	key := testKey()
	for _, addr := range addresses {
		enc, err := EncryptAddress(key, addr)
		if err != nil {
			t.Fatalf("EncryptAddress(%q): %v", addr, err)
		}
		dec, err := DecryptAddress(key, enc)
		if err != nil {
			t.Fatalf("DecryptAddress(%q): %v", addr, err)
		}
		if dec != addr {
			t.Errorf("round-trip failed: got %q, want %q", dec, addr)
		}
	}
}

func TestDecryptWrongKey(t *testing.T) {
	// Invariant: wrong key must return an error — never silently return garbage
	key1 := testKey()
	key2, _ := PrepareEncKey("completely-different-key-here-32", "", false)

	enc, _ := EncryptAddress(key1, "1A1zP1eP5QGefi2DMPTfTL5SLmv7Divfna")
	_, err := DecryptAddress(key2, enc)
	if err == nil {
		t.Fatal("expected error with wrong key, got nil")
	}
}

func TestDecryptTamperedData(t *testing.T) {
	// Invariant: GCM auth tag must catch any bit flip in ciphertext
	key := testKey()
	enc, _ := EncryptAddress(key, "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq")

	// Flip one byte in the middle of the base64
	b := []byte(enc)
	b[len(b)/2] ^= 0xFF
	tampered := string(b)

	_, err := DecryptAddress(key, tampered)
	if err == nil {
		t.Fatal("expected error with tampered ciphertext, got nil")
	}
}

func TestEncryptProducesUnique(t *testing.T) {
	// Invariant: same plaintext → different ciphertext each time (random nonce)
	key := testKey()
	addr := "1A1zP1eP5QGefi2DMPTfTL5SLmv7Divfna"
	enc1, _ := EncryptAddress(key, addr)
	enc2, _ := EncryptAddress(key, addr)
	if enc1 == enc2 {
		t.Fatal("two encryptions of same address produced identical ciphertext — nonce is not random")
	}
}

func TestPrepareEncKeyDev(t *testing.T) {
	// Invariant: dev mode without key → stable derived key (same salt → same key)
	k1, err := PrepareEncKey("", "my-server-salt", true)
	if err != nil {
		t.Fatalf("dev mode PrepareEncKey: %v", err)
	}
	k2, _ := PrepareEncKey("", "my-server-salt", true)
	if string(k1) != string(k2) {
		t.Fatal("dev mode key is not deterministic")
	}
	if len(k1) != 32 {
		t.Fatalf("key length: got %d, want 32", len(k1))
	}
}

func TestPrepareEncKeyProd(t *testing.T) {
	// Invariant: production without key must hard-fail, never fall back to plain text
	_, err := PrepareEncKey("", "my-server-salt", false)
	if err == nil {
		t.Fatal("expected error in production mode without key, got nil")
	}
	if !strings.Contains(err.Error(), "WALLET_ENC_KEY") {
		t.Errorf("error should mention WALLET_ENC_KEY, got: %v", err)
	}
}

func TestDecryptTooShort(t *testing.T) {
	// Invariant: malformed/short input must not panic, must return error
	key := testKey()
	_, err := DecryptAddress(key, "abc")
	if err == nil {
		t.Fatal("expected error for too-short ciphertext, got nil")
	}
}
