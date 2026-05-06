package web

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

//go:embed templates static
var FS embed.FS

// DevFS returns an fs.FS rooted at this package's source directory, so dev mode
// picks up edits to templates/static without rebuilding. Resolved from the
// caller's source path rather than the process cwd, so debuggers that launch
// from a different directory still work.
func DevFS() fs.FS {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return FS
	}
	return os.DirFS(filepath.Dir(file))
}
