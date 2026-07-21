package v2

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"
)

// Listing sentinel errors.
var (
	ErrListingCapabilityNotFound = errors.New("v2: listing capability not found")
	ErrFormNotReady              = errors.New("v2: payment not confirmed or form not ready")
	ErrEntitlementExpired        = errors.New("v2: entitlement expired")
	ErrBindingRequired           = errors.New("v2: active notification binding required")
	ErrListingAlreadyExists      = errors.New("v2: listing already exists for this flow")
	ErrAlreadyVisible            = errors.New("v2: listing is already visible")
	ErrLowBalance                = errors.New("v2: balance below required floor")
	ErrInvalidListingInput       = errors.New("v2: invalid listing or contact input")
	ErrBindingTTLInvalid         = errors.New("v2: binding TTL must be between 1s and 15m")
	ErrDestinationMissing        = errors.New("v2: transport destination missing for binding")
)

const listingDailyWindow = 24 * time.Hour
const listingReactivateFloorUSD = 120.0

// maxDisplayNameRetries caps bounded retry when display_name collides.
const maxDisplayNameRetries = 10

// ListingView is the safe management view returned to authenticated callers.
// It never includes contact ciphertext, nonce, key version, raw contact value,
// flow ID, binding ref, wallet fingerprint, or management code hash.
type ListingView struct {
	ID             string
	City           string
	CountryCode    string
	DependencyType string
	HelpType       string
	Urgency        string
	Languages      []string
	DisplayName    string
	ContactType    string // type only, not the contact value
	State          string
	VisibleUntil   *time.Time

	FirstPublishedAt     time.Time
	LastActivatedAt      time.Time
	EntitlementExpiresAt time.Time
	ActivationCount      int
	CreatedAt, UpdatedAt time.Time
}

// PublicListingView is the board-level public view returned to unauthenticated callers.
// It contains no contact data, no flow/invoice/wallet/code/fingerprint/binding data.
type PublicListingView struct {
	ID               string
	DisplayName      string
	City             string
	CountryCode      string
	DependencyType   string
	HelpType         string
	Urgency          string
	Languages        []string
	VisibleUntil     time.Time
	TimeLeftSec      int64
	ClientReputation ClientReputationView // live from v2_client_profiles; zero if no profile yet
}

// ListingService implements listing lifecycle operations on top of the core Service.
type ListingService struct {
	svc    *Service
	cipher ContactCipher
	names  DisplayNameGenerator
	cv     ContactValidator
	// _testHook is called in FirstPublish and Reactivate between pre-checks and
	// the atomic DB operation. It is nil in production. It must not accept secret data.
	_testHook func()
}

// NewListingService creates a ListingService. All arguments must be non-nil.
func NewListingService(svc *Service, cipher ContactCipher, names DisplayNameGenerator, cv ContactValidator) (*ListingService, error) {
	if svc == nil {
		return nil, errors.New("v2: NewListingService: svc must not be nil")
	}
	if cipher == nil {
		return nil, errors.New("v2: NewListingService: cipher must not be nil")
	}
	if names == nil {
		return nil, errors.New("v2: NewListingService: names must not be nil")
	}
	if cv == nil {
		return nil, errors.New("v2: NewListingService: cv must not be nil")
	}
	return &ListingService{svc: svc, cipher: cipher, names: names, cv: cv}, nil
}

func (ls *ListingService) now() time.Time { return ls.svc.now() }

// isValidBindingRef reports whether ref has the required opaque format:
// "bnd_" followed by exactly 32 lowercase hex characters.
func isValidBindingRef(ref string) bool {
	if len(ref) != 36 || !strings.HasPrefix(ref, "bnd_") {
		return false
	}
	for _, c := range ref[4:] {
		if !((c >= 'a' && c <= 'f') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// attachReadyBinding registers or replaces the ready notification binding for a flow.
// bindingRef must be an opaque reference with format bnd_<32 lowercase hex chars>.
// verifiedAt is when the binding was verified; readyUntil is the expiry of the ready window.
// TTL (readyUntil - verifiedAt) must be > 0 and <= 15 minutes.
//
// An active (state='active') binding that's still within its valid_until window
// blocks replacement and returns ErrAlreadyVisible.
// A ready (state='ready') or expired binding is replaced atomically.
func (ls *ListingService) attachReadyBinding(flowID, bindingRef string, verifiedAt, readyUntil time.Time) error {
	if flowID == "" || bindingRef == "" {
		return fmt.Errorf("%w: flowID and bindingRef must not be empty", ErrFormNotReady)
	}
	if !isValidBindingRef(bindingRef) {
		return fmt.Errorf("%w: binding reference has invalid format", ErrInvalidListingInput)
	}

	// TTL check.
	ttl := readyUntil.Sub(verifiedAt)
	if ttl <= 0 || ttl > 15*time.Minute {
		return ErrBindingTTLInvalid
	}

	nowUnix := ls.now().Unix()

	tx, err := ls.svc.db.Begin()
	if err != nil {
		return fmt.Errorf("v2: AttachReadyBinding: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Read: flow state, invoice entitlement_expires_at, listing state/visible_until/activation_count.
	var flowState string
	var entitlementExpiresAt sql.NullInt64
	var listingState sql.NullString
	var listingVisibleUntil sql.NullInt64
	var activationCount sql.NullInt64
	err = tx.QueryRow(`
		SELECT f.state, i.entitlement_expires_at,
		       l.state, l.visible_until, l.activation_count
		FROM v2_client_flows f
		JOIN v2_invoices i ON i.flow_id = f.id
		LEFT JOIN v2_listings l ON l.flow_id = f.id
		WHERE f.id = ?`, flowID,
	).Scan(&flowState, &entitlementExpiresAt, &listingState, &listingVisibleUntil, &activationCount)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrListingCapabilityNotFound
	}
	if err != nil {
		return fmt.Errorf("v2: AttachReadyBinding: read flow: %w", err)
	}

	// Invoice must be confirmed AND entitlement live.
	if !entitlementExpiresAt.Valid {
		return ErrFormNotReady
	}
	if nowUnix >= entitlementExpiresAt.Int64 {
		return ErrEntitlementExpired
	}

	// Determine window number.
	var windowNumber int64
	if !listingState.Valid {
		// No listing yet: flow must be form_ready; window = 1.
		if flowState != StateFormReady {
			return ErrFormNotReady
		}
		windowNumber = 1
	} else {
		// Listing exists: must not be finished; must not be effectively visible.
		if listingState.String == "finished" {
			return ErrEntitlementExpired
		}
		effectivelyVisible := listingState.String == "visible" && listingVisibleUntil.Valid && nowUnix < listingVisibleUntil.Int64
		if effectivelyVisible {
			return ErrAlreadyVisible
		}
		windowNumber = activationCount.Int64 + 1
	}

	// Delete existing ready or expired binding (but not an active unexpired one).
	if _, err = tx.Exec(`
		DELETE FROM v2_client_notification_bindings
		WHERE flow_id = ? AND (state = 'ready' OR valid_until <= ?)`,
		flowID, nowUnix,
	); err != nil {
		return fmt.Errorf("v2: AttachReadyBinding: delete old: %w", err)
	}

	bindingID := newID()
	verifiedUnix := verifiedAt.Unix()
	validUntilUnix := readyUntil.Unix()

	_, insErr := tx.Exec(`
		INSERT INTO v2_client_notification_bindings
		  (id, flow_id, binding_ref, state, window_number, verified_at, valid_until,
		   created_at, updated_at)
		VALUES (?, ?, ?, 'ready', ?, ?, ?, ?, ?)`,
		bindingID, flowID, bindingRef, windowNumber,
		verifiedUnix, validUntilUnix, nowUnix, nowUnix,
	)
	if insErr != nil {
		if isSQLiteUniqueOnColumn(insErr, "v2_client_notification_bindings.flow_id") {
			// An active unexpired binding still exists.
			return ErrAlreadyVisible
		}
		return fmt.Errorf("v2: AttachReadyBinding: insert: %w", insErr)
	}
	return tx.Commit()
}

// InvalidateBinding physically deletes the binding for a flow. No-op if none exists.
func (ls *ListingService) InvalidateBinding(flowID string) error {
	_, err := ls.svc.db.Exec(`DELETE FROM v2_client_notification_bindings WHERE flow_id = ?`, flowID)
	return err
}

// FirstPublish creates the first listing for a confirmed, form_ready flow.
// The flow must have a ready notification binding for window 1.
// Contact is encrypted before any DB write; no plaintext contact is stored or logged.
//
// The critical preconditions (flow state, invoice, entitlement, ready binding for window 1,
// and absence of an existing listing) are verified atomically inside a transaction.
// A bounded retry (maxDisplayNameRetries) handles display_name collisions.
func (ls *ListingService) FirstPublish(rawCode, walletAddress string, input ListingInput) (ListingView, error) {
	// Step 1: verify capability via code+wallet.
	fv, err := ls.svc.RestorePaymentIntent(rawCode, walletAddress)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ListingView{}, ErrListingCapabilityNotFound
		}
		return ListingView{}, fmt.Errorf("v2: FirstPublish: restore: %w", err)
	}

	nowUnix := ls.now().Unix()

	// Step 2: pre-check confirmed + form_ready + live entitlement (fast path rejection).
	if fv.State != StateFormReady || fv.InvoiceStatus != InvoiceStatusConfirmed || fv.EntitlementExpiresAt == nil {
		return ListingView{}, ErrFormNotReady
	}
	entitlementUnix := fv.EntitlementExpiresAt.Unix()
	if nowUnix >= entitlementUnix {
		return ListingView{}, ErrEntitlementExpired
	}

	// Step 3: validate listing input (including contact type and raw contact).
	vl, err := validateListingInput(input, ls.cv)
	if err != nil {
		return ListingView{}, err
	}

	// Step 4: generate listing ID first (needed as AAD for encryption).
	listingID := newID()

	// Step 5: encrypt contact — no DB write has happened yet, so a failure here
	// leaves no trace. The raw contact is never passed to slog or included in errors.
	ctHex, nonceHex, keyVer, err := ls.cipher.Encrypt(vl.normalizedContact, listingID, fv.FlowID, vl.contactType)
	if err != nil {
		slog.Error("v2: FirstPublish: encrypt contact", "err", "[internal]")
		return ListingView{}, fmt.Errorf("v2: FirstPublish: encrypt: [internal]")
	}

	// Step 6: compute visible_until = min(now+24h, entitlementExpiresAt).
	visibleUntil := entitlementUnix
	if candidate := nowUnix + int64(listingDailyWindow.Seconds()); candidate < entitlementUnix {
		visibleUntil = candidate
	}

	// Step 7: bounded retry loop for display_name collision.
	// Each attempt:
	//   a. Generates a fresh validated name.
	//   b. Calls _testHook (for interleaving tests).
	//   c. Begins a transaction.
	//   d. CAS UPDATE binding: ready→active for window 1.
	//   e. If 0 rows: ROLLBACK, classify, return error.
	//   f. INSERT listing (binding already claimed).
	//   g. On display_name collision: ROLLBACK, retry.
	//   h. On flow_id collision: ROLLBACK, return ErrListingAlreadyExists.
	//   i. On 0 rows from INSERT: ROLLBACK, classify.
	//   j. COMMIT.
	for attempt := 0; attempt < maxDisplayNameRetries; attempt++ {
		displayName, genErr := ls.names.Generate()
		if genErr != nil {
			return ListingView{}, fmt.Errorf("v2: FirstPublish: name: %w", genErr)
		}
		if valErr := validateDisplayName(displayName); valErr != nil {
			// Generator produced invalid output; no listing row has been created.
			return ListingView{}, fmt.Errorf("v2: FirstPublish: name validation: [internal]")
		}

		// Call test hook so interleaving tests can delete the binding
		// between this point and the atomic UPDATE below.
		if ls._testHook != nil {
			ls._testHook()
		}

		tx, txErr := ls.svc.db.Begin()
		if txErr != nil {
			return ListingView{}, fmt.Errorf("v2: FirstPublish: begin: %w", txErr)
		}

		// CAS UPDATE binding: ready → active for window 1, with full precondition check.
		bindRes, bindErr := tx.Exec(`
			UPDATE v2_client_notification_bindings
			SET state='active', activated_at=?, valid_until=?, updated_at=?
			WHERE flow_id=?
			  AND state='ready'
			  AND window_number=1
			  AND ? < valid_until
			  AND EXISTS (
			    SELECT 1 FROM v2_client_flows f
			    JOIN v2_invoices i ON i.flow_id = f.id
			    WHERE f.id = ?
			      AND f.state = 'form_ready'
			      AND i.status = 'confirmed'
			      AND i.entitlement_expires_at = ?
			      AND ? < i.entitlement_expires_at
			  )
			  AND NOT EXISTS (
			    SELECT 1 FROM v2_listings WHERE flow_id = ?
			  )`,
			nowUnix, visibleUntil, nowUnix,
			fv.FlowID,
			nowUnix,
			fv.FlowID, entitlementUnix, nowUnix,
			fv.FlowID,
		)
		if bindErr != nil {
			tx.Rollback() //nolint:errcheck
			return ListingView{}, fmt.Errorf("v2: FirstPublish: binding update: %w", bindErr)
		}
		bindN, bindRowsErr := bindRes.RowsAffected()
		if bindRowsErr != nil {
			tx.Rollback() //nolint:errcheck
			return ListingView{}, fmt.Errorf("v2: FirstPublish: binding rows affected: %w", bindRowsErr)
		}
		if bindN == 0 {
			tx.Rollback() //nolint:errcheck
			return ListingView{}, ls.classifyPublishFailure(fv.FlowID, entitlementUnix, nowUnix)
		}

		// STRICT: require exactly 1 destination updated.
		var activeBindingRef string
		refErr := tx.QueryRow(`
			SELECT binding_ref FROM v2_client_notification_bindings
			WHERE flow_id=? AND state='active' AND window_number=1`,
			fv.FlowID,
		).Scan(&activeBindingRef)
		if refErr != nil {
			// Any error including sql.ErrNoRows → rollback and return ErrDestinationMissing.
			tx.Rollback() //nolint:errcheck
			return ListingView{}, ErrDestinationMissing
		}
		destRes, destErr := tx.Exec(`UPDATE v2_telegram_destinations SET expires_at=? WHERE binding_ref=?`,
			visibleUntil, activeBindingRef)
		if destErr != nil {
			tx.Rollback() //nolint:errcheck
			return ListingView{}, fmt.Errorf("v2: FirstPublish: update destination: %w", destErr)
		}
		destN, destRowsErr := destRes.RowsAffected()
		if destRowsErr != nil {
			tx.Rollback() //nolint:errcheck
			return ListingView{}, fmt.Errorf("v2: FirstPublish: destination rows affected: %w", destRowsErr)
		}
		if destN == 0 {
			tx.Rollback() //nolint:errcheck
			return ListingView{}, ErrDestinationMissing
		}
		if destN != 1 {
			tx.Rollback() //nolint:errcheck
			return ListingView{}, fmt.Errorf("v2: FirstPublish: unexpected destination row count: [internal]")
		}

		// Binding is now active; INSERT listing.
		res, insErr := tx.Exec(`
			INSERT INTO v2_listings
			  (id, flow_id, city, country_code, dependency_type, help_type, urgency,
			   languages, display_name, contact_type, contact_ciphertext, contact_nonce,
			   contact_key_version, state, visible_until, first_published_at,
			   last_activated_at, entitlement_expires_at, activation_count,
			   created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'visible', ?, ?, ?, ?, 1, ?, ?)`,
			listingID, fv.FlowID, vl.city, vl.countryCode, vl.dependencyType,
			vl.helpType, vl.urgency, vl.languagesJSON, displayName,
			vl.contactType, ctHex, nonceHex, keyVer,
			visibleUntil, nowUnix, nowUnix, entitlementUnix,
			nowUnix, nowUnix,
		)
		if insErr != nil {
			tx.Rollback() //nolint:errcheck
			if isSQLiteUniqueOnColumn(insErr, "v2_listings.display_name") {
				// Name taken by another listing — try a new name.
				continue
			}
			if isSQLiteUniqueOnColumn(insErr, "v2_listings.flow_id") {
				// A concurrent first-publish won the race for this flow.
				return ListingView{}, ErrListingAlreadyExists
			}
			return ListingView{}, fmt.Errorf("v2: FirstPublish: insert: %w", insErr)
		}

		n, rowsErr := res.RowsAffected()
		if rowsErr != nil {
			tx.Rollback() //nolint:errcheck
			return ListingView{}, fmt.Errorf("v2: FirstPublish: rows affected: %w", rowsErr)
		}
		if n == 0 {
			tx.Rollback() //nolint:errcheck
			return ListingView{}, ls.classifyPublishFailure(fv.FlowID, entitlementUnix, nowUnix)
		}

		if commitErr := tx.Commit(); commitErr != nil {
			return ListingView{}, fmt.Errorf("v2: FirstPublish: commit: %w", commitErr)
		}

		return ls.scanListingView(fv.FlowID)
	}

	// All retries exhausted due to display_name collisions.
	return ListingView{}, fmt.Errorf("v2: FirstPublish: display name exhausted: [internal]")
}

// classifyPublishFailure re-reads state after an atomic UPDATE/INSERT returned zero rows
// and returns the most specific sentinel, without leaking capability.
func (ls *ListingService) classifyPublishFailure(flowID string, entitlementUnix, nowUnix int64) error {
	// Check whether a listing already exists for this flow.
	var existingID string
	if err := ls.svc.db.QueryRow(`SELECT id FROM v2_listings WHERE flow_id = ?`, flowID).Scan(&existingID); err == nil {
		return ErrListingAlreadyExists
	}
	// Check for a ready binding with window_number=1 that's still within its TTL.
	var bindingState string
	err := ls.svc.db.QueryRow(`
		SELECT state FROM v2_client_notification_bindings
		WHERE flow_id = ? AND state = 'ready' AND window_number = 1 AND ? < valid_until`,
		flowID, nowUnix,
	).Scan(&bindingState)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBindingRequired
	}
	return fmt.Errorf("v2: FirstPublish: precondition lost: [internal]")
}

// NormalizeExpired transitions stale listings to hidden or finished.
// Idempotent: safe to call repeatedly at any time.
//
// Rules (evaluated in order to avoid hidden→finished skipping):
//  1. Delete expired bindings (valid_until <= now).
//  2. visible/hidden with entitlement_expires_at <= now → finished
//  3. visible with visible_until <= now AND entitlement still live → hidden
func (ls *ListingService) NormalizeExpired(now time.Time) error {
	nowUnix := now.Unix()
	tx, err := ls.svc.db.Begin()
	if err != nil {
		return fmt.Errorf("v2: NormalizeExpired: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Delete expired bindings before listing transitions.
	if _, err = tx.Exec(`
		DELETE FROM v2_client_notification_bindings WHERE valid_until <= ?`,
		nowUnix,
	); err != nil {
		return fmt.Errorf("v2: NormalizeExpired: delete bindings: %w", err)
	}

	// Finish: entitlement expired — transition visible or hidden to finished.
	if _, err = tx.Exec(`
		UPDATE v2_listings SET state='finished', visible_until=NULL, updated_at=?
		WHERE state IN ('visible','hidden') AND ? >= entitlement_expires_at`,
		nowUnix, nowUnix,
	); err != nil {
		return fmt.Errorf("v2: NormalizeExpired: finish: %w", err)
	}

	// Hide: daily window expired but entitlement still valid — visible → hidden.
	if _, err = tx.Exec(`
		UPDATE v2_listings SET state='hidden', visible_until=NULL, updated_at=?
		WHERE state='visible' AND ? >= visible_until AND ? < entitlement_expires_at`,
		nowUnix, nowUnix, nowUnix,
	); err != nil {
		return fmt.Errorf("v2: NormalizeExpired: hide: %w", err)
	}

	return tx.Commit()
}

// Reactivate opens a new 24-hour visibility window for a hidden (or stale visible) listing.
// Requires: live entitlement, listing not finished, ready binding for next window, balance >= $120.
// balanceUSD must be finite, non-negative, and is provided by the caller's balance check.
//
// The ready binding is claimed atomically inside a transaction, so a concurrent deletion
// between any pre-read and the CAS cannot produce a new visible transition without a live binding.
func (ls *ListingService) Reactivate(rawCode, walletAddress string, balanceUSD float64) (ListingView, error) {
	// Validate balance before any DB access.
	if math.IsNaN(balanceUSD) || math.IsInf(balanceUSD, 0) || balanceUSD < 0 {
		return ListingView{}, fmt.Errorf("%w: balance must be finite and non-negative", ErrInvalidListingInput)
	}

	// Verify capability.
	fv, err := ls.svc.RestorePaymentIntent(rawCode, walletAddress)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ListingView{}, ErrListingCapabilityNotFound
		}
		return ListingView{}, fmt.Errorf("v2: Reactivate: restore: %w", err)
	}

	if fv.EntitlementExpiresAt == nil {
		return ListingView{}, ErrEntitlementExpired
	}
	entitlementUnix := fv.EntitlementExpiresAt.Unix()
	nowUnix := ls.now().Unix()
	if nowUnix >= entitlementUnix {
		return ListingView{}, ErrEntitlementExpired
	}

	// Pre-read current listing state + activation_count for fast-path classification and
	// to determine the next window number.
	var (
		listingState    string
		visibleUntilN   sql.NullInt64
		activationCount int64
	)
	err = ls.svc.db.QueryRow(`
		SELECT state, visible_until, activation_count FROM v2_listings WHERE flow_id = ?`, fv.FlowID,
	).Scan(&listingState, &visibleUntilN, &activationCount)
	if errors.Is(err, sql.ErrNoRows) {
		return ListingView{}, ErrFormNotReady // no listing yet
	}
	if err != nil {
		return ListingView{}, fmt.Errorf("v2: Reactivate: read listing: %w", err)
	}

	if listingState == "finished" {
		return ListingView{}, ErrEntitlementExpired
	}

	// Check if listing is effectively still visible (window not yet expired).
	effectivelyVisible := listingState == "visible" && visibleUntilN.Valid && nowUnix < visibleUntilN.Int64
	if effectivelyVisible {
		return ListingView{}, ErrAlreadyVisible
	}

	// Check balance floor.
	if balanceUSD < listingReactivateFloorUSD {
		return ListingView{}, ErrLowBalance
	}

	nextWindowNum := activationCount + 1

	// Compute new visible_until = min(now+24h, entitlementExpiresAt).
	newVisibleUntil := entitlementUnix
	if candidate := nowUnix + int64(listingDailyWindow.Seconds()); candidate < entitlementUnix {
		newVisibleUntil = candidate
	}

	// Call test hook so interleaving tests can delete the binding
	// between the pre-read above and the atomic CAS below.
	if ls._testHook != nil {
		ls._testHook()
	}

	// Begin transaction for atomic binding claim + listing update.
	tx, txErr := ls.svc.db.Begin()
	if txErr != nil {
		return ListingView{}, fmt.Errorf("v2: Reactivate: begin: %w", txErr)
	}
	defer tx.Rollback() //nolint:errcheck

	// CAS UPDATE binding: ready → active for nextWindowNum.
	bindRes, bindErr := tx.Exec(`
		UPDATE v2_client_notification_bindings
		SET state='active', activated_at=?, valid_until=?, updated_at=?
		WHERE flow_id=? AND state='ready' AND window_number=? AND ? < valid_until`,
		nowUnix, newVisibleUntil, nowUnix,
		fv.FlowID, nextWindowNum, nowUnix,
	)
	if bindErr != nil {
		return ListingView{}, fmt.Errorf("v2: Reactivate: binding update: %w", bindErr)
	}
	bindN, bindRowsErr := bindRes.RowsAffected()
	if bindRowsErr != nil {
		return ListingView{}, fmt.Errorf("v2: Reactivate: binding rows affected: %w", bindRowsErr)
	}
	if bindN == 0 {
		tx.Rollback() //nolint:errcheck
		return ListingView{}, ls.classifyReactivateFailure(fv.FlowID, nowUnix, nextWindowNum)
	}

	// STRICT: require exactly 1 destination updated.
	var activeBindingRef string
	refErr := tx.QueryRow(`
		SELECT binding_ref FROM v2_client_notification_bindings
		WHERE flow_id=? AND state='active' AND window_number=?`,
		fv.FlowID, nextWindowNum,
	).Scan(&activeBindingRef)
	if refErr != nil {
		tx.Rollback() //nolint:errcheck
		return ListingView{}, ErrDestinationMissing
	}
	destRes, destErr := tx.Exec(`UPDATE v2_telegram_destinations SET expires_at=? WHERE binding_ref=?`,
		newVisibleUntil, activeBindingRef)
	if destErr != nil {
		tx.Rollback() //nolint:errcheck
		return ListingView{}, fmt.Errorf("v2: Reactivate: update destination: %w", destErr)
	}
	destN, destRowsErr := destRes.RowsAffected()
	if destRowsErr != nil {
		tx.Rollback() //nolint:errcheck
		return ListingView{}, fmt.Errorf("v2: Reactivate: destination rows affected: %w", destRowsErr)
	}
	if destN == 0 {
		tx.Rollback() //nolint:errcheck
		return ListingView{}, ErrDestinationMissing
	}
	if destN != 1 {
		tx.Rollback() //nolint:errcheck
		return ListingView{}, fmt.Errorf("v2: Reactivate: unexpected destination row count: [internal]")
	}

	// CAS UPDATE listing: transition to visible.
	res, err := tx.Exec(`
		UPDATE v2_listings
		SET state='visible', visible_until=?, last_activated_at=?,
		    activation_count=activation_count+1, updated_at=?
		WHERE flow_id=? AND state IN ('visible','hidden')
		  AND (state='hidden' OR (state='visible' AND visible_until <= ?))
		  AND ? < entitlement_expires_at`,
		newVisibleUntil, nowUnix, nowUnix,
		fv.FlowID,
		nowUnix,
		nowUnix,
	)
	if err != nil {
		return ListingView{}, fmt.Errorf("v2: Reactivate: update: %w", err)
	}
	n, rowsErr := res.RowsAffected()
	if rowsErr != nil {
		return ListingView{}, fmt.Errorf("v2: Reactivate: rows affected: %w", rowsErr)
	}
	if n != 1 {
		tx.Rollback() //nolint:errcheck
		return ListingView{}, ls.classifyReactivateFailure(fv.FlowID, nowUnix, nextWindowNum)
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return ListingView{}, fmt.Errorf("v2: Reactivate: commit: %w", commitErr)
	}

	return ls.scanListingView(fv.FlowID)
}

// classifyReactivateFailure re-reads state after CAS UPDATE returned zero rows
// and returns the most specific sentinel.
func (ls *ListingService) classifyReactivateFailure(flowID string, nowUnix int64, nextWindowNum int64) error {
	var state string
	var visibleUntilN sql.NullInt64
	var activationCount int64
	err := ls.svc.db.QueryRow(`
		SELECT state, visible_until, activation_count FROM v2_listings WHERE flow_id=?`, flowID,
	).Scan(&state, &visibleUntilN, &activationCount)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrFormNotReady
	}
	if err != nil {
		return fmt.Errorf("v2: Reactivate: classify: %w", err)
	}
	if state == "finished" {
		return ErrEntitlementExpired
	}
	if state == "visible" && visibleUntilN.Valid && nowUnix < visibleUntilN.Int64 {
		return ErrAlreadyVisible
	}
	// Check for a ready binding for the next window.
	var bindState string
	bindErr := ls.svc.db.QueryRow(`
		SELECT state FROM v2_client_notification_bindings
		WHERE flow_id = ? AND state = 'ready' AND window_number = ? AND ? < valid_until`,
		flowID, activationCount+1, nowUnix,
	).Scan(&bindState)
	if errors.Is(bindErr, sql.ErrNoRows) {
		return ErrBindingRequired
	}
	return ErrConflict
}

// BoardQuery returns visible listings for a city at the given time.
// Only listings with state='visible', visible_until > now, and entitlement_expires_at > now
// are returned. Unknown city returns ErrInvalidListingInput.
func (ls *ListingService) BoardQuery(city string, now time.Time) ([]PublicListingView, error) {
	city = strings.TrimSpace(strings.ToLower(city))
	if _, ok := cityCountry[city]; !ok {
		return nil, fmt.Errorf("%w: unknown city for board query", ErrInvalidListingInput)
	}
	nowUnix := now.Unix()
	rows, err := ls.svc.db.Query(`
		SELECT l.id, l.display_name, l.city, l.country_code, l.dependency_type, l.help_type,
		       l.urgency, l.languages, l.visible_until, l.entitlement_expires_at,
		       COALESCE(cp.created_at, 0), COALESCE(cp.positive_count, 0), COALESCE(cp.negative_count, 0)
		FROM v2_listings l
		JOIN v2_client_flows f ON f.id = l.flow_id
		LEFT JOIN v2_client_profiles cp ON cp.id = f.client_profile_id
		WHERE l.city = ? AND l.state = 'visible'
		  AND l.visible_until > ? AND l.entitlement_expires_at > ?
		ORDER BY l.last_activated_at DESC, l.id ASC`,
		city, nowUnix, nowUnix,
	)
	if err != nil {
		return nil, fmt.Errorf("v2: BoardQuery: query: %w", err)
	}
	defer rows.Close()

	var result []PublicListingView
	for rows.Next() {
		var (
			p              PublicListingView
			visUntil       int64
			entExp         int64
			langsJSON      string
			repMemberSince int64
			repPos, repNeg int
		)
		if err = rows.Scan(&p.ID, &p.DisplayName, &p.City, &p.CountryCode,
			&p.DependencyType, &p.HelpType, &p.Urgency,
			&langsJSON, &visUntil, &entExp,
			&repMemberSince, &repPos, &repNeg,
		); err != nil {
			return nil, fmt.Errorf("v2: BoardQuery: scan: %w", err)
		}
		p.VisibleUntil = time.Unix(visUntil, 0)
		p.TimeLeftSec = visUntil - nowUnix
		if p.TimeLeftSec < 0 {
			p.TimeLeftSec = 0
		}
		if err = json.Unmarshal([]byte(langsJSON), &p.Languages); err != nil {
			return nil, fmt.Errorf("v2: BoardQuery: parse languages: %w", err)
		}
		p.ClientReputation = ClientReputationView{
			MemberSince:   time.Unix(repMemberSince, 0),
			PositiveCount: repPos,
			NegativeCount: repNeg,
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// GetPublicListing returns the public view of a currently effectively visible listing.
// Only listings with state='visible', visible_until > now, and entitlement_expires_at > now
// are returned. Hidden, expired, finished, unknown, and empty IDs all return ErrNotFound.
// This method does not mutate any lifecycle state.
func (ls *ListingService) GetPublicListing(listingID string, now time.Time) (PublicListingView, error) {
	if strings.TrimSpace(listingID) == "" {
		return PublicListingView{}, ErrNotFound
	}
	nowUnix := now.Unix()
	var (
		p              PublicListingView
		visUntil       int64
		langsJSON      string
		repMemberSince int64
		repPos, repNeg int
	)
	err := ls.svc.db.QueryRow(`
		SELECT l.id, l.display_name, l.city, l.country_code, l.dependency_type, l.help_type,
		       l.urgency, l.languages, l.visible_until, l.entitlement_expires_at,
		       COALESCE(cp.created_at, 0), COALESCE(cp.positive_count, 0), COALESCE(cp.negative_count, 0)
		FROM v2_listings l
		JOIN v2_client_flows f ON f.id = l.flow_id
		LEFT JOIN v2_client_profiles cp ON cp.id = f.client_profile_id
		WHERE l.id = ? AND l.state = 'visible'
		  AND l.visible_until > ? AND l.entitlement_expires_at > ?`,
		listingID, nowUnix, nowUnix,
	).Scan(&p.ID, &p.DisplayName, &p.City, &p.CountryCode,
		&p.DependencyType, &p.HelpType, &p.Urgency,
		&langsJSON, &visUntil, new(int64), // entitlement_expires_at scanned but not exposed
		&repMemberSince, &repPos, &repNeg,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PublicListingView{}, ErrNotFound
	}
	if err != nil {
		return PublicListingView{}, fmt.Errorf("v2: GetPublicListing: %w", err)
	}
	p.VisibleUntil = time.Unix(visUntil, 0)
	p.TimeLeftSec = visUntil - nowUnix
	if p.TimeLeftSec < 0 {
		p.TimeLeftSec = 0
	}
	if err = json.Unmarshal([]byte(langsJSON), &p.Languages); err != nil {
		return PublicListingView{}, fmt.Errorf("v2: GetPublicListing: parse languages: %w", err)
	}
	p.ClientReputation = ClientReputationView{
		MemberSince:   time.Unix(repMemberSince, 0),
		PositiveCount: repPos,
		NegativeCount: repNeg,
	}
	return p, nil
}

// scanListingView reads the listing for the given flowID and returns a ListingView.
// Contact ciphertext, nonce, key version, flow ID, and any other capability fields
// are intentionally excluded from the result.
func (ls *ListingService) scanListingView(flowID string) (ListingView, error) {
	var (
		lv                                              ListingView
		visUntil                                        sql.NullInt64
		firstPub, lastAct, entExp, createdAt, updatedAt int64
		langsJSON                                       string
	)
	err := ls.svc.db.QueryRow(`
		SELECT id, city, country_code, dependency_type, help_type, urgency,
		       languages, display_name, contact_type, state, visible_until,
		       first_published_at, last_activated_at, entitlement_expires_at,
		       activation_count, created_at, updated_at
		FROM v2_listings WHERE flow_id = ?`, flowID,
	).Scan(
		&lv.ID, &lv.City, &lv.CountryCode, &lv.DependencyType,
		&lv.HelpType, &lv.Urgency, &langsJSON, &lv.DisplayName, &lv.ContactType,
		&lv.State, &visUntil,
		&firstPub, &lastAct, &entExp,
		&lv.ActivationCount, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ListingView{}, ErrNotFound
	}
	if err != nil {
		return ListingView{}, fmt.Errorf("v2: scanListingView: %w", err)
	}

	if visUntil.Valid {
		t := time.Unix(visUntil.Int64, 0)
		lv.VisibleUntil = &t
	}
	lv.FirstPublishedAt = time.Unix(firstPub, 0)
	lv.LastActivatedAt = time.Unix(lastAct, 0)
	lv.EntitlementExpiresAt = time.Unix(entExp, 0)
	lv.CreatedAt = time.Unix(createdAt, 0)
	lv.UpdatedAt = time.Unix(updatedAt, 0)
	if err = json.Unmarshal([]byte(langsJSON), &lv.Languages); err != nil {
		return ListingView{}, fmt.Errorf("v2: scanListingView: parse languages: %w", err)
	}
	return lv, nil
}

// isSQLiteUniqueViolation reports whether err is a SQLite UNIQUE constraint failure.
func isSQLiteUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// isSQLiteUniqueOnColumn reports whether err is a UNIQUE constraint failure on the
// given fully-qualified column name (e.g. "v2_listings.display_name").
func isSQLiteUniqueOnColumn(err error, column string) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: "+column)
}
