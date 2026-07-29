package v2

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"

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

// columnExists reports whether a column named col exists in table tbl.
// Uses PRAGMA table_info, which is always reliable in SQLite.
func columnExists(db *sql.DB, tbl, col string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + tbl + `)`)
	if err != nil {
		return false, fmt.Errorf("v2: columnExists: PRAGMA table_info(%s): %w", tbl, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return false, fmt.Errorf("v2: columnExists: scan: %w", err)
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// connColumnExists is like columnExists but operates on a specific connection
// (required inside pinned-connection blocks where PRAGMA and TX share one conn).
func connColumnExists(ctx context.Context, conn *sql.Conn, tbl, col string) (bool, error) {
	rows, err := conn.QueryContext(ctx, `PRAGMA table_info(`+tbl+`)`)
	if err != nil {
		return false, fmt.Errorf("v2: connColumnExists: PRAGMA table_info(%s): %w", tbl, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return false, fmt.Errorf("v2: connColumnExists: scan: %w", err)
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// tableExists reports whether a table named tbl exists in the database.
func tableExists(db *sql.DB, tbl string) (bool, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("v2: tableExists: %w", err)
	}
	return count > 0, nil
}

// connTableExists is like tableExists but operates on a specific connection.
func connTableExists(ctx context.Context, conn *sql.Conn, tbl string) (bool, error) {
	var count int
	err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("v2: connTableExists(%s): %w", tbl, err)
	}
	return count > 0, nil
}

// addColumnIfMissing adds a column to a table using PRAGMA introspection.
// Returns nil if the column already exists.
func addColumnIfMissing(db *sql.DB, tbl, col, definition string) error {
	exists, err := columnExists(db, tbl, col)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, execErr := db.Exec(`ALTER TABLE ` + tbl + ` ADD COLUMN ` + col + ` ` + definition)
	if execErr != nil {
		return fmt.Errorf("v2: addColumnIfMissing: ALTER %s.%s: %w", tbl, col, execErr)
	}
	return nil
}

// connOutboxInspect checks v2_informer_outbox for dead columns (notified_refs, claimed_at)
// and reports which optional columns exist (claimed_by, claim_token, lease_until).
//
//   - needsRebuild is true if notified_refs or claimed_at are present, OR if the table
//     exists but any of claimed_by/claim_token/lease_until are missing.
//   - hasClaimedBy, hasClaimToken, hasLeaseUntil report column presence for the copy SQL.
func connOutboxInspect(ctx context.Context, conn *sql.Conn) (needsRebuild, hasClaimedBy, hasClaimToken, hasLeaseUntil bool, err error) {
	exists, err := connTableExists(ctx, conn, "v2_informer_outbox")
	if err != nil {
		return false, false, false, false, fmt.Errorf("connOutboxInspect: table check: %w", err)
	}
	if !exists {
		// ApplySchema will create it fresh; no rebuild needed.
		return false, false, false, false, nil
	}

	hasNotifiedRefs, err := connColumnExists(ctx, conn, "v2_informer_outbox", "notified_refs")
	if err != nil {
		return false, false, false, false, fmt.Errorf("connOutboxInspect: notified_refs: %w", err)
	}
	if hasNotifiedRefs {
		// Dead column present — we still need to know which optional cols exist for copy SQL.
		hasClaimedBy, err = connColumnExists(ctx, conn, "v2_informer_outbox", "claimed_by")
		if err != nil {
			return false, false, false, false, fmt.Errorf("connOutboxInspect: claimed_by: %w", err)
		}
		hasClaimToken, err = connColumnExists(ctx, conn, "v2_informer_outbox", "claim_token")
		if err != nil {
			return false, false, false, false, fmt.Errorf("connOutboxInspect: claim_token: %w", err)
		}
		hasLeaseUntil, err = connColumnExists(ctx, conn, "v2_informer_outbox", "lease_until")
		if err != nil {
			return false, false, false, false, fmt.Errorf("connOutboxInspect: lease_until: %w", err)
		}
		return true, hasClaimedBy, hasClaimToken, hasLeaseUntil, nil
	}

	hasClaimedAt, err := connColumnExists(ctx, conn, "v2_informer_outbox", "claimed_at")
	if err != nil {
		return false, false, false, false, fmt.Errorf("connOutboxInspect: claimed_at: %w", err)
	}
	if hasClaimedAt {
		hasClaimedBy, err = connColumnExists(ctx, conn, "v2_informer_outbox", "claimed_by")
		if err != nil {
			return false, false, false, false, fmt.Errorf("connOutboxInspect: claimed_by: %w", err)
		}
		hasClaimToken, err = connColumnExists(ctx, conn, "v2_informer_outbox", "claim_token")
		if err != nil {
			return false, false, false, false, fmt.Errorf("connOutboxInspect: claim_token: %w", err)
		}
		hasLeaseUntil, err = connColumnExists(ctx, conn, "v2_informer_outbox", "lease_until")
		if err != nil {
			return false, false, false, false, fmt.Errorf("connOutboxInspect: lease_until: %w", err)
		}
		return true, hasClaimedBy, hasClaimToken, hasLeaseUntil, nil
	}

	// No dead columns. Check for missing optional columns.
	hasClaimedBy, err = connColumnExists(ctx, conn, "v2_informer_outbox", "claimed_by")
	if err != nil {
		return false, false, false, false, fmt.Errorf("connOutboxInspect: claimed_by: %w", err)
	}
	hasClaimToken, err = connColumnExists(ctx, conn, "v2_informer_outbox", "claim_token")
	if err != nil {
		return false, false, false, false, fmt.Errorf("connOutboxInspect: claim_token: %w", err)
	}
	hasLeaseUntil, err = connColumnExists(ctx, conn, "v2_informer_outbox", "lease_until")
	if err != nil {
		return false, false, false, false, fmt.Errorf("connOutboxInspect: lease_until: %w", err)
	}

	// If any required optional column is missing, we need a rebuild.
	if !hasClaimedBy || !hasClaimToken || !hasLeaseUntil {
		return true, hasClaimedBy, hasClaimToken, hasLeaseUntil, nil
	}

	return false, hasClaimedBy, hasClaimToken, hasLeaseUntil, nil
}

// connListingsDisplayNameIsUnique checks whether the v2_listings table's CREATE SQL
// contains an inline UNIQUE constraint on display_name (old schema).
func connListingsDisplayNameIsUnique(ctx context.Context, conn *sql.Conn) (bool, error) {
	var createSQL string
	err := conn.QueryRowContext(ctx,
		`SELECT COALESCE(sql, '') FROM sqlite_master WHERE type='table' AND name='v2_listings'`,
	).Scan(&createSQL)
	if err != nil {
		return false, fmt.Errorf("connListingsDisplayNameIsUnique: %w", err)
	}
	// Old schema: "display_name           TEXT NOT NULL UNIQUE,"
	// New schema: "display_name           TEXT NOT NULL," (no UNIQUE)
	return strings.Contains(createSQL, "display_name") &&
		strings.Contains(createSQL, "NOT NULL UNIQUE"), nil
}

// connHelperPurchasesNeedsRebuild checks if v2_helper_purchases needs to be
// rebuilt to update the CHECK constraints to use required_post_payment_floor_usd.
func connHelperPurchasesNeedsRebuild(ctx context.Context, conn *sql.Conn) (bool, error) {
	var createSQL string
	err := conn.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='v2_helper_purchases'`,
	).Scan(&createSQL)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("connHelperPurchasesNeedsRebuild: %w", err)
	}
	return !strings.Contains(createSQL, "required_post_payment_floor_usd"), nil
}

// connRecipientsMigrateNeeded reads sqlite_master to check if the recipients table
// has the old CHECK constraint (missing retry_exhausted). Returns (needsRebuild, err).
func connRecipientsMigrateNeeded(ctx context.Context, conn *sql.Conn) (bool, error) {
	var createSQL string
	err := conn.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='v2_informer_outbox_recipients'`,
	).Scan(&createSQL)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // table doesn't exist; handled elsewhere
	}
	if err != nil {
		return false, fmt.Errorf("connRecipientsMigrateNeeded: %w", err)
	}
	// If the CREATE TABLE sql mentions 'retry_exhausted', the CHECK is current.
	return !strings.Contains(createSQL, "retry_exhausted"), nil
}

// connHasLegacyHelperNames returns true if v2_helper_profiles has any rows
// with a public_name that does not contain the "·" alias separator.
// Legacy names have the form "adj_noun_<32hex>" (no middle dot).
func connHasLegacyHelperNames(ctx context.Context, conn *sql.Conn) (bool, error) {
	var count int
	err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM v2_helper_profiles WHERE public_name NOT LIKE '%·%'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("connHasLegacyHelperNames: %w", err)
	}
	return count > 0, nil
}

// MigrateSchema applies incremental V2 schema migrations on top of ApplySchema.
// Each step is idempotent: column existence is checked via PRAGMA table_info
// before issuing ALTER TABLE. No error-string matching.
//
// Upgrade path:
//   - DBs from checkpoint a0d6d99 have the original v2_informer_outbox without
//     claim_token/lease_until and no recipients table at all.
//   - DBs already at 09A-REPAIR have claimed_by/claimed_at but not claim_token/lease_until.
//   - All paths end with the same schema as a fresh DB from schema.sql.
//
// Uses a single pinned *sql.Conn for ALL operations so that PRAGMA foreign_keys
// and the transaction share exactly the same SQLite connection — required because
// SQLite's PRAGMA foreign_keys cannot be changed inside a transaction.
func MigrateSchema(db *sql.DB) error {
	ctx := context.Background()

	// Pin a single connection for all operations.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("v2: MigrateSchema: get conn: %w", err)
	}
	defer conn.Close() //nolint:errcheck

	// ── Introspect (outside any TX) ──────────────────────────────────────────

	outboxExists, err := connTableExists(ctx, conn, "v2_informer_outbox")
	if err != nil {
		return fmt.Errorf("v2: MigrateSchema: outbox table check: %w", err)
	}

	var outboxNeedsRebuild, hasClaimedBy, hasClaimToken, hasLeaseUntil bool
	if outboxExists {
		outboxNeedsRebuild, hasClaimedBy, hasClaimToken, hasLeaseUntil, err = connOutboxInspect(ctx, conn)
		if err != nil {
			return fmt.Errorf("v2: MigrateSchema: outbox inspect: %w", err)
		}
	}

	recipientsExists, err := connTableExists(ctx, conn, "v2_informer_outbox_recipients")
	if err != nil {
		return fmt.Errorf("v2: MigrateSchema: recipients table check: %w", err)
	}

	var recipientsNeedsRebuild bool
	var attemptsExists bool
	if recipientsExists {
		recipientsNeedsRebuild, err = connRecipientsMigrateNeeded(ctx, conn)
		if err != nil {
			return fmt.Errorf("v2: MigrateSchema: recipients migrate check: %w", err)
		}
		attemptsExists, err = connColumnExists(ctx, conn, "v2_informer_outbox_recipients", "attempts")
		if err != nil {
			return fmt.Errorf("v2: MigrateSchema: attempts column check: %w", err)
		}
	}

	// Task 10C: check new snapshot columns.
	clientFlowsHardFloor, err := connColumnExists(ctx, conn, "v2_client_flows", "required_hard_floor_usd")
	if err != nil {
		return fmt.Errorf("v2: MigrateSchema: client_flows hard_floor check: %w", err)
	}

	helperPurchasesNeedsRebuild, err := connHelperPurchasesNeedsRebuild(ctx, conn)
	if err != nil {
		return fmt.Errorf("v2: MigrateSchema: helper_purchases rebuild check: %w", err)
	}

	// Task 10D: check client profiles public_name and listings display_name UNIQUE.
	clientProfilesPublicName, err := connColumnExists(ctx, conn, "v2_client_profiles", "public_name")
	if err != nil {
		return fmt.Errorf("v2: MigrateSchema: client_profiles public_name check: %w", err)
	}

	listingsDisplayNameHasUnique, err := connListingsDisplayNameIsUnique(ctx, conn)
	if err != nil {
		return fmt.Errorf("v2: MigrateSchema: listings display_name unique check: %w", err)
	}

	// Task 10D fix: check whether any helper profiles still have legacy names.
	// Legacy format has no "·" separator; new alias format always contains "·".
	helperProfilesTableExists, err := connTableExists(ctx, conn, "v2_helper_profiles")
	if err != nil {
		return fmt.Errorf("v2: MigrateSchema: helper_profiles table check: %w", err)
	}
	hasLegacyHelperNames := false
	if helperProfilesTableExists {
		hasLegacyHelperNames, err = connHasLegacyHelperNames(ctx, conn)
		if err != nil {
			return fmt.Errorf("v2: MigrateSchema: legacy helper names check: %w", err)
		}
	}

	// Task 11C: payment observability columns on v2_helper_purchases.
	helperPurchasesHasLastCheckAttempt, err := connColumnExists(ctx, conn, "v2_helper_purchases", "last_check_attempt_at")
	if err != nil {
		return fmt.Errorf("v2: MigrateSchema: last_check_attempt_at check: %w", err)
	}
	helperPurchasesHasLastSuccessfulCheck, err := connColumnExists(ctx, conn, "v2_helper_purchases", "last_successful_chain_check_at")
	if err != nil {
		return fmt.Errorf("v2: MigrateSchema: last_successful_chain_check_at check: %w", err)
	}

	// Task 11G handoff: existing production DBs predate the alternate browser
	// token column even though fresh DBs receive it from schema.sql.
	helperPurchasesHasAltBrowserToken, err := connColumnExists(ctx, conn, "v2_helper_purchases", "alt_browser_token_hash")
	if err != nil {
		return fmt.Errorf("v2: MigrateSchema: alt_browser_token_hash check: %w", err)
	}

	// Task 11G: available_at column on v2_review_entitlements.
	reviewEntitlementsHasAvailableAt, err := connColumnExists(ctx, conn, "v2_review_entitlements", "available_at")
	if err != nil {
		return fmt.Errorf("v2: MigrateSchema: available_at check: %w", err)
	}

	// ── Early return: nothing to do ──────────────────────────────────────────
	//
	// Condition: outbox exists and is clean, all optional cols present,
	// recipients exists and is current, attempts column present,
	// Task 10C snapshot columns are present, Task 10D columns are correct,
	// no legacy helper profile names remain, Task 11C observability columns present,
	// and Task 11G available_at column present.
	if outboxExists && !outboxNeedsRebuild && hasClaimedBy && hasClaimToken && hasLeaseUntil &&
		recipientsExists && !recipientsNeedsRebuild && attemptsExists &&
		clientFlowsHardFloor && !helperPurchasesNeedsRebuild &&
		clientProfilesPublicName && !listingsDisplayNameHasUnique &&
		!hasLegacyHelperNames &&
		helperPurchasesHasLastCheckAttempt && helperPurchasesHasLastSuccessfulCheck &&
		helperPurchasesHasAltBrowserToken &&
		reviewEntitlementsHasAvailableAt {
		return nil
	}

	// ── Set PRAGMA foreign_keys = OFF (outside TX — SQLite requirement) ──────
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("v2: MigrateSchema: disable FK: %w", err)
	}
	defer conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`) //nolint:errcheck

	// ── Begin TX on the same conn ────────────────────────────────────────────
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("v2: MigrateSchema: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// ── v2_informer_outbox ───────────────────────────────────────────────────

	if outboxExists && outboxNeedsRebuild {
		if err := rebuildOutboxInTx(ctx, tx, hasClaimedBy, hasClaimToken, hasLeaseUntil); err != nil {
			return fmt.Errorf("v2: MigrateSchema: rebuild outbox: %w", err)
		}
	} else if outboxExists {
		// Table has the right structure; add any missing columns incrementally inside TX.
		if !hasClaimedBy {
			if _, err := tx.ExecContext(ctx,
				`ALTER TABLE v2_informer_outbox ADD COLUMN claimed_by TEXT NOT NULL DEFAULT ''`,
			); err != nil {
				return fmt.Errorf("v2: MigrateSchema: add claimed_by: %w", err)
			}
		}
		if !hasClaimToken {
			if _, err := tx.ExecContext(ctx,
				`ALTER TABLE v2_informer_outbox ADD COLUMN claim_token TEXT NOT NULL DEFAULT ''`,
			); err != nil {
				return fmt.Errorf("v2: MigrateSchema: add claim_token: %w", err)
			}
		}
		if !hasLeaseUntil {
			if _, err := tx.ExecContext(ctx,
				`ALTER TABLE v2_informer_outbox ADD COLUMN lease_until INTEGER NOT NULL DEFAULT 0`,
			); err != nil {
				return fmt.Errorf("v2: MigrateSchema: add lease_until: %w", err)
			}
		}
	}

	// ── v2_informer_outbox_recipients ────────────────────────────────────────

	if !recipientsExists {
		if _, err := tx.ExecContext(ctx, `
CREATE TABLE v2_informer_outbox_recipients (
    outbox_id  TEXT NOT NULL REFERENCES v2_informer_outbox(id) ON DELETE CASCADE,
    sub_ref    TEXT NOT NULL,
    state      TEXT NOT NULL DEFAULT 'pending'
                   CHECK (state IN ('pending', 'delivered', 'permanent_failed',
                                    'retry_exhausted', 'decrypt_failed')),
    attempts   INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (outbox_id, sub_ref)
)`); err != nil {
			return fmt.Errorf("v2: MigrateSchema: create outbox_recipients: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_v2_outbox_recipients_pending
    ON v2_informer_outbox_recipients(outbox_id) WHERE state = 'pending'`); err != nil {
			return fmt.Errorf("v2: MigrateSchema: create recipients index: %w", err)
		}
	} else {
		if !attemptsExists {
			if _, err := tx.ExecContext(ctx,
				`ALTER TABLE v2_informer_outbox_recipients ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0`,
			); err != nil {
				return fmt.Errorf("v2: MigrateSchema: add attempts: %w", err)
			}
		}
		if recipientsNeedsRebuild {
			if err := rebuildRecipientsInTx(ctx, tx); err != nil {
				return fmt.Errorf("v2: MigrateSchema: rebuild recipients: %w", err)
			}
		}
	}

	// ── Task 10C: v2_client_flows.required_hard_floor_usd ────────────────────
	if !clientFlowsHardFloor {
		if _, err := tx.ExecContext(ctx,
			`ALTER TABLE v2_client_flows ADD COLUMN required_hard_floor_usd REAL NOT NULL DEFAULT 120.0`,
		); err != nil {
			return fmt.Errorf("v2: MigrateSchema: add required_hard_floor_usd: %w", err)
		}
	}

	// ── Task 10C: v2_helper_purchases rebuild with required_post_payment_floor_usd ──
	if helperPurchasesNeedsRebuild {
		if err := rebuildHelperPurchasesInTx(ctx, tx); err != nil {
			return fmt.Errorf("v2: MigrateSchema: rebuild helper_purchases: %w", err)
		}
	}

	// ── Task 10D: v2_client_profiles.public_name ─────────────────────────────
	if !clientProfilesPublicName {
		if _, err := tx.ExecContext(ctx,
			`ALTER TABLE v2_client_profiles ADD COLUMN public_name TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return fmt.Errorf("v2: MigrateSchema: add client profiles public_name: %w", err)
		}
	}
	// Create partial unique index (WHERE public_name != '') to allow empty during migration.
	if _, err := tx.ExecContext(ctx, `
		CREATE UNIQUE INDEX IF NOT EXISTS uniq_v2_client_profiles_public_name
		ON v2_client_profiles(public_name) WHERE public_name != ''`); err != nil {
		return fmt.Errorf("v2: MigrateSchema: create client profiles alias index: %w", err)
	}
	// Populate empty public_names for existing profiles.
	rows10d, err := tx.QueryContext(ctx, `SELECT id FROM v2_client_profiles WHERE public_name = ''`)
	if err != nil {
		return fmt.Errorf("v2: MigrateSchema: list empty alias profiles: %w", err)
	}
	var emptyProfileIDs []string
	for rows10d.Next() {
		var id string
		if err := rows10d.Scan(&id); err != nil {
			rows10d.Close()
			return fmt.Errorf("v2: MigrateSchema: scan profile id: %w", err)
		}
		emptyProfileIDs = append(emptyProfileIDs, id)
	}
	rows10d.Close()
	if err := rows10d.Err(); err != nil {
		return fmt.Errorf("v2: MigrateSchema: list alias profiles iter: %w", err)
	}
	gen10d := NewRandomAliasGenerator()
	for _, profileID := range emptyProfileIDs {
		alias, genErr := gen10d.GenerateAlias()
		if genErr != nil {
			return fmt.Errorf("v2: MigrateSchema: generate alias: %w", genErr)
		}
		for attempt := 0; attempt < 10; attempt++ {
			_, upErr := tx.ExecContext(ctx,
				`UPDATE v2_client_profiles SET public_name = ?, updated_at = strftime('%s', 'now') WHERE id = ?`,
				alias, profileID,
			)
			if upErr == nil {
				break
			}
			if isSQLiteUniqueOnColumn(upErr, "uniq_v2_client_profiles_public_name") {
				// Collision: regenerate.
				alias, genErr = gen10d.GenerateAlias()
				if genErr != nil {
					return fmt.Errorf("v2: MigrateSchema: regenerate alias: %w", genErr)
				}
				continue
			}
			return fmt.Errorf("v2: MigrateSchema: update profile alias: %w", upErr)
		}
	}

	// ── Task 10D: v2_listings.display_name remove UNIQUE + update from profile ─
	if listingsDisplayNameHasUnique {
		if err := rebuildListingsInTx(ctx, tx); err != nil {
			return fmt.Errorf("v2: MigrateSchema: rebuild listings: %w", err)
		}
		// Update listing display_names to use their profile's public_name.
		if _, err := tx.ExecContext(ctx, `
			UPDATE v2_listings SET display_name = (
				SELECT cp.public_name
				FROM v2_client_flows f
				JOIN v2_client_profiles cp ON cp.id = f.client_profile_id
				WHERE f.id = v2_listings.flow_id
			), updated_at = strftime('%s', 'now')
			WHERE EXISTS (
				SELECT 1
				FROM v2_client_flows f
				JOIN v2_client_profiles cp ON cp.id = f.client_profile_id
				WHERE f.id = v2_listings.flow_id AND cp.public_name != ''
			)`); err != nil {
			return fmt.Errorf("v2: MigrateSchema: update listings display_name: %w", err)
		}
	}

	// ── Task 10D fix: migrate legacy helper profile names to alias format ────
	// Legacy format: "adj_noun_<32hex>" (contains underscore, no "·" character).
	// New format: "Adj Noun · XXXX"
	// Only migrate rows where public_name does not contain the middle dot separator.
	legacyHelperRows, err := tx.QueryContext(ctx,
		`SELECT id FROM v2_helper_profiles WHERE public_name NOT LIKE '%·%'`)
	if err != nil {
		return fmt.Errorf("v2: MigrateSchema: list legacy helper profiles: %w", err)
	}
	var legacyHelperIDs []string
	for legacyHelperRows.Next() {
		var hid string
		if err := legacyHelperRows.Scan(&hid); err != nil {
			legacyHelperRows.Close()
			return fmt.Errorf("v2: MigrateSchema: scan legacy helper profile id: %w", err)
		}
		legacyHelperIDs = append(legacyHelperIDs, hid)
	}
	legacyHelperRows.Close()
	if err := legacyHelperRows.Err(); err != nil {
		return fmt.Errorf("v2: MigrateSchema: legacy helper profiles iter: %w", err)
	}
	genHelper := NewRandomAliasGenerator()
	for _, hid := range legacyHelperIDs {
		var newAlias string
		var genErr error
		migrated := false
		for attempt := 0; attempt < 10; attempt++ {
			newAlias, genErr = genHelper.GenerateAlias()
			if genErr != nil {
				return fmt.Errorf("v2: MigrateSchema: generate helper alias: %w", genErr)
			}
			_, upErr := tx.ExecContext(ctx,
				`UPDATE v2_helper_profiles SET public_name = ?, updated_at = strftime('%s', 'now') WHERE id = ?`,
				newAlias, hid,
			)
			if upErr == nil {
				migrated = true
				break
			}
			if isSQLiteUniqueOnColumn(upErr, "v2_helper_profiles.public_name") {
				// Collision: regenerate alias and retry.
				continue
			}
			return fmt.Errorf("v2: MigrateSchema: update helper profile alias: %w", upErr)
		}
		if !migrated {
			return fmt.Errorf("v2: MigrateSchema: exceeded 10 alias retries for helper profile %s", hid)
		}
	}

	// ── Task 11C: payment observability columns on v2_helper_purchases ───────
	if !helperPurchasesHasLastCheckAttempt {
		if _, err := tx.ExecContext(ctx,
			`ALTER TABLE v2_helper_purchases ADD COLUMN last_check_attempt_at INTEGER`,
		); err != nil {
			return fmt.Errorf("v2: MigrateSchema: add last_check_attempt_at: %w", err)
		}
	}
	if !helperPurchasesHasLastSuccessfulCheck {
		if _, err := tx.ExecContext(ctx,
			`ALTER TABLE v2_helper_purchases ADD COLUMN last_successful_chain_check_at INTEGER`,
		); err != nil {
			return fmt.Errorf("v2: MigrateSchema: add last_successful_chain_check_at: %w", err)
		}
	}
	if !helperPurchasesHasAltBrowserToken {
		if _, err := tx.ExecContext(ctx,
			`ALTER TABLE v2_helper_purchases ADD COLUMN alt_browser_token_hash TEXT`,
		); err != nil {
			return fmt.Errorf("v2: MigrateSchema: add alt_browser_token_hash: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE UNIQUE INDEX IF NOT EXISTS uniq_v2_helper_purchases_alt_browser_token
		ON v2_helper_purchases(alt_browser_token_hash)
		WHERE alt_browser_token_hash IS NOT NULL`); err != nil {
		return fmt.Errorf("v2: MigrateSchema: create alt_browser_token_hash index: %w", err)
	}

	// ── Task 11G: available_at column on v2_review_entitlements ──────────────
	if !reviewEntitlementsHasAvailableAt {
		if _, err := tx.ExecContext(ctx,
			`ALTER TABLE v2_review_entitlements ADD COLUMN available_at INTEGER NOT NULL DEFAULT 0`,
		); err != nil {
			return fmt.Errorf("v2: MigrateSchema: add available_at: %w", err)
		}
		// Migrate existing rows: set available_at = created_at + 3600 WHERE available_at = 0.
		// This is idempotent: rows already populated will have available_at != 0 and are skipped.
		if _, err := tx.ExecContext(ctx,
			`UPDATE v2_review_entitlements SET available_at = created_at + 3600 WHERE available_at = 0`,
		); err != nil {
			return fmt.Errorf("v2: MigrateSchema: backfill available_at: %w", err)
		}
	}

	// ── Commit ───────────────────────────────────────────────────────────────
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("v2: MigrateSchema: commit: %w", err)
	}

	// ── Post-commit foreign key check (on same conn, outside TX) ─────────────
	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("v2: MigrateSchema: foreign_key_check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("v2: MigrateSchema: foreign_key_check: integrity violation detected after migration")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("v2: MigrateSchema: foreign_key_check scan: %w", err)
	}

	return nil
}

// rebuildOutboxInTx rebuilds v2_informer_outbox within an already-open transaction,
// preserving data from all columns that exist in both old and new schemas.
//
// hasClaimedBy/hasClaimToken/hasLeaseUntil must be determined before opening the TX
// (via connOutboxInspect) because SQLite rejects references to absent columns even
// inside COALESCE. Literal defaults (”/0) are substituted for absent columns.
func rebuildOutboxInTx(ctx context.Context, tx *sql.Tx, hasClaimedBy, hasClaimToken, hasLeaseUntil bool) error {
	claimedByExpr := "''"
	if hasClaimedBy {
		claimedByExpr = "claimed_by"
	}
	claimTokenExpr := "''"
	if hasClaimToken {
		claimTokenExpr = "claim_token"
	}
	leaseUntilExpr := "0"
	if hasLeaseUntil {
		leaseUntilExpr = "lease_until"
	}

	if _, err := tx.ExecContext(ctx, `
CREATE TABLE v2_informer_outbox_new (
    id           TEXT PRIMARY KEY,
    listing_id   TEXT NOT NULL,
    city         TEXT NOT NULL,
    display_name TEXT NOT NULL,
    help_type    TEXT NOT NULL,
    dep_type     TEXT NOT NULL,
    urgency      TEXT NOT NULL,
    listing_url  TEXT NOT NULL,
    event_key    TEXT NOT NULL UNIQUE,
    state        TEXT NOT NULL DEFAULT 'pending'
                     CHECK (state IN ('pending', 'done', 'failed')),
    attempt      INTEGER NOT NULL DEFAULT 0,
    claimed_by   TEXT NOT NULL DEFAULT '',
    claim_token  TEXT NOT NULL DEFAULT '',
    lease_until  INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
)`); err != nil {
		return fmt.Errorf("rebuildOutboxInTx: create new: %w", err)
	}

	copySQL := fmt.Sprintf(`
INSERT INTO v2_informer_outbox_new
    (id, listing_id, city, display_name, help_type, dep_type, urgency,
     listing_url, event_key, state, attempt, claimed_by, claim_token,
     lease_until, last_error, created_at, updated_at)
SELECT
    id, listing_id, city, display_name, help_type, dep_type, urgency,
    listing_url, event_key, state, attempt,
    %s,
    %s,
    %s,
    last_error, created_at, updated_at
FROM v2_informer_outbox`, claimedByExpr, claimTokenExpr, leaseUntilExpr)

	if _, err := tx.ExecContext(ctx, copySQL); err != nil {
		return fmt.Errorf("rebuildOutboxInTx: copy data: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DROP TABLE v2_informer_outbox`); err != nil {
		return fmt.Errorf("rebuildOutboxInTx: drop old: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE v2_informer_outbox_new RENAME TO v2_informer_outbox`); err != nil {
		return fmt.Errorf("rebuildOutboxInTx: rename: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_v2_informer_outbox_state ON v2_informer_outbox(state);
CREATE INDEX IF NOT EXISTS idx_v2_informer_outbox_claim ON v2_informer_outbox(state, lease_until);
`); err != nil {
		return fmt.Errorf("rebuildOutboxInTx: indexes: %w", err)
	}

	return nil
}

// rebuildRecipientsInTx rebuilds v2_informer_outbox_recipients with the correct
// CHECK constraint (including retry_exhausted and decrypt_failed) within an
// already-open transaction. The caller is responsible for committing.
func rebuildRecipientsInTx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE v2_informer_outbox_recipients_new (
    outbox_id  TEXT NOT NULL REFERENCES v2_informer_outbox(id) ON DELETE CASCADE,
    sub_ref    TEXT NOT NULL,
    state      TEXT NOT NULL DEFAULT 'pending'
                   CHECK (state IN ('pending', 'delivered', 'permanent_failed',
                                    'retry_exhausted', 'decrypt_failed')),
    attempts   INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (outbox_id, sub_ref)
)`); err != nil {
		return fmt.Errorf("rebuildRecipientsInTx: create new: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO v2_informer_outbox_recipients_new
    (outbox_id, sub_ref, state, attempts, updated_at)
SELECT outbox_id, sub_ref, state, COALESCE(attempts, 0), updated_at
FROM v2_informer_outbox_recipients`); err != nil {
		return fmt.Errorf("rebuildRecipientsInTx: copy data: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DROP TABLE v2_informer_outbox_recipients`); err != nil {
		return fmt.Errorf("rebuildRecipientsInTx: drop old: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE v2_informer_outbox_recipients_new RENAME TO v2_informer_outbox_recipients`); err != nil {
		return fmt.Errorf("rebuildRecipientsInTx: rename: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_v2_outbox_recipients_pending
    ON v2_informer_outbox_recipients(outbox_id) WHERE state='pending'`); err != nil {
		return fmt.Errorf("rebuildRecipientsInTx: index: %w", err)
	}

	return nil
}

// rebuildHelperPurchasesInTx rebuilds v2_helper_purchases with required_post_payment_floor_usd
// and updated CHECK constraints within an already-open transaction.
func rebuildHelperPurchasesInTx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE v2_helper_purchases_new (
    id                        TEXT PRIMARY KEY,
    listing_id                TEXT NOT NULL REFERENCES v2_listings(id),
    helper_profile_id         TEXT NOT NULL REFERENCES v2_helper_profiles(id),
    browser_token_hash        TEXT NOT NULL UNIQUE,
    state                     TEXT NOT NULL DEFAULT 'awaiting_payment'
                                   CHECK (state IN (
                                       'awaiting_payment', 'payment_detected',
                                       'payment_confirmed', 'paid_low_balance',
                                       'contact_ready', 'failed',
                                       'invoice_expired', 'receipt_expired'
                                   )),
    country_code_snapshot     TEXT NOT NULL,
    contact_ready_at          INTEGER,
    first_revealed_at         INTEGER,
    receipt_expires_at        INTEGER,
    balance_retry_deadline_at INTEGER,
    last_balance_usd          REAL CHECK (last_balance_usd IS NULL
                                          OR (last_balance_usd >= 0 AND last_balance_usd < 1e15)),
    last_balance_checked_at   INTEGER,
    required_post_payment_floor_usd REAL NOT NULL DEFAULT 1000.0,
    created_at                INTEGER NOT NULL,
    updated_at                INTEGER NOT NULL,

    CHECK (length(id) = 64 AND NOT (id GLOB '*[^0-9a-f]*')),
    CHECK (length(browser_token_hash) = 64 AND NOT (browser_token_hash GLOB '*[^0-9a-f]*')),
    CHECK (length(country_code_snapshot) = 2
           AND NOT (country_code_snapshot GLOB '*[^A-Z]*')),
    CHECK (first_revealed_at IS NULL OR receipt_expires_at = first_revealed_at + 86400),
    CHECK (first_revealed_at IS NULL OR contact_ready_at IS NULL
           OR first_revealed_at >= contact_ready_at),
    CHECK (
        (state = 'awaiting_payment'
            AND last_balance_usd IS NULL AND last_balance_checked_at IS NULL
            AND balance_retry_deadline_at IS NULL
            AND contact_ready_at IS NULL
            AND first_revealed_at IS NULL AND receipt_expires_at IS NULL)
        OR
        (state = 'payment_detected'
            AND last_balance_usd IS NULL AND last_balance_checked_at IS NULL
            AND balance_retry_deadline_at IS NULL
            AND contact_ready_at IS NULL
            AND first_revealed_at IS NULL AND receipt_expires_at IS NULL)
        OR
        (state = 'payment_confirmed'
            AND balance_retry_deadline_at IS NOT NULL
            AND last_balance_usd IS NULL AND last_balance_checked_at IS NULL
            AND contact_ready_at IS NULL
            AND first_revealed_at IS NULL AND receipt_expires_at IS NULL)
        OR
        (state = 'paid_low_balance'
            AND balance_retry_deadline_at IS NOT NULL
            AND last_balance_usd IS NOT NULL AND last_balance_usd < required_post_payment_floor_usd
            AND last_balance_checked_at IS NOT NULL
            AND contact_ready_at IS NULL
            AND first_revealed_at IS NULL AND receipt_expires_at IS NULL)
        OR
        (state = 'contact_ready'
            AND balance_retry_deadline_at IS NOT NULL
            AND last_balance_usd IS NOT NULL AND last_balance_usd >= required_post_payment_floor_usd
            AND last_balance_checked_at IS NOT NULL
            AND contact_ready_at IS NOT NULL
            AND (first_revealed_at IS NULL) = (receipt_expires_at IS NULL))
        OR
        (state = 'invoice_expired'
            AND last_balance_usd IS NULL AND last_balance_checked_at IS NULL
            AND balance_retry_deadline_at IS NULL
            AND contact_ready_at IS NULL
            AND first_revealed_at IS NULL AND receipt_expires_at IS NULL)
        OR
        (state = 'failed'
            AND contact_ready_at IS NULL
            AND first_revealed_at IS NULL AND receipt_expires_at IS NULL)
        OR
        (state = 'receipt_expired'
            AND contact_ready_at IS NOT NULL
            AND balance_retry_deadline_at IS NOT NULL
            AND last_balance_usd IS NOT NULL AND last_balance_usd >= required_post_payment_floor_usd
            AND last_balance_checked_at IS NOT NULL
            AND (first_revealed_at IS NULL) = (receipt_expires_at IS NULL))
    ),
    CHECK (updated_at >= created_at)
)`); err != nil {
		return fmt.Errorf("rebuildHelperPurchasesInTx: create new: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO v2_helper_purchases_new
    (id, listing_id, helper_profile_id, browser_token_hash, state,
     country_code_snapshot, contact_ready_at, first_revealed_at, receipt_expires_at,
     balance_retry_deadline_at, last_balance_usd, last_balance_checked_at,
     required_post_payment_floor_usd, created_at, updated_at)
SELECT
    id, listing_id, helper_profile_id, browser_token_hash, state,
    country_code_snapshot, contact_ready_at, first_revealed_at, receipt_expires_at,
    balance_retry_deadline_at, last_balance_usd, last_balance_checked_at,
    1000.0,
    created_at, updated_at
FROM v2_helper_purchases`); err != nil {
		return fmt.Errorf("rebuildHelperPurchasesInTx: copy data: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DROP TABLE v2_helper_purchases`); err != nil {
		return fmt.Errorf("rebuildHelperPurchasesInTx: drop old: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE v2_helper_purchases_new RENAME TO v2_helper_purchases`); err != nil {
		return fmt.Errorf("rebuildHelperPurchasesInTx: rename: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_v2_helper_purchases_profile ON v2_helper_purchases(helper_profile_id);
CREATE INDEX IF NOT EXISTS idx_v2_helper_purchases_listing ON v2_helper_purchases(listing_id);
CREATE INDEX IF NOT EXISTS idx_v2_helper_purchases_state   ON v2_helper_purchases(state);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_v2_helper_purchases_active
    ON v2_helper_purchases(helper_profile_id, listing_id)
    WHERE state NOT IN ('invoice_expired', 'failed', 'receipt_expired');
`); err != nil {
		return fmt.Errorf("rebuildHelperPurchasesInTx: indexes: %w", err)
	}

	return nil
}

// rebuildListingsInTx rebuilds v2_listings without the inline UNIQUE constraint on
// display_name, preserving all data. Called within an already-open transaction.
func rebuildListingsInTx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE v2_listings_new (
    id                     TEXT PRIMARY KEY,
    flow_id                TEXT NOT NULL UNIQUE REFERENCES v2_client_flows(id),
    city                   TEXT NOT NULL,
    country_code           TEXT NOT NULL,
    dependency_type        TEXT NOT NULL,
    help_type              TEXT NOT NULL,
    urgency                TEXT NOT NULL,
    languages              TEXT NOT NULL,
    display_name           TEXT NOT NULL,
    contact_type           TEXT NOT NULL CHECK (contact_type IN ('telegram', 'signal')),
    contact_ciphertext     TEXT NOT NULL,
    contact_nonce          TEXT NOT NULL,
    contact_key_version    TEXT NOT NULL,
    state                  TEXT NOT NULL DEFAULT 'visible'
                                CHECK (state IN ('visible', 'hidden', 'finished')),
    visible_until          INTEGER,
    first_published_at     INTEGER NOT NULL,
    last_activated_at      INTEGER NOT NULL,
    entitlement_expires_at INTEGER NOT NULL,
    activation_count       INTEGER NOT NULL DEFAULT 1 CHECK (activation_count >= 1),
    created_at             INTEGER NOT NULL,
    updated_at             INTEGER NOT NULL,

    CHECK (length(display_name) > 0),
    CHECK (length(contact_ciphertext) > 0),
    CHECK (length(contact_nonce) > 0),
    CHECK (length(contact_key_version) > 0),

    CHECK (
        (state = 'visible'  AND visible_until IS NOT NULL)
        OR
        (state = 'hidden'   AND visible_until IS NULL)
        OR
        (state = 'finished' AND visible_until IS NULL)
    ),

    CHECK (created_at <= first_published_at),
    CHECK (updated_at >= created_at),
    CHECK (first_published_at <= last_activated_at),
    CHECK (last_activated_at <= entitlement_expires_at),
    CHECK (entitlement_expires_at > created_at),

    CHECK (state != 'visible' OR (last_activated_at < visible_until AND visible_until <= entitlement_expires_at))
)`); err != nil {
		return fmt.Errorf("rebuildListingsInTx: create new: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO v2_listings_new
    (id, flow_id, city, country_code, dependency_type, help_type, urgency,
     languages, display_name, contact_type, contact_ciphertext, contact_nonce,
     contact_key_version, state, visible_until, first_published_at, last_activated_at,
     entitlement_expires_at, activation_count, created_at, updated_at)
SELECT
    id, flow_id, city, country_code, dependency_type, help_type, urgency,
    languages, display_name, contact_type, contact_ciphertext, contact_nonce,
    contact_key_version, state, visible_until, first_published_at, last_activated_at,
    entitlement_expires_at, activation_count, created_at, updated_at
FROM v2_listings`); err != nil {
		return fmt.Errorf("rebuildListingsInTx: copy data: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DROP TABLE v2_listings`); err != nil {
		return fmt.Errorf("rebuildListingsInTx: drop old: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE v2_listings_new RENAME TO v2_listings`); err != nil {
		return fmt.Errorf("rebuildListingsInTx: rename: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_v2_listings_flow  ON v2_listings(flow_id);
CREATE INDEX IF NOT EXISTS idx_v2_listings_state ON v2_listings(state);
CREATE INDEX IF NOT EXISTS idx_v2_listings_city  ON v2_listings(city);
`); err != nil {
		return fmt.Errorf("rebuildListingsInTx: indexes: %w", err)
	}

	return nil
}

// OpenMemory opens an in-memory SQLite database with V2 schema applied.
// Intended for tests only.
func OpenMemory() (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file::memory:?mode=memory&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("v2: OpenMemory: open: %w", err)
	}
	// SQLite in-memory databases are connection-scoped. With MaxOpenConns=1,
	// all queries share the same connection and the same in-memory DB.
	// This also serialises writes, making CAS updates safe in concurrent tests.
	db.SetMaxOpenConns(1)

	// Enable foreign keys on this connection (must be done per connection in SQLite).
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("v2: OpenMemory: enable foreign keys: %w", err)
	}

	if err := ApplySchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("v2: OpenMemory: apply schema: %w", err)
	}
	if err := MigrateSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("v2: OpenMemory: migrate schema: %w", err)
	}
	return db, nil
}

// OpenFile opens a file-backed SQLite database with V2 schema applied.
// WAL journal mode and a busy timeout are configured for concurrent access.
// MaxOpenConns is set to 1 to serialise writes (SQLite best practice for WAL).
func OpenFile(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_busy_timeout=5000&_journal_mode=WAL"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("v2: OpenFile: open: %w", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("v2: OpenFile: enable foreign keys: %w", err)
	}

	if err := ApplySchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("v2: OpenFile: apply schema: %w", err)
	}
	if err := MigrateSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("v2: OpenFile: migrate schema: %w", err)
	}
	return db, nil
}

// tableCreateSQL returns the CREATE TABLE SQL from sqlite_master for the given table.
func tableCreateSQL(db *sql.DB, tbl string) (string, error) {
	var sql string
	err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, tbl,
	).Scan(&sql)
	if errors.Is(err, sqlErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("v2: tableCreateSQL(%s): %w", tbl, err)
	}
	return sql, nil
}

// sqlErrNoRows is a package-level alias to avoid importing database/sql in callers.
var sqlErrNoRows = sql.ErrNoRows

// schemaColumns returns the column names for a table, in definition order.
func schemaColumns(db *sql.DB, tbl string) ([]string, error) {
	rows, err := db.Query(`PRAGMA table_info(` + tbl + `)`)
	if err != nil {
		return nil, fmt.Errorf("v2: schemaColumns: PRAGMA table_info(%s): %w", tbl, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("v2: schemaColumns: table %q not found", tbl)
	}
	return cols, nil
}

// ErrSchemaColumnMissing is returned when a required column is absent after migration.
var ErrSchemaColumnMissing = errors.New("v2: schema column missing after migration")

// VerifyRequiredColumns checks that all required columns are present in a table.
// Used by release tests to validate fresh and upgraded DBs produce equivalent schemas.
func VerifyRequiredColumns(db *sql.DB, tbl string, required []string) error {
	cols, err := schemaColumns(db, tbl)
	if err != nil {
		return err
	}
	colSet := make(map[string]bool, len(cols))
	for _, c := range cols {
		colSet[c] = true
	}
	for _, req := range required {
		if !colSet[req] {
			return fmt.Errorf("%w: %s.%s", ErrSchemaColumnMissing, tbl, req)
		}
	}
	return nil
}
