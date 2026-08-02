package v2

// §4 tests for TelegramInformerBotSender HTTP status classification.
//
// Uses newTelegramInformerBotSenderForTest (unexported) with httptest.Server so
// the real HTTP dispatch code is exercised against a controlled server.
//
// Classification contract:
//   200            → nil (success)
//   429            → retryable (NOT ErrInformerNotificationPermanent)
//   5xx            → retryable
//   4xx except 429 → ErrInformerNotificationPermanent (permanent)
//   network/timeout → retryable
//   context cancel  → returns (no hang, no panic)
//   token/chatID   → never appear in error text

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestSenderWith creates a TelegramInformerBotSender pointed at the given
// httptest.Server. Short timeout so timeout tests don't slow the suite.
func newTestSenderWith(t *testing.T, srv *httptest.Server) *TelegramInformerBotSender {
	t.Helper()
	s, err := newTelegramInformerBotSenderForTest(
		"secret-token-1234",
		srv.URL,
		&http.Client{Timeout: 200 * time.Millisecond},
	)
	if err != nil {
		t.Fatalf("newTelegramInformerBotSenderForTest: %v", err)
	}
	return s
}

// newStaticServer returns an httptest.Server that always replies with the given
// HTTP status code.
func newStaticServer(t *testing.T, code int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestTelegramSender_200_Success verifies that a 200 response is treated as success (nil error).
func TestTelegramSender_200_Success(t *testing.T) {
	srv := newStaticServer(t, http.StatusOK)
	s := newTestSenderWith(t, srv)
	if err := s.SendInformerNotification(context.Background(), 12345, InformerNotification{Text: "hello"}); err != nil {
		t.Errorf("200 OK: expected nil error, got: %v", err)
	}
}

// TestTelegramSender_429_Retryable verifies that 429 is retryable (not permanent).
func TestTelegramSender_429_Retryable(t *testing.T) {
	srv := newStaticServer(t, http.StatusTooManyRequests)
	s := newTestSenderWith(t, srv)
	err := s.SendInformerNotification(context.Background(), 12345, InformerNotification{Text: "hello"})
	if err == nil {
		t.Fatal("429: expected non-nil error")
	}
	if errors.Is(err, ErrInformerNotificationPermanent) {
		t.Error("429: must NOT be permanent — it is retryable")
	}
}

// TestTelegramSender_500_Retryable verifies that 500 is retryable.
func TestTelegramSender_500_Retryable(t *testing.T) {
	srv := newStaticServer(t, http.StatusInternalServerError)
	s := newTestSenderWith(t, srv)
	err := s.SendInformerNotification(context.Background(), 12345, InformerNotification{Text: "hello"})
	if err == nil {
		t.Fatal("500: expected non-nil error")
	}
	if errors.Is(err, ErrInformerNotificationPermanent) {
		t.Error("500: must NOT be permanent — it is retryable")
	}
}

// TestTelegramSender_503_Retryable verifies that 503 is retryable.
func TestTelegramSender_503_Retryable(t *testing.T) {
	srv := newStaticServer(t, http.StatusServiceUnavailable)
	s := newTestSenderWith(t, srv)
	err := s.SendInformerNotification(context.Background(), 12345, InformerNotification{Text: "hello"})
	if err == nil {
		t.Fatal("503: expected non-nil error")
	}
	if errors.Is(err, ErrInformerNotificationPermanent) {
		t.Error("503: must NOT be permanent — it is retryable")
	}
}

// TestTelegramSender_400_Permanent verifies that 400 is a permanent failure.
func TestTelegramSender_400_Permanent(t *testing.T) {
	srv := newStaticServer(t, http.StatusBadRequest)
	s := newTestSenderWith(t, srv)
	err := s.SendInformerNotification(context.Background(), 12345, InformerNotification{Text: "hello"})
	if !errors.Is(err, ErrInformerNotificationPermanent) {
		t.Errorf("400: expected ErrInformerNotificationPermanent, got: %v", err)
	}
}

// TestTelegramSender_403_Permanent verifies that 403 (bot blocked) is permanent.
func TestTelegramSender_403_Permanent(t *testing.T) {
	srv := newStaticServer(t, http.StatusForbidden)
	s := newTestSenderWith(t, srv)
	err := s.SendInformerNotification(context.Background(), 12345, InformerNotification{Text: "hello"})
	if !errors.Is(err, ErrInformerNotificationPermanent) {
		t.Errorf("403: expected ErrInformerNotificationPermanent, got: %v", err)
	}
}

// TestTelegramSender_410_Permanent verifies that 410 (Gone) is permanent.
func TestTelegramSender_410_Permanent(t *testing.T) {
	srv := newStaticServer(t, http.StatusGone)
	s := newTestSenderWith(t, srv)
	err := s.SendInformerNotification(context.Background(), 12345, InformerNotification{Text: "hello"})
	if !errors.Is(err, ErrInformerNotificationPermanent) {
		t.Errorf("410: expected ErrInformerNotificationPermanent, got: %v", err)
	}
}

// TestTelegramSender_Timeout_Retryable verifies that a server that doesn't
// respond within the client timeout causes a retryable (not permanent) error.
func TestTelegramSender_Timeout_Retryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the request context is done (client dropped) OR for at most
		// 2 seconds. The select prevents the handler from blocking srv.Close()
		// indefinitely during test cleanup.
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)

	// Use a very short 50ms timeout to avoid slowing the test suite.
	sender, err := newTelegramInformerBotSenderForTest(
		"secret-token-1234",
		srv.URL,
		&http.Client{Timeout: 50 * time.Millisecond},
	)
	if err != nil {
		t.Fatalf("newTelegramInformerBotSenderForTest: %v", err)
	}

	err = sender.SendInformerNotification(context.Background(), 12345, InformerNotification{Text: "hello"})
	if err == nil {
		t.Fatal("timeout: expected non-nil error")
	}
	if errors.Is(err, ErrInformerNotificationPermanent) {
		t.Error("timeout: must NOT be permanent — it is retryable")
	}
}

// TestTelegramSender_ContextCancelled verifies that context cancellation does
// not hang and does not produce a permanent error.
func TestTelegramSender_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)

	s := newTestSenderWith(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := s.SendInformerNotification(ctx, 12345, InformerNotification{Text: "hello"})
	// Must return quickly (not hang); error must not be permanent.
	if err == nil {
		t.Fatal("cancelled context: expected non-nil error")
	}
	if errors.Is(err, ErrInformerNotificationPermanent) {
		t.Error("cancelled context: must NOT be permanent — it is retryable/cancellation")
	}
}

// TestTelegramSender_TokenNotInErrors verifies that the bot token, chat_id, and
// response body never appear in error text — privacy invariant.
func TestTelegramSender_TokenNotInErrors(t *testing.T) {
	const secretToken = "secret-token-9999abcd"
	const chatID int64 = 987654321

	// Server returns a 403 so we get a non-nil error to inspect.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"bot was blocked"}`)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	sender, err := newTelegramInformerBotSenderForTest(
		secretToken, srv.URL,
		&http.Client{Timeout: 200 * time.Millisecond},
	)
	if err != nil {
		t.Fatalf("newTelegramInformerBotSenderForTest: %v", err)
	}

	sendErr := sender.SendInformerNotification(context.Background(), chatID, InformerNotification{Text: "test message"})
	if sendErr == nil {
		t.Fatal("expected non-nil error from 403")
	}
	errText := sendErr.Error()
	if strings.Contains(errText, secretToken) {
		t.Error("error message must NOT contain the bot token")
	}
	if strings.Contains(errText, "987654321") {
		t.Error("error message must NOT contain the chat_id")
	}
	if strings.Contains(errText, "bot was blocked") {
		t.Error("error message must NOT contain response body content")
	}
}

// ── §5 Inline keyboard "Open listing" button tests ────────────────────────────
//
// Task: "убрать длинный внутренний ID объявления из видимого Telegram-сообщения
// Informer, сохранив ссылку внутри кнопки Open listing".

// newCapturingServer returns an httptest.Server that always replies 200 OK and
// captures the last raw request body sent to it.
func newCapturingServer(t *testing.T) (*httptest.Server, *[]byte) {
	t.Helper()
	var lastBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		lastBody = b
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &lastBody
}

type capturedTelegramPayload struct {
	ChatID      int64  `json:"chat_id"`
	Text        string `json:"text"`
	ReplyMarkup *struct {
		InlineKeyboard [][]struct {
			Text string `json:"text"`
			URL  string `json:"url"`
		} `json:"inline_keyboard"`
	} `json:"reply_markup"`
}

// TestTelegramSender_ButtonURL_PresentAndCorrect (regression items 1, 2):
// a notification with ButtonURL set produces exactly one inline keyboard row
// with exactly one button, whose url is byte-identical to ButtonURL.
func TestTelegramSender_ButtonURL_PresentAndCorrect(t *testing.T) {
	srv, body := newCapturingServer(t)
	s := newTestSenderWith(t, srv)

	const wantURL = "https://naroom.net/v2/listing/a16859a9722e91418cb600d78658d27638cdfe258a82fe76f33651ecd1498e72"
	err := s.SendInformerNotification(context.Background(), 12345, InformerNotification{
		Text:       "New listing in Tbilisi",
		ButtonText: "Open listing",
		ButtonURL:  wantURL,
	})
	if err != nil {
		t.Fatalf("SendInformerNotification: %v", err)
	}

	var payload capturedTelegramPayload
	if jsonErr := json.Unmarshal(*body, &payload); jsonErr != nil {
		t.Fatalf("unmarshal captured Telegram request body: %v", jsonErr)
	}
	if payload.ReplyMarkup == nil {
		t.Fatal("no reply_markup in request — inline keyboard button missing")
	}
	if len(payload.ReplyMarkup.InlineKeyboard) != 1 || len(payload.ReplyMarkup.InlineKeyboard[0]) != 1 {
		t.Fatalf("expected exactly one row with exactly one button, got %+v", payload.ReplyMarkup.InlineKeyboard)
	}
	btn := payload.ReplyMarkup.InlineKeyboard[0][0]
	if btn.Text != "Open listing" {
		t.Errorf("button text = %q, want %q", btn.Text, "Open listing")
	}
	if btn.URL != wantURL {
		t.Errorf("button url = %q, want byte-identical %q", btn.URL, wantURL)
	}
}

// TestTelegramSender_VisibleText_NoURLNoListingID (regression item 3): the
// visible text must never contain the button URL or the raw listing_id, even
// though the button itself carries the full URL.
func TestTelegramSender_VisibleText_NoURLNoListingID(t *testing.T) {
	srv, body := newCapturingServer(t)
	s := newTestSenderWith(t, srv)

	const listingID = "a16859a9722e91418cb600d78658d27638cdfe258a82fe76f33651ecd1498e72"
	const buttonURL = "https://naroom.net/v2/listing/" + listingID
	err := s.SendInformerNotification(context.Background(), 12345, InformerNotification{
		Text:       "New listing in Tbilisi\nSample · crisis · urgent",
		ButtonText: "Open listing",
		ButtonURL:  buttonURL,
	})
	if err != nil {
		t.Fatalf("SendInformerNotification: %v", err)
	}

	var payload capturedTelegramPayload
	if jsonErr := json.Unmarshal(*body, &payload); jsonErr != nil {
		t.Fatalf("unmarshal captured Telegram request body: %v", jsonErr)
	}
	if strings.Contains(payload.Text, listingID) {
		t.Errorf("visible text leaks the raw listing_id: %q", payload.Text)
	}
	if strings.Contains(payload.Text, buttonURL) || strings.Contains(payload.Text, "https://") {
		t.Errorf("visible text leaks the full URL: %q", payload.Text)
	}
}

// TestTelegramSender_NoButtonURL_NoReplyMarkup: a notification with an empty
// ButtonURL (e.g. the Informer /start confirmation reply) must not attach any
// reply_markup at all — preserving the pre-existing plain-text-only behavior
// for non-listing notifications.
func TestTelegramSender_NoButtonURL_NoReplyMarkup(t *testing.T) {
	srv, body := newCapturingServer(t)
	s := newTestSenderWith(t, srv)

	err := s.SendInformerNotification(context.Background(), 12345, InformerNotification{Text: "Connected to naroom informer bot"})
	if err != nil {
		t.Fatalf("SendInformerNotification: %v", err)
	}

	var payload capturedTelegramPayload
	if jsonErr := json.Unmarshal(*body, &payload); jsonErr != nil {
		t.Fatalf("unmarshal captured Telegram request body: %v", jsonErr)
	}
	if payload.ReplyMarkup != nil {
		t.Errorf("expected no reply_markup for a button-less notification, got %+v", payload.ReplyMarkup)
	}
}
