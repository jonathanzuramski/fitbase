package db

import (
	"path/filepath"
	"testing"
)

var migrateTestKey = []byte("fitbase-test-key-do-not-use-prod")

func openTempDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "migrate_test.db")
	d, err := Open(path, migrateTestKey)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// setMetaTx is how a migration requests an archive rebuild when its inputs
// aren't stored in the database (see MetaRebuildPending). A value it writes
// inside the migration transaction must be visible to GetMeta after commit, so
// the boot path can act on it. A neutral key keeps these tests independent of
// what the ladder itself writes.
func TestSetMetaTx_PersistsAfterCommit(t *testing.T) {
	d := openTempDB(t)

	tx, err := d.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := setMetaTx(tx, "test_flag", "1"); err != nil {
		t.Fatalf("setMetaTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if v, _ := d.GetMeta("test_flag"); v != "1" {
		t.Errorf("GetMeta(test_flag) = %q after commit, want \"1\"", v)
	}
}

// The meta write shares the migration's transaction with the version bump, so a
// migration that rolls back must not leave its flag set.
func TestSetMetaTx_DiscardedOnRollback(t *testing.T) {
	d := openTempDB(t)

	tx, err := d.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := setMetaTx(tx, "test_flag", "1"); err != nil {
		t.Fatalf("setMetaTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	if v, _ := d.GetMeta("test_flag"); v != "" {
		t.Errorf("GetMeta after rollback = %q, want empty", v)
	}
}

// No shipped migration sets the rebuild flag today (v3 converges in place);
// this pins the boot-path contract for whichever future migration does:
// clearing the flag after a successful rebuild sticks.
func TestRebuildPending_ClearSticks(t *testing.T) {
	d := openTempDB(t)

	if err := d.SetMeta(MetaRebuildPending, "1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := d.DeleteMeta(MetaRebuildPending); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if v, _ := d.GetMeta(MetaRebuildPending); v != "" {
		t.Errorf("flag still %q after clear, want empty", v)
	}
}

// setMetaTx upserts like SetMeta: a later migration can overwrite a value an
// earlier one (or a *DB caller) wrote.
func TestSetMetaTx_Upserts(t *testing.T) {
	d := openTempDB(t)
	if err := d.SetMeta("k", "old"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tx, err := d.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := setMetaTx(tx, "k", "new"); err != nil {
		t.Fatalf("setMetaTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if v, _ := d.GetMeta("k"); v != "new" {
		t.Errorf("after upsert = %q, want new", v)
	}
}
