package handler

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"naroom/internal/crypto"
	"naroom/internal/middleware"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// ChatHub manages active WebSocket connections per room.
type ChatHub struct {
	mu    sync.RWMutex
	rooms map[string]map[string]*wsConn // room_id → wallet_hash → conn
}

type wsConn struct {
	conn   *websocket.Conn
	cancel context.CancelFunc
}

func NewChatHub() *ChatHub {
	return &ChatHub{
		rooms: make(map[string]map[string]*wsConn),
	}
}

type wsMessage struct {
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
	MsgType    string `json:"msg_type"` // text | image_file | image_camera
}

type wsOutMessage struct {
	ID           string `json:"id"`
	SenderPubkey string `json:"sender_pubkey"`
	Nonce        string `json:"nonce"`
	Ciphertext   string `json:"ciphertext"`
	MsgType      string `json:"msg_type"`
	CreatedAt    int64  `json:"created_at"`
}

// ChatWS handles WS /chat/ws?room_id=xxx.
// Session token is passed via Sec-WebSocket-Protocol header (browser sends it when
// the second argument to `new WebSocket(url, [token])` is set).
// The server echoes back the accepted subprotocol so the browser's WebSocket handshake succeeds.
func (h *Handler) ChatWS(hub *ChatHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := r.URL.Query().Get("room_id")
		if roomID == "" {
			writeError(w, 400, "room_id required")
			return
		}

		// Resolve wallet identity (wallet_hash).
		// Priority 1: RequireSession middleware sets walletHash via Authorization header.
		// Priority 2: Token from Sec-WebSocket-Protocol header (browser WS API, can't send custom headers).
		walletHash := middleware.SessionWalletHash(r.Context())
		wsProtoToken := "" // set when auth was via Sec-WebSocket-Protocol; must be echoed back
		if walletHash == "" {
			rawToken := r.Header.Get("Sec-WebSocket-Protocol")
			if rawToken != "" {
				tokenHash := middleware.HashToken(rawToken)
				now := time.Now().Unix()
				h.DB.QueryRow(`
					SELECT wallet_hash FROM sessions
					WHERE token_hash = ? AND expires_at > ? AND revoked_at IS NULL
				`, tokenHash, now).Scan(&walletHash)
				if walletHash != "" {
					wsProtoToken = rawToken
				}
			}
		}
		if walletHash == "" {
			writeError(w, 401, "session required")
			return
		}

		// Determine pubkey from wallet identity via hash comparison
		var roomStatus string
		var clientPubkey, counselorPubkey, clientHash, counselorHash string
		err := h.DB.QueryRow(`
			SELECT status, client_pubkey, counselor_pubkey, client_hash, counselor_hash
			FROM chat_rooms WHERE id = ?
		`, roomID).Scan(&roomStatus, &clientPubkey, &counselorPubkey, &clientHash, &counselorHash)
		if err != nil {
			writeError(w, 404, "room not found")
			return
		}
		if roomStatus != "active" && roomStatus != "peer_left" {
			writeError(w, 410, "room closed")
			return
		}

		var pubkey string
		if walletHash == clientHash {
			pubkey = clientPubkey
		} else if walletHash == counselorHash {
			pubkey = counselorPubkey
		} else {
			writeError(w, 403, "not a participant")
			return
		}

		acceptOpts := &websocket.AcceptOptions{
			OriginPatterns: []string{"*"}, // tighten in production
		}
		// Echo back the accepted subprotocol — browser requires this for the handshake to succeed.
		if wsProtoToken != "" {
			acceptOpts.Subprotocols = []string{wsProtoToken}
		}
		conn, err := websocket.Accept(w, r, acceptOpts)
		if err != nil {
			return
		}
		conn.SetReadLimit(8 * 1024 * 1024) // 8MB — для изображений

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// Register connection keyed by wallet_hash (never stores plain address in memory map)
		hub.mu.Lock()
		if hub.rooms[roomID] == nil {
			hub.rooms[roomID] = make(map[string]*wsConn)
		}
		hub.rooms[roomID][walletHash] = &wsConn{conn: conn, cancel: cancel}
		hub.mu.Unlock()

		defer func() {
			hub.mu.Lock()
			delete(hub.rooms[roomID], walletHash)
			if len(hub.rooms[roomID]) == 0 {
				delete(hub.rooms, roomID)
			}
			hub.mu.Unlock()
			conn.Close(websocket.StatusNormalClosure, "")
		}()

		// Send history (messages still in DB)
		h.sendHistory(ctx, conn, roomID)

		// Heartbeat
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					conn.Ping(ctx)
				}
			}
		}()

		// Read loop
		for {
			var msg wsMessage
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				return
			}

			if msg.Nonce == "" || msg.Ciphertext == "" {
				continue
			}

			// Validate msg_type
			msgType := msg.MsgType
			if msgType != "text" && msgType != "image_file" && msgType != "image_camera" {
				msgType = "text"
			}

			now := time.Now().Unix()
			msgID := crypto.NewID("msg")

			// Save encrypted message
			h.DB.Exec(`
				INSERT INTO encrypted_messages (id, room_id, sender_pubkey, nonce, ciphertext, msg_type, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)
			`, msgID, roomID, pubkey, msg.Nonce, msg.Ciphertext, msgType, now)

			// Forward to other participant
			out := wsOutMessage{
				ID:           msgID,
				SenderPubkey: pubkey,
				Nonce:        msg.Nonce,
				Ciphertext:   msg.Ciphertext,
				MsgType:      msgType,
				CreatedAt:    now,
			}

			hub.mu.RLock()
			if room, ok := hub.rooms[roomID]; ok {
				for pk, wsc := range room {
					if pk != pubkey {
						wsjson.Write(ctx, wsc.conn, out)
					}
				}
			}
			hub.mu.RUnlock()
		}
	}
}

func (h *Handler) sendHistory(ctx context.Context, conn *websocket.Conn, roomID string) {
	rows, err := h.DB.Query(`
		SELECT id, sender_pubkey, nonce, ciphertext, msg_type, created_at
		FROM encrypted_messages
		WHERE room_id = ?
		ORDER BY created_at ASC
	`, roomID)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var msg wsOutMessage
		if err := rows.Scan(&msg.ID, &msg.SenderPubkey, &msg.Nonce, &msg.Ciphertext, &msg.MsgType, &msg.CreatedAt); err != nil {
			continue
		}
		if err := wsjson.Write(ctx, conn, msg); err != nil {
			return
		}
	}
}

// ResumeChat handles GET /resume — returns any active chat room for this wallet (peer or client).
// Used when a participant loses their session and needs to find their room.
func (h *Handler) ResumeChat(w http.ResponseWriter, r *http.Request) {
	walletHash := middleware.SessionWalletHash(r.Context())
	if walletHash == "" {
		writeError(w, 401, "session required")
		return
	}
	var roomID string
	err := h.DB.QueryRow(`
		SELECT id FROM chat_rooms
		WHERE (counselor_hash = ? OR client_hash = ?) AND status = 'active'
		ORDER BY started_at DESC LIMIT 1
	`, walletHash, walletHash).Scan(&roomID)
	if err != nil {
		writeError(w, 404, "no active chat room")
		return
	}
	writeJSON(w, 200, map[string]any{"room_id": roomID})
}

// ResumePeerChat handles GET /peer/resume — returns any active chat room for this peer.
// Used when peer loses their session and needs to find their room without a listing_id.
func (h *Handler) ResumePeerChat(w http.ResponseWriter, r *http.Request) {
	walletHash := middleware.SessionWalletHash(r.Context())
	if walletHash == "" {
		writeError(w, 401, "session required")
		return
	}
	var roomID string
	err := h.DB.QueryRow(`
		SELECT id FROM chat_rooms
		WHERE counselor_hash = ? AND status = 'active'
		ORDER BY started_at DESC LIMIT 1
	`, walletHash).Scan(&roomID)
	if err != nil {
		writeError(w, 404, "no active chat room")
		return
	}
	writeJSON(w, 200, map[string]any{"room_id": roomID})
}

// GetCounselorChatRoom handles GET /peer/chatroom?listing_id=Y
// Counselor polls this to know when client accepted and chat room opened.
// listing_id scopes the lookup to prevent stale rooms from previous sessions being returned.
func (h *Handler) GetCounselorChatRoom(w http.ResponseWriter, r *http.Request) {
	walletHash := middleware.SessionWalletHash(r.Context())
	listingID := r.URL.Query().Get("listing_id")
	if walletHash == "" || listingID == "" {
		writeError(w, 400, "listing_id required")
		return
	}

	var roomID, status string
	var expiresAt int64
	err := h.DB.QueryRow(`
		SELECT id, status, expires_at FROM chat_rooms
		WHERE counselor_hash = ? AND listing_id = ? AND status = 'active'
		ORDER BY started_at DESC LIMIT 1
	`, walletHash, listingID).Scan(&roomID, &status, &expiresAt)
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

// GetChatRoom handles GET /chat/{room_id} — returns room metadata for a participant.
// Participant identity resolved from session.
func (h *Handler) GetChatRoom(w http.ResponseWriter, r *http.Request) {
	roomID := chi.URLParam(r, "room_id")
	walletHash := middleware.SessionWalletHash(r.Context())
	if roomID == "" || walletHash == "" {
		writeError(w, 400, "room_id required")
		return
	}

	var status, clientPubkey, counselorPubkey, clientHash, counselorHash string
	var startedAt, expiresAt int64
	var peerLeftAt sql.NullInt64
	err := h.DB.QueryRow(`
		SELECT status, client_pubkey, counselor_pubkey, client_hash, counselor_hash, started_at, expires_at,
		       peer_left_at
		FROM chat_rooms WHERE id = ?
	`, roomID).Scan(&status, &clientPubkey, &counselorPubkey, &clientHash, &counselorHash, &startedAt, &expiresAt,
		&peerLeftAt)
	if err != nil {
		writeError(w, 404, "room not found")
		return
	}
	if walletHash != clientHash && walletHash != counselorHash {
		writeError(w, 403, "not a participant")
		return
	}

	// If the OTHER side left (not us), we can still access the room in read mode.
	// If WE left (our status), treat as closed for us.
	if (walletHash == clientHash && status == "client_left") ||
		(walletHash == counselorHash && status == "peer_left") {
		// This side already left — treat as closed for them
		writeJSON(w, 200, map[string]any{"room_id": roomID, "status": "closed"})
		return
	}

	role := "client"
	myPubkey := clientPubkey
	peerPubkey := counselorPubkey
	if walletHash == counselorHash {
		role = "peer"
		myPubkey = counselorPubkey
		peerPubkey = clientPubkey
	}

	resp := map[string]any{
		"room_id":     roomID,
		"status":      status,
		"role":        role,
		"my_pubkey":   myPubkey,
		"peer_pubkey": peerPubkey,
		"started_at":  startedAt,
		"expires_at":  expiresAt,
	}
	if peerLeftAt.Valid {
		resp["peer_left_at"] = peerLeftAt.Int64
	}
	writeJSON(w, 200, resp)
}

// UpdateChatPubkey handles POST /chat/{room_id}/pubkey — lets a participant update
// their stored public key after session loss and keypair regeneration.
// Only the public key is updated; private keys never leave the browser.
func (h *Handler) UpdateChatPubkey(w http.ResponseWriter, r *http.Request) {
	roomID := chi.URLParam(r, "room_id")
	walletHash := middleware.SessionWalletHash(r.Context())
	if roomID == "" || walletHash == "" {
		writeError(w, 400, "room_id required")
		return
	}
	var req struct {
		Pubkey string `json:"pubkey"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Pubkey == "" {
		writeError(w, 400, "pubkey required")
		return
	}

	var clientHash, counselorHash, status string
	err := h.DB.QueryRow(`SELECT client_hash, counselor_hash, status FROM chat_rooms WHERE id = ?`, roomID).
		Scan(&clientHash, &counselorHash, &status)
	if err != nil {
		writeError(w, 404, "room not found")
		return
	}
	if status != "active" {
		writeError(w, 410, "room not active")
		return
	}

	var col string
	if walletHash == clientHash {
		col = "client_pubkey"
	} else if walletHash == counselorHash {
		col = "counselor_pubkey"
	} else {
		writeError(w, 403, "not a participant")
		return
	}

	if _, err := h.DB.Exec(`UPDATE chat_rooms SET `+col+` = ? WHERE id = ?`, req.Pubkey, roomID); err != nil {
		writeError(w, 500, "db error")
		return
	}
	// Notify the other participant via WS so they reload the room and recompute shared key
	if h.Hub != nil {
		h.Hub.broadcastSystem(roomID, walletHash, wsSystemMsg{Type: "system", Event: "pubkey_updated"})
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// wsSystemMsg is sent over WebSocket to notify participants of room state changes.
type wsSystemMsg struct {
	Type  string `json:"type"`  // always "system"
	Event string `json:"event"` // "peer_left" | "room_closed"
}

// broadcastSystem sends a system event to all WS connections in a room except the sender.
// senderKey is the wallet_hash of the sender (hub key).
func (hub *ChatHub) broadcastSystem(roomID, senderKey string, event wsSystemMsg) {
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	if room, ok := hub.rooms[roomID]; ok {
		for key, wsc := range room {
			if key != senderKey {
				wsjson.Write(context.Background(), wsc.conn, event)
			}
		}
	}
}

// CloseChat handles POST /chat/{room_id}/close.
//
// Rules:
//   - Peer closes   → room stays open (status stays 'active'), peer_left_at set.
//     Client receives WS "peer_left" event. Peer gets 200 {"status":"peer_left"}.
//   - Client closes → room closed permanently. Peer receives WS "room_closed".
//     Listing restored, review token issued if eligible.
func (h *Handler) CloseChat(w http.ResponseWriter, r *http.Request) {
	roomID := chi.URLParam(r, "room_id")
	if roomID == "" {
		writeError(w, 400, "room_id required")
		return
	}

	walletHash := middleware.SessionWalletHash(r.Context())
	if walletHash == "" {
		writeError(w, 401, "session required")
		return
	}

	var clientPubkey, counselorPubkey, clientHash, counselorHash, responseID string
	var startedAt int64
	var status string
	err := h.DB.QueryRow(`
		SELECT status, client_pubkey, counselor_pubkey, client_hash, counselor_hash, started_at, response_id
		FROM chat_rooms WHERE id = ?
	`, roomID).Scan(&status, &clientPubkey, &counselorPubkey, &clientHash, &counselorHash, &startedAt, &responseID)
	if err == sql.ErrNoRows {
		writeError(w, 404, "room not found")
		return
	}
	// Allow close if active, peer_left (client closing after peer left),
	// or client_left (peer closing after client left).
	if status != "active" && status != "peer_left" && status != "client_left" {
		writeError(w, 410, "room already closed")
		return
	}
	if walletHash != clientHash && walletHash != counselorHash {
		writeError(w, 403, "not a participant")
		return
	}

	now := time.Now().Unix()

	isPeer   := walletHash == counselorHash
	isClient := walletHash == clientHash

	// ── One side leaves first → mark their departure, other side keeps messages ──
	// If the OTHER side already left, this is the final close → delete messages.
	otherAlreadyLeft := (isPeer && status == "client_left") || (isClient && status == "peer_left")

	if !otherAlreadyLeft {
		// First person to leave — room stays accessible for the other side.
		var newStatus, col, wsEvent string
		if isPeer {
			newStatus = "peer_left"
			col       = "peer_left_at"
			wsEvent   = "peer_left"
		} else {
			newStatus = "client_left"
			col       = "client_left_at"
			wsEvent   = "client_left"
		}
		res, err := h.DB.Exec(`
			UPDATE chat_rooms SET status = ?, `+col+` = ? WHERE id = ? AND status = 'active'
		`, newStatus, now, roomID)
		if err != nil {
			writeError(w, 500, "db error")
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			writeJSON(w, 200, map[string]any{"status": "already_closed"})
			return
		}
		if h.Hub != nil {
			h.Hub.broadcastSystem(roomID, walletHash, wsSystemMsg{Type: "system", Event: wsEvent})
		}
		writeJSON(w, 200, map[string]any{"status": newStatus})
		return
	}

	// ── Both sides have now left → fully close, delete messages ──────────
	closedBy := "client"
	if isPeer {
		closedBy = "peer"
	}

	tx, err := h.DB.Begin()
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	defer tx.Rollback()

	res, err := tx.Exec(`
		UPDATE chat_rooms SET status = 'closed', closed_at = ?, closed_by = ?
		WHERE id = ? AND status IN ('peer_left', 'client_left')
	`, now, closedBy, roomID)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		tx.Rollback()
		writeJSON(w, 200, map[string]any{"status": "already_closed"})
		return
	}
	tx.Exec(`UPDATE responses SET status = 'closed' WHERE id = ?`, responseID)
	tx.Exec(`
		UPDATE listings SET status = 'active'
		WHERE id = (SELECT listing_id FROM chat_rooms WHERE id = ?)
		  AND status = 'matched' AND visible_until > ?
	`, roomID, now)

	chatDuration := now - startedAt
	minDuration := int64(6 * 3600)
	if h.DevMode {
		minDuration = 0
	}

	resp := map[string]any{"status": "closed"}

	if chatDuration >= minDuration {
		tx.Exec(`UPDATE reputation SET sessions_total = sessions_total + 1, sessions_completed = sessions_completed + 1 WHERE counselor_hash = ?`, counselorHash)
		token := crypto.RandomToken()
		tx.Exec(`
			INSERT INTO review_tokens (token, counselor_hash, is_paid, used, created_at, expires_at)
			VALUES (?, ?, TRUE, FALSE, ?, ?)
		`, token, counselorHash, now, now+86400)
		resp["review_token"] = token
	} else {
		tx.Exec(`UPDATE reputation SET sessions_total = sessions_total + 1, sessions_early_exit = sessions_early_exit + 1 WHERE counselor_hash = ?`, counselorHash)
		log.Printf("chat %s closed by %s after %ds", roomID, closedBy, chatDuration)
	}

	if err := tx.Commit(); err != nil {
		writeError(w, 500, "db error")
		return
	}

	h.DB.Exec(`DELETE FROM encrypted_messages WHERE room_id = ?`, roomID)

	if h.Hub != nil {
		h.Hub.broadcastSystem(roomID, walletHash, wsSystemMsg{Type: "system", Event: "room_closed"})
	}

	writeJSON(w, 200, resp)
}
