package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/aicoach"
	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
)

func TestFTPCheckLine(t *testing.T) {
	lower := fitness.CheckFTP(245, &models.FTPEffort{DurationSecs: 1200, Watts: 226, Day: "2026-09-26"}, nil)
	cases := []struct {
		name  string
		check models.FTPCheck
		want  []string
	}{
		{"lower names the evidence and how to act", lower,
			[]string{"- FTP check: no recent ride backs this FTP", "best 20 min in the last 90 days 226 W (2026-09-26)",
				"estimate 215 W, 88% of FTP", "anchor interval targets on recent efforts"}},
		{"raise", fitness.CheckFTP(245, nil, &models.FTPEffort{DurationSecs: 3600, Watts: 260, Day: "2026-11-01"}),
			[]string{"recent rides show more than this FTP", "best 60 min", "overstate effort"}},
		{"ok", fitness.CheckFTP(245, &models.FTPEffort{DurationSecs: 1200, Watts: 255, Day: "2026-11-01"}, nil),
			[]string{"backed by a recent effort"}},
		{"indoor effort says which line it was scored on",
			fitness.CheckFTP(250, &models.FTPEffort{DurationSecs: 1200, Watts: 238, Day: "2026-11-01",
				Indoor: true, IndoorFTPWatts: 230, EquivalentWatts: 259}, nil),
			[]string{"backed by a recent effort", "238 W on an indoor ride, against an indoor FTP of 230 W (259 W against this FTP)",
				"estimate 246 W, 98% of FTP"}},
		{"unverified", fitness.CheckFTP(245, nil, nil),
			[]string{"unverified: no hard 20- or 60-minute effort in the last 90 days", "%FTP targets as approximate"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ftpCheckLine(c.check)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("line missing %q:\n%s", w, got)
				}
			}
			if !strings.HasSuffix(got, "\n") || strings.Contains(got, "%!") {
				t.Errorf("malformed line: %q", got)
			}
		})
	}

	lower.Dismissed = true
	if got := ftpCheckLine(lower); !strings.Contains(got, "chosen to keep this FTP") {
		t.Errorf("a dismissed check should tell the coach not to press: %s", got)
	}
}

// The verdict reaches every coach run through the shared system context.
func TestChatSystemContext_CarriesFTPCheck(t *testing.T) {
	h := coachFixture(t)
	ctx := h.chatSystemContext(time.Now())
	if !strings.Contains(ctx, "- FTP: 250 W\n- ") || !strings.Contains(ctx, "- FTP check: unverified") {
		t.Errorf("system context lacks the FTP check:\n%s", ctx)
	}
}

// The coach reads the rider's own account of a ride in the list and detail
// tools.
func TestCoachTools_RiderFeedback(t *testing.T) {
	h := coachFixture(t)
	w := &models.Workout{ID: "fb1", Filename: "fb1.fit", Sport: "cycling", RecordedAt: time.Now().Add(-24 * time.Hour),
		DurationSecs: 3600, CreatedAt: time.Now()}
	if err := h.db.InsertWorkout(w, nil); err != nil {
		t.Fatal(err)
	}
	rpe := 9
	if err := h.db.SaveWorkoutFeedback("fb1", &rpe, "felt flat, short night"); err != nil {
		t.Fatal(err)
	}
	list, err := h.toolListRecentWorkouts([]byte(`{"days":7}`))
	if err != nil || !strings.Contains(list, `"rpe":9`) || !strings.Contains(list, `"rider_note":"felt flat, short night"`) {
		t.Errorf("list_recent_workouts lacks the feedback (%v):\n%s", err, list)
	}
	detail, err := h.toolWorkoutDetail([]byte(`{"id":"fb1"}`))
	if err != nil || !strings.Contains(detail, `"rider_feedback":{"note":"felt flat, short night","rpe":9,"rpe_label":"max effort"}`) {
		t.Errorf("get_workout_detail lacks the feedback (%v):\n%s", err, detail)
	}
}

// The FTP history the coach reads mixes two lines: the general FTP and, for a
// rider with a separate indoor FTP, the indoor one. Read as one list, general
// 242 → indoor 235 → general 250 says "FTP fell 7 W" and then "rose 15 W";
// the real general-line gain is 8 W. Each entry carries its change from the
// previous entry on its own line, and the model is told to stay within a line.
func TestCoachFTPHistory_ComparesWithinALine(t *testing.T) {
	h := coachFixture(t)
	if err := h.db.ClearFTPHistory(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, e := range []struct {
		ftp     int
		daysAgo int
		indoor  bool
	}{{242, 80, false}, {235, 78, true}, {250, 20, false}} {
		if err := h.db.LogFTPChangeAtFor(e.ftp, now.AddDate(0, 0, -e.daysAgo), e.indoor); err != nil {
			t.Fatal(err)
		}
	}
	type point struct {
		FTPWatts   int  `json:"ftp_watts"`
		Indoor     bool `json:"indoor"`
		DeltaWatts *int `json:"delta_watts"`
	}
	check := func(source string, got []point) {
		t.Helper()
		if len(got) != 3 {
			t.Fatalf("%s: %d entries, want 3: %+v", source, len(got), got)
		}
		if got[0].DeltaWatts != nil || !got[1].Indoor || got[1].DeltaWatts != nil {
			t.Errorf("%s: the first entry on each line has no delta: %+v %+v", source, got[0], got[1])
		}
		if got[2].DeltaWatts == nil || *got[2].DeltaWatts != 8 {
			t.Errorf("%s: 250 W delta = %v, want +8 (from 242 on the general line)", source, got[2].DeltaWatts)
		}
	}

	data, err := h.collectCoachingData()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(data.FTPHistory)
	var insights []point
	if err := json.Unmarshal(raw, &insights); err != nil {
		t.Fatal(err)
	}
	check("insights ftp_history", insights)
	if !strings.Contains(aicoach.SystemPrompt, "Entries in ftp_history marked indoor") {
		t.Error("the insights prompt doesn't say how to read indoor entries")
	}

	out, err := h.toolPerformanceTrend([]byte(`{"days":90}`))
	if err != nil {
		t.Fatal(err)
	}
	var trend struct {
		Changes []point `json:"ftp_changes_in_window"`
		Note    string  `json:"note"`
	}
	if err := json.Unmarshal([]byte(out), &trend); err != nil {
		t.Fatal(err)
	}
	check("get_performance_trend", trend.Changes)
	if !strings.Contains(trend.Note, "marked indoor") {
		t.Errorf("get_performance_trend note doesn't explain indoor entries: %s", trend.Note)
	}

	out, err = h.toolFTPHistory()
	if err != nil {
		t.Fatal(err)
	}
	var history struct {
		Changes []point `json:"changes"`
	}
	if err := json.Unmarshal([]byte(out), &history); err != nil {
		t.Fatal(err)
	}
	check("get_ftp_history", history.Changes)
}
