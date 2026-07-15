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
// Requires session (client role). Issues a one-time linking token for the client bot.
//
// Transaction boundary: ownership check, invalidation of previous unused tokens, and
// insertion of the new token all run inside one database transaction. If the INSERT
// fails the rollback leaves previous tokens untouched and usable. Every SQL error is
// checked; any failure returns 500 before commit.
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
	principalID := middleware.SessionPrincipalID(r.Context())
	if walletHash == "" {
		writeError(w, 401, "session required")
		return
	}
	// Strict: principal required — wallet-hash-only (legacy) sessions cannot issue tokens.
	if principalID == "" {
		writeError(w, 401, "session requires /session/init")
		return
	}

	now := time.Now().Unix()

	// Open the transaction that covers all three steps: check, invalidate, insert.
	// defer-Rollback is a no-op after a successful Commit.
	tx, err := h.DB.Begin()
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Read listing state inside the transaction for a consistent snapshot.
	var status string
	var ownerPrincipalID sql.NullString
	var visibleUntil int64
	if err := tx.QueryRow(
		`SELECT owner_principal_id, status, visible_until FROM listings WHERE id = ?`,
		req.ListingID,
	).Scan(&ownerPrincipalID, &status, &visibleUntil); err != nil {
		writeError(w, 404, "listing not found")
		return
	}
	if !ownerPrincipalID.Valid || ownerPrincipalID.String == "" {
		writeError(w, 403, "session upgrade required")
		return
	}
	if ownerPrincipalID.String != principalID {
		writeError(w, 403, "not your listing")
		return
	}
	if status != "pending" && status != "active" {
		writeError(w, 409, "listing cannot connect telegram in current state")
		return
	}
	// Reject immediately if the listing window has already closed.
	if status == "active" && visibleUntil > 0 && visibleUntil <= now {
		writeError(w, 409, "listing window has expired")
		return
	}

	// Invalidate all previous unused client tokens for this listing.
	// Runs inside the same transaction: if the INSERT below fails and we rollback,
	// this UPDATE is also rolled back and the previous tokens stay live.
	if _, err := tx.Exec(
		`UPDATE telegram_link_tokens SET used = TRUE
		 WHERE listing_id = ? AND token_type = 'client' AND used = FALSE`,
		req.ListingID,
	); err != nil {
		writeError(w, 500, "db error")
		return
	}

	// Insert the replacement token in the same transaction.
	token := crypto.RandomToken()
	if _, err := tx.Exec(
		`INSERT INTO telegram_link_tokens
		     (id, token, token_type, listing_id, helper_filters_json, counselor_hash, principal_id,
		      created_at, expires_at, used)
		 VALUES (?, ?, 'client', ?, NULL, NULL, ?, ?, ?, FALSE)`,
		crypto.NewID("tgl"), token, req.ListingID, principalID, now, now+telegramTokenTTL,
	); err != nil {
		// Rollback restores any invalidated tokens to used=FALSE — they remain live.
		writeError(w, 500, "db error")
		return
	}

	if err := tx.Commit(); err != nil {
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
	token, err := h.createTelegramToken("helper", "", string(filtersJSON), walletHash, "")
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
// Requires session. Returns {confirmed: true} when an active, non-expired binding exists.
// Returns 500 (not false) when the binding COUNT query itself fails — callers must not
// misinterpret a server error as "not connected".
func (h *Handler) TelegramClientConfirm(w http.ResponseWriter, r *http.Request) {
	listingID := r.URL.Query().Get("listing_id")
	if listingID == "" {
		writeError(w, 400, "listing_id required")
		return
	}

	// Require principal — no wallet-bypass fallback, no legacy session fallback.
	principalID := middleware.SessionPrincipalID(r.Context())
	if principalID == "" {
		writeError(w, 403, "session requires /session/init")
		return
	}

	// Verify the caller owns the listing.
	var ownerPrincipalID sql.NullString
	if err := h.DB.QueryRow(`SELECT owner_principal_id FROM listings WHERE id = ?`,
		listingID).Scan(&ownerPrincipalID); err != nil {
		writeError(w, 404, "listing not found")
		return
	}
	if !ownerPrincipalID.Valid || ownerPrincipalID.String == "" || ownerPrincipalID.String != principalID {
		writeError(w, 403, "not your listing")
		return
	}

	now := time.Now().Unix()
	var n int
	// A query error returns 500 — never silently report false on a DB failure.
	if err := h.DB.QueryRow(`
		SELECT COUNT(*) FROM client_listing_notifications
		WHERE listing_id = ? AND active = TRUE AND expires_at > ?
	`, listingID, now).Scan(&n); err != nil {
		writeError(w, 500, "db error")
		return
	}
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
// Used only by TelegramHelperToken. Client tokens are now issued inline in
// TelegramClientToken inside the atomic invalidation+insert transaction.
// counselorHash is stored for helper tokens only (enables direct "chat opened"
// notifications). principalID is passed for client tokens; pass "" for helper tokens.
func (h *Handler) createTelegramToken(tokenType, listingID, filtersJSON, counselorHash, principalID string) (string, error) {
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
	var nullPrincipal sql.NullString
	if principalID != "" {
		nullPrincipal = sql.NullString{String: principalID, Valid: true}
	}
	_, err := h.DB.Exec(`
		INSERT INTO telegram_link_tokens
			(id, token, token_type, listing_id, helper_filters_json, counselor_hash, principal_id, created_at, expires_at, used)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, FALSE)
	`, crypto.NewID("tgl"), token, tokenType, nullListing, nullFilters, nullCounselor, nullPrincipal, now, now+telegramTokenTTL)
	return token, err
}

// consumeClientToken validates a client one-time token and creates the notification binding.
// Returns the listing_id so the caller can attempt ActivateListingIfReady.
//
// All six steps run inside a single database transaction:
//   1. Claim the token (UPDATE used=TRUE) — RowsAffected=0 means invalid/expired/consumed.
//   2. Read token's principal_id — fail closed if NULL or empty.
//   3. Read listing: fail closed if owner_principal_id does not exactly match token's principal.
//   4. Reject if active listing's visible_until has already passed.
//   5. Deactivate existing active binding — SQL error rolls back steps 1-4.
//   6. Insert new binding — SQL error rolls back everything including the token claim.
//   7. Commit.
func (h *Handler) consumeClientToken(token, chatID string) (string, error) {
	now := time.Now().Unix()
	tx, err := h.DB.Begin()
	if err != nil {
		return "", fmt.Errorf("db error")
	}
	defer tx.Rollback() //nolint:errcheck

	// Step 1 — Claim the token atomically.
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

	// Step 2 — Read token metadata.
	var listingID string
	var tokenPrincipalID sql.NullString
	if err = tx.QueryRow(`SELECT listing_id, principal_id FROM telegram_link_tokens WHERE token = ?`,
		token).Scan(&listingID, &tokenPrincipalID); err != nil {
		return "", fmt.Errorf("db error")
	}

	// Fail closed: NULL or empty principal_id is rejected outright.
	// Tokens issued before the reconnect feature lack a principal and cannot be consumed.
	if !tokenPrincipalID.Valid || tokenPrincipalID.String == "" {
		return "", fmt.Errorf("token missing principal")
	}

	// Step 3 — Verify listing ownership.
	var ownerPrincipalID sql.NullString
	var listingStatus string
	var visibleUntil int64
	if err = tx.QueryRow(`SELECT owner_principal_id, status, visible_until FROM listings WHERE id = ?`,
		listingID).Scan(&ownerPrincipalID, &listingStatus, &visibleUntil); err != nil {
		return "", fmt.Errorf("db error")
	}
	// Exact equality required — if ownership changed between issuance and consumption, reject.
	if !ownerPrincipalID.Valid || ownerPrincipalID.String != tokenPrincipalID.String {
		return "", fmt.Errorf("principal no longer owns listing")
	}

	// Step 4 — Reject if the listing's visibility window is already closed.
	if listingStatus == "active" && visibleUntil > 0 && visibleUntil <= now {
		return "", fmt.Errorf("listing window has expired")
	}

	// Step 5 — Deactivate any existing active binding. Failure here is a hard error
	// that triggers rollback: the token claim is also undone so the caller can retry.
	if _, err := tx.Exec(`
		UPDATE client_listing_notifications SET active = FALSE
		WHERE listing_id = ? AND active = TRUE
	`, listingID); err != nil {
		return "", fmt.Errorf("db error")
	}

	// Step 6 — Insert the new binding.
	expiresAt := now + int64(h.ListingTTL)
	if h.ListingTTL == 0 {
		expiresAt = now + 21600
	}
	if listingStatus == "active" && visibleUntil > 0 && visibleUntil < expiresAt {
		expiresAt = visibleUntil
	}

	if _, err = tx.Exec(`
		INSERT INTO client_listing_notifications
			(id, listing_id, telegram_chat_id, created_at, expires_at, active)
		VALUES (?, ?, ?, ?, ?, TRUE)
	`, crypto.NewID("tgc"), listingID, chatID, now, expiresAt); err != nil {
		return "", fmt.Errorf("db error")
	}

	// Step 7 — Commit.
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("db error")
	}
	return listingID, nil
}

// consumeHelperToken validates a helper one-time token and creates/replaces the subscription.
func (h *Handler) consumeHelperToken(token, chatID string) error {
	now := time.Now().Unix()
	tx, err := h.DB.Begin()
	if err != nil {
		return fmt.Errorf("db error")
	}
	defer tx.Rollback() //nolint:errcheck

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

	_, _ = tx.Exec(`
		UPDATE helper_board_subscriptions SET active = FALSE
		WHERE telegram_chat_id = ? AND active = TRUE
	`, chatID)

	var nullCounselor sql.NullString
	if counselorHashRaw.Valid && counselorHashRaw.String != "" {
		nullCounselor = counselorHashRaw
	}

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
