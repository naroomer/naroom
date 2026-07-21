// Package v2 implements the NA Room V2 client payment intent domain service.
//
// Isolation: this package uses only v2_* tables and must never be wired into
// cmd/naroom/main.go or share storage with V1 listings/invoices/sessions.
package v2

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Domain separation prefixes — different from V1's "naroom:v1:" namespace.
const (
	walletDomain = "naroom:v2:wallet:"
	codeDomain   = "naroom:v2:management-code:"
)

// entitlementDuration is the fixed five-calendar-day entitlement window.
const entitlementDuration = 5 * 24 * time.Hour

// clientFlowUSDCents is the required amount_usd_cents for a client payment intent.
const clientFlowUSDCents = 500

// Invoice lifecycle timing constants.
const (
	detectionWindow    = 60 * time.Minute
	confirmationWindow = 24 * time.Hour
)

// Invoice statuses.
const (
	InvoiceStatusPending   = "pending"
	InvoiceStatusDetected  = "payment_detected"
	InvoiceStatusConfirmed = "confirmed"
	InvoiceStatusExpired   = "expired"
)

// Flow lifecycle states.
const (
	StateAwaitingPayment  = "awaiting_payment"
	StatePaymentConfirmed = "payment_confirmed"
	StatePaidLowBalance   = "paid_low_balance"
	StateFormReady        = "form_ready"
)

// Sentinel errors returned to callers.
var (
	// ErrNotFound is returned when the code+wallet pair does not match any flow,
	// or when the requested flow/invoice does not exist. The same error is returned
	// for both wrong-code and wrong-wallet to prevent enumeration.
	ErrNotFound = errors.New("v2: payment intent not found")

	// ErrConflict is returned when a concurrent state transition causes a CAS
	// update to find 0 matching rows, or when detection is attempted with a
	// different txid after one is already locked.
	ErrConflict = errors.New("v2: conflicting update; re-read state and retry if appropriate")

	// ErrInvalidState is returned when an operation is not permitted in the
	// current flow state (e.g. balance check before payment confirmation).
	ErrInvalidState = errors.New("v2: operation not permitted in current state")

	// ErrInvalidCurrency is returned when currency is not BTC or LTC.
	ErrInvalidCurrency = errors.New("v2: currency must be BTC or LTC")

	// ErrInvalidDraft is returned when the InvoiceDraft fails validation.
	ErrInvalidDraft = errors.New("v2: invalid invoice draft")

	// ErrInvalidInput is returned when a numeric argument (balance, floor) is
	// NaN, Inf, negative, or otherwise outside permitted range.
	ErrInvalidInput = errors.New("v2: invalid numeric input")

	// ErrExpired is returned when an operation requires a live invoice but the
	// invoice is expired or a deadline has passed.
	ErrExpired = errors.New("v2: invoice is expired or deadline has passed")

	// ErrSenderMismatch is returned when none of the transaction input addresses
	// match the expected wallet fingerprint stored on the flow.
	ErrSenderMismatch = errors.New("v2: no input address matches the expected wallet fingerprint")

	// ErrInsufficientPayment is returned when the transaction amount is below
	// the required amount_atomic on the invoice.
	ErrInsufficientPayment = errors.New("v2: transaction amount below required amount_atomic")
)

// InvoiceDraft carries the invoice snapshot the caller must prepare before
// calling CreatePaymentIntent. The snapshot is stored atomically with the flow
// and the management code hash; it is returned unchanged by RestorePaymentIntent.
//
// Rules enforced at creation time:
//   - PaymentAddress must be non-empty.
//   - AmountAtomic must be > 0 (satoshis for BTC, litoshis for LTC).
//   - AmountUSDCents must equal 500 (the fixed $5 client listing fee).
//
// Monetary amounts are integers; no REAL is used for payment amounts.
type InvoiceDraft struct {
	PaymentAddress string // platform receive address
	AmountAtomic   int64  // exact amount in satoshis (BTC) or litoshis (LTC)
	AmountUSDCents int64  // USD amount in cents; must be 500 for client flow
}

// FlowView is the safe, outward-facing view of a flow and its linked invoice.
// It never exposes wallet fingerprint, management code hash, or raw secrets.
type FlowView struct {
	FlowID    string
	InvoiceID string
	Currency  string
	State     string

	// Invoice snapshot (stable from creation).
	InvoiceStatus  string
	PaymentAddress string
	AmountAtomic   int64
	AmountUSDCents int64

	// Detection deadline (always set at creation).
	DetectionDeadlineAt time.Time

	// Detection fields (set when payment_detected or confirmed).
	DetectedTxid      *string
	PaymentDetectedAt *time.Time
	// Confirmation grace deadline (set when payment_detected).
	ConfirmationDeadlineAt *time.Time

	// Payment confirmation fields (nil until confirmed).
	PaymentTxid          *string
	PaymentConfirmedAt   *time.Time
	EntitlementExpiresAt *time.Time

	// Balance check fields (nil until first check).
	LastBalanceUSD       *float64
	LastBalanceCheckedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Service provides transactional domain operations for V2 client payment intents.
type Service struct {
	db      *sql.DB
	hmacKey []byte
	now     func() time.Time
}

// New creates a Service. hmacKey must be a server secret; it must not be empty.
func New(db *sql.DB, hmacKey []byte) (*Service, error) {
	return NewWithClock(db, hmacKey, time.Now)
}

// NewWithClock creates a Service with an injectable clock. Intended for tests.
func NewWithClock(db *sql.DB, hmacKey []byte, now func() time.Time) (*Service, error) {
	if len(hmacKey) == 0 {
		return nil, errors.New("v2: hmacKey must not be empty")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{db: db, hmacKey: hmacKey, now: now}, nil
}

// nowUnix returns the current unix timestamp from the injectable clock.
func (s *Service) nowUnix() int64 { return s.now().Unix() }

// ── HMAC helpers ─────────────────────────────────────────────────────────────

// walletFingerprint returns HMAC-SHA256(key, "naroom:v2:wallet:"+currency+":"+normalized).
// Currency is included so BTC and LTC fingerprints of the same address string differ.
func (s *Service) walletFingerprint(currency, normalizedAddr string) string {
	mac := hmac.New(sha256.New, s.hmacKey)
	mac.Write([]byte(walletDomain))
	mac.Write([]byte(currency))
	mac.Write([]byte(":"))
	mac.Write([]byte(normalizedAddr))
	return hex.EncodeToString(mac.Sum(nil))
}

// codeHash returns HMAC-SHA256(key, "naroom:v2:management-code:"+rawCode).
func (s *Service) codeHash(rawCode string) string {
	mac := hmac.New(sha256.New, s.hmacKey)
	mac.Write([]byte(codeDomain))
	mac.Write([]byte(rawCode))
	return hex.EncodeToString(mac.Sum(nil))
}

// ── Address and input validation ──────────────────────────────────────────────

// normalizeAddress canonicalizes a wallet address. Bech32 (bc1…, ltc1…) is
// lowercased; legacy addresses are case-sensitive and returned unchanged.
func normalizeAddress(addr string) string {
	addr = strings.TrimSpace(addr)
	lower := strings.ToLower(addr)
	if strings.HasPrefix(lower, "bc1") || strings.HasPrefix(lower, "ltc1") {
		return lower
	}
	return addr
}

func validateCurrency(currency string) error {
	if currency != "BTC" && currency != "LTC" {
		return fmt.Errorf("%w: got %q", ErrInvalidCurrency, currency)
	}
	return nil
}

func validateDraft(d InvoiceDraft) error {
	if d.PaymentAddress == "" {
		return fmt.Errorf("%w: payment_address must not be empty", ErrInvalidDraft)
	}
	if d.AmountAtomic <= 0 {
		return fmt.Errorf("%w: amount_atomic must be > 0, got %d", ErrInvalidDraft, d.AmountAtomic)
	}
	if d.AmountUSDCents != clientFlowUSDCents {
		return fmt.Errorf("%w: amount_usd_cents must be %d for client flow, got %d",
			ErrInvalidDraft, clientFlowUSDCents, d.AmountUSDCents)
	}
	return nil
}

func validateBalanceInputs(balanceUSD, hardFloor float64) error {
	if math.IsNaN(balanceUSD) || math.IsInf(balanceUSD, 0) || balanceUSD < 0 {
		return fmt.Errorf("%w: balance must be a finite non-negative number, got %v",
			ErrInvalidInput, balanceUSD)
	}
	if math.IsNaN(hardFloor) || math.IsInf(hardFloor, 0) || hardFloor <= 0 {
		return fmt.Errorf("%w: hard floor must be a finite positive number, got %v",
			ErrInvalidInput, hardFloor)
	}
	return nil
}

// ── Random ID generation ──────────────────────────────────────────────────────

func newID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("v2: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func newRawCode() string { return newID() }

// ── Scan helpers ──────────────────────────────────────────────────────────────

func fromUnixPtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0)
	return &t
}

func fromFloat64Ptr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}

func fromStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

// scanFlowView reads one row from the joined flow+invoice query into a FlowView.
// Column order must match selectFlowByID and the RestorePaymentIntent query
// (minus wallet_fingerprint which RestorePaymentIntent scans separately).
func scanFlowView(row interface {
	Scan(dest ...any) error
}) (FlowView, error) {
	var (
		fv                     FlowView
		createdAt, updatedAt   int64
		detectionDeadlineAt    int64
		detectedTxid           sql.NullString
		paymentDetectedAt      sql.NullInt64
		confirmationDeadlineAt sql.NullInt64
		txid                   sql.NullString
		confirmedAt            sql.NullInt64
		expiresAt              sql.NullInt64
		balanceUSD             sql.NullFloat64
		balanceCheckedAt       sql.NullInt64
	)
	err := row.Scan(
		&fv.FlowID,
		&fv.Currency,
		&fv.State,
		&createdAt,
		&updatedAt,
		&fv.InvoiceID,
		&fv.InvoiceStatus,
		&fv.PaymentAddress,
		&fv.AmountUSDCents,
		&fv.AmountAtomic,
		&detectionDeadlineAt,
		&detectedTxid,
		&paymentDetectedAt,
		&confirmationDeadlineAt,
		&txid,
		&confirmedAt,
		&expiresAt,
		&balanceUSD,
		&balanceCheckedAt,
	)
	if err != nil {
		return FlowView{}, err
	}
	fv.CreatedAt = time.Unix(createdAt, 0)
	fv.UpdatedAt = time.Unix(updatedAt, 0)
	fv.DetectionDeadlineAt = time.Unix(detectionDeadlineAt, 0)
	fv.DetectedTxid = fromStringPtr(detectedTxid)
	fv.PaymentDetectedAt = fromUnixPtr(paymentDetectedAt)
	fv.ConfirmationDeadlineAt = fromUnixPtr(confirmationDeadlineAt)
	fv.PaymentTxid = fromStringPtr(txid)
	fv.PaymentConfirmedAt = fromUnixPtr(confirmedAt)
	fv.EntitlementExpiresAt = fromUnixPtr(expiresAt)
	fv.LastBalanceUSD = fromFloat64Ptr(balanceUSD)
	fv.LastBalanceCheckedAt = fromUnixPtr(balanceCheckedAt)
	return fv, nil
}

// selectFlowByID is the canonical joined query; column order matches scanFlowView.
const selectFlowByID = `
	SELECT f.id, f.currency, f.state, f.created_at, f.updated_at,
	       i.id, i.status,
	       i.payment_address, i.amount_usd_cents, i.amount_atomic,
	       i.detection_deadline_at, i.detected_txid, i.payment_detected_at,
	       i.confirmation_deadline_at,
	       i.payment_txid, i.payment_confirmed_at,
	       i.entitlement_expires_at, i.last_balance_usd, i.last_balance_checked_at
	FROM v2_client_flows f
	JOIN v2_invoices i ON i.flow_id = f.id
	WHERE f.id = ?`

// ── Domain service methods ────────────────────────────────────────────────────

// CreatePaymentIntent atomically creates a client flow, a linked invoice, and a
// management code. The raw code is returned exactly once and is never stored.
// The wallet address is never stored; only its keyed fingerprint is persisted.
//
// Validation order: currency → wallet address → draft; no DB write on any error.
func (s *Service) CreatePaymentIntent(
	walletAddress, currency string,
	draft InvoiceDraft,
) (rawCode string, fv FlowView, err error) {
	if err = validateCurrency(currency); err != nil {
		return "", FlowView{}, err
	}
	normalized := normalizeAddress(walletAddress)
	if normalized == "" {
		return "", FlowView{}, fmt.Errorf("v2: wallet address must not be empty")
	}
	// Trim surrounding whitespace from PaymentAddress before validation and storage.
	draft.PaymentAddress = strings.TrimSpace(draft.PaymentAddress)
	if err = validateDraft(draft); err != nil {
		return "", FlowView{}, err
	}

	fp := s.walletFingerprint(currency, normalized)
	rawCode = newRawCode()
	ch := s.codeHash(rawCode)
	flowID := newID()
	invoiceID := newID()
	now := s.nowUnix()
	detectionDeadlineAt := now + int64(detectionWindow.Seconds())

	tx, err := s.db.Begin()
	if err != nil {
		return "", FlowView{}, fmt.Errorf("v2: CreatePaymentIntent: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.Exec(`
		INSERT INTO v2_client_flows
		  (id, wallet_fingerprint, currency, management_code_hash, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		flowID, fp, currency, ch, StateAwaitingPayment, now, now,
	)
	if err != nil {
		return "", FlowView{}, fmt.Errorf("v2: CreatePaymentIntent: insert flow: %w", err)
	}

	_, err = tx.Exec(`
		INSERT INTO v2_invoices
		  (id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
		   detection_deadline_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		invoiceID, flowID, InvoiceStatusPending,
		draft.PaymentAddress, draft.AmountUSDCents, draft.AmountAtomic,
		detectionDeadlineAt, now, now,
	)
	if err != nil {
		return "", FlowView{}, fmt.Errorf("v2: CreatePaymentIntent: insert invoice: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return "", FlowView{}, fmt.Errorf("v2: CreatePaymentIntent: commit: %w", err)
	}

	fv = FlowView{
		FlowID:              flowID,
		InvoiceID:           invoiceID,
		Currency:            currency,
		State:               StateAwaitingPayment,
		InvoiceStatus:       InvoiceStatusPending,
		PaymentAddress:      draft.PaymentAddress,
		AmountAtomic:        draft.AmountAtomic,
		AmountUSDCents:      draft.AmountUSDCents,
		DetectionDeadlineAt: time.Unix(detectionDeadlineAt, 0),
		CreatedAt:           time.Unix(now, 0),
		UpdatedAt:           time.Unix(now, 0),
	}
	return rawCode, fv, nil
}

// RestorePaymentIntent verifies rawCode+walletAddress and returns the current
// flow/invoice view, including the full invoice snapshot. Both an invalid code
// and a mismatched wallet address return ErrNotFound to prevent enumeration.
func (s *Service) RestorePaymentIntent(rawCode, walletAddress string) (FlowView, error) {
	if rawCode == "" || walletAddress == "" {
		return FlowView{}, ErrNotFound
	}
	ch := s.codeHash(rawCode)

	const q = `
		SELECT f.id, f.currency, f.wallet_fingerprint, f.state, f.created_at, f.updated_at,
		       i.id, i.status,
		       i.payment_address, i.amount_usd_cents, i.amount_atomic,
		       i.detection_deadline_at, i.detected_txid, i.payment_detected_at,
		       i.confirmation_deadline_at,
		       i.payment_txid, i.payment_confirmed_at,
		       i.entitlement_expires_at, i.last_balance_usd, i.last_balance_checked_at
		FROM v2_client_flows f
		JOIN v2_invoices i ON i.flow_id = f.id
		WHERE f.management_code_hash = ?`

	row := s.db.QueryRow(q, ch)

	var (
		storedFP               string
		fv                     FlowView
		createdAt, updatedAt   int64
		detectionDeadlineAt    int64
		detectedTxid           sql.NullString
		paymentDetectedAt      sql.NullInt64
		confirmationDeadlineAt sql.NullInt64
		txid                   sql.NullString
		confirmedAt            sql.NullInt64
		expiresAt              sql.NullInt64
		balanceUSD             sql.NullFloat64
		balanceCheckedAt       sql.NullInt64
	)
	err := row.Scan(
		&fv.FlowID,
		&fv.Currency,
		&storedFP,
		&fv.State,
		&createdAt,
		&updatedAt,
		&fv.InvoiceID,
		&fv.InvoiceStatus,
		&fv.PaymentAddress,
		&fv.AmountUSDCents,
		&fv.AmountAtomic,
		&detectionDeadlineAt,
		&detectedTxid,
		&paymentDetectedAt,
		&confirmationDeadlineAt,
		&txid,
		&confirmedAt,
		&expiresAt,
		&balanceUSD,
		&balanceCheckedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return FlowView{}, ErrNotFound
	}
	if err != nil {
		return FlowView{}, fmt.Errorf("v2: RestorePaymentIntent: scan: %w", err)
	}

	// Constant-time comparison prevents timing side-channel between wrong-code
	// and wrong-wallet error paths.
	normalizedAddr := normalizeAddress(walletAddress)
	expectedFP := s.walletFingerprint(fv.Currency, normalizedAddr)
	if !hmac.Equal([]byte(expectedFP), []byte(storedFP)) {
		return FlowView{}, ErrNotFound
	}

	fv.CreatedAt = time.Unix(createdAt, 0)
	fv.UpdatedAt = time.Unix(updatedAt, 0)
	fv.DetectionDeadlineAt = time.Unix(detectionDeadlineAt, 0)
	fv.DetectedTxid = fromStringPtr(detectedTxid)
	fv.PaymentDetectedAt = fromUnixPtr(paymentDetectedAt)
	fv.ConfirmationDeadlineAt = fromUnixPtr(confirmationDeadlineAt)
	fv.PaymentTxid = fromStringPtr(txid)
	fv.PaymentConfirmedAt = fromUnixPtr(confirmedAt)
	fv.EntitlementExpiresAt = fromUnixPtr(expiresAt)
	fv.LastBalanceUSD = fromFloat64Ptr(balanceUSD)
	fv.LastBalanceCheckedAt = fromUnixPtr(balanceCheckedAt)
	return fv, nil
}

// RecordPaymentDetected transitions an invoice from pending to payment_detected.
// It verifies that the transaction amount meets the minimum, that the observation
// is within the detection window, and that one of the input addresses matches the
// stored wallet fingerprint.
//
// Idempotency: same txid after payment_detected → success, timestamps unchanged.
// Conflict: different txid after payment_detected → ErrConflict.
func (s *Service) RecordPaymentDetected(
	flowID, invoiceID, txid string,
	inputAddresses []string,
	amountAtomic int64,
	observedAt time.Time,
) (FlowView, error) {
	if txid == "" {
		return FlowView{}, fmt.Errorf("v2: RecordPaymentDetected: txid must not be empty")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return FlowView{}, fmt.Errorf("v2: RecordPaymentDetected: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Read flow fingerprint and currency.
	var walletFP, currency string
	err = tx.QueryRow(`
		SELECT wallet_fingerprint, currency FROM v2_client_flows WHERE id = ?`, flowID,
	).Scan(&walletFP, &currency)
	if errors.Is(err, sql.ErrNoRows) {
		return FlowView{}, ErrNotFound
	}
	if err != nil {
		return FlowView{}, fmt.Errorf("v2: RecordPaymentDetected: read flow: %w", err)
	}

	// Read invoice state.
	var (
		status            string
		invoiceAtomic     int64
		detectionDeadline int64
		storedTxid        sql.NullString
	)
	err = tx.QueryRow(`
		SELECT status, amount_atomic, detection_deadline_at, detected_txid
		FROM v2_invoices WHERE id = ? AND flow_id = ?`, invoiceID, flowID,
	).Scan(&status, &invoiceAtomic, &detectionDeadline, &storedTxid)
	if errors.Is(err, sql.ErrNoRows) {
		return FlowView{}, ErrNotFound
	}
	if err != nil {
		return FlowView{}, fmt.Errorf("v2: RecordPaymentDetected: read invoice: %w", err)
	}

	// Idempotency: same txid already detected.
	if status == InvoiceStatusDetected && storedTxid.Valid && storedTxid.String == txid {
		if err = tx.Commit(); err != nil {
			return FlowView{}, fmt.Errorf("v2: RecordPaymentDetected: commit (idempotent): %w", err)
		}
		return s.readFlowView(flowID)
	}

	// Conflict: different txid already locked.
	if status == InvoiceStatusDetected && storedTxid.Valid && storedTxid.String != txid {
		return FlowView{}, ErrConflict
	}

	// Terminal states.
	if status == InvoiceStatusExpired {
		return FlowView{}, ErrExpired
	}
	if status == InvoiceStatusConfirmed {
		return FlowView{}, ErrConflict
	}

	// Check detection deadline (inclusive boundary: observedAt <= deadline is accepted).
	if observedAt.Unix() > detectionDeadline {
		return FlowView{}, ErrExpired
	}

	// Check amount.
	if amountAtomic < invoiceAtomic {
		return FlowView{}, ErrInsufficientPayment
	}

	// Sender check: at least one input address must match the stored fingerprint.
	matched := false
	for _, addr := range inputAddresses {
		fp := s.walletFingerprint(currency, normalizeAddress(addr))
		if fp == walletFP {
			matched = true
			break
		}
	}
	if !matched {
		return FlowView{}, ErrSenderMismatch
	}

	now := observedAt.Unix()
	confirmDeadline := now + int64(confirmationWindow.Seconds())

	// CAS UPDATE: only transitions if status is still 'pending'.
	res, err := tx.Exec(`
		UPDATE v2_invoices
		SET status = ?, detected_txid = ?, payment_detected_at = ?,
		    confirmation_deadline_at = ?, updated_at = ?
		WHERE id = ? AND flow_id = ? AND status = ?`,
		InvoiceStatusDetected, txid, now, confirmDeadline, now,
		invoiceID, flowID, InvoiceStatusPending,
	)
	if err != nil {
		return FlowView{}, fmt.Errorf("v2: RecordPaymentDetected: update invoice: %w", err)
	}
	{
		n, raErr := res.RowsAffected()
		if raErr != nil {
			return FlowView{}, fmt.Errorf("v2: RecordPaymentDetected: rows affected: %w", raErr)
		}
		if n == 0 {
			// Lost the race — re-read to determine outcome.
			_ = tx.Rollback()
			return s.resolveDetectionRace(flowID, invoiceID, txid)
		}
		if n != 1 {
			return FlowView{}, fmt.Errorf("v2: RecordPaymentDetected: invoice update affected %d rows, expected 1", n)
		}
	}

	if err = tx.Commit(); err != nil {
		return FlowView{}, fmt.Errorf("v2: RecordPaymentDetected: commit: %w", err)
	}
	return s.readFlowView(flowID)
}

// resolveDetectionRace reads the committed state after a CAS miss.
func (s *Service) resolveDetectionRace(flowID, invoiceID, txid string) (FlowView, error) {
	var storedTxid sql.NullString
	var status string
	err := s.db.QueryRow(`
		SELECT status, detected_txid FROM v2_invoices
		WHERE id = ? AND flow_id = ?`, invoiceID, flowID,
	).Scan(&status, &storedTxid)
	if errors.Is(err, sql.ErrNoRows) {
		return FlowView{}, ErrNotFound
	}
	if err != nil {
		return FlowView{}, fmt.Errorf("v2: resolveDetectionRace: %w", err)
	}
	if status == InvoiceStatusDetected && storedTxid.Valid && storedTxid.String == txid {
		return s.readFlowView(flowID)
	}
	if status == InvoiceStatusExpired {
		return FlowView{}, ErrExpired
	}
	return FlowView{}, ErrConflict
}

// ConfirmPayment transitions a payment_detected invoice to confirmed and sets the
// five-day entitlement. observedAt is used as the confirmation timestamp.
//
//   - pending → ErrInvalidState (must detect first).
//   - expired → ErrExpired.
//   - confirmed → idempotent success.
//   - payment_detected → CAS update to confirmed.
func (s *Service) ConfirmPayment(flowID, invoiceID string, observedAt time.Time) (FlowView, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return FlowView{}, fmt.Errorf("v2: ConfirmPayment: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var (
		status          string
		storedTxid      sql.NullString
		confirmDeadline sql.NullInt64
	)
	err = tx.QueryRow(`
		SELECT i.status, i.detected_txid, i.confirmation_deadline_at
		FROM v2_client_flows f
		JOIN v2_invoices i ON i.flow_id = f.id
		WHERE f.id = ? AND i.id = ?`, flowID, invoiceID,
	).Scan(&status, &storedTxid, &confirmDeadline)
	if errors.Is(err, sql.ErrNoRows) {
		return FlowView{}, ErrNotFound
	}
	if err != nil {
		return FlowView{}, fmt.Errorf("v2: ConfirmPayment: read state: %w", err)
	}

	switch status {
	case InvoiceStatusPending:
		return FlowView{}, ErrInvalidState
	case InvoiceStatusExpired:
		return FlowView{}, ErrExpired
	case InvoiceStatusConfirmed:
		// Idempotent success.
		if err = tx.Commit(); err != nil {
			return FlowView{}, fmt.Errorf("v2: ConfirmPayment: commit (idempotent): %w", err)
		}
		return s.readFlowView(flowID)
	}

	// status == payment_detected — check grace deadline.
	if confirmDeadline.Valid && observedAt.Unix() > confirmDeadline.Int64 {
		return FlowView{}, ErrExpired
	}

	now := observedAt.Unix()
	expiresAt := now + int64(entitlementDuration.Seconds())
	confirmedTxid := ""
	if storedTxid.Valid {
		confirmedTxid = storedTxid.String
	}

	// Get-or-create Client profile atomically inside the same transaction.
	// walletFP and currency are already stored in v2_client_flows; read them inside the tx.
	var walletFP, flowCurrency string
	fpErr := tx.QueryRow(`SELECT wallet_fingerprint, currency FROM v2_client_flows WHERE id = ?`, flowID).
		Scan(&walletFP, &flowCurrency)
	if fpErr != nil {
		return FlowView{}, fmt.Errorf("v2: ConfirmPayment: read wallet fp: %w", fpErr)
	}
	clientProfileID, profErr := getOrCreateClientProfileTx(tx, walletFP, flowCurrency, now)
	if profErr != nil {
		return FlowView{}, fmt.Errorf("v2: ConfirmPayment: get-or-create client profile: %w", profErr)
	}

	// Update flow state and set client_profile_id atomically.
	flowRes, err := tx.Exec(`
		UPDATE v2_client_flows
		SET state = ?, client_profile_id = ?, updated_at = ?
		WHERE id = ? AND state = ?`,
		StatePaymentConfirmed, clientProfileID, now, flowID, StateAwaitingPayment,
	)
	if err != nil {
		return FlowView{}, fmt.Errorf("v2: ConfirmPayment: update flow: %w", err)
	}
	{
		n, raErr := flowRes.RowsAffected()
		if raErr != nil {
			return FlowView{}, fmt.Errorf("v2: ConfirmPayment: rows affected (flow): %w", raErr)
		}
		if n == 0 {
			// Flow already moved (e.g., concurrent confirm). Check outcome.
			_ = tx.Rollback()
			return s.resolveConfirmRaceNew(flowID, invoiceID)
		}
	}

	// Update invoice.
	invRes, err := tx.Exec(`
		UPDATE v2_invoices
		SET status = ?, payment_txid = ?, payment_confirmed_at = ?,
		    entitlement_expires_at = ?, updated_at = ?
		WHERE id = ? AND flow_id = ? AND status = ?`,
		InvoiceStatusConfirmed, confirmedTxid, now, expiresAt, now,
		invoiceID, flowID, InvoiceStatusDetected,
	)
	if err != nil {
		return FlowView{}, fmt.Errorf("v2: ConfirmPayment: update invoice: %w", err)
	}
	{
		n, raErr := invRes.RowsAffected()
		if raErr != nil {
			return FlowView{}, fmt.Errorf("v2: ConfirmPayment: rows affected (invoice): %w", raErr)
		}
		if n != 1 {
			return FlowView{}, fmt.Errorf("v2: ConfirmPayment: invoice update affected %d rows, expected 1", n)
		}
	}

	if err = tx.Commit(); err != nil {
		return FlowView{}, fmt.Errorf("v2: ConfirmPayment: commit: %w", err)
	}
	return s.readFlowView(flowID)
}

// resolveConfirmRaceNew reads committed state after a CAS miss on flow update.
func (s *Service) resolveConfirmRaceNew(flowID, invoiceID string) (FlowView, error) {
	var status string
	err := s.db.QueryRow(`
		SELECT i.status FROM v2_invoices i
		JOIN v2_client_flows f ON f.id = i.flow_id
		WHERE f.id = ? AND i.id = ?`, flowID, invoiceID,
	).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return FlowView{}, ErrNotFound
	}
	if err != nil {
		return FlowView{}, fmt.Errorf("v2: resolveConfirmRaceNew: %w", err)
	}
	if status == InvoiceStatusConfirmed {
		return s.readFlowView(flowID)
	}
	return FlowView{}, ErrConflict
}

// ExpireInvoice transitions an invoice to expired when the appropriate deadline
// has passed. The decision and the update are a single atomic CAS operation:
//
//   - pending AND now > detection_deadline_at    → expire
//   - payment_detected AND now > confirmation_deadline_at → expire
//
// Other outcomes (read-after-CAS-miss):
//   - already expired → idempotent success.
//   - confirmed → ErrConflict (cannot expire a confirmed invoice).
//   - pending / payment_detected, not-yet-due → ErrInvalidState (deadline not passed).
//
// Boundary: now == deadline is NOT expired (strictly greater than required).
func (s *Service) ExpireInvoice(flowID, invoiceID string, now time.Time) (FlowView, error) {
	nowUnix := now.Unix()

	// Single atomic CAS: the WHERE clause encodes both the status check and its
	// corresponding deadline check. There is no prior SELECT; the decision about
	// whether to expire and the actual UPDATE are one SQL operation.
	//
	// Race-proof: if a concurrent detection commits between a watcher's deadline
	// check and this UPDATE, the status is no longer 'pending', so the pending
	// branch of the WHERE clause misses. The invoice stays payment_detected with
	// its full 24h grace — the stale expiry cannot erase it.
	res, err := s.db.Exec(`
		UPDATE v2_invoices
		SET status = 'expired', updated_at = ?
		WHERE id = ? AND flow_id = ?
		  AND (
		    (status = 'pending'
		     AND ? > detection_deadline_at)
		    OR
		    (status = 'payment_detected'
		     AND confirmation_deadline_at IS NOT NULL
		     AND ? > confirmation_deadline_at)
		  )`,
		nowUnix, invoiceID, flowID,
		nowUnix, // for pending deadline check
		nowUnix, // for payment_detected deadline check
	)
	if err != nil {
		return FlowView{}, fmt.Errorf("v2: ExpireInvoice: update: %w", err)
	}

	n, raErr := res.RowsAffected()
	if raErr != nil {
		return FlowView{}, fmt.Errorf("v2: ExpireInvoice: rows affected: %w", raErr)
	}
	if n == 1 {
		// CAS succeeded — invoice is now expired.
		return s.readFlowView(flowID)
	}

	// n == 0: CAS missed. Re-read to determine why.
	return s.resolveExpireRace(flowID, invoiceID, now)
}

// resolveExpireRace reads committed state after an ExpireInvoice CAS miss and
// maps it to the correct sentinel error.
func (s *Service) resolveExpireRace(flowID, invoiceID string, now time.Time) (FlowView, error) {
	var (
		status          string
		detectDeadline  int64
		confirmDeadline sql.NullInt64
	)
	err := s.db.QueryRow(`
		SELECT i.status, i.detection_deadline_at, i.confirmation_deadline_at
		FROM v2_invoices i
		JOIN v2_client_flows f ON f.id = i.flow_id
		WHERE f.id = ? AND i.id = ?`, flowID, invoiceID,
	).Scan(&status, &detectDeadline, &confirmDeadline)
	if errors.Is(err, sql.ErrNoRows) {
		return FlowView{}, ErrNotFound
	}
	if err != nil {
		return FlowView{}, fmt.Errorf("v2: ExpireInvoice: resolve: %w", err)
	}

	switch status {
	case InvoiceStatusExpired:
		// Concurrent expiry already committed — idempotent success.
		return s.readFlowView(flowID)
	case InvoiceStatusConfirmed:
		// Cannot expire a confirmed invoice.
		return FlowView{}, ErrConflict
	case InvoiceStatusPending:
		// CAS missed because now <= detection_deadline_at (not yet due).
		return FlowView{}, ErrInvalidState
	case InvoiceStatusDetected:
		// CAS missed because now <= confirmation_deadline_at or deadline not set yet.
		return FlowView{}, ErrInvalidState
	default:
		return FlowView{}, fmt.Errorf("v2: ExpireInvoice: unexpected status %q after CAS miss", status)
	}
}

// MatchSenderAddress finds the first address from candidates whose HMAC fingerprint
// matches the wallet fingerprint stored on the flow. Returns ErrSenderMismatch if
// none match. Used by the watcher to identify the sender address for balance checks
// without storing the address.
func (s *Service) MatchSenderAddress(flowID string, candidates []string) (string, error) {
	var walletFP, currency string
	err := s.db.QueryRow(`
		SELECT wallet_fingerprint, currency FROM v2_client_flows WHERE id = ?`, flowID,
	).Scan(&walletFP, &currency)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("v2: MatchSenderAddress: read flow: %w", err)
	}

	for _, addr := range candidates {
		fp := s.walletFingerprint(currency, normalizeAddress(addr))
		if fp == walletFP {
			return addr, nil
		}
	}
	return "", ErrSenderMismatch
}

// LoadWatchableInvoices returns all invoices that need watcher attention:
//   - Invoice status IN ('pending', 'payment_detected')
//   - OR (invoice status == 'confirmed' AND flow.state == 'payment_confirmed' AND last_balance_usd IS NULL)
func (s *Service) LoadWatchableInvoices() ([]FlowView, error) {
	const q = `
		SELECT f.id, f.currency, f.state, f.created_at, f.updated_at,
		       i.id, i.status,
		       i.payment_address, i.amount_usd_cents, i.amount_atomic,
		       i.detection_deadline_at, i.detected_txid, i.payment_detected_at,
		       i.confirmation_deadline_at,
		       i.payment_txid, i.payment_confirmed_at,
		       i.entitlement_expires_at, i.last_balance_usd, i.last_balance_checked_at
		FROM v2_client_flows f
		JOIN v2_invoices i ON i.flow_id = f.id
		WHERE i.status IN ('pending', 'payment_detected')
		   OR (i.status = 'confirmed' AND f.state = 'payment_confirmed' AND i.last_balance_usd IS NULL)`

	rows, err := s.db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("v2: LoadWatchableInvoices: query: %w", err)
	}
	defer rows.Close()

	var result []FlowView
	for rows.Next() {
		fv, err := scanFlowView(rows)
		if err != nil {
			return nil, fmt.Errorf("v2: LoadWatchableInvoices: scan: %w", err)
		}
		result = append(result, fv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("v2: LoadWatchableInvoices: rows: %w", err)
	}
	return result, nil
}

// RecordPostPaymentBalance records an authoritative post-payment balance check and
// transitions the flow state accordingly.
//
//   - Rejected before any DB access: NaN/Inf/negative balance, zero/negative floor.
//   - Rejected: state is awaiting_payment (ErrInvalidState).
//   - balance >= hardFloor → form_ready.
//   - balance < hardFloor → paid_low_balance, unless already form_ready (no downgrade).
//   - CAS update is verified; if 0 rows affected (concurrent state change), the invoice
//     balance snapshot is NOT written and ErrConflict is returned.
func (s *Service) RecordPostPaymentBalance(flowID string, balanceUSD, hardFloor float64) (FlowView, error) {
	if err := validateBalanceInputs(balanceUSD, hardFloor); err != nil {
		return FlowView{}, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return FlowView{}, fmt.Errorf("v2: RecordPostPaymentBalance: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var (
		currentState string
		confirmedAt  sql.NullInt64
	)
	err = tx.QueryRow(`
		SELECT f.state, i.payment_confirmed_at
		FROM v2_client_flows f
		JOIN v2_invoices i ON i.flow_id = f.id
		WHERE f.id = ?`, flowID,
	).Scan(&currentState, &confirmedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return FlowView{}, ErrNotFound
	}
	if err != nil {
		return FlowView{}, fmt.Errorf("v2: RecordPostPaymentBalance: read state: %w", err)
	}

	// Balance result is only meaningful after a confirmed payment.
	if currentState == StateAwaitingPayment || !confirmedAt.Valid {
		return FlowView{}, ErrInvalidState
	}

	now := s.nowUnix()

	// Determine target state. form_ready is never downgraded.
	newState := StatePaidLowBalance
	if balanceUSD >= hardFloor {
		newState = StateFormReady
	} else if currentState == StateFormReady {
		newState = StateFormReady // no downgrade
	}

	// CAS update: only transitions if state matches what we read.
	res, err := tx.Exec(`
		UPDATE v2_client_flows SET state = ?, updated_at = ?
		WHERE id = ? AND state = ?`,
		newState, now, flowID, currentState,
	)
	if err != nil {
		// Any SQL error (including trigger ABORT) rolls back via defer tx.Rollback().
		return FlowView{}, fmt.Errorf("v2: RecordPostPaymentBalance: update flow: %w", err)
	}
	{
		n, raErr := res.RowsAffected()
		if raErr != nil {
			return FlowView{}, fmt.Errorf("v2: RecordPostPaymentBalance: rows affected (flow): %w", raErr)
		}
		if n == 0 {
			// CAS found 0 matching rows: state was modified between our SELECT and this
			// UPDATE by a concurrent caller. Roll back without writing the balance snapshot.
			_ = tx.Rollback()
			return FlowView{}, ErrConflict
		}
		if n != 1 {
			return FlowView{}, fmt.Errorf("v2: RecordPostPaymentBalance: flow update affected %d rows, expected 1", n)
		}
	}

	// State update succeeded; now record the balance snapshot.
	// This write is inside the same transaction and rolls back with it on any error.
	invRes, err := tx.Exec(`
		UPDATE v2_invoices
		SET last_balance_usd = ?, last_balance_checked_at = ?, updated_at = ?
		WHERE flow_id = ?`,
		balanceUSD, now, now, flowID,
	)
	if err != nil {
		return FlowView{}, fmt.Errorf("v2: RecordPostPaymentBalance: update invoice: %w", err)
	}
	{
		n, raErr := invRes.RowsAffected()
		if raErr != nil {
			return FlowView{}, fmt.Errorf("v2: RecordPostPaymentBalance: rows affected (invoice): %w", raErr)
		}
		if n != 1 {
			return FlowView{}, fmt.Errorf("v2: RecordPostPaymentBalance: invoice update affected %d rows, expected 1", n)
		}
	}

	if err = tx.Commit(); err != nil {
		return FlowView{}, fmt.Errorf("v2: RecordPostPaymentBalance: commit: %w", err)
	}
	return s.readFlowView(flowID)
}

// readFlowView fetches the current FlowView for a flowID.
func (s *Service) readFlowView(flowID string) (FlowView, error) {
	row := s.db.QueryRow(selectFlowByID, flowID)
	fv, err := scanFlowView(row)
	if errors.Is(err, sql.ErrNoRows) {
		return FlowView{}, ErrNotFound
	}
	if err != nil {
		return FlowView{}, fmt.Errorf("v2: readFlowView: %w", err)
	}
	return fv, nil
}
