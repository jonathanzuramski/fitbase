package web_test

import (
	"strings"
	"testing"
)

// The dashboard is the pre-1.2 dashboard again: its own name, the "this
// week" streak and the training load card, and no Today card or phone tab
// bar. The nav names every page as it was, with progress added.
func TestDashboard_IsTheDashboardAgain(t *testing.T) {
	_, h := ftpFixture(t)
	body := get(t, h, "/")
	for _, want := range []string{"<h1>dashboard", "streak-card", "training load"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
	for _, gone := range []string{"today-card", `class="tabbar"`, "<h1>today"} {
		if strings.Contains(body, gone) {
			t.Errorf("dashboard still has %q", gone)
		}
	}

	nav := []string{
		`<a href="/">dashboard</a>`,
		`<a href="/calendar">calendar</a>`,
		`<a href="/heatmap">heatmap</a>`,
		`<a href="/progress">progress</a>`,
		`<a href="/settings">settings</a>`,
	}
	at := 0
	for _, link := range nav {
		i := strings.Index(body[at:], link)
		if i < 0 {
			t.Fatalf("the nav lacks %s, or has it out of order", link)
		}
		at += i + len(link)
	}
}

// The ride page keeps its pre-1.2 cards and charts, plus the "how did it
// feel?" card the coach reads: the saved RPE is picked and the note filled.
func TestWorkoutPage_FeedbackCard(t *testing.T) {
	d, h := ftpFixture(t)
	steadyRide(t, d, "felt", 1, 230)
	rpe := 8
	if err := d.SaveWorkoutFeedback("felt", &rpe, "heavy legs after a bad night"); err != nil {
		t.Fatal(err)
	}
	body := get(t, h, "/workouts/felt")
	for _, want := range []string{
		`<h2 class="card-title">activity</h2>`,
		`<h2 class="card-title">time in power zone</h2>`,
		`id="feel" data-workout="felt"`,
		`class="rpe-btn rpe-hard selected" data-rpe="8"`,
		"heavy legs after a bad night</textarea>",
		`<script src="/static/js/feedback.js" defer></script>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ride page lacks %q", want)
		}
	}
	// The pre-1.2 page set the FTP by re-sending the whole profile and wiped
	// what it didn't know about; the FTP button only sends the FTP.
	if strings.Contains(body, "weight_kg") {
		t.Error("the ride page re-sends the profile to set the FTP")
	}
}
