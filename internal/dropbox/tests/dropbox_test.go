package dropbox_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/dropbox"
)

// newTestClient returns a Client pointed at the given test server for all three base URLs.
func newTestClient(srv *httptest.Server) *dropbox.Client {
	return dropbox.NewWithConfig("test-token", srv.URL, srv.URL, srv.URL)
}

func TestListFITFiles_FiltersNonFIT(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"entries": []map[string]any{
				{".tag": "file", "name": "ride.fit", "path_lower": "/ride.fit"},
				{".tag": "file", "name": "readme.txt", "path_lower": "/readme.txt"},
				{".tag": "folder", "name": "photos", "path_lower": "/photos"},
			},
			"cursor":   "cur1",
			"has_more": false,
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	files, err := c.ListFITFiles(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 1 || files[0].Name != "ride.fit" {
		t.Errorf("got %v, want exactly [ride.fit]", files)
	}
}

func TestListFITFiles_CaseInsensitive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"entries": []map[string]any{
				{".tag": "file", "name": "RIDE.FIT", "path_lower": "/ride.fit"},
			},
			"cursor":   "cur1",
			"has_more": false,
		})
	}))
	defer srv.Close()

	files, err := newTestClient(srv).ListFITFiles(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 file, got %d", len(files))
	}
}

func TestListFITFiles_Pagination(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		if page == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"entries":  []map[string]any{{".tag": "file", "name": "a.fit", "path_lower": "/a.fit"}},
				"cursor":   "cursor-page2",
				"has_more": true,
			})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"entries":  []map[string]any{{".tag": "file", "name": "b.fit", "path_lower": "/b.fit"}},
				"cursor":   "cursor-done",
				"has_more": false,
			})
		}
	}))
	defer srv.Close()

	files, err := newTestClient(srv).ListFITFiles(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files across pages, got %d", len(files))
	}
}

func TestListFITFiles_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := newTestClient(srv).ListFITFiles(context.Background(), "")
	if err == nil {
		t.Error("expected error on HTTP 401, got nil")
	}
}

func TestDownload_ReturnsBytes(t *testing.T) {
	want := []byte("FIT file bytes here")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(want) //nolint:errcheck
	}))
	defer srv.Close()

	got, err := newTestClient(srv).Download(context.Background(), "/ride.fit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDownload_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := newTestClient(srv).Download(context.Background(), "/missing.fit")
	if err == nil {
		t.Error("expected error on HTTP 404, got nil")
	}
}

func TestGetLatestCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// this is fine for tests
		json.NewEncoder(w).Encode(map[string]string{"cursor": "latest-cursor-abc"}) //nolint:errcheck
	}))
	defer srv.Close()

	cursor, err := newTestClient(srv).GetLatestCursor(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cursor != "latest-cursor-abc" {
		t.Errorf("got %q, want %q", cursor, "latest-cursor-abc")
	}
}

func TestGetLatestCursor_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newTestClient(srv).GetLatestCursor(context.Background(), "")
	if err == nil {
		t.Error("expected error on HTTP 500, got nil")
	}
}

func TestLongpoll_Changes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"changes": true, "backoff": 0})
	}))
	defer srv.Close()

	changes, backoff, err := newTestClient(srv).Longpoll(context.Background(), "cursor", 30)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changes {
		t.Error("expected changes=true")
	}
	if backoff != 0 {
		t.Errorf("expected backoff=0, got %d", backoff)
	}
}

func TestLongpoll_NoChanges(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"changes": false, "backoff": 0})
	}))
	defer srv.Close()

	changes, _, err := newTestClient(srv).Longpoll(context.Background(), "cursor", 30)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changes {
		t.Error("expected changes=false")
	}
}

func TestLongpoll_Backoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"changes": false, "backoff": 5})
	}))
	defer srv.Close()

	_, backoff, err := newTestClient(srv).Longpoll(context.Background(), "cursor", 30)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if backoff != 5 {
		t.Errorf("expected backoff=5, got %d", backoff)
	}
}

func TestLongpoll_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	_, _, err := newTestClient(srv).Longpoll(context.Background(), "cursor", 30)
	if err == nil {
		t.Error("expected error on HTTP 400, got nil")
	}
}

func TestListFolderContinue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"entries": []map[string]any{
				{".tag": "file", "name": "new.fit", "path_lower": "/new.fit", "server_modified": time.Now().UTC().Format(time.RFC3339)},
				{".tag": "deleted", "name": "old.fit", "path_lower": "/old.fit"},
			},
			"cursor":   "new-cursor",
			"has_more": false,
		})
	}))
	defer srv.Close()

	files, cursor, hasMore, err := newTestClient(srv).ListFolderContinue(context.Background(), "cursor")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 1 || files[0].Name != "new.fit" {
		t.Errorf("expected [new.fit], got %v", files)
	}
	if cursor != "new-cursor" {
		t.Errorf("cursor = %q, want %q", cursor, "new-cursor")
	}
	if hasMore {
		t.Error("expected hasMore=false")
	}
}
