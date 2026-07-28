// cmd/v2testserver — in-process Go test server for Playwright critical-path E2E tests.
//
// Opens file-backed (or in-memory) SQLite, wires V2 system with recording adapters,
// and serves:
//
//	/api/v2/* — all V2 API routes (strips /api prefix before routing)
//	/dev/*    — control endpoints for test orchestration
//
// Start:
//
//	V2TEST_PORT=4174 V2TEST_DB_PATH=/tmp/run/naroom.db go run ./cmd/v2testserver
//
// When V2TEST_DB_PATH is set, a file-backed DB is used so that a restart
// (same port, same DB path) resumes exactly the same domain state — enabling
// the restart/restore steps in the critical journey.
//
// Prints "V2TEST_READY=1" to stdout after listening so Playwright knows it's ready.
// Never use in production.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	v2 "naroom/internal/v2"

	_ "modernc.org/sqlite"
)

// ── Fixed test keys (non-secret; test only) ──────────────────────────────────

var (
	testHMACKey    = make32(0xAA)
	testContactKey = make32(0xBB)
	testDestKey    = make32(0xCC)
)

func make32(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

// ── Clock with adjustable offset ──────────────────────────────────────────────

type adjustableClock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *adjustableClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *adjustableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset += d
}

// ── Recording: BotAPISender + ReviewNotificationSender ───────────────────────

type capturedReviewPrompt struct {
	ChatID  int64  `json:"chat_id"`
	Text    string `json:"text"`
	PosData string `json:"pos_data"`
	NegData string `json:"neg_data"`
}

type recordingBotSender struct {
	mu      sync.Mutex
	prompts []capturedReviewPrompt
}

func (s *recordingBotSender) SendMessage(_ context.Context, _ int64, _ string) error { return nil }
func (s *recordingBotSender) SendPlainMessage(_ context.Context, _ int64, _ string) error {
	return nil
}

func (s *recordingBotSender) SendReviewPrompt(_ context.Context, chatID int64, text, posData, negData string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts = append(s.prompts, capturedReviewPrompt{
		ChatID:  chatID,
		Text:    text,
		PosData: posData,
		NegData: negData,
	})
	return nil
}

func (s *recordingBotSender) AnswerCallback(_ context.Context, _, _ string) error { return nil }
func (s *recordingBotSender) EditMessage(_ context.Context, _ int64, _ int64, _ string) error {
	return nil
}

// promptsForChat returns captured prompts for the given chat ID (0 = all).
func (s *recordingBotSender) promptsForChat(chatID int64) []capturedReviewPrompt {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []capturedReviewPrompt
	for _, p := range s.prompts {
		if chatID == 0 || p.ChatID == chatID {
			result = append(result, p)
		}
	}
	return result
}

// ── Recording: InformerBotSender ─────────────────────────────────────────────

type capturedInformerMsg struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

type recordingInformerSender struct {
	mu   sync.Mutex
	msgs []capturedInformerMsg
}

func (s *recordingInformerSender) SendInformerNotification(_ context.Context, chatID int64, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, capturedInformerMsg{ChatID: chatID, Text: text})
	return nil
}

func (s *recordingInformerSender) messages() []capturedInformerMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]capturedInformerMsg, len(s.msgs))
	copy(cp, s.msgs)
	return cp
}

// ── Stub: controlledBalanceReader ────────────────────────────────────────────

type controlledBalanceReader struct {
	mu       sync.Mutex
	balances map[string]float64
}

func newControlledBalanceReader() *controlledBalanceReader {
	return &controlledBalanceReader{balances: make(map[string]float64)}
}

func (r *controlledBalanceReader) BalanceUSD(_ context.Context, addr, _ string) (float64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.balances[addr]; ok {
		return b, nil
	}
	return 1100.0, nil // default: $1100 (above $120 client floor and $1000 helper floor)
}

func (r *controlledBalanceReader) set(addr string, usd float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.balances[addr] = usd
}

// ── Stub: chain client ────────────────────────────────────────────────────────

// paymentAddrToWallet maps payment_address → user_wallet_address.
var paymentAddrToWallet sync.Map

// fakeAutoChain returns a single confirmed transaction for any address.
type fakeAutoChain struct{}

func (s *fakeAutoChain) GetInvoiceTxs(_ context.Context, addr string) ([]v2.V2TxResult, error) {
	senderAddr := addr
	if wallet, ok := paymentAddrToWallet.Load(addr); ok {
		senderAddr = wallet.(string)
	}
	return []v2.V2TxResult{
		{
			Txid:           "faketx_" + addr,
			AmountAtomic:   9_000_000,
			Confirmations:  3,
			InputAddresses: []string{senderAddr},
		},
	}, nil
}

// interceptWalletMapping records payment_address → wallet_address from API responses.
func interceptWalletMapping(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		path := r.URL.Path
		isClient := path == "/api/v2/client/payment-intents"
		isHelper := path == "/api/v2/helper/contact-purchases"
		if !isClient && !isHelper {
			next.ServeHTTP(w, r)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		var req struct {
			WalletAddress string `json:"wallet_address"`
		}
		_ = json.Unmarshal(body, &req)

		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)

		if req.WalletAddress != "" && rec.Code >= 200 && rec.Code < 300 {
			var resp struct {
				Invoice struct {
					PaymentAddress string `json:"payment_address"`
				} `json:"invoice"`
			}
			if jerr := json.Unmarshal(rec.Body.Bytes(), &resp); jerr == nil && resp.Invoice.PaymentAddress != "" {
				paymentAddrToWallet.Store(resp.Invoice.PaymentAddress, req.WalletAddress)
			}
		}

		for k, vs := range rec.Result().Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes()) //nolint:errcheck
	})
}

// ── Stub: price source ────────────────────────────────────────────────────────

type stubPrice struct{}

func (s *stubPrice) PricePerCoin(_ context.Context, currency string) (float64, error) {
	if currency == "BTC" {
		return 50000.0, nil
	}
	return 100.0, nil
}

// ── Stub: atomic balance reader ───────────────────────────────────────────────

type stubAtomicBal struct{}

func (s *stubAtomicBal) GetBalanceAtomic(_ context.Context, _ string) (int64, error) {
	return 10_000_000, nil
}

// ── Stub: address allocator ───────────────────────────────────────────────────

type stubAlloc struct {
	mu  sync.Mutex
	seq int
}

func (s *stubAlloc) AllocateAddress(_ context.Context, currency string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return fmt.Sprintf("test_%s_addr_%d", currency, s.seq), nil
}

// ── CORS middleware ───────────────────────────────────────────────────────────

func withCORS(next http.Handler) http.Handler {
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

// ── JSON helpers ──────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// ── InformerWorker polling loop ───────────────────────────────────────────────

func runInformerWorker(ctx context.Context, w *v2.InformerWorker, interval time.Duration) {
	if ctx.Err() == nil {
		_ = w.RunOnce(ctx)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			_ = w.RunOnce(ctx)
		}
	}
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	port := 4174
	if p := os.Getenv("V2TEST_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}

	// Open DB: file-backed when V2TEST_DB_PATH is set, in-memory otherwise.
	var db *sql.DB
	if dbPath := os.Getenv("V2TEST_DB_PATH"); dbPath != "" {
		var err error
		db, err = v2.OpenFile(dbPath)
		if err != nil {
			log.Fatalf("[v2testserver] OpenFile(%s): %v", dbPath, err)
		}
	} else {
		var err error
		db, err = v2.OpenMemory()
		if err != nil {
			log.Fatalf("[v2testserver] OpenMemory: %v", err)
		}
	}
	defer db.Close()

	// Build keys.
	contactCipher, err := v2.NewAESGCMContactCipher(testContactKey, "v1")
	if err != nil {
		log.Fatalf("[v2testserver] NewAESGCMContactCipher: %v", err)
	}
	destCipher, err := v2.NewDestinationCipher(testDestKey, "v1")
	if err != nil {
		log.Fatalf("[v2testserver] NewDestinationCipher: %v", err)
	}

	balReader := newControlledBalanceReader()
	botSender := &recordingBotSender{}
	informerSender := &recordingInformerSender{}
	clock := &adjustableClock{}

	sys, err := v2.WireV2System(
		db,
		v2.V2Keys{
			HMACKey:           testHMACKey,
			ContactCipher:     contactCipher,
			DestCipher:        destCipher,
			ContactKeyVersion: "v1",
		},
		v2.V2BotConfig{
			ClientBotName:         "v2testbot",
			ClientWebhookSecret:   []byte("testwebhooksecret12345678901234"),
			InformerBotName:       "v2testinformerbot",
			InformerWebhookSecret: []byte("testinformersecret1234567890123"),
		},
		v2.V2Adapters{
			BTCChain:       &fakeAutoChain{},
			LTCChain:       &fakeAutoChain{},
			PriceSource:    &stubPrice{},
			BTCBalance:     &stubAtomicBal{},
			LTCBalance:     &stubAtomicBal{},
			HDAllocator:    &stubAlloc{},
			ClientSender:   botSender,
			InformerSender: informerSender,
			Now:            clock.now,
		},
		v2.DefaultV2BalancePolicy(),
	)
	if err != nil {
		log.Fatalf("[v2testserver] WireV2System: %v", err)
	}

	// Disable rate limits so Playwright can poll freely.
	sys.DevDisableRateLimits()

	// ── V2 API routes ─────────────────────────────────────────────────────────
	sysMux := http.NewServeMux()
	sys.MountRoutes(sysMux)

	mainMux := http.NewServeMux()
	mainMux.Handle("/api/v2/", http.StripPrefix("/api", sysMux))

	// ── Dev control endpoints ─────────────────────────────────────────────────
	devMux := http.NewServeMux()

	// GET /dev/status
	devMux.HandleFunc("GET /dev/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	// POST /dev/payment/confirm — body: {flow_id, wallet_address}
	devMux.HandleFunc("POST /dev/payment/confirm", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			FlowID        string `json:"flow_id"`
			WalletAddress string `json:"wallet_address"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid JSON")
			return
		}
		if req.FlowID == "" || req.WalletAddress == "" {
			writeErr(w, 400, "flow_id and wallet_address required")
			return
		}
		if err := sys.DevConfirmClientPayment(req.FlowID, req.WalletAddress); err != nil {
			log.Printf("[v2testserver] DevConfirmClientPayment: %v", err)
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	// POST /dev/telegram/connect — body: {management_code, wallet_address}
	devMux.HandleFunc("POST /dev/telegram/connect", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ManagementCode string `json:"management_code"`
			WalletAddress  string `json:"wallet_address"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid JSON")
			return
		}
		if req.ManagementCode == "" || req.WalletAddress == "" {
			writeErr(w, 400, "management_code and wallet_address required")
			return
		}
		if err := sys.DevConnectTelegramByManagementCode(req.ManagementCode, req.WalletAddress); err != nil {
			log.Printf("[v2testserver] DevConnectTelegramByManagementCode: %v", err)
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	// POST /dev/helper/payment/confirm — body: {purchase_id, wallet_address}
	devMux.HandleFunc("POST /dev/helper/payment/confirm", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			PurchaseID    string `json:"purchase_id"`
			WalletAddress string `json:"wallet_address"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid JSON")
			return
		}
		if req.PurchaseID == "" || req.WalletAddress == "" {
			writeErr(w, 400, "purchase_id and wallet_address required")
			return
		}
		if err := sys.DevConfirmHelperPayment(req.PurchaseID, req.WalletAddress); err != nil {
			log.Printf("[v2testserver] DevConfirmHelperPayment: %v", err)
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	// POST /dev/balance/set — body: {wallet_address, balance_usd}
	devMux.HandleFunc("POST /dev/balance/set", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			WalletAddress string  `json:"wallet_address"`
			BalanceUSD    float64 `json:"balance_usd"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid JSON")
			return
		}
		if req.WalletAddress == "" {
			writeErr(w, 400, "wallet_address required")
			return
		}
		balReader.set(req.WalletAddress, req.BalanceUSD)
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	// POST /dev/time/advance — body: {seconds: N}
	// Shifts the adjustable clock forward by N seconds. Used for daily-reactivation
	// tests that require the listing to have passed its 24-hour window.
	// Also extends receipt_expires_at for all contact_ready / receipt_expired helper
	// purchases by the same amount so that receipts remain valid after the advance.
	devMux.HandleFunc("POST /dev/time/advance", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Seconds float64 `json:"seconds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid JSON")
			return
		}
		if req.Seconds <= 0 {
			writeErr(w, 400, "seconds must be positive")
			return
		}
		delta := int64(req.Seconds)
		// Extend receipt_expires_at for contact_ready and receipt_expired purchases
		// so that the receipt window shifts with the test clock. Reset receipt_expired
		// back to contact_ready (the receipt was valid; only the test clock jumped).
		// Shift first_revealed_at and receipt_expires_at together to preserve the
		// DB CHECK constraint: receipt_expires_at = first_revealed_at + 86400.
		res, execErr := db.Exec(`
			UPDATE v2_helper_purchases
			SET first_revealed_at  = CASE WHEN first_revealed_at  IS NOT NULL THEN first_revealed_at  + ? ELSE NULL END,
			    receipt_expires_at = CASE WHEN receipt_expires_at IS NOT NULL THEN receipt_expires_at + ? ELSE NULL END,
			    state = CASE WHEN state = 'receipt_expired' THEN 'contact_ready' ELSE state END,
			    updated_at = ?
			WHERE state IN ('contact_ready', 'receipt_expired')
			  AND receipt_expires_at IS NOT NULL`,
			delta, delta, clock.now().Unix())
		if execErr != nil {
			log.Printf("[v2testserver] time/advance receipt update err: %v", execErr)
		} else if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("[v2testserver] time/advance: extended receipt for %d purchase(s) by %ds", n, delta)
		} else {
			log.Printf("[v2testserver] time/advance: no contact_ready purchases with receipt to extend (delta=%ds)", delta)
		}
		clock.advance(time.Duration(req.Seconds * float64(time.Second)))
		writeJSON(w, 200, map[string]any{"ok": true, "advanced_seconds": req.Seconds})
	})

	// GET /dev/review-prompts?chat_id=N
	// Returns captured SendReviewPrompt calls for the given chat_id (0 = all).
	devMux.HandleFunc("GET /dev/review-prompts", func(w http.ResponseWriter, r *http.Request) {
		chatID := int64(0)
		if s := r.URL.Query().Get("chat_id"); s != "" {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				chatID = n
			}
		}
		prompts := botSender.promptsForChat(chatID)
		if prompts == nil {
			prompts = []capturedReviewPrompt{}
		}
		writeJSON(w, 200, map[string]any{"prompts": prompts})
	})

	// GET /dev/informer-messages
	// Returns captured SendInformerNotification calls.
	devMux.HandleFunc("GET /dev/informer-messages", func(w http.ResponseWriter, r *http.Request) {
		msgs := informerSender.messages()
		if msgs == nil {
			msgs = []capturedInformerMsg{}
		}
		writeJSON(w, 200, map[string]any{"messages": msgs})
	})

	mainMux.Handle("/dev/", withCORS(devMux))

	handler := withCORS(interceptWalletMapping(mainMux))

	// Listen on the configured port.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		log.Fatalf("[v2testserver] listen: %v", err)
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port
	log.Printf("[v2testserver] listening on http://127.0.0.1:%d", actualPort)

	// Start domain workers with signal-aware context.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// V2Watcher polls fakeAutoChain every 500ms for fast payment detection.
	go sys.V2Watcher.Run(ctx)
	// HelperWatcher polls fakeAutoChain for helper purchases.
	go sys.HelperWatcher.Run(ctx)
	// InformerWorker polls outbox every 500ms for prompt notification delivery.
	go runInformerWorker(ctx, sys.InformerWorker, 500*time.Millisecond)
	// LifecycleWorker runs expiry and review delivery with fast test intervals.
	go sys.LifecycleWorker.Run(ctx, 500*time.Millisecond, 500*time.Millisecond)

	// Signal readiness to Playwright.
	fmt.Printf("V2TEST_READY=1\n")

	srv := &http.Server{Handler: handler}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx) //nolint:errcheck
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[v2testserver] serve: %v", err)
	}
}

// compile-time interface checks.
var _ v2.ClientBalanceReader = (*controlledBalanceReader)(nil)
var _ v2.V2ChainClient = (*fakeAutoChain)(nil)
var _ v2.V2PriceSource = (*stubPrice)(nil)
var _ v2.V2AtomicBalanceReader = (*stubAtomicBal)(nil)
var _ v2.V2AddressAllocator = (*stubAlloc)(nil)
var _ v2.BotAPISender = (*recordingBotSender)(nil)
var _ v2.ReviewNotificationSender = (*recordingBotSender)(nil)
var _ v2.InformerBotSender = (*recordingInformerSender)(nil)
