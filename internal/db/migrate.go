package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Meta keys: one-shot flags handed from a migration to the boot path, stored
// in the meta table (PRAGMA user_version can't express these).
const (
	// MetaRebuildPending is "1" when a migration has requested a full rebuild
	// of derived data from the archive. cmd/fitbase reads it on boot, runs
	// RebuildFromArchive, and clears it only on success — failures retry every
	// boot. See the migrations doc below for when to use it.
	MetaRebuildPending = "rebuild_pending"
)

// GetMeta returns the stored value for key, or "" if absent.
func (db *DB) GetMeta(key string) (string, error) {
	var v string
	err := db.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

const metaUpsertSQL = `
	INSERT INTO meta (key, value) VALUES (?, ?)
	ON CONFLICT(key) DO UPDATE SET value = excluded.value`

// SetMeta upserts a meta key/value.
func (db *DB) SetMeta(key, value string) error {
	_, err := db.Exec(metaUpsertSQL, key, value)
	return err
}

// setMetaTx upserts a meta key/value inside a migration's transaction, so the
// write commits (or rolls back) atomically with the DDL and version bump.
func setMetaTx(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec(metaUpsertSQL, key, value)
	return err
}

// DeleteMeta removes a meta key (no error if absent).
func (db *DB) DeleteMeta(key string) error {
	_, err := db.Exec("DELETE FROM meta WHERE key = ?", key)
	return err
}

// migrations is the single source of truth for the database's shape. Every
// database — brand-new or years old — replays this ladder from wherever its
// PRAGMA user_version left off (version = number of entries that have run), so
// fresh and upgraded installs are structurally identical by construction.
//
// Rules for adding a migration:
//
//   - APPEND only. Never reorder, edit, or delete a shipped entry — including
//     schema.sql, which is v1's frozen payload. A migration owns its code:
//     use local helpers, not shared ones that later refactors could change.
//   - Each entry runs in one transaction with its version bump; on error it
//     rolls back and re-runs next boot.
//   - Migrations run on fresh databases too — guard anything that assumes
//     pre-existing state (v1 and v2 show the patterns).
//   - Derived-data changes: if the inputs are in the DB and recomputing is
//     fast, rederive in place inside the migration (v3 is the template).
//     Otherwise set MetaRebuildPending via setMetaTx — the boot path rebuilds
//     from the FIT archive in the background behind the progress UI.
//     Migrations block boot (db.Open runs before the HTTP server), so slow
//     work must take the background path.
var migrations = []func(*sql.Tx) error{
	migrateSchema,              // v1 — frozen baseline schema (schema.sql)
	migrateBaseline,            // v2 — reconcile pre-user_version schema drift
	migrateConvergeDerivedData, // v3 — rederive legacy derived data in place
}

// migrate replays every migration the database hasn't run yet.
func migrate(sqldb *sql.DB) error {
	cur, err := userVersion(sqldb)
	if err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	for i := cur; i < len(migrations); i++ {
		tx, err := sqldb.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", i+1, err)
		}
		if err := migrations[i](tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		// Transactional, so it commits atomically with the migration's DDL.
		// Pragmas can't take bind parameters; an int is injection-safe.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("stamp version %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", i+1, err)
		}
	}
	return nil
}

// gets the PRAGMA user_version for SQLite DB.
func userVersion(sqldb *sql.DB) (int, error) {
	var v int
	err := sqldb.QueryRow("PRAGMA user_version").Scan(&v)
	return v, err
}

// migrateSchema (v1) executes the frozen baseline schema. Everything in it is
// IF NOT EXISTS: it builds an empty database in full, and on a pre-versioning
// database only fills in missing objects, leaving existing tables for v2.
func migrateSchema(tx *sql.Tx) error {
	if _, err := tx.Exec(schema); err != nil {
		return fmt.Errorf("baseline schema: %w", err)
	}
	return nil
}

// migrateBaseline (v2) reconciles every schema change that predates
// user_version tracking. Written defensively (column-exists / DDL-text guards)
// because a legacy database may be in any partially-migrated state; on a
// database v1 just built, every guard no-ops.
func migrateBaseline(tx *sql.Tx) error {
	addColumn := func(table, col, def string) error {
		_, err := tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col, def))
		if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("add %s.%s: %w", table, col, err)
		}
		return nil
	}

	// Pre-computed GPS thumbnails, their format version, and the local training
	// day — added to the workouts table after initial release.
	if err := addColumn("workouts", "route_coords", "TEXT DEFAULT NULL"); err != nil {
		return err
	}
	if err := addColumn("workouts", "route_coords_v", "INTEGER DEFAULT NULL"); err != nil {
		return err
	}
	if err := addColumn("workouts", "training_day", "TEXT DEFAULT NULL"); err != nil {
		return err
	}
	// Created here rather than in schema.sql because on a legacy database the
	// training_day column doesn't exist until the ALTER above runs — schema.sql
	// (v1) would fail trying to index it.
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_workouts_training_day ON workouts(training_day)`); err != nil {
		return fmt.Errorf("create training_day index: %w", err)
	}

	// Sweet Spot time-in-band alongside the 7 power zones.
	if err := addColumn("workout_zone_times", "ss_secs", "INTEGER"); err != nil {
		return err
	}

	// imported_files PK: hash → (hash, filename). The old shape silently dropped
	// INSERTs for a hash already seen under a different name, breaking
	// filename-based dedup. Rebuild only if still on the old single-column PK.
	var filesDef string
	switch err := tx.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='imported_files'`,
	).Scan(&filesDef); {
	case err == sql.ErrNoRows:
		// no table yet — schema.sql already created the modern shape
	case err != nil:
		return fmt.Errorf("inspect imported_files: %w", err)
	case !strings.Contains(filesDef, "PRIMARY KEY (hash, filename)"):
		if _, err := tx.Exec(`
			CREATE TABLE imported_files_new (
				hash        TEXT NOT NULL,
				filename    TEXT NOT NULL,
				imported_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
				PRIMARY KEY (hash, filename)
			);
			INSERT INTO imported_files_new (hash, filename, imported_at)
				SELECT hash, filename, imported_at FROM imported_files;
			DROP TABLE imported_files;
			ALTER TABLE imported_files_new RENAME TO imported_files;
			CREATE INDEX IF NOT EXISTS idx_imported_files_filename ON imported_files(filename);
		`); err != nil {
			return fmt.Errorf("migrate imported_files PK: %w", err)
		}
	}

	// idx_workouts_sport_recorded_at was first created ASC; rebuild DESC so
	// per-sport "newest first" listings scan the index forward.
	var idxDef string
	switch err := tx.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_workouts_sport_recorded_at'`,
	).Scan(&idxDef); {
	case err == sql.ErrNoRows:
		// schema.sql already created it DESC
	case err != nil:
		return fmt.Errorf("inspect idx_workouts_sport_recorded_at: %w", err)
	case !strings.Contains(idxDef, "recorded_at DESC"):
		if _, err := tx.Exec(`
			DROP INDEX idx_workouts_sport_recorded_at;
			CREATE INDEX idx_workouts_sport_recorded_at ON workouts(sport, recorded_at DESC);
		`); err != nil {
			return fmt.Errorf("migrate idx_workouts_sport_recorded_at: %w", err)
		}
	}

	return nil
}

// migrateConvergeDerivedData (v3) rederives legacy derived columns in place,
// from data already in the database: NULL training_day (from recorded_at +
// athlete timezone) and legacy 75-point route_coords (re-simplified from
// workout_streams). Output is identical to a full reimport; converged and
// fresh databases select zero rows and no-op. The version threshold is a
// literal 2, not routeCoordsVersion — a shipped migration must not change
// meaning when the const is bumped. (Calling the live simplifyCoords is fine:
// a future simplification change ships its own migration that re-converges
// rows below the new version, including these.)
func migrateConvergeDerivedData(tx *sql.Tx) error {
	// Same computation InsertWorkout stamps on new rows.
	tz := time.UTC
	var tzName string
	switch err := tx.QueryRow(`SELECT timezone FROM athlete WHERE id = 1`).Scan(&tzName); {
	case err == sql.ErrNoRows:
		// no profile yet — UTC default
	case err != nil:
		return fmt.Errorf("read athlete timezone: %w", err)
	default:
		if loc, err := time.LoadLocation(tzName); err == nil {
			tz = loc
		}
	}

	type dayRow struct{ id, rec string }
	var days []dayRow
	rows, err := tx.Query(`SELECT id, recorded_at FROM workouts WHERE training_day IS NULL`)
	if err != nil {
		return fmt.Errorf("scan for NULL training_day: %w", err)
	}
	for rows.Next() {
		var r dayRow
		if err := rows.Scan(&r.id, &r.rec); err != nil {
			rows.Close()
			return err
		}
		days = append(days, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range days {
		rec, err := time.Parse(time.RFC3339, r.rec)
		if err != nil {
			continue // unparseable timestamp: leave NULL rather than fail the ladder
		}
		if _, err := tx.Exec(`UPDATE workouts SET training_day = ? WHERE id = ?`,
			rec.In(tz).Format("2006-01-02"), r.id); err != nil {
			return fmt.Errorf("backfill training_day for %s: %w", r.id, err)
		}
	}

	// Re-simplify legacy route_coords from the stored GPS streams.
	var stale []string
	rows, err = tx.Query(`SELECT id FROM workouts
		WHERE is_indoor = 0 AND (route_coords_v IS NULL OR route_coords_v < 2)`)
	if err != nil {
		return fmt.Errorf("scan for legacy route_coords: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		stale = append(stale, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range stale {
		pts, err := gpsPointsTx(tx, id)
		if err != nil {
			return fmt.Errorf("read gps points for %s: %w", id, err)
		}
		js, _ := json.Marshal(simplifyCoords(pts, 500))
		if _, err := tx.Exec(`UPDATE workouts SET route_coords = ?, route_coords_v = 2 WHERE id = ?`,
			string(js), id); err != nil {
			return fmt.Errorf("rebuild route_coords for %s: %w", id, err)
		}
	}
	return nil
}

// gpsPointsTx reads a workout's ordered GPS track. Migration-local by design —
// see the "migration owns its code" rule on migrations.
func gpsPointsTx(tx *sql.Tx, workoutID string) ([][2]float64, error) {
	rows, err := tx.Query(`
		SELECT lng, lat FROM workout_streams
		WHERE workout_id = ?
		  AND lat IS NOT NULL AND lng IS NOT NULL
		  AND NOT (lat = 0 AND lng = 0)
		ORDER BY timestamp`, workoutID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var pts [][2]float64
	for rows.Next() {
		var lng, lat float64
		if err := rows.Scan(&lng, &lat); err != nil {
			return nil, err
		}
		pts = append(pts, [2]float64{lng, lat})
	}
	return pts, rows.Err()
}
