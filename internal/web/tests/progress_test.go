package web_test

import (
	"strings"
	"testing"
)

// Progress charts what the rides say about FTP, the power curve, weekly load
// and the intensity split.
func TestProgress(t *testing.T) {
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

	body := get(t, h, "/progress")
	for _, want := range []string{
		"This FTP may be set high.", // the FTP check note
		`"estimate":[{"t":`,         // the FTP chart has estimate points
		`"w":215}`,                  // 95% of 226 W
		`"days_28":[`,               // the recent power curve
		`<div class="vol-tss">70</div>`,
		"easy <span class=\"muted\">Z1–Z2</span> <strong>62%</strong>", // 60 of 96 minutes
	} {
		if !strings.Contains(body, want) {
			t.Errorf("progress lacks %q", want)
		}
	}
	if !strings.Contains(body, `<a href="/progress" data-also="/heatmap">progress</a>`) {
		t.Error("the nav should link progress (covering the heatmap)")
	}
}

// With no rides the page still renders, each chart saying it has nothing yet.
func TestProgress_Empty(t *testing.T) {
	_, h := ftpFixture(t)
	if body := get(t, h, "/progress"); !strings.Contains(body, "Nothing in the last 90 days was hard enough") {
		t.Error("an empty history should say the FTP is unverified")
	}
}
