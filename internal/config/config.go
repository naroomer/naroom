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
