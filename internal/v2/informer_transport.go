// Package v2 — Telegram webhook transport for the Informer bot.
//
// Completely isolated from TelegramTransport (the Client notification bot).
// Handles /start <token> → Subscribe and /stop → Unsubscribe.
// Webhook secret validated constant-time before any body parsing.
package v2

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// InformerTransport handles Telegram webhook updates for the Informer bot.
type InformerTransport struct {
	svc           *InformerService
	webhookSecret []byte
	botUsername   string
	now           func() time.Time
}

// NewInformerTransport creates an InformerTransport.
func NewInformerTransport(
	svc *InformerService,
	webhookSecret []byte,
	botUsername string,
	now func() time.Time,
) (*InformerTransport, error) {
	if svc == nil {
		return nil, errors.New("v2: NewInformerTransport: svc must not be nil")
	}
	if len(webhookSecret) == 0 {
		return nil, errors.New("v2: NewInformerTransport: webhookSecret must not be empty")
	}
	if botUsername == "" {
		return nil, errors.New("v2: NewInformerTransport: botUsername must not be empty")
	}
	if now == nil {
		now = time.Now
	}
	return &InformerTransport{
		svc: svc, webhookSecret: webhookSecret,
		botUsername: botUsername, now: now,
	}, nil
}

// HandleWebhook handles POST /v2/telegram/informer/webhook.
// Validates X-Telegram-Bot-Api-Secret-Token header before parsing body.
func (t *InformerTransport) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	// Constant-time webhook secret validation.
	got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	expected := sha256.Sum256(t.webhookSecret)
	gotDigest := sha256.Sum256([]byte(got))
	if !hmac.Equal(expected[:], gotDigest[:]) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	var update struct {
		Message *struct {
			MessageID int64 `json:"message_id"`
			Chat      struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
			Text string `json:"text"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &update); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	if update.Message == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	msg := update.Message
	// Only accept private chats with positive IDs.
	if msg.Chat.Type != "private" || msg.Chat.ID <= 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	text := strings.TrimSpace(msg.Text)
	switch {
	case strings.HasPrefix(text, "/start "):
		rawToken := strings.TrimSpace(strings.TrimPrefix(text, "/start "))
		if rawToken != "" {
			if err := t.svc.Subscribe(msg.Chat.ID, rawToken); err != nil {
				// Token errors are not retried by Telegram; always 200.
			}
		}
	case text == "/stop":
		_ = t.svc.Unsubscribe(msg.Chat.ID)
	}
	w.WriteHeader(http.StatusOK)
}

// Routes returns an http.Handler for the Informer webhook.
func (t *InformerTransport) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/telegram/informer/webhook", t.HandleWebhook)
	return mux
}
