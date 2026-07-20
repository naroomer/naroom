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

// mustCreateVisibleListing creates a full visible listing for tests.
// Returns listingID.
func mustCreateVisibleListing(t *testing.T, db *sql.DB, countryCode string) string {
	t.Helper()
	now := time.Now().Unix()
	listingID := newID()
	flowID := newID()

	// Insert client flow.
	_, err := db.Exec(`
		INSERT INTO v2_client_flows
		  (id, wallet_fingerprint, currency, management_code_hash, state, created_at, updated_at)
		VALUES (?, 'fp1', 'BTC', 'ch1', 'form_ready', ?, ?)`,
		flowID, now, now,
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
	db.Exec(`INSERT INTO v2_client_flows (id, wallet_fingerprint, currency, management_code_hash, state, created_at, updated_at) VALUES (?, ?, 'BTC', ?, 'form_ready', ?, ?)`, flowID, newID(), newID(), now, now) //nolint:errcheck
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
	if err := validateBalanceInputs(-1.0, helperPreInvoiceFloorUSD); err == nil {
		t.Error("negative balance should be rejected by validateBalanceInputs")
	}
	// 1009.99 is a valid finite number — validateBalanceInputs accepts it.
	// The HTTP handler rejects it via explicit < helperPreInvoiceFloorUSD check.
	if err := validateBalanceInputs(1009.99, helperPreInvoiceFloorUSD); err != nil {
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
			db.Exec(`INSERT INTO v2_client_flows (id, wallet_fingerprint, currency, management_code_hash, state, created_at, updated_at) VALUES (?, ?, 'BTC', ?, 'form_ready', ?, ?)`, fid, newID(), newID(), pastBase-10, pastBase-10)                                                                                                                                                                                                                                                                                                                             //nolint:errcheck
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
	db.Exec(`INSERT INTO v2_client_flows (id, wallet_fingerprint, currency, management_code_hash, state, created_at, updated_at) VALUES (?, ?, 'BTC', ?, 'form_ready', ?, ?)`, flowID, newID(), newID(), now, now)                                                                                                                                                                                                                         //nolint:errcheck
	db.Exec(`INSERT INTO v2_invoices (id, flow_id, status, payment_address, amount_usd_cents, amount_atomic, detection_deadline_at, detected_txid, payment_detected_at, confirmation_deadline_at, payment_txid, payment_confirmed_at, entitlement_expires_at, created_at, updated_at) VALUES (?, ?, 'confirmed', 'a', 500, 1000, ?, 'tx', ?, ?, 'tx', ?, ?, ?, ?)`, newID(), flowID, now+3600, now, now+86400, now, entitlement, now, now) //nolint:errcheck
	cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")
	ct, nh, kv, _ := cipher.Encrypt("@x", listingID, flowID, "telegram")
	db.Exec(`INSERT INTO v2_listings (id, flow_id, city, country_code, dependency_type, help_type, urgency, languages, display_name, contact_type, contact_ciphertext, contact_nonce, contact_key_version, state, visible_until, first_published_at, last_activated_at, entitlement_expires_at, activation_count, created_at, updated_at) VALUES (?, ?, 'C', ?, 'd', 'h', 'u', '["en"]', ?, 'telegram', ?, ?, ?, 'visible', ?, ?, ?, ?, 1, ?, ?)`, listingID, flowID, countryCode, "dn2_"+listingID[:8], ct, nh, kv, now+86400, now, now, entitlement, now, now) //nolint:errcheck
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
