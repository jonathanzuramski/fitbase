package api

import (
	"sync"

	"github.com/fitbase/fitbase/internal/db"
)

// SyncManager owns the "which sync source is active" state.
// Handlers register their stop functions; enabling one source
// automatically disables all others.
type SyncManager struct {
	db       *db.DB
	mu       sync.Mutex
	stoppers map[string]func()
}

func NewSyncManager(database *db.DB) *SyncManager {
	return &SyncManager{
		db:       database,
		stoppers: make(map[string]func()),
	}
}

// Register adds a named sync source and its stop function.
func (sm *SyncManager) Register(name string, stop func()) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.stoppers[name] = stop
}

// Enable activates auto-sync for the named source, disabling all others.
func (sm *SyncManager) Enable(name string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for n, stop := range sm.stoppers {
		if n != name {
			stop()
			if err := sm.db.SetAutoSync(n, false); err != nil {
				return err
			}
		}
	}
	return sm.db.SetAutoSync(name, true)
}

// Disable stops auto-sync for the named source.
func (sm *SyncManager) Disable(name string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if stop, ok := sm.stoppers[name]; ok {
		stop()
	}
	return sm.db.SetAutoSync(name, false)
}

// IsEnabled returns whether auto-sync is active for the named source.
func (sm *SyncManager) IsEnabled(name string) bool {
	v, _ := sm.db.GetAutoSync(name)
	return v
}
