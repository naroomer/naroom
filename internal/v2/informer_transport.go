// Package v2 — Telegram webhook transport for the Informer bot.
//
// Completely isolated from TelegramTransport (the Client notification bot).
// Handles /start <token> → Subscribe and /stop → Unsubscribe.
// Webhook secret validated constant-time before any body parsing.
package v2

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func cityName(id string) string {
	if c, ok := CityByID(id); ok {
		return c.Label
	}
	return id
}

// InformerTransport handles Telegram webhook updates for the Informer bot.
type InformerTransport struct {
	svc           *InformerService
	webhookSecret []byte
	botUsername   string
	now           func() time.Time
	sender        InformerBotSender // optional; set via SetSender
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

// SetSender wires in the InformerBotSender used to reply to users.
// Must be called before the first webhook is processed.
func (t *InformerTransport) SetSender(s InformerBotSender) { t.sender = s }

// send is a nil-safe helper: sends text if sender is configured.
func (t *InformerTransport) send(ctx context.Context, chatID int64, text string) {
	if t.sender != nil {
		_ = t.sender.SendInformerNotification(ctx, chatID, text)
	}
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

	ctx := r.Context()
	text := strings.TrimSpace(msg.Text)
	chatID := msg.Chat.ID

	switch {
	case strings.HasPrefix(text, "/start "):
		rawToken := strings.TrimSpace(strings.TrimPrefix(text, "/start "))
		if rawToken == "" {
			// Treat malformed "/start " (token empty after trim) as plain /start.
			t.send(ctx, chatID, informerStartInstruction())
			break
		}
		err := t.svc.Subscribe(chatID, rawToken)
		switch {
		case err == nil:
			// Successful subscription: look up city for the confirmation message.
			city := t.subscribedCity(ctx, chatID)
			t.send(ctx, chatID, fmt.Sprintf(
				"NA Room: Informer connected for %s. You will receive new listing notifications. Send /stop to unsubscribe.",
				cityName(city),
			))
		case errors.Is(err, ErrInformerTokenClaimed):
			// Token already used — subscription exists. No duplicate; no message needed.
		case errors.Is(err, ErrInformerTokenExpired):
			t.send(ctx, chatID, "NA Room: This link has expired (15 min). Please visit naroom.net/v2/informer to get a new link.")
		case errors.Is(err, ErrInformerTokenNotFound):
			t.send(ctx, chatID, "NA Room: This link is invalid. Please visit naroom.net/v2/informer to get a new link.")
		default:
			// Unexpected error — do not reveal internals; Telegram will not retry 200 responses.
		}

	case text == "/start":
		// Plain /start with no token — guide the user.
		t.send(ctx, chatID, informerStartInstruction())

	case text == "/stop":
		_ = t.svc.Unsubscribe(chatID)
		t.send(ctx, chatID, "NA Room: You have been unsubscribed from listing notifications. Visit naroom.net/v2/informer to subscribe again.")
	}

	w.WriteHeader(http.StatusOK)
}

// subscribedCity looks up the city for chatID's active subscription.
// Returns the raw city ID as fallback (never empty — Subscribe just succeeded).
func (t *InformerTransport) subscribedCity(ctx context.Context, chatID int64) string {
	_, city, err := t.svc.HasActiveSubscription(chatID)
	if err != nil || city == "" {
		return ""
	}
	return city
}

// informerStartInstruction returns the plain-/start help message.
func informerStartInstruction() string {
	return "NA Room: To subscribe as an Informer and receive new listing notifications, visit naroom.net/v2/informer"
}

// Routes returns an http.Handler for the Informer webhook.
func (t *InformerTransport) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/telegram/informer/webhook", t.HandleWebhook)
	return mux
}
