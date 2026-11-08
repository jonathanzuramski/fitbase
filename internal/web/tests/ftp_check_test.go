package web_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/models"
	"github.com/fitbase/fitbase/internal/web"
)

func ftpFixture(t *testing.T) (*db.DB, http.Handler) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "ftp.db"), []byte("fitbase-test-key-do-not-use-prod"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.MarkSetupComplete(); err != nil {
		t.Fatal(err)
	}
	return d, web.NewTemplateHandler(d, false, web.FS)
}

// steadyRide stores a ride of 21 minutes at a constant power, recorded
// daysAgo, with its power curve (so it counts as a best effort).
func steadyRide(t *testing.T, d *db.DB, id string, daysAgo, watts int) *models.Workout {
	t.Helper()
	np := float64(watts)
	w := &models.Workout{
		ID: id, Filename: id + ".fit", Sport: "cycling",
		RecordedAt:   time.Now().UTC().AddDate(0, 0, -daysAgo),
		DurationSecs: 1260, NormalizedPower: &np, AvgPowerWatts: &np, CreatedAt: time.Now().UTC(),
	}
	w.TrainingDay = w.RecordedAt.Format("2006-01-02")
	var streams []models.Stream
	for i := 0; i < 1260; i++ {
		p := watts
		streams = append(streams, models.Stream{Timestamp: w.RecordedAt.Add(time.Duration(i) * time.Second), PowerWatts: &p})
	}
	if err := d.InsertWorkout(w, streams); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
	if err := d.InsertPowerCurve(id, map[int]int{1200: watts}); err != nil {
		t.Fatal(err)
	}
	return w
}

func get(t *testing.T, h http.Handler, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d", path, rec.Code)
	}
	return rec.Body.String()
}

func TestDashboard_FTPCheckBanner(t *testing.T) {
	d, h := ftpFixture(t)
	if err := d.SetFTP(245); err != nil {
		t.Fatal(err)
	}
	ride := steadyRide(t, d, "best", 10, 226)

	body := get(t, h, "/")
	for _, want := range []string{
		"Your 245 W FTP may be set high.",
		`<a href="/workouts/best">226 W on ` + ride.RecordedAt.Format("Jan 2") + `</a>, puts it near 215 W.`,
		`data-ftp-set="215"`, "keep 245 W",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}

	check, _ := d.FTPCheck(time.Now())
	if err := d.DismissFTPCheck(check, time.Now()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(get(t, h, "/"), "data-ftp-check") {
		t.Error("a dismissed check should not show the banner")
	}
	// Settings keeps saying it: dismissing silences the prompt, not the facts.
	if s := get(t, h, "/settings"); !strings.Contains(s, "This FTP may be set high.") || !strings.Contains(s, `data-ftp-set="215"`) {
		t.Error("settings should still show the verdict and the use button")
	}
}

// A seeded default FTP gets the existing "set your FTP" banner, not the check.
func TestDashboard_DefaultFTPKeepsItsBanner(t *testing.T) {
	d, h := ftpFixture(t)
	steadyRide(t, d, "best", 10, 180)
	body := get(t, h, "/")
	if !strings.Contains(body, "FTP is set to the default") || strings.Contains(body, "data-ftp-check") {
		t.Error("default FTP should show only the default-FTP banner")
	}
}

func TestSettings_FTPCheckNote(t *testing.T) {
	d, h := ftpFixture(t)
	if err := d.SetFTP(245); err != nil {
		t.Fatal(err)
	}
	if s := get(t, h, "/settings"); !strings.Contains(s, "Nothing in the last 90 days was hard enough") || strings.Contains(s, "data-ftp-set") {
		t.Error("with no rides, settings should say the FTP is unverified and offer no number")
	}
	steadyRide(t, d, "best", 5, 255)
	if s := get(t, h, "/settings"); !strings.Contains(s, "Backed by your rides.") || strings.Contains(s, "data-ftp-set") {
		t.Error("a backed FTP should say so without offering a change")
	}
}

// eFTP from an easy ride is a low number beside the real FTP, not information;
// the ride page shows it only from a hard ride, and offers it as the new FTP
// only when it clearly beats the current one.
func TestWorkoutPage_EFTP(t *testing.T) {
	d, h := ftpFixture(t)
	if err := d.SetFTP(245); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		id          string
		watts       int
		showEFTP    bool
		newFTPOffer string
	}{
		{"easy", 200, false, ""},           // eFTP 190: hidden
		{"hard", 255, true, ""},            // eFTP 242: shown, not a new FTP
		{"breakthrough", 280, true, "266"}, // eFTP 266 > 103% of 245
	}
	for _, c := range cases {
		steadyRide(t, d, c.id, 1, c.watts)
		body := get(t, h, "/workouts/"+c.id)
		if got := strings.Contains(body, `<div class="metric-label">eFTP</div>`); got != c.showEFTP {
			t.Errorf("%s: eFTP shown = %v, want %v", c.id, got, c.showEFTP)
		}
		offer := strings.Contains(body, "New FTP detected")
		if offer != (c.newFTPOffer != "") || (offer && !strings.Contains(body, `data-ftp-set="`+c.newFTPOffer+`"`)) {
			t.Errorf("%s: new-FTP banner shown = %v, want offer %q", c.id, offer, c.newFTPOffer)
		}
	}
}
