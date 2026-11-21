package timeutil_test

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/timeutil"
)

// span renders a period as "first..last" for compact comparisons.
func span(p timeutil.Period) string { return p.FirstDay() + ".." + p.LastDay() }

func TestParseComparison(t *testing.T) {
	today := date(2026, 11, 21) // a Saturday
	cases := []struct {
		name   string
		query  string
		a, b   string
		toDate bool
	}{
		// A running month is cut at today, and so is last month: the first
		// 21 days of each.
		{"month to date, previous", "", "2026-11-01..2026-11-21", "2026-10-01..2026-10-21", true},
		{"month to date, a year earlier", "against=year", "2026-11-01..2026-11-21", "2025-11-01..2025-11-21", true},
		// A whole past month against the whole month before: February's 28
		// days against January's 31.
		{"past month", "date=2026-02-10", "2026-02-01..2026-02-28", "2026-01-01..2026-01-31", false},
		{"week to date", "period=week", "2026-11-16..2026-11-21", "2026-11-09..2026-11-14", true},
		// A week a year earlier is 52 weeks back, so it still runs Monday to
		// Sunday.
		{"past week, a year earlier", "period=week&date=2026-03-04&against=year", "2026-03-02..2026-03-08", "2025-03-03..2025-03-09", false},
		{"year to date", "period=year", "2026-01-01..2026-11-21", "2025-01-01..2025-11-21", true},
		{"past year", "period=year&date=2025-06-01", "2025-01-01..2025-12-31", "2024-01-01..2024-12-31", false},
		{"custom, previous", "period=custom&from=2026-09-01&to=2026-09-10", "2026-09-01..2026-09-10", "2026-08-22..2026-08-31", false},
		{"custom running past today", "period=custom&from=2026-11-15&to=2026-11-30", "2026-11-15..2026-11-21", "2026-11-08..2026-11-14", true},
		{"custom against custom", "period=custom&from=2026-09-01&to=2026-09-10&against=custom&against_from=2025-01-01&against_to=2025-03-31",
			"2026-09-01..2026-09-10", "2025-01-01..2025-03-31", false},
		// A period the rider picks by hand is never cut.
		{"month against a picked period", "against=custom&against_from=2026-07-01&against_to=2026-07-31", "2026-11-01..2026-11-21", "2026-07-01..2026-07-31", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q, _ := url.ParseQuery(c.query)
			got, err := timeutil.ParseComparison(q, today)
			if err != nil {
				t.Fatalf("ParseComparison(%q): %v", c.query, err)
			}
			if span(got.A) != c.a || span(got.B) != c.b || got.ToDate != c.toDate {
				t.Errorf("ParseComparison(%q) = A %s, B %s, to date %v; want A %s, B %s, to date %v",
					c.query, span(got.A), span(got.B), got.ToDate, c.a, c.b, c.toDate)
			}
		})
	}
}

// "Today" is the viewer's: 23:30 on November 30 in New York is already
// December 1 in UTC, but the month being looked at is November.
func TestParseComparison_TodayIsTheViewers(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	now := time.Date(2026, 11, 30, 23, 30, 0, 0, ny)
	got, err := timeutil.ParseComparison(url.Values{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if span(got.A) != "2026-11-01..2026-11-30" || got.ToDate {
		t.Errorf("A = %s (to date %v), want the whole of November", span(got.A), got.ToDate)
	}
}

func TestParseComparison_Invalid(t *testing.T) {
	for _, query := range []string{
		"period=fortnight",
		"date=21/11/2026",
		"period=custom", // no range
		"period=custom&from=2026-09-10&to=2026-09-01", // backwards
		"period=custom&from=2000-01-01&to=2026-01-01", // too long
		"against=decade",
		"against=custom&against_from=2026-01-01", // half a range
	} {
		q, _ := url.ParseQuery(query)
		if _, err := timeutil.ParseComparison(q, date(2026, 11, 21)); err == nil {
			t.Errorf("ParseComparison(%q) accepted it, want an error", query)
		}
	}
}

func TestPeriodLabel(t *testing.T) {
	cases := []struct {
		p    timeutil.Period
		want string
	}{
		{timeutil.Period{Start: date(2026, 11, 1), End: date(2026, 12, 1)}, "November 2026"},
		{timeutil.Period{Start: date(2026, 1, 1), End: date(2027, 1, 1)}, "2026"},
		{timeutil.Period{Start: date(2026, 11, 16), End: date(2026, 11, 23)}, "Nov 16–22, 2026"},
		{timeutil.Period{Start: date(2026, 10, 3), End: date(2026, 11, 22)}, "Oct 3 – Nov 21, 2026"},
		{timeutil.Period{Start: date(2025, 12, 29), End: date(2026, 1, 5)}, "Dec 29, 2025 – Jan 4, 2026"},
		{timeutil.Period{Start: date(2026, 11, 21), End: date(2026, 11, 22)}, "Nov 21, 2026"},
	}
	for _, c := range cases {
		if got := c.p.Label(); got != c.want {
			t.Errorf("Label(%s) = %q, want %q", span(c.p), got, c.want)
		}
	}
}

// Across a DST change a period still counts whole days: dates are UTC
// midnights, never local instants.
func TestPeriodDays(t *testing.T) {
	p, err := timeutil.PeriodContaining(timeutil.PeriodMonth, date(2026, 3, 15))
	if err != nil {
		t.Fatal(err)
	}
	if p.Days() != 31 || p.EndDay() != "2026-04-01" {
		t.Errorf("March: %d days ending %s, want 31 ending 2026-04-01", p.Days(), p.EndDay())
	}
	if _, err := timeutil.PeriodContaining("fortnight", date(2026, 3, 15)); err == nil || !strings.Contains(err.Error(), "fortnight") {
		t.Errorf("PeriodContaining(fortnight) = %v, want an error naming it", err)
	}
}
