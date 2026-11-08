package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"
)

// Static files must be revalidated: in dev mode they come from disk with a
// real Last-Modified, and a heuristically cached stylesheet paired with a newer
// template breaks the page layout.
func TestStaticHandler_RevalidatesEveryLoad(t *testing.T) {
	fsys := fstest.MapFS{"css/app.css": {Data: []byte("body{}"), ModTime: time.Now().Add(-48 * time.Hour)}}
	rr := httptest.NewRecorder()
	staticHandler(http.FS(fsys)).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/static/css/app.css", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "body{}" {
		t.Fatalf("GET /static/css/app.css = %d %q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
}
