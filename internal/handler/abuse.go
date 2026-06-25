package handler

import (
	"database/sql"
	"net/http"
	"time"

	"naroom/internal/crypto"
	"naroom/internal/middleware"
)

type abuseReportReq struct {
	RoomID     string   `json:"room_id"`
	Categories []string `json:"categories"` // misuse, threatening, drugs, links, other
}

var validAbuseCategories = map[string]bool{
	"misuse": true, "threatening": true, "drugs": true, "links": true, "other": true,
}

// AbuseReport handles POST /abuse-report — counselor (peer) reports a client.
// Counselor identity is resolved from session. room_id proves participation.
func (h *Handler) AbuseReport(w http.ResponseWriter, r *http.Request) {
	counselorHash := middleware.SessionWalletHash(r.Context())
	if counselorHash == "" {
		writeError(w, 401, "session required")
		return
	}
	role := middleware.SessionRole(r.Context())
	if role != "peer" {
		writeError(w, 403, "only peers can submit abuse reports")
		return
	}

	var req abuseReportReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}
	if req.RoomID == "" || len(req.Categories) == 0 {
		writeError(w, 400, "room_id and categories required")
		return
	}

	for _, cat := range req.Categories {
		if !validAbuseCategories[cat] {
			writeError(w, 400, "invalid category: "+cat)
			return
		}
	}

	// Verify counselor participated in this room and retrieve stored client_hash.
	var clientHash string
	err := h.DB.QueryRow(`
		SELECT client_hash FROM chat_rooms
		WHERE id = ? AND counselor_hash = ?
	`, req.RoomID, counselorHash).Scan(&clientHash)
	if err == sql.ErrNoRows {
		writeError(w, 403, "you are not a participant in this room")
		return
	}
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	pairHash := crypto.Hash(counselorHash, clientHash)

	// Check dedup — one counselor can report one client only once per room
	var dedupCount int
	h.DB.QueryRow(`SELECT COUNT(*) FROM abuse_dedup WHERE pair_hash = ?`, pairHash).Scan(&dedupCount)
	if dedupCount > 0 {
		writeError(w, 409, "already reported this client")
		return
	}

	now := time.Now().Unix()
	dedupExpires := now + 30*24*3600 // 30 days

	tx, err := h.DB.Begin()
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	defer tx.Rollback()

	// Insert dedup record
	tx.Exec(`INSERT INTO abuse_dedup (pair_hash, created_at, expires_at) VALUES (?, ?, ?)`,
		pairHash, now, dedupExpires)

	// Upsert abuse counters
	tx.Exec(`INSERT OR IGNORE INTO abuse_counters (client_hash) VALUES (?)`, clientHash)

	for _, cat := range req.Categories {
		switch cat {
		case "misuse":
			tx.Exec(`UPDATE abuse_counters SET abuse_misuse = abuse_misuse + 1, total = total + 1 WHERE client_hash = ?`, clientHash)
		case "threatening":
			tx.Exec(`UPDATE abuse_counters SET abuse_threatening = abuse_threatening + 1, total = total + 1 WHERE client_hash = ?`, clientHash)
		case "drugs":
			tx.Exec(`UPDATE abuse_counters SET abuse_drugs = abuse_drugs + 1, total = total + 1 WHERE client_hash = ?`, clientHash)
		case "links":
			tx.Exec(`UPDATE abuse_counters SET abuse_links = abuse_links + 1, total = total + 1 WHERE client_hash = ?`, clientHash)
		case "other":
			tx.Exec(`UPDATE abuse_counters SET abuse_other = abuse_other + 1, total = total + 1 WHERE client_hash = ?`, clientHash)
		}
	}

	// Check thresholds
	var total int
	tx.QueryRow(`SELECT total FROM abuse_counters WHERE client_hash = ?`, clientHash).Scan(&total)

	if total >= 5 {
		// Permanent ban (far future)
		tx.Exec(`UPDATE abuse_counters SET banned_until = ? WHERE client_hash = ?`, now+10*365*24*3600, clientHash)
	} else if total >= 3 {
		// 72h ban
		tx.Exec(`UPDATE abuse_counters SET banned_until = ? WHERE client_hash = ?`, now+259200, clientHash)
	}

	if err := tx.Commit(); err != nil {
		writeError(w, 500, "db error")
		return
	}

	writeJSON(w, 200, map[string]string{"status": "reported"})
}
