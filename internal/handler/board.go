package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"naroom/internal/model"
)

// Board returns active listings for a city.
// GET /board/{city}
func (h *Handler) Board(w http.ResponseWriter, r *http.Request) {
	city := chi.URLParam(r, "city")
	if city == "" {
		writeError(w, 400, "city required")
		return
	}

	now := time.Now().Unix()
	rows, err := h.DB.Query(`
		SELECT l.id, l.city, l.dependency_type, l.help_type, l.urgency,
		       l.languages, l.visible_until, l.created_at,
		       (SELECT COUNT(*) FROM responses r WHERE r.listing_id = l.id AND r.status = 'pending') as resp_count,
		       l.is_sample
		FROM listings l
		WHERE l.city = ? AND l.status = 'active' AND l.visible_until > ?
		  AND COALESCE(l.opened_chats_count, 0) < 2
		ORDER BY l.is_sample ASC, l.created_at DESC
		LIMIT 50
	`, city, now)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	defer rows.Close()

	listings := []model.Listing{}
	for rows.Next() {
		var l model.Listing
		var langs string
		var isSample int
		if err := rows.Scan(&l.ID, &l.City, &l.DependencyType, &l.HelpType,
			&l.Urgency, &langs, &l.VisibleUntil, &l.CreatedAt, &l.ResponsesCount, &isSample); err != nil {
			continue
		}
		l.Status = "active"
		l.IsSample = isSample == 1
		json.Unmarshal([]byte(langs), &l.Languages)
		l.TimeLeft = l.VisibleUntil - now
		if l.TimeLeft < 0 {
			l.TimeLeft = 0
		}
		listings = append(listings, l)
	}

	writeJSON(w, 200, listings)
}
