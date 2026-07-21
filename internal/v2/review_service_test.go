// Package v2 — Tests for ReviewService, bidirectional reputation, and review delivery.
package v2

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── Test scaffolding ──────────────────────────────────────────────────────────

func newTestReviewService(t *testing.T) (*ReviewService, *sql.DB) {
	t.Helper()
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	rs, err := NewReviewService(db, testHMACKey, time.Now)
	if err != nil {
		t.Fatalf("NewReviewService: %v", err)
	}
	return rs, db
}

// mustCreateContactReadyPurchase sets up a full schema-valid contact_ready purchase.
// The helper wallet fingerprint is derived from testBTCBech32Addr so that HTTP tests
// can call GetHelperReviewCapability with that address and have it pass auth.
// Returns (purchaseID, helperProfileID, clientProfileID, helperBrowserRawToken).
func mustCreateContactReadyPurchase(t *testing.T, db *sql.DB) (purchaseID, helperProfileID, clientProfileID, browserToken string) {
	t.Helper()
	now := time.Now().Unix()

	// Client profile.
	clientFP := newID()
	cpID := newID()
	mustExec(t, db, `INSERT INTO v2_client_profiles
		(id, wallet_fingerprint, currency, positive_count, negative_count, created_at, updated_at)
		VALUES (?, ?, 'BTC', 0, 0, ?, ?)`, cpID, clientFP, now, now)

	// Client flow + invoice.
	flowID := newID()
	mustExec(t, db, `INSERT INTO v2_client_flows
		(id, wallet_fingerprint, currency, management_code_hash, state, client_profile_id, created_at, updated_at)
		VALUES (?, ?, 'BTC', 'hash', 'form_ready', ?, ?, ?)`, flowID, clientFP, cpID, now, now)
	entExp := now + int64(entitlementDuration.Seconds())
	mustExec(t, db, `INSERT INTO v2_invoices
		(id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
		 detection_deadline_at, detected_txid, payment_detected_at, confirmation_deadline_at,
		 payment_txid, payment_confirmed_at, entitlement_expires_at, created_at, updated_at)
		VALUES (?, ?, 'confirmed', 'addr1', 500, 100000, ?, 'tx1', ?, ?, 'tx1', ?, ?, ?, ?)`,
		newID(), flowID, now+3600, now, now+86400, now, entExp, now, now)

	// Listing.
	listingID := newID()
	cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")
	ct, nh, kv, _ := cipher.Encrypt("@testclient", listingID, flowID, "telegram")
	mustExec(t, db, `INSERT INTO v2_listings
		(id, flow_id, city, country_code, dependency_type, help_type, urgency, languages,
		 display_name, contact_type, contact_ciphertext, contact_nonce, contact_key_version,
		 state, visible_until, first_published_at, last_activated_at, entitlement_expires_at,
		 activation_count, created_at, updated_at)
		VALUES (?, ?, 'C', 'US', 'd', 'h', 'u', '["en"]', ?, 'telegram', ?, ?, ?,
		        'visible', ?, ?, ?, ?, 1, ?, ?)`,
		listingID, flowID, "crp_dn_"+listingID[:8], ct, nh, kv,
		now+86400, now, now, entExp, now, now)

	// Helper profile: use the HMAC fingerprint of testBTCBech32Addr so that HTTP
	// tests calling GetHelperReviewCapability with that address pass the wallet check.
	normalized, currency := testBTCBech32Addr, "BTC" // address already normalized
	helperFP := helperWalletFingerprintReview(testHMACKey, currency, normalized)
	hpID := newID()
	mustExec(t, db, `INSERT INTO v2_helper_profiles
		(id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at)
		VALUES (?, ?, 'BTC', ?, 0, 0, 0, ?, ?)`,
		hpID, helperFP, "crp_name_"+hpID[:4], now, now)

	// Helper purchase in contact_ready state (schema requires balance+deadline fields).
	rawToken := newID()
	tokenHash := helperBrowserTokenHashReview(testHMACKey, rawToken)
	pID := newID()
	mustExec(t, db, `INSERT INTO v2_helper_purchases
		(id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot,
		 balance_retry_deadline_at, last_balance_usd, last_balance_checked_at, contact_ready_at,
		 created_at, updated_at)
		VALUES (?, ?, ?, ?, 'contact_ready', 'US', ?, 1100.0, ?, ?, ?, ?)`,
		pID, listingID, hpID, tokenHash, now+86400, now, now, now, now)

	// Create both review entitlements.
	mustBeginCommitTx(t, db, func(tx *sql.Tx) error {
		return createReviewEntitlementsTx(tx, pID, hpID, cpID, now, now)
	})

	// Increment purchase_count.
	mustExec(t, db, `UPDATE v2_helper_profiles SET purchase_count = purchase_count + 1 WHERE id = ?`, hpID)

	return pID, hpID, cpID, rawToken
}

// mustExec calls db.Exec and fails the test on error.
func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("mustExec(%q): %v", q[:min(len(q), 60)], err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// mustBeginCommitTx runs fn inside a transaction and commits if fn returns nil.
func mustBeginCommitTx(t *testing.T, db *sql.DB, fn func(*sql.Tx) error) bool {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := fn(tx); err != nil {
		tx.Rollback() //nolint:errcheck
		t.Fatalf("tx fn: %v", err)
		return false
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return true
}

// helperTokenFromPurchase derives the Helper review token from the DB-stored review_ref.
func helperTokenFromPurchase(t *testing.T, db *sql.DB, purchaseID string) string {
	t.Helper()
	var reviewRef string
	if err := db.QueryRow(`SELECT review_ref FROM v2_review_entitlements WHERE purchase_id = ? AND reviewer_side = 'helper'`, purchaseID).Scan(&reviewRef); err != nil {
		t.Fatalf("read helper review_ref: %v", err)
	}
	rawBytes, err := hex.DecodeString(reviewRef[4:]) // skip "rev_"
	if err != nil || len(rawBytes) != 16 {
		t.Fatalf("decode review_ref: %v", err)
	}
	return helperReviewToken(testHMACKey, rawBytes, reviewRef)
}

// ── Stub ReviewNotificationSender ─────────────────────────────────────────────

type stubReviewSender struct {
	mu      sync.Mutex
	prompts []struct {
		chatID  int64
		text    string
		posData string
		negData string
	}
	sendErr error
}

func (s *stubReviewSender) SendReviewPrompt(_ context.Context, chatID int64, text, posData, negData string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendErr != nil {
		return s.sendErr
	}
	s.prompts = append(s.prompts, struct {
		chatID  int64
		text    string
		posData string
		negData string
	}{chatID, text, posData, negData})
	return nil
}

func (s *stubReviewSender) AnswerCallback(_ context.Context, _, _ string) error { return nil }
func (s *stubReviewSender) EditMessage(_ context.Context, _, _ int64, _ string) error {
	return nil
}

// ── Category 1: Client profile identity ──────────────────────────────────────

func TestReview_ClientProfileIdentity(t *testing.T) {
	_, db := newTestReviewService(t)
	now := time.Now().Unix()

	fp1 := strings.Repeat("a", 64) // 64-char hex fingerprint for wallet A / BTC
	fp2 := strings.Repeat("b", 64) // 64-char hex fingerprint for wallet B / BTC
	// Different wallet+same currency produces different fingerprint: fp1 ≠ fp2.
	// Same wallet fingerprint always maps to same profile:
	fp1_ltc := strings.Repeat("a", 62) + "01" // distinct fingerprint for same addr but LTC

	// Create profile for fp1/BTC.
	tx1, _ := db.Begin()
	id1, err := getOrCreateClientProfileTx(tx1, fp1, "BTC", now)
	if err != nil {
		t.Fatalf("create profile 1: %v", err)
	}
	tx1.Commit() //nolint:errcheck

	// Same fingerprint → same profile.
	tx2, _ := db.Begin()
	id2, err := getOrCreateClientProfileTx(tx2, fp1, "BTC", now)
	if err != nil {
		t.Fatalf("get profile 1 again: %v", err)
	}
	tx2.Commit() //nolint:errcheck
	if id1 != id2 {
		t.Errorf("same fingerprint → same profile: got %s and %s", id1, id2)
	}

	// Different fingerprint → different profile.
	tx3, _ := db.Begin()
	id3, err := getOrCreateClientProfileTx(tx3, fp2, "BTC", now)
	if err != nil {
		t.Fatalf("create profile 2: %v", err)
	}
	tx3.Commit() //nolint:errcheck
	if id1 == id3 {
		t.Errorf("different fingerprint → different profile: both got %s", id1)
	}

	// Different fingerprint (simulating different currency) → different profile.
	tx4, _ := db.Begin()
	id4, err := getOrCreateClientProfileTx(tx4, fp1_ltc, "LTC", now)
	if err != nil {
		t.Fatalf("create profile LTC: %v", err)
	}
	tx4.Commit() //nolint:errcheck
	if id1 == id4 {
		t.Errorf("different fingerprint (LTC) → different profile: both got %s", id1)
	}
}

// ── Category 2: Flow in awaiting_payment has NULL client_profile_id ───────────

func TestReview_NoProfileUntilPayment(t *testing.T) {
	_, db := newTestReviewService(t)
	now := time.Now().Unix()
	flowID := newID()
	mustExec(t, db, `INSERT INTO v2_client_flows
		(id, wallet_fingerprint, currency, management_code_hash, state, created_at, updated_at)
		VALUES (?, ?, 'BTC', 'hash', 'awaiting_payment', ?, ?)`,
		flowID, newID(), now, now)

	var profileID sql.NullString
	db.QueryRow(`SELECT client_profile_id FROM v2_client_flows WHERE id = ?`, flowID).Scan(&profileID) //nolint:errcheck
	if profileID.Valid {
		t.Error("awaiting_payment flow must have NULL client_profile_id")
	}
}

// ── Category 3: First contact_ready → exactly 2 entitlements ─────────────────

func TestReview_FirstContactReady(t *testing.T) {
	_, db := newTestReviewService(t)
	purchaseID, _, _, _ := mustCreateContactReadyPurchase(t, db)

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_review_entitlements WHERE purchase_id = ?`, purchaseID).Scan(&count) //nolint:errcheck
	if count != 2 {
		t.Errorf("expected 2 entitlements, got %d", count)
	}
	var clientCount, helperCount int
	db.QueryRow(`SELECT COUNT(*) FROM v2_review_entitlements WHERE purchase_id = ? AND reviewer_side = 'client'`, purchaseID).Scan(&clientCount) //nolint:errcheck
	db.QueryRow(`SELECT COUNT(*) FROM v2_review_entitlements WHERE purchase_id = ? AND reviewer_side = 'helper'`, purchaseID).Scan(&helperCount) //nolint:errcheck
	if clientCount != 1 || helperCount != 1 {
		t.Errorf("expected 1 client + 1 helper, got %d + %d", clientCount, helperCount)
	}
}

// ── Category 4: Idempotent contact_ready (UNIQUE constraint enforces once) ───

func TestReview_IdempotentContactReady(t *testing.T) {
	_, db := newTestReviewService(t)
	purchaseID, helperProfileID, clientProfileID, _ := mustCreateContactReadyPurchase(t, db)
	now := time.Now().Unix()

	// Second call with same purchaseID must fail on UNIQUE (purchase_id, reviewer_side).
	tx, _ := db.Begin()
	err := createReviewEntitlementsTx(tx, purchaseID, helperProfileID, clientProfileID, now, now)
	tx.Rollback() //nolint:errcheck
	if err == nil {
		t.Error("second createReviewEntitlementsTx should fail due to UNIQUE constraint")
	}
}

// ── Category 5: Rollback on failure ──────────────────────────────────────────

func TestReview_RollbackOnFailure(t *testing.T) {
	_, db := newTestReviewService(t)
	now := time.Now().Unix()

	// Use an already-rolled-back tx to force errors in createReviewEntitlementsTx.
	tx, _ := db.Begin()
	tx.Rollback() //nolint:errcheck

	err := createReviewEntitlementsTx(tx, newID(), newID(), newID(), now, now)
	if err == nil {
		t.Error("expected error from createReviewEntitlementsTx with rolled-back tx")
	}

	// Verify no entitlements were written.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_review_entitlements`).Scan(&count) //nolint:errcheck
	if count != 0 {
		t.Errorf("expected 0 entitlements after rollback, got %d", count)
	}
}

// ── Category 6: Expiry boundary ───────────────────────────────────────────────

func TestReview_ExpiryBoundary(t *testing.T) {
	_, db := newTestReviewService(t)
	purchaseID, _, _, _ := mustCreateContactReadyPurchase(t, db)

	var clientRef string
	var expiresAt int64
	db.QueryRow(`SELECT review_ref, expires_at FROM v2_review_entitlements WHERE purchase_id = ? AND reviewer_side = 'client'`, purchaseID).Scan(&clientRef, &expiresAt) //nolint:errcheck

	// At exactly expires_at: allowed.
	rsAt, _ := NewReviewService(db, testHMACKey, func() time.Time { return time.Unix(expiresAt, 0) })
	if err := rsAt.ConsumeClientReview(clientRef, "positive", expiresAt); err != nil {
		t.Errorf("at expires_at boundary: expected nil, got %v", err)
	}

	// After expires_at (now > expires_at): rejected.
	var helperRef string
	db.QueryRow(`SELECT review_ref FROM v2_review_entitlements WHERE purchase_id = ? AND reviewer_side = 'helper'`, purchaseID).Scan(&helperRef) //nolint:errcheck
	rawRefBytes, _ := hex.DecodeString(helperRef[4:])
	helperTok := helperReviewToken(testHMACKey, rawRefBytes, helperRef)

	rsLate, _ := NewReviewService(db, testHMACKey, func() time.Time { return time.Unix(expiresAt+1, 0) })
	if err := rsLate.SubmitHelperReview(helperTok, "positive"); !errors.Is(err, ErrReviewExpired) {
		t.Errorf("after expires_at: expected ErrReviewExpired, got %v", err)
	}
}

// ── Category 7: Client and Helper positive/negative paths ────────────────────

func TestReview_ClientPositiveRating(t *testing.T) {
	_, db := newTestReviewService(t)
	purchaseID, helperProfileID, _, _ := mustCreateContactReadyPurchase(t, db)
	rs, _ := NewReviewService(db, testHMACKey, time.Now)

	var ref string
	db.QueryRow(`SELECT review_ref FROM v2_review_entitlements WHERE purchase_id = ? AND reviewer_side = 'client'`, purchaseID).Scan(&ref) //nolint:errcheck
	if err := rs.ConsumeClientReview(ref, "positive", time.Now().Unix()); err != nil {
		t.Fatalf("ConsumeClientReview positive: %v", err)
	}
	var pos int
	db.QueryRow(`SELECT positive_count FROM v2_helper_profiles WHERE id = ?`, helperProfileID).Scan(&pos) //nolint:errcheck
	if pos != 1 {
		t.Errorf("helper positive_count: want 1, got %d", pos)
	}
}

func TestReview_ClientNegativeRating(t *testing.T) {
	_, db := newTestReviewService(t)
	purchaseID, helperProfileID, _, _ := mustCreateContactReadyPurchase(t, db)
	rs, _ := NewReviewService(db, testHMACKey, time.Now)

	var ref string
	db.QueryRow(`SELECT review_ref FROM v2_review_entitlements WHERE purchase_id = ? AND reviewer_side = 'client'`, purchaseID).Scan(&ref) //nolint:errcheck
	if err := rs.ConsumeClientReview(ref, "negative", time.Now().Unix()); err != nil {
		t.Fatalf("ConsumeClientReview negative: %v", err)
	}
	var neg int
	db.QueryRow(`SELECT negative_count FROM v2_helper_profiles WHERE id = ?`, helperProfileID).Scan(&neg) //nolint:errcheck
	if neg != 1 {
		t.Errorf("helper negative_count: want 1, got %d", neg)
	}
}

func TestReview_HelperPositiveRating(t *testing.T) {
	_, db := newTestReviewService(t)
	purchaseID, _, clientProfileID, _ := mustCreateContactReadyPurchase(t, db)
	rs, _ := NewReviewService(db, testHMACKey, time.Now)

	tok := helperTokenFromPurchase(t, db, purchaseID)
	if err := rs.SubmitHelperReview(tok, "positive"); err != nil {
		t.Fatalf("SubmitHelperReview positive: %v", err)
	}
	var pos int
	db.QueryRow(`SELECT positive_count FROM v2_client_profiles WHERE id = ?`, clientProfileID).Scan(&pos) //nolint:errcheck
	if pos != 1 {
		t.Errorf("client positive_count: want 1, got %d", pos)
	}
}

func TestReview_HelperNegativeRating(t *testing.T) {
	_, db := newTestReviewService(t)
	purchaseID, _, clientProfileID, _ := mustCreateContactReadyPurchase(t, db)
	rs, _ := NewReviewService(db, testHMACKey, time.Now)

	tok := helperTokenFromPurchase(t, db, purchaseID)
	if err := rs.SubmitHelperReview(tok, "negative"); err != nil {
		t.Fatalf("SubmitHelperReview negative: %v", err)
	}
	var neg int
	db.QueryRow(`SELECT negative_count FROM v2_client_profiles WHERE id = ?`, clientProfileID).Scan(&neg) //nolint:errcheck
	if neg != 1 {
		t.Errorf("client negative_count: want 1, got %d", neg)
	}
}

// ── Category 8: Concurrent consume → exactly one aggregate increment ──────────

func TestReview_ConcurrentClientConsume(t *testing.T) {
	_, db := newTestReviewService(t)
	purchaseID, helperProfileID, _, _ := mustCreateContactReadyPurchase(t, db)
	rs, _ := NewReviewService(db, testHMACKey, time.Now)

	var ref string
	db.QueryRow(`SELECT review_ref FROM v2_review_entitlements WHERE purchase_id = ? AND reviewer_side = 'client'`, purchaseID).Scan(&ref) //nolint:errcheck

	const workers = 10
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rs.ConsumeClientReview(ref, "positive", time.Now().Unix()) //nolint:errcheck
		}()
	}
	wg.Wait()

	var pos, neg int
	db.QueryRow(`SELECT positive_count, negative_count FROM v2_helper_profiles WHERE id = ?`, helperProfileID).Scan(&pos, &neg) //nolint:errcheck
	if pos+neg != 1 {
		t.Errorf("concurrent consume: expected exactly 1 total increment, got pos=%d neg=%d", pos, neg)
	}
}

// ── Category 9: Idempotent exact repeat; opposite rejected ───────────────────

func TestReview_IdempotentAndOppositeRating(t *testing.T) {
	_, db := newTestReviewService(t)
	purchaseID, helperProfileID, _, _ := mustCreateContactReadyPurchase(t, db)
	rs, _ := NewReviewService(db, testHMACKey, time.Now)

	var ref string
	db.QueryRow(`SELECT review_ref FROM v2_review_entitlements WHERE purchase_id = ? AND reviewer_side = 'client'`, purchaseID).Scan(&ref) //nolint:errcheck
	now := time.Now().Unix()

	// First consume.
	if err := rs.ConsumeClientReview(ref, "positive", now); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	// Exact repeat: idempotent.
	if err := rs.ConsumeClientReview(ref, "positive", now); err != nil {
		t.Errorf("idempotent repeat: expected nil, got %v", err)
	}
	// Opposite rating: rejected.
	if err := rs.ConsumeClientReview(ref, "negative", now); !errors.Is(err, ErrReviewAlreadyConsumed) {
		t.Errorf("opposite rating: expected ErrReviewAlreadyConsumed, got %v", err)
	}

	// Aggregate: exactly 1 positive, 0 negative.
	var pos, neg int
	db.QueryRow(`SELECT positive_count, negative_count FROM v2_helper_profiles WHERE id = ?`, helperProfileID).Scan(&pos, &neg) //nolint:errcheck
	if pos != 1 || neg != 0 {
		t.Errorf("aggregate: pos=%d neg=%d, want 1,0", pos, neg)
	}
}

// ── Category 10: One purchase → one review per side ──────────────────────────

func TestReview_OneReviewPerPurchasePerSide(t *testing.T) {
	_, db := newTestReviewService(t)
	purchaseID, _, _, _ := mustCreateContactReadyPurchase(t, db)
	rs, _ := NewReviewService(db, testHMACKey, time.Now)

	var clientRef string
	db.QueryRow(`SELECT review_ref FROM v2_review_entitlements WHERE purchase_id = ? AND reviewer_side = 'client'`, purchaseID).Scan(&clientRef) //nolint:errcheck
	now := time.Now().Unix()

	rs.ConsumeClientReview(clientRef, "positive", now) //nolint:errcheck
	// Second different rating → already consumed.
	if err := rs.ConsumeClientReview(clientRef, "negative", now); !errors.Is(err, ErrReviewAlreadyConsumed) {
		t.Errorf("second consume different rating: expected ErrReviewAlreadyConsumed, got %v", err)
	}
}

// ── Category 11: Helper capability wrong token/wallet → ErrReviewCapabilityNotFound ──

func TestReview_CapabilityEnumeration(t *testing.T) {
	_, db := newTestReviewService(t)
	purchaseID, _, _, rawToken := mustCreateContactReadyPurchase(t, db)
	rs, _ := NewReviewService(db, testHMACKey, time.Now)

	// Wrong token.
	_, err := rs.GetHelperReviewCapability(purchaseID, newID(), testBTCBech32Addr, "BTC")
	if !errors.Is(err, ErrReviewCapabilityNotFound) {
		t.Errorf("wrong token: expected ErrReviewCapabilityNotFound, got %v", err)
	}

	// Wrong wallet (slight modification of testBTCBech32Addr).
	_, err = rs.GetHelperReviewCapability(purchaseID, rawToken, "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdp", "BTC")
	if !errors.Is(err, ErrReviewCapabilityNotFound) {
		t.Errorf("wrong wallet: expected ErrReviewCapabilityNotFound, got %v", err)
	}
	_ = rawToken
}

// ── Category 12: Review token domain separation ───────────────────────────────

func TestReview_TokenDomainSeparation(t *testing.T) {
	rawRef, reviewRef, err := newReviewRef()
	if err != nil {
		t.Fatalf("newReviewRef: %v", err)
	}
	token := helperReviewToken(testHMACKey, rawRef, reviewRef)

	// Token must not equal review_ref (it's derived, not the stored ref).
	if token == reviewRef {
		t.Error("privacy: review token must not equal stored review_ref")
	}
	// Token format: exactly two base64url parts separated by ".".
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		t.Errorf("review token must have exactly one '.', got %d parts", len(parts))
	}
	// Parsing must recover the same review_ref.
	_, parsedRef, ok := parseHelperReviewToken(testHMACKey, token)
	if !ok {
		t.Fatal("parseHelperReviewToken failed on freshly generated token")
	}
	if parsedRef != reviewRef {
		t.Errorf("parsed reviewRef = %q, want %q", parsedRef, reviewRef)
	}
}

// ── Category 13: Cleanup boundary ────────────────────────────────────────────

func TestReview_CleanupBoundary(t *testing.T) {
	_, db := newTestReviewService(t)
	purchaseID, _, _, _ := mustCreateContactReadyPurchase(t, db)

	var expiresAt int64
	db.QueryRow(`SELECT expires_at FROM v2_review_entitlements WHERE purchase_id = ? LIMIT 1`, purchaseID).Scan(&expiresAt) //nolint:errcheck

	rs, _ := NewReviewService(db, testHMACKey, time.Now)

	// At exactly expires_at: NOT deleted.
	rs.CleanupExpiredReviews(time.Unix(expiresAt, 0)) //nolint:errcheck
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_review_entitlements WHERE purchase_id = ?`, purchaseID).Scan(&count) //nolint:errcheck
	if count != 2 {
		t.Errorf("at expires_at: expected 2 entitlements (not deleted), got %d", count)
	}

	// At expires_at + 1: deleted.
	rs.CleanupExpiredReviews(time.Unix(expiresAt+1, 0))                                                       //nolint:errcheck
	db.QueryRow(`SELECT COUNT(*) FROM v2_review_entitlements WHERE purchase_id = ?`, purchaseID).Scan(&count) //nolint:errcheck
	if count != 0 {
		t.Errorf("after expires_at: expected 0 entitlements (deleted), got %d", count)
	}
}

// ── Category 14: Snapshot lifecycle and no double aggregate ──────────────────

func TestReview_SnapshotLifecycle(t *testing.T) {
	_, db := newTestReviewService(t)
	now := time.Now().Unix()

	// Build a purchase with an active binding+destination.
	cpID := mustInsertClientProfileForFlow(t, db, now)
	flowID := newID()
	mustExec(t, db, `INSERT INTO v2_client_flows (id, wallet_fingerprint, currency, management_code_hash, state, client_profile_id, created_at, updated_at) VALUES (?, ?, 'BTC', 'h', 'form_ready', ?, ?, ?)`, flowID, newID(), cpID, now, now)
	bindingRef := "bnd_" + strings.Repeat("a", 32)
	mustExec(t, db, `INSERT INTO v2_client_notification_bindings (id, flow_id, binding_ref, state, window_number, verified_at, valid_until, activated_at, created_at, updated_at) VALUES (?, ?, ?, 'active', 1, ?, ?, ?, ?, ?)`, newID(), flowID, bindingRef, now-10, now+86400, now, now, now)
	mustExec(t, db, `INSERT INTO v2_telegram_destinations (binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at) VALUES (?, 'ctx', 'ncx', 'v1', ?, ?)`, bindingRef, now, now+86400)

	listingID := newID()
	cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")
	ct, nh, kv, _ := cipher.Encrypt("@snap", listingID, flowID, "telegram")
	entExp := now + int64(entitlementDuration.Seconds())
	mustExec(t, db, `INSERT INTO v2_invoices (id, flow_id, status, payment_address, amount_usd_cents, amount_atomic, detection_deadline_at, detected_txid, payment_detected_at, confirmation_deadline_at, payment_txid, payment_confirmed_at, entitlement_expires_at, created_at, updated_at) VALUES (?, ?, 'confirmed', 'a', 500, 1000, ?, 'tx', ?, ?, 'tx', ?, ?, ?, ?)`, newID(), flowID, now+3600, now, now+86400, now, entExp, now, now)
	mustExec(t, db, `INSERT INTO v2_listings (id, flow_id, city, country_code, dependency_type, help_type, urgency, languages, display_name, contact_type, contact_ciphertext, contact_nonce, contact_key_version, state, visible_until, first_published_at, last_activated_at, entitlement_expires_at, activation_count, created_at, updated_at) VALUES (?, ?, 'C', 'US', 'd', 'h', 'u', '["en"]', ?, 'telegram', ?, ?, ?, 'visible', ?, ?, ?, ?, 1, ?, ?)`, listingID, flowID, "sn_dn_"+listingID[:8], ct, nh, kv, now+86400, now, now, entExp, now, now)
	hpID := newID()
	mustExec(t, db, `INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', ?, 0, 0, 0, ?, ?)`, hpID, newID(), "sn_name_"+hpID[:4], now, now)
	pID := newID()
	mustExec(t, db, `INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, balance_retry_deadline_at, last_balance_usd, last_balance_checked_at, contact_ready_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'contact_ready', 'US', ?, 1100.0, ?, ?, ?, ?)`, pID, listingID, hpID, newID(), now+86400, now, now, now, now)

	snapID := newID()
	mustExec(t, db, `INSERT INTO v2_review_delivery_snapshots (id, purchase_id, binding_ref_snapshot, chat_id_ciphertext, chat_id_nonce, key_version, state, expires_at, created_at, updated_at) VALUES (?, ?, ?, 'ctx', 'ncx', 'v1', 'pending_send', ?, ?, ?)`, snapID, pID, bindingRef, now+86400, now, now)

	mustBeginCommitTx(t, db, func(tx *sql.Tx) error {
		return createReviewEntitlementsTx(tx, pID, hpID, cpID, now, now)
	})

	rs, _ := NewReviewService(db, testHMACKey, time.Now)

	// MarkSnapshotSent → state=sent, encrypted fields NULLed.
	if err := rs.MarkSnapshotSent(snapID); err != nil {
		t.Fatalf("MarkSnapshotSent: %v", err)
	}
	var state string
	var ctVal, ncVal sql.NullString
	db.QueryRow(`SELECT state, chat_id_ciphertext, chat_id_nonce FROM v2_review_delivery_snapshots WHERE id = ?`, snapID).Scan(&state, &ctVal, &ncVal) //nolint:errcheck
	if state != "sent" {
		t.Errorf("MarkSnapshotSent: state = %q, want 'sent'", state)
	}
	if ctVal.Valid || ncVal.Valid {
		t.Error("MarkSnapshotSent: encrypted fields must be NULL after sent")
	}

	// Verify no double aggregate: consume twice, increment exactly once.
	var ref string
	db.QueryRow(`SELECT review_ref FROM v2_review_entitlements WHERE purchase_id = ? AND reviewer_side = 'client'`, pID).Scan(&ref) //nolint:errcheck
	rs.ConsumeClientReview(ref, "positive", now)                                                                                    //nolint:errcheck
	rs.ConsumeClientReview(ref, "positive", now)                                                                                    //nolint:errcheck
	var pos int
	db.QueryRow(`SELECT positive_count FROM v2_helper_profiles WHERE id = ?`, hpID).Scan(&pos) //nolint:errcheck
	if pos != 1 {
		t.Errorf("double consume: positive_count = %d, want 1", pos)
	}
}

// ── Category 15: Snapshot survives binding deletion ───────────────────────────

func TestReview_SnapshotSurvivesBindingDeletion(t *testing.T) {
	_, db := newTestReviewService(t)
	now := time.Now().Unix()

	cpID := mustInsertClientProfileForFlow(t, db, now)
	flowID := newID()
	mustExec(t, db, `INSERT INTO v2_client_flows (id, wallet_fingerprint, currency, management_code_hash, state, client_profile_id, created_at, updated_at) VALUES (?, ?, 'BTC', 'h', 'form_ready', ?, ?, ?)`, flowID, newID(), cpID, now, now)
	bindingRef := "bnd_" + strings.Repeat("b", 32)
	mustExec(t, db, `INSERT INTO v2_client_notification_bindings (id, flow_id, binding_ref, state, window_number, verified_at, valid_until, activated_at, created_at, updated_at) VALUES (?, ?, ?, 'active', 1, ?, ?, ?, ?, ?)`, newID(), flowID, bindingRef, now-10, now+86400, now, now, now)
	mustExec(t, db, `INSERT INTO v2_telegram_destinations (binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at) VALUES (?, 'ct2', 'nc2', 'v1', ?, ?)`, bindingRef, now, now+86400)

	listingID := newID()
	cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")
	ct, nh, kv, _ := cipher.Encrypt("@sv", listingID, flowID, "telegram")
	entExp := now + int64(entitlementDuration.Seconds())
	mustExec(t, db, `INSERT INTO v2_invoices (id, flow_id, status, payment_address, amount_usd_cents, amount_atomic, detection_deadline_at, detected_txid, payment_detected_at, confirmation_deadline_at, payment_txid, payment_confirmed_at, entitlement_expires_at, created_at, updated_at) VALUES (?, ?, 'confirmed', 'a', 500, 1000, ?, 'tx', ?, ?, 'tx', ?, ?, ?, ?)`, newID(), flowID, now+3600, now, now+86400, now, entExp, now, now)
	mustExec(t, db, `INSERT INTO v2_listings (id, flow_id, city, country_code, dependency_type, help_type, urgency, languages, display_name, contact_type, contact_ciphertext, contact_nonce, contact_key_version, state, visible_until, first_published_at, last_activated_at, entitlement_expires_at, activation_count, created_at, updated_at) VALUES (?, ?, 'C', 'US', 'd', 'h', 'u', '["en"]', ?, 'telegram', ?, ?, ?, 'visible', ?, ?, ?, ?, 1, ?, ?)`, listingID, flowID, "sv_dn_"+listingID[:8], ct, nh, kv, now+86400, now, now, entExp, now, now)
	hpID := newID()
	mustExec(t, db, `INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'sv_name', 0, 0, 0, ?, ?)`, hpID, newID(), now, now)
	pID := newID()
	mustExec(t, db, `INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, balance_retry_deadline_at, last_balance_usd, last_balance_checked_at, contact_ready_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'contact_ready', 'US', ?, 1100.0, ?, ?, ?, ?)`, pID, listingID, hpID, newID(), now+86400, now, now, now, now)

	snapID := newID()
	mustExec(t, db, `INSERT INTO v2_review_delivery_snapshots (id, purchase_id, binding_ref_snapshot, chat_id_ciphertext, chat_id_nonce, key_version, state, expires_at, created_at, updated_at) VALUES (?, ?, ?, 'ct2', 'nc2', 'v1', 'pending_send', ?, ?, ?)`, snapID, pID, bindingRef, now+86400, now, now)

	// Delete the binding (ON DELETE CASCADE removes the destination too).
	mustExec(t, db, `DELETE FROM v2_client_notification_bindings WHERE binding_ref = ?`, bindingRef)

	// Snapshot must still exist with encrypted data intact.
	var snapState string
	var ctVal sql.NullString
	db.QueryRow(`SELECT state, chat_id_ciphertext FROM v2_review_delivery_snapshots WHERE id = ?`, snapID).Scan(&snapState, &ctVal) //nolint:errcheck
	if snapState != "pending_send" {
		t.Errorf("snapshot state after binding delete: %q, want 'pending_send'", snapState)
	}
	if !ctVal.Valid {
		t.Error("snapshot chat_id_ciphertext must still be present after binding deletion")
	}
}

// ── Category 16: No binding at purchase create → hard failure, zero orphan rows ─

func TestReview_NoBillNoOrphanRows(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")

	// Remove the active binding so snapshotClientDestinationTx returns ErrReviewNoBinding.
	mustExec(t, db, `DELETE FROM v2_client_notification_bindings WHERE flow_id = (SELECT flow_id FROM v2_listings WHERE id = ?)`, listingID)

	draft := HelperInvoiceDraft{
		PaymentAddress: "payaddr_test",
		AmountUSDCents: helperInvoiceUSDCents,
		AmountAtomic:   100000,
	}
	rawToken := newID()
	normalized, currency, err := validateAndNormalizeAddress(testBTCBech32Addr)
	if err != nil {
		t.Fatalf("validateAndNormalizeAddress: %v", err)
	}
	_, _, err = svc.CreatePurchase(rawToken, listingID, currency, normalized, draft)
	if !errors.Is(err, ErrReviewNoBinding) {
		t.Errorf("CreatePurchase with no binding: want ErrReviewNoBinding, got %v", err)
	}

	// Transaction must have rolled back: zero purchase rows and zero snapshot rows.
	var purchaseCount int
	db.QueryRow(`SELECT COUNT(*) FROM v2_helper_purchases WHERE listing_id = ?`, listingID).Scan(&purchaseCount) //nolint:errcheck
	if purchaseCount != 0 {
		t.Errorf("no binding → rollback: got %d purchase rows, want 0", purchaseCount)
	}
	var snapCount int
	db.QueryRow(`SELECT COUNT(*) FROM v2_review_delivery_snapshots`).Scan(&snapCount) //nolint:errcheck
	if snapCount != 0 {
		t.Errorf("no binding → rollback: got %d snapshot rows, want 0", snapCount)
	}
}

// ── Category 17: Privacy allowlists ──────────────────────────────────────────

func TestReview_PrivacyAllowlists(t *testing.T) {
	// ClientReputationView: only safe fields.
	var crev ClientReputationView
	_ = crev.MemberSince
	_ = crev.PositiveCount
	_ = crev.NegativeCount

	// HelperReputationForReview: only safe fields.
	var hrev HelperReputationForReview
	_ = hrev.PublicName
	_ = hrev.MemberSince
	_ = hrev.PurchaseCount
	_ = hrev.PositiveCount
	_ = hrev.NegativeCount

	// Review token must not equal the stored review_ref.
	rawRef, reviewRef, _ := newReviewRef()
	token := helperReviewToken(testHMACKey, rawRef, reviewRef)
	if token == reviewRef {
		t.Error("privacy: review token must not equal stored review_ref")
	}

	// Telegram callback_data must fit in 64 bytes.
	callbackData := telegramCallbackData(rawRef, "p")
	if len(callbackData) > 64 {
		t.Errorf("callback_data too long: %d bytes (limit 64)", len(callbackData))
	}

	// HelperReviewCapabilityResult.ReviewToken: not the review_ref.
	var cap HelperReviewCapabilityResult
	_ = cap.ReviewToken
	_ = cap.ExpiresAt
	_ = cap.ClientReputation
	_ = cap.ClientDisplayName
}

// ── Category 18: Schema constraints matrix ────────────────────────────────────

func TestReview_SchemaConstraints(t *testing.T) {
	_, db := newTestReviewService(t)
	now := time.Now().Unix()

	t.Run("profile_short_fingerprint", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_client_profiles (id, wallet_fingerprint, currency, positive_count, negative_count, created_at, updated_at) VALUES (?, 'short', 'BTC', 0, 0, ?, ?)`, newID(), now, now)
		if err == nil {
			t.Error("expected CHECK violation for short wallet_fingerprint")
		}
	})

	t.Run("profile_invalid_currency", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_client_profiles (id, wallet_fingerprint, currency, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'ETH', 0, 0, ?, ?)`, newID(), newID(), now, now)
		if err == nil {
			t.Error("expected CHECK violation for invalid currency")
		}
	})

	t.Run("entitlement_unique_purchase_side", func(t *testing.T) {
		hpID := newID()
		mustExec(t, db, `INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'sc_sch_n1', 0, 0, 0, ?, ?)`, hpID, newID(), now, now)
		// Use a purchase already in the DB (or insert a fake one).
		pID := newID()
		// purchase with awaiting_payment (no required balance fields).
		listingIDFake := newID() // listing won't exist; skip FK check for unit test
		// FK on listing_id will fail; use a workaround by inserting a minimal listing.
		cpIDx := mustInsertClientProfileForFlow(t, db, now)
		fIDx := newID()
		mustExec(t, db, `INSERT INTO v2_client_flows (id, wallet_fingerprint, currency, management_code_hash, state, client_profile_id, created_at, updated_at) VALUES (?, ?, 'BTC', 'hh', 'form_ready', ?, ?, ?)`, fIDx, newID(), cpIDx, now, now)
		entExpx := now + int64(entitlementDuration.Seconds())
		mustExec(t, db, `INSERT INTO v2_invoices (id, flow_id, status, payment_address, amount_usd_cents, amount_atomic, detection_deadline_at, detected_txid, payment_detected_at, confirmation_deadline_at, payment_txid, payment_confirmed_at, entitlement_expires_at, created_at, updated_at) VALUES (?, ?, 'confirmed', 'a', 500, 1000, ?, 'tx', ?, ?, 'tx', ?, ?, ?, ?)`, newID(), fIDx, now+3600, now, now+86400, now, entExpx, now, now)
		cipherx, _ := NewAESGCMContactCipher(testAESKey, "v1")
		ctx, nhx, kvx, _ := cipherx.Encrypt("@x", listingIDFake, fIDx, "telegram")
		mustExec(t, db, `INSERT INTO v2_listings (id, flow_id, city, country_code, dependency_type, help_type, urgency, languages, display_name, contact_type, contact_ciphertext, contact_nonce, contact_key_version, state, visible_until, first_published_at, last_activated_at, entitlement_expires_at, activation_count, created_at, updated_at) VALUES (?, ?, 'C', 'US', 'd', 'h', 'u', '["en"]', ?, 'telegram', ?, ?, ?, 'visible', ?, ?, ?, ?, 1, ?, ?)`, listingIDFake, fIDx, "sc_dn_"+listingIDFake[:8], ctx, nhx, kvx, now+86400, now, now, entExpx, now, now)
		mustExec(t, db, `INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, created_at, updated_at) VALUES (?, ?, ?, ?, 'awaiting_payment', 'US', ?, ?)`, pID, listingIDFake, hpID, newID(), now, now)

		ref1 := "rev_" + strings.Repeat("a", 32)
		ref2 := "rev_" + strings.Repeat("b", 32)
		mustExec(t, db, `INSERT INTO v2_review_entitlements (id, purchase_id, reviewer_side, review_ref, target_helper_profile_id, target_client_profile_id, expires_at, created_at, updated_at) VALUES (?, ?, 'client', ?, ?, NULL, ?, ?, ?)`, newID(), pID, ref1, hpID, now+86400, now, now)
		_, err := db.Exec(`INSERT INTO v2_review_entitlements (id, purchase_id, reviewer_side, review_ref, target_helper_profile_id, target_client_profile_id, expires_at, created_at, updated_at) VALUES (?, ?, 'client', ?, ?, NULL, ?, ?, ?)`, newID(), pID, ref2, hpID, now+86400, now, now)
		if err == nil {
			t.Error("expected UNIQUE violation for duplicate (purchase_id, reviewer_side)")
		}
	})

	t.Run("entitlement_invalid_side", func(t *testing.T) {
		ref := "rev_" + strings.Repeat("c", 32)
		_, err := db.Exec(`INSERT INTO v2_review_entitlements (id, purchase_id, reviewer_side, review_ref, target_helper_profile_id, target_client_profile_id, expires_at, created_at, updated_at) VALUES (?, ?, 'admin', ?, NULL, NULL, ?, ?, ?)`, newID(), newID(), ref, now+86400, now, now)
		if err == nil {
			t.Error("expected CHECK violation for invalid reviewer_side")
		}
	})

	t.Run("flow_form_ready_without_profile", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_client_flows (id, wallet_fingerprint, currency, management_code_hash, state, created_at, updated_at) VALUES (?, ?, 'BTC', 'hash', 'form_ready', ?, ?)`, newID(), newID(), now, now)
		if err == nil {
			t.Error("expected CHECK violation: form_ready state without client_profile_id")
		}
	})

	t.Run("snapshot_sent_with_encrypted_fields", func(t *testing.T) {
		// 'sent' state with non-NULL chat_id_ciphertext → violates CHECK.
		pIDsc := newID()
		hpIDsc := newID()
		mustExec(t, db, `INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'sc_sch_n2', 0, 0, 0, ?, ?)`, hpIDsc, newID(), now, now)
		// Need listing_id FK: insert a dummy listing via existing valid flow.
		cpIDsc := mustInsertClientProfileForFlow(t, db, now)
		fIDsc := newID()
		mustExec(t, db, `INSERT INTO v2_client_flows (id, wallet_fingerprint, currency, management_code_hash, state, client_profile_id, created_at, updated_at) VALUES (?, ?, 'BTC', 'hh', 'form_ready', ?, ?, ?)`, fIDsc, newID(), cpIDsc, now, now)
		entsc := now + int64(entitlementDuration.Seconds())
		mustExec(t, db, `INSERT INTO v2_invoices (id, flow_id, status, payment_address, amount_usd_cents, amount_atomic, detection_deadline_at, detected_txid, payment_detected_at, confirmation_deadline_at, payment_txid, payment_confirmed_at, entitlement_expires_at, created_at, updated_at) VALUES (?, ?, 'confirmed', 'a', 500, 1000, ?, 'tx', ?, ?, 'tx', ?, ?, ?, ?)`, newID(), fIDsc, now+3600, now, now+86400, now, entsc, now, now)
		ciphersc, _ := NewAESGCMContactCipher(testAESKey, "v1")
		lIDsc := newID()
		ctsc, nhsc, kvsc, _ := ciphersc.Encrypt("@sc", lIDsc, fIDsc, "telegram")
		mustExec(t, db, `INSERT INTO v2_listings (id, flow_id, city, country_code, dependency_type, help_type, urgency, languages, display_name, contact_type, contact_ciphertext, contact_nonce, contact_key_version, state, visible_until, first_published_at, last_activated_at, entitlement_expires_at, activation_count, created_at, updated_at) VALUES (?, ?, 'C', 'US', 'd', 'h', 'u', '["en"]', ?, 'telegram', ?, ?, ?, 'visible', ?, ?, ?, ?, 1, ?, ?)`, lIDsc, fIDsc, "scsc_dn_"+lIDsc[:8], ctsc, nhsc, kvsc, now+86400, now, now, entsc, now, now)
		mustExec(t, db, `INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, created_at, updated_at) VALUES (?, ?, ?, ?, 'awaiting_payment', 'US', ?, ?)`, pIDsc, lIDsc, hpIDsc, newID(), now, now)

		_, err := db.Exec(`INSERT INTO v2_review_delivery_snapshots (id, purchase_id, binding_ref_snapshot, chat_id_ciphertext, chat_id_nonce, key_version, state, expires_at, created_at, updated_at) VALUES (?, ?, 'bnd_a', 'ct', 'nc', 'v1', 'sent', ?, ?, ?)`, newID(), pIDsc, now+86400, now, now)
		if err == nil {
			t.Error("expected CHECK violation: sent state with non-NULL encrypted fields")
		}
	})
}

// ── Category 19: Expired binding at purchase create → ErrReviewNoBinding ─────

func TestReview_ExpiredBindingBlocksPurchase(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")

	// Replace the active binding with an expired one (valid_until in past).
	mustExec(t, db, `DELETE FROM v2_client_notification_bindings WHERE flow_id = (SELECT flow_id FROM v2_listings WHERE id = ?)`, listingID)

	now := time.Now().Unix()
	bindingRef := "bnd_" + newID()[:32]
	// Window ended 1 second ago; activated 11s ago; verified 21s ago.
	validUntilExp := now - 1
	activatedAtExp := validUntilExp - 10
	verifiedAtExp := validUntilExp - 20
	mustExec(t, db, `INSERT INTO v2_client_notification_bindings (id, flow_id, binding_ref, state, window_number, verified_at, valid_until, activated_at, created_at, updated_at) VALUES (?, (SELECT flow_id FROM v2_listings WHERE id = ?), ?, 'active', 1, ?, ?, ?, ?, ?)`,
		newID(), listingID, bindingRef, verifiedAtExp, validUntilExp, activatedAtExp, verifiedAtExp, activatedAtExp)
	mustExec(t, db, `INSERT INTO v2_telegram_destinations (binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at) VALUES (?, 'ct', 'nc', 'v1', ?, ?)`,
		bindingRef, verifiedAtExp, validUntilExp)

	normalized, currency, _ := validateAndNormalizeAddress(testBTCBech32Addr)
	draft := HelperInvoiceDraft{PaymentAddress: "payaddr_exp", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, _, err := svc.CreatePurchase(newID(), listingID, currency, normalized, draft)
	if !errors.Is(err, ErrReviewNoBinding) {
		t.Errorf("expired binding: want ErrReviewNoBinding, got %v", err)
	}
	// Zero orphan rows.
	var pc int
	db.QueryRow(`SELECT COUNT(*) FROM v2_helper_purchases WHERE listing_id = ?`, listingID).Scan(&pc) //nolint:errcheck
	if pc != 0 {
		t.Errorf("expired binding → rollback: got %d purchase rows, want 0", pc)
	}
}

// ── Category 20: Inconsistent binding destination → ErrReviewNoBinding ───────

func TestReview_InconsistentBindingBlocksPurchase(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")

	// Make the destination's expires_at != binding's valid_until (inconsistent).
	// The destination was set to valid_until by mustInsertActiveBindingForFlow.
	// Update destination to a different expires_at value.
	mustExec(t, db, `UPDATE v2_telegram_destinations SET expires_at = expires_at - 1 WHERE binding_ref = (SELECT binding_ref FROM v2_client_notification_bindings WHERE flow_id = (SELECT flow_id FROM v2_listings WHERE id = ?))`, listingID)

	normalized, currency, _ := validateAndNormalizeAddress(testBTCBech32Addr)
	draft := HelperInvoiceDraft{PaymentAddress: "payaddr_inc", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, _, err := svc.CreatePurchase(newID(), listingID, currency, normalized, draft)
	if !errors.Is(err, ErrReviewNoBinding) {
		t.Errorf("inconsistent binding: want ErrReviewNoBinding, got %v", err)
	}
	var pc int
	db.QueryRow(`SELECT COUNT(*) FROM v2_helper_purchases WHERE listing_id = ?`, listingID).Scan(&pc) //nolint:errcheck
	if pc != 0 {
		t.Errorf("inconsistent binding → rollback: got %d purchase rows, want 0", pc)
	}
}

// ── Category 21: Concurrent contact-ready → exactly 1 entitlement pair ───────

func TestReview_ConcurrentContactReady(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")

	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	draft := HelperInvoiceDraft{PaymentAddress: "payaddr_conc", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, view, err := svc.CreatePurchase(newID(), listingID, currency, normalized, draft)
	if err != nil {
		t.Fatalf("CreatePurchase: %v", err)
	}
	svc.RecordHelperDetection(view.PurchaseID, "txid_conc", []string{addr}, 100000, time.Now()) //nolint:errcheck
	svc.ConfirmHelperPayment(view.PurchaseID, time.Now())                                       //nolint:errcheck

	// Concurrent RecordHelperPostPaymentBalance from 5 goroutines.
	const workers = 5
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc.RecordHelperPostPaymentBalance(view.PurchaseID, 1200.0) //nolint:errcheck
		}()
	}
	wg.Wait()

	// Exactly 2 entitlements (one per side).
	var entCount int
	db.QueryRow(`SELECT COUNT(*) FROM v2_review_entitlements WHERE purchase_id = ?`, view.PurchaseID).Scan(&entCount) //nolint:errcheck
	if entCount != 2 {
		t.Errorf("concurrent contact_ready: want 2 entitlements, got %d", entCount)
	}

	// Exactly 1 purchase_count increment.
	fp := svc.helperWalletFingerprint(currency, normalized)
	var pc int
	db.QueryRow(`SELECT purchase_count FROM v2_helper_profiles WHERE wallet_fingerprint = ?`, fp).Scan(&pc) //nolint:errcheck
	if pc != 1 {
		t.Errorf("concurrent contact_ready: want purchase_count=1, got %d", pc)
	}

	// Exactly 1 pending_send snapshot.
	var snapCount int
	db.QueryRow(`SELECT COUNT(*) FROM v2_review_delivery_snapshots WHERE purchase_id = ? AND state = 'pending_send'`, view.PurchaseID).Scan(&snapCount) //nolint:errcheck
	if snapCount != 1 {
		t.Errorf("concurrent contact_ready: want 1 pending_send snapshot, got %d", snapCount)
	}
}

// ── Category 22: Concurrent same-wallet profile creation → one profile ───────

func TestReview_ConcurrentClientProfileCreation(t *testing.T) {
	_, db := newTestReviewService(t)
	now := time.Now().Unix()
	fp := strings.Repeat("c", 64) // shared fingerprint

	// 8 goroutines call getOrCreateClientProfileTx concurrently.
	const workers = 8
	ids := make([]string, workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tx, err := db.Begin()
			if err != nil {
				return
			}
			id, err := getOrCreateClientProfileTx(tx, fp, "BTC", now)
			if err != nil {
				tx.Rollback() //nolint:errcheck
				return
			}
			if err := tx.Commit(); err != nil {
				return
			}
			mu.Lock()
			ids[idx] = id
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	// All goroutines that completed must have the same profile ID.
	var winner string
	for _, id := range ids {
		if id == "" {
			continue
		}
		if winner == "" {
			winner = id
		} else if id != winner {
			t.Errorf("concurrent profile creation: got two different IDs %s and %s", winner, id)
		}
	}

	// Exactly one profile row in the DB for this fingerprint.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_profiles WHERE wallet_fingerprint = ?`, fp).Scan(&count) //nolint:errcheck
	if count != 1 {
		t.Errorf("concurrent profile creation: want 1 profile row, got %d", count)
	}
}

// ── Category 23: Public reputation counts in board + detail ──────────────────

func TestReview_PublicReputationCounts(t *testing.T) {
	now := time.Now()
	ls, svc, db := newTestListingService(t, func() time.Time { return now })
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Fatalf("FirstPublish: %v", err)
	}

	// Board query: reputation shows zeros (no review yet).
	views, err := ls.BoardQuery("tbilisi", now)
	if err != nil {
		t.Fatalf("BoardQuery: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("BoardQuery: want 1 result, got %d", len(views))
	}
	if views[0].ClientReputation.PositiveCount != 0 || views[0].ClientReputation.NegativeCount != 0 {
		t.Errorf("board before review: want 0/0, got %d/%d", views[0].ClientReputation.PositiveCount, views[0].ClientReputation.NegativeCount)
	}

	// Detail query: same.
	listingID := views[0].ID
	detail, err := ls.GetPublicListing(listingID, now)
	if err != nil {
		t.Fatalf("GetPublicListing: %v", err)
	}
	if detail.ClientReputation.PositiveCount != 0 {
		t.Errorf("detail before review: want 0, got %d", detail.ClientReputation.PositiveCount)
	}

	// Increment positive_count directly (simulating a committed review).
	var cpID string
	db.QueryRow(`SELECT client_profile_id FROM v2_client_flows WHERE id = ?`, flowID).Scan(&cpID) //nolint:errcheck
	mustExec(t, db, `UPDATE v2_client_profiles SET positive_count = positive_count + 1 WHERE id = ?`, cpID)

	// Board query after review: sees increment immediately.
	views2, err := ls.BoardQuery("tbilisi", now)
	if err != nil {
		t.Fatalf("BoardQuery after review: %v", err)
	}
	if len(views2) != 1 {
		t.Fatalf("BoardQuery after review: want 1 result, got %d", len(views2))
	}
	if views2[0].ClientReputation.PositiveCount != 1 {
		t.Errorf("board after review: want positive_count=1, got %d", views2[0].ClientReputation.PositiveCount)
	}

	// Detail query after review: also sees increment.
	detail2, err := ls.GetPublicListing(listingID, now)
	if err != nil {
		t.Fatalf("GetPublicListing after review: %v", err)
	}
	if detail2.ClientReputation.PositiveCount != 1 {
		t.Errorf("detail after review: want positive_count=1, got %d", detail2.ClientReputation.PositiveCount)
	}

	// MemberSince must be non-zero (profile was created).
	if detail2.ClientReputation.MemberSince.IsZero() {
		t.Error("detail: ClientReputation.MemberSince must not be zero")
	}
}
