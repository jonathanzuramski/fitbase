package config_test

import (
	"testing"

	"github.com/fitbase/fitbase/internal/config"
)

func TestLoad_Defaults(t *testing.T) {
	// Clear any env overrides that might be set in CI
	for _, k := range []string{
		"FITBASE_PORT", "FITBASE_DB_PATH", "FITBASE_WATCH_DIR", "FITBASE_ARCHIVE_DIR",
	} {
		t.Setenv(k, "")
	}

	cfg := config.Load()

	if cfg.Port != "8080" {
		t.Errorf("Port: got %q want %q", cfg.Port, "8080")
	}
	if cfg.DBPath == "" {
		t.Error("DBPath should have a default")
	}
	if cfg.WatchDir == "" {
		t.Error("WatchDir should have a default")
	}
	if cfg.ArchiveDir == "" {
		t.Error("ArchiveDir should have a default")
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv("FITBASE_PORT", "9090")
	t.Setenv("FITBASE_DB_PATH", "/tmp/custom.db")
	t.Setenv("FITBASE_WATCH_DIR", "/tmp/watch")
	t.Setenv("FITBASE_ARCHIVE_DIR", "/tmp/archive")

	cfg := config.Load()

	if cfg.Port != "9090" {
		t.Errorf("Port: got %q want %q", cfg.Port, "9090")
	}
	if cfg.DBPath != "/tmp/custom.db" {
		t.Errorf("DBPath: got %q want %q", cfg.DBPath, "/tmp/custom.db")
	}
	if cfg.WatchDir != "/tmp/watch" {
		t.Errorf("WatchDir: got %q want %q", cfg.WatchDir, "/tmp/watch")
	}
	if cfg.ArchiveDir != "/tmp/archive" {
		t.Errorf("ArchiveDir: got %q want %q", cfg.ArchiveDir, "/tmp/archive")
	}
}

