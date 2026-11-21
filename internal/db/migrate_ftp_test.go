package db

import "testing"

// v10 re-homes date-only FTP entries (stored at UTC midnight by the settings
// form) to midnight in the athlete's timezone, so they keep showing the day
// that was entered now that history displays in local days. Timestamped
// entries are left alone. Rerunning it over a migrated database is a no-op.
func TestMigrateFTPHistoryIndoor_RehomesDateOnlyEntries(t *testing.T) {
	d := openTempDB(t)
	if _, err := d.Exec(`UPDATE athlete SET timezone='America/New_York' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO ftp_history (ftp_watts, effective_from) VALUES
		(245, '2026-06-09T00:00:00Z'), (235, '2025-11-11T23:10:07Z')`); err != nil {
		t.Fatal(err)
	}

	for run := 0; run < 2; run++ {
		tx, err := d.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := migrateFTPHistoryIndoor(tx); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	want := map[int]string{245: "2026-06-09T04:00:00Z", 235: "2025-11-11T23:10:07Z"}
	for ftp, at := range want {
		var got string
		if err := d.QueryRow(`SELECT effective_from FROM ftp_history WHERE ftp_watts=?`, ftp).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != at {
			t.Errorf("%d W entry at %s, want %s", ftp, got, at)
		}
	}
}
