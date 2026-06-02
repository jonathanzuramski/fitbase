CREATE TABLE IF NOT EXISTS workouts (
    id                    TEXT PRIMARY KEY,
    filename              TEXT NOT NULL,
    recorded_at           DATETIME NOT NULL,
    sport                 TEXT NOT NULL DEFAULT 'cycling',
    duration_secs         INTEGER NOT NULL,
    elapsed_secs          INTEGER NOT NULL DEFAULT 0,
    distance_meters       REAL NOT NULL DEFAULT 0,
    elevation_gain_meters REAL NOT NULL DEFAULT 0,
    avg_power_watts       REAL,
    max_power_watts       REAL,
    normalized_power      REAL,
    avg_heart_rate        INTEGER,
    max_heart_rate        INTEGER,
    avg_cadence           INTEGER,
    avg_speed_mps         REAL NOT NULL DEFAULT 0,
    tss                   REAL,
    intensity_factor      REAL,
    is_indoor             INTEGER NOT NULL DEFAULT 0,
    route_id              TEXT DEFAULT NULL,
    created_at            DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE TABLE IF NOT EXISTS workout_streams (
    workout_id       TEXT NOT NULL REFERENCES workouts(id) ON DELETE CASCADE,
    timestamp        DATETIME NOT NULL,
    power_watts      INTEGER,
    heart_rate_bpm   INTEGER,
    cadence_rpm      INTEGER,
    speed_mps        REAL,
    altitude_meters  REAL,
    lat              REAL,
    lng              REAL,
    distance_meters  REAL,
    PRIMARY KEY (workout_id, timestamp)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_workouts_recorded_at ON workouts(recorded_at DESC);
CREATE INDEX IF NOT EXISTS idx_workouts_is_indoor_recorded_at ON workouts(is_indoor, recorded_at DESC);
CREATE INDEX IF NOT EXISTS idx_workouts_route_id ON workouts(route_id);
CREATE INDEX IF NOT EXISTS idx_workouts_sport_recorded_at ON workouts(sport, recorded_at);

CREATE TABLE IF NOT EXISTS athlete (
    id             INTEGER PRIMARY KEY DEFAULT 1,
    ftp_watts      INTEGER NOT NULL DEFAULT 250,
    weight_kg      REAL NOT NULL DEFAULT 70.0,
    threshold_hr   INTEGER NOT NULL DEFAULT 0,
    max_hr         INTEGER NOT NULL DEFAULT 0,
    resting_hr     INTEGER NOT NULL DEFAULT 0,
    age            INTEGER NOT NULL DEFAULT 0,
    location       TEXT NOT NULL DEFAULT '',
    language       TEXT NOT NULL DEFAULT 'en',
    timezone       TEXT NOT NULL DEFAULT 'UTC',
    units          TEXT NOT NULL DEFAULT 'imperial',
    setup_complete INTEGER NOT NULL DEFAULT 0,
    hr_zones_json  TEXT NOT NULL DEFAULT '',
    updated_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- Seed default athlete row
INSERT OR IGNORE INTO athlete (id, ftp_watts, weight_kg) VALUES (1, 250, 70.0);

-- Composite PK on (hash, filename) so the same content imported under multiple
-- names (e.g. local archive name + intervals.icu activity name) is recorded
-- under each name. Filename-based dedup needs every name present.
CREATE TABLE IF NOT EXISTS imported_files (
    hash       TEXT NOT NULL,
    filename   TEXT NOT NULL,
    imported_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    PRIMARY KEY (hash, filename)
);
CREATE INDEX IF NOT EXISTS idx_imported_files_filename ON imported_files(filename);

-- Best average power for each effort duration, one row per (workout, duration).
-- Used to render the all-time power curve overlay.
CREATE TABLE IF NOT EXISTS workout_power_curve (
    workout_id    TEXT    NOT NULL REFERENCES workouts(id) ON DELETE CASCADE,
    duration_secs INTEGER NOT NULL,
    watts         INTEGER NOT NULL,
    PRIMARY KEY (workout_id, duration_secs)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_power_curve_duration_watts ON workout_power_curve(duration_secs, watts DESC);

-- Stores OAuth tokens and config for optional integrations (e.g. gdrive).
CREATE TABLE IF NOT EXISTS integrations (
    name        TEXT PRIMARY KEY,
    token_json  TEXT NOT NULL DEFAULT '',
    cursor      TEXT NOT NULL DEFAULT '',
    longpoll    INTEGER NOT NULL DEFAULT 0,
    updated_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- Stores OAuth app credentials (client ID + secret) entered via the settings UI.
-- Both fields are AES-256-GCM encrypted with the master key.
CREATE TABLE IF NOT EXISTS integration_credentials (
    name          TEXT PRIMARY KEY,
    client_id     TEXT NOT NULL DEFAULT '',
    client_secret TEXT NOT NULL DEFAULT '',
    updated_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- FTP change history. One row per change; effective_from marks when it took effect.
-- Seeded on startup from athlete.ftp_watts if the table is empty.
CREATE TABLE IF NOT EXISTS ftp_history (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    ftp_watts      INTEGER NOT NULL,
    effective_from DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_ftp_history_effective_from ON ftp_history(effective_from DESC);

-- Pre-computed time spent in each power zone (7 zones) and HR zone (5 zones).
-- Stored as JSON int arrays, e.g. [120,3600,900,300,0,0,0].
-- Computed at import time using FTP and threshold HR in effect at that moment.
-- ss_secs is the time in the Sweet Spot reference band (88–94% FTP). It overlaps
-- Z3/Z4 by design, so it's stored separately rather than as part of the 7-zone
-- partition. NULL means not yet computed (legacy rows are backfilled on boot).
CREATE TABLE IF NOT EXISTS workout_zone_times (
    workout_id TEXT PRIMARY KEY REFERENCES workouts(id) ON DELETE CASCADE,
    power_secs TEXT NOT NULL DEFAULT '[]',
    hr_secs    TEXT NOT NULL DEFAULT '[]',
    ss_secs    INTEGER
);

-- Per-sport mileage goals. One row per sport; upserted on save.
CREATE TABLE IF NOT EXISTS mileage_goals (
    sport          TEXT PRIMARY KEY,
    weekly_meters  REAL NOT NULL DEFAULT 0,
    yearly_meters  REAL NOT NULL DEFAULT 0,
    updated_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- GPS route fingerprints for route matching.
CREATE TABLE IF NOT EXISTS routes (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL DEFAULT '',
    cells      TEXT NOT NULL,
    cell_count INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- AI coach configuration: provider, model selection, and AES-256-GCM encrypted API key.
CREATE TABLE IF NOT EXISTS ai_settings (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    provider   TEXT NOT NULL DEFAULT '',
    model      TEXT NOT NULL DEFAULT '',
    api_key    TEXT NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
INSERT OR IGNORE INTO ai_settings (id) VALUES (1);

-- Most recent AI coaching response (Markdown). Single row — regenerating overwrites it.
-- We cache so the homepage can show insights instantly without re-calling the LLM.
-- _v2 suffix because the original shape split prose into three columns; the new
-- streaming markdown output is a single blob. Old table (if present) is left alone.
CREATE TABLE IF NOT EXISTS ai_insights_cache_v2 (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    provider     TEXT NOT NULL DEFAULT '',
    model        TEXT NOT NULL DEFAULT '',
    content      TEXT NOT NULL DEFAULT '',
    generated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
DROP TABLE IF EXISTS ai_insights_cache;

-- Planned (future) workouts shown on the calendar. Independent of the
-- completed `workouts` table — no auto-matching by date in v1.
-- intervals_json is a JSON array of structured steps (warmup/work/recovery/
-- cooldown with target %FTP, watts, or zone); empty string when there are
-- no structured intervals.
CREATE TABLE IF NOT EXISTS planned_workouts (
    id               TEXT PRIMARY KEY,
    planned_date     DATE NOT NULL,
    sport            TEXT NOT NULL DEFAULT 'cycling',
    title            TEXT NOT NULL DEFAULT '',
    description      TEXT NOT NULL DEFAULT '',
    duration_secs    INTEGER NOT NULL DEFAULT 0,
    tss              REAL,
    intensity_factor REAL,
    intervals_json   TEXT NOT NULL DEFAULT '',
    source           TEXT NOT NULL DEFAULT 'manual',   -- 'manual' | 'coach'
    created_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_planned_workouts_date ON planned_workouts(planned_date);

-- Coach-proposed schedules pending user review. The AI tool writes the
-- proposed payload here keyed by a short preview id; the chat UI fetches it
-- to render the preview card and commits it (or discards). Drafts are tiny
-- JSON blobs — periodic cleanup isn't required for v1.
CREATE TABLE IF NOT EXISTS planned_workout_drafts (
    id           TEXT PRIMARY KEY,
    payload_json TEXT NOT NULL,
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
