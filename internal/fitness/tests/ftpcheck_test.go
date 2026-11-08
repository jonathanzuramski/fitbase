package fitness_test

import (
	"testing"

	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
)

func effort(secs, watts int) *models.FTPEffort {
	return &models.FTPEffort{DurationSecs: secs, Watts: watts, WorkoutID: "w"}
}

func TestCheckFTP(t *testing.T) {
	cases := []struct {
		name           string
		ftp            int
		best20, best60 *models.FTPEffort
		status         string
		estimate       int
		basisSecs      int
	}{
		// The rider's real case: FTP 245, best recent 20 min 226 W → 215 W.
		{"modest shortfall may be high", 245, effort(1200, 226), effort(3600, 199), models.FTPCheckLower, 215, 1200},
		{"backed by a recent effort", 245, effort(1200, 250), nil, models.FTPCheckOK, 238, 1200},
		{"at the raise bar is still ok", 200, effort(1200, 216), nil, models.FTPCheckOK, 205, 1200}, // 102.6%
		{"clearly above proves it low", 245, effort(1200, 272), nil, models.FTPCheckRaise, 258, 1200},
		// A big gap more likely means no hard rides than lost fitness: no number.
		{"large shortfall is unverified", 245, effort(1200, 180), effort(3600, 150), models.FTPCheckUnverified, 171, 1200},
		{"60-minute effort wins when higher", 220, effort(1200, 220), effort(3600, 214), models.FTPCheckOK, 214, 3600},
		{"only a 60-minute effort", 220, nil, effort(3600, 200), models.FTPCheckLower, 200, 3600},
		{"no efforts", 245, nil, nil, models.FTPCheckUnverified, 0, 0},
		{"no FTP set", 0, effort(1200, 250), nil, models.FTPCheckUnverified, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := fitness.CheckFTP(c.ftp, c.best20, c.best60)
			if got.Status != c.status || got.EstimateWatts != c.estimate {
				t.Fatalf("CheckFTP = %s, %d W; want %s, %d W", got.Status, got.EstimateWatts, c.status, c.estimate)
			}
			if got.FTPWatts != c.ftp || got.WindowDays != fitness.FTPCheckWindowDays {
				t.Errorf("FTPWatts %d, WindowDays %d", got.FTPWatts, got.WindowDays)
			}
			if c.basisSecs == 0 {
				if got.Basis != nil {
					t.Errorf("basis = %+v, want none", got.Basis)
				}
				return
			}
			if got.Basis == nil || got.Basis.DurationSecs != c.basisSecs {
				t.Errorf("basis = %+v, want the %ds effort", got.Basis, c.basisSecs)
			}
		})
	}
}

func TestCheckFTP_PctOfFTP(t *testing.T) {
	got := fitness.CheckFTP(245, effort(1200, 226), nil)
	if got.PctOfFTP != 87.6 { // 214.7 / 245
		t.Errorf("PctOfFTP = %v, want 87.6", got.PctOfFTP)
	}
	if !got.Actionable() {
		t.Error("a lower verdict should be actionable")
	}
	if fitness.CheckFTP(245, effort(1200, 250), nil).Actionable() {
		t.Error("an ok verdict should not be actionable")
	}
}

// The single-ride banner and the check share one bar: a ride estimating a
// watt or two over FTP is noise, not a new FTP.
func TestEstimateRaisesFTP(t *testing.T) {
	if fitness.EstimateRaisesFTP(247, 245) {
		t.Error("247 W should not raise a 245 W FTP")
	}
	if !fitness.EstimateRaisesFTP(253, 245) {
		t.Error("253 W (103.3%) should raise a 245 W FTP")
	}
	if fitness.EstimateRaisesFTP(300, 0) {
		t.Error("no FTP set: nothing to raise")
	}
}
