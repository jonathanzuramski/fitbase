package syncer

import (
	"context"
	"log/slog"
	"sync"

	"github.com/fitbase/fitbase/internal/db"
)

// SyncSource is implemented by integrations that pull activities from external services.
type SyncSource interface {
	// Sync performs a one-time sync. If onProgress is non-nil, it receives events
	// like ("start", ...), ("file", ...) suitable for SSE streaming.
	Sync(ctx context.Context, onProgress func(event string, data any)) (imported, skipped, failed int)
	// StartAuto begins the background auto-sync loop.
	StartAuto() error
	// StopAuto stops the background auto-sync loop.
	StopAuto()
	// Running reports whether auto-sync is currently active.
	Running() bool
	// Disconnect removes all stored credentials and stops auto-sync.
	Disconnect() error
}

// Manager coordinates sync sources and enforces mutual exclusivity:
// enabling one source automatically disables all others.
type Manager struct {
	db      *db.DB
	mu      sync.Mutex
	sources map[string]SyncSource
}

func NewManager(database *db.DB) *Manager {
	return &Manager{
		db:      database,
		sources: make(map[string]SyncSource),
	}
}

// Register adds a named sync source.
func (m *Manager) Register(name string, src SyncSource) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sources[name] = src
}

// Enable activates auto-sync for the named source, disabling all others.
func (m *Manager) Enable(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for n, src := range m.sources {
		if n != name {
			src.StopAuto()
			if err := m.db.SetAutoSync(n, false); err != nil {
				return err
			}
		}
	}
	if err := m.db.SetAutoSync(name, true); err != nil {
		return err
	}
	return m.sources[name].StartAuto()
}

// Disable stops auto-sync for the named source.
func (m *Manager) Disable(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if src, ok := m.sources[name]; ok {
		src.StopAuto()
	}
	return m.db.SetAutoSync(name, false)
}

// IsEnabled returns whether auto-sync is active for the named source.
func (m *Manager) IsEnabled(name string) bool {
	v, _ := m.db.GetAutoSync(name)
	return v
}

// RestoreAll re-enables auto-sync for any source that was active before shutdown.
func (m *Manager) RestoreAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, src := range m.sources {
		enabled, err := m.db.GetAutoSync(name)
		if err != nil || !enabled {
			continue
		}
		if err := src.StartAuto(); err != nil {
			slog.Warn("failed to restore auto-sync", "source", name, "err", err)
		}
	}
}
