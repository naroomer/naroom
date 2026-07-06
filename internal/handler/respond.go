package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"naroom/internal/crypto"
	"naroom/internal/middleware"
	"naroom/internal/telegram"
)

type respondReq struct {
	CounselorPubkey string `json:"peer_pubkey"`
}

// Respond handles POST /listing/{id}/respond — counselor responds to a listing.
// Counselor identity comes from the session; only X25519 pubkey is in the body.
func (h *Handler) Respond(w http.ResponseWriter, r *http.Request) {
	listingID := chi.URLParam(r, "id")
	if listingID == "" {
		writeError(w, 400, "listing id required")
		return
	}

	counselorHash := middleware.SessionWalletHash(r.Context())
	if counselorHash == "" {
		writeError(w, 401, "session required")
		return
	}

	// Clients may never respond to listings — enforce role regardless of DevMode.
	if role := middleware.SessionRole(r.Context()); role == "client" {
		writeError(w, 403, "clients cannot respond to listings")
		return
	}

	var req respondReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}

	if req.CounselorPubkey == "" {
		writeError(w, 400, "peer_pubkey required")
		return
	}

	if !h.DevMode {
		// Check wallet verified as peer
		var balanceStatus string
		err := h.DB.QueryRow(`SELECT balance_status FROM wallet_sessions WHERE wallet_hash = ? AND role = 'peer'`,
			counselorHash).Scan(&balanceStatus)
		if err != nil {
			writeError(w, 403, "wallet not verified as peer")
			return
		}
		if balanceStatus != "ok" {
			writeError(w, 403, "insufficient balance")
			return
		}

		// Slot limit: $1000 balance = 2 simultaneous active responses.
		// Each additional $1000 grants one more slot (3 slots at $2000, etc.).
		var activeResponses int
		h.DB.QueryRow(`
			SELECT COUNT(*) FROM responses
			WHERE counselor_hash = ? AND status IN ('pending', 'accepted')
		`, counselorHash).Scan(&activeResponses)

		var minRequired float64
		h.DB.QueryRow(`SELECT min_required_usd FROM wallet_sessions WHERE wallet_hash = ?`,
			counselorHash).Scan(&minRequired)

		// maxSlots = floor(minRequired / 1000) * 2, minimum 2 at $1000
		maxSlots := int(minRequired/1000) * 2
		if maxSlots < 2 {
			maxSlots = 2
		}
		if activeResponses >= maxSlots {
			writeError(w, 403, "need higher balance for more response slots")
			return
		}
	}

	now := time.Now().Unix()

	// Serialise the listing-slot check + INSERT inside a transaction to prevent
	// two concurrent counselors both slipping in when pendingCount == 1.
	tx, err := h.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Check listing exists and is active — also fetch city for region lock check
	var listingStatus, listingCity string
	if err = tx.QueryRow(`SELECT status, city FROM listings WHERE id = ? AND status = 'active'`, listingID).Scan(&listingStatus, &listingCity); err != nil {
		writeError(w, 404, "listing not found or not active")
		return
	}

	// Region lock: first response permanently locks the peer to this city.
	// Atomic UPDATE (WHERE region = '') ensures only the first committing transaction sets the lock —
	// no separate SELECT-then-UPDATE race. We then read back the actual region to verify.
	res, err := tx.Exec(`UPDATE reputation SET region = ? WHERE counselor_hash = ? AND region = ''`,
		listingCity, counselorHash)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	var lockedRegion string
	if err = tx.QueryRow(`SELECT region FROM reputation WHERE counselor_hash = ?`, counselorHash).Scan(&lockedRegion); err != nil {
		// Row missing — reputation entry should always exist after wallet registration.
		writeError(w, 500, "reputation record missing")
		return
	}
	// If UPDATE affected 0 rows AND region is still empty, the row existed but was in an unexpected state.
	if n, _ := res.RowsAffected(); n == 0 && lockedRegion == "" {
		writeError(w, 500, "region lock failed")
		return
	}
	if lockedRegion != listingCity {
		writeJSON(w, 403, map[string]string{
			"error":         "region_locked",
			"locked_region": lockedRegion,
		})
		return
	}

	// Max 2 pending responses per listing
	var pendingCount int
	tx.QueryRow(`SELECT COUNT(*) FROM responses WHERE listing_id = ? AND status = 'pending'`, listingID).Scan(&pendingCount)
	if pendingCount >= 2 {
		writeError(w, 409, "listing already has maximum responses")
		return
	}

	// Check cooldown on this listing
	var cooldownCount int
	tx.QueryRow(`
		SELECT COUNT(*) FROM responses
		WHERE counselor_hash = ? AND listing_id = ? AND cooldown_until > ?
	`, counselorHash, listingID, now).Scan(&cooldownCount)
	if cooldownCount > 0 {
		writeError(w, 429, "cooldown active for this listing")
		return
	}

	// Check not already responded to this listing
	var existingCount int
	tx.QueryRow(`
		SELECT COUNT(*) FROM responses
		WHERE counselor_hash = ? AND listing_id = ? AND status = 'pending'
	`, counselorHash, listingID).Scan(&existingCount)
	if existingCount > 0 {
		writeError(w, 409, "already responded to this listing")
		return
	}

	responseID := crypto.NewID("rsp")
	_, err = tx.Exec(`
		INSERT INTO responses (id, listing_id, counselor_hash, counselor_pubkey, status, created_at)
		VALUES (?, ?, ?, ?, 'pending', ?)
	`, responseID, listingID, counselorHash, req.CounselorPubkey, now)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}

	if err = tx.Commit(); err != nil {
		writeError(w, 500, "db error")
		return
	}

	// Notify the client via Telegram that someone replied (fire-and-forget)
	go telegram.NotifyClientReply(context.Background(), h.DB, h.Telegram, listingID)

	writeJSON(w, 201, map[string]string{
		"response_id": responseID,
		"status":      "pending",
	})
}

// GetPeerRegion handles GET /peer/region — returns the peer's locked city (or null if first time).
func (h *Handler) GetPeerRegion(w http.ResponseWriter, r *http.Request) {
	counselorHash := middleware.SessionWalletHash(r.Context())
	if counselorHash == "" {
		writeError(w, 401, "session required")
		return
	}

	var region string
	h.DB.QueryRow(`SELECT region FROM reputation WHERE counselor_hash = ?`, counselorHash).Scan(&region)

	if region == "" {
		writeJSON(w, 200, map[string]any{"region": nil})
	} else {
		writeJSON(w, 200, map[string]any{"region": region})
	}
}

// CancelResponse handles POST /response/{id}/cancel — counselor cancels pending response.
func (h *Handler) CancelResponse(w http.ResponseWriter, r *http.Request) {
	responseID := chi.URLParam(r, "id")

	counselorHash := middleware.SessionWalletHash(r.Context())
	if counselorHash == "" {
		writeError(w, 401, "session required")
		return
	}

	// Check response belongs to this counselor and is pending
	var listingID string
	err := h.DB.QueryRow(`
		SELECT listing_id FROM responses
		WHERE id = ? AND counselor_hash = ? AND status = 'pending'
	`, responseID, counselorHash).Scan(&listingID)
	if err != nil {
		writeError(w, 404, "response not found or not cancellable")
		return
	}

	now := time.Now().Unix()
	cooldownUntil := now + 1800 // 30 min

	_, err = h.DB.Exec(`
		UPDATE responses SET status = 'cancelled', cancelled_at = ?, cooldown_until = ?
		WHERE id = ?
	`, now, cooldownUntil, responseID)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}

	writeJSON(w, 200, map[string]any{
		"status":         "cancelled",
		"cooldown_until": cooldownUntil,
	})
}
