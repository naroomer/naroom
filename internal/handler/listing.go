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
