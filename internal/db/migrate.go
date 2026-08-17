package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fitbase/fitbase/internal/geo"
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
	migrateAICoach,             // v4 — AI coach settings/cache, schedule drafts, decoupling cache
	migrateRideLocalTime,       // v5 — per-ride UTC offset, start coords, county/state + backfill
	migrateLedgerTombstones,    // v6 — imported_files.deleted flag so deletes survive auto-sync
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
			_ = rows.Close()
			return err
		}
		days = append(days, r)
	}
	_ = rows.Close()
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
			_ = rows.Close()
			return err
		}
		stale = append(stale, id)
	}
	_ = rows.Close()
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

// migrateAICoach (v4) creates the AI-coach feature's tables: provider settings,
// the single-row insights cache, coach-proposed schedule drafts, and the
// per-workout aerobic-decoupling cache. All new objects — IF NOT EXISTS only to
// stay idempotent against a dev database that pre-created them.
func migrateAICoach(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		-- Configured AI provider/model and the encrypted API key. Single row.
		CREATE TABLE IF NOT EXISTS ai_settings (
			id         INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
			provider   TEXT NOT NULL DEFAULT '',
			model      TEXT NOT NULL DEFAULT '',
			api_key    TEXT NOT NULL DEFAULT '',
			updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
		);

		-- Last generated coaching response, persisted for instant display.
		-- Single row; content is the full Markdown blob from the provider.
		CREATE TABLE IF NOT EXISTS ai_insights_cache_v2 (
			id           INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
			provider     TEXT NOT NULL DEFAULT '',
			model        TEXT NOT NULL DEFAULT '',
			content      TEXT NOT NULL DEFAULT '',
			generated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
		);

		-- Coach-proposed schedules awaiting rider review. The coach's
		-- propose_schedule tool writes a draft here; the UI fetches it to render
		-- the preview card and commits or discards it. Abandoned drafts are
		-- swept opportunistically on the next SavePlannedDraft.
		CREATE TABLE IF NOT EXISTS planned_workout_drafts (
			id           TEXT PRIMARY KEY,
			payload_json TEXT NOT NULL,
			created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
		);

		-- Cached aerobic decoupling per workout, filled lazily on first compute
		-- so coach calls don't re-fetch full streams for immutable rides.
		-- A row with NULL decoupling_pct means "computed: not derivable from
		-- this ride's streams"; no row means "not computed yet".
		CREATE TABLE IF NOT EXISTS workout_decoupling (
			workout_id     TEXT PRIMARY KEY REFERENCES workouts(id) ON DELETE CASCADE,
			decoupling_pct REAL
		)`); err != nil {
		return fmt.Errorf("create ai coach tables: %w", err)
	}
	return nil
}

// migrateRideLocalTime (v5) makes each workout self-describing in time and
// place: it adds utc_offset_secs (the UTC offset where/when the ride started),
// start_lat/start_lng (first GPS fix), county/state (US Census lookup), and
// geo_v (lookup-data version stamp), then backfills them all in place from
// data already in the database.
//
// Offsets are resolved from the ride's own GPS position, so historical travel
// rides and DST both come out correct; rides without GPS (indoor) fall back to
// the athlete profile timezone evaluated at the ride's date. training_day is
// then re-derived from the per-ride offset, converging rows that were stamped
// with whatever the profile timezone happened to be at insert time.
//
// Calling the live geo package from a migration is deliberate — the v3 /
// simplifyCoords precedent: a future dataset or lookup change ships its own
// migration that re-converges rows whose geo_v is below the new version. The
// "not yet converged" predicate here is geo_v IS NULL, never the live const.
func migrateRideLocalTime(tx *sql.Tx) error {
	addColumn := func(col, def string) error {
		_, err := tx.Exec(fmt.Sprintf("ALTER TABLE workouts ADD COLUMN %s %s", col, def))
		if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("add workouts.%s: %w", col, err)
		}
		return nil
	}
	for _, c := range []struct{ col, def string }{
		{"utc_offset_secs", "INTEGER"},
		{"start_lat", "REAL"},
		{"start_lng", "REAL"},
		{"county", "TEXT"},
		{"state", "TEXT"},
		{"geo_v", "INTEGER"},
	} {
		if err := addColumn(c.col, c.def); err != nil {
			return err
		}
	}

	// Fallback timezone for GPS-less rides: the athlete profile, same policy
	// InsertWorkout used when these rows were written.
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

	type wrow struct {
		id, rec string
		indoor  bool
	}
	var work []wrow
	rows, err := tx.Query(`SELECT id, recorded_at, is_indoor FROM workouts WHERE geo_v IS NULL`)
	if err != nil {
		return fmt.Errorf("scan for unconverged workouts: %w", err)
	}
	for rows.Next() {
		var r wrow
		if err := rows.Scan(&r.id, &r.rec, &r.indoor); err != nil {
			_ = rows.Close()
			return err
		}
		work = append(work, r)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, r := range work {
		rec, err := time.Parse(time.RFC3339, r.rec)
		if err != nil {
			continue // unparseable timestamp: leave untouched rather than fail the ladder
		}

		var startLat, startLng any
		var county, state any
		offset, haveOffset := 0, false

		// Indoor rides are excluded from GPS resolution entirely: virtual
		// platforms record in-game coordinates (Zwift's Watopia sits in the
		// Solomon Islands), which would yield an absurd timezone and place.
		if !r.indoor {
			if lat, lng, ok := firstGPSFixTx(tx, r.id); ok {
				startLat, startLng = lat, lng
				if secs, ok := geo.UTCOffsetAt(lat, lng, rec); ok {
					offset, haveOffset = secs, true
				}
				if c, s, ok := geo.CountyState(lat, lng); ok {
					county, state = c, s
				}
			}
		}
		if !haveOffset {
			_, offset = rec.In(tz).Zone()
		}

		trainingDay := rec.In(time.FixedZone("", offset)).Format("2006-01-02")
		if _, err := tx.Exec(`
			UPDATE workouts
			SET utc_offset_secs = ?, start_lat = ?, start_lng = ?,
			    county = ?, state = ?, training_day = ?, geo_v = 1
			WHERE id = ?`,
			offset, startLat, startLng, county, state, trainingDay, r.id); err != nil {
			return fmt.Errorf("backfill ride-local time for %s: %w", r.id, err)
		}
	}
	return nil
}

// firstGPSFixTx returns a workout's first valid GPS coordinate, or ok=false
// if it has none. Migration-local by design — see the "migration owns its
// code" rule on migrations.
func firstGPSFixTx(tx *sql.Tx, workoutID string) (lat, lng float64, ok bool) {
	err := tx.QueryRow(`
		SELECT lat, lng FROM workout_streams
		WHERE workout_id = ?
		  AND lat IS NOT NULL AND lng IS NOT NULL
		  AND NOT (lat = 0 AND lng = 0)
		ORDER BY timestamp LIMIT 1`, workoutID).Scan(&lat, &lng)
	if err != nil {
		return 0, 0, false
	}
	return lat, lng, true
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

// migrateLedgerTombstones (v6) adds a deleted flag to the import ledger.
// Deleting a workout used to erase its ledger rows so the file could be
// deliberately re-imported — but the auto-syncers treat "filename not in the
// ledger" as "new remote file", so a deleted ride still present at the source
// (intervals.icu, Dropbox) resurrected within one poll cycle. Tombstoned rows
// keep filename-level dedup intact (syncers skip the file forever) while
// hash-level checks ignore them (an explicit re-import still works and
// revives the row).
func migrateLedgerTombstones(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE imported_files ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0`)
	if err != nil && strings.Contains(err.Error(), "duplicate column name") {
		// Already present — a test (or recovery) rewound user_version and
		// replayed the ladder over a database that had run v6 before.
		return nil
	}
	return err
}
