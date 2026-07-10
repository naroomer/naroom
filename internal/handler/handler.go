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
	Mempool     *crypto.MempoolClient
	Blockcypher *crypto.BlockcypherClient
	Blockchair  *crypto.BlockchairClient
	Prices      *crypto.PriceCache
	Wallet      *crypto.HDWallet
	DevMode    bool
	ListingTTL int
	ChatTTL          int
	ChatMinTTL       int
	ClientMinBalanceUSD float64 // default 150; override via CLIENT_MIN_BALANCE_USD
	PeerMinBalanceUSD   float64 // default 1000; override via PEER_MIN_BALANCE_USD
	Hub              *ChatHub // for broadcasting room_closed to WS clients

	// Telegram notification bots. Nil when tokens are not configured.
	Telegram              telegram.Sender
	TelegramClientBotName string
	TelegramHelperBotName string
	TelegramWebhookSecret string
	PublicBaseURL         string
	RequireTelegram       bool // true when client bot token is configured
}

func (h *Handler) clientMinBalance() float64 {
	if h.ClientMinBalanceUSD > 0 {
		return h.ClientMinBalanceUSD
	}
	return 150.0
}

func (h *Handler) peerMinBalance() float64 {
	if h.PeerMinBalanceUSD > 0 {
		return h.PeerMinBalanceUSD
	}
	return 1000.0
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
