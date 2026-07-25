// wire_devhelpers.go — exported Dev* helpers on *V2System for use by cmd/v2testserver only.
//
// These methods access the unexported svc, helperSvc, and destCipher fields which is
// valid because this file is in the same package (package v2).
// NEVER call these methods from production code or cmd/naroom.
package v2

import (
	"fmt"
	"time"
)

// DevConfirmClientPayment simulates payment detection + confirmation for a client flow.
// For use by cmd/v2testserver only — never call from production code.
func (sys *V2System) DevConfirmClientPayment(flowID, walletAddress string) error {
	// Read invoice from DB
	var invoiceID string
	var amountAtomic int64
	err := sys.db.QueryRow(`
		SELECT i.id, i.amount_atomic
		FROM v2_invoices i
		WHERE i.flow_id = ? AND i.status = 'pending'
		ORDER BY i.created_at DESC LIMIT 1`, flowID,
	).Scan(&invoiceID, &amountAtomic)
	if err != nil {
		return fmt.Errorf("DevConfirmClientPayment: find invoice: %w", err)
	}
	now := time.Now()
	fakeTxid := fmt.Sprintf("devtx-%d", now.UnixNano())
	_, err = sys.svc.RecordPaymentDetected(flowID, invoiceID, fakeTxid, []string{walletAddress}, amountAtomic, now)
	if err != nil {
		return fmt.Errorf("DevConfirmClientPayment: detect: %w", err)
	}
	_, err = sys.svc.ConfirmPayment(flowID, invoiceID, now)
	if err != nil {
		return fmt.Errorf("DevConfirmClientPayment: confirm: %w", err)
	}
	_, err = sys.svc.RecordPostPaymentBalance(flowID, 200.0, 120.0)
	if err != nil {
		return fmt.Errorf("DevConfirmClientPayment: balance: %w", err)
	}
	return nil
}

// DevConnectTelegramByManagementCode simulates Telegram binding for a client flow.
// managementCode is the raw_code from the payment intent response.
func (sys *V2System) DevConnectTelegramByManagementCode(managementCode, walletAddress string) error {
	fv, err := sys.svc.RestorePaymentIntent(managementCode, walletAddress)
	if err != nil {
		return fmt.Errorf("DevConnectTelegramByManagementCode: restore: %w", err)
	}
	const devFakeChatID int64 = 123456789
	return DevInsertReadyBinding(sys.db, sys.destCipher, fv.FlowID, devFakeChatID, time.Now())
}

// DevDisableRateLimits replaces all per-IP rate limiters with near-unlimited ones
// (1_000_000 requests per minute). For use by cmd/v2testserver only — never call
// from production code.
func (sys *V2System) DevDisableRateLimits() {
	const bigLimit = 1_000_000
	const bigEntries = 1_000_000
	noLim := func() *fixedWindowLimiter {
		return newFixedWindowLimiter(bigLimit, time.Minute, bigEntries, time.Now)
	}
	// ClientHandler (payment intent create / restore / recheck).
	sys.ClientHandler.createLim = noLim()
	sys.ClientHandler.restoreLim = noLim()
	sys.ClientHandler.recheckLim = noLim()
	// JourneyHandler (board / listing detail / publish / reactivate / restore).
	sys.JourneyHandler.restoreLim = noLim()
	sys.JourneyHandler.publishLim = noLim()
	sys.JourneyHandler.reactivateLim = noLim()
	sys.JourneyHandler.boardLim = noLim()
	sys.JourneyHandler.detailLim = noLim()
	// InformerHandler.
	sys.InformerHandler.lim = noLim()
	// TelegramLinkHandler (link create / status poll).
	sys.TelegramLink.linkLim = noLim()
	sys.TelegramLink.statusLim = noLim()
	// HelperHandler (purchase create / restore / recheck / reveal).
	sys.HelperHandler.createLim = noLim()
	sys.HelperHandler.restoreLim = noLim()
	sys.HelperHandler.recheckLim = noLim()
	sys.HelperHandler.revealLim = noLim()
	// HelperReviewHandler.
	sys.ReviewHandler.capabilityLim = noLim()
	sys.ReviewHandler.reviewLim = noLim()
}

// DevConfirmHelperPayment simulates helper payment detection + confirmation.
func (sys *V2System) DevConfirmHelperPayment(purchaseID, walletAddress string) error {
	var amountAtomic int64
	err := sys.db.QueryRow(`
		SELECT i.amount_atomic
		FROM v2_helper_invoices i
		WHERE i.purchase_id = ? AND i.status = 'pending'`, purchaseID,
	).Scan(&amountAtomic)
	if err != nil {
		return fmt.Errorf("DevConfirmHelperPayment: find invoice: %w", err)
	}
	now := time.Now()
	fakeTxid := fmt.Sprintf("devtx-helper-%d", now.UnixNano())
	_, err = sys.helperSvc.RecordHelperDetection(purchaseID, fakeTxid, []string{walletAddress}, amountAtomic, now)
	if err != nil {
		return fmt.Errorf("DevConfirmHelperPayment: detect: %w", err)
	}
	_, err = sys.helperSvc.ConfirmHelperPayment(purchaseID, now)
	if err != nil {
		return fmt.Errorf("DevConfirmHelperPayment: confirm: %w", err)
	}
	_, err = sys.helperSvc.RecordHelperPostPaymentBalance(purchaseID, 1100.0)
	if err != nil {
		return fmt.Errorf("DevConfirmHelperPayment: balance: %w", err)
	}
	return nil
}
