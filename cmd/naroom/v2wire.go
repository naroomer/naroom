package main

// V2 production composition module.
//
// wireV2 mounts all approved /v2/... routes and starts V2 workers when
// V2_ENABLED=true. When V2_ENABLED=false (default) this function is a no-op
// and the binary behaves exactly like the V1-only binary.
//
// Secrets fail-fast: if V2_ENABLED=true and any required secret is missing or
// malformed, wireV2 returns an error and main calls log.Fatal before serving.
//
// Production safety:
//   - No dev-endpoint simulation handlers are registered here.
//   - No seed/demo rows are inserted.
//   - V2 workers have bounded intervals, context cancellation, and clean shutdown.
//   - V1 /health contract preserved byte-for-byte (plaintext "ok", no Content-Type).
//   - V2 readiness exposed at /v2/health only when V2_ENABLED=true.

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"naroom/internal/config"
	ncrypto "naroom/internal/crypto"
	"naroom/internal/v2"
)

// wireV2 wires all V2 services, HTTP handlers and workers onto r.
// Returns nil immediately when V2_ENABLED=false.
// Returns a non-nil error if V2_ENABLED=true and any required secret is missing
// or invalid — caller must log.Fatal before accepting connections.
func wireV2(
	ctx context.Context,
	r *chi.Mux,
	cfg *config.Config,
	db *sql.DB,
	wallet *ncrypto.HDWallet,
	prices *ncrypto.PriceCache,
	mempool *ncrypto.MempoolClient,
	blockcypher *ncrypto.BlockcypherClient,
) error {
	if !cfg.V2Enabled {
		return nil
	}

	// ── Fail-fast secret validation ──────────────────────────────────────────
	if err := validateV2Secrets(cfg); err != nil {
		return fmt.Errorf("v2wire: %w", err)
	}

	// ── Decode hex keys ──────────────────────────────────────────────────────
	hmacKey, err := hex.DecodeString(cfg.V2HMACKey)
	if err != nil || len(hmacKey) != 32 {
		return fmt.Errorf("v2wire: V2_HMAC_KEY must be 64 lowercase hex chars (32 bytes)")
	}

	contactKeyBytes, err := hex.DecodeString(cfg.V2ContactEncKey)
	if err != nil || len(contactKeyBytes) != 32 {
		return fmt.Errorf("v2wire: V2_CONTACT_ENC_KEY must be 64 lowercase hex chars (32 bytes)")
	}

	destKeyBytes, err := hex.DecodeString(cfg.V2DestEncKey)
	if err != nil || len(destKeyBytes) != 32 {
		return fmt.Errorf("v2wire: V2_DEST_ENC_KEY must be 64 lowercase hex chars (32 bytes)")
	}

	// ── Enable foreign key enforcement on every connection ──────────────────
	// internal/db.Open() already sets _foreign_keys=ON in the DSN and
	// SetMaxOpenConns(1). The explicit PRAGMA here is belt-and-suspenders:
	// it guarantees FK enforcement even if the DSN parameter is not honoured
	// by the current modernc.org/sqlite version.
	if _, fkErr := db.Exec("PRAGMA foreign_keys = ON"); fkErr != nil {
		return fmt.Errorf("v2wire: enable foreign keys: %w", fkErr)
	}

	// ── Apply V2 schema (idempotent) ─────────────────────────────────────────
	if schErr := v2.ApplySchema(db); schErr != nil {
		return fmt.Errorf("v2wire: apply schema: %w", schErr)
	}
	if migErr := v2.MigrateSchema(db); migErr != nil {
		return fmt.Errorf("v2wire: migrate schema: %w", migErr)
	}

	// ── Build V2 balance policy from config ───────────────────────────────────
	policy := v2.V2BalancePolicy{
		ClientPublicMinUSD:      cfg.V2ClientPublicMinBalanceUSD,
		ClientHardFloorUSD:      cfg.V2ClientHardFloorUSD,
		HelperPostPaymentMinUSD: cfg.V2HelperPostPaymentMinBalanceUSD,
		InformerMinUSD:          cfg.V2InformerMinBalanceUSD,
	}
	if policy.ClientPublicMinUSD <= 0 || policy.ClientHardFloorUSD <= 0 ||
		policy.HelperPostPaymentMinUSD <= 0 || policy.InformerMinUSD <= 0 {
		return fmt.Errorf("v2wire: V2 balance policy values must all be positive")
	}
	if policy.ClientPublicMinUSD < policy.ClientHardFloorUSD {
		return fmt.Errorf("v2wire: V2_CLIENT_PUBLIC_MIN_BALANCE_USD must be >= V2_CLIENT_HARD_FLOOR_USD")
	}

	// ── Build crypto primitives ───────────────────────────────────────────────
	contactCipher, err := v2.NewAESGCMContactCipher(contactKeyBytes, cfg.V2ContactKeyVersion)
	if err != nil {
		return fmt.Errorf("v2wire: contact cipher: %w", err)
	}

	destCipher, err := v2.NewDestinationCipher(destKeyBytes, cfg.V2DestKeyVersion)
	if err != nil {
		return fmt.Errorf("v2wire: dest cipher: %w", err)
	}

	// ── Build production adapters ─────────────────────────────────────────────
	btcChain := v2.NewMempoolV2Adapter(cfg.MempoolAPI)
	ltcChain := v2.NewFallbackV2ChainClient(
		v2.NewBlockcypherV2Adapter(cfg.BlockcypherAPI, cfg.BlockcypherToken),
		v2.NewMempoolV2Adapter("https://litecoinspace.org/api"),
	)
	priceAdap := v2.NewPriceCacheV2Adapter(prices)
	btcBal := v2.NewMempoolBalanceAdapter(mempool)
	ltcBal := v2.NewFallbackV2AtomicBalanceReader(
		v2.NewBlockcypherBalanceAdapter(blockcypher),
		v2.NewMempoolBalanceAdapter(ncrypto.NewMempoolClient("https://litecoinspace.org/api")),
	)
	hdAlloc := v2.NewHDAllocatorAdapter(wallet)

	// ── Build Telegram bot senders ─────────────────────────────────────────────
	clientBotSender, err := v2.NewHTTPBotAPISender(
		"https://api.telegram.org",
		cfg.V2ClientBotToken,
		&http.Client{Timeout: 15 * time.Second},
	)
	if err != nil {
		return fmt.Errorf("v2wire: client bot sender: %w", err)
	}

	informerBotSender, err := v2.NewTelegramInformerBotSender(cfg.V2InformerBotToken)
	if err != nil {
		return fmt.Errorf("v2wire: informer bot sender: %w", err)
	}

	// ── Assemble V2 system via production composition boundary ────────────────
	// WireV2System is also called by release E2E tests with fake adapters,
	// guaranteeing tests exercise the exact same composition path.
	sys, err := v2.WireV2System(
		db,
		v2.V2Keys{
			HMACKey:           hmacKey,
			ContactCipher:     contactCipher,
			DestCipher:        destCipher,
			ContactKeyVersion: cfg.V2ContactKeyVersion,
		},
		v2.V2BotConfig{
			ClientBotName:         cfg.V2ClientBotName,
			ClientWebhookSecret:   []byte(cfg.V2ClientWebhookSecret),
			InformerBotName:       cfg.V2InformerBotName,
			InformerWebhookSecret: []byte(cfg.V2InformerWebhookSecret),
			PublicBaseURL:         cfg.PublicBaseURL,
		},
		v2.V2Adapters{
			BTCChain:       btcChain,
			LTCChain:       ltcChain,
			PriceSource:    priceAdap,
			BTCBalance:     btcBal,
			LTCBalance:     ltcBal,
			HDAllocator:    hdAlloc,
			ClientSender:   clientBotSender,
			InformerSender: informerBotSender,
			Now:            time.Now,
		},
		policy,
	)
	if err != nil {
		return fmt.Errorf("v2wire: wire system: %w", err)
	}

	// ── Mount V2 routes onto chi ──────────────────────────────────────────────
	mountV2Routes(r, sys)

	// ── Start V2 workers ──────────────────────────────────────────────────────
	go sys.V2Watcher.Run(ctx)
	go sys.HelperWatcher.Run(ctx)
	go runInformerWorker(ctx, sys.InformerWorker)
	go sys.LifecycleWorker.Run(ctx, v2.NormalizeInterval, v2.ReviewInterval)

	return nil
}

// mountV2Routes registers all V2 HTTP routes on the chi router.
// Single source of truth: delegates to V2System.MountRoutes (which uses net/http
// ServeMux pattern syntax internally), then bridges each pattern to chi by mounting
// the entire ServeMux as a chi catch-all under /v2/.
//
// This eliminates route duplication: cmd/naroom never lists individual routes.
// Any route added to V2System.MountRoutes is automatically available here.
func mountV2Routes(r *chi.Mux, sys *v2.V2System) {
	mux := http.NewServeMux()
	sys.MountRoutes(mux)
	// Forward all /v2/* and /v2/health traffic to the ServeMux.
	// chi does NOT strip the path prefix when using Handle (unlike Mount),
	// so mux receives the full URL (e.g. /v2/board/tbilisi) and its own
	// route patterns (registered as "GET /v2/board/{city}") match correctly.
	r.Handle("/v2/*", mux)
	r.Handle("/v2/health", mux) // exact /v2/health (no trailing segment for /* to match)
}

// runInformerWorker polls the Informer outbox every 30 seconds.
// Context cancellation stops the worker cleanly after the current RunOnce completes.
// Non-overlapping: ticker fires only after the previous RunOnce returns.
func runInformerWorker(ctx context.Context, w *v2.InformerWorker) {
	// Initial run immediately at startup.
	if ctx.Err() == nil {
		_ = w.RunOnce(ctx)
	}
	ticker := time.NewTicker(30 * time.Second)
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

// validateV2Secrets checks that all required V2 secrets are present when V2 is enabled.
func validateV2Secrets(cfg *config.Config) error {
	type field struct {
		envVar string
		value  string
	}
	required := []field{
		{"V2_HMAC_KEY", cfg.V2HMACKey},
		{"V2_CONTACT_ENC_KEY", cfg.V2ContactEncKey},
		{"V2_CONTACT_KEY_VERSION", cfg.V2ContactKeyVersion},
		{"V2_DEST_ENC_KEY", cfg.V2DestEncKey},
		{"V2_DEST_KEY_VERSION", cfg.V2DestKeyVersion},
		{"V2_CLIENT_BOT_TOKEN", cfg.V2ClientBotToken},
		{"V2_CLIENT_BOT_NAME", cfg.V2ClientBotName},
		{"V2_CLIENT_WEBHOOK_SECRET", cfg.V2ClientWebhookSecret},
		{"V2_INFORMER_BOT_TOKEN", cfg.V2InformerBotToken},
		{"V2_INFORMER_BOT_NAME", cfg.V2InformerBotName},
		{"V2_INFORMER_WEBHOOK_SECRET", cfg.V2InformerWebhookSecret},
	}
	var missing []string
	for _, f := range required {
		if f.value == "" {
			missing = append(missing, f.envVar)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("V2_ENABLED=true requires these environment variables to be set: %v", missing)
	}
	return nil
}
