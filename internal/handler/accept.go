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

	if req.ClientPubkey == "" {
		writeError(w, 400, "client_pubkey required")
		return
	}
	// Always use the currency from the wallet session — never trust the frontend value.
	var sessionCurrency string
	if err := h.DB.QueryRow(`SELECT currency FROM wallet_sessions WHERE wallet_hash = ? AND role = 'client'`,
		clientHash).Scan(&sessionCurrency); err != nil || (sessionCurrency != "BTC" && sessionCurrency != "LTC") {
		writeError(w, 403, "wallet session not found")
		return
	}
	req.Currency = sessionCurrency

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

	// Guard: listing must have fewer than 2 opened paid chats
	var openedChatsCount int
	tx.QueryRow(`SELECT COALESCE(opened_chats_count, 0) FROM listings WHERE id = ?`, listingID).Scan(&openedChatsCount)
	if openedChatsCount >= 2 {
		writeError(w, 409, "listing already has 2 opened chats — no more peers can be accepted")
		return
	}

	// Guard: no other accepted response for this listing with no room yet (unpaid reservation in flight).
	// A second acceptance is fine if the first already has a chat_room (payment confirmed).
	var unpaidReservations int
	tx.QueryRow(`
		SELECT COUNT(*) FROM responses r
		WHERE r.listing_id = ? AND r.id != ? AND r.status = 'accepted'
		  AND NOT EXISTS (SELECT 1 FROM chat_rooms cr WHERE cr.response_id = r.id)
	`, listingID, responseID).Scan(&unpaidReservations)
	if unpaidReservations > 0 {
		writeError(w, 409, "another accepted response has a pending unpaid invoice")
		return
	}

	// Check no active chat for this client in a DIFFERENT listing.
	// A client may accept a second helper for the same listing (two-helper model).
	var activeChatElsewhere int
	tx.QueryRow(`
		SELECT COUNT(*) FROM chat_rooms
		WHERE client_hash = ? AND listing_id != ? AND status IN ('active', 'peer_left')
	`, clientHash, listingID).Scan(&activeChatElsewhere)
	if activeChatElsewhere > 0 {
		writeError(w, 409, "already have active chat in another listing")
		return
	}

	// Peer capacity check based on active chat_rooms (not pending responses)
	var peerActiveChatCount int
	tx.QueryRow(`
		SELECT COUNT(*) FROM chat_rooms
		WHERE counselor_hash = ? AND status IN ('active', 'peer_left', 'client_left')
	`, counselorHash).Scan(&peerActiveChatCount)
	var peerMinRequired float64
	tx.QueryRow(`SELECT COALESCE(min_required_usd, 1000) FROM wallet_sessions WHERE wallet_hash = ?`, counselorHash).Scan(&peerMinRequired)
	peerMaxSlots := int(peerMinRequired/1000) * 2
	if peerMaxSlots < 2 {
		peerMaxSlots = 2
	}
	if peerActiveChatCount >= peerMaxSlots {
		writeError(w, 409, "Peer is currently at chat capacity. Choose another peer or try later.")
		return
	}

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
