package api

import (
	"strings"
	"testing"
	"time"

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
