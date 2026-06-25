package middleware

import (
	"bufio"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// NoLogIP logs requests WITHOUT IP addresses, query strings, or path parameters.
// Logs only the route pattern (e.g. /listing/{id}) so no user identifiers appear in logs.
func NoLogIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(ww, r)

		// Use chi route pattern (/listing/{id}) instead of actual path (/listing/lst_abc123).
		// This prevents IDs, room IDs, wallet addresses from appearing in logs.
		routePattern := chi.RouteContext(r.Context()).RoutePattern()
		if routePattern == "" {
			routePattern = r.URL.Path // fallback for unmatched routes
		}
		log.Printf("%s %s %d %s", r.Method, routePattern, ww.status, time.Since(start).Round(time.Millisecond))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Hijack implements http.Hijacker so WebSocket upgrades work through this middleware.
// Without this, nhooyr/websocket returns 501 Not Implemented.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hj.Hijack()
}
