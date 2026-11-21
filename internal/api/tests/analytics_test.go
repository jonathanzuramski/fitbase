package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/api"
	"github.com/fitbase/fitbase/internal/models"
)

func analyticsGet(t *testing.T, h *api.Handler, handler func(*api.Handler) http.HandlerFunc, target string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	handler(h)(rr, httptest.NewRequest(http.MethodGet, target, nil))
	return rr
}

func compare(h *api.Handler) http.HandlerFunc { return h.GetAnalyticsCompare }
func trends(h *api.Handler) http.HandlerFunc  { return h.GetAnalyticsTrends }
func years(h *api.Handler) http.HandlerFunc   { return h.GetAnalyticsYears }

// GET /api/analytics/compare compares two periods; a custom pair of ranges
// comes back as asked, with a row per metric and the change.
func TestAnalyticsCompare(t *testing.T) {
	h, d := newTestHandler(t)
	insertSampleWorkout(t, d, "march2024") // 2024-03-15, one hour, 36 km
	rr := analyticsGet(t, h, compare,
		"/api/analytics/compare?period=custom&from=2024-03-01&to=2024-03-31&against=custom&against_from=2024-02-01&against_to=2024-02-29")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var c models.PeriodComparison
	if msg := decodeEnvelope(t, rr.Body.Bytes(), &c); msg != "" {
		t.Fatalf("error: %s", msg)
	}
	if c.Period != "custom" || c.A.Label != "March 2024" || c.B.Label != "February 2024" || c.ToDate {
		t.Errorf("periods = %s %q against %q (to date %v), want custom March against February 2024", c.Period, c.A.Label, c.B.Label, c.ToDate)
	}
	if c.A.Activities != 1 || c.A.DistanceMeters != 36000 || c.B.Activities != 0 {
		t.Errorf("A %d activities %.0f m, B %d; want 1, 36000, 0", c.A.Activities, c.A.DistanceMeters, c.B.Activities)
	}
	var found bool
	for _, row := range c.Rows {
		if row.Key == "distance_meters" {
			found = true
			if row.Delta == nil || *row.Delta != 36000 || row.DeltaPct != nil {
				t.Errorf("distance row = %+v, want +36000 m and no percentage of zero", row)
			}
		}
	}
	if !found {
		t.Error("no distance row")
	}
}

// The default is this month to date against last month, for the viewer.
func TestAnalyticsCompare_Default(t *testing.T) {
	h, _ := newTestHandler(t)
	rr := analyticsGet(t, h, compare, "/api/analytics/compare")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var c models.PeriodComparison
	decodeEnvelope(t, rr.Body.Bytes(), &c)
	today := time.Now().UTC().Format("2006-01-02") // the athlete's timezone, UTC by default
	if c.Period != "month" || c.Against != "previous" || c.A.FirstDay[:8] != today[:8] || c.Scope != models.ScopeAll {
		t.Errorf("default = %s against %s from %s in scope %q; want this month against the previous, all sports (no rides yet)",
			c.Period, c.Against, c.A.FirstDay, c.Scope)
	}
}

func TestAnalyticsCompare_BadRequest(t *testing.T) {
	h, _ := newTestHandler(t)
	for _, q := range []string{
		"period=fortnight",
		"period=custom&from=2024-03-31&to=2024-03-01",
		"against=custom",
		"scope=curling",
	} {
		rr := analyticsGet(t, h, compare, "/api/analytics/compare?"+q)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("?%s: %d, want 400", q, rr.Code)
		}
		if msg := decodeEnvelope(t, rr.Body.Bytes(), nil); msg == "" {
			t.Errorf("?%s: no error message", q)
		}
	}
}

func TestAnalyticsTrends(t *testing.T) {
	h, _ := newTestHandler(t)
	rr := analyticsGet(t, h, trends, "/api/analytics/trends?range=6m")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var tr models.AnalyticsTrends
	decodeEnvelope(t, rr.Body.Bytes(), &tr)
	if tr.Range != "6m" || tr.Bucket != "week" || len(tr.Buckets) != 26 {
		t.Errorf("6m = %s in %d %s buckets, want 26 weeks", tr.Range, len(tr.Buckets), tr.Bucket)
	}
	// Empty lists stay lists for a client to range over.
	for _, key := range []string{`"rides":[]`, `"estimate":[]`, `"months":[`} {
		if !strings.Contains(rr.Body.String(), key) {
			t.Errorf("trends lacks %s", key)
		}
	}
	if rr := analyticsGet(t, h, trends, "/api/analytics/trends?range=5y"); rr.Code != http.StatusBadRequest {
		t.Errorf("range=5y: %d, want 400", rr.Code)
	}
}

func TestAnalyticsYears(t *testing.T) {
	h, d := newTestHandler(t)
	insertSampleWorkout(t, d, "march2024")
	rr := analyticsGet(t, h, years, "/api/analytics/years?scope=all")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var y models.YearOverYear
	decodeEnvelope(t, rr.Body.Bytes(), &y)
	if y.Scope != "all" || y.Today.Year != time.Now().Year() {
		t.Errorf("years = %+v", y)
	}
	if rr := analyticsGet(t, h, years, "/api/analytics/years?scope=curling"); rr.Code != http.StatusBadRequest {
		t.Errorf("scope=curling: %d, want 400", rr.Code)
	}
}
