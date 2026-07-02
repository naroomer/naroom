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
