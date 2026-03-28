package config

import (
	"log/slog"
	"os"
	"path/filepath"
)

type Config struct {
	Port       string
	DBPath     string
	KeyPath    string // path to the 32-byte AES-256 master key file
	WatchDir   string
	ArchiveDir string
	Dev        bool // FITBASE_DEV=true: serve templates/static from disk, re-parse on each request
}

func Load() *Config {
	home, err := os.UserHomeDir()
	if err != nil {
		slog.Warn("could not determine home directory, using current directory", "err", err)
		home = "."
	}
	base := filepath.Join(home, ".fitbase")

	return &Config{
		Port:       getenv("FITBASE_PORT", "8080"),
		DBPath:     getenv("FITBASE_DB_PATH", filepath.Join(base, "fitbase.db")),
		KeyPath:    getenv("FITBASE_KEY_PATH", filepath.Join(base, "master.key")),
		WatchDir:   getenv("FITBASE_WATCH_DIR", filepath.Join(base, "watch")),
		ArchiveDir: getenv("FITBASE_ARCHIVE_DIR", filepath.Join(base, "archive")),
		Dev:        getenv("FITBASE_DEV", "false") == "true",
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
