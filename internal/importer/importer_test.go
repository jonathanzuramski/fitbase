package importer_test

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/importer"
	"github.com/fitbase/fitbase/internal/models"

	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/proto"
)

// testKey is a fixed 32-byte key used only in tests.
var testKey = []byte("fitbase-test-key-do-not-use-prod")

func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"), testKey)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// buildMinimalFIT produces a minimal valid FIT activity binary.
func buildMinimalFIT(t *testing.T) []byte {
	return buildFITAt(t, time.Date(2024, 3, 15, 8, 0, 0, 0, time.UTC))
}

// buildFITAt produces a minimal valid FIT activity binary starting at start —
// distinct start times yield distinct workouts (different IDs, no dedup).
func buildFITAt(t *testing.T, start time.Time) []byte {
	t.Helper()

	fileId := mesgdef.NewFileId(nil)
	fileId.Type = typedef.FileActivity
	fileId.Manufacturer = typedef.ManufacturerGarmin
	fileId.TimeCreated = start

	session := mesgdef.NewSession(nil)
	session.Sport = typedef.SportCycling
	session.StartTime = start
	session.Timestamp = start.Add(3600 * time.Second)
	session.Event = typedef.EventSession
	session.EventType = typedef.EventTypeStop
	session.TotalTimerTime = 3600 * 1000
	session.TotalDistance = 36000 * 100

	activity := mesgdef.NewActivity(nil)
	activity.Timestamp = session.Timestamp
	activity.NumSessions = 1
	activity.Type = typedef.ActivityManual
	activity.Event = typedef.EventActivity
	activity.EventType = typedef.EventTypeStop

	fit := proto.FIT{
		Messages: []proto.Message{
			fileId.ToMesg(nil),
			session.ToMesg(nil),
			activity.ToMesg(nil),
		},
	}

	var buf bytes.Buffer
	if err := encoder.New(&buf).Encode(&fit); err != nil {
		t.Fatalf("encode test FIT: %v", err)
	}
	return buf.Bytes()
}

// ── ArchivePath ───────────────────────────────────────────────────────────────

func TestArchivePath(t *testing.T) {
	d := newTestDB(t)
	imp := importer.NewImporter(d, "/archive")

	w := &models.Workout{
		Sport:      "cycling",
		RecordedAt: time.Date(2024, 6, 15, 8, 30, 0, 0, time.UTC),
	}
	got := imp.ArchivePath(w)
	want := filepath.Join("/archive", "2024", "06", "2024-06-15T08-30-00-cycling.fit")
	if got != want {
		t.Errorf("ArchivePath: got %q want %q", got, want)
	}
}

func TestArchivePath_UsesUTC(t *testing.T) {
	d := newTestDB(t)
	imp := importer.NewImporter(d, "/arc")

	// Workout recorded in a non-UTC zone: Jan 31 11pm ET = Feb 1 04:00 UTC
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("timezone data not available")
	}
	w := &models.Workout{
		ID:         "testid0000000000",
		RecordedAt: time.Date(2024, 1, 31, 23, 0, 0, 0, loc),
	}
	path := imp.ArchivePath(w)
	// UTC month should be "02" (February)
	month := filepath.Base(filepath.Dir(path))
	if month != "02" {
		t.Errorf("expected month 02 (UTC), got %q (full path: %q)", month, path)
	}
}

// ── ImportBytes ───────────────────────────────────────────────────────────────

func TestImportBytes_ValidFIT(t *testing.T) {
	d := newTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())

	data := buildMinimalFIT(t)
	id, err := imp.ImportBytes(data, "test.fit")
	if err != nil {
		t.Fatalf("ImportBytes: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty workout ID")
	}
	if len(id) != 16 {
		t.Errorf("expected 16-char ID, got %d chars: %q", len(id), id)
	}

	w, err := d.GetWorkout(id)
	if err != nil || w == nil {
		t.Fatalf("workout not stored in DB after import: %v", err)
	}
	if w.Sport != "cycling" {
		t.Errorf("Sport: got %q want cycling", w.Sport)
	}
	if w.DurationSecs != 3600 {
		t.Errorf("DurationSecs: got %d want 3600", w.DurationSecs)
	}
}

func TestImportBytes_Deduplication(t *testing.T) {
	d := newTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	data := buildMinimalFIT(t)

	id1, err := imp.ImportBytes(data, "ride.fit")
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if id1 == "" {
		t.Fatal("first import should return a non-empty ID")
	}

	// Same bytes, different filename → should be silently skipped
	id2, err := imp.ImportBytes(data, "ride_copy.fit")
	if err != nil {
		t.Fatalf("second import error: %v", err)
	}
	if id2 != "" {
		t.Errorf("duplicate import should return empty ID, got %q", id2)
	}
}

func TestImportBytes_InvalidData(t *testing.T) {
	d := newTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())

	_, err := imp.ImportBytes([]byte("not a fit file at all"), "bad.fit")
	if err == nil {
		t.Error("expected error for invalid FIT data")
	}
}

func TestImportBytes_EmptyData(t *testing.T) {
	d := newTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())

	_, err := imp.ImportBytes([]byte{}, "empty.fit")
	if err == nil {
		t.Error("expected error for empty FIT data")
	}
}

func TestImportBytes_ArchivesFile(t *testing.T) {
	archiveDir := t.TempDir()
	d := newTestDB(t)
	imp := importer.NewImporter(d, archiveDir)
	data := buildMinimalFIT(t)

	id, err := imp.ImportBytes(data, "ride.fit")
	if err != nil {
		t.Fatal(err)
	}

	w, _ := d.GetWorkout(id)
	archivePath := imp.ArchivePath(w)

	if _, err := os.Stat(archivePath); os.IsNotExist(err) {
		t.Errorf("archived file not found at %q", archivePath)
	}
}

func TestImportBytes_DifferentFiles_DifferentIDs(t *testing.T) {
	d := newTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())

	data1 := buildMinimalFIT(t)

	// Slightly different FIT: different session power value
	start := time.Date(2024, 3, 16, 8, 0, 0, 0, time.UTC)
	fileId := mesgdef.NewFileId(nil)
	fileId.Type = typedef.FileActivity
	fileId.TimeCreated = start
	session := mesgdef.NewSession(nil)
	session.Sport = typedef.SportCycling
	session.StartTime = start
	session.Timestamp = start.Add(3600 * time.Second)
	session.Event = typedef.EventSession
	session.EventType = typedef.EventTypeStop
	session.TotalTimerTime = 3600 * 1000
	session.TotalDistance = 50000 * 100 // different distance
	activity := mesgdef.NewActivity(nil)
	activity.Timestamp = session.Timestamp
	activity.NumSessions = 1
	activity.Type = typedef.ActivityManual
	activity.Event = typedef.EventActivity
	activity.EventType = typedef.EventTypeStop
	fit := proto.FIT{Messages: []proto.Message{fileId.ToMesg(nil), session.ToMesg(nil), activity.ToMesg(nil)}}
	var buf bytes.Buffer
	_ = encoder.New(&buf).Encode(&fit)
	data2 := buf.Bytes()

	id1, err := imp.ImportBytes(data1, "ride1.fit")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := imp.ImportBytes(data2, "ride2.fit")
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Error("different FIT files should produce different IDs")
	}
}

// ── Sweet Spot backfill ───────────────────────────────────────────────────────

func TestBackfillSweetSpotZone(t *testing.T) {
	d := newTestDB(t)
	base := time.Date(2024, 3, 15, 8, 0, 0, 0, time.UTC)
	w := &models.Workout{
		ID:           "backfillss000001",
		Filename:     "backfill.fit",
		RecordedAt:   base,
		Sport:        "cycling",
		DurationSecs: 3,
		CreatedAt:    time.Now().UTC(),
	}
	mk := func(off, watts int) models.Stream {
		p := watts
		return models.Stream{Timestamp: base.Add(time.Duration(off) * time.Second), PowerWatts: &p}
	}
	// Athlete FTP defaults to 250 → SS band 220–235. 225 & 230 in-band, 240 out.
	streams := []models.Stream{mk(0, 225), mk(1, 230), mk(2, 240)}
	if err := d.InsertWorkout(w, streams); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}
	// Legacy zone-times row predating ss_secs (stored NULL).
	if err := d.InsertZoneTimes(w.ID, [7]int{}, [5]int{}, -1); err != nil {
		t.Fatalf("InsertZoneTimes: %v", err)
	}

	imp := importer.NewImporter(d, t.TempDir())
	imp.BackfillSweetSpotZone()

	_, _, ss, err := d.GetZoneTimes(w.ID)
	if err != nil {
		t.Fatalf("GetZoneTimes: %v", err)
	}
	if ss == nil {
		t.Fatal("ss_secs still NULL after backfill")
	}
	if *ss != 2 {
		t.Errorf("ss = %d, want 2 (225w, 230w in band; 240w out)", *ss)
	}

	// Backfill must converge: nothing left needing it on a second pass.
	ids, err := d.WorkoutIDsWithoutSSZone()
	if err != nil {
		t.Fatalf("WorkoutIDsWithoutSSZone: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected no rows needing backfill, got %v", ids)
	}
}

func TestBackfillSweetSpotZone_NoFTPStoresZero(t *testing.T) {
	d := newTestDB(t)
	// Zero the athlete FTP so SweetSpotBand returns (0,0) at backfill time.
	a, err := d.GetAthlete()
	if err != nil {
		t.Fatalf("GetAthlete: %v", err)
	}
	a.FTPWatts = 0
	if err := d.UpdateAthlete(a); err != nil {
		t.Fatalf("UpdateAthlete: %v", err)
	}

	w := &models.Workout{
		ID:           "backfillss000002",
		Filename:     "backfill2.fit",
		RecordedAt:   time.Date(2024, 3, 15, 8, 0, 0, 0, time.UTC),
		Sport:        "cycling",
		DurationSecs: 1,
		CreatedAt:    time.Now().UTC(),
	}
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatalf("InsertWorkout: %v", err)
	}
	if err := d.InsertZoneTimes(w.ID, [7]int{}, [5]int{}, -1); err != nil {
		t.Fatalf("InsertZoneTimes: %v", err)
	}

	imp := importer.NewImporter(d, t.TempDir())
	imp.BackfillSweetSpotZone()

	// With no FTP, SS is stored as 0 (not left NULL) so it isn't retried forever.
	_, _, ss, err := d.GetZoneTimes(w.ID)
	if err != nil {
		t.Fatalf("GetZoneTimes: %v", err)
	}
	if ss == nil || *ss != 0 {
		t.Errorf("ss = %v, want 0 when FTP is unknown", ss)
	}
}

// ── Watch dir as inbox ────────────────────────────────────────────────────────

// Every handled watch file must be removed — imported or skipped — so nothing
// sits in the watch dir getting silently rescanned forever.
func TestImport_RemovesWatchFileOnImportAndOnDuplicate(t *testing.T) {
	d := newTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	watchDir := t.TempDir()
	fit := buildMinimalFIT(t)

	// First drop: imported, watch file removed.
	path := filepath.Join(watchDir, "ride.fit")
	if err := os.WriteFile(path, fit, 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := imp.Import(path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if id == "" {
		t.Fatal("expected a workout id on first import")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("watch file not removed after successful import")
	}

	// Second drop of the same bytes: skipped as duplicate, but still removed.
	if err := os.WriteFile(path, fit, 0o644); err != nil {
		t.Fatal(err)
	}
	if id, err := imp.Import(path); err != nil || id != "" {
		t.Fatalf("duplicate import = (%q, %v), want skip", id, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("watch file not removed after duplicate skip")
	}
	if n, _ := d.CountWorkouts(); n != 1 {
		t.Errorf("workout count = %d, want 1", n)
	}
}

// ── Delete then re-import ─────────────────────────────────────────────────────

// Deleting a workout must make it re-importable: the ledger entries and the
// archived FIT file go with it, so the same bytes import as brand new (and a
// future archive rebuild can't resurrect the deleted workout).
func TestDeleteWorkout_AllowsReimport(t *testing.T) {
	archiveDir := t.TempDir()
	d := newTestDB(t)
	imp := importer.NewImporter(d, archiveDir)
	fit := buildMinimalFIT(t)

	id, err := imp.ImportBytes(fit, "ride.fit")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	w, _ := d.GetWorkout(id)
	archivePath := imp.ArchivePath(w)
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("setup: archive file missing: %v", err)
	}

	if err := imp.DeleteWorkout(id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n, _ := d.CountWorkouts(); n != 0 {
		t.Errorf("workout count after delete = %d, want 0", n)
	}
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Error("archived FIT file not removed on delete (a rebuild would resurrect it)")
	}

	// Same bytes again: must import as brand new, not be skipped by the ledger.
	id2, err := imp.ImportBytes(fit, "ride.fit")
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if id2 == "" {
		t.Fatal("re-import was skipped; deleted workout is not re-importable")
	}
	if n, _ := d.CountWorkouts(); n != 1 {
		t.Errorf("workout count after re-import = %d, want 1", n)
	}
}

func TestDeleteWorkout_MissingReturnsNoRows(t *testing.T) {
	d := newTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	if err := imp.DeleteWorkout("nope000000000000"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("delete missing = %v, want sql.ErrNoRows", err)
	}
}

// ── Hardened archive ──────────────────────────────────────────────────────────

func TestImportBytes_ArchiveFailureAbortsImport(t *testing.T) {
	d := newTestDB(t)
	// Point the archive dir at a regular file, so creating the year/month
	// subdirectories underneath it fails — simulating an unwritable archive.
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	imp := importer.NewImporter(d, blocker)

	_, err := imp.ImportBytes(buildMinimalFIT(t), "ride.fit")
	if err == nil {
		t.Fatal("expected import to fail when the archive write fails")
	}
	// The workout must not be persisted if it couldn't be archived: the archive
	// is the source of truth for rebuilds.
	if n, _ := d.CountWorkouts(); n != 0 {
		t.Errorf("workout persisted despite archive failure: count=%d", n)
	}
}

// ── RebuildFromArchive ────────────────────────────────────────────────────────

func TestRebuildFromArchive_RestoresFromArchive(t *testing.T) {
	archiveDir := t.TempDir()
	d := newTestDB(t)
	imp := importer.NewImporter(d, archiveDir)

	id, err := imp.ImportBytes(buildMinimalFIT(t), "ride.fit")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n, _ := d.CountWorkouts(); n != 1 {
		t.Fatalf("setup: want 1 workout, got %d", n)
	}

	imported, gap, err := imp.RebuildFromArchive()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if gap != 0 {
		t.Errorf("coverage gap = %d, want 0 (archive intact)", gap)
	}
	if imported != 1 {
		t.Errorf("imported = %d, want 1", imported)
	}
	if w, _ := d.GetWorkout(id); w == nil {
		t.Errorf("workout %s not restored after rebuild", id)
	}
}

func TestRebuildFromArchive_ReportsCoverageGap(t *testing.T) {
	archiveDir := t.TempDir()
	d := newTestDB(t)
	imp := importer.NewImporter(d, archiveDir)

	// Two distinct workouts so a partial gap (1 of 2) doesn't trip the
	// total-gap guard — that refusal path has its own test below.
	id1, err := imp.ImportBytes(buildFITAt(t, time.Date(2024, 3, 15, 8, 0, 0, 0, time.UTC)), "ride1.fit")
	if err != nil {
		t.Fatalf("import 1: %v", err)
	}
	id2, err := imp.ImportBytes(buildFITAt(t, time.Date(2024, 3, 16, 9, 0, 0, 0, time.UTC)), "ride2.fit")
	if err != nil {
		t.Fatalf("import 2: %v", err)
	}
	// Delete one archived file so that workout can't be rebuilt.
	w1, _ := d.GetWorkout(id1)
	if err := os.Remove(imp.ArchivePath(w1)); err != nil {
		t.Fatalf("remove archive file: %v", err)
	}

	imported, gap, err := imp.RebuildFromArchive()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if gap != 1 {
		t.Errorf("coverage gap = %d, want 1", gap)
	}
	if imported != 1 {
		t.Errorf("imported = %d, want 1 (only ride2 remains in the archive)", imported)
	}
	if w, _ := d.GetWorkout(id2); w == nil {
		t.Errorf("covered workout %s not restored", id2)
	}
	if w, _ := d.GetWorkout(id1); w != nil {
		t.Errorf("uncovered workout %s still present, want dropped pending resync", id1)
	}
	// The gap is surfaced for the UI to prompt a resync.
	if st := imp.ReimportStatus(); st.ResyncGap != 1 {
		t.Errorf("ReimportStatus.ResyncGap = %d, want 1", st.ResyncGap)
	}
}

func TestRebuildFromArchive_RefusesTotalGap(t *testing.T) {
	archiveDir := t.TempDir()
	d := newTestDB(t)
	imp := importer.NewImporter(d, archiveDir)

	id, err := imp.ImportBytes(buildMinimalFIT(t), "ride.fit")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	// Every workout missing from the archive looks like an unmounted archive
	// dir, not genuinely lost files — the rebuild must refuse to wipe.
	w, _ := d.GetWorkout(id)
	if err := os.Remove(imp.ArchivePath(w)); err != nil {
		t.Fatalf("remove archive file: %v", err)
	}

	if _, _, err := imp.RebuildFromArchive(); err == nil {
		t.Fatal("expected rebuild to refuse when the archive covers no workouts")
	}
	if n, _ := d.CountWorkouts(); n != 1 {
		t.Errorf("workout count = %d, want 1 (database untouched by refused rebuild)", n)
	}
}
