package api

import (
	"context"
	"encoding/json"
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

// Verify the JSON response helpers used in handlers compile correctly.
func TestWriteJSON_Compiles(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var got map[string]any
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
}
