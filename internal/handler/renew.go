package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"naroom/internal/middleware"
)

// RenewListing handles POST /listing/{id}/renew.
// Renewal is FREE until the listing has 2 responses — clients already paid $5 upfront.
// Once 2 pending responses exist the client must choose a peer instead of renewing.
func (h *Handler) RenewListing(w http.ResponseWriter, r *http.Request) {
	listingID := chi.URLParam(r, "id")

	walletHash := middleware.SessionWalletHash(r.Context())
	if walletHash == "" {
		writeError(w, 401, "session required")
		return
	}

	// Load listing and verify ownership via hash
	var ownerHash, status string
	var firstActivatedAt int64
	var renewalCount int
	err := h.DB.QueryRow(`
		SELECT wallet_hash, status, COALESCE(first_activated_at, created_at), COALESCE(renewal_count, 0)
		FROM listings WHERE id = ? AND is_sample = 0
	`, listingID).Scan(&ownerHash, &status, &firstActivatedAt, &renewalCount)
	if err != nil {
		writeError(w, 404, "listing not found")
		return
	}
	if ownerHash != walletHash {
		writeError(w, 403, "not your listing")
		return
	}
	if status != "active" && status != "expired" {
		writeError(w, 409, "listing cannot be renewed (status: "+status+")")
		return
	}

	now := time.Now().Unix()

	// Block renewal if already has 2 responses — client must choose a peer
	var pendingCount int
	h.DB.QueryRow(`SELECT COUNT(*) FROM responses WHERE listing_id = ? AND status = 'pending'`, listingID).Scan(&pendingCount)
	if pendingCount >= 2 {
		writeError(w, 409, "listing has 2 responses — please choose a peer instead of renewing")
		return
	}

	// Free renewal: extend listing and Telegram notification by ListingTTL (6h)
	ttl := int64(h.ListingTTL)
	if ttl == 0 {
		ttl = 21600
	}
	newExpiry := now + ttl

	_, err = h.DB.Exec(`
		UPDATE listings
		SET status = 'active', visible_until = ?,
		    renewal_count = COALESCE(renewal_count, 0) + 1
		WHERE id = ?
	`, newExpiry, listingID)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}

	// Extend Telegram notification to match new expiry
	h.DB.Exec(`
		UPDATE client_listing_notifications
		SET expires_at = ?
		WHERE listing_id = ? AND active = TRUE
	`, newExpiry, listingID)

	writeJSON(w, 200, map[string]any{
		"status":        "renewed",
		"free":          true,
		"renewal_count": renewalCount + 1,
		"visible_until": newExpiry,
	})
}
