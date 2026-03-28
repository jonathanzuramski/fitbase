package importer_test

import (
	"bytes"
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
	t.Helper()
	start := time.Date(2024, 3, 15, 8, 0, 0, 0, time.UTC)

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
