# AUDIT_CONTEXT — NA Room Security Audit Package

## 1. Дерево проекта

```
.env.example
.gitignore
ARCHITECTURE.md
BACKLOG.md
DATA_RETENTION.md
GITHUB_RELEASE_CHECKLIST.md
PRIVACY_MODEL.md
README.md
SECURITY.md
SELF_HOSTING.md
TESTING.md
TESTING_BRIEF.md
THREAT_MODEL.md
USER_GUIDE.md
USER_GUIDE_ES.md
USER_GUIDE_KA.md
USER_GUIDE_RU.md
cmd/checkaddr/main.go
cmd/naroom/main.go
dev.sh
docs/E2E_PROTOCOL.md
docs/INVARIANTS.md
docs/TEST_MATRIX.md
docs/archive/CODEX_BRIEF.md
docs/archive/DOMAIN_BRIEF.md
docs/archive/DONE.md
docs/archive/SECURITY_PLAN.md
docs/archive/TRANSLATION_TASK_KA.md
docs/archive/TRANSLATION_TASK_KA_GUIDE.md
e2e/lib/assert.js
e2e/lib/chain_stub.js
e2e/lib/crypto.js
e2e/lib/http.js
e2e/lib/runner.js
e2e/lib/server.js
e2e/lib/ws.js
e2e/package-lock.json
e2e/package.json
e2e/run-all.js
e2e/tests/001_happy_path.js
e2e/tests/002_stale_room_guard.js
e2e/tests/003_role_separation_review.js
e2e/tests/004_remote_close_state.js
e2e/tests/005_large_image_payload.js
e2e/tests/006_state_bleed.js
e2e/tests/007_rate_limiting.js
e2e/tests/008_wallet_challenge.js
e2e/tests/009_session_lifecycle.js
e2e/tests/010_ws_auth.js
e2e/tests/011_peer_left_expiry.js
e2e/tests/013_invoice_scoping.js
e2e/tests/014_reputation.js
e2e/tests/015_region_lock.js
e2e/tests/016_role_separation_respond.js
e2e/tests/017_max_responses.js
e2e/tests/018_balance_threshold.js
e2e/tests/019_renewal_blocked.js
e2e/tests/020_devmode_headers.js
e2e/tests/021_cancel_cooldown.js
e2e/tests/022_message_ttl.js
e2e/tests/023_wallet_session_ttl.js
e2e/tests/024_log_privacy.js
e2e/tests/026_analytics_privacy.js
e2e/tests/027_challenge_replay.js
e2e/tests/028_payment_edge_cases.js
e2e/tests/029_ciphertext_only.js
e2e/tests/030_content_type_spoofing.js
e2e/tests/031_concurrent_accept.js
e2e/tests/032_concurrent_close.js
e2e/tests/033_devmode_prod_failsafe.js
frontend/.gitignore
frontend/.npmrc
frontend/README.md
frontend/analytics.test.js
frontend/jsconfig.json
frontend/package-lock.json
frontend/package.json
frontend/src/app.html
frontend/src/lib/analytics.js
frontend/src/lib/assets/favicon.svg
frontend/src/lib/cities.js
frontend/src/lib/i18n.js
frontend/src/lib/index.js
frontend/src/routes/+layout.svelte
frontend/src/routes/+page.server.js
frontend/src/routes/board/[city]/+page.server.js
frontend/src/routes/board/[city]/+page.svelte
frontend/src/routes/chat/[room_id]/+page.svelte
frontend/src/routes/helper/+page.svelte
frontend/src/routes/how-it-works/+page.svelte
frontend/src/routes/listing/[id]/+page.server.js
frontend/src/routes/listing/[id]/+page.svelte
frontend/src/routes/new/+page.svelte
frontend/static/llms.txt
frontend/static/robots.txt
frontend/static/sitemap.xml
frontend/svelte.config.js
frontend/vite.config.js
go.mod
go.sum
internal/config/config.go
internal/config/devmode_dev.go
internal/config/devmode_prod.go
internal/crypto/blockcypher.go
internal/crypto/encrypt.go
internal/crypto/encrypt_test.go
internal/crypto/hdwallet.go
internal/crypto/id.go
internal/crypto/mempool.go
internal/crypto/price.go
internal/crypto/verify.go
internal/crypto/verify_test.go
internal/db/db.go
internal/db/schema.sql
internal/db/seed.go
internal/handler/accept.go
internal/handler/balance.go
internal/handler/board.go
internal/handler/chat_poll.go
internal/handler/chat_ws.go
internal/handler/handler.go
internal/handler/invoice.go
internal/handler/listing.go
internal/handler/register.go
internal/handler/renew.go
internal/handler/respond.go
internal/handler/review.go
internal/handler/session.go
internal/handler/telegram.go
internal/handler/wallet.go
internal/middleware/language.go
internal/middleware/nolog.go
internal/middleware/ratelimit.go
internal/middleware/security.go
internal/middleware/session.go
internal/model/models.go
internal/telegram/client.go
internal/telegram/listing.go
internal/worker/balance_checker.go
internal/worker/client_iface.go
internal/worker/invoice_watcher.go
internal/worker/invoice_watcher_test.go
internal/worker/ttl_cleaner.go
scripts/selftest.sh
```

## 2. Метаданные

### git log --oneline -20
```
8464b0b Security fixes from Fable Five audit (033/028/031/032/build tags)
b8d489a Add testing brief for external reviewer
e995d52 Fix HD wallet derivation path to match Trezor receive addresses
a6fa8a3 Fix public discovery links
1de66e0 Add optional public page analytics
8e5df5b Clarify HASH_KEY documentation
2ed1ff2 Fix repository clone URL
1bacc67 Initial NA Room release
```

### go.mod
```
module naroom

go 1.25.0

require (
	github.com/btcsuite/btcd v0.24.2
	github.com/btcsuite/btcd/btcec/v2 v2.1.3
	github.com/btcsuite/btcd/btcutil v1.1.5
	github.com/go-chi/chi/v5 v5.0.12
	golang.org/x/time v0.15.0
	modernc.org/sqlite v1.29.6
	nhooyr.io/websocket v1.8.11
)

require (
	github.com/btcsuite/btcd/chaincfg/chainhash v1.1.0 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.0.1 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/mattn/go-isatty v0.0.16 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/crypto v0.0.0-20200622213623-75b288015ac9 // indirect
	golang.org/x/sys v0.16.0 // indirect
	modernc.org/gc/v3 v3.0.0-20240107210532-573471604cb6 // indirect
	modernc.org/libc v1.41.0 // indirect
	modernc.org/mathutil v1.6.0 // indirect
	modernc.org/memory v1.7.2 // indirect
	modernc.org/strutil v1.2.0 // indirect
	modernc.org/token v1.1.0 // indirect
)
```

### frontend/package.json
```json
{
	"name": "naroom-frontend",
	"private": true,
	"version": "0.0.1",
	"type": "module",
	"scripts": {
		"dev": "vite dev",
		"build": "vite build",
		"preview": "vite preview",
		"prepare": "svelte-kit sync || echo ''",
		"check": "svelte-kit sync && svelte-check"
	},
	"devDependencies": {
		"@sveltejs/adapter-auto": "^7.0.1",
		"@sveltejs/adapter-node": "^5.5.4",
		"@sveltejs/kit": "^2.63.0",
		"@sveltejs/vite-plugin-svelte": "^7.1.2",
		"svelte": "^5.56.1",
		"svelte-check": "^4.7.1",
		"vite": "^8.0.16"
	},
	"dependencies": {
		"tweetnacl": "^1.0.3"
	}
}
```

### .env.example
```
# Server
PORT=8080
DB_PATH=./naroom.db

# Security
SERVER_SALT=change-me-to-random-64-char-hex-string

# HD Wallet (xpub for generating invoice addresses)
BTC_XPUB=xpub...
LTC_XPUB=Ltub...

# External APIs
MEMPOOL_API=https://mempool.space/api
BLOCKCYPHER_API=https://api.blockcypher.com/v1/ltc/main

# Workers
BALANCE_CHECK_INTERVAL=600
TTL_CLEAN_INTERVAL=60
INVOICE_WATCH_INTERVAL=30

# Optional public-page-only analytics. Leave empty to disable.
PUBLIC_GOATCOUNTER_CODE=
# Example for naroom.net deployment:
# PUBLIC_GOATCOUNTER_CODE=naroom
```

## 3. Ключевой код


### cmd/naroom/main.go
```go
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

	if cfg.DevMode && !config.DevModeAllowed {
		log.Fatal("DEV_MODE=true rejected: binary compiled without -tags dev (production build)")
	}

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
		DevMode:      cfg.DevMode,
		SkipPayments: cfg.DevMode || os.Getenv("DEV_SKIP_PAYMENTS") == "true",
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
```

### internal/config/config.go
```go
package config

import (
	"os"
	"strconv"
)

type Config struct {
	Port         string
	DBPath       string
	ServerSalt   string
	HashKey      []byte // HMAC key for wallet address hashing; loaded from HASH_KEY env (falls back to SERVER_SALT)
	WalletEncKey []byte // AES-256-GCM key for wallet_sessions.wallet_address_enc; required in production

	BTCXpub string
	LTCXpub string

	MempoolAPI     string
	BlockcypherAPI string

	BalanceCheckInterval int // seconds
	TTLCleanInterval     int
	InvoiceWatchInterval int

	// Dev mode: mock payments, no real blockchain checks
	DevMode bool

	// Configurable TTLs for testing (seconds)
	ListingTTL int // default 21600 (6h)
	ChatTTL    int // default 86400 (24h)
	ChatMinTTL int // minimum chat duration for rating (default 21600 = 6h)

	// Telegram notification bots (optional; both must be set to enable)
	TelegramClientBotToken string
	TelegramHelperBotToken string
	TelegramWebhookSecret  string
	TelegramClientBotName  string // e.g. "NARoomClientBot"
	TelegramHelperBotName  string // e.g. "NARoomHelperBot"
	PublicBaseURL          string // e.g. "https://naroom.net"
}

func Load() *Config {
	serverSalt := envOr("SERVER_SALT", "")
	hashKeyStr := envOr("HASH_KEY", serverSalt) // separate key preferred; falls back to SERVER_SALT

	return &Config{
		Port:       envOr("PORT", "8080"),
		DBPath:     envOr("DB_PATH", "./naroom.db"),
		ServerSalt: serverSalt,
		HashKey:    []byte(hashKeyStr),

		BTCXpub: envOr("BTC_XPUB", ""),
		LTCXpub: envOr("LTC_XPUB", ""),

		MempoolAPI:     envOr("MEMPOOL_API", "https://mempool.space/api"),
		BlockcypherAPI: envOr("BLOCKCYPHER_API", "https://api.blockcypher.com/v1/ltc/main"),

		BalanceCheckInterval: envInt("BALANCE_CHECK_INTERVAL", 600),
		TTLCleanInterval:     envInt("TTL_CLEAN_INTERVAL", 60),
		InvoiceWatchInterval: envInt("INVOICE_WATCH_INTERVAL", 30),

		DevMode: envOr("DEV_MODE", "") == "true",

		ListingTTL: envInt("LISTING_TTL", 21600),  // 6h
		ChatTTL:    envInt("CHAT_TTL", 86400),     // 24h
		ChatMinTTL: envInt("CHAT_MIN_TTL", 21600), // 6h minimum for rating

		TelegramClientBotToken: envOr("TELEGRAM_CLIENT_BOT_TOKEN", ""),
		TelegramHelperBotToken: envOr("TELEGRAM_HELPER_BOT_TOKEN", ""),
		TelegramWebhookSecret:  envOr("TELEGRAM_WEBHOOK_SECRET", ""),
		TelegramClientBotName:  envOr("TELEGRAM_CLIENT_BOT_NAME", "NARoomClientBot"),
		TelegramHelperBotName:  envOr("TELEGRAM_HELPER_BOT_NAME", "NARoomHelperBot"),
		PublicBaseURL:          envOr("PUBLIC_BASE_URL", "https://naroom.net"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
```

### internal/config/devmode_dev.go
```go
//go:build dev

package config

// DevModeAllowed is true in dev/test builds (compiled with -tags dev).
const DevModeAllowed = true
```

### internal/config/devmode_prod.go
```go
//go:build !dev

package config

// DevModeAllowed is false in production builds (no -tags dev).
const DevModeAllowed = false
```

### internal/worker/invoice_watcher.go
```go
package worker

import (
	"context"
	"database/sql"
	"log"
	"math"
	"strconv"
	"time"

	ncrypto "naroom/internal/crypto"
	"naroom/internal/telegram"
)

// InvoiceWatcher checks pending invoices for incoming payments.
type InvoiceWatcher struct {
	DB      *sql.DB
	HashKey []byte // HMAC key for WalletHash — matches handler.HashKey
	Mempool     *ncrypto.MempoolClient
	Blockcypher *ncrypto.BlockcypherClient
	Prices      PriceFetcher // implemented by *ncrypto.PriceCache; interface for testability
	Interval    time.Duration
	DevMode      bool
	SkipPayments bool // auto-confirm all invoices without blockchain checks
	ListingTTL   int
	ChatTTL      int

	// Telegram support — set when bot tokens are configured.
	// When RequireTelegram is true, listings only activate after BOTH payment
	// AND Telegram binding are confirmed. When false, payment alone activates
	// (dev mode and deployments without Telegram configured).
	RequireTelegram bool
	TelegramSender  telegram.Sender
	PublicBaseURL   string
}

func (iw *InvoiceWatcher) Run(ctx context.Context) {
	log.Printf("invoice_watcher started (interval %s)", iw.Interval)
	ticker := time.NewTicker(iw.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("invoice_watcher stopped")
			return
		case <-ticker.C:
			iw.watch(ctx)
		}
	}
}

func (iw *InvoiceWatcher) watch(ctx context.Context) {
	rows, err := iw.DB.Query(`
		SELECT id, type, address, amount_crypto, currency, listing_id, response_id, client_pubkey, payer_address, created_at, payment_detected_at, price_at_creation
		FROM invoices
		WHERE status = 'pending'
	`)
	if err != nil {
		log.Printf("invoice_watcher query error: %v", err)
		return
	}
	defer rows.Close()

	type invoice struct {
		id                string
		typ               string
		address           string
		amountCrypto      string
		currency          string
		listingID         sql.NullString
		responseID        sql.NullString
		clientPubkey      sql.NullString
		payerAddress      sql.NullString
		createdAt         int64
		paymentDetectedAt sql.NullInt64
		priceAtCreation   sql.NullFloat64
	}

	var invoices []invoice
	for rows.Next() {
		var inv invoice
		if err := rows.Scan(&inv.id, &inv.typ, &inv.address, &inv.amountCrypto,
			&inv.currency, &inv.listingID, &inv.responseID, &inv.clientPubkey, &inv.payerAddress,
			&inv.createdAt, &inv.paymentDetectedAt, &inv.priceAtCreation); err != nil {
			continue
		}
		invoices = append(invoices, inv)
	}
	rows.Close()

	now := time.Now().Unix()

	for _, inv := range invoices {
		select {
		case <-ctx.Done():
			return
		default:
		}

		time.Sleep(200 * time.Millisecond) // rate limit

		// Expiry logic:
		//   Normal: expire after 1 hour if no payment detected.
		//   Bounded grace: if a payment was detected but balance/price API is down,
		//   give an extra 24h from detection time before expiring.
		//   This prevents punishing valid payments during a temporary API outage.
		expiryDeadline := inv.createdAt + 3600
		if inv.paymentDetectedAt.Valid {
			grace := inv.paymentDetectedAt.Int64 + 86400
			if grace > expiryDeadline {
				expiryDeadline = grace
			}
		}
		if now > expiryDeadline {
			iw.DB.Exec(`UPDATE invoices SET status = 'expired' WHERE id = ?`, inv.id)
			log.Printf("invoice_watcher: expired invoice %s (type=%s)", inv.id, inv.typ)
			continue
		}

		// Dev mode or SkipPayments: автоматически подтверждаем все pending invoices
		if iw.DevMode || iw.SkipPayments {
			iw.confirmInvoice(inv.id, inv.typ, "dev_txid_"+inv.id, 1000000,
				inv.listingID.String, inv.responseID.String, inv.clientPubkey.String)
			continue
		}

		// Check for confirmed payment
		if inv.currency == "BTC" {
			tx, amount, senders, err := iw.Mempool.FindPayment(inv.address, 0)
			if err != nil {
				log.Printf("invoice_watcher: BTC check error for %s: %v", inv.id, err)
				continue
			}
			if tx != nil {
				// Payment found on-chain — record detection time for bounded grace period.
				// This ensures API outages don't expire valid payments within 24h of detection.
				if !inv.paymentDetectedAt.Valid {
					iw.DB.Exec(`UPDATE invoices SET payment_detected_at = ? WHERE id = ? AND payment_detected_at IS NULL`, now, inv.id)
				}
				if !iw.verifySenderAndBalance(inv.id, inv.typ, inv.currency, inv.payerAddress.String, senders, inv.priceAtCreation.Float64) {
					continue
				}
				iw.confirmInvoice(inv.id, inv.typ, tx.TxID, amount,
					inv.listingID.String, inv.responseID.String, inv.clientPubkey.String)
			}
		} else {
			tx, amount, senders, err := iw.Blockcypher.FindPayment(inv.address, 0)
			if err != nil {
				log.Printf("invoice_watcher: LTC check error for %s: %v", inv.id, err)
				continue
			}
			if tx != nil {
				// Payment found on-chain — record detection time for bounded grace period.
				if !inv.paymentDetectedAt.Valid {
					iw.DB.Exec(`UPDATE invoices SET payment_detected_at = ? WHERE id = ? AND payment_detected_at IS NULL`, now, inv.id)
				}
				if !iw.verifySenderAndBalance(inv.id, inv.typ, inv.currency, inv.payerAddress.String, senders, inv.priceAtCreation.Float64) {
					continue
				}
				iw.confirmInvoice(inv.id, inv.typ, tx.Hash, amount,
					inv.listingID.String, inv.responseID.String, inv.clientPubkey.String)
			}
		}
	}
}

// satoshisFromCryptoStr converts a human-readable crypto amount string (e.g. "0.00045678")
// to satoshis/litoshis (integer, 8 decimal places).
func satoshisFromCryptoStr(s string) int64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(math.Round(f * 1e8))
}

func (iw *InvoiceWatcher) confirmInvoice(invoiceID, typ, txid string, amount int64,
	listingID, responseID, clientPubkey string) {

	// Fetch expected amount and currency before confirming.
	var amountCrypto, currency string
	err := iw.DB.QueryRow(`SELECT amount_crypto, currency FROM invoices WHERE id = ?`, invoiceID).
		Scan(&amountCrypto, &currency)
	if err != nil {
		log.Printf("invoice_watcher: cannot fetch invoice %s for amount check: %v", invoiceID, err)
		return
	}

	// In dev mode we skip the amount check (mocked payments send dummy amounts).
	if !iw.DevMode {
		expected := satoshisFromCryptoStr(amountCrypto)
		// Allow up to 1% underpayment (mempool fee fluctuation).
		minAccepted := int64(math.Round(float64(expected) * 0.99))
		if amount < minAccepted {
			log.Printf("invoice_watcher: dust payment for invoice %s: got %d satoshis, need %d (expected %s %s)",
				invoiceID, amount, expected, amountCrypto, currency)
			return
		}
	}

	log.Printf("invoice_watcher: confirmed %s (type=%s, txid=%s, amount=%d sat)", invoiceID, typ, txid, amount)

	tx, err := iw.DB.Begin()
	if err != nil {
		log.Printf("invoice_watcher: begin tx for invoice %s: %v", invoiceID, err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	res, err := tx.Exec(`UPDATE invoices SET status = 'confirmed', txid = ? WHERE id = ? AND status = 'pending'`,
		txid, invoiceID)
	if err != nil {
		log.Printf("invoice_watcher: mark confirmed invoice %s: %v", invoiceID, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Invoice was already confirmed/expired by a concurrent worker — skip all side effects.
		log.Printf("invoice_watcher: invoice %s already processed, skipping", invoiceID)
		return
	}

	now := time.Now().Unix()

	var notifyListingID string // set when a listing is activated/renewed; notified after commit

	switch typ {
	case "listing":
		if listingID == "" {
			log.Printf("invoice_watcher: listing invoice %s has no listing_id", invoiceID)
			return
		}
		if iw.RequireTelegram {
			// Two-gate activation: both confirmed invoice AND Telegram binding required.
			// ActivateListingIfReady checks both within the same transaction.
			activated, err := telegram.ActivateListingIfReady(tx, listingID, iw.ListingTTL, true)
			if err != nil {
				log.Printf("invoice_watcher: activate listing %s: %v", listingID, err)
				return
			}
			if activated {
				log.Printf("invoice_watcher: listing %s activated (payment+telegram)", listingID)
				notifyListingID = listingID
			} else {
				log.Printf("invoice_watcher: listing %s payment confirmed, awaiting telegram binding", listingID)
			}
		} else {
			// Dev mode or no Telegram configured: activate on payment alone.
			ttl := int64(iw.ListingTTL)
			if ttl == 0 {
				ttl = 21600
			}
			res, err := tx.Exec(`
				UPDATE listings
				SET status = 'active', visible_until = ?, payment_txid = ?,
				    first_activated_at = COALESCE(first_activated_at, ?)
				WHERE id = ? AND status = 'pending'
			`, now+ttl, txid, now, listingID)
			if err != nil {
				log.Printf("invoice_watcher: activate listing %s: %v", listingID, err)
				return
			}
			if n, _ := res.RowsAffected(); n > 0 {
				log.Printf("invoice_watcher: listing %s activated (6h)", listingID)
				notifyListingID = listingID
			}
		}

	case "listing_renew":
		if listingID == "" {
			log.Printf("invoice_watcher: renew invoice %s has no listing_id", invoiceID)
			return
		}
		if iw.RequireTelegram {
			renewed, err := telegram.RenewListingIfReady(tx, listingID, iw.ListingTTL, true)
			if err != nil {
				log.Printf("invoice_watcher: renew listing %s: %v", listingID, err)
				return
			}
			if renewed {
				log.Printf("invoice_watcher: listing %s renewed (payment+telegram)", listingID)
				notifyListingID = listingID
			} else {
				log.Printf("invoice_watcher: listing %s renewal payment confirmed, awaiting fresh telegram binding", listingID)
			}
		} else {
			ttl := int64(iw.ListingTTL)
			if ttl == 0 {
				ttl = 21600
			}
			res, err := tx.Exec(`
				UPDATE listings
				SET status = 'active',
				    visible_until = ? + ?,
				    renewal_count = COALESCE(renewal_count, 0) + 1
				WHERE id = ? AND status IN ('active', 'expired')
			`, now, ttl, listingID)
			if err != nil {
				log.Printf("invoice_watcher: renew listing %s: %v", listingID, err)
				return
			}
			if n, _ := res.RowsAffected(); n > 0 {
				log.Printf("invoice_watcher: listing %s renewed (+6h)", listingID)
				notifyListingID = listingID
			}
		}

	case "chat":
		if responseID == "" || clientPubkey == "" {
			log.Printf("invoice_watcher: chat invoice %s missing response_id or client_pubkey", invoiceID)
			return
		}

		// Защита от дублей — не создавать комнату дважды (читаем внутри транзакции)
		var existing string
		err := tx.QueryRow(`SELECT chat_room_id FROM invoices WHERE id = ? AND chat_room_id IS NOT NULL`, invoiceID).Scan(&existing)
		if err == nil && existing != "" {
			log.Printf("invoice_watcher: chat room already created for invoice %s", invoiceID)
			return
		}

		// Получить данные из response — counselor_hash уже хранится хешем
		var listingIDFromResp, counselorHash, counselorPubkey string
		err = tx.QueryRow(`
			SELECT listing_id, counselor_hash, counselor_pubkey
			FROM responses WHERE id = ?
		`, responseID).Scan(&listingIDFromResp, &counselorHash, &counselorPubkey)
		if err != nil {
			log.Printf("invoice_watcher: response %s not found: %v", responseID, err)
			return
		}

		// Получить хеш клиента из listing — wallet_hash уже хранится хешем
		var clientHash string
		err = tx.QueryRow(`SELECT wallet_hash FROM listings WHERE id = ?`, listingIDFromResp).Scan(&clientHash)
		if err != nil {
			log.Printf("invoice_watcher: listing %s not found: %v", listingIDFromResp, err)
			return
		}

		// Создать chat_room — хранятся хеши, не plain адреса
		roomID := ncrypto.NewID("room")
		chatTTL := int64(iw.ChatTTL)
		if chatTTL == 0 {
			chatTTL = 86400
		}
		_, err = tx.Exec(`
			INSERT INTO chat_rooms (id, listing_id, response_id, client_hash, counselor_hash,
			                        client_pubkey, counselor_pubkey, started_at, expires_at, status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active')
		`, roomID, listingIDFromResp, responseID,
			clientHash, counselorHash,
			clientPubkey, counselorPubkey,
			now, now+chatTTL)
		if err != nil {
			log.Printf("invoice_watcher: create chat_room: %v", err)
			return
		}

		// Убрать листинг с борда — чат уже найден
		if _, err = tx.Exec(`UPDATE listings SET status = 'matched' WHERE id = ?`, listingIDFromResp); err != nil {
			log.Printf("invoice_watcher: mark listing matched %s: %v", listingIDFromResp, err)
			return
		}

		// Записать room_id в invoice (защита от дублей)
		if _, err = tx.Exec(`UPDATE invoices SET chat_room_id = ? WHERE id = ?`, roomID, invoiceID); err != nil {
			log.Printf("invoice_watcher: set chat_room_id on invoice %s: %v", invoiceID, err)
			return
		}

		log.Printf("invoice_watcher: chat room %s created (listing=%s, response=%s)",
			roomID, listingIDFromResp, responseID)
	}

	if err := tx.Commit(); err != nil {
		log.Printf("invoice_watcher: commit invoice %s: %v", invoiceID, err)
		return
	}

	// Notify matching helpers after commit (listing must be 'active' in DB)
	if notifyListingID != "" && iw.TelegramSender != nil {
		boardURL := iw.PublicBaseURL + "/board"
		go telegram.NotifyMatchingHelpers(context.Background(), iw.DB, iw.TelegramSender, notifyListingID, boardURL)
	}
}

// verifySenderAndBalance checks that the payment came from the registered wallet address
// and that the sender's balance still meets the minimum threshold (with $10 buffer for price swings).
//
// BTC/LTC transactions can have multiple inputs from different addresses.
// We accept the payment if ANY of the senders matches the registered wallet hash.
//
// Error handling:
//   - Confirmed mismatch (wrong sender, wrong hash, no senders): reject invoice
//   - API error (balance check, price feed): leave invoice pending — will retry next cycle
//   - Empty payerAddress: reject — indicates a data integrity problem
// verifySenderAndBalance checks sender identity and post-payment balance.
// priceAtCreation: USD/coin rate stored when the invoice was created (0 if unknown).
// At confirmation we use the more user-favorable of creation price and current price,
// so a price drop between creation and confirmation does not incorrectly fail the gate.
func (iw *InvoiceWatcher) verifySenderAndBalance(invoiceID, typ, currency, payerAddress string, senders []string, priceAtCreation float64) bool {
	// DevMode: skip all verification, confirm everything
	if iw.DevMode {
		return true
	}

	// Empty payerAddress means the invoice was created without a registered wallet — data integrity error
	if payerAddress == "" {
		log.Printf("invoice_watcher: empty payer_address for invoice %s — rejecting", invoiceID)
		iw.DB.Exec(`UPDATE invoices SET status = 'rejected' WHERE id = ?`, invoiceID)
		return false
	}

	// No senders in transaction inputs — unreadable tx, reject
	if len(senders) == 0 {
		log.Printf("invoice_watcher: no sender addresses in tx for invoice %s — rejecting", invoiceID)
		iw.DB.Exec(`UPDATE invoices SET status = 'rejected' WHERE id = ?`, invoiceID)
		return false
	}

	// Verify at least one sender matches the registered wallet — compare hashes, never plain addresses
	var matchedSender string
	for _, s := range senders {
		if ncrypto.WalletHash(iw.HashKey, s) == payerAddress {
			matchedSender = s
			break
		}
	}
	if matchedSender == "" {
		log.Printf("invoice_watcher: no sender hash matches payer for invoice %s — rejecting", invoiceID)
		iw.DB.Exec(`UPDATE invoices SET status = 'rejected' WHERE id = ?`, invoiceID)
		return false
	}

	// Balance check: sender must still hold the required minimum AFTER paying the invoice.
	//
	// Invariant (intentional product decision):
	//   listing ($5 payment):  client registered with ≥$150 → post-payment check ≥$135 ($150 - $5 - $10 buffer)
	//   chat ($15 payment):    peer registered with ≥$1000 → post-payment check ≥$975 ($1000 - $15 - $10 buffer)
	//
	// The lower post-payment threshold is by design — we do not penalize users for the platform fee itself.
	// The $10 buffer covers price volatility in the ~30s poll interval. This is a heuristic, not a hard guarantee.
	if iw.Prices == nil {
		return true // no price client configured, skip balance check
	}

	invoiceCost := 5.0
	minHold := 150.0
	if typ == "chat" {
		invoiceCost = 15.0
		minHold = 1000.0
	}
	minUSD := minHold - invoiceCost - 10.0 // subtract invoice cost + $10 volatility buffer

	var balanceUSD float64
	if currency == "BTC" {
		sat, err := iw.Mempool.GetBalance(matchedSender)
		if err != nil {
			log.Printf("invoice_watcher: BTC balance check failed for invoice %s: %v — leaving pending", invoiceID, err)
			return false // leave pending, retry next cycle
		}
		currentPrice, err := iw.Prices.BTCPrice()
		if err != nil {
			log.Printf("invoice_watcher: BTC price unavailable for invoice %s: %v — leaving pending", invoiceID, err)
			return false // leave pending, retry next cycle
		}
		// Use the more favorable price (higher = more USD per coin = higher apparent balance).
		// This protects users from price drops between invoice creation and confirmation.
		price := currentPrice
		if priceAtCreation > price {
			price = priceAtCreation
		}
		balanceUSD = float64(sat) / 1e8 * price
	} else {
		lit, err := iw.Blockcypher.GetBalance(matchedSender)
		if err != nil {
			log.Printf("invoice_watcher: LTC balance check failed for invoice %s: %v — leaving pending", invoiceID, err)
			return false // leave pending, retry next cycle
		}
		currentPrice, err := iw.Prices.LTCPrice()
		if err != nil {
			log.Printf("invoice_watcher: LTC price unavailable for invoice %s: %v — leaving pending", invoiceID, err)
			return false // leave pending, retry next cycle
		}
		price := currentPrice
		if priceAtCreation > price {
			price = priceAtCreation
		}
		balanceUSD = float64(lit) / 1e8 * price
	}

	if balanceUSD < minUSD {
		log.Printf("invoice_watcher: insufficient balance for invoice %s: sender has $%.2f, need $%.2f — rejecting",
			invoiceID, balanceUSD, minUSD)
		iw.DB.Exec(`UPDATE invoices SET status = 'rejected' WHERE id = ?`, invoiceID)
		return false
	}

	log.Printf("invoice_watcher: sender verified for invoice %s: balance $%.2f ≥ $%.2f", invoiceID, balanceUSD, minUSD)
	return true
}
```

### internal/worker/balance_checker.go
```go
package worker

import (
	"context"
	"database/sql"
	"log"
	"time"

	ncrypto "naroom/internal/crypto"
)

const gracePeriodSec = 30 * 60 // 30 минут grace period перед fail

// BalanceChecker periodically checks balances of active wallet sessions.
type BalanceChecker struct {
	DB           *sql.DB
	HashKey      []byte // HMAC key for WalletHash — matches handler.HashKey
	WalletEncKey []byte // AES-256-GCM key for decrypting wallet_address_enc
	Mempool      *ncrypto.MempoolClient
	Blockcypher  *ncrypto.BlockcypherClient
	Prices       *ncrypto.PriceCache
	Interval     time.Duration
}

func (bc *BalanceChecker) Run(ctx context.Context) {
	log.Printf("balance_checker started (interval %s)", bc.Interval)
	ticker := time.NewTicker(bc.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("balance_checker stopped")
			return
		case <-ticker.C:
			bc.check(ctx)
		}
	}
}

type walletSession struct {
	walletHash  string
	addrEnc     string
	currency    string
	role        string
	status      string
	minRequired float64
	lowSince    sql.NullInt64
}

func shortHash(h string) string {
	if len(h) <= 8 {
		return h
	}
	return h[:8]
}

func (bc *BalanceChecker) check(ctx context.Context) {
	rows, err := bc.DB.Query(`
		SELECT wallet_hash, wallet_address_enc, currency, role, balance_status, min_required_usd, low_since
		FROM wallet_sessions
		WHERE balance_status IN ('ok', 'low')
	`)
	if err != nil {
		log.Printf("balance_checker query error: %v", err)
		return
	}
	defer rows.Close()

	var sessions []walletSession
	for rows.Next() {
		var s walletSession
		if err := rows.Scan(&s.walletHash, &s.addrEnc, &s.currency, &s.role, &s.status, &s.minRequired, &s.lowSince); err != nil {
			continue
		}
		sessions = append(sessions, s)
	}
	rows.Close()

	// Fetch prices once for the whole iteration
	btcPrice, btcErr := bc.Prices.BTCPrice()
	ltcPrice, ltcErr := bc.Prices.LTCPrice()

	for _, s := range sessions {
		select {
		case <-ctx.Done():
			return
		default:
		}

		time.Sleep(100 * time.Millisecond) // rate limit

		// Decrypt address only when needed for the blockchain API call.
		// Plain address is never stored in memory beyond this scope.
		plainAddr, err := ncrypto.DecryptAddress(bc.WalletEncKey, s.addrEnc)
		if err != nil {
			log.Printf("balance_checker: decrypt error for %s: %v — skipping", shortHash(s.walletHash), err)
			continue
		}

		var balanceSat int64
		var fetchErr error
		var usdBalance float64

		if s.currency == "BTC" {
			if btcErr != nil {
				log.Printf("balance_checker: BTC price unavailable, skipping %s", shortHash(s.walletHash))
				continue
			}
			balanceSat, fetchErr = bc.Mempool.GetBalance(plainAddr)
			if fetchErr != nil {
				log.Printf("balance_checker: %s fetch error: %v (keeping status)", shortHash(s.walletHash), fetchErr)
				continue
			}
			usdBalance = float64(balanceSat) / 1e8 * btcPrice
		} else {
			if ltcErr != nil {
				log.Printf("balance_checker: LTC price unavailable, skipping %s", shortHash(s.walletHash))
				continue
			}
			balanceSat, fetchErr = bc.Blockcypher.GetBalance(plainAddr)
			if fetchErr != nil {
				log.Printf("balance_checker: %s fetch error: %v (keeping status)", shortHash(s.walletHash), fetchErr)
				continue
			}
			usdBalance = float64(balanceSat) / 1e8 * ltcPrice
		}

		now := time.Now().Unix()
		bc.updateStatus(s, plainAddr, usdBalance, now)
	}

	if len(sessions) > 0 {
		log.Printf("balance_checker: checked %d wallets", len(sessions))
	}
}

func (bc *BalanceChecker) updateStatus(s walletSession, plainAddr string, usdBalance float64, now int64) {
	if usdBalance >= s.minRequired {
		if s.status != "ok" {
			bc.DB.Exec(`UPDATE wallet_sessions SET balance_status = 'ok', balance_usd = ?, low_since = NULL, last_checked_at = ? WHERE wallet_hash = ?`,
				usdBalance, now, s.walletHash)
			log.Printf("balance_checker: %s restored to ok (%.2f USD)", shortHash(s.walletHash), usdBalance)
		} else {
			bc.DB.Exec(`UPDATE wallet_sessions SET balance_usd = ?, last_checked_at = ? WHERE wallet_hash = ?`, usdBalance, now, s.walletHash)
		}
		return
	}

	if s.status == "ok" {
		bc.DB.Exec(`UPDATE wallet_sessions SET balance_status = 'low', balance_usd = ?, low_since = ?, last_checked_at = ? WHERE wallet_hash = ?`,
			usdBalance, now, now, s.walletHash)
		log.Printf("balance_checker: %s went low (%.2f USD < %.2f required)", shortHash(s.walletHash), usdBalance, s.minRequired)
		return
	}

	if s.lowSince.Valid && (now-s.lowSince.Int64) >= gracePeriodSec {
		bc.DB.Exec(`UPDATE wallet_sessions SET balance_status = 'fail', balance_usd = ?, last_checked_at = ? WHERE wallet_hash = ?`,
			usdBalance, now, s.walletHash)
		log.Printf("balance_checker: %s FAIL after grace period (%.2f USD)", shortHash(s.walletHash), usdBalance)
		bc.closeChatsAndListings(s.walletHash)
	} else {
		bc.DB.Exec(`UPDATE wallet_sessions SET balance_usd = ?, last_checked_at = ? WHERE wallet_hash = ?`, usdBalance, now, s.walletHash)
	}
}

// closeChatsAndListings closes all active chats and listings for a wallet that failed the balance gate.
// Uses wallet_hash — plain address is never stored in or compared against chats/listings tables.
func (bc *BalanceChecker) closeChatsAndListings(walletHash string) {
	now := time.Now().Unix()

	res, _ := bc.DB.Exec(`
		UPDATE chat_rooms
		SET status = 'closed', closed_at = ?, closed_by = 'balance'
		WHERE status = 'active'
		AND (client_hash = ? OR counselor_hash = ?)
	`, now, walletHash, walletHash)
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("balance_checker: closed %d chats for %s (balance fail)", n, shortHash(walletHash))
	}

	res, _ = bc.DB.Exec(`
		UPDATE listings SET status = 'closed_balance'
		WHERE status = 'active' AND wallet_hash = ?
	`, walletHash)
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("balance_checker: closed %d listings for %s (balance fail)", n, shortHash(walletHash))
	}
}
```

### internal/worker/client_iface.go
```go
package worker

// PriceFetcher is satisfied by *ncrypto.PriceCache.
// Using an interface here enables test mocking without external API calls.
type PriceFetcher interface {
	BTCPrice() (float64, error)
	LTCPrice() (float64, error)
}
```

### internal/crypto/verify.go
```go
package crypto

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
)

// WalletVerifier verifies that a wallet address signed a given message.
type WalletVerifier interface {
	Verify(address, message, signatureBase64 string) error
}

// VerifyBTCMessage verifies a Bitcoin signed message (legacy format, all common address types).
// Supports P2PKH (1...), P2WPKH (bc1...).
func VerifyBTCMessage(address, message, sigBase64 string) error {
	return verifyMessage(address, message, sigBase64, "Bitcoin Signed Message:\n", &chaincfg.MainNetParams)
}

// VerifyLTCMessage verifies a Litecoin signed message (same algorithm, different magic + network).
func VerifyLTCMessage(address, message, sigBase64 string) error {
	return verifyMessage(address, message, sigBase64, "Litecoin Signed Message:\n", ltcMainNetParams)
}

// ltcMainNetParams defines the minimal Litecoin mainnet address parameters.
// btcd does not bundle Litecoin, so we declare just what we need.
var ltcMainNetParams = &chaincfg.Params{
	Name:             "ltc-mainnet",
	PubKeyHashAddrID: 0x30, // 'L' addresses
	ScriptHashAddrID: 0x32, // 'M' addresses
	Bech32HRPSegwit:  "ltc",
}

// verifyMessage is the shared implementation for BTC and LTC.
func verifyMessage(address, message, sigBase64, magic string, params *chaincfg.Params) error {
	sigBytes, err := base64.StdEncoding.DecodeString(sigBase64)
	if err != nil {
		return errors.New("invalid signature encoding: must be base64")
	}
	if len(sigBytes) != 65 {
		return fmt.Errorf("invalid signature length: got %d, want 65", len(sigBytes))
	}

	hash := bitcoinMessageHash(magic, message)

	pubKey, compressed, err := ecdsa.RecoverCompact(sigBytes, hash)
	if err != nil {
		return fmt.Errorf("signature recovery failed: %w", err)
	}

	// Try all address types the recovered key can produce and see if any matches.
	candidates, err := addressCandidates(pubKey.SerializeCompressed(), pubKey.SerializeUncompressed(), compressed, params)
	if err != nil {
		return fmt.Errorf("address derivation failed: %w", err)
	}

	for _, candidate := range candidates {
		if candidate == address {
			return nil
		}
	}
	return errors.New("signature does not match address")
}

// addressCandidates returns all Bitcoin-style addresses the recovered key could map to.
func addressCandidates(compressed, uncompressed []byte, isCompressed bool, params *chaincfg.Params) ([]string, error) {
	var out []string

	// P2PKH compressed
	if addr, err := p2pkhAddress(compressed, params); err == nil {
		out = append(out, addr)
	}

	// P2PKH uncompressed (only if the signature indicated uncompressed key)
	if !isCompressed {
		if addr, err := p2pkhAddress(uncompressed, params); err == nil {
			out = append(out, addr)
		}
	}

	// P2WPKH native segwit (bc1... / ltc1...) — only compressed keys
	if params.Bech32HRPSegwit != "" {
		if addr, err := p2wpkhAddress(compressed, params); err == nil {
			out = append(out, addr)
		}
	}

	return out, nil
}

func p2pkhAddress(pubKeyBytes []byte, params *chaincfg.Params) (string, error) {
	hash160 := btcutil.Hash160(pubKeyBytes)
	addr, err := btcutil.NewAddressPubKeyHash(hash160, params)
	if err != nil {
		return "", err
	}
	return addr.EncodeAddress(), nil
}

func p2wpkhAddress(pubKeyBytes []byte, params *chaincfg.Params) (string, error) {
	hash160 := btcutil.Hash160(pubKeyBytes)
	addr, err := btcutil.NewAddressWitnessPubKeyHash(hash160, params)
	if err != nil {
		return "", err
	}
	return addr.EncodeAddress(), nil
}

// bitcoinMessageHash computes the double-SHA256 hash used for Bitcoin-style message signing.
// Format: varint(len(magic)) + magic + varint(len(message)) + message
func bitcoinMessageHash(magic, message string) []byte {
	payload := appendVarString(nil, magic)
	payload = appendVarString(payload, message)
	h1 := sha256.Sum256(payload)
	h2 := sha256.Sum256(h1[:])
	return h2[:]
}

// appendVarString appends a Bitcoin-varint-prefixed string to dst.
func appendVarString(dst []byte, s string) []byte {
	n := len(s)
	switch {
	case n < 0xfd:
		dst = append(dst, byte(n))
	case n <= 0xffff:
		dst = append(dst, 0xfd, byte(n), byte(n>>8))
	case n <= 0xffffffff:
		dst = append(dst, 0xfe, byte(n), byte(n>>8), byte(n>>16), byte(n>>24))
	default:
		dst = append(dst, 0xff,
			byte(n), byte(n>>8), byte(n>>16), byte(n>>24),
			byte(n>>32), byte(n>>40), byte(n>>48), byte(n>>56))
	}
	return append(dst, s...)
}
```

### internal/crypto/encrypt.go
```go
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

// EncryptAddress encrypts a wallet address with AES-256-GCM.
// Returns a base64url-encoded string: nonce (12 bytes) + GCM ciphertext + auth tag.
// Each call produces different output (random nonce) — safe for storage.
func EncryptAddress(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("encrypt: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("encrypt: new gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("encrypt: nonce: %w", err)
	}
	// Seal appends ciphertext+tag to nonce → result is nonce||ciphertext||tag
	out := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawURLEncoding.EncodeToString(out), nil
}

// DecryptAddress reverses EncryptAddress. Returns an error on key mismatch or tampered data
// (GCM authentication tag protects integrity).
func DecryptAddress(key []byte, encoded string) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decrypt: base64: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("decrypt: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("decrypt: new gcm: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize+gcm.Overhead() {
		return "", fmt.Errorf("decrypt: ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: authentication failed (wrong key or tampered data)")
	}
	return string(plaintext), nil
}

// IsLikelyBTC returns true if the address looks like a Bitcoin address (1…, 3…, bc1…).
// Used when currency is not stored explicitly (e.g. migration of old rows).
func IsLikelyBTC(addr string) bool {
	if len(addr) == 0 {
		return true
	}
	return addr[0] == '1' || addr[0] == '3' || (len(addr) > 3 && addr[:3] == "bc1")
}

// PrepareEncKey normalises a raw key string to exactly 32 bytes via SHA-256.
// In dev mode: if rawKey is empty, derives a key from serverSalt (so dev doesn't need extra config).
// In production: rawKey must be non-empty; hard-fails otherwise.
func PrepareEncKey(rawKey, serverSalt string, devMode bool) ([]byte, error) {
	if rawKey != "" {
		h := sha256.Sum256([]byte(rawKey))
		return h[:], nil
	}
	if devMode {
		// Dev-only fallback: derive from SERVER_SALT with a fixed domain separator.
		// This produces a stable key for local testing without requiring extra config.
		h := sha256.Sum256(append([]byte("naroom-wallet-enc:"), []byte(serverSalt)...))
		return h[:], nil
	}
	return nil, fmt.Errorf("WALLET_ENC_KEY is required in production (DEV_MODE is not set)")
}
```

### internal/crypto/mempool.go
```go
package crypto

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// MempoolClient talks to mempool.space API for BTC.
type MempoolClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewMempoolClient(baseURL string) *MempoolClient {
	return &MempoolClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// GetBalance returns confirmed balance in satoshis for a BTC address.
func (m *MempoolClient) GetBalance(address string) (int64, error) {
	url := fmt.Sprintf("%s/address/%s", m.baseURL, address)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, fmt.Errorf("mempool balance: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; naroom/1.0)")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("mempool balance: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		return 0, fmt.Errorf("mempool rate limited")
	}
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("mempool status %d", resp.StatusCode)
	}

	var data struct {
		ChainStats struct {
			FundedSum int64 `json:"funded_txo_sum"`
			SpentSum  int64 `json:"spent_txo_sum"`
		} `json:"chain_stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, fmt.Errorf("mempool decode: %w", err)
	}

	return data.ChainStats.FundedSum - data.ChainStats.SpentSum, nil
}

// GetReceivedByAddress checks if a specific address received any transaction.
func (m *MempoolClient) GetReceivedByAddress(address string) ([]MempoolTx, error) {
	url := fmt.Sprintf("%s/address/%s/txs", m.baseURL, address)
	resp, err := m.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("mempool txs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("mempool txs status %d", resp.StatusCode)
	}

	var txs []MempoolTx
	if err := json.NewDecoder(resp.Body).Decode(&txs); err != nil {
		return nil, fmt.Errorf("mempool txs decode: %w", err)
	}
	return txs, nil
}

// MempoolTx is a simplified transaction from mempool.space.
type MempoolTx struct {
	TxID   string `json:"txid"`
	Status struct {
		Confirmed   bool  `json:"confirmed"`
		BlockHeight int64 `json:"block_height"`
	} `json:"status"`
	Vout []struct {
		ScriptPubkeyAddress string `json:"scriptpubkey_address"`
		Value               int64  `json:"value"` // satoshis
	} `json:"vout"`
	Vin []struct {
		Prevout struct {
			ScriptPubkeyAddress string `json:"scriptpubkey_address"`
		} `json:"prevout"`
	} `json:"vin"`
}

// FindPayment looks for a confirmed payment of at least minSatoshis to the given address.
// Returns the transaction, amount in satoshis, and all unique sender addresses from all inputs.
// BTC transactions can have multiple inputs from different addresses — callers must check all of them.
func (m *MempoolClient) FindPayment(address string, minSatoshis int64) (*MempoolTx, int64, []string, error) {
	txs, err := m.GetReceivedByAddress(address)
	if err != nil {
		return nil, 0, nil, err
	}

	for i := range txs {
		tx := &txs[i]
		if !tx.Status.Confirmed {
			continue
		}
		for _, vout := range tx.Vout {
			if vout.ScriptPubkeyAddress == address && vout.Value >= minSatoshis {
				var senders []string
				seen := map[string]bool{}
				for _, vin := range tx.Vin {
					if addr := vin.Prevout.ScriptPubkeyAddress; addr != "" && !seen[addr] {
						senders = append(senders, addr)
						seen[addr] = true
					}
				}
				return tx, vout.Value, senders, nil
			}
		}
	}

	return nil, 0, nil, nil
}
```

### internal/crypto/blockcypher.go
```go
package crypto

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// BlockcypherClient talks to blockcypher API for LTC.
type BlockcypherClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewBlockcypherClient(baseURL string) *BlockcypherClient {
	return &BlockcypherClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// GetBalance returns confirmed balance in litoshis for an LTC address.
func (b *BlockcypherClient) GetBalance(address string) (int64, error) {
	url := fmt.Sprintf("%s/addrs/%s/balance", b.baseURL, address)
	resp, err := b.httpClient.Get(url)
	if err != nil {
		return 0, fmt.Errorf("blockcypher balance: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		return 0, fmt.Errorf("blockcypher rate limited")
	}
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("blockcypher status %d", resp.StatusCode)
	}

	var data struct {
		Balance int64 `json:"balance"` // confirmed
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, fmt.Errorf("blockcypher decode: %w", err)
	}

	return data.Balance, nil
}

// GetTransactions returns transactions for an LTC address.
func (b *BlockcypherClient) GetTransactions(address string) ([]BlockcypherTx, error) {
	url := fmt.Sprintf("%s/addrs/%s/full?limit=10", b.baseURL, address)
	resp, err := b.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("blockcypher txs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("blockcypher txs status %d", resp.StatusCode)
	}

	var data struct {
		Txs []BlockcypherTx `json:"txs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("blockcypher txs decode: %w", err)
	}

	return data.Txs, nil
}

type BlockcypherTx struct {
	Hash          string `json:"hash"`
	Confirmations int    `json:"confirmations"`
	Outputs       []struct {
		Addresses []string `json:"addresses"`
		Value     int64    `json:"value"` // litoshis
	} `json:"outputs"`
	Inputs []struct {
		Addresses []string `json:"addresses"`
	} `json:"inputs"`
}

// FindPayment looks for a confirmed payment to the given address.
// Returns the transaction, amount in litoshis, and all unique sender addresses from all inputs.
// LTC transactions can have multiple inputs from different addresses — callers must check all of them.
func (b *BlockcypherClient) FindPayment(address string, minLitoshis int64) (*BlockcypherTx, int64, []string, error) {
	txs, err := b.GetTransactions(address)
	if err != nil {
		return nil, 0, nil, err
	}

	for i := range txs {
		tx := &txs[i]
		if tx.Confirmations < 1 {
			continue
		}
		for _, out := range tx.Outputs {
			for _, addr := range out.Addresses {
				if addr == address && out.Value >= minLitoshis {
					var senders []string
					seen := map[string]bool{}
					for _, inp := range tx.Inputs {
						for _, a := range inp.Addresses {
							if a != "" && !seen[a] {
								senders = append(senders, a)
								seen[a] = true
							}
						}
					}
					return tx, out.Value, senders, nil
				}
			}
		}
	}

	return nil, 0, nil, nil
}
```

### internal/crypto/hdwallet.go
```go
package crypto

import (
	"crypto/sha256"
	"database/sql"
	"fmt"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
)

// litecoinMainNetParams — минимальный набор параметров для LTC P2PKH адресов.
var litecoinMainNetParams = &chaincfg.Params{
	Name:             "ltc-mainnet",
	PubKeyHashAddrID: 0x30, // L...
	ScriptHashAddrID: 0x32, // M...
}

// HDWallet деривирует BTC/LTC адреса из xpub/zpub для приёма платежей.
type HDWallet struct {
	db         *sql.DB
	btcKey     *hdkeychain.ExtendedKey
	btcSegwit  bool // true → генерировать bech32 (bc1...) вместо legacy (1...)
	ltcKey     *hdkeychain.ExtendedKey
	ltcSegwit  bool // true → генерировать bech32 LTC (ltc1...) вместо legacy (L...)
}

// zpubVersions: известные SegWit-версии extended public key → заменяем на xpub-версию
// чтобы hdkeychain мог распарсить, а адрес генерируем сами через P2WPKH.
var segwitPubVersions = map[[4]byte]bool{
	{0x04, 0xB2, 0x47, 0x46}: true, // BTC zpub (BIP84)
	{0x04, 0x88, 0xB2, 0x1E}: false, // BTC xpub (BIP44) — legacy
	{0x01, 0xB2, 0x6E, 0xF6}: true, // LTC zpub (BIP84)
	{0x01, 0x9D, 0xA4, 0x62}: false, // LTC xpub — legacy
}

// btcXpubVersion — стандартная xpub версия mainnet
var btcXpubVersion = [4]byte{0x04, 0x88, 0xB2, 0x1E}

// normaliseExtKey принимает xpub или zpub (BTC/LTC), возвращает hdkeychain.ExtendedKey
// и флаг segwit. zpub конвертируется в xpub заменой версии (ключи идентичны).
func normaliseExtKey(pub string) (*hdkeychain.ExtendedKey, bool, error) {
	raw := base58.Decode(pub)
	if len(raw) != 82 {
		return nil, false, fmt.Errorf("invalid key length %d", len(raw))
	}

	var ver [4]byte
	copy(ver[:], raw[:4])

	isSegwit, known := segwitPubVersions[ver]
	if !known {
		return nil, false, fmt.Errorf("unknown key version %x", ver)
	}

	if isSegwit {
		// Заменяем версию на стандартный xpub чтобы hdkeychain мог прочитать
		copy(raw[:4], btcXpubVersion[:])
		h1 := sha256.Sum256(raw[:78])
		h2 := sha256.Sum256(h1[:])
		copy(raw[78:], h2[:4])
		pub = base58.Encode(raw)
	}

	key, err := hdkeychain.NewKeyFromString(pub)
	if err != nil {
		return nil, false, err
	}
	return key, isSegwit, nil
}

// NewHDWallet создаёт HDWallet. Если ключ пустой — работает в dev-режиме
// (возвращает placeholder-адреса). Принимает xpub и zpub форматы.
func NewHDWallet(db *sql.DB, btcXpub, ltcXpub string) (*HDWallet, error) {
	w := &HDWallet{db: db}

	if btcXpub != "" {
		key, segwit, err := normaliseExtKey(btcXpub)
		if err != nil {
			return nil, fmt.Errorf("parse BTC key: %w", err)
		}
		// Спускаемся на external chain (index 0) — стандарт BIP-44/84.
		// Trezor экспортирует zpub на уровне аккаунта (m/84'/0'/0'),
		// receive-адреса находятся по пути m/84'/0'/0'/0/i.
		ext, err := key.Derive(0)
		if err != nil {
			return nil, fmt.Errorf("derive BTC external chain: %w", err)
		}
		w.btcKey = ext
		w.btcSegwit = segwit
	}

	if ltcXpub != "" {
		key, segwit, err := normaliseExtKey(ltcXpub)
		if err != nil {
			return nil, fmt.Errorf("parse LTC key: %w", err)
		}
		ext, err := key.Derive(0)
		if err != nil {
			return nil, fmt.Errorf("derive LTC external chain: %w", err)
		}
		w.ltcKey = ext
		w.ltcSegwit = segwit
	}

	return w, nil
}

// NextBTCAddress возвращает следующий уникальный BTC адрес.
// Если ключ — zpub, генерирует bech32 (bc1...). Иначе legacy (1...).
func (w *HDWallet) NextBTCAddress() (address string, index uint32, err error) {
	index, err = w.incrementIndex("BTC")
	if err != nil {
		return "", 0, err
	}

	if w.btcKey == nil {
		return fmt.Sprintf("btc_dev_%d", index), index, nil
	}

	child, err := w.btcKey.Derive(index)
	if err != nil {
		return "", 0, fmt.Errorf("derive BTC[%d]: %w", index, err)
	}

	if w.btcSegwit {
		pubKey, err := child.ECPubKey()
		if err != nil {
			return "", 0, fmt.Errorf("BTC pubkey[%d]: %w", index, err)
		}
		addr, err := btcutil.NewAddressWitnessPubKeyHash(
			btcutil.Hash160(pubKey.SerializeCompressed()),
			&chaincfg.MainNetParams,
		)
		if err != nil {
			return "", 0, fmt.Errorf("BTC bech32[%d]: %w", index, err)
		}
		return addr.EncodeAddress(), index, nil
	}

	addr, err := child.Address(&chaincfg.MainNetParams)
	if err != nil {
		return "", 0, fmt.Errorf("BTC address[%d]: %w", index, err)
	}
	return addr.EncodeAddress(), index, nil
}

// NextLTCAddress возвращает следующий уникальный LTC адрес.
// Если ключ — zpub, генерирует bech32 (ltc1...). Иначе legacy (L...).
func (w *HDWallet) NextLTCAddress() (address string, index uint32, err error) {
	index, err = w.incrementIndex("LTC")
	if err != nil {
		return "", 0, err
	}

	if w.ltcKey == nil {
		return fmt.Sprintf("ltc_dev_%d", index), index, nil
	}

	child, err := w.ltcKey.Derive(index)
	if err != nil {
		return "", 0, fmt.Errorf("derive LTC[%d]: %w", index, err)
	}

	if w.ltcSegwit {
		pubKey, err := child.ECPubKey()
		if err != nil {
			return "", 0, fmt.Errorf("LTC pubkey[%d]: %w", index, err)
		}
		// LTC bech32: hrp = "ltc"
		ltcSegwitParams := &chaincfg.Params{Bech32HRPSegwit: "ltc"}
		addr, err := btcutil.NewAddressWitnessPubKeyHash(
			btcutil.Hash160(pubKey.SerializeCompressed()),
			ltcSegwitParams,
		)
		if err != nil {
			return "", 0, fmt.Errorf("LTC bech32[%d]: %w", index, err)
		}
		return addr.EncodeAddress(), index, nil
	}

	addr, err := child.Address(litecoinMainNetParams)
	if err != nil {
		return "", 0, fmt.Errorf("LTC address[%d]: %w", index, err)
	}
	return addr.EncodeAddress(), index, nil
}

// incrementIndex атомарно увеличивает счётчик и возвращает текущее значение.
func (w *HDWallet) incrementIndex(currency string) (uint32, error) {
	tx, err := w.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("incrementIndex begin: %w", err)
	}
	defer tx.Rollback()

	var idx uint32
	err = tx.QueryRow(`SELECT next_index FROM invoice_index WHERE currency = ?`, currency).Scan(&idx)
	if err == sql.ErrNoRows {
		idx = 0
		if _, err = tx.Exec(`INSERT INTO invoice_index (currency, next_index) VALUES (?, 1)`, currency); err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	} else {
		if _, err = tx.Exec(`UPDATE invoice_index SET next_index = next_index + 1 WHERE currency = ?`, currency); err != nil {
			return 0, err
		}
	}

	return idx, tx.Commit()
}
```

### internal/crypto/price.go
```go
package crypto

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// PriceCache caches USD→BTC/LTC rates.
type PriceCache struct {
	mu       sync.RWMutex
	btcPrice float64 // 1 BTC in USD
	ltcPrice float64 // 1 LTC in USD
	updated  time.Time
	ttl      time.Duration
}

func NewPriceCache(ttl time.Duration) *PriceCache {
	return &PriceCache{ttl: ttl}
}

// SetDevPrices seeds fixed prices for dev/test mode, avoiding real API calls.
func (pc *PriceCache) SetDevPrices(btcUSD, ltcUSD float64) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.btcPrice = btcUSD
	pc.ltcPrice = ltcUSD
	pc.updated = time.Now()
}

// BTCPrice returns current BTC price in USD.
func (pc *PriceCache) BTCPrice() (float64, error) {
	return pc.getBTCPrice()
}

// LTCPrice returns current LTC price in USD.
func (pc *PriceCache) LTCPrice() (float64, error) {
	return pc.getLTCPrice()
}

// BTCAmount converts USD to BTC amount string.
func (pc *PriceCache) BTCAmount(usd float64) (string, error) {
	price, err := pc.getBTCPrice()
	if err != nil {
		return "", err
	}
	btc := usd / price
	return fmt.Sprintf("%.8f", btc), nil
}

// LTCAmount converts USD to LTC amount string.
func (pc *PriceCache) LTCAmount(usd float64) (string, error) {
	price, err := pc.getLTCPrice()
	if err != nil {
		return "", err
	}
	ltc := usd / price
	return fmt.Sprintf("%.8f", ltc), nil
}

func (pc *PriceCache) getBTCPrice() (float64, error) {
	pc.mu.RLock()
	if time.Since(pc.updated) < pc.ttl && pc.btcPrice > 0 {
		p := pc.btcPrice
		pc.mu.RUnlock()
		return p, nil
	}
	pc.mu.RUnlock()
	return pc.refreshBTC()
}

func (pc *PriceCache) getLTCPrice() (float64, error) {
	pc.mu.RLock()
	if time.Since(pc.updated) < pc.ttl && pc.ltcPrice > 0 {
		p := pc.ltcPrice
		pc.mu.RUnlock()
		return p, nil
	}
	pc.mu.RUnlock()
	return pc.refreshLTC()
}

func (pc *PriceCache) refreshBTC() (float64, error) {
	// mempool.space price API
	resp, err := http.Get("https://mempool.space/api/v1/prices")
	if err != nil {
		return 0, fmt.Errorf("btc price: %w", err)
	}
	defer resp.Body.Close()

	var data struct {
		USD float64 `json:"USD"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, fmt.Errorf("btc price decode: %w", err)
	}

	pc.mu.Lock()
	pc.btcPrice = data.USD
	pc.updated = time.Now()
	pc.mu.Unlock()
	return data.USD, nil
}

func (pc *PriceCache) refreshLTC() (float64, error) {
	resp, err := http.Get("https://api.blockcypher.com/v1/ltc/main")
	if err != nil {
		return 0, fmt.Errorf("ltc price: %w", err)
	}
	defer resp.Body.Close()

	// blockcypher doesn't provide USD price directly,
	// so we use a simple coingecko fallback
	resp2, err := http.Get("https://api.coingecko.com/api/v3/simple/price?ids=litecoin&vs_currencies=usd")
	if err != nil {
		return 0, fmt.Errorf("ltc price: %w", err)
	}
	defer resp2.Body.Close()

	var data struct {
		Litecoin struct {
			USD float64 `json:"usd"`
		} `json:"litecoin"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&data); err != nil {
		return 0, fmt.Errorf("ltc price decode: %w", err)
	}

	pc.mu.Lock()
	pc.ltcPrice = data.Litecoin.USD
	pc.updated = time.Now()
	pc.mu.Unlock()
	return data.Litecoin.USD, nil
}
```

### internal/crypto/id.go
```go
package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// NewID generates a cryptographically random ID with prefix.
func NewID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return prefix + "_" + hex.EncodeToString(b)
}

// WalletHash returns HMAC-SHA256(key, "naroom:v1:" + NormalizeAddress(address)).
// This is the canonical keyed hash for all wallet addresses stored in the database.
// Use this instead of Hash() for any wallet address — HMAC is the correct construction
// for keyed hashing and avoids length-extension attacks from plain SHA256 concatenation.
func WalletHash(key []byte, address string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("naroom:v1:"))
	mac.Write([]byte(NormalizeAddress(address)))
	return hex.EncodeToString(mac.Sum(nil))
}

// NormalizeAddress canonicalizes a wallet address before hashing.
// Bech32 addresses (bc1..., ltc1...) are lowercased — they are case-insensitive
// by spec. Legacy addresses (1..., 3..., L..., M...) are case-sensitive and
// returned unchanged. Surrounding whitespace is always trimmed.
func NormalizeAddress(addr string) string {
	addr = strings.TrimSpace(addr)
	lower := strings.ToLower(addr)
	if strings.HasPrefix(lower, "bc1") || strings.HasPrefix(lower, "ltc1") {
		return lower
	}
	return addr
}

// Hash returns SHA256 hex of inputs concatenated. Use for non-wallet, non-keyed
// hashing only (e.g. pair deduplication from already-hashed values, token hashing
// in middleware which is handled separately). For wallet addresses always use WalletHash.
func Hash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprint(h, p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// RandomToken generates a random token for reviews etc.
func RandomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
```

### internal/handler/handler.go
```go
package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"naroom/internal/crypto"
	"naroom/internal/telegram"
)

// Handler holds shared dependencies for all HTTP handlers.
type Handler struct {
	DB           *sql.DB
	HashKey      []byte // HMAC key for WalletHash — never log or expose
	WalletEncKey []byte // AES-256-GCM key for wallet_address_enc — never log or expose
	Mempool *crypto.MempoolClient
	Blockcypher *crypto.BlockcypherClient
	Prices      *crypto.PriceCache
	Wallet      *crypto.HDWallet
	DevMode     bool
	ListingTTL  int
	ChatTTL     int
	ChatMinTTL  int
	Hub         *ChatHub // for broadcasting room_closed to WS clients

	// Telegram notification bots. Nil when tokens are not configured.
	Telegram              telegram.Sender
	TelegramClientBotName string
	TelegramHelperBotName string
	TelegramWebhookSecret string
	PublicBaseURL         string
	RequireTelegram       bool // true when client bot token is configured
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}
```

### internal/handler/register.go
```go
package handler

import (
	"net/http"

	"naroom/internal/crypto"
)

type walletRegisterReq struct {
	WalletAddress string `json:"wallet_address"`
	Currency      string `json:"currency"`
	Role          string `json:"role"`
}

// WalletRegister handles POST /wallet/register.
// Checks that the address has ≥$1000 balance and issues a session token.
// No signature required — proof of ownership happens at payment time.
func (h *Handler) WalletRegister(w http.ResponseWriter, r *http.Request) {
	var req walletRegisterReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}
	if req.WalletAddress == "" || req.Currency == "" || req.Role == "" {
		writeError(w, 400, "wallet_address, currency, role required")
		return
	}
	if req.Role != "client" && req.Role != "peer" {
		writeError(w, 400, "role must be client or peer")
		return
	}
	if req.Currency != "BTC" && req.Currency != "LTC" {
		writeError(w, 400, "currency must be BTC or LTC")
		return
	}

	// ── Dev mode: skip balance check ─────────────────────────────────────────
	if h.DevMode {
		if err := h.upsertWalletSession(req.WalletAddress, req.Role, req.Currency); err != nil {
			writeError(w, 500, "db error")
			return
		}
		walletHash := crypto.WalletHash(h.HashKey, req.WalletAddress)
		token, err := h.issueSession(walletHash, req.Role, req.Currency)
		if err != nil {
			writeError(w, 500, "session creation failed")
			return
		}
		writeJSON(w, 200, map[string]any{
			"status":        "ok",
			"session_token": token,
			"expires_in":    86400,
		})
		return
	}

	// ── Check balance ─────────────────────────────────────────────────────────
	var minUSD float64
	switch req.Role {
	case "client":
		minUSD = 150.0
	default: // peer
		minUSD = 1000.0
	}

	balanceUSD, err := h.checkBalanceUSD(req.WalletAddress, req.Currency)
	if err != nil {
		writeError(w, 502, "balance check failed: "+err.Error())
		return
	}
	if balanceUSD < minUSD {
		writeJSON(w, 402, map[string]any{
			"error":       "insufficient balance",
			"balance_usd": balanceUSD,
			"required_usd": minUSD,
		})
		return
	}

	// ── Issue session ─────────────────────────────────────────────────────────
	if err := h.upsertWalletSession(req.WalletAddress, req.Role, req.Currency); err != nil {
		writeError(w, 500, "db error")
		return
	}
	walletHash := crypto.WalletHash(h.HashKey, req.WalletAddress)
	token, err := h.issueSession(walletHash, req.Role, req.Currency)
	if err != nil {
		writeError(w, 500, "session creation failed")
		return
	}
	writeJSON(w, 200, map[string]any{
		"status":        "ok",
		"balance_usd":   balanceUSD,
		"session_token": token,
		"expires_in":    86400,
	})
}

// checkBalanceUSD returns the USD value of the wallet balance.
func (h *Handler) checkBalanceUSD(address, currency string) (float64, error) {
	switch currency {
	case "BTC":
		satoshis, err := h.Mempool.GetBalance(address)
		if err != nil {
			return 0, err
		}
		btc := float64(satoshis) / 1e8
		price, err := h.Prices.BTCPrice()
		if err != nil {
			return 0, err
		}
		return btc * price, nil

	case "LTC":
		litoshis, err := h.Blockcypher.GetBalance(address)
		if err != nil {
			return 0, err
		}
		ltc := float64(litoshis) / 1e8
		price, err := h.Prices.LTCPrice()
		if err != nil {
			return 0, err
		}
		return ltc * price, nil
	}
	return 0, nil
}
```

### internal/handler/wallet.go
```go
package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"naroom/internal/crypto"
)

// issueSession creates a new session token, stores the hash, and returns the raw token.
// walletHash must be pre-computed with crypto.WalletHash — plain address is never stored in sessions.
func (h *Handler) issueSession(walletHash, role, currency string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hashBytes := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(hashBytes[:])

	now := time.Now().Unix()
	expiresAt := now + 86400 // 24h

	_, err := h.DB.Exec(`
		INSERT INTO sessions (token_hash, wallet_hash, currency, role, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, tokenHash, walletHash, currency, role, now, expiresAt)
	if err != nil {
		return "", err
	}
	return token, nil
}

func (h *Handler) upsertWalletSession(walletAddress, role, currency string) error {
	now := time.Now().Unix()

	var minRequired float64
	switch role {
	case "client":
		minRequired = 150.0
	default: // peer
		minRequired = 1000.0
	}

	walletHash := crypto.WalletHash(h.HashKey, walletAddress)

	// Encrypt the plain address before writing — plain address must never be stored in wallet_sessions.
	addrEnc, err := crypto.EncryptAddress(h.WalletEncKey, walletAddress)
	if err != nil {
		return fmt.Errorf("upsertWalletSession: encrypt: %w", err)
	}

	_, err = h.DB.Exec(`
		INSERT INTO wallet_sessions (wallet_hash, wallet_address_enc, currency, role, balance_status, min_required_usd, balance_usd, last_checked_at, verified, first_seen, created_at)
		VALUES (?, ?, ?, ?, 'ok', ?, ?, ?, TRUE, ?, ?)
		ON CONFLICT(wallet_hash) DO UPDATE SET
			wallet_address_enc = excluded.wallet_address_enc,
			currency           = excluded.currency,
			role               = excluded.role,
			balance_status     = 'ok',
			min_required_usd   = excluded.min_required_usd,
			last_checked_at    = excluded.last_checked_at,
			verified           = TRUE
	`, walletHash, addrEnc, currency, role, minRequired, minRequired, now, now, now, now)
	if err != nil {
		return err
	}

	// Ensure reputation entry exists for counselors
	if role == "peer" {
		counselorHash := crypto.WalletHash(h.HashKey, walletAddress)
		h.DB.Exec(`
			INSERT OR IGNORE INTO reputation (counselor_hash, region, first_seen)
			VALUES (?, '', ?)
		`, counselorHash, now)
	}
	return nil
}
```

### internal/handler/listing.go
```go
package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"naroom/internal/crypto"
	"naroom/internal/middleware"
)

type createListingReq struct {
	City               string   `json:"city"`
	DependencyType     string   `json:"dependency_type"`
	HelpType           string   `json:"help_type"`
	Urgency            string   `json:"urgency"`
	Languages          []string `json:"languages"`
	Currency           string   `json:"currency"` // BTC or LTC
}

var validCity = map[string]bool{
	"tbilisi": true, "batumi": true, "buenos_aires": true, "sao_paulo": true,
	"almaty": true, "yerevan": true, "moscow": true, "nha_trang": true, "da_nang": true,
}

var validLanguage = map[string]bool{
	"en": true, "ru": true, "ka": true, "es": true,
}

var validDependency = map[string]bool{
	"opioids": true, "stimulants": true, "alcohol": true, "cannabis": true,
	"benzodiazepines": true, "polysubstance": true, "gambling": true,
	"mephedrone": true, "cocaine": true,
}

var validHelp = map[string]bool{
	"crisis": true, "relapse_prevention": true,
	"motivation": true, "just_talk": true, "recovery_plan": true,
}

var validUrgency = map[string]bool{
	"can_wait": true, "soon": true, "urgent": true,
}

// GetListing handles GET /listing/{id} — returns listing details + response count.
func (h *Handler) GetListing(w http.ResponseWriter, r *http.Request) {
	listingID := chi.URLParam(r, "id")
	if listingID == "" {
		writeError(w, 400, "listing id required")
		return
	}

	now := time.Now().Unix()
	var id, city, depType, helpType, urgency, langsRaw string
	var visibleUntil, createdAt int64
	var status string
	var firstActivatedAt sql.NullInt64
	var renewalCount int
	var isSample bool

	err := h.DB.QueryRow(`
		SELECT id, city, dependency_type, help_type, urgency, languages,
		       visible_until, created_at, status,
		       first_activated_at, COALESCE(renewal_count, 0), is_sample
		FROM listings WHERE id = ?
	`, listingID).Scan(&id, &city, &depType, &helpType, &urgency,
		&langsRaw, &visibleUntil, &createdAt, &status,
		&firstActivatedAt, &renewalCount, &isSample)
	if err != nil {
		writeError(w, 404, "listing not found")
		return
	}

	var langs []string
	json.Unmarshal([]byte(langsRaw), &langs)

	var respCount int
	h.DB.QueryRow(`SELECT COUNT(*) FROM responses WHERE listing_id = ? AND status = 'pending'`,
		listingID).Scan(&respCount)

	timeLeft := visibleUntil - now
	if timeLeft < 0 {
		timeLeft = 0
	}

	// Renewal eligibility
	daysRemaining := 0
	canRenew := false
	if firstActivatedAt.Valid {
		daysElapsed := (now - firstActivatedAt.Int64) / 86400
		daysRemaining = 30 - int(daysElapsed)
		if daysRemaining < 0 {
			daysRemaining = 0
		}
		canRenew = daysRemaining > 0 && respCount < 2
	}

	writeJSON(w, 200, map[string]any{
		"id":                 id,
		"city":               city,
		"dependency_type":    depType,
		"help_type":          helpType,
		"urgency":            urgency,
		"languages":          langs,
		"visible_until":      visibleUntil,
		"created_at":         createdAt,
		"status":             status,
		"time_left":          timeLeft,
		"responses_count":    respCount,
		"renewal_count":      renewalCount,
		"days_remaining":     daysRemaining,
		"can_renew":          canRenew,
		"is_sample":          isSample,
	})
}

// GetListingResponses handles GET /listing/{id}/responses
// Returns pending responses only to the listing owner (identified by session).
func (h *Handler) GetListingResponses(w http.ResponseWriter, r *http.Request) {
	listingID := chi.URLParam(r, "id")
	walletHash := middleware.SessionWalletHash(r.Context())
	if listingID == "" || walletHash == "" {
		writeError(w, 400, "listing id required")
		return
	}

	// Verify ownership via hash
	var ownerHash string
	err := h.DB.QueryRow(`SELECT wallet_hash FROM listings WHERE id = ?`, listingID).Scan(&ownerHash)
	if err != nil {
		writeError(w, 404, "listing not found")
		return
	}
	if ownerHash != walletHash {
		writeError(w, 403, "not your listing")
		return
	}

	type peerReputation struct {
		SessionsCompleted int   `json:"sessions_completed"`
		ThumbsUp          int   `json:"thumbs_up"`
		ThumbsDown        int   `json:"thumbs_down"`
		BalanceTier       int   `json:"balance_tier"` // floor(balance_usd / 1000)
		MemberSince       int64 `json:"member_since"`  // unix timestamp of first_seen
		IsNew             bool  `json:"is_new"`        // true if < 5 completed sessions
	}
	type rawResponse struct {
		id             string
		counselorHash  string
		pubkey         string
		status         string
		createdAt      int64
	}
	type response struct {
		ID         string         `json:"id"`
		PeerPubkey string         `json:"peer_pubkey"`
		Status     string         `json:"status"`
		CreatedAt  int64          `json:"created_at"`
		Reputation peerReputation `json:"reputation"`
	}

	// Load all response rows first, then close the cursor before querying reputation.
	// Required because db.MaxOpenConns=1: holding rows open while calling QueryRow deadlocks.
	rows, err := h.DB.Query(`
		SELECT id, counselor_hash, counselor_pubkey, status, created_at
		FROM responses
		WHERE listing_id = ? AND status = 'pending'
		ORDER BY created_at ASC
	`, listingID)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	var raw []rawResponse
	for rows.Next() {
		var r rawResponse
		if err := rows.Scan(&r.id, &r.counselorHash, &r.pubkey, &r.status, &r.createdAt); err != nil {
			continue
		}
		raw = append(raw, r)
	}
	rows.Close() // release the connection before querying reputation

	var responses []response
	for _, r := range raw {
		var rep peerReputation
		var balanceUSD float64
		h.DB.QueryRow(`
			SELECT rep.sessions_completed, rep.thumbs_up, rep.thumbs_down, rep.first_seen, COALESCE(ws.balance_usd, 0)
			FROM reputation rep
			LEFT JOIN wallet_sessions ws ON ws.wallet_hash = ?
			WHERE rep.counselor_hash = ?
		`, r.counselorHash, r.counselorHash).Scan(
			&rep.SessionsCompleted, &rep.ThumbsUp, &rep.ThumbsDown, &rep.MemberSince, &balanceUSD,
		)
		rep.BalanceTier = int(balanceUSD / 1000)
		rep.IsNew = rep.SessionsCompleted < 5
		responses = append(responses, response{
			ID:         r.id,
			PeerPubkey: r.pubkey,
			Status:     r.status,
			CreatedAt:  r.createdAt,
			Reputation: rep,
		})
	}
	if responses == nil {
		responses = []response{}
	}
	writeJSON(w, 200, responses)
}

// GetListingChatRoom handles GET /listing/{id}/chatroom
// Returns active chat room for this listing if one exists (session identifies client).
func (h *Handler) GetListingChatRoom(w http.ResponseWriter, r *http.Request) {
	listingID := chi.URLParam(r, "id")
	walletHash := middleware.SessionWalletHash(r.Context())
	if listingID == "" || walletHash == "" {
		writeError(w, 400, "listing id required")
		return
	}

	var roomID, status string
	var expiresAt int64
	err := h.DB.QueryRow(`
		SELECT id, status, expires_at FROM chat_rooms
		WHERE listing_id = ? AND client_hash = ? AND status = 'active'
		ORDER BY started_at DESC LIMIT 1
	`, listingID, walletHash).Scan(&roomID, &status, &expiresAt)
	if err != nil {
		writeError(w, 404, "no active chat room")
		return
	}

	writeJSON(w, 200, map[string]any{
		"room_id":    roomID,
		"status":     status,
		"expires_at": expiresAt,
	})
}

// CreateListing handles POST /listing/create.
func (h *Handler) CreateListing(w http.ResponseWriter, r *http.Request) {
	var req createListingReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}

	walletHash := middleware.SessionWalletHash(r.Context())
	if walletHash == "" {
		writeError(w, 401, "session required")
		return
	}

	// Validate fields
	if req.City == "" || req.Currency == "" {
		writeError(w, 400, "city, currency required")
		return
	}
	if !validDependency[req.DependencyType] {
		writeError(w, 400, "invalid dependency_type")
		return
	}
	if !validHelp[req.HelpType] {
		writeError(w, 400, "invalid help_type")
		return
	}
	if !validUrgency[req.Urgency] {
		writeError(w, 400, "invalid urgency")
		return
	}
	if len(req.Languages) == 0 {
		writeError(w, 400, "at least one language required")
		return
	}
	if req.Currency != "BTC" && req.Currency != "LTC" {
		writeError(w, 400, "currency must be BTC or LTC")
		return
	}

	// Check wallet is verified and has sufficient balance
	if !h.DevMode {
		var balanceStatus string
		err := h.DB.QueryRow(`SELECT balance_status FROM wallet_sessions WHERE wallet_hash = ? AND role = 'client'`,
			walletHash).Scan(&balanceStatus)
		if err != nil {
			writeError(w, 403, "wallet not verified")
			return
		}
		if balanceStatus != "ok" {
			writeError(w, 403, "insufficient balance")
			return
		}
	}

	// Check no active listing from this wallet (excluding listings that already have a chat)
	var activeCount int
	h.DB.QueryRow(`
		SELECT COUNT(*) FROM listings
		WHERE wallet_hash = ? AND status IN ('active', 'pending')
		  AND NOT EXISTS (SELECT 1 FROM chat_rooms cr WHERE cr.listing_id = listings.id AND cr.status = 'active')
	`, walletHash).Scan(&activeCount)
	if activeCount > 0 {
		writeError(w, 409, "already have active listing")
		return
	}

	// Create invoice for $5
	listingID := crypto.NewID("lst")
	invoiceID := crypto.NewID("inv")

	var amountCrypto string
	var invoiceAddr string
	var priceAtCreation float64
	var err error

	if req.Currency == "BTC" {
		priceAtCreation, err = h.Prices.BTCPrice()
		if err != nil {
			writeError(w, 500, "price unavailable")
			return
		}
		amountCrypto = fmt.Sprintf("%.8f", 5.0/priceAtCreation)
		invoiceAddr, _, err = h.Wallet.NextBTCAddress()
		if err != nil {
			writeError(w, 500, "address generation failed")
			return
		}
	} else {
		priceAtCreation, err = h.Prices.LTCPrice()
		if err != nil {
			writeError(w, 500, "price unavailable")
			return
		}
		amountCrypto = fmt.Sprintf("%.8f", 5.0/priceAtCreation)
		invoiceAddr, _, err = h.Wallet.NextLTCAddress()
		if err != nil {
			writeError(w, 500, "address generation failed")
			return
		}
	}

	langsJSON, _ := json.Marshal(req.Languages)
	now := time.Now().Unix()

	// Look up encrypted wallet address BEFORE opening the transaction (MaxOpenConns=1 — can't query inside tx).
	// Decrypt → compute HMAC → store only the hash in the invoice. Plain address is discarded immediately.
	var addrEnc string
	h.DB.QueryRow(`SELECT wallet_address_enc FROM wallet_sessions WHERE wallet_hash = ?`, walletHash).Scan(&addrEnc)
	if addrEnc == "" {
		// Session token is valid but wallet session is missing or stale — ask user to re-register.
		writeError(w, 403, "wallet session invalid, re-register")
		return
	}
	var rawPayerAddress string
	rawPayerAddress, err = crypto.DecryptAddress(h.WalletEncKey, addrEnc)
	if err != nil {
		writeError(w, 500, "wallet decryption failed")
		return
	}
	payerHash := crypto.WalletHash(h.HashKey, rawPayerAddress)

	tx, err := h.DB.Begin()
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	defer tx.Rollback()

	// Insert listing (pending until payment confirmed) — store hash, not plain address
	_, err = tx.Exec(`
		INSERT INTO listings (id, city, dependency_type, help_type, urgency, languages,
		                      wallet_hash, visible_until, created_at, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending')
	`, listingID, req.City, req.DependencyType, req.HelpType, req.Urgency,
		string(langsJSON), walletHash, 0, now)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}

	// Insert invoice — payer_address column stores a hash, never plain address
	_, err = tx.Exec(`
		INSERT INTO invoices (id, type, address, amount_usd, amount_crypto, currency, listing_id, payer_address, price_at_creation, status, created_at)
		VALUES (?, 'listing', ?, 5.0, ?, ?, ?, ?, ?, 'pending', ?)
	`, invoiceID, invoiceAddr, amountCrypto, req.Currency, listingID, payerHash, priceAtCreation, now)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, 500, "db error")
		return
	}

	writeJSON(w, 201, map[string]any{
		"listing_id":    listingID,
		"invoice_id":    invoiceID,
		"address":       invoiceAddr,
		"amount_usd":    5.0,
		"amount_crypto": amountCrypto,
		"currency":      req.Currency,
	})
}
```

### internal/handler/accept.go
```go
package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"naroom/internal/crypto"
	"naroom/internal/middleware"
)

type acceptReq struct {
	ClientPubkey string `json:"client_pubkey"` // X25519 pubkey для E2E шифрования
	Currency     string `json:"currency"`      // BTC or LTC
}

// AcceptResponse handles POST /response/{id}/accept — client accepts a counselor.
// Client identity comes from the session; only X25519 pubkey and currency are in the body.
func (h *Handler) AcceptResponse(w http.ResponseWriter, r *http.Request) {
	responseID := chi.URLParam(r, "id")

	clientHash := middleware.SessionWalletHash(r.Context())
	if clientHash == "" {
		writeError(w, 401, "session required")
		return
	}

	var req acceptReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}

	if req.Currency == "" || req.ClientPubkey == "" {
		writeError(w, 400, "client_pubkey and currency required")
		return
	}
	if req.Currency != "BTC" && req.Currency != "LTC" {
		writeError(w, 400, "currency must be BTC or LTC")
		return
	}

	// Resolve prices and address before opening a transaction (external calls must be outside tx).
	var amountCrypto string
	var priceAtCreation float64
	var priceErr error
	if req.Currency == "BTC" {
		priceAtCreation, priceErr = h.Prices.BTCPrice()
		if priceErr == nil {
			amountCrypto = fmt.Sprintf("%.8f", 15.0/priceAtCreation)
		}
	} else {
		priceAtCreation, priceErr = h.Prices.LTCPrice()
		if priceErr == nil {
			amountCrypto = fmt.Sprintf("%.8f", 15.0/priceAtCreation)
		}
	}
	if priceErr != nil {
		writeError(w, 500, "price unavailable")
		return
	}

	var invoiceAddr string
	var addrErr error
	if req.Currency == "BTC" {
		invoiceAddr, _, addrErr = h.Wallet.NextBTCAddress()
	} else {
		invoiceAddr, _, addrErr = h.Wallet.NextLTCAddress()
	}
	if addrErr != nil {
		writeError(w, 500, "address generation failed")
		return
	}

	tx, err := h.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Atomically flip response to 'accepted' — this is the guard against double-accept.
	res, err := tx.Exec(`
		UPDATE responses SET status = 'accepted'
		WHERE id = ? AND status = 'pending'
	`, responseID)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, 404, "response not found or already processed")
		return
	}

	// Verify that the response belongs to the client's own listing.
	var listingID, counselorHash, counselorPubkey string
	err = tx.QueryRow(`
		SELECT r.listing_id, r.counselor_hash, r.counselor_pubkey
		FROM responses r
		JOIN listings l ON l.id = r.listing_id
		WHERE r.id = ? AND l.wallet_hash = ?
	`, responseID, clientHash).Scan(&listingID, &counselorHash, &counselorPubkey)
	if err != nil {
		writeError(w, 404, "response not found or not yours")
		return
	}

	// Guard: no other accepted response for this listing (TOCTOU protection)
	var alreadyAccepted int
	tx.QueryRow(`
		SELECT COUNT(*) FROM responses
		WHERE listing_id = ? AND id != ? AND status = 'accepted'
	`, listingID, responseID).Scan(&alreadyAccepted)
	if alreadyAccepted > 0 {
		writeError(w, 409, "another response already accepted for this listing")
		return
	}

	// Check no active chat for this client (peer_left counts — client must close it first).
	var activeChatCount int
	tx.QueryRow(`
		SELECT COUNT(*) FROM chat_rooms WHERE client_hash = ? AND status IN ('active', 'peer_left')
	`, clientHash).Scan(&activeChatCount)
	if activeChatCount > 0 {
		writeError(w, 409, "already have active chat")
		return
	}

	// Reject other pending responses for this listing.
	tx.Exec(`
		UPDATE responses SET status = 'rejected'
		WHERE listing_id = ? AND id != ? AND status = 'pending'
	`, listingID, responseID)

	// Create invoice ($15) for counselor.
	invoiceID := crypto.NewID("inv")
	now := time.Now().Unix()

	// Look up encrypted counselor address inside the tx (same connection, avoids MaxOpenConns=1 deadlock).
	// Decrypt → compute HMAC → store only the hash. Plain address is discarded immediately.
	var addrEnc string
	tx.QueryRow(`SELECT wallet_address_enc FROM wallet_sessions WHERE wallet_hash = ?`, counselorHash).Scan(&addrEnc)
	if addrEnc == "" {
		writeError(w, 403, "wallet session invalid, re-register")
		return
	}
	var rawCounselorAddress string
	rawCounselorAddress, err = crypto.DecryptAddress(h.WalletEncKey, addrEnc)
	if err != nil {
		writeError(w, 500, "wallet decryption failed")
		return
	}
	counselorPayerHash := crypto.WalletHash(h.HashKey, rawCounselorAddress)

	_, err = tx.Exec(`
		INSERT INTO invoices (id, type, address, amount_usd, amount_crypto, currency,
		                      response_id, client_pubkey, payer_address, price_at_creation, status, created_at)
		VALUES (?, 'chat', ?, 15.0, ?, ?, ?, ?, ?, ?, 'pending', ?)
	`, invoiceID, invoiceAddr, amountCrypto, req.Currency,
		responseID, req.ClientPubkey, counselorPayerHash, priceAtCreation, now)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}

	if err = tx.Commit(); err != nil {
		writeError(w, 500, "db error")
		return
	}

	writeJSON(w, 200, map[string]any{
		"invoice_id":    invoiceID,
		"address":       invoiceAddr,
		"amount_usd":    15.0,
		"amount_crypto": amountCrypto,
		"currency":      req.Currency,
		"message":       "counselor must pay $15 to open chat",
	})
}
```

### internal/handler/chat_ws.go
```go
package handler

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"naroom/internal/crypto"
	"naroom/internal/middleware"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// ChatHub manages active WebSocket connections per room.
type ChatHub struct {
	mu    sync.RWMutex
	rooms map[string]map[string]*wsConn // room_id → wallet_hash → conn
}

type wsConn struct {
	conn   *websocket.Conn
	cancel context.CancelFunc
}

func NewChatHub() *ChatHub {
	return &ChatHub{
		rooms: make(map[string]map[string]*wsConn),
	}
}

type wsMessage struct {
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
	MsgType    string `json:"msg_type"` // text | image_file | image_camera
}

type wsOutMessage struct {
	ID           string `json:"id"`
	SenderPubkey string `json:"sender_pubkey"`
	Nonce        string `json:"nonce"`
	Ciphertext   string `json:"ciphertext"`
	MsgType      string `json:"msg_type"`
	CreatedAt    int64  `json:"created_at"`
}

// ChatWS handles WS /chat/ws?room_id=xxx.
// Session token is passed via Sec-WebSocket-Protocol header (browser sends it when
// the second argument to `new WebSocket(url, [token])` is set).
// The server echoes back the accepted subprotocol so the browser's WebSocket handshake succeeds.
func (h *Handler) ChatWS(hub *ChatHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := r.URL.Query().Get("room_id")
		if roomID == "" {
			writeError(w, 400, "room_id required")
			return
		}

		// Resolve wallet identity (wallet_hash).
		// Priority 1: RequireSession middleware sets walletHash via Authorization header.
		// Priority 2: Token from Sec-WebSocket-Protocol header (browser WS API, can't send custom headers).
		walletHash := middleware.SessionWalletHash(r.Context())
		wsProtoToken := "" // set when auth was via Sec-WebSocket-Protocol; must be echoed back
		if walletHash == "" {
			rawToken := r.Header.Get("Sec-WebSocket-Protocol")
			if rawToken != "" {
				tokenHash := middleware.HashToken(rawToken)
				now := time.Now().Unix()
				h.DB.QueryRow(`
					SELECT wallet_hash FROM sessions
					WHERE token_hash = ? AND expires_at > ? AND revoked_at IS NULL
				`, tokenHash, now).Scan(&walletHash)
				if walletHash != "" {
					wsProtoToken = rawToken
				}
			}
		}
		if walletHash == "" {
			writeError(w, 401, "session required")
			return
		}

		// Determine pubkey from wallet identity via hash comparison
		var roomStatus string
		var clientPubkey, counselorPubkey, clientHash, counselorHash string
		err := h.DB.QueryRow(`
			SELECT status, client_pubkey, counselor_pubkey, client_hash, counselor_hash
			FROM chat_rooms WHERE id = ?
		`, roomID).Scan(&roomStatus, &clientPubkey, &counselorPubkey, &clientHash, &counselorHash)
		if err != nil {
			writeError(w, 404, "room not found")
			return
		}
		if roomStatus != "active" && roomStatus != "peer_left" {
			writeError(w, 410, "room closed")
			return
		}

		var pubkey string
		if walletHash == clientHash {
			pubkey = clientPubkey
		} else if walletHash == counselorHash {
			pubkey = counselorPubkey
		} else {
			writeError(w, 403, "not a participant")
			return
		}

		acceptOpts := &websocket.AcceptOptions{
			OriginPatterns: []string{"*"}, // tighten in production
		}
		// Echo back the accepted subprotocol — browser requires this for the handshake to succeed.
		if wsProtoToken != "" {
			acceptOpts.Subprotocols = []string{wsProtoToken}
		}
		conn, err := websocket.Accept(w, r, acceptOpts)
		if err != nil {
			return
		}
		conn.SetReadLimit(8 * 1024 * 1024) // 8MB — для изображений

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// Register connection keyed by wallet_hash (never stores plain address in memory map)
		hub.mu.Lock()
		if hub.rooms[roomID] == nil {
			hub.rooms[roomID] = make(map[string]*wsConn)
		}
		hub.rooms[roomID][walletHash] = &wsConn{conn: conn, cancel: cancel}
		hub.mu.Unlock()

		defer func() {
			hub.mu.Lock()
			delete(hub.rooms[roomID], walletHash)
			if len(hub.rooms[roomID]) == 0 {
				delete(hub.rooms, roomID)
			}
			hub.mu.Unlock()
			conn.Close(websocket.StatusNormalClosure, "")
		}()

		// Send history (messages still in DB)
		h.sendHistory(ctx, conn, roomID)

		// Heartbeat
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					conn.Ping(ctx)
				}
			}
		}()

		// Read loop
		for {
			var msg wsMessage
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				return
			}

			if msg.Nonce == "" || msg.Ciphertext == "" {
				continue
			}

			// Validate msg_type
			msgType := msg.MsgType
			if msgType != "text" && msgType != "image_file" && msgType != "image_camera" {
				msgType = "text"
			}

			now := time.Now().Unix()
			msgID := crypto.NewID("msg")

			// Save encrypted message
			h.DB.Exec(`
				INSERT INTO encrypted_messages (id, room_id, sender_pubkey, nonce, ciphertext, msg_type, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)
			`, msgID, roomID, pubkey, msg.Nonce, msg.Ciphertext, msgType, now)

			// Forward to other participant
			out := wsOutMessage{
				ID:           msgID,
				SenderPubkey: pubkey,
				Nonce:        msg.Nonce,
				Ciphertext:   msg.Ciphertext,
				MsgType:      msgType,
				CreatedAt:    now,
			}

			hub.mu.RLock()
			if room, ok := hub.rooms[roomID]; ok {
				for pk, wsc := range room {
					if pk != pubkey {
						wsjson.Write(ctx, wsc.conn, out)
					}
				}
			}
			hub.mu.RUnlock()
		}
	}
}

func (h *Handler) sendHistory(ctx context.Context, conn *websocket.Conn, roomID string) {
	rows, err := h.DB.Query(`
		SELECT id, sender_pubkey, nonce, ciphertext, msg_type, created_at
		FROM encrypted_messages
		WHERE room_id = ?
		ORDER BY created_at ASC
	`, roomID)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var msg wsOutMessage
		if err := rows.Scan(&msg.ID, &msg.SenderPubkey, &msg.Nonce, &msg.Ciphertext, &msg.MsgType, &msg.CreatedAt); err != nil {
			continue
		}
		if err := wsjson.Write(ctx, conn, msg); err != nil {
			return
		}
	}
}

// GetCounselorChatRoom handles GET /peer/chatroom?listing_id=Y
// Counselor polls this to know when client accepted and chat room opened.
// listing_id scopes the lookup to prevent stale rooms from previous sessions being returned.
func (h *Handler) GetCounselorChatRoom(w http.ResponseWriter, r *http.Request) {
	walletHash := middleware.SessionWalletHash(r.Context())
	listingID := r.URL.Query().Get("listing_id")
	if walletHash == "" || listingID == "" {
		writeError(w, 400, "listing_id required")
		return
	}

	var roomID, status string
	var expiresAt int64
	err := h.DB.QueryRow(`
		SELECT id, status, expires_at FROM chat_rooms
		WHERE counselor_hash = ? AND listing_id = ? AND status = 'active'
		ORDER BY started_at DESC LIMIT 1
	`, walletHash, listingID).Scan(&roomID, &status, &expiresAt)
	if err != nil {
		writeError(w, 404, "no active chat room")
		return
	}

	writeJSON(w, 200, map[string]any{
		"room_id":    roomID,
		"status":     status,
		"expires_at": expiresAt,
	})
}

// GetChatRoom handles GET /chat/{room_id} — returns room metadata for a participant.
// Participant identity resolved from session.
func (h *Handler) GetChatRoom(w http.ResponseWriter, r *http.Request) {
	roomID := chi.URLParam(r, "room_id")
	walletHash := middleware.SessionWalletHash(r.Context())
	if roomID == "" || walletHash == "" {
		writeError(w, 400, "room_id required")
		return
	}

	var status, clientPubkey, counselorPubkey, clientHash, counselorHash string
	var startedAt, expiresAt int64
	var peerLeftAt sql.NullInt64
	err := h.DB.QueryRow(`
		SELECT status, client_pubkey, counselor_pubkey, client_hash, counselor_hash, started_at, expires_at,
		       peer_left_at
		FROM chat_rooms WHERE id = ?
	`, roomID).Scan(&status, &clientPubkey, &counselorPubkey, &clientHash, &counselorHash, &startedAt, &expiresAt,
		&peerLeftAt)
	if err != nil {
		writeError(w, 404, "room not found")
		return
	}
	if walletHash != clientHash && walletHash != counselorHash {
		writeError(w, 403, "not a participant")
		return
	}

	role := "client"
	myPubkey := clientPubkey
	peerPubkey := counselorPubkey
	if walletHash == counselorHash {
		role = "peer"
		myPubkey = counselorPubkey
		peerPubkey = clientPubkey
	}

	resp := map[string]any{
		"room_id":     roomID,
		"status":      status,
		"role":        role,
		"my_pubkey":   myPubkey,
		"peer_pubkey": peerPubkey,
		"started_at":  startedAt,
		"expires_at":  expiresAt,
	}
	if peerLeftAt.Valid {
		resp["peer_left_at"] = peerLeftAt.Int64
	}
	writeJSON(w, 200, resp)
}

// wsSystemMsg is sent over WebSocket to notify participants of room state changes.
type wsSystemMsg struct {
	Type  string `json:"type"`  // always "system"
	Event string `json:"event"` // "peer_left" | "room_closed"
}

// broadcastSystem sends a system event to all WS connections in a room except the sender.
// senderKey is the wallet_hash of the sender (hub key).
func (hub *ChatHub) broadcastSystem(roomID, senderKey string, event wsSystemMsg) {
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	if room, ok := hub.rooms[roomID]; ok {
		for key, wsc := range room {
			if key != senderKey {
				wsjson.Write(context.Background(), wsc.conn, event)
			}
		}
	}
}

// CloseChat handles POST /chat/{room_id}/close.
//
// Rules:
//   - Peer closes   → room stays open (status stays 'active'), peer_left_at set.
//     Client receives WS "peer_left" event. Peer gets 200 {"status":"peer_left"}.
//   - Client closes → room closed permanently. Peer receives WS "room_closed".
//     Listing restored, review token issued if eligible.
func (h *Handler) CloseChat(w http.ResponseWriter, r *http.Request) {
	roomID := chi.URLParam(r, "room_id")
	if roomID == "" {
		writeError(w, 400, "room_id required")
		return
	}

	walletHash := middleware.SessionWalletHash(r.Context())
	if walletHash == "" {
		writeError(w, 401, "session required")
		return
	}

	var clientPubkey, counselorPubkey, clientHash, counselorHash, responseID string
	var startedAt int64
	var status string
	err := h.DB.QueryRow(`
		SELECT status, client_pubkey, counselor_pubkey, client_hash, counselor_hash, started_at, response_id
		FROM chat_rooms WHERE id = ?
	`, roomID).Scan(&status, &clientPubkey, &counselorPubkey, &clientHash, &counselorHash, &startedAt, &responseID)
	if err == sql.ErrNoRows {
		writeError(w, 404, "room not found")
		return
	}
	// Allow close if active or peer_left (client closing after peer left)
	if status != "active" && status != "peer_left" {
		writeError(w, 410, "room already closed")
		return
	}
	if walletHash != clientHash && walletHash != counselorHash {
		writeError(w, 403, "not a participant")
		return
	}

	now := time.Now().Unix()

	// ── Peer leaves ──────────────────────────────────────────────────────
	if walletHash == counselorHash {
		// Don't close the room — client must do it manually.
		_, err := h.DB.Exec(`
			UPDATE chat_rooms SET status = 'peer_left', peer_left_at = ? WHERE id = ?
		`, now, roomID)
		if err != nil {
			writeError(w, 500, "db error")
			return
		}
		// Notify client via WebSocket (hub keyed by wallet_hash)
		if h.Hub != nil {
			h.Hub.broadcastSystem(roomID, walletHash, wsSystemMsg{Type: "system", Event: "peer_left"})
		}
		writeJSON(w, 200, map[string]any{"status": "peer_left"})
		return
	}

	// ── Client closes ────────────────────────────────────────────────────
	tx, err := h.DB.Begin()
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	defer tx.Rollback()

	res, err := tx.Exec(`
		UPDATE chat_rooms SET status = 'closed', closed_at = ?, closed_by = 'client'
		WHERE id = ? AND status IN ('active', 'peer_left')
	`, now, roomID)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		tx.Rollback()
		writeJSON(w, 200, map[string]any{"status": "already_closed"})
		return
	}
	tx.Exec(`UPDATE responses SET status = 'closed' WHERE id = ?`, responseID)
	tx.Exec(`
		UPDATE listings SET status = 'active'
		WHERE id = (SELECT listing_id FROM chat_rooms WHERE id = ?)
		  AND status = 'matched' AND visible_until > ?
	`, roomID, now)
	chatDuration := now - startedAt
	minDuration := int64(6 * 3600)
	if h.DevMode {
		minDuration = 0
	}

	resp := map[string]any{"status": "closed"}

	if chatDuration >= minDuration {
		tx.Exec(`UPDATE reputation SET sessions_total = sessions_total + 1, sessions_completed = sessions_completed + 1 WHERE counselor_hash = ?`, counselorHash)
		token := crypto.RandomToken()
		tx.Exec(`
			INSERT INTO review_tokens (token, counselor_hash, is_paid, used, created_at, expires_at)
			VALUES (?, ?, TRUE, FALSE, ?, ?)
		`, token, counselorHash, now, now+86400)
		resp["review_token"] = token
	} else {
		tx.Exec(`UPDATE reputation SET sessions_total = sessions_total + 1, sessions_early_exit = sessions_early_exit + 1 WHERE counselor_hash = ?`, counselorHash)
		log.Printf("chat %s closed early by client after %ds", roomID, chatDuration)
	}

	if err := tx.Commit(); err != nil {
		writeError(w, 500, "db error")
		return
	}

	h.DB.Exec(`DELETE FROM encrypted_messages WHERE room_id = ?`, roomID)

	// Notify peer via WebSocket that session is over (hub keyed by wallet_hash)
	if h.Hub != nil {
		h.Hub.broadcastSystem(roomID, walletHash, wsSystemMsg{Type: "system", Event: "room_closed"})
	}

	writeJSON(w, 200, resp)
}
```

### internal/handler/respond.go
```go
package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"naroom/internal/crypto"
	"naroom/internal/middleware"
	"naroom/internal/telegram"
)

type respondReq struct {
	CounselorPubkey string `json:"peer_pubkey"`
}

// Respond handles POST /listing/{id}/respond — counselor responds to a listing.
// Counselor identity comes from the session; only X25519 pubkey is in the body.
func (h *Handler) Respond(w http.ResponseWriter, r *http.Request) {
	listingID := chi.URLParam(r, "id")
	if listingID == "" {
		writeError(w, 400, "listing id required")
		return
	}

	counselorHash := middleware.SessionWalletHash(r.Context())
	if counselorHash == "" {
		writeError(w, 401, "session required")
		return
	}

	// Clients may never respond to listings — enforce role regardless of DevMode.
	if role := middleware.SessionRole(r.Context()); role == "client" {
		writeError(w, 403, "clients cannot respond to listings")
		return
	}

	var req respondReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}

	if req.CounselorPubkey == "" {
		writeError(w, 400, "peer_pubkey required")
		return
	}

	if !h.DevMode {
		// Check wallet verified as peer
		var balanceStatus string
		err := h.DB.QueryRow(`SELECT balance_status FROM wallet_sessions WHERE wallet_hash = ? AND role = 'peer'`,
			counselorHash).Scan(&balanceStatus)
		if err != nil {
			writeError(w, 403, "wallet not verified as peer")
			return
		}
		if balanceStatus != "ok" {
			writeError(w, 403, "insufficient balance")
			return
		}

		// Check balance covers (activeResponses + 1) * $1000
		var activeResponses int
		h.DB.QueryRow(`
			SELECT COUNT(*) FROM responses
			WHERE counselor_hash = ? AND status IN ('pending', 'accepted')
		`, counselorHash).Scan(&activeResponses)

		var minRequired float64
		h.DB.QueryRow(`SELECT min_required_usd FROM wallet_sessions WHERE wallet_hash = ?`,
			counselorHash).Scan(&minRequired)

		needed := float64((activeResponses + 1) * 1000)
		if minRequired < needed {
			writeError(w, 403, "need higher balance for more response slots")
			return
		}
	}

	now := time.Now().Unix()

	// Serialise the listing-slot check + INSERT inside a transaction to prevent
	// two concurrent counselors both slipping in when pendingCount == 1.
	tx, err := h.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Check listing exists and is active — also fetch city for region lock check
	var listingStatus, listingCity string
	if err = tx.QueryRow(`SELECT status, city FROM listings WHERE id = ? AND status = 'active'`, listingID).Scan(&listingStatus, &listingCity); err != nil {
		writeError(w, 404, "listing not found or not active")
		return
	}

	// Region lock: first response permanently locks the peer to this city.
	// Atomic UPDATE (WHERE region = '') ensures only the first committing transaction sets the lock —
	// no separate SELECT-then-UPDATE race. We then read back the actual region to verify.
	res, err := tx.Exec(`UPDATE reputation SET region = ? WHERE counselor_hash = ? AND region = ''`,
		listingCity, counselorHash)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	var lockedRegion string
	if err = tx.QueryRow(`SELECT region FROM reputation WHERE counselor_hash = ?`, counselorHash).Scan(&lockedRegion); err != nil {
		// Row missing — reputation entry should always exist after wallet registration.
		writeError(w, 500, "reputation record missing")
		return
	}
	// If UPDATE affected 0 rows AND region is still empty, the row existed but was in an unexpected state.
	if n, _ := res.RowsAffected(); n == 0 && lockedRegion == "" {
		writeError(w, 500, "region lock failed")
		return
	}
	if lockedRegion != listingCity {
		writeJSON(w, 403, map[string]string{
			"error":         "region_locked",
			"locked_region": lockedRegion,
		})
		return
	}

	// Max 2 pending responses per listing
	var pendingCount int
	tx.QueryRow(`SELECT COUNT(*) FROM responses WHERE listing_id = ? AND status = 'pending'`, listingID).Scan(&pendingCount)
	if pendingCount >= 2 {
		writeError(w, 409, "listing already has maximum responses")
		return
	}

	// Check cooldown on this listing
	var cooldownCount int
	tx.QueryRow(`
		SELECT COUNT(*) FROM responses
		WHERE counselor_hash = ? AND listing_id = ? AND cooldown_until > ?
	`, counselorHash, listingID, now).Scan(&cooldownCount)
	if cooldownCount > 0 {
		writeError(w, 429, "cooldown active for this listing")
		return
	}

	// Check not already responded to this listing
	var existingCount int
	tx.QueryRow(`
		SELECT COUNT(*) FROM responses
		WHERE counselor_hash = ? AND listing_id = ? AND status = 'pending'
	`, counselorHash, listingID).Scan(&existingCount)
	if existingCount > 0 {
		writeError(w, 409, "already responded to this listing")
		return
	}

	responseID := crypto.NewID("rsp")
	_, err = tx.Exec(`
		INSERT INTO responses (id, listing_id, counselor_hash, counselor_pubkey, status, created_at)
		VALUES (?, ?, ?, ?, 'pending', ?)
	`, responseID, listingID, counselorHash, req.CounselorPubkey, now)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}

	if err = tx.Commit(); err != nil {
		writeError(w, 500, "db error")
		return
	}

	// Notify the client via Telegram that someone replied (fire-and-forget)
	go telegram.NotifyClientReply(context.Background(), h.DB, h.Telegram, listingID)

	writeJSON(w, 201, map[string]string{
		"response_id": responseID,
		"status":      "pending",
	})
}

// GetPeerRegion handles GET /peer/region — returns the peer's locked city (or null if first time).
func (h *Handler) GetPeerRegion(w http.ResponseWriter, r *http.Request) {
	counselorHash := middleware.SessionWalletHash(r.Context())
	if counselorHash == "" {
		writeError(w, 401, "session required")
		return
	}

	var region string
	h.DB.QueryRow(`SELECT region FROM reputation WHERE counselor_hash = ?`, counselorHash).Scan(&region)

	if region == "" {
		writeJSON(w, 200, map[string]any{"region": nil})
	} else {
		writeJSON(w, 200, map[string]any{"region": region})
	}
}

// CancelResponse handles POST /response/{id}/cancel — counselor cancels pending response.
func (h *Handler) CancelResponse(w http.ResponseWriter, r *http.Request) {
	responseID := chi.URLParam(r, "id")

	counselorHash := middleware.SessionWalletHash(r.Context())
	if counselorHash == "" {
		writeError(w, 401, "session required")
		return
	}

	// Check response belongs to this counselor and is pending
	var listingID string
	err := h.DB.QueryRow(`
		SELECT listing_id FROM responses
		WHERE id = ? AND counselor_hash = ? AND status = 'pending'
	`, responseID, counselorHash).Scan(&listingID)
	if err != nil {
		writeError(w, 404, "response not found or not cancellable")
		return
	}

	now := time.Now().Unix()
	cooldownUntil := now + 1800 // 30 min

	_, err = h.DB.Exec(`
		UPDATE responses SET status = 'cancelled', cancelled_at = ?, cooldown_until = ?
		WHERE id = ?
	`, now, cooldownUntil, responseID)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}

	writeJSON(w, 200, map[string]any{
		"status":         "cancelled",
		"cooldown_until": cooldownUntil,
	})
}
```

### internal/handler/invoice.go
```go
package handler

import (
	"database/sql"
	"net/http"

	"github.com/go-chi/chi/v5"

	"naroom/internal/middleware"
)

// InvoiceStatus handles GET /invoice/{id}/status.
// Requires session — only the listing owner can poll their invoice.
func (h *Handler) InvoiceStatus(w http.ResponseWriter, r *http.Request) {
	invoiceID := chi.URLParam(r, "id")
	if invoiceID == "" {
		writeError(w, 400, "invoice id required")
		return
	}

	walletHash := middleware.SessionWalletHash(r.Context())
	if walletHash == "" {
		writeError(w, 401, "session required")
		return
	}

	var status, address, amountCrypto, currency string
	var amountUSD float64
	var listingID sql.NullString
	err := h.DB.QueryRow(`
		SELECT status, address, amount_usd, amount_crypto, currency, listing_id
		FROM invoices WHERE id = ?
	`, invoiceID).Scan(&status, &address, &amountUSD, &amountCrypto, &currency, &listingID)
	if err != nil {
		writeError(w, 404, "invoice not found")
		return
	}

	// Verify session wallet owns the invoice's listing (compare by hash).
	if listingID.Valid {
		var ownerHash string
		err = h.DB.QueryRow(`SELECT wallet_hash FROM listings WHERE id = ?`, listingID.String).Scan(&ownerHash)
		if err != nil || ownerHash != walletHash {
			writeError(w, 403, "not your invoice")
			return
		}
	}

	writeJSON(w, 200, map[string]any{
		"invoice_id":    invoiceID,
		"status":        status,
		"address":       address,
		"amount_usd":    amountUSD,
		"amount_crypto": amountCrypto,
		"currency":      currency,
	})
}
```

### internal/handler/board.go
```go
package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"naroom/internal/model"
)

// Board returns active listings for a city.
// GET /board/{city}
func (h *Handler) Board(w http.ResponseWriter, r *http.Request) {
	city := chi.URLParam(r, "city")
	if city == "" {
		writeError(w, 400, "city required")
		return
	}

	now := time.Now().Unix()
	rows, err := h.DB.Query(`
		SELECT l.id, l.city, l.dependency_type, l.help_type, l.urgency,
		       l.languages, l.visible_until, l.created_at,
		       (SELECT COUNT(*) FROM responses r WHERE r.listing_id = l.id AND r.status = 'pending') as resp_count,
		       l.is_sample
		FROM listings l
		WHERE l.city = ? AND l.status = 'active' AND l.visible_until > ?
		  AND NOT EXISTS (SELECT 1 FROM chat_rooms cr WHERE cr.listing_id = l.id AND cr.status = 'active')
		ORDER BY l.is_sample ASC, l.created_at DESC
		LIMIT 50
	`, city, now)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	defer rows.Close()

	listings := []model.Listing{}
	for rows.Next() {
		var l model.Listing
		var langs string
		var isSample int
		if err := rows.Scan(&l.ID, &l.City, &l.DependencyType, &l.HelpType,
			&l.Urgency, &langs, &l.VisibleUntil, &l.CreatedAt, &l.ResponsesCount, &isSample); err != nil {
			continue
		}
		l.Status = "active"
		l.IsSample = isSample == 1
		json.Unmarshal([]byte(langs), &l.Languages)
		l.TimeLeft = l.VisibleUntil - now
		if l.TimeLeft < 0 {
			l.TimeLeft = 0
		}
		listings = append(listings, l)
	}

	writeJSON(w, 200, listings)
}
```

### internal/handler/session.go
```go
package handler

import (
	"net/http"
	"strings"
	"time"

	"naroom/internal/middleware"
)

// SessionRefresh handles POST /session/refresh — rotates the session token.
// Returns a new token; the old one is revoked.
func (h *Handler) SessionRefresh(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		writeError(w, 401, "authorization required")
		return
	}
	rawToken := strings.TrimPrefix(authHeader, "Bearer ")
	oldHash := middleware.HashToken(rawToken)

	now := time.Now().Unix()

	var walletHash, role, currency string
	err := h.DB.QueryRow(`
		SELECT wallet_hash, role, currency FROM sessions
		WHERE token_hash = ? AND expires_at > ? AND revoked_at IS NULL
	`, oldHash, now).Scan(&walletHash, &role, &currency)
	if err != nil {
		writeError(w, 401, "invalid or expired session")
		return
	}

	// Issue new token
	newToken, err := h.issueSession(walletHash, role, currency)
	if err != nil {
		writeError(w, 500, "session creation failed")
		return
	}

	// Revoke old token
	h.DB.Exec(`UPDATE sessions SET revoked_at = ? WHERE token_hash = ?`, now, oldHash)

	writeJSON(w, 200, map[string]any{
		"token":      newToken,
		"expires_at": now + 86400,
	})
}

// SessionRevoke handles POST /session/revoke — invalidates the current session.
func (h *Handler) SessionRevoke(w http.ResponseWriter, r *http.Request) {
	wallet := middleware.SessionWalletHash(r.Context())
	if wallet == "" {
		writeError(w, 401, "authorization required")
		return
	}
	authHeader := r.Header.Get("Authorization")
	rawToken := strings.TrimPrefix(authHeader, "Bearer ")
	tokenHash := middleware.HashToken(rawToken)
	h.DB.Exec(`UPDATE sessions SET revoked_at = ? WHERE token_hash = ?`, time.Now().Unix(), tokenHash)
	writeJSON(w, 200, map[string]string{"status": "revoked"})
}
```

### internal/handler/review.go
```go
package handler

import (
	"net/http"
	"time"
)

type reviewReq struct {
	Token  string `json:"token"`
	Rating string `json:"rating"` // "up" or "down"
}

// Review handles POST /review — anonymous thumbs up/down.
// No auth, no wallet, just a one-time token.
func (h *Handler) Review(w http.ResponseWriter, r *http.Request) {
	var req reviewReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}

	if req.Token == "" {
		writeError(w, 400, "token required")
		return
	}
	if req.Rating != "up" && req.Rating != "down" {
		writeError(w, 400, "rating must be up or down")
		return
	}

	now := time.Now().Unix()

	// Find token, check it's valid
	var counselorHash string
	var used bool
	var expiresAt int64
	err := h.DB.QueryRow(`
		SELECT counselor_hash, used, expires_at FROM review_tokens WHERE token = ?
	`, req.Token).Scan(&counselorHash, &used, &expiresAt)
	if err != nil {
		writeError(w, 404, "invalid token")
		return
	}
	if used {
		writeError(w, 409, "token already used")
		return
	}
	if expiresAt < now {
		writeError(w, 410, "token expired")
		return
	}

	tx, err := h.DB.Begin()
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	defer tx.Rollback()

	// Increment reputation counter
	if req.Rating == "up" {
		tx.Exec(`UPDATE reputation SET thumbs_up = thumbs_up + 1 WHERE counselor_hash = ?`, counselorHash)
	} else {
		tx.Exec(`UPDATE reputation SET thumbs_down = thumbs_down + 1 WHERE counselor_hash = ?`, counselorHash)
	}

	// Delete token forever
	tx.Exec(`DELETE FROM review_tokens WHERE token = ?`, req.Token)

	if err := tx.Commit(); err != nil {
		writeError(w, 500, "db error")
		return
	}

	writeJSON(w, 200, map[string]string{"status": "recorded"})
}
```

### internal/handler/renew.go
```go
package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"naroom/internal/middleware"
)

// RenewListing handles POST /listing/{id}/renew.
// Renewal is FREE until the listing has 2 responses — clients already paid $5 upfront.
// Once 2 pending responses exist the client must choose a peer instead of renewing.
func (h *Handler) RenewListing(w http.ResponseWriter, r *http.Request) {
	listingID := chi.URLParam(r, "id")

	walletHash := middleware.SessionWalletHash(r.Context())
	if walletHash == "" {
		writeError(w, 401, "session required")
		return
	}

	// Load listing and verify ownership via hash
	var ownerHash, status string
	var firstActivatedAt int64
	var renewalCount int
	err := h.DB.QueryRow(`
		SELECT wallet_hash, status, COALESCE(first_activated_at, created_at), COALESCE(renewal_count, 0)
		FROM listings WHERE id = ? AND is_sample = 0
	`, listingID).Scan(&ownerHash, &status, &firstActivatedAt, &renewalCount)
	if err != nil {
		writeError(w, 404, "listing not found")
		return
	}
	if ownerHash != walletHash {
		writeError(w, 403, "not your listing")
		return
	}
	if status != "active" && status != "expired" {
		writeError(w, 409, "listing cannot be renewed (status: "+status+")")
		return
	}

	now := time.Now().Unix()

	// Block renewal if already has 2 responses — client must choose a peer
	var pendingCount int
	h.DB.QueryRow(`SELECT COUNT(*) FROM responses WHERE listing_id = ? AND status = 'pending'`, listingID).Scan(&pendingCount)
	if pendingCount >= 2 {
		writeError(w, 409, "listing has 2 responses — please choose a peer instead of renewing")
		return
	}

	// Free renewal: extend listing and Telegram notification by ListingTTL (6h)
	ttl := int64(h.ListingTTL)
	if ttl == 0 {
		ttl = 21600
	}
	newExpiry := now + ttl

	_, err = h.DB.Exec(`
		UPDATE listings
		SET status = 'active', visible_until = ?,
		    renewal_count = COALESCE(renewal_count, 0) + 1
		WHERE id = ?
	`, newExpiry, listingID)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}

	// Extend Telegram notification to match new expiry
	h.DB.Exec(`
		UPDATE client_listing_notifications
		SET expires_at = ?
		WHERE listing_id = ? AND active = TRUE
	`, newExpiry, listingID)

	writeJSON(w, 200, map[string]any{
		"status":        "renewed",
		"free":          true,
		"renewal_count": renewalCount + 1,
		"visible_until": newExpiry,
	})
}
```

### internal/handler/balance.go
```go
package handler

import (
	"net/http"

	"naroom/internal/crypto"
)

// BalanceStatus handles GET /api/balance-status?wallet=xxx.
// Looks up by HMAC hash — plain address is never stored.
func (h *Handler) BalanceStatus(w http.ResponseWriter, r *http.Request) {
	wallet := r.URL.Query().Get("wallet")
	if wallet == "" {
		writeError(w, 400, "wallet parameter required")
		return
	}

	walletHash := crypto.WalletHash(h.HashKey, wallet)

	var status, role string
	var minRequired float64
	var lastChecked *int64

	err := h.DB.QueryRow(`
		SELECT balance_status, role, min_required_usd, last_checked_at
		FROM wallet_sessions WHERE wallet_hash = ?
	`, walletHash).Scan(&status, &role, &minRequired, &lastChecked)
	if err != nil {
		writeError(w, 404, "wallet not found")
		return
	}

	writeJSON(w, 200, map[string]any{
		"status":           status,
		"role":             role,
		"min_required_usd": minRequired,
		"last_checked_at":  lastChecked,
	})
}
```

### internal/db/db.go
```go
package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"log"

	ncrypto "naroom/internal/crypto"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Open opens SQLite database and runs DDL migrations.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_foreign_keys=ON&_synchronous=NORMAL", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	db.SetMaxOpenConns(1) // SQLite — один writer
	db.SetMaxIdleConns(1)

	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("run schema: %w", err)
	}

	// Column additions for existing databases (idempotent — errors are ignored)
	db.Exec(`ALTER TABLE encrypted_messages ADD COLUMN msg_type TEXT NOT NULL DEFAULT 'text'`)
	db.Exec(`ALTER TABLE chat_rooms ADD COLUMN peer_left_at INTEGER`)
	db.Exec(`ALTER TABLE invoices ADD COLUMN payment_detected_at INTEGER`)
	db.Exec(`ALTER TABLE invoices ADD COLUMN price_at_creation REAL`)

	// Schema cleanup migrations (must not silently fail if column/table is present)
	// wallet_challenges stored plain wallet_address and was never used by any handler.
	// Dropping it eliminates the plain-text address exposure entirely.
	db.Exec(`DROP TABLE IF EXISTS wallet_challenges`)
	db.Exec(`DROP INDEX IF EXISTS idx_wallet_challenges_wallet`)

	// reconnection_hashes was a stub feature never read by any handler or frontend.
	// ALTER TABLE … DROP COLUMN IF EXISTS is not valid SQLite syntax — check first.
	if columnExists(db, "listings", "reconnection_hashes") {
		if _, err := db.Exec(`ALTER TABLE listings DROP COLUMN reconnection_hashes`); err != nil {
			db.Close()
			return nil, fmt.Errorf("migration: drop listings.reconnection_hashes: %w", err)
		}
	}

	// wallet_hash was mistakenly added to Telegram tables — it links Telegram identity
	// to wallet_hash, which violates the privacy model. Remove if present.
	if columnExists(db, "helper_board_subscriptions", "wallet_hash") {
		if _, err := db.Exec(`ALTER TABLE helper_board_subscriptions DROP COLUMN wallet_hash`); err != nil {
			db.Close()
			return nil, fmt.Errorf("migration: drop helper_board_subscriptions.wallet_hash: %w", err)
		}
	}
	if columnExists(db, "telegram_link_tokens", "wallet_hash") {
		if _, err := db.Exec(`ALTER TABLE telegram_link_tokens DROP COLUMN wallet_hash`); err != nil {
			db.Close()
			return nil, fmt.Errorf("migration: drop telegram_link_tokens.wallet_hash: %w", err)
		}
	}

	return db, nil
}

// columnExists reports whether table t has a column named col.
func columnExists(db *sql.DB, table, col string) bool {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ, notnull string
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			continue
		}
		if name == col {
			return true
		}
	}
	return false
}

// MigrateWalletEncryption detects whether wallet_sessions still uses the old schema
// (plain wallet_address as PRIMARY KEY) and if so, migrates to the new schema:
// wallet_hash as PK + AES-256-GCM encrypted wallet_address_enc.
//
// Safe to call on already-migrated databases (no-op).
// Must be called after Open() and after the encryption key is available.
func MigrateWalletEncryption(db *sql.DB, encKey []byte) error {
	// Check whether wallet_sessions still has the old plain-text wallet_address column.
	rows, err := db.Query(`PRAGMA table_info(wallet_sessions)`)
	if err != nil {
		return fmt.Errorf("wallet migration: pragma table_info: %w", err)
	}
	hasOldAddressCol := false
	hasEncCol := false
	for rows.Next() {
		var cid int
		var name, typ, notnull string
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			continue
		}
		if name == "wallet_address" {
			hasOldAddressCol = true
		}
		if name == "wallet_address_enc" {
			hasEncCol = true
		}
	}
	rows.Close()

	if !hasOldAddressCol || hasEncCol {
		// Already migrated or new DB — nothing to do.
		return nil
	}

	log.Println("db: migrating wallet_sessions to encrypted schema...")

	// Read all existing rows before any DDL.
	type oldRow struct {
		address     string
		walletHash  string
		role        string
		status      string
		minRequired float64
		balanceUSD  float64
		lastChecked sql.NullInt64
		lowSince    sql.NullInt64
		verified    bool
		firstSeen   int64
		createdAt   int64
	}
	r, err := db.Query(`SELECT wallet_address, COALESCE(wallet_hash,''), role, balance_status, min_required_usd,
		COALESCE(balance_usd,0), last_checked_at, low_since, verified, first_seen, created_at
		FROM wallet_sessions`)
	if err != nil {
		return fmt.Errorf("wallet migration: read old rows: %w", err)
	}
	var oldRows []oldRow
	for r.Next() {
		var row oldRow
		if err := r.Scan(&row.address, &row.walletHash, &row.role, &row.status,
			&row.minRequired, &row.balanceUSD, &row.lastChecked, &row.lowSince,
			&row.verified, &row.firstSeen, &row.createdAt); err != nil {
			continue
		}
		oldRows = append(oldRows, row)
	}
	r.Close()

	// Encrypt all addresses before touching the schema.
	type newRow struct {
		oldRow
		enc      string
		currency string
	}
	var newRows []newRow
	for _, row := range oldRows {
		enc, err := ncrypto.EncryptAddress(encKey, row.address)
		if err != nil {
			return fmt.Errorf("wallet migration: encrypt %s: %w", row.address[:min(8, len(row.address))], err)
		}
		cur := "BTC"
		if !ncrypto.IsLikelyBTC(row.address) {
			cur = "LTC"
		}
		newRows = append(newRows, newRow{oldRow: row, enc: enc, currency: cur})
	}

	// Execute table rebuild inside a single transaction.
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("wallet migration: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err = tx.Exec(`DROP TABLE IF EXISTS wallet_sessions_new`); err != nil {
		return fmt.Errorf("wallet migration: drop new: %w", err)
	}
	if _, err = tx.Exec(`
		CREATE TABLE wallet_sessions_new (
			wallet_hash        TEXT PRIMARY KEY,
			wallet_address_enc TEXT NOT NULL,
			currency           TEXT NOT NULL DEFAULT 'BTC',
			role               TEXT NOT NULL,
			balance_status     TEXT DEFAULT 'ok',
			min_required_usd   REAL NOT NULL,
			balance_usd        REAL DEFAULT 0,
			last_checked_at    INTEGER,
			low_since          INTEGER,
			verified           BOOLEAN DEFAULT FALSE,
			first_seen         INTEGER NOT NULL,
			created_at         INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("wallet migration: create new table: %w", err)
	}

	for _, row := range newRows {
		if _, err = tx.Exec(`
			INSERT INTO wallet_sessions_new
			(wallet_hash, wallet_address_enc, currency, role, balance_status, min_required_usd, balance_usd,
			 last_checked_at, low_since, verified, first_seen, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, row.walletHash, row.enc, row.currency, row.role, row.status,
			row.minRequired, row.balanceUSD, row.lastChecked, row.lowSince,
			row.verified, row.firstSeen, row.createdAt); err != nil {
			return fmt.Errorf("wallet migration: insert row: %w", err)
		}
	}

	if _, err = tx.Exec(`DROP TABLE wallet_sessions`); err != nil {
		return fmt.Errorf("wallet migration: drop old: %w", err)
	}
	if _, err = tx.Exec(`ALTER TABLE wallet_sessions_new RENAME TO wallet_sessions`); err != nil {
		return fmt.Errorf("wallet migration: rename: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("wallet migration: commit: %w", err)
	}

	// Recreate indexes (dropped with old table).
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_wallet_sessions_hash ON wallet_sessions(wallet_hash)`)

	log.Printf("db: wallet_sessions migrated: %d rows encrypted", len(newRows))
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

### internal/db/seed.go
```go
package db

import (
	"database/sql"
	"log"
	"time"
)

// SeedSamples inserts demo listings for each city if none exist yet.
// These are marked is_sample=1 so the UI can show a "Sample" badge.
// They never expire (visible_until = far future) and can't be responded to.
func SeedSamples(db *sql.DB) {
	type sample struct {
		city       string
		dep        string
		help       string
		urgency    string
		langs      string
	}

	samples := []sample{
		// Tbilisi
		{"tbilisi", "alcohol", "relapse_prevention", "soon", `["en","ru","ka"]`},
		{"tbilisi", "gambling", "just_talk", "can_wait", `["en","ru"]`},
		// Batumi
		{"batumi", "alcohol", "just_talk", "soon", `["en","ru","ka"]`},
		{"batumi", "cannabis", "motivation", "can_wait", `["ru","ka"]`},
		// Nha Trang
		{"nha_trang", "opioids", "crisis", "urgent", `["en"]`},
		{"nha_trang", "cannabis", "motivation", "can_wait", `["en"]`},
		// Da Nang
		{"da_nang", "alcohol", "just_talk", "soon", `["en"]`},
		{"da_nang", "stimulants", "relapse_prevention", "can_wait", `["en"]`},
		// Buenos Aires
		{"buenos_aires", "stimulants", "crisis", "urgent", `["en","es"]`},
		{"buenos_aires", "alcohol", "recovery_plan", "soon", `["es"]`},
		// Sao Paulo
		{"sao_paulo", "polysubstance", "just_talk", "soon", `["en","es"]`},
		{"sao_paulo", "gambling", "relapse_prevention", "can_wait", `["es"]`},
		// Almaty
		{"almaty", "opioids", "recovery_plan", "soon", `["ru"]`},
		{"almaty", "alcohol", "motivation", "can_wait", `["ru","en"]`},
		// Yerevan
		{"yerevan", "alcohol", "just_talk", "can_wait", `["ru","en"]`},
		{"yerevan", "cannabis", "relapse_prevention", "soon", `["ru"]`},
		// Moscow
		{"moscow", "alcohol", "crisis", "urgent", `["ru"]`},
		{"moscow", "opioids", "just_talk", "soon", `["ru","en"]`},
	}

	farFuture := time.Now().Add(365 * 24 * time.Hour).Unix()
	now := time.Now().Unix()

	inserted := 0
	for _, s := range samples {
		// Check if sample already exists for this city+dep+help
		var count int
		db.QueryRow(`SELECT COUNT(*) FROM listings WHERE city=? AND dependency_type=? AND help_type=? AND is_sample=1`,
			s.city, s.dep, s.help).Scan(&count)
		if count > 0 {
			continue
		}

		id := "sample_" + s.city + "_" + s.dep + "_" + s.help
		_, err := db.Exec(`
			INSERT OR IGNORE INTO listings
			  (id, city, dependency_type, help_type, urgency, languages,
			   wallet_hash, visible_until, created_at, status, is_sample)
			VALUES (?, ?, ?, ?, ?, ?, '_sample', ?, ?, 'active', 1)
		`, id, s.city, s.dep, s.help, s.urgency, s.langs, farFuture, now)
		if err != nil {
			log.Printf("seed: %v", err)
		} else {
			inserted++
		}
	}

	if inserted > 0 {
		log.Printf("seed: inserted %d sample listings", inserted)
	}
}
```

### internal/middleware/session.go
```go
package middleware

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	ncrypto "naroom/internal/crypto"
)

type contextKey int

const (
	ctxWalletHash contextKey = iota
	ctxWalletRole
)

// SessionWalletHash returns the HMAC wallet hash stored in ctx after session validation, or "".
// Handlers use this directly as the identity key for all DB queries — no plain address needed.
func SessionWalletHash(ctx context.Context) string {
	v, _ := ctx.Value(ctxWalletHash).(string)
	return v
}

// SessionRole returns the role ("client" or "peer") stored in ctx, or "".
func SessionRole(ctx context.Context) string {
	v, _ := ctx.Value(ctxWalletRole).(string)
	return v
}

// HashToken returns the SHA-256 hex digest of a raw session token.
func HashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// RequireSession is middleware that validates the Bearer token in the Authorization header.
// On success it stores wallet_hash and role in the request context.
// Skipped when devMode is true and the Authorization header is absent — the wallet address
// from X-Dev-Wallet is then hashed with hashKey and stored as wallet_hash (dev only).
func RequireSession(db *sql.DB, devMode bool, hashKey []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")

			// ── Dev mode shortcut ────────────────────────────────────────────
			if devMode && authHeader == "" {
				wallet := r.Header.Get("X-Dev-Wallet")
				role := r.Header.Get("X-Dev-Role")
				if wallet != "" && role != "" {
					walletHash := ncrypto.WalletHash(hashKey, wallet)
					ctx := context.WithValue(r.Context(), ctxWalletHash, walletHash)
					ctx = context.WithValue(ctx, ctxWalletRole, role)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				// Fall through to normal auth — still require a token in dev mode
				// if neither header is set (e.g., direct API calls).
			}

			// ── Parse Bearer token ────────────────────────────────────────────
			if !strings.HasPrefix(authHeader, "Bearer ") {
				http.Error(w, `{"error":"authorization required"}`, http.StatusUnauthorized)
				return
			}
			rawToken := strings.TrimPrefix(authHeader, "Bearer ")
			if rawToken == "" {
				http.Error(w, `{"error":"empty token"}`, http.StatusUnauthorized)
				return
			}

			tokenHash := HashToken(rawToken)
			now := time.Now().Unix()

			var walletHash, role string
			err := db.QueryRow(`
				SELECT wallet_hash, role FROM sessions
				WHERE token_hash = ? AND expires_at > ? AND revoked_at IS NULL
			`, tokenHash, now).Scan(&walletHash, &role)
			if err != nil {
				http.Error(w, `{"error":"invalid or expired session"}`, http.StatusUnauthorized)
				return
			}

			// Update last_seen_at asynchronously (non-critical)
			go db.Exec(`UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?`, now, tokenHash)

			ctx := context.WithValue(r.Context(), ctxWalletHash, walletHash)
			ctx = context.WithValue(ctx, ctxWalletRole, role)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
```

### internal/middleware/ratelimit.go
```go
package middleware

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// entry holds a rate limiter and the last time it was accessed.
type entry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimiter holds per-key token-bucket limiters with periodic cleanup.
type RateLimiter struct {
	mu      sync.Mutex
	entries map[string]*entry
	r       rate.Limit // tokens per second
	burst   int
}

// NewRateLimiter creates a limiter. r is events/second, burst is bucket size.
func NewRateLimiter(r rate.Limit, burst int) *RateLimiter {
	rl := &RateLimiter{
		entries: make(map[string]*entry),
		r:       r,
		burst:   burst,
	}
	go rl.cleanup()
	return rl
}

func (rl *RateLimiter) allow(key string) bool {
	rl.mu.Lock()
	e, ok := rl.entries[key]
	if !ok {
		e = &entry{limiter: rate.NewLimiter(rl.r, rl.burst)}
		rl.entries[key] = e
	}
	e.lastSeen = time.Now()
	ok = e.limiter.Allow()
	rl.mu.Unlock()
	return ok
}

// cleanup removes entries not seen for 10 minutes.
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-10 * time.Minute)
		rl.mu.Lock()
		for k, e := range rl.entries {
			if e.lastSeen.Before(cutoff) {
				delete(rl.entries, k)
			}
		}
		rl.mu.Unlock()
	}
}

// hashIP returns a stable non-reversible key for the request IP.
// Uses /24 subnet for IPv4, /48 for IPv6 — avoids exact IP logging.
func hashIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		h := sha256.Sum256([]byte(host))
		return fmt.Sprintf("ip:%x", h[:8])
	}
	var subnet string
	if ip4 := ip.To4(); ip4 != nil {
		// mask to /24
		subnet = fmt.Sprintf("%d.%d.%d", ip4[0], ip4[1], ip4[2])
	} else {
		// mask to /48
		subnet = fmt.Sprintf("%x:%x:%x", ip[0:2], ip[2:4], ip[4:6])
	}
	h := sha256.Sum256([]byte(subnet))
	return fmt.Sprintf("ip:%x", h[:8])
}

// Limit returns middleware that enforces this limiter using keyFn to derive the bucket key.
// keyFn receives the request; return empty string to skip limiting for that request.
func (rl *RateLimiter) Limit(keyFn func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := keyFn(r)
			if key != "" && !rl.allow(key) {
				http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ByIP is a convenience key function: limit per hashed IP subnet.
func ByIP(r *http.Request) string {
	return hashIP(r)
}

// NoLimit is a key function that disables rate limiting (returns empty key).
// Use in dev/test mode to avoid throttling E2E tests.
func NoLimit(*http.Request) string { return "" }

// ByWallet limits by wallet_address query param or JSON body wallet_address field.
// Falls back to IP if wallet not present.
// NOTE: used only for pre-auth endpoints; post-auth use session middleware.
func ByWalletOrIP(r *http.Request) string {
	if w := r.URL.Query().Get("wallet_address"); w != "" {
		h := sha256.Sum256([]byte(w))
		return fmt.Sprintf("wallet:%x", h[:8])
	}
	return hashIP(r)
}
```

### internal/middleware/security.go
```go
package middleware

import "net/http"

// LimitBody rejects requests whose body exceeds maxBytes.
// This prevents memory exhaustion from large uploads.
func LimitBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders sets strict CSP and security headers.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self' wss:; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("X-XSS-Protection", "0") // modern browsers use CSP
		next.ServeHTTP(w, r)
	})
}
```

### internal/middleware/language.go
```go
package middleware

import (
	"context"
	"net/http"
	"strings"
)

type langKey struct{}

var supportedLangs = map[string]bool{
	"en": true, "ru": true, "ka": true, "es": true, "de": true, "vi": true,
}

// Language extracts language from URL prefix or Accept-Language header.
func Language(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := "en" // default

		// Check URL prefix: /ru/board/tbilisi → lang=ru
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
		if len(parts) >= 1 && supportedLangs[parts[0]] {
			lang = parts[0]
			// Strip lang prefix from path for downstream handlers
			remaining := "/"
			if len(parts) > 1 {
				remaining = "/" + parts[1]
			}
			r.URL.Path = remaining
		} else {
			// Fallback: Accept-Language header
			if al := r.Header.Get("Accept-Language"); al != "" {
				for _, tag := range strings.Split(al, ",") {
					code := strings.TrimSpace(strings.SplitN(tag, ";", 2)[0])
					short := strings.SplitN(code, "-", 2)[0]
					if supportedLangs[short] {
						lang = short
						break
					}
				}
			}
		}

		ctx := context.WithValue(r.Context(), langKey{}, lang)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// LangFrom extracts language from context.
func LangFrom(ctx context.Context) string {
	if v, ok := ctx.Value(langKey{}).(string); ok {
		return v
	}
	return "en"
}
```

### internal/middleware/nolog.go
```go
package middleware

import (
	"bufio"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// NoLogIP logs requests WITHOUT IP addresses, query strings, or path parameters.
// Logs only the route pattern (e.g. /listing/{id}) so no user identifiers appear in logs.
func NoLogIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(ww, r)

		// Use chi route pattern (/listing/{id}) instead of actual path (/listing/lst_abc123).
		// This prevents IDs, room IDs, wallet addresses from appearing in logs.
		routePattern := chi.RouteContext(r.Context()).RoutePattern()
		if routePattern == "" {
			routePattern = r.URL.Path // fallback for unmatched routes
		}
		log.Printf("%s %s %d %s", r.Method, routePattern, ww.status, time.Since(start).Round(time.Millisecond))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Hijack implements http.Hijacker so WebSocket upgrades work through this middleware.
// Without this, nhooyr/websocket returns 501 Not Implemented.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hj.Hijack()
}
```

## 4. Тесты


### e2e/lib/server.js
```js
// lib/server.js — start/stop the Go backend on an isolated port with a temp DB
import { spawn, execFileSync } from 'child_process';
import { mkdtempSync, rmSync } from 'fs';
import { tmpdir } from 'os';
import { join } from 'path';
import net from 'net';
import { createHmac, createHash, createCipheriv, randomBytes } from 'crypto';

const BACKEND_DIR = new URL('../../', import.meta.url).pathname.replace(/\/$/, '');

// Test-fixed values — must match env vars passed to TestServer
const TEST_SALT    = 'e2e-test-salt';
const TEST_ENC_KEY = 'e2e-test-wallet-enc-key-32bytes!';

export function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

export async function findFreePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.listen(0, '127.0.0.1', () => {
      const port = srv.address().port;
      srv.close(() => resolve(port));
    });
    srv.on('error', reject);
  });
}

export async function assertPortClosed(port, timeout = 3000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const free = await new Promise(resolve => {
      const c = net.createConnection(port, '127.0.0.1');
      c.on('connect', () => { c.destroy(); resolve(false); });
      c.on('error', () => resolve(true));
    });
    if (free) return;
    await sleep(100);
  }
  throw new Error(`Port ${port} still in use after teardown`);
}

// Mirror of Go crypto.WalletHash: HMAC-SHA256(salt, "naroom:v1:" + normalizedAddress)
function walletHash(address) {
  const addr = address.trim();
  const lower = addr.toLowerCase();
  const normalized = (lower.startsWith('bc1') || lower.startsWith('ltc1')) ? lower : addr;
  return createHmac('sha256', Buffer.from(TEST_SALT))
    .update('naroom:v1:' + normalized)
    .digest('hex');
}

// Mirror of Go crypto.EncryptAddress: AES-256-GCM, nonce||ciphertext||tag, base64url
function encryptAddress(address) {
  const keyBytes = createHash('sha256').update(TEST_ENC_KEY).digest();
  const nonce = randomBytes(12);
  const cipher = createCipheriv('aes-256-gcm', keyBytes, nonce);
  const ct = Buffer.concat([cipher.update(address, 'utf8'), cipher.final()]);
  const tag = cipher.getAuthTag();
  return Buffer.concat([nonce, ct, tag]).toString('base64url');
}

export class TestServer {
  constructor({ devMode = true, extraEnv = {} } = {}) {
    this.port = null;
    this.dbPath = null;
    this.proc = null;
    this.tmpDir = null;
    this.base = null;
    this.wsBase = null;
    this._devMode = devMode;
    this._extraEnv = extraEnv;
  }

  async start() {
    this.port = await findFreePort();
    this.tmpDir = mkdtempSync(join(tmpdir(), 'naroom-e2e-'));
    this.dbPath = join(this.tmpDir, 'naroom.db');
    this.base = `http://127.0.0.1:${this.port}`;
    this.wsBase = `ws://127.0.0.1:${this.port}`;

    // Use -tags dev so DEV_MODE=true is accepted by the binary
    this.proc = spawn('go', ['run', '-tags', 'dev', './cmd/naroom/main.go'], {
      cwd: BACKEND_DIR,
      env: {
        ...process.env,
        DEV_MODE: this._devMode ? 'true' : 'false',
        SERVER_SALT: TEST_SALT,
        // Pin HASH_KEY explicitly — prevents parent env from overriding SERVER_SALT fallback.
        HASH_KEY: TEST_SALT,
        // Required in prod mode (devMode=false). Provide a fixed 32-byte test key.
        WALLET_ENC_KEY: TEST_ENC_KEY,
        PORT: String(this.port),
        DB_PATH: this.dbPath,
        TTL_CLEAN_INTERVAL: '5',       // fast cleanup for tests
        INVOICE_WATCH_INTERVAL: '2',   // fast invoice confirm for tests (default 30s is too slow)
        // Allow callers to override any env var (e.g. MEMPOOL_API, DEV_SKIP_PAYMENTS)
        ...this._extraEnv,
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });

    // Uncomment for debugging:
    // this.proc.stdout.on('data', d => process.stdout.write(d));
    // this.proc.stderr.on('data', d => process.stderr.write(d));

    // Wait for /health
    const deadline = Date.now() + 15000;
    while (Date.now() < deadline) {
      try {
        const r = await fetch(`${this.base}/health`);
        if (r.ok) return this;
      } catch {}
      await sleep(250);
    }
    throw new Error('Backend failed to start in 15s');
  }

  async stop() {
    if (this.proc) {
      this.proc.kill('SIGKILL');
      this.proc = null;
      await sleep(300);
    }
    if (this.tmpDir) {
      try { rmSync(this.tmpDir, { recursive: true, force: true }); } catch {}
      this.tmpDir = null;
    }
    if (this.port) {
      await assertPortClosed(this.port).catch(e => console.warn('  warning:', e.message));
      this.port = null;
    }
  }

  db(sql) {
    return execFileSync('sqlite3', [this.dbPath, sql], { encoding: 'utf8' }).trim();
  }

  // registerDirect injects a wallet session and session token directly into the DB,
  // bypassing the /wallet/register API (and thus the blockchain balance check).
  // Used by tests that run in devMode=false but need registered wallets without real API calls.
  // Returns the raw session token (ready to use as Bearer token).
  registerDirect(address, role, currency = 'BTC', minRequiredUSD = null) {
    const now = Math.floor(Date.now() / 1000);
    const hash = walletHash(address);
    const enc  = encryptAddress(address);
    const minReq = minRequiredUSD !== null ? minRequiredUSD : (role === 'peer' ? 1000 : 150);

    this.db(
      `INSERT OR REPLACE INTO wallet_sessions ` +
      `(wallet_hash, wallet_address_enc, currency, role, balance_status, min_required_usd, balance_usd, last_checked_at, verified, first_seen, created_at) ` +
      `VALUES ('${hash}', '${enc}', '${currency}', '${role}', 'ok', ${minReq}, ${minReq}, ${now}, 1, ${now}, ${now})`
    );

    if (role === 'peer') {
      this.db(
        `INSERT OR IGNORE INTO reputation (counselor_hash, region, first_seen) ` +
        `VALUES ('${hash}', '', ${now})`
      );
    }

    const rawToken = randomBytes(32).toString('base64url');
    const tokenHash = createHash('sha256').update(rawToken).digest('hex');
    this.db(
      `INSERT INTO sessions (token_hash, wallet_hash, currency, role, created_at, expires_at) ` +
      `VALUES ('${tokenHash}', '${hash}', '${currency}', '${role}', ${now}, ${now + 86400})`
    );

    return rawToken;
  }
}
```

### e2e/lib/http.js
```js
// lib/http.js — session-aware API client
// After verifyWallet(), the session token is stored and used automatically
// for all protected endpoints via Authorization: Bearer <token>.
export class ApiClient {
  constructor(base) {
    this.base = base;
    this.tokens = {}; // wallet_address → { token, role }
  }

  // Returns { Authorization: 'Bearer ...' } for a wallet that has verified
  auth(wallet) {
    const s = this.tokens[wallet];
    if (!s) return {};
    return { 'Authorization': `Bearer ${s.token}` };
  }

  // Raw token string for a wallet (for WS Sec-WebSocket-Protocol)
  getToken(wallet) {
    return this.tokens[wallet]?.token ?? '';
  }

  async _req(method, path, data, wallet) {
    const headers = { ...(wallet ? this.auth(wallet) : {}) };
    if (data !== undefined) headers['Content-Type'] = 'application/json';
    const opts = { method, headers };
    if (data !== undefined) opts.body = JSON.stringify(data);
    const r = await fetch(this.base + path, opts);
    const body = await r.json().catch(() => ({}));
    return { status: r.status, body };
  }

  async get(path, wallet = null) { return this._req('GET', path, undefined, wallet); }
  async post(path, data, wallet = null) { return this._req('POST', path, data, wallet); }

  // ── Wallet / session ──────────────────────────────────────────────────────

  // Register wallet and get session token (no signature required).
  async verifyWallet(wallet, currency = 'BTC', role) {
    const r = await this.post('/wallet/register', {
      wallet_address: wallet, currency, role,
    });
    if (r.status === 200 && r.body.session_token) {
      this.tokens[wallet] = { token: r.body.session_token, role };
    }
    return r;
  }

  // POST /session/refresh with the current token for wallet.
  // On success, updates the stored token to the new one.
  async sessionRefresh(wallet) {
    const oldToken = this.getToken(wallet);
    const r = await this._reqWithToken('POST', '/session/refresh', {}, oldToken);
    if (r.status === 200 && r.body.token) {
      // Update stored token to the refreshed one (old is revoked server-side)
      const session = this.tokens[wallet];
      if (session) this.tokens[wallet] = { ...session, token: r.body.token };
    }
    return r;
  }

  async sessionRevoke(wallet) {
    return this.post('/session/revoke', {}, wallet);
  }

  // Post with an explicit raw token (not from stored sessions)
  async _reqWithToken(method, path, data, rawToken) {
    const headers = { 'Content-Type': 'application/json' };
    if (rawToken) headers['Authorization'] = `Bearer ${rawToken}`;
    const r = await fetch(this.base + path, {
      method, headers, body: JSON.stringify(data),
    });
    const body = await r.json().catch(() => ({}));
    return { status: r.status, body };
  }

  // ── Listings ──────────────────────────────────────────────────────────────

  async createListing(wallet, city = 'new_york') {
    return this.post('/listing/create', {
      city, dependency_type: 'alcohol', help_type: 'crisis', urgency: 'urgent',
      languages: ['en'], currency: 'BTC',
    }, wallet);
  }

  async getListing(id) { return this.get(`/listing/${id}`); }
  async getBoard(city = 'new_york') { return this.get(`/board/${city}`); }

  // wallet = the listing owner's wallet (for auth)
  async getResponses(listingId, wallet) {
    return this.get(`/listing/${listingId}/responses`, wallet);
  }

  async getListingChatRoom(listingId, wallet) {
    return this.get(`/listing/${listingId}/chatroom`, wallet);
  }

  // ── Responses ─────────────────────────────────────────────────────────────

  // peerWallet for auth, peerPubkey for E2E
  async respond(listingId, peerWallet, peerPubkey) {
    return this.post(`/listing/${listingId}/respond`, { peer_pubkey: peerPubkey }, peerWallet);
  }

  async cancelResponse(responseId, peerWallet) {
    return this.post(`/response/${responseId}/cancel`, {}, peerWallet);
  }

  // clientWallet for auth, clientPubkey for E2E registration
  async acceptResponse(responseId, clientWallet, clientPubkey) {
    return this.post(`/response/${responseId}/accept`, {
      client_pubkey: clientPubkey, currency: 'BTC',
    }, clientWallet);
  }

  // ── Chat ──────────────────────────────────────────────────────────────────

  async getPeerChatroom(peerWallet, listingId) {
    return this.get(`/peer/chatroom?listing_id=${encodeURIComponent(listingId)}`, peerWallet);
  }

  // Returns { room_id, status, role, my_pubkey, peer_pubkey, ... }
  async getChatRoom(roomId, wallet) {
    return this.get(`/chat/${roomId}`, wallet);
  }

  // pubkey still required in body — handler identifies sender by pubkey for E2E attribution
  async pollSend(roomId, wallet, pubkey, nonce, ciphertext, msgType = 'text') {
    return this.post('/chat/poll/send', { room_id: roomId, pubkey, nonce, ciphertext, msg_type: msgType }, wallet);
  }

  async pollReceive(roomId, wallet, pubkey, since = 0) {
    return this.get(`/chat/poll/receive?room_id=${roomId}&pubkey=${encodeURIComponent(pubkey)}&since=${since}`, wallet);
  }

  async closeChat(roomId, wallet) {
    return this.post(`/chat/${roomId}/close`, {}, wallet);
  }

  // ── Misc ──────────────────────────────────────────────────────────────────

  async submitReview(token, rating) {
    return this.post('/review', { token, rating });
  }

  async invoiceStatus(invoiceId, wallet) {
    return this.get(`/invoice/${invoiceId}/status`, wallet);
  }
}
```

### e2e/lib/assert.js
```js
// lib/assert.js — assertion helpers
import { execFileSync } from 'child_process';

export function assert(cond, msg) {
  if (!cond) throw new Error(`ASSERT FAILED: ${msg}`);
}

export function assertEqual(a, b, msg) {
  if (a !== b) throw new Error(`ASSERT FAILED: ${msg} — expected ${JSON.stringify(b)}, got ${JSON.stringify(a)}`);
}

export function assertStatus(res, expected, label) {
  if (res.status !== expected) {
    throw new Error(`ASSERT FAILED: ${label} — expected HTTP ${expected}, got ${res.status}: ${JSON.stringify(res.body)}`);
  }
}

export function assertHasField(obj, field, label) {
  if (!obj[field]) throw new Error(`ASSERT FAILED: ${label} — missing field "${field}" in ${JSON.stringify(obj)}`);
}

export function assertNoField(obj, field, label) {
  if (obj[field] !== undefined && obj[field] !== null && obj[field] !== '') {
    throw new Error(`ASSERT FAILED: ${label} — field "${field}" should NOT be present, got ${JSON.stringify(obj[field])}`);
  }
}

// Poll until predicate returns truthy, with timeout
export async function pollUntil(fn, { timeout = 45000, interval = 2000, label = 'condition' } = {}) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    last = await fn();
    if (last) return last;
    await sleep(interval);
  }
  throw new Error(`TIMEOUT: "${label}" not met within ${timeout}ms. Last: ${JSON.stringify(last)}`);
}

// Assert room is NOT visible to actor before expected phase (peerWallet has a verified session)
export async function assertNoRoom(api, peerWallet, listingId, label) {
  const r = await api.getPeerChatroom(peerWallet, listingId);
  if (r.status === 200 && r.body.room_id) {
    throw new Error(`ASSERT FAILED: ${label} — peer should NOT see a room yet, got ${JSON.stringify(r.body)}`);
  }
}

// Assert SQLite DB state directly
export function dbQuery(dbPath, sql) {
  return execFileSync('sqlite3', [dbPath, sql], { encoding: 'utf8' }).trim();
}

export function assertDbCount(dbPath, sql, expected, label) {
  const result = dbQuery(dbPath, sql);
  const count = parseInt(result, 10);
  if (count !== expected) {
    throw new Error(`ASSERT FAILED DB: ${label} — expected ${expected}, got ${count}. SQL: ${sql}`);
  }
}

export function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

export function log(label, msg) {
  console.log(`  [${label}] ${msg}`);
}

export function pass(msg) {
  console.log(`  ✓ ${msg}`);
}

export function fail(msg) {
  console.error(`  ✗ ${msg}`);
}
```

### e2e/lib/crypto.js
```js
// lib/crypto.js — NaCl keypair generation, encrypt, decrypt
import nacl from 'tweetnacl';

export function newKeypair() {
  const kp = nacl.box.keyPair();
  return {
    pub:  toHex(kp.publicKey),
    priv: toHex(kp.secretKey),
    _pub: kp.publicKey,
    _priv: kp.secretKey,
  };
}

export function sharedKey(myPrivHex, peerPubHex) {
  return nacl.box.before(fromHex(peerPubHex), fromHex(myPrivHex));
}

export function encrypt(text, myPrivHex, peerPubHex) {
  const key = sharedKey(myPrivHex, peerPubHex);
  const nonce = nacl.randomBytes(nacl.box.nonceLength);
  const box = nacl.box.after(new TextEncoder().encode(text), nonce, key);
  return { nonce: toHex(nonce), ciphertext: toHex(box) };
}

export function decrypt(nonceHex, ciphertextHex, myPrivHex, peerPubHex) {
  const key = sharedKey(myPrivHex, peerPubHex);
  const plain = nacl.box.open.after(fromHex(ciphertextHex), fromHex(nonceHex), key);
  if (!plain) return null;
  return new TextDecoder().decode(plain);
}

export function toHex(b) {
  return Array.from(b).map(x => x.toString(16).padStart(2, '0')).join('');
}
export function fromHex(h) {
  return new Uint8Array(h.match(/../g).map(x => parseInt(x, 16)));
}

// Generate a fake 300KB image payload (base64 jpeg-like data url)
export function fakeImageDataUrl(sizeBytes = 300_000) {
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
  let s = '';
  while (s.length < sizeBytes) s += chars[(Math.random() * 64) | 0];
  return 'data:image/jpeg;base64,' + s.slice(0, sizeBytes);
}
```

### e2e/lib/ws.js
```js
// lib/ws.js — WebSocket actor with bounded reconnect and terminal state detection
// Authentication: session token passed as Sec-WebSocket-Protocol header (Step 5).
import WebSocket from 'ws';
import { sleep } from './server.js';
import { encrypt, decrypt } from './crypto.js';

export class ChatWS {
  // token: session token for WS auth (Sec-WebSocket-Protocol)
  // wallet: wallet address for API calls in reconnect logic
  // myPubkey: X25519 pubkey — identifies "my" messages in history
  // privkey, peerPubkey: keypair for E2E decryption
  constructor(wsBase, roomId, token, wallet, myPubkey, privkey, peerPubkey) {
    this.wsBase = wsBase;
    this.roomId = roomId;
    this.token = token;
    this.wallet = wallet;
    this.myPubkey = myPubkey;
    this.privkey = privkey;
    this.peerPubkey = peerPubkey;
    this.ws = null;
    this.messages = [];
    this.systemEvents = [];
    this.closed = false;
    this.reconnectCount = 0;
    this.MAX_RECONNECTS = 3;
  }

  connect() {
    return new Promise((resolve, reject) => {
      const url = `${this.wsBase}/chat/ws?room_id=${this.roomId}`;
      // Token sent as Sec-WebSocket-Protocol — only way browser WS API can send auth material.
      // ws npm package sends it in the Sec-WebSocket-Protocol header.
      this.ws = new WebSocket(url, this.token ? [this.token] : []);

      this.ws.on('open', () => resolve());
      this.ws.on('error', reject);

      this.ws.on('message', (raw) => {
        try {
          const data = JSON.parse(raw);
          if (data.type === 'system') {
            this.systemEvents.push(data);
            return;
          }
          const text = decrypt(data.nonce, data.ciphertext, this.privkey, this.peerPubkey);
          const from = data.sender_pubkey === this.myPubkey ? 'me' : 'them';
          this.messages.push({ ...data, decrypted: text, from });
        } catch {}
      });

      this.ws.on('close', () => {
        if (!this.closed) {
          this.closed = true;
        }
      });
    });
  }

  send(text, msgType = 'text') {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) throw new Error('WS not open');
    const enc = encrypt(text, this.privkey, this.peerPubkey);
    this.ws.send(JSON.stringify({ ...enc, msg_type: msgType }));
  }

  close() {
    this.closed = true;
    if (this.ws) { this.ws.terminate(); this.ws = null; }
  }

  // Returns: { terminal: true, status } or throws if max reconnects exceeded
  async reconnectUntilTerminal(api) {
    for (let attempt = 1; attempt <= this.MAX_RECONNECTS; attempt++) {
      const r = await api.getChatRoom(this.roomId, this.wallet);
      if (r.status === 200 && (r.body.status === 'closed' || r.body.status === 'expired')) {
        return { terminal: true, status: r.body.status };
      }
      if (r.status === 404 || r.status === 403) {
        return { terminal: true, status: 'not_found' };
      }
      this.closed = false;
      try {
        await this.connect();
        await sleep(500);
        return { terminal: false, reconnected: true };
      } catch {}
      await sleep(1000);
    }
    throw new Error(`Reconnect loop did not reach terminal state after ${this.MAX_RECONNECTS} attempts`);
  }

  waitForSystemEvent(eventName, timeout = 10000) {
    const existing = this.systemEvents.find(e => e.event === eventName);
    if (existing) return Promise.resolve(existing);

    return new Promise((resolve, reject) => {
      const deadline = Date.now() + timeout;
      const check = setInterval(() => {
        const found = this.systemEvents.find(e => e.event === eventName);
        if (found) { clearInterval(check); resolve(found); }
        else if (Date.now() >= deadline) {
          clearInterval(check);
          reject(new Error(`Timeout waiting for system event: ${eventName}`));
        }
      }, 100);
    });
  }

  waitForMessage(timeout = 10000) {
    return new Promise((resolve, reject) => {
      const before = this.messages.length;
      const deadline = Date.now() + timeout;
      const check = setInterval(() => {
        if (this.messages.length > before) {
          clearInterval(check);
          resolve(this.messages[this.messages.length - 1]);
        }
        if (Date.now() >= deadline) {
          clearInterval(check);
          reject(new Error('Timeout waiting for WS message'));
        }
      }, 100);
    });
  }
}
```

### e2e/lib/runner.js
```js
// lib/runner.js — minimal test runner
export class Runner {
  constructor(name) {
    this.name = name;
    this.passed = 0;
    this.failed = 0;
    this.errors = [];
  }

  async run(label, fn) {
    try {
      await fn();
      console.log(`  ✓ ${label}`);
      this.passed++;
    } catch(e) {
      console.error(`  ✗ ${label}`);
      console.error(`    ${e.message}`);
      this.failed++;
      this.errors.push({ label, error: e.message });
    }
  }

  summary() {
    const total = this.passed + this.failed;
    console.log(`\n  ${this.name}: ${this.passed}/${total} passed`);
    if (this.failed > 0) {
      for (const { label, error } of this.errors) {
        console.error(`    FAILED: ${label}\n      ${error}`);
      }
      return false;
    }
    return true;
  }
}
```

### e2e/lib/chain_stub.js
```js
// e2e/lib/chain_stub.js
// HTTP-заглушка, имитирующая mempool.space и BlockCypher.
// Управляется через control-эндпоинт: тест задаёт сценарий, watcher бэкенда
// ходит в заглушку как в реальный API.
//
// Использование в тесте:
//   const stub = await startChainStub();
//   const server = new TestServer({ extraEnv: {
//     MEMPOOL_API: stub.url + '/mempool',
//     BLOCKCYPHER_API: stub.url + '/blockcypher',
//   }});
//   stub.setAddressState('bc1q...', {
//     txs: [{ txid: 'aa', value_sats: 40000, confirmations: 1, senders: ['1Sender...'] }],
//     balance_sats: 500000,  // for balance check after payment
//   });
//
// tx fields:
//   txid          string
//   value_sats    number   — amount received at the invoice address
//   confirmations number   — 0 = unconfirmed, ≥1 = confirmed
//   senders       string[] — optional: sender addresses (vin). Required for payer verification.

import http from 'node:http';

export async function startChainStub() {
  // state: address → { txs, balance_sats }
  const state = new Map();
  let globalMode = 'ok'; // 'ok' | 'timeout' | 'error429' | 'error500'

  const server = http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://x');

    // ── control API (used only by tests) ────────────────────────────────────
    if (url.pathname === '/_control/set' && req.method === 'POST') {
      let body = '';
      for await (const chunk of req) body += chunk;
      const { address, txs, balance_sats, mode } = JSON.parse(body);
      if (address !== undefined) {
        state.set(address, { txs: txs ?? [], balance_sats: balance_sats ?? 0 });
      }
      if (mode !== undefined) globalMode = mode;
      res.writeHead(200).end('{"ok":true}');
      return;
    }

    // ── failure modes ────────────────────────────────────────────────────────
    if (globalMode === 'timeout') {
      // Hold connection longer than the Go HTTP client timeout (15s)
      const t = setTimeout(() => { try { res.writeHead(504).end(); } catch {} }, 30_000);
      req.on('close', () => clearTimeout(t));
      return;
    }
    if (globalMode === 'error429') {
      res.writeHead(429, { 'content-type': 'application/json' }).end('{"error":"rate limit"}');
      return;
    }
    if (globalMode === 'error500') {
      res.writeHead(500).end('{"error":"internal"}');
      return;
    }

    // ── mempool.space: GET /mempool/address/:addr/txs ────────────────────────
    let m = url.pathname.match(/^\/mempool\/address\/([^/]+)\/txs$/);
    if (m) {
      const addr = m[1];
      const s = state.get(addr) ?? { txs: [] };
      const txs = s.txs.map(t => ({
        txid: t.txid,
        status: {
          confirmed: t.confirmations > 0,
          block_height: t.confirmations > 0 ? 900000 : null,
        },
        vout: [{ scriptpubkey_address: addr, value: t.value_sats }],
        // vin: sender addresses for payer verification
        vin: (t.senders ?? []).map(senderAddr => ({
          prevout: { scriptpubkey_address: senderAddr },
        })),
      }));
      res.writeHead(200, { 'content-type': 'application/json' }).end(JSON.stringify(txs));
      return;
    }

    // ── mempool.space: GET /mempool/address/:addr (balance) ─────────────────
    m = url.pathname.match(/^\/mempool\/address\/([^/]+)$/);
    if (m) {
      const s = state.get(m[1]) ?? { balance_sats: 0 };
      const bal = s.balance_sats ?? 0;
      res.writeHead(200, { 'content-type': 'application/json' }).end(JSON.stringify({
        chain_stats: { funded_txo_sum: bal, spent_txo_sum: 0 },
        mempool_stats: { funded_txo_sum: 0, spent_txo_sum: 0 },
      }));
      return;
    }

    // ── BlockCypher: GET /blockcypher/addrs/:addr ────────────────────────────
    m = url.pathname.match(/^\/blockcypher\/addrs\/([^/]+)/);
    if (m) {
      const addr = m[1];
      const s = state.get(addr) ?? { txs: [], balance_sats: 0 };
      const txrefs = s.txs.map(t => ({
        tx_hash: t.txid,
        value: t.value_sats,
        confirmations: t.confirmations,
        addresses: t.senders ?? [],
      }));
      res.writeHead(200, { 'content-type': 'application/json' }).end(JSON.stringify({
        address: addr,
        balance: s.balance_sats ?? 0,
        txrefs,
      }));
      return;
    }

    res.writeHead(404).end();
  });

  await new Promise(r => server.listen(0, '127.0.0.1', r));
  const port = server.address().port;
  const stubUrl = `http://127.0.0.1:${port}`;

  return {
    url: stubUrl,
    async setAddressState(address, { txs = [], balance_sats = 0 } = {}) {
      await fetch(`${stubUrl}/_control/set`, {
        method: 'POST',
        body: JSON.stringify({ address, txs, balance_sats }),
      });
    },
    async setMode(mode) {
      await fetch(`${stubUrl}/_control/set`, {
        method: 'POST',
        body: JSON.stringify({ mode }),
      });
    },
    close: () => new Promise(r => server.close(r)),
  };
}
```

### e2e/tests/001_happy_path.js
```js
// 001_happy_path.js — full E2E: wallet → listing → respond → accept → chat → close → review
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair, encrypt, fakeImageDataUrl } from '../lib/crypto.js';
import { ChatWS } from '../lib/ws.js';
import { assertStatus, assertHasField, assertNoRoom, assertDbCount, pollUntil, pass, sleep } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 001: Happy Path (full E2E) ===');
  const srv = new TestServer();
  const t = new Runner('001_happy_path');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);
    const clientKeys = newKeypair();
    const peerKeys   = newKeypair();
    let listingId, responseId, invoiceId, roomId, reviewToken;

    // ── Phase 1: Wallet verification ─────────────────────────────────────
    await t.run('client wallet verifies + gets session token', async () => {
      const r = await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      assertStatus(r, 200, 'client verify');
      assertHasField(r.body, 'session_token', 'client verify');
    });

    await t.run('peer wallet verifies + gets session token', async () => {
      const r = await api.verifyWallet(PEER_WALLET, 'BTC', 'peer');
      assertStatus(r, 200, 'peer verify');
      assertHasField(r.body, 'session_token', 'peer verify');
    });

    // ── Phase 2: Create listing ──────────────────────────────────────────
    await t.run('client creates listing', async () => {
      const r = await api.createListing(CLIENT_WALLET);
      assertStatus(r, 201, 'create listing');
      assertHasField(r.body, 'listing_id', 'create listing');
      assertHasField(r.body, 'invoice_id', 'create listing');
      listingId = r.body.listing_id;
      invoiceId = r.body.invoice_id;
    });

    await t.run('duplicate listing rejected', async () => {
      const r = await api.createListing(CLIENT_WALLET);
      assertStatus(r, 409, 'duplicate listing');
    });

    await t.run('no session → create listing rejected', async () => {
      const r = await fetch(`${srv.base}/listing/create`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ city: 'london', dependency_type: 'alcohol', help_type: 'crisis', urgency: 'urgent', languages: ['en'], currency: 'BTC' }),
      });
      if (r.status !== 401) throw new Error(`Expected 401 without session, got ${r.status}`);
    });

    // ── Phase 3: Listing activates ───────────────────────────────────────
    await t.run('listing activates after invoice auto-confirm', async () => {
      const listing = await pollUntil(async () => {
        const r = await api.getListing(listingId);
        return r.body.status === 'active' ? r.body : null;
      }, { timeout: 45000, label: 'listing active' });
      if (listing.status !== 'active') throw new Error('Listing not active');
    });

    await t.run('listing appears on board', async () => {
      const r = await api.getBoard('new_york');
      assertStatus(r, 200, 'board');
      const found = r.body.find(l => l.id === listingId);
      if (!found) throw new Error('Listing not on board');
    });

    await t.run('peer cannot see chat room before respond', async () => {
      await assertNoRoom(api, PEER_WALLET, listingId, 'before respond');
    });

    // ── Phase 4: Peer responds ───────────────────────────────────────────
    await t.run('peer responds to listing', async () => {
      const r = await api.respond(listingId, PEER_WALLET, peerKeys.pub);
      assertStatus(r, 201, 'respond');
      assertHasField(r.body, 'response_id', 'respond');
      responseId = r.body.response_id;
    });

    await t.run('peer cannot respond twice', async () => {
      const r = await api.respond(listingId, PEER_WALLET, peerKeys.pub);
      assertStatus(r, 409, 'duplicate respond');
    });

    await t.run('peer cannot see chat room before client accepts', async () => {
      await assertNoRoom(api, PEER_WALLET, listingId, 'before accept');
    });

    // ── Phase 5: Client accepts ──────────────────────────────────────────
    await t.run('client sees responses (no peer_address exposed)', async () => {
      const r = await api.getResponses(listingId, CLIENT_WALLET);
      assertStatus(r, 200, 'get responses');
      const resp = r.body.find(x => x.id === responseId);
      if (!resp) throw new Error('Response not listed');
      if (resp.peer_address !== undefined) throw new Error('peer_address should NOT be in response');
      if (!resp.peer_pubkey) throw new Error('peer_pubkey missing');
    });

    await t.run('stranger cannot see responses', async () => {
      const r = await api.getResponses(listingId, PEER_WALLET);
      assertStatus(r, 403, 'stranger responses');
    });

    await t.run('client accepts response', async () => {
      const r = await api.acceptResponse(responseId, CLIENT_WALLET, clientKeys.pub);
      assertStatus(r, 200, 'accept');
      assertHasField(r.body, 'invoice_id', 'accept');
    });

    await t.run('peer poll returns 404 while invoice not yet confirmed', async () => {
      await assertNoRoom(api, PEER_WALLET, listingId, 'after accept, before invoice');
    });

    // ── Phase 6: Chat room opens ─────────────────────────────────────────
    await t.run('chat room created after peer pays $15', async () => {
      const room = await pollUntil(async () => {
        const r = await api.getPeerChatroom(PEER_WALLET, listingId);
        return r.status === 200 ? r.body : null;
      }, { timeout: 45000, label: 'chat room for peer' });
      roomId = room.room_id;
    });

    await t.run('listing removed from board after chat opens', async () => {
      const r = await api.getBoard('new_york');
      const found = r.body.find(l => l.id === listingId);
      if (found) throw new Error('Listing still on board after match');
    });

    await t.run('DB: room response_id matches current response', async () => {
      const val = srv.db(`SELECT response_id FROM chat_rooms WHERE id='${roomId}'`);
      if (val !== responseId) throw new Error(`room.response_id=${val}, expected=${responseId}`);
    });

    await t.run('client gets room with role=client and my_pubkey', async () => {
      const r = await api.getChatRoom(roomId, CLIENT_WALLET);
      assertStatus(r, 200, 'client getChatRoom');
      if (r.body.role !== 'client') throw new Error(`Expected role=client, got ${r.body.role}`);
      if (r.body.peer_pubkey !== peerKeys.pub) throw new Error('peer_pubkey mismatch');
      if (!r.body.my_pubkey) throw new Error('my_pubkey missing from getChatRoom response');
    });

    await t.run('peer gets room with role=peer and my_pubkey', async () => {
      const r = await api.getChatRoom(roomId, PEER_WALLET);
      assertStatus(r, 200, 'peer getChatRoom');
      if (r.body.role !== 'peer') throw new Error(`Expected role=peer, got ${r.body.role}`);
      if (r.body.peer_pubkey !== clientKeys.pub) throw new Error('peer_pubkey mismatch');
    });

    await t.run('stranger cannot access room', async () => {
      // No session for stranger — must be 401, not 403
      const r = await fetch(`${srv.base}/chat/${roomId}`, { method: 'GET' });
      if (r.status !== 401) throw new Error(`Expected 401 without session, got ${r.status}`);
    });

    // ── Phase 7: WebSocket chat ──────────────────────────────────────────
    let clientWS, peerWS;

    await t.run('both actors connect via WebSocket (token as Sec-WebSocket-Protocol)', async () => {
      const clientToken = api.getToken(CLIENT_WALLET);
      const peerToken   = api.getToken(PEER_WALLET);
      clientWS = new ChatWS(srv.wsBase, roomId, clientToken, CLIENT_WALLET, clientKeys.pub, clientKeys.priv, peerKeys.pub);
      peerWS   = new ChatWS(srv.wsBase, roomId, peerToken,   PEER_WALLET,   peerKeys.pub,   peerKeys.priv,   clientKeys.pub);
      await clientWS.connect();
      await peerWS.connect();
    });

    await t.run('client sends message, peer receives it', async () => {
      const waiter = peerWS.waitForMessage(8000);
      clientWS.send('Hello from client');
      const msg = await waiter;
      if (msg.decrypted !== 'Hello from client') throw new Error(`Decrypted: ${msg.decrypted}`);
      if (msg.sender_pubkey !== clientKeys.pub) throw new Error('Wrong sender');
    });

    await t.run('peer sends reply, client receives it', async () => {
      const waiter = clientWS.waitForMessage(8000);
      peerWS.send('Hello from peer');
      const msg = await waiter;
      if (msg.decrypted !== 'Hello from peer') throw new Error(`Decrypted: ${msg.decrypted}`);
    });

    // ── Phase 7b: Poll-based messaging ───────────────────────────────────
    await t.run('poll send text (client)', async () => {
      const enc = encrypt('poll text from client', clientKeys.priv, peerKeys.pub);
      const r = await api.pollSend(roomId, CLIENT_WALLET, clientKeys.pub, enc.nonce, enc.ciphertext, 'text');
      assertStatus(r, 201, 'poll send');
      assertHasField(r.body, 'id', 'poll send');
    });

    await t.run('poll receive (peer sees messages)', async () => {
      const r = await api.pollReceive(roomId, PEER_WALLET, peerKeys.pub, 0);
      assertStatus(r, 200, 'poll receive');
      if (!r.body.messages || r.body.messages.length < 1) throw new Error('No messages in poll');
    });

    // ── Phase 7c: Image payload ───────────────────────────────────────────
    await t.run('300KB image sends via poll', async () => {
      const img = fakeImageDataUrl(300_000);
      const enc = encrypt(img, clientKeys.priv, peerKeys.pub);
      const r = await api.pollSend(roomId, CLIENT_WALLET, clientKeys.pub, enc.nonce, enc.ciphertext, 'image_camera');
      assertStatus(r, 201, 'image poll send');
    });

    await t.run('image preserved in history (peer reload)', async () => {
      const r = await api.pollReceive(roomId, PEER_WALLET, peerKeys.pub, 0);
      const imgs = r.body.messages.filter(m => m.msg_type === 'image_camera');
      if (imgs.length < 1) throw new Error('Image not in history');
      if (imgs[0].ciphertext.length < 100) throw new Error('Image ciphertext suspiciously small');
    });

    // ── Phase 8: Close chat ───────────────────────────────────────────────
    await t.run('client closes chat, receives review_token', async () => {
      const r = await api.closeChat(roomId, CLIENT_WALLET);
      assertStatus(r, 200, 'close chat');
      if (r.body.status !== 'closed') throw new Error('status not closed');
      assertHasField(r.body, 'review_token', 'close by client');
      reviewToken = r.body.review_token;
    });

    await t.run('room is now closed', async () => {
      const r = await api.getChatRoom(roomId, CLIENT_WALLET);
      if (r.body.status !== 'closed') throw new Error(`status=${r.body.status}`);
    });

    await t.run('cannot send to closed room', async () => {
      const enc = encrypt('late msg', clientKeys.priv, peerKeys.pub);
      const r = await api.pollSend(roomId, CLIENT_WALLET, clientKeys.pub, enc.nonce, enc.ciphertext);
      assertStatus(r, 410, 'send to closed room');
    });

    await t.run('peer WS detects closed room (terminal state, stops reconnect)', async () => {
      const result = await peerWS.reconnectUntilTerminal(api);
      if (!result.terminal) throw new Error('Expected terminal state');
    });

    // ── Phase 9: Review ───────────────────────────────────────────────────
    await t.run('client submits review', async () => {
      const r = await api.submitReview(reviewToken, 'up');
      assertStatus(r, 200, 'submit review');
    });

    await t.run('review token cannot be reused', async () => {
      const r = await api.submitReview(reviewToken, 'down');
      if (r.status === 200) throw new Error('Token reuse should be rejected');
    });

    // ── Phase 10: Original listing restored to board after close ─────────
    await t.run('original listing restored to board after chat closed', async () => {
      const r = await api.getListing(listingId);
      if (r.body.status !== 'active') throw new Error(`Listing status=${r.body.status}, expected active`);
      const board = await api.getBoard('new_york');
      const found = board.body.find(l => l.id === listingId);
      if (!found) throw new Error('Restored listing not on board');
    });

    await t.run('client cannot create second listing while original is active', async () => {
      const r2 = await api.createListing(CLIENT_WALLET, 'london');
      assertStatus(r2, 409, 'duplicate listing while original active');
    });

    clientWS?.close();
    peerWS?.close();

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
```

### e2e/tests/008_wallet_challenge.js
```js
// 008_wallet_register.js — wallet registration endpoint correctness
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { assertStatus, assertHasField } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 008: Wallet Register ===');
  const srv = new TestServer();
  const t = new Runner('008_wallet_register');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    await t.run('POST /wallet/register (client) issues session_token', async () => {
      const r = await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      assertStatus(r, 200, 'register');
      assertHasField(r.body, 'session_token', 'register response');
      if (r.body.expires_in !== 86400) throw new Error(`expires_in=${r.body.expires_in}, expected 86400`);
    });

    await t.run('DB: session token stored as hash (not plain)', async () => {
      const tokenHash = srv.db(`SELECT token_hash FROM sessions LIMIT 1`);
      if (tokenHash.length !== 64) throw new Error(`token_hash length=${tokenHash.length}, expected 64`);
    });

    await t.run('missing wallet_address → 400', async () => {
      const r = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ currency: 'BTC', role: 'client' }),
      });
      if (r.status !== 400) throw new Error(`Expected 400, got ${r.status}`);
    });

    await t.run('invalid role → 400', async () => {
      const r = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ wallet_address: CLIENT_WALLET, currency: 'BTC', role: 'admin' }),
      });
      if (r.status !== 400 && r.status !== 429) throw new Error(`Expected 400 or 429, got ${r.status}`);
    });

    await t.run('invalid currency → 400', async () => {
      const r = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ wallet_address: CLIENT_WALLET, currency: 'ETH', role: 'client' }),
      });
      if (r.status !== 400 && r.status !== 429) throw new Error(`Expected 400 or 429, got ${r.status}`);
    });

    await t.run('POST /wallet/register (peer) issues session_token', async () => {
      const r = await api.verifyWallet(PEER_WALLET, 'BTC', 'peer');
      assertStatus(r, 200, 'register peer');
      assertHasField(r.body, 'session_token', 'register peer response');
    });

    await t.run('second register for same wallet returns new token', async () => {
      const r1 = await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      const r2 = await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      if (r1.body.session_token === r2.body.session_token) {
        throw new Error('Expected different tokens on re-register');
      }
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
```

### e2e/tests/018_balance_threshold.js
```js
// 018_balance_threshold.js — balance gate enforces slot cost ($1000/slot) in prod mode (RS-5)
// devMode=false so the balance check in respond.go is active.
// Wallet sessions are injected directly (registerDirect) to bypass the blockchain API.
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_A = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const CLIENT_B = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';
const PEER     = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 018: Balance Threshold (RS-5) ===');
  // devMode=false enables the balance/slot-cost check in respond.go
  const srv = new TestServer({ devMode: false });
  const t = new Runner('018_balance_threshold');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    // Inject wallet sessions directly — avoids real blockchain API calls in prod mode.
    const tokenA    = srv.registerDirect(CLIENT_A, 'client');
    const tokenB    = srv.registerDirect(CLIENT_B, 'client');
    const tokenPeer = srv.registerDirect(PEER,     'peer', 'BTC', 1000);
    api.tokens[CLIENT_A] = { token: tokenA,    role: 'client' };
    api.tokens[CLIENT_B] = { token: tokenB,    role: 'client' };
    api.tokens[PEER]     = { token: tokenPeer, role: 'peer'   };

    let listingA, listingB;
    const future = Math.floor(Date.now() / 1000) + 3600;

    await t.run('create listing A (client A)', async () => {
      // In prod mode listing/create still issues an invoice — force-activate via DB.
      const r = await api.post('/listing/create', {
        city: 'tbilisi', dependency_type: 'alcohol', help_type: 'crisis',
        urgency: 'urgent', languages: ['en'], currency: 'BTC',
      }, CLIENT_A);
      assertStatus(r, 201, 'createListing A');
      listingA = r.body.listing_id;
      srv.db(`UPDATE listings SET status='active', visible_until=${future} WHERE id='${listingA}'`);
    });

    await t.run('create listing B (client B)', async () => {
      const r = await api.post('/listing/create', {
        city: 'tbilisi', dependency_type: 'alcohol', help_type: 'crisis',
        urgency: 'urgent', languages: ['en'], currency: 'BTC',
      }, CLIENT_B);
      assertStatus(r, 201, 'createListing B');
      listingB = r.body.listing_id;
      srv.db(`UPDATE listings SET status='active', visible_until=${future} WHERE id='${listingB}'`);
    });

    await t.run('peer starts with min_required_usd=1000 (verified on register)', async () => {
      const minReq = srv.db(`SELECT min_required_usd FROM wallet_sessions WHERE role='peer'`);
      if (parseFloat(minReq) !== 1000) throw new Error(`expected min_required_usd=1000, got ${minReq}`);
    });

    await t.run('peer responds to listing A (slot 1: $1000 needed, $1000 have) → 201', async () => {
      const r = await api.respond(listingA, PEER, newKeypair().pub);
      assertStatus(r, 201, 'respond slot 1');
    });

    await t.run('peer responds to listing B (slot 2: $2000 needed, $1000 have) → 403', async () => {
      const r = await api.respond(listingB, PEER, newKeypair().pub);
      if (r.status !== 403) throw new Error(`expected 403 balance gate, got ${r.status}: ${JSON.stringify(r.body)}`);
    });

    await t.run('after injecting $2000 balance, peer can respond to listing B → 201', async () => {
      // Raise the min_required_usd to $2000 (simulates a verified higher balance)
      srv.db(`UPDATE wallet_sessions SET min_required_usd=2000 WHERE role='peer'`);
      const r = await api.respond(listingB, PEER, newKeypair().pub);
      assertStatus(r, 201, 'respond slot 2 after balance raise');
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
```

### e2e/tests/020_devmode_headers.js
```js
// 020_devmode_headers.js — X-Dev-* headers rejected in production mode (SE-4)
// devMode=false: X-Dev-Wallet + X-Dev-Role headers must NOT grant access.
// Only a valid Bearer token (from registerDirect) should work.
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { assertStatus } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const PEER_WALLET = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 020: DevMode Headers Rejected in Prod (SE-4) ===');
  const srv = new TestServer({ devMode: false });
  const t = new Runner('020_devmode_headers');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    // Inject a peer session directly (bypasses blockchain API)
    const token = srv.registerDirect(PEER_WALLET, 'peer', 'BTC', 1000);
    api.tokens[PEER_WALLET] = { token, role: 'peer' };

    await t.run('X-Dev-Wallet + X-Dev-Role headers without Bearer token → 401', async () => {
      const r = await fetch(`${srv.base}/peer/region`, {
        method: 'GET',
        headers: {
          'X-Dev-Wallet': PEER_WALLET,
          'X-Dev-Role': 'peer',
        },
      });
      if (r.status !== 401) throw new Error(`expected 401, got ${r.status}`);
    });

    await t.run('X-Dev-* headers combined with valid Bearer token → 200 (Bearer wins)', async () => {
      const r = await fetch(`${srv.base}/peer/region`, {
        method: 'GET',
        headers: {
          'Authorization': `Bearer ${token}`,
          'X-Dev-Wallet': PEER_WALLET,
          'X-Dev-Role': 'peer',
        },
      });
      // Bearer token is valid so request should succeed
      if (r.status !== 200) throw new Error(`expected 200, got ${r.status}`);
    });

    await t.run('valid Bearer token alone (no X-Dev-* headers) → 200', async () => {
      const r = await api.get('/peer/region', PEER_WALLET);
      assertStatus(r, 200, 'Bearer token GET /peer/region');
    });

    await t.run('no headers at all → 401', async () => {
      const r = await fetch(`${srv.base}/peer/region`, { method: 'GET' });
      if (r.status !== 401) throw new Error(`expected 401, got ${r.status}`);
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
```

### e2e/tests/028_payment_edge_cases.js
```js
// 028_payment_edge_cases
// Требует chain_stub (в e2e/lib/) + бэкенд с настраиваемыми URL API.
// DEV_SKIP_PAYMENTS не установлен — invoice watcher работает по-настоящему,
// но ходит в заглушку вместо реальных API.
//
// Сценарии:
//   a) недоплата: tx < суммы инвойса → инвойс НЕ подтверждён
//   b) две транзакции в сумме = инвойс → фиксируем политику (одна TX или сумма)
//   c) API недоступен (таймаут) → watcher не падает, ретраит, после восстановления — подтверждает
import { TestServer, sleep } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';
import { startChainStub } from '../lib/chain_stub.js';

// Separate wallets per scenario — one active listing per wallet at a time
const WALLET_A = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const WALLET_B = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';
const WALLET_C = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';

export async function run() {
  console.log('\n=== 028: Payment Edge Cases ===');
  const t = new Runner('028_payment_edge_cases');

  const stub = await startChainStub();

  const srv = new TestServer({
    devMode: true, // still use dev build tag
    extraEnv: {
      DEV_MODE: 'false',           // turn off auto-confirm
      DEV_SKIP_PAYMENTS: 'false',  // invoice watcher must check real (stub) API
      MEMPOOL_API: stub.url + '/mempool',
      BLOCKCYPHER_API: stub.url + '/blockcypher',
      INVOICE_WATCH_INTERVAL: '1', // poll every 1s for fast tests
    },
  });

  try {
    await srv.start();

    // Register all wallets directly (balance check would fail without real API)
    const api = new ApiClient(srv.base);
    for (const [wallet, idx] of [[WALLET_A, 0], [WALLET_B, 1], [WALLET_C, 2]]) {
      const token = srv.registerDirect(wallet, 'client', 'BTC');
      api.tokens[wallet] = { token, role: 'client' };
    }

    // Per-scenario wallets so "already have active listing" never triggers
    const scenarioWallets = [
      [WALLET_A, 'BTC'],
      [WALLET_B, 'BTC'],
      [WALLET_C, 'BTC'],
    ];
    let scenarioIndex = 0;

    // Helper: create listing, return { listingId, invoiceId, invoiceAddress, amountSats, senderWallet }
    async function createInvoice() {
      const [wallet] = scenarioWallets[scenarioIndex++];
      const r = await api.createListing(wallet);
      assertStatus(r, 201, 'create listing');
      const inv = r.body;
      // Convert amount_crypto (BTC string "0.00012345") to satoshis
      const amountSats = Math.round(parseFloat(inv.amount_crypto || '0.00050000') * 1e8);
      return {
        listingId: inv.listing_id,
        invoiceId: inv.invoice_id,
        invoiceAddress: inv.address,
        amountSats,
        senderWallet: wallet, // the wallet that will pay (for payer verification)
      };
    }

    // Helper: get listing status
    async function listingStatus(listingId) {
      const r = await api.getListing(listingId);
      return r.body.status;
    }

    // ── (a) недоплата ────────────────────────────────────────────────────────
    await t.run('(a) underpayment: tx at 60% of invoice → NOT confirmed', async () => {
      const inv = await createInvoice();
      const underpay = Math.floor(inv.amountSats * 0.6);

      await stub.setAddressState(inv.invoiceAddress, {
        txs: [{ txid: 'tx-under-' + inv.invoiceId, value_sats: underpay, confirmations: 2, senders: [inv.senderWallet] }],
        balance_sats: 50_000_000, // sender has plenty of BTC (> $150 threshold)
      });
      // Also register sender balance so payer verification passes the balance check
      await stub.setAddressState(inv.senderWallet, { balance_sats: 50_000_000 });

      // Give watcher 4 cycles to check (4s at interval=1)
      await sleep(4000);
      const status = await listingStatus(inv.listingId);
      if (status === 'active') {
        throw new Error(`UNDERPAYMENT: listing confirmed after paying only ${underpay} sats (${(underpay / inv.amountSats * 100).toFixed(0)}%)`);
      }
    });

    // ── (b) две транзакции ───────────────────────────────────────────────────
    await t.run('(b) two txs summing to invoice amount — record actual policy', async () => {
      const inv = await createInvoice();
      const half = Math.ceil(inv.amountSats / 2);

      await stub.setAddressState(inv.invoiceAddress, {
        txs: [
          { txid: 'tx-p1-' + inv.invoiceId, value_sats: half, confirmations: 2, senders: [inv.senderWallet] },
          { txid: 'tx-p2-' + inv.invoiceId, value_sats: inv.amountSats - half, confirmations: 2, senders: [inv.senderWallet] },
        ],
        balance_sats: 50_000_000,
      });
      await stub.setAddressState(inv.senderWallet, { balance_sats: 50_000_000 });

      // Wait up to 10s for watcher to process
      let finalStatus = 'pending_payment';
      const deadline = Date.now() + 10000;
      while (Date.now() < deadline) {
        finalStatus = await listingStatus(inv.listingId);
        if (finalStatus !== 'pending_payment') break;
        await sleep(1000);
      }

      // Policy documentation (not a hard fail — both outcomes are valid):
      if (finalStatus === 'active') {
        console.log('  [info] policy: multi-TX summation IS supported — listing activated');
      } else {
        console.log(`  [info] policy: multi-TX summation NOT supported — status=${finalStatus} (requires single TX)`);
      }
      // Either way, must not be a 5xx-derived crash
    });

    // ── (c) API таймаут → сервер живёт, потом восстанавливается ─────────────
    await t.run('(c) API timeout: server stays alive, confirms after recovery', async () => {
      const inv = await createInvoice();

      // Stage 1: stub times out
      await stub.setMode('timeout');
      await sleep(4000);

      // Server must still respond to health check
      const health = await fetch(srv.base + '/health');
      if (!health.ok) {
        throw new Error(`Server died while blockchain API was timing out (status ${health.status})`);
      }

      // Invoice must still be pending (not confirmed from nowhere)
      const midStatus = await listingStatus(inv.listingId);
      if (midStatus === 'active') {
        throw new Error('Invoice confirmed while API was unavailable — where did the data come from?');
      }

      // Stage 2: stub recovers and tx arrives
      await stub.setMode('ok');
      await stub.setAddressState(inv.invoiceAddress, {
        txs: [{ txid: 'tx-late-' + inv.invoiceId, value_sats: inv.amountSats, confirmations: 2, senders: [inv.senderWallet] }],
        balance_sats: 50_000_000,
      });
      await stub.setAddressState(inv.senderWallet, { balance_sats: 50_000_000 });

      await pollUntil(async () => {
        const s = await listingStatus(inv.listingId);
        return s === 'active' ? true : null;
      }, { timeout: 40000, label: 'listing confirmed after API recovery' });
    });

  } finally {
    await srv.stop();
    await stub.close();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
```

### e2e/tests/031_concurrent_accept.js
```js
// 031_concurrent_accept
// Свойство: клиент отправляет два accept'а на два разных отклика ОДНОВРЕМЕННО
// → ровно один accept проходит, создаётся ровно одна запись с status='accepted'.
// Ловит TOCTOU: check-then-act без атомарной гарантии.
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_A_WALLET = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';
const PEER_B_WALLET = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';

export async function run() {
  console.log('\n=== 031: Concurrent Accept (TOCTOU) ===');
  const srv = new TestServer();
  const t = new Runner('031_concurrent_accept');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    let listingId, respAId, respBId;

    await t.run('register client and two peers', async () => {
      await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      await api.verifyWallet(PEER_A_WALLET, 'BTC', 'peer');
      await api.verifyWallet(PEER_B_WALLET, 'BTC', 'peer');
    });

    await t.run('client creates listing', async () => {
      const r = await api.createListing(CLIENT_WALLET);
      assertStatus(r, 201, 'create listing');
      listingId = r.body.listing_id;
    });

    await t.run('listing becomes active', async () => {
      await pollUntil(async () => {
        const r = await api.getListing(listingId);
        return r.body.status === 'active' ? true : null;
      }, { timeout: 30000, label: 'listing active' });
    });

    await t.run('both peers respond', async () => {
      const rA = await api.respond(listingId, PEER_A_WALLET, newKeypair().pub);
      assertStatus(rA, 201, 'peer A respond');
      respAId = rA.body.response_id;

      const rB = await api.respond(listingId, PEER_B_WALLET, newKeypair().pub);
      assertStatus(rB, 201, 'peer B respond');
      respBId = rB.body.response_id;
    });

    await t.run('client accepts both simultaneously — only one succeeds', async () => {
      const clientPub = newKeypair().pub;

      const [rA, rB] = await Promise.allSettled([
        api.post(`/response/${respAId}/accept`, { client_pubkey: clientPub, currency: 'BTC' }, CLIENT_WALLET),
        api.post(`/response/${respBId}/accept`, { client_pubkey: clientPub, currency: 'BTC' }, CLIENT_WALLET),
      ]);

      const statuses = [rA, rB].map(r =>
        r.status === 'fulfilled' ? r.value.status : 'network-error'
      );
      const okCount = statuses.filter(s => s === 200).length;

      if (okCount !== 1) {
        throw new Error(
          `Expected exactly 1 successful accept, got ${okCount} (statuses: ${statuses.join(', ')})`
        );
      }

      // Loser must not be 5xx
      const loserCode = statuses.find(s => s !== 200 && s !== 'network-error');
      if (loserCode && loserCode >= 500) {
        throw new Error(`Losing accept returned ${loserCode} instead of 4xx`);
      }
    });

    await t.run('DB: exactly one accepted response for listing', async () => {
      const count = parseInt(srv.db(
        `SELECT COUNT(*) FROM responses WHERE listing_id='${listingId}' AND status='accepted'`
      ), 10);
      if (count !== 1) {
        throw new Error(`Expected 1 accepted response, found ${count}`);
      }
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
```

### e2e/tests/032_concurrent_close.js
```js
// 032_concurrent_close
// Свойство: обе стороны закрывают чат ОДНОВРЕМЕННО → ровно один переход
// в status='closed', без дублирования побочных эффектов.
// Идемпотентность close: второй запрос возвращает 200/410, но не 500.
import { TestServer, sleep } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 032: Concurrent Close ===');
  const srv = new TestServer();
  const t = new Runner('032_concurrent_close');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);
    let listingId, responseId, roomId;

    await t.run('register client and peer', async () => {
      await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      await api.verifyWallet(PEER_WALLET,   'BTC', 'peer');
    });

    await t.run('client creates listing and it becomes active', async () => {
      const r = await api.createListing(CLIENT_WALLET);
      assertStatus(r, 201, 'create listing');
      listingId = r.body.listing_id;
      await pollUntil(async () => {
        const s = await api.getListing(listingId);
        return s.body.status === 'active' ? true : null;
      }, { timeout: 30000, label: 'listing active' });
    });

    await t.run('peer responds', async () => {
      const r = await api.respond(listingId, PEER_WALLET, newKeypair().pub);
      assertStatus(r, 201, 'respond');
      responseId = r.body.response_id;
    });

    await t.run('client accepts peer', async () => {
      const r = await api.post(
        `/response/${responseId}/accept`,
        { client_pubkey: newKeypair().pub, currency: 'BTC' },
        CLIENT_WALLET
      );
      assertStatus(r, 200, 'accept');
    });

    await t.run('chat room opens after invoice confirmed', async () => {
      await pollUntil(async () => {
        const r = await api.getListingChatRoom(listingId, CLIENT_WALLET);
        if (r.status === 200 && r.body.room_id) {
          roomId = r.body.room_id;
          return true;
        }
        return null;
      }, { timeout: 30000, label: 'chat room open' });
    });

    await t.run('both sides close simultaneously — no 5xx, room closed exactly once', async () => {
      const [rc, rp] = await Promise.allSettled([
        api.post(`/chat/${roomId}/close`, {}, CLIENT_WALLET),
        api.post(`/chat/${roomId}/close`, {}, PEER_WALLET),
      ]);

      const codes = [rc, rp].map(r => r.status === 'fulfilled' ? r.value.status : 'network-error');

      // Neither response may be 5xx
      for (const c of codes) {
        if (typeof c === 'number' && c >= 500) {
          throw new Error(`Concurrent close returned ${c} — must not be 5xx (codes: ${codes.join(', ')})`);
        }
      }

      // Small wait for DB writes to settle
      await sleep(300);

      // Exactly one closed row in DB
      const count = parseInt(srv.db(
        `SELECT COUNT(*) FROM chat_rooms WHERE id='${roomId}' AND status='closed'`
      ), 10);
      if (count !== 1) {
        throw new Error(`Expected 1 closed room, got ${count}`);
      }
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
```

### e2e/tests/033_devmode_prod_failsafe.js
```js
// 033_devmode_prod_failsafe
// Свойство (приоритет #1 из аудита): прод-сборка НЕ МОЖЕТ работать в DEV_MODE.
// Env-переменная не должна быть достаточной для включения dev-послаблений
// (автоподтверждение инвойсов, пропуск проверки кошелька/баланса).
//
// Отличается от 020: 020 проверяет, что dev-ЗАГОЛОВКИ не утекают в ответах.
// 033 проверяет, что сам dev-РЕЖИМ физически недоступен без dev build tag.
// Это защита на уровне компиляции, а не рантайма.
//
// РЕКОМЕНДУЕМАЯ РЕАЛИЗАЦИЯ (Go build tags):
//   internal/config/devmode_dev.go   //go:build dev      → DevModeAllowed = true
//   internal/config/devmode_prod.go  //go:build !dev     → DevModeAllowed = false
//   При старте: if os.Getenv("DEV_MODE")=="true" && !DevModeAllowed {
//                 log.Fatal("DEV_MODE requested but binary built without -tags dev")
//               }
//   Прод-Makefile собирает `go build` (без -tags dev). Dev/тесты: `go build -tags dev`.
//
// Этот тест — на уровне сборки, а не ApiClient. Он компилирует бэкенд ДВАЖДЫ
// и наблюдает поведение. Запускать в selftest.sh отдельной секцией (нужен go).

import { execFileSync, spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

export const name = '033_devmode_prod_failsafe';

const REPO = process.env.NAROOM_REPO || path.resolve('.'); // ADAPT: корень репо
const MAIN_PKG = './cmd/naroom';                            // ADAPT: путь к main

function build(outPath, tags) {
  const args = ['build', '-o', outPath];
  if (tags) args.push('-tags', tags);
  args.push(MAIN_PKG);
  execFileSync('go', args, { cwd: REPO, stdio: 'pipe' });
}

// Запускает бинарник с DEV_MODE=true и возвращает { exited, code, stdout, stderr, devActive }.
async function probe(binPath) {
  return new Promise((resolve) => {
    const proc = spawn(binPath, [], {
      cwd: REPO,
      env: { ...process.env, DEV_MODE: 'true', PORT: '0' },
    });
    let stdout = '', stderr = '';
    proc.stdout.on('data', d => (stdout += d));
    proc.stderr.on('data', d => (stderr += d));

    // Если процесс сразу упал (log.Fatal) — это ожидаемое прод-поведение.
    const timer = setTimeout(() => {
      // выжил > 2с — считаем, что стартовал; проверим, активен ли dev-режим
      proc.kill('SIGKILL');
      resolve({ exited: false, code: null, stdout, stderr });
    }, 2000);

    proc.on('exit', (code) => {
      clearTimeout(timer);
      resolve({ exited: true, code, stdout, stderr });
    });
  });
}

export async function run() {
  const dir = mkdtempSync(path.join(tmpdir(), 'naroom-build-'));
  const prodBin = path.join(dir, 'naroom-prod');
  const devBin = path.join(dir, 'naroom-dev');

  try {
    // --- прод-сборка (без -tags dev) ---
    build(prodBin, null);
    const prod = await probe(prodBin);

    // Прод-бинарник с DEV_MODE=true обязан ЛИБО упасть с ненулевым кодом,
    // ЛИБО стартовать, но с ВЫКЛЮЧЕННЫМ dev-режимом.
    const prodRefused =
      (prod.exited && prod.code !== 0) ||
      /built without .*dev|DEV_MODE.*(ignored|not allowed|disallowed)/i.test(prod.stderr + prod.stdout);

    if (!prodRefused) {
      // Стартовал молча — надо доказать, что dev-послабления НЕ активны.
      // Признак активного dev-режима: в логах есть маркер, либо (сильнее)
      // инвойс автоподтверждается. Здесь используем лог-маркер как быстрый
      // сигнал; полная проверка — что реальная оплата всё ещё требуется —
      // делается связкой с 028 на прод-бинарнике.
      const devMarker = /DEV_MODE\s*(=|:)?\s*(true|on|enabled|active)/i.test(prod.stdout + prod.stderr);
      if (devMarker) {
        throw new Error(
          'FAIL-SAFE ПРОБИТ: прод-сборка приняла DEV_MODE=true и включила dev-режим ' +
          '(env-переменной достаточно). Нужен build tag / компиляционный гейт.'
        );
      }
      // Нет маркера и не упал — приемлемо, но требует ручной сверки, что
      // послабления действительно off. Помечаем предупреждением в выводе.
      console.warn('[033] прод-бинарник стартовал с DEV_MODE=true без явного маркера; ' +
                   'проверить связкой с 028, что оплата реально требуется.');
    }

    // --- dev-сборка (с -tags dev) — контрольная группа ---
    // Убеждаемся, что dev-режим ВООБЩЕ достижим — иначе тест дал бы ложный
    // PASS даже если dev-код просто удалён (регрессия удобства разработки).
    let devReachable = false;
    try {
      build(devBin, 'dev');
      const dev = await probe(devBin);
      devReachable =
        !(dev.exited && dev.code !== 0) &&
        /DEV_MODE\s*(=|:)?\s*(true|on|enabled|active)/i.test(dev.stdout + dev.stderr);
    } catch (e) {
      // Если -tags dev не собирается — это отдельная проблема сборки, сообщаем.
      throw new Error(`dev-сборка (-tags dev) не компилируется: ${e.message}`);
    }
    if (!devReachable) {
      throw new Error(
        'КОНТРОЛЬ: dev-сборка НЕ включает dev-режим при DEV_MODE=true. ' +
        'Либо build tag настроен неверно, либо dev-путь сломан.'
      );
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}
```

### internal/crypto/verify_test.go
```go
package crypto

import (
	"encoding/base64"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
)

// signMessage signs a message the same way Bitcoin wallets do (compact ECDSA).
func signMessage(privKey *btcec.PrivateKey, magic, message string) string {
	hash := bitcoinMessageHash(magic, message)
	sig, _ := ecdsa.SignCompact(privKey, hash, true) // true = compressed
	return base64.StdEncoding.EncodeToString(sig)
}

func TestVerifyBTCMessage_P2PKH(t *testing.T) {
	// Generate a deterministic test key
	privKey, _ := btcec.PrivKeyFromBytes(make([]byte, 31)) // 31 zero bytes + implied structure
	// Use a proper 32-byte key
	keyBytes := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	privKey, pubKey := btcec.PrivKeyFromBytes(keyBytes[:])
	_ = pubKey

	// Derive P2PKH address
	hash160 := btcutil.Hash160(privKey.PubKey().SerializeCompressed())
	addr, _ := btcutil.NewAddressPubKeyHash(hash160, &chaincfg.MainNetParams)
	address := addr.EncodeAddress()

	message := "test message for signing"
	sig := signMessage(privKey, "Bitcoin Signed Message:\n", message)

	if err := VerifyBTCMessage(address, message, sig); err != nil {
		t.Fatalf("P2PKH verify failed: %v", err)
	}
}

func TestVerifyBTCMessage_P2WPKH(t *testing.T) {
	keyBytes := [32]byte{10, 20, 30, 40, 50}
	privKey, _ := btcec.PrivKeyFromBytes(keyBytes[:])

	hash160 := btcutil.Hash160(privKey.PubKey().SerializeCompressed())
	addr, _ := btcutil.NewAddressWitnessPubKeyHash(hash160, &chaincfg.MainNetParams)
	address := addr.EncodeAddress()

	message := "segwit test message"
	sig := signMessage(privKey, "Bitcoin Signed Message:\n", message)

	if err := VerifyBTCMessage(address, message, sig); err != nil {
		t.Fatalf("P2WPKH verify failed: %v", err)
	}
}

func TestVerifyBTCMessage_WrongAddress(t *testing.T) {
	keyBytes := [32]byte{99}
	privKey, _ := btcec.PrivKeyFromBytes(keyBytes[:])

	// Sign with one key but verify against a different address
	wrongKey := [32]byte{100}
	wrongPriv, _ := btcec.PrivKeyFromBytes(wrongKey[:])
	hash160 := btcutil.Hash160(wrongPriv.PubKey().SerializeCompressed())
	wrongAddr, _ := btcutil.NewAddressPubKeyHash(hash160, &chaincfg.MainNetParams)

	message := "tampered"
	sig := signMessage(privKey, "Bitcoin Signed Message:\n", message)

	if err := VerifyBTCMessage(wrongAddr.EncodeAddress(), message, sig); err == nil {
		t.Fatal("expected error for wrong address, got nil")
	}
}

func TestVerifyBTCMessage_WrongMessage(t *testing.T) {
	keyBytes := [32]byte{55}
	privKey, _ := btcec.PrivKeyFromBytes(keyBytes[:])

	hash160 := btcutil.Hash160(privKey.PubKey().SerializeCompressed())
	addr, _ := btcutil.NewAddressPubKeyHash(hash160, &chaincfg.MainNetParams)

	sig := signMessage(privKey, "Bitcoin Signed Message:\n", "original message")

	if err := VerifyBTCMessage(addr.EncodeAddress(), "tampered message", sig); err == nil {
		t.Fatal("expected error for wrong message, got nil")
	}
}

func TestVerifyLTCMessage_P2PKH(t *testing.T) {
	keyBytes := [32]byte{77, 88, 99}
	privKey, _ := btcec.PrivKeyFromBytes(keyBytes[:])

	hash160 := btcutil.Hash160(privKey.PubKey().SerializeCompressed())
	addr, _ := btcutil.NewAddressPubKeyHash(hash160, ltcMainNetParams)
	address := addr.EncodeAddress()

	message := "litecoin test message"
	sig := signMessage(privKey, "Litecoin Signed Message:\n", message)

	if err := VerifyLTCMessage(address, message, sig); err != nil {
		t.Fatalf("LTC P2PKH verify failed: %v", err)
	}
}

func TestVerifyBTCMessage_InvalidBase64(t *testing.T) {
	if err := VerifyBTCMessage("1abc", "msg", "not-valid-base64!!!"); err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestVerifyBTCMessage_ShortSignature(t *testing.T) {
	shortSig := base64.StdEncoding.EncodeToString([]byte("short"))
	if err := VerifyBTCMessage("1abc", "msg", shortSig); err == nil {
		t.Fatal("expected error for short signature")
	}
}
```

### internal/crypto/encrypt_test.go
```go
package crypto

import (
	"strings"
	"testing"
)

// Test matrix:
// | test                      | invariant                                      | bug it catches                                   |
// |---------------------------|------------------------------------------------|--------------------------------------------------|
// | TestEncryptDecryptRoundTrip | encrypt→decrypt returns original address      | broken cipher, wrong key derivation              |
// | TestDecryptWrongKey        | wrong key → error (not silent garbage)        | key confusion, accidental plain-text fallback    |
// | TestDecryptTamperedData    | bit-flip in ciphertext → auth error           | missing GCM integrity check                      |
// | TestEncryptProducesUnique  | same input → different ciphertext each time   | deterministic nonce (catastrophic GCM failure)   |
// | TestPrepareEncKeyDev       | dev mode without key → derives stable key     | dev mode hard-failing when key not set           |
// | TestPrepareEncKeyProd      | prod mode without key → hard error            | silent plain-text fallback in production         |
// | TestDecryptTooShort        | short ciphertext → error, not panic           | out-of-bounds read on malformed input            |

func testKey() []byte {
	key, _ := PrepareEncKey("test-key-for-unit-tests-only-32b", "", false)
	return key
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	// Invariant: encrypt(key, addr) → decrypt(key, result) == addr
	addresses := []string{
		"1A1zP1eP5QGefi2DMPTfTL5SLmv7Divfna",
		"bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
		"ltc1q3w3pzrh3vs6g87kxpn9a8jdmm5nj60e8w7hnp",
		"LQ3Khqf5HRyRpZXiKrNe1qdQCHxiomKXbV",
	}
	key := testKey()
	for _, addr := range addresses {
		enc, err := EncryptAddress(key, addr)
		if err != nil {
			t.Fatalf("EncryptAddress(%q): %v", addr, err)
		}
		dec, err := DecryptAddress(key, enc)
		if err != nil {
			t.Fatalf("DecryptAddress(%q): %v", addr, err)
		}
		if dec != addr {
			t.Errorf("round-trip failed: got %q, want %q", dec, addr)
		}
	}
}

func TestDecryptWrongKey(t *testing.T) {
	// Invariant: wrong key must return an error — never silently return garbage
	key1 := testKey()
	key2, _ := PrepareEncKey("completely-different-key-here-32", "", false)

	enc, _ := EncryptAddress(key1, "1A1zP1eP5QGefi2DMPTfTL5SLmv7Divfna")
	_, err := DecryptAddress(key2, enc)
	if err == nil {
		t.Fatal("expected error with wrong key, got nil")
	}
}

func TestDecryptTamperedData(t *testing.T) {
	// Invariant: GCM auth tag must catch any bit flip in ciphertext
	key := testKey()
	enc, _ := EncryptAddress(key, "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq")

	// Flip one byte in the middle of the base64
	b := []byte(enc)
	b[len(b)/2] ^= 0xFF
	tampered := string(b)

	_, err := DecryptAddress(key, tampered)
	if err == nil {
		t.Fatal("expected error with tampered ciphertext, got nil")
	}
}

func TestEncryptProducesUnique(t *testing.T) {
	// Invariant: same plaintext → different ciphertext each time (random nonce)
	key := testKey()
	addr := "1A1zP1eP5QGefi2DMPTfTL5SLmv7Divfna"
	enc1, _ := EncryptAddress(key, addr)
	enc2, _ := EncryptAddress(key, addr)
	if enc1 == enc2 {
		t.Fatal("two encryptions of same address produced identical ciphertext — nonce is not random")
	}
}

func TestPrepareEncKeyDev(t *testing.T) {
	// Invariant: dev mode without key → stable derived key (same salt → same key)
	k1, err := PrepareEncKey("", "my-server-salt", true)
	if err != nil {
		t.Fatalf("dev mode PrepareEncKey: %v", err)
	}
	k2, _ := PrepareEncKey("", "my-server-salt", true)
	if string(k1) != string(k2) {
		t.Fatal("dev mode key is not deterministic")
	}
	if len(k1) != 32 {
		t.Fatalf("key length: got %d, want 32", len(k1))
	}
}

func TestPrepareEncKeyProd(t *testing.T) {
	// Invariant: production without key must hard-fail, never fall back to plain text
	_, err := PrepareEncKey("", "my-server-salt", false)
	if err == nil {
		t.Fatal("expected error in production mode without key, got nil")
	}
	if !strings.Contains(err.Error(), "WALLET_ENC_KEY") {
		t.Errorf("error should mention WALLET_ENC_KEY, got: %v", err)
	}
}

func TestDecryptTooShort(t *testing.T) {
	// Invariant: malformed/short input must not panic, must return error
	key := testKey()
	_, err := DecryptAddress(key, "abc")
	if err == nil {
		t.Fatal("expected error for too-short ciphertext, got nil")
	}
}
```

### internal/worker/invoice_watcher_test.go
```go
package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	ncrypto "naroom/internal/crypto"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// mockPrices is a test double for PriceFetcher.
type mockPrices struct {
	btc    float64
	ltc    float64
	btcErr error
	ltcErr error
}

func (m *mockPrices) BTCPrice() (float64, error) { return m.btc, m.btcErr }
func (m *mockPrices) LTCPrice() (float64, error) { return m.ltc, m.ltcErr }

// openTestDB creates a temporary SQLite database with the invoices AND listings tables.
// Both are needed: invoices for all tests, listings to prove confirmInvoice side-effects.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	f, err := os.CreateTemp("", "naroom-iw-test-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	name := f.Name()
	f.Close()
	t.Cleanup(func() { os.Remove(name) })

	db, err := sql.Open("sqlite", name)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE invoices (
			id                  TEXT PRIMARY KEY,
			type                TEXT NOT NULL,
			address             TEXT NOT NULL DEFAULT '',
			amount_usd          REAL NOT NULL DEFAULT 0,
			amount_crypto       TEXT NOT NULL DEFAULT '0',
			currency            TEXT NOT NULL,
			payer_address       TEXT,
			txid                TEXT,
			status              TEXT NOT NULL DEFAULT 'pending',
			listing_id          TEXT,
			response_id         TEXT,
			client_pubkey       TEXT,
			chat_room_id        TEXT,
			payment_detected_at INTEGER,
			price_at_creation   REAL,
			created_at          INTEGER NOT NULL
		);
		CREATE TABLE listings (
			id            TEXT PRIMARY KEY,
			city          TEXT NOT NULL DEFAULT 'tbilisi',
			dependency_type TEXT NOT NULL DEFAULT 'alcohol',
			help_type     TEXT NOT NULL DEFAULT 'crisis',
			urgency       TEXT NOT NULL DEFAULT 'urgent',
			languages     TEXT NOT NULL DEFAULT 'en',
			wallet_hash   TEXT NOT NULL DEFAULT 'test-hash',
			visible_until INTEGER NOT NULL DEFAULT 0,
			created_at    INTEGER NOT NULL DEFAULT 0,
			status        TEXT NOT NULL DEFAULT 'pending'
		)
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}

// insertInvoice inserts a minimal invoice row for testing.
func insertInvoice(t *testing.T, db *sql.DB, id, currency, payerAddress, status string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO invoices (id, type, address, amount_crypto, currency, payer_address, status, created_at)
		 VALUES (?, 'listing', 'test-addr', '0', ?, ?, ?, ?)`,
		id, currency, payerAddress, status, time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("insert invoice %s: %v", id, err)
	}
}

// invoiceStatus reads the current status of an invoice from the test DB.
func invoiceStatus(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM invoices WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatalf("read invoice status %s: %v", id, err)
	}
	return status
}

// newMempoolServer returns an httptest.Server that simulates mempool.space /address/:addr
// responding with the given confirmed balance in satoshis.
func newMempoolServer(t *testing.T, balanceSat int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"chain_stats": map[string]any{
				"funded_txo_sum": balanceSat,
				"spent_txo_sum":  int64(0),
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newErrorServer returns an httptest.Server that always responds with HTTP 503.
func newErrorServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newEmptyTxServer returns an httptest.Server that returns an empty tx list —
// simulating an address with no blockchain activity.
func newEmptyTxServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

const testHashKey = "test-hash-key-for-invoice-watcher"

// ── verifySenderAndBalance tests (DevMode=false) ─────────────────────────────

// IN-3: Empty payer_address → invoice immediately rejected, no blockchain call.
func TestVerify_EmptyPayerAddress(t *testing.T) {
	db := openTestDB(t)
	insertInvoice(t, db, "inv-empty-payer", "BTC", "", "pending")

	iw := &InvoiceWatcher{
		DB: db, HashKey: []byte(testHashKey), DevMode: false,
	}

	got := iw.verifySenderAndBalance("inv-empty-payer", "listing", "BTC", "", []string{"some-addr"}, 0)
	if got {
		t.Fatal("expected false for empty payer_address")
	}
	if s := invoiceStatus(t, db, "inv-empty-payer"); s != "rejected" {
		t.Fatalf("expected status=rejected, got %q", s)
	}
}

// IN-3: No senders in tx inputs → invoice rejected.
func TestVerify_NoSenders(t *testing.T) {
	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "addr-A")
	insertInvoice(t, db, "inv-no-senders", "BTC", payerHash, "pending")

	iw := &InvoiceWatcher{
		DB: db, HashKey: key, DevMode: false,
	}

	got := iw.verifySenderAndBalance("inv-no-senders", "listing", "BTC", payerHash, []string{}, 0)
	if got {
		t.Fatal("expected false for empty senders list")
	}
	if s := invoiceStatus(t, db, "inv-no-senders"); s != "rejected" {
		t.Fatalf("expected status=rejected, got %q", s)
	}
}

// IN-3: Sender hash does not match registered wallet → invoice rejected.
func TestVerify_WrongWallet(t *testing.T) {
	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "addr-A")
	insertInvoice(t, db, "inv-wrong-wallet", "BTC", payerHash, "pending")

	iw := &InvoiceWatcher{
		DB: db, HashKey: key, DevMode: false,
	}

	got := iw.verifySenderAndBalance("inv-wrong-wallet", "listing", "BTC", payerHash, []string{"addr-B"}, 0)
	if got {
		t.Fatal("expected false: sender addr-B does not match registered addr-A")
	}
	if s := invoiceStatus(t, db, "inv-wrong-wallet"); s != "rejected" {
		t.Fatalf("expected status=rejected, got %q", s)
	}
}

// IN-3: Multi-input tx — only one of multiple senders matches → accepted.
func TestVerify_MultiInputOneMatches(t *testing.T) {
	mempoolSrv := newMempoolServer(t, 10_000_000_000) // 100 BTC — well above $135 threshold

	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "addr-A")
	insertInvoice(t, db, "inv-multi-input", "BTC", payerHash, "pending")

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: key,
		Mempool: ncrypto.NewMempoolClient(mempoolSrv.URL),
		Prices:  &mockPrices{btc: 50_000},
		DevMode: false,
	}

	// addr-B first (no match), addr-A second (matches). Any match → accept.
	got := iw.verifySenderAndBalance("inv-multi-input", "listing", "BTC", payerHash,
		[]string{"addr-B", "addr-A"}, 50_000)
	if !got {
		t.Fatal("expected true: addr-A is a valid registered sender")
	}
	// verifySenderAndBalance returns true without setting 'confirmed' — that is confirmInvoice's job.
	if s := invoiceStatus(t, db, "inv-multi-input"); s != "pending" {
		t.Fatalf("expected status=pending after successful verify, got %q", s)
	}
}

// IN-5, IN-6: Balance API returns 503 → false returned, invoice stays 'pending' (not 'rejected').
// API outage must not permanently fail a payment that was already found on-chain.
func TestVerify_APIError_LeavesPending(t *testing.T) {
	errSrv := newErrorServer(t)

	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "addr-A")
	insertInvoice(t, db, "inv-api-error", "BTC", payerHash, "pending")

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: key,
		Mempool: ncrypto.NewMempoolClient(errSrv.URL),
		Prices:  &mockPrices{btc: 50_000},
		DevMode: false,
	}

	got := iw.verifySenderAndBalance("inv-api-error", "listing", "BTC", payerHash,
		[]string{"addr-A"}, 50_000)
	if got {
		t.Fatal("expected false: API error should not confirm the invoice")
	}
	// MUST be 'pending', not 'rejected'. Rejected is permanent; pending allows retry.
	if s := invoiceStatus(t, db, "inv-api-error"); s != "pending" {
		t.Fatalf("expected status=pending after API error, got %q", s)
	}
}

// ── IN-4: Double-confirm guard ────────────────────────────────────────────────

// IN-4: confirmInvoice on an already-confirmed invoice must be a complete no-op.
// Proves:
//  1. txid is not overwritten (WHERE status='pending' guard)
//  2. Linked listing is NOT activated (switch block never entered)
//
// Limitation: does not test chat room side-effects (would require full DB schema).
// Chat path uses the same RowsAffected=0 early-return, so the guard is structural,
// not type-specific. Documented as ⚠️ partial in TEST_MATRIX.md.
func TestDoubleConfirmGuard(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().Unix()

	// Insert a listing in 'pending' state — it should NOT become 'active'.
	_, err := db.Exec(`INSERT INTO listings (id, status, visible_until, created_at)
		VALUES ('list-1', 'pending', 0, ?)`, now)
	if err != nil {
		t.Fatalf("insert listing: %v", err)
	}

	// Invoice is already confirmed — a previous watcher tick confirmed it.
	_, err = db.Exec(`INSERT INTO invoices
		(id, type, address, amount_crypto, currency, status, txid, listing_id, created_at)
		VALUES ('inv-dupe', 'listing', 'test-addr', '0', 'BTC', 'confirmed', 'original-txid', 'list-1', ?)`,
		now)
	if err != nil {
		t.Fatalf("insert pre-confirmed invoice: %v", err)
	}

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: []byte(testHashKey),
		DevMode: false,
		// Prices=nil → balance check skipped. We never reach it anyway (RowsAffected=0 returns early).
	}

	// Simulate a second watcher tick trying to confirm the same invoice.
	iw.confirmInvoice("inv-dupe", "listing", "duplicate-txid", 0, "list-1", "", "")

	// 1. txid must not be overwritten.
	var txid sql.NullString
	db.QueryRow(`SELECT txid FROM invoices WHERE id = 'inv-dupe'`).Scan(&txid) //nolint:errcheck
	if txid.String == "duplicate-txid" {
		t.Fatal("IN-4 FAIL: txid overwritten despite WHERE status='pending' guard")
	}
	if txid.String != "original-txid" {
		t.Fatalf("unexpected txid %q", txid.String)
	}

	// 2. Invoice status must remain 'confirmed'.
	if s := invoiceStatus(t, db, "inv-dupe"); s != "confirmed" {
		t.Fatalf("expected status=confirmed, got %q", s)
	}

	// 3. Listing must NOT have been activated — the side-effect switch block was never entered.
	var listingStatus string
	db.QueryRow(`SELECT status FROM listings WHERE id = 'list-1'`).Scan(&listingStatus) //nolint:errcheck
	if listingStatus == "active" {
		t.Fatal("IN-4 FAIL: listing was activated by a duplicate confirm — switch block entered despite RowsAffected=0")
	}
	if listingStatus != "pending" {
		t.Fatalf("unexpected listing status %q (expected pending)", listingStatus)
	}
}

// ── IN-5: Balance math threshold tests ───────────────────────────────────────
//
// Formula: minUSD = minHold - invoiceCost - 10
//   listing: 150 - 5  - 10 = $135  (client must have at least $135 remaining)
//   chat:    1000 - 15 - 10 = $975  (peer must have at least $975 remaining)
//
// Threshold is strict: balance < minUSD → rejected; balance >= minUSD → true (caller confirms).
// Test price: $100,000/BTC. Satoshi conversions: $135 = 135000 sat, $134.999 = 134999 sat.

// IN-5: listing invoice; sender balance exactly at threshold ($135) → passes.
func TestBalanceThreshold_ListingPassesAt135(t *testing.T) {
	const btcPrice = 100_000.0
	// $135 = 135000 satoshis at $100k/BTC
	const sat135 = int64(135_000)

	mempoolSrv := newMempoolServer(t, sat135)
	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "addr-exact")
	insertInvoice(t, db, "inv-balance-pass", "BTC", payerHash, "pending")

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: key,
		Mempool: ncrypto.NewMempoolClient(mempoolSrv.URL),
		Prices:  &mockPrices{btc: btcPrice},
		DevMode: false,
	}

	got := iw.verifySenderAndBalance("inv-balance-pass", "listing", "BTC", payerHash,
		[]string{"addr-exact"}, btcPrice)
	if !got {
		t.Fatalf("IN-5 FAIL: expected true at exactly $135 (balance=minUSD), got false")
	}
	// verifySenderAndBalance returns true but does not confirm — status stays pending.
	if s := invoiceStatus(t, db, "inv-balance-pass"); s != "pending" {
		t.Fatalf("unexpected status after verify-pass: %q", s)
	}
}

// IN-5: listing invoice; sender balance one cent below threshold ($134.999) → rejected.
func TestBalanceThreshold_ListingFailsAt134(t *testing.T) {
	const btcPrice = 100_000.0
	// $134.999 = 134999 satoshis at $100k/BTC (1 sat below $135 threshold)
	const sat134_999 = int64(134_999)

	mempoolSrv := newMempoolServer(t, sat134_999)
	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "addr-low")
	insertInvoice(t, db, "inv-balance-fail", "BTC", payerHash, "pending")

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: key,
		Mempool: ncrypto.NewMempoolClient(mempoolSrv.URL),
		Prices:  &mockPrices{btc: btcPrice},
		DevMode: false,
	}

	got := iw.verifySenderAndBalance("inv-balance-fail", "listing", "BTC", payerHash,
		[]string{"addr-low"}, btcPrice)
	if got {
		t.Fatalf("IN-5 FAIL: expected false at $134.999 (1 sat below $135 threshold), got true")
	}
	if s := invoiceStatus(t, db, "inv-balance-fail"); s != "rejected" {
		t.Fatalf("IN-5 FAIL: expected status=rejected, got %q", s)
	}
}

// IN-5: chat invoice; sender balance exactly at threshold ($975) → passes.
func TestBalanceThreshold_ChatPassesAt975(t *testing.T) {
	const btcPrice = 100_000.0
	// $975 = 975000 satoshis at $100k/BTC
	const sat975 = int64(975_000)

	mempoolSrv := newMempoolServer(t, sat975)
	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "peer-addr-exact")

	// Chat invoice — must use type='chat' to trigger 1000/15 thresholds
	_, err := db.Exec(
		`INSERT INTO invoices (id, type, address, amount_crypto, currency, payer_address, status, created_at)
		 VALUES ('inv-chat-pass', 'chat', 'test-addr', '0', 'BTC', ?, 'pending', ?)`,
		payerHash, time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("insert chat invoice: %v", err)
	}

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: key,
		Mempool: ncrypto.NewMempoolClient(mempoolSrv.URL),
		Prices:  &mockPrices{btc: btcPrice},
		DevMode: false,
	}

	got := iw.verifySenderAndBalance("inv-chat-pass", "chat", "BTC", payerHash,
		[]string{"peer-addr-exact"}, btcPrice)
	if !got {
		t.Fatalf("IN-5 FAIL: expected true at exactly $975 (chat threshold), got false")
	}
}

// IN-5: chat invoice; sender balance one cent below threshold ($974.999) → rejected.
func TestBalanceThreshold_ChatFailsAt974(t *testing.T) {
	const btcPrice = 100_000.0
	// $974.999 = 974999 satoshis (1 sat below $975 threshold)
	const sat974_999 = int64(974_999)

	mempoolSrv := newMempoolServer(t, sat974_999)
	db := openTestDB(t)
	key := []byte(testHashKey)
	payerHash := ncrypto.WalletHash(key, "peer-addr-low")

	_, err := db.Exec(
		`INSERT INTO invoices (id, type, address, amount_crypto, currency, payer_address, status, created_at)
		 VALUES ('inv-chat-fail', 'chat', 'test-addr', '0', 'BTC', ?, 'pending', ?)`,
		payerHash, time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("insert chat invoice: %v", err)
	}

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: key,
		Mempool: ncrypto.NewMempoolClient(mempoolSrv.URL),
		Prices:  &mockPrices{btc: btcPrice},
		DevMode: false,
	}

	got := iw.verifySenderAndBalance("inv-chat-fail", "chat", "BTC", payerHash,
		[]string{"peer-addr-low"}, btcPrice)
	if got {
		t.Fatalf("IN-5 FAIL: expected false at $974.999 (1 sat below $975 chat threshold), got true")
	}
	if s := invoiceStatus(t, db, "inv-chat-fail"); s != "rejected" {
		t.Fatalf("IN-5 FAIL: expected status=rejected, got %q", s)
	}
}

// ── IN-6: Grace-window expiry tests ──────────────────────────────────────────
//
// These tests exercise the expiry logic inside watch(), not verifySenderAndBalance.
// watch() applies the deadline BEFORE calling any blockchain API.
//
// Deadline rules (from invoice_watcher.go):
//   expiryDeadline = created_at + 3600           (1-hour normal TTL)
//   if payment_detected_at valid:
//       grace = payment_detected_at + 86400       (24-hour grace from detection)
//       expiryDeadline = max(expiryDeadline, grace)
//   if now > expiryDeadline → mark 'expired' and continue

// IN-6a: Normal TTL has passed, but payment was detected and grace window is still open.
// Invoice must NOT be expired.
func TestGraceWindow_NotExpiredWithinGrace(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().Unix()

	// Timestamps:
	//   created_at         = now - 7200  →  normal deadline = now - 7200 + 3600 = now - 3600 (expired 1h ago)
	//   payment_detected_at = now - 1800  →  grace deadline  = now - 1800 + 86400 = now + 84600 (active for 23.5h)
	// Expected: max(now-3600, now+84600) = now+84600 → NOT expired
	createdAt := now - 7200
	detectedAt := now - 1800

	_, err := db.Exec(`INSERT INTO invoices
		(id, type, address, amount_crypto, currency, payer_address, status, payment_detected_at, created_at)
		VALUES ('inv-grace-active', 'listing', 'btc-addr', '0', 'BTC', 'some-hash', 'pending', ?, ?)`,
		detectedAt, createdAt)
	if err != nil {
		t.Fatalf("insert invoice: %v", err)
	}

	// Mock mempool: /address/:addr/txs returns empty list (no new payment to process).
	// watch() will: check expiry (not expired) → call FindPayment → tx=nil → skip.
	txSrv := newEmptyTxServer(t)

	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: []byte(testHashKey),
		Mempool: ncrypto.NewMempoolClient(txSrv.URL),
		Prices:  nil,
		DevMode: false,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	iw.watch(ctx)

	// Invoice must still be 'pending' — not expired, not confirmed (no tx found).
	if s := invoiceStatus(t, db, "inv-grace-active"); s != "pending" {
		t.Fatalf("IN-6 FAIL: expected pending (within grace window), got %q", s)
	}
}

// IN-6b: Both normal TTL and grace window have passed.
// Invoice must be marked 'expired'.
func TestGraceWindow_ExpiredAfterGrace(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().Unix()

	// Timestamps:
	//   created_at          = now - 90000  →  normal deadline = now - 90000 + 3600  = now - 86400 (past)
	//   payment_detected_at = now - 87000  →  grace deadline  = now - 87000 + 86400 = now - 600   (10 min ago, past)
	// Expected: max(now-86400, now-600) = now-600 → EXPIRED (600s ago)
	createdAt := now - 90000
	detectedAt := now - 87000

	_, err := db.Exec(`INSERT INTO invoices
		(id, type, address, amount_crypto, currency, payer_address, status, payment_detected_at, created_at)
		VALUES ('inv-grace-expired', 'listing', 'btc-addr', '0', 'BTC', 'some-hash', 'pending', ?, ?)`,
		detectedAt, createdAt)
	if err != nil {
		t.Fatalf("insert invoice: %v", err)
	}

	// No mock needed for blockchain: expiry check fires before FindPayment.
	iw := &InvoiceWatcher{
		DB:      db,
		HashKey: []byte(testHashKey),
		DevMode: false,
		// Mempool=nil is safe: watch() marks expired via `continue` before reaching FindPayment.
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	iw.watch(ctx)

	if s := invoiceStatus(t, db, "inv-grace-expired"); s != "expired" {
		t.Fatalf("IN-6 FAIL: expected expired (grace window passed), got %q", s)
	}
}
```

## 5. Схема БД

```sql
CREATE TABLE listings (
    id TEXT PRIMARY KEY,
    city TEXT NOT NULL,
    dependency_type TEXT NOT NULL,
    help_type TEXT NOT NULL,
    urgency TEXT NOT NULL,
    languages TEXT NOT NULL,
    wallet_hash TEXT NOT NULL,         -- HMAC-SHA256(HASH_KEY, address); plain address never stored
    payment_txid TEXT,
    visible_until INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    status TEXT DEFAULT 'active',
    is_sample INTEGER DEFAULT 0,        -- 1 = demo listing shown to new visitors
    renewal_count INTEGER DEFAULT 0,    -- how many times renewed
    first_activated_at INTEGER          -- set on first payment, used for 30-day renewal window
);
CREATE TABLE responses (
    id TEXT PRIMARY KEY,
    listing_id TEXT NOT NULL REFERENCES listings(id),
    counselor_hash TEXT NOT NULL,      -- HMAC-SHA256(salt, counselor_address)
    counselor_pubkey TEXT NOT NULL,
    status TEXT DEFAULT 'pending',
    created_at INTEGER NOT NULL,
    cancelled_at INTEGER,
    cooldown_until INTEGER
);
CREATE TABLE invoices (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    address TEXT NOT NULL,
    amount_usd REAL NOT NULL,
    amount_crypto TEXT,
    currency TEXT NOT NULL,
    payer_address TEXT,    -- HMAC-SHA256 hash of sender wallet (plain address never stored)
    txid TEXT,
    status TEXT DEFAULT 'pending',
    listing_id TEXT,      -- для type=listing: что активировать после оплаты
    response_id TEXT,     -- для type=chat: откуда брать counselor
    client_pubkey TEXT,   -- для type=chat: pubkey клиента для E2E
    chat_room_id TEXT,         -- заполняется после создания комнаты (защита от дублей)
    payment_detected_at INTEGER, -- set when payment tx found; extends expiry +24h if API is down
    price_at_creation REAL,      -- USD/coin rate at invoice creation; used at confirmation if favorable
    created_at INTEGER NOT NULL
);
CREATE TABLE wallet_sessions (
    wallet_hash        TEXT PRIMARY KEY,  -- HMAC-SHA256(HASH_KEY, address); plain address never stored here
    wallet_address_enc TEXT NOT NULL,     -- AES-256-GCM encrypted address (nonce||ciphertext, base64url)
    currency           TEXT NOT NULL DEFAULT 'BTC',  -- BTC or LTC; stored to avoid decrypting for type detection
    role               TEXT NOT NULL,
    balance_status     TEXT DEFAULT 'ok',
    min_required_usd   REAL NOT NULL,
    balance_usd        REAL DEFAULT 0,
    last_checked_at    INTEGER,
    low_since          INTEGER,
    verified           BOOLEAN DEFAULT FALSE,
    first_seen         INTEGER NOT NULL,
    created_at         INTEGER NOT NULL
);
CREATE TABLE reputation (
    counselor_hash TEXT PRIMARY KEY,
    region TEXT NOT NULL,
    sessions_total INTEGER DEFAULT 0,
    sessions_completed INTEGER DEFAULT 0,
    sessions_early_exit INTEGER DEFAULT 0,
    thumbs_up INTEGER DEFAULT 0,
    thumbs_down INTEGER DEFAULT 0,
    returning_clients INTEGER DEFAULT 0,
    first_seen INTEGER NOT NULL
);
CREATE TABLE review_tokens (
    token TEXT PRIMARY KEY,
    counselor_hash TEXT NOT NULL,
    is_paid BOOLEAN DEFAULT FALSE,
    used BOOLEAN DEFAULT FALSE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE TABLE abuse_counters (
    client_hash TEXT PRIMARY KEY,
    abuse_misuse INTEGER DEFAULT 0,
    abuse_threatening INTEGER DEFAULT 0,
    abuse_drugs INTEGER DEFAULT 0,
    abuse_links INTEGER DEFAULT 0,
    abuse_other INTEGER DEFAULT 0,
    total INTEGER DEFAULT 0,
    banned_until INTEGER
);
CREATE TABLE abuse_dedup (
    pair_hash TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE TABLE chat_rooms (
    id TEXT PRIMARY KEY,
    listing_id TEXT REFERENCES listings(id),
    response_id TEXT REFERENCES responses(id),
    client_hash TEXT NOT NULL,         -- HMAC-SHA256(salt, client_address)
    counselor_hash TEXT NOT NULL,      -- HMAC-SHA256(salt, counselor_address)
    client_pubkey TEXT NOT NULL,
    counselor_pubkey TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    closed_at INTEGER,
    closed_by TEXT,
    peer_left_at INTEGER,
    status TEXT DEFAULT 'active'
);
CREATE TABLE encrypted_messages (
    id TEXT PRIMARY KEY,
    room_id TEXT REFERENCES chat_rooms(id),
    sender_pubkey TEXT NOT NULL,
    nonce TEXT NOT NULL,
    ciphertext TEXT NOT NULL,
    msg_type TEXT NOT NULL DEFAULT 'text',  -- text | image_file | image_camera
    created_at INTEGER NOT NULL
);
CREATE TABLE telegram_link_tokens (
    id TEXT PRIMARY KEY,
    token TEXT NOT NULL UNIQUE,
    token_type TEXT NOT NULL CHECK (token_type IN ('client', 'helper')),
    listing_id TEXT REFERENCES listings(id),
    helper_filters_json TEXT,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,   -- 10 minutes
    used BOOLEAN DEFAULT FALSE
);
CREATE TABLE client_listing_notifications (
    id TEXT PRIMARY KEY,
    listing_id TEXT NOT NULL REFERENCES listings(id),
    telegram_chat_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,   -- matches listing visible_until (6h)
    active BOOLEAN DEFAULT TRUE
);
CREATE TABLE helper_board_subscriptions (
    id TEXT PRIMARY KEY,
    telegram_chat_id TEXT NOT NULL,
    city TEXT,
    language TEXT,
    problem TEXT,
    help_type TEXT,
    urgency TEXT,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,   -- 24h from creation
    active BOOLEAN DEFAULT TRUE
);
CREATE TABLE invoice_index (
    currency TEXT PRIMARY KEY,
    next_index INTEGER DEFAULT 0
);
CREATE TABLE sessions (
    token_hash    TEXT PRIMARY KEY,
    wallet_hash   TEXT NOT NULL,   -- HMAC-SHA256(HASH_KEY, address); plain address never stored
    currency      TEXT NOT NULL,
    role          TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    last_seen_at  INTEGER,
    revoked_at    INTEGER
);
CREATE INDEX idx_listings_city_status ON listings(city, status);
CREATE INDEX idx_listings_visible ON listings(visible_until);
CREATE INDEX idx_responses_listing ON responses(listing_id);
CREATE INDEX idx_responses_counselor ON responses(counselor_hash);
CREATE INDEX idx_wallet_sessions_hash ON wallet_sessions(wallet_hash);
CREATE INDEX idx_listings_wallet_hash ON listings(wallet_hash);
CREATE INDEX idx_chat_rooms_client_hash ON chat_rooms(client_hash);
CREATE INDEX idx_chat_rooms_counselor_hash ON chat_rooms(counselor_hash);
CREATE INDEX idx_invoices_status ON invoices(status);
CREATE INDEX idx_chat_rooms_status ON chat_rooms(status);
CREATE INDEX idx_chat_rooms_expires ON chat_rooms(expires_at);
CREATE INDEX idx_encrypted_messages_room ON encrypted_messages(room_id);
CREATE INDEX idx_encrypted_messages_created ON encrypted_messages(created_at);
CREATE INDEX idx_review_tokens_expires ON review_tokens(expires_at);
CREATE INDEX idx_abuse_dedup_expires ON abuse_dedup(expires_at);
CREATE INDEX idx_sessions_wallet  ON sessions(wallet_hash, role);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);
CREATE INDEX idx_telegram_link_tokens_token   ON telegram_link_tokens(token);
CREATE INDEX idx_telegram_link_tokens_expires ON telegram_link_tokens(expires_at);
CREATE INDEX idx_client_notifications_listing ON client_listing_notifications(listing_id, active, expires_at);
CREATE INDEX idx_client_notifications_expires ON client_listing_notifications(expires_at);
CREATE INDEX idx_helper_subs_active           ON helper_board_subscriptions(active, expires_at);
CREATE UNIQUE INDEX idx_helper_subs_active_chat
    ON helper_board_subscriptions(telegram_chat_id) WHERE active = TRUE;
```

## 6. Реальные факты о поведении

### go build ./...
```
```

### go vet ./...
```
```

### grep DEV_MODE --include=*.go
```
./cmd/naroom/main.go:27:		log.Fatal("DEV_MODE=true rejected: binary compiled without -tags dev (production build)")
./cmd/naroom/main.go:80:		log.Println("WARNING: DEV_MODE enabled — payments are mocked, do NOT use in production")
./internal/crypto/encrypt.go:84:	return nil, fmt.Errorf("WALLET_ENC_KEY is required in production (DEV_MODE is not set)")
./internal/config/config.go:62:		DevMode: envOr("DEV_MODE", "") == "true",
```

### grep DevMode --include=*.go (struct field usage)
```
./cmd/naroom/main.go:26:	if cfg.DevMode && !config.DevModeAllowed {
./cmd/naroom/main.go:41:	walletEncKey, err := crypto.PrepareEncKey(walletEncKeyStr, cfg.ServerSalt, cfg.DevMode)
./cmd/naroom/main.go:79:	if cfg.DevMode {
./cmd/naroom/main.go:105:		DevMode:     cfg.DevMode,
./cmd/naroom/main.go:135:	if cfg.DevMode {
./cmd/naroom/main.go:140:	requireSession := middleware.RequireSession(database, cfg.DevMode, cfg.HashKey)
./cmd/naroom/main.go:219:		DevMode:      cfg.DevMode,
./cmd/naroom/main.go:220:		SkipPayments: cfg.DevMode || os.Getenv("DEV_SKIP_PAYMENTS") == "true",
./internal/handler/respond.go:34:	// Clients may never respond to listings — enforce role regardless of DevMode.
./internal/handler/respond.go:51:	if !h.DevMode {
./internal/handler/register.go:38:	if h.DevMode {
./internal/handler/handler.go:21:	DevMode     bool
./internal/handler/listing.go:286:	if !h.DevMode {
./internal/handler/chat_ws.go:429:	if h.DevMode {
./internal/config/config.go:26:	DevMode bool
./internal/config/config.go:62:		DevMode: envOr("DEV_MODE", "") == "true",
./internal/config/devmode_dev.go:5:// DevModeAllowed is true in dev/test builds (compiled with -tags dev).
./internal/config/devmode_dev.go:6:const DevModeAllowed = true
./internal/config/devmode_prod.go:5:// DevModeAllowed is false in production builds (no -tags dev).
./internal/config/devmode_prod.go:6:const DevModeAllowed = false
./internal/worker/invoice_watcher_test.go:151:// ── verifySenderAndBalance tests (DevMode=false) ─────────────────────────────
./internal/worker/invoice_watcher_test.go:159:		DB: db, HashKey: []byte(testHashKey), DevMode: false,
./internal/worker/invoice_watcher_test.go:179:		DB: db, HashKey: key, DevMode: false,
./internal/worker/invoice_watcher_test.go:199:		DB: db, HashKey: key, DevMode: false,
./internal/worker/invoice_watcher_test.go:225:		DevMode: false,
./internal/worker/invoice_watcher_test.go:255:		DevMode: false,
./internal/worker/invoice_watcher_test.go:302:		DevMode: false,
./internal/worker/invoice_watcher_test.go:361:		DevMode: false,
./internal/worker/invoice_watcher_test.go:392:		DevMode: false,
./internal/worker/invoice_watcher_test.go:431:		DevMode: false,
./internal/worker/invoice_watcher_test.go:466:		DevMode: false,
./internal/worker/invoice_watcher_test.go:521:		DevMode: false,
./internal/worker/invoice_watcher_test.go:559:		DevMode: false,
./internal/worker/invoice_watcher.go:23:	DevMode      bool
./internal/worker/invoice_watcher.go:24:	SkipPayments bool // auto-confirm all invoices without blockchain checks
./internal/worker/invoice_watcher.go:121:		// Dev mode or SkipPayments: автоматически подтверждаем все pending invoices
./internal/worker/invoice_watcher.go:122:		if iw.DevMode || iw.SkipPayments {
./internal/worker/invoice_watcher.go:191:	if !iw.DevMode {
./internal/worker/invoice_watcher.go:403:	// DevMode: skip all verification, confirm everything
./internal/worker/invoice_watcher.go:404:	if iw.DevMode {
```

### ./scripts/selftest.sh (full output)
```

[1m[1/4] Build[0m
  [0;32m✓[0m go build ./...

[1m[2/4] Unit tests  (go test ./...)[0m
  [0;32m✓[0m all packages  (26 tests, 0 skipped, 2 pkg)

[1m[3/4] Frontend[0m
  [0;32m✓[0m npm run check  (0 errors, 37 warnings)
  [0;32m✓[0m npm run build

[1m[4/4] E2E tests[0m
  [0;32m✓[0m 001_happy_path
  [0;32m✓[0m 002_stale_room_guard
  [0;32m✓[0m 003_role_separation_review
  [0;32m✓[0m 004_remote_close_state
  [0;32m✓[0m 005_large_image_payload
  [0;32m✓[0m 006_state_bleed
  [0;32m✓[0m 007_rate_limiting
  [0;32m✓[0m 008_wallet_challenge
  [0;32m✓[0m 009_session_lifecycle
  [0;32m✓[0m 010_ws_auth
  [0;32m✓[0m 011_peer_left_expiry
  [0;32m✓[0m 013_invoice_scoping
  [0;32m✓[0m 014_reputation
  [0;32m✓[0m 015_region_lock
  [0;32m✓[0m 016_role_separation_respond
  [0;32m✓[0m 017_max_responses
  [0;32m✓[0m 018_balance_threshold
  [0;32m✓[0m 019_renewal_blocked
  [0;32m✓[0m 020_devmode_headers
  [0;32m✓[0m 021_cancel_cooldown
  [0;32m✓[0m 022_message_ttl
  [0;32m✓[0m 023_wallet_session_ttl
  [0;32m✓[0m 024_log_privacy
  [0;31m✗[0m 027_challenge_replay
    node:internal/modules/esm/resolve:271
        throw new ERR_MODULE_NOT_FOUND(
              ^
    
    Error [ERR_MODULE_NOT_FOUND]: Cannot find module '/Users/dmitrijulybin/Projects/shelter/naroom/e2e/lib/testwallets.js' imported from /Users/dmitrijulybin/Projects/shelter/naroom/e2e/tests/027_challenge_replay.js
        at finalizeResolution (node:internal/modules/esm/resolve:271:11)
        at moduleResolve (node:internal/modules/esm/resolve:861:10)
        at defaultResolve (node:internal/modules/esm/resolve:988:11)
        at #cachedDefaultResolve (node:internal/modules/esm/loader:697:20)
        at #resolveAndMaybeBlockOnLoaderThread (node:internal/modules/esm/loader:714:38)
        at ModuleLoader.resolveSync (node:internal/modules/esm/loader:746:52)
        at #resolve (node:internal/modules/esm/loader:679:17)
        at ModuleLoader.getOrCreateModuleJob (node:internal/modules/esm/loader:599:35)
        at ModuleJob.syncLink (node:internal/modules/esm/module_job:162:33)
        at ModuleJob.link (node:internal/modules/esm/module_job:252:17) {
      code: 'ERR_MODULE_NOT_FOUND',
      url: 'file:///Users/dmitrijulybin/Projects/shelter/naroom/e2e/lib/testwallets.js'
    }
    
    Node.js v25.9.0
  [0;32m✓[0m 028_payment_edge_cases
  [0;31m✗[0m 029_ciphertext_only
    node:internal/modules/esm/resolve:271
        throw new ERR_MODULE_NOT_FOUND(
              ^
    
    Error [ERR_MODULE_NOT_FOUND]: Cannot find module '/Users/dmitrijulybin/Projects/shelter/naroom/e2e/lib/db.js' imported from /Users/dmitrijulybin/Projects/shelter/naroom/e2e/tests/029_ciphertext_only.js
        at finalizeResolution (node:internal/modules/esm/resolve:271:11)
        at moduleResolve (node:internal/modules/esm/resolve:861:10)
        at defaultResolve (node:internal/modules/esm/resolve:988:11)
        at #cachedDefaultResolve (node:internal/modules/esm/loader:697:20)
        at #resolveAndMaybeBlockOnLoaderThread (node:internal/modules/esm/loader:714:38)
        at ModuleLoader.resolveSync (node:internal/modules/esm/loader:746:52)
        at #resolve (node:internal/modules/esm/loader:679:17)
        at ModuleLoader.getOrCreateModuleJob (node:internal/modules/esm/loader:599:35)
        at ModuleJob.syncLink (node:internal/modules/esm/module_job:162:33)
        at ModuleJob.link (node:internal/modules/esm/module_job:252:17) {
      code: 'ERR_MODULE_NOT_FOUND',
      url: 'file:///Users/dmitrijulybin/Projects/shelter/naroom/e2e/lib/db.js'
    }
    
    Node.js v25.9.0
  [0;32m✓[0m 030_content_type_spoofing
  [0;32m✓[0m 031_concurrent_accept
  [0;32m✓[0m 032_concurrent_close
  [0;32m✓[0m 033_devmode_prod_failsafe

════════════════════════════════════════
  Unit:      [0;32mPASS[0m
  Frontend:  [0;32mPASS (build)[0m
  E2E:       [0;31m30/32 PASS  (2 FAIL)[0m
  Time:      342s
════════════════════════════════════════
```

## 7. Точные ответы на вопросы аудитора

### Q1: Как задаются base URL для mempool.space и BlockCypher?

**internal/config/config.go** (строки с env):
```go
18:	MempoolAPI     string
19:	BlockcypherAPI string
55:		MempoolAPI:     envOr("MEMPOOL_API", "https://mempool.space/api"),
56:		BlockcypherAPI: envOr("BLOCKCYPHER_API", "https://api.blockcypher.com/v1/ltc/main"),
```

**internal/crypto/mempool.go** (NewMempoolClient):
```go
12:	baseURL    string
13:	httpClient *http.Client
16:func NewMempoolClient(baseURL string) *MempoolClient {
18:		baseURL: baseURL,
19:		httpClient: &http.Client{
20:			Timeout: 15 * time.Second,
27:	url := fmt.Sprintf("%s/address/%s", m.baseURL, address)
33:	resp, err := m.httpClient.Do(req)
61:	url := fmt.Sprintf("%s/address/%s/txs", m.baseURL, address)
62:	resp, err := m.httpClient.Get(url)
```

**cmd/naroom/main.go** (передача URL в клиент):
```go
26:	if cfg.DevMode && !config.DevModeAllowed {
30:	if cfg.ServerSalt == "" {
33:	if len(cfg.HashKey) == 0 {
41:	walletEncKey, err := crypto.PrepareEncKey(walletEncKeyStr, cfg.ServerSalt, cfg.DevMode)
45:	cfg.WalletEncKey = walletEncKey
48:	database, err := db.Open(cfg.DBPath)
63:	mempool := crypto.NewMempoolClient(cfg.MempoolAPI)
64:	blockcypher := crypto.NewBlockcypherClient(cfg.BlockcypherAPI)
68:	wallet, err := crypto.NewHDWallet(database, cfg.BTCXpub, cfg.LTCXpub)
72:	if cfg.BTCXpub == "" {
75:	if cfg.LTCXpub == "" {
79:	if cfg.DevMode {
90:	requireTelegram := cfg.TelegramClientBotToken != "" && cfg.TelegramHelperBotToken != "" && cfg.TelegramWebhookSecret != ""
92:		tgClient = telegram.NewClient(cfg.TelegramClientBotToken, cfg.TelegramHelperBotToken)
99:		HashKey:      cfg.HashKey,
105:		DevMode:     cfg.DevMode,
106:		ListingTTL:  cfg.ListingTTL,
107:		ChatTTL:     cfg.ChatTTL,
108:		ChatMinTTL:  cfg.ChatMinTTL,
112:		TelegramClientBotName: cfg.TelegramClientBotName,
```

### Q2: DEV_MODE — один флаг или разбит?

**Что DevMode=true отключает (все места в хендлерах/воркерах):**
```go
./cmd/naroom/main.go:26:	if cfg.DevMode && !config.DevModeAllowed {
./cmd/naroom/main.go:27:		log.Fatal("DEV_MODE=true rejected: binary compiled without -tags dev (production build)")
./cmd/naroom/main.go:41:	walletEncKey, err := crypto.PrepareEncKey(walletEncKeyStr, cfg.ServerSalt, cfg.DevMode)
./cmd/naroom/main.go:79:	if cfg.DevMode {
./cmd/naroom/main.go:80:		log.Println("WARNING: DEV_MODE enabled — payments are mocked, do NOT use in production")
./cmd/naroom/main.go:105:		DevMode:     cfg.DevMode,
./cmd/naroom/main.go:135:	if cfg.DevMode {
./cmd/naroom/main.go:140:	requireSession := middleware.RequireSession(database, cfg.DevMode, cfg.HashKey)
./cmd/naroom/main.go:219:		DevMode:      cfg.DevMode,
./cmd/naroom/main.go:220:		SkipPayments: cfg.DevMode || os.Getenv("DEV_SKIP_PAYMENTS") == "true",
./internal/handler/respond.go:34:	// Clients may never respond to listings — enforce role regardless of DevMode.
./internal/handler/respond.go:51:	if !h.DevMode {
./internal/handler/register.go:38:	if h.DevMode {
./internal/handler/handler.go:21:	DevMode     bool
./internal/handler/listing.go:286:	if !h.DevMode {
./internal/handler/chat_ws.go:429:	if h.DevMode {
./internal/middleware/session.go:43:// Skipped when devMode is true and the Authorization header is absent — the wallet address
./internal/middleware/session.go:45:func RequireSession(db *sql.DB, devMode bool, hashKey []byte) func(http.Handler) http.Handler {
./internal/middleware/session.go:51:			if devMode && authHeader == "" {
./internal/crypto/encrypt.go:73:func PrepareEncKey(rawKey, serverSalt string, devMode bool) ([]byte, error) {
./internal/crypto/encrypt.go:78:	if devMode {
./internal/crypto/encrypt.go:84:	return nil, fmt.Errorf("WALLET_ENC_KEY is required in production (DEV_MODE is not set)")
./internal/config/config.go:26:	DevMode bool
./internal/config/config.go:62:		DevMode: envOr("DEV_MODE", "") == "true",
./internal/config/devmode_dev.go:5:// DevModeAllowed is true in dev/test builds (compiled with -tags dev).
./internal/config/devmode_dev.go:6:const DevModeAllowed = true
./internal/config/devmode_prod.go:5:// DevModeAllowed is false in production builds (no -tags dev).
./internal/config/devmode_prod.go:6:const DevModeAllowed = false
./internal/worker/invoice_watcher.go:23:	DevMode      bool
./internal/worker/invoice_watcher.go:24:	SkipPayments bool // auto-confirm all invoices without blockchain checks
./internal/worker/invoice_watcher.go:121:		// Dev mode or SkipPayments: автоматически подтверждаем все pending invoices
./internal/worker/invoice_watcher.go:122:		if iw.DevMode || iw.SkipPayments {
./internal/worker/invoice_watcher.go:191:	if !iw.DevMode {
./internal/worker/invoice_watcher.go:403:	// DevMode: skip all verification, confirm everything
./internal/worker/invoice_watcher.go:404:	if iw.DevMode {
```

### Q3: Роуты challenge и verify — точные пути и HTTP-методы

**cmd/naroom/main.go (router setup):**
```go
37:	// Prepare wallet address encryption key (AES-256-GCM).
40:	walletEncKeyStr := os.Getenv("WALLET_ENC_KEY")
41:	walletEncKey, err := crypto.PrepareEncKey(walletEncKeyStr, cfg.ServerSalt, cfg.DevMode)
45:	cfg.WalletEncKey = walletEncKey
54:	// Migrate wallet_sessions to encrypted schema (no-op if already migrated).
55:	if err := db.MigrateWalletEncryption(database, walletEncKey); err != nil {
56:		log.Fatalf("wallet encryption migration: %v", err)
67:	// Init HD wallet (dev mode если xpub не задан)
68:	wallet, err := crypto.NewHDWallet(database, cfg.BTCXpub, cfg.LTCXpub)
70:		log.Fatalf("failed to init HD wallet: %v", err)
100:		WalletEncKey: walletEncKey,
104:		Wallet:      wallet,
125:	rlWalletVerify  := middleware.NewRateLimiter(10.0/60, 10)  // 10/min/IP
151:	r.With(middleware.LimitBody(64*1024), rlWalletVerify.Limit(rateFn)).Post("/wallet/register", h.WalletRegister)
200:		WalletEncKey: walletEncKey,
```

### Q4: Точные пути — listing create, accept, close, upload

```go
149:	r.With(rlBoard.Limit(rateFn)).Get("/board/{city}", h.Board)
150:	r.With(rlGeneral.Limit(rateFn)).Get("/listing/{id}", h.GetListing)
151:	r.With(middleware.LimitBody(64*1024), rlWalletVerify.Limit(rateFn)).Post("/wallet/register", h.WalletRegister)
152:	r.With(requireSession, rlInvoice.Limit(rateFn)).Get("/invoice/{id}/status", h.InvoiceStatus)
153:	r.Get("/api/balance-status", h.BalanceStatus)
156:	r.With(requireSession, middleware.LimitBody(64*1024), rlGeneral.Limit(rateFn)).Post("/listing/create", h.CreateListing)
157:	r.With(requireSession, middleware.LimitBody(64*1024), rlGeneral.Limit(rateFn)).Post("/listing/{id}/renew", h.RenewListing)
158:	r.With(requireSession, rlGeneral.Limit(rateFn)).Get("/listing/{id}/responses", h.GetListingResponses)
159:	r.With(requireSession, rlGeneral.Limit(rateFn)).Get("/listing/{id}/chatroom", h.GetListingChatRoom)
160:	r.With(requireSession, middleware.LimitBody(64*1024), rlRespond.Limit(rateFn)).Post("/listing/{id}/respond", h.Respond)
162:	r.With(requireSession, middleware.LimitBody(64*1024), rlGeneral.Limit(rateFn)).Post("/response/{id}/accept", h.AcceptResponse)
163:	r.With(requireSession, rlGeneral.Limit(rateFn)).Get("/peer/region", h.GetPeerRegion)
164:	r.With(requireSession, rlGeneral.Limit(rateFn)).Get("/peer/chatroom", h.GetCounselorChatRoom)
165:	r.With(requireSession, rlGeneral.Limit(rateFn)).Get("/chat/{room_id}", h.GetChatRoom)
166:	r.Get("/chat/ws", h.ChatWS(hub)) // auth handled inside handler (WS can't send custom headers)
167:	r.With(requireSession, middleware.LimitBody(8*1024*1024), rlGeneral.Limit(rateFn)).Post("/chat/poll/send", h.ChatPollSend)
168:	r.With(requireSession, rlGeneral.Limit(rateFn)).Get("/chat/poll/receive", h.ChatPollReceive)
169:	r.With(requireSession, middleware.LimitBody(64*1024), rlGeneral.Limit(rateFn)).Post("/chat/{room_id}/close", h.CloseChat)
171:	r.With(middleware.LimitBody(64*1024), rlGeneral.Limit(rateFn)).Post("/review", h.Review)
175:	r.With(middleware.LimitBody(64*1024), rlGeneral.Limit(rateFn)).Post("/session/refresh", h.SessionRefresh)
176:	r.With(requireSession, middleware.LimitBody(64*1024)).Post("/session/revoke", h.SessionRevoke)
180:	r.With(requireSession, middleware.LimitBody(4*1024), rlGeneral.Limit(rateFn)).Post("/telegram/helper/token", h.TelegramHelperToken)
182:	r.With(rlGeneral.Limit(rateFn)).Get("/telegram/helper/confirm", h.TelegramHelperConfirm)
185:	r.With(middleware.LimitBody(64*1024)).Post("/telegram/helper/webhook", h.TelegramHelperWebhook)
188:	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
```

### Q5: Challenge — привязан к адресу или «плавающий»?

**Поиск по слову challenge в хендлерах и схеме:**
```
./internal/db/db.go:40:	// wallet_challenges stored plain wallet_address and was never used by any handler.
./internal/db/db.go:42:	db.Exec(`DROP TABLE IF EXISTS wallet_challenges`)
./internal/db/db.go:43:	db.Exec(`DROP INDEX IF EXISTS idx_wallet_challenges_wallet`)
```

Из схемы БД (wallet_sessions таблица — нет колонки challenge):
Вывод: challenge-механизма нет. /wallet/register принимает адрес напрямую, без подписи.

### Q6: UNIQUE-ограничение на активную комнату для listing?

```sql
```

**Поиск UNIQUE в db.go:**
```go
35:	db.Exec(`ALTER TABLE chat_rooms ADD COLUMN peer_left_at INTEGER`)
```
