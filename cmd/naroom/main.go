package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"naroom/internal/config"
	"naroom/internal/crypto"
	"naroom/internal/db"
	"naroom/internal/handler"
	"naroom/internal/middleware"
	"naroom/internal/telegram"
	"naroom/internal/worker"
)

func main() {
	cfg := config.Load()

	if cfg.ServerSalt == "" {
		log.Fatal("SERVER_SALT is required")
	}
	if len(cfg.HashKey) == 0 {
		log.Fatal("HASH_KEY (or SERVER_SALT as fallback) is required")
	}

	// Prepare wallet address encryption key (AES-256-GCM).
	// In dev mode: derived from SERVER_SALT if WALLET_ENC_KEY not set.
	// In production: WALLET_ENC_KEY must be set explicitly.
	walletEncKeyStr := os.Getenv("WALLET_ENC_KEY")
	walletEncKey, err := crypto.PrepareEncKey(walletEncKeyStr, cfg.ServerSalt, cfg.DevMode)
	if err != nil {
		log.Fatalf("WALLET_ENC_KEY: %v", err)
	}
	cfg.WalletEncKey = walletEncKey

	// Open database
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("failed to open db: %v", err)
	}
	defer database.Close()

	// Migrate wallet_sessions to encrypted schema (no-op if already migrated).
	if err := db.MigrateWalletEncryption(database, walletEncKey); err != nil {
		log.Fatalf("wallet encryption migration: %v", err)
	}

	// Seed demo listings for all cities
	db.SeedSamples(database)

	// Init crypto clients
	mempool := crypto.NewMempoolClient(cfg.MempoolAPI)
	blockcypher := crypto.NewBlockcypherClient(cfg.BlockcypherAPI)
	prices := crypto.NewPriceCache(5 * time.Minute)

	// Init HD wallet (dev mode если xpub не задан)
	wallet, err := crypto.NewHDWallet(database, cfg.BTCXpub, cfg.LTCXpub)
	if err != nil {
		log.Fatalf("failed to init HD wallet: %v", err)
	}
	if cfg.BTCXpub == "" {
		log.Println("WARNING: BTC xpub not configured — placeholder addresses in use")
	}
	if cfg.LTCXpub == "" {
		log.Println("WARNING: LTC xpub not configured — placeholder addresses in use")
	}

	if cfg.DevMode {
		log.Println("WARNING: DEV_MODE enabled — payments are mocked, do NOT use in production")
		// Seed fixed prices so tests never hit external price APIs (avoids rate limits/network flakiness).
		prices.SetDevPrices(100000.0, 100.0) // $100k BTC, $100 LTC
	}

	// WebSocket hub (created before handler so it can be injected)
	hub := handler.NewChatHub()

	// Init Telegram bots (optional — nil when tokens not configured)
	var tgClient telegram.Sender
	requireTelegram := cfg.TelegramClientBotToken != "" && cfg.TelegramHelperBotToken != "" && cfg.TelegramWebhookSecret != ""
	if requireTelegram {
		tgClient = telegram.NewClient(cfg.TelegramClientBotToken, cfg.TelegramHelperBotToken)
		log.Println("Telegram notification bots enabled")
	}

	// Init handler
	h := &handler.Handler{
		DB:           database,
		HashKey:      cfg.HashKey,
		WalletEncKey: walletEncKey,
		Mempool:      mempool,
		Blockcypher: blockcypher,
		Prices:      prices,
		Wallet:      wallet,
		DevMode:     cfg.DevMode,
		ListingTTL:  cfg.ListingTTL,
		ChatTTL:     cfg.ChatTTL,
		ChatMinTTL:  cfg.ChatMinTTL,
		Hub:         hub,

		Telegram:              tgClient,
		TelegramClientBotName: cfg.TelegramClientBotName,
		TelegramHelperBotName: cfg.TelegramHelperBotName,
		TelegramWebhookSecret: cfg.TelegramWebhookSecret,
		PublicBaseURL:         cfg.PublicBaseURL,
		RequireTelegram:       requireTelegram,
	}

	// ── Rate limiters ────────────────────────────────────────────────────
	// Per-IP (hashed subnet), no raw IPs stored or logged.
	//
	// Notation: NewRateLimiter(events/sec, burst)
	//   5/min  = rate.Limit(5.0/60)  burst 5
	//   60/min = rate.Limit(1.0)     burst 60
	rlWalletVerify  := middleware.NewRateLimiter(10.0/60, 10)  // 10/min/IP
	rlRespond       := middleware.NewRateLimiter(3.0/60, 3)    // 3/min/IP
	rlBoard         := middleware.NewRateLimiter(1.0, 60)      // 60/min/IP
	rlInvoice       := middleware.NewRateLimiter(30.0/60, 30)  // 30/min/IP
	rlAbuse         := middleware.NewRateLimiter(5.0/3600, 5)  // 5/hour/IP
	rlGeneral       := middleware.NewRateLimiter(30.0/60, 30)  // 30/min/IP — всё остальное

	// In dev mode: bypass all rate limits so E2E tests aren't throttled.
	// Rate limiting is tested separately in test 007.
	rateFn := middleware.ByIP
	if cfg.DevMode {
		rateFn = middleware.NoLimit
	}

	// Session middleware (reads Authorization: Bearer <token>)
	requireSession := middleware.RequireSession(database, cfg.DevMode, cfg.HashKey)

	// Router
	r := chi.NewRouter()
	r.Use(middleware.NoLogIP)
	r.Use(middleware.SecurityHeaders)
	r.Use(middleware.Language)

	// Public — no session required
	r.With(rlBoard.Limit(rateFn)).Get("/board/{city}", h.Board)
	r.With(rlGeneral.Limit(rateFn)).Get("/listing/{id}", h.GetListing)
	r.With(middleware.LimitBody(64*1024), rlWalletVerify.Limit(rateFn)).Post("/wallet/register", h.WalletRegister)
	r.With(requireSession, rlInvoice.Limit(rateFn)).Get("/invoice/{id}/status", h.InvoiceStatus)
	r.Get("/api/balance-status", h.BalanceStatus)

	// Session — requires valid Bearer token
	r.With(requireSession, middleware.LimitBody(64*1024), rlGeneral.Limit(rateFn)).Post("/listing/create", h.CreateListing)
	r.With(requireSession, middleware.LimitBody(64*1024), rlGeneral.Limit(rateFn)).Post("/listing/{id}/renew", h.RenewListing)
	r.With(requireSession, rlGeneral.Limit(rateFn)).Get("/listing/{id}/responses", h.GetListingResponses)
	r.With(requireSession, rlGeneral.Limit(rateFn)).Get("/listing/{id}/chatroom", h.GetListingChatRoom)
	r.With(requireSession, middleware.LimitBody(64*1024), rlRespond.Limit(rateFn)).Post("/listing/{id}/respond", h.Respond)
	r.With(requireSession, middleware.LimitBody(64*1024), rlGeneral.Limit(rateFn)).Post("/response/{id}/cancel", h.CancelResponse)
	r.With(requireSession, middleware.LimitBody(64*1024), rlGeneral.Limit(rateFn)).Post("/response/{id}/accept", h.AcceptResponse)
	r.With(requireSession, rlGeneral.Limit(rateFn)).Get("/peer/region", h.GetPeerRegion)
	r.With(requireSession, rlGeneral.Limit(rateFn)).Get("/peer/chatroom", h.GetCounselorChatRoom)
	r.With(requireSession, rlGeneral.Limit(rateFn)).Get("/chat/{room_id}", h.GetChatRoom)
	r.Get("/chat/ws", h.ChatWS(hub)) // auth handled inside handler (WS can't send custom headers)
	r.With(requireSession, middleware.LimitBody(8*1024*1024), rlGeneral.Limit(rateFn)).Post("/chat/poll/send", h.ChatPollSend)
	r.With(requireSession, rlGeneral.Limit(rateFn)).Get("/chat/poll/receive", h.ChatPollReceive)
	r.With(requireSession, middleware.LimitBody(64*1024), rlGeneral.Limit(rateFn)).Post("/chat/{room_id}/close", h.CloseChat)
	// Review: auth via review_token (one-time anonymous token), no session required
	r.With(middleware.LimitBody(64*1024), rlGeneral.Limit(rateFn)).Post("/review", h.Review)
	r.With(requireSession, middleware.LimitBody(64*1024), rlAbuse.Limit(rateFn)).Post("/abuse-report", h.AbuseReport)

	// Session management
	r.With(middleware.LimitBody(64*1024), rlGeneral.Limit(rateFn)).Post("/session/refresh", h.SessionRefresh)
	r.With(requireSession, middleware.LimitBody(64*1024)).Post("/session/revoke", h.SessionRevoke)

	// Telegram notification bots
	r.With(requireSession, middleware.LimitBody(4*1024), rlGeneral.Limit(rateFn)).Post("/telegram/client/token", h.TelegramClientToken)
	r.With(requireSession, middleware.LimitBody(4*1024), rlGeneral.Limit(rateFn)).Post("/telegram/helper/token", h.TelegramHelperToken)
	r.With(rlGeneral.Limit(rateFn)).Get("/telegram/client/confirm", h.TelegramClientConfirm)
	r.With(rlGeneral.Limit(rateFn)).Get("/telegram/helper/confirm", h.TelegramHelperConfirm)
	// Webhooks are called by Telegram servers — no session, but validated by secret token
	r.With(middleware.LimitBody(64*1024)).Post("/telegram/client/webhook", h.TelegramClientWebhook)
	r.With(middleware.LimitBody(64*1024)).Post("/telegram/helper/webhook", h.TelegramHelperWebhook)

	// Health
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start workers
	balanceChecker := &worker.BalanceChecker{
		DB:           database,
		HashKey:      cfg.HashKey,
		WalletEncKey: walletEncKey,
		Mempool:      mempool,
		Blockcypher:  blockcypher,
		Prices:       prices,
		Interval:     time.Duration(cfg.BalanceCheckInterval) * time.Second,
	}

	ttlCleaner := &worker.TTLCleaner{
		DB:       database,
		Interval: time.Duration(cfg.TTLCleanInterval) * time.Second,
	}

	invoiceWatcher := &worker.InvoiceWatcher{
		DB:      database,
		HashKey: cfg.HashKey,
		Mempool: mempool,
		Blockcypher: blockcypher,
		Prices:      prices,
		Interval:    time.Duration(cfg.InvoiceWatchInterval) * time.Second,
		DevMode:     cfg.DevMode,
		ListingTTL:  cfg.ListingTTL,
		ChatTTL:     cfg.ChatTTL,

		RequireTelegram: requireTelegram,
		TelegramSender:  tgClient,
		PublicBaseURL:   cfg.PublicBaseURL,
	}

	go balanceChecker.Run(ctx)
	go ttlCleaner.Run(ctx)
	go invoiceWatcher.Run(ctx)

	// HTTP server
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second, // longer for WebSocket upgrade
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("shutting down...")
		cancel() // stop workers

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Printf("NA Room backend starting on :%s", cfg.Port)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
	log.Println("server stopped")
}
