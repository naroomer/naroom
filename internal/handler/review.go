package handler

import (
	"net/http"
	"time"
)

type reviewReq struct {
	Token  string `json:"token"`
	Rating string `json:"rating"` // "up" or "down"
}

// Review handles POST /review — anonymous thumbs up/down.
// No auth, no wallet, just a one-time token.
func (h *Handler) Review(w http.ResponseWriter, r *http.Request) {
	var req reviewReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}

	if req.Token == "" {
		writeError(w, 400, "token required")
		return
	}
	if req.Rating != "up" && req.Rating != "down" {
		writeError(w, 400, "rating must be up or down")
		return
	}

	now := time.Now().Unix()

	// Find token, check it's valid
	var counselorHash string
	var used bool
	var expiresAt int64
	err := h.DB.QueryRow(`
		SELECT counselor_hash, used, expires_at FROM review_tokens WHERE token = ?
	`, req.Token).Scan(&counselorHash, &used, &expiresAt)
	if err != nil {
		writeError(w, 404, "invalid token")
		return
	}
	if used {
		writeError(w, 409, "token already used")
		return
	}
	if expiresAt < now {
		writeError(w, 410, "token expired")
		return
	}

	tx, err := h.DB.Begin()
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	defer tx.Rollback()

	// Increment reputation counter
	if req.Rating == "up" {
		tx.Exec(`UPDATE reputation SET thumbs_up = thumbs_up + 1 WHERE counselor_hash = ?`, counselorHash)
	} else {
		tx.Exec(`UPDATE reputation SET thumbs_down = thumbs_down + 1 WHERE counselor_hash = ?`, counselorHash)
	}

	// Delete token forever
	tx.Exec(`DELETE FROM review_tokens WHERE token = ?`, req.Token)

	if err := tx.Commit(); err != nil {
		writeError(w, 500, "db error")
		return
	}

	writeJSON(w, 200, map[string]string{"status": "recorded"})
}
