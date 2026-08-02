// Package v2 — Production Telegram sender for Informer outbox delivery.
//
// TelegramInformerBotSender classifies Bot API responses into retryable and
// permanent failures so that InformerWorker can apply the correct retry policy:
//
//   - 200 OK                     → success
//   - 429 Too Many Requests      → retryable (caller backs off)
//   - 5xx Server Error           → retryable
//   - 4xx (not 429)              → permanent (ErrInformerNotificationPermanent)
//     includes: 403 bot blocked / user deactivated, 400 chat not found, 410 Gone
//   - Network error              → retryable
//
// Privacy: token, chat_id, response body are never logged or returned in errors.
package v2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// TelegramInformerBotSender delivers Informer notifications via Telegram Bot API.
// It implements InformerBotSender.
type TelegramInformerBotSender struct {
	token      string // never logged or returned in errors
	baseURL    string // e.g. "https://api.telegram.org" (no trailing slash)
	httpClient *http.Client
}

// NewTelegramInformerBotSender creates a production sender for the given bot token.
// The token must be non-empty; it is stored internally and never logged.
func NewTelegramInformerBotSender(token string) (*TelegramInformerBotSender, error) {
	if token == "" {
		return nil, fmt.Errorf("v2: NewTelegramInformerBotSender: token must not be empty")
	}
	return &TelegramInformerBotSender{
		token:   token,
		baseURL: "https://api.telegram.org",
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}, nil
}

// newTelegramInformerBotSenderForTest creates a sender with an injected http.Client
// and base URL. For use in unit tests only (via httptest.Server).
// The scheme of baseURL must be "http" or "https".
func newTelegramInformerBotSenderForTest(token, baseURL string, client *http.Client) (*TelegramInformerBotSender, error) {
	if token == "" {
		return nil, fmt.Errorf("v2: sender: token must not be empty")
	}
	if baseURL == "" {
		return nil, fmt.Errorf("v2: sender: baseURL must not be empty")
	}
	return &TelegramInformerBotSender{token: token, baseURL: baseURL, httpClient: client}, nil
}

// SendInformerNotification sends a structured notification to the given
// Telegram private chat. When notification.ButtonURL is non-empty, the
// message includes a single inline keyboard button (ButtonText, ButtonURL)
// instead of the URL appearing in the visible text.
//
// Error classification:
//   - nil              → delivered
//   - retryable error  → transient (429, 5xx, network)
//   - ErrInformerNotificationPermanent (wrapped) → permanent (4xx except 429)
//
// The raw token, chat_id, and response body are never included in errors.
func (s *TelegramInformerBotSender) SendInformerNotification(ctx context.Context, chatID int64, notification InformerNotification) error {
	apiURL := s.baseURL + "/bot" + s.token + "/sendMessage"

	body := map[string]any{
		"chat_id": chatID,
		"text":    notification.Text,
	}
	if notification.ButtonURL != "" {
		body["reply_markup"] = map[string]any{
			"inline_keyboard": [][]map[string]string{
				{{"text": notification.ButtonText, "url": notification.ButtonURL}},
			},
		}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("v2: TelegramInformerBotSender: marshal: [internal]")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("v2: TelegramInformerBotSender: build request: [internal]")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("v2: TelegramInformerBotSender: context cancelled: [internal]")
		}
		// Network error → retryable.
		return fmt.Errorf("v2: TelegramInformerBotSender: network: [internal]")
	}
	defer resp.Body.Close()
	// Drain body for connection reuse; ignore content.
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) //nolint:errcheck

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	// 429 Too Many Requests → retryable.
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("v2: TelegramInformerBotSender: rate limited (429): [internal]")
	}

	// 5xx Server Error → retryable.
	if resp.StatusCode >= 500 {
		return fmt.Errorf("v2: TelegramInformerBotSender: server error %d: [internal]", resp.StatusCode)
	}

	// All other 4xx (403 blocked/deactivated, 400 chat not found, 410 Gone, etc.)
	// → permanent failure: this subscriber will never receive messages.
	return fmt.Errorf("%w: status %d: [internal]", ErrInformerNotificationPermanent, resp.StatusCode)
}

// Ensure TelegramInformerBotSender implements InformerBotSender.
var _ InformerBotSender = (*TelegramInformerBotSender)(nil)
