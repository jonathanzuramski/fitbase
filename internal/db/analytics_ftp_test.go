package db

import (
	"path/filepath"
	"testing"
	"time"
)

// Analytics resolves each ride's FTP from one read of the history
// (ftpLines) instead of two queries per ride. It must agree with
// GetFTPForRide and GetFTPAtDate everywhere: before any entry, on an entry's
// exact instant, when an indoor entry is older or newer than the general one,
// and on ties.
func TestFTPLines_AgreeWithGetFTPForRide(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "ftp.db"), []byte("fitbase-test-key-do-not-use-prod"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	at := func(m time.Month, day int) time.Time { return time.Date(2026, m, day, 5, 0, 0, 0, time.UTC) }
	for _, e := range []struct {
		w      int
		t      time.Time
		indoor bool
	}{
		{240, at(1, 1), false},
		{228, at(2, 1), true},  // indoor line, newer than the general
		{250, at(4, 1), false}, // a general change outranks the older indoor entry
		{236, at(5, 1), true},
		{238, at(5, 1), true}, // same instant: the later insert wins
		{255, at(7, 1), false},
		{255, at(7, 1), true}, // same instant as a general entry: indoor still counts
	} {
		if err := d.LogFTPChangeAtFor(e.w, e.t, e.indoor); err != nil {
			t.Fatal(err)
		}
	}
	lines, err := d.loadFTPLines()
	if err != nil {
		t.Fatal(err)
	}
	for _, ts := range []time.Time{
		at(1, 1).Add(-time.Hour), at(1, 1), at(1, 15), at(2, 1), at(3, 1), at(4, 1), at(4, 2),
		at(5, 1), at(6, 1), at(7, 1), at(7, 1).Add(time.Second), at(12, 31),
		at(3, 1).Add(500 * time.Millisecond),
	} {
		for _, indoor := range []bool{false, true} {
			if got, want := lines.forRide(ts, indoor), d.GetFTPForRide(ts, indoor); got != want {
				t.Errorf("forRide(%s, indoor=%v) = %d, GetFTPForRide = %d", ts.Format(time.RFC3339Nano), indoor, got, want)
			}
		}
		if got, want := lines.generalAt(ts), d.GetFTPAtDate(ts); got != want {
			t.Errorf("generalAt(%s) = %d, GetFTPAtDate = %d", ts.Format(time.RFC3339), got, want)
		}
	}
}
