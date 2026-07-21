package v2

// Test matrix for Task 04 — 35 scenarios + WatcherPollInterval.
// All tests use injectable clock, deterministic fakes, in-memory SQLite.
// 0 SKIP, 0 TCP, 0 real sleeps.

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── Fakes ────────────────────────────────────────────────────────────────────

type fakeContactValidator struct{}

func (f *fakeContactValidator) ValidateContact(ct, raw string) (string, error) {
	if raw == "" {
		return "", errors.New("empty contact")
	}
	return "normalized_" + raw, nil
}

type fakeDisplayNameGenerator struct{ name string }

func (f *fakeDisplayNameGenerator) Generate() (string, error) {
	if f.name != "" {
		return f.name, nil
	}
	return "calm_river_07", nil
}

var testAESKey = func() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = 0x42
	}
	return k
}()

// newBindingRef returns a random opaque binding ref.
// Format: bnd_ + 32 lowercase hex chars.
func newBindingRef() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("v2: crypto/rand failed: " + err.Error())
	}
	return "bnd_" + hex.EncodeToString(b)
}

// ── Test helpers ──────────────────────────────────────────────────────────────

func newTestListingService(t *testing.T, nowFn func() time.Time) (*ListingService, *Service, *sql.DB) {
	t.Helper()
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if nowFn == nil {
		nowFn = time.Now
	}
	svc, err := NewWithClock(db, testHMACKey, nowFn)
	if err != nil {
		t.Fatalf("NewWithClock: %v", err)
	}
	cipher, err := NewAESGCMContactCipher(testAESKey, "v1")
	if err != nil {
		t.Fatalf("NewAESGCMContactCipher: %v", err)
	}
	names := &fakeDisplayNameGenerator{}
	cv := &fakeContactValidator{}
	ls, err := NewListingService(svc, cipher, names, cv)
	if err != nil {
		t.Fatalf("NewListingService: %v", err)
	}
	return ls, svc, db
}

// makeFormReadyFlow creates a confirmed, form_ready flow and returns rawCode + flowID.
func makeFormReadyFlow(t *testing.T, svc *Service, walletAddr, currency string) (rawCode string, flowID string) {
	t.Helper()
	rawCode, fv, err := svc.CreatePaymentIntent(walletAddr, currency, validDraft())
	if err != nil {
		t.Fatalf("CreatePaymentIntent: %v", err)
	}
	now := svc.now()
	_, err = svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_"+newID()[:8],
		[]string{walletAddr}, fv.AmountAtomic, now)
	if err != nil {
		t.Fatalf("RecordPaymentDetected: %v", err)
	}
	_, err = svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, now)
	if err != nil {
		t.Fatalf("ConfirmPayment: %v", err)
	}
	_, err = svc.RecordPostPaymentBalance(fv.FlowID, 150.0, hardFloorUSD)
	if err != nil {
		t.Fatalf("RecordPostPaymentBalance: %v", err)
	}
	return rawCode, fv.FlowID
}

// attachTestBinding attaches a test binding to the flow using a valid opaque ref.
// verifiedAt uses the service clock so that fake-clock tests stay internally consistent.
// It also inserts a stub destination row so FirstPublish/Reactivate can update it.
func attachTestBinding(t *testing.T, ls *ListingService, flowID string) string {
	t.Helper()
	now := ls.now()
	ref := newBindingRef()
	if err := ls.attachReadyBinding(flowID, ref, now, now.Add(10*time.Minute)); err != nil {
		t.Fatalf("attachTestBinding: %v", err)
	}
	db := ls.svc.db
	_, err := db.Exec(`INSERT INTO v2_telegram_destinations
		(binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at)
		VALUES (?, 'stub_ct', 'stub_nonce', 'test_v1', ?, ?)`,
		ref, now.Unix(), now.Add(10*time.Minute).Unix())
	if err != nil {
		t.Fatalf("attachTestBinding: insert destination: %v", err)
	}
	return ref
}

// attachNextWindowBinding attaches a ready binding for the next window (after FirstPublish).
// It also inserts a stub destination row so Reactivate can update it.
func attachNextWindowBinding(t *testing.T, ls *ListingService, flowID string) string {
	t.Helper()
	now := ls.now()
	ref := newBindingRef()
	if err := ls.attachReadyBinding(flowID, ref, now, now.Add(10*time.Minute)); err != nil {
		t.Fatalf("attachNextWindowBinding: %v", err)
	}
	db := ls.svc.db
	_, err := db.Exec(`INSERT INTO v2_telegram_destinations
		(binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at)
		VALUES (?, 'stub_ct', 'stub_nonce', 'test_v1', ?, ?)`,
		ref, now.Unix(), now.Add(10*time.Minute).Unix())
	if err != nil {
		t.Fatalf("attachNextWindowBinding: insert destination: %v", err)
	}
	return ref
}

func validListingInput() ListingInput {
	return ListingInput{
		City: "tbilisi", DependencyType: "alcohol", HelpType: "crisis",
		Urgency: "urgent", Languages: []string{"en", "ru"},
		ContactType: "telegram", RawContact: "@testuser",
	}
}

// ── Schema tests ──────────────────────────────────────────────────────────────

func TestListingSchemaIdempotent(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	defer db.Close()
	if err = ApplySchema(db); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	// Both tables must exist.
	for _, tbl := range []string{"v2_client_notification_bindings", "v2_listings"} {
		var n int
		db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n) //nolint:errcheck
		if n != 1 {
			t.Errorf("table %q not found after double apply", tbl)
		}
	}
}

func TestListingOnePerFlow(t *testing.T) {
	ls, svc, db := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)

	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Fatalf("first publish: %v", err)
	}

	// Direct INSERT must fail on UNIQUE(flow_id).
	_, err := db.Exec(`INSERT INTO v2_listings
		(id,flow_id,city,country_code,dependency_type,help_type,urgency,languages,
		 display_name,contact_type,contact_ciphertext,contact_nonce,contact_key_version,
		 state,visible_until,first_published_at,last_activated_at,entitlement_expires_at,
		 activation_count,created_at,updated_at)
		VALUES ('x',?,'tbilisi','GE','alcohol','crisis','urgent','["en"]',
		 'n','telegram','ct','nc','v1','visible',9999999999,1,1,9999999999,1,1,1)`, flowID)
	if err == nil {
		t.Error("expected UNIQUE violation on second listing insert, got nil")
	}
}

func TestListingCheckConstraintInvalid(t *testing.T) {
	_, _, db := newTestListingService(t, nil)
	// state='visible' + visible_until=NULL must fail CHECK constraint.
	_, err := db.Exec(`INSERT INTO v2_listings
		(id,flow_id,city,country_code,dependency_type,help_type,urgency,languages,
		 display_name,contact_type,contact_ciphertext,contact_nonce,contact_key_version,
		 state,visible_until,first_published_at,last_activated_at,entitlement_expires_at,
		 activation_count,created_at,updated_at)
		VALUES ('a1','f1','tbilisi','GE','alcohol','crisis','urgent','["en"]',
		 'n','telegram','ct','nc','v1','visible',NULL,1,1,9999999999,1,1,1)`)
	if err == nil {
		t.Error("expected CHECK violation for visible+NULL visible_until")
	}

	// state='hidden' + visible_until NOT NULL must fail CHECK constraint.
	_, err = db.Exec(`INSERT INTO v2_listings
		(id,flow_id,city,country_code,dependency_type,help_type,urgency,languages,
		 display_name,contact_type,contact_ciphertext,contact_nonce,contact_key_version,
		 state,visible_until,first_published_at,last_activated_at,entitlement_expires_at,
		 activation_count,created_at,updated_at)
		VALUES ('a2','f2','tbilisi','GE','alcohol','crisis','urgent','["en"]',
		 'n','telegram','ct','nc','v1','hidden',9999999999,1,1,9999999999,1,1,1)`)
	if err == nil {
		t.Error("expected CHECK violation for hidden+NOT NULL visible_until")
	}
}

func TestListingFirstPublishAtomicityTrigger(t *testing.T) {
	ls, svc, db := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)

	_, err := db.Exec(`
		CREATE TRIGGER v2_test_block_listing_insert
		BEFORE INSERT ON v2_listings
		BEGIN SELECT RAISE(ABORT, 'test: listing insert blocked'); END`)
	if err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	defer db.Exec(`DROP TRIGGER IF EXISTS v2_test_block_listing_insert`) //nolint:errcheck

	_, err = ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if err == nil {
		t.Fatal("expected error from trigger, got nil")
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_listings`).Scan(&count) //nolint:errcheck
	if count != 0 {
		t.Errorf("listing row found after failed publish; want 0")
	}
}

// ── First publish tests ───────────────────────────────────────────────────────

func TestListingFirstPublishSuccess(t *testing.T) {
	ls, svc, _ := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)

	lv, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if err != nil {
		t.Fatalf("FirstPublish: %v", err)
	}
	if lv.State != "visible" {
		t.Errorf("state: got %q, want visible", lv.State)
	}
	if lv.VisibleUntil == nil {
		t.Error("visible_until must be set")
	}
	if lv.ActivationCount != 1 {
		t.Errorf("activation_count: got %d, want 1", lv.ActivationCount)
	}
	if lv.ContactType != "telegram" {
		t.Errorf("contact_type: got %q, want telegram", lv.ContactType)
	}
}

func TestListingVisibleUntilMin24hEntitlement(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ls, svc, _ := newTestListingService(t, func() time.Time { return now })
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)

	lv, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if err != nil {
		t.Fatalf("FirstPublish: %v", err)
	}
	if lv.VisibleUntil == nil {
		t.Fatal("visible_until nil")
	}

	want24h := now.Add(24 * time.Hour)
	ent := lv.EntitlementExpiresAt
	wantVisUntil := want24h
	if ent.Before(want24h) {
		wantVisUntil = ent
	}

	if !lv.VisibleUntil.Equal(wantVisUntil) {
		t.Errorf("visible_until: got %v, want %v", lv.VisibleUntil, wantVisUntil)
	}
}

func TestListingVisibleUntilNearDeadline(t *testing.T) {
	// Publish with only 1h left on entitlement — visible_until must equal entitlement.
	baseNow := time.Now()
	var clockNow time.Time
	clockMu := sync.Mutex{}
	getNow := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clockNow }
	clockMu.Lock()
	clockNow = baseNow
	clockMu.Unlock()

	ls, svc, _ := newTestListingService(t, getNow)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")

	// Get entitlement from DB.
	var entUnix int64
	svc.db.QueryRow(`SELECT entitlement_expires_at FROM v2_invoices WHERE flow_id=?`, flowID).Scan(&entUnix) //nolint:errcheck

	// Advance clock to 1h before entitlement.
	oneHourBefore := time.Unix(entUnix, 0).Add(-1 * time.Hour)
	clockMu.Lock()
	clockNow = oneHourBefore
	clockMu.Unlock()

	attachTestBinding(t, ls, flowID)
	lv, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if err != nil {
		t.Fatalf("FirstPublish near deadline: %v", err)
	}
	if lv.VisibleUntil == nil {
		t.Fatal("visible_until nil")
	}
	if lv.VisibleUntil.Unix() != entUnix {
		t.Errorf("visible_until near deadline: got %v, want %v (entitlement)", lv.VisibleUntil.Unix(), entUnix)
	}
}

func TestListingBlockedNonFormReady(t *testing.T) {
	t.Run("awaiting_payment", func(t *testing.T) {
		ls, svc, _ := newTestListingService(t, nil)
		rawCode, fv, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		// No binding — flow is still awaiting_payment; AttachReadyBinding would fail too.
		_, pubErr := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
		if !errors.Is(pubErr, ErrFormNotReady) {
			t.Errorf("awaiting_payment: got %v, want ErrFormNotReady", pubErr)
		}
		_ = fv
	})

	t.Run("payment_confirmed", func(t *testing.T) {
		ls, svc, _ := newTestListingService(t, nil)
		rawCode, fv, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		now := time.Now()
		svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_b", []string{"bc1qtest"}, fv.AmountAtomic, now) //nolint:errcheck
		svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, now)                                                         //nolint:errcheck
		// Force to payment_confirmed (skip RecordPostPaymentBalance).
		svc.db.Exec(`UPDATE v2_client_flows SET state='payment_confirmed' WHERE id=?`, fv.FlowID) //nolint:errcheck

		// Attach binding (succeeds because invoice is confirmed).
		ls.attachReadyBinding(fv.FlowID, newBindingRef(), time.Now(), time.Now().Add(10*time.Minute)) //nolint:errcheck

		_, pubErr := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
		if !errors.Is(pubErr, ErrFormNotReady) {
			t.Errorf("payment_confirmed: got %v, want ErrFormNotReady", pubErr)
		}
	})

	t.Run("paid_low_balance", func(t *testing.T) {
		ls, svc, _ := newTestListingService(t, nil)
		rawCode, fv, err := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		now := time.Now()
		svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_c", []string{"bc1qtest"}, fv.AmountAtomic, now) //nolint:errcheck
		svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, now)                                                         //nolint:errcheck
		// Force to paid_low_balance.
		svc.db.Exec(`UPDATE v2_client_flows SET state='paid_low_balance' WHERE id=?`, fv.FlowID) //nolint:errcheck

		ls.attachReadyBinding(fv.FlowID, newBindingRef(), time.Now(), time.Now().Add(10*time.Minute)) //nolint:errcheck

		_, pubErr := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
		if !errors.Is(pubErr, ErrFormNotReady) {
			t.Errorf("paid_low_balance: got %v, want ErrFormNotReady", pubErr)
		}
	})
}

func TestListingBlockedExpiredEntitlement(t *testing.T) {
	baseNow := time.Now()
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = baseNow
	mu.Unlock()

	ls, svc, _ := newTestListingService(t, getNow)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)

	// Advance past entitlement.
	var entUnix int64
	svc.db.QueryRow(`SELECT entitlement_expires_at FROM v2_invoices WHERE flow_id=?`, flowID).Scan(&entUnix) //nolint:errcheck
	mu.Lock()
	clockNow = time.Unix(entUnix+1, 0)
	mu.Unlock()

	_, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if !errors.Is(err, ErrEntitlementExpired) {
		t.Errorf("got %v, want ErrEntitlementExpired", err)
	}
}

func TestListingBlockedMissingBinding(t *testing.T) {
	ls, svc, _ := newTestListingService(t, nil)
	rawCode, _ := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	// No binding attached.
	_, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if !errors.Is(err, ErrBindingRequired) {
		t.Errorf("got %v, want ErrBindingRequired", err)
	}
}

func TestListingWrongCapability(t *testing.T) {
	ls, svc, _ := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)

	cases := []struct{ code, wallet string }{
		{"wrongcode000", "bc1qtest"}, // wrong code
		{rawCode, "bc1qwrongwallet"}, // wrong wallet
		{"bc1qtest", rawCode},        // swapped
	}
	for _, tc := range cases {
		_, err := ls.FirstPublish(tc.code, tc.wallet, validListingInput())
		if !errors.Is(err, ErrListingCapabilityNotFound) {
			t.Errorf("code=%q wallet=%q: got %v, want ErrListingCapabilityNotFound", tc.code, tc.wallet, err)
		}
	}
}

func TestListingInvalidInput(t *testing.T) {
	ls, svc, db := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)

	cases := []struct {
		name  string
		input ListingInput
	}{
		{"unknown_city", ListingInput{City: "atlantis", DependencyType: "alcohol", HelpType: "crisis", Urgency: "urgent", Languages: []string{"en"}, ContactType: "telegram", RawContact: "@x"}},
		{"unknown_dep", ListingInput{City: "tbilisi", DependencyType: "heroin", HelpType: "crisis", Urgency: "urgent", Languages: []string{"en"}, ContactType: "telegram", RawContact: "@x"}},
		{"too_many_langs", ListingInput{City: "tbilisi", DependencyType: "alcohol", HelpType: "crisis", Urgency: "urgent", Languages: []string{"en", "ru", "ka", "es", "de"}, ContactType: "telegram", RawContact: "@x"}},
		{"dup_lang", ListingInput{City: "tbilisi", DependencyType: "alcohol", HelpType: "crisis", Urgency: "urgent", Languages: []string{"en", "en"}, ContactType: "telegram", RawContact: "@x"}},
		{"bad_contact_type", ListingInput{City: "tbilisi", DependencyType: "alcohol", HelpType: "crisis", Urgency: "urgent", Languages: []string{"en"}, ContactType: "whatsapp", RawContact: "@x"}},
		{"empty_contact", ListingInput{City: "tbilisi", DependencyType: "alcohol", HelpType: "crisis", Urgency: "urgent", Languages: []string{"en"}, ContactType: "telegram", RawContact: ""}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := ls.FirstPublish(rawCode, "bc1qtest", tc.input)
			if !errors.Is(err, ErrInvalidListingInput) {
				t.Errorf("%s: got %v, want ErrInvalidListingInput", tc.name, err)
			}
			var count int
			db.QueryRow(`SELECT COUNT(*) FROM v2_listings`).Scan(&count) //nolint:errcheck
			if count != 0 {
				t.Errorf("%s: %d listing rows after rejection, want 0", tc.name, count)
			}
		})
	}
}

func TestListingDuplicateSubmit(t *testing.T) {
	ls, svc, db := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)

	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	_, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if !errors.Is(err, ErrListingAlreadyExists) {
		t.Errorf("second publish: got %v, want ErrListingAlreadyExists", err)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_listings`).Scan(&count) //nolint:errcheck
	if count != 1 {
		t.Errorf("listing count: got %d, want 1", count)
	}
}

func TestListingSecondContactNoChange(t *testing.T) {
	ls, svc, db := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)

	lv1, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}

	// Read original ciphertext.
	var origCT string
	db.QueryRow(`SELECT contact_ciphertext FROM v2_listings WHERE id=?`, lv1.ID).Scan(&origCT) //nolint:errcheck

	// Submit with a different contact.
	input2 := validListingInput()
	input2.RawContact = "@differentuser"
	_, err = ls.FirstPublish(rawCode, "bc1qtest", input2)
	if !errors.Is(err, ErrListingAlreadyExists) {
		t.Errorf("second publish different contact: got %v, want ErrListingAlreadyExists", err)
	}

	// Ciphertext must be unchanged.
	var nowCT string
	db.QueryRow(`SELECT contact_ciphertext FROM v2_listings WHERE id=?`, lv1.ID).Scan(&nowCT) //nolint:errcheck
	if nowCT != origCT {
		t.Error("ciphertext changed after rejected second submit")
	}
}

// ── Contact privacy tests ─────────────────────────────────────────────────────

func TestListingContactNotInDBColumns(t *testing.T) {
	rawContact := "@secretuser_privacy_test"
	ls, svc, db := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	input := validListingInput()
	input.RawContact = rawContact
	if _, err := ls.FirstPublish(rawCode, "bc1qtest", input); err != nil {
		t.Fatalf("FirstPublish: %v", err)
	}

	// Scan all text columns from v2_listings.
	rows, err := db.Query(`SELECT id, flow_id, city, country_code, dependency_type,
		help_type, urgency, languages, display_name, contact_type,
		contact_ciphertext, contact_nonce, contact_key_version, state
		FROM v2_listings`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cols [14]string
		dest := make([]any, 14)
		for i := range cols {
			dest[i] = &cols[i]
		}
		if err = rows.Scan(dest...); err != nil {
			t.Fatalf("scan: %v", err)
		}
		for _, col := range cols {
			if strings.Contains(col, rawContact) {
				t.Errorf("raw contact %q found in DB column: %q", rawContact, col)
			}
		}
	}
}

func TestListingContactNotInViewsOrErrors(t *testing.T) {
	rawContact := "@viewtest_secret"
	ls, svc, _ := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	input := validListingInput()
	input.RawContact = rawContact
	lv, err := ls.FirstPublish(rawCode, "bc1qtest", input)
	if err != nil {
		t.Fatalf("FirstPublish: %v", err)
	}

	// ListingView must not contain raw contact.
	lvJSON, _ := json.Marshal(lv)
	if strings.Contains(string(lvJSON), rawContact) {
		t.Errorf("raw contact found in ListingView JSON: %s", lvJSON)
	}

	// PublicListingView via BoardQuery must not contain raw contact.
	views, err := ls.BoardQuery("tbilisi", ls.now())
	if err != nil {
		t.Fatalf("BoardQuery: %v", err)
	}
	for _, pv := range views {
		pvJSON, _ := json.Marshal(pv)
		if strings.Contains(string(pvJSON), rawContact) {
			t.Errorf("raw contact found in PublicListingView JSON: %s", pvJSON)
		}
	}

	// Errors must not contain raw contact.
	_, err = ls.FirstPublish(rawCode, "bc1qtest", input) // duplicate
	if err != nil && strings.Contains(err.Error(), rawContact) {
		t.Errorf("raw contact found in error: %v", err)
	}
}

func TestListingCipherRoundTrip(t *testing.T) {
	c, err := NewAESGCMContactCipher(testAESKey, "v1")
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}

	plaintext := "@roundtrip_test"
	ct1, n1, kv1, err := c.Encrypt(plaintext, "lid1", "fid1", "telegram")
	if err != nil {
		t.Fatalf("encrypt1: %v", err)
	}
	ct2, n2, _, err := c.Encrypt(plaintext, "lid1", "fid1", "telegram")
	if err != nil {
		t.Fatalf("encrypt2: %v", err)
	}

	// Different nonces and ciphertexts due to fresh random nonce each time.
	if n1 == n2 {
		t.Error("nonces must differ between encryptions")
	}
	if ct1 == ct2 {
		t.Error("ciphertexts must differ between encryptions")
	}

	// Decryption round-trip.
	got, err := c.Decrypt(ct1, n1, kv1, "lid1", "fid1", "telegram")
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != plaintext {
		t.Errorf("decrypt: got %q, want %q", got, plaintext)
	}
}

func TestListingCipherTamper(t *testing.T) {
	c, _ := NewAESGCMContactCipher(testAESKey, "v1")
	ct, n, kv, _ := c.Encrypt("@secret", "lid1", "fid1", "telegram")

	// Tampered ciphertext.
	tampered := ct[:len(ct)-2] + "ff"
	if _, err := c.Decrypt(tampered, n, kv, "lid1", "fid1", "telegram"); err == nil {
		t.Error("expected error on tampered ciphertext")
	}

	// Wrong AAD: different listing ID.
	if _, err := c.Decrypt(ct, n, kv, "lid_WRONG", "fid1", "telegram"); err == nil {
		t.Error("expected error on wrong listingID in AAD")
	}

	// Cross-listing swap: encrypt for lid2, try to decrypt as lid1.
	ct2, n2, kv2, _ := c.Encrypt("@other", "lid2", "fid2", "telegram")
	if _, err := c.Decrypt(ct2, n2, kv2, "lid1", "fid1", "telegram"); err == nil {
		t.Error("expected error on cross-listing blob swap")
	}
}

// ── Binding tests ─────────────────────────────────────────────────────────────

func TestBindingAttachReplace(t *testing.T) {
	ls, svc, db := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	_ = rawCode

	now := ls.now()
	if err := ls.attachReadyBinding(flowID, newBindingRef(), now, now.Add(10*time.Minute)); err != nil {
		t.Fatalf("first attach: %v", err)
	}
	now2 := ls.now()
	if err := ls.attachReadyBinding(flowID, newBindingRef(), now2, now2.Add(10*time.Minute)); err != nil {
		t.Fatalf("second attach: %v", err)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings WHERE flow_id=?`, flowID).Scan(&count) //nolint:errcheck
	if count != 1 {
		t.Errorf("binding count: got %d, want 1 (atomic replace)", count)
	}

	var state string
	db.QueryRow(`SELECT state FROM v2_client_notification_bindings WHERE flow_id=?`, flowID).Scan(&state) //nolint:errcheck
	if state != "ready" {
		t.Errorf("binding state: got %q, want ready", state)
	}
}

func TestBindingInvalidateBlocksPublish(t *testing.T) {
	ls, svc, _ := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")

	now := ls.now()
	if err := ls.attachReadyBinding(flowID, newBindingRef(), now, now.Add(10*time.Minute)); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if err := ls.InvalidateBinding(flowID); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	_, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if !errors.Is(err, ErrBindingRequired) {
		t.Errorf("after invalidate: got %v, want ErrBindingRequired", err)
	}
}

func TestBindingReattachAllows(t *testing.T) {
	ls, svc, db := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")

	now := ls.now()
	ls.attachReadyBinding(flowID, newBindingRef(), now, now.Add(10*time.Minute)) //nolint:errcheck
	ls.InvalidateBinding(flowID)                                                 //nolint:errcheck
	now2 := ls.now()
	ref2 := newBindingRef()
	ls.attachReadyBinding(flowID, ref2, now2, now2.Add(10*time.Minute)) //nolint:errcheck
	// Insert stub destination so FirstPublish strict enforcement passes.
	if _, err := db.Exec(`INSERT INTO v2_telegram_destinations
		(binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at)
		VALUES (?, 'stub_ct', 'stub_nonce', 'test_v1', ?, ?)`,
		ref2, now2.Unix(), now2.Add(10*time.Minute).Unix()); err != nil {
		t.Fatalf("insert stub destination: %v", err)
	}

	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Errorf("after reattach: got %v, want success", err)
	}
}

func TestBindingBlockedAfterExpiry(t *testing.T) {
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = time.Now()
	mu.Unlock()

	ls, svc, _ := newTestListingService(t, getNow)
	_, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")

	// Advance past entitlement.
	var entUnix int64
	svc.db.QueryRow(`SELECT entitlement_expires_at FROM v2_invoices WHERE flow_id=?`, flowID).Scan(&entUnix) //nolint:errcheck
	mu.Lock()
	clockNow = time.Unix(entUnix+1, 0)
	mu.Unlock()

	now3 := time.Now()
	err := ls.attachReadyBinding(flowID, newBindingRef(), now3, now3.Add(10*time.Minute))
	if !errors.Is(err, ErrEntitlementExpired) {
		t.Errorf("attach after expiry: got %v, want ErrEntitlementExpired", err)
	}
}

// ── Daily lifecycle tests ─────────────────────────────────────────────────────

func TestListingVisibleBeforeExpiry(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ls, svc, _ := newTestListingService(t, func() time.Time { return now })
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Fatalf("publish: %v", err)
	}
	views, err := ls.BoardQuery("tbilisi", now)
	if err != nil {
		t.Fatalf("BoardQuery: %v", err)
	}
	if len(views) != 1 {
		t.Errorf("board results: got %d, want 1", len(views))
	}
}

func TestListingStaleExcludedWithoutCleanup(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ls, svc, _ := newTestListingService(t, func() time.Time { return now })
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// Query 25 hours later without calling NormalizeExpired.
	later := now.Add(25 * time.Hour)
	views, err := ls.BoardQuery("tbilisi", later)
	if err != nil {
		t.Fatalf("BoardQuery later: %v", err)
	}
	if len(views) != 0 {
		t.Errorf("stale listing not excluded: got %d results, want 0", len(views))
	}
}

func TestListingNormalizeHides(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ls, svc, db := newTestListingService(t, func() time.Time { return now })
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// Normalize 25h later (daily window expired, entitlement still valid).
	later := now.Add(25 * time.Hour)
	if err := ls.NormalizeExpired(later); err != nil {
		t.Fatalf("NormalizeExpired: %v", err)
	}

	var state string
	db.QueryRow(`SELECT state FROM v2_listings WHERE flow_id=?`, flowID).Scan(&state) //nolint:errcheck
	if state != "hidden" {
		t.Errorf("state after normalize: got %q, want hidden", state)
	}

	// Run again — idempotent.
	if err := ls.NormalizeExpired(later); err != nil {
		t.Fatalf("second NormalizeExpired: %v", err)
	}
	db.QueryRow(`SELECT state FROM v2_listings WHERE flow_id=?`, flowID).Scan(&state) //nolint:errcheck
	if state != "hidden" {
		t.Errorf("state after 2nd normalize: got %q, want hidden", state)
	}
}

func TestListingEarlyReactivationBlocked(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ls, svc, _ := newTestListingService(t, func() time.Time { return now })
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// Reactivate while still visible (window not expired).
	_, err := ls.Reactivate(rawCode, "bc1qtest", 150.0)
	if !errors.Is(err, ErrAlreadyVisible) {
		t.Errorf("early reactivate: got %v, want ErrAlreadyVisible", err)
	}
}

func TestListingReactivateSuccess(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = now
	mu.Unlock()

	ls, svc, _ := newTestListingService(t, getNow)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	lv1, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Advance 25h (daily window expired, entitlement valid).
	mu.Lock()
	clockNow = now.Add(25 * time.Hour)
	mu.Unlock()
	ls.NormalizeExpired(clockNow) //nolint:errcheck

	attachNextWindowBinding(t, ls, flowID)

	lv2, err := ls.Reactivate(rawCode, "bc1qtest", 120.0)
	if err != nil {
		t.Fatalf("Reactivate: %v", err)
	}
	if lv2.State != "visible" {
		t.Errorf("state: got %q, want visible", lv2.State)
	}
	if lv2.ActivationCount != 2 {
		t.Errorf("activation_count: got %d, want 2", lv2.ActivationCount)
	}
	if lv2.DisplayName != lv1.DisplayName {
		t.Error("display name changed after reactivation")
	}
}

func TestListingReactivateLowBalance(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = now
	mu.Unlock()

	ls, svc, _ := newTestListingService(t, getNow)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	_, _ = ls.FirstPublish(rawCode, "bc1qtest", validListingInput())

	mu.Lock()
	clockNow = now.Add(25 * time.Hour)
	mu.Unlock()
	ls.NormalizeExpired(clockNow) //nolint:errcheck

	_, err := ls.Reactivate(rawCode, "bc1qtest", 119.99)
	if !errors.Is(err, ErrLowBalance) {
		t.Errorf("low balance: got %v, want ErrLowBalance", err)
	}

	// Count and window unchanged.
	lv, _ := ls.scanListingView(flowID)
	if lv.ActivationCount != 1 {
		t.Errorf("count changed: got %d, want 1", lv.ActivationCount)
	}
	if lv.VisibleUntil != nil {
		t.Error("visible_until changed after failed reactivation")
	}
}

func TestListingReactivateInvalidBalance(t *testing.T) {
	ls, svc, _ := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	ls.FirstPublish(rawCode, "bc1qtest", validListingInput()) //nolint:errcheck

	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.01} {
		_, err := ls.Reactivate(rawCode, "bc1qtest", bad)
		if !errors.Is(err, ErrInvalidListingInput) {
			t.Errorf("balance %v: got %v, want ErrInvalidListingInput", bad, err)
		}
	}
}

func TestListingReactivateNearDeadline(t *testing.T) {
	baseNow := time.Unix(1_000_000, 0)
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = baseNow
	mu.Unlock()

	ls, svc, _ := newTestListingService(t, getNow)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	ls.FirstPublish(rawCode, "bc1qtest", validListingInput()) //nolint:errcheck

	var entUnix int64
	svc.db.QueryRow(`SELECT entitlement_expires_at FROM v2_invoices WHERE flow_id=?`, flowID).Scan(&entUnix) //nolint:errcheck

	// Advance to 1h before entitlement (past daily window from pub time).
	oneHourBefore := time.Unix(entUnix, 0).Add(-1 * time.Hour)
	mu.Lock()
	clockNow = oneHourBefore
	mu.Unlock()
	ls.NormalizeExpired(clockNow) //nolint:errcheck

	attachNextWindowBinding(t, ls, flowID)

	lv, err := ls.Reactivate(rawCode, "bc1qtest", 150.0)
	if err != nil {
		t.Fatalf("Reactivate near deadline: %v", err)
	}
	if lv.VisibleUntil == nil {
		t.Fatal("visible_until nil")
	}
	if lv.VisibleUntil.Unix() != entUnix {
		t.Errorf("visible_until near deadline: got %v, want %v (entitlement)", lv.VisibleUntil.Unix(), entUnix)
	}
}

func TestListingFinishedNoReactivation(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = now
	mu.Unlock()

	ls, svc, db := newTestListingService(t, getNow)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	ls.FirstPublish(rawCode, "bc1qtest", validListingInput()) //nolint:errcheck

	var entUnix int64
	svc.db.QueryRow(`SELECT entitlement_expires_at FROM v2_invoices WHERE flow_id=?`, flowID).Scan(&entUnix) //nolint:errcheck

	// Advance past entitlement.
	afterExpiry := time.Unix(entUnix+1, 0)
	mu.Lock()
	clockNow = afterExpiry
	mu.Unlock()
	if err := ls.NormalizeExpired(afterExpiry); err != nil {
		t.Fatalf("NormalizeExpired: %v", err)
	}

	var state string
	db.QueryRow(`SELECT state FROM v2_listings WHERE flow_id=?`, flowID).Scan(&state) //nolint:errcheck
	if state != "finished" {
		t.Errorf("state: got %q, want finished", state)
	}

	_, err := ls.Reactivate(rawCode, "bc1qtest", 150.0)
	if !errors.Is(err, ErrEntitlementExpired) {
		t.Errorf("reactivate finished: got %v, want ErrEntitlementExpired", err)
	}
}

func TestListingConcurrentReactivation(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = now
	mu.Unlock()

	ls, svc, db := newTestListingService(t, getNow)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	ls.FirstPublish(rawCode, "bc1qtest", validListingInput()) //nolint:errcheck

	mu.Lock()
	clockNow = now.Add(25 * time.Hour)
	mu.Unlock()
	ls.NormalizeExpired(clockNow) //nolint:errcheck

	// One binding for window 2; all 8 goroutines compete, only one wins.
	attachNextWindowBinding(t, ls, flowID)

	const goroutines = 8
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ls.Reactivate(rawCode, "bc1qtest", 150.0) //nolint:errcheck
		}()
	}
	wg.Wait()

	var count int
	db.QueryRow(`SELECT activation_count FROM v2_listings WHERE flow_id=?`, flowID).Scan(&count) //nolint:errcheck
	if count != 2 {
		t.Errorf("concurrent reactivation: activation_count=%d, want 2", count)
	}
}

func TestListingReactivateNoChangeImmutable(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = now
	mu.Unlock()

	ls, svc, db := newTestListingService(t, getNow)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	lv1, _ := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())

	var origCT, origNonce, origName string
	var origFirstPub, origEntitlement int64
	db.QueryRow(`SELECT contact_ciphertext, contact_nonce, display_name, first_published_at, entitlement_expires_at FROM v2_listings WHERE flow_id=?`, flowID).
		Scan(&origCT, &origNonce, &origName, &origFirstPub, &origEntitlement) //nolint:errcheck

	mu.Lock()
	clockNow = now.Add(25 * time.Hour)
	mu.Unlock()
	ls.NormalizeExpired(clockNow) //nolint:errcheck
	attachNextWindowBinding(t, ls, flowID)
	lv2, err := ls.Reactivate(rawCode, "bc1qtest", 150.0)
	if err != nil {
		t.Fatalf("Reactivate: %v", err)
	}

	var newCT, newNonce, newName string
	var newFirstPub, newEntitlement int64
	db.QueryRow(`SELECT contact_ciphertext, contact_nonce, display_name, first_published_at, entitlement_expires_at FROM v2_listings WHERE flow_id=?`, flowID).
		Scan(&newCT, &newNonce, &newName, &newFirstPub, &newEntitlement) //nolint:errcheck

	if newCT != origCT {
		t.Error("ciphertext changed after reactivation")
	}
	if newNonce != origNonce {
		t.Error("nonce changed after reactivation")
	}
	if newName != origName {
		t.Error("display_name changed after reactivation")
	}
	if newFirstPub != origFirstPub {
		t.Error("first_published_at changed after reactivation")
	}
	if newEntitlement != origEntitlement {
		t.Error("entitlement_expires_at changed after reactivation")
	}
	if lv2.DisplayName != lv1.DisplayName {
		t.Error("DisplayName in view changed")
	}
}

// ── Public board / privacy tests ──────────────────────────────────────────────

func TestListingBoardQueryCityFilter(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ls, svc, _ := newTestListingService(t, func() time.Time { return now })
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	ls.FirstPublish(rawCode, "bc1qtest", validListingInput()) //nolint:errcheck (tbilisi)

	// Unknown city → error.
	_, err := ls.BoardQuery("atlantis", now)
	if !errors.Is(err, ErrInvalidListingInput) {
		t.Errorf("unknown city: got %v, want ErrInvalidListingInput", err)
	}

	// Known city with no visible listings.
	views, err := ls.BoardQuery("moscow", now)
	if err != nil {
		t.Fatalf("moscow query: %v", err)
	}
	if len(views) != 0 {
		t.Errorf("moscow: got %d results, want 0", len(views))
	}

	// tbilisi has one visible listing.
	views, err = ls.BoardQuery("tbilisi", now)
	if err != nil {
		t.Fatalf("tbilisi query: %v", err)
	}
	if len(views) != 1 {
		t.Errorf("tbilisi: got %d results, want 1", len(views))
	}

	// After normalize (hide), zero results.
	ls.NormalizeExpired(now.Add(25 * time.Hour)) //nolint:errcheck
	views, err = ls.BoardQuery("tbilisi", now.Add(25*time.Hour))
	if err != nil {
		t.Fatalf("tbilisi post-hide: %v", err)
	}
	if len(views) != 0 {
		t.Errorf("after hide tbilisi: got %d results, want 0", len(views))
	}
}

func TestListingSafeViewAllowlist(t *testing.T) {
	// Positive allowlist: only these exported field names are permitted on ListingView.
	// Any new field must be explicitly added here; this prevents accidental leakage.
	listingViewAllowed := map[string]bool{
		"ID": true, "City": true, "CountryCode": true,
		"DependencyType": true, "HelpType": true, "Urgency": true,
		"Languages": true, "DisplayName": true, "ContactType": true,
		"State": true, "VisibleUntil": true,
		"FirstPublishedAt": true, "LastActivatedAt": true, "EntitlementExpiresAt": true,
		"ActivationCount": true, "CreatedAt": true, "UpdatedAt": true,
	}
	publicViewAllowed := map[string]bool{
		"ID": true, "DisplayName": true, "City": true, "CountryCode": true,
		"DependencyType": true, "HelpType": true, "Urgency": true,
		"Languages": true, "VisibleUntil": true, "TimeLeftSec": true,
		"ClientReputation": true, // public reputation aggregate; no PII
	}

	for _, tc := range []struct {
		name    string
		v       any
		allowed map[string]bool
	}{
		{"ListingView", ListingView{}, listingViewAllowed},
		{"PublicListingView", PublicListingView{}, publicViewAllowed},
	} {
		rt := reflect.TypeOf(tc.v)
		for i := 0; i < rt.NumField(); i++ {
			fname := rt.Field(i).Name
			if !tc.allowed[fname] {
				t.Errorf("type %s: unexpected field %q — add to allowlist only if safe to expose", tc.name, fname)
			}
		}
	}
}

func TestListingSafeViewForbiddenFields(t *testing.T) {
	// Explicit JSON/reflection regression: none of the secret substrings may appear
	// as JSON key names in either view type. We marshal to lower-case JSON and check
	// for key-name patterns that would indicate accidental secret field leakage.
	// Note: "code" is intentionally excluded (matches CountryCode); "telegram" is
	// excluded (matches ContactType value "telegram"). Use specific compound terms.
	forbidden := []string{
		"flow_id", "flowid",
		"invoice",
		"txid",
		"wallet", "fingerprint",
		"codehash", "managementcode", "management_code",
		"bindingref", "binding_ref",
		"rawcontact", "raw_contact",
		"ciphertext",
		"nonce",
		"keyversion", "key_version",
	}

	ls, svc, _ := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	lv, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if err != nil {
		t.Fatalf("FirstPublish: %v", err)
	}
	lvJSON, _ := json.Marshal(lv)
	lvLower := strings.ToLower(string(lvJSON))
	for _, f := range forbidden {
		if strings.Contains(lvLower, f) {
			t.Errorf("ListingView JSON contains forbidden term %q: %s", f, lvJSON)
		}
	}

	views, err := ls.BoardQuery("tbilisi", ls.now())
	if err != nil || len(views) == 0 {
		t.Fatalf("BoardQuery: %v / %d results", err, len(views))
	}
	pvJSON, _ := json.Marshal(views[0])
	pvLower := strings.ToLower(string(pvJSON))
	for _, f := range forbidden {
		if strings.Contains(pvLower, f) {
			t.Errorf("PublicListingView JSON contains forbidden term %q: %s", f, pvJSON)
		}
	}
}

// ── Binding ref format tests ──────────────────────────────────────────────────

func TestBindingRefFormatValid(t *testing.T) {
	ls, svc, _ := newTestListingService(t, nil)
	_, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")

	// A structurally valid opaque ref must be accepted.
	validRef := "bnd_" + strings.Repeat("a", 32)
	now := time.Now()
	if err := ls.attachReadyBinding(flowID, validRef, now, now.Add(10*time.Minute)); err != nil {
		t.Errorf("valid opaque ref rejected: %v", err)
	}
}

func TestBindingRefFormatInvalid(t *testing.T) {
	ls, svc, _ := newTestListingService(t, nil)
	_, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")

	cases := []struct {
		name string
		ref  string
	}{
		{"numeric_raw", "1234567890"},
		{"plain_string", "some_binding_ref"},
		{"no_prefix", strings.Repeat("a", 36)},
		{"wrong_prefix", "ref_" + strings.Repeat("a", 32)},
		{"too_short", "bnd_" + strings.Repeat("a", 16)},
		{"too_long", "bnd_" + strings.Repeat("a", 64)},
		{"uppercase_hex", "bnd_" + strings.Repeat("A", 32)},
		{"non_hex_suffix", "bnd_" + strings.Repeat("z", 32)},
		{"empty", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			err := ls.attachReadyBinding(flowID, tc.ref, now, now.Add(10*time.Minute))
			if err == nil {
				t.Errorf("%s: expected rejection, got nil", tc.name)
			}
			// Error must not echo the ref value.
			if tc.ref != "" && strings.Contains(err.Error(), tc.ref) {
				t.Errorf("%s: error leaks ref value: %v", tc.name, err)
			}
		})
	}
}

// ── Contact boundary fake-validator tests ─────────────────────────────────────

// fakeContactValidatorResult returns a pre-set normalized value with nil error
// regardless of input, simulating a broken or adversarial validator.
type fakeContactValidatorResult struct{ normalized string }

func (f *fakeContactValidatorResult) ValidateContact(_, _ string) (string, error) {
	return f.normalized, nil
}

func TestContactBoundaryFakeValidator(t *testing.T) {
	// Helper to publish with a custom validator.
	publish := func(t *testing.T, normalized string) error {
		t.Helper()
		db, _ := OpenMemory()
		t.Cleanup(func() { db.Close() })
		svc, _ := NewWithClock(db, testHMACKey, time.Now)
		cipher, _ := NewAESGCMContactCipher(testAESKey, "v1")
		ls, _ := NewListingService(svc, cipher, &fakeDisplayNameGenerator{}, &fakeContactValidatorResult{normalized: normalized})
		rawCode, fv, _ := svc.CreatePaymentIntent("bc1qtest", "BTC", validDraft())
		now := time.Now()
		svc.RecordPaymentDetected(fv.FlowID, fv.InvoiceID, "txid_"+newID()[:8], []string{"bc1qtest"}, fv.AmountAtomic, now) //nolint:errcheck
		svc.ConfirmPayment(fv.FlowID, fv.InvoiceID, now)                                                                    //nolint:errcheck
		svc.RecordPostPaymentBalance(fv.FlowID, 150.0, hardFloorUSD)                                                        //nolint:errcheck
		ls.attachReadyBinding(fv.FlowID, newBindingRef(), time.Now(), time.Now().Add(10*time.Minute))                       //nolint:errcheck

		input := validListingInput()
		input.RawContact = "@anything"
		_, err := ls.FirstPublish(rawCode, "bc1qtest", input)

		// Verify no row was created.
		var count int
		db.QueryRow(`SELECT COUNT(*) FROM v2_listings`).Scan(&count) //nolint:errcheck
		if count != 0 {
			t.Errorf("listing row created despite invalid normalized contact %q", normalized)
		}
		return err
	}

	t.Run("empty_nil", func(t *testing.T) {
		if err := publish(t, ""); !errors.Is(err, ErrInvalidListingInput) {
			t.Errorf("empty: got %v, want ErrInvalidListingInput", err)
		}
	})
	t.Run("whitespace_nil", func(t *testing.T) {
		if err := publish(t, "   "); !errors.Is(err, ErrInvalidListingInput) {
			t.Errorf("whitespace: got %v, want ErrInvalidListingInput", err)
		}
	})
	t.Run("oversized_nil", func(t *testing.T) {
		big := strings.Repeat("a", maxNormalizedContactBytes+1)
		if err := publish(t, big); !errors.Is(err, ErrInvalidListingInput) {
			t.Errorf("oversized: got %v, want ErrInvalidListingInput", err)
		}
	})
	t.Run("invalid_utf8_nil", func(t *testing.T) {
		// 0xFF is an invalid UTF-8 byte.
		bad := string([]byte{0xFF, 0xFE, 0xFD})
		if err := publish(t, bad); !errors.Is(err, ErrInvalidListingInput) {
			t.Errorf("invalid_utf8: got %v, want ErrInvalidListingInput", err)
		}
	})
}

// ── Display name validation tests ─────────────────────────────────────────────

// fakeDisplayNameGeneratorSeq returns names from a pre-set sequence, then errors.
type fakeDisplayNameGeneratorSeq struct {
	names []string
	idx   int
}

func (f *fakeDisplayNameGeneratorSeq) Generate() (string, error) {
	if f.idx >= len(f.names) {
		return "", errors.New("v2: fakeDisplayNameGeneratorSeq: exhausted")
	}
	n := f.names[f.idx]
	f.idx++
	return n, nil
}

func newListingServiceWithGen(t *testing.T, gen DisplayNameGenerator) (*ListingService, *Service, *sql.DB) {
	t.Helper()
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	svc, err := NewWithClock(db, testHMACKey, time.Now)
	if err != nil {
		t.Fatalf("NewWithClock: %v", err)
	}
	cipher, err := NewAESGCMContactCipher(testAESKey, "v1")
	if err != nil {
		t.Fatalf("NewAESGCMContactCipher: %v", err)
	}
	ls, err := NewListingService(svc, cipher, gen, &fakeContactValidator{})
	if err != nil {
		t.Fatalf("NewListingService: %v", err)
	}
	return ls, svc, db
}

func TestDisplayNameValidationRejectsInvalid(t *testing.T) {
	cases := []struct {
		name      string
		generated string
	}{
		{"empty", ""},
		{"whitespace", "   "},
		{"uppercase", "Calm_river_07"},
		{"special_chars", "calm river 07"},
		{"overlong", strings.Repeat("a", 65)},
		{"invalid_utf8", string([]byte{0xFF, 0xFE})},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			gen := &fakeDisplayNameGeneratorSeq{names: []string{tc.generated}}
			ls, svc, db := newListingServiceWithGen(t, gen)
			rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
			ls.attachReadyBinding(flowID, newBindingRef(), time.Now(), time.Now().Add(10*time.Minute)) //nolint:errcheck

			_, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
			if err == nil {
				t.Errorf("%s: expected error, got nil", tc.name)
			}
			var count int
			db.QueryRow(`SELECT COUNT(*) FROM v2_listings`).Scan(&count) //nolint:errcheck
			if count != 0 {
				t.Errorf("%s: listing row created for invalid name", tc.name)
			}
		})
	}
}

func TestDisplayNameCollisionRetry(t *testing.T) {
	// First call returns a name that is already taken; second call returns a unique name.
	// Publish must succeed and save the second name.
	ls, svc, db := newTestListingService(t, nil)

	// Publish a first listing that occupies "calm_river_07".
	rawCode1, flowID1 := makeFormReadyFlow(t, svc, "bc1qtest_first", "BTC")
	ref1 := newBindingRef()
	now1 := time.Now()
	ls.attachReadyBinding(flowID1, ref1, now1, now1.Add(10*time.Minute)) //nolint:errcheck
	if _, err := db.Exec(`INSERT INTO v2_telegram_destinations
		(binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at)
		VALUES (?, 'stub_ct', 'stub_nonce', 'test_v1', ?, ?)`,
		ref1, now1.Unix(), now1.Add(10*time.Minute).Unix()); err != nil {
		t.Fatalf("insert stub destination flowID1: %v", err)
	}
	ls.names = &fakeDisplayNameGenerator{name: "calm_river_07"}
	lv1, err := ls.FirstPublish(rawCode1, "bc1qtest_first", validListingInput())
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if lv1.DisplayName != "calm_river_07" {
		t.Fatalf("first name: got %q", lv1.DisplayName)
	}

	// Second flow: generator returns taken name first, then a unique one.
	ls.names = &fakeDisplayNameGeneratorSeq{names: []string{"calm_river_07", "brave_shore_01"}}
	rawCode2, flowID2 := makeFormReadyFlow(t, svc, "bc1qtest_second", "BTC")
	ref2 := newBindingRef()
	now2 := time.Now()
	ls.attachReadyBinding(flowID2, ref2, now2, now2.Add(10*time.Minute)) //nolint:errcheck
	if _, err := db.Exec(`INSERT INTO v2_telegram_destinations
		(binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at)
		VALUES (?, 'stub_ct', 'stub_nonce', 'test_v1', ?, ?)`,
		ref2, now2.Unix(), now2.Add(10*time.Minute).Unix()); err != nil {
		t.Fatalf("insert stub destination flowID2: %v", err)
	}
	lv2, err := ls.FirstPublish(rawCode2, "bc1qtest_second", validListingInput())
	if err != nil {
		t.Fatalf("second publish with collision: %v", err)
	}
	if lv2.DisplayName != "brave_shore_01" {
		t.Errorf("expected second name %q, got %q", "brave_shore_01", lv2.DisplayName)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_listings`).Scan(&count) //nolint:errcheck
	if count != 2 {
		t.Errorf("listing count: got %d, want 2", count)
	}
}

func TestDisplayNamePerpetualCollisionBounded(t *testing.T) {
	// Generator always returns the same taken name; publish must fail without hanging
	// and must not create any listing row.
	ls, svc, db := newTestListingService(t, nil)

	// Occupy "calm_river_07".
	rawCode1, flowID1 := makeFormReadyFlow(t, svc, "bc1qtest_occ", "BTC")
	ref1 := newBindingRef()
	now1 := time.Now()
	ls.attachReadyBinding(flowID1, ref1, now1, now1.Add(10*time.Minute)) //nolint:errcheck
	if _, err := db.Exec(`INSERT INTO v2_telegram_destinations
		(binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at)
		VALUES (?, 'stub_ct', 'stub_nonce', 'test_v1', ?, ?)`,
		ref1, now1.Unix(), now1.Add(10*time.Minute).Unix()); err != nil {
		t.Fatalf("insert stub destination flowID1: %v", err)
	}
	ls.names = &fakeDisplayNameGenerator{name: "calm_river_07"}
	if _, err := ls.FirstPublish(rawCode1, "bc1qtest_occ", validListingInput()); err != nil {
		t.Fatalf("occupy: %v", err)
	}

	// New flow with always-colliding generator.
	rawCode2, flowID2 := makeFormReadyFlow(t, svc, "bc1qtest_col", "BTC")
	ls.attachReadyBinding(flowID2, newBindingRef(), time.Now(), time.Now().Add(10*time.Minute)) //nolint:errcheck
	// Generator always returns the taken name. No destination needed since FirstPublish
	// will fail at the INSERT listing (display_name collision) and rollback.
	ls.names = &fakeDisplayNameGenerator{name: "calm_river_07"}
	_, err := ls.FirstPublish(rawCode2, "bc1qtest_col", validListingInput())
	if err == nil {
		t.Error("expected error from perpetual collision, got nil")
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_listings`).Scan(&count) //nolint:errcheck
	if count != 1 {
		t.Errorf("listing count: got %d, want 1 (only the pre-occupied one)", count)
	}
}

func TestDisplayNameUniqueAcrossListings(t *testing.T) {
	// Two listings must not share a display name (enforced by UNIQUE constraint in DB).
	_, _, db := newTestListingService(t, nil)

	// Create prerequisite flows for FK-safe testing.
	for _, id := range []string{"uniq_flow1", "uniq_flow2"} {
		profileID := mustInsertClientProfileForFlow(t, db, 1)
		if _, err := db.Exec(`INSERT INTO v2_client_flows
			(id, wallet_fingerprint, currency, management_code_hash, state, client_profile_id, created_at, updated_at)
			VALUES (?, 'fp', 'BTC', 'hash', 'form_ready', ?, 1, 1)`, id, profileID); err != nil {
			t.Fatalf("prereq flow: %v", err)
		}
	}

	insert := func(id, flowID, name string) error {
		_, err := db.Exec(`INSERT INTO v2_listings
			(id,flow_id,city,country_code,dependency_type,help_type,urgency,languages,
			 display_name,contact_type,contact_ciphertext,contact_nonce,contact_key_version,
			 state,visible_until,first_published_at,last_activated_at,entitlement_expires_at,
			 activation_count,created_at,updated_at)
			VALUES (?,?,'tbilisi','GE','alcohol','crisis','urgent','["en"]',
			 ?,'telegram','ct','nc','v1','visible',9999999999,1,1,9999999999,1,1,1)`,
			id, flowID, name)
		return err
	}

	if err := insert("id1", "uniq_flow1", "unique_name_01"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := insert("id2", "uniq_flow2", "unique_name_01"); err == nil {
		t.Error("expected UNIQUE violation on duplicate display_name, got nil")
	}
}

func TestReactivationDoesNotChangeName(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = now
	mu.Unlock()

	ls, svc, db := newTestListingService(t, getNow)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	lv1, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	mu.Lock()
	clockNow = now.Add(25 * time.Hour)
	mu.Unlock()
	ls.NormalizeExpired(clockNow) //nolint:errcheck
	attachNextWindowBinding(t, ls, flowID)

	lv2, err := ls.Reactivate(rawCode, "bc1qtest", 150.0)
	if err != nil {
		t.Fatalf("Reactivate: %v", err)
	}
	if lv2.DisplayName != lv1.DisplayName {
		t.Errorf("display name changed: %q → %q", lv1.DisplayName, lv2.DisplayName)
	}

	var dbName string
	db.QueryRow(`SELECT display_name FROM v2_listings WHERE flow_id=?`, flowID).Scan(&dbName) //nolint:errcheck
	if dbName != lv1.DisplayName {
		t.Errorf("DB display_name changed: %q → %q", lv1.DisplayName, dbName)
	}
}

// ── Atomic binding gate regression tests ─────────────────────────────────────

func TestFirstPublishAtomicBindingInvalidated(t *testing.T) {
	// Simulate: binding is invalidated between pre-checks and the atomic INSERT.
	// The INSERT WHERE EXISTS must reject the row; no listing may appear.
	ls, svc, db := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)

	ls._testHook = func() {
		// Runs between pre-checks and atomic INSERT.
		ls.InvalidateBinding(flowID) //nolint:errcheck
	}

	_, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if !errors.Is(err, ErrBindingRequired) {
		t.Errorf("after interleaved invalidate: got %v, want ErrBindingRequired", err)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_listings`).Scan(&count) //nolint:errcheck
	if count != 0 {
		t.Errorf("listing row created despite invalidated binding: count=%d", count)
	}
}

func TestReactivateAtomicBindingInvalidated(t *testing.T) {
	// Simulate: binding is deleted between pre-read and the CAS UPDATE.
	// The CAS binding UPDATE must return 0 rows; no reactivation may occur.
	now := time.Unix(1_000_000, 0)
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = now
	mu.Unlock()

	ls, svc, db := newTestListingService(t, getNow)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Fatalf("publish: %v", err)
	}

	mu.Lock()
	clockNow = now.Add(25 * time.Hour)
	mu.Unlock()
	ls.NormalizeExpired(clockNow) //nolint:errcheck

	// Attach the next-window binding, then the hook will delete it.
	attachNextWindowBinding(t, ls, flowID)

	ls._testHook = func() {
		// Runs between pre-read and CAS UPDATE binding.
		ls.InvalidateBinding(flowID) //nolint:errcheck
	}

	_, err := ls.Reactivate(rawCode, "bc1qtest", 150.0)
	if !errors.Is(err, ErrBindingRequired) {
		t.Errorf("after interleaved invalidate: got %v, want ErrBindingRequired", err)
	}

	// State must remain hidden (reactivation did not occur).
	var state string
	db.QueryRow(`SELECT state FROM v2_listings WHERE flow_id=?`, flowID).Scan(&state) //nolint:errcheck
	if state != "hidden" {
		t.Errorf("state after failed reactivation: got %q, want hidden", state)
	}
}

func TestFirstPublishConcurrentAtMostOneRow(t *testing.T) {
	// Multiple goroutines publish simultaneously; at most one row must be created.
	ls, svc, db := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)

	const goroutines = 8
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ls.FirstPublish(rawCode, "bc1qtest", validListingInput()) //nolint:errcheck
		}()
	}
	wg.Wait()

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_listings WHERE flow_id=?`, flowID).Scan(&count) //nolint:errcheck
	if count != 1 {
		t.Errorf("concurrent first publish: count=%d, want 1", count)
	}
}

func TestConcurrentInvalidateReactivate(t *testing.T) {
	// If binding is invalidated while reactivation is in flight, the final DB state
	// must never show a successful reactivation that happened after invalidation.
	now := time.Unix(1_000_000, 0)
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = now
	mu.Unlock()

	ls, svc, db := newTestListingService(t, getNow)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	ls.FirstPublish(rawCode, "bc1qtest", validListingInput()) //nolint:errcheck

	mu.Lock()
	clockNow = now.Add(25 * time.Hour)
	mu.Unlock()
	ls.NormalizeExpired(clockNow) //nolint:errcheck

	// Attach next-window binding; hook will delete it before the CAS.
	attachNextWindowBinding(t, ls, flowID)

	// Use the hook to delete the binding right before the CAS.
	ls._testHook = func() {
		ls.InvalidateBinding(flowID) //nolint:errcheck
	}

	_, err := ls.Reactivate(rawCode, "bc1qtest", 150.0)
	// Either the pre-read detects the missing binding or the CAS fails.
	if err == nil {
		t.Error("expected error from reactivation after binding deleted, got nil")
	}

	// Listing must stay hidden.
	var state string
	db.QueryRow(`SELECT state FROM v2_listings WHERE flow_id=?`, flowID).Scan(&state) //nolint:errcheck
	if state != "hidden" {
		t.Errorf("listing state after invalidated reactivation: got %q, want hidden", state)
	}
}

// ── Schema CHECK constraint negative tests ────────────────────────────────────

func TestSchemaCheckConstraintsNegative(t *testing.T) {
	_, _, db := newTestListingService(t, nil)

	// Insert prerequisite flow rows so FK constraints do not fire before CHECKs.
	// This ensures the test actually exercises the CHECK constraints being verified.
	for _, id := range []string{
		"bfl1", "bfl2", "bflv",
		"bdup1", "bdup2", // dedicated flows for the duplicate binding_ref subtest
		"fl1", "fl2", "fl3", "fl4", "fl5", "fl6", "fl7", "flv",
	} {
		profileID := mustInsertClientProfileForFlow(t, db, 1)
		if _, err := db.Exec(`INSERT INTO v2_client_flows
			(id, wallet_fingerprint, currency, management_code_hash, state, client_profile_id, created_at, updated_at)
			VALUES (?, 'fp', 'BTC', 'hash', 'form_ready', ?, 1, 1)`, id, profileID); err != nil {
			t.Fatalf("prereq flow %q: %v", id, err)
		}
	}

	// ── Binding constraints ──

	t.Run("binding_empty_ref", func(t *testing.T) {
		// empty binding_ref fails: length != 36, substr prefix wrong, GLOB suffix check
		_, err := db.Exec(`INSERT INTO v2_client_notification_bindings
			(id, flow_id, binding_ref, state, window_number, verified_at, valid_until, created_at, updated_at)
			VALUES ('bid1', 'bfl1', '', 'ready', 1, 1, 9999999999, 1, 1)`)
		if err == nil {
			t.Error("expected CHECK violation for empty binding_ref")
		}
	})

	t.Run("binding_ready_with_activated_at", func(t *testing.T) {
		// ready state with activated_at NOT NULL violates CHECK
		_, err := db.Exec(`INSERT INTO v2_client_notification_bindings
			(id, flow_id, binding_ref, state, window_number, verified_at, valid_until, activated_at, created_at, updated_at)
			VALUES ('bid2', 'bfl2', 'bnd_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'ready', 1, 1, 9999999999, 5, 1, 1)`)
		if err == nil {
			t.Error("expected CHECK violation for ready state with activated_at NOT NULL")
		}
	})

	t.Run("binding_active_without_activated_at", func(t *testing.T) {
		// active state with activated_at IS NULL violates CHECK
		_, err := db.Exec(`INSERT INTO v2_client_notification_bindings
			(id, flow_id, binding_ref, state, window_number, verified_at, valid_until, created_at, updated_at)
			VALUES ('bid3', 'bflv', 'bnd_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'active', 1, 1, 9999999999, 1, 1)`)
		if err == nil {
			t.Error("expected CHECK violation for active state with activated_at IS NULL")
		}
	})

	t.Run("binding_valid_row_passes", func(t *testing.T) {
		// A valid ready binding must succeed (positive case).
		_, err := db.Exec(`INSERT INTO v2_client_notification_bindings
			(id, flow_id, binding_ref, state, window_number, verified_at, valid_until, created_at, updated_at)
			VALUES ('bidv', 'bfl1', 'bnd_cccccccccccccccccccccccccccccccc', 'ready', 1, 1, 9999999999, 1, 1)`)
		if err != nil {
			t.Errorf("valid binding row rejected: %v", err)
		}
	})

	// ── Direct SQLite schema regression tests for binding_ref contract ──
	// These bypass AttachReadyBinding and hit CHECK/UNIQUE constraints directly.

	t.Run("binding_prefix_not_literal_bnd_underscore", func(t *testing.T) {
		// "bndX" replaces the literal '_' — substr check must reject it.
		_, err := db.Exec(`INSERT INTO v2_client_notification_bindings
			(id, flow_id, binding_ref, state, window_number, verified_at, valid_until, created_at, updated_at)
			VALUES ('bds1', 'bfl2', 'bndXaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'ready', 1, 1, 9999999999, 1, 1)`)
		if err == nil {
			t.Error("expected CHECK violation for prefix bndX (wrong 4th char)")
		}
	})

	t.Run("binding_uppercase_hex_suffix", func(t *testing.T) {
		// Uppercase hex chars must be rejected by the negative GLOB.
		_, err := db.Exec(`INSERT INTO v2_client_notification_bindings
			(id, flow_id, binding_ref, state, window_number, verified_at, valid_until, created_at, updated_at)
			VALUES ('bds2', 'bflv', 'bnd_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA', 'ready', 1, 1, 9999999999, 1, 1)`)
		if err == nil {
			t.Error("expected CHECK violation for uppercase hex suffix")
		}
	})

	t.Run("binding_non_hex_suffix", func(t *testing.T) {
		// 'z' is outside [0-9a-f]; must be rejected by the negative GLOB.
		_, err := db.Exec(`INSERT INTO v2_client_notification_bindings
			(id, flow_id, binding_ref, state, window_number, verified_at, valid_until, created_at, updated_at)
			VALUES ('bds3', 'bfl2', 'bnd_zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz', 'ready', 1, 1, 9999999999, 1, 1)`)
		if err == nil {
			t.Error("expected CHECK violation for non-hex suffix")
		}
	})

	t.Run("binding_wrong_length_ref", func(t *testing.T) {
		// 35-char ref (one char short); length CHECK must reject it.
		_, err := db.Exec(`INSERT INTO v2_client_notification_bindings
			(id, flow_id, binding_ref, state, window_number, verified_at, valid_until, created_at, updated_at)
			VALUES ('bds4', 'bfl2', 'bnd_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'ready', 1, 1, 9999999999, 1, 1)`)
		if err == nil {
			t.Error("expected CHECK violation for wrong-length binding_ref (35 chars)")
		}
	})

	t.Run("binding_duplicate_ref_different_flow", func(t *testing.T) {
		// Same valid binding_ref for a second flow must fail UNIQUE(binding_ref).
		// Uses dedicated bdup1/bdup2 flows so no prior subtest binding interferes.
		validRef := "bnd_ffffffffffffffffffffffffffffffff"
		_, err := db.Exec(`INSERT INTO v2_client_notification_bindings
			(id, flow_id, binding_ref, state, window_number, verified_at, valid_until, created_at, updated_at)
			VALUES ('bds5a', 'bdup1', ?, 'ready', 1, 1, 9999999999, 1, 1)`, validRef)
		if err != nil {
			t.Fatalf("first insert of valid ref failed unexpectedly: %v", err)
		}
		_, err = db.Exec(`INSERT INTO v2_client_notification_bindings
			(id, flow_id, binding_ref, state, window_number, verified_at, valid_until, created_at, updated_at)
			VALUES ('bds5b', 'bdup2', ?, 'ready', 1, 1, 9999999999, 1, 1)`, validRef)
		if err == nil {
			t.Error("expected UNIQUE violation for duplicate binding_ref on different flow")
		}
	})

	t.Run("binding_verified_at_after_created_at", func(t *testing.T) {
		// verified_at > created_at violates CHECK (verified_at <= created_at).
		_, err := db.Exec(`INSERT INTO v2_client_notification_bindings
			(id, flow_id, binding_ref, state, window_number, verified_at, valid_until, created_at, updated_at)
			VALUES ('bds6', 'bflv', 'bnd_dddddddddddddddddddddddddddddddd', 'ready', 1, 100, 9999999999, 50, 100)`)
		if err == nil {
			t.Error("expected CHECK violation for verified_at > created_at")
		}
	})

	t.Run("binding_active_activated_at_after_updated_at", func(t *testing.T) {
		// active binding with activated_at > updated_at violates CHECK (activated_at <= updated_at).
		_, err := db.Exec(`INSERT INTO v2_client_notification_bindings
			(id, flow_id, binding_ref, state, window_number, verified_at, valid_until, activated_at, created_at, updated_at)
			VALUES ('bds7', 'bfl2', 'bnd_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee', 'active', 1, 1, 9999999999, 200, 1, 100)`)
		if err == nil {
			t.Error("expected CHECK violation for active binding with activated_at > updated_at")
		}
	})

	// ── Listing constraints ──

	t.Run("listing_empty_display_name", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_listings
			(id,flow_id,city,country_code,dependency_type,help_type,urgency,languages,
			 display_name,contact_type,contact_ciphertext,contact_nonce,contact_key_version,
			 state,visible_until,first_published_at,last_activated_at,entitlement_expires_at,
			 activation_count,created_at,updated_at)
			VALUES ('l1','fl1','tbilisi','GE','alcohol','crisis','urgent','["en"]',
			 '','telegram','ct','nc','v1','visible',9999999999,1,1,9999999999,1,1,1)`)
		if err == nil {
			t.Error("expected CHECK violation for empty display_name")
		}
	})

	t.Run("listing_empty_ciphertext", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_listings
			(id,flow_id,city,country_code,dependency_type,help_type,urgency,languages,
			 display_name,contact_type,contact_ciphertext,contact_nonce,contact_key_version,
			 state,visible_until,first_published_at,last_activated_at,entitlement_expires_at,
			 activation_count,created_at,updated_at)
			VALUES ('l2','fl2','tbilisi','GE','alcohol','crisis','urgent','["en"]',
			 'name2','telegram','','nc','v1','visible',9999999999,1,1,9999999999,1,1,1)`)
		if err == nil {
			t.Error("expected CHECK violation for empty contact_ciphertext")
		}
	})

	t.Run("listing_first_published_after_last_activated", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_listings
			(id,flow_id,city,country_code,dependency_type,help_type,urgency,languages,
			 display_name,contact_type,contact_ciphertext,contact_nonce,contact_key_version,
			 state,visible_until,first_published_at,last_activated_at,entitlement_expires_at,
			 activation_count,created_at,updated_at)
			VALUES ('l3','fl3','tbilisi','GE','alcohol','crisis','urgent','["en"]',
			 'name3','telegram','ct','nc','v1','visible',9999999999,100,50,9999999999,1,1,100)`)
		if err == nil {
			t.Error("expected CHECK violation for first_published_at > last_activated_at")
		}
	})

	t.Run("listing_last_activated_after_entitlement", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_listings
			(id,flow_id,city,country_code,dependency_type,help_type,urgency,languages,
			 display_name,contact_type,contact_ciphertext,contact_nonce,contact_key_version,
			 state,visible_until,first_published_at,last_activated_at,entitlement_expires_at,
			 activation_count,created_at,updated_at)
			VALUES ('l4','fl4','tbilisi','GE','alcohol','crisis','urgent','["en"]',
			 'name4','telegram','ct','nc','v1','hidden',NULL,1,9999999999,100,1,1,1)`)
		if err == nil {
			t.Error("expected CHECK violation for last_activated_at > entitlement_expires_at")
		}
	})

	t.Run("listing_created_after_first_published", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_listings
			(id,flow_id,city,country_code,dependency_type,help_type,urgency,languages,
			 display_name,contact_type,contact_ciphertext,contact_nonce,contact_key_version,
			 state,visible_until,first_published_at,last_activated_at,entitlement_expires_at,
			 activation_count,created_at,updated_at)
			VALUES ('l5','fl5','tbilisi','GE','alcohol','crisis','urgent','["en"]',
			 'name5','telegram','ct','nc','v1','hidden',NULL,1,1,9999999999,1,100,100)`)
		if err == nil {
			t.Error("expected CHECK violation for created_at > first_published_at")
		}
	})

	t.Run("listing_updated_before_created", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_listings
			(id,flow_id,city,country_code,dependency_type,help_type,urgency,languages,
			 display_name,contact_type,contact_ciphertext,contact_nonce,contact_key_version,
			 state,visible_until,first_published_at,last_activated_at,entitlement_expires_at,
			 activation_count,created_at,updated_at)
			VALUES ('l6','fl6','tbilisi','GE','alcohol','crisis','urgent','["en"]',
			 'name6','telegram','ct','nc','v1','hidden',NULL,100,100,9999999999,1,100,50)`)
		if err == nil {
			t.Error("expected CHECK violation for updated_at < created_at")
		}
	})

	t.Run("listing_visible_window_exceeds_entitlement", func(t *testing.T) {
		// visible_until (10000000000) > entitlement_expires_at (9999999999).
		_, err := db.Exec(`INSERT INTO v2_listings
			(id,flow_id,city,country_code,dependency_type,help_type,urgency,languages,
			 display_name,contact_type,contact_ciphertext,contact_nonce,contact_key_version,
			 state,visible_until,first_published_at,last_activated_at,entitlement_expires_at,
			 activation_count,created_at,updated_at)
			VALUES ('l7','fl7','tbilisi','GE','alcohol','crisis','urgent','["en"]',
			 'name7','telegram','ct','nc','v1','visible',10000000000,1,1,9999999999,1,1,1)`)
		if err == nil {
			t.Error("expected CHECK violation for visible_until > entitlement_expires_at")
		}
	})

	// Verify that a valid row still passes all new constraints.
	t.Run("valid_row_passes", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO v2_listings
			(id,flow_id,city,country_code,dependency_type,help_type,urgency,languages,
			 display_name,contact_type,contact_ciphertext,contact_nonce,contact_key_version,
			 state,visible_until,first_published_at,last_activated_at,entitlement_expires_at,
			 activation_count,created_at,updated_at)
			VALUES ('lv','flv','tbilisi','GE','alcohol','crisis','urgent','["en"]',
			 'valid_name','telegram','ciphertext_ok','nonce_ok','v1',
			 'visible',9999999998,1,1,9999999999,1,1,1)`)
		if err != nil {
			t.Errorf("valid row rejected: %v", err)
		}
	})
}

// ── Watcher pollInterval test ─────────────────────────────────────────────────

func TestWatcherPollIntervalNegativeRejected(t *testing.T) {
	svc, _ := newTestService(t)
	chain := newFakeChainClient()
	bal := &fakeBalanceReader{}

	_, err := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, nil, nil, -1,
	)
	if err == nil {
		t.Error("expected error for negative pollInterval, got nil")
	}

	// Zero should succeed (defaults to 5s).
	w, err := NewV2Watcher(svc,
		map[string]V2ChainClient{"BTC": chain},
		bal, nil, nil, 0,
	)
	if err != nil {
		t.Errorf("pollInterval=0: got error %v, want nil", err)
	}
	if w == nil {
		t.Error("watcher nil for pollInterval=0")
	}
	if w != nil && w.pollInterval != defaultPollInterval {
		t.Errorf("default pollInterval: got %v, want %v", w.pollInterval, defaultPollInterval)
	}
}

// ── Production display-name generator tests ───────────────────────────────────

// TestProductionGeneratorFormat checks that Generate produces a name that:
//   - passes validateDisplayName (non-empty, valid UTF-8, ≤64 bytes, [a-z0-9_]);
//   - has the form <adj>_<noun>_<32 lowercase hex chars>;
//   - whose suffix decodes to exactly 16 bytes.
func TestProductionGeneratorFormat(t *testing.T) {
	g := NewRandomDisplayNameGenerator()
	name, err := g.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := validateDisplayName(name); err != nil {
		t.Errorf("name %q failed validateDisplayName: %v", name, err)
	}
	// Expect exactly 3 underscore-separated tokens: adj, noun, 32-char hex suffix.
	// Use SplitN with n=4 so we detect extra underscores in the hex suffix as well.
	parts := strings.SplitN(name, "_", 4)
	if len(parts) != 3 {
		t.Fatalf("expected 3 underscore-delimited parts, got %d in %q", len(parts), name)
	}
	suffix := parts[2]
	if len(suffix) != 32 {
		t.Errorf("suffix length: got %d, want 32 in %q", len(suffix), name)
	}
	decoded, err := hex.DecodeString(suffix)
	if err != nil {
		t.Errorf("suffix %q is not valid hex: %v", suffix, err)
	}
	if len(decoded) != 16 {
		t.Errorf("decoded suffix: got %d bytes, want 16", len(decoded))
	}
}

// TestProductionGeneratorNoDuplicates generates 1 000 names and verifies that
// none collide. This is not a proof of uniqueness; it guards against accidental
// determinism (e.g. seeded PRNG, constant suffix). The real uniqueness contract
// is 128 random bits + DB UNIQUE + bounded retry.
func TestProductionGeneratorNoDuplicates(t *testing.T) {
	g := NewRandomDisplayNameGenerator()
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		name, err := g.Generate()
		if err != nil {
			t.Fatalf("Generate at i=%d: %v", i, err)
		}
		if _, dup := seen[name]; dup {
			t.Errorf("duplicate name at i=%d: %q", i, name)
		}
		seen[name] = struct{}{}
	}
}

// ── Production contact validator tests ────────────────────────────────────────

func TestProductionContactValidator(t *testing.T) {
	cv := NewProductionContactValidator()

	// ── Telegram valid ──
	t.Run("tg_at_username", func(t *testing.T) {
		got, err := cv.ValidateContact("telegram", "@testuser")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://t.me/testuser" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("tg_at_uppercase_normalized", func(t *testing.T) {
		got, err := cv.ValidateContact("telegram", "@TestUser")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://t.me/testuser" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("tg_url_t_me", func(t *testing.T) {
		got, err := cv.ValidateContact("telegram", "https://t.me/myuser")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://t.me/myuser" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("tg_url_subdomain", func(t *testing.T) {
		got, err := cv.ValidateContact("telegram", "https://myuser.t.me")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://t.me/myuser" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("tg_url_subdomain_trailing_slash", func(t *testing.T) {
		got, err := cv.ValidateContact("telegram", "https://myuser.t.me/")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://t.me/myuser" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("tg_short_username", func(t *testing.T) {
		got, err := cv.ValidateContact("telegram", "@x")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://t.me/x" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("tg_max_length_username", func(t *testing.T) {
		name := strings.Repeat("a", 64)
		got, err := cv.ValidateContact("telegram", "@"+name)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://t.me/"+name {
			t.Errorf("got %q", got)
		}
	})
	t.Run("tg_reject_phone_link", func(t *testing.T) {
		_, err := cv.ValidateContact("telegram", "https://t.me/+1234567890")
		if err == nil {
			t.Error("expected error for phone link")
		}
		if err != nil && strings.Contains(err.Error(), "+1234567890") {
			t.Errorf("error leaks raw contact: %v", err)
		}
	})
	t.Run("tg_reject_extra_path", func(t *testing.T) {
		_, err := cv.ValidateContact("telegram", "https://t.me/user/extra")
		if err == nil {
			t.Error("expected error for extra path segment")
		}
	})
	t.Run("tg_reject_reserved_joinchat", func(t *testing.T) {
		_, err := cv.ValidateContact("telegram", "@joinchat")
		if err == nil {
			t.Error("expected error for reserved word joinchat")
		}
	})
	t.Run("tg_reject_reserved_s", func(t *testing.T) {
		_, err := cv.ValidateContact("telegram", "@s")
		if err == nil {
			t.Error("expected error for reserved word s")
		}
	})
	t.Run("tg_reject_reserved_c", func(t *testing.T) {
		_, err := cv.ValidateContact("telegram", "@c")
		if err == nil {
			t.Error("expected error for reserved word c")
		}
	})
	t.Run("tg_reject_http_scheme", func(t *testing.T) {
		_, err := cv.ValidateContact("telegram", "http://t.me/user")
		if err == nil {
			t.Error("expected error for http scheme")
		}
	})
	t.Run("tg_reject_port", func(t *testing.T) {
		_, err := cv.ValidateContact("telegram", "https://t.me:8080/user")
		if err == nil {
			t.Error("expected error for port")
		}
	})
	t.Run("tg_reject_query", func(t *testing.T) {
		_, err := cv.ValidateContact("telegram", "https://t.me/user?foo=bar")
		if err == nil {
			t.Error("expected error for query")
		}
	})
	t.Run("tg_reject_too_long_username", func(t *testing.T) {
		_, err := cv.ValidateContact("telegram", "@"+strings.Repeat("a", 65))
		if err == nil {
			t.Error("expected error for too long username")
		}
	})
	t.Run("tg_reject_empty_username", func(t *testing.T) {
		_, err := cv.ValidateContact("telegram", "@")
		if err == nil {
			t.Error("expected error for empty username")
		}
	})
	t.Run("tg_reject_invalid_chars", func(t *testing.T) {
		_, err := cv.ValidateContact("telegram", "@user-name")
		if err == nil {
			t.Error("expected error for invalid char -")
		}
	})
	t.Run("tg_reject_whitespace_in_input", func(t *testing.T) {
		_, err := cv.ValidateContact("telegram", "@user name")
		if err == nil {
			t.Error("expected error for whitespace in input")
		}
	})
	t.Run("tg_reject_too_long_input", func(t *testing.T) {
		_, err := cv.ValidateContact("telegram", strings.Repeat("x", 513))
		if err == nil {
			t.Error("expected error for input > 512 bytes")
		}
	})
	t.Run("tg_reject_invalid_utf8", func(t *testing.T) {
		_, err := cv.ValidateContact("telegram", string([]byte{0xFF, 0xFE}))
		if err == nil {
			t.Error("expected error for invalid UTF-8")
		}
	})
	t.Run("signal_valid", func(t *testing.T) {
		got, err := cv.ValidateContact("signal", "https://signal.me/#p/abc123XYZ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://signal.me/#p/abc123XYZ" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("signal_preserves_fragment_verbatim", func(t *testing.T) {
		frag := "SomeOpaquePayload_WithSpecialChars.123"
		got, err := cv.ValidateContact("signal", "https://signal.me/#"+frag)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://signal.me/#"+frag {
			t.Errorf("got %q, want fragment preserved verbatim", got)
		}
	})
	t.Run("signal_max_fragment", func(t *testing.T) {
		frag := strings.Repeat("a", 300)
		got, err := cv.ValidateContact("signal", "https://signal.me/#"+frag)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://signal.me/#"+frag {
			t.Errorf("got %q", got)
		}
	})
	t.Run("signal_reject_no_fragment", func(t *testing.T) {
		_, err := cv.ValidateContact("signal", "https://signal.me/")
		if err == nil {
			t.Error("expected error for no fragment")
		}
	})
	t.Run("signal_reject_empty_fragment", func(t *testing.T) {
		_, err := cv.ValidateContact("signal", "https://signal.me/#")
		if err == nil {
			t.Error("expected error for empty fragment")
		}
	})
	t.Run("signal_reject_wrong_host", func(t *testing.T) {
		_, err := cv.ValidateContact("signal", "https://evil.me/#abc")
		if err == nil {
			t.Error("expected error for wrong host")
		}
	})
	t.Run("signal_reject_http", func(t *testing.T) {
		_, err := cv.ValidateContact("signal", "http://signal.me/#abc")
		if err == nil {
			t.Error("expected error for http scheme")
		}
	})
	t.Run("signal_reject_port", func(t *testing.T) {
		_, err := cv.ValidateContact("signal", "https://signal.me:443/#abc")
		if err == nil {
			t.Error("expected error for port")
		}
	})
	t.Run("signal_reject_query", func(t *testing.T) {
		_, err := cv.ValidateContact("signal", "https://signal.me/?foo=bar#abc")
		if err == nil {
			t.Error("expected error for query")
		}
	})
	t.Run("signal_reject_path", func(t *testing.T) {
		_, err := cv.ValidateContact("signal", "https://signal.me/extra#abc")
		if err == nil {
			t.Error("expected error for non-empty path")
		}
	})
	t.Run("signal_reject_fragment_too_long", func(t *testing.T) {
		frag := strings.Repeat("a", 301)
		_, err := cv.ValidateContact("signal", "https://signal.me/#"+frag)
		if err == nil {
			t.Error("expected error for fragment > 300 bytes")
		}
	})
	t.Run("signal_reject_fragment_with_space", func(t *testing.T) {
		_, err := cv.ValidateContact("signal", "https://signal.me/#abc def")
		if err == nil {
			t.Error("expected error for whitespace in fragment")
		}
	})
	t.Run("signal_reject_userinfo", func(t *testing.T) {
		_, err := cv.ValidateContact("signal", "https://user@signal.me/#abc")
		if err == nil {
			t.Error("expected error for userinfo")
		}
	})
	t.Run("signal_no_raw_contact_in_error", func(t *testing.T) {
		raw := "https://signal.me/extra#secretpayload"
		_, err := cv.ValidateContact("signal", raw)
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Contains(err.Error(), "secretpayload") {
			t.Errorf("error leaks raw contact: %v", err)
		}
	})
	// ── Percent-encoded path regression (ambiguous URLs must be rejected) ──
	t.Run("tg_reject_percent_encoded_username", func(t *testing.T) {
		// %75 decodes to 'u'; url.Parse sets RawPath which we must reject.
		_, err := cv.ValidateContact("telegram", "https://t.me/%75ser")
		if err == nil {
			t.Error("expected error for percent-encoded username path")
		}
		if err != nil && strings.Contains(err.Error(), "%75ser") {
			t.Errorf("error leaks raw contact: %v", err)
		}
	})
	t.Run("tg_reject_percent_encoded_separator", func(t *testing.T) {
		// %2F is a percent-encoded '/'; path appears as /user%2Fextra.
		_, err := cv.ValidateContact("telegram", "https://t.me/user%2Fextra")
		if err == nil {
			t.Error("expected error for percent-encoded separator in path")
		}
	})
}

// ── Binding lifecycle tests ────────────────────────────────────────────────────

func TestBindingFirstWindowIsOne(t *testing.T) {
	ls, svc, db := newTestListingService(t, nil)
	_, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)

	var wn int
	db.QueryRow(`SELECT window_number FROM v2_client_notification_bindings WHERE flow_id=?`, flowID).Scan(&wn) //nolint:errcheck
	if wn != 1 {
		t.Errorf("window_number: got %d, want 1", wn)
	}
}

func TestBindingReadyTTLValidation(t *testing.T) {
	ls, svc, _ := newTestListingService(t, nil)
	_, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	now := time.Now()

	if err := ls.attachReadyBinding(flowID, newBindingRef(), now, now); !errors.Is(err, ErrBindingTTLInvalid) {
		t.Errorf("TTL=0: got %v, want ErrBindingTTLInvalid", err)
	}
	if err := ls.attachReadyBinding(flowID, newBindingRef(), now, now.Add(-1*time.Second)); !errors.Is(err, ErrBindingTTLInvalid) {
		t.Errorf("negative TTL: got %v, want ErrBindingTTLInvalid", err)
	}
	if err := ls.attachReadyBinding(flowID, newBindingRef(), now, now.Add(16*time.Minute)); !errors.Is(err, ErrBindingTTLInvalid) {
		t.Errorf("TTL>15m: got %v, want ErrBindingTTLInvalid", err)
	}
	if err := ls.attachReadyBinding(flowID, newBindingRef(), now, now.Add(1*time.Second)); err != nil {
		t.Errorf("TTL=1s: got %v, want success", err)
	}
	ls.InvalidateBinding(flowID) //nolint:errcheck
	if err := ls.attachReadyBinding(flowID, newBindingRef(), now, now.Add(15*time.Minute)); err != nil {
		t.Errorf("TTL=15m: got %v, want success", err)
	}
}

func TestBindingActiveCannotBeReplaced(t *testing.T) {
	ls, svc, _ := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Fatalf("FirstPublish: %v", err)
	}
	now := ls.now()
	err := ls.attachReadyBinding(flowID, newBindingRef(), now, now.Add(10*time.Minute))
	if !errors.Is(err, ErrAlreadyVisible) {
		t.Errorf("got %v, want ErrAlreadyVisible", err)
	}
}

func TestBindingExpiredReadyCanBeReplaced(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = now
	mu.Unlock()

	ls, svc, db := newTestListingService(t, getNow)
	_, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")

	if err := ls.attachReadyBinding(flowID, newBindingRef(), now, now.Add(5*time.Minute)); err != nil {
		t.Fatalf("first attach: %v", err)
	}

	mu.Lock()
	clockNow = now.Add(6 * time.Minute)
	mu.Unlock()

	now2 := ls.now()
	if err := ls.attachReadyBinding(flowID, newBindingRef(), now2, now2.Add(10*time.Minute)); err != nil {
		t.Errorf("replace expired ready: got %v, want success", err)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings WHERE flow_id=?`, flowID).Scan(&count) //nolint:errcheck
	if count != 1 {
		t.Errorf("binding count: got %d, want 1", count)
	}
}

func TestBindingNextWindowNumberAfterExpiry(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ls, svc, db := newTestListingService(t, func() time.Time { return now })
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Fatalf("FirstPublish: %v", err)
	}
	later := now.Add(25 * time.Hour)
	ls.NormalizeExpired(later) //nolint:errcheck
	attachNextWindowBinding(t, ls, flowID)

	var wn int
	db.QueryRow(`SELECT window_number FROM v2_client_notification_bindings WHERE flow_id=?`, flowID).Scan(&wn) //nolint:errcheck
	if wn != 2 {
		t.Errorf("window_number after first publish: got %d, want 2", wn)
	}
}

func TestBindingExpiredReadyBlocksPublish(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = now
	mu.Unlock()

	ls, svc, _ := newTestListingService(t, getNow)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")

	if err := ls.attachReadyBinding(flowID, newBindingRef(), now, now.Add(1*time.Minute)); err != nil {
		t.Fatalf("attach: %v", err)
	}

	mu.Lock()
	clockNow = now.Add(2 * time.Minute)
	mu.Unlock()

	_, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if !errors.Is(err, ErrBindingRequired) {
		t.Errorf("expired ready binding: got %v, want ErrBindingRequired", err)
	}
}

func TestBindingInvalidateDeletes(t *testing.T) {
	ls, svc, db := newTestListingService(t, nil)
	_, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)

	if err := ls.InvalidateBinding(flowID); err != nil {
		t.Fatalf("InvalidateBinding: %v", err)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings WHERE flow_id=?`, flowID).Scan(&count) //nolint:errcheck
	if count != 0 {
		t.Errorf("binding count after invalidate: got %d, want 0", count)
	}
}

func TestFirstPublishConsumesReadyBinding(t *testing.T) {
	ls, svc, db := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)

	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Fatalf("FirstPublish: %v", err)
	}

	var state string
	db.QueryRow(`SELECT state FROM v2_client_notification_bindings WHERE flow_id=?`, flowID).Scan(&state) //nolint:errcheck
	if state != "active" {
		t.Errorf("binding state after publish: got %q, want active", state)
	}
}

func TestFirstPublishRollbackOnFailure(t *testing.T) {
	ls, svc, db := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)

	db.Exec(`CREATE TRIGGER v2_fp_rollback_test BEFORE INSERT ON v2_listings BEGIN SELECT RAISE(ABORT,'blocked'); END`) //nolint:errcheck
	defer db.Exec(`DROP TRIGGER IF EXISTS v2_fp_rollback_test`)                                                         //nolint:errcheck

	_, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if err == nil {
		t.Fatal("expected error from trigger, got nil")
	}

	var state string
	db.QueryRow(`SELECT state FROM v2_client_notification_bindings WHERE flow_id=?`, flowID).Scan(&state) //nolint:errcheck
	if state != "ready" {
		t.Errorf("binding state after rollback: got %q, want ready", state)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_listings`).Scan(&count) //nolint:errcheck
	if count != 0 {
		t.Errorf("listing rows after rollback: got %d, want 0", count)
	}
}

func TestFirstPublishConcurrentExactlyOne(t *testing.T) {
	ls, svc, db := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)

	const goroutines = 8
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ls.FirstPublish(rawCode, "bc1qtest", validListingInput()) //nolint:errcheck
		}()
	}
	wg.Wait()

	var listingCount, bindingCount int
	db.QueryRow(`SELECT COUNT(*) FROM v2_listings WHERE flow_id=?`, flowID).Scan(&listingCount)                                        //nolint:errcheck
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings WHERE flow_id=? AND state='active'`, flowID).Scan(&bindingCount) //nolint:errcheck
	if listingCount != 1 {
		t.Errorf("listing count: got %d, want 1", listingCount)
	}
	if bindingCount != 1 {
		t.Errorf("active binding count: got %d, want 1", bindingCount)
	}
}

func TestReactivateConsumesNextWindowBinding(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = now
	mu.Unlock()

	ls, svc, db := newTestListingService(t, getNow)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Fatalf("FirstPublish: %v", err)
	}

	mu.Lock()
	clockNow = now.Add(25 * time.Hour)
	mu.Unlock()
	ls.NormalizeExpired(ls.now()) //nolint:errcheck
	attachNextWindowBinding(t, ls, flowID)

	if _, err := ls.Reactivate(rawCode, "bc1qtest", 150.0); err != nil {
		t.Fatalf("Reactivate: %v", err)
	}

	var state string
	var wn int
	db.QueryRow(`SELECT state, window_number FROM v2_client_notification_bindings WHERE flow_id=?`, flowID).Scan(&state, &wn) //nolint:errcheck
	if state != "active" {
		t.Errorf("binding state after reactivate: got %q, want active", state)
	}
	if wn != 2 {
		t.Errorf("window_number: got %d, want 2", wn)
	}
}

func TestReactivateRollbackOnFailure(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = now
	mu.Unlock()

	ls, svc, db := newTestListingService(t, getNow)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Fatalf("FirstPublish: %v", err)
	}

	mu.Lock()
	clockNow = now.Add(25 * time.Hour)
	mu.Unlock()
	ls.NormalizeExpired(ls.now()) //nolint:errcheck
	attachNextWindowBinding(t, ls, flowID)

	// Use a far-future updated_at to pass the CHECK (updated_at >= created_at).
	finishAt := now.Add(25 * time.Hour).Unix()
	ls._testHook = func() {
		db.Exec(`UPDATE v2_listings SET state='finished', visible_until=NULL, updated_at=? WHERE flow_id=?`, finishAt, flowID) //nolint:errcheck
	}

	_, err := ls.Reactivate(rawCode, "bc1qtest", 150.0)
	if err == nil {
		t.Fatal("expected error from failed listing UPDATE, got nil")
	}

	var state string
	db.QueryRow(`SELECT state FROM v2_client_notification_bindings WHERE flow_id=?`, flowID).Scan(&state) //nolint:errcheck
	if state != "ready" {
		t.Errorf("binding state after rollback: got %q, want ready", state)
	}
}

func TestReactivateConcurrentExactlyOnce(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = now
	mu.Unlock()

	ls, svc, db := newTestListingService(t, getNow)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Fatalf("FirstPublish: %v", err)
	}

	mu.Lock()
	clockNow = now.Add(25 * time.Hour)
	mu.Unlock()
	ls.NormalizeExpired(ls.now()) //nolint:errcheck
	attachNextWindowBinding(t, ls, flowID)

	const goroutines = 8
	var wg sync.WaitGroup
	var succMu sync.Mutex
	successes := 0
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ls.Reactivate(rawCode, "bc1qtest", 150.0); err == nil {
				succMu.Lock()
				successes++
				succMu.Unlock()
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Errorf("concurrent reactivate successes: got %d, want 1", successes)
	}
	var count int
	db.QueryRow(`SELECT activation_count FROM v2_listings WHERE flow_id=?`, flowID).Scan(&count) //nolint:errcheck
	if count != 2 {
		t.Errorf("activation_count: got %d, want 2", count)
	}
}

func TestRaceBindingDeletedBeforePublish(t *testing.T) {
	ls, svc, db := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)

	ls._testHook = func() {
		ls.InvalidateBinding(flowID) //nolint:errcheck
	}

	_, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if !errors.Is(err, ErrBindingRequired) {
		t.Errorf("got %v, want ErrBindingRequired", err)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_listings`).Scan(&count) //nolint:errcheck
	if count != 0 {
		t.Errorf("listing count: got %d, want 0", count)
	}
}

func TestWrongWindowNumberBindingBlocked(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ls, svc, _ := newTestListingService(t, func() time.Time { return now })
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Fatalf("FirstPublish: %v", err)
	}
	later := now.Add(25 * time.Hour)
	ls.NormalizeExpired(later) //nolint:errcheck

	// No attachNextWindowBinding — no binding for window 2.
	_, err := ls.Reactivate(rawCode, "bc1qtest", 150.0)
	if !errors.Is(err, ErrBindingRequired) {
		t.Errorf("wrong window: got %v, want ErrBindingRequired", err)
	}
}

func TestNormalizeDeletesExpiredBindings(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = now
	mu.Unlock()

	ls, svc, db := newTestListingService(t, getNow)
	_, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")

	if err := ls.attachReadyBinding(flowID, newBindingRef(), now, now.Add(1*time.Minute)); err != nil {
		t.Fatalf("attach: %v", err)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings WHERE flow_id=?`, flowID).Scan(&count) //nolint:errcheck
	if count != 1 {
		t.Fatalf("expected 1 binding, got %d", count)
	}

	mu.Lock()
	clockNow = now.Add(2 * time.Minute)
	mu.Unlock()

	if err := ls.NormalizeExpired(ls.now()); err != nil {
		t.Fatalf("NormalizeExpired: %v", err)
	}

	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings WHERE flow_id=?`, flowID).Scan(&count) //nolint:errcheck
	if count != 0 {
		t.Errorf("binding count after NormalizeExpired: got %d, want 0", count)
	}
}

func TestBindingAbsentAfterDailyCleanup(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var clockNow time.Time
	mu := sync.Mutex{}
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }
	mu.Lock()
	clockNow = now
	mu.Unlock()

	ls, svc, db := newTestListingService(t, getNow)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Fatalf("FirstPublish: %v", err)
	}

	mu.Lock()
	clockNow = now.Add(25 * time.Hour)
	mu.Unlock()
	if err := ls.NormalizeExpired(ls.now()); err != nil {
		t.Fatalf("NormalizeExpired: %v", err)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_notification_bindings WHERE flow_id=?`, flowID).Scan(&count) //nolint:errcheck
	if count != 0 {
		t.Errorf("binding count after daily cleanup: got %d, want 0", count)
	}
}

func TestBoardAndSafeViewsUnchanged(t *testing.T) {
	ls, svc, _ := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	lv, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if err != nil {
		t.Fatalf("FirstPublish: %v", err)
	}

	lvJSON, _ := json.Marshal(lv)
	lvStr := strings.ToLower(string(lvJSON))
	for _, forbidden := range []string{"binding", "window_number", "activated_at", "binding_ref"} {
		if strings.Contains(lvStr, forbidden) {
			t.Errorf("ListingView JSON contains binding field %q: %s", forbidden, lvStr)
		}
	}

	views, err := ls.BoardQuery("tbilisi", ls.now())
	if err != nil || len(views) == 0 {
		t.Fatalf("BoardQuery: %v / %d results", err, len(views))
	}
	pvJSON, _ := json.Marshal(views[0])
	pvStr := strings.ToLower(string(pvJSON))
	for _, forbidden := range []string{"binding", "window_number", "activated_at"} {
		if strings.Contains(pvStr, forbidden) {
			t.Errorf("PublicListingView JSON contains binding field %q: %s", forbidden, pvStr)
		}
	}
}

func TestFullDBScanNoRawContact(t *testing.T) {
	rawContact := "@full_scan_secret_12345"
	ls, svc, db := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID)
	input := validListingInput()
	input.RawContact = rawContact
	if _, err := ls.FirstPublish(rawCode, "bc1qtest", input); err != nil {
		t.Fatalf("FirstPublish: %v", err)
	}

	rows, err := db.Query(`SELECT id, flow_id, city, country_code, dependency_type,
		help_type, urgency, languages, display_name, contact_type,
		contact_ciphertext, contact_nonce, contact_key_version, state
		FROM v2_listings`)
	if err != nil {
		t.Fatalf("listings query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cols [14]string
		dest := make([]any, 14)
		for i := range cols {
			dest[i] = &cols[i]
		}
		if err = rows.Scan(dest...); err != nil {
			t.Fatalf("scan: %v", err)
		}
		for _, col := range cols {
			if strings.Contains(col, rawContact) {
				t.Errorf("raw contact found in v2_listings column: %q", col)
			}
		}
	}

	brows, err := db.Query(`SELECT id, flow_id, binding_ref, state FROM v2_client_notification_bindings`)
	if err != nil {
		t.Fatalf("bindings query: %v", err)
	}
	defer brows.Close()
	for brows.Next() {
		var cols [4]string
		dest := make([]any, 4)
		for i := range cols {
			dest[i] = &cols[i]
		}
		if err = brows.Scan(dest...); err != nil {
			t.Fatalf("binding scan: %v", err)
		}
		for _, col := range cols {
			if strings.Contains(col, rawContact) {
				t.Errorf("raw contact found in v2_client_notification_bindings column: %q", col)
			}
		}
	}
}

// ── Task 04B-FIX: adversarial tests ──────────────────────────────────────────

// TestFirstPublishWithoutDestinationRollsBack verifies that FirstPublish returns
// ErrDestinationMissing and does NOT create a listing when the ready binding has
// no corresponding destination row.
func TestFirstPublishWithoutDestinationRollsBack(t *testing.T) {
	ls, svc, db := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")

	// Attach binding WITHOUT inserting a destination.
	now := ls.now()
	ref := newBindingRef()
	if err := ls.attachReadyBinding(flowID, ref, now, now.Add(10*time.Minute)); err != nil {
		t.Fatalf("attachReadyBinding: %v", err)
	}

	_, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if !errors.Is(err, ErrDestinationMissing) {
		t.Errorf("got %v, want ErrDestinationMissing", err)
	}

	// Listing must NOT be created.
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_listings`).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Errorf("listing created despite missing destination: count %d", cnt)
	}

	// Binding must have been rolled back to 'ready' (not 'active').
	var state string
	db.QueryRow(`SELECT state FROM v2_client_notification_bindings WHERE binding_ref=?`, ref).Scan(&state) //nolint:errcheck
	if state != "ready" {
		t.Errorf("binding state after rollback: got %q, want ready", state)
	}
}

// TestReactivateWithoutDestinationRollsBack verifies that Reactivate returns
// ErrDestinationMissing and does NOT update the listing when the ready binding
// has no corresponding destination row.
func TestReactivateWithoutDestinationRollsBack(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	var clockNow = now
	getNow := func() time.Time { mu.Lock(); defer mu.Unlock(); return clockNow }

	ls, svc, db := newTestListingService(t, getNow)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")
	attachTestBinding(t, ls, flowID) // has destination → FirstPublish works

	if _, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput()); err != nil {
		t.Fatalf("FirstPublish: %v", err)
	}

	// Advance 25h, normalize.
	mu.Lock()
	clockNow = now.Add(25 * time.Hour)
	mu.Unlock()
	ls.NormalizeExpired(clockNow) //nolint:errcheck

	// Attach binding for window 2 WITHOUT destination.
	cn := clockNow
	ref2 := newBindingRef()
	if err := ls.attachReadyBinding(flowID, ref2, cn, cn.Add(10*time.Minute)); err != nil {
		t.Fatalf("attachReadyBinding w2: %v", err)
	}
	// Do NOT insert destination for ref2.

	_, err := ls.Reactivate(rawCode, "bc1qtest", 150.0)
	if !errors.Is(err, ErrDestinationMissing) {
		t.Errorf("got %v, want ErrDestinationMissing", err)
	}

	// Listing must remain 'hidden', NOT become 'visible'.
	var state string
	db.QueryRow(`SELECT state FROM v2_listings WHERE flow_id=?`, flowID).Scan(&state) //nolint:errcheck
	if state != "hidden" {
		t.Errorf("listing state after rollback: got %q, want hidden", state)
	}

	// activation_count must remain 1.
	var count int
	db.QueryRow(`SELECT activation_count FROM v2_listings WHERE flow_id=?`, flowID).Scan(&count) //nolint:errcheck
	if count != 1 {
		t.Errorf("activation_count changed: got %d, want 1", count)
	}
}

// TestFirstPublishDestinationRowsAffectedExactlyOne verifies that having a
// destination deleted before FirstPublish (simulating race or inconsistency)
// returns ErrDestinationMissing and does not create a listing.
func TestFirstPublishDestinationRowsAffectedExactlyOne(t *testing.T) {
	ls, svc, db := newTestListingService(t, nil)
	rawCode, flowID := makeFormReadyFlow(t, svc, "bc1qtest", "BTC")

	// Attach binding WITH destination, then delete the destination.
	now := ls.now()
	ref := newBindingRef()
	if err := ls.attachReadyBinding(flowID, ref, now, now.Add(10*time.Minute)); err != nil {
		t.Fatalf("attachReadyBinding: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO v2_telegram_destinations
		(binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at)
		VALUES (?, 'stub_ct', 'stub_nonce', 'test_v1', ?, ?)`,
		ref, now.Unix(), now.Add(10*time.Minute).Unix()); err != nil {
		t.Fatalf("insert destination: %v", err)
	}
	// Delete destination (simulates race or inconsistency).
	db.Exec(`DELETE FROM v2_telegram_destinations WHERE binding_ref=?`, ref) //nolint:errcheck

	_, err := ls.FirstPublish(rawCode, "bc1qtest", validListingInput())
	if !errors.Is(err, ErrDestinationMissing) {
		t.Errorf("missing destination: got %v, want ErrDestinationMissing", err)
	}

	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM v2_listings`).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Errorf("listing created: %d rows", cnt)
	}
}
