package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Analytics charts what the rides say about FTP, the power curve, weekly load
// and the intensity split.
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
		`<div class="vol-tss">70</div>`,
		"easy <span class=\"muted\">Z1–Z2</span> <strong>62%</strong>", // 60 of 96 minutes
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

// With no rides the page still renders, each chart saying it has nothing yet.
func TestAnalytics_Empty(t *testing.T) {
	_, h := ftpFixture(t)
	if body := get(t, h, "/analytics"); !strings.Contains(body, "Nothing in the last 90 days was hard enough") {
		t.Error("an empty history should say the FTP is unverified")
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
