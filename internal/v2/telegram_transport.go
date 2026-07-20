// Package v2 — Telegram transport for V2 notification delivery.
//
// Privacy invariants:
//   - Raw tokens are never stored; only HMAC-SHA256 hashes.
//   - chat_id is encrypted at rest using AES-256-GCM with binding-ref AAD.
//   - No permanent Client-Telegram identity is stored.
//   - flow_id, wallet, management code are never included in error responses.
package v2

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ── Token constants and primitives ─────────────────────────────────────────────

const telegramLinkDomain = "naroom:v2:telegram-link:"
const telegramLinkTTL = 15 * time.Minute
const leaseDuration = 30 * time.Second

// bindingRefGenerator is injectable for tests to simulate collisions.
type bindingRefGenerator func() (string, error)

// errBindingRefCollision is returned when a binding_ref UNIQUE constraint fires.
var errBindingRefCollision = errors.New("v2: binding_ref UNIQUE collision")

// maxBindingRefRetries caps the retry loop in HandleWebhook for binding_ref collisions.
const maxBindingRefRetries = 5

// newRawToken returns 32 crypto/rand bytes encoded as base64.RawURLEncoding (exactly 43 chars).
func newRawToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("v2: newRawToken: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// computeTokenHash returns HMAC-SHA256(secret, telegramLinkDomain+rawToken) as 64 lowercase hex.
func computeTokenHash(secret []byte, rawToken string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(telegramLinkDomain))
	mac.Write([]byte(rawToken))
	return hex.EncodeToString(mac.Sum(nil))
}

// isValidRawToken returns true iff s is exactly 43 chars of valid base64url (no padding).
func isValidRawToken(s string) bool {
	if len(s) != 43 {
		return false
	}
	for _, c := range s {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// isValidTokenHash returns true iff s is exactly 64 lowercase hex chars.
func isValidTokenHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// ── DestinationCipher ─────────────────────────────────────────────────────────

const destinationAADPrefix = "naroom:v2:destination-aad:"

// DestinationCipher encrypts/decrypts decimal chat_id strings using AES-256-GCM.
// AAD: "naroom:v2:destination-aad:" + keyVersion + ":" + bindingRef
// This binds the ciphertext to a specific binding so it cannot be replayed elsewhere.
type DestinationCipher struct {
	key        []byte
	keyVersion string
}

// NewDestinationCipher creates a DestinationCipher.
// key must be exactly 32 bytes; keyVersion must not be empty.
func NewDestinationCipher(key []byte, keyVersion string) (*DestinationCipher, error) {
	if len(key) != 32 {
		return nil, errors.New("v2: NewDestinationCipher: key must be exactly 32 bytes for AES-256")
	}
	if keyVersion == "" {
		return nil, errors.New("v2: NewDestinationCipher: keyVersion must not be empty")
	}
	k := make([]byte, 32)
	copy(k, key)
	return &DestinationCipher{key: k, keyVersion: keyVersion}, nil
}

func (c *DestinationCipher) aad(keyVersion, bindingRef string) []byte {
	return []byte(destinationAADPrefix + keyVersion + ":" + bindingRef)
}

// EncryptChatID encrypts the decimal string representation of chatID.
// Returns hex-encoded ciphertext and nonce.
func (c *DestinationCipher) EncryptChatID(chatID int64, bindingRef string) (ciphertextHex, nonceHex string, err error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", "", fmt.Errorf("v2: EncryptChatID: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", fmt.Errorf("v2: EncryptChatID gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", "", fmt.Errorf("v2: EncryptChatID nonce: [internal]")
	}
	aad := c.aad(c.keyVersion, bindingRef)
	plaintext := []byte(strconv.FormatInt(chatID, 10))
	ct := gcm.Seal(nil, nonce, plaintext, aad)
	return hex.EncodeToString(ct), hex.EncodeToString(nonce), nil
}

// DecryptChatID decrypts and returns chatID as int64.
func (c *DestinationCipher) DecryptChatID(ciphertextHex, nonceHex, keyVersion, bindingRef string) (int64, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return 0, fmt.Errorf("v2: DecryptChatID: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return 0, fmt.Errorf("v2: DecryptChatID gcm: %w", err)
	}
	ct, err := hex.DecodeString(ciphertextHex)
	if err != nil {
		return 0, errors.New("v2: DecryptChatID: malformed ciphertext")
	}
	nonce, err := hex.DecodeString(nonceHex)
	if err != nil {
		return 0, errors.New("v2: DecryptChatID: malformed nonce")
	}
	aad := c.aad(keyVersion, bindingRef)
	pt, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return 0, errors.New("v2: DecryptChatID: authentication failed")
	}
	id, err := strconv.ParseInt(string(pt), 10, 64)
	if err != nil {
		return 0, errors.New("v2: DecryptChatID: invalid plaintext")
	}
	return id, nil
}

// ── BotAPISender ──────────────────────────────────────────────────────────────

// ErrPermanentDelivery wraps errors where the delivery cannot succeed (e.g. bot blocked, chat not found).
var ErrPermanentDelivery = errors.New("v2: permanent delivery failure")

// BotAPISender delivers a Telegram message. Implementations must classify errors.
// Returned errors should be either permanent (ErrPermanentDelivery) or retryable.
type BotAPISender interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
}

// HTTPBotAPISender sends messages via the Telegram Bot API over HTTPS.
// baseURL is injectable for tests (production: "https://api.telegram.org").
// client must be provided; DefaultClient is never used.
type HTTPBotAPISender struct {
	baseURL  string
	botToken string
	client   *http.Client
}

// NewHTTPBotAPISender creates an HTTPBotAPISender.
// baseURL must not be empty and must use HTTPS; botToken must not be empty; client must not be nil.
func NewHTTPBotAPISender(baseURL, botToken string, client *http.Client) (*HTTPBotAPISender, error) {
	if baseURL == "" {
		return nil, errors.New("v2: NewHTTPBotAPISender: baseURL must not be empty")
	}
	if botToken == "" {
		return nil, errors.New("v2: NewHTTPBotAPISender: botToken must not be empty")
	}
	if client == nil {
		return nil, errors.New("v2: NewHTTPBotAPISender: client must not be nil")
	}
	parsedURL, err := url.Parse(baseURL)
	validPath := parsedURL != nil && (parsedURL.Path == "" || parsedURL.Path == "/")
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" ||
		parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" ||
		!validPath || parsedURL.RawPath != "" || parsedURL.Opaque != "" {
		return nil, errors.New("v2: NewHTTPBotAPISender: baseURL must be a valid HTTPS origin (scheme=https, host only — no path/query/fragment/userinfo/opaque)")
	}
	return &HTTPBotAPISender{
		baseURL:  strings.TrimRight(baseURL, "/"),
		botToken: botToken,
		client:   client,
	}, nil
}

// SendMessage sends a Telegram message via the Bot API.
// POST {baseURL}/bot{botToken}/sendMessage with JSON {chat_id, text}.
// Response body limited to 64 KiB.
// 429 or 5xx or network error → retryable (not ErrPermanentDelivery).
// 4xx (not 429) → wrap as ErrPermanentDelivery.
// Telegram {ok: false} → permanent delivery error.
// Never log or return the raw response body, token, or chat_id in errors.
func (s *HTTPBotAPISender) SendMessage(ctx context.Context, chatID int64, text string) error {
	url := s.baseURL + "/bot" + s.botToken + "/sendMessage"

	body, err := json.Marshal(map[string]any{
		"chat_id": chatID,
		"text":    text,
	})
	if err != nil {
		return fmt.Errorf("v2: SendMessage: marshal: [internal]")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("v2: SendMessage: build request: [internal]")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("v2: SendMessage: context: [internal]")
		}
		return fmt.Errorf("v2: SendMessage: network: [internal]")
	}
	defer resp.Body.Close()

	// HTTP status classification takes priority over body size.
	// 429 and 5xx are always retryable regardless of body size.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		// Consume and discard body safely; ignore read errors.
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024+1)) //nolint:errcheck
		return fmt.Errorf("v2: SendMessage: retryable status %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		// 4xx (not 429) → permanent. Body not needed.
		return fmt.Errorf("%w: status %d", ErrPermanentDelivery, resp.StatusCode)
	}

	// 2xx: read body with size limit (+1 to detect oversized).
	limitedBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if readErr != nil {
		return fmt.Errorf("v2: SendMessage: read response: [internal]")
	}
	if len(limitedBody) > 64*1024 {
		// Oversized 2xx body → permanent (cannot parse valid success).
		return fmt.Errorf("%w: response too large", ErrPermanentDelivery)
	}

	// Parse Telegram response to check ok field.
	var tgResp struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(limitedBody, &tgResp); err != nil {
		// Malformed JSON → permanent.
		return fmt.Errorf("%w: malformed response", ErrPermanentDelivery)
	}
	if !tgResp.OK {
		return fmt.Errorf("%w: ok=false", ErrPermanentDelivery)
	}
	return nil
}

// ── TelegramTransport ─────────────────────────────────────────────────────────

// TelegramTransport manages the Telegram link/webhook flow for V2 notifications.
type TelegramTransport struct {
	svc                 *Service
	ls                  *ListingService
	tokenSecret         []byte   // HMAC key for token hash
	webhookSecret       []byte   // for X-Telegram-Bot-Api-Secret-Token validation
	webhookSecretDigest [32]byte // SHA-256(webhookSecret) for fixed-length constant-time comparison
	destCipher          *DestinationCipher
	botUsername         string // configured bot username (without @)
	sender              BotAPISender
	now                 func() time.Time
	bindingRefGen       bindingRefGenerator // injectable for tests
	_testAfterSendHook  func() error        // nil in production, injectable for tests
}

// botUsernameRe validates bot username length/charset: 5-32 chars, letters/digits/underscores.
// A valid Telegram bot username must also end in "bot" (case-insensitive), checked separately.
var botUsernameRe = regexp.MustCompile(`^[A-Za-z0-9_]{5,32}$`)

// NewTelegramTransport validates all dependencies and returns a TelegramTransport.
func NewTelegramTransport(
	svc *Service,
	ls *ListingService,
	tokenSecret, webhookSecret []byte,
	destCipher *DestinationCipher,
	botUsername string,
	sender BotAPISender,
	now func() time.Time,
) (*TelegramTransport, error) {
	if svc == nil {
		return nil, errors.New("v2: NewTelegramTransport: svc must not be nil")
	}
	if ls == nil {
		return nil, errors.New("v2: NewTelegramTransport: ls must not be nil")
	}
	if len(tokenSecret) == 0 {
		return nil, errors.New("v2: NewTelegramTransport: tokenSecret must not be empty")
	}
	if len(webhookSecret) == 0 {
		return nil, errors.New("v2: NewTelegramTransport: webhookSecret must not be empty")
	}
	if destCipher == nil {
		return nil, errors.New("v2: NewTelegramTransport: destCipher must not be nil")
	}
	if !botUsernameRe.MatchString(botUsername) || !strings.HasSuffix(strings.ToLower(botUsername), "bot") {
		return nil, errors.New("v2: NewTelegramTransport: botUsername invalid (5-32 alphanumeric/underscore, must end in 'bot')")
	}
	if sender == nil {
		return nil, errors.New("v2: NewTelegramTransport: sender must not be nil")
	}
	if now == nil {
		return nil, errors.New("v2: NewTelegramTransport: now must not be nil")
	}
	ts := make([]byte, len(tokenSecret))
	copy(ts, tokenSecret)
	ws := make([]byte, len(webhookSecret))
	copy(ws, webhookSecret)
	// Precompute SHA-256 digest of webhookSecret for fixed-length constant-time comparison.
	wsDigest := sha256.Sum256(ws)
	return &TelegramTransport{
		svc:                 svc,
		ls:                  ls,
		tokenSecret:         ts,
		webhookSecret:       ws,
		webhookSecretDigest: wsDigest,
		destCipher:          destCipher,
		botUsername:         botUsername,
		sender:              sender,
		now:                 now,
		bindingRefGen:       defaultBindingRefGen,
	}, nil
}

// ── CreateLinkResult ─────────────────────────────────────────────────────────

// CreateLinkResult is returned by CreateLink.
type CreateLinkResult struct {
	BotURL    string // https://t.me/<botUsername>?start=<rawToken>
	ExpiresAt time.Time
}

// CreateLink verifies rawCode+walletAddress capability, checks flow readiness,
// determines window_number, clears expired/ready prior attempt, and stores a new
// pending link attempt (HMAC only). Returns the bot deep-link URL and expiry.
// All DB mutations happen inside one atomic transaction.
//
// Errors:
//   - ErrListingCapabilityNotFound — wrong code/wallet
//   - ErrFormNotReady / ErrEntitlementExpired — flow not ready
//   - ErrAlreadyVisible — ready or active binding exists (window not yet open)
//   - ErrConflict — processing attempt with live lease exists
func (t *TelegramTransport) CreateLink(rawCode, walletAddress string) (CreateLinkResult, error) {
	// 1. Verify capability.
	fv, err := t.svc.RestorePaymentIntent(rawCode, walletAddress)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return CreateLinkResult{}, ErrListingCapabilityNotFound
		}
		return CreateLinkResult{}, fmt.Errorf("v2: CreateLink: restore: %w", err)
	}

	now := t.now()
	nowUnix := now.Unix()

	// 2. Fast-path checks (outside tx; will be re-verified inside tx).
	if fv.InvoiceStatus != InvoiceStatusConfirmed || fv.EntitlementExpiresAt == nil {
		return CreateLinkResult{}, ErrFormNotReady
	}
	if nowUnix >= fv.EntitlementExpiresAt.Unix() {
		return CreateLinkResult{}, ErrEntitlementExpired
	}

	// 3. Generate token before opening tx (failure here doesn't destroy existing attempt).
	rawToken, err := newRawToken()
	if err != nil {
		return CreateLinkResult{}, fmt.Errorf("v2: CreateLink: token: %w", err)
	}
	tokenHash := computeTokenHash(t.tokenSecret, rawToken)
	expiresAt := now.Add(telegramLinkTTL)

	// 4. All DB mutations in one atomic tx.
	tx, txErr := t.svc.db.Begin()
	if txErr != nil {
		return CreateLinkResult{}, fmt.Errorf("v2: CreateLink: begin: %w", txErr)
	}
	defer tx.Rollback() //nolint:errcheck

	// 4a. Re-read flow state, invoice, entitlement, listing state inside tx.
	var flowState string
	var listingState sql.NullString
	var listingVisibleUntil sql.NullInt64
	var activationCount sql.NullInt64
	var invoiceStatus string
	var entitlementExpiresAt sql.NullInt64
	err = tx.QueryRow(`
		SELECT f.state, i.status, i.entitlement_expires_at,
		       l.state, l.visible_until, l.activation_count
		FROM v2_client_flows f
		JOIN v2_invoices i ON i.flow_id = f.id
		LEFT JOIN v2_listings l ON l.flow_id = f.id
		WHERE f.id = ?`, fv.FlowID,
	).Scan(&flowState, &invoiceStatus, &entitlementExpiresAt,
		&listingState, &listingVisibleUntil, &activationCount)
	if errors.Is(err, sql.ErrNoRows) {
		return CreateLinkResult{}, ErrListingCapabilityNotFound
	}
	if err != nil {
		return CreateLinkResult{}, fmt.Errorf("v2: CreateLink: read flow: %w", err)
	}

	// 4b. Re-verify invoice/entitlement inside tx.
	if invoiceStatus != InvoiceStatusConfirmed || !entitlementExpiresAt.Valid {
		return CreateLinkResult{}, ErrFormNotReady
	}
	if nowUnix >= entitlementExpiresAt.Int64 {
		return CreateLinkResult{}, ErrEntitlementExpired
	}

	// 4c. Determine window_number inside tx.
	var windowNumber int64
	if !listingState.Valid {
		if flowState != StateFormReady {
			return CreateLinkResult{}, ErrFormNotReady
		}
		windowNumber = 1
	} else {
		if listingState.String == "finished" {
			return CreateLinkResult{}, ErrEntitlementExpired
		}
		effectivelyVisible := listingState.String == "visible" && listingVisibleUntil.Valid && nowUnix < listingVisibleUntil.Int64
		if effectivelyVisible {
			return CreateLinkResult{}, ErrAlreadyVisible
		}
		windowNumber = activationCount.Int64 + 1
	}

	// 4d. Check for existing ready/active binding inside tx.
	var bindingState string
	bindErr := tx.QueryRow(`
		SELECT state FROM v2_client_notification_bindings
		WHERE flow_id = ? AND ? < valid_until AND window_number = ?`,
		fv.FlowID, nowUnix, windowNumber,
	).Scan(&bindingState)
	if bindErr == nil {
		return CreateLinkResult{}, ErrAlreadyVisible
	}
	if !errors.Is(bindErr, sql.ErrNoRows) {
		return CreateLinkResult{}, fmt.Errorf("v2: CreateLink: read binding: %w", bindErr)
	}

	// 4e. Read existing attempt inside tx.
	var attemptState string
	var leaseUntil sql.NullInt64
	var attemptExpires int64
	attErr := tx.QueryRow(`
		SELECT state, lease_until, expires_at
		FROM v2_telegram_link_attempts
		WHERE flow_id = ?`, fv.FlowID,
	).Scan(&attemptState, &leaseUntil, &attemptExpires)
	if attErr != nil && !errors.Is(attErr, sql.ErrNoRows) {
		return CreateLinkResult{}, fmt.Errorf("v2: CreateLink: read attempt: %w", attErr)
	}
	if attErr == nil {
		// Attempt exists.
		if attemptState == "processing" && leaseUntil.Valid && leaseUntil.Int64 > nowUnix && attemptExpires > nowUnix {
			// Processing attempt with live lease: conflict.
			return CreateLinkResult{}, ErrConflict
		}
		// 4f. Delete expired binding (FK cascade removes destination atomically).
		if _, err = tx.Exec(`
			DELETE FROM v2_client_notification_bindings
			WHERE flow_id = ? AND valid_until <= ?`,
			fv.FlowID, nowUnix,
		); err != nil {
			return CreateLinkResult{}, fmt.Errorf("v2: CreateLink: delete expired binding: %w", err)
		}
		// 4g. Delete the expired/stale/pending attempt.
		// A processing attempt is protected ONLY while both lease AND token are alive.
		// An attempt whose token has expired (expires_at <= now) may be replaced even if lease_until > now.
		if _, err = tx.Exec(`
			DELETE FROM v2_telegram_link_attempts
			WHERE flow_id = ? AND NOT (state='processing' AND lease_until IS NOT NULL AND lease_until > ? AND expires_at > ?)`,
			fv.FlowID, nowUnix, nowUnix,
		); err != nil {
			return CreateLinkResult{}, fmt.Errorf("v2: CreateLink: delete old attempt: %w", err)
		}
	} else {
		// No existing attempt; still delete expired bindings (could be orphaned).
		if _, err = tx.Exec(`
			DELETE FROM v2_client_notification_bindings
			WHERE flow_id = ? AND valid_until <= ?`,
			fv.FlowID, nowUnix,
		); err != nil {
			return CreateLinkResult{}, fmt.Errorf("v2: CreateLink: delete expired binding: %w", err)
		}
	}

	// 4h. Insert new pending attempt.
	attemptID := newID()
	_, insErr := tx.Exec(`
		INSERT INTO v2_telegram_link_attempts
		  (id, flow_id, token_hash, window_number, state, expires_at, lease_until, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'pending', ?, NULL, ?, ?)`,
		attemptID, fv.FlowID, tokenHash, windowNumber,
		expiresAt.Unix(), nowUnix, nowUnix,
	)
	if insErr != nil {
		// UNIQUE(flow_id) means a concurrent CreateLink (or webhook CAS) won the race.
		if isSQLiteUniqueOnColumn(insErr, "v2_telegram_link_attempts.flow_id") {
			return CreateLinkResult{}, ErrConflict
		}
		return CreateLinkResult{}, fmt.Errorf("v2: CreateLink: insert attempt: [internal]")
	}

	// 5. Commit.
	if err = tx.Commit(); err != nil {
		return CreateLinkResult{}, fmt.Errorf("v2: CreateLink: commit: %w", err)
	}

	botURL := "https://t.me/" + t.botUsername + "?start=" + rawToken
	return CreateLinkResult{
		BotURL:    botURL,
		ExpiresAt: expiresAt,
	}, nil
}

// ── QueryLinkStatus ──────────────────────────────────────────────────────────

// QueryLinkStatus returns the current binding/link state for a flow.
// Returns one of: "needs_link" | "link_pending" | "ready" | "active"
func (t *TelegramTransport) QueryLinkStatus(rawCode, walletAddress string) (string, error) {
	// Verify capability.
	fv, err := t.svc.RestorePaymentIntent(rawCode, walletAddress)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", ErrListingCapabilityNotFound
		}
		return "", fmt.Errorf("v2: QueryLinkStatus: restore: %w", err)
	}

	now := t.now()
	nowUnix := now.Unix()

	// Check confirmed + live entitlement.
	if fv.InvoiceStatus != InvoiceStatusConfirmed || fv.EntitlementExpiresAt == nil {
		return "", ErrFormNotReady
	}
	if nowUnix >= fv.EntitlementExpiresAt.Unix() {
		return "", ErrFormNotReady
	}

	// Read binding state — must have matching destination with exactly equal expiry.
	// d.expires_at = b.valid_until is the invariant set at finalization time.
	// Mismatched future expiries (even if both live) do not count as ready/active.
	var bindingState string
	var validUntil int64
	bindErr := t.svc.db.QueryRow(`
		SELECT b.state, b.valid_until
		FROM v2_client_notification_bindings b
		INNER JOIN v2_telegram_destinations d ON d.binding_ref = b.binding_ref
		WHERE b.flow_id = ? AND ? < d.expires_at AND d.expires_at = b.valid_until
		ORDER BY b.valid_until DESC LIMIT 1`, fv.FlowID, nowUnix,
	).Scan(&bindingState, &validUntil)
	if bindErr != nil && !errors.Is(bindErr, sql.ErrNoRows) {
		return "", fmt.Errorf("v2: QueryLinkStatus: read binding: %w", bindErr)
	}
	if bindErr == nil && validUntil > nowUnix {
		if bindingState == "active" {
			return "active", nil
		}
		if bindingState == "ready" {
			return "ready", nil
		}
	}

	// Read attempt state.
	var attemptState string
	var attemptExpires int64
	attErr := t.svc.db.QueryRow(`
		SELECT state, expires_at FROM v2_telegram_link_attempts
		WHERE flow_id = ?`, fv.FlowID,
	).Scan(&attemptState, &attemptExpires)
	if attErr != nil && !errors.Is(attErr, sql.ErrNoRows) {
		return "", fmt.Errorf("v2: QueryLinkStatus: read attempt: %w", attErr)
	}
	if attErr == nil && attemptExpires > nowUnix {
		return "link_pending", nil
	}

	return "needs_link", nil
}

// ── Webhook ───────────────────────────────────────────────────────────────────

// webhookMessage is the minimal Telegram Update struct we parse.
type webhookMessage struct {
	Message *struct {
		Chat *struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

// HandleWebhook processes a Telegram webhook update.
func (t *TelegramTransport) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	// 1. Validate X-Telegram-Bot-Api-Secret-Token (constant-time) BEFORE parsing body.
	// SHA-256 of both operands ensures fixed-length (32-byte) comparison so hmac.Equal
	// cannot short-circuit on length differences.
	headerSecret := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	headerDigest := sha256.Sum256([]byte(headerSecret))
	if !hmac.Equal(headerDigest[:], t.webhookSecretDigest[:]) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	// 2. Read body with 64 KiB limit and parse JSON.
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var update webhookMessage
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&update); err != nil {
		// Non-JSON / malformed → 200 neutral.
		w.WriteHeader(http.StatusOK)
		return
	}
	// Check for trailing JSON or garbage after the first JSON value.
	var trailingCheck json.RawMessage
	if decErr := dec.Decode(&trailingCheck); !errors.Is(decErr, io.EOF) {
		// Trailing content present → neutral 200.
		w.WriteHeader(http.StatusOK)
		return
	}

	// 3. Check chat.type == "private".
	if update.Message == nil || update.Message.Chat == nil || update.Message.Chat.Type != "private" {
		w.WriteHeader(http.StatusOK)
		return
	}

	chatID := update.Message.Chat.ID
	// Private chat IDs must be strictly positive. Zero and negative values are invalid.
	if chatID <= 0 {
		w.WriteHeader(http.StatusOK)
		return
	}
	text := update.Message.Text

	// 4. Parse message text: must match /start <token> or /start@<botUsername> <token>.
	rawToken, ok := parseStartToken(text, t.botUsername)
	if !ok {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 5. Validate raw token format (43 chars).
	if !isValidRawToken(rawToken) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 6. Compute token hash.
	tokenHash := computeTokenHash(t.tokenSecret, rawToken)

	now := t.now()
	nowUnix := now.Unix()
	newLease := nowUnix + int64(leaseDuration.Seconds())

	// 7. CAS attempt: pending or stale-processing → processing.
	res, err := t.svc.db.Exec(`
		UPDATE v2_telegram_link_attempts
		SET state='processing', lease_until=?, updated_at=?
		WHERE token_hash=? AND ?<expires_at
		  AND (state='pending' OR (state='processing' AND ?>=lease_until))`,
		newLease, nowUnix,
		tokenHash, nowUnix, nowUnix,
	)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	affected, err := res.RowsAffected()
	if err != nil || affected == 0 {
		// 0 rows = 200 neutral (expired, already consumed, or unknown token).
		w.WriteHeader(http.StatusOK)
		return
	}

	// 8. Read flow_id + window_number from attempt.
	var flowID string
	var windowNumber int64
	err = t.svc.db.QueryRow(`
		SELECT flow_id, window_number FROM v2_telegram_link_attempts
		WHERE token_hash=?`, tokenHash,
	).Scan(&flowID, &windowNumber)
	if err != nil {
		// Reset attempt to pending.
		t.resetAttemptToPending(tokenHash, newLease)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	// 9. Generate initial binding ref and encrypt chatID (before send, before retry loop).
	bindingRef, genErr := t.bindingRefGen()
	if genErr != nil {
		t.resetAttemptToPending(tokenHash, newLease)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	chatIDCiphertext, chatIDNonce, encErr := t.destCipher.EncryptChatID(chatID, bindingRef)
	if encErr != nil {
		t.resetAttemptToPending(tokenHash, newLease)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	// 10. Send confirmation message (exactly once, before retry loop).
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	sendErr := t.sender.SendMessage(ctx, chatID, "Telegram подключён. Вернитесь в NA Room.")
	if sendErr != nil {
		t.resetAttemptToPending(tokenHash, newLease)
		if errors.Is(sendErr, ErrPermanentDelivery) {
			// Permanent — neutral to client.
			w.WriteHeader(http.StatusOK)
		} else {
			// Retryable.
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		return
	}

	// 11. Fresh timestamp after external send.
	finalizeNow := t.now()
	finalizeNowUnix := finalizeNow.Unix()

	// Test hook for injecting behavior after send (nil in production).
	if t._testAfterSendHook != nil {
		hookErr := t._testAfterSendHook()
		t._testAfterSendHook = nil
		if hookErr != nil {
			// Simulate DB unavailable after send; attempt stays processing.
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// Hook may have advanced the clock.
		finalizeNow = t.now()
		finalizeNowUnix = finalizeNow.Unix()
	}

	// 12. Retry loop for binding_ref UNIQUE collision.
	for retry := 0; retry < maxBindingRefRetries; retry++ {
		if retry > 0 {
			// New binding ref and re-encrypt chatID with new AAD.
			bindingRef, genErr = t.bindingRefGen()
			if genErr != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			chatIDCiphertext, chatIDNonce, encErr = t.destCipher.EncryptChatID(chatID, bindingRef)
			if encErr != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}

		// Refresh clock immediately after generation/encryption and before db.Begin.
		// This ensures the timestamp reflects any time elapsed during ref generation.
		finalizeNow = t.now()
		finalizeNowUnix = finalizeNow.Unix()

		tx, txErr := t.svc.db.Begin()
		if txErr != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		// Re-verify attempt inside tx using exact claim predicate:
		//   token_hash + state='processing' + lease_until = newLease (exact ownership marker).
		// If the row is missing or has a different lease, this handler is superseded.
		var txFlowID string
		var txWindowNumber int64
		var txLeaseUntil sql.NullInt64
		var txExpiresAt int64
		reErr := tx.QueryRow(`
			SELECT flow_id, window_number, lease_until, expires_at
			FROM v2_telegram_link_attempts
			WHERE token_hash=? AND state='processing' AND lease_until=?`,
			tokenHash, newLease,
		).Scan(&txFlowID, &txWindowNumber, &txLeaseUntil, &txExpiresAt)
		if errors.Is(reErr, sql.ErrNoRows) {
			// Row gone or replaced by another handler/CreateLink — this finalizer is superseded.
			// Return neutral 200: do not create binding, do not delete foreign state.
			tx.Rollback() //nolint:errcheck
			w.WriteHeader(http.StatusOK)
			return
		}
		if reErr != nil || !txLeaseUntil.Valid {
			tx.Rollback() //nolint:errcheck
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		// If the token itself has permanently expired after the send, atomically delete
		// exact claimed attempt (verified by lease ownership) and return neutral 200.
		if txExpiresAt <= finalizeNowUnix {
			w.WriteHeader(t.cleanupExpiredAttemptTx(tx, tokenHash, newLease))
			return
		}

		// Lease boundary is half-open: finalizeNow must be strictly less than newLease.
		// At exact equality (finalizeNow == newLease) the lease has expired; return 503
		// so a new claimant can reclaim via the CAS.
		if txLeaseUntil.Int64 <= finalizeNowUnix {
			tx.Rollback() //nolint:errcheck
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		// Verify flow for webhook (use finalizeNowUnix).
		entitlementUnix, flowVerifyErr := t.verifyFlowForWebhook(tx, txFlowID, txWindowNumber, finalizeNowUnix)
		if errors.Is(flowVerifyErr, errFinalExpiry) {
			// Entitlement permanently expired after send: clean up exact claimed attempt.
			w.WriteHeader(t.cleanupExpiredAttemptTx(tx, tokenHash, newLease))
			return
		}
		if flowVerifyErr != nil {
			tx.Rollback() //nolint:errcheck
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		// Compute single validUntil = min(finalizeNow+15min, entitlementExpiresAt).
		validUntilUnix := finalizeNowUnix + int64(telegramLinkTTL.Seconds())
		if entitlementUnix < validUntilUnix {
			validUntilUnix = entitlementUnix
		}

		// Attach binding+destination.
		attachErr := t.attachBindingAndDestinationTx(
			tx, txFlowID, bindingRef, txWindowNumber,
			finalizeNow, time.Unix(validUntilUnix, 0),
			chatIDCiphertext, chatIDNonce, t.destCipher.keyVersion,
		)
		if errors.Is(attachErr, errBindingRefCollision) {
			tx.Rollback() //nolint:errcheck
			continue      // retry with new ref
		}
		if attachErr != nil {
			tx.Rollback() //nolint:errcheck
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		// Success: delete exact claimed attempt (lease ownership required) and commit.
		delRes, delErr := tx.Exec(
			`DELETE FROM v2_telegram_link_attempts WHERE token_hash=? AND state='processing' AND lease_until=?`,
			tokenHash, newLease,
		)
		if delErr != nil {
			tx.Rollback() //nolint:errcheck
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		delAffected, raErr := delRes.RowsAffected()
		if raErr != nil || delAffected != 1 {
			tx.Rollback() //nolint:errcheck
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if commitErr := tx.Commit(); commitErr != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// All retries exhausted after a successful send: leave attempt in processing state so
	// an immediate retry does not re-send. The lease will expire and a new claimant can reclaim.
	w.WriteHeader(http.StatusServiceUnavailable)
}

// cleanupExpiredAttemptTx atomically deletes the exact claimed attempt inside an open
// transaction and returns the HTTP status code to write.
//
// Exact predicate: token_hash + state='processing' + lease_until=newLease ensures we
// only delete our own claim and never touch a replacement or reclaimed row.
//
// Returns 200 on confirmed cleanup (RowsAffected==1 + commit success).
// Returns 200 on RowsAffected==0 (claim already lost/replaced — do not touch foreign state).
// Returns 503 on any DB error or commit error.
func (t *TelegramTransport) cleanupExpiredAttemptTx(tx *sql.Tx, tokenHash string, newLease int64) int {
	res, delErr := tx.Exec(
		`DELETE FROM v2_telegram_link_attempts WHERE token_hash=? AND state='processing' AND lease_until=?`,
		tokenHash, newLease,
	)
	if delErr != nil {
		tx.Rollback() //nolint:errcheck
		return http.StatusServiceUnavailable
	}
	affected, raErr := res.RowsAffected()
	if raErr != nil {
		tx.Rollback() //nolint:errcheck
		return http.StatusServiceUnavailable
	}
	if affected == 0 {
		// Claim was already lost or replaced; do not mutate foreign state.
		tx.Rollback() //nolint:errcheck
		return http.StatusOK
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return http.StatusServiceUnavailable
	}
	return http.StatusOK
}

// errFinalExpiry is returned by verifyFlowForWebhook when the entitlement has permanently
// expired. The caller must delete the exact attempt and return neutral 200.
var errFinalExpiry = errors.New("v2: entitlement permanently expired")

// parseStartToken parses /start <token> or /start@<botUsername> <token> from message text.
// Returns (rawToken, true) on success.
func parseStartToken(text, botUsername string) (string, bool) {
	text = strings.TrimSpace(text)
	var rest string
	lowerText := strings.ToLower(text)
	lowerBot := strings.ToLower(botUsername)

	// Match /start<space> or /start@<bot><space>.
	if strings.HasPrefix(lowerText, "/start@"+lowerBot+" ") {
		rest = text[len("/start@"+botUsername)+1:]
	} else if strings.HasPrefix(lowerText, "/start ") {
		rest = text[len("/start "):]
	} else {
		return "", false
	}

	// Token must be the only thing after /start.
	rest = strings.TrimSpace(rest)
	if strings.ContainsFunc(rest, unicode.IsSpace) {
		return "", false
	}
	return rest, rest != ""
}

// resetAttemptToPending resets a processing attempt back to pending.
func (t *TelegramTransport) resetAttemptToPending(tokenHash string, lease int64) {
	now := t.now().Unix()
	t.svc.db.Exec( //nolint:errcheck
		`UPDATE v2_telegram_link_attempts
		SET state='pending', lease_until=NULL, updated_at=?
		WHERE token_hash=? AND state='processing' AND lease_until=?`,
		now, tokenHash, lease,
	)
}

// verifyFlowForWebhook verifies that the flow has a confirmed invoice with live entitlement
// and the correct window_number. Returns (entitlementUnix, nil) on success.
func (t *TelegramTransport) verifyFlowForWebhook(tx *sql.Tx, flowID string, windowNumber, nowUnix int64) (int64, error) {
	// Read flow + invoice state.
	var flowState string
	var invoiceStatus string
	var entitlementExpiresAt sql.NullInt64
	var listingState sql.NullString
	var listingVisibleUntil sql.NullInt64
	var activationCount sql.NullInt64

	err := tx.QueryRow(`
		SELECT f.state, i.status, i.entitlement_expires_at,
		       l.state, l.visible_until, l.activation_count
		FROM v2_client_flows f
		JOIN v2_invoices i ON i.flow_id = f.id
		LEFT JOIN v2_listings l ON l.flow_id = f.id
		WHERE f.id = ?`, flowID,
	).Scan(&flowState, &invoiceStatus, &entitlementExpiresAt,
		&listingState, &listingVisibleUntil, &activationCount)
	if err != nil {
		return 0, fmt.Errorf("v2: verifyFlowForWebhook: read: %w", err)
	}

	if invoiceStatus != InvoiceStatusConfirmed || !entitlementExpiresAt.Valid {
		return 0, errors.New("v2: verifyFlowForWebhook: not confirmed")
	}
	entUnix := entitlementExpiresAt.Int64
	if nowUnix >= entUnix {
		// Return errFinalExpiry so the caller can delete the exact attempt and return neutral 200
		// rather than leaving the attempt in a retry loop it can never escape.
		return 0, fmt.Errorf("v2: verifyFlowForWebhook: entitlement expired: %w", errFinalExpiry)
	}

	// Determine expected window number.
	var expectedWindow int64
	if !listingState.Valid {
		if flowState != StateFormReady {
			return 0, errors.New("v2: verifyFlowForWebhook: not form_ready")
		}
		expectedWindow = 1
	} else {
		if listingState.String == "finished" {
			return 0, errors.New("v2: verifyFlowForWebhook: listing finished")
		}
		effectivelyVisible := listingState.String == "visible" && listingVisibleUntil.Valid && nowUnix < listingVisibleUntil.Int64
		if effectivelyVisible {
			return 0, errors.New("v2: verifyFlowForWebhook: listing already visible")
		}
		expectedWindow = activationCount.Int64 + 1
	}

	if windowNumber != expectedWindow {
		return 0, fmt.Errorf("v2: verifyFlowForWebhook: window mismatch: got %d want %d", windowNumber, expectedWindow)
	}

	return entUnix, nil
}

// attachBindingAndDestinationTx creates a ready binding and its encrypted transport
// destination in one transaction. Called by HandleWebhook only.
//
// validUntil is the single expiry for both binding.valid_until and destination.expires_at.
// It is computed by the caller as min(finalizeNow+15min, entitlementExpiresAt).
func (t *TelegramTransport) attachBindingAndDestinationTx(
	tx *sql.Tx,
	flowID, bindingRef string,
	windowNumber int64,
	verifiedAt, validUntil time.Time,
	chatIDCiphertext, chatIDNonce, keyVersion string,
) error {
	nowUnix := verifiedAt.Unix()
	verifiedUnix := verifiedAt.Unix()
	validUntilUnix := validUntil.Unix()

	// 1. Delete existing ready or expired bindings for flow.
	if _, err := tx.Exec(`
		DELETE FROM v2_client_notification_bindings
		WHERE flow_id = ? AND (state = 'ready' OR valid_until <= ?)`,
		flowID, nowUnix,
	); err != nil {
		return fmt.Errorf("v2: attachBindingAndDestinationTx: delete old: %w", err)
	}

	// 2. Insert binding row.
	bindingID := newID()
	_, insErr := tx.Exec(`
		INSERT INTO v2_client_notification_bindings
		  (id, flow_id, binding_ref, state, window_number, verified_at, valid_until,
		   created_at, updated_at)
		VALUES (?, ?, ?, 'ready', ?, ?, ?, ?, ?)`,
		bindingID, flowID, bindingRef, windowNumber,
		verifiedUnix, validUntilUnix, nowUnix, nowUnix,
	)
	if insErr != nil {
		if isSQLiteUniqueOnColumn(insErr, "v2_client_notification_bindings.binding_ref") {
			return errBindingRefCollision
		}
		if isSQLiteUniqueOnColumn(insErr, "v2_client_notification_bindings.flow_id") {
			// An active unexpired binding still exists.
			return ErrAlreadyVisible
		}
		return fmt.Errorf("v2: attachBindingAndDestinationTx: insert binding: %w", insErr)
	}

	// 3. Insert destination row (same validUntilUnix for expires_at).
	_, destErr := tx.Exec(`
		INSERT INTO v2_telegram_destinations
		  (binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		bindingRef, chatIDCiphertext, chatIDNonce, keyVersion, nowUnix, validUntilUnix,
	)
	if destErr != nil {
		return fmt.Errorf("v2: attachBindingAndDestinationTx: insert destination: %w", destErr)
	}

	return nil
}

// defaultBindingRefGen generates a random opaque binding ref: bnd_ + 32 lowercase hex chars.
// Returns an error instead of panicking on rand failure.
func defaultBindingRefGen() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("v2: defaultBindingRefGen: %w", err)
	}
	return "bnd_" + hex.EncodeToString(b), nil
}

// NormalizeExpiredAttempts deletes expired pending/processing attempts. Idempotent.
func (t *TelegramTransport) NormalizeExpiredAttempts(now time.Time) error {
	nowUnix := now.Unix()
	_, err := t.svc.db.Exec(`
		DELETE FROM v2_telegram_link_attempts WHERE expires_at <= ?`, nowUnix)
	if err != nil {
		return fmt.Errorf("v2: NormalizeExpiredAttempts: %w", err)
	}
	return nil
}
