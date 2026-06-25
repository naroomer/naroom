package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"log"

	ncrypto "naroom/internal/crypto"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Open opens SQLite database and runs DDL migrations.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_foreign_keys=ON&_synchronous=NORMAL", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	db.SetMaxOpenConns(1) // SQLite — один writer
	db.SetMaxIdleConns(1)

	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("run schema: %w", err)
	}

	// Column additions for existing databases (idempotent — errors are ignored)
	db.Exec(`ALTER TABLE encrypted_messages ADD COLUMN msg_type TEXT NOT NULL DEFAULT 'text'`)
	db.Exec(`ALTER TABLE chat_rooms ADD COLUMN peer_left_at INTEGER`)
	db.Exec(`ALTER TABLE invoices ADD COLUMN payment_detected_at INTEGER`)
	db.Exec(`ALTER TABLE invoices ADD COLUMN price_at_creation REAL`)

	// Schema cleanup migrations (must not silently fail if column/table is present)
	// wallet_challenges stored plain wallet_address and was never used by any handler.
	// Dropping it eliminates the plain-text address exposure entirely.
	db.Exec(`DROP TABLE IF EXISTS wallet_challenges`)
	db.Exec(`DROP INDEX IF EXISTS idx_wallet_challenges_wallet`)

	// reconnection_hashes was a stub feature never read by any handler or frontend.
	// ALTER TABLE … DROP COLUMN IF EXISTS is not valid SQLite syntax — check first.
	if columnExists(db, "listings", "reconnection_hashes") {
		if _, err := db.Exec(`ALTER TABLE listings DROP COLUMN reconnection_hashes`); err != nil {
			db.Close()
			return nil, fmt.Errorf("migration: drop listings.reconnection_hashes: %w", err)
		}
	}

	// wallet_hash was mistakenly added to Telegram tables — it links Telegram identity
	// to wallet_hash, which violates the privacy model. Remove if present.
	if columnExists(db, "helper_board_subscriptions", "wallet_hash") {
		if _, err := db.Exec(`ALTER TABLE helper_board_subscriptions DROP COLUMN wallet_hash`); err != nil {
			db.Close()
			return nil, fmt.Errorf("migration: drop helper_board_subscriptions.wallet_hash: %w", err)
		}
	}
	if columnExists(db, "telegram_link_tokens", "wallet_hash") {
		if _, err := db.Exec(`ALTER TABLE telegram_link_tokens DROP COLUMN wallet_hash`); err != nil {
			db.Close()
			return nil, fmt.Errorf("migration: drop telegram_link_tokens.wallet_hash: %w", err)
		}
	}

	return db, nil
}

// columnExists reports whether table t has a column named col.
func columnExists(db *sql.DB, table, col string) bool {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ, notnull string
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			continue
		}
		if name == col {
			return true
		}
	}
	return false
}

// MigrateWalletEncryption detects whether wallet_sessions still uses the old schema
// (plain wallet_address as PRIMARY KEY) and if so, migrates to the new schema:
// wallet_hash as PK + AES-256-GCM encrypted wallet_address_enc.
//
// Safe to call on already-migrated databases (no-op).
// Must be called after Open() and after the encryption key is available.
func MigrateWalletEncryption(db *sql.DB, encKey []byte) error {
	// Check whether wallet_sessions still has the old plain-text wallet_address column.
	rows, err := db.Query(`PRAGMA table_info(wallet_sessions)`)
	if err != nil {
		return fmt.Errorf("wallet migration: pragma table_info: %w", err)
	}
	hasOldAddressCol := false
	hasEncCol := false
	for rows.Next() {
		var cid int
		var name, typ, notnull string
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			continue
		}
		if name == "wallet_address" {
			hasOldAddressCol = true
		}
		if name == "wallet_address_enc" {
			hasEncCol = true
		}
	}
	rows.Close()

	if !hasOldAddressCol || hasEncCol {
		// Already migrated or new DB — nothing to do.
		return nil
	}

	log.Println("db: migrating wallet_sessions to encrypted schema...")

	// Read all existing rows before any DDL.
	type oldRow struct {
		address     string
		walletHash  string
		role        string
		status      string
		minRequired float64
		balanceUSD  float64
		lastChecked sql.NullInt64
		lowSince    sql.NullInt64
		verified    bool
		firstSeen   int64
		createdAt   int64
	}
	r, err := db.Query(`SELECT wallet_address, COALESCE(wallet_hash,''), role, balance_status, min_required_usd,
		COALESCE(balance_usd,0), last_checked_at, low_since, verified, first_seen, created_at
		FROM wallet_sessions`)
	if err != nil {
		return fmt.Errorf("wallet migration: read old rows: %w", err)
	}
	var oldRows []oldRow
	for r.Next() {
		var row oldRow
		if err := r.Scan(&row.address, &row.walletHash, &row.role, &row.status,
			&row.minRequired, &row.balanceUSD, &row.lastChecked, &row.lowSince,
			&row.verified, &row.firstSeen, &row.createdAt); err != nil {
			continue
		}
		oldRows = append(oldRows, row)
	}
	r.Close()

	// Encrypt all addresses before touching the schema.
	type newRow struct {
		oldRow
		enc      string
		currency string
	}
	var newRows []newRow
	for _, row := range oldRows {
		enc, err := ncrypto.EncryptAddress(encKey, row.address)
		if err != nil {
			return fmt.Errorf("wallet migration: encrypt %s: %w", row.address[:min(8, len(row.address))], err)
		}
		cur := "BTC"
		if !ncrypto.IsLikelyBTC(row.address) {
			cur = "LTC"
		}
		newRows = append(newRows, newRow{oldRow: row, enc: enc, currency: cur})
	}

	// Execute table rebuild inside a single transaction.
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("wallet migration: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err = tx.Exec(`DROP TABLE IF EXISTS wallet_sessions_new`); err != nil {
		return fmt.Errorf("wallet migration: drop new: %w", err)
	}
	if _, err = tx.Exec(`
		CREATE TABLE wallet_sessions_new (
			wallet_hash        TEXT PRIMARY KEY,
			wallet_address_enc TEXT NOT NULL,
			currency           TEXT NOT NULL DEFAULT 'BTC',
			role               TEXT NOT NULL,
			balance_status     TEXT DEFAULT 'ok',
			min_required_usd   REAL NOT NULL,
			balance_usd        REAL DEFAULT 0,
			last_checked_at    INTEGER,
			low_since          INTEGER,
			verified           BOOLEAN DEFAULT FALSE,
			first_seen         INTEGER NOT NULL,
			created_at         INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("wallet migration: create new table: %w", err)
	}

	for _, row := range newRows {
		if _, err = tx.Exec(`
			INSERT INTO wallet_sessions_new
			(wallet_hash, wallet_address_enc, currency, role, balance_status, min_required_usd, balance_usd,
			 last_checked_at, low_since, verified, first_seen, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, row.walletHash, row.enc, row.currency, row.role, row.status,
			row.minRequired, row.balanceUSD, row.lastChecked, row.lowSince,
			row.verified, row.firstSeen, row.createdAt); err != nil {
			return fmt.Errorf("wallet migration: insert row: %w", err)
		}
	}

	if _, err = tx.Exec(`DROP TABLE wallet_sessions`); err != nil {
		return fmt.Errorf("wallet migration: drop old: %w", err)
	}
	if _, err = tx.Exec(`ALTER TABLE wallet_sessions_new RENAME TO wallet_sessions`); err != nil {
		return fmt.Errorf("wallet migration: rename: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("wallet migration: commit: %w", err)
	}

	// Recreate indexes (dropped with old table).
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_wallet_sessions_hash ON wallet_sessions(wallet_hash)`)

	log.Printf("db: wallet_sessions migrated: %d rows encrypted", len(newRows))
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
