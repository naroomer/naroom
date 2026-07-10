package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"naroom/internal/crypto"
	"naroom/internal/middleware"
	"naroom/internal/telegram"
)

const (
	telegramTokenTTL = 10 * 60 // 10 minutes
)

type telegramClientTokenReq struct {
	ListingID string `json:"listing_id"`
}

type telegramHelperTokenReq struct {
	City     string `json:"city"`
	Language string `json:"language"`
	Problem  string `json:"problem"`
	HelpType string `json:"help_type"`
	Urgency  string `json:"urgency"`
}

type helperFilters struct {
	City     string `json:"city,omitempty"`
	Language string `json:"language,omitempty"`
	Problem  string `json:"problem,omitempty"`
	HelpType string `json:"help_type,omitempty"`
	Urgency  string `json:"urgency,omitempty"`
}

type telegramUpdate struct {
	Message struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

// TelegramClientToken handles POST /api/telegram/client/token.
// Requires session (client role). Generates a one-time linking token for the client bot.
func (h *Handler) TelegramClientToken(w http.ResponseWriter, r *http.Request) {
	var req telegramClientTokenReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}
	if req.ListingID == "" {
		writeError(w, 400, "listing_id required")
		return
	}

	walletHash := middleware.SessionWalletHash(r.Context())
	if walletHash == "" {
		writeError(w, 401, "session required")
		return
	}

	// Verify listing ownership
	var ownerHash, status string
	if err := h.DB.QueryRow(`SELECT wallet_hash, status FROM listings WHERE id = ?`,
		req.ListingID).Scan(&ownerHash, &status); err != nil {
		writeError(w, 404, "listing not found")
		return
	}
	if ownerHash != walletHash {
		writeError(w, 403, "not your listing")
		return
	}
	if status != "pending" && status != "active" {
		writeError(w, 409, "listing cannot connect telegram in current state")
		return
	}

	token, err := h.createTelegramToken("client", req.ListingID, "", "")
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	writeJSON(w, 201, map[string]any{
		"token":      token,
		"bot_url":    fmt.Sprintf("https://t.me/%s?start=%s", h.TelegramClientBotName, token),
		"expires_in": telegramTokenTTL,
	})
}

// TelegramHelperToken handles POST /api/telegram/helper/token.
// Requires session (peer role) and wallet balance >= $1000.
func (h *Handler) TelegramHelperToken(w http.ResponseWriter, r *http.Request) {
	var req telegramHelperTokenReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}

	walletHash := middleware.SessionWalletHash(r.Context())
	if walletHash == "" {
		writeError(w, 401, "session required")
		return
	}
	if middleware.SessionRole(r.Context()) != "peer" {
		writeError(w, 403, "peer role required")
		return
	}

	// Validate optional filters
	if req.City != "" && !validCity[req.City] {
		writeError(w, 400, "invalid city")
		return
	}
	if req.Language != "" && !validLanguage[req.Language] {
		writeError(w, 400, "invalid language")
		return
	}
	if req.Problem != "" && !validDependency[req.Problem] {
		writeError(w, 400, "invalid problem")
		return
	}
	if req.HelpType != "" && !validHelp[req.HelpType] {
		writeError(w, 400, "invalid help_type")
		return
	}
	if req.Urgency != "" && !validUrgency[req.Urgency] {
		writeError(w, 400, "invalid urgency")
		return
	}

	// Verify balance >= $1000
	var balanceStatus string
	var minRequiredUSD float64
	err := h.DB.QueryRow(`
		SELECT balance_status, min_required_usd
		FROM wallet_sessions WHERE wallet_hash = ? AND role = 'peer'
	`, walletHash).Scan(&balanceStatus, &minRequiredUSD)
	if err != nil {
		writeError(w, 403, "wallet not verified as peer")
		return
	}
	if balanceStatus != "ok" || minRequiredUSD < h.peerMinBalance() {
		writeError(w, 403, fmt.Sprintf("peer balance verification required (min $%.0f)", h.peerMinBalance()))
		return
	}

	filtersJSON, err := json.Marshal(helperFilters{
		City: req.City, Language: req.Language, Problem: req.Problem,
		HelpType: req.HelpType, Urgency: req.Urgency,
	})
	if err != nil {
		writeError(w, 500, "encode error")
		return
	}
	token, err := h.createTelegramToken("helper", "", string(filtersJSON), walletHash)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	writeJSON(w, 201, map[string]any{
		"token":      token,
		"bot_url":    fmt.Sprintf("https://t.me/%s?start=%s", h.TelegramHelperBotName, token),
		"expires_in": telegramTokenTTL,
	})
}

// TelegramClientConfirm handles GET /api/telegram/client/confirm?listing_id=<id>.
// Frontend polls this to detect when the bot confirmed the binding.
func (h *Handler) TelegramClientConfirm(w http.ResponseWriter, r *http.Request) {
	listingID := r.URL.Query().Get("listing_id")
	if listingID == "" {
		writeError(w, 400, "listing_id required")
		return
	}
	now := time.Now().Unix()
	var n int
	_ = h.DB.QueryRow(`
		SELECT COUNT(*) FROM client_listing_notifications
		WHERE listing_id = ? AND active = TRUE AND expires_at > ?
	`, listingID, now).Scan(&n)
	writeJSON(w, 200, map[string]bool{"confirmed": n > 0})
}

// TelegramHelperConfirm handles GET /api/telegram/helper/confirm?token=<token>.
// Frontend polls this to detect when the helper bot confirmed the subscription.
func (h *Handler) TelegramHelperConfirm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, 400, "token required")
		return
	}
	var used bool
	err := h.DB.QueryRow(`
		SELECT used FROM telegram_link_tokens WHERE token = ? AND token_type = 'helper'
	`, token).Scan(&used)
	if err != nil || !used {
		writeJSON(w, 200, map[string]bool{"confirmed": false})
		return
	}
	writeJSON(w, 200, map[string]bool{"confirmed": true})
}

// TelegramClientWebhook handles POST /api/telegram/client/webhook.
// Telegram calls this when user sends /start <token> to the client bot.
func (h *Handler) TelegramClientWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.validTelegramSecret(r) {
		writeError(w, 403, "forbidden")
		return
	}
	var update telegramUpdate
	if err := decodeJSON(r, &update); err != nil {
		writeError(w, 400, "invalid json")
		return
	}
	token := parseTelegramStartToken(update.Message.Text)
	if token == "" || update.Message.Chat.ID == 0 {
		writeJSON(w, 200, map[string]string{"status": "ignored"})
		return
	}
	chatID := fmt.Sprint(update.Message.Chat.ID)

	listingID, err := h.consumeClientToken(token, chatID)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}

	// Send confirmation to user
	if h.Telegram != nil {
		if err := h.Telegram.SendClientMessage(r.Context(), chatID, telegram.ClientConfirmText); err != nil {
			log.Printf("telegram webhook: send client confirm (listing=%s): %v", listingID, err)
		}
	}

	// Try to activate the listing now that both gates may be satisfied
	go func() {
		if h.RequireTelegram {
			db := h.DB
			tx, err := db.Begin()
			if err != nil {
				return
			}
			activated, err := telegram.ActivateListingIfReady(tx, listingID, h.ListingTTL, true)
			if err != nil {
				tx.Rollback()
				return
			}
			renewed, err := telegram.RenewListingIfReady(tx, listingID, h.ListingTTL, true)
			if err != nil {
				tx.Rollback()
				return
			}
			if err := tx.Commit(); err != nil {
				return
			}
			if (activated || renewed) && h.Telegram != nil {
				ctx := context.Background()
				boardURL := h.PublicBaseURL + "/board"
				if err := telegram.NotifyClientListingActivated(ctx, db, h.Telegram, listingID); err != nil {
					log.Printf("telegram webhook: notify client listing activated (listing=%s): %v", listingID, err)
				}
				if err := telegram.NotifyMatchingHelpers(ctx, db, h.Telegram, listingID, boardURL); err != nil {
					log.Printf("telegram webhook: notify matching helpers (listing=%s): %v", listingID, err)
				}
			}
		}
	}()

	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// TelegramHelperWebhook handles POST /api/telegram/helper/webhook.
// Telegram calls this when user sends /start <token> to the helper bot.
func (h *Handler) TelegramHelperWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.validTelegramSecret(r) {
		writeError(w, 403, "forbidden")
		return
	}
	var update telegramUpdate
	if err := decodeJSON(r, &update); err != nil {
		writeError(w, 400, "invalid json")
		return
	}
	token := parseTelegramStartToken(update.Message.Text)
	if token == "" || update.Message.Chat.ID == 0 {
		writeJSON(w, 200, map[string]string{"status": "ignored"})
		return
	}
	chatID := fmt.Sprint(update.Message.Chat.ID)

	if err := h.consumeHelperToken(token, chatID); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if h.Telegram != nil {
		if err := h.Telegram.SendHelperMessage(r.Context(), chatID, telegram.HelperConfirmText); err != nil {
			log.Printf("telegram webhook: send helper confirm: %v", err)
		}
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// createTelegramToken generates and stores a one-time linking token.
// wallet_address is never stored. counselorHash is stored for helper tokens only — it enables
// direct "chat opened" notifications without exposing the wallet address.
func (h *Handler) createTelegramToken(tokenType, listingID, filtersJSON, counselorHash string) (string, error) {
	now := time.Now().Unix()
	token := crypto.RandomToken()

	var nullListing sql.NullString
	if listingID != "" {
		nullListing = sql.NullString{String: listingID, Valid: true}
	}
	var nullFilters sql.NullString
	if filtersJSON != "" {
		nullFilters = sql.NullString{String: filtersJSON, Valid: true}
	}
	var nullCounselor sql.NullString
	if counselorHash != "" {
		nullCounselor = sql.NullString{String: counselorHash, Valid: true}
	}
	_, err := h.DB.Exec(`
		INSERT INTO telegram_link_tokens
			(id, token, token_type, listing_id, helper_filters_json, counselor_hash, created_at, expires_at, used)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, FALSE)
	`, crypto.NewID("tgl"), token, tokenType, nullListing, nullFilters, nullCounselor, now, now+telegramTokenTTL)
	return token, err
}

// consumeClientToken validates a client one-time token and creates the notification binding.
// Returns the listing_id so the caller can attempt ActivateListingIfReady.
// Token is claimed atomically via UPDATE+RowsAffected to prevent double-consumption on
// concurrent Telegram webhook retries.
func (h *Handler) consumeClientToken(token, chatID string) (string, error) {
	now := time.Now().Unix()
	tx, err := h.DB.Begin()
	if err != nil {
		return "", fmt.Errorf("db error")
	}
	defer tx.Rollback()

	// Claim the token atomically — prevents race on concurrent Telegram retries.
	res, err := tx.Exec(`
		UPDATE telegram_link_tokens SET used = TRUE
		WHERE token = ? AND token_type = 'client' AND used = FALSE AND expires_at > ?
	`, token, now)
	if err != nil {
		return "", fmt.Errorf("db error")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", fmt.Errorf("invalid or expired token")
	}

	var listingID string
	if err = tx.QueryRow(`SELECT listing_id FROM telegram_link_tokens WHERE token = ?`,
		token).Scan(&listingID); err != nil {
		return "", fmt.Errorf("db error")
	}

	expiresAt := now + int64(h.ListingTTL)
	if h.ListingTTL == 0 {
		expiresAt = now + 21600
	}
	_, err = tx.Exec(`
		INSERT INTO client_listing_notifications
			(id, listing_id, telegram_chat_id, created_at, expires_at, active)
		VALUES (?, ?, ?, ?, ?, TRUE)
	`, crypto.NewID("tgc"), listingID, chatID, now, expiresAt)
	if err != nil {
		return "", fmt.Errorf("db error")
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("db error")
	}
	return listingID, nil
}

// consumeHelperToken validates a helper one-time token and creates/replaces the subscription.
// Token is claimed atomically via UPDATE+RowsAffected to prevent double-consumption on
// concurrent Telegram webhook retries.
func (h *Handler) consumeHelperToken(token, chatID string) error {
	now := time.Now().Unix()
	tx, err := h.DB.Begin()
	if err != nil {
		return fmt.Errorf("db error")
	}
	defer tx.Rollback()

	// Claim the token atomically — prevents race on concurrent Telegram retries.
	res, err := tx.Exec(`
		UPDATE telegram_link_tokens SET used = TRUE
		WHERE token = ? AND token_type = 'helper' AND used = FALSE AND expires_at > ?
	`, token, now)
	if err != nil {
		return fmt.Errorf("db error")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("invalid or expired token")
	}

	var filtersRaw sql.NullString
	var counselorHashRaw sql.NullString
	if err = tx.QueryRow(`SELECT helper_filters_json, counselor_hash FROM telegram_link_tokens WHERE token = ?`,
		token).Scan(&filtersRaw, &counselorHashRaw); err != nil {
		return fmt.Errorf("db error")
	}

	var filters helperFilters
	if filtersRaw.Valid {
		_ = json.Unmarshal([]byte(filtersRaw.String), &filters)
	}

	// Deactivate any existing subscription for this chat_id (same Telegram account, new subscribe).
	_, _ = tx.Exec(`
		UPDATE helper_board_subscriptions SET active = FALSE
		WHERE telegram_chat_id = ? AND active = TRUE
	`, chatID)

	// counselor_hash is stored to enable direct "chat opened" notifications. It is derived
	// from the helper's wallet and set when the link token was created (see TelegramHelperToken).
	var nullCounselor sql.NullString
	if counselorHashRaw.Valid && counselorHashRaw.String != "" {
		nullCounselor = counselorHashRaw
	}

	// expires_at: 24h TTL as documented in schema.
	_, err = tx.Exec(`
		INSERT INTO helper_board_subscriptions
			(id, telegram_chat_id, counselor_hash, city, language, problem, help_type, urgency,
			 created_at, expires_at, active)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, TRUE)
	`, crypto.NewID("tgh"), chatID, nullCounselor,
		nullStr(filters.City), nullStr(filters.Language), nullStr(filters.Problem),
		nullStr(filters.HelpType), nullStr(filters.Urgency),
		now, now+86400)
	if err != nil {
		return fmt.Errorf("db error")
	}
	return tx.Commit()
}

func (h *Handler) validTelegramSecret(r *http.Request) bool {
	return h.TelegramWebhookSecret != "" &&
		r.Header.Get("X-Telegram-Bot-Api-Secret-Token") == h.TelegramWebhookSecret
}

func parseTelegramStartToken(text string) string {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) != 2 || fields[0] != "/start" {
		return ""
	}
	return fields[1]
}

func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
