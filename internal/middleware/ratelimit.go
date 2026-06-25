package middleware

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// entry holds a rate limiter and the last time it was accessed.
type entry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimiter holds per-key token-bucket limiters with periodic cleanup.
type RateLimiter struct {
	mu      sync.Mutex
	entries map[string]*entry
	r       rate.Limit // tokens per second
	burst   int
}

// NewRateLimiter creates a limiter. r is events/second, burst is bucket size.
func NewRateLimiter(r rate.Limit, burst int) *RateLimiter {
	rl := &RateLimiter{
		entries: make(map[string]*entry),
		r:       r,
		burst:   burst,
	}
	go rl.cleanup()
	return rl
}

func (rl *RateLimiter) allow(key string) bool {
	rl.mu.Lock()
	e, ok := rl.entries[key]
	if !ok {
		e = &entry{limiter: rate.NewLimiter(rl.r, rl.burst)}
		rl.entries[key] = e
	}
	e.lastSeen = time.Now()
	ok = e.limiter.Allow()
	rl.mu.Unlock()
	return ok
}

// cleanup removes entries not seen for 10 minutes.
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-10 * time.Minute)
		rl.mu.Lock()
		for k, e := range rl.entries {
			if e.lastSeen.Before(cutoff) {
				delete(rl.entries, k)
			}
		}
		rl.mu.Unlock()
	}
}

// hashIP returns a stable non-reversible key for the request IP.
// Uses /24 subnet for IPv4, /48 for IPv6 — avoids exact IP logging.
func hashIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		h := sha256.Sum256([]byte(host))
		return fmt.Sprintf("ip:%x", h[:8])
	}
	var subnet string
	if ip4 := ip.To4(); ip4 != nil {
		// mask to /24
		subnet = fmt.Sprintf("%d.%d.%d", ip4[0], ip4[1], ip4[2])
	} else {
		// mask to /48
		subnet = fmt.Sprintf("%x:%x:%x", ip[0:2], ip[2:4], ip[4:6])
	}
	h := sha256.Sum256([]byte(subnet))
	return fmt.Sprintf("ip:%x", h[:8])
}

// Limit returns middleware that enforces this limiter using keyFn to derive the bucket key.
// keyFn receives the request; return empty string to skip limiting for that request.
func (rl *RateLimiter) Limit(keyFn func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := keyFn(r)
			if key != "" && !rl.allow(key) {
				http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ByIP is a convenience key function: limit per hashed IP subnet.
func ByIP(r *http.Request) string {
	return hashIP(r)
}

// NoLimit is a key function that disables rate limiting (returns empty key).
// Use in dev/test mode to avoid throttling E2E tests.
func NoLimit(*http.Request) string { return "" }

// ByWallet limits by wallet_address query param or JSON body wallet_address field.
// Falls back to IP if wallet not present.
// NOTE: used only for pre-auth endpoints; post-auth use session middleware.
func ByWalletOrIP(r *http.Request) string {
	if w := r.URL.Query().Get("wallet_address"); w != "" {
		h := sha256.Sum256([]byte(w))
		return fmt.Sprintf("wallet:%x", h[:8])
	}
	return hashIP(r)
}
