package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fitbase/fitbase/internal/models"
)

// ReadinessReport's ramp_rate is a 28-day CTL change; the planning rules are
// per week. The coach gets the ramp stated per week, and no bare ramp_rate a
// "~8/week" rule could be misread against.
func TestCoachReadiness_StatesRampPerWeek(t *testing.T) {
	r := coachReadiness(models.ReadinessReport{Fitness: 50, Form: -5, RampRate: 12, Recommendation: "Ease Up", RecommendationTone: "ease"})
	if r.CTLRampPerWeek != 3 || r.CTLChange28d != 12 || r.Recommendation != "Ease Up" {
		t.Errorf("coachReadiness = %+v, want 3/week from a 28-day change of 12, badge kept", r)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{`"ctl_ramp_per_week":3`, `"ctl_change_28d":12`} {
		if !strings.Contains(js, want) {
			t.Errorf("json lacks %s: %s", want, js)
		}
	}
	for _, unwanted := range []string{`"ramp_rate"`, `"recommendation_tone"`} {
		if strings.Contains(js, unwanted) {
			t.Errorf("json still carries %s: %s", unwanted, js)
		}
	}

	if got := coachReadiness(models.ReadinessReport{RampRate: -6}).CTLRampPerWeek; got != -1.5 {
		t.Errorf("ramp per week for a 28-day change of -6 = %v, want -1.5", got)
	}
}

func TestToolReadiness_ReturnsCoachShape(t *testing.T) {
	h := coachFixture(t)
	out, err := h.toolReadiness()
	if err != nil {
		t.Fatalf("toolReadiness: %v", err)
	}
	for _, want := range []string{`"ctl_ramp_per_week"`, `"note"`} {
		if !strings.Contains(out, want) {
			t.Errorf("get_readiness result lacks %s: %s", want, out)
		}
	}
}
