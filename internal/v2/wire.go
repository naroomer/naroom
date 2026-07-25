package v2

// wire.go — production V2 composition boundary.
//
// V2System bundles all wired services, handlers, and workers. Both
// cmd/naroom/v2wire.go and the release E2E tests use WireV2System so that
// tests exercise the exact same composition path as the production binary.
// No /dev/* endpoints or direct DB mutations are registered here.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// V2Keys holds all derived key material needed for composition.
// Keys are []byte (already decoded from hex by the caller).
type V2Keys struct {
	HMACKey           []byte
	ContactCipher     *AESGCMContactCipher
	DestCipher        *DestinationCipher
	ContactKeyVersion string
}

// V2BotConfig holds Telegram bot identity (no tokens stored here; tokens
// are carried inside the BotAPISender/InformerBotSender implementations).
type V2BotConfig struct {
	ClientBotName         string
	ClientWebhookSecret   []byte
	InformerBotName       string
	InformerWebhookSecret []byte
}

// V2Adapters holds injectable chain/price/allocation/Telegram adapters.
// In production these wrap real external clients; in tests they are stubs.
type V2Adapters struct {
	BTCChain     V2ChainClient
	LTCChain     V2ChainClient
	PriceSource  V2PriceSource
	BTCBalance   V2AtomicBalanceReader
	LTCBalance   V2AtomicBalanceReader
	HDAllocator  V2AddressAllocator
	ClientSender interface {
		BotAPISender
		ReviewNotificationSender
	}
	InformerSender InformerBotSender
	Now            func() time.Time
}

// V2System is the fully assembled V2 subsystem returned by WireV2System.
type V2System struct {
	// HTTP handlers (exported — used by cmd/naroom/v2wire.go and MountRoutes)
	ClientHandler     *ClientHandler
	JourneyHandler    *ClientJourneyHandler
	TelegramLink      *TelegramLinkHandler
	InformerHandler   *InformerHandler
	InformerTransport *InformerTransport
	HelperHandler     *HelperPurchaseHandler
	ReviewHandler     *HelperReviewHandler

	// Workers (exported — goroutines started by cmd/naroom/v2wire.go)
	V2Watcher       *V2Watcher
	HelperWatcher   *HelperPurchaseWatcher
	InformerWorker  *InformerWorker
	LifecycleWorker *LifecycleWorker

	// Unexported: accessible only to package-internal tests (release_e2e_test.go,
	// lifecycle_worker_test.go). Never exported to callers outside this package.
	db          *sql.DB
	svc         *Service
	listingSvc  *ListingService
	helperSvc   *HelperPurchaseService
	reviewSvc   *ReviewService
	informerSvc *InformerService
	destCipher  *DestinationCipher
	now         func() time.Time
	policy      V2BalancePolicy
}

// WireV2System assembles the complete V2 subsystem.
// db must already have V2 schema applied (ApplySchema + MigrateSchema) and
// PRAGMA foreign_keys=ON enforced before calling.
// policy configures all USD balance thresholds; use DefaultV2BalancePolicy() for defaults.
// Returns a non-nil error if any component fails to construct.
func WireV2System(db *sql.DB, keys V2Keys, bots V2BotConfig, adapters V2Adapters, policy V2BalancePolicy) (*V2System, error) {
	now := adapters.Now
	if now == nil {
		now = time.Now
	}

	// ── Balance reader ────────────────────────────────────────────────────────
	balReader := NewV2BalanceReader(adapters.BTCBalance, adapters.LTCBalance, adapters.PriceSource)

	// ── Invoice issuers ────────────────────────────────────────────────────────
	clientIssuer := NewV2InvoiceIssuer(adapters.HDAllocator, adapters.PriceSource)
	helperIssuer := NewV2HelperInvoiceIssuer(adapters.HDAllocator, adapters.PriceSource)

	// ── Chain clients map ────────────────────────────────────────────────────
	chainClients := map[string]V2ChainClient{
		"BTC": adapters.BTCChain,
		"LTC": adapters.LTCChain,
	}

	// ── Core payment service ──────────────────────────────────────────────────
	svc, err := NewWithClock(db, keys.HMACKey, now)
	if err != nil {
		return nil, fmt.Errorf("v2: WireV2System: payment service: %w", err)
	}
	svc.SetPolicy(policy)
	aliases := NewRandomAliasGenerator()
	svc.SetAliasGenerator(aliases)

	// ── Display names + contact validator ─────────────────────────────────────
	names := NewRandomDisplayNameGenerator()
	cv := NewProductionContactValidator()

	// ── Listing service ────────────────────────────────────────────────────────
	listingSvc, err := NewListingService(svc, keys.ContactCipher, names, cv)
	if err != nil {
		return nil, fmt.Errorf("v2: WireV2System: listing service: %w", err)
	}
	listingSvc.SetPolicy(policy)

	// ── Helper purchase service ────────────────────────────────────────────────
	helperSvc, err := NewHelperPurchaseService(db, keys.HMACKey, keys.ContactCipher, names, now)
	if err != nil {
		return nil, fmt.Errorf("v2: WireV2System: helper service: %w", err)
	}
	helperSvc.SetPolicy(policy)

	// ── Review service ────────────────────────────────────────────────────────
	reviewSvc, err := NewReviewService(db, keys.HMACKey, now)
	if err != nil {
		return nil, fmt.Errorf("v2: WireV2System: review service: %w", err)
	}

	// ── Informer service ──────────────────────────────────────────────────────
	informerSvc, err := NewInformerService(db, keys.HMACKey, keys.HMACKey, keys.DestCipher, now)
	if err != nil {
		return nil, fmt.Errorf("v2: WireV2System: informer service: %w", err)
	}
	informerSvc.SetPolicy(policy)
	listingSvc.SetInformerNotifier(informerSvc)

	// ── Telegram client transport ──────────────────────────────────────────────
	telegramTransport, err := NewTelegramTransport(
		svc, listingSvc, keys.HMACKey,
		bots.ClientWebhookSecret,
		keys.DestCipher,
		bots.ClientBotName,
		adapters.ClientSender,
		now,
	)
	if err != nil {
		return nil, fmt.Errorf("v2: WireV2System: telegram transport: %w", err)
	}
	telegramTransport.SetReviewService(reviewSvc, adapters.ClientSender)

	// ── Informer transport ─────────────────────────────────────────────────────
	informerTransport, err := NewInformerTransport(
		informerSvc, bots.InformerWebhookSecret, bots.InformerBotName, now,
	)
	if err != nil {
		return nil, fmt.Errorf("v2: WireV2System: informer transport: %w", err)
	}
	informerTransport.SetSender(adapters.InformerSender)

	// ── HTTP handlers ──────────────────────────────────────────────────────────
	clientHandler, err := NewClientHandler(svc, clientIssuer, balReader, keys.HMACKey, now)
	if err != nil {
		return nil, fmt.Errorf("v2: WireV2System: client handler: %w", err)
	}
	clientHandler.SetPolicy(policy)

	journeyHandler, err := NewClientJourneyHandler(svc, listingSvc, telegramTransport, balReader, keys.HMACKey, now)
	if err != nil {
		return nil, fmt.Errorf("v2: WireV2System: journey handler: %w", err)
	}

	telegramLinkHandler, err := NewTelegramLinkHandler(svc, telegramTransport, keys.HMACKey, now)
	if err != nil {
		return nil, fmt.Errorf("v2: WireV2System: telegram link handler: %w", err)
	}

	informerHandler, err := NewInformerHandler(informerSvc, balReader, bots.InformerBotName, keys.HMACKey, now)
	if err != nil {
		return nil, fmt.Errorf("v2: WireV2System: informer handler: %w", err)
	}

	helperHandler, err := NewHelperPurchaseHandler(helperSvc, helperIssuer, balReader, keys.HMACKey, now)
	if err != nil {
		return nil, fmt.Errorf("v2: WireV2System: helper handler: %w", err)
	}

	reviewHandler, err := NewHelperReviewHandler(reviewSvc, keys.HMACKey, now)
	if err != nil {
		return nil, fmt.Errorf("v2: WireV2System: review handler: %w", err)
	}

	// ── Workers ────────────────────────────────────────────────────────────────
	v2Watcher, err := NewV2Watcher(svc, chainClients, balReader, now, nil, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("v2: WireV2System: v2 watcher: %w", err)
	}
	v2Watcher.SetPolicy(policy)

	helperWatcher, err := NewHelperPurchaseWatcher(helperSvc, chainClients, balReader, now, nil, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("v2: WireV2System: helper watcher: %w", err)
	}

	informerWorker := NewInformerWorker(informerSvc, adapters.InformerSender)
	lifecycleWorker := NewLifecycleWorker(listingSvc, helperSvc, telegramTransport, now)

	return &V2System{
		ClientHandler:     clientHandler,
		JourneyHandler:    journeyHandler,
		TelegramLink:      telegramLinkHandler,
		InformerHandler:   informerHandler,
		InformerTransport: informerTransport,
		HelperHandler:     helperHandler,
		ReviewHandler:     reviewHandler,
		V2Watcher:         v2Watcher,
		HelperWatcher:     helperWatcher,
		InformerWorker:    informerWorker,
		LifecycleWorker:   lifecycleWorker,
		// Unexported — for package-internal test access only.
		db:          db,
		svc:         svc,
		listingSvc:  listingSvc,
		helperSvc:   helperSvc,
		reviewSvc:   reviewSvc,
		informerSvc: informerSvc,
		destCipher:  keys.DestCipher,
		now:         now,
		policy:      policy,
	}, nil
}

// MountRoutes registers all 21 V2 HTTP routes on r (an http.ServeMux or chi router
// that satisfies the pattern-based routing interface). Uses net/http for simplicity;
// the production binary wraps this via chi.
// Pass an *http.ServeMux as router for test servers.
func (sys *V2System) MountRoutes(mux *http.ServeMux) {
	cMux := sys.ClientHandler.Routes()
	jMux := sys.JourneyHandler.Routes()
	tMux := sys.TelegramLink.Routes()
	iMux := sys.InformerHandler.Routes()
	iwMux := sys.InformerTransport.Routes()
	hMux := sys.HelperHandler.Routes()
	rMux := sys.ReviewHandler.Routes()

	// Client payment intents
	mux.HandleFunc("POST /v2/client/payment-intents", cMux.ServeHTTP)
	mux.HandleFunc("POST /v2/client/payment-intents/restore", cMux.ServeHTTP)
	mux.HandleFunc("POST /v2/client/payment-intents/recheck-balance", cMux.ServeHTTP)

	// Client listing journey
	mux.HandleFunc("POST /v2/client/listings/restore", jMux.ServeHTTP)
	mux.HandleFunc("POST /v2/client/listings/publish", jMux.ServeHTTP)
	mux.HandleFunc("POST /v2/client/listings/reactivate", jMux.ServeHTTP)
	mux.HandleFunc("GET /v2/board/{city}", jMux.ServeHTTP)
	mux.HandleFunc("GET /v2/listings/{listing_id}", jMux.ServeHTTP)

	// Telegram Client bot deep-link + webhook
	mux.HandleFunc("POST /v2/client/telegram-links", tMux.ServeHTTP)
	mux.HandleFunc("POST /v2/client/telegram-links/status", tMux.ServeHTTP)
	mux.HandleFunc("POST /v2/telegram/client/webhook", tMux.ServeHTTP)

	// Informer subscription API
	mux.HandleFunc("POST /v2/informer/access", iMux.ServeHTTP)
	mux.HandleFunc("POST /v2/informer/status", iMux.ServeHTTP)

	// Informer Telegram webhook
	mux.HandleFunc("POST /v2/telegram/informer/webhook", iwMux.ServeHTTP)

	// Helper contact purchases
	mux.HandleFunc("POST /v2/helper/contact-purchases", hMux.ServeHTTP)
	mux.HandleFunc("POST /v2/helper/contact-purchases/restore", hMux.ServeHTTP)
	mux.HandleFunc("POST /v2/helper/contact-purchases/recheck-balance", hMux.ServeHTTP)
	mux.HandleFunc("POST /v2/helper/contact-purchases/reveal", hMux.ServeHTTP)

	// Helper reviews
	mux.HandleFunc("POST /v2/helper/reviews/capability", rMux.ServeHTTP)
	mux.HandleFunc("POST /v2/helper/reviews", rMux.ServeHTTP)

	// V2 public configuration endpoint — returns all configurable balance thresholds.
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
		p := sys.policy
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(resp{ //nolint:errcheck
			ClientFeeUSDCents:       500,
			ClientPublicMinUSD:      p.ClientPublicMinUSD,
			ClientHardFloorUSD:      p.ClientHardFloorUSD,
			HelperFeeUSDCents:       1000,
			HelperPreInvoiceMinUSD:  p.HelperPreInvoiceMinUSD(),
			HelperPostPaymentMinUSD: p.HelperPostPaymentMinUSD,
			InformerMinUSD:          p.InformerMinUSD,
		})
	})

	// V2 readiness endpoint (separate from V1 /health)
	mux.HandleFunc("GET /v2/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"v2":"ready"}`)) //nolint:errcheck
	})
}
