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

	MempoolAPI       string
	BlockcypherAPI   string
	BlockcypherToken string // optional API token

	BalanceCheckInterval int // seconds
	TTLCleanInterval     int
	InvoiceWatchInterval int

	// Dev mode: mock payments, no real blockchain checks
	DevMode       bool
	DevSeedPrices bool // seed fixed prices ($100k BTC, $100 LTC) without enabling full DevMode

	// Configurable TTLs for testing (seconds)
	ListingTTL int // default 21600 (6h)
	ChatTTL    int // default 86400 (24h)
	ChatMinTTL int // minimum chat duration for rating (default 21600 = 6h)

	// Balance thresholds for wallet registration and post-payment verification.
	// Defaults: ClientMinBalanceUSD=150, PeerMinBalanceUSD=1000.
	// Override via CLIENT_MIN_BALANCE_USD / PEER_MIN_BALANCE_USD env vars.
	// Lower values (e.g. 50) can be set temporarily for smoke-testing with limited funds.
	ClientMinBalanceUSD float64
	PeerMinBalanceUSD   float64

	// Telegram notification bots (optional; both must be set to enable)
	TelegramClientBotToken string
	TelegramHelperBotToken string
	TelegramWebhookSecret  string
	TelegramClientBotName  string // e.g. "NARoomClientBot"
	TelegramHelperBotName  string // e.g. "NARoomHelperBot"
	PublicBaseURL          string // e.g. "https://naroom.net"

	// ── V2 feature flag and secrets ────────────────────────────────────────────
	// All fields below are only required (and validated fail-fast) when V2Enabled=true.

	// V2Enabled activates V2 routes, workers and schema migrations.
	// OFF (default): V2 code is compiled but no routes/workers are registered.
	// ON: all approved /v2/... API routes and V2 workers are mounted.
	V2Enabled bool

	// V2HMACKey is the HMAC-SHA256 secret for V2 wallet fingerprinting, rate-limit
	// key derivation, token authentication, and Informer subscription lookup.
	// Required: 64 hex chars (32 bytes). Domain-separated from V1 keys.
	V2HMACKey string

	// V2 contact encryption (AES-256-GCM for listing contact handles).
	V2ContactEncKey     string // 64 hex chars (32 bytes)
	V2ContactKeyVersion string // non-empty version label, e.g. "v1"

	// V2 destination encryption (AES-256-GCM for Telegram chat_id at rest).
	V2DestEncKey     string // 64 hex chars (32 bytes)
	V2DestKeyVersion string // non-empty version label, e.g. "v1"

	// V2 Client notification bot (separate from V1 Telegram bots).
	V2ClientBotToken      string // Telegram bot token for V2 client binding/review delivery
	V2ClientBotName       string // bot username, must end in "bot"
	V2ClientWebhookSecret string // Telegram webhook secret for V2 client bot

	// V2 Informer bot.
	V2InformerBotToken      string // Telegram bot token for Informer outbox delivery
	V2InformerBotName       string // bot username, must end in "bot"
	V2InformerWebhookSecret string // Telegram webhook secret for Informer bot

	// V2 configurable balance thresholds (optional; fail-fast validation if invalid).
	V2ClientPublicMinBalanceUSD      float64
	V2ClientHardFloorUSD             float64
	V2HelperPostPaymentMinBalanceUSD float64
	V2InformerMinBalanceUSD          float64
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

		MempoolAPI:       envOr("MEMPOOL_API", "https://mempool.space/api"),
		BlockcypherAPI:   envOr("BLOCKCYPHER_API", "https://api.blockcypher.com/v1/ltc/main"),
		BlockcypherToken: envOr("BLOCKCYPHER_TOKEN", ""),

		BalanceCheckInterval: envInt("BALANCE_CHECK_INTERVAL", 600),
		TTLCleanInterval:     envInt("TTL_CLEAN_INTERVAL", 60),
		InvoiceWatchInterval: envInt("INVOICE_WATCH_INTERVAL", 30),

		DevMode:       envOr("DEV_MODE", "") == "true",
		DevSeedPrices: envOr("DEV_SEED_PRICES", "") == "true",

		ListingTTL: envInt("LISTING_TTL", 86400),  // 24h
		ChatTTL:    envInt("CHAT_TTL", 86400),     // 24h
		ChatMinTTL: envInt("CHAT_MIN_TTL", 21600), // 6h minimum for rating

		ClientMinBalanceUSD: envFloat("CLIENT_MIN_BALANCE_USD", 150.0),
		PeerMinBalanceUSD:   envFloat("PEER_MIN_BALANCE_USD", 1000.0),

		TelegramClientBotToken: envOr("TELEGRAM_CLIENT_BOT_TOKEN", ""),
		TelegramHelperBotToken: envOr("TELEGRAM_HELPER_BOT_TOKEN", ""),
		TelegramWebhookSecret:  envOr("TELEGRAM_WEBHOOK_SECRET", ""),
		TelegramClientBotName:  envOr("TELEGRAM_CLIENT_BOT_NAME", "NARoomClientBot"),
		TelegramHelperBotName:  envOr("TELEGRAM_HELPER_BOT_NAME", "NARoomHelperBot"),
		PublicBaseURL:          envOr("PUBLIC_BASE_URL", "https://naroom.net"),

		// V2 feature flag and secrets.
		V2Enabled: envOr("V2_ENABLED", "") == "true",

		V2HMACKey: envOr("V2_HMAC_KEY", ""),

		V2ContactEncKey:     envOr("V2_CONTACT_ENC_KEY", ""),
		V2ContactKeyVersion: envOr("V2_CONTACT_KEY_VERSION", ""),

		V2DestEncKey:     envOr("V2_DEST_ENC_KEY", ""),
		V2DestKeyVersion: envOr("V2_DEST_KEY_VERSION", ""),

		V2ClientBotToken:      envOr("V2_CLIENT_BOT_TOKEN", ""),
		V2ClientBotName:       envOr("V2_CLIENT_BOT_NAME", ""),
		V2ClientWebhookSecret: envOr("V2_CLIENT_WEBHOOK_SECRET", ""),

		V2InformerBotToken:      envOr("V2_INFORMER_BOT_TOKEN", ""),
		V2InformerBotName:       envOr("V2_INFORMER_BOT_NAME", ""),
		V2InformerWebhookSecret: envOr("V2_INFORMER_WEBHOOK_SECRET", ""),

		V2ClientPublicMinBalanceUSD:      envFloat("V2_CLIENT_PUBLIC_MIN_BALANCE_USD", 150.0),
		V2ClientHardFloorUSD:             envFloat("V2_CLIENT_HARD_FLOOR_USD", 120.0),
		V2HelperPostPaymentMinBalanceUSD: envFloat("V2_HELPER_POST_PAYMENT_MIN_BALANCE_USD", 1000.0),
		V2InformerMinBalanceUSD:          envFloat("V2_INFORMER_MIN_BALANCE_USD", 1000.0),
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

func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return fallback
}
