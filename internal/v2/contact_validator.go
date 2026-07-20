package v2

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ProductionContactValidator implements ContactValidator.
// Format-only; no network lookups.
type ProductionContactValidator struct{}

// NewProductionContactValidator returns a new ProductionContactValidator.
func NewProductionContactValidator() *ProductionContactValidator {
	return &ProductionContactValidator{}
}

// reservedTelegramWords are path segments that do not identify users.
var reservedTelegramWords = map[string]bool{
	"joinchat":    true,
	"addstickers": true,
	"share":       true,
	"proxy":       true,
	"s":           true,
	"c":           true,
	"addtheme":    true,
}

// ValidateContact validates and normalizes a contact value for the given contactType.
// Raw contact is never included in error messages.
func (v *ProductionContactValidator) ValidateContact(contactType, rawContact string) (string, error) {
	// Limit raw input to 512 bytes before any parsing.
	if len(rawContact) > 512 {
		return "", errors.New("contact: input too long")
	}
	if !utf8.ValidString(rawContact) {
		return "", errors.New("contact: invalid UTF-8")
	}
	// Reject whitespace and control characters anywhere in the input.
	for _, r := range rawContact {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", errors.New("contact: whitespace or control characters not allowed")
		}
	}

	switch contactType {
	case "telegram":
		return validateTelegramContact(rawContact)
	case "signal":
		return validateSignalContact(rawContact)
	default:
		return "", errors.New("contact: unsupported contact type")
	}
}

// validateTelegramContact parses and normalizes a Telegram contact.
// Accepted forms:
//   - @username
//   - https://t.me/username
//   - https://username.t.me
//
// Output: https://t.me/<lowercase_username>
func validateTelegramContact(raw string) (string, error) {
	var username string

	if strings.HasPrefix(raw, "@") {
		// @username form
		username = raw[1:]
	} else {
		// URL form
		u, err := url.Parse(raw)
		if err != nil {
			return "", errors.New("contact: invalid telegram URL")
		}
		// Scheme must be https (url.Parse lowercases it).
		if u.Scheme != "https" {
			return "", errors.New("contact: telegram URL must use https scheme")
		}
		// No userinfo allowed.
		if u.User != nil {
			return "", errors.New("contact: telegram URL must not contain userinfo")
		}
		// No port allowed.
		if u.Port() != "" {
			return "", errors.New("contact: telegram URL must not contain port")
		}
		// No query allowed.
		if u.RawQuery != "" {
			return "", errors.New("contact: telegram URL must not contain query")
		}
		// No fragment allowed.
		if u.Fragment != "" {
			return "", errors.New("contact: telegram URL must not contain fragment")
		}

		host := strings.ToLower(u.Hostname())

		if host == "t.me" {
			// https://t.me/username
			// Reject percent-encoded paths: RawPath is set by url.Parse only when
			// the path contains escape sequences that differ from the decoded form.
			if u.RawPath != "" {
				return "", errors.New("contact: percent-encoded path not allowed in telegram URL")
			}
			// Path must be /<username> with no extra segments.
			path := strings.TrimPrefix(u.Path, "/")
			if path == "" {
				return "", errors.New("contact: telegram URL missing username path segment")
			}
			// Extra path segments (e.g., t.me/username/something) are rejected.
			if strings.Contains(path, "/") {
				return "", errors.New("contact: telegram URL has extra path segments")
			}
			username = path
		} else if strings.HasSuffix(host, ".t.me") {
			// https://username.t.me
			// Path must be empty or "/".
			if u.Path != "" && u.Path != "/" {
				return "", errors.New("contact: telegram subdomain URL must not have a path")
			}
			// Extract username from subdomain.
			username = strings.TrimSuffix(host, ".t.me")
			if username == "" {
				return "", errors.New("contact: telegram subdomain URL missing username")
			}
		} else {
			return "", errors.New("contact: unrecognized telegram URL host")
		}
	}

	// Reject phone links: username starting with '+'.
	if strings.HasPrefix(username, "+") {
		return "", errors.New("contact: telegram phone links are not accepted")
	}

	// Validate and normalize username.
	username = strings.ToLower(username)
	if !isValidTelegramUsername(username) {
		return "", errors.New("contact: invalid telegram username format")
	}

	// Reject reserved words.
	if reservedTelegramWords[username] {
		return "", errors.New("contact: telegram username is a reserved word")
	}

	return "https://t.me/" + username, nil
}

// isValidTelegramUsername checks ASCII [a-zA-Z0-9_], no minimum, max 64 chars.
// Called after lowercasing so only [a-z0-9_] need to be checked.
func isValidTelegramUsername(u string) bool {
	if u == "" || len(u) > 64 {
		return false
	}
	for _, c := range u {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// validateSignalContact parses and normalizes a Signal contact.
// Accepted form: https://signal.me/#<nonempty_opaque_payload>
// Fragment is preserved verbatim.
func validateSignalContact(raw string) (string, error) {
	// url.Parse does not handle fragment after '#' when it appears before '?'.
	// We need to split manually to preserve the fragment verbatim.

	// Find the '#' to split fragment.
	hashIdx := strings.Index(raw, "#")
	if hashIdx < 0 {
		return "", errors.New("contact: signal URL must contain a fragment (#)")
	}

	baseURL := raw[:hashIdx]
	fragment := raw[hashIdx+1:]

	// Parse the base (before fragment).
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", errors.New("contact: invalid signal URL")
	}

	// Scheme must be https.
	if u.Scheme != "https" {
		return "", errors.New("contact: signal URL must use https scheme")
	}
	// No userinfo.
	if u.User != nil {
		return "", errors.New("contact: signal URL must not contain userinfo")
	}
	// No port.
	if u.Port() != "" {
		return "", errors.New("contact: signal URL must not contain port")
	}
	// No query.
	if u.RawQuery != "" {
		return "", errors.New("contact: signal URL must not contain query")
	}
	// Hostname must be signal.me (case-insensitive).
	if strings.ToLower(u.Hostname()) != "signal.me" {
		return "", errors.New("contact: signal URL must have hostname signal.me")
	}
	// Path must be empty or "/".
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("contact: signal URL must not have a path")
	}

	// Fragment must be non-empty.
	if fragment == "" {
		return "", errors.New("contact: signal URL fragment must not be empty")
	}
	// Fragment max 300 bytes.
	if len(fragment) > 300 {
		return "", errors.New("contact: signal URL fragment too long")
	}
	// Fragment must not contain whitespace or control characters.
	for _, r := range fragment {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", errors.New("contact: signal URL fragment contains whitespace or control characters")
		}
	}
	// Fragment must be valid UTF-8.
	if !utf8.ValidString(fragment) {
		return "", errors.New("contact: signal URL fragment is not valid UTF-8")
	}

	return "https://signal.me/#" + fragment, nil
}
