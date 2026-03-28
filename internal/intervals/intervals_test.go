package intervals_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fitbase/fitbase/internal/intervals"
)

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(data) //nolint:errcheck
	_ = w.Close()
	return buf.Bytes()
}

func newTestClient(srv *httptest.Server) *intervals.Client {
	return intervals.NewWithBase("i12345", "test-api-key", srv.URL)
}

func TestListActivities_ParsesJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "1001", "start_date_local": "2024-03-15T08:00:00", "type": "Ride", "name": "Morning ride"},
			{"id": "1002", "start_date_local": "2024-03-16T09:00:00", "type": "Run", "name": "Easy run"},
		})
	}))
	defer srv.Close()

	acts, err := newTestClient(srv).ListActivities(context.Background(), "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(acts) != 2 {
		t.Fatalf("expected 2 activities, got %d", len(acts))
	}
	if acts[0].ID != "1001" || acts[0].Name != "Morning ride" {
		t.Errorf("unexpected activity[0]: %+v", acts[0])
	}
}

func TestListActivities_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newTestClient(srv).ListActivities(context.Background(), "", "")
	if err == nil {
		t.Error("expected error on HTTP 500, got nil")
	}
}

func TestListActivities_OldestFilter(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()

	newTestClient(srv).ListActivities(context.Background(), "2024-01-01", "") //nolint:errcheck
	if !strings.Contains(gotURL, "oldest=2024-01-01") {
		t.Errorf("expected oldest param in URL, got %q", gotURL)
	}
}

func TestDownloadFIT_ReturnsBytes(t *testing.T) {
	want := []byte("FIT binary data")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(gzipBytes(t, want)) //nolint:errcheck
	}))
	defer srv.Close()

	got, err := newTestClient(srv).DownloadFIT(context.Background(), "1001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDownloadFIT_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := newTestClient(srv).DownloadFIT(context.Background(), "9999")
	if err == nil {
		t.Error("expected error on HTTP 404, got nil")
	}
}

func TestValidateCredentials_Valid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "i12345"})
	}))
	defer srv.Close()

	if err := newTestClient(srv).ValidateCredentials(context.Background()); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateCredentials_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	if err := newTestClient(srv).ValidateCredentials(context.Background()); err == nil {
		t.Error("expected error on HTTP 401, got nil")
	}
}

func TestValidateCredentials_UsesBasicAuth(t *testing.T) {
	var gotUser, gotPass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		_ = json.NewEncoder(w).Encode(map[string]string{})
	}))
	defer srv.Close()

	newTestClient(srv).ValidateCredentials(context.Background()) //nolint:errcheck
	if gotUser != "API_KEY" {
		t.Errorf("basic auth username = %q, want %q", gotUser, "API_KEY")
	}
	if gotPass != "test-api-key" {
		t.Errorf("basic auth password = %q, want %q", gotPass, "test-api-key")
	}
}
