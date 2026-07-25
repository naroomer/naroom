// Package v2 — HelperPurchaseService: domain logic for Helper contact purchases.
//
// Privacy invariants:
//   - Plain wallet address never stored; only HMAC fingerprint persisted.
//   - Raw browser token returned once and never stored; only its HMAC hash is in DB.
//   - Raw txid never stored; only HMAC hash of txid persisted.
//   - Contact plaintext never copied into purchase/invoice/profile rows.
//   - Logs never contain wallet, fingerprint, contact, token, txid, or inputs.
package v2

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"time"
)

// Helper domain-separation HMAC prefixes — distinct from Client prefixes.
const (
	helperWalletDomain = "naroom:v2:helper-wallet:"
	helperTokenDomain  = "naroom:v2:helper-browser-token:"
	helperTxidDomain   = "naroom:v2:helper-txid:"
)

// helperInvoiceUSDCents is the fixed $10 invoice fee — not a balance threshold.
const helperInvoiceUSDCents = 1000

// Helper purchase states.
const (
	HPStateAwaitingPayment  = "awaiting_payment"
	HPStatePaymentDetected  = "payment_detected"
	HPStatePaymentConfirmed = "payment_confirmed"
	HPStatePaidLowBalance   = "paid_low_balance"
	HPStateContactReady     = "contact_ready"
	HPStateFailed           = "failed"
	HPStateInvoiceExpired   = "invoice_expired"
	HPStateReceiptExpired   = "receipt_expired"
)

// Helper invoice statuses (same vocabulary as client invoices).
const (
	HPInvoicePending   = "pending"
	HPInvoiceDetected  = "payment_detected"
	HPInvoiceConfirmed = "confirmed"
	HPInvoiceExpired   = "expired"
)

// Helper-specific sentinel errors.
var (
	// ErrHelperNotFound: wrong token, wrong wallet, or no purchase — same response to prevent enumeration.
	ErrHelperNotFound = errors.New("v2: helper purchase not found")

	// ErrHelperCountryMismatch: country lock conflict; no contact issued.
	ErrHelperCountryMismatch = errors.New("v2: helper country mismatch: profile locked to a different country")

	// ErrHelperRetryDeadlinePassed: now > balance_retry_deadline_at.
	ErrHelperRetryDeadlinePassed = errors.New("v2: balance retry deadline has passed")

	// ErrHelperRevealDeadlinePassed: first reveal window closed.
	ErrHelperRevealDeadlinePassed = errors.New("v2: contact claim deadline has passed (24h from contact_ready_at)")

	// ErrHelperReceiptExpired: receipt window closed.
	ErrHelperReceiptExpired = errors.New("v2: contact receipt has expired")

	// ErrHelperDuplicateActivePurchase: a non-terminal purchase already exists
	// for this profile+listing and the new token is different.
	ErrHelperDuplicateActivePurchase = errors.New("v2: an active helper purchase already exists for this listing")
)

// HelperInvoiceDraft carries the $10 invoice snapshot prepared by the issuer.
type HelperInvoiceDraft struct {
	PaymentAddress string
	AmountAtomic   int64
	AmountUSDCents int64 // must be 1000
}

// HelperPurchaseView is the safe outward-facing view of a Helper purchase.
// Never includes raw wallet, browser token, raw txid, or contact plaintext.
type HelperPurchaseView struct {
	PurchaseID    string
	ProfileID     string
	ListingID     string
	PublicName    string
	CountryCode   string // snapshot at purchase creation
	State         string
	InvoiceStatus string
	Currency      string

	// Invoice snapshot.
	PaymentAddress          string
	AmountAtomic            int64
	AmountUSDCents          int64
	DetectionDeadlineAt     time.Time
	DetectedTxidHashPresent bool // true if txid_hash is set (never the hash itself)
	PaymentDetectedAt       *time.Time
	ConfirmationDeadlineAt  *time.Time
	ConfirmedAt             *time.Time

	// Balance fields.
	BalanceRetryDeadlineAt *time.Time
	LastBalanceUSD         *float64
	LastBalanceCheckedAt   *time.Time

	// Reveal fields.
	ContactReadyAt   *time.Time
	FirstRevealedAt  *time.Time
	ReceiptExpiresAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// HelperRevealResult carries the decrypted contact, returned only after successful reveal.
type HelperRevealResult struct {
	PurchaseID       string
	ContactType      string
	Contact          string // plaintext — never logged or stored
	ReceiptExpiresAt time.Time
}

// HelperWatchableView carries the minimum data for the watcher to process a purchase.
type HelperWatchableView struct {
	PurchaseID              string
	InvoiceID               string
	Currency                string
	State                   string
	InvoiceStatus           string
	PaymentAddress          string
	AmountAtomic            int64
	DetectionDeadlineAt     time.Time
	ConfirmationDeadlineAt  *time.Time
	DetectedTxidHashPresent bool
	ProfileID               string
}

// HelperInvoiceIssuer prepares a $10 invoice draft for the given currency.
type HelperInvoiceIssuer interface {
	CreateHelperInvoice(ctx interface{ Done() <-chan struct{} }, currency string) (HelperInvoiceDraft, error)
}

// HelperPurchaseService implements domain operations for Helper contact purchases.
type HelperPurchaseService struct {
	db      *sql.DB
	hmacKey []byte
	cipher  ContactCipher
	names   DisplayNameGenerator
	now     func() time.Time
	policy  V2BalancePolicy
	// _testHook is called inside setHelperContactReady between the country read and
	// the CAS UPDATE. Nil in production; used only for concurrency tests.
	_testHook func()
	// _testRevealHook is called inside the first-reveal branch of RevealHelperContact
	// AFTER the initial read but BEFORE the CAS UPDATE, using the same *sql.Tx.
	// Nil in production. The hook writes winner timestamps via the same tx so that
	// the CAS UPDATE returns RowsAffected==0, enabling deterministic CAS-miss tests
	// without a second DB connection (which would deadlock SQLite).
	_testRevealHook func(tx *sql.Tx, purchaseID string) error
}

// NewHelperPurchaseService creates a HelperPurchaseService.
func NewHelperPurchaseService(
	db *sql.DB,
	hmacKey []byte,
	cipher ContactCipher,
	names DisplayNameGenerator,
	now func() time.Time,
) (*HelperPurchaseService, error) {
	if db == nil {
		return nil, errors.New("v2: NewHelperPurchaseService: db must not be nil")
	}
	if len(hmacKey) == 0 {
		return nil, errors.New("v2: NewHelperPurchaseService: hmacKey must not be empty")
	}
	if cipher == nil {
		return nil, errors.New("v2: NewHelperPurchaseService: cipher must not be nil")
	}
	if names == nil {
		return nil, errors.New("v2: NewHelperPurchaseService: names must not be nil")
	}
	if now == nil {
		now = time.Now
	}
	return &HelperPurchaseService{
		db:      db,
		hmacKey: hmacKey,
		cipher:  cipher,
		names:   names,
		now:     now,
		policy:  DefaultV2BalancePolicy(),
	}, nil
}

// SetPolicy replaces the balance policy on this HelperPurchaseService.
func (hs *HelperPurchaseService) SetPolicy(p V2BalancePolicy) { hs.policy = p }

// ── HMAC helpers ──────────────────────────────────────────────────────────────

func (hs *HelperPurchaseService) helperWalletFingerprint(currency, normalizedAddr string) string {
	mac := hmac.New(sha256.New, hs.hmacKey)
	mac.Write([]byte(helperWalletDomain))
	mac.Write([]byte(currency))
	mac.Write([]byte(":"))
	mac.Write([]byte(normalizedAddr))
	return hex.EncodeToString(mac.Sum(nil))
}

func (hs *HelperPurchaseService) helperBrowserTokenHash(rawToken string) string {
	mac := hmac.New(sha256.New, hs.hmacKey)
	mac.Write([]byte(helperTokenDomain))
	mac.Write([]byte(rawToken))
	return hex.EncodeToString(mac.Sum(nil))
}

// HelperTxidHash returns HMAC-SHA256(key, "naroom:v2:helper-txid:"+txid).
// Public so the watcher can compute it.
func (hs *HelperPurchaseService) HelperTxidHash(txid string) string {
	mac := hmac.New(sha256.New, hs.hmacKey)
	mac.Write([]byte(helperTxidDomain))
	mac.Write([]byte(txid))
	return hex.EncodeToString(mac.Sum(nil))
}

// validatePurchaseToken returns true iff s is exactly 64 lowercase hex chars.
func validatePurchaseToken(s string) bool {
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

// ── GetOrCreateProfile ────────────────────────────────────────────────────────

// maxHelperNameRetries caps bounded retry for public_name UNIQUE collision.
const maxHelperNameRetries = 10

// GetOrCreateProfile returns an existing profile ID for the wallet fingerprint,
// or creates a new one. Returns (profileID, countryCode, publicName, err).
// countryCode is nil when the profile has no country lock yet.
func (hs *HelperPurchaseService) GetOrCreateProfile(
	currency, normalizedAddr string,
) (profileID string, countryCode *string, publicName string, err error) {
	fp := hs.helperWalletFingerprint(currency, normalizedAddr)

	// Attempt lookup first (fast path).
	var cc sql.NullString
	lookupErr := hs.db.QueryRow(`
		SELECT id, country_code, public_name FROM v2_helper_profiles
		WHERE wallet_fingerprint = ?`, fp,
	).Scan(&profileID, &cc, &publicName)
	if lookupErr == nil {
		if cc.Valid {
			countryCode = &cc.String
		}
		return profileID, countryCode, publicName, nil
	}
	if !errors.Is(lookupErr, sql.ErrNoRows) {
		return "", nil, "", fmt.Errorf("v2: GetOrCreateProfile: lookup: %w", lookupErr)
	}

	// Not found — create with bounded retry for public_name collision.
	now := hs.now().Unix()
	newProfileID := newID()

	for attempt := 0; attempt < maxHelperNameRetries; attempt++ {
		name, genErr := hs.names.Generate()
		if genErr != nil {
			return "", nil, "", fmt.Errorf("v2: GetOrCreateProfile: generate name: %w", genErr)
		}

		_, insErr := hs.db.Exec(`
			INSERT INTO v2_helper_profiles
			  (id, wallet_fingerprint, currency, public_name,
			   country_code, purchase_count, positive_count, negative_count,
			   created_at, updated_at)
			VALUES (?, ?, ?, ?, NULL, 0, 0, 0, ?, ?)`,
			newProfileID, fp, currency, name, now, now,
		)
		if insErr == nil {
			return newProfileID, nil, name, nil
		}

		// Race: another goroutine inserted the same wallet_fingerprint.
		if isSQLiteUniqueOnColumn(insErr, "v2_helper_profiles.wallet_fingerprint") {
			// Re-read the winner's row.
			var cc2 sql.NullString
			readErr := hs.db.QueryRow(`
				SELECT id, country_code, public_name FROM v2_helper_profiles
				WHERE wallet_fingerprint = ?`, fp,
			).Scan(&profileID, &cc2, &publicName)
			if readErr != nil {
				return "", nil, "", fmt.Errorf("v2: GetOrCreateProfile: re-read after race: %w", readErr)
			}
			if cc2.Valid {
				countryCode = &cc2.String
			}
			return profileID, countryCode, publicName, nil
		}

		// public_name collision — retry with a new name.
		if isSQLiteUniqueOnColumn(insErr, "v2_helper_profiles.public_name") {
			continue
		}

		return "", nil, "", fmt.Errorf("v2: GetOrCreateProfile: insert: %w", insErr)
	}
	return "", nil, "", fmt.Errorf("v2: GetOrCreateProfile: exceeded %d name retries", maxHelperNameRetries)
}

// getOrCreateProfileTx does the same as GetOrCreateProfile but inside a transaction.
func (hs *HelperPurchaseService) getOrCreateProfileTx(tx *sql.Tx, currency, normalizedAddr string) (profileID, publicName string, err error) {
	fp := hs.helperWalletFingerprint(currency, normalizedAddr)

	// Attempt lookup first (fast path inside tx).
	var cc sql.NullString
	lookupErr := tx.QueryRow(`
		SELECT id, country_code, public_name FROM v2_helper_profiles
		WHERE wallet_fingerprint = ?`, fp,
	).Scan(&profileID, &cc, &publicName)
	if lookupErr == nil {
		return profileID, publicName, nil
	}
	if !errors.Is(lookupErr, sql.ErrNoRows) {
		return "", "", fmt.Errorf("v2: getOrCreateProfileTx: lookup: %w", lookupErr)
	}

	// Not found — create with bounded retry for public_name collision.
	now := hs.now().Unix()
	newProfileID := newID()

	for attempt := 0; attempt < maxHelperNameRetries; attempt++ {
		name, genErr := hs.names.Generate()
		if genErr != nil {
			return "", "", fmt.Errorf("v2: getOrCreateProfileTx: generate name: %w", genErr)
		}

		_, insErr := tx.Exec(`
			INSERT INTO v2_helper_profiles
			  (id, wallet_fingerprint, currency, public_name,
			   country_code, purchase_count, positive_count, negative_count,
			   created_at, updated_at)
			VALUES (?, ?, ?, ?, NULL, 0, 0, 0, ?, ?)`,
			newProfileID, fp, currency, name, now, now,
		)
		if insErr == nil {
			return newProfileID, name, nil
		}

		// Race: another goroutine inserted the same wallet_fingerprint.
		if isSQLiteUniqueOnColumn(insErr, "v2_helper_profiles.wallet_fingerprint") {
			// Re-read the winner's row.
			var cc2 sql.NullString
			readErr := tx.QueryRow(`
				SELECT id, country_code, public_name FROM v2_helper_profiles
				WHERE wallet_fingerprint = ?`, fp,
			).Scan(&profileID, &cc2, &publicName)
			if readErr != nil {
				return "", "", fmt.Errorf("v2: getOrCreateProfileTx: re-read after race: %w", readErr)
			}
			return profileID, publicName, nil
		}

		// public_name collision — retry with a new name.
		if isSQLiteUniqueOnColumn(insErr, "v2_helper_profiles.public_name") {
			continue
		}

		return "", "", fmt.Errorf("v2: getOrCreateProfileTx: insert: %w", insErr)
	}
	return "", "", fmt.Errorf("v2: getOrCreateProfileTx: exceeded %d name retries", maxHelperNameRetries)
}

// ── GetListingForPurchase ──────────────────────────────────────────────────────

// GetListingForPurchase verifies that listingID is effectively visible at now.
// Returns the listing's country_code for pre-check.
// Effective visibility: state='visible' AND visible_until > now AND entitlement_expires_at > now.
func (hs *HelperPurchaseService) GetListingForPurchase(listingID string, now time.Time) (countryCode string, err error) {
	nowUnix := now.Unix()
	var state string
	var visibleUntil sql.NullInt64
	var entitlementExpiresAt int64
	err = hs.db.QueryRow(`
		SELECT state, visible_until, entitlement_expires_at, country_code
		FROM v2_listings WHERE id = ?`, listingID,
	).Scan(&state, &visibleUntil, &entitlementExpiresAt, &countryCode)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("v2: GetListingForPurchase: scan: %w", err)
	}
	if state != "visible" || !visibleUntil.Valid || visibleUntil.Int64 <= nowUnix || entitlementExpiresAt <= nowUnix {
		return "", ErrNotFound
	}
	return countryCode, nil
}

// ── LookupPurchaseByToken ─────────────────────────────────────────────────────

// LookupPurchaseByToken checks whether a browser token already maps to an
// existing purchase WITHOUT touching balance or issuer.
//
//   - Returns (view, true, nil)  if the token maps to a purchase AND the
//     wallet fingerprint and listing_id both match. Caller may return 200
//     immediately without any external calls.
//   - Returns (zero, false, ErrHelperNotFound)  if the token exists but the
//     wallet or listing_id does NOT match. Caller should return 404.
//   - Returns (zero, false, nil)  if the token is unknown. Caller should
//     proceed with a new create (balance + issuer + CreatePurchase).
func (hs *HelperPurchaseService) LookupPurchaseByToken(
	rawToken, listingID, currency, normalizedAddr string,
) (HelperPurchaseView, bool, error) {
	tokenHash := hs.helperBrowserTokenHash(rawToken)
	expectedFP := hs.helperWalletFingerprint(currency, normalizedAddr)

	var existingID, storedFP, storedListingID string
	err := hs.db.QueryRow(`
		SELECT p.id, hp.wallet_fingerprint, p.listing_id
		FROM v2_helper_purchases p
		JOIN v2_helper_profiles hp ON hp.id = p.helper_profile_id
		WHERE p.browser_token_hash = ?`, tokenHash,
	).Scan(&existingID, &storedFP, &storedListingID)

	if errors.Is(err, sql.ErrNoRows) {
		return HelperPurchaseView{}, false, nil // unknown token
	}
	if err != nil {
		return HelperPurchaseView{}, false, fmt.Errorf("v2: LookupPurchaseByToken: %w", err)
	}

	// Token found: verify wallet fingerprint (constant-time) and listing.
	if !hmac.Equal([]byte(expectedFP), []byte(storedFP)) {
		return HelperPurchaseView{}, false, ErrHelperNotFound
	}
	if storedListingID != listingID {
		return HelperPurchaseView{}, false, ErrHelperNotFound
	}

	v, err := hs.readPurchaseView(existingID)
	return v, true, err
}

// ── CreatePurchase ─────────────────────────────────────────────────────────────

// CreatePurchase atomically re-verifies listing visibility and country, then
// inserts purchase + invoice. Accepts a browser-supplied rawToken (64 lowercase hex).
// Returns (isNew, view, err). isNew=false means idempotent return for same token.
func (hs *HelperPurchaseService) CreatePurchase(
	rawToken string, // browser-supplied 64 lowercase hex
	listingID, currency, normalizedAddr string,
	draft HelperInvoiceDraft,
) (isNew bool, view HelperPurchaseView, err error) {
	if draft.AmountUSDCents != helperInvoiceUSDCents {
		return false, HelperPurchaseView{}, fmt.Errorf("v2: CreatePurchase: amount_usd_cents must be %d, got %d", helperInvoiceUSDCents, draft.AmountUSDCents)
	}
	if draft.PaymentAddress == "" {
		return false, HelperPurchaseView{}, fmt.Errorf("v2: CreatePurchase: payment_address must not be empty")
	}
	if draft.AmountAtomic <= 0 {
		return false, HelperPurchaseView{}, fmt.Errorf("v2: CreatePurchase: amount_atomic must be > 0")
	}

	tokenHash := hs.helperBrowserTokenHash(rawToken)
	expectedFP := hs.helperWalletFingerprint(currency, normalizedAddr)

	// Idempotency fast path (before tx).
	var existingPurchaseID, storedFP, storedListingID string
	idErr := hs.db.QueryRow(`
		SELECT p.id, hp.wallet_fingerprint, p.listing_id
		FROM v2_helper_purchases p
		JOIN v2_helper_profiles hp ON hp.id = p.helper_profile_id
		WHERE p.browser_token_hash = ?`, tokenHash,
	).Scan(&existingPurchaseID, &storedFP, &storedListingID)
	if idErr == nil {
		// Token exists: verify ownership.
		if !hmac.Equal([]byte(expectedFP), []byte(storedFP)) {
			return false, HelperPurchaseView{}, ErrHelperNotFound
		}
		if storedListingID != listingID {
			return false, HelperPurchaseView{}, ErrHelperNotFound
		}
		// Idempotent return.
		v, err := hs.readPurchaseView(existingPurchaseID)
		return false, v, err
	}
	if !errors.Is(idErr, sql.ErrNoRows) {
		return false, HelperPurchaseView{}, fmt.Errorf("v2: CreatePurchase: idempotency check: %w", idErr)
	}

	purchaseID := newID()
	invoiceID := newID()
	now := hs.now().Unix()
	detectionDeadlineAt := now + int64(detectionWindow.Seconds())

	tx, err := hs.db.Begin()
	if err != nil {
		return false, HelperPurchaseView{}, fmt.Errorf("v2: CreatePurchase: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Re-verify listing is effectively visible inside transaction.
	var state string
	var visibleUntil sql.NullInt64
	var entitlementExpiresAt int64
	var countryCode string
	err = tx.QueryRow(`
		SELECT state, visible_until, entitlement_expires_at, country_code
		FROM v2_listings WHERE id = ?`, listingID,
	).Scan(&state, &visibleUntil, &entitlementExpiresAt, &countryCode)
	if errors.Is(err, sql.ErrNoRows) {
		return false, HelperPurchaseView{}, ErrNotFound
	}
	if err != nil {
		return false, HelperPurchaseView{}, fmt.Errorf("v2: CreatePurchase: read listing: %w", err)
	}
	if state != "visible" || !visibleUntil.Valid || visibleUntil.Int64 <= now || entitlementExpiresAt <= now {
		return false, HelperPurchaseView{}, ErrNotFound
	}

	// Get-or-create profile inside the tx.
	profileID, _, err := hs.getOrCreateProfileTx(tx, currency, normalizedAddr)
	if err != nil {
		return false, HelperPurchaseView{}, fmt.Errorf("v2: CreatePurchase: get-or-create profile: %w", err)
	}

	// Re-check country lock with profile from tx.
	var profileCountry sql.NullString
	err = tx.QueryRow(`
		SELECT country_code FROM v2_helper_profiles WHERE id = ?`, profileID,
	).Scan(&profileCountry)
	if errors.Is(err, sql.ErrNoRows) {
		return false, HelperPurchaseView{}, ErrHelperNotFound
	}
	if err != nil {
		return false, HelperPurchaseView{}, fmt.Errorf("v2: CreatePurchase: read profile country: %w", err)
	}
	if profileCountry.Valid && profileCountry.String != countryCode {
		return false, HelperPurchaseView{}, ErrHelperCountryMismatch
	}

	// Active purchase guard: no non-terminal purchase for this profile+listing.
	var activeCount int
	err = tx.QueryRow(`
		SELECT COUNT(*) FROM v2_helper_purchases
		WHERE helper_profile_id = ? AND listing_id = ?
		  AND state NOT IN ('invoice_expired', 'failed', 'receipt_expired')`,
		profileID, listingID,
	).Scan(&activeCount)
	if err != nil {
		return false, HelperPurchaseView{}, fmt.Errorf("v2: CreatePurchase: active purchase check: %w", err)
	}
	if activeCount > 0 {
		return false, HelperPurchaseView{}, ErrHelperDuplicateActivePurchase
	}

	// Insert purchase with the supplied tokenHash.
	_, err = tx.Exec(`
		INSERT INTO v2_helper_purchases
		  (id, listing_id, helper_profile_id, browser_token_hash, state,
		   country_code_snapshot, required_post_payment_floor_usd, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		purchaseID, listingID, profileID, tokenHash, HPStateAwaitingPayment,
		countryCode, hs.policy.HelperPostPaymentMinUSD, now, now,
	)
	if err != nil {
		// Partial UNIQUE index: concurrent different-token insert for same profile+listing.
		if isSQLiteUniqueViolation(err) {
			return false, HelperPurchaseView{}, ErrHelperDuplicateActivePurchase
		}
		return false, HelperPurchaseView{}, fmt.Errorf("v2: CreatePurchase: insert purchase: %w", err)
	}

	// Insert invoice.
	_, err = tx.Exec(`
		INSERT INTO v2_helper_invoices
		  (id, purchase_id, status, payment_address, amount_usd_cents, amount_atomic,
		   detection_deadline_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		invoiceID, purchaseID, HPInvoicePending,
		draft.PaymentAddress, draft.AmountUSDCents, draft.AmountAtomic,
		detectionDeadlineAt, now, now,
	)
	if err != nil {
		return false, HelperPurchaseView{}, fmt.Errorf("v2: CreatePurchase: insert invoice: %w", err)
	}

	// Snapshot active Client Telegram destination atomically with the purchase.
	// If no active Client binding+destination exists, CreatePurchase returns
	// ErrReviewNoBinding and the transaction rolls back (zero orphan rows).
	if snapErr := hs.snapshotClientDestinationTx(tx, listingID, purchaseID, detectionDeadlineAt, now); snapErr != nil {
		return false, HelperPurchaseView{}, fmt.Errorf("v2: CreatePurchase: snapshot destination: %w", snapErr)
	}

	if err = tx.Commit(); err != nil {
		return false, HelperPurchaseView{}, fmt.Errorf("v2: CreatePurchase: commit: %w", err)
	}

	view, err = hs.readPurchaseView(purchaseID)
	if err != nil {
		return false, HelperPurchaseView{}, err
	}
	return true, view, nil
}

// ── snapshotClientDestinationTx ───────────────────────────────────────────────

// snapshotClientDestinationTx reads the active Client binding+destination for the
// listing's flow and inserts a v2_review_delivery_snapshots row within the tx.
// If no active Client binding+destination exists, returns ErrReviewNoBinding and the
// whole CreatePurchase transaction is rolled back (no orphan rows).
// snapshotExpiresAt = detectionDeadlineAt + 86400 (phase-1 awaiting lifetime).
// At contact_ready the snapshot is promoted to pending_send with expires_at reset to 24h.
func (hs *HelperPurchaseService) snapshotClientDestinationTx(
	tx *sql.Tx,
	listingID, purchaseID string,
	detectionDeadlineAt, nowUnix int64,
) error {
	snapshotExpiresAt := detectionDeadlineAt + 86400

	// Read the listing's flow_id.
	var flowID string
	err := tx.QueryRow(`SELECT flow_id FROM v2_listings WHERE id = ?`, listingID).Scan(&flowID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("v2: snapshotClientDestinationTx: read flow_id: %w", err)
	}

	// Read the active binding and its destination for the flow.
	// Require d.expires_at = b.valid_until to ensure the destination is consistent.
	var bindingRef, chatIDCiphertext, chatIDNonce, keyVersion string
	err = tx.QueryRow(`
		SELECT b.binding_ref, d.chat_id_ciphertext, d.chat_id_nonce, d.key_version
		FROM v2_client_notification_bindings b
		JOIN v2_telegram_destinations d ON d.binding_ref = b.binding_ref
		WHERE b.flow_id = ? AND b.state = 'active' AND b.valid_until > ?
		  AND d.expires_at = b.valid_until`,
		flowID, nowUnix,
	).Scan(&bindingRef, &chatIDCiphertext, &chatIDNonce, &keyVersion)
	if errors.Is(err, sql.ErrNoRows) {
		// No active, consistent binding+destination: abort purchase.
		return ErrReviewNoBinding
	}
	if err != nil {
		return fmt.Errorf("v2: snapshotClientDestinationTx: read binding: %w", err)
	}

	snapID := newID()
	_, insErr := tx.Exec(`
		INSERT INTO v2_review_delivery_snapshots
		  (id, purchase_id, binding_ref_snapshot,
		   chat_id_ciphertext, chat_id_nonce, key_version,
		   state, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'awaiting_contact_ready', ?, ?, ?)`,
		snapID, purchaseID, bindingRef,
		chatIDCiphertext, chatIDNonce, keyVersion,
		snapshotExpiresAt, nowUnix, nowUnix,
	)
	if insErr != nil {
		return fmt.Errorf("v2: snapshotClientDestinationTx: insert snapshot: [internal]")
	}
	return nil
}

// ── RestorePurchase ───────────────────────────────────────────────────────────

// RestorePurchase finds a purchase by token hash and verifies the wallet fingerprint.
// Wrong token OR wrong wallet both return ErrHelperNotFound (no enumeration).
func (hs *HelperPurchaseService) RestorePurchase(rawToken, normalizedAddr string, currency string) (HelperPurchaseView, error) {
	if rawToken == "" || normalizedAddr == "" {
		return HelperPurchaseView{}, ErrHelperNotFound
	}
	tokenHash := hs.helperBrowserTokenHash(rawToken)
	expectedFP := hs.helperWalletFingerprint(currency, normalizedAddr)

	var purchaseID, storedFP string
	err := hs.db.QueryRow(`
		SELECT p.id, hp.wallet_fingerprint
		FROM v2_helper_purchases p
		JOIN v2_helper_profiles hp ON hp.id = p.helper_profile_id
		WHERE p.browser_token_hash = ?`, tokenHash,
	).Scan(&purchaseID, &storedFP)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperPurchaseView{}, ErrHelperNotFound
	}
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: RestorePurchase: lookup: %w", err)
	}

	// Constant-time comparison prevents timing side-channel.
	if !hmac.Equal([]byte(expectedFP), []byte(storedFP)) {
		return HelperPurchaseView{}, ErrHelperNotFound
	}

	return hs.readPurchaseView(purchaseID)
}

// ── RecordHelperDetection ──────────────────────────────────────────────────────

// RecordHelperDetection transitions awaiting_payment → payment_detected.
// Stores HMAC hash of txid, not raw txid.
// Idempotent if same txid (same hash) already stored.
func (hs *HelperPurchaseService) RecordHelperDetection(
	purchaseID, txid string,
	inputAddresses []string,
	amountAtomic int64,
	at time.Time,
) (HelperPurchaseView, error) {
	if txid == "" {
		return HelperPurchaseView{}, fmt.Errorf("v2: RecordHelperDetection: txid must not be empty")
	}

	txidHash := hs.HelperTxidHash(txid)

	tx, err := hs.db.Begin()
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: RecordHelperDetection: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Read purchase + invoice + profile wallet fingerprint.
	var (
		pState             string
		invStatus          string
		invAtomic          int64
		detDeadline        int64
		storedTxidHash     sql.NullString
		profileID          string
		walletFP, currency string
	)
	err = tx.QueryRow(`
		SELECT p.state, i.status, i.amount_atomic, i.detection_deadline_at,
		       i.detected_txid_hash, p.helper_profile_id,
		       hp.wallet_fingerprint, hp.currency
		FROM v2_helper_purchases p
		JOIN v2_helper_invoices i ON i.purchase_id = p.id
		JOIN v2_helper_profiles hp ON hp.id = p.helper_profile_id
		WHERE p.id = ?`, purchaseID,
	).Scan(&pState, &invStatus, &invAtomic, &detDeadline, &storedTxidHash,
		&profileID, &walletFP, &currency)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperPurchaseView{}, ErrHelperNotFound
	}
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: RecordHelperDetection: read: %w", err)
	}

	// Idempotent: same txid hash already detected.
	if invStatus == HPInvoiceDetected && storedTxidHash.Valid && storedTxidHash.String == txidHash {
		if err = tx.Commit(); err != nil {
			return HelperPurchaseView{}, fmt.Errorf("v2: RecordHelperDetection: commit (idempotent): %w", err)
		}
		return hs.readPurchaseView(purchaseID)
	}

	// Conflict: different txid already locked.
	if invStatus == HPInvoiceDetected {
		return HelperPurchaseView{}, ErrConflict
	}
	if invStatus == HPInvoiceExpired {
		return HelperPurchaseView{}, ErrExpired
	}
	if invStatus == HPInvoiceConfirmed {
		return HelperPurchaseView{}, ErrConflict
	}

	// Detection deadline.
	if at.Unix() > detDeadline {
		return HelperPurchaseView{}, ErrExpired
	}

	// Amount check.
	if amountAtomic < invAtomic {
		return HelperPurchaseView{}, ErrInsufficientPayment
	}

	// Sender check.
	matched := false
	for _, addr := range inputAddresses {
		fp := hs.helperWalletFingerprint(currency, normalizeAddress(addr))
		if fp == walletFP {
			matched = true
			break
		}
	}
	if !matched {
		return HelperPurchaseView{}, ErrSenderMismatch
	}

	nowUnix := at.Unix()
	confirmDeadline := nowUnix + int64(confirmationWindow.Seconds())

	// CAS: update invoice pending → payment_detected.
	res, err := tx.Exec(`
		UPDATE v2_helper_invoices
		SET status = ?, detected_txid_hash = ?, payment_detected_at = ?,
		    confirmation_deadline_at = ?, updated_at = ?
		WHERE purchase_id = ? AND status = ?`,
		HPInvoiceDetected, txidHash, nowUnix, confirmDeadline, nowUnix,
		purchaseID, HPInvoicePending,
	)
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: RecordHelperDetection: update invoice: %w", err)
	}
	n, raErr := res.RowsAffected()
	if raErr != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: RecordHelperDetection: rows affected: %w", raErr)
	}
	if n == 0 {
		_ = tx.Rollback()
		return HelperPurchaseView{}, ErrConflict
	}

	// Update purchase state.
	_, err = tx.Exec(`
		UPDATE v2_helper_purchases SET state = ?, updated_at = ? WHERE id = ?`,
		HPStatePaymentDetected, nowUnix, purchaseID,
	)
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: RecordHelperDetection: update purchase: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: RecordHelperDetection: commit: %w", err)
	}
	return hs.readPurchaseView(purchaseID)
}

// ── ConfirmHelperPayment ───────────────────────────────────────────────────────

// ConfirmHelperPayment transitions payment_detected → payment_confirmed.
// Sets balance_retry_deadline_at = confirmation_deadline_at + 24h.
func (hs *HelperPurchaseService) ConfirmHelperPayment(purchaseID string, at time.Time) (HelperPurchaseView, error) {
	tx, err := hs.db.Begin()
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: ConfirmHelperPayment: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var invStatus string
	var confirmDeadline sql.NullInt64
	err = tx.QueryRow(`
		SELECT i.status, i.confirmation_deadline_at
		FROM v2_helper_purchases p
		JOIN v2_helper_invoices i ON i.purchase_id = p.id
		WHERE p.id = ?`, purchaseID,
	).Scan(&invStatus, &confirmDeadline)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperPurchaseView{}, ErrHelperNotFound
	}
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: ConfirmHelperPayment: read: %w", err)
	}

	switch invStatus {
	case HPInvoicePending:
		return HelperPurchaseView{}, ErrInvalidState
	case HPInvoiceExpired:
		return HelperPurchaseView{}, ErrExpired
	case HPInvoiceConfirmed:
		// Idempotent.
		if err = tx.Commit(); err != nil {
			return HelperPurchaseView{}, fmt.Errorf("v2: ConfirmHelperPayment: commit (idempotent): %w", err)
		}
		return hs.readPurchaseView(purchaseID)
	}

	// payment_detected — check grace deadline.
	if confirmDeadline.Valid && at.Unix() > confirmDeadline.Int64 {
		return HelperPurchaseView{}, ErrExpired
	}

	nowUnix := at.Unix()
	// balance_retry_deadline_at = confirmation_deadline_at + 24h
	var retryDeadline int64
	if confirmDeadline.Valid {
		retryDeadline = confirmDeadline.Int64 + int64(confirmationWindow.Seconds())
	} else {
		retryDeadline = nowUnix + int64(confirmationWindow.Seconds())
	}

	// Update invoice.
	invRes, err := tx.Exec(`
		UPDATE v2_helper_invoices
		SET status = ?, confirmed_at = ?, updated_at = ?
		WHERE purchase_id = ? AND status = ?`,
		HPInvoiceConfirmed, nowUnix, nowUnix,
		purchaseID, HPInvoiceDetected,
	)
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: ConfirmHelperPayment: update invoice: %w", err)
	}
	n, raErr := invRes.RowsAffected()
	if raErr != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: ConfirmHelperPayment: rows affected (invoice): %w", raErr)
	}
	if n == 0 {
		_ = tx.Rollback()
		return hs.readPurchaseView(purchaseID) // concurrent confirm — idempotent
	}

	// Update purchase state.
	_, err = tx.Exec(`
		UPDATE v2_helper_purchases
		SET state = ?, balance_retry_deadline_at = ?, updated_at = ?
		WHERE id = ? AND state = ?`,
		HPStatePaymentConfirmed, retryDeadline, nowUnix,
		purchaseID, HPStatePaymentDetected,
	)
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: ConfirmHelperPayment: update purchase: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: ConfirmHelperPayment: commit: %w", err)
	}
	return hs.readPurchaseView(purchaseID)
}

// ── ExpireHelperInvoice ────────────────────────────────────────────────────────

// ExpireHelperInvoice expires the invoice for a purchase if the appropriate deadline has passed.
// Both the invoice and the purchase are updated atomically in a single transaction.
func (hs *HelperPurchaseService) ExpireHelperInvoice(purchaseID string, now time.Time) (HelperPurchaseView, error) {
	nowUnix := now.Unix()

	tx, err := hs.db.Begin()
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: ExpireHelperInvoice: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// CAS: expire invoice if deadline passed.
	res, err := tx.Exec(`
		UPDATE v2_helper_invoices
		SET status = ?, updated_at = ?
		WHERE purchase_id = ?
		  AND (
		    (status = ? AND ? > detection_deadline_at)
		    OR
		    (status = ? AND confirmation_deadline_at IS NOT NULL AND ? > confirmation_deadline_at)
		  )`,
		HPInvoiceExpired, nowUnix,
		purchaseID,
		HPInvoicePending, nowUnix,
		HPInvoiceDetected, nowUnix,
	)
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: ExpireHelperInvoice: update invoice: %w", err)
	}
	n, raErr := res.RowsAffected()
	if raErr != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: ExpireHelperInvoice: rows affected (invoice): %w", raErr)
	}

	if n == 1 {
		// Also expire purchase atomically in same tx.
		_, pErr := tx.Exec(`
			UPDATE v2_helper_purchases
			SET state = ?, updated_at = ?
			WHERE id = ? AND state IN (?, ?)`,
			HPStateInvoiceExpired, nowUnix,
			purchaseID, HPStateAwaitingPayment, HPStatePaymentDetected,
		)
		if pErr != nil {
			return HelperPurchaseView{}, fmt.Errorf("v2: ExpireHelperInvoice: update purchase: %w", pErr)
		}
		if err = tx.Commit(); err != nil {
			return HelperPurchaseView{}, fmt.Errorf("v2: ExpireHelperInvoice: commit: %w", err)
		}
		return hs.readPurchaseView(purchaseID)
	}

	// CAS miss - check current status to classify error.
	var invStatus string
	err = tx.QueryRow(`SELECT status FROM v2_helper_invoices WHERE purchase_id = ?`, purchaseID).Scan(&invStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperPurchaseView{}, ErrHelperNotFound
	}
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: ExpireHelperInvoice: resolve: %w", err)
	}
	_ = tx.Rollback()
	switch invStatus {
	case HPInvoiceExpired:
		return hs.readPurchaseView(purchaseID) // idempotent
	case HPInvoiceConfirmed:
		return HelperPurchaseView{}, ErrConflict
	default:
		return HelperPurchaseView{}, ErrInvalidState // deadline not yet passed
	}
}

// ── RecordHelperPostPaymentBalance ────────────────────────────────────────────

// RecordHelperPostPaymentBalance records a balance result and transitions state.
// The floor is read from required_post_payment_floor_usd stored on the purchase row.
// balance >= floor → setHelperContactReady
// balance < floor  → setHelperLowBalance (no downgrade from contact_ready)
func (hs *HelperPurchaseService) RecordHelperPostPaymentBalance(
	purchaseID string,
	balanceUSD float64,
) (HelperPurchaseView, error) {
	// Read the per-purchase floor from DB (set at creation time from the policy).
	var floorUSD float64
	err := hs.db.QueryRow(
		`SELECT required_post_payment_floor_usd FROM v2_helper_purchases WHERE id = ?`, purchaseID,
	).Scan(&floorUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperPurchaseView{}, ErrHelperNotFound
	}
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: RecordHelperPostPaymentBalance: read floor: %w", err)
	}

	if err := validateBalanceInputs(balanceUSD, floorUSD); err != nil {
		return HelperPurchaseView{}, err
	}

	if balanceUSD >= floorUSD {
		return hs.setHelperContactReady(purchaseID, balanceUSD)
	}
	return hs.setHelperLowBalance(purchaseID, balanceUSD)
}

// setHelperContactReady: atomic country CAS + contact_ready + purchase_count++.
func (hs *HelperPurchaseService) setHelperContactReady(purchaseID string, balanceUSD float64) (HelperPurchaseView, error) {
	tx, err := hs.db.Begin()
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: setHelperContactReady: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Read purchase + profile country.
	var pState, countrySnapshot, profileID string
	var profileCountry sql.NullString
	err = tx.QueryRow(`
		SELECT p.state, p.country_code_snapshot, p.helper_profile_id, hp.country_code
		FROM v2_helper_purchases p
		JOIN v2_helper_profiles hp ON hp.id = p.helper_profile_id
		WHERE p.id = ?`, purchaseID,
	).Scan(&pState, &countrySnapshot, &profileID, &profileCountry)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperPurchaseView{}, ErrHelperNotFound
	}
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: setHelperContactReady: read: %w", err)
	}

	// Idempotency: if already contact_ready, entitlements already exist.
	if pState == HPStateContactReady {
		_ = tx.Commit()
		return hs.readPurchaseView(purchaseID)
	}
	if pState != HPStatePaymentConfirmed && pState != HPStatePaidLowBalance {
		return HelperPurchaseView{}, ErrInvalidState
	}

	// Test hook: fires between read and CAS for concurrency tests.
	if hs._testHook != nil {
		hs._testHook()
	}

	nowUnix := hs.now().Unix()
	contactReadyAt := nowUnix

	// Atomic country CAS:
	// COALESCE(country_code, countrySnapshot) = countrySnapshot means:
	// either country_code IS NULL (lock it) or country_code = countrySnapshot (already locked to our country).
	countryRes, err := tx.Exec(`
		UPDATE v2_helper_profiles
		SET country_code = ?,
		    purchase_count = purchase_count + 1,
		    updated_at = ?
		WHERE id = ?
		  AND (country_code IS NULL OR country_code = ?)`,
		countrySnapshot, nowUnix,
		profileID, countrySnapshot,
	)
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: setHelperContactReady: country CAS: %w", err)
	}
	cn, raErr := countryRes.RowsAffected()
	if raErr != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: setHelperContactReady: rows affected (country): %w", raErr)
	}
	if cn == 0 {
		// Country CAS lost: set purchase to failed atomically.
		_, failErr := tx.Exec(`
			UPDATE v2_helper_purchases
			SET state = ?, last_balance_usd = ?, last_balance_checked_at = ?, updated_at = ?
			WHERE id = ?`,
			HPStateFailed, balanceUSD, nowUnix, nowUnix, purchaseID,
		)
		if failErr != nil {
			return HelperPurchaseView{}, fmt.Errorf("v2: setHelperContactReady: fail on country mismatch: %w", failErr)
		}
		if cErr := tx.Commit(); cErr != nil {
			return HelperPurchaseView{}, fmt.Errorf("v2: setHelperContactReady: commit fail: %w", cErr)
		}
		return HelperPurchaseView{}, ErrHelperCountryMismatch
	}

	// Update purchase to contact_ready.
	_, err = tx.Exec(`
		UPDATE v2_helper_purchases
		SET state = ?, contact_ready_at = ?,
		    last_balance_usd = ?, last_balance_checked_at = ?,
		    updated_at = ?
		WHERE id = ?`,
		HPStateContactReady, contactReadyAt,
		balanceUSD, nowUnix, nowUnix,
		purchaseID,
	)
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: setHelperContactReady: update purchase: %w", err)
	}

	// Update invoice balance snapshot.
	_, err = tx.Exec(`
		UPDATE v2_helper_invoices
		SET updated_at = ?
		WHERE purchase_id = ?`,
		nowUnix, purchaseID,
	)
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: setHelperContactReady: update invoice ts: %w", err)
	}

	// Atomically create both review entitlements and activate delivery snapshot.
	// Read the Client profile ID from the listing's flow.
	var listingID string
	err = tx.QueryRow(`SELECT listing_id FROM v2_helper_purchases WHERE id = ?`, purchaseID).Scan(&listingID)
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: setHelperContactReady: read listing id: %w", err)
	}
	var clientProfileID sql.NullString
	err = tx.QueryRow(`
		SELECT f.client_profile_id
		FROM v2_listings l
		JOIN v2_client_flows f ON f.id = l.flow_id
		WHERE l.id = ?`, listingID,
	).Scan(&clientProfileID)
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: setHelperContactReady: read client profile: %w", err)
	}
	if !clientProfileID.Valid {
		return HelperPurchaseView{}, fmt.Errorf("v2: setHelperContactReady: client profile not set: [internal]")
	}

	if entErr := createReviewEntitlementsTx(tx, purchaseID, profileID, clientProfileID.String, contactReadyAt, nowUnix); entErr != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: setHelperContactReady: create entitlements: %w", entErr)
	}

	// Activate the delivery snapshot: awaiting_contact_ready → pending_send.
	// Phase-2 lifetime: set delivery expires_at = contactReadyAt + 86400 to match
	// the review entitlement window. CAS must touch exactly one row.
	snapRes, err := tx.Exec(`
		UPDATE v2_review_delivery_snapshots
		SET state = 'pending_send', expires_at = ?, updated_at = ?
		WHERE purchase_id = ? AND state = 'awaiting_contact_ready'`,
		contactReadyAt+86400, nowUnix, purchaseID,
	)
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: setHelperContactReady: activate snapshot: %w", err)
	}
	snapN, snapRAErr := snapRes.RowsAffected()
	if snapRAErr != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: setHelperContactReady: activate snapshot rows: %w", snapRAErr)
	}
	if snapN != 1 {
		return HelperPurchaseView{}, fmt.Errorf("v2: setHelperContactReady: snapshot CAS affected %d rows, expected 1: [internal]", snapN)
	}

	if err = tx.Commit(); err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: setHelperContactReady: commit: %w", err)
	}
	return hs.readPurchaseView(purchaseID)
}

// setHelperLowBalance: set paid_low_balance. Never downgrades from contact_ready.
func (hs *HelperPurchaseService) setHelperLowBalance(purchaseID string, balanceUSD float64) (HelperPurchaseView, error) {
	nowUnix := hs.now().Unix()

	// Read current state.
	var pState string
	err := hs.db.QueryRow(`SELECT state FROM v2_helper_purchases WHERE id = ?`, purchaseID).Scan(&pState)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperPurchaseView{}, ErrHelperNotFound
	}
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: setHelperLowBalance: read state: %w", err)
	}

	// No downgrade from contact_ready.
	if pState == HPStateContactReady {
		return hs.readPurchaseView(purchaseID)
	}
	if pState != HPStatePaymentConfirmed && pState != HPStatePaidLowBalance {
		return HelperPurchaseView{}, ErrInvalidState
	}

	res, err := hs.db.Exec(`
		UPDATE v2_helper_purchases
		SET state = ?,
		    last_balance_usd = ?, last_balance_checked_at = ?,
		    updated_at = ?
		WHERE id = ? AND state = ?`,
		HPStatePaidLowBalance,
		balanceUSD, nowUnix, nowUnix,
		purchaseID, pState,
	)
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: setHelperLowBalance: update: %w", err)
	}
	n, raErr := res.RowsAffected()
	if raErr != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: setHelperLowBalance: rows affected: %w", raErr)
	}
	if n == 0 {
		return HelperPurchaseView{}, ErrConflict
	}
	return hs.readPurchaseView(purchaseID)
}

// ── RecheckHelperBalance ───────────────────────────────────────────────────────

// RecheckHelperBalance validates the retry deadline and re-runs the balance check.
// Equality is allowed: now == deadline is still within window. now > deadline → ErrHelperRetryDeadlinePassed.
func (hs *HelperPurchaseService) RecheckHelperBalance(purchaseID string, balanceUSD float64, now time.Time) (HelperPurchaseView, error) {
	if math.IsNaN(balanceUSD) || math.IsInf(balanceUSD, 0) || balanceUSD < 0 {
		return HelperPurchaseView{}, fmt.Errorf("%w: balance must be finite non-negative", ErrInvalidInput)
	}

	var pState string
	var retryDeadline sql.NullInt64
	err := hs.db.QueryRow(`
		SELECT state, balance_retry_deadline_at FROM v2_helper_purchases WHERE id = ?`, purchaseID,
	).Scan(&pState, &retryDeadline)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperPurchaseView{}, ErrHelperNotFound
	}
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: RecheckHelperBalance: read: %w", err)
	}

	if pState != HPStatePaymentConfirmed && pState != HPStatePaidLowBalance && pState != HPStateContactReady {
		return HelperPurchaseView{}, ErrInvalidState
	}

	// Equality allowed: now > deadline (strictly greater) means expired.
	if retryDeadline.Valid && now.Unix() > retryDeadline.Int64 {
		return HelperPurchaseView{}, ErrHelperRetryDeadlinePassed
	}

	return hs.RecordHelperPostPaymentBalance(purchaseID, balanceUSD)
}

// ── RevealHelperContact ────────────────────────────────────────────────────────

// RevealHelperContact verifies capability and state, atomically sets first_revealed_at
// (if not yet set), then decrypts and returns the contact. Contact is never stored.
func (hs *HelperPurchaseService) RevealHelperContact(purchaseID, rawToken, normalizedAddr, currency string) (HelperRevealResult, error) {
	tokenHash := hs.helperBrowserTokenHash(rawToken)
	expectedFP := hs.helperWalletFingerprint(currency, normalizedAddr)

	tx, err := hs.db.Begin()
	if err != nil {
		return HelperRevealResult{}, fmt.Errorf("v2: RevealHelperContact: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Read purchase + profile + listing contact fields.
	var (
		pState, pID, profileID, listingID string
		storedFP                          string
		contactReadyAt                    sql.NullInt64
		firstRevealedAt                   sql.NullInt64
		receiptExpiresAt                  sql.NullInt64
		tokenHashDB                       string
		listingFlowID                     string
		ctHex, nonceHex, keyVer, ctType   string
	)
	err = tx.QueryRow(`
		SELECT p.id, p.state, p.helper_profile_id, p.listing_id,
		       p.browser_token_hash,
		       p.contact_ready_at, p.first_revealed_at, p.receipt_expires_at,
		       hp.wallet_fingerprint,
		       l.flow_id, l.contact_ciphertext, l.contact_nonce, l.contact_key_version, l.contact_type
		FROM v2_helper_purchases p
		JOIN v2_helper_profiles hp ON hp.id = p.helper_profile_id
		JOIN v2_listings l ON l.id = p.listing_id
		WHERE p.id = ?`, purchaseID,
	).Scan(
		&pID, &pState, &profileID, &listingID,
		&tokenHashDB,
		&contactReadyAt, &firstRevealedAt, &receiptExpiresAt,
		&storedFP,
		&listingFlowID, &ctHex, &nonceHex, &keyVer, &ctType,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperRevealResult{}, ErrHelperNotFound
	}
	if err != nil {
		return HelperRevealResult{}, fmt.Errorf("v2: RevealHelperContact: read: %w", err)
	}

	// Token check (constant-time).
	if !hmac.Equal([]byte(tokenHash), []byte(tokenHashDB)) {
		return HelperRevealResult{}, ErrHelperNotFound
	}
	// Wallet fingerprint check (constant-time).
	if !hmac.Equal([]byte(expectedFP), []byte(storedFP)) {
		return HelperRevealResult{}, ErrHelperNotFound
	}

	nowUnix := hs.now().Unix()

	// State must be contact_ready or receipt_expired (latter gives 410).
	if pState == HPStateReceiptExpired {
		return HelperRevealResult{}, ErrHelperReceiptExpired
	}
	if pState != HPStateContactReady {
		return HelperRevealResult{}, ErrInvalidState
	}

	// First reveal window: 24h from contact_ready_at.
	if firstRevealedAt.Valid {
		// Already revealed. Check receipt window.
		if receiptExpiresAt.Valid && nowUnix > receiptExpiresAt.Int64 {
			return HelperRevealResult{}, ErrHelperReceiptExpired
		}
		// Fall through to commit with existing receiptExpiresAt.
	} else {
		// First reveal. Check claim deadline: now > contact_ready_at + 24h means expired.
		if !contactReadyAt.Valid {
			return HelperRevealResult{}, ErrInvalidState
		}
		claimDeadline := contactReadyAt.Int64 + int64(confirmationWindow.Seconds()) // 24h
		if nowUnix > claimDeadline {
			return HelperRevealResult{}, ErrHelperRevealDeadlinePassed
		}

		// Test hook: fires BEFORE the CAS UPDATE using the SAME tx so that
		// the hook's write is visible to the UPDATE without a second connection.
		// This makes the CAS return RowsAffected==0 deterministically in tests.
		if hs._testRevealHook != nil {
			if hookErr := hs._testRevealHook(tx, purchaseID); hookErr != nil {
				return HelperRevealResult{}, fmt.Errorf("v2: RevealHelperContact: test hook: %w", hookErr)
			}
		}

		// Set first_revealed_at and receipt_expires_at atomically.
		receiptExp := nowUnix + int64(confirmationWindow.Seconds()) // 24h = 86400s
		res, updErr := tx.Exec(`
			UPDATE v2_helper_purchases
			SET first_revealed_at = ?, receipt_expires_at = ?, updated_at = ?
			WHERE id = ? AND first_revealed_at IS NULL`,
			nowUnix, receiptExp, nowUnix,
			purchaseID,
		)
		if updErr != nil {
			return HelperRevealResult{}, fmt.Errorf("v2: RevealHelperContact: set reveal: %w", updErr)
		}
		n, raErr := res.RowsAffected()
		if raErr != nil {
			return HelperRevealResult{}, fmt.Errorf("v2: RevealHelperContact: rows affected: %w", raErr)
		}
		if n == 0 {
			// CAS miss: a concurrent reveal (or test hook) already set first_revealed_at.
			// Re-read both fields inside this tx; they must be present.
			var winnerRevealed, winnerExp sql.NullInt64
			reReadErr := tx.QueryRow(
				`SELECT first_revealed_at, receipt_expires_at FROM v2_helper_purchases WHERE id = ?`,
				purchaseID,
			).Scan(&winnerRevealed, &winnerExp)
			if reReadErr != nil {
				return HelperRevealResult{}, fmt.Errorf("v2: RevealHelperContact: re-read: %w", reReadErr)
			}
			if !winnerExp.Valid {
				return HelperRevealResult{}, fmt.Errorf("v2: RevealHelperContact: winner expiry missing: [internal]")
			}
			receiptExpiresAt = winnerExp
		} else {
			receiptExpiresAt = sql.NullInt64{Valid: true, Int64: receiptExp}
		}
	}

	if err = tx.Commit(); err != nil {
		return HelperRevealResult{}, fmt.Errorf("v2: RevealHelperContact: commit: %w", err)
	}

	// Decrypt contact using listing's Client flow_id as AAD (never the purchase ID).
	plaintext, err := hs.cipher.Decrypt(ctHex, nonceHex, keyVer, listingID, listingFlowID, ctType)
	if err != nil {
		return HelperRevealResult{}, fmt.Errorf("v2: RevealHelperContact: decrypt: [internal]")
	}

	expTime := time.Unix(receiptExpiresAt.Int64, 0)
	return HelperRevealResult{
		PurchaseID:       purchaseID,
		ContactType:      ctType,
		Contact:          plaintext,
		ReceiptExpiresAt: expTime,
	}, nil
}

// ── NormalizeHelperExpired ─────────────────────────────────────────────────────

// NormalizeHelperExpired expires invoices and purchases whose deadlines have passed.
// Five atomic UPDATE statements in one transaction.
func (hs *HelperPurchaseService) NormalizeHelperExpired(now time.Time) error {
	nowUnix := now.Unix()

	tx, err := hs.db.Begin()
	if err != nil {
		return fmt.Errorf("v2: NormalizeHelperExpired: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// 1. Expire pending invoices past detection deadline.
	_, err = tx.Exec(`
		UPDATE v2_helper_invoices
		SET status = ?, updated_at = ?
		WHERE status = ? AND ? > detection_deadline_at`,
		HPInvoiceExpired, nowUnix, HPInvoicePending, nowUnix,
	)
	if err != nil {
		return fmt.Errorf("v2: NormalizeHelperExpired: expire pending invoices: %w", err)
	}

	// 2. Expire detected invoices past confirmation deadline.
	_, err = tx.Exec(`
		UPDATE v2_helper_invoices
		SET status = ?, updated_at = ?
		WHERE status = ? AND confirmation_deadline_at IS NOT NULL AND ? > confirmation_deadline_at`,
		HPInvoiceExpired, nowUnix, HPInvoiceDetected, nowUnix,
	)
	if err != nil {
		return fmt.Errorf("v2: NormalizeHelperExpired: expire detected invoices: %w", err)
	}

	// 3. Set invoice_expired on purchases whose invoice is now expired.
	_, err = tx.Exec(`
		UPDATE v2_helper_purchases
		SET state = ?, updated_at = ?
		WHERE state IN (?, ?)
		  AND id IN (
		    SELECT purchase_id FROM v2_helper_invoices WHERE status = ?
		  )`,
		HPStateInvoiceExpired, nowUnix,
		HPStateAwaitingPayment, HPStatePaymentDetected,
		HPInvoiceExpired,
	)
	if err != nil {
		return fmt.Errorf("v2: NormalizeHelperExpired: set invoice_expired: %w", err)
	}

	// 4. Set failed: confirmed/low_balance past retry deadline.
	_, err = tx.Exec(`
		UPDATE v2_helper_purchases
		SET state = ?, updated_at = ?
		WHERE state IN (?, ?)
		  AND balance_retry_deadline_at IS NOT NULL
		  AND ? > balance_retry_deadline_at`,
		HPStateFailed, nowUnix,
		HPStatePaymentConfirmed, HPStatePaidLowBalance,
		nowUnix,
	)
	if err != nil {
		return fmt.Errorf("v2: NormalizeHelperExpired: set failed: %w", err)
	}

	// 5. Set receipt_expired: contact_ready whose reveal window has closed.
	_, err = tx.Exec(`
		UPDATE v2_helper_purchases
		SET state = ?, updated_at = ?
		WHERE state = ?
		  AND (
		    (first_revealed_at IS NULL AND ? > contact_ready_at + 86400)
		    OR
		    (first_revealed_at IS NOT NULL AND receipt_expires_at IS NOT NULL AND ? > receipt_expires_at)
		  )`,
		HPStateReceiptExpired, nowUnix,
		HPStateContactReady,
		nowUnix,
		nowUnix,
	)
	if err != nil {
		return fmt.Errorf("v2: NormalizeHelperExpired: set receipt_expired: %w", err)
	}

	return tx.Commit()
}

// ── LoadWatchablePurchases ────────────────────────────────────────────────────

// LoadWatchablePurchases returns purchases that need watcher attention.
func (hs *HelperPurchaseService) LoadWatchablePurchases() ([]HelperWatchableView, error) {
	const q = `
		SELECT p.id, i.id, hp.currency, p.state, i.status,
		       i.payment_address, i.amount_atomic, i.detection_deadline_at,
		       i.confirmation_deadline_at, i.detected_txid_hash, p.helper_profile_id
		FROM v2_helper_purchases p
		JOIN v2_helper_invoices i ON i.purchase_id = p.id
		JOIN v2_helper_profiles hp ON hp.id = p.helper_profile_id
		WHERE i.status IN (?, ?)
		   OR (i.status = ? AND p.state = ? AND p.last_balance_usd IS NULL)`

	rows, err := hs.db.Query(q,
		HPInvoicePending, HPInvoiceDetected,
		HPInvoiceConfirmed, HPStatePaymentConfirmed,
	)
	if err != nil {
		return nil, fmt.Errorf("v2: LoadWatchablePurchases: query: %w", err)
	}
	defer rows.Close()

	var result []HelperWatchableView
	for rows.Next() {
		var v HelperWatchableView
		var confirmDeadline sql.NullInt64
		var txidHash sql.NullString
		var detDeadline int64
		err := rows.Scan(
			&v.PurchaseID, &v.InvoiceID, &v.Currency, &v.State, &v.InvoiceStatus,
			&v.PaymentAddress, &v.AmountAtomic, &detDeadline,
			&confirmDeadline, &txidHash, &v.ProfileID,
		)
		if err != nil {
			return nil, fmt.Errorf("v2: LoadWatchablePurchases: scan: %w", err)
		}
		v.DetectionDeadlineAt = time.Unix(detDeadline, 0)
		if confirmDeadline.Valid {
			t := time.Unix(confirmDeadline.Int64, 0)
			v.ConfirmationDeadlineAt = &t
		}
		v.DetectedTxidHashPresent = txidHash.Valid && txidHash.String != ""
		result = append(result, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("v2: LoadWatchablePurchases: rows: %w", err)
	}
	return result, nil
}

// ── MatchHelperSenderAddress ──────────────────────────────────────────────────

// MatchHelperSenderAddress finds the first address from candidates whose fingerprint
// matches the wallet fingerprint stored on the purchase's profile.
func (hs *HelperPurchaseService) MatchHelperSenderAddress(purchaseID string, candidates []string) (string, error) {
	var walletFP, currency string
	err := hs.db.QueryRow(`
		SELECT hp.wallet_fingerprint, hp.currency
		FROM v2_helper_purchases p
		JOIN v2_helper_profiles hp ON hp.id = p.helper_profile_id
		WHERE p.id = ?`, purchaseID,
	).Scan(&walletFP, &currency)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrHelperNotFound
	}
	if err != nil {
		return "", fmt.Errorf("v2: MatchHelperSenderAddress: read: %w", err)
	}

	for _, addr := range candidates {
		fp := hs.helperWalletFingerprint(currency, normalizeAddress(addr))
		if fp == walletFP {
			return addr, nil
		}
	}
	return "", ErrSenderMismatch
}

// ── readPurchaseView ──────────────────────────────────────────────────────────

func (hs *HelperPurchaseService) readPurchaseView(purchaseID string) (HelperPurchaseView, error) {
	const q = `
		SELECT p.id, p.helper_profile_id, p.listing_id, p.state,
		       p.country_code_snapshot,
		       p.contact_ready_at, p.first_revealed_at, p.receipt_expires_at,
		       p.balance_retry_deadline_at,
		       p.last_balance_usd, p.last_balance_checked_at,
		       p.created_at, p.updated_at,
		       hp.public_name, hp.currency,
		       i.id, i.status,
		       i.payment_address, i.amount_usd_cents, i.amount_atomic,
		       i.detection_deadline_at,
		       i.detected_txid_hash,
		       i.payment_detected_at, i.confirmation_deadline_at, i.confirmed_at
		FROM v2_helper_purchases p
		JOIN v2_helper_profiles hp ON hp.id = p.helper_profile_id
		JOIN v2_helper_invoices i ON i.purchase_id = p.id
		WHERE p.id = ?`

	var (
		v                    HelperPurchaseView
		createdAt, updatedAt int64
		detDeadline          int64
		contactReadyAt       sql.NullInt64
		firstRevealedAt      sql.NullInt64
		receiptExpiresAt     sql.NullInt64
		retryDeadline        sql.NullInt64
		lastBalUSD           sql.NullFloat64
		lastBalAt            sql.NullInt64
		txidHash             sql.NullString
		payDetectedAt        sql.NullInt64
		confirmDeadline      sql.NullInt64
		confirmedAt          sql.NullInt64
		invoiceID            string
	)
	err := hs.db.QueryRow(q, purchaseID).Scan(
		&v.PurchaseID, &v.ProfileID, &v.ListingID, &v.State,
		&v.CountryCode,
		&contactReadyAt, &firstRevealedAt, &receiptExpiresAt,
		&retryDeadline,
		&lastBalUSD, &lastBalAt,
		&createdAt, &updatedAt,
		&v.PublicName, &v.Currency,
		&invoiceID, &v.InvoiceStatus,
		&v.PaymentAddress, &v.AmountUSDCents, &v.AmountAtomic,
		&detDeadline,
		&txidHash,
		&payDetectedAt, &confirmDeadline, &confirmedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperPurchaseView{}, ErrHelperNotFound
	}
	if err != nil {
		return HelperPurchaseView{}, fmt.Errorf("v2: readPurchaseView: scan: %w", err)
	}

	v.CreatedAt = time.Unix(createdAt, 0)
	v.UpdatedAt = time.Unix(updatedAt, 0)
	v.DetectionDeadlineAt = time.Unix(detDeadline, 0)
	v.DetectedTxidHashPresent = txidHash.Valid && txidHash.String != ""
	v.ContactReadyAt = fromUnixPtr(contactReadyAt)
	v.FirstRevealedAt = fromUnixPtr(firstRevealedAt)
	v.ReceiptExpiresAt = fromUnixPtr(receiptExpiresAt)
	v.BalanceRetryDeadlineAt = fromUnixPtr(retryDeadline)
	v.LastBalanceUSD = fromFloat64Ptr(lastBalUSD)
	v.LastBalanceCheckedAt = fromUnixPtr(lastBalAt)
	v.PaymentDetectedAt = fromUnixPtr(payDetectedAt)
	v.ConfirmationDeadlineAt = fromUnixPtr(confirmDeadline)
	v.ConfirmedAt = fromUnixPtr(confirmedAt)

	return v, nil
}
