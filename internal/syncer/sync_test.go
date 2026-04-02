package syncer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/dropbox"
	"github.com/fitbase/fitbase/internal/importer"
	"github.com/fitbase/fitbase/internal/intervals"
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
