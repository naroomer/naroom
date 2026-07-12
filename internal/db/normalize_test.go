package db

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openNormTestDB creates an in-memory SQLite DB with just the listings table
// (only the columns needed for NormalizeListingStatus).
func openNormTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`CREATE TABLE listings (
		id                  TEXT PRIMARY KEY,
		status              TEXT NOT NULL DEFAULT 'pending',
		opened_chats_count  INTEGER NOT NULL DEFAULT 0,
		visible_until       INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		t.Fatalf("create listings table: %v", err)
	}
	return db
}

func insertNormListing(t *testing.T, db *sql.DB, id, status string, count int, visibleUntil int64) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO listings (id, status, opened_chats_count, visible_until)
		VALUES (?, ?, ?, ?)`, id, status, count, visibleUntil)
	if err != nil {
		t.Fatalf("insert listing %s: %v", id, err)
	}
}

func normListingStatus(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM listings WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatalf("read status for %s: %v", id, err)
	}
	return status
}

// NORM-1: matched + count<2 + visible_until>now → active
func TestNormalizeListingStatus_MatchedToActive(t *testing.T) {
	db := openNormTestDB(t)
	now := time.Now().Unix()

	insertNormListing(t, db, "norm-1", "matched", 0, now+3600) // still visible
	insertNormListing(t, db, "norm-1b", "matched", 1, now+3600) // count=1, still visible

	NormalizeListingStatus(db)

	if s := normListingStatus(t, db, "norm-1"); s != "active" {
		t.Fatalf("NORM-1 FAIL: expected 'active', got %q", s)
	}
	if s := normListingStatus(t, db, "norm-1b"); s != "active" {
		t.Fatalf("NORM-1b FAIL: expected 'active', got %q", s)
	}
}

// NORM-2: matched + count<2 + visible_until≤now → expired
func TestNormalizeListingStatus_MatchedToExpired(t *testing.T) {
	db := openNormTestDB(t)
	now := time.Now().Unix()

	insertNormListing(t, db, "norm-2", "matched", 0, now-100) // already expired
	insertNormListing(t, db, "norm-2b", "matched", 1, now-1)  // expired by 1s

	NormalizeListingStatus(db)

	if s := normListingStatus(t, db, "norm-2"); s != "expired" {
		t.Fatalf("NORM-2 FAIL: expected 'expired', got %q", s)
	}
	if s := normListingStatus(t, db, "norm-2b"); s != "expired" {
		t.Fatalf("NORM-2b FAIL: expected 'expired', got %q", s)
	}
}

// NORM-3: any status + count≥2 + not closed → closed
func TestNormalizeListingStatus_CountGe2ToClosed(t *testing.T) {
	db := openNormTestDB(t)
	now := time.Now().Unix()

	insertNormListing(t, db, "norm-3a", "active", 2, now+3600)  // count=2, still active
	insertNormListing(t, db, "norm-3b", "matched", 2, now+3600) // count=2, matched
	insertNormListing(t, db, "norm-3c", "expired", 2, now-100)  // count=2, expired
	// Already closed: must not be re-touched (row remains 'closed')
	insertNormListing(t, db, "norm-3d", "closed", 2, now-100)

	NormalizeListingStatus(db)

	for _, id := range []string{"norm-3a", "norm-3b", "norm-3c", "norm-3d"} {
		if s := normListingStatus(t, db, id); s != "closed" {
			t.Fatalf("NORM-3 FAIL: %s expected 'closed', got %q", id, s)
		}
	}
}

// NORM-4: idempotent — running twice yields same result
func TestNormalizeListingStatus_Idempotent(t *testing.T) {
	db := openNormTestDB(t)
	now := time.Now().Unix()

	insertNormListing(t, db, "idem-1", "matched", 0, now+3600)  // → active
	insertNormListing(t, db, "idem-2", "matched", 0, now-100)   // → expired
	insertNormListing(t, db, "idem-3", "active", 2, now+3600)   // → closed

	NormalizeListingStatus(db)
	// Run a second time — must not change anything
	NormalizeListingStatus(db)

	cases := map[string]string{
		"idem-1": "active",
		"idem-2": "expired",
		"idem-3": "closed",
	}
	for id, want := range cases {
		if s := normListingStatus(t, db, id); s != want {
			t.Fatalf("NORM-4 idempotent FAIL: %s expected %q, got %q", id, want, s)
		}
	}
}
