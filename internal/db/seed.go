package db

import (
	"database/sql"
	"log"
	"time"
)

// SeedSamples inserts demo listings for each city if none exist yet.
// These are marked is_sample=1 so the UI can show a "Sample" badge.
// They never expire (visible_until = far future) and can't be responded to.
func SeedSamples(db *sql.DB) {
	type sample struct {
		city       string
		dep        string
		help       string
		urgency    string
		langs      string
	}

	samples := []sample{
		// Tbilisi
		{"tbilisi", "alcohol", "relapse_prevention", "soon", `["en","ru","ka"]`},
		{"tbilisi", "gambling", "just_talk", "can_wait", `["en","ru"]`},
		// Batumi
		{"batumi", "alcohol", "just_talk", "soon", `["en","ru","ka"]`},
		{"batumi", "cannabis", "motivation", "can_wait", `["ru","ka"]`},
		// Nha Trang
		{"nha_trang", "opioids", "crisis", "urgent", `["en"]`},
		{"nha_trang", "cannabis", "motivation", "can_wait", `["en"]`},
		// Da Nang
		{"da_nang", "alcohol", "just_talk", "soon", `["en"]`},
		{"da_nang", "stimulants", "relapse_prevention", "can_wait", `["en"]`},
		// Buenos Aires
		{"buenos_aires", "stimulants", "crisis", "urgent", `["en","es"]`},
		{"buenos_aires", "alcohol", "recovery_plan", "soon", `["es"]`},
		// Sao Paulo
		{"sao_paulo", "polysubstance", "just_talk", "soon", `["en","es"]`},
		{"sao_paulo", "gambling", "relapse_prevention", "can_wait", `["es"]`},
		// Almaty
		{"almaty", "opioids", "recovery_plan", "soon", `["ru"]`},
		{"almaty", "alcohol", "motivation", "can_wait", `["ru","en"]`},
		// Yerevan
		{"yerevan", "alcohol", "just_talk", "can_wait", `["ru","en"]`},
		{"yerevan", "cannabis", "relapse_prevention", "soon", `["ru"]`},
		// Moscow
		{"moscow", "alcohol", "crisis", "urgent", `["ru"]`},
		{"moscow", "opioids", "just_talk", "soon", `["ru","en"]`},
	}

	farFuture := time.Now().Add(365 * 24 * time.Hour).Unix()
	now := time.Now().Unix()

	inserted := 0
	for _, s := range samples {
		// Check if sample already exists for this city+dep+help
		var count int
		db.QueryRow(`SELECT COUNT(*) FROM listings WHERE city=? AND dependency_type=? AND help_type=? AND is_sample=1`,
			s.city, s.dep, s.help).Scan(&count)
		if count > 0 {
			continue
		}

		id := "sample_" + s.city + "_" + s.dep + "_" + s.help
		_, err := db.Exec(`
			INSERT OR IGNORE INTO listings
			  (id, city, dependency_type, help_type, urgency, languages,
			   wallet_hash, visible_until, created_at, status, is_sample)
			VALUES (?, ?, ?, ?, ?, ?, '_sample', ?, ?, 'active', 1)
		`, id, s.city, s.dep, s.help, s.urgency, s.langs, farFuture, now)
		if err != nil {
			log.Printf("seed: %v", err)
		} else {
			inserted++
		}
	}

	if inserted > 0 {
		log.Printf("seed: inserted %d sample listings", inserted)
	}
}
