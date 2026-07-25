// Package v2 — ReviewService: domain logic for bidirectional one-time reviews.
//
// Privacy invariants:
//   - wallet_fingerprint, chat_id, raw tokens, profile IDs are never in logs/errors.
//   - review_ref stored in DB is opaque random; the deliverable token is server-derived.
//   - Client and Helper profiles are returned only through allowlisted safe DTOs.
package v2

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ── Domain separation prefixes ────────────────────────────────────────────────

// reviewTokenDomain is the HMAC prefix for deriving the Helper website review token
// from the stored review_ref. Domain-separated from all other HMAC uses in this package.
const reviewTokenDomain = "naroom:v2:review-token:"

// reviewCallbackDomain prefixes Telegram callback_data encoding.
// callback_data = "rv:" + hex(raw_ref_16bytes) + ":" + action ("p"|"n")
const reviewCallbackPrefix = "rv:"

// ── Sentinel errors ───────────────────────────────────────────────────────────

var (
	// ErrReviewNotFound: wrong token, wrong wallet, or no entitlement.
	ErrReviewNotFound = errors.New("v2: review entitlement not found")

	// ErrReviewExpired: now > expires_at (strictly greater).
	ErrReviewExpired = errors.New("v2: review entitlement has expired")

	// ErrReviewAlreadyConsumed: rating already set (idempotent path handled separately).
	ErrReviewAlreadyConsumed = errors.New("v2: review entitlement already consumed")

	// ErrReviewNoBinding: no active Client binding/destination at purchase create time.
	ErrReviewNoBinding = errors.New("v2: no active Client notification binding for review snapshot")

	// ErrReviewCapabilityNotFound: wrong purchase_token or wrong wallet for capability.
	ErrReviewCapabilityNotFound = errors.New("v2: helper review capability not found")
)

// ── Safe view types ───────────────────────────────────────────────────────────

// ClientReputationView is the public reputation snapshot of a Client profile.
// Never includes wallet_fingerprint, profile ID, flow ID, or any contact data.
type ClientReputationView struct {
	MemberSince   time.Time
	PositiveCount int
	NegativeCount int
}

// HelperReputationForReview is the reputation snapshot shown to the Client in the
// Telegram review notification. Never includes wallet or fingerprint data.
type HelperReputationForReview struct {
	PublicName    string
	MemberSince   time.Time
	PurchaseCount int
	PositiveCount int
	NegativeCount int
}

// HelperReviewCapabilityResult is returned by GetHelperReviewCapability.
// ReviewToken is the one-time review capability; it must not be logged.
type HelperReviewCapabilityResult struct {
	ReviewToken       string // MUST NOT be logged or returned in error paths
	ExpiresAt         time.Time
	ClientReputation  ClientReputationView
	ClientDisplayName string // listing-scoped temporary display name
}

// ── review_ref generation ─────────────────────────────────────────────────────

// newReviewRef generates a new opaque review reference.
// Format: "rev_" + 32 lowercase hex chars (16 random bytes = 128 bits entropy).
func newReviewRef() (rawRef []byte, reviewRef string, err error) {
	b := make([]byte, 16)
	if _, err = rand.Read(b); err != nil {
		return nil, "", fmt.Errorf("v2: newReviewRef: %w", err)
	}
	return b, "rev_" + hex.EncodeToString(b), nil
}

// helperReviewToken derives the Helper website review token from the stored review_ref.
// token = base64url(raw_ref_16bytes) + "." + base64url(HMAC-SHA256(hmacKey, domain+reviewRef))
// The raw token is never stored in DB; it can be reconstructed deterministically.
func helperReviewToken(hmacKey []byte, rawRef []byte, reviewRef string) string {
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write([]byte(reviewTokenDomain))
	mac.Write([]byte(reviewRef))
	tag := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(rawRef) + "." + base64.RawURLEncoding.EncodeToString(tag)
}

// parseHelperReviewToken validates and parses a Helper review token.
// Returns (rawRef, reviewRef, ok). Verifies the HMAC tag constant-time.
func parseHelperReviewToken(hmacKey []byte, token string) (rawRef []byte, reviewRef string, ok bool) {
	// Expect exactly two base64url segments separated by "."
	dot := -1
	for i, c := range token {
		if c == '.' {
			if dot != -1 {
				return nil, "", false // more than one dot
			}
			dot = i
		}
	}
	if dot < 0 || dot == 0 || dot == len(token)-1 {
		return nil, "", false
	}
	rawRefB64 := token[:dot]
	tagB64 := token[dot+1:]

	rawRefBytes, err := base64.RawURLEncoding.DecodeString(rawRefB64)
	if err != nil || len(rawRefBytes) != 16 {
		return nil, "", false
	}

	// Reconstruct review_ref from raw bytes.
	ref := "rev_" + hex.EncodeToString(rawRefBytes)

	// Recompute expected HMAC tag.
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write([]byte(reviewTokenDomain))
	mac.Write([]byte(ref))
	expectedTag := mac.Sum(nil)

	// Decode provided tag.
	providedTag, err := base64.RawURLEncoding.DecodeString(tagB64)
	if err != nil || len(providedTag) != 32 {
		return nil, "", false
	}

	// Constant-time comparison.
	if !hmac.Equal(expectedTag, providedTag) {
		return nil, "", false
	}
	return rawRefBytes, ref, true
}

// telegramCallbackData returns the callback_data string for a Telegram inline button.
// Format: "rv:" + hex(raw_ref_16bytes) + ":" + action ("p" or "n").
func telegramCallbackData(rawRef []byte, action string) string {
	return reviewCallbackPrefix + hex.EncodeToString(rawRef) + ":" + action
}

// parseTelegramCallbackData parses callback_data and returns (reviewRef, isPositive, ok).
// Expected format: "rv:" + 32 hex chars + ":" + ("p"|"n")
func parseTelegramCallbackData(data string) (reviewRef string, isPositive bool, ok bool) {
	if len(data) < len(reviewCallbackPrefix)+32+2 { // "rv:" + 32hex + ":" + "p"/"n"
		return "", false, false
	}
	if data[:len(reviewCallbackPrefix)] != reviewCallbackPrefix {
		return "", false, false
	}
	rest := data[len(reviewCallbackPrefix):]
	// rest = 32 hex + ":" + action
	if len(rest) < 34 { // 32 + 1 + 1
		return "", false, false
	}
	hexPart := rest[:32]
	if rest[32] != ':' {
		return "", false, false
	}
	action := rest[33:]
	if action != "p" && action != "n" {
		return "", false, false
	}
	// Validate hex chars
	for _, c := range hexPart {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return "", false, false
		}
	}
	rawRef, err := hex.DecodeString(hexPart)
	if err != nil || len(rawRef) != 16 {
		return "", false, false
	}
	return "rev_" + hexPart, action == "p", true
}

// ── DB helpers (package-level functions, called within transactions) ──────────

// getOrCreateClientProfileTx atomically gets or creates a Client profile for the
// given wallet_fingerprint and currency, within an existing transaction.
// Returns profileID. gen is used to generate a permanent public_name alias on INSERT.
func getOrCreateClientProfileTx(tx *sql.Tx, walletFP, currency string, now int64, gen AliasGenerator) (string, error) {
	// Fast path: lookup.
	var profileID string
	err := tx.QueryRow(`
		SELECT id FROM v2_client_profiles WHERE wallet_fingerprint = ?`, walletFP,
	).Scan(&profileID)
	if err == nil {
		return profileID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("v2: getOrCreateClientProfileTx: lookup: %w", err)
	}

	// Not found: insert with bounded retry for public_name collision.
	const maxAliasRetries = 10
	var insErr error
	for attempt := 0; attempt < maxAliasRetries; attempt++ {
		alias, aliasErr := gen.GenerateAlias()
		if aliasErr != nil {
			return "", fmt.Errorf("v2: getOrCreateClientProfileTx: generate alias: %w", aliasErr)
		}
		if valErr := validateAlias(alias); valErr != nil {
			return "", fmt.Errorf("v2: getOrCreateClientProfileTx: alias validation: [internal]")
		}
		newProfileID := newID()
		_, insErr = tx.Exec(`
			INSERT INTO v2_client_profiles
			  (id, wallet_fingerprint, currency, public_name, positive_count, negative_count, created_at, updated_at)
			VALUES (?, ?, ?, ?, 0, 0, ?, ?)`,
			newProfileID, walletFP, currency, alias, now, now,
		)
		if insErr == nil {
			return newProfileID, nil
		}
		if isSQLiteUniqueOnColumn(insErr, "v2_client_profiles.wallet_fingerprint") {
			// Race: another goroutine inserted the same fingerprint.
			readErr := tx.QueryRow(`
				SELECT id FROM v2_client_profiles WHERE wallet_fingerprint = ?`, walletFP,
			).Scan(&profileID)
			if readErr != nil {
				return "", fmt.Errorf("v2: getOrCreateClientProfileTx: re-read: %w", readErr)
			}
			return profileID, nil
		}
		if isSQLiteUniqueOnColumn(insErr, "uniq_v2_client_profiles_public_name") {
			// Alias collision — retry with a new alias.
			continue
		}
		return "", fmt.Errorf("v2: getOrCreateClientProfileTx: insert: %w", insErr)
	}
	return "", errors.New("v2: getOrCreateClientProfileTx: alias collision exhausted after 10 retries")
}

// createReviewEntitlementsTx inserts both review entitlements (client + helper) within
// an existing transaction. contactReadyAt is the unix timestamp of the transition.
// Both entitlements get expires_at = contactReadyAt + 86400.
// The caller must verify idempotency (check entitlements don't already exist) before calling.
func createReviewEntitlementsTx(
	tx *sql.Tx,
	purchaseID, helperProfileID, clientProfileID string,
	contactReadyAt, nowUnix int64,
) error {
	expiresAt := contactReadyAt + 86400

	// Client entitlement: client reviews helper.
	clientRawRef, clientRef, err := newReviewRef()
	if err != nil {
		return fmt.Errorf("v2: createReviewEntitlementsTx: client ref: %w", err)
	}
	_ = clientRawRef // stored in DB via review_ref; raw bytes not needed here
	clientEntitlementID := newID()
	_, err = tx.Exec(`
		INSERT INTO v2_review_entitlements
		  (id, purchase_id, reviewer_side, review_ref,
		   target_helper_profile_id, target_client_profile_id,
		   expires_at, created_at, updated_at)
		VALUES (?, ?, 'client', ?, ?, NULL, ?, ?, ?)`,
		clientEntitlementID, purchaseID, clientRef,
		helperProfileID, expiresAt, contactReadyAt, nowUnix,
	)
	if err != nil {
		return fmt.Errorf("v2: createReviewEntitlementsTx: client insert: [internal]")
	}

	// Helper entitlement: helper reviews client.
	_, helperRef, err := newReviewRef()
	if err != nil {
		return fmt.Errorf("v2: createReviewEntitlementsTx: helper ref: %w", err)
	}
	helperEntitlementID := newID()
	_, err = tx.Exec(`
		INSERT INTO v2_review_entitlements
		  (id, purchase_id, reviewer_side, review_ref,
		   target_helper_profile_id, target_client_profile_id,
		   expires_at, created_at, updated_at)
		VALUES (?, ?, 'helper', ?, NULL, ?, ?, ?, ?)`,
		helperEntitlementID, purchaseID, helperRef,
		clientProfileID, expiresAt, contactReadyAt, nowUnix,
	)
	if err != nil {
		return fmt.Errorf("v2: createReviewEntitlementsTx: helper insert: [internal]")
	}

	return nil
}

// ── ReviewService ─────────────────────────────────────────────────────────────

// ReviewService provides domain operations for the bidirectional review system.
type ReviewService struct {
	db      *sql.DB
	hmacKey []byte
	now     func() time.Time
}

// NewReviewService creates a ReviewService.
func NewReviewService(db *sql.DB, hmacKey []byte, now func() time.Time) (*ReviewService, error) {
	if db == nil {
		return nil, errors.New("v2: NewReviewService: db must not be nil")
	}
	if len(hmacKey) == 0 {
		return nil, errors.New("v2: NewReviewService: hmacKey must not be empty")
	}
	if now == nil {
		now = time.Now
	}
	k := make([]byte, len(hmacKey))
	copy(k, hmacKey)
	return &ReviewService{db: db, hmacKey: k, now: now}, nil
}

// GetHelperReviewCapability verifies purchase ownership (same auth as RestorePurchase)
// and returns the Helper's deterministic review token plus the Client's safe reputation.
// The purchase must be in contact_ready state and the Helper entitlement must not be expired.
// Exact retry returns the same deterministic token.
//
// Wrong purchase_token OR wrong wallet → byte-identical ErrReviewCapabilityNotFound.
func (rs *ReviewService) GetHelperReviewCapability(
	purchaseID, rawToken, normalizedAddr, currency string,
) (HelperReviewCapabilityResult, error) {
	nowUnix := rs.now().Unix()

	// Look up purchase by ID + token hash + wallet fingerprint.
	// Use same HMAC key and domain as HelperPurchaseService to match existing fingerprints.
	tokenHash := helperBrowserTokenHashReview(rs.hmacKey, rawToken)
	expectedFP := helperWalletFingerprintReview(rs.hmacKey, currency, normalizedAddr)

	var (
		pState          string
		storedFP        string
		storedTokenHash string
		helperProfileID string
		listingID       string
	)
	err := rs.db.QueryRow(`
		SELECT p.state, hp.wallet_fingerprint, p.browser_token_hash,
		       p.helper_profile_id, p.listing_id
		FROM v2_helper_purchases p
		JOIN v2_helper_profiles hp ON hp.id = p.helper_profile_id
		WHERE p.id = ?`, purchaseID,
	).Scan(&pState, &storedFP, &storedTokenHash, &helperProfileID, &listingID)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperReviewCapabilityResult{}, ErrReviewCapabilityNotFound
	}
	if err != nil {
		return HelperReviewCapabilityResult{}, fmt.Errorf("v2: GetHelperReviewCapability: lookup: %w", err)
	}

	// Constant-time checks: wrong token or wrong wallet → same error.
	tokenMatch := hmac.Equal([]byte(tokenHash), []byte(storedTokenHash))
	walletMatch := hmac.Equal([]byte(expectedFP), []byte(storedFP))
	if !tokenMatch || !walletMatch {
		return HelperReviewCapabilityResult{}, ErrReviewCapabilityNotFound
	}

	// Purchase must be contact_ready.
	if pState != HPStateContactReady {
		return HelperReviewCapabilityResult{}, ErrReviewCapabilityNotFound
	}

	// Read Helper entitlement for this purchase.
	var reviewRef string
	var expiresAt int64
	err = rs.db.QueryRow(`
		SELECT review_ref, expires_at
		FROM v2_review_entitlements
		WHERE purchase_id = ? AND reviewer_side = 'helper'`,
		purchaseID,
	).Scan(&reviewRef, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperReviewCapabilityResult{}, ErrReviewCapabilityNotFound
	}
	if err != nil {
		return HelperReviewCapabilityResult{}, fmt.Errorf("v2: GetHelperReviewCapability: read entitlement: %w", err)
	}

	// Check expiry: now > expires_at → expired.
	if nowUnix > expiresAt {
		return HelperReviewCapabilityResult{}, ErrReviewExpired
	}

	// Reconstruct raw_ref from review_ref and derive token deterministically.
	// review_ref = "rev_" + 32 hex chars (16 bytes)
	if len(reviewRef) != 36 || reviewRef[:4] != "rev_" {
		return HelperReviewCapabilityResult{}, fmt.Errorf("v2: GetHelperReviewCapability: bad review_ref: [internal]")
	}
	rawRefBytes, hexErr := hex.DecodeString(reviewRef[4:])
	if hexErr != nil || len(rawRefBytes) != 16 {
		return HelperReviewCapabilityResult{}, fmt.Errorf("v2: GetHelperReviewCapability: decode ref: [internal]")
	}
	token := helperReviewToken(rs.hmacKey, rawRefBytes, reviewRef)

	// Read Client profile for this listing's flow.
	var clientProfileID string
	var clientMemberSince int64
	var clientPositive, clientNegative int
	err = rs.db.QueryRow(`
		SELECT f.client_profile_id, cp.created_at, cp.positive_count, cp.negative_count
		FROM v2_listings l
		JOIN v2_client_flows f ON f.id = l.flow_id
		JOIN v2_client_profiles cp ON cp.id = f.client_profile_id
		WHERE l.id = ?`, listingID,
	).Scan(&clientProfileID, &clientMemberSince, &clientPositive, &clientNegative)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperReviewCapabilityResult{}, ErrReviewCapabilityNotFound
	}
	if err != nil {
		return HelperReviewCapabilityResult{}, fmt.Errorf("v2: GetHelperReviewCapability: read client profile: %w", err)
	}

	// Read listing display_name (listing-scoped temp name).
	var displayName string
	err = rs.db.QueryRow(`SELECT display_name FROM v2_listings WHERE id = ?`, listingID).Scan(&displayName)
	if err != nil {
		return HelperReviewCapabilityResult{}, fmt.Errorf("v2: GetHelperReviewCapability: read listing name: %w", err)
	}

	return HelperReviewCapabilityResult{
		ReviewToken: token,
		ExpiresAt:   time.Unix(expiresAt, 0),
		ClientReputation: ClientReputationView{
			MemberSince:   time.Unix(clientMemberSince, 0),
			PositiveCount: clientPositive,
			NegativeCount: clientNegative,
		},
		ClientDisplayName: displayName,
	}, nil
}

// SubmitHelperReview validates the review token and atomically consumes the Helper
// entitlement, incrementing the Client's aggregate exactly once.
//
// Expiry boundary: now == expires_at → allowed; now > expires_at → ErrReviewExpired.
// Exact repeat of the same rating → idempotent 200 (no second increment).
// Opposite rating after first consume → ErrReviewAlreadyConsumed.
func (rs *ReviewService) SubmitHelperReview(reviewToken, rating string) error {
	if rating != "positive" && rating != "negative" {
		return fmt.Errorf("%w: rating must be 'positive' or 'negative'", ErrInvalidInput)
	}

	rawRefBytes, reviewRef, ok := parseHelperReviewToken(rs.hmacKey, reviewToken)
	_ = rawRefBytes
	if !ok {
		return ErrReviewNotFound
	}

	nowUnix := rs.now().Unix()
	return rs.consumeEntitlement("helper", reviewRef, rating, nowUnix)
}

// ConsumeClientReview is called by the Telegram webhook handler when a Client clicks a
// thumb up/down inline button. reviewRef is parsed from callback_data.
//
// Expiry boundary: now == expires_at → allowed; now > expires_at → ErrReviewExpired.
// Exact repeat → idempotent (no second increment). Concurrent: only one wins.
func (rs *ReviewService) ConsumeClientReview(reviewRef, rating string, nowUnix int64) error {
	if rating != "positive" && rating != "negative" {
		return fmt.Errorf("%w: rating must be 'positive' or 'negative'", ErrInvalidInput)
	}
	return rs.consumeEntitlement("client", reviewRef, rating, nowUnix)
}

// consumeEntitlement is the shared CAS consume path for both client and helper sides.
func (rs *ReviewService) consumeEntitlement(side, reviewRef, rating string, nowUnix int64) error {
	tx, err := rs.db.Begin()
	if err != nil {
		return fmt.Errorf("v2: consumeEntitlement: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Read entitlement.
	var (
		entID            string
		expiresAt        int64
		storedRating     sql.NullString
		storedConsumedAt sql.NullInt64
		targetHelperID   sql.NullString
		targetClientID   sql.NullString
	)
	err = tx.QueryRow(`
		SELECT id, expires_at, rating, consumed_at,
		       target_helper_profile_id, target_client_profile_id
		FROM v2_review_entitlements
		WHERE review_ref = ? AND reviewer_side = ?`,
		reviewRef, side,
	).Scan(&entID, &expiresAt, &storedRating, &storedConsumedAt, &targetHelperID, &targetClientID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrReviewNotFound
	}
	if err != nil {
		return fmt.Errorf("v2: consumeEntitlement: read: %w", err)
	}

	// Check expiry: strictly greater means expired.
	if nowUnix > expiresAt {
		return ErrReviewExpired
	}

	// Already consumed?
	if storedConsumedAt.Valid {
		// Idempotent exact repeat.
		if storedRating.Valid && storedRating.String == rating {
			return nil // idempotent success
		}
		// Different rating → already consumed with other value.
		return ErrReviewAlreadyConsumed
	}

	// CAS consume: only if rating IS NULL (not yet consumed).
	res, err := tx.Exec(`
		UPDATE v2_review_entitlements
		SET rating = ?, consumed_at = ?, updated_at = ?
		WHERE id = ? AND rating IS NULL`,
		rating, nowUnix, nowUnix, entID,
	)
	if err != nil {
		return fmt.Errorf("v2: consumeEntitlement: update: %w", err)
	}
	n, raErr := res.RowsAffected()
	if raErr != nil {
		return fmt.Errorf("v2: consumeEntitlement: rows affected: %w", raErr)
	}
	if n == 0 {
		// CAS miss: concurrent consume already committed.
		// Re-read to determine outcome.
		var winnerRating sql.NullString
		reErr := tx.QueryRow(
			`SELECT rating FROM v2_review_entitlements WHERE id = ?`, entID,
		).Scan(&winnerRating)
		if reErr != nil {
			return fmt.Errorf("v2: consumeEntitlement: re-read: %w", reErr)
		}
		if winnerRating.Valid && winnerRating.String == rating {
			return nil // concurrent same rating: idempotent
		}
		return ErrReviewAlreadyConsumed
	}

	// Increment the target profile aggregate.
	var profileID string
	switch side {
	case "client":
		// client reviewed helper → increment helper aggregate
		if !targetHelperID.Valid {
			return fmt.Errorf("v2: consumeEntitlement: missing helper target: [internal]")
		}
		profileID = targetHelperID.String
		if rating == "positive" {
			_, err = tx.Exec(`UPDATE v2_helper_profiles SET positive_count = positive_count + 1, updated_at = ? WHERE id = ?`, nowUnix, profileID)
		} else {
			_, err = tx.Exec(`UPDATE v2_helper_profiles SET negative_count = negative_count + 1, updated_at = ? WHERE id = ?`, nowUnix, profileID)
		}
	case "helper":
		// helper reviewed client → increment client aggregate
		if !targetClientID.Valid {
			return fmt.Errorf("v2: consumeEntitlement: missing client target: [internal]")
		}
		profileID = targetClientID.String
		if rating == "positive" {
			_, err = tx.Exec(`UPDATE v2_client_profiles SET positive_count = positive_count + 1, updated_at = ? WHERE id = ?`, nowUnix, profileID)
		} else {
			_, err = tx.Exec(`UPDATE v2_client_profiles SET negative_count = negative_count + 1, updated_at = ? WHERE id = ?`, nowUnix, profileID)
		}
	}
	if err != nil {
		return fmt.Errorf("v2: consumeEntitlement: increment aggregate: %w", err)
	}

	return tx.Commit()
}

// GetClientReputationByFlowID returns the Client reputation for the Client associated
// with a given listing's flow. Used for the public board view.
func (rs *ReviewService) GetClientReputationByFlowID(flowID string) (ClientReputationView, bool, error) {
	var memberSince int64
	var positive, negative int
	err := rs.db.QueryRow(`
		SELECT cp.created_at, cp.positive_count, cp.negative_count
		FROM v2_client_flows f
		JOIN v2_client_profiles cp ON cp.id = f.client_profile_id
		WHERE f.id = ?`, flowID,
	).Scan(&memberSince, &positive, &negative)
	if errors.Is(err, sql.ErrNoRows) {
		return ClientReputationView{}, false, nil
	}
	if err != nil {
		return ClientReputationView{}, false, fmt.Errorf("v2: GetClientReputationByFlowID: %w", err)
	}
	return ClientReputationView{
		MemberSince:   time.Unix(memberSince, 0),
		PositiveCount: positive,
		NegativeCount: negative,
	}, true, nil
}

// GetHelperReputationForReview returns the safe helper reputation view for inclusion in
// the Telegram review notification.
func (rs *ReviewService) GetHelperReputationForReview(helperProfileID string) (HelperReputationForReview, error) {
	var name string
	var memberSince int64
	var purchaseCount, positiveCount, negativeCount int
	err := rs.db.QueryRow(`
		SELECT public_name, created_at, purchase_count, positive_count, negative_count
		FROM v2_helper_profiles WHERE id = ?`, helperProfileID,
	).Scan(&name, &memberSince, &purchaseCount, &positiveCount, &negativeCount)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperReputationForReview{}, ErrReviewNotFound
	}
	if err != nil {
		return HelperReputationForReview{}, fmt.Errorf("v2: GetHelperReputationForReview: %w", err)
	}
	return HelperReputationForReview{
		PublicName:    name,
		MemberSince:   time.Unix(memberSince, 0),
		PurchaseCount: purchaseCount,
		PositiveCount: positiveCount,
		NegativeCount: negativeCount,
	}, nil
}

// CleanupExpiredReviews deletes expired entitlements and snapshots.
// cleanup does NOT delete entitlements at now == expires_at (equality is allowed).
// Idempotent; a DB error is not a successful cleanup.
func (rs *ReviewService) CleanupExpiredReviews(now time.Time) error {
	nowUnix := now.Unix()

	tx, err := rs.db.Begin()
	if err != nil {
		return fmt.Errorf("v2: CleanupExpiredReviews: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Null out encrypted fields on delivery snapshots whose review window has closed.
	// state IN ('sent','permanent_failure') are already cleaned up at delivery time.
	// Snapshots in awaiting_contact_ready or pending_send whose expires_at has passed
	// (strictly) are treated as permanently failed.
	_, err = tx.Exec(`
		UPDATE v2_review_delivery_snapshots
		SET state = 'permanent_failure',
		    chat_id_ciphertext = NULL,
		    chat_id_nonce = NULL,
		    updated_at = ?
		WHERE state IN ('awaiting_contact_ready', 'pending_send')
		  AND ? > expires_at`,
		nowUnix, nowUnix,
	)
	if err != nil {
		return fmt.Errorf("v2: CleanupExpiredReviews: expire snapshots: %w", err)
	}

	// Delete expired entitlements (strictly past expires_at, not equal).
	_, err = tx.Exec(`
		DELETE FROM v2_review_entitlements
		WHERE ? > expires_at`,
		nowUnix,
	)
	if err != nil {
		return fmt.Errorf("v2: CleanupExpiredReviews: delete entitlements: %w", err)
	}

	// Delete delivery snapshots whose review window has passed and are fully resolved.
	_, err = tx.Exec(`
		DELETE FROM v2_review_delivery_snapshots
		WHERE ? > expires_at`,
		nowUnix,
	)
	if err != nil {
		return fmt.Errorf("v2: CleanupExpiredReviews: delete snapshots: %w", err)
	}

	return tx.Commit()
}

// PendingDeliverySnapshot is returned by LoadPendingDeliverySnapshots.
type PendingDeliverySnapshot struct {
	SnapshotID         string
	PurchaseID         string
	BindingRefSnapshot string
	ChatIDCiphertext   string
	ChatIDNonce        string
	KeyVersion         string
	ExpiresAt          time.Time
	ClientReviewRef    string // review_ref for the client-side entitlement
	HelperProfileID    string // for building the review notification text
}

// LoadPendingDeliverySnapshots returns snapshots in 'pending_send' state whose
// review window has not yet expired.
func (rs *ReviewService) LoadPendingDeliverySnapshots(now time.Time) ([]PendingDeliverySnapshot, error) {
	nowUnix := now.Unix()
	rows, err := rs.db.Query(`
		SELECT ds.id, ds.purchase_id, ds.binding_ref_snapshot,
		       ds.chat_id_ciphertext, ds.chat_id_nonce, ds.key_version, ds.expires_at,
		       re.review_ref, p.helper_profile_id
		FROM v2_review_delivery_snapshots ds
		JOIN v2_helper_purchases p ON p.id = ds.purchase_id
		JOIN v2_review_entitlements re
		     ON re.purchase_id = ds.purchase_id AND re.reviewer_side = 'client'
		WHERE ds.state = 'pending_send'
		  AND ds.expires_at >= ?`,
		nowUnix,
	)
	if err != nil {
		return nil, fmt.Errorf("v2: LoadPendingDeliverySnapshots: query: %w", err)
	}
	defer rows.Close()

	var result []PendingDeliverySnapshot
	for rows.Next() {
		var snap PendingDeliverySnapshot
		var exp int64
		if err = rows.Scan(
			&snap.SnapshotID, &snap.PurchaseID, &snap.BindingRefSnapshot,
			&snap.ChatIDCiphertext, &snap.ChatIDNonce, &snap.KeyVersion, &exp,
			&snap.ClientReviewRef, &snap.HelperProfileID,
		); err != nil {
			return nil, fmt.Errorf("v2: LoadPendingDeliverySnapshots: scan: %w", err)
		}
		snap.ExpiresAt = time.Unix(exp, 0)
		result = append(result, snap)
	}
	return result, rows.Err()
}

// MarkSnapshotSent marks a snapshot as sent and NULLs the encrypted destination.
// Idempotent: safe to call multiple times.
func (rs *ReviewService) MarkSnapshotSent(snapshotID string) error {
	nowUnix := rs.now().Unix()
	_, err := rs.db.Exec(`
		UPDATE v2_review_delivery_snapshots
		SET state = 'sent',
		    chat_id_ciphertext = NULL,
		    chat_id_nonce = NULL,
		    updated_at = ?
		WHERE id = ? AND state = 'pending_send'`,
		nowUnix, snapshotID,
	)
	return err
}

// MarkSnapshotPermanentFailure marks a snapshot as permanently failed and NULLs the
// encrypted destination.
func (rs *ReviewService) MarkSnapshotPermanentFailure(snapshotID string) error {
	nowUnix := rs.now().Unix()
	_, err := rs.db.Exec(`
		UPDATE v2_review_delivery_snapshots
		SET state = 'permanent_failure',
		    chat_id_ciphertext = NULL,
		    chat_id_nonce = NULL,
		    updated_at = ?
		WHERE id = ? AND state = 'pending_send'`,
		nowUnix, snapshotID,
	)
	return err
}

// ── HMAC bridge helpers ───────────────────────────────────────────────────────
// These mirror the private HMAC helpers in helper_service.go using the same domains.
// ReviewService uses the same hmacKey as HelperPurchaseService.

func helperBrowserTokenHashReview(hmacKey []byte, rawToken string) string {
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write([]byte(helperTokenDomain))
	mac.Write([]byte(rawToken))
	return hex.EncodeToString(mac.Sum(nil))
}

func helperWalletFingerprintReview(hmacKey []byte, currency, normalizedAddr string) string {
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write([]byte(helperWalletDomain))
	mac.Write([]byte(currency))
	mac.Write([]byte(":"))
	mac.Write([]byte(normalizedAddr))
	return hex.EncodeToString(mac.Sum(nil))
}

// ReviewNotificationSender delivers review notification messages via Telegram.
// Tests inject a stub that records calls without hitting the network.
type ReviewNotificationSender interface {
	// SendReviewPrompt sends a message with thumb up/down inline buttons.
	// posData and negData are the callback_data strings for the two buttons.
	SendReviewPrompt(ctx context.Context, chatID int64, text, posData, negData string) error
	// AnswerCallback acknowledges a Telegram callback query (best-effort after DB commit).
	AnswerCallback(ctx context.Context, callbackQueryID, text string) error
	// EditMessage edits the text of an existing message (best-effort after DB commit).
	EditMessage(ctx context.Context, chatID, messageID int64, text string) error
}
