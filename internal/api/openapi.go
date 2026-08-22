package api

import (
	"bytes"
	"net/http"

	fitbase "github.com/fitbase/fitbase"
)

// GET /openapi.yaml
// ServeOpenAPI serves the embedded API spec with the servers URL rewritten to
// the host the request arrived on, so an agent that fetches the spec gets a
// base URL it can actually call (the file on disk says localhost:8080).
func ServeOpenAPI(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	spec := bytes.Replace(fitbase.OpenAPISpec,
		[]byte("url: http://localhost:8080"),
		[]byte("url: "+scheme+"://"+r.Host), 1)

	w.Header().Set("Content-Type", "application/yaml")
	w.Write(spec)
}
