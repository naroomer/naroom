package v2

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── Test helpers ──────────────────────────────────────────────────────────────

func newTestHelperService(t *testing.T) (*HelperPurchaseService, *sql.DB) {
	t.Helper()
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cipher, err := NewAESGCMContactCipher(testAESKey, "v1")
	if err != nil {
		t.Fatalf("NewAESGCMContactCipher: %v", err)
	}

	svc, err := NewHelperPurchaseService(
		db,
		testHMACKey,
		cipher,
		NewRandomDisplayNameGenerator(),
		time.Now,
	)
	if err != nil {
		t.Fatalf("NewHelperPurchaseService: %v", err)
	}
	return svc, db
}

// mustInsertClientProfileForFlow inserts a v2_client_profiles row with a random
// 64-char fingerprint and returns the profileID. Call this before inserting a
// v2_client_flows row in any non-'awaiting_payment' state (Task 06 CHECK constraint).
func mustInsertClientProfileForFlow(t *testing.T, db *sql.DB, now int64) string {
	t.Helper()
	profileID := newID()
	fp := newID() // 64 lowercase hex chars — satisfies CHECK on wallet_fingerprint
	if _, err := db.Exec(`
		INSERT INTO v2_client_profiles
		  (id, wallet_fingerprint, currency, positive_count, negative_count, created_at, updated_at)
		VALUES (?, ?, 'BTC', 0, 0, ?, ?)`,
		profileID, fp, now, now,
	); err != nil {
		t.Fatalf("mustInsertClientProfileForFlow: insert profile: %v", err)
	}
	return profileID
}

// mustInsertActiveBindingForFlow inserts a v2_client_notification_bindings row in
// 'active' state for the given flowID and a matching v2_telegram_destinations row.
// This satisfies the binding gate in snapshotClientDestinationTx so that
// CreatePurchase succeeds in tests that call mustCreateVisibleListing.
func mustInsertActiveBindingForFlow(t *testing.T, db *sql.DB, flowID string, now int64) {
	t.Helper()
	bindingRef := "bnd_" + newID()[:32]
	validUntil := now + 86400

	_, err := db.Exec(`
		INSERT INTO v2_client_notification_bindings
		  (id, flow_id, binding_ref, state, window_number,
		   verified_at, valid_until, activated_at, created_at, updated_at)
		VALUES (?, ?, ?, 'active', 1, ?, ?, ?, ?, ?)`,
		newID(), flowID, bindingRef,
		now, validUntil, now, now, now,
	)
	if err != nil {
		t.Fatalf("mustInsertActiveBindingForFlow: insert binding: %v", err)
	}

	_, err = db.Exec(`
		INSERT INTO v2_telegram_destinations
		  (binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at)
		VALUES (?, 'aabbcc', 'ddeeff', 'v1', ?, ?)`,
		bindingRef, now, validUntil,
	)
	if err != nil {
		t.Fatalf("mustInsertActiveBindingForFlow: insert destination: %v", err)
	}
}

// mustCreateVisibleListing creates a full visible listing for tests.
// Returns listingID.
func mustCreateVisibleListing(t *testing.T, db *sql.DB, countryCode string) string {
	t.Helper()
	now := time.Now().Unix()
	listingID := newID()
	flowID := newID()
	clientProfileID := mustInsertClientProfileForFlow(t, db, now)

	// Insert client flow (Task 06: client_profile_id required for non-awaiting_payment states).
	_, err := db.Exec(`
		INSERT INTO v2_client_flows
		  (id, wallet_fingerprint, currency, management_code_hash, state, client_profile_id, created_at, updated_at)
		VALUES (?, 'fp1', 'BTC', 'ch1', 'form_ready', ?, ?, ?)`,
		flowID, clientProfileID, now, now,
	)
	if err != nil {
		t.Fatalf("insert flow: %v", err)
	}

	// Insert confirmed invoice with entitlement.
	invoiceID := newID()
	entitlement := now + int64(entitlementDuration.Seconds())
	_, err = db.Exec(`
		INSERT INTO v2_invoices
		  (id, flow_id, status, payment_address, amount_usd_cents, amount_atomic,
		   detection_deadline_at, detected_txid, payment_detected_at, confirmation_deadline_at,
		   payment_txid, payment_confirmed_at, entitlement_expires_at, created_at, updated_at)
		VALUES (?, ?, 'confirmed', 'addr1', 500, 100000,
		        ?, 'txid1', ?, ?,
		        'txid1', ?, ?, ?, ?)`,
		invoiceID, flowID,
		now+3600, now, now+86400,
		now, entitlement, now, now,
	)
	if err != nil {
		t.Fatalf("insert invoice: %v", err)
	}

	// Encrypt a dummy contact.
	cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")
	ctHex, nonceHex, keyVer, _ := cipher.Encrypt("@testhelper", listingID, flowID, "telegram")

	// Insert listing (visible).
	visibleUntil := now + int64(listingDailyWindow.Seconds())
	_, err = db.Exec(`
		INSERT INTO v2_listings
		  (id, flow_id, city, country_code, dependency_type, help_type, urgency, languages,
		   display_name, contact_type, contact_ciphertext, contact_nonce, contact_key_version,
		   state, visible_until, first_published_at, last_activated_at, entitlement_expires_at,
		   activation_count, created_at, updated_at)
		VALUES (?, ?, 'TestCity', ?, 'housing', 'money', 'high', '["en"]',
		        ?, 'telegram', ?, ?, ?,
		        'visible', ?, ?, ?, ?,
		        1, ?, ?)`,
		listingID, flowID, countryCode,
		"display_"+listingID[:8],
		ctHex, nonceHex, keyVer,
		visibleUntil, now, now, entitlement,
		now, now,
	)
	if err != nil {
		t.Fatalf("insert listing: %v", err)
	}

	// Insert active binding+destination so snapshotClientDestinationTx succeeds.
	mustInsertActiveBindingForFlow(t, db, flowID, now)

	return listingID
}

// ── 1. Schema/privacy tests ───────────────────────────────────────────────────

// Test 1: CHECK constraints on v2_helper_profiles.
func TestHelperSchema_ProfileConstraints(t *testing.T) {
	_, db := newTestHelperService(t)
	now := time.Now().Unix()

	id1 := newID()
	fp1 := newID()

	// Valid insert.
	_, err := db.Exec(`
		INSERT INTO v2_helper_profiles
		  (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count,
		   negative_count, created_at, updated_at)
		VALUES (?, ?, 'BTC', 'calm_river_aa', 0, 0, 0, ?, ?)`,
		id1, fp1, now, now)
	if err != nil {
		t.Fatalf("valid insert failed: %v", err)
	}

	// Invalid currency.
	_, err = db.Exec(`
		INSERT INTO v2_helper_profiles
		  (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count,
		   negative_count, created_at, updated_at)
		VALUES (?, ?, 'ETH', 'brave_stone_bb', 0, 0, 0, ?, ?)`,
		newID(), newID(), now, now)
	if err == nil {
		t.Fatal("expected error for invalid currency ETH")
	}

	// Negative purchase_count.
	_, err = db.Exec(`
		INSERT INTO v2_helper_profiles
		  (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count,
		   negative_count, created_at, updated_at)
		VALUES (?, ?, 'LTC', 'gentle_cloud_cc', -1, 0, 0, ?, ?)`,
		newID(), newID(), now, now)
	if err == nil {
		t.Fatal("expected error for negative purchase_count")
	}

	// UNIQUE on wallet_fingerprint.
	_, err = db.Exec(`
		INSERT INTO v2_helper_profiles
		  (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count,
		   negative_count, created_at, updated_at)
		VALUES (?, ?, 'BTC', 'steady_dawn_dd', 0, 0, 0, ?, ?)`,
		newID(), fp1, now, now)
	if err == nil {
		t.Fatal("expected error for duplicate wallet_fingerprint")
	}
	if !isSQLiteUniqueOnColumn(err, "v2_helper_profiles.wallet_fingerprint") {
		t.Fatalf("expected UNIQUE on wallet_fingerprint, got: %v", err)
	}

	// UNIQUE on public_name.
	_, err = db.Exec(`
		INSERT INTO v2_helper_profiles
		  (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count,
		   negative_count, created_at, updated_at)
		VALUES (?, ?, 'BTC', 'calm_river_aa', 0, 0, 0, ?, ?)`,
		newID(), newID(), now, now)
	if err == nil {
		t.Fatal("expected error for duplicate public_name")
	}
}

// Test 1 continued: invoice state machine constraints.
func TestHelperSchema_InvoiceConstraints(t *testing.T) {
	_, db := newTestHelperService(t)
	now := time.Now().Unix()

	// Create a profile and purchase first.
	profileID := newID()
	profileFP := newID()
	_, err := db.Exec(`
		INSERT INTO v2_helper_profiles
		  (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count,
		   negative_count, created_at, updated_at)
		VALUES (?, ?, 'BTC', 'calm_river_a1', 0, 0, 0, ?, ?)`,
		profileID, profileFP, now, now)
	if err != nil {
		t.Fatalf("insert profile: %v", err)
	}
	// Need a valid listing.
	listingID := newID()
	flowID := newID()
	cpID := mustInsertClientProfileForFlow(t, db, now)
	db.Exec(`INSERT INTO v2_client_flows (id, wallet_fingerprint, currency, management_code_hash, state, client_profile_id, created_at, updated_at) VALUES (?, ?, 'BTC', ?, 'form_ready', ?, ?, ?)`, flowID, newID(), newID(), cpID, now, now) //nolint:errcheck
	entitlement := now + int64(entitlementDuration.Seconds())
	db.Exec(`INSERT INTO v2_invoices (id, flow_id, status, payment_address, amount_usd_cents, amount_atomic, detection_deadline_at, detected_txid, payment_detected_at, confirmation_deadline_at, payment_txid, payment_confirmed_at, entitlement_expires_at, created_at, updated_at) VALUES (?, ?, 'confirmed', 'a', 500, 1000, ?, 'tx', ?, ?, 'tx', ?, ?, ?, ?)`, newID(), flowID, now+3600, now, now+86400, now, entitlement, now, now) //nolint:errcheck
	ct, nonceH, kv, _ := func() (string, string, string, error) {
		c, _ := NewAESGCMContactCipher(testAESKey, "v1")
		return c.Encrypt("@x", listingID, flowID, "telegram")
	}()
	db.Exec(`INSERT INTO v2_listings (id, flow_id, city, country_code, dependency_type, help_type, urgency, languages, display_name, contact_type, contact_ciphertext, contact_nonce, contact_key_version, state, visible_until, first_published_at, last_activated_at, entitlement_expires_at, activation_count, created_at, updated_at) VALUES (?, ?, 'C', 'US', 'd', 'h', 'u', '["en"]', ?, 'telegram', ?, ?, ?, 'visible', ?, ?, ?, ?, 1, ?, ?)`, listingID, flowID, "dn_"+listingID[:6], ct, nonceH, kv, now+86400, now, now, entitlement, now, now) //nolint:errcheck

	purchaseID := newID()
	tokenHash := strings.Repeat("a", 64)
	db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, created_at, updated_at) VALUES (?, ?, ?, ?, 'awaiting_payment', 'US', ?, ?)`, purchaseID, listingID, profileID, tokenHash, now, now) //nolint:errcheck

	// amount_usd_cents must equal 1000.
	_, err = db.Exec(`
		INSERT INTO v2_helper_invoices
		  (id, purchase_id, status, payment_address, amount_usd_cents, amount_atomic,
		   detection_deadline_at, created_at, updated_at)
		VALUES (?, ?, 'pending', 'payaddr', 500, 1000, ?, ?, ?)`,
		newID(), purchaseID, now+3600, now, now)
	if err == nil {
		t.Fatal("expected error for amount_usd_cents != 1000")
	}

	// Valid pending invoice.
	_, err = db.Exec(`
		INSERT INTO v2_helper_invoices
		  (id, purchase_id, status, payment_address, amount_usd_cents, amount_atomic,
		   detection_deadline_at, created_at, updated_at)
		VALUES (?, ?, 'pending', 'payaddr', 1000, 100000, ?, ?, ?)`,
		newID(), purchaseID, now+3600, now, now)
	if err != nil {
		t.Fatalf("valid invoice insert failed: %v", err)
	}

	// Invalid browser_token_hash (not hex).
	_, err = db.Exec(`
		INSERT INTO v2_helper_purchases
		  (id, listing_id, helper_profile_id, browser_token_hash, state,
		   country_code_snapshot, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'awaiting_payment', 'US', ?, ?)`,
		newID(), listingID, profileID, strings.Repeat("Z", 64), now, now)
	if err == nil {
		t.Fatal("expected error for non-hex browser_token_hash")
	}
}

// Test 2: Plain wallet, browser token and contact absent from DB.
func TestHelperPrivacy_NoSecretsInDB(t *testing.T) {
	svc, db := newTestHelperService(t)

	listingID := mustCreateVisibleListing(t, db, "US")

	rawWallet := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	normalized, currency, err := validateAndNormalizeAddress(rawWallet)
	if err != nil {
		t.Fatalf("validateAndNormalizeAddress: %v", err)
	}

	rawToken := newID()
	draft := HelperInvoiceDraft{
		PaymentAddress: "bc1qpay1234567890",
		AmountAtomic:   10000,
		AmountUSDCents: helperInvoiceUSDCents,
	}
	_, view, err := svc.CreatePurchase(rawToken, listingID, currency, normalized, draft)
	if err != nil {
		t.Fatalf("CreatePurchase: %v", err)
	}

	// Scan entire DB for secrets.
	checkAbsent := func(secret, label string) {
		t.Helper()
		tables := []string{
			"v2_helper_profiles", "v2_helper_purchases", "v2_helper_invoices",
		}
		for _, tbl := range tables {
			rows, err := db.Query("SELECT * FROM " + tbl)
			if err != nil {
				t.Errorf("query %s: %v", tbl, err)
				continue
			}
			cols, _ := rows.Columns()
			for rows.Next() {
				vals := make([]interface{}, len(cols))
				ptrs := make([]interface{}, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				rows.Scan(ptrs...) //nolint:errcheck
				for _, v := range vals {
					if s, ok := v.(string); ok && strings.Contains(s, secret) {
						t.Errorf("table %s contains %s: %q", tbl, label, s)
					}
				}
			}
			rows.Close()
		}
	}

	checkAbsent(rawWallet, "plain wallet")
	checkAbsent(rawToken, "raw browser token")
	checkAbsent("@testhelper", "contact plaintext")
	_ = view
}

// Test 3: Fingerprint/token domains are distinct and differ from Client.
func TestHelperDomainSeparation(t *testing.T) {
	svc, _ := newTestHelperService(t)

	// Same address + currency: helper vs client produce different fingerprints.
	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	currency := "BTC"

	helperFP := svc.helperWalletFingerprint(currency, addr)

	clientSvc, err := NewWithClock(svc.db, testHMACKey, time.Now)
	if err != nil {
		t.Fatalf("NewWithClock: %v", err)
	}
	clientFP := clientSvc.walletFingerprint(currency, addr)

	if helperFP == clientFP {
		t.Error("helper wallet fingerprint must differ from client wallet fingerprint")
	}

	// Token hash vs wallet fingerprint must differ.
	tokenHash := svc.helperBrowserTokenHash(addr)
	if tokenHash == helperFP {
		t.Error("token hash must differ from wallet fingerprint")
	}

	// Txid hash domain must differ.
	txidHash := svc.HelperTxidHash(addr)
	if txidHash == helperFP || txidHash == tokenHash {
		t.Error("txid hash must differ from wallet fp and token hash")
	}
}

// Test 4 (implicit in Test 3 above): public JSON safe — covered in HTTP tests.

// Test 5: Visible listing + valid wallet + balance exactly 1010 creates profile/purchase/$10 invoice.
func TestHelperCreate_ExactFloor(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")

	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	rawToken := newID()
	draft := HelperInvoiceDraft{
		PaymentAddress: "payaddr1",
		AmountAtomic:   100000,
		AmountUSDCents: helperInvoiceUSDCents,
	}
	isNew, view, err := svc.CreatePurchase(rawToken, listingID, currency, normalized, draft)
	if err != nil {
		t.Fatalf("CreatePurchase: %v", err)
	}

	if !isNew {
		t.Error("first create must return isNew=true")
	}
	if view.AmountUSDCents != helperInvoiceUSDCents {
		t.Errorf("invoice cents: got %d, want %d", view.AmountUSDCents, helperInvoiceUSDCents)
	}
	if view.State != HPStateAwaitingPayment {
		t.Errorf("state: got %q, want awaiting_payment", view.State)
	}
	if view.InvoiceStatus != HPInvoicePending {
		t.Errorf("invoice status: got %q, want pending", view.InvoiceStatus)
	}
}

// Test 6: 1009.99 balance → blocked in HTTP layer. Invalid inputs rejected by validateBalanceInputs.
func TestHelperCreate_InsufficientBalance(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")

	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	// validateBalanceInputs rejects NaN/Inf/negative, but NOT values below floor.
	// The floor comparison (< 1010) is the HTTP handler's responsibility.
	if err := validateBalanceInputs(-1.0, DefaultV2BalancePolicy().HelperPreInvoiceMinUSD()); err == nil {
		t.Error("negative balance should be rejected by validateBalanceInputs")
	}
	// 1009.99 is a valid finite number — validateBalanceInputs accepts it.
	// The HTTP handler rejects it via explicit < DefaultV2BalancePolicy().HelperPreInvoiceMinUSD() check.
	if err := validateBalanceInputs(1009.99, DefaultV2BalancePolicy().HelperPreInvoiceMinUSD()); err != nil {
		t.Errorf("validateBalanceInputs(1009.99) should pass (floor check is separate): %v", err)
	}

	// HTTP-layer: fakeHelperBalance returning 1009.99 → 402 PaymentRequired.
	issuer := &fakeHelperIssuer{}
	bal := &fakeHelperBalance{result: 1009.99}
	h, _ := NewHelperPurchaseHandler(svc, issuer, bal, testHMACKey, time.Now)

	rr := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
		"purchase_token": newID(),
		"listing_id":     listingID,
		"wallet_address": addr,
	})
	if rr.Code != http.StatusPaymentRequired {
		t.Errorf("1009.99 balance: got status %d, want 402", rr.Code)
	}

	// No purchase row created.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_helper_purchases`).Scan(&count) //nolint:errcheck
	if count != 0 {
		t.Errorf("expected 0 purchases after failed balance check, got %d", count)
	}

	// Provider outage → 503.
	bal.err = errors.New("provider unavailable")
	bal.result = 0
	rr2 := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
		"purchase_token": newID(),
		"listing_id":     listingID,
		"wallet_address": addr,
	})
	if rr2.Code != http.StatusServiceUnavailable {
		t.Errorf("provider outage: got status %d, want 503", rr2.Code)
	}
	db.QueryRow(`SELECT COUNT(*) FROM v2_helper_purchases`).Scan(&count) //nolint:errcheck
	if count != 0 {
		t.Errorf("expected 0 purchases after provider outage, got %d", count)
	}
	_ = normalized
	_ = currency
}

// Test 7: Hidden/stale/finished/unknown listing → ErrNotFound, no rows created.
func TestHelperCreate_InvisibleListing(t *testing.T) {
	svc, db := newTestHelperService(t)

	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	tests := []struct {
		name  string
		setup func() string // returns listingID
	}{
		{"unknown", func() string { return "nonexistent-id" }},
		{"hidden", func() string {
			id := mustCreateVisibleListing(t, db, "US")
			// Move to hidden.
			db.Exec(`UPDATE v2_listings SET state='hidden', visible_until=NULL, updated_at=? WHERE id=?`, time.Now().Unix(), id) //nolint:errcheck
			return id
		}},
		{"stale visible_until", func() string {
			// Insert a listing where visible_until is in the past.
			// All timestamps must satisfy schema constraints:
			//   created_at <= first_published_at <= last_activated_at < visible_until <= entitlement_expires_at
			now := time.Now().Unix()
			id := newID()
			fid := newID()
			cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")
			ct, nh, kv, _ := cipher.Encrypt("@x", id, fid, "telegram")
			pastBase := now - 200
			visibleUntilPast := now - 1 // 1 second in the past
			entExp := now + int64(entitlementDuration.Seconds())
			staleCPID := mustInsertClientProfileForFlow(t, db, pastBase-10)
			db.Exec(`INSERT INTO v2_client_flows (id, wallet_fingerprint, currency, management_code_hash, state, client_profile_id, created_at, updated_at) VALUES (?, ?, 'BTC', ?, 'form_ready', ?, ?, ?)`, fid, newID(), newID(), staleCPID, pastBase-10, pastBase-10)                                                                                                                                                                                                                                                                                            //nolint:errcheck
			db.Exec(`INSERT INTO v2_invoices (id, flow_id, status, payment_address, amount_usd_cents, amount_atomic, detection_deadline_at, detected_txid, payment_detected_at, confirmation_deadline_at, payment_txid, payment_confirmed_at, entitlement_expires_at, created_at, updated_at) VALUES (?, ?, 'confirmed', 'a', 500, 1000, ?, 'tx', ?, ?, 'tx', ?, ?, ?, ?)`, newID(), fid, pastBase+3600, pastBase, pastBase+86400, pastBase, entExp, pastBase, pastBase)                                                                                            //nolint:errcheck
			db.Exec(`INSERT INTO v2_listings (id, flow_id, city, country_code, dependency_type, help_type, urgency, languages, display_name, contact_type, contact_ciphertext, contact_nonce, contact_key_version, state, visible_until, first_published_at, last_activated_at, entitlement_expires_at, activation_count, created_at, updated_at) VALUES (?, ?, 'C', 'US', 'd', 'h', 'u', '["en"]', ?, 'telegram', ?, ?, ?, 'visible', ?, ?, ?, ?, 1, ?, ?)`, id, fid, "stale_dn_"+id[:8], ct, nh, kv, visibleUntilPast, pastBase, pastBase, entExp, pastBase, now) //nolint:errcheck
			return id
		}},
		{"finished", func() string {
			id := mustCreateVisibleListing(t, db, "US")
			db.Exec(`UPDATE v2_listings SET state='finished', visible_until=NULL WHERE id=?`, id) //nolint:errcheck
			return id
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.setup()
			_, err := svc.GetListingForPurchase(id, time.Now())
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("GetListingForPurchase(%q): got %v, want ErrNotFound", tc.name, err)
			}
		})
	}
	_ = normalized
	_ = currency
}

// Test 8: Same-country profile passes; different-country profile blocked.
func TestHelperCreate_CountryCheck(t *testing.T) {
	svc, db := newTestHelperService(t)

	// Create listing in US.
	listingID := mustCreateVisibleListing(t, db, "US")

	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	// First create a profile by making one purchase, then lock it to CA.
	profileID, _, _, err := svc.GetOrCreateProfile(currency, normalized)
	if err != nil {
		t.Fatalf("GetOrCreateProfile: %v", err)
	}

	// Lock profile to CA (different country).
	db.Exec(`UPDATE v2_helper_profiles SET country_code='CA' WHERE id=?`, profileID) //nolint:errcheck

	// Purchase listing in US → should fail country check.
	_, _, err = svc.CreatePurchase(newID(), listingID, currency, normalized, HelperInvoiceDraft{
		PaymentAddress: "payaddr", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents,
	})
	if !errors.Is(err, ErrHelperCountryMismatch) {
		t.Errorf("expected ErrHelperCountryMismatch, got %v", err)
	}

	// Lock profile to US → should succeed.
	db.Exec(`UPDATE v2_helper_profiles SET country_code='US' WHERE id=?`, profileID) //nolint:errcheck
	_, view, err := svc.CreatePurchase(newID(), listingID, currency, normalized, HelperInvoiceDraft{
		PaymentAddress: "payaddr2", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents,
	})
	if err != nil {
		t.Fatalf("same-country purchase failed: %v", err)
	}
	if view.State != HPStateAwaitingPayment {
		t.Errorf("state: got %q", view.State)
	}
}

// Test 9: Concurrent first create for one wallet → no duplicate profile/public name.
func TestHelperCreate_ConcurrentProfileCreation(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")

	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	const goroutines = 5
	errs := make([]error, goroutines)
	profiles := make([]string, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, _, _, err := svc.GetOrCreateProfile(currency, normalized)
			errs[i] = err
			profiles[i] = id
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: GetOrCreateProfile error: %v", i, err)
		}
	}

	// All should return the same profileID.
	firstID := profiles[0]
	for i, id := range profiles {
		if id != firstID {
			t.Errorf("goroutine %d returned different profile ID: %q vs %q", i, id, firstID)
		}
	}

	// Exactly one row in v2_helper_profiles for this fingerprint.
	fp := svc.helperWalletFingerprint(currency, normalized)
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_helper_profiles WHERE wallet_fingerprint=?`, fp).Scan(&count) //nolint:errcheck
	if count != 1 {
		t.Errorf("expected 1 profile row, got %d", count)
	}

	// First purchase — use a new token.
	draft := HelperInvoiceDraft{PaymentAddress: "payaddr", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, _, err1 := svc.CreatePurchase(newID(), listingID, currency, normalized, draft)
	// Second purchase — terminal state needed first. Instead just check two separate listings.
	listing2 := mustCreateVisibleListingDB(t, db, "US")
	draft2 := HelperInvoiceDraft{PaymentAddress: "payaddr2", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, _, err2 := svc.CreatePurchase(newID(), listing2, currency, normalized, draft2)
	if err1 != nil || err2 != nil {
		t.Errorf("independent purchases: err1=%v err2=%v", err1, err2)
	}
}

// Test 10: Client flow with same wallet fingerprint does NOT block Helper create.
func TestHelperCreate_ClientFlowDoesNotBlock(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")

	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	// Create a client flow with the same wallet (using client domain).
	clientSvc, _ := NewWithClock(db, testHMACKey, time.Now)
	clientFP := clientSvc.walletFingerprint(currency, normalized)

	now := time.Now().Unix()
	db.Exec(`INSERT INTO v2_client_flows (id, wallet_fingerprint, currency, management_code_hash, state, created_at, updated_at) VALUES (?, ?, 'BTC', 'codehash1', 'awaiting_payment', ?, ?)`, newID(), clientFP, now, now) //nolint:errcheck

	// Helper create should still work.
	_, _, err := svc.CreatePurchase(newID(), listingID, currency, normalized, HelperInvoiceDraft{
		PaymentAddress: "payaddr", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents,
	})
	if err != nil {
		t.Errorf("expected success, got %v", err)
	}
}

// ── 19. Country lock tests ─────────────────────────────────────────────────────

// Test 19: First successful purchase fixes country_code.
func TestHelperCountryLock_FirstPurchase(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")

	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	profileID, countryBefore, _, err := svc.GetOrCreateProfile(currency, normalized)
	if err != nil {
		t.Fatalf("GetOrCreateProfile: %v", err)
	}
	if countryBefore != nil {
		t.Errorf("new profile should have nil country_code, got %q", *countryBefore)
	}

	rawToken := newID()
	draft := HelperInvoiceDraft{PaymentAddress: "payaddr", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, view, err := svc.CreatePurchase(rawToken, listingID, currency, normalized, draft)
	if err != nil {
		t.Fatalf("CreatePurchase: %v", err)
	}

	// Simulate watcher: detect payment.
	addr2 := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	_, err = svc.RecordHelperDetection(view.PurchaseID, "txid1", []string{addr2}, 100000, time.Now())
	if err != nil {
		t.Fatalf("RecordHelperDetection: %v", err)
	}

	// Confirm.
	_, err = svc.ConfirmHelperPayment(view.PurchaseID, time.Now())
	if err != nil {
		t.Fatalf("ConfirmHelperPayment: %v", err)
	}

	// Balance check — sufficient.
	_, err = svc.RecordHelperPostPaymentBalance(view.PurchaseID, 1500.0)
	if err != nil {
		t.Fatalf("RecordHelperPostPaymentBalance: %v", err)
	}

	// Country should now be locked to US.
	var cc sql.NullString
	db.QueryRow(`SELECT country_code FROM v2_helper_profiles WHERE id=?`, profileID).Scan(&cc) //nolint:errcheck
	if !cc.Valid || cc.String != "US" {
		t.Errorf("expected country_code=US after successful purchase, got %v", cc)
	}

	// purchase_count must be 1.
	var pc int
	db.QueryRow(`SELECT purchase_count FROM v2_helper_profiles WHERE id=?`, profileID).Scan(&pc) //nolint:errcheck
	if pc != 1 {
		t.Errorf("expected purchase_count=1, got %d", pc)
	}

	_ = rawToken
}

// Test 20: Two concurrent successful purchases in different countries: exactly one CAS wins.
func TestHelperCountryLock_ConcurrentDifferentCountries(t *testing.T) {
	now := time.Now()
	fixedNow := func() time.Time { return now }

	db, _ := OpenMemory()
	defer db.Close()
	cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")

	svc1, _ := NewHelperPurchaseService(db, testHMACKey, cipher, NewRandomDisplayNameGenerator(), fixedNow)

	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	// Two listings in different countries.
	listing_us := mustCreateVisibleListingDB(t, db, "US")
	listing_ca := mustCreateVisibleListingDB(t, db, "CA")

	// Create two purchases (different listings → active purchase guard allows both).
	draft := HelperInvoiceDraft{PaymentAddress: "payaddr_us", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, viewUS, err := svc1.CreatePurchase(newID(), listing_us, currency, normalized, draft)
	if err != nil {
		t.Fatalf("CreatePurchase US: %v", err)
	}
	draft2 := HelperInvoiceDraft{PaymentAddress: "payaddr_ca", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, viewCA, err := svc1.CreatePurchase(newID(), listing_ca, currency, normalized, draft2)
	if err != nil {
		t.Fatalf("CreatePurchase CA: %v", err)
	}

	// Detect + confirm both.
	svc1.RecordHelperDetection(viewUS.PurchaseID, "txid_us", []string{addr}, 100000, now) //nolint:errcheck
	svc1.RecordHelperDetection(viewCA.PurchaseID, "txid_ca", []string{addr}, 100000, now) //nolint:errcheck
	svc1.ConfirmHelperPayment(viewUS.PurchaseID, now)                                     //nolint:errcheck
	svc1.ConfirmHelperPayment(viewCA.PurchaseID, now)                                     //nolint:errcheck

	// Use test hook to interleave: first call reads both, then one CAS happens.
	results := make([]error, 2)
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, results[0] = svc1.RecordHelperPostPaymentBalance(viewUS.PurchaseID, 2000.0)
	}()
	go func() {
		defer wg.Done()
		_, results[1] = svc1.RecordHelperPostPaymentBalance(viewCA.PurchaseID, 2000.0)
	}()
	wg.Wait()

	// Exactly one must succeed, other gets ErrHelperCountryMismatch.
	successes := 0
	mismatches := 0
	for _, e := range results {
		if e == nil {
			successes++
		} else if errors.Is(e, ErrHelperCountryMismatch) {
			mismatches++
		}
	}
	if successes != 1 {
		t.Errorf("expected exactly 1 success, got %d (errors: %v)", successes, results)
	}
	if mismatches != 1 {
		t.Errorf("expected exactly 1 mismatch, got %d", mismatches)
	}

	// purchase_count must be exactly 1.
	var profileID string
	db.QueryRow(`SELECT id FROM v2_helper_profiles WHERE wallet_fingerprint = ?`, //nolint:errcheck
		svc1.helperWalletFingerprint(currency, normalized)).Scan(&profileID)
	var pc int
	db.QueryRow(`SELECT purchase_count FROM v2_helper_profiles WHERE id=?`, profileID).Scan(&pc) //nolint:errcheck
	if pc != 1 {
		t.Errorf("expected purchase_count=1, got %d", pc)
	}
}

// mustCreateVisibleListingDB is a variant that takes a db directly (for tests creating multiple listings).
func mustCreateVisibleListingDB(t *testing.T, db *sql.DB, countryCode string) string {
	t.Helper()
	now := time.Now().Unix()
	listingID := newID()
	flowID := newID()
	entitlement := now + int64(entitlementDuration.Seconds())
	clientProfileID := mustInsertClientProfileForFlow(t, db, now)
	db.Exec(`INSERT INTO v2_client_flows (id, wallet_fingerprint, currency, management_code_hash, state, client_profile_id, created_at, updated_at) VALUES (?, ?, 'BTC', ?, 'form_ready', ?, ?, ?)`, flowID, newID(), newID(), clientProfileID, now, now)                                                                                                                                                                                  //nolint:errcheck
	db.Exec(`INSERT INTO v2_invoices (id, flow_id, status, payment_address, amount_usd_cents, amount_atomic, detection_deadline_at, detected_txid, payment_detected_at, confirmation_deadline_at, payment_txid, payment_confirmed_at, entitlement_expires_at, created_at, updated_at) VALUES (?, ?, 'confirmed', 'a', 500, 1000, ?, 'tx', ?, ?, 'tx', ?, ?, ?, ?)`, newID(), flowID, now+3600, now, now+86400, now, entitlement, now, now) //nolint:errcheck
	cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")
	ct, nh, kv, _ := cipher.Encrypt("@x", listingID, flowID, "telegram")
	db.Exec(`INSERT INTO v2_listings (id, flow_id, city, country_code, dependency_type, help_type, urgency, languages, display_name, contact_type, contact_ciphertext, contact_nonce, contact_key_version, state, visible_until, first_published_at, last_activated_at, entitlement_expires_at, activation_count, created_at, updated_at) VALUES (?, ?, 'C', ?, 'd', 'h', 'u', '["en"]', ?, 'telegram', ?, ?, ?, 'visible', ?, ?, ?, ?, 1, ?, ?)`, listingID, flowID, countryCode, "dn2_"+listingID[:8], ct, nh, kv, now+86400, now, now, entitlement, now, now) //nolint:errcheck

	// Insert active binding+destination so snapshotClientDestinationTx succeeds.
	mustInsertActiveBindingForFlow(t, db, flowID, now)

	return listingID
}

// Test 21: Purchases in different cities of same country both increment count.
func TestHelperCountryLock_SameCityDifferentPurchases(t *testing.T) {
	svc, db := newTestHelperService(t)

	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	// Two listings in US.
	l1 := mustCreateVisibleListing(t, db, "US")
	l2 := mustCreateVisibleListingDB(t, db, "US")

	doFullPurchase := func(listingID, payAddr string) {
		t.Helper()
		draft := HelperInvoiceDraft{PaymentAddress: payAddr, AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
		_, view, err := svc.CreatePurchase(newID(), listingID, currency, normalized, draft)
		if err != nil {
			t.Fatalf("CreatePurchase: %v", err)
		}
		svc.RecordHelperDetection(view.PurchaseID, newID(), []string{addr}, 100000, time.Now()) //nolint:errcheck
		svc.ConfirmHelperPayment(view.PurchaseID, time.Now())                                   //nolint:errcheck
		_, err = svc.RecordHelperPostPaymentBalance(view.PurchaseID, 1500.0)
		if err != nil {
			t.Fatalf("RecordHelperPostPaymentBalance: %v", err)
		}
	}

	doFullPurchase(l1, "payaddr_us1")
	doFullPurchase(l2, "payaddr_us2")

	fp := svc.helperWalletFingerprint(currency, normalized)
	var profileID string
	db.QueryRow(`SELECT id FROM v2_helper_profiles WHERE wallet_fingerprint = ?`, fp).Scan(&profileID) //nolint:errcheck
	var pc int
	db.QueryRow(`SELECT purchase_count FROM v2_helper_profiles WHERE id=?`, profileID).Scan(&pc) //nolint:errcheck
	if pc != 2 {
		t.Errorf("expected purchase_count=2, got %d", pc)
	}
}

// Test 22: Retry/duplicate reveal does not change country, created_at, or counters again.
func TestHelperCountryLock_RevealIdempotent(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")

	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	rawToken := newID()
	draft := HelperInvoiceDraft{PaymentAddress: "payaddr", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, view, _ := svc.CreatePurchase(rawToken, listingID, currency, normalized, draft)
	svc.RecordHelperDetection(view.PurchaseID, "txid1", []string{addr}, 100000, time.Now()) //nolint:errcheck
	svc.ConfirmHelperPayment(view.PurchaseID, time.Now())                                   //nolint:errcheck
	svc.RecordHelperPostPaymentBalance(view.PurchaseID, 1500.0)                             //nolint:errcheck

	// Capture state before reveals.
	fp := svc.helperWalletFingerprint(currency, normalized)
	var profileID string
	db.QueryRow(`SELECT id FROM v2_helper_profiles WHERE wallet_fingerprint = ?`, fp).Scan(&profileID) //nolint:errcheck
	var cc1 sql.NullString
	var createdAt1 int64
	var pc1 int
	db.QueryRow(`SELECT country_code, created_at, purchase_count FROM v2_helper_profiles WHERE id=?`, profileID).Scan(&cc1, &createdAt1, &pc1) //nolint:errcheck

	// First reveal.
	r1, err := svc.RevealHelperContact(view.PurchaseID, rawToken, normalized, currency)
	if err != nil {
		t.Fatalf("first reveal: %v", err)
	}

	// Second reveal (within receipt window).
	r2, err := svc.RevealHelperContact(view.PurchaseID, rawToken, normalized, currency)
	if err != nil {
		t.Fatalf("second reveal: %v", err)
	}

	if r1.ReceiptExpiresAt != r2.ReceiptExpiresAt {
		t.Error("repeated reveal must return same receipt_expires_at")
	}
	if r1.Contact != r2.Contact {
		t.Error("repeated reveal must return same contact")
	}

	// Verify counters unchanged after reveals.
	var cc2 sql.NullString
	var createdAt2 int64
	var pc2 int
	db.QueryRow(`SELECT country_code, created_at, purchase_count FROM v2_helper_profiles WHERE id=?`, profileID).Scan(&cc2, &createdAt2, &pc2) //nolint:errcheck

	if cc1.String != cc2.String {
		t.Error("country_code changed after reveal")
	}
	if createdAt1 != createdAt2 {
		t.Error("created_at changed after reveal")
	}
	if pc1 != pc2 {
		t.Errorf("purchase_count changed after reveal: before %d, after %d", pc1, pc2)
	}
}

// ── Task 05-FIX: New required tests ──────────────────────────────────────────

// TestHelperSchema_StrictConstraints verifies the new strict CHECK constraints
// added in Task 05-FIX for IDs, country codes, balance, and state-specific fields.
func TestHelperSchema_StrictConstraints(t *testing.T) {
	_, db := newTestHelperService(t)
	now := time.Now().Unix()

	// Helper: valid profile row prerequisite.
	validProfileID := newID()
	validFP := newID()
	if _, err := db.Exec(`INSERT INTO v2_helper_profiles
		(id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at)
		VALUES (?, ?, 'BTC', 'valid_name_x1', 0, 0, 0, ?, ?)`, validProfileID, validFP, now, now); err != nil {
		t.Fatalf("prerequisite profile insert: %v", err)
	}
	// Helper: valid listing prerequisite.
	listingID := newID()
	flowID := newID()
	schCPID := mustInsertClientProfileForFlow(t, db, now)
	if _, err := db.Exec(`INSERT INTO v2_client_flows (id, wallet_fingerprint, currency, management_code_hash, state, client_profile_id, created_at, updated_at) VALUES (?, 'fp_flow', 'BTC', 'ch1', 'form_ready', ?, ?, ?)`, flowID, schCPID, now, now); err != nil {
		t.Fatalf("prerequisite flow: %v", err)
	}
	entExp := now + int64(entitlementDuration.Seconds())
	if _, err := db.Exec(`INSERT INTO v2_invoices (id, flow_id, status, payment_address, amount_usd_cents, amount_atomic, detection_deadline_at, detected_txid, payment_detected_at, confirmation_deadline_at, payment_txid, payment_confirmed_at, entitlement_expires_at, created_at, updated_at) VALUES (?, ?, 'confirmed', 'pa', 500, 1000, ?, 'tx1', ?, ?, 'tx1', ?, ?, ?, ?)`, newID(), flowID, now+3600, now, now+86400, now, entExp, now, now); err != nil {
		t.Fatalf("prerequisite invoice: %v", err)
	}
	cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")
	ct, nh, kv, _ := cipher.Encrypt("@x", listingID, flowID, "telegram")
	if _, err := db.Exec(`INSERT INTO v2_listings (id, flow_id, city, country_code, dependency_type, help_type, urgency, languages, display_name, contact_type, contact_ciphertext, contact_nonce, contact_key_version, state, visible_until, first_published_at, last_activated_at, entitlement_expires_at, activation_count, created_at, updated_at) VALUES (?, ?, 'C', 'US', 'd', 'h', 'u', '["en"]', 'dn', 'telegram', ?, ?, ?, 'visible', ?, ?, ?, ?, 1, ?, ?)`, listingID, flowID, ct, nh, kv, now+86400, now, now, entExp, now, now); err != nil {
		t.Fatalf("prerequisite listing: %v", err)
	}

	t.Run("profile_id_must_be_64_hex", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES ('short_id', ?, 'BTC', 'test_name_s2', 0, 0, 0, ?, ?)`, newID(), now, now)
		if err == nil {
			t.Fatal("expected error for non-64-hex profile id")
		}
	})

	t.Run("wallet_fingerprint_must_be_64_hex", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, 'notHex!!', 'BTC', 'test_name_s3', 0, 0, 0, ?, ?)`, newID(), now, now)
		if err == nil {
			t.Fatal("expected error for non-hex wallet_fingerprint")
		}
	})

	t.Run("country_code_must_be_2_uppercase", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, country_code, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'test_name_s4', 'usa', 0, 0, 0, ?, ?)`, newID(), newID(), now, now)
		if err == nil {
			t.Fatal("expected error for 3-char country_code")
		}
	})

	t.Run("country_code_lowercase_rejected", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, country_code, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'test_name_s5', 'us', 0, 0, 0, ?, ?)`, newID(), newID(), now, now)
		if err == nil {
			t.Fatal("expected error for lowercase country_code")
		}
	})

	t.Run("country_code_null_ok", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'LTC', 'test_name_s6', 0, 0, 0, ?, ?)`, newID(), newID(), now, now)
		if err != nil {
			t.Fatalf("NULL country_code should be valid: %v", err)
		}
	})

	t.Run("country_code_2_uppercase_ok", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, country_code, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'test_name_s7', 'DE', 0, 0, 0, ?, ?)`, newID(), newID(), now, now)
		if err != nil {
			t.Fatalf("valid 2-char uppercase country_code: %v", err)
		}
	})

	// Purchase tests.
	validTokenHash := newID()
	validPurchaseID := newID()
	retryDeadline := now + 86400
	if _, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, balance_retry_deadline_at, last_balance_usd, last_balance_checked_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'payment_confirmed', 'US', ?, NULL, NULL, ?, ?)`,
		validPurchaseID, listingID, validProfileID, validTokenHash, retryDeadline, now, now); err != nil {
		t.Fatalf("prerequisite purchase: %v", err)
	}

	t.Run("purchase_country_code_snapshot_lowercase_rejected", func(t *testing.T) {
		// Fresh profile so partial UNIQUE does not mask the target CHECK constraint.
		pID := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'cc_snap_neg', 0, 0, 0, ?, ?)`, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, created_at, updated_at) VALUES (?, ?, ?, ?, 'awaiting_payment', 'us', ?, ?)`,
			newID(), listingID, pID, newID(), now, now)
		if err == nil {
			t.Fatal("expected error for lowercase country_code_snapshot")
		}
	})

	t.Run("paid_low_balance_requires_balance_under_1000", func(t *testing.T) {
		// paid_low_balance with balance >= 1000 must fail. Fresh profile avoids UNIQUE masking.
		pID := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'plb_neg_prof', 0, 0, 0, ?, ?)`, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, balance_retry_deadline_at, last_balance_usd, last_balance_checked_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'paid_low_balance', 'US', ?, 1000.0, ?, ?, ?)`,
			newID(), listingID, pID, newID(), retryDeadline, now, now, now)
		if err == nil {
			t.Fatal("expected error: paid_low_balance with balance >= 1000")
		}
	})

	t.Run("paid_low_balance_ok_with_999", func(t *testing.T) {
		// Use a fresh profile so the partial UNIQUE (profile, listing) is not violated.
		pID := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'ok_low_bal_prof', 0, 0, 0, ?, ?)`, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, balance_retry_deadline_at, last_balance_usd, last_balance_checked_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'paid_low_balance', 'US', ?, 999.99, ?, ?, ?)`,
			newID(), listingID, pID, newID(), retryDeadline, now, now, now)
		if err != nil {
			t.Fatalf("valid paid_low_balance: %v", err)
		}
	})

	t.Run("contact_ready_requires_balance_1000_and_contact_ready_at", func(t *testing.T) {
		// contact_ready without contact_ready_at should fail. Fresh profile avoids UNIQUE masking.
		pID := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'cr_nocat_neg', 0, 0, 0, ?, ?)`, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, balance_retry_deadline_at, last_balance_usd, last_balance_checked_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'contact_ready', 'US', ?, 1000.0, ?, ?, ?)`,
			newID(), listingID, pID, newID(), retryDeadline, now, now, now)
		if err == nil {
			t.Fatal("expected error: contact_ready without contact_ready_at")
		}
	})

	t.Run("contact_ready_with_low_balance_rejected", func(t *testing.T) {
		// contact_ready with balance < 1000 must fail. Fresh profile avoids UNIQUE masking.
		pID := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'cr_lowbal_neg', 0, 0, 0, ?, ?)`, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, balance_retry_deadline_at, last_balance_usd, last_balance_checked_at, contact_ready_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'contact_ready', 'US', ?, 999.0, ?, ?, ?, ?)`,
			newID(), listingID, pID, newID(), retryDeadline, now, now, now, now, now)
		if err == nil {
			t.Fatal("expected error: contact_ready with balance < 1000")
		}
	})

	t.Run("contact_ready_ok", func(t *testing.T) {
		// Use a fresh profile so the partial UNIQUE (profile, listing) is not violated.
		pID := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'ok_cr_prof', 0, 0, 0, ?, ?)`, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, balance_retry_deadline_at, last_balance_usd, last_balance_checked_at, contact_ready_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'contact_ready', 'US', ?, 1000.0, ?, ?, ?, ?)`,
			newID(), listingID, pID, newID(), retryDeadline, now, now, now, now, now)
		if err != nil {
			t.Fatalf("valid contact_ready: %v", err)
		}
	})

	t.Run("first_revealed_at_outside_contact_ready_rejected", func(t *testing.T) {
		// first_revealed_at set but state != contact_ready/receipt_expired.
		// Fresh profile so UNIQUE doesn't mask the CHECK constraint.
		pID := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'rev_outside_neg', 0, 0, 0, ?, ?)`, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, balance_retry_deadline_at, last_balance_usd, last_balance_checked_at, contact_ready_at, first_revealed_at, receipt_expires_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'payment_confirmed', 'US', ?, 1200.0, ?, ?, ?, ?, ?, ?)`,
			newID(), listingID, pID, newID(), retryDeadline, now, now, now, now, now+86400, now, now)
		if err == nil {
			t.Fatal("expected error: first_revealed_at in payment_confirmed state")
		}
	})

	t.Run("invoice_empty_payment_address_rejected", func(t *testing.T) {
		// Fresh profile so the purchase INSERT succeeds and only the invoice CHECK fires.
		invP := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'inv_empty_addr', 0, 0, 0, ?, ?)`, invP, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		pid := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, created_at, updated_at) VALUES (?, ?, ?, ?, 'awaiting_payment', 'US', ?, ?)`, pid, listingID, invP, newID(), now, now); dbErr != nil {
			t.Fatalf("create purchase prereq: %v", dbErr)
		}
		_, err := db.Exec(`INSERT INTO v2_helper_invoices (id, purchase_id, status, payment_address, amount_usd_cents, amount_atomic, detection_deadline_at, created_at, updated_at) VALUES (?, ?, 'pending', '', 1000, 100000, ?, ?, ?)`, newID(), pid, now+3600, now, now)
		if err == nil {
			t.Fatal("expected error for empty payment_address")
		}
	})

	t.Run("invoice_non_hex_id_rejected", func(t *testing.T) {
		// Fresh profile so the purchase INSERT succeeds and only the invoice CHECK fires.
		invP2 := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'inv_nonhex_id', 0, 0, 0, ?, ?)`, invP2, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		pid2 := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, created_at, updated_at) VALUES (?, ?, ?, ?, 'awaiting_payment', 'US', ?, ?)`, pid2, listingID, invP2, newID(), now, now); dbErr != nil {
			t.Fatalf("create purchase prereq: %v", dbErr)
		}
		_, err := db.Exec(`INSERT INTO v2_helper_invoices (id, purchase_id, status, payment_address, amount_usd_cents, amount_atomic, detection_deadline_at, created_at, updated_at) VALUES ('non_hex_id', ?, 'pending', 'payaddr', 1000, 100000, ?, ?, ?)`, pid2, now+3600, now, now)
		if err == nil {
			t.Fatal("expected error for non-hex invoice id")
		}
	})

	t.Run("negative_balance_rejected", func(t *testing.T) {
		// Fresh profile so UNIQUE doesn't mask the target balance CHECK constraint.
		pID := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'neg_bal_prof', 0, 0, 0, ?, ?)`, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, balance_retry_deadline_at, last_balance_usd, last_balance_checked_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'paid_low_balance', 'US', ?, -1.0, ?, ?, ?)`,
			newID(), listingID, pID, newID(), retryDeadline, now, now, now)
		if err == nil {
			t.Fatal("expected error for negative last_balance_usd")
		}
	})

	// ── All 8 purchase states: positive rows and key negative constraints ─────────

	t.Run("awaiting_payment_ok", func(t *testing.T) {
		pID := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'ap_ok_prof', 0, 0, 0, ?, ?)`, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, created_at, updated_at) VALUES (?, ?, ?, ?, 'awaiting_payment', 'US', ?, ?)`,
			newID(), listingID, pID, newID(), now, now)
		if err != nil {
			t.Fatalf("valid awaiting_payment: %v", err)
		}
	})

	t.Run("awaiting_payment_with_balance_deadline_rejected", func(t *testing.T) {
		pID := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'ap_bd_neg', 0, 0, 0, ?, ?)`, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, balance_retry_deadline_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'awaiting_payment', 'US', ?, ?, ?)`,
			newID(), listingID, pID, newID(), retryDeadline, now, now)
		if err == nil {
			t.Fatal("expected error: awaiting_payment with balance_retry_deadline_at set")
		}
	})

	t.Run("payment_detected_ok", func(t *testing.T) {
		pID := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'pd_ok_prof', 0, 0, 0, ?, ?)`, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, created_at, updated_at) VALUES (?, ?, ?, ?, 'payment_detected', 'US', ?, ?)`,
			newID(), listingID, pID, newID(), now, now)
		if err != nil {
			t.Fatalf("valid payment_detected: %v", err)
		}
	})

	t.Run("payment_confirmed_missing_retry_deadline_rejected", func(t *testing.T) {
		pID := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'pc_noret_neg', 0, 0, 0, ?, ?)`, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, created_at, updated_at) VALUES (?, ?, ?, ?, 'payment_confirmed', 'US', ?, ?)`,
			newID(), listingID, pID, newID(), now, now)
		if err == nil {
			t.Fatal("expected error: payment_confirmed without balance_retry_deadline_at")
		}
	})

	// invoice_expired, failed, receipt_expired are terminal: partial UNIQUE does not
	// apply, so validProfileID+listingID can be used without conflict.

	t.Run("invoice_expired_ok", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, created_at, updated_at) VALUES (?, ?, ?, ?, 'invoice_expired', 'US', ?, ?)`,
			newID(), listingID, validProfileID, newID(), now, now)
		if err != nil {
			t.Fatalf("valid invoice_expired: %v", err)
		}
	})

	t.Run("invoice_expired_with_contact_ready_at_rejected", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, contact_ready_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'invoice_expired', 'US', ?, ?, ?)`,
			newID(), listingID, validProfileID, newID(), now, now, now)
		if err == nil {
			t.Fatal("expected error: invoice_expired with contact_ready_at set")
		}
	})

	t.Run("failed_ok", func(t *testing.T) {
		// failed allows retry+balance snapshot (from confirmed state) but no contact/reveal.
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, balance_retry_deadline_at, last_balance_usd, last_balance_checked_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'failed', 'US', ?, 500.0, ?, ?, ?)`,
			newID(), listingID, validProfileID, newID(), retryDeadline, now, now, now)
		if err != nil {
			t.Fatalf("valid failed with retry+balance snapshot: %v", err)
		}
	})

	t.Run("failed_with_contact_ready_at_rejected", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, contact_ready_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'failed', 'US', ?, ?, ?)`,
			newID(), listingID, validProfileID, newID(), now, now, now)
		if err == nil {
			t.Fatal("expected error: failed with contact_ready_at set")
		}
	})

	t.Run("receipt_expired_ok_unrevealed", func(t *testing.T) {
		// receipt_expired with contact_ready_at + balance/retry but no reveal pair.
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, balance_retry_deadline_at, last_balance_usd, last_balance_checked_at, contact_ready_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'receipt_expired', 'US', ?, 1200.0, ?, ?, ?, ?)`,
			newID(), listingID, validProfileID, newID(), retryDeadline, now, now, now, now, now)
		if err != nil {
			t.Fatalf("valid receipt_expired (unrevealed): %v", err)
		}
	})

	t.Run("receipt_expired_ok_revealed", func(t *testing.T) {
		// receipt_expired where the receipt was revealed: both first_revealed_at and
		// receipt_expires_at must be set with exact equation (expiry = reveal + 86400).
		revealTime := now - 86401
		contactTime := revealTime - 1 // contact_ready_at must be <= first_revealed_at
		expiry := revealTime + 86400  // exact equation
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, balance_retry_deadline_at, last_balance_usd, last_balance_checked_at, contact_ready_at, first_revealed_at, receipt_expires_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'receipt_expired', 'US', ?, 1200.0, ?, ?, ?, ?, ?, ?)`,
			newID(), listingID, validProfileID, newID(), retryDeadline, now, contactTime, revealTime, expiry, now, now)
		if err != nil {
			t.Fatalf("valid receipt_expired (revealed): %v", err)
		}
	})

	t.Run("receipt_expired_missing_contact_ready_at_rejected", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, balance_retry_deadline_at, last_balance_usd, last_balance_checked_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'receipt_expired', 'US', ?, 1200.0, ?, ?, ?)`,
			newID(), listingID, validProfileID, newID(), retryDeadline, now, now, now)
		if err == nil {
			t.Fatal("expected error: receipt_expired without contact_ready_at")
		}
	})

	// ── Partial UNIQUE: terminal row does not block a new non-terminal row ────────

	t.Run("partial_unique_terminal_does_not_block_new", func(t *testing.T) {
		pID := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'uniq_term_prof', 0, 0, 0, ?, ?)`, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		// Terminal row first.
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, created_at, updated_at) VALUES (?, ?, ?, ?, 'invoice_expired', 'US', ?, ?)`,
			newID(), listingID, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("insert terminal purchase: %v", dbErr)
		}
		// Non-terminal for same (profile, listing) must succeed.
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, created_at, updated_at) VALUES (?, ?, ?, ?, 'awaiting_payment', 'US', ?, ?)`,
			newID(), listingID, pID, newID(), now, now)
		if err != nil {
			t.Fatalf("terminal should not block non-terminal: %v", err)
		}
	})

	// ── Exact deadline equations ──────────────────────────────────────────────────

	t.Run("receipt_expires_at_wrong_equation_rejected", func(t *testing.T) {
		// contact_ready with first_revealed_at+86401 (off by 1) must fail.
		pID := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'eq_rev_neg', 0, 0, 0, ?, ?)`, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		revealTime := now - 100
		wrongExpiry := revealTime + 86401 // off by 1
		_, err := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, balance_retry_deadline_at, last_balance_usd, last_balance_checked_at, contact_ready_at, first_revealed_at, receipt_expires_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'contact_ready', 'US', ?, 1500.0, ?, ?, ?, ?, ?, ?)`,
			newID(), listingID, pID, newID(), retryDeadline, now, revealTime-1, revealTime, wrongExpiry, now, now)
		if err == nil {
			t.Fatal("expected error: receipt_expires_at != first_revealed_at + 86400")
		}
	})

	t.Run("invoice_confirmation_deadline_wrong_equation_rejected", func(t *testing.T) {
		// payment_detected invoice where confirmation_deadline_at = payment_detected_at+86401.
		pID := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'eq_conf_neg', 0, 0, 0, ?, ?)`, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		eqPurchaseID := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, created_at, updated_at) VALUES (?, ?, ?, ?, 'payment_detected', 'US', ?, ?)`,
			eqPurchaseID, listingID, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("insert purchase: %v", dbErr)
		}
		_, err := db.Exec(`INSERT INTO v2_helper_invoices (id, purchase_id, status, payment_address, amount_usd_cents, amount_atomic, detection_deadline_at, detected_txid_hash, payment_detected_at, confirmation_deadline_at, created_at, updated_at) VALUES (?, ?, 'payment_detected', 'payaddr', 1000, 100000, ?, ?, ?, ?, ?, ?)`,
			newID(), eqPurchaseID, now+3600, strings.Repeat("a", 64), now, now+86401, now, now)
		if err == nil {
			t.Fatal("expected error: confirmation_deadline_at != payment_detected_at + 86400")
		}
	})

	t.Run("invoice_confirmed_at_before_payment_detected_rejected", func(t *testing.T) {
		// confirmed invoice where confirmed_at < payment_detected_at.
		pID := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_profiles (id, wallet_fingerprint, currency, public_name, purchase_count, positive_count, negative_count, created_at, updated_at) VALUES (?, ?, 'BTC', 'eq_conf2_neg', 0, 0, 0, ?, ?)`, pID, newID(), now, now); dbErr != nil {
			t.Fatalf("create profile: %v", dbErr)
		}
		eqPurchaseID2 := newID()
		if _, dbErr := db.Exec(`INSERT INTO v2_helper_purchases (id, listing_id, helper_profile_id, browser_token_hash, state, country_code_snapshot, balance_retry_deadline_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'payment_confirmed', 'US', ?, ?, ?)`,
			eqPurchaseID2, listingID, pID, newID(), retryDeadline, now, now); dbErr != nil {
			t.Fatalf("insert purchase: %v", dbErr)
		}
		_, err := db.Exec(`INSERT INTO v2_helper_invoices (id, purchase_id, status, payment_address, amount_usd_cents, amount_atomic, detection_deadline_at, detected_txid_hash, payment_detected_at, confirmation_deadline_at, confirmed_at, created_at, updated_at) VALUES (?, ?, 'confirmed', 'payaddr', 1000, 100000, ?, ?, ?, ?, ?, ?, ?)`,
			newID(), eqPurchaseID2, now+3600, strings.Repeat("b", 64), now, now+86400, now-1, now, now)
		if err == nil {
			t.Fatal("expected error: confirmed_at < payment_detected_at")
		}
	})
}

// TestHelperCreate_NoOrphanRowsOnFailure verifies that no profile/purchase/invoice
// rows are created when precheck or issuer fails.
func TestHelperCreate_NoOrphanRowsOnFailure(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")

	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	countRows := func(t *testing.T) (profiles, purchases, invoices int) {
		t.Helper()
		db.QueryRow(`SELECT COUNT(*) FROM v2_helper_profiles`).Scan(&profiles)   //nolint:errcheck
		db.QueryRow(`SELECT COUNT(*) FROM v2_helper_purchases`).Scan(&purchases) //nolint:errcheck
		db.QueryRow(`SELECT COUNT(*) FROM v2_helper_invoices`).Scan(&invoices)   //nolint:errcheck
		return
	}

	t.Run("invalid_draft_no_rows", func(t *testing.T) {
		p0, pu0, i0 := countRows(t)
		// Bad draft: amount_usd_cents != 1000.
		_, _, err := svc.CreatePurchase(newID(), listingID, currency, normalized, HelperInvoiceDraft{
			PaymentAddress: "payaddr", AmountAtomic: 100000, AmountUSDCents: 500,
		})
		if err == nil {
			t.Fatal("expected error for bad draft")
		}
		p1, pu1, i1 := countRows(t)
		if p1 != p0 || pu1 != pu0 || i1 != i0 {
			t.Errorf("rows leaked: profiles %d→%d, purchases %d→%d, invoices %d→%d", p0, p1, pu0, pu1, i0, i1)
		}
	})

	t.Run("invisible_listing_no_rows", func(t *testing.T) {
		p0, pu0, i0 := countRows(t)
		_, _, err := svc.CreatePurchase(newID(), newID()+"_missing", currency, normalized, HelperInvoiceDraft{
			PaymentAddress: "payaddr", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents,
		})
		if err == nil {
			t.Fatal("expected error for missing listing")
		}
		p1, pu1, i1 := countRows(t)
		if p1 != p0 || pu1 != pu0 || i1 != i0 {
			t.Errorf("rows leaked: profiles %d→%d, purchases %d→%d, invoices %d→%d", p0, p1, pu0, pu1, i0, i1)
		}
	})

	t.Run("country_mismatch_no_new_purchase", func(t *testing.T) {
		// Create profile locked to "GB" first.
		gbAddr := "LM2WMpR1Rp6j3Sa59cMXMs1SPzj9eXpGc1"
		gbNorm, gbCurr, _ := validateAndNormalizeAddress(gbAddr)
		gbListingID := mustCreateVisibleListing(t, db, "GB")
		_, _, err := svc.CreatePurchase(newID(), gbListingID, gbCurr, gbNorm, HelperInvoiceDraft{
			PaymentAddress: "gbpay", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents,
		})
		if err != nil {
			t.Fatalf("setup GB purchase: %v", err)
		}
		// Drive to contact_ready to lock country.
		var gbPurchaseID string
		fp := svc.helperWalletFingerprint(gbCurr, gbNorm)
		db.QueryRow(`SELECT p.id FROM v2_helper_purchases p JOIN v2_helper_profiles hp ON hp.id=p.helper_profile_id WHERE hp.wallet_fingerprint=? ORDER BY p.created_at DESC LIMIT 1`, fp).Scan(&gbPurchaseID) //nolint:errcheck
		svc.RecordHelperDetection(gbPurchaseID, "txgb1", []string{gbAddr}, 100000, time.Now())                                                                                                                 //nolint:errcheck
		svc.ConfirmHelperPayment(gbPurchaseID, time.Now())                                                                                                                                                     //nolint:errcheck
		svc.RecordHelperPostPaymentBalance(gbPurchaseID, 1500.0)                                                                                                                                               //nolint:errcheck

		// Now try US listing with GB wallet — should conflict.
		p0, pu0, i0 := countRows(t)
		_, _, err = svc.CreatePurchase(newID(), listingID, gbCurr, gbNorm, HelperInvoiceDraft{
			PaymentAddress: "uspay", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents,
		})
		if !errors.Is(err, ErrHelperCountryMismatch) {
			t.Fatalf("expected ErrHelperCountryMismatch, got: %v", err)
		}
		p1, pu1, i1 := countRows(t)
		// Profile count may be same (profile already exists), but no new purchase or invoice.
		if pu1 != pu0 || i1 != i0 {
			t.Errorf("purchase/invoice leaked: purchases %d→%d, invoices %d→%d", pu0, pu1, i0, i1)
		}
		_ = p0
		_ = p1
	})
}

// TestHelperCreate_Idempotent verifies the browser-supplied purchase_token
// makes create idempotent: same token+wallet+listing returns same purchase.
func TestHelperCreate_Idempotent(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")

	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	rawToken := newID() // 64 lowercase hex
	draft := HelperInvoiceDraft{PaymentAddress: "payaddr1", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}

	// First create.
	isNew1, v1, err := svc.CreatePurchase(rawToken, listingID, currency, normalized, draft)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if !isNew1 {
		t.Fatal("first create should return isNew=true")
	}

	// Idempotent retry with same token — even different draft (issuer would normally return different address).
	draft2 := HelperInvoiceDraft{PaymentAddress: "differentaddr", AmountAtomic: 200000, AmountUSDCents: helperInvoiceUSDCents}
	isNew2, v2, err := svc.CreatePurchase(rawToken, listingID, currency, normalized, draft2)
	if err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if isNew2 {
		t.Fatal("idempotent retry should return isNew=false")
	}
	if v1.PurchaseID != v2.PurchaseID {
		t.Errorf("purchase ID changed: %s → %s", v1.PurchaseID, v2.PurchaseID)
	}
	// Original payment address preserved (not overwritten by new draft).
	if v2.PaymentAddress != "payaddr1" {
		t.Errorf("payment address changed on retry: %s", v2.PaymentAddress)
	}

	// Exactly one profile, one purchase, one invoice.
	var nP, nPu, nI int
	db.QueryRow(`SELECT COUNT(*) FROM v2_helper_profiles`).Scan(&nP)   //nolint:errcheck
	db.QueryRow(`SELECT COUNT(*) FROM v2_helper_purchases`).Scan(&nPu) //nolint:errcheck
	db.QueryRow(`SELECT COUNT(*) FROM v2_helper_invoices`).Scan(&nI)   //nolint:errcheck
	if nP != 1 || nPu != 1 || nI != 1 {
		t.Errorf("expected 1/1/1 rows, got profiles=%d purchases=%d invoices=%d", nP, nPu, nI)
	}

	// Same token + different wallet → ErrHelperNotFound.
	otherAddr := "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"
	otherNorm, otherCurr, _ := validateAndNormalizeAddress(otherAddr)
	_, _, err = svc.CreatePurchase(rawToken, listingID, otherCurr, otherNorm, draft)
	if !errors.Is(err, ErrHelperNotFound) {
		t.Fatalf("expected ErrHelperNotFound for different wallet, got: %v", err)
	}

	// Different token → active purchase guard → ErrHelperDuplicateActivePurchase.
	newToken := newID()
	_, _, err = svc.CreatePurchase(newToken, listingID, currency, normalized, draft)
	if !errors.Is(err, ErrHelperDuplicateActivePurchase) {
		t.Fatalf("expected ErrHelperDuplicateActivePurchase for different token, got: %v", err)
	}
}

// TestHelperExpiry_AtomicRollback verifies that ExpireHelperInvoice rolls back
// the invoice update if the purchase update fails (no split-brain).
func TestHelperExpiry_AtomicRollback(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")

	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	rawToken := newID()
	draft := HelperInvoiceDraft{PaymentAddress: "payaddr", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, view, err := svc.CreatePurchase(rawToken, listingID, currency, normalized, draft)
	if err != nil {
		t.Fatalf("CreatePurchase: %v", err)
	}

	// Install trigger that blocks the purchase state update.
	if _, err := db.Exec(`CREATE TRIGGER block_invoice_expired
		BEFORE UPDATE ON v2_helper_purchases
		WHEN NEW.state = 'invoice_expired'
		BEGIN SELECT RAISE(ABORT, 'blocked for rollback test'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	// Expire with a future "now" past the detection deadline.
	expireAt := view.DetectionDeadlineAt.Add(time.Second)
	_, err = svc.ExpireHelperInvoice(view.PurchaseID, expireAt)
	if err == nil {
		t.Fatal("expected error from trigger-blocked purchase update")
	}

	// Invoice must still be 'pending' (not 'expired') — rollback worked.
	var invStatus string
	if scanErr := db.QueryRow(`SELECT status FROM v2_helper_invoices WHERE purchase_id = ?`, view.PurchaseID).Scan(&invStatus); scanErr != nil {
		t.Fatalf("scan invoice status: %v", scanErr)
	}
	if invStatus != HPInvoicePending {
		t.Errorf("invoice status should still be 'pending' after rollback, got: %s", invStatus)
	}

	// Purchase must still be 'awaiting_payment'.
	var pState string
	if scanErr := db.QueryRow(`SELECT state FROM v2_helper_purchases WHERE id = ?`, view.PurchaseID).Scan(&pState); scanErr != nil {
		t.Fatalf("scan purchase state: %v", scanErr)
	}
	if pState != HPStateAwaitingPayment {
		t.Errorf("purchase state should still be 'awaiting_payment' after rollback, got: %s", pState)
	}
}

// TestHelperCountryCASLoser_AtomicFailed verifies that when the country CAS fails
// (another country won), the purchase atomically transitions to 'failed' in the same
// transaction, contact_ready_at is NULL, and purchase_count is not incremented.
func TestHelperCountryCASLoser_AtomicFailed(t *testing.T) {
	svc, db := newTestHelperService(t)
	usListing := mustCreateVisibleListing(t, db, "US")

	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	// Create and drive purchase to payment_confirmed.
	rawToken := newID()
	draft := HelperInvoiceDraft{PaymentAddress: "payaddr", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, view, err := svc.CreatePurchase(rawToken, usListing, currency, normalized, draft)
	if err != nil {
		t.Fatalf("CreatePurchase: %v", err)
	}
	svc.RecordHelperDetection(view.PurchaseID, "txid1", []string{addr}, 100000, time.Now()) //nolint:errcheck
	svc.ConfirmHelperPayment(view.PurchaseID, time.Now())                                   //nolint:errcheck

	// Directly lock the profile to a DIFFERENT country (GB) before balance check.
	fp := svc.helperWalletFingerprint(currency, normalized)
	if _, err := db.Exec(`UPDATE v2_helper_profiles SET country_code = 'GB' WHERE wallet_fingerprint = ?`, fp); err != nil {
		t.Fatalf("lock profile to GB: %v", err)
	}

	// Now RecordHelperPostPaymentBalance with balance >= 1000 — country CAS must fail.
	_, err = svc.RecordHelperPostPaymentBalance(view.PurchaseID, 1200.0)
	if !errors.Is(err, ErrHelperCountryMismatch) {
		t.Fatalf("expected ErrHelperCountryMismatch, got: %v", err)
	}

	// Purchase must be 'failed', not payment_confirmed.
	var pState string
	var contactReadyAt sql.NullInt64
	db.QueryRow(`SELECT state, contact_ready_at FROM v2_helper_purchases WHERE id = ?`, view.PurchaseID).Scan(&pState, &contactReadyAt) //nolint:errcheck
	if pState != HPStateFailed {
		t.Errorf("purchase should be 'failed', got: %s", pState)
	}
	if contactReadyAt.Valid {
		t.Error("contact_ready_at must be NULL for failed purchase")
	}

	// purchase_count must be 0 (CAS lost, count not incremented).
	var pc int
	db.QueryRow(`SELECT purchase_count FROM v2_helper_profiles WHERE wallet_fingerprint = ?`, fp).Scan(&pc) //nolint:errcheck
	if pc != 0 {
		t.Errorf("purchase_count should be 0, got: %d", pc)
	}

	// LoadWatchablePurchases must not include this failed purchase.
	watchable, err := svc.LoadWatchablePurchases()
	if err != nil {
		t.Fatalf("LoadWatchablePurchases: %v", err)
	}
	for _, w := range watchable {
		if w.PurchaseID == view.PurchaseID {
			t.Error("failed purchase must not be in watchable list")
		}
	}
}

// TestHelperReveal_DeterministicCASMiss verifies that RevealHelperContact
// correctly handles a concurrent first-reveal race: when first_revealed_at is
// already set (winner wrote before us), it reads the winner's expiry and returns
// it without error. SQLite serialises writes so we inject the winner row directly
// before calling RevealHelperContact, simulating the "loser" code path.
// TestHelperReveal_DeterministicCASMiss verifies the CAS-miss branch of
// RevealHelperContact using a tx-aware test hook. The hook writes
// first_revealed_at + receipt_expires_at inside the SAME transaction and
// before the CAS UPDATE, so the UPDATE sees RowsAffected==0. The service
// must then re-read and return the winner's exact expiry — not a zero time.
// This is the only valid CAS-miss test: pre-injecting the row before calling
// RevealHelperContact only exercises the "already revealed" early-exit path.
func TestHelperReveal_DeterministicCASMiss(t *testing.T) {
	svc, db := newTestHelperService(t)
	listingID := mustCreateVisibleListing(t, db, "US")

	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	normalized, currency, _ := validateAndNormalizeAddress(addr)

	rawToken := newID()
	draft := HelperInvoiceDraft{PaymentAddress: "payaddr", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
	_, view, err := svc.CreatePurchase(rawToken, listingID, currency, normalized, draft)
	if err != nil {
		t.Fatalf("CreatePurchase: %v", err)
	}
	svc.RecordHelperDetection(view.PurchaseID, "txid1", []string{addr}, 100000, time.Now()) //nolint:errcheck
	svc.ConfirmHelperPayment(view.PurchaseID, time.Now())                                   //nolint:errcheck
	svc.RecordHelperPostPaymentBalance(view.PurchaseID, 1500.0)                             //nolint:errcheck

	// Install the tx-aware hook. It fires inside RevealHelperContact's transaction,
	// BEFORE the CAS UPDATE, writing first_revealed_at via the SAME tx. This makes
	// the CAS UPDATE find RowsAffected==0 deterministically.
	fixedRevealTime := time.Now().Unix()
	fixedExpiry := fixedRevealTime + int64(confirmationWindow.Seconds())
	var hookCalled bool

	svc._testRevealHook = func(tx *sql.Tx, purchaseID string) error {
		hookCalled = true
		_, execErr := tx.Exec(
			`UPDATE v2_helper_purchases SET first_revealed_at = ?, receipt_expires_at = ?, updated_at = ? WHERE id = ? AND first_revealed_at IS NULL`,
			fixedRevealTime, fixedExpiry, fixedRevealTime, purchaseID,
		)
		return execErr
	}
	defer func() { svc._testRevealHook = nil }()

	// RevealHelperContact enters the first-reveal branch (row not yet revealed),
	// fires the hook (CAS miss), then re-reads and returns the winner's expiry.
	result, err := svc.RevealHelperContact(view.PurchaseID, rawToken, normalized, currency)
	if err != nil {
		t.Fatalf("RevealHelperContact with tx-aware CAS miss: %v", err)
	}
	if !hookCalled {
		t.Error("_testRevealHook was not called: CAS miss was not exercised")
	}
	if result.ReceiptExpiresAt.IsZero() {
		t.Error("receipt expiry must not be zero on CAS miss")
	}
	if result.ReceiptExpiresAt.Unix() != fixedExpiry {
		t.Errorf("expiry mismatch: want %d (winner's), got %d", fixedExpiry, result.ReceiptExpiresAt.Unix())
	}
	if result.Contact == "" {
		t.Error("contact must be non-empty on CAS miss")
	}
}

// TestHelperReveal_RealCipherBothTypes verifies real AES-GCM decrypt for both
// telegram and signal contact types, with AAD = listingID + flowID + contactType.
func TestHelperReveal_RealCipherBothTypes(t *testing.T) {
	for _, ctType := range []string{"telegram", "signal"} {
		t.Run(ctType, func(t *testing.T) {
			svc, db := newTestHelperService(t)

			contact := "@testcontact_" + ctType
			now := time.Now().Unix()
			listingID := newID()
			flowID := newID()

			// Build listing with this contact type.
			cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")
			ctHex, nonceHex, keyVer, err := cipher.Encrypt(contact, listingID, flowID, ctType)
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}

			ctCPID := mustInsertClientProfileForFlow(t, db, now)
			if _, err := db.Exec(`INSERT INTO v2_client_flows (id, wallet_fingerprint, currency, management_code_hash, state, client_profile_id, created_at, updated_at) VALUES (?, 'fp_flow_ct', 'BTC', 'ch1', 'form_ready', ?, ?, ?)`, flowID, ctCPID, now, now); err != nil {
				t.Fatalf("flow: %v", err)
			}
			entExp := now + int64(entitlementDuration.Seconds())
			if _, err := db.Exec(`INSERT INTO v2_invoices (id, flow_id, status, payment_address, amount_usd_cents, amount_atomic, detection_deadline_at, detected_txid, payment_detected_at, confirmation_deadline_at, payment_txid, payment_confirmed_at, entitlement_expires_at, created_at, updated_at) VALUES (?, ?, 'confirmed', 'pa', 500, 1000, ?, 'tx1', ?, ?, 'tx1', ?, ?, ?, ?)`, newID(), flowID, now+3600, now, now+86400, now, entExp, now, now); err != nil {
				t.Fatalf("invoice: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO v2_listings (id, flow_id, city, country_code, dependency_type, help_type, urgency, languages, display_name, contact_type, contact_ciphertext, contact_nonce, contact_key_version, state, visible_until, first_published_at, last_activated_at, entitlement_expires_at, activation_count, created_at, updated_at) VALUES (?, ?, 'C', 'US', 'd', 'h', 'u', '["en"]', 'dn', ?, ?, ?, ?, 'visible', ?, ?, ?, ?, 1, ?, ?)`, listingID, flowID, ctType, ctHex, nonceHex, keyVer, now+86400, now, now, entExp, now, now); err != nil {
				t.Fatalf("listing: %v", err)
			}

			// Insert active binding+destination so snapshotClientDestinationTx succeeds.
			mustInsertActiveBindingForFlow(t, db, flowID, now)

			addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
			normalized, currency, _ := validateAndNormalizeAddress(addr)

			rawToken := newID()
			draft := HelperInvoiceDraft{PaymentAddress: "payaddr_ct", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
			_, view, err := svc.CreatePurchase(rawToken, listingID, currency, normalized, draft)
			if err != nil {
				t.Fatalf("CreatePurchase: %v", err)
			}
			svc.RecordHelperDetection(view.PurchaseID, "txid1ct", []string{addr}, 100000, time.Now()) //nolint:errcheck
			svc.ConfirmHelperPayment(view.PurchaseID, time.Now())                                     //nolint:errcheck
			svc.RecordHelperPostPaymentBalance(view.PurchaseID, 1200.0)                               //nolint:errcheck

			result, err := svc.RevealHelperContact(view.PurchaseID, rawToken, normalized, currency)
			if err != nil {
				t.Fatalf("RevealHelperContact: %v", err)
			}
			if result.Contact != contact {
				t.Errorf("contact mismatch: want %q, got %q", contact, result.Contact)
			}
			if result.ContactType != ctType {
				t.Errorf("contact_type mismatch: want %q, got %q", ctType, result.ContactType)
			}
		})
	}
}
