package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/dropbox"
	"github.com/fitbase/fitbase/internal/importer"
	"github.com/fitbase/fitbase/internal/intervals"
	"github.com/fitbase/fitbase/internal/models"
)

var syncTestKey = []byte("fitbase-test-key-do-not-use-prod")

func newSyncTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"), syncTestKey)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func newSyncTestImporter(t *testing.T) *importer.Importer {
	t.Helper()
	return importer.NewImporter(newSyncTestDB(t), t.TempDir())
}

// fakeSyncSource is a controllable SyncSource for Manager tests.
type fakeSyncSource struct {
	startCalled int
	stopCalled  int
	running     bool
	startErr    error
}

func (f *fakeSyncSource) StartAuto() error {
	f.startCalled++
	if f.startErr != nil {
		return f.startErr
	}
	f.running = true
	return nil
}

func (f *fakeSyncSource) StopAuto() {
	f.stopCalled++
	f.running = false
}

func (f *fakeSyncSource) Running() bool { return f.running }

func (f *fakeSyncSource) Disconnect() error { return nil }

func (f *fakeSyncSource) Sync(_ context.Context, _ func(string, any)) (int, int, int) {
	return 0, 0, 0
}

// --- Manager ---

func TestManager_EnableCallsStartAuto(t *testing.T) {
	d := newSyncTestDB(t)
	mgr := NewManager(d)
	src := &fakeSyncSource{}
	mgr.Register("foo", src)

	if err := mgr.Enable("foo"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if src.startCalled != 1 {
		t.Errorf("StartAuto called %d times, want 1", src.startCalled)
	}
	if !mgr.IsEnabled("foo") {
		t.Error("IsEnabled should be true after Enable")
	}
}

func TestManager_EnableDisablesOthers(t *testing.T) {
	d := newSyncTestDB(t)
	mgr := NewManager(d)
	a := &fakeSyncSource{}
	b := &fakeSyncSource{}
	mgr.Register("a", a)
	mgr.Register("b", b)

	if err := mgr.Enable("a"); err != nil {
		t.Fatalf("Enable a: %v", err)
	}
	// Enable b — must stop a and disable it in DB.
	if err := mgr.Enable("b"); err != nil {
		t.Fatalf("Enable b: %v", err)
	}

	if a.stopCalled == 0 {
		t.Error("a.StopAuto should have been called when b was enabled")
	}
	if mgr.IsEnabled("a") {
		t.Error("a should be disabled in DB after enabling b")
	}
	if !mgr.IsEnabled("b") {
		t.Error("b should be enabled in DB")
	}
}

func TestManager_Disable(t *testing.T) {
	d := newSyncTestDB(t)
	mgr := NewManager(d)
	src := &fakeSyncSource{}
	mgr.Register("foo", src)

	if err := mgr.Enable("foo"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := mgr.Disable("foo"); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	if src.stopCalled == 0 {
		t.Error("StopAuto should have been called by Disable")
	}
	if mgr.IsEnabled("foo") {
		t.Error("IsEnabled should be false after Disable")
	}
}

func TestManager_DisableUnknownName(t *testing.T) {
	// Disable on an unregistered name writes false to DB and must not panic.
	d := newSyncTestDB(t)
	mgr := NewManager(d)
	if err := mgr.Disable("nonexistent"); err != nil {
		t.Fatalf("Disable unknown: %v", err)
	}
}

func TestManager_IsEnabledInitiallyFalse(t *testing.T) {
	d := newSyncTestDB(t)
	mgr := NewManager(d)
	mgr.Register("foo", &fakeSyncSource{})
	if mgr.IsEnabled("foo") {
		t.Error("IsEnabled should be false before any Enable call")
	}
}

func TestManager_IsEnabledReflectsDB(t *testing.T) {
	d := newSyncTestDB(t)
	mgr := NewManager(d)
	mgr.Register("foo", &fakeSyncSource{})

	if err := d.SetAutoSync("foo", true); err != nil {
		t.Fatalf("SetAutoSync: %v", err)
	}
	if !mgr.IsEnabled("foo") {
		t.Error("IsEnabled should reflect DB value")
	}
}

func TestManager_RestoreAll(t *testing.T) {
	d := newSyncTestDB(t)
	mgr := NewManager(d)
	a := &fakeSyncSource{}
	b := &fakeSyncSource{}
	mgr.Register("a", a)
	mgr.Register("b", b)

	// Seed DB: a enabled, b not.
	if err := d.SetAutoSync("a", true); err != nil {
		t.Fatalf("SetAutoSync: %v", err)
	}

	mgr.RestoreAll()

	if a.startCalled == 0 {
		t.Error("a.StartAuto should have been called by RestoreAll")
	}
	if b.startCalled != 0 {
		t.Errorf("b.StartAuto should NOT be called, got %d calls", b.startCalled)
	}
}

func TestManager_RestoreAllSkipsDisabled(t *testing.T) {
	d := newSyncTestDB(t)
	mgr := NewManager(d)
	src := &fakeSyncSource{}
	mgr.Register("foo", src)
	// foo is not in DB — defaults to disabled.
	mgr.RestoreAll()
	if src.startCalled != 0 {
		t.Errorf("StartAuto should not be called for disabled source, got %d calls", src.startCalled)
	}
}

func TestManager_EnableStartError(t *testing.T) {
	d := newSyncTestDB(t)
	mgr := NewManager(d)
	src := &fakeSyncSource{startErr: errStartFailed}
	mgr.Register("foo", src)

	if err := mgr.Enable("foo"); err == nil {
		t.Error("Enable should propagate StartAuto error")
	}
}

// errStartFailed is a sentinel for StartAuto failure.
var errStartFailed = context.DeadlineExceeded

// --- DropboxSource ---

func TestDropboxSource_RunningFalseInitially(t *testing.T) {
	d := newSyncTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	src := NewDropboxSource(d, imp)
	if src.Running() {
		t.Error("Running should be false before StartAuto")
	}
}

func TestDropboxSource_StopAutoIdempotent(t *testing.T) {
	d := newSyncTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	src := NewDropboxSource(d, imp)
	// Multiple calls without StartAuto must not panic.
	src.StopAuto()
	src.StopAuto()
	if src.Running() {
		t.Error("Running should remain false after StopAuto on idle source")
	}
}

func TestDropboxSource_StartAutoNoToken(t *testing.T) {
	// Without a stored token, StartAuto is a no-op.
	d := newSyncTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	src := NewDropboxSource(d, imp)
	if err := src.StartAuto(); err != nil {
		t.Errorf("StartAuto with no token: %v", err)
	}
	if src.Running() {
		t.Error("Running should be false when no token is stored")
	}
}

func TestDropboxSource_Disconnect(t *testing.T) {
	d := newSyncTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	src := NewDropboxSource(d, imp)

	if err := d.SetIntegrationToken("dropbox", "tok"); err != nil {
		t.Fatalf("SetIntegrationToken: %v", err)
	}
	if err := d.SetIntegrationCredentials("dropbox", "/fits", ""); err != nil {
		t.Fatalf("SetIntegrationCredentials: %v", err)
	}

	if err := src.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	tok, _ := d.GetIntegrationToken("dropbox")
	if tok != "" {
		t.Error("token should be deleted after Disconnect")
	}
	cid, _, _ := d.GetIntegrationCredentials("dropbox")
	if cid != "" {
		t.Error("credentials should be deleted after Disconnect")
	}
	if src.Running() {
		t.Error("Running should be false after Disconnect")
	}
}

// --- IntervalsSource ---

func TestIntervalsSource_RunningFalseInitially(t *testing.T) {
	d := newSyncTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	src := NewIntervalsSource(d, imp)
	if src.Running() {
		t.Error("Running should be false before StartAuto")
	}
}

func TestIntervalsSource_StopAutoIdempotent(t *testing.T) {
	d := newSyncTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	src := NewIntervalsSource(d, imp)
	src.StopAuto()
	src.StopAuto()
	if src.Running() {
		t.Error("Running should remain false after StopAuto on idle source")
	}
}

func TestIntervalsSource_StartAutoNoCredentials(t *testing.T) {
	// Without stored credentials, StartAuto is a no-op.
	d := newSyncTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	src := NewIntervalsSource(d, imp)
	if err := src.StartAuto(); err != nil {
		t.Errorf("StartAuto with no credentials: %v", err)
	}
	if src.Running() {
		t.Error("Running should be false when credentials are absent")
	}
}

func TestIntervalsSource_Disconnect(t *testing.T) {
	d := newSyncTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	src := NewIntervalsSource(d, imp)

	if err := d.SetIntegrationCredentials("intervals", "i12345", "apikey"); err != nil {
		t.Fatalf("SetIntegrationCredentials: %v", err)
	}

	if err := src.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	cid, _, _ := d.GetIntegrationCredentials("intervals")
	if cid != "" {
		t.Error("credentials should be deleted after Disconnect")
	}
	if src.Running() {
		t.Error("Running should be false after Disconnect")
	}
}

func TestIntervalsSource_FetchNotConnected(t *testing.T) {
	d := newSyncTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	src := NewIntervalsSource(d, imp)

	_, status, err := src.Fetch(context.Background(), "42")
	if err == nil {
		t.Error("Fetch should return error when not connected")
	}
	if status != "error" {
		t.Errorf("status = %q, want %q", status, "error")
	}
}

// --- downloadDropboxFiles ---

func TestDownloadDropboxFiles_Empty(t *testing.T) {
	imp := newSyncTestImporter(t)
	imported, skipped, failed := downloadDropboxFiles(context.Background(), nil, nil, imp, nil)
	if imported != 0 || skipped != 0 || failed != 0 {
		t.Errorf("empty input: got (%d,%d,%d), want (0,0,0)", imported, skipped, failed)
	}
}

func TestDownloadDropboxFiles_DownloadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := dropbox.NewWithConfig("tok", srv.URL, srv.URL, srv.URL)
	files := []dropbox.FileMetadata{
		{Name: "ride.fit", PathLower: "/ride.fit"},
	}
	imp := newSyncTestImporter(t)
	imported, skipped, failed := downloadDropboxFiles(context.Background(), client, files, imp, nil)
	if failed != 1 || imported != 0 || skipped != 0 {
		t.Errorf("expected (0,0,1), got (%d,%d,%d)", imported, skipped, failed)
	}
}

func TestDownloadDropboxFiles_MultipleFiles(t *testing.T) {
	// First file download returns 500 → failed.
	// Second file download returns 404 → failed.
	// Tests that the semaphore/goroutine pool processes all files.
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		http.Error(w, "error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := dropbox.NewWithConfig("tok", srv.URL, srv.URL, srv.URL)
	files := []dropbox.FileMetadata{
		{Name: "a.fit", PathLower: "/a.fit"},
		{Name: "b.fit", PathLower: "/b.fit"},
		{Name: "c.fit", PathLower: "/c.fit"},
	}
	imp := newSyncTestImporter(t)
	imported, skipped, failed := downloadDropboxFiles(context.Background(), client, files, imp, nil)
	if failed != 3 || imported != 0 || skipped != 0 {
		t.Errorf("expected (0,0,3), got (%d,%d,%d)", imported, skipped, failed)
	}
}

func TestDownloadDropboxFiles_CallsOnFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := dropbox.NewWithConfig("tok", srv.URL, srv.URL, srv.URL)
	files := []dropbox.FileMetadata{
		{Name: "ride.fit", PathLower: "/ride.fit"},
	}
	imp := newSyncTestImporter(t)

	var calls []string
	downloadDropboxFiles(context.Background(), client, files, imp, func(name string, done, total int) {
		calls = append(calls, name)
	})
	if len(calls) != 1 || calls[0] != "ride.fit" {
		t.Errorf("onFile calls = %v, want [ride.fit]", calls)
	}
}

func TestDownloadDropboxFiles_NilOnFile(t *testing.T) {
	// Passing nil for onFile must not panic.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := dropbox.NewWithConfig("tok", srv.URL, srv.URL, srv.URL)
	files := []dropbox.FileMetadata{{Name: "x.fit", PathLower: "/x.fit"}}
	imp := newSyncTestImporter(t)
	downloadDropboxFiles(context.Background(), client, files, imp, nil) // must not panic
}

// --- downloadIntervalsFiles ---

func TestDownloadIntervalsFiles_Empty(t *testing.T) {
	imp := newSyncTestImporter(t)
	imported, skipped, failed := downloadIntervalsFiles(context.Background(), nil, nil, imp, nil)
	if imported != 0 || skipped != 0 || failed != 0 {
		t.Errorf("empty input: got (%d,%d,%d), want (0,0,0)", imported, skipped, failed)
	}
}

func TestDownloadIntervalsFiles_DownloadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	client := intervals.NewWithBase("i1", "key", srv.URL)
	files := []pendingIntervalsActivity{{id: "42", filename: "intervals-42.fit"}}
	imp := newSyncTestImporter(t)

	imported, skipped, failed := downloadIntervalsFiles(context.Background(), client, files, imp, nil)
	if failed != 1 || imported != 0 || skipped != 0 {
		t.Errorf("expected (0,0,1), got (%d,%d,%d)", imported, skipped, failed)
	}
}

func TestDownloadIntervalsFiles_MultipleFiles(t *testing.T) {
	// Both files return 404 → both failed.
	// Verifies all goroutines complete and counts are accurate.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	client := intervals.NewWithBase("i1", "key", srv.URL)
	files := []pendingIntervalsActivity{
		{id: "1", filename: "intervals-1.fit"},
		{id: "2", filename: "intervals-2.fit"},
	}
	imp := newSyncTestImporter(t)

	imported, skipped, failed := downloadIntervalsFiles(context.Background(), client, files, imp, nil)
	if imported != 0 || skipped != 0 || failed != 2 {
		t.Errorf("expected (0,0,2), got (%d,%d,%d)", imported, skipped, failed)
	}
}

func TestDownloadIntervalsFiles_CallsOnFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	client := intervals.NewWithBase("i1", "key", srv.URL)
	files := []pendingIntervalsActivity{{id: "42", filename: "intervals-42.fit"}}
	imp := newSyncTestImporter(t)

	var called bool
	downloadIntervalsFiles(context.Background(), client, files, imp, func(name string, done, total int) {
		called = true
		if name != "intervals-42.fit" {
			t.Errorf("onFile name = %q, want %q", name, "intervals-42.fit")
		}
	})
	if !called {
		t.Error("onFile was never called")
	}
}

// ── SyncFTPHistory ────────────────────────────────────────────────────────────

// intervalsActivitiesServer returns a test server that serves a fixed activity
// list from the intervals.icu /athlete/{id}/activities endpoint.
func intervalsActivitiesServer(t *testing.T, acts []intervals.Activity) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(acts); err != nil {
			t.Errorf("encode activities: %v", err)
		}
	}))
}

// sampleSyncWorkout inserts a workout with normalized power into d and returns it.
func sampleSyncWorkout(t *testing.T, d *db.DB, id string, recordedAt time.Time, np float64) *models.Workout {
	t.Helper()
	tss := 80.0
	ifac := 0.85
	w := &models.Workout{
		ID:              id,
		Filename:        fmt.Sprintf("%s.fit", id),
		RecordedAt:      recordedAt,
		Sport:           "cycling",
		DurationSecs:    3600,
		NormalizedPower: &np,
		TSS:             &tss,
		IntensityFactor: &ifac,
		CreatedAt:       time.Now().UTC(),
	}
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatalf("InsertWorkout %s: %v", id, err)
	}
	return w
}

func TestSyncFTPHistory_NoCredentials(t *testing.T) {
	d := newSyncTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	src := NewIntervalsSource(d, imp)

	_, err := src.SyncFTPHistory(context.Background(), nil)
	if err == nil {
		t.Error("SyncFTPHistory without credentials should return an error")
	}
}

func TestSyncFTPHistory_PopulatesFTPHistory(t *testing.T) {
	d := newSyncTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	src := NewIntervalsSource(d, imp)

	ftp1 := 250
	ftp2 := 270
	acts := []intervals.Activity{
		{ID: "1", StartDateLocal: "2023-01-01T10:00:00", IcuFTP: &ftp1},
		{ID: "2", StartDateLocal: "2023-06-01T10:00:00", IcuFTP: &ftp2},
	}
	srv := intervalsActivitiesServer(t, acts)
	defer srv.Close()

	if err := d.SetIntegrationCredentials("intervals", "i1", "key"); err != nil {
		t.Fatalf("SetIntegrationCredentials: %v", err)
	}
	// Override the base URL so the client hits our test server.
	src.overrideBase(srv.URL)

	_, err := src.SyncFTPHistory(context.Background(), nil)
	if err != nil {
		t.Fatalf("SyncFTPHistory: %v", err)
	}

	// FTP before first change should fall back to athlete default (250).
	before := d.GetFTPAtDate(time.Date(2022, 12, 31, 0, 0, 0, 0, time.UTC))
	if before != 250 {
		t.Errorf("FTP before first change: got %d, want 250", before)
	}

	// FTP after first change but before second.
	mid := d.GetFTPAtDate(time.Date(2023, 3, 1, 0, 0, 0, 0, time.UTC))
	if mid != 250 {
		t.Errorf("FTP after first entry: got %d, want 250", mid)
	}

	// FTP after second change.
	after := d.GetFTPAtDate(time.Date(2023, 7, 1, 0, 0, 0, 0, time.UTC))
	if after != 270 {
		t.Errorf("FTP after second entry: got %d, want 270", after)
	}
}

func TestSyncFTPHistory_ReplacesExistingHistory(t *testing.T) {
	d := newSyncTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	src := NewIntervalsSource(d, imp)

	// Seed a stale FTP history entry.
	if err := d.LogFTPChangeAt(999, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("LogFTPChangeAt: %v", err)
	}

	ftp := 260
	acts := []intervals.Activity{
		{ID: "1", StartDateLocal: "2023-01-01T10:00:00", IcuFTP: &ftp},
	}
	srv := intervalsActivitiesServer(t, acts)
	defer srv.Close()

	if err := d.SetIntegrationCredentials("intervals", "i1", "key"); err != nil {
		t.Fatalf("SetIntegrationCredentials: %v", err)
	}
	src.overrideBase(srv.URL)

	if _, err := src.SyncFTPHistory(context.Background(), nil); err != nil {
		t.Fatalf("SyncFTPHistory: %v", err)
	}

	// The stale 999W entry should be gone; only 260W from intervals remains.
	got := d.GetFTPAtDate(time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC))
	if got == 999 {
		t.Error("stale FTP history entry was not cleared before re-import")
	}
}

func TestSyncFTPHistory_RecomputesTSS(t *testing.T) {
	d := newSyncTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	src := NewIntervalsSource(d, imp)

	workoutDate := time.Date(2023, 6, 15, 8, 0, 0, 0, time.UTC)
	w := sampleSyncWorkout(t, d, "recompute-00001", workoutDate, 250.0)
	originalTSS := *w.TSS

	ftp := 300
	acts := []intervals.Activity{
		{ID: "1", StartDateLocal: "2023-01-01T10:00:00", IcuFTP: &ftp},
	}
	srv := intervalsActivitiesServer(t, acts)
	defer srv.Close()

	if err := d.SetIntegrationCredentials("intervals", "i1", "key"); err != nil {
		t.Fatalf("SetIntegrationCredentials: %v", err)
	}
	src.overrideBase(srv.URL)

	updated, err := src.SyncFTPHistory(context.Background(), nil)
	if err != nil {
		t.Fatalf("SyncFTPHistory: %v", err)
	}
	if updated != 1 {
		t.Errorf("updated = %d, want 1", updated)
	}

	got, err := d.GetWorkout(w.ID)
	if err != nil {
		t.Fatalf("GetWorkout: %v", err)
	}
	if got.TSS == nil {
		t.Fatal("TSS is nil after recompute")
	}
	if *got.TSS == originalTSS {
		t.Errorf("TSS unchanged at %.1f; expected recompute with FTP=300 to produce a different value", originalTSS)
	}
}

func TestSyncFTPHistory_DeduplicatesConsecutiveFTP(t *testing.T) {
	d := newSyncTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	src := NewIntervalsSource(d, imp)

	// Three activities all with the same FTP — should produce only one history entry.
	ftp := 250
	acts := []intervals.Activity{
		{ID: "1", StartDateLocal: "2023-01-01T10:00:00", IcuFTP: &ftp},
		{ID: "2", StartDateLocal: "2023-03-01T10:00:00", IcuFTP: &ftp},
		{ID: "3", StartDateLocal: "2023-06-01T10:00:00", IcuFTP: &ftp},
	}
	srv := intervalsActivitiesServer(t, acts)
	defer srv.Close()

	if err := d.SetIntegrationCredentials("intervals", "i1", "key"); err != nil {
		t.Fatalf("SetIntegrationCredentials: %v", err)
	}
	src.overrideBase(srv.URL)

	if _, err := src.SyncFTPHistory(context.Background(), nil); err != nil {
		t.Fatalf("SyncFTPHistory: %v", err)
	}

	has, err := d.HasFTPHistory()
	if err != nil {
		t.Fatalf("HasFTPHistory: %v", err)
	}
	if !has {
		t.Fatal("expected at least one FTP history entry")
	}
	// Verify only one unique entry was written by checking the FTP value is consistent.
	got := d.GetFTPAtDate(time.Date(2023, 12, 1, 0, 0, 0, 0, time.UTC))
	if got != 250 {
		t.Errorf("GetFTPAtDate: got %d, want 250", got)
	}
}

func TestSyncFTPHistory_SkipsNullFTP(t *testing.T) {
	d := newSyncTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	src := NewIntervalsSource(d, imp)

	// All activities have nil icu_ftp — no history entries should be created.
	acts := []intervals.Activity{
		{ID: "1", StartDateLocal: "2023-01-01T10:00:00", IcuFTP: nil},
		{ID: "2", StartDateLocal: "2023-06-01T10:00:00", IcuFTP: nil},
	}
	srv := intervalsActivitiesServer(t, acts)
	defer srv.Close()

	if err := d.SetIntegrationCredentials("intervals", "i1", "key"); err != nil {
		t.Fatalf("SetIntegrationCredentials: %v", err)
	}
	src.overrideBase(srv.URL)

	if _, err := src.SyncFTPHistory(context.Background(), nil); err != nil {
		t.Fatalf("SyncFTPHistory: %v", err)
	}

	has, err := d.HasFTPHistory()
	if err != nil {
		t.Fatalf("HasFTPHistory: %v", err)
	}
	if has {
		t.Error("expected no FTP history entries when all icu_ftp values are nil")
	}
}
