package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/models"
)

// Analytics keeps everything Progress showed — the FTP check, FTP against
// the rides, the power curve, weekly load against plan, durability and the
// intensity split — and adds the period comparison and the years. The page
// arrives with its data inline, so it draws without a round trip.
func TestAnalytics(t *testing.T) {
	d, h := ftpFixture(t)
	if err := d.SetFTP(245); err != nil {
		t.Fatal(err)
	}
	ride := steadyRide(t, d, "recent", 10, 226) // a 226 W 20-minute best, ten days ago
	tss := 70.0
	if err := d.UpdateWorkoutLoad(ride.ID, tss, 0.85); err != nil {
		t.Fatal(err)
	}
	// 60 minutes easy, 30 tempo/threshold, 6 hard.
	if err := d.InsertZoneTimes(ride.ID, [7]int{1200, 2400, 1200, 600, 240, 120, 0}, [5]int{}, 0); err != nil {
		t.Fatal(err)
	}

	body := get(t, h, "/analytics")
	for _, want := range []string{
		"This FTP may be set high.", // the FTP check note
		`"estimate":[{"t":`,         // the FTP chart has estimate points
		`"w":215}`,                  // 95% of 226 W
		`"days_28":[`,               // the recent power curve
		`"tss":70,"planned_tss":0`,  // the ride in the weekly load
		`"easy_pct":62.5`,           // the intensity split: 60 of 96 minutes
		`id="cmp-form"`,             // the comparison's controls
		`data-period="month"`,
		`"period":"month","against":"previous"`, // this month against last, by default
		`id="an-yoy"`,
		`<script src="/static/js/analytics.js"></script>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("analytics lacks %q", want)
		}
	}
	// The nav lists every page by its own name: the heatmap keeps its link.
	for _, link := range []string{`<a href="/">dashboard</a>`, `<a href="/heatmap">heatmap</a>`, `<a href="/analytics">analytics</a>`} {
		if !strings.Contains(body, link) {
			t.Errorf("the nav lacks %s", link)
		}
	}
	if strings.Contains(body, `href="/progress"`) {
		t.Error("the nav still links to /progress")
	}
}

// The URL carries the view: a bookmarked comparison and range render as
// they were left, and the controls show them.
func TestAnalytics_URLState(t *testing.T) {
	d, h := ftpFixture(t)
	steadyRide(t, d, "r", 3, 200)
	body := get(t, h, "/analytics?period=custom&from=2026-01-05&to=2026-02-01&against=year&scope=all&range=6m")
	for _, want := range []string{
		`"period":"custom","against":"year"`,
		`"label":"Jan 5 – Feb 1, 2026"`,
		`"label":"Jan 5 – Feb 1, 2025"`,
		`"scope":"all"`,
		`"range":"6m"`,
		`name="from" value="2026-01-05"`,
		`class="filter-tab active" data-period="custom"`,
		`class="filter-tab active" data-range="6m"`,
		`<option value="year" selected>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("analytics?custom lacks %q", want)
		}
	}
}

// A hand-edited URL with a bad parameter falls back to the defaults instead
// of failing the page (the API answers 400 for the same input).
func TestAnalytics_BadParamsFallBack(t *testing.T) {
	_, h := ftpFixture(t)
	body := get(t, h, "/analytics?period=fortnight&scope=curling&range=5y")
	for _, want := range []string{`"period":"month"`, `"range":"1y"`, `"scope":"all"`} {
		if !strings.Contains(body, want) {
			t.Errorf("fallback lacks %q", want)
		}
	}
}

// With no rides the page still renders, each chart saying it has nothing yet.
func TestAnalytics_Empty(t *testing.T) {
	_, h := ftpFixture(t)
	body := get(t, h, "/analytics")
	if !strings.Contains(body, "Nothing in the last 90 days was hard enough") {
		t.Error("an empty history should say the FTP is unverified")
	}
	if !strings.Contains(body, `"rides":[]`) || !strings.Contains(body, `"years":[]`) {
		t.Error("an empty history should still send empty lists for the charts")
	}
}

// Units follow the toggle: the page tells its script which to draw in.
func TestAnalytics_Units(t *testing.T) {
	d, h := ftpFixture(t)
	if err := d.UpdateAthleteUnits("metric"); err != nil {
		t.Fatal(err)
	}
	if body := get(t, h, "/analytics"); !strings.Contains(body, `data-imperial="false"`) {
		t.Error("metric riders should get metric charts")
	}
}

// Indoor rides with power land on their own side of the comparison.
func TestAnalytics_IndoorKeptApart(t *testing.T) {
	d, h := ftpFixture(t)
	now := time.Now().UTC()
	np, hr := 180.0, 140
	ifac, tss := 0.72, 60.0
	w := &models.Workout{
		ID: "trainer", Filename: "trainer.fit", Sport: "cycling", IsIndoor: true,
		RecordedAt: now.Add(-time.Hour), DurationSecs: 3600,
		AvgPowerWatts: &np, NormalizedPower: &np, AvgHeartRate: &hr,
		IntensityFactor: &ifac, TSS: &tss, CreatedAt: now,
	}
	if err := d.InsertWorkout(w, nil); err != nil {
		t.Fatal(err)
	}
	body := get(t, h, "/analytics?period=week&against=previous")
	if !strings.Contains(body, `"key":"ef_indoor"`) || strings.Contains(body, `"key":"ef_outdoor"`) {
		t.Error("an indoor ride's EF should be compared on the indoor row only")
	}
}

// Progress became Analytics: the old address answers with a permanent
// redirect, so bookmarks and links in old coach replies still land.
func TestProgress_RedirectsToAnalytics(t *testing.T) {
	_, h := ftpFixture(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/progress", nil))
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("GET /progress: %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/analytics" {
		t.Errorf("GET /progress redirects to %q, want /analytics", loc)
	}
}

// A sport in the URL that the rider has no workouts in yet stays picked in
// the menu, so the next change doesn't quietly switch to all sports.
func TestAnalytics_ScopeWithoutWorkoutsStaysPicked(t *testing.T) {
	d, h := ftpFixture(t)
	steadyRide(t, d, "r", 3, 200)
	body := get(t, h, "/analytics?scope=hiking")
	if !strings.Contains(body, `<option value="hiking" selected>hiking</option>`) {
		t.Error("the sport menu lacks the picked sport, hiking")
	}
	if strings.Count(body, `<option value="cycling"`) != 1 {
		t.Error("the rider's own sports should still be listed once")
	}
}
