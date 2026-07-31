// Package v2 — City summary HTTP handler (Task 11 H2.2).
//
// GET /v2/board/cities — public endpoint, no auth required.
// Returns city registry enriched with active listing counts.
// Uses a 30-second server-side cache.
package v2

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

const (
	cityCacheTTL = 30 * time.Second
	sampleCount  = 3 // matches the three isolated board sample journeys
)

type cityCacheEntry struct {
	data      []citySummaryJSON
	expiresAt time.Time
}

// CitySummaryHandler serves GET /v2/board/cities.
type CitySummaryHandler struct {
	db    *sql.DB
	now   func() time.Time
	lim   *fixedWindowLimiter
	mu    sync.Mutex
	cache *cityCacheEntry
}

type citySummaryJSON struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	CountryCode  string `json:"country_code"`
	CountryLabel string `json:"country_label"`
	ActiveCount  int    `json:"active_count"`
	SampleCount  int    `json:"sample_count"`
}

// NewCitySummaryHandler creates a CitySummaryHandler.
func NewCitySummaryHandler(db *sql.DB, now func() time.Time) *CitySummaryHandler {
	if now == nil {
		now = time.Now
	}
	return &CitySummaryHandler{
		db:  db,
		now: now,
		lim: newFixedWindowLimiter(60, time.Minute, defaultMaxLimiterEntries, now),
	}
}

// Routes returns an http.Handler for the city summary endpoint.
func (h *CitySummaryHandler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/board/cities", h.handleCities)
	return mux
}

func (h *CitySummaryHandler) handleCities(w http.ResponseWriter, r *http.Request) {
	// Rate limit by the request's real client IP (see RealClientIP).
	host := RealClientIP(r)
	if !h.lim.Allow(host) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]string{"error": "rate limit exceeded"}) //nolint:errcheck
		return
	}

	now := h.now()

	h.mu.Lock()
	if h.cache != nil && now.Before(h.cache.expiresAt) {
		data := h.cache.data
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(data) //nolint:errcheck
		return
	}
	h.mu.Unlock()

	// Build counts from DB.
	nowUnix := now.Unix()
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT city, COUNT(*) as cnt
		FROM v2_listings
		WHERE state = 'visible' AND visible_until > ?
		GROUP BY city`, nowUnix)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "internal error"}) //nolint:errcheck
		return
	}
	defer rows.Close()

	activeCounts := make(map[string]int)
	for rows.Next() {
		var city string
		var cnt int
		if scanErr := rows.Scan(&city, &cnt); scanErr != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "internal error"}) //nolint:errcheck
			return
		}
		activeCounts[city] = cnt
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "internal error"}) //nolint:errcheck
		return
	}

	cities := EnabledCities()
	result := make([]citySummaryJSON, 0, len(cities))
	for _, c := range cities {
		result = append(result, citySummaryJSON{
			ID:           c.ID,
			Label:        c.Label,
			CountryCode:  c.CountryCode,
			CountryLabel: CountryLabel(c.CountryCode),
			ActiveCount:  activeCounts[c.ID],
			SampleCount:  sampleCount,
		})
	}

	// Only cache on success.
	h.mu.Lock()
	h.cache = &cityCacheEntry{
		data:      result,
		expiresAt: now.Add(cityCacheTTL),
	}
	h.mu.Unlock()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(result) //nolint:errcheck
}
