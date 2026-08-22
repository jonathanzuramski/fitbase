package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fitbase/fitbase/internal/api"
)

func TestServeOpenAPIRewritesServerURLToRequestHost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://192.168.1.27:8780/openapi.yaml", nil)
	rr := httptest.NewRecorder()
	api.ServeOpenAPI(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("Content-Type = %q, want application/yaml", ct)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "openapi: 3.1.0") {
		t.Errorf("body does not look like the OpenAPI spec: %.200s", body)
	}
	if !strings.Contains(body, "url: http://192.168.1.27:8780") {
		t.Errorf("servers URL not rewritten to request host")
	}
	if strings.Contains(body, "url: http://localhost:8080") {
		t.Errorf("placeholder servers URL still present after rewrite")
	}
}
