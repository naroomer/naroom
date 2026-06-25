package handler

import (
	"net/http"
	"strconv"
	"time"

	"naroom/internal/crypto"
)

// ChatPollSend handles POST /chat/poll/send — Tor fallback for sending messages.
func (h *Handler) ChatPollSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RoomID     string `json:"room_id"`
		Pubkey     string `json:"pubkey"`
		Nonce      string `json:"nonce"`
		Ciphertext string `json:"ciphertext"`
		MsgType    string `json:"msg_type"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}

	if req.RoomID == "" || req.Pubkey == "" || req.Nonce == "" || req.Ciphertext == "" {
		writeError(w, 400, "all fields required")
		return
	}

	msgType := req.MsgType
	if msgType != "text" && msgType != "image_file" && msgType != "image_camera" {
		msgType = "text"
	}

	// Verify participant
	var status, clientPubkey, counselorPubkey string
	var expiresAt int64
	err := h.DB.QueryRow(`
		SELECT status, client_pubkey, counselor_pubkey, expires_at
		FROM chat_rooms WHERE id = ?
	`, req.RoomID).Scan(&status, &clientPubkey, &counselorPubkey, &expiresAt)
	if err != nil {
		writeError(w, 404, "room not found")
		return
	}
	if status != "active" || expiresAt < time.Now().Unix() {
		writeError(w, 410, "room closed")
		return
	}
	if req.Pubkey != clientPubkey && req.Pubkey != counselorPubkey {
		writeError(w, 403, "not a participant")
		return
	}

	now := time.Now().Unix()
	msgID := crypto.NewID("msg")

	_, err = h.DB.Exec(`
		INSERT INTO encrypted_messages (id, room_id, sender_pubkey, nonce, ciphertext, msg_type, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, msgID, req.RoomID, req.Pubkey, req.Nonce, req.Ciphertext, msgType, now)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}

	writeJSON(w, 201, map[string]string{"id": msgID})
}

// ChatPollReceive handles GET /chat/poll/receive?room_id=xxx&pubkey=xxx&since=timestamp.
func (h *Handler) ChatPollReceive(w http.ResponseWriter, r *http.Request) {
	roomID := r.URL.Query().Get("room_id")
	pubkey := r.URL.Query().Get("pubkey")
	sinceStr := r.URL.Query().Get("since")

	if roomID == "" || pubkey == "" {
		writeError(w, 400, "room_id and pubkey required")
		return
	}

	var since int64
	if sinceStr != "" {
		since, _ = strconv.ParseInt(sinceStr, 10, 64)
	}

	// Verify participant
	var status, clientPubkey, counselorPubkey string
	err := h.DB.QueryRow(`
		SELECT status, client_pubkey, counselor_pubkey
		FROM chat_rooms WHERE id = ?
	`, roomID).Scan(&status, &clientPubkey, &counselorPubkey)
	if err != nil {
		writeError(w, 404, "room not found")
		return
	}
	if pubkey != clientPubkey && pubkey != counselorPubkey {
		writeError(w, 403, "not a participant")
		return
	}

	rows, err := h.DB.Query(`
		SELECT id, sender_pubkey, nonce, ciphertext, msg_type, created_at
		FROM encrypted_messages
		WHERE room_id = ? AND created_at > ?
		ORDER BY created_at ASC
		LIMIT 100
	`, roomID, since)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	defer rows.Close()

	type msg struct {
		ID           string `json:"id"`
		SenderPubkey string `json:"sender_pubkey"`
		Nonce        string `json:"nonce"`
		Ciphertext   string `json:"ciphertext"`
		MsgType      string `json:"msg_type"`
		CreatedAt    int64  `json:"created_at"`
	}

	messages := []msg{}
	for rows.Next() {
		var m msg
		if err := rows.Scan(&m.ID, &m.SenderPubkey, &m.Nonce, &m.Ciphertext, &m.MsgType, &m.CreatedAt); err != nil {
			continue
		}
		messages = append(messages, m)
	}

	writeJSON(w, 200, map[string]any{
		"messages":   messages,
		"room_status": status,
	})
}
