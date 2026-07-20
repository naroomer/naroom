package v2

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"unicode/utf8"
)

// DisplayNameGenerator generates a random anonymous display name for a listing.
// The name is stable for the lifetime of a listing — it is generated once at
// first publish and never regenerated on reactivation.
type DisplayNameGenerator interface {
	Generate() (string, error)
}

var nameAdjectives = []string{
	"calm", "brave", "gentle", "steady", "quiet",
	"warm", "clear", "swift", "bold", "bright",
	"sharp", "still", "kind", "neat", "wise",
	"fresh", "keen", "deep", "fair", "true",
}

var nameNouns = []string{
	"river", "stone", "forest", "cloud", "star",
	"dawn", "field", "valley", "bridge", "flame",
	"shore", "ridge", "creek", "grove", "wind",
	"slope", "trail", "peak", "marsh", "bloom",
}

// Namespace: 20 adjectives × 20 nouns × 2^128 hex suffixes.
// The suffix alone provides 128 bits of random entropy, making the practical
// namespace effectively unlimited. The DB UNIQUE constraint and bounded retry
// remain as guards against the astronomically unlikely collision.
const maxDisplayNameLen = 64

// RandomDisplayNameGenerator generates names of the form "<adj>_<noun>_<32 hex chars>"
// using crypto/rand for both word selection and suffix generation.
//
// Example: "calm_river_a3f7c1e2d4b6890012345678901234ab"
//
// The 32-char lowercase hex suffix encodes exactly 16 random bytes (128 bits of entropy).
type RandomDisplayNameGenerator struct{}

// NewRandomDisplayNameGenerator creates a RandomDisplayNameGenerator.
func NewRandomDisplayNameGenerator() *RandomDisplayNameGenerator {
	return &RandomDisplayNameGenerator{}
}

// Generate returns a random display name, e.g. "calm_river_a3f7c1e2d4b6890012345678901234ab".
// The suffix contains exactly 16 crypto/rand bytes encoded as lowercase hex (128 random bits).
// No timestamp, counter, wallet, contact, flow or listing fragment is used.
func (g *RandomDisplayNameGenerator) Generate() (string, error) {
	adj, err := randListChoice(nameAdjectives)
	if err != nil {
		return "", err
	}
	noun, err := randListChoice(nameNouns)
	if err != nil {
		return "", err
	}
	var suffix [16]byte
	if _, err = rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("v2: crypto/rand: %w", err)
	}
	return fmt.Sprintf("%s_%s_%s", adj, noun, hex.EncodeToString(suffix[:])), nil
}

// validateDisplayName verifies that name:
//   - is non-empty;
//   - is valid UTF-8;
//   - does not exceed maxDisplayNameLen bytes (no mid-rune slicing);
//   - contains only lowercase ASCII letters, digits, or underscore.
//
// It never accepts output that would need silent truncation or correction.
func validateDisplayName(name string) error {
	if name == "" {
		return errors.New("v2: display name is empty")
	}
	if !utf8.ValidString(name) {
		return errors.New("v2: display name is not valid UTF-8")
	}
	if len(name) > maxDisplayNameLen {
		return fmt.Errorf("v2: display name exceeds %d bytes", maxDisplayNameLen)
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return fmt.Errorf("v2: display name contains disallowed character")
		}
	}
	return nil
}

func randListChoice(list []string) (string, error) {
	if len(list) == 0 {
		return "", errors.New("v2: randListChoice: empty list")
	}
	idx, err := randN(len(list))
	if err != nil {
		return "", err
	}
	return list[idx], nil
}

func randN(max int) (int, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("v2: crypto/rand: %w", err)
	}
	return int(binary.BigEndian.Uint32(b[:]) % uint32(max)), nil
}
