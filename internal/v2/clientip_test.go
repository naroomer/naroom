package v2

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Test matrix for RealClientIP (V2 rate-limit real-IP fix):
//
// | 1 | TestRealClientIP_DirectRequest_UsesRemoteAddr        | direct request → RemoteAddr             |
// | 2 | TestRealClientIP_TrustedIPv4LoopbackProxy            | loopback peer + XFF IPv4 → forwarded IP  |
// | 3 | TestRealClientIP_TrustedIPv6LoopbackProxy            | loopback peer + XFF IPv6 → forwarded IP  |
// | 4 | TestRealClientIP_UntrustedPeerCannotSpoofViaHeader   | non-loopback peer + XFF → RemoteAddr wins |
// | 5 | TestRealClientIP_MalformedProxyHeader_FallsBackSafely | loopback peer + garbage XFF → RemoteAddr |
// | 6 | TestRealClientIP_TwoProxiedIPs_GetDistinctKeys       | two different proxied IPs → distinct keys |
// | 7 | TestRealClientIP_SameIP_HitsExistingLimit429         | same derived key → limiter denies at burst|

func reqWithRemoteAddr(remoteAddr string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v2/board/cities", nil)
	r.RemoteAddr = remoteAddr
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestRealClientIP_DirectRequest_UsesRemoteAddr(t *testing.T) {
	r := reqWithRemoteAddr("203.0.113.7:54321", map[string]string{
		"X-Forwarded-For": "198.51.100.9",
		"X-Real-IP":       "198.51.100.9",
	})
	got := RealClientIP(r)
	if got != "203.0.113.7" {
		t.Fatalf("direct request: got %q, want RemoteAddr 203.0.113.7 (proxy headers must be ignored when peer is not loopback)", got)
	}
}

func TestRealClientIP_TrustedIPv4LoopbackProxy(t *testing.T) {
	r := reqWithRemoteAddr("127.0.0.1:9000", map[string]string{
		"X-Forwarded-For": "198.51.100.42",
	})
	got := RealClientIP(r)
	if got != "198.51.100.42" {
		t.Fatalf("trusted IPv4 loopback proxy: got %q, want forwarded 198.51.100.42", got)
	}
}

func TestRealClientIP_TrustedIPv6LoopbackProxy(t *testing.T) {
	r := reqWithRemoteAddr("[::1]:9000", map[string]string{
		"X-Forwarded-For": "2001:db8::42",
	})
	got := RealClientIP(r)
	want := "2001:db8::42"
	if got != want {
		t.Fatalf("trusted IPv6 loopback proxy: got %q, want forwarded %q", got, want)
	}
}

func TestRealClientIP_UntrustedPeerCannotSpoofViaHeader(t *testing.T) {
	// A public client connecting directly must not be able to override its own
	// rate-limit identity by sending X-Forwarded-For/X-Real-IP itself.
	r := reqWithRemoteAddr("198.51.100.200:1234", map[string]string{
		"X-Forwarded-For": "1.2.3.4",
		"X-Real-IP":       "5.6.7.8",
	})
	got := RealClientIP(r)
	if got != "198.51.100.200" {
		t.Fatalf("spoof attempt: got %q, want RemoteAddr 198.51.100.200 (headers must be ignored for non-loopback peer)", got)
	}
}

func TestRealClientIP_MalformedProxyHeader_FallsBackSafely(t *testing.T) {
	r := reqWithRemoteAddr("127.0.0.1:9000", map[string]string{
		"X-Forwarded-For": "not-an-ip, also not one",
	})
	got := RealClientIP(r)
	if got != "127.0.0.1" {
		t.Fatalf("malformed XFF: got %q, want safe fallback to RemoteAddr 127.0.0.1", got)
	}

	r2 := reqWithRemoteAddr("127.0.0.1:9000", nil)
	got2 := RealClientIP(r2)
	if got2 != "127.0.0.1" {
		t.Fatalf("missing XFF from trusted proxy: got %q, want safe fallback to RemoteAddr 127.0.0.1", got2)
	}
}

func TestRealClientIP_TwoProxiedIPs_GetDistinctKeys(t *testing.T) {
	rA := reqWithRemoteAddr("127.0.0.1:1", map[string]string{"X-Forwarded-For": "10.0.0.1"})
	rB := reqWithRemoteAddr("127.0.0.1:2", map[string]string{"X-Forwarded-For": "10.0.0.2"})
	ipA := RealClientIP(rA)
	ipB := RealClientIP(rB)
	if ipA == ipB {
		t.Fatalf("two distinct proxied clients resolved to the same IP: %q", ipA)
	}
	if ipA != "10.0.0.1" || ipB != "10.0.0.2" {
		t.Fatalf("got ipA=%q ipB=%q, want 10.0.0.1 / 10.0.0.2", ipA, ipB)
	}

	// Distinctness must also propagate through the shared HMAC key-building
	// path used by every V2 rate limiter, not just the raw IP.
	rateKey := []byte("test-rate-limit-key-32-bytes-ok")
	ch := &ClientHandler{rateLimitKey: rateKey}
	keyA := ch.clientKey(rA)
	keyB := ch.clientKey(rB)
	if keyA == keyB {
		t.Fatalf("clientKey collided for two distinct proxied clients")
	}
}

func TestRealClientIP_SameIP_HitsExistingLimit429(t *testing.T) {
	// Does not modify any production limiter's configured limit/burst/TTL —
	// this constructs its own limiter instance purely to prove that requests
	// bucketed by RealClientIP-derived keys are still correctly throttled at
	// whatever limit a handler configures.
	fixedNow := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	lim := newFixedWindowLimiter(2, time.Minute, defaultMaxLimiterEntries, func() time.Time { return fixedNow })

	r := reqWithRemoteAddr("127.0.0.1:1", map[string]string{"X-Forwarded-For": "10.0.0.9"})
	key := RealClientIP(r)

	if !lim.Allow(key) {
		t.Fatalf("1st request from same IP should be allowed")
	}
	if !lim.Allow(key) {
		t.Fatalf("2nd request from same IP should be allowed (limit=2)")
	}
	if lim.Allow(key) {
		t.Fatalf("3rd request from same IP within the window should be denied (429) — limit=2 exceeded")
	}

	// A different proxied IP must have its own independent bucket.
	r2 := reqWithRemoteAddr("127.0.0.1:2", map[string]string{"X-Forwarded-For": "10.0.0.10"})
	key2 := RealClientIP(r2)
	if !lim.Allow(key2) {
		t.Fatalf("a different client IP must not be blocked by another client's exhausted bucket")
	}
}
