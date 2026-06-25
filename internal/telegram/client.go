package telegram

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	ClientReplyMessage = "NA Room: someone replied to your request. Open NA Room to continue."
	HelperConfirmText  = "NA Room: board notifications enabled for 24 hours."
	ClientConfirmText  = "NA Room: notifications connected. You will receive a notification if someone replies."
)

// Sender is implemented by the real Telegram client and by tests.
type Sender interface {
	SendClientMessage(ctx context.Context, chatID, text string) error
	SendHelperMessage(ctx context.Context, chatID, text string) error
}

// Client sends messages through two separate Telegram bots.
type Client struct {
	ClientBotToken string
	HelperBotToken string
	HTTPClient     *http.Client
}

func NewClient(clientBotToken, helperBotToken string) *Client {
	return &Client{
		ClientBotToken: clientBotToken,
		HelperBotToken: helperBotToken,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *Client) SendClientMessage(ctx context.Context, chatID, text string) error {
	return c.send(ctx, c.ClientBotToken, chatID, text)
}

func (c *Client) SendHelperMessage(ctx context.Context, chatID, text string) error {
	return c.send(ctx, c.HelperBotToken, chatID, text)
}

func (c *Client) send(ctx context.Context, token, chatID, text string) error {
	if token == "" {
		return fmt.Errorf("telegram bot token not configured")
	}
	body, err := json.Marshal(map[string]any{
		"chat_id": chatID,
		"text":    text,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram send failed with status %d", resp.StatusCode)
	}
	return nil
}

// NotifyClientReply sends the neutral client notification when a helper replies.
func NotifyClientReply(ctx context.Context, db *sql.DB, sender Sender, listingID string) error {
	if sender == nil {
		return nil
	}
	now := time.Now().Unix()
	rows, err := db.Query(`
		SELECT telegram_chat_id
		FROM client_listing_notifications
		WHERE listing_id = ? AND active = TRUE AND expires_at > ?
	`, listingID, now)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var chatID string
		if err := rows.Scan(&chatID); err != nil {
			continue
		}
		_ = sender.SendClientMessage(ctx, chatID, ClientReplyMessage)
	}
	return rows.Err()
}

// NotifyMatchingHelpers sends board notifications to active helper subscriptions matching the listing.
func NotifyMatchingHelpers(ctx context.Context, db *sql.DB, sender Sender, listingID, boardURL string) error {
	if sender == nil {
		return nil
	}
	var city, problem, helpType, urgency, langsRaw string
	err := db.QueryRow(`
		SELECT city, dependency_type, help_type, urgency, languages
		FROM listings WHERE id = ? AND status = 'active'
	`, listingID).Scan(&city, &problem, &helpType, &urgency, &langsRaw)
	if err != nil {
		return err
	}

	var languages []string
	_ = json.Unmarshal([]byte(langsRaw), &languages)
	if len(languages) == 0 {
		languages = []string{""}
	}

	now := time.Now().Unix()
	rows, err := db.Query(`
		SELECT telegram_chat_id,
		       COALESCE(city, ''), COALESCE(language, ''),
		       COALESCE(problem, ''), COALESCE(help_type, ''), COALESCE(urgency, '')
		FROM helper_board_subscriptions
		WHERE active = TRUE AND expires_at > ?
	`, now)
	if err != nil {
		return err
	}
	defer rows.Close()

	sent := make(map[string]bool)
	for rows.Next() {
		var chatID, subCity, subLang, subProblem, subHelp, subUrgency string
		if err := rows.Scan(&chatID, &subCity, &subLang, &subProblem, &subHelp, &subUrgency); err != nil {
			continue
		}
		if sent[chatID] {
			continue
		}
		if !matchesFilter(subCity, city) || !matchesFilter(subProblem, problem) ||
			!matchesFilter(subHelp, helpType) || !matchesFilter(subUrgency, urgency) ||
			!langMatchesFilter(subLang, languages) {
			continue
		}
		sent[chatID] = true
		lang := subLang
		if lang == "" {
			lang = strings.Join(languages, ", ")
		}
		msg := fmt.Sprintf("New request on NA Room\n\nCity: %s\nLanguage: %s\nTopic: %s\nNeed: %s\nUrgency: %s\n\nOpen board: %s",
			city, lang, problem, helpType, urgency, boardURL)
		_ = sender.SendHelperMessage(ctx, chatID, msg)
	}
	return rows.Err()
}

func matchesFilter(filter, value string) bool {
	return filter == "" || filter == value
}

func langMatchesFilter(filter string, values []string) bool {
	if filter == "" {
		return true
	}
	for _, v := range values {
		if v == filter {
			return true
		}
	}
	return false
}
