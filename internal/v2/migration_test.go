package v2

// Migration acceptance tests — §2 of the FIX4 acceptance task.
//
// Covers four distinct scenarios:
//   TestMigration_09ARepairIntermediateFixture — exact 09A-REPAIR intermediate schema
//     (claimed_by + claimed_at present, claim_token + lease_until absent).
//     This is the schema that produced "no such column: claim_token" before the
//     column-introspection fix in rebuildOutboxTable.
//
//   TestMigration_SchemaDescriptor — canonical schema comparison between a fresh DB
//     and a 09A-REPAIR-migrated DB, using table_xinfo, sqlite_master.sql,
//     foreign_key_list, and index_list+index_xinfo.
//
//   TestMigration_FileBackedRehearsal — end-to-end rehearsal on actual SQLite files:
//     create old DB → close/backup → open+migrate → write/read new-schema data →
//     close → restore backup → open restored file → old-schema smoke.
//
//   TestMigration_IdempotentAllPaths — MigrateSchema is idempotent on all three
//     starting schemas (fresh, a0d6d99, 09A-REPAIR) when called 3 times each.

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ── 09A-REPAIR DDL constants ──────────────────────────────────────────────────

// outbox09ARepairDDL is the exact outbox schema present in 09A-REPAIR deployments:
// claimed_by and claimed_at present; claim_token and lease_until absent.
// The dead column claimed_at (not in current schema.sql) triggers outboxRequiresRebuild.
const outbox09ARepairDDL = `CREATE TABLE v2_informer_outbox (
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
    claimed_at   INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
)`

// recipients09AOldCheckDDL is the old recipients table with the narrow CHECK
// constraint (missing retry_exhausted and decrypt_failed).
const recipients09AOldCheckDDL = `CREATE TABLE v2_informer_outbox_recipients (
    outbox_id  TEXT NOT NULL REFERENCES v2_informer_outbox(id) ON DELETE CASCADE,
    sub_ref    TEXT NOT NULL,
    state      TEXT NOT NULL DEFAULT 'pending'
                   CHECK (state IN ('pending', 'delivered', 'permanent_failed')),
    updated_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (outbox_id, sub_ref)
)`

// a0d6d99OutboxDDL is the original outbox schema from the a0d6d99 checkpoint:
// no claim columns at all (no claimed_by, claimed_at, claim_token, lease_until).
const a0d6d99OutboxDDL = `CREATE TABLE v2_informer_outbox (
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
    last_error   TEXT,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
)`

// ── Test DB setup helpers ─────────────────────────────────────────────────────

// openInMemoryPreMigrate opens a fresh in-memory DB with ApplySchema applied
// but MigrateSchema NOT yet called. The returned DB has MaxOpenConns=1 and
// FK enabled. Use to set up fixture schemas before calling MigrateSchema.
func openInMemoryPreMigrate(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?mode=memory&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("openInMemoryPreMigrate: sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("openInMemoryPreMigrate: FK pragma: %v", err)
	}
	if err := ApplySchema(db); err != nil {
		t.Fatalf("openInMemoryPreMigrate: ApplySchema: %v", err)
	}
	return db
}

// apply09ARepairSchema replaces the outbox + recipients tables in db with the
// 09A-REPAIR intermediate schema. db must already have ApplySchema applied.
func apply09ARepairSchema(db *sql.DB) error {
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("apply09ARepairSchema: FK off: %w", err)
	}
	if _, err := db.Exec(`DROP TABLE IF EXISTS v2_informer_outbox_recipients`); err != nil {
		return fmt.Errorf("apply09ARepairSchema: drop recipients: %w", err)
	}
	if _, err := db.Exec(`DROP TABLE IF EXISTS v2_informer_outbox`); err != nil {
		return fmt.Errorf("apply09ARepairSchema: drop outbox: %w", err)
	}
	if _, err := db.Exec(outbox09ARepairDDL); err != nil {
		return fmt.Errorf("apply09ARepairSchema: create outbox: %w", err)
	}
	if _, err := db.Exec(recipients09AOldCheckDDL); err != nil {
		return fmt.Errorf("apply09ARepairSchema: create recipients: %w", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("apply09ARepairSchema: FK on: %w", err)
	}
	return nil
}

// applyA0D6D99Schema replaces the outbox table in db with the a0d6d99 schema
// and drops recipients (it didn't exist at a0d6d99). db must already have
// ApplySchema applied.
func applyA0D6D99Schema(db *sql.DB) error {
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("applyA0D6D99Schema: FK off: %w", err)
	}
	if _, err := db.Exec(`DROP TABLE IF EXISTS v2_informer_outbox_recipients`); err != nil {
		return fmt.Errorf("applyA0D6D99Schema: drop recipients: %w", err)
	}
	if _, err := db.Exec(`DROP TABLE IF EXISTS v2_informer_outbox`); err != nil {
		return fmt.Errorf("applyA0D6D99Schema: drop outbox: %w", err)
	}
	if _, err := db.Exec(a0d6d99OutboxDDL); err != nil {
		return fmt.Errorf("applyA0D6D99Schema: create outbox: %w", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("applyA0D6D99Schema: FK on: %w", err)
	}
	return nil
}

// ── Schema descriptor helpers ─────────────────────────────────────────────────

type migColDesc struct {
	cid     int
	name    string
	colType string
	notNull int
	dflt    string
	pk      int
	hidden  int
}

func (c migColDesc) String() string {
	return fmt.Sprintf("{cid=%d %s %s nn=%d dflt=%q pk=%d hidden=%d}",
		c.cid, c.name, c.colType, c.notNull, c.dflt, c.pk, c.hidden)
}

// collectMigColDescs returns visible column descriptors sorted by column name,
// using PRAGMA table_xinfo. Records cid, name, type, notNull, dflt, pk, and hidden.
// Entries with hidden != 0 are included with their hidden value for completeness.
func collectMigColDescs(db *sql.DB, tbl string) ([]migColDesc, error) {
	rows, err := db.Query(`PRAGMA table_xinfo(` + tbl + `)`)
	if err != nil {
		return nil, fmt.Errorf("PRAGMA table_xinfo(%s): %w", tbl, err)
	}
	defer rows.Close()
	var result []migColDesc
	for rows.Next() {
		var cid, notNull, pk, hidden int
		var name, colType string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk, &hidden); err != nil {
			return nil, err
		}
		dv := ""
		if dflt.Valid {
			dv = dflt.String
		}
		result = append(result, migColDesc{
			cid: cid, name: name, colType: colType,
			notNull: notNull, dflt: dv, pk: pk, hidden: hidden,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].name < result[j].name })
	return result, nil
}

type migFKDesc struct {
	id       int
	seq      int
	toTable  string
	fromCol  string
	toCol    string
	onUpdate string
	onDelete string
	match    string
}

func (f migFKDesc) String() string {
	return fmt.Sprintf("{id=%d seq=%d %s->%s.%s onUpd=%s onDel=%s match=%s}",
		f.id, f.seq, f.fromCol, f.toTable, f.toCol, f.onUpdate, f.onDelete, f.match)
}

func collectMigFKDescs(db *sql.DB, tbl string) ([]migFKDesc, error) {
	rows, err := db.Query(`PRAGMA foreign_key_list(` + tbl + `)`)
	if err != nil {
		return nil, fmt.Errorf("PRAGMA foreign_key_list(%s): %w", tbl, err)
	}
	defer rows.Close()
	var result []migFKDesc
	for rows.Next() {
		var d migFKDesc
		if err := rows.Scan(&d.id, &d.seq, &d.toTable, &d.fromCol, &d.toCol, &d.onUpdate, &d.onDelete, &d.match); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].fromCol != result[j].fromCol {
			return result[i].fromCol < result[j].fromCol
		}
		return result[i].seq < result[j].seq
	})
	return result, nil
}

type migIdxColDesc struct {
	name string
	desc int    // 0=ASC, 1=DESC
	coll string // collating sequence
}

func (c migIdxColDesc) String() string {
	return fmt.Sprintf("{%s desc=%d coll=%s}", c.name, c.desc, c.coll)
}

type migIdxDesc struct {
	name    string
	unique  int
	origin  string
	partial int
	cols    []migIdxColDesc // key columns in index order
}

func (d migIdxDesc) String() string {
	return fmt.Sprintf("{%s unique=%d origin=%s partial=%d cols=%v}",
		d.name, d.unique, d.origin, d.partial, d.cols)
}

func collectMigIdxDescs(db *sql.DB, tbl string) ([]migIdxDesc, error) {
	rows, err := db.Query(`PRAGMA index_list(` + tbl + `)`)
	if err != nil {
		return nil, fmt.Errorf("PRAGMA index_list(%s): %w", tbl, err)
	}
	defer rows.Close()
	var result []migIdxDesc
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return nil, err
		}
		if origin == "pk" {
			continue // skip auto-generated PRIMARY KEY indexes
		}
		result = append(result, migIdxDesc{name: name, unique: unique, origin: origin, partial: partial})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Collect key column descriptors for each index via PRAGMA index_xinfo.
	for i := range result {
		xrows, err := db.Query(`PRAGMA index_xinfo(` + result[i].name + `)`)
		if err != nil {
			return nil, fmt.Errorf("PRAGMA index_xinfo(%s): %w", result[i].name, err)
		}
		var cols []migIdxColDesc
		for xrows.Next() {
			var seqno, cid, descV, key int
			var name sql.NullString // NULL for rowid sentinel entries (cid = -1, -2)
			var coll string
			if err := xrows.Scan(&seqno, &cid, &name, &descV, &coll, &key); err != nil {
				xrows.Close()
				return nil, err
			}
			if key == 1 && cid >= 0 && name.Valid { // key column (not rowid sentinel)
				cols = append(cols, migIdxColDesc{name: name.String, desc: descV, coll: coll})
			}
		}
		xrows.Close()
		if err := xrows.Err(); err != nil {
			return nil, err
		}
		result[i].cols = cols
	}

	sort.Slice(result, func(i, j int) bool { return result[i].name < result[j].name })
	return result, nil
}

// ── Test M01: 09A-REPAIR intermediate fixture ─────────────────────────────────

// TestMigration_09ARepairIntermediateFixture proves MigrateSchema succeeds on
// the exact 09A-REPAIR intermediate schema. Before the column-introspection fix,
// rebuildOutboxTable emitted COALESCE(claim_token, ”) which SQLite rejects as
// "no such column: claim_token" even inside COALESCE.
func TestMigration_09ARepairIntermediateFixture(t *testing.T) {
	db := openInMemoryPreMigrate(t)
	if err := apply09ARepairSchema(db); err != nil {
		t.Fatalf("apply 09A-REPAIR schema: %v", err)
	}

	// Insert a row before migration; it must survive the full table rebuild.
	const now = int64(1700000000)
	if _, err := db.Exec(`
		INSERT INTO v2_informer_outbox
		  (id, listing_id, city, display_name, help_type, dep_type, urgency,
		   listing_url, event_key, state, attempt, claimed_by, claimed_at,
		   created_at, updated_at)
		VALUES ('m01-obx', 'm01-lst', 'tbilisi', 'TestM01', 'crisis', 'alcohol', 'urgent',
		        '/v2/listing/m01', 'first_publish:m01', 'pending', 0, 'worker-1', ?, ?, ?)`,
		now, now, now,
	); err != nil {
		t.Fatalf("insert test row: %v", err)
	}

	// Must NOT fail with "no such column: claim_token".
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("MigrateSchema on 09A-REPAIR: %v", err)
	}

	// Idempotent: second run must also succeed.
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("MigrateSchema (idempotent 2nd run): %v", err)
	}

	// Data must survive: row inserted before migration must still be readable.
	var gotID string
	if err := db.QueryRow(`SELECT id FROM v2_informer_outbox WHERE id='m01-obx'`).Scan(&gotID); err != nil {
		t.Fatalf("test row lost after rebuild: %v", err)
	}

	// claimed_by must be preserved; claim_token defaults to ''; lease_until to 0.
	var claimedBy, claimToken string
	var leaseUntil int64
	if err := db.QueryRow(
		`SELECT claimed_by, claim_token, lease_until FROM v2_informer_outbox WHERE id='m01-obx'`,
	).Scan(&claimedBy, &claimToken, &leaseUntil); err != nil {
		t.Fatalf("scan claim fields: %v", err)
	}
	if claimedBy != "worker-1" {
		t.Errorf("claimed_by: want %q, got %q", "worker-1", claimedBy)
	}
	if claimToken != "" {
		t.Errorf("claim_token: want empty string, got %q", claimToken)
	}
	if leaseUntil != 0 {
		t.Errorf("lease_until: want 0, got %d", leaseUntil)
	}

	// Dead column claimed_at must be absent after rebuild.
	if has, _ := columnExists(db, "v2_informer_outbox", "claimed_at"); has {
		t.Error("dead column 'claimed_at' still present after rebuild — rebuild did not run")
	}

	// All 17 required outbox columns must be present.
	required := []string{
		"id", "listing_id", "city", "display_name", "help_type", "dep_type",
		"urgency", "listing_url", "event_key", "state", "attempt",
		"claimed_by", "claim_token", "lease_until", "last_error", "created_at", "updated_at",
	}
	if err := VerifyRequiredColumns(db, "v2_informer_outbox", required); err != nil {
		t.Fatalf("columns after 09A-REPAIR migration: %v", err)
	}

	// Recipients table must accept retry_exhausted (extended CHECK constraint).
	if _, err := db.Exec(`
		INSERT INTO v2_informer_outbox_recipients (outbox_id, sub_ref, state, attempts, updated_at)
		VALUES ('m01-obx', 'isub_retry', 'retry_exhausted', 2, ?)`, now,
	); err != nil {
		t.Fatalf("INSERT retry_exhausted after migration: %v", err)
	}

	// Recipients table must accept decrypt_failed.
	if _, err := db.Exec(`
		INSERT INTO v2_informer_outbox_recipients (outbox_id, sub_ref, state, attempts, updated_at)
		VALUES ('m01-obx', 'isub_decrypt', 'decrypt_failed', 1, ?)`, now,
	); err != nil {
		t.Fatalf("INSERT decrypt_failed after migration: %v", err)
	}
}

// ── Test M02: Canonical schema descriptor comparison ─────────────────────────

// TestMigration_SchemaDescriptor verifies that migrating from 09A-REPAIR produces
// v2_informer_outbox and v2_informer_outbox_recipients schemas that are identical
// to a fresh DB, as measured by:
//
//	(1) PRAGMA table_xinfo  — column names, types, NOT NULL, default values
//	(2) sqlite_master.sql   — CREATE TABLE sql contains required CHECK/UNIQUE strings
//	(3) PRAGMA foreign_key_list — FK constraints
//	(4) PRAGMA index_list + index_xinfo — index names, uniqueness, partial flags, columns
func TestMigration_SchemaDescriptor(t *testing.T) {
	// Baseline: fresh DB (ApplySchema + MigrateSchema via OpenMemory).
	freshDB, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory (fresh): %v", err)
	}
	defer freshDB.Close()

	// Subject: 09A-REPAIR DB migrated to current schema.
	repairedDB := openInMemoryPreMigrate(t)
	if err := apply09ARepairSchema(repairedDB); err != nil {
		t.Fatalf("apply09ARepairSchema: %v", err)
	}
	if err := MigrateSchema(repairedDB); err != nil {
		t.Fatalf("MigrateSchema on 09A-REPAIR: %v", err)
	}

	tables := []string{"v2_informer_outbox", "v2_informer_outbox_recipients"}
	for _, tbl := range tables {
		tbl := tbl
		t.Run(tbl, func(t *testing.T) {
			// (1) table_xinfo — column descriptors sorted by name.
			freshCols, err := collectMigColDescs(freshDB, tbl)
			if err != nil {
				t.Fatalf("collect fresh cols: %v", err)
			}
			repairCols, err := collectMigColDescs(repairedDB, tbl)
			if err != nil {
				t.Fatalf("collect repair cols: %v", err)
			}
			if fmt.Sprintf("%v", freshCols) != fmt.Sprintf("%v", repairCols) {
				t.Errorf("table_xinfo mismatch:\n  fresh:  %v\n  repair: %v", freshCols, repairCols)
			}

			// (2) sqlite_master.sql — normalized full CREATE TABLE body must match.
			// normalizeCreateSQL strips IF NOT EXISTS, back-tick/double-quote identifiers,
			// collapses whitespace, and returns only the content from '(' onward so that
			// table-name differences do not affect the comparison.
			normalizeCreateSQL := func(s string) string {
				s = strings.ReplaceAll(s, `"`, "")
				s = strings.ReplaceAll(s, "`", "")
				s = strings.ReplaceAll(s, "IF NOT EXISTS", "")
				s = strings.Join(strings.Fields(s), " ")
				if idx := strings.IndexByte(s, '('); idx >= 0 {
					s = s[idx:]
				}
				return s
			}
			freshSQL, err := tableCreateSQL(freshDB, tbl)
			if err != nil {
				t.Fatalf("tableCreateSQL fresh %s: %v", tbl, err)
			}
			repairSQL, err := tableCreateSQL(repairedDB, tbl)
			if err != nil {
				t.Fatalf("tableCreateSQL repair %s: %v", tbl, err)
			}
			normFresh := normalizeCreateSQL(freshSQL)
			normRepair := normalizeCreateSQL(repairSQL)
			if normFresh != normRepair {
				t.Errorf("sqlite_master.sql body mismatch for %s:\n  fresh:  %s\n  repair: %s",
					tbl, normFresh, normRepair)
			}

			// (3) foreign_key_list — FK from/to/onDelete must match.
			freshFKs, err := collectMigFKDescs(freshDB, tbl)
			if err != nil {
				t.Fatalf("collect fresh FKs: %v", err)
			}
			repairFKs, err := collectMigFKDescs(repairedDB, tbl)
			if err != nil {
				t.Fatalf("collect repair FKs: %v", err)
			}
			if fmt.Sprintf("%v", freshFKs) != fmt.Sprintf("%v", repairFKs) {
				t.Errorf("FK mismatch:\n  fresh:  %v\n  repair: %v", freshFKs, repairFKs)
			}

			// (4) index_list + index_xinfo — index descriptors must match.
			freshIdxs, err := collectMigIdxDescs(freshDB, tbl)
			if err != nil {
				t.Fatalf("collect fresh indexes: %v", err)
			}
			repairIdxs, err := collectMigIdxDescs(repairedDB, tbl)
			if err != nil {
				t.Fatalf("collect repair indexes: %v", err)
			}
			if fmt.Sprintf("%v", freshIdxs) != fmt.Sprintf("%v", repairIdxs) {
				t.Errorf("index mismatch:\n  fresh:  %v\n  repair: %v", freshIdxs, repairIdxs)
			}
		})
	}
}

// ── Test M03: File-backed rehearsal ──────────────────────────────────────────

// TestMigration_FileBackedRehearsal proves migration works on an actual SQLite
// file on disk (not in-memory). Steps:
//  1. Create old DB file (a0d6d99 schema) and insert test data.
//  2. Close file; backup file to disk.
//  3. Open file, run MigrateSchema; verify old data preserved.
//  4. Write and read new-schema data (claim_token, lease_until).
//  5. Close file; restore from backup.
//  6. Open restored file; verify old schema is intact (smoke).
func TestMigration_FileBackedRehearsal(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "naroom.db")
	backupPath := filepath.Join(dir, "naroom_backup.db")

	openFileDB := func(path string) *sql.DB {
		t.Helper()
		db, err := sql.Open("sqlite", "file:"+path+"?_busy_timeout=5000")
		if err != nil {
			t.Fatalf("openFileDB(%s): %v", path, err)
		}
		db.SetMaxOpenConns(1)
		if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
			t.Fatalf("openFileDB FK pragma: %v", err)
		}
		return db
	}

	// ── Step 1: Create old DB (a0d6d99 schema) ───────────────────────────────
	oldDB := openFileDB(dbPath)
	if err := ApplySchema(oldDB); err != nil {
		t.Fatalf("ApplySchema (old): %v", err)
	}
	if err := applyA0D6D99Schema(oldDB); err != nil {
		t.Fatalf("applyA0D6D99Schema: %v", err)
	}
	const now = int64(1700000000)
	if _, err := oldDB.Exec(`
		INSERT INTO v2_informer_outbox
		  (id, listing_id, city, display_name, help_type, dep_type, urgency,
		   listing_url, event_key, state, attempt, created_at, updated_at)
		VALUES ('fbr-obx', 'fbr-lst', 'yerevan', 'FBR Test', 'crisis', 'alcohol',
		        'urgent', '/v2/listing/fbr', 'first_publish:fbr', 'pending', 0, ?, ?)`,
		now, now,
	); err != nil {
		t.Fatalf("insert old data: %v", err)
	}
	// Close before file copy — SQLite requires exclusive access for safe copy.
	if err := oldDB.Close(); err != nil {
		t.Fatalf("close old DB: %v", err)
	}

	// ── Step 2: Backup ─────────────────────────────────────────────────────────
	data, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read DB file: %v", err)
	}
	if err := os.WriteFile(backupPath, data, 0600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	// ── Step 3: Open and migrate ───────────────────────────────────────────────
	migratedDB := openFileDB(dbPath)
	if err := MigrateSchema(migratedDB); err != nil {
		t.Fatalf("MigrateSchema: %v", err)
	}
	// Old data must be preserved.
	var gotCity string
	if err := migratedDB.QueryRow(`SELECT city FROM v2_informer_outbox WHERE id='fbr-obx'`).Scan(&gotCity); err != nil {
		t.Fatalf("old data lost after migration: %v", err)
	}
	if gotCity != "yerevan" {
		t.Errorf("city: want yerevan, got %q", gotCity)
	}

	// ── Step 4: Write and read new-schema data ─────────────────────────────────
	const now2 = now + 100
	const wantToken = "tok-abc123"
	const wantLease = now2 + 30
	if _, err := migratedDB.Exec(`
		INSERT INTO v2_informer_outbox
		  (id, listing_id, city, display_name, help_type, dep_type, urgency,
		   listing_url, event_key, state, attempt, claimed_by, claim_token,
		   lease_until, created_at, updated_at)
		VALUES ('fbr-obx2', 'fbr-lst2', 'yerevan', 'FBR Test 2', 'crisis', 'alcohol',
		        'urgent', '/v2/listing/fbr2', 'first_publish:fbr2', 'pending', 0,
		        'worker-A', ?, ?, ?, ?)`,
		wantToken, wantLease, now2, now2,
	); err != nil {
		t.Fatalf("insert new-schema data: %v", err)
	}
	var claimToken string
	var leaseUntil int64
	if err := migratedDB.QueryRow(
		`SELECT claim_token, lease_until FROM v2_informer_outbox WHERE id='fbr-obx2'`,
	).Scan(&claimToken, &leaseUntil); err != nil {
		t.Fatalf("read new-schema data: %v", err)
	}
	if claimToken != wantToken {
		t.Errorf("claim_token: want %q, got %q", wantToken, claimToken)
	}
	if leaseUntil != wantLease {
		t.Errorf("lease_until: want %d, got %d", wantLease, leaseUntil)
	}
	if err := migratedDB.Close(); err != nil {
		t.Fatalf("close migrated DB: %v", err)
	}

	// ── Step 5: Restore from backup ────────────────────────────────────────────
	if err := os.WriteFile(dbPath, data, 0600); err != nil {
		t.Fatalf("restore backup: %v", err)
	}

	// ── Step 6: Old-schema smoke on restored file ──────────────────────────────
	restoredDB := openFileDB(dbPath)
	defer restoredDB.Close()

	// claim_token must NOT exist — backup restore must be complete.
	if has, _ := columnExists(restoredDB, "v2_informer_outbox", "claim_token"); has {
		t.Error("restored DB has claim_token column — backup restore failed")
	}
	// New row must NOT exist (it was written to the post-migration file, not the backup).
	var cnt int
	_ = restoredDB.QueryRow(`SELECT COUNT(*) FROM v2_informer_outbox WHERE id='fbr-obx2'`).Scan(&cnt)
	if cnt != 0 {
		t.Errorf("restored DB has new row 'fbr-obx2' — backup restore failed")
	}
	// Original test row must be present with old-schema columns.
	if err := restoredDB.QueryRow(
		`SELECT city FROM v2_informer_outbox WHERE id='fbr-obx'`,
	).Scan(&gotCity); err != nil {
		t.Fatalf("original row not in restored DB: %v", err)
	}
	if gotCity != "yerevan" {
		t.Errorf("restored row city: want yerevan, got %q", gotCity)
	}
}

// ── Test M05: Rollback leaves no half-schema ─────────────────────────────────

// TestMigration_Rollback proves that a migration failure mid-run leaves
// v2_informer_outbox in its original pre-migration state and restores
// PRAGMA foreign_keys = ON on exit.
//
// Sabotage strategy: pre-create v2_informer_outbox_new (the temp table used by
// rebuildOutboxInTx) BEFORE calling MigrateSchema. The CREATE TABLE inside the
// migration TX will fail with "table already exists", forcing a rollback. All
// DDL inside the TX is rolled back; the original schema and data are intact.
func TestMigration_Rollback(t *testing.T) {
	db := openInMemoryPreMigrate(t)
	if err := apply09ARepairSchema(db); err != nil {
		t.Fatalf("apply09ARepairSchema: %v", err)
	}

	// Confirm dead column is present before sabotage.
	if has, _ := columnExists(db, "v2_informer_outbox", "claimed_at"); !has {
		t.Fatal("precondition: claimed_at should be present in 09A-REPAIR schema")
	}

	// Insert a row so we can verify data survives rollback.
	const now = int64(1700000000)
	if _, err := db.Exec(`
		INSERT INTO v2_informer_outbox
		  (id, listing_id, city, display_name, help_type, dep_type, urgency,
		   listing_url, event_key, state, attempt, claimed_by, claimed_at,
		   created_at, updated_at)
		VALUES ('roll-obx', 'roll-lst', 'tbilisi', 'Rollback Test', 'crisis', 'alcohol',
		        'urgent', '/v2/listing/roll', 'first_publish:roll', 'pending', 0, '', ?, ?, ?)`,
		now, now, now,
	); err != nil {
		t.Fatalf("insert test row: %v", err)
	}

	// Sabotage: pre-create the temp table that rebuildOutboxInTx needs to CREATE.
	// This is created outside any transaction so it is not affected by MigrateSchema's
	// internal TX rollback — it remains as the blocking artifact.
	if _, err := db.Exec(`CREATE TABLE v2_informer_outbox_new (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create sabotage table: %v", err)
	}

	// MigrateSchema must fail; rebuildOutboxInTx's CREATE TABLE will hit "already exists".
	if err := MigrateSchema(db); err == nil {
		t.Fatal("MigrateSchema must return an error when sabotage table exists")
	}

	// ── Verify no half-schema left ───────────────────────────────────────────

	// Dead column claimed_at must still be present — the rebuild was rolled back.
	if has, _ := columnExists(db, "v2_informer_outbox", "claimed_at"); !has {
		t.Error("claimed_at missing after rollback — original schema was modified")
	}

	// claim_token must NOT be present — it was never successfully added.
	if has, _ := columnExists(db, "v2_informer_outbox", "claim_token"); has {
		t.Error("claim_token present after rollback — half-migration left behind")
	}

	// Original data must survive the rollback.
	var gotCity string
	if err := db.QueryRow(`SELECT city FROM v2_informer_outbox WHERE id='roll-obx'`).Scan(&gotCity); err != nil {
		t.Fatalf("test row lost after rollback: %v", err)
	}
	if gotCity != "tbilisi" {
		t.Errorf("city after rollback: want tbilisi, got %q", gotCity)
	}

	// ── Verify PRAGMA foreign_keys = ON restored after failed migration ────────

	// MaxOpenConns=1 and in-memory DB: the pinned conn's deferred PRAGMA ON fires
	// before conn.Close(), so when the same underlying SQLite connection is reused
	// by this query it sees FK = ON.
	var fkOn int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fkOn); err != nil {
		t.Fatalf("PRAGMA foreign_keys query: %v", err)
	}
	if fkOn != 1 {
		t.Errorf("foreign_keys after failed migration: want 1 (ON), got %d", fkOn)
	}
}

// ── Test M04: Idempotency on all three paths ──────────────────────────────────

// TestMigration_IdempotentAllPaths proves MigrateSchema is idempotent when called
// three times consecutively on each starting schema (fresh, a0d6d99, 09A-REPAIR).
func TestMigration_IdempotentAllPaths(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		db, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		defer db.Close()
		// OpenMemory already calls MigrateSchema once; run 2 more times.
		for i := 2; i <= 3; i++ {
			if err := MigrateSchema(db); err != nil {
				t.Fatalf("MigrateSchema run %d (fresh): %v", i, err)
			}
		}
	})

	t.Run("a0d6d99", func(t *testing.T) {
		db := openInMemoryPreMigrate(t)
		if err := applyA0D6D99Schema(db); err != nil {
			t.Fatalf("applyA0D6D99Schema: %v", err)
		}
		for i := 1; i <= 3; i++ {
			if err := MigrateSchema(db); err != nil {
				t.Fatalf("MigrateSchema run %d (a0d6d99): %v", i, err)
			}
		}
		// After 3 runs, all required columns must be present.
		required := []string{"claimed_by", "claim_token", "lease_until"}
		if err := VerifyRequiredColumns(db, "v2_informer_outbox", required); err != nil {
			t.Fatalf("columns after a0d6d99 idempotency: %v", err)
		}
	})

	t.Run("09A-REPAIR", func(t *testing.T) {
		db := openInMemoryPreMigrate(t)
		if err := apply09ARepairSchema(db); err != nil {
			t.Fatalf("apply09ARepairSchema: %v", err)
		}
		for i := 1; i <= 3; i++ {
			if err := MigrateSchema(db); err != nil {
				t.Fatalf("MigrationSchema run %d (09A-REPAIR): %v", i, err)
			}
		}
		// Dead column must be absent; required columns present.
		if has, _ := columnExists(db, "v2_informer_outbox", "claimed_at"); has {
			t.Error("dead column 'claimed_at' still present after idempotent migration")
		}
		required := []string{"claimed_by", "claim_token", "lease_until"}
		if err := VerifyRequiredColumns(db, "v2_informer_outbox", required); err != nil {
			t.Fatalf("columns after 09A-REPAIR idempotency: %v", err)
		}
	})
}

func TestMigration_AddsAltBrowserTokenToExistingHelperPurchases(t *testing.T) {
	oldSchema := strings.Replace(
		schemaSQL,
		"    alt_browser_token_hash    TEXT UNIQUE,\n",
		"",
		1,
	)
	if oldSchema == schemaSQL {
		t.Fatal("old-schema fixture did not remove alt_browser_token_hash")
	}

	db, err := sql.Open("sqlite", "file::memory:?mode=memory&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatalf("apply old schema: %v", err)
	}

	const (
		clientProfileID  = "1111111111111111111111111111111111111111111111111111111111111111"
		clientWalletFP   = "2222222222222222222222222222222222222222222222222222222222222222"
		flowID           = "3333333333333333333333333333333333333333333333333333333333333333"
		listingID        = "4444444444444444444444444444444444444444444444444444444444444444"
		helperProfileID  = "5555555555555555555555555555555555555555555555555555555555555555"
		helperWalletFP   = "6666666666666666666666666666666666666666666666666666666666666666"
		purchaseID       = "7777777777777777777777777777777777777777777777777777777777777777"
		browserTokenHash = "8888888888888888888888888888888888888888888888888888888888888888"
	)
	const now = int64(1700000000)

	if _, err := db.Exec(`
		INSERT INTO v2_client_profiles
			(id, wallet_fingerprint, currency, public_name, created_at, updated_at)
		VALUES (?, ?, 'LTC', 'Old Client', ?, ?)`,
		clientProfileID, clientWalletFP, now, now,
	); err != nil {
		t.Fatalf("insert client profile: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO v2_client_flows
			(id, wallet_fingerprint, currency, management_code_hash, state,
			 client_profile_id, required_hard_floor_usd, created_at, updated_at)
		VALUES (?, ?, 'LTC', 'old-code-hash', 'form_ready', ?, 50, ?, ?)`,
		flowID, clientWalletFP, clientProfileID, now, now,
	); err != nil {
		t.Fatalf("insert client flow: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO v2_listings
			(id, flow_id, city, country_code, dependency_type, help_type, urgency,
			 languages, display_name, contact_type, contact_ciphertext, contact_nonce,
			 contact_key_version, state, visible_until, first_published_at,
			 last_activated_at, entitlement_expires_at, activation_count, created_at, updated_at)
		VALUES (?, ?, 'tbilisi', 'GE', 'alcohol', 'crisis', 'urgent',
			'["en"]', 'Old Listing', 'telegram', 'cipher', 'nonce', 'v1',
			'visible', ?, ?, ?, ?, 1, ?, ?)`,
		listingID, flowID, now+86400, now, now, now+5*86400, now, now,
	); err != nil {
		t.Fatalf("insert listing: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO v2_helper_profiles
			(id, wallet_fingerprint, currency, public_name, created_at, updated_at)
		VALUES (?, ?, 'LTC', 'Old Helper', ?, ?)`,
		helperProfileID, helperWalletFP, now, now,
	); err != nil {
		t.Fatalf("insert helper profile: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO v2_helper_purchases
			(id, listing_id, helper_profile_id, browser_token_hash, state,
			 country_code_snapshot, required_post_payment_floor_usd, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'awaiting_payment', 'GE', 50, ?, ?)`,
		purchaseID, listingID, helperProfileID, browserTokenHash, now, now,
	); err != nil {
		t.Fatalf("insert helper purchase: %v", err)
	}

	for run := 1; run <= 2; run++ {
		if err := MigrateSchema(db); err != nil {
			t.Fatalf("MigrateSchema run %d: %v", run, err)
		}
	}
	if err := VerifyRequiredColumns(db, "v2_helper_purchases", []string{"alt_browser_token_hash"}); err != nil {
		t.Fatal(err)
	}

	var gotID string
	if err := db.QueryRow(`
		SELECT id FROM v2_helper_purchases
		WHERE browser_token_hash = ? OR alt_browser_token_hash = ?`,
		browserTokenHash, browserTokenHash,
	).Scan(&gotID); err != nil {
		t.Fatalf("lookup using migrated query: %v", err)
	}
	if gotID != purchaseID {
		t.Fatalf("purchase changed during migration: got %s want %s", gotID, purchaseID)
	}

	altHash := "9999999999999999999999999999999999999999999999999999999999999999"
	if _, err := db.Exec(`UPDATE v2_helper_purchases SET alt_browser_token_hash = ? WHERE id = ?`, altHash, purchaseID); err != nil {
		t.Fatalf("set alternate browser token: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO v2_helper_purchases
			(id, listing_id, helper_profile_id, browser_token_hash, alt_browser_token_hash,
			 state, country_code_snapshot, required_post_payment_floor_usd, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'failed', 'GE', 50, ?, ?)`,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		listingID, helperProfileID,
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		altHash, now, now,
	); err == nil {
		t.Fatal("duplicate alt_browser_token_hash must violate unique index")
	}
}
