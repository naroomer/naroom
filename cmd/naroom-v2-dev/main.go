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

// ── Stub: dev invoice issuer (client $5) ─────────────────────────────────────

type devClientIssuer struct {
	mu      sync.Mutex
	counter int
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
}

func newDevBalanceReader() *devBalanceReader {
	return &devBalanceReader{balances: make(map[string]float64)}
}

func (r *devBalanceReader) BalanceUSD(_ context.Context, walletAddress, _ string) (float64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.balances[walletAddress]; ok {
		return b, nil
	}
	return 200.0, nil // default: $200 (above client $120 floor and helper $1000 floor)
}

func (r *devBalanceReader) set(walletAddress string, balanceUSD float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.balances[walletAddress] = balanceUSD
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
	if err := v2.ApplySchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return db, nil
}

// ── Dev services ──────────────────────────────────────────────────────────────

type devServer struct {
	db            *sql.DB
	svc           *v2.Service
	ls            *v2.ListingService
	helperSvc     *v2.HelperPurchaseService
	reviewSvc     *v2.ReviewService
	transport     *v2.TelegramTransport
	destCipher    *v2.DestinationCipher
	balance       *devBalanceReader
	reviewSender  *devReviewSender
	botSender     *devBotSender

	// Handlers
	clientH  *v2.ClientHandler
	journeyH *v2.ClientJourneyHandler
	tgH      *v2.TelegramLinkHandler
	helperH  *v2.HelperPurchaseHandler
	reviewH  *v2.HelperReviewHandler
}

func buildDevServer(db *sql.DB) (*devServer, error) {
	ds := &devServer{
		db:           db,
		balance:      newDevBalanceReader(),
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
	helperSvc, err := v2.NewHelperPurchaseService(db, devHMACKey, contactCipher, names, time.Now)
	if err != nil {
		return nil, fmt.Errorf("NewHelperPurchaseService: %w", err)
	}
	ds.helperSvc = helperSvc

	// Review service
	reviewSvc, err := v2.NewReviewService(db, devHMACKey, time.Now)
	if err != nil {
		return nil, fmt.Errorf("NewReviewService: %w", err)
	}
	ds.reviewSvc = reviewSvc

	// Wire review service into transport
	transport.SetReviewService(reviewSvc, ds.reviewSender)

	// HTTP handlers
	clientH, err := v2.NewClientHandler(svc, &devClientIssuer{}, ds.balance, devRateLimitKey, time.Now)
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

	helperH, err := v2.NewHelperPurchaseHandler(helperSvc, &devHelperIssuer{}, ds.balance, devRateLimitKey, time.Now)
	if err != nil {
		return nil, fmt.Errorf("NewHelperPurchaseHandler: %w", err)
	}
	ds.helperH = helperH

	reviewH, err := v2.NewHelperReviewHandler(reviewSvc, devRateLimitKey, time.Now)
	if err != nil {
		return nil, fmt.Errorf("NewHelperReviewHandler: %w", err)
	}
	ds.reviewH = reviewH

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

	// Send pending review notifications now (normally done by watcher)
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

// devHandleStatus handles GET /dev/status
func (ds *devServer) devHandleStatus(w http.ResponseWriter, r *http.Request) {
	devJSON(w, map[string]any{
		"ok":          true,
		"description": "NA Room V2 dev runner — NOT for production use",
		"endpoints": []string{
			"POST /dev/payment/confirm              {flow_id, wallet_address}",
			"POST /dev/telegram/connect             {management_code, wallet_address}",
			"POST /dev/helper/payment/confirm       {purchase_id, wallet_address}",
			"POST /dev/balance/set                  {wallet_address, balance_usd}",
			"POST /dev/listing/expire               {listing_id}",
			"POST /dev/telegram/review-callback     {callback_data}",
			"POST /dev/telegram/simulate-start      {raw_token}",
			"GET  /dev/helper/reputation            ?purchase_id=...",
			"GET  /dev/notifications",
			"GET  /dev/status",
		},
		"bot_url":  "https://t.me/" + devBotUsername + "?start=dev",
		"chat_id":  devFakeChatID,
		"note":     "Use /dev/telegram/simulate-start (routes through real webhook) instead of /dev/telegram/connect for new E2E tests.",
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

	// Build a combined V2 handler. Each Routes() result handles its own patterns;
	// we chain them so each handler gets a chance to serve the request, and the
	// first non-404 response wins.
	v2Handler := chainHandlers(
		ds.clientH.Routes(),
		ds.journeyH.Routes(),
		ds.tgH.Routes(),
		ds.helperH.Routes(),
		ds.reviewH.Routes(),
	)

	mux := http.NewServeMux()
	mux.Handle("/v2/", v2Handler)

	// Dev control endpoints
	devMux := http.NewServeMux()
	devMux.HandleFunc("GET /dev/status", ds.devHandleStatus)
	devMux.HandleFunc("GET /dev/notifications", ds.devHandleNotifications)
	devMux.HandleFunc("POST /dev/payment/confirm", ds.devHandleConfirmPayment)
	devMux.HandleFunc("POST /dev/telegram/connect", ds.devHandleConnectTelegram)
	devMux.HandleFunc("POST /dev/helper/payment/confirm", ds.devHandleConfirmHelperPayment)
	devMux.HandleFunc("POST /dev/balance/set", ds.devHandleSetBalance)
	devMux.HandleFunc("POST /dev/listing/expire", ds.devHandleExpireListing)
	devMux.HandleFunc("POST /dev/telegram/review-callback", ds.devHandleSimulateReviewCallback)
	devMux.HandleFunc("POST /dev/telegram/simulate-start", ds.devHandleSimulateStart)
	devMux.HandleFunc("GET /dev/helper/reputation", ds.devHandleHelperReputation)
	mux.Handle("/dev/", devCORS(devMux))

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

// captureResponseWriter captures the status code without writing to the real writer.
type captureResponseWriter struct {
	http.ResponseWriter
	code    int
	headers http.Header
	buf     []byte
	written bool
}

func (c *captureResponseWriter) WriteHeader(code int) {
	if !c.written {
		c.code = code
		// Copy real headers into capture buffer
		for k, vv := range c.ResponseWriter.Header() {
			c.headers[k] = vv
		}
	}
}

func (c *captureResponseWriter) Write(b []byte) (int, error) {
	c.written = true
	c.buf = append(c.buf, b...)
	return len(b), nil
}

func (c *captureResponseWriter) flush() {
	if c.code != 0 {
		for k, vv := range c.headers {
			c.ResponseWriter.Header()[k] = vv
		}
		c.ResponseWriter.WriteHeader(c.code)
	}
	if len(c.buf) > 0 {
		c.ResponseWriter.Write(c.buf) //nolint:errcheck
	}
}

// chainHandlers returns an http.Handler that tries each sub-handler in order.
// The first handler that writes a response (non-404, non-405) wins.
// This works because each sub-mux from Routes() registers non-overlapping
// specific patterns and returns 404/405 for patterns it doesn't know.
func chainHandlers(handlers ...http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, h := range handlers {
			crw := &captureResponseWriter{
				ResponseWriter: w,
				headers:        make(http.Header),
			}
			h.ServeHTTP(crw, r)
			// A real response was written (not just a default ServeMux 404/405)
			if crw.code != 0 && crw.code != http.StatusNotFound && crw.code != http.StatusMethodNotAllowed {
				crw.flush()
				return
			}
			if crw.code == 0 && len(crw.buf) > 0 {
				// Handler wrote body without explicit WriteHeader (implicit 200)
				crw.code = http.StatusOK
				crw.flush()
				return
			}
		}
		http.NotFound(w, r)
	})
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
