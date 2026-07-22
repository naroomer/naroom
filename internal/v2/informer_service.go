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

// MaxOutboxAttempts is the maximum number of delivery attempts before an
// outbox entry is permanently marked failed.
const MaxOutboxAttempts = 3

// Informer sentinel errors.
var (
	ErrInformerTokenExpired              = errors.New("v2: informer token expired")
	ErrInformerTokenClaimed              = errors.New("v2: informer token already claimed")
	ErrInformerTokenNotFound             = errors.New("v2: informer token not found")
	ErrInformerNotPrivateChat            = errors.New("v2: informer requires private chat")
	ErrInformerLowBalance                = errors.New("v2: informer balance below $1000 floor")
	ErrInformerInvalidCity               = errors.New("v2: informer city not supported")
	ErrInformerDuplicateEvent            = errors.New("v2: informer outbox duplicate (already exists)")
	// ErrInformerNotificationPermanent signals that a send failure is permanent
	// and the outbox entry must be marked failed immediately without retry.
	ErrInformerNotificationPermanent     = errors.New("v2: informer notification permanent failure")
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
	}, nil
}

// newInformerSubRef returns "isub_" + 32 lowercase hex chars (16 random bytes).
func newInformerSubRef() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "isub_" + hex.EncodeToString(b), nil
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

// CreateAccess validates city, checks balanceUSD ≥ $1000, and returns a 15-min raw token.
// The wallet address itself is NOT passed here — the caller performs balance check externally.
// Only the city (and balance result) determine eligibility; the wallet is never stored.
func (s *InformerService) CreateAccess(city string, balanceUSD float64) (rawToken string, expiresAt time.Time, err error) {
	city = strings.TrimSpace(strings.ToLower(city))
	if !isInformerCity(city) {
		return "", time.Time{}, fmt.Errorf("%w: %q", ErrInformerInvalidCity, city)
	}
	if balanceUSD < InformerMinBalanceUSD {
		return "", time.Time{}, fmt.Errorf("%w: %.2f < %.2f", ErrInformerLowBalance, balanceUSD, InformerMinBalanceUSD)
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
// Returns ErrInformerTokenNotFound or ErrInformerTokenExpired on failure.
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
// - If chatID already has an active subscription for the SAME city → idempotent success.
// - If chatID already has an active subscription for a DIFFERENT city → replace atomically.
// - Token becomes "claimed" on success.
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

	// Read and validate token.
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

	// CAS: pending → claimed.
	res, updErr := tx.Exec(
		`UPDATE v2_informer_tokens SET state='claimed' WHERE id=? AND state='pending'`, tokenID,
	)
	if updErr != nil {
		return fmt.Errorf("v2: Subscribe: claim token: %w", updErr)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrInformerTokenClaimed
	}

	// Look up existing subscription for this chatID via chat_hmac index.
	chatH := s.chatHMAC(chatID)
	var existingSubRef string
	existingErr := tx.QueryRow(
		`SELECT sub_ref FROM v2_informer_chat_index WHERE chat_hmac = ?`, chatH,
	).Scan(&existingSubRef)

	if existingErr == nil {
		// Existing subscription found.
		var existingCity string
		_ = tx.QueryRow(
			`SELECT city FROM v2_informer_subscriptions WHERE sub_ref=?`, existingSubRef,
		).Scan(&existingCity)
		if existingCity == tokenCity {
			// Same city → idempotent.
			return tx.Commit()
		}
		// Different city: delete old subscription (cascade removes destination + chat_index).
		if _, delErr := tx.Exec(
			`DELETE FROM v2_informer_subscriptions WHERE sub_ref=?`, existingSubRef,
		); delErr != nil {
			return fmt.Errorf("v2: Subscribe: delete old sub: %w", delErr)
		}
	}

	// Create new subscription.
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

	// Encrypt and store chat_id.
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

	// Insert chat_hmac index (unique per chat — replaces old if cascade deleted it).
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
		return nil // already unsubscribed
	}
	if err != nil {
		return fmt.Errorf("v2: Unsubscribe: read index: %w", err)
	}
	// Delete subscription — cascade removes destination and chat_index.
	if _, delErr := s.db.Exec(
		`DELETE FROM v2_informer_subscriptions WHERE sub_ref=?`, subRef,
	); delErr != nil {
		return fmt.Errorf("v2: Unsubscribe: delete: %w", delErr)
	}
	return nil
}

// EnqueueFirstPublishTx implements InformerTxEnqueuer. It inserts the outbox
// event inside the caller's open transaction so that the outbox write and the
// listing INSERT are committed atomically. A failure here rolls back the entire
// listing transaction — first publish is not committed without an outbox entry.
//
// Duplicate event_key (impossible in normal flow but handled defensively)
// returns ErrInformerDuplicateEvent.
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
	return nil
}

// IncrementOutboxAttempt bumps the attempt counter on a pending outbox entry,
// leaving it in the 'pending' state for the next worker run.
func (s *InformerService) IncrementOutboxAttempt(id string) error {
	now := s.now().Unix()
	_, err := s.db.Exec(
		`UPDATE v2_informer_outbox SET attempt=attempt+1, updated_at=? WHERE id=?`, now, id,
	)
	return err
}

// QueryOutboxState returns the state column of the outbox entry whose
// listing_id matches the given value. Returns ("", nil) if none found.
// Intended for tests and diagnostics; do not use in hot paths.
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

// NotifyFirstPublish inserts an outbox event in its own transaction.
// Duplicate event_key (same listing published twice) → ErrInformerDuplicateEvent.
// This method is retained for direct testing and the dev fake-first-publish
// endpoint. Production first-publish uses EnqueueFirstPublishTx instead.
func (s *InformerService) NotifyFirstPublish(
	listingID, city, displayName, helpType, depType, urgency string, now time.Time,
) error {
	eventKey := "first_publish:" + listingID
	listingURL := "/v2/listing/" + listingID
	id := newID()
	nowUnix := now.Unix()
	_, err := s.db.Exec(`
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
		return fmt.Errorf("v2: NotifyFirstPublish: insert: %w", err)
	}
	return nil
}

// LoadPendingOutbox returns all pending outbox entries.
func (s *InformerService) LoadPendingOutbox() ([]InformerOutboxEntry, error) {
	rows, err := s.db.Query(`
		SELECT id, listing_id, city, display_name, help_type, dep_type, urgency,
		       listing_url, state, attempt, created_at
		FROM v2_informer_outbox WHERE state='pending'`)
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
			&e.DepType, &e.Urgency, &e.ListingURL, &e.State, &e.Attempt, &ts,
		); sErr != nil {
			return nil, fmt.Errorf("v2: LoadPendingOutbox: scan: %w", sErr)
		}
		e.CreatedAt = time.Unix(ts, 0)
		out = append(out, e)
	}
	return out, rows.Err()
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

// MarkOutboxDone marks an outbox entry as done.
func (s *InformerService) MarkOutboxDone(id string) error {
	now := s.now().Unix()
	_, err := s.db.Exec(
		`UPDATE v2_informer_outbox SET state='done', updated_at=? WHERE id=?`, now, id,
	)
	return err
}

// MarkOutboxFailed marks an outbox entry as failed with an error message.
func (s *InformerService) MarkOutboxFailed(id, errMsg string) error {
	now := s.now().Unix()
	_, err := s.db.Exec(`
		UPDATE v2_informer_outbox
		SET state='failed', last_error=?, attempt=attempt+1, updated_at=?
		WHERE id=?`, errMsg, now, id,
	)
	return err
}

// HasActiveSubscription reports whether chatID has an active subscription.
// Used for testing only.
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
	// Read city
	var city string
	_ = s.db.QueryRow(`SELECT city FROM v2_informer_subscriptions WHERE sub_ref=?`, subRef).Scan(&city)
	return true, city, nil
}

// ── InformerWorker ────────────────────────────────────────────────────────────

// InformerWorker processes pending outbox entries and delivers notifications.
type InformerWorker struct {
	svc    *InformerService
	sender InformerBotSender
}

// NewInformerWorker creates an InformerWorker.
func NewInformerWorker(svc *InformerService, sender InformerBotSender) *InformerWorker {
	return &InformerWorker{svc: svc, sender: sender}
}

// RunOnce processes all pending outbox entries. Infrastructure errors (DB
// failures when loading subscribers) are logged and leave the entry pending
// for the next run. Delivery state transitions (retryable, permanent, done)
// are managed entirely within processEntry.
func (w *InformerWorker) RunOnce(ctx context.Context) error {
	entries, err := w.svc.LoadPendingOutbox()
	if err != nil {
		return fmt.Errorf("v2: InformerWorker: load outbox: %w", err)
	}
	for _, e := range entries {
		if processErr := w.processEntry(ctx, e); processErr != nil {
			// Infrastructure error (e.g., DB failure loading subscribers).
			// Leave the entry pending so it is retried on the next run.
			slog.Error("v2: InformerWorker: process entry", "id", e.ID, "err", "[internal]")
		}
	}
	return nil
}

// processEntry delivers notifications for one outbox entry and transitions its state:
//   - All deliveries succeeded → mark done.
//   - Any send returns ErrInformerNotificationPermanent → mark failed immediately.
//   - Any send returns a retryable error:
//     - attempt+1 < MaxOutboxAttempts → increment attempt counter (stay pending).
//     - attempt+1 >= MaxOutboxAttempts → mark failed (max retries exhausted).
//
// Infrastructure errors (loading subscribers, decrypting chat_id) are returned
// as errors; the caller leaves the entry pending for the next run.
func (w *InformerWorker) processEntry(ctx context.Context, e InformerOutboxEntry) error {
	subRefs, err := w.svc.LoadActiveSubscribersForCity(e.City)
	if err != nil {
		return err
	}
	text := fmt.Sprintf("New listing in %s\n%s · %s · %s\n%s",
		e.City, e.DisplayName, e.HelpType, e.Urgency, e.ListingURL)

	var deliveryFailed bool
	var permanentFailure bool

	for _, ref := range subRefs {
		chatID, decErr := w.svc.DecryptSubChatID(ref)
		if decErr != nil {
			slog.Error("v2: InformerWorker: decrypt chat_id", "err", "[internal]")
			deliveryFailed = true
			continue
		}
		sendErr := w.sender.SendInformerNotification(ctx, chatID, text)
		if sendErr == nil {
			continue
		}
		slog.Error("v2: InformerWorker: send notification", "err", "[internal]")
		deliveryFailed = true
		if errors.Is(sendErr, ErrInformerNotificationPermanent) {
			permanentFailure = true
		}
	}

	if !deliveryFailed {
		// All deliveries succeeded (or no subscribers — event consumed).
		return w.svc.MarkOutboxDone(e.ID)
	}

	// At least one delivery failed.
	if permanentFailure || e.Attempt+1 >= MaxOutboxAttempts {
		return w.svc.MarkOutboxFailed(e.ID, "permanent or max attempts exhausted")
	}
	// Retryable: leave pending with incremented attempt counter.
	return w.svc.IncrementOutboxAttempt(e.ID)
}
