package v2

// Boundary tests for the final production balance policy (task: "финальные
// лимиты"): Client $150/$120, Helper $1010/$1000, Informer $1000. Every test
// here constructs its own in-memory service via DefaultV2BalancePolicy() (the
// same defaults cmd/naroom/v2wire.go uses when no env override is present) —
// no production DB is used, per the task's explicit requirement.
//
// Client pre-invoice ($150, "до публикации"): investigated and confirmed this
// value has NO backend accept/reject gate anywhere in the code — handleCreate
// (payment-intent creation) never calls a balance provider at all. It is
// purely the ClientPublicMinUSD figure surfaced via GET /v2/public-config and
// shown in the frontend's advisory wallet-hint text (matches PRODUCT_SPEC.md
// §3.1: "$150 public / $120 hard floor... intentional volatility/fee
// buffer", not a second gate). So there is no "150.00 accept / 149.99 reject"
// case to test here; TestPublicConfig_FinalValues below proves the *reported*
// number is correct instead.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ── Client post-payment floor: $120.00 accept, $119.99 reject ────────────────

func TestClientPolicyBoundary_HardFloor120(t *testing.T) {
	floor := DefaultV2BalancePolicy().ClientHardFloorUSD
	if floor != 120.0 {
		t.Fatalf("sanity: DefaultV2BalancePolicy().ClientHardFloorUSD = %v, want 120.0", floor)
	}

	t.Run("119.99_rejected", func(t *testing.T) {
		svc, _ := newTestService(t)
		_, fv := createIntent(t, svc, "bc1qtest", "BTC")
		fv = confirmPayment(t, svc, fv, "txid-119")
		fv, err := svc.RecordPostPaymentBalance(fv.FlowID, 119.99, floor)
		if err != nil {
			t.Fatalf("RecordPostPaymentBalance: %v", err)
		}
		if fv.State != StatePaidLowBalance {
			t.Errorf("balance $119.99 < $120 floor: state = %q, want %q", fv.State, StatePaidLowBalance)
		}
	})

	t.Run("120.00_accepted", func(t *testing.T) {
		svc, _ := newTestService(t)
		_, fv := createIntent(t, svc, "bc1qtest", "BTC")
		fv = confirmPayment(t, svc, fv, "txid-120")
		fv, err := svc.RecordPostPaymentBalance(fv.FlowID, 120.00, floor)
		if err != nil {
			t.Fatalf("RecordPostPaymentBalance: %v", err)
		}
		if fv.State != StateFormReady {
			t.Errorf("balance $120.00 == $120 floor: state = %q, want %q (inclusive boundary)", fv.State, StateFormReady)
		}
	})
}

// ── Helper pre-invoice floor: $1010.00 accept, $1009.99 reject ───────────────

func TestHelperPolicyBoundary_PreInvoice1010(t *testing.T) {
	want := DefaultV2BalancePolicy().HelperPreInvoiceMinUSD()
	if want != 1010.0 {
		t.Fatalf("sanity: HelperPreInvoiceMinUSD() = %v, want 1010.0", want)
	}

	t.Run("1009.99_rejected", func(t *testing.T) {
		h, svc := newTestHelperHandler(t, 1009.99)
		listingID := mustCreateVisibleListing(t, svc.db, "US")
		rr := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
			"purchase_token": newID(),
			"listing_id":     listingID,
			"wallet_address": testBTCBech32Addr,
		})
		if rr.Code != http.StatusPaymentRequired {
			t.Errorf("balance $1009.99 < $1010 floor: status = %d, want %d (%s)", rr.Code, http.StatusPaymentRequired, rr.Body)
		}
	})

	t.Run("1010.00_accepted", func(t *testing.T) {
		h, svc := newTestHelperHandler(t, 1010.00)
		listingID := mustCreateVisibleListing(t, svc.db, "US")
		rr := helperPost(t, h.Routes(), "/v2/helper/contact-purchases", map[string]string{
			"purchase_token": newID(),
			"listing_id":     listingID,
			"wallet_address": testBTCBech32Addr,
		})
		if rr.Code != http.StatusCreated {
			t.Errorf("balance $1010.00 == $1010 floor: status = %d, want %d (inclusive boundary) (%s)", rr.Code, http.StatusCreated, rr.Body)
		}
	})
}

// ── Helper post-payment floor: $1000.00 accept, $999.99 reject ───────────────

func TestHelperPolicyBoundary_PostPayment1000(t *testing.T) {
	want := DefaultV2BalancePolicy().HelperPostPaymentMinUSD
	if want != 1000.0 {
		t.Fatalf("sanity: HelperPostPaymentMinUSD = %v, want 1000.0", want)
	}

	newReadyPurchase := func(t *testing.T) (*HelperPurchaseService, string) {
		t.Helper()
		svc, db := newTestHelperService(t)
		listingID := mustCreateVisibleListing(t, db, "US")
		draft := HelperInvoiceDraft{PaymentAddress: "payaddr_boundary", AmountAtomic: 100000, AmountUSDCents: helperInvoiceUSDCents}
		_, pv, err := svc.CreatePurchase(newID(), listingID, "BTC", testBTCBech32Addr, draft)
		if err != nil {
			t.Fatalf("CreatePurchase: %v", err)
		}
		// Simulate watcher: detect then confirm payment before the post-payment
		// balance check is allowed (matches the real production sequence).
		if _, err := svc.RecordHelperDetection(pv.PurchaseID, "txid_boundary", []string{testBTCBech32Addr}, 100000, time.Now()); err != nil {
			t.Fatalf("RecordHelperDetection: %v", err)
		}
		if _, err := svc.ConfirmHelperPayment(pv.PurchaseID, time.Now()); err != nil {
			t.Fatalf("ConfirmHelperPayment: %v", err)
		}
		return svc, pv.PurchaseID
	}

	t.Run("999.99_rejected", func(t *testing.T) {
		svc, purchaseID := newReadyPurchase(t)
		pv, err := svc.RecordHelperPostPaymentBalance(purchaseID, 999.99)
		if err != nil {
			t.Fatalf("RecordHelperPostPaymentBalance: %v", err)
		}
		if pv.State == HPStateContactReady {
			t.Errorf("balance $999.99 < $1000 floor: state = %q, must NOT be %q", pv.State, HPStateContactReady)
		}
	})

	t.Run("1000.00_accepted", func(t *testing.T) {
		svc, purchaseID := newReadyPurchase(t)
		pv, err := svc.RecordHelperPostPaymentBalance(purchaseID, 1000.00)
		if err != nil {
			t.Fatalf("RecordHelperPostPaymentBalance: %v", err)
		}
		if pv.State != HPStateContactReady {
			t.Errorf("balance $1000.00 == $1000 floor: state = %q, want %q (inclusive boundary)", pv.State, HPStateContactReady)
		}
	})
}

// ── Informer floor: $1000.00 accept, $999.99 reject ──────────────────────────

func TestInformerPolicyBoundary_1000(t *testing.T) {
	want := DefaultV2BalancePolicy().InformerMinUSD
	if want != 1000.0 {
		t.Fatalf("sanity: InformerMinUSD = %v, want 1000.0", want)
	}

	newBoundaryInformerSvc := func(t *testing.T) *InformerService {
		t.Helper()
		db, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { db.Close() })
		destCipher, err := NewDestinationCipher(make([]byte, 32), "test_v1")
		if err != nil {
			t.Fatalf("NewDestinationCipher: %v", err)
		}
		svc, err := NewInformerService(db, testHMACKey, []byte("test-token-secret-32-bytes-12345"), destCipher, time.Now)
		if err != nil {
			t.Fatalf("NewInformerService: %v", err)
		}
		return svc
	}

	t.Run("999.99_rejected", func(t *testing.T) {
		svc := newBoundaryInformerSvc(t)
		if _, _, err := svc.CreateAccess("tbilisi", 999.99); err == nil {
			t.Error("balance $999.99 < $1000 floor: expected error, got nil")
		}
	})

	t.Run("1000.00_accepted", func(t *testing.T) {
		svc := newBoundaryInformerSvc(t)
		if _, _, err := svc.CreateAccess("tbilisi", 1000.00); err != nil {
			t.Errorf("balance $1000.00 == $1000 floor: expected success (inclusive boundary), got: %v", err)
		}
	})
}

// ── Regression item 7: GET /v2/public-config reports the final values ────────

func TestPublicConfig_FinalValues(t *testing.T) {
	c := newE2EComp(t)
	mux := http.NewServeMux()
	c.sys.MountRoutes(mux)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v2/public-config", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /v2/public-config: status %d, body: %s", rr.Code, rr.Body)
	}

	var got struct {
		ClientFeeUSDCents       int     `json:"client_fee_usd_cents"`
		ClientPublicMinUSD      float64 `json:"client_public_min_usd"`
		ClientHardFloorUSD      float64 `json:"client_hard_floor_usd"`
		HelperFeeUSDCents       int     `json:"helper_fee_usd_cents"`
		HelperPreInvoiceMinUSD  float64 `json:"helper_pre_invoice_min_usd"`
		HelperPostPaymentMinUSD float64 `json:"helper_post_payment_min_usd"`
		InformerMinUSD          float64 `json:"informer_min_usd"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal public-config response: %v", err)
	}

	want := struct {
		ClientFeeUSDCents       int
		ClientPublicMinUSD      float64
		ClientHardFloorUSD      float64
		HelperFeeUSDCents       int
		HelperPreInvoiceMinUSD  float64
		HelperPostPaymentMinUSD float64
		InformerMinUSD          float64
	}{500, 150.0, 120.0, 1000, 1010.0, 1000.0, 1000.0}

	if got.ClientFeeUSDCents != want.ClientFeeUSDCents {
		t.Errorf("client_fee_usd_cents = %d, want %d ($5 fee must not change)", got.ClientFeeUSDCents, want.ClientFeeUSDCents)
	}
	if got.ClientPublicMinUSD != want.ClientPublicMinUSD {
		t.Errorf("client_public_min_usd = %v, want %v", got.ClientPublicMinUSD, want.ClientPublicMinUSD)
	}
	if got.ClientHardFloorUSD != want.ClientHardFloorUSD {
		t.Errorf("client_hard_floor_usd = %v, want %v", got.ClientHardFloorUSD, want.ClientHardFloorUSD)
	}
	if got.HelperFeeUSDCents != want.HelperFeeUSDCents {
		t.Errorf("helper_fee_usd_cents = %d, want %d ($10 fee must not change)", got.HelperFeeUSDCents, want.HelperFeeUSDCents)
	}
	if got.HelperPreInvoiceMinUSD != want.HelperPreInvoiceMinUSD {
		t.Errorf("helper_pre_invoice_min_usd = %v, want %v", got.HelperPreInvoiceMinUSD, want.HelperPreInvoiceMinUSD)
	}
	if got.HelperPostPaymentMinUSD != want.HelperPostPaymentMinUSD {
		t.Errorf("helper_post_payment_min_usd = %v, want %v", got.HelperPostPaymentMinUSD, want.HelperPostPaymentMinUSD)
	}
	if got.InformerMinUSD != want.InformerMinUSD {
		t.Errorf("informer_min_usd = %v, want %v", got.InformerMinUSD, want.InformerMinUSD)
	}
}
