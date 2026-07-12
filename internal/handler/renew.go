package handler

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"naroom/internal/middleware"
	"naroom/internal/telegram"
)

// RenewListing handles POST /listing/{id}/renew.
// Renewal is FREE while opened_chats_count < 2 — no time-based cutoff.
// Once 2 paid chats have been opened the listing is permanently consumed (renewal → 409).
//
// Eligibility (evaluated atomically to prevent duplicate updates):
//   - status='expired', OR
//   - status='active' AND visible_until <= now+3600 (at most 1 hour left)
//
// Duplicate or early-renewal calls return 409 without incrementing renewal_count
// or dispatching Telegram notifications.
func (h *Handler) RenewListing(w http.ResponseWriter, r *http.Request) {
	listingID := chi.URLParam(r, "id")

	walletHash := middleware.SessionWalletHash(r.Context())
	if walletHash == "" {
		writeError(w, 401, "session required")
		return
	}

	// Load listing for 404 / 403 / count checks.
	// These are pre-checks only; the authoritative eligibility gate is the atomic UPDATE below.
	var ownerHash string
	var renewalCount, openedChatsCount int
	err := h.DB.QueryRow(`
		SELECT wallet_hash, COALESCE(renewal_count, 0), COALESCE(opened_chats_count, 0)
		FROM listings WHERE id = ? AND is_sample = 0
	`, listingID).Scan(&ownerHash, &renewalCount, &openedChatsCount)
	if err != nil {
		writeError(w, 404, "listing not found")
		return
	}
	if ownerHash != walletHash {
		writeError(w, 403, "not your listing")
		return
	}
	if openedChatsCount >= 2 {
		writeError(w, 409, "listing has 2 opened chats — renewal not allowed")
		return
	}

	// Atomic eligibility check + update.
	// The WHERE clause ensures:
	//   - count is still < 2 (belt-and-suspenders vs. concurrent watcher)
	//   - listing is either expired or within the last hour of active visibility
	// If RowsAffected == 0, the listing is not eligible (already fresh, already renewed,
	// or status is 'closed'/'pending'). No renewal_count increment occurs.
	ttl := int64(h.ListingTTL)
	if ttl == 0 {
		ttl = 86400
	}
	now := time.Now().Unix()
	newExpiry := now + ttl

	res, err := h.DB.Exec(`
		UPDATE listings
		SET status = 'active', visible_until = ?,
		    renewal_count = COALESCE(renewal_count, 0) + 1
		WHERE id = ?
		  AND COALESCE(opened_chats_count, 0) < 2
		  AND (status = 'expired' OR (status = 'active' AND visible_until <= ?))
	`, newExpiry, listingID, now+3600)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, 409, "renewal not allowed: listing still has more than 1 hour visibility remaining")
		return
	}

	// Extend Telegram notification expiry to match new visible_until.
	h.DB.Exec(`
		UPDATE client_listing_notifications
		SET expires_at = ?
		WHERE listing_id = ? AND active = TRUE
	`, newExpiry, listingID)

	// Re-notify matching helpers exactly once per eligible renewal.
	// This goroutine is spawned only on the success path (RowsAffected > 0).
	if h.Telegram != nil {
		lID := listingID
		boardURL := h.PublicBaseURL + "/board"
		go func() {
			ctx := context.Background()
			if err := telegram.NotifyMatchingHelpers(ctx, h.DB, h.Telegram, lID, boardURL); err != nil {
				log.Printf("renew: notify helpers (listing=%s): %v", lID, err)
			}
		}()
	}

	writeJSON(w, 200, map[string]any{
		"status":        "renewed",
		"free":          true,
		"renewal_count": renewalCount + 1,
		"visible_until": newExpiry,
	})
}
