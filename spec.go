// Package fitbase embeds repo-root assets that ship inside the binary.
package fitbase

import _ "embed"

// OpenAPISpec is the REST API spec, embedded so a running instance can serve
// it at GET /openapi.yaml for agent discovery.
//
//go:embed openapi.yaml
var OpenAPISpec []byte
