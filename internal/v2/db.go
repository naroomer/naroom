package v2

import (
	"database/sql"
	_ "embed"
	"fmt"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// ApplySchema runs the V2 DDL on db. Idempotent: uses CREATE TABLE/INDEX IF NOT EXISTS.
func ApplySchema(db *sql.DB) error {
	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("v2: apply schema: %w", err)
	}
	return nil
}

// OpenMemory opens an in-memory SQLite database with V2 schema applied.
// Intended for tests only.
func OpenMemory() (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file::memory:?mode=memory&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("v2: open memory db: %w", err)
	}
	// MaxOpenConns=1 serializes writers; required by SQLite.
	db.SetMaxOpenConns(1)
	// Enable foreign key enforcement. The _foreign_keys DSN parameter is not
	// reliably honored by all versions of modernc.org/sqlite, so we apply it
	// explicitly. With MaxOpenConns=1 this PRAGMA persists for the lifetime of
	// the single connection.
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("v2: enable foreign keys: %w", err)
	}
	if err := ApplySchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
