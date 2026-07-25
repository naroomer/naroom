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
	"errors"
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
	if err := s.SendInformerNotification(context.Background(), 12345, "hello"); err != nil {
		t.Errorf("200 OK: expected nil error, got: %v", err)
	}
}

// TestTelegramSender_429_Retryable verifies that 429 is retryable (not permanent).
func TestTelegramSender_429_Retryable(t *testing.T) {
	srv := newStaticServer(t, http.StatusTooManyRequests)
	s := newTestSenderWith(t, srv)
	err := s.SendInformerNotification(context.Background(), 12345, "hello")
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
	err := s.SendInformerNotification(context.Background(), 12345, "hello")
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
	err := s.SendInformerNotification(context.Background(), 12345, "hello")
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
	err := s.SendInformerNotification(context.Background(), 12345, "hello")
	if !errors.Is(err, ErrInformerNotificationPermanent) {
		t.Errorf("400: expected ErrInformerNotificationPermanent, got: %v", err)
	}
}

// TestTelegramSender_403_Permanent verifies that 403 (bot blocked) is permanent.
func TestTelegramSender_403_Permanent(t *testing.T) {
	srv := newStaticServer(t, http.StatusForbidden)
	s := newTestSenderWith(t, srv)
	err := s.SendInformerNotification(context.Background(), 12345, "hello")
	if !errors.Is(err, ErrInformerNotificationPermanent) {
		t.Errorf("403: expected ErrInformerNotificationPermanent, got: %v", err)
	}
}

// TestTelegramSender_410_Permanent verifies that 410 (Gone) is permanent.
func TestTelegramSender_410_Permanent(t *testing.T) {
	srv := newStaticServer(t, http.StatusGone)
	s := newTestSenderWith(t, srv)
	err := s.SendInformerNotification(context.Background(), 12345, "hello")
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

	err = sender.SendInformerNotification(context.Background(), 12345, "hello")
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

	err := s.SendInformerNotification(ctx, 12345, "hello")
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

	sendErr := sender.SendInformerNotification(context.Background(), chatID, "test message")
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
