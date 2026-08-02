// cmd/naroom-v2-dev — isolated local V2 dev runner.
//
// NOT wired into cmd/naroom/main.go or any production router.
// Provides:
//   - V2 backend on a free dynamic port (default 0 → assigned by OS)
//   - Stub payment/Telegram/balance adapters (no real blockchain)
//   - Dev-only control endpoints under /dev/*
//
// Start with:
//
//	go run ./cmd/naroom-v2-dev
//
// Then in another terminal:
//
//	cd frontend && BACKEND_URL_V2=http://localhost:<port> npm run dev
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	v2 "naroom/internal/v2"

	_ "modernc.org/sqlite"
)

// ── Fixed dev keys (non-secret; dev only) ────────────────────────────────────

var (
	devContactKey    = bytes.Repeat([]byte{0x11}, 32)
	devDestKey       = bytes.Repeat([]byte{0x22}, 32)
	devHMACKey       = bytes.Repeat([]byte{0x33}, 32)
	devTokenSecret   = []byte("dev-token-secret-32-bytes-12345!!")
	devWebhookSecret = []byte("dev-webhook-secret-v200000000012")
	devRateLimitKey  = []byte("dev-rate-limit-key-v200000000012")
)

const devBotUsername = "naroom_devtestbot" // must end in "bot", 5-32 chars

// devFakeChatID is the fake Telegram chat ID used for all dev Telegram bindings.
const devFakeChatID int64 = 123456789

// devPolicy is the balance policy used by all dev/test services.
// Smaller thresholds than production: pre-invoice = $60, post-payment = $50, fee = $10.
var devPolicy = v2.V2BalancePolicy{
	ClientPublicMinUSD:      10.0,
	ClientHardFloorUSD:      8.0,
	HelperPostPaymentMinUSD: 50.0,
	InformerMinUSD:          50.0,
}

// ── Informer dev keys (separate from client bot) ─────────────────────────────

var (
	devInformerDestKey       = bytes.Repeat([]byte{0x44}, 32)
	devInformerWebhookSecret = []byte("dev-informer-webhook-secret-v200")
	devInformerTokenSecret   = []byte("dev-informer-token-secret-v20000")
)

const devInformerBotUsername = "naroom_informer_devbot"

// devInformerFakeChatID is separate from devFakeChatID to prove different bots are independent.
const devInformerFakeChatID int64 = 987654321

// ── Stub: dev InformerBotSender ───────────────────────────────────────────────

type devInformerMessage struct {
	ChatID     int64  `json:"chat_id"`
	Text       string `json:"text"`
	ButtonText string `json:"button_text"`
	ButtonURL  string `json:"button_url"`
}

type devInformerSender struct {
	mu   sync.Mutex
	msgs []devInformerMessage
}

func (s *devInformerSender) SendInformerNotification(_ context.Context, chatID int64, n v2.InformerNotification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, devInformerMessage{ChatID: chatID, Text: n.Text, ButtonText: n.ButtonText, ButtonURL: n.ButtonURL})
	log.Printf("[dev-informer] notification to chat %d: %q button=%q", chatID, truncate(n.Text, 80), n.ButtonURL)
	return nil
}

func (s *devInformerSender) drain() []devInformerMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.msgs
	s.msgs = nil
	return out
}

// ── Stub: dev invoice issuer (client $5) ─────────────────────────────────────

type devClientIssuer struct {
	mu      sync.Mutex
	counter int
}

func (s *devClientIssuer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counter
}

func (s *devClientIssuer) CreateClientInvoice(_ context.Context, currency string) (v2.InvoiceDraft, error) {
	s.mu.Lock()
	s.counter++
	_ = s.counter
	s.mu.Unlock()

	// Use non-empty fake addresses — domain only requires non-empty + AmountAtomic>0.
	// The dev runner bypasses real blockchain; these are display-only.
	var addr string
	var amountAtomic int64
	switch currency {
	case "LTC":
		addr = "ltc1qdevplatform00clientinvoice00devonly"
		amountAtomic = 300000 // ~$5 at $16/LTC (display only)
	default: // BTC
		addr = "bc1qdevplatform00clientinvoice00devonly00"
		amountAtomic = 10000 // ~$5 at $50k/BTC (display only)
	}
	return v2.InvoiceDraft{
		PaymentAddress: addr,
		AmountAtomic:   amountAtomic,
		AmountUSDCents: 500, // $5 exactly
	}, nil
}

// ── Stub: dev invoice issuer (helper $10) ────────────────────────────────────

type devHelperIssuer struct {
	mu      sync.Mutex
	counter int
}

func (s *devHelperIssuer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counter
}

func (s *devHelperIssuer) CreateHelperInvoice(_ context.Context, currency string) (v2.HelperInvoiceDraft, error) {
	s.mu.Lock()
	s.counter++
	_ = s.counter
	s.mu.Unlock()

	var addr string
	var amountAtomic int64
	switch currency {
	case "LTC":
		addr = "ltc1qdevplatform00helperinvoice00devonly"
		amountAtomic = 600000
	default: // BTC
		addr = "bc1qdevplatform00helperinvoice00devonly00"
		amountAtomic = 20000
	}
	return v2.HelperInvoiceDraft{
		PaymentAddress: addr,
		AmountAtomic:   amountAtomic,
		AmountUSDCents: 1000, // $10 exactly
	}, nil
}

// ── Stub: dev balance reader ──────────────────────────────────────────────────

type devBalanceReader struct {
	mu       sync.Mutex
	balances map[string]float64 // wallet_address → USD balance
	outage   bool               // when true, BalanceUSD returns an error
	calls    int
}

func newDevBalanceReader() *devBalanceReader {
	return &devBalanceReader{balances: make(map[string]float64)}
}

func (r *devBalanceReader) BalanceUSD(_ context.Context, walletAddress, _ string) (float64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.outage {
		return 0, fmt.Errorf("dev: balance service outage simulated")
	}
	if b, ok := r.balances[walletAddress]; ok {
		return b, nil
	}
	return 200.0, nil // default: $200 (above client $120 floor and helper $60 dev floor)
}

func (r *devBalanceReader) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *devBalanceReader) set(walletAddress string, balanceUSD float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.balances[walletAddress] = balanceUSD
}

func (r *devBalanceReader) setOutage(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outage = enabled
}

// ── Stub: dev BotAPISender ────────────────────────────────────────────────────

type devBotSender struct {
	mu       sync.Mutex
	messages []devBotMessage
}

type devBotMessage struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

func (s *devBotSender) SendMessage(_ context.Context, chatID int64, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, devBotMessage{ChatID: chatID, Text: text})
	log.Printf("[dev-telegram] sent message to chat %d: %q", chatID, truncate(text, 80))
	return nil
}

// ── Stub: dev ReviewNotificationSender ───────────────────────────────────────

type devNotification struct {
	ChatID  int64  `json:"chat_id"`
	Text    string `json:"text"`
	PosData string `json:"pos_data"`
	NegData string `json:"neg_data"`
}

type devReviewSender struct {
	mu            sync.Mutex
	notifications []devNotification
}

func (s *devReviewSender) SendReviewPrompt(_ context.Context, chatID int64, text, posData, negData string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := devNotification{ChatID: chatID, Text: text, PosData: posData, NegData: negData}
	s.notifications = append(s.notifications, n)
	log.Printf("[dev-telegram] review prompt to chat %d: pos=%q neg=%q", chatID, posData, negData)
	return nil
}

func (s *devReviewSender) SendPlainMessage(_ context.Context, chatID int64, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifications = append(s.notifications, devNotification{ChatID: chatID, Text: text})
	log.Printf("[dev-telegram] immediate notice to chat %d", chatID)
	return nil
}
func (s *devReviewSender) AnswerCallback(_ context.Context, _, _ string) error { return nil }
func (s *devReviewSender) EditMessage(_ context.Context, _, _ int64, _ string) error {
	return nil
}

func (s *devReviewSender) drain() []devNotification {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.notifications
	s.notifications = nil
	return out
}

func (s *devReviewSender) list() []devNotification {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]devNotification, len(s.notifications))
	copy(out, s.notifications)
	return out
}

// ── DB helper: open file-based SQLite ────────────────────────────────────────

func openFileDB(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=ON", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	// The _foreign_keys DSN parameter is not reliably honored by all versions
	// of modernc.org/sqlite (same caveat as in internal/v2/db.go). Apply it
	// explicitly so CASCADE deletes work correctly with the file-based DB.
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	if err := v2.ApplySchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := v2.MigrateSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	return db, nil
}

// ── Dev services ──────────────────────────────────────────────────────────────

type devServer struct {
	db           *sql.DB
	svc          *v2.Service
	ls           *v2.ListingService
	helperSvc    *v2.HelperPurchaseService
	reviewSvc    *v2.ReviewService
	transport    *v2.TelegramTransport
	destCipher   *v2.DestinationCipher
	balance      *devBalanceReader
	clientIssuer *devClientIssuer
	helperIssuer *devHelperIssuer
	reviewSender *devReviewSender
	botSender    *devBotSender

	// Informer subsystem
	informerSvc       *v2.InformerService
	informerTransport *v2.InformerTransport
	informerWorker    *v2.InformerWorker
	informerH         *v2.InformerHandler
	informerSender    *devInformerSender

	// Handlers
	clientH  *v2.ClientHandler
	journeyH *v2.ClientJourneyHandler
	tgH      *v2.TelegramLinkHandler
	helperH  *v2.HelperPurchaseHandler
	reviewH  *v2.HelperReviewHandler
	cityH    *v2.CitySummaryHandler
}

func buildDevServer(db *sql.DB) (*devServer, error) {
	ds := &devServer{
		db:           db,
		balance:      newDevBalanceReader(),
		clientIssuer: &devClientIssuer{},
		helperIssuer: &devHelperIssuer{},
		reviewSender: &devReviewSender{},
		botSender:    &devBotSender{},
	}

	// Core service
	svc, err := v2.New(db, devHMACKey)
	if err != nil {
		return nil, fmt.Errorf("NewService: %w", err)
	}
	ds.svc = svc

	// Contact cipher
	contactCipher, err := v2.NewAESGCMContactCipher(devContactKey, "dev_v1")
	if err != nil {
		return nil, fmt.Errorf("NewAESGCMContactCipher: %w", err)
	}

	// Destination cipher (for Telegram chat IDs)
	destCipher, err := v2.NewDestinationCipher(devDestKey, "dest_dev_v1")
	if err != nil {
		return nil, fmt.Errorf("NewDestinationCipher: %w", err)
	}
	ds.destCipher = destCipher

	// Display name generator
	names := v2.NewRandomDisplayNameGenerator()

	// Alias generator (for helper profiles)
	aliases := v2.NewRandomAliasGenerator()

	// Contact validator
	cv := v2.NewProductionContactValidator()

	// Listing service
	ls, err := v2.NewListingService(svc, contactCipher, names, cv)
	if err != nil {
		return nil, fmt.Errorf("NewListingService: %w", err)
	}
	ds.ls = ls

	// Telegram transport (with stub sender)
	transport, err := v2.NewTelegramTransport(
		svc, ls,
		devTokenSecret, devWebhookSecret,
		destCipher,
		devBotUsername,
		ds.botSender,
		time.Now,
	)
	if err != nil {
		return nil, fmt.Errorf("NewTelegramTransport: %w", err)
	}
	ds.transport = transport

	// Helper purchase service
	helperSvc, err := v2.NewHelperPurchaseService(db, devHMACKey, contactCipher, aliases, time.Now)
	if err != nil {
		return nil, fmt.Errorf("NewHelperPurchaseService: %w", err)
	}
	// Apply dev policy (smaller thresholds for testing)
	helperSvc.SetPolicy(devPolicy)
	ds.helperSvc = helperSvc

	// Review service
	reviewSvc, err := v2.NewReviewService(db, devHMACKey, time.Now)
	if err != nil {
		return nil, fmt.Errorf("NewReviewService: %w", err)
	}
	ds.reviewSvc = reviewSvc

	// Wire review service into transport
	transport.SetReviewService(reviewSvc, ds.reviewSender)

	// ── Informer subsystem ────────────────────────────────────────────────────

	ds.informerSender = &devInformerSender{}

	informerDestCipher, err := v2.NewDestinationCipher(devInformerDestKey, "informer_dest_dev_v1")
	if err != nil {
		return nil, fmt.Errorf("NewDestinationCipher (informer): %w", err)
	}

	informerSvc, err := v2.NewInformerService(db, devHMACKey, devInformerTokenSecret, informerDestCipher, time.Now)
	if err != nil {
		return nil, fmt.Errorf("NewInformerService: %w", err)
	}
	// DEV_PUBLIC_BASE_URL lets the E2E harness point the Informer "Open
	// listing" button at the actual dynamic frontend port for this run;
	// falls back to the conventional local `npm run dev` port otherwise.
	devPublicBaseURL := os.Getenv("DEV_PUBLIC_BASE_URL")
	if devPublicBaseURL == "" {
		devPublicBaseURL = "http://localhost:5173"
	}
	informerSvc.SetPublicBaseURL(devPublicBaseURL)
	ds.informerSvc = informerSvc

	informerTransport, err := v2.NewInformerTransport(informerSvc, devInformerWebhookSecret, devInformerBotUsername, time.Now)
	if err != nil {
		return nil, fmt.Errorf("NewInformerTransport: %w", err)
	}
	ds.informerTransport = informerTransport

	ds.informerWorker = v2.NewInformerWorker(informerSvc, ds.informerSender)

	informerH, err := v2.NewInformerHandler(informerSvc, ds.balance, devInformerBotUsername, devRateLimitKey, time.Now)
	if err != nil {
		return nil, fmt.Errorf("NewInformerHandler: %w", err)
	}
	ds.informerH = informerH

	// Wire informer into listing service (first-publish hook).
	ls.SetInformerNotifier(informerSvc)

	// HTTP handlers
	clientH, err := v2.NewClientHandler(svc, ds.clientIssuer, ds.balance, devRateLimitKey, time.Now)
	if err != nil {
		return nil, fmt.Errorf("NewClientHandler: %w", err)
	}
	ds.clientH = clientH

	journeyH, err := v2.NewClientJourneyHandler(svc, ls, transport, ds.balance, devRateLimitKey, time.Now)
	if err != nil {
		return nil, fmt.Errorf("NewClientJourneyHandler: %w", err)
	}
	ds.journeyH = journeyH

	tgH, err := v2.NewTelegramLinkHandler(svc, transport, devRateLimitKey, time.Now)
	if err != nil {
		return nil, fmt.Errorf("NewTelegramLinkHandler: %w", err)
	}
	ds.tgH = tgH

	helperH, err := v2.NewHelperPurchaseHandler(helperSvc, ds.helperIssuer, ds.balance, devRateLimitKey, time.Now)
	if err != nil {
		return nil, fmt.Errorf("NewHelperPurchaseHandler: %w", err)
	}
	ds.helperH = helperH

	reviewH, err := v2.NewHelperReviewHandler(reviewSvc, devRateLimitKey, time.Now)
	if err != nil {
		return nil, fmt.Errorf("NewHelperReviewHandler: %w", err)
	}
	ds.reviewH = reviewH
	ds.cityH = v2.NewCitySummaryHandler(db, time.Now)

	return ds, nil
}

// ── Dev control endpoints ─────────────────────────────────────────────────────

// devJSON writes a JSON response.
func devJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func devErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	http.Error(w, msg, status)
}

// devCORS handles preflight and sets CORS headers.
func devCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// readFlowRaw reads flow_id, invoice_id, amount_atomic, currency from the DB.
func (ds *devServer) readFlowRaw(flowID string) (invoiceID string, amountAtomic int64, currency string, err error) {
	err = ds.db.QueryRow(`
		SELECT i.id, i.amount_atomic, f.currency
		FROM v2_invoices i
		JOIN v2_client_flows f ON f.id = i.flow_id
		WHERE i.flow_id = ? AND i.status = 'pending'
		ORDER BY i.created_at DESC LIMIT 1`, flowID,
	).Scan(&invoiceID, &amountAtomic, &currency)
	return
}

// readPurchaseRaw reads purchase invoice data from the DB.
func (ds *devServer) readPurchaseRaw(purchaseID string) (invoiceID string, amountAtomic int64, currency string, err error) {
	err = ds.db.QueryRow(`
		SELECT i.id, i.amount_atomic, hp.currency
		FROM v2_helper_invoices i
		JOIN v2_helper_purchases p ON p.id = i.purchase_id
		JOIN v2_helper_profiles hp ON hp.id = p.helper_profile_id
		WHERE p.id = ? AND i.status = 'pending'`, purchaseID,
	).Scan(&invoiceID, &amountAtomic, &currency)
	return
}

// devHandleConfirmPayment handles POST /dev/payment/confirm
// Body: {"flow_id":"...", "wallet_address":"..."}
func (ds *devServer) devHandleConfirmPayment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FlowID        string `json:"flow_id"`
		WalletAddress string `json:"wallet_address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		devErr(w, 400, "invalid JSON")
		return
	}
	if req.FlowID == "" || req.WalletAddress == "" {
		devErr(w, 400, "flow_id and wallet_address required")
		return
	}

	invoiceID, amountAtomic, _, err := ds.readFlowRaw(req.FlowID)
	if err != nil {
		devErr(w, 404, fmt.Sprintf("flow not found or already detected: %v", err))
		return
	}

	now := time.Now()
	fakeTxid := fmt.Sprintf("devtx-%d", now.UnixNano())

	// Record detection (sender = client's own wallet)
	_, err = ds.svc.RecordPaymentDetected(
		req.FlowID, invoiceID, fakeTxid,
		[]string{req.WalletAddress}, amountAtomic, now,
	)
	if err != nil {
		devErr(w, 500, fmt.Sprintf("RecordPaymentDetected: %v", err))
		return
	}

	// Confirm immediately
	fv, err := ds.svc.ConfirmPayment(req.FlowID, invoiceID, now)
	if err != nil {
		devErr(w, 500, fmt.Sprintf("ConfirmPayment: %v", err))
		return
	}

	// Record balance: $200 (above client $120 floor)
	fv, err = ds.svc.RecordPostPaymentBalance(req.FlowID, 200.0, 120.0)
	if err != nil && !errors.Is(err, v2.ErrInvalidState) {
		devErr(w, 500, fmt.Sprintf("RecordPostPaymentBalance: %v", err))
		return
	}

	devJSON(w, map[string]any{
		"ok":    true,
		"state": fv.State,
		"msg":   "Client payment confirmed. State is now: " + fv.State,
	})
}

// devHandleConnectTelegram handles POST /dev/telegram/connect
// Body: {"management_code":"...", "wallet_address":"..."}
// Directly inserts a ready binding into the DB (simulates Telegram bot callback).
func (ds *devServer) devHandleConnectTelegram(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ManagementCode string `json:"management_code"`
		WalletAddress  string `json:"wallet_address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		devErr(w, 400, "invalid JSON")
		return
	}
	if req.ManagementCode == "" || req.WalletAddress == "" {
		devErr(w, 400, "management_code and wallet_address required")
		return
	}

	// Restore to get flow_id and verify capability
	fv, err := ds.svc.RestorePaymentIntent(req.ManagementCode, req.WalletAddress)
	if err != nil {
		devErr(w, 404, fmt.Sprintf("payment intent not found: %v", err))
		return
	}

	// Create binding directly in the DB using the exported dev helper
	if err := v2.DevInsertReadyBinding(ds.db, ds.destCipher, fv.FlowID, devFakeChatID, time.Now()); err != nil {
		devErr(w, 500, fmt.Sprintf("DevInsertReadyBinding: %v", err))
		return
	}

	devJSON(w, map[string]any{
		"ok":  true,
		"msg": "Telegram binding created (state=ready). You can now publish the listing.",
	})
}

// devHandleConfirmHelperPayment handles POST /dev/helper/payment/confirm
// Body: {"purchase_id":"...", "wallet_address":"..."}
func (ds *devServer) devHandleConfirmHelperPayment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PurchaseID    string `json:"purchase_id"`
		WalletAddress string `json:"wallet_address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		devErr(w, 400, "invalid JSON")
		return
	}
	if req.PurchaseID == "" || req.WalletAddress == "" {
		devErr(w, 400, "purchase_id and wallet_address required")
		return
	}

	_, amountAtomic, _, err := ds.readPurchaseRaw(req.PurchaseID)
	if err != nil {
		devErr(w, 404, fmt.Sprintf("purchase not found or already detected: %v", err))
		return
	}

	now := time.Now()
	fakeTxid := fmt.Sprintf("devtx-helper-%d", now.UnixNano())

	// Record detection
	_, err = ds.helperSvc.RecordHelperDetection(
		req.PurchaseID, fakeTxid,
		[]string{req.WalletAddress}, amountAtomic, now,
	)
	if err != nil {
		devErr(w, 500, fmt.Sprintf("RecordHelperDetection: %v", err))
		return
	}

	// Confirm immediately
	_, err = ds.helperSvc.ConfirmHelperPayment(req.PurchaseID, now)
	if err != nil {
		devErr(w, 500, fmt.Sprintf("ConfirmHelperPayment: %v", err))
		return
	}

	// Record balance: $1100 (above $1000 helper floor)
	pv, err := ds.helperSvc.RecordHelperPostPaymentBalance(req.PurchaseID, 1100.0)
	if err != nil {
		devErr(w, 500, fmt.Sprintf("RecordHelperPostPaymentBalance: %v", err))
		return
	}

	// Flush the immediate Client notice (no review buttons) now that contact_ready
	// has been reached. In production this is done by LifecycleWorker.ReviewDeliveryOnce
	// on a 30s tick; the dev binary has no background worker, so it must be triggered
	// explicitly at every point where new deliverable state may exist.
	if notifyErr := ds.transport.SendPendingImmediateNotices(); notifyErr != nil {
		log.Printf("[dev] SendPendingImmediateNotices: %v", notifyErr)
	}
	// Also flush the delayed review-prompt queue: harmless no-op here (the
	// entitlement doesn't exist until first reveal), but keeps this call site
	// consistent with production's combined delivery tick.
	if notifyErr := ds.transport.SendPendingReviewNotifications(); notifyErr != nil {
		log.Printf("[dev] SendPendingReviewNotifications: %v", notifyErr)
	}

	devJSON(w, map[string]any{
		"ok":    true,
		"phase": pv.State,
		"msg":   "Helper payment confirmed. Phase: " + pv.State,
	})
}

// devHandleSetBalance handles POST /dev/balance/set
// Body: {"wallet_address":"...", "balance_usd":200.0}
func (ds *devServer) devHandleSetBalance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WalletAddress string  `json:"wallet_address"`
		BalanceUSD    float64 `json:"balance_usd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		devErr(w, 400, "invalid JSON")
		return
	}
	if req.WalletAddress == "" {
		devErr(w, 400, "wallet_address required")
		return
	}
	ds.balance.set(req.WalletAddress, req.BalanceUSD)
	devJSON(w, map[string]any{
		"ok":  true,
		"msg": fmt.Sprintf("Balance for %s set to $%.2f", req.WalletAddress, req.BalanceUSD),
	})
}

// devHandleBalanceOutage handles POST /dev/balance/outage
// Body: {"enabled": true/false} — enables/disables balance service outage simulation.
func (ds *devServer) devHandleBalanceOutage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		devErr(w, 400, "invalid JSON")
		return
	}
	ds.balance.setOutage(req.Enabled)
	devJSON(w, map[string]any{
		"ok":     true,
		"outage": req.Enabled,
		"msg":    fmt.Sprintf("Balance outage simulation: %v", req.Enabled),
	})
}

// devHandleNotifications handles GET /dev/notifications
// Returns captured review notification prompts.
func (ds *devServer) devHandleNotifications(w http.ResponseWriter, r *http.Request) {
	notifications := ds.reviewSender.list()
	devJSON(w, map[string]any{
		"notifications": notifications,
		"count":         len(notifications),
	})
}

// devHandleExpireListing handles POST /dev/listing/expire
// Body: {"listing_id": "..."} — forces listing's visible_until to now-1s
func (ds *devServer) devHandleExpireListing(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ListingID string `json:"listing_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		devErr(w, 400, "invalid JSON")
		return
	}
	if req.ListingID == "" {
		devErr(w, 400, "listing_id required")
		return
	}

	// Transition to 'hidden' state with visible_until = NULL.
	// Schema CHECKs:
	//   1. state!='visible' OR (last_activated_at < visible_until AND ...) — satisfied when state='hidden'
	//   2. (state='visible' AND visible_until IS NOT NULL) OR (state='hidden' AND visible_until IS NULL) OR ...
	//      — satisfied when state='hidden' AND visible_until IS NULL
	now := time.Now().Unix()
	res, err := ds.db.Exec(`
		UPDATE v2_listings
		SET state = 'hidden',
		    visible_until = NULL,
		    updated_at = ?
		WHERE id = ? AND state = 'visible'`,
		now, req.ListingID)
	if err != nil {
		devErr(w, 500, fmt.Sprintf("update: %v", err))
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		devErr(w, 404, "listing not found")
		return
	}

	// Delete bindings so next activation requires fresh Telegram connect
	// (simulates natural 15-min binding TTL expiry that occurs in production)
	_, _ = ds.db.Exec(`
		DELETE FROM v2_client_notification_bindings
		WHERE flow_id = (SELECT flow_id FROM v2_listings WHERE id = ?)`,
		req.ListingID)

	devJSON(w, map[string]any{
		"ok":  true,
		"msg": "Listing visibility window expired (state=hidden).",
	})
}

// devHandleSimulateReviewCallback handles POST /dev/telegram/review-callback
// Body: {"callback_data":"..."} — simulates Client clicking Telegram inline keyboard button.
// Routes through TelegramTransport.HandleWebhook → handleCallbackQuery (real path).
func (ds *devServer) devHandleSimulateReviewCallback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CallbackData string `json:"callback_data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		devErr(w, 400, "invalid JSON")
		return
	}
	if req.CallbackData == "" {
		devErr(w, 400, "callback_data required")
		return
	}

	// Build a Telegram callback_query update. Routes through the real transport path.
	// All shape fields are required by handleCallbackQuery:
	//   cq.ID non-empty, cq.Message non-nil, cq.Message.Chat.Type=="private",
	//   cq.Message.Chat.ID>0, cq.Message.MessageID>0.
	update := map[string]any{
		"update_id": 999999,
		"callback_query": map[string]any{
			"id":   "dev-callback-id-1",
			"data": req.CallbackData,
			"message": map[string]any{
				"message_id": int64(1),
				"chat": map[string]any{
					"id":   devFakeChatID,
					"type": "private",
				},
			},
		},
	}
	body, err := json.Marshal(update)
	if err != nil {
		devErr(w, 500, "marshal update: "+err.Error())
		return
	}

	webhookReq, err := http.NewRequest("POST", "/v2/telegram/client/webhook", bytes.NewReader(body))
	if err != nil {
		devErr(w, 500, "build webhook request: "+err.Error())
		return
	}
	webhookReq.Header.Set("Content-Type", "application/json")
	// Header value must equal devWebhookSecret so SHA-256(header)==SHA-256(secret).
	webhookReq.Header.Set("X-Telegram-Bot-Api-Secret-Token", string(devWebhookSecret))

	rr := httptest.NewRecorder()
	ds.transport.HandleWebhook(rr, webhookReq)

	// HandleWebhook returns 200 for neutral outcomes (already consumed, unknown ref)
	// as well as success. Only non-200 (503) indicates a transient backend error.
	if rr.Code != http.StatusOK {
		devErr(w, 422, fmt.Sprintf("transport webhook returned %d — transient error", rr.Code))
		return
	}
	devJSON(w, map[string]any{"ok": true, "msg": "Review callback routed through transport webhook"})
}

// devHandleSimulateStart handles POST /dev/telegram/simulate-start
// Body: {"raw_token":"..."} — raw token from bot_url ?start= param.
// Simulates Telegram sending /start <rawToken> message to the webhook.
// Must be called AFTER the browser has clicked "Connect Telegram" (creating a pending attempt).
func (ds *devServer) devHandleSimulateStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RawToken string `json:"raw_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		devErr(w, 400, "invalid JSON")
		return
	}
	if req.RawToken == "" {
		devErr(w, 400, "raw_token required")
		return
	}

	// Build a Telegram /start message webhook update.
	update := map[string]any{
		"update_id": 888888,
		"message": map[string]any{
			"message_id": int64(1),
			"chat": map[string]any{
				"id":   devFakeChatID,
				"type": "private",
			},
			"text": "/start " + req.RawToken,
		},
	}
	body, err := json.Marshal(update)
	if err != nil {
		devErr(w, 500, "marshal update: "+err.Error())
		return
	}

	webhookReq, err := http.NewRequest("POST", "/v2/telegram/client/webhook", bytes.NewReader(body))
	if err != nil {
		devErr(w, 500, "build webhook request: "+err.Error())
		return
	}
	webhookReq.Header.Set("Content-Type", "application/json")
	webhookReq.Header.Set("X-Telegram-Bot-Api-Secret-Token", string(devWebhookSecret))

	rr := httptest.NewRecorder()
	ds.transport.HandleWebhook(rr, webhookReq)

	if rr.Code != http.StatusOK {
		devErr(w, 422, fmt.Sprintf("transport webhook returned %d — token may be expired, already consumed, or unknown", rr.Code))
		return
	}
	devJSON(w, map[string]any{"ok": true, "msg": "Telegram /start simulated via transport webhook"})
}

// devHandleExpireHelperInvoice handles POST /dev/helper/invoice/expire
// Body: {"purchase_id":"..."} — forces invoice to 'expired' by passing a far-future time.
func (ds *devServer) devHandleExpireHelperInvoice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PurchaseID string `json:"purchase_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		devErr(w, 400, "invalid JSON")
		return
	}
	if req.PurchaseID == "" {
		devErr(w, 400, "purchase_id required")
		return
	}

	// Use a time 48 hours in the future so the detection_deadline_at has certainly passed.
	futureTime := time.Now().Add(48 * time.Hour)
	view, err := ds.helperSvc.ExpireHelperInvoice(req.PurchaseID, futureTime)
	if err != nil {
		devErr(w, 500, fmt.Sprintf("ExpireHelperInvoice: %v", err))
		return
	}

	devJSON(w, map[string]any{
		"ok":    true,
		"phase": view.State,
		"msg":   "Helper invoice expired. State: " + view.State,
	})
}

// devHandleExpireHelperHandoff handles POST /dev/helper/handoff/expire
// Body: {"purchase_id":"..."} — expires pending handoffs for one purchase.
// This is a dev/E2E-only clock boundary control and is never wired in production.
func (ds *devServer) devHandleExpireHelperHandoff(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PurchaseID string `json:"purchase_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		devErr(w, 400, "invalid JSON")
		return
	}
	if req.PurchaseID == "" {
		devErr(w, 400, "purchase_id required")
		return
	}

	res, err := ds.db.Exec(`
		UPDATE v2_helper_purchase_handoffs
		SET state = 'expired',
		    expires_at = created_at + 1,
		    updated_at = ?
		WHERE purchase_id = ? AND state = 'pending'`,
		time.Now().Unix(), req.PurchaseID,
	)
	if err != nil {
		devErr(w, 500, fmt.Sprintf("expire helper handoff: %v", err))
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		devErr(w, 500, fmt.Sprintf("handoff rows affected: %v", err))
		return
	}
	if n == 0 {
		devErr(w, 404, "pending helper handoff not found")
		return
	}

	devJSON(w, map[string]any{"ok": true, "rows": n})
}

// devHandleHelperReputation handles GET /dev/helper/reputation?purchase_id=...
// Returns helper aggregate counts for a purchase. No wallet, token, ciphertext, or chat_id exposed.
func (ds *devServer) devHandleHelperReputation(w http.ResponseWriter, r *http.Request) {
	purchaseID := r.URL.Query().Get("purchase_id")
	if purchaseID == "" {
		devErr(w, 400, "purchase_id query param required")
		return
	}

	var positiveCount, negativeCount int
	err := ds.db.QueryRow(`
		SELECT hp.positive_count, hp.negative_count
		FROM v2_helper_purchases p
		JOIN v2_helper_profiles hp ON hp.id = p.helper_profile_id
		WHERE p.id = ?`, purchaseID,
	).Scan(&positiveCount, &negativeCount)
	if err != nil {
		devErr(w, 404, "purchase not found")
		return
	}

	devJSON(w, map[string]any{
		"positive_count": positiveCount,
		"negative_count": negativeCount,
	})
}

// devHandleReviewUnlock handles POST /dev/helper/review/unlock
// Body: {"purchase_id":"..."} — zeroes out available_at so review buttons appear immediately.
// E2E-only: bypasses the 1-hour review delay enforced by RevealHelperContact.
func (ds *devServer) devHandleReviewUnlock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PurchaseID string `json:"purchase_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		devErr(w, 400, "invalid JSON")
		return
	}
	if req.PurchaseID == "" {
		devErr(w, 400, "purchase_id required")
		return
	}
	res, err := ds.db.Exec(
		`UPDATE v2_review_entitlements SET available_at = 0 WHERE purchase_id = ?`,
		req.PurchaseID,
	)
	if err != nil {
		devErr(w, 500, fmt.Sprintf("update: %v", err))
		return
	}
	n, _ := res.RowsAffected()

	// Re-trigger delivery now that the entitlement is unlocked. In production,
	// LifecycleWorker.ReviewDeliveryOnce runs on a 30s tick and would pick this
	// up automatically; the dev binary has no background worker, so unlocking
	// without this call left /dev/notifications permanently empty for any flow
	// that unlocks after the one-shot send in devHandleConfirmHelperPayment.
	if notifyErr := ds.transport.SendPendingReviewNotifications(); notifyErr != nil {
		log.Printf("[dev] SendPendingReviewNotifications (post-unlock): %v", notifyErr)
	}

	devJSON(w, map[string]any{
		"ok":   true,
		"rows": n,
		"msg":  fmt.Sprintf("review available_at zeroed for purchase %s (%d rows)", req.PurchaseID, n),
	})
}

// devHandleDBCounts handles GET /dev/db/counts
// Returns global row counts for tables E2E tests need to prove "0 new rows"
// domain-side-effect assertions (wallet_already_visible, self-purchase guard).
// This is a dev/E2E-only diagnostic endpoint — never exposed in production.
func (ds *devServer) devHandleDBCounts(w http.ResponseWriter, r *http.Request) {
	counts := map[string]int{}
	tables := []string{
		"v2_client_flows", "v2_invoices",
		"v2_helper_purchases", "v2_helper_invoices",
		"v2_helper_purchase_handoffs",
	}
	for _, tbl := range tables {
		var n int
		if err := ds.db.QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&n); err != nil {
			devErr(w, 500, fmt.Sprintf("count %s: %v", tbl, err))
			return
		}
		counts[tbl] = n
	}
	counts["balance_provider_calls"] = ds.balance.callCount()
	counts["client_invoice_provider_calls"] = ds.clientIssuer.calls()
	counts["helper_invoice_provider_calls"] = ds.helperIssuer.calls()
	devJSON(w, counts)
}

// devHandleInformerSimulateStart handles POST /dev/informer/simulate-start
// Body: {"raw_token":"..."} — simulates Telegram /start <rawToken> to the Informer webhook.
// After subscription, runs the outbox worker to deliver any pending notifications.
func (ds *devServer) devHandleInformerSimulateStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RawToken string `json:"raw_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		devErr(w, 400, "invalid JSON")
		return
	}
	if req.RawToken == "" {
		devErr(w, 400, "raw_token required")
		return
	}

	update := map[string]any{
		"update_id": 777777,
		"message": map[string]any{
			"message_id": int64(1),
			"chat": map[string]any{
				"id":   devInformerFakeChatID,
				"type": "private",
			},
			"text": "/start " + req.RawToken,
		},
	}
	body, err := json.Marshal(update)
	if err != nil {
		devErr(w, 500, "marshal update: "+err.Error())
		return
	}

	webhookReq, err := http.NewRequest("POST", "/v2/telegram/informer/webhook", bytes.NewReader(body))
	if err != nil {
		devErr(w, 500, "build webhook request: "+err.Error())
		return
	}
	webhookReq.Header.Set("Content-Type", "application/json")
	webhookReq.Header.Set("X-Telegram-Bot-Api-Secret-Token", string(devInformerWebhookSecret))

	rr := httptest.NewRecorder()
	ds.informerTransport.HandleWebhook(rr, webhookReq)
	if rr.Code != http.StatusOK {
		devErr(w, 422, fmt.Sprintf("informer transport returned %d — token may be expired or unknown", rr.Code))
		return
	}

	// Run worker to deliver any pending outbox events.
	_ = ds.informerWorker.RunOnce(r.Context())

	devJSON(w, map[string]any{"ok": true, "msg": "Informer /start simulated; worker ran"})
}

// devHandleInformerFakeFirstPublish handles POST /dev/informer/fake-first-publish
// Body: {"listing_id":"...","city":"...","display_name":"..."}
// Creates a fake informer outbox event and immediately runs the worker.
// Useful for E2E tests that need to prove notification delivery without a full listing flow.
func (ds *devServer) devHandleInformerFakeFirstPublish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ListingID   string `json:"listing_id"`
		City        string `json:"city"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		devErr(w, 400, "invalid JSON")
		return
	}
	if req.ListingID == "" || req.City == "" {
		devErr(w, 400, "listing_id and city required")
		return
	}
	if req.DisplayName == "" {
		req.DisplayName = "Dev Listing"
	}

	notifyErr := ds.informerSvc.NotifyFirstPublish(
		req.ListingID, req.City, req.DisplayName,
		"support", "alcohol", "urgent", time.Now(),
	)
	if notifyErr != nil && !errors.Is(notifyErr, v2.ErrInformerDuplicateEvent) {
		devErr(w, 500, fmt.Sprintf("NotifyFirstPublish: %v", notifyErr))
		return
	}

	_ = ds.informerWorker.RunOnce(r.Context())

	devJSON(w, map[string]any{"ok": true, "msg": "Informer fake first-publish event created; worker ran"})
}

// devHandleInformerRunWorker handles POST /dev/informer/run-worker
// Runs the informer outbox worker once and delivers any pending notifications.
// Used by E2E tests after a real first-publish to trigger notification delivery.
func (ds *devServer) devHandleInformerRunWorker(w http.ResponseWriter, r *http.Request) {
	if err := ds.informerWorker.RunOnce(r.Context()); err != nil {
		devErr(w, 500, fmt.Sprintf("RunOnce: %v", err))
		return
	}
	devJSON(w, map[string]any{"ok": true, "msg": "informer worker ran"})
}

// devHandleInformerNotifications handles GET /dev/informer/notifications
// Returns and clears all pending informer notification messages.
func (ds *devServer) devHandleInformerNotifications(w http.ResponseWriter, r *http.Request) {
	msgs := ds.informerSender.drain()
	devJSON(w, map[string]any{"notifications": msgs, "count": len(msgs)})
}

// devHandleStatus handles GET /dev/status
func (ds *devServer) devHandleStatus(w http.ResponseWriter, r *http.Request) {
	devJSON(w, map[string]any{
		"ok":          true,
		"description": "NA Room V2 dev runner — NOT for production use",
		"endpoints": []string{
			"POST /dev/payment/confirm              {flow_id, wallet_address}",
			"POST /dev/telegram/connect             {management_code, wallet_address}",
			"POST /dev/helper/payment/confirm       {purchase_id, wallet_address}",
			"POST /dev/helper/invoice/expire        {purchase_id}",
			"POST /dev/helper/handoff/expire        {purchase_id}",
			"POST /dev/helper/review/unlock         {purchase_id}",
			"POST /dev/balance/set                  {wallet_address, balance_usd}",
			"POST /dev/balance/outage               {enabled}",
			"POST /dev/listing/expire               {listing_id}",
			"POST /dev/telegram/review-callback     {callback_data}",
			"POST /dev/telegram/simulate-start      {raw_token}",
			"GET  /dev/helper/reputation            ?purchase_id=...",
			"GET  /dev/notifications",
			"GET  /dev/db/counts",
			"GET  /dev/status",
		},
		"bot_url": "https://t.me/" + devBotUsername + "?start=dev",
		"chat_id": devFakeChatID,
		"note":    "Use /dev/telegram/simulate-start (routes through real webhook) instead of /dev/telegram/connect for new E2E tests.",
	})
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	// DB path: use temp dir by default, or DEV_DB_PATH env var
	dbPath := os.Getenv("DEV_DB_PATH")
	if dbPath == "" {
		dir := os.TempDir()
		dbPath = filepath.Join(dir, "naroom-v2-dev.db")
	}

	log.Printf("[v2-dev] opening DB at %s", dbPath)
	db, err := openFileDB(dbPath)
	if err != nil {
		log.Fatalf("[v2-dev] DB open failed: %v", err)
	}
	defer db.Close()

	ds, err := buildDevServer(db)
	if err != nil {
		log.Fatalf("[v2-dev] build failed: %v", err)
	}

	// Choose port: DEV_PORT env or dynamic
	port := 0
	if p := os.Getenv("DEV_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}

	mux := http.NewServeMux()
	mountDevV2Routes(mux, ds)

	// Dev control endpoints
	devMux := http.NewServeMux()
	devMux.HandleFunc("GET /dev/status", ds.devHandleStatus)
	devMux.HandleFunc("GET /dev/notifications", ds.devHandleNotifications)
	devMux.HandleFunc("POST /dev/payment/confirm", ds.devHandleConfirmPayment)
	devMux.HandleFunc("POST /dev/telegram/connect", ds.devHandleConnectTelegram)
	devMux.HandleFunc("POST /dev/helper/payment/confirm", ds.devHandleConfirmHelperPayment)
	devMux.HandleFunc("POST /dev/balance/set", ds.devHandleSetBalance)
	devMux.HandleFunc("POST /dev/balance/outage", ds.devHandleBalanceOutage)
	devMux.HandleFunc("POST /dev/listing/expire", ds.devHandleExpireListing)
	devMux.HandleFunc("POST /dev/telegram/review-callback", ds.devHandleSimulateReviewCallback)
	devMux.HandleFunc("POST /dev/telegram/simulate-start", ds.devHandleSimulateStart)
	devMux.HandleFunc("GET /dev/helper/reputation", ds.devHandleHelperReputation)
	devMux.HandleFunc("POST /dev/helper/review/unlock", ds.devHandleReviewUnlock)
	devMux.HandleFunc("POST /dev/helper/invoice/expire", ds.devHandleExpireHelperInvoice)
	devMux.HandleFunc("POST /dev/helper/handoff/expire", ds.devHandleExpireHelperHandoff)
	devMux.HandleFunc("POST /dev/informer/simulate-start", ds.devHandleInformerSimulateStart)
	devMux.HandleFunc("POST /dev/informer/fake-first-publish", ds.devHandleInformerFakeFirstPublish)
	devMux.HandleFunc("POST /dev/informer/run-worker", ds.devHandleInformerRunWorker)
	devMux.HandleFunc("GET /dev/informer/notifications", ds.devHandleInformerNotifications)
	devMux.HandleFunc("GET /dev/db/counts", ds.devHandleDBCounts)
	mux.Handle("/dev/", devCORS(devMux))

	// Public config + health endpoints (match wire.go equivalents but using devPolicy)
	mux.HandleFunc("GET /v2/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"v2":"ready"}`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /v2/public-config", func(w http.ResponseWriter, r *http.Request) {
		type resp struct {
			ClientFeeUSDCents       int     `json:"client_fee_usd_cents"`
			ClientPublicMinUSD      float64 `json:"client_public_min_usd"`
			ClientHardFloorUSD      float64 `json:"client_hard_floor_usd"`
			HelperFeeUSDCents       int     `json:"helper_fee_usd_cents"`
			HelperPreInvoiceMinUSD  float64 `json:"helper_pre_invoice_min_usd"`
			HelperPostPaymentMinUSD float64 `json:"helper_post_payment_min_usd"`
			InformerMinUSD          float64 `json:"informer_min_usd"`
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(resp{ //nolint:errcheck
			ClientFeeUSDCents:       500,
			ClientPublicMinUSD:      devPolicy.ClientPublicMinUSD,
			ClientHardFloorUSD:      devPolicy.ClientHardFloorUSD,
			HelperFeeUSDCents:       1000,
			HelperPreInvoiceMinUSD:  devPolicy.HelperPreInvoiceMinUSD(),
			HelperPostPaymentMinUSD: devPolicy.HelperPostPaymentMinUSD,
			InformerMinUSD:          devPolicy.InformerMinUSD,
		})
	})

	// CORS middleware for all V2 routes (dev only)
	handler := devCORS(mux)

	// Listen
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		log.Fatalf("[v2-dev] listen: %v", err)
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port

	log.Printf("[v2-dev] ════════════════════════════════════════════")
	log.Printf("[v2-dev]  NA Room V2 Dev Server")
	log.Printf("[v2-dev]  Backend:  http://127.0.0.1:%d", actualPort)
	log.Printf("[v2-dev]  DB path:  %s", dbPath)
	log.Printf("[v2-dev] ────────────────────────────────────────────")
	log.Printf("[v2-dev]  Start frontend:")
	log.Printf("[v2-dev]  cd frontend && BACKEND_URL_V2=http://127.0.0.1:%d npm run dev", actualPort)
	log.Printf("[v2-dev] ════════════════════════════════════════════")

	// Write port to stdout for scripts to capture
	fmt.Printf("V2_DEV_PORT=%d\n", actualPort)

	if err := http.Serve(ln, handler); err != nil {
		log.Fatalf("[v2-dev] serve: %v", err)
	}
}

// mountDevV2Routes mirrors V2System.MountRoutes. Explicit route ownership is
// important because a domain handler may intentionally return a safe JSON 404.
func mountDevV2Routes(mux *http.ServeMux, ds *devServer) {
	cMux := ds.clientH.Routes()
	jMux := ds.journeyH.Routes()
	tMux := ds.tgH.Routes()
	hMux := ds.helperH.Routes()
	rMux := ds.reviewH.Routes()
	iMux := ds.informerH.Routes()
	iwMux := ds.informerTransport.Routes()
	csMux := ds.cityH.Routes()

	mux.HandleFunc("POST /v2/client/payment-intents", cMux.ServeHTTP)
	mux.HandleFunc("POST /v2/client/payment-intents/restore", cMux.ServeHTTP)
	mux.HandleFunc("POST /v2/client/payment-intents/recheck-balance", cMux.ServeHTTP)

	mux.HandleFunc("POST /v2/client/listings/restore", jMux.ServeHTTP)
	mux.HandleFunc("POST /v2/client/listings/publish", jMux.ServeHTTP)
	mux.HandleFunc("POST /v2/client/listings/reactivate", jMux.ServeHTTP)
	mux.HandleFunc("POST /v2/listings/{id}/owner-view", jMux.ServeHTTP)
	mux.HandleFunc("GET /v2/board/cities", csMux.ServeHTTP)
	mux.HandleFunc("GET /v2/board/{city}", jMux.ServeHTTP)
	mux.HandleFunc("GET /v2/listings/{listing_id}", jMux.ServeHTTP)

	mux.HandleFunc("POST /v2/client/telegram-links", tMux.ServeHTTP)
	mux.HandleFunc("POST /v2/client/telegram-links/status", tMux.ServeHTTP)
	mux.HandleFunc("POST /v2/telegram/client/webhook", tMux.ServeHTTP)

	mux.HandleFunc("POST /v2/helper/contact-purchases", hMux.ServeHTTP)
	mux.HandleFunc("POST /v2/helper/contact-purchases/restore", hMux.ServeHTTP)
	mux.HandleFunc("POST /v2/helper/contact-purchases/recheck-balance", hMux.ServeHTTP)
	mux.HandleFunc("POST /v2/helper/contact-purchases/reveal", hMux.ServeHTTP)
	mux.HandleFunc("POST /v2/helper/handoff/create", hMux.ServeHTTP)
	mux.HandleFunc("POST /v2/helper/handoff/redeem", hMux.ServeHTTP)
	mux.HandleFunc("POST /v2/helper/reviews/reminder-link", hMux.ServeHTTP)

	mux.HandleFunc("POST /v2/helper/reviews/capability", rMux.ServeHTTP)
	mux.HandleFunc("POST /v2/helper/reviews", rMux.ServeHTTP)

	mux.HandleFunc("POST /v2/informer/access", iMux.ServeHTTP)
	mux.HandleFunc("POST /v2/informer/status", iMux.ServeHTTP)
	mux.HandleFunc("POST /v2/telegram/informer/webhook", iwMux.ServeHTTP)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// ensure stub types implement their interfaces at compile time.
var _ v2.ClientInvoiceIssuer = (*devClientIssuer)(nil)
var _ v2.ClientBalanceReader = (*devBalanceReader)(nil)
var _ v2.BotAPISender = (*devBotSender)(nil)
var _ v2.ReviewNotificationSender = (*devReviewSender)(nil)
