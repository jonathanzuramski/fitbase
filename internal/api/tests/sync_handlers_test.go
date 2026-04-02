package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fitbase/fitbase/internal/api"
	"github.com/fitbase/fitbase/internal/importer"
	"github.com/fitbase/fitbase/internal/syncer"
)

// newSyncTestSetup builds a Manager with both sources registered, backed by a
// fresh in-memory DB. It reuses newTestDB defined in handlers_test.go.
func newSyncTestSetup(t *testing.T) (*syncer.Manager, *syncer.DropboxSource, *syncer.IntervalsSource) {
	t.Helper()
	d := newTestDB(t)
	imp := importer.NewImporter(d, t.TempDir())
	mgr := syncer.NewManager(d)
	dbxSrc := syncer.NewDropboxSource(d, imp)
	ivlSrc := syncer.NewIntervalsSource(d, imp)
	mgr.Register("dropbox", dbxSrc)
	mgr.Register("intervals", ivlSrc)
	return mgr, dbxSrc, ivlSrc
}

// postJSON is a convenience wrapper for building a JSON POST request.
func postJSON(t *testing.T, body any) *http.Request {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// decodeData unmarshals the "data" field from a standard response envelope.
func decodeData(t *testing.T, body []byte, dest any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v (body=%s)", err, body)
	}
	if dest != nil {
		if err := json.Unmarshal(env.Data, dest); err != nil {
			t.Fatalf("unmarshal data: %v", err)
		}
	}
}

// --- DropboxHandler ---

func TestDropboxHandler_Sync_Background(t *testing.T) {
	mgr, dbxSrc, _ := newSyncTestSetup(t)
	h := api.NewDropboxHandler(mgr, dbxSrc)

	req := httptest.NewRequest(http.MethodPost, "/api/dropbox/sync", nil)
	w := httptest.NewRecorder()
	h.Sync(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	var data map[string]any
	decodeData(t, w.Body.Bytes(), &data)
	if data["status"] != "sync started" {
		t.Errorf("data = %v, want status=sync started", data)
	}
}

func TestDropboxHandler_Sync_Stream_NoCredentials(t *testing.T) {
	// Without credentials source.Sync returns (0,0,0) immediately;
	// the handler still writes a "done" SSE event.
	mgr, dbxSrc, _ := newSyncTestSetup(t)
	h := api.NewDropboxHandler(mgr, dbxSrc)

	req := httptest.NewRequest(http.MethodPost, "/api/dropbox/sync?stream=1", nil)
	w := httptest.NewRecorder()
	h.Sync(w, req)

	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "event: done") {
		t.Errorf("SSE body missing 'event: done': %s", body)
	}
}

func TestDropboxHandler_SetLongpoll_Enable(t *testing.T) {
	mgr, dbxSrc, _ := newSyncTestSetup(t)
	h := api.NewDropboxHandler(mgr, dbxSrc)

	req := postJSON(t, map[string]any{"enabled": true})
	w := httptest.NewRecorder()
	h.SetLongpoll(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body)
	}
	var data map[string]any
	decodeData(t, w.Body.Bytes(), &data)
	if data["ok"] != true {
		t.Errorf("data = %v, want ok=true", data)
	}
	if !mgr.IsEnabled("dropbox") {
		t.Error("dropbox should be enabled in DB after SetLongpoll enable")
	}
}

func TestDropboxHandler_SetLongpoll_Disable(t *testing.T) {
	mgr, dbxSrc, _ := newSyncTestSetup(t)
	h := api.NewDropboxHandler(mgr, dbxSrc)

	// Enable first, then disable.
	mgr.Enable("dropbox") //nolint:errcheck

	req := postJSON(t, map[string]any{"enabled": false})
	w := httptest.NewRecorder()
	h.SetLongpoll(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if mgr.IsEnabled("dropbox") {
		t.Error("dropbox should be disabled in DB after SetLongpoll disable")
	}
}

func TestDropboxHandler_SetLongpoll_BadJSON(t *testing.T) {
	mgr, dbxSrc, _ := newSyncTestSetup(t)
	h := api.NewDropboxHandler(mgr, dbxSrc)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.SetLongpoll(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestDropboxHandler_Disconnect(t *testing.T) {
	mgr, dbxSrc, _ := newSyncTestSetup(t)
	h := api.NewDropboxHandler(mgr, dbxSrc)

	req := httptest.NewRequest(http.MethodPost, "/api/dropbox/disconnect", nil)
	w := httptest.NewRecorder()
	h.Disconnect(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body)
	}
	var data map[string]any
	decodeData(t, w.Body.Bytes(), &data)
	if data["ok"] != true {
		t.Errorf("data = %v, want ok=true", data)
	}
}

// --- IntervalsHandler ---

func TestIntervalsHandler_Sync_Background(t *testing.T) {
	mgr, _, ivlSrc := newSyncTestSetup(t)
	h := api.NewIntervalsHandler(mgr, ivlSrc)

	req := httptest.NewRequest(http.MethodPost, "/api/intervals/sync", nil)
	w := httptest.NewRecorder()
	h.Sync(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	var data map[string]any
	decodeData(t, w.Body.Bytes(), &data)
	if data["status"] != "sync started" {
		t.Errorf("data = %v, want status=sync started", data)
	}
}

func TestIntervalsHandler_Sync_Stream_NoCredentials(t *testing.T) {
	mgr, _, ivlSrc := newSyncTestSetup(t)
	h := api.NewIntervalsHandler(mgr, ivlSrc)

	req := httptest.NewRequest(http.MethodPost, "/api/intervals/sync?stream=1", nil)
	w := httptest.NewRecorder()
	h.Sync(w, req)

	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if !strings.Contains(w.Body.String(), "event: done") {
		t.Errorf("SSE body missing 'event: done': %s", w.Body)
	}
}

func TestIntervalsHandler_SetAutoSync_Enable(t *testing.T) {
	mgr, _, ivlSrc := newSyncTestSetup(t)
	h := api.NewIntervalsHandler(mgr, ivlSrc)

	req := postJSON(t, map[string]any{"enabled": true})
	w := httptest.NewRecorder()
	h.SetAutoSync(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body)
	}
	var data map[string]any
	decodeData(t, w.Body.Bytes(), &data)
	if data["ok"] != true {
		t.Errorf("data = %v, want ok=true", data)
	}
	if !mgr.IsEnabled("intervals") {
		t.Error("intervals should be enabled in DB after SetAutoSync enable")
	}
}

func TestIntervalsHandler_SetAutoSync_Disable(t *testing.T) {
	mgr, _, ivlSrc := newSyncTestSetup(t)
	h := api.NewIntervalsHandler(mgr, ivlSrc)

	mgr.Enable("intervals") //nolint:errcheck

	req := postJSON(t, map[string]any{"enabled": false})
	w := httptest.NewRecorder()
	h.SetAutoSync(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if mgr.IsEnabled("intervals") {
		t.Error("intervals should be disabled in DB after SetAutoSync disable")
	}
}

func TestIntervalsHandler_SetAutoSync_BadJSON(t *testing.T) {
	mgr, _, ivlSrc := newSyncTestSetup(t)
	h := api.NewIntervalsHandler(mgr, ivlSrc)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.SetAutoSync(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestIntervalsHandler_Disconnect(t *testing.T) {
	mgr, _, ivlSrc := newSyncTestSetup(t)
	h := api.NewIntervalsHandler(mgr, ivlSrc)

	req := httptest.NewRequest(http.MethodPost, "/api/intervals/disconnect", nil)
	w := httptest.NewRecorder()
	h.Disconnect(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body)
	}
	var data map[string]any
	decodeData(t, w.Body.Bytes(), &data)
	if data["ok"] != true {
		t.Errorf("data = %v, want ok=true", data)
	}
}

func TestIntervalsHandler_Fetch_NotConnected(t *testing.T) {
	// Without credentials, Fetch returns status "error".
	mgr, _, ivlSrc := newSyncTestSetup(t)
	h := api.NewIntervalsHandler(mgr, ivlSrc)

	req := withURLParam(
		httptest.NewRequest(http.MethodGet, "/api/intervals/activities/42", nil),
		"id", "42",
	)
	w := httptest.NewRecorder()
	h.Fetch(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var data map[string]any
	decodeData(t, w.Body.Bytes(), &data)
	if data["status"] != "error" {
		t.Errorf("data = %v, want status=error", data)
	}
	if data["activity_id"] != "42" {
		t.Errorf("data = %v, want activity_id=42", data)
	}
}

func TestIntervalsHandler_EnableDisablesDropbox(t *testing.T) {
	// Enabling intervals via the handler must disable dropbox (mutual exclusivity).
	mgr, dbxSrc, ivlSrc := newSyncTestSetup(t)
	dbxH := api.NewDropboxHandler(mgr, dbxSrc)
	ivlH := api.NewIntervalsHandler(mgr, ivlSrc)

	// Enable dropbox first.
	req := postJSON(t, map[string]any{"enabled": true})
	dbxH.SetLongpoll(httptest.NewRecorder(), req)
	if !mgr.IsEnabled("dropbox") {
		t.Fatal("dropbox should be enabled")
	}

	// Now enable intervals — dropbox must be disabled.
	req = postJSON(t, map[string]any{"enabled": true})
	ivlH.SetAutoSync(httptest.NewRecorder(), req)

	if mgr.IsEnabled("dropbox") {
		t.Error("dropbox should be disabled after enabling intervals")
	}
	if !mgr.IsEnabled("intervals") {
		t.Error("intervals should be enabled")
	}
}
