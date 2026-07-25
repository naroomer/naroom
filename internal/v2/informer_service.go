// Package v2 — Informer V2 service.
//
// Completely isolated from Client/Helper identity. Raw wallet is checked for
// balance but never stored. chat_id is encrypted at rest with AES-256-GCM.
// The InformerService implements InformerTxEnqueuer so it can be wired into
// ListingService.SetInformerNotifier to atomically enqueue outbox events
// inside the listing's first-publish transaction.
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
	"log/slog"
	"strings"
	"time"
)

const informerChatDomain = "naroom:v2:informer-chat:"
const informerTokenDomain = "naroom:v2:informer-token:"
const informerTokenTTL = 15 * time.Minute

// InformerMinBalanceUSD is the minimum balance required for Informer subscription.
const InformerMinBalanceUSD = 1000.0

// MaxRecipientAttempts is the maximum per-recipient delivery attempts before the
// recipient row is marked retry_exhausted (subscription is kept — outage ≠ invalid).
const MaxRecipientAttempts = 3

// MaxOutboxAttempts is retained for API compatibility; delivery termination is now
// driven by per-recipient attempt counters rather than the outbox-level counter.
const MaxOutboxAttempts = MaxRecipientAttempts

// defaultInformerWorkerLeaseSecs is the default claim lease duration in seconds.
const defaultInformerWorkerLeaseSecs = 120

// Informer sentinel errors.
var (
	ErrInformerTokenExpired   = errors.New("v2: informer token expired")
	ErrInformerTokenClaimed   = errors.New("v2: informer token already claimed")
	ErrInformerTokenNotFound  = errors.New("v2: informer token not found")
	ErrInformerNotPrivateChat = errors.New("v2: informer requires private chat")
	ErrInformerLowBalance     = errors.New("v2: informer balance below required floor")
	ErrInformerInvalidCity    = errors.New("v2: informer city not supported")
	ErrInformerDuplicateEvent = errors.New("v2: informer outbox duplicate (already exists)")
	// ErrInformerNotificationPermanent signals that a send failure is permanent
	// and the outbox entry must be marked failed immediately without retry.
	ErrInformerNotificationPermanent = errors.New("v2: informer notification permanent failure")
	// ErrInformerLostClaim signals that the worker lost its claim (e.g. lease expired
	// and another worker reclaimed the entry). Current worker must stop immediately.
	ErrInformerLostClaim = errors.New("v2: informer outbox claim lost")
)

// InformerBotSender sends a text notification to a Telegram chat.
type InformerBotSender interface {
	SendInformerNotification(ctx context.Context, chatID int64, text string) error
}

// InformerOutboxEntry is a public view of one outbox row.
type InformerOutboxEntry struct {
	ID          string
	ListingID   string
	City        string
	DisplayName string
	HelpType    string
	DepType     string
	Urgency     string
	ListingURL  string
	State       string
	Attempt     int
	ClaimedBy   string
	ClaimToken  string // random per-claim token; only the current claimer knows this
	LeaseUntil  int64  // unix epoch when claim expires; 0 = unclaimed
	CreatedAt   time.Time
}

// InformerService manages subscription lifecycle and outbox events.
// It implements InformerTxEnqueuer so it can be passed to ListingService.SetInformerNotifier.
type InformerService struct {
	db          *sql.DB
	hmacKey     []byte
	tokenSecret []byte
	destCipher  *DestinationCipher
	now         func() time.Time
	policy      V2BalancePolicy
}

// NewInformerService creates an InformerService.
func NewInformerService(db *sql.DB, hmacKey, tokenSecret []byte, destCipher *DestinationCipher, now func() time.Time) (*InformerService, error) {
	if db == nil {
		return nil, errors.New("v2: NewInformerService: db must not be nil")
	}
	if len(hmacKey) == 0 {
		return nil, errors.New("v2: NewInformerService: hmacKey must not be empty")
	}
	if len(tokenSecret) == 0 {
		return nil, errors.New("v2: NewInformerService: tokenSecret must not be empty")
	}
	if destCipher == nil {
		return nil, errors.New("v2: NewInformerService: destCipher must not be nil")
	}
	if now == nil {
		now = time.Now
	}
	return &InformerService{
		db: db, hmacKey: hmacKey, tokenSecret: tokenSecret,
		destCipher: destCipher, now: now,
		policy: DefaultV2BalancePolicy(),
	}, nil
}

// SetPolicy replaces the balance policy on this InformerService.
func (s *InformerService) SetPolicy(p V2BalancePolicy) { s.policy = p }

// newInformerSubRef returns "isub_" + 32 lowercase hex chars (16 random bytes).
func newInformerSubRef() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "isub_" + hex.EncodeToString(b), nil
}

// newClaimToken returns a fresh random 16-byte token as a 32-char hex string.
func newClaimToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("v2: newClaimToken: rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// chatHMAC returns HMAC-SHA256(hmacKey, informerChatDomain + decimal(chatID)).
func (s *InformerService) chatHMAC(chatID int64) string {
	mac := hmac.New(sha256.New, s.hmacKey)
	mac.Write([]byte(informerChatDomain))
	mac.Write([]byte(fmt.Sprintf("%d", chatID)))
	return hex.EncodeToString(mac.Sum(nil))
}

// tokenHMAC returns HMAC-SHA256(tokenSecret, informerTokenDomain + rawToken).
func (s *InformerService) tokenHMAC(rawToken string) string {
	mac := hmac.New(sha256.New, s.tokenSecret)
	mac.Write([]byte(informerTokenDomain))
	mac.Write([]byte(rawToken))
	return hex.EncodeToString(mac.Sum(nil))
}

// informerSupportedCities returns the set of supported city IDs (same as listing form).
func informerSupportedCities() map[string]bool {
	return map[string]bool{
		"buenos_aires": true, "sao_paulo": true, "nha_trang": true,
		"da_nang": true, "tbilisi": true, "batumi": true,
		"almaty": true, "yerevan": true, "moscow": true,
	}
}

// isInformerCity reports whether city is in the supported list.
func isInformerCity(city string) bool {
	return informerSupportedCities()[city]
}

// CreateAccess validates city, checks balanceUSD ≥ policy.InformerMinUSD, and returns a 15-min raw token.
func (s *InformerService) CreateAccess(city string, balanceUSD float64) (rawToken string, expiresAt time.Time, err error) {
	city = strings.TrimSpace(strings.ToLower(city))
	if !isInformerCity(city) {
		return "", time.Time{}, fmt.Errorf("%w: %q", ErrInformerInvalidCity, city)
	}
	if balanceUSD < s.policy.InformerMinUSD {
		return "", time.Time{}, fmt.Errorf("%w: %.2f < %.2f", ErrInformerLowBalance, balanceUSD, s.policy.InformerMinUSD)
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, fmt.Errorf("v2: CreateAccess: rand: %w", err)
	}
	rawToken = base64.RawURLEncoding.EncodeToString(b)
	th := s.tokenHMAC(rawToken)

	now := s.now()
	exp := now.Add(informerTokenTTL)
	id := newID()

	_, dbErr := s.db.Exec(`
		INSERT INTO v2_informer_tokens (id, token_hmac, city, state, expires_at, created_at)
		VALUES (?, ?, ?, 'pending', ?, ?)`,
		id, th, city, exp.Unix(), now.Unix(),
	)
	if dbErr != nil {
		return "", time.Time{}, fmt.Errorf("v2: CreateAccess: insert: %w", dbErr)
	}
	return rawToken, exp, nil
}

// QueryStatus returns city and state ("pending" or "claimed") of a token.
func (s *InformerService) QueryStatus(rawToken string) (city, state string, err error) {
	th := s.tokenHMAC(rawToken)
	var expiresAt int64
	var dbState, dbCity string
	dbErr := s.db.QueryRow(
		`SELECT city, state, expires_at FROM v2_informer_tokens WHERE token_hmac = ?`, th,
	).Scan(&dbCity, &dbState, &expiresAt)
	if errors.Is(dbErr, sql.ErrNoRows) {
		return "", "", ErrInformerTokenNotFound
	}
	if dbErr != nil {
		return "", "", fmt.Errorf("v2: QueryStatus: %w", dbErr)
	}
	if dbState == "expired" || s.now().Unix() > expiresAt {
		return "", "", ErrInformerTokenExpired
	}
	return dbCity, dbState, nil
}

// Subscribe atomically claims a pending token and upserts ONE subscription for chatID.
func (s *InformerService) Subscribe(chatID int64, rawToken string) error {
	if chatID <= 0 {
		return ErrInformerNotPrivateChat
	}
	th := s.tokenHMAC(rawToken)
	nowUnix := s.now().Unix()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("v2: Subscribe: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var tokenID, tokenCity, tokenState string
	var expiresAt int64
	scanErr := tx.QueryRow(
		`SELECT id, city, state, expires_at FROM v2_informer_tokens WHERE token_hmac = ?`, th,
	).Scan(&tokenID, &tokenCity, &tokenState, &expiresAt)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return ErrInformerTokenNotFound
	}
	if scanErr != nil {
		return fmt.Errorf("v2: Subscribe: read token: %w", scanErr)
	}
	if tokenState == "expired" || nowUnix > expiresAt {
		return ErrInformerTokenExpired
	}
	if tokenState == "claimed" {
		return ErrInformerTokenClaimed
	}

	res, updErr := tx.Exec(
		`UPDATE v2_informer_tokens SET state='claimed' WHERE id=? AND state='pending'`, tokenID,
	)
	if updErr != nil {
		return fmt.Errorf("v2: Subscribe: claim token: %w", updErr)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrInformerTokenClaimed
	}

	chatH := s.chatHMAC(chatID)
	var existingSubRef string
	existingErr := tx.QueryRow(
		`SELECT sub_ref FROM v2_informer_chat_index WHERE chat_hmac = ?`, chatH,
	).Scan(&existingSubRef)

	if existingErr == nil {
		var existingCity string
		_ = tx.QueryRow(
			`SELECT city FROM v2_informer_subscriptions WHERE sub_ref=?`, existingSubRef,
		).Scan(&existingCity)
		if existingCity == tokenCity {
			return tx.Commit()
		}
		if _, delErr := tx.Exec(
			`DELETE FROM v2_informer_subscriptions WHERE sub_ref=?`, existingSubRef,
		); delErr != nil {
			return fmt.Errorf("v2: Subscribe: delete old sub: %w", delErr)
		}
	}

	subRef, refErr := newInformerSubRef()
	if refErr != nil {
		return fmt.Errorf("v2: Subscribe: gen sub_ref: %w", refErr)
	}
	subID := newID()
	if _, insErr := tx.Exec(`
		INSERT INTO v2_informer_subscriptions (id, sub_ref, city, state, created_at, updated_at)
		VALUES (?, ?, ?, 'active', ?, ?)`,
		subID, subRef, tokenCity, nowUnix, nowUnix,
	); insErr != nil {
		return fmt.Errorf("v2: Subscribe: insert sub: %w", insErr)
	}

	ctHex, nonceHex, encErr := s.destCipher.EncryptChatID(chatID, subRef)
	if encErr != nil {
		return fmt.Errorf("v2: Subscribe: encrypt: [internal]")
	}
	if _, destErr := tx.Exec(`
		INSERT INTO v2_informer_destinations
		  (sub_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		subRef, ctHex, nonceHex, s.destCipher.keyVersion, nowUnix,
	); destErr != nil {
		return fmt.Errorf("v2: Subscribe: insert dest: %w", destErr)
	}

	if _, idxErr := tx.Exec(`
		INSERT INTO v2_informer_chat_index (chat_hmac, sub_ref) VALUES (?, ?)`,
		chatH, subRef,
	); idxErr != nil {
		return fmt.Errorf("v2: Subscribe: insert chat index: %w", idxErr)
	}

	return tx.Commit()
}

// Unsubscribe deletes the subscription for chatID. Idempotent if none exists.
func (s *InformerService) Unsubscribe(chatID int64) error {
	chatH := s.chatHMAC(chatID)
	var subRef string
	err := s.db.QueryRow(
		`SELECT sub_ref FROM v2_informer_chat_index WHERE chat_hmac = ?`, chatH,
	).Scan(&subRef)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("v2: Unsubscribe: read index: %w", err)
	}
	if _, delErr := s.db.Exec(
		`DELETE FROM v2_informer_subscriptions WHERE sub_ref=?`, subRef,
	); delErr != nil {
		return fmt.Errorf("v2: Unsubscribe: delete: %w", delErr)
	}
	return nil
}

// EnqueueFirstPublishTx implements InformerTxEnqueuer.
//
// The recipient snapshot is captured atomically inside the caller's transaction:
// active city subscribers are queried and inserted as recipient rows in the same
// TX that creates the outbox entry. A subscriber who joins after this TX commits
// will never appear in the snapshot and therefore never receives this event.
// An empty snapshot (no current subscribers) is valid — the worker marks the
// outbox done immediately.
func (s *InformerService) EnqueueFirstPublishTx(
	tx *sql.Tx,
	listingID, city, displayName, helpType, depType, urgency string,
	now time.Time,
) error {
	eventKey := "first_publish:" + listingID
	listingURL := "/v2/listing/" + listingID
	id := newID()
	nowUnix := now.Unix()
	_, err := tx.Exec(`
		INSERT INTO v2_informer_outbox
		  (id, listing_id, city, display_name, help_type, dep_type, urgency,
		   listing_url, event_key, state, attempt, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 0, ?, ?)`,
		id, listingID, city, displayName, helpType, depType, urgency,
		listingURL, eventKey, nowUnix, nowUnix,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: v2_informer_outbox.event_key") {
			return ErrInformerDuplicateEvent
		}
		return fmt.Errorf("v2: EnqueueFirstPublishTx: insert: %w", err)
	}

	// Capture recipient snapshot within the same TX.
	if err := snapshotRecipientsInTx(tx, id, city, nowUnix); err != nil {
		return fmt.Errorf("v2: EnqueueFirstPublishTx: snapshot recipients: %w", err)
	}
	return nil
}

// snapshotRecipientsInTx queries active subscribers for city and inserts them as
// pending recipients for outboxID, all within the provided transaction.
func snapshotRecipientsInTx(tx *sql.Tx, outboxID, city string, nowUnix int64) error {
	rows, err := tx.Query(
		`SELECT sub_ref FROM v2_informer_subscriptions WHERE city=? AND state='active'`, city,
	)
	if err != nil {
		return fmt.Errorf("query subscribers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ref string
		if sErr := rows.Scan(&ref); sErr != nil {
			return fmt.Errorf("scan sub_ref: %w", sErr)
		}
		if _, iErr := tx.Exec(`
			INSERT OR IGNORE INTO v2_informer_outbox_recipients
			  (outbox_id, sub_ref, state, attempts, updated_at)
			VALUES (?, ?, 'pending', 0, ?)`,
			outboxID, ref, nowUnix,
		); iErr != nil {
			return fmt.Errorf("insert recipient %s: %w", ref, iErr)
		}
	}
	return rows.Err()
}

// NotifyFirstPublish inserts an outbox event and captures the recipient snapshot
// atomically in its own transaction. Used for testing and the dev endpoint.
func (s *InformerService) NotifyFirstPublish(
	listingID, city, displayName, helpType, depType, urgency string, now time.Time,
) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("v2: NotifyFirstPublish: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	eventKey := "first_publish:" + listingID
	listingURL := "/v2/listing/" + listingID
	id := newID()
	nowUnix := now.Unix()
	if _, err := tx.Exec(`
		INSERT INTO v2_informer_outbox
		  (id, listing_id, city, display_name, help_type, dep_type, urgency,
		   listing_url, event_key, state, attempt, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 0, ?, ?)`,
		id, listingID, city, displayName, helpType, depType, urgency,
		listingURL, eventKey, nowUnix, nowUnix,
	); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: v2_informer_outbox.event_key") {
			return ErrInformerDuplicateEvent
		}
		return fmt.Errorf("v2: NotifyFirstPublish: insert: %w", err)
	}

	if err := snapshotRecipientsInTx(tx, id, city, nowUnix); err != nil {
		return fmt.Errorf("v2: NotifyFirstPublish: snapshot recipients: %w", err)
	}
	return tx.Commit()
}

// QueryOutboxState returns the state of the outbox entry whose listing_id matches.
func (s *InformerService) QueryOutboxState(listingID string) (string, error) {
	var state string
	err := s.db.QueryRow(
		`SELECT state FROM v2_informer_outbox WHERE listing_id = ?`, listingID,
	).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return state, err
}

// LoadPendingOutbox returns all unclaimed or expired-lease pending outbox entries.
func (s *InformerService) LoadPendingOutbox() ([]InformerOutboxEntry, error) {
	now := s.now().Unix()
	rows, err := s.db.Query(`
		SELECT id, listing_id, city, display_name, help_type, dep_type, urgency,
		       listing_url, state, attempt, claimed_by, claim_token, lease_until, created_at
		FROM v2_informer_outbox
		WHERE state='pending' AND (claim_token='' OR lease_until <= ?)`, now)
	if err != nil {
		return nil, fmt.Errorf("v2: LoadPendingOutbox: query: %w", err)
	}
	defer rows.Close()
	var out []InformerOutboxEntry
	for rows.Next() {
		var e InformerOutboxEntry
		var ts int64
		if sErr := rows.Scan(
			&e.ID, &e.ListingID, &e.City, &e.DisplayName, &e.HelpType,
			&e.DepType, &e.Urgency, &e.ListingURL, &e.State, &e.Attempt,
			&e.ClaimedBy, &e.ClaimToken, &e.LeaseUntil, &ts,
		); sErr != nil {
			return nil, fmt.Errorf("v2: LoadPendingOutbox: scan: %w", sErr)
		}
		e.CreatedAt = time.Unix(ts, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ClaimOutboxEntry atomically claims a specific pending outbox entry.
// Returns (entry, claimToken, true, nil) on success.
// Returns (nil, "", false, nil) if already claimed by another worker.
//
// Each successful claim generates a fresh random claimToken. All subsequent
// mutations (MarkRecipientDelivered, MarkOutboxDone, etc.) require this token
// for CAS validation. A zero RowsAffected on any mutation means the claim was
// lost (e.g. lease expired and another worker reclaimed it).
//
// Crash recovery: any entry with lease_until <= now is available for re-claim.
func (s *InformerService) ClaimOutboxEntry(workerID, id string, leaseSecs int64) (*InformerOutboxEntry, string, bool, error) {
	now := s.now().Unix()
	leaseUntil := now + leaseSecs

	tok, err := newClaimToken()
	if err != nil {
		return nil, "", false, fmt.Errorf("v2: ClaimOutboxEntry: gen token: %w", err)
	}

	res, err := s.db.Exec(`
		UPDATE v2_informer_outbox
		SET claimed_by=?, claim_token=?, lease_until=?, updated_at=?
		WHERE id=? AND state='pending' AND (claim_token='' OR lease_until <= ?)`,
		workerID, tok, leaseUntil, now, id, now,
	)
	if err != nil {
		return nil, "", false, fmt.Errorf("v2: ClaimOutboxEntry: claim: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return nil, "", false, err
	}

	var e InformerOutboxEntry
	var ts int64
	err = s.db.QueryRow(`
		SELECT id, listing_id, city, display_name, help_type, dep_type, urgency,
		       listing_url, state, attempt, claimed_by, claim_token, lease_until, created_at
		FROM v2_informer_outbox WHERE id=?`, id,
	).Scan(
		&e.ID, &e.ListingID, &e.City, &e.DisplayName, &e.HelpType,
		&e.DepType, &e.Urgency, &e.ListingURL, &e.State, &e.Attempt,
		&e.ClaimedBy, &e.ClaimToken, &e.LeaseUntil, &ts,
	)
	if err != nil {
		return nil, "", false, fmt.Errorf("v2: ClaimOutboxEntry: read: %w", err)
	}
	e.CreatedAt = time.Unix(ts, 0)
	return &e, tok, true, nil
}

// ClaimSpecificOutboxEntry is the old name kept for test compatibility.
// It delegates to ClaimOutboxEntry and discards the claim token from the return.
// New code must use ClaimOutboxEntry.
//
// Deprecated: use ClaimOutboxEntry.
func (s *InformerService) ClaimSpecificOutboxEntry(workerID, id string, leaseSecs int64) (*InformerOutboxEntry, bool, error) {
	e, _, ok, err := s.ClaimOutboxEntry(workerID, id, leaseSecs)
	return e, ok, err
}

// RenewOutboxClaim extends the lease for an active claim.
// Returns (true, nil) if the renewal succeeded (the caller still owns the claim).
// Returns (false, nil) if the claim was lost (lease expired and another worker reclaimed it).
// The caller MUST stop processing and not send to any further recipients if this returns false.
func (s *InformerService) RenewOutboxClaim(outboxID, claimToken string, leaseSecs int64) (bool, error) {
	now := s.now().Unix()
	leaseUntil := now + leaseSecs
	res, err := s.db.Exec(`
		UPDATE v2_informer_outbox
		SET lease_until=?, updated_at=?
		WHERE id=? AND claim_token=? AND lease_until > ?`,
		leaseUntil, now, outboxID, claimToken, now,
	)
	if err != nil {
		return false, fmt.Errorf("v2: RenewOutboxClaim: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("v2: RenewOutboxClaim: rows affected: %w", err)
	}
	return n > 0, nil
}

// ReleaseClaimByToken releases a worker's claim using its claim token.
// Only the current claimer (holding claimToken) whose lease is still active
// can release the entry. Returns (true, nil) if the release was applied,
// (false, nil) if the claim was already lost (lease expired or stolen).
func (s *InformerService) ReleaseClaimByToken(outboxID, claimToken string) (bool, error) {
	now := s.now().Unix()
	res, err := s.db.Exec(`
		UPDATE v2_informer_outbox
		SET claim_token='', lease_until=0, updated_at=?
		WHERE id=? AND claim_token=? AND lease_until > ?`,
		now, outboxID, claimToken, now,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// InitOutboxRecipients inserts recipient rows for all subRefs exactly once.
// If any recipient rows already exist for outboxID, this is a no-op (one-time init).
// This prevents new subscribers who join after the outbox event was enqueued from
// being added on subsequent worker ticks.
func (s *InformerService) InitOutboxRecipients(outboxID string, subRefs []string) error {
	if len(subRefs) == 0 {
		return nil
	}
	now := s.now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("v2: InitOutboxRecipients: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// One-time init: if recipients already exist, skip all inserts.
	var existingCount int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM v2_informer_outbox_recipients WHERE outbox_id=?`, outboxID,
	).Scan(&existingCount); err != nil {
		return fmt.Errorf("v2: InitOutboxRecipients: count: %w", err)
	}
	if existingCount > 0 {
		return tx.Commit()
	}

	for _, ref := range subRefs {
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO v2_informer_outbox_recipients
			  (outbox_id, sub_ref, state, attempts, updated_at)
			VALUES (?, ?, 'pending', 0, ?)`,
			outboxID, ref, now,
		); err != nil {
			return fmt.Errorf("v2: InitOutboxRecipients: insert: %w", err)
		}
	}
	return tx.Commit()
}

// LoadPendingOutboxRecipients returns sub_refs with state='pending'.
func (s *InformerService) LoadPendingOutboxRecipients(outboxID string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT sub_ref FROM v2_informer_outbox_recipients
		WHERE outbox_id=? AND state='pending'`, outboxID,
	)
	if err != nil {
		return nil, fmt.Errorf("v2: LoadPendingOutboxRecipients: %w", err)
	}
	defer rows.Close()
	var refs []string
	for rows.Next() {
		var r string
		if sErr := rows.Scan(&r); sErr != nil {
			return nil, sErr
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}

// MarkRecipientDelivered marks one recipient as delivered.
// claimToken is verified so that a stale worker cannot corrupt another worker's progress.
// Returns (false, nil) if the claim was lost (another worker reclaimed the outbox entry).
func (s *InformerService) MarkRecipientDelivered(outboxID, subRef, claimToken string) (bool, error) {
	now := s.now().Unix()
	// Verify claim is still valid before marking.
	res, err := s.db.Exec(`
		UPDATE v2_informer_outbox_recipients
		SET state='delivered', updated_at=?
		WHERE outbox_id=? AND sub_ref=? AND state='pending'
		  AND EXISTS (
		    SELECT 1 FROM v2_informer_outbox
		    WHERE id=? AND claim_token=? AND lease_until > ?
		  )`,
		now, outboxID, subRef, outboxID, claimToken, now,
	)
	if err != nil {
		return false, fmt.Errorf("v2: MarkRecipientDelivered: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("v2: MarkRecipientDelivered: rows: %w", err)
	}
	return n > 0, nil
}

// MarkRecipientPermanentFailed marks one recipient as permanently failed and
// atomically deletes the corresponding Informer subscription.
// Uses inline EXISTS for CAS to eliminate the SELECT+UPDATE race window.
// Returns (false, nil) if the claim was lost (n=0).
func (s *InformerService) MarkRecipientPermanentFailed(outboxID, subRef, claimToken string) (bool, error) {
	now := s.now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("v2: MarkRecipientPermanentFailed: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	res, err := tx.Exec(`
		UPDATE v2_informer_outbox_recipients
		SET state='permanent_failed', updated_at=?
		WHERE outbox_id=? AND sub_ref=? AND state='pending'
		  AND EXISTS (SELECT 1 FROM v2_informer_outbox WHERE id=? AND claim_token=? AND lease_until > ?)`,
		now, outboxID, subRef, outboxID, claimToken, now,
	)
	if err != nil {
		return false, fmt.Errorf("v2: MarkRecipientPermanentFailed: mark: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil // claim lost or already settled
	}

	if _, err := tx.Exec(
		`DELETE FROM v2_informer_subscriptions WHERE sub_ref=?`, subRef,
	); err != nil {
		return false, fmt.Errorf("v2: MarkRecipientPermanentFailed: delete sub: %w", err)
	}

	return true, tx.Commit()
}

// IncrementAndMaybeExhaust increments the attempt counter for a pending recipient
// and, if the new count reaches maxAttempts, transitions state to retry_exhausted.
// All operations are performed in a single transaction with an EXISTS claim check.
//
// Returns:
//   - exhausted=true if the state was transitioned to retry_exhausted this call.
//   - ok=true if the claim was still valid (n>0 on the increment).
//   - err for any unexpected database error.
func (s *InformerService) IncrementAndMaybeExhaust(outboxID, subRef, claimToken string, maxAttempts int) (exhausted bool, ok bool, err error) {
	now := s.now().Unix()
	tx, txErr := s.db.Begin()
	if txErr != nil {
		return false, false, fmt.Errorf("v2: IncrementAndMaybeExhaust: begin: %w", txErr)
	}
	defer tx.Rollback() //nolint:errcheck

	// Increment attempts with claim check.
	res, execErr := tx.Exec(`
		UPDATE v2_informer_outbox_recipients
		SET attempts=attempts+1, updated_at=?
		WHERE outbox_id=? AND sub_ref=? AND state='pending'
		  AND EXISTS (SELECT 1 FROM v2_informer_outbox WHERE id=? AND claim_token=? AND lease_until > ?)`,
		now, outboxID, subRef, outboxID, claimToken, now,
	)
	if execErr != nil {
		return false, false, fmt.Errorf("v2: IncrementAndMaybeExhaust: increment: %w", execErr)
	}
	n, rowsErr := res.RowsAffected()
	if rowsErr != nil {
		return false, false, fmt.Errorf("v2: IncrementAndMaybeExhaust: rows: %w", rowsErr)
	}
	if n == 0 {
		// Claim lost or recipient no longer pending.
		return false, false, nil
	}

	// Read the new attempts count within the same transaction.
	var newAttempts int
	if scanErr := tx.QueryRow(
		`SELECT attempts FROM v2_informer_outbox_recipients WHERE outbox_id=? AND sub_ref=?`,
		outboxID, subRef,
	).Scan(&newAttempts); scanErr != nil {
		return false, false, fmt.Errorf("v2: IncrementAndMaybeExhaust: read attempts: %w", scanErr)
	}

	if newAttempts >= maxAttempts {
		// Transition to retry_exhausted, again with EXISTS claim check.
		exhaustRes, exhaustErr := tx.Exec(`
			UPDATE v2_informer_outbox_recipients
			SET state='retry_exhausted', updated_at=?
			WHERE outbox_id=? AND sub_ref=?
			  AND EXISTS (SELECT 1 FROM v2_informer_outbox WHERE id=? AND claim_token=? AND lease_until > ?)`,
			now, outboxID, subRef, outboxID, claimToken, now,
		)
		if exhaustErr != nil {
			return false, false, fmt.Errorf("v2: IncrementAndMaybeExhaust: exhaust: %w", exhaustErr)
		}
		en, _ := exhaustRes.RowsAffected()
		if err := tx.Commit(); err != nil {
			return false, false, fmt.Errorf("v2: IncrementAndMaybeExhaust: commit: %w", err)
		}
		return en > 0, true, nil
	}

	if err := tx.Commit(); err != nil {
		return false, false, fmt.Errorf("v2: IncrementAndMaybeExhaust: commit: %w", err)
	}
	return false, true, nil
}

// MarkRecipientDecryptFailed marks a recipient as decrypt_failed with a CAS claim check.
// The subscription is NOT deleted.
// Returns (true, nil) if the update was applied, (false, nil) if the claim was lost.
func (s *InformerService) MarkRecipientDecryptFailed(outboxID, subRef, claimToken string) (bool, error) {
	now := s.now().Unix()
	res, err := s.db.Exec(`
		UPDATE v2_informer_outbox_recipients
		SET state='decrypt_failed', updated_at=?
		WHERE outbox_id=? AND sub_ref=? AND state='pending'
		  AND EXISTS (SELECT 1 FROM v2_informer_outbox WHERE id=? AND claim_token=? AND lease_until > ?)`,
		now, outboxID, subRef, outboxID, claimToken, now,
	)
	if err != nil {
		return false, fmt.Errorf("v2: MarkRecipientDecryptFailed: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("v2: MarkRecipientDecryptFailed: rows: %w", err)
	}
	return n > 0, nil
}

// AllRecipientsSettled reports true when no recipient row for an outbox entry
// has state='pending'. All must be delivered, permanent_failed, retry_exhausted,
// or decrypt_failed.
func (s *InformerService) AllRecipientsSettled(outboxID string) (bool, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM v2_informer_outbox_recipients
		WHERE outbox_id=? AND state='pending'`, outboxID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("v2: AllRecipientsSettled: %w", err)
	}
	return count == 0, nil
}

// HasFailedRecipients reports true when any recipient has state IN
// ('retry_exhausted', 'decrypt_failed') — these mark the outbox as failed.
func (s *InformerService) HasFailedRecipients(outboxID string) (bool, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM v2_informer_outbox_recipients
		WHERE outbox_id=? AND state IN ('retry_exhausted', 'decrypt_failed')`, outboxID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("v2: HasFailedRecipients: %w", err)
	}
	return count > 0, nil
}

// RecipientState holds the state and per-recipient attempt count for one outbox recipient.
type RecipientState struct {
	State    string
	Attempts int
}

// LoadRecipientStates returns the state and attempt count for every recipient of outboxID.
// Keyed by sub_ref. Used in tests to verify per-recipient delivery outcomes.
func (s *InformerService) LoadRecipientStates(outboxID string) (map[string]RecipientState, error) {
	rows, err := s.db.Query(
		`SELECT sub_ref, state, attempts FROM v2_informer_outbox_recipients WHERE outbox_id=?`, outboxID,
	)
	if err != nil {
		return nil, fmt.Errorf("v2: LoadRecipientStates: %w", err)
	}
	defer rows.Close()
	result := make(map[string]RecipientState)
	for rows.Next() {
		var ref, state string
		var attempts int
		if sErr := rows.Scan(&ref, &state, &attempts); sErr != nil {
			return nil, sErr
		}
		result[ref] = RecipientState{State: state, Attempts: attempts}
	}
	return result, rows.Err()
}

// LoadActiveSubscribersForCity returns all active subscription sub_refs for a city.
func (s *InformerService) LoadActiveSubscribersForCity(city string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT sub_ref FROM v2_informer_subscriptions WHERE city=? AND state='active'`, city,
	)
	if err != nil {
		return nil, fmt.Errorf("v2: LoadActiveSubscribersForCity: %w", err)
	}
	defer rows.Close()
	var refs []string
	for rows.Next() {
		var r string
		if sErr := rows.Scan(&r); sErr != nil {
			return nil, sErr
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}

// DecryptSubChatID decrypts the chat_id for a sub_ref.
func (s *InformerService) DecryptSubChatID(subRef string) (int64, error) {
	var ctHex, nonceHex, keyVersion string
	err := s.db.QueryRow(`
		SELECT chat_id_ciphertext, chat_id_nonce, key_version
		FROM v2_informer_destinations WHERE sub_ref=?`, subRef,
	).Scan(&ctHex, &nonceHex, &keyVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrInformerTokenNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("v2: DecryptSubChatID: %w", err)
	}
	return s.destCipher.DecryptChatID(ctHex, nonceHex, keyVersion, subRef)
}

// MarkOutboxDone marks an outbox entry as done, verifying claim ownership via token.
// Returns (false, nil) if the claim was lost before this call.
func (s *InformerService) MarkOutboxDone(id, claimToken string) (bool, error) {
	now := s.now().Unix()
	res, err := s.db.Exec(`
		UPDATE v2_informer_outbox
		SET state='done', claim_token='', lease_until=0, updated_at=?
		WHERE id=? AND claim_token=? AND lease_until > ?`,
		now, id, claimToken, now,
	)
	if err != nil {
		return false, fmt.Errorf("v2: MarkOutboxDone: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MarkOutboxFailed marks an outbox entry as failed, verifying claim ownership.
func (s *InformerService) MarkOutboxFailed(id, claimToken, errMsg string) (bool, error) {
	now := s.now().Unix()
	res, err := s.db.Exec(`
		UPDATE v2_informer_outbox
		SET state='failed', last_error=?, claim_token='', lease_until=0, updated_at=?
		WHERE id=? AND claim_token=? AND lease_until > ?`,
		errMsg, now, id, claimToken, now,
	)
	if err != nil {
		return false, fmt.Errorf("v2: MarkOutboxFailed: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// HasActiveSubscription reports whether chatID has an active subscription.
func (s *InformerService) HasActiveSubscription(chatID int64) (bool, string, error) {
	chatH := s.chatHMAC(chatID)
	var subRef string
	err := s.db.QueryRow(
		`SELECT ci.sub_ref FROM v2_informer_chat_index ci
		 JOIN v2_informer_subscriptions sub ON sub.sub_ref = ci.sub_ref
		 WHERE ci.chat_hmac = ? AND sub.state = 'active'`, chatH,
	).Scan(&subRef)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	var city string
	_ = s.db.QueryRow(`SELECT city FROM v2_informer_subscriptions WHERE sub_ref=?`, subRef).Scan(&city)
	return true, city, nil
}

// ── InformerWorker ────────────────────────────────────────────────────────────

// InformerWorker processes pending outbox entries and delivers notifications.
//
// Claim ownership model (§1):
//   - Each RunOnce call claims entries via ClaimOutboxEntry, which generates a
//     fresh random claimToken per claim.
//   - All mutations (MarkRecipientDelivered, MarkOutboxDone, etc.) carry the
//     claimToken and use a CAS predicate: id + claim_token + lease_until > now.
//   - Before each recipient send, the worker calls RenewOutboxClaim to extend
//     the lease. Zero RowsAffected on renewal = claim lost; worker stops.
//   - After a crash, the entry's lease_until is in the past; any worker can
//     re-claim it with a new token, and the old claimToken is invalidated.
//
// Per-recipient retry policy (§2):
//   - Each recipient has its own attempts counter.
//   - Retryable errors (429/5xx/timeout): increment recipient.attempts.
//     If attempts >= MaxRecipientAttempts → retry_exhausted (sub kept).
//   - Permanent failure (4xx): permanent_failed (sub deleted).
//   - Decrypt failure: decrypt_failed (sub kept; safe terminal).
//   - Outbox done if all settled (delivered + permanent_failed, no retry_exhausted).
//   - Outbox failed if any retry_exhausted or decrypt_failed.
//
// Crash window:
//
//	After an external Telegram send succeeds but before MarkRecipientDelivered
//	commits, a crash causes that recipient to receive a duplicate on the next claim.
//	This is the only acceptable duplicate window.
type InformerWorker struct {
	svc       *InformerService
	sender    InformerBotSender
	workerID  string
	leaseSecs int64
}

// NewInformerWorker creates an InformerWorker with a random per-instance workerID.
func NewInformerWorker(svc *InformerService, sender InformerBotSender) *InformerWorker {
	b := make([]byte, 8)
	rand.Read(b) //nolint:errcheck
	return &InformerWorker{
		svc:       svc,
		sender:    sender,
		workerID:  hex.EncodeToString(b),
		leaseSecs: defaultInformerWorkerLeaseSecs,
	}
}

// newInformerWorkerWithID creates a worker with a fixed ID — for deterministic tests.
func newInformerWorkerWithID(svc *InformerService, sender InformerBotSender, workerID string) *InformerWorker {
	return &InformerWorker{
		svc:       svc,
		sender:    sender,
		workerID:  workerID,
		leaseSecs: defaultInformerWorkerLeaseSecs,
	}
}

// RunOnce processes all currently available pending outbox entries.
func (w *InformerWorker) RunOnce(ctx context.Context) error {
	pending, err := w.svc.LoadPendingOutbox()
	if err != nil {
		return fmt.Errorf("v2: InformerWorker: load pending: %w", err)
	}
	for _, snapshot := range pending {
		if ctx.Err() != nil {
			return nil
		}
		entry, claimToken, ok, err := w.svc.ClaimOutboxEntry(w.workerID, snapshot.ID, w.leaseSecs)
		if err != nil {
			return fmt.Errorf("v2: InformerWorker: claim: %w", err)
		}
		if !ok {
			continue
		}
		if processErr := w.processEntry(ctx, entry, claimToken); processErr != nil {
			if !errors.Is(processErr, ErrInformerLostClaim) {
				slog.Error("v2: InformerWorker: process entry", "err", "[internal]")
				w.svc.ReleaseClaimByToken(entry.ID, claimToken) //nolint:errcheck
			}
		}
	}
	return nil
}

// processEntry processes one claimed outbox entry with full per-recipient logic.
//
// Recipient snapshot was captured atomically at enqueue time (EnqueueFirstPublishTx /
// NotifyFirstPublish). The worker reads the already-populated recipients table
// directly — no InitOutboxRecipients call here. An empty snapshot (no pending refs)
// means no subscribers existed at enqueue time; the entry is settled immediately.
func (w *InformerWorker) processEntry(ctx context.Context, e *InformerOutboxEntry, claimToken string) error {
	pendingRefs, err := w.svc.LoadPendingOutboxRecipients(e.ID)
	if err != nil {
		return fmt.Errorf("v2: processEntry: load pending recipients: %w", err)
	}

	text := fmt.Sprintf("New listing in %s\n%s · %s · %s\n%s",
		e.City, e.DisplayName, e.HelpType, e.Urgency, e.ListingURL)

	for _, ref := range pendingRefs {
		if ctx.Err() != nil {
			w.svc.ReleaseClaimByToken(e.ID, claimToken) //nolint:errcheck
			return ErrInformerLostClaim
		}

		// Renew claim before each send. If renewal fails, another worker owns the entry.
		renewed, renewErr := w.svc.RenewOutboxClaim(e.ID, claimToken, w.leaseSecs)
		if renewErr != nil {
			return fmt.Errorf("v2: processEntry: renew: %w", renewErr)
		}
		if !renewed {
			// Claim was taken by another worker. Stop immediately — do not send.
			return ErrInformerLostClaim
		}

		chatID, decErr := w.svc.DecryptSubChatID(ref)
		if decErr != nil {
			slog.Error("v2: InformerWorker: decrypt chat_id", "err", "[internal]")
			dfOk, dfErr := w.svc.MarkRecipientDecryptFailed(e.ID, ref, claimToken)
			if dfErr != nil {
				slog.Error("v2: InformerWorker: mark decrypt failed", "err", "[internal]")
				continue
			}
			if !dfOk {
				return ErrInformerLostClaim
			}
			continue
		}

		sendErr := w.sender.SendInformerNotification(ctx, chatID, text)
		if sendErr == nil {
			ok, dbErr := w.svc.MarkRecipientDelivered(e.ID, ref, claimToken)
			if dbErr != nil {
				slog.Error("v2: InformerWorker: mark delivered", "err", "[internal]")
			} else if !ok {
				// Claim lost between renew and mark — stop.
				return ErrInformerLostClaim
			}
			continue
		}

		slog.Error("v2: InformerWorker: send notification", "err", "[internal]")

		if errors.Is(sendErr, ErrInformerNotificationPermanent) {
			ok, dbErr := w.svc.MarkRecipientPermanentFailed(e.ID, ref, claimToken)
			if dbErr != nil {
				slog.Error("v2: InformerWorker: mark permanent failed", "err", "[internal]")
			} else if !ok {
				return ErrInformerLostClaim
			}
			continue
		}

		// Retryable error: increment per-recipient attempt counter and maybe exhaust.
		_, incOk, incErr := w.svc.IncrementAndMaybeExhaust(e.ID, ref, claimToken, MaxRecipientAttempts)
		if incErr != nil {
			slog.Error("v2: InformerWorker: increment attempt", "err", "[internal]")
			continue
		}
		if !incOk {
			return ErrInformerLostClaim
		}
		// else: recipient stays pending or is now retry_exhausted; either way continue
	}

	// All pending recipients processed. Check settled state.
	settled, err := w.svc.AllRecipientsSettled(e.ID)
	if err != nil {
		return fmt.Errorf("v2: processEntry: check settled: %w", err)
	}
	if !settled {
		// Some recipients are still pending (shouldn't happen if we processed all,
		// but guard for unsubscribed recipients that were initialized then deleted).
		w.svc.ReleaseClaimByToken(e.ID, claimToken) //nolint:errcheck
		return nil
	}

	hasFailed, err := w.svc.HasFailedRecipients(e.ID)
	if err != nil {
		return fmt.Errorf("v2: processEntry: check failed: %w", err)
	}
	if hasFailed {
		ok, err := w.svc.MarkOutboxFailed(e.ID, claimToken, "recipients exhausted or decrypt failed")
		if err != nil {
			return fmt.Errorf("v2: processEntry: mark failed: %w", err)
		}
		if !ok {
			return ErrInformerLostClaim
		}
		return nil
	}

	ok, err := w.svc.MarkOutboxDone(e.ID, claimToken)
	if err != nil {
		return fmt.Errorf("v2: processEntry: mark done: %w", err)
	}
	if !ok {
		return ErrInformerLostClaim
	}
	return nil
}
