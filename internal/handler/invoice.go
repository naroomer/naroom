package handler

import (
	"database/sql"
	"net/http"

	"github.com/go-chi/chi/v5"

	"naroom/internal/middleware"
)

// InvoiceStatus handles GET /invoice/{id}/status.
// Requires session — only the listing owner can poll their invoice.
func (h *Handler) InvoiceStatus(w http.ResponseWriter, r *http.Request) {
	invoiceID := chi.URLParam(r, "id")
	if invoiceID == "" {
		writeError(w, 400, "invoice id required")
		return
	}

	walletHash := middleware.SessionWalletHash(r.Context())
	if walletHash == "" {
		writeError(w, 401, "session required")
		return
	}

	var status, address, amountCrypto, currency string
	var amountUSD float64
	var listingID sql.NullString
	err := h.DB.QueryRow(`
		SELECT status, address, amount_usd, amount_crypto, currency, listing_id
		FROM invoices WHERE id = ?
	`, invoiceID).Scan(&status, &address, &amountUSD, &amountCrypto, &currency, &listingID)
	if err != nil {
		writeError(w, 404, "invoice not found")
		return
	}

	// Verify session wallet owns the invoice's listing (compare by hash).
	if listingID.Valid {
		var ownerHash string
		err = h.DB.QueryRow(`SELECT wallet_hash FROM listings WHERE id = ?`, listingID.String).Scan(&ownerHash)
		if err != nil || ownerHash != walletHash {
			writeError(w, 403, "not your invoice")
			return
		}
	}

	writeJSON(w, 200, map[string]any{
		"invoice_id":    invoiceID,
		"status":        status,
		"address":       address,
		"amount_usd":    amountUSD,
		"amount_crypto": amountCrypto,
		"currency":      currency,
	})
}

// PeerPendingInvoice handles GET /peer/invoice — returns the peer's pending chat invoice if any.
// Lets a peer recover their payment page after losing their session.
func (h *Handler) PeerPendingInvoice(w http.ResponseWriter, r *http.Request) {
	walletHash := middleware.SessionWalletHash(r.Context())
	if walletHash == "" {
		writeError(w, 401, "session required")
		return
	}

	var invoiceID, address, amountCrypto, currency, listingID string
	var amountUSD float64
	err := h.DB.QueryRow(`
		SELECT i.id, i.address, i.amount_usd, COALESCE(i.amount_crypto,''), i.currency, COALESCE(i.listing_id,'')
		FROM invoices i
		JOIN responses r ON r.id = i.response_id
		WHERE r.counselor_hash = ? AND i.status = 'pending' AND i.type = 'chat'
		ORDER BY i.created_at DESC LIMIT 1
	`, walletHash).Scan(&invoiceID, &address, &amountUSD, &amountCrypto, &currency, &listingID)
	if err != nil {
		writeError(w, 404, "no pending invoice")
		return
	}

	writeJSON(w, 200, map[string]any{
		"invoice_id":    invoiceID,
		"address":       address,
		"amount_usd":    amountUSD,
		"amount_crypto": amountCrypto,
		"currency":      currency,
		"listing_id":    listingID,
	})
}
