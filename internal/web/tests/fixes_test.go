package web_test

import (
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/models"
)

// FTP history shows days in the athlete's timezone, with indoor entries tagged.
// An evening change (Eastern) is already the next day in UTC.
func TestSettings_FTPHistoryLocalDaysAndIndoor(t *testing.T) {
	d, h := ftpFixture(t)
	if err := d.UpdateAthlete(&models.Athlete{FTPWatts: 245, WeightKG: 80, Timezone: "America/New_York"}); err != nil {
		t.Fatal(err)
	}
	if err := d.ClearFTPHistory(); err != nil {
		t.Fatal(err)
	}
	// 22:30 Eastern on Nov 7 is 03:30 UTC on Nov 8.
	if err := d.LogFTPChangeAtFor(235, time.Date(2026, 11, 8, 3, 30, 0, 0, time.UTC), true); err != nil {
		t.Fatal(err)
	}
	body := get(t, h, "/settings")
	if !strings.Contains(body, "<td>2026-11-07</td>") || strings.Contains(body, "<td>2026-11-08</td>") {
		t.Error("FTP history should show the local day, 2026-11-07")
	}
	if !strings.Contains(body, `235 W <span class="ftp-history-indoor"`) {
		t.Error("indoor entry should be tagged")
	}
}

// The dashboard badge is rendered from db.Readiness, the same recommendation
// the coach and the readiness API give (the JS copy used other thresholds).
func TestDashboard_ReadinessBadge(t *testing.T) {
	d, h := ftpFixture(t)
	if err := d.SetFTP(245); err != nil {
		t.Fatal(err)
	}
	w := steadyRide(t, d, "yesterday", 1, 200)
	tss := 60.0
	if err := d.UpdateWorkoutLoad(w.ID, tss, 0.8); err != nil {
		t.Fatal(err)
	}
	r, err := d.Readiness(time.UTC)
	if err != nil || r.Recommendation == "" || r.RecommendationTone == "" {
		t.Fatalf("readiness = %+v (%v)", r, err)
	}
	body := get(t, h, "/")
	want := `<div class="rec-badge rec-` + r.RecommendationTone + `"><span class="rec-label">` + r.Recommendation + `</span>`
	if !strings.Contains(body, want) {
		t.Errorf("dashboard lacks the server-rendered badge %q", want)
	}
}
