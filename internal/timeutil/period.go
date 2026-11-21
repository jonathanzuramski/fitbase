package timeutil

import (
	"fmt"
	"net/url"
	"time"
)

// Period kinds: what a compared period is. A week is an ISO week (Monday to
// Sunday), a month and a year are calendar ones, and a custom period is any
// run of days.
const (
	PeriodWeek   = "week"
	PeriodMonth  = "month"
	PeriodYear   = "year"
	PeriodCustom = "custom"
)

// What a period is compared against: the period before it, the same period a
// year earlier, or any period the rider picks.
const (
	AgainstPrevious = "previous"
	AgainstYear     = "year"
	AgainstCustom   = "custom"
)

// MaxPeriodDays bounds a custom period, so one request can't ask for a
// century of day-by-day work.
const MaxPeriodDays = 3700

const dayLayout = "2006-01-02"

// Period is a run of whole calendar days, [Start, End). Both are midnights in
// UTC that stand for calendar dates — training_day values, not instants — so
// day arithmetic never meets a DST change, and a day means the day the rider
// rode in their own timezone.
type Period struct {
	Start time.Time
	End   time.Time // the day after the last day
}

// Date is the calendar date y-m-d as a Period endpoint.
func Date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// DateOf is the calendar date of t in t's own location: pass a time already in
// the viewer's timezone to get "today" as they see it.
func DateOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return Date(y, m, d)
}

// ParseDay parses a YYYY-MM-DD day.
func ParseDay(s string) (time.Time, error) {
	return time.Parse(dayLayout, s)
}

// FirstDay is the period's first day, YYYY-MM-DD.
func (p Period) FirstDay() string { return p.Start.Format(dayLayout) }

// LastDay is the period's last day, YYYY-MM-DD.
func (p Period) LastDay() string { return p.End.AddDate(0, 0, -1).Format(dayLayout) }

// EndDay is the day after the period, YYYY-MM-DD: the exclusive bound for a
// training_day query.
func (p Period) EndDay() string { return p.End.Format(dayLayout) }

// Days is the number of days in the period.
func (p Period) Days() int { return int(p.End.Sub(p.Start).Hours()/24 + 0.5) }

// Contains reports whether day falls in the period.
func (p Period) Contains(day time.Time) bool { return !day.Before(p.Start) && day.Before(p.End) }

// PeriodContaining is the week, month or year that day falls in.
func PeriodContaining(kind string, day time.Time) (Period, error) {
	day = DateOf(day)
	switch kind {
	case PeriodWeek:
		start := MondayOf(day)
		return Period{start, start.AddDate(0, 0, 7)}, nil
	case PeriodMonth:
		start := Date(day.Year(), day.Month(), 1)
		return Period{start, start.AddDate(0, 1, 0)}, nil
	case PeriodYear:
		start := Date(day.Year(), 1, 1)
		return Period{start, start.AddDate(1, 0, 0)}, nil
	}
	return Period{}, fmt.Errorf("unknown period %q", kind)
}

// Previous is the period of the same kind just before p: the week, month or
// year before, or for a custom period the same number of days ending the day
// before it starts.
func (p Period) Previous(kind string) Period {
	switch kind {
	case PeriodWeek:
		return Period{p.Start.AddDate(0, 0, -7), p.Start}
	case PeriodMonth:
		return Period{p.Start.AddDate(0, -1, 0), p.Start}
	case PeriodYear:
		return Period{p.Start.AddDate(-1, 0, 0), p.Start}
	}
	return Period{p.Start.AddDate(0, 0, -p.Days()), p.Start}
}

// YearEarlier is the same period a year before. A week moves back 52 weeks,
// so it still runs Monday to Sunday; anything else moves back a calendar
// year (a period starting on February 29 starts on March 1).
func (p Period) YearEarlier(kind string) Period {
	if kind == PeriodWeek {
		return Period{p.Start.AddDate(0, 0, -364), p.End.AddDate(0, 0, -364)}
	}
	return Period{p.Start.AddDate(-1, 0, 0), p.End.AddDate(-1, 0, 0)}
}

// Comparison is a period and the period it is compared against.
type Comparison struct {
	Kind    string // PeriodWeek, PeriodMonth, PeriodYear or PeriodCustom
	Against string // AgainstPrevious, AgainstYear or AgainstCustom
	A       Period // the period looked at
	B       Period // what it is compared against
	// ToDate is set when A was still running and was cut at today: B was cut
	// to match (the first 21 days of last month against the first 21 of
	// this one), so a period in progress isn't measured against a whole one.
	ToDate bool
}

// ParseComparison reads a comparison from query parameters:
//
//	period        week | month | year | custom (default month)
//	date          a day in the period, for week, month and year (default today)
//	from, to      the first and last day of a custom period
//	against       previous | year | custom (default previous)
//	against_from  the first and last day of a custom comparison period
//	against_to
//
// Days are YYYY-MM-DD. today is the viewer's today (DateOf in their
// timezone). A period still running is cut at today, and so is what it is
// compared against, unless that was picked by hand.
func ParseComparison(q url.Values, today time.Time) (Comparison, error) {
	today = DateOf(today)
	c := Comparison{Kind: q.Get("period"), Against: q.Get("against")}
	if c.Kind == "" {
		c.Kind = PeriodMonth
	}
	if c.Against == "" {
		c.Against = AgainstPrevious
	}

	var err error
	switch c.Kind {
	case PeriodWeek, PeriodMonth, PeriodYear:
		day := today
		if s := q.Get("date"); s != "" {
			if day, err = ParseDay(s); err != nil {
				return c, fmt.Errorf("date: want YYYY-MM-DD, got %q", s)
			}
		}
		c.A, _ = PeriodContaining(c.Kind, day)
	case PeriodCustom:
		if c.A, err = parseRange(q.Get("from"), q.Get("to"), "from", "to"); err != nil {
			return c, err
		}
	default:
		return c, fmt.Errorf("period: want week, month, year or custom, got %q", c.Kind)
	}

	full := c.A
	if c.A.Contains(today) && c.A.End.After(today.AddDate(0, 0, 1)) {
		c.A.End = today.AddDate(0, 0, 1)
		c.ToDate = true
	}

	switch c.Against {
	case AgainstPrevious:
		switch c.Kind {
		case PeriodCustom:
			c.B = c.A.Previous(c.Kind) // as long as A, cut or not
		case PeriodYear:
			c.B = c.A.YearEarlier(c.Kind) // the same dates: Jan 1–Nov 21 against Jan 1–Nov 21
		default:
			c.B = full.Previous(c.Kind)
			if c.ToDate { // the first n days against the first n days
				c.B.End = minTime(c.B.End, c.B.Start.AddDate(0, 0, c.A.Days()))
			}
		}
	case AgainstYear:
		c.B = c.A.YearEarlier(c.Kind)
	case AgainstCustom:
		if c.B, err = parseRange(q.Get("against_from"), q.Get("against_to"), "against_from", "against_to"); err != nil {
			return c, err
		}
	default:
		return c, fmt.Errorf("against: want previous, year or custom, got %q", c.Against)
	}
	return c, nil
}

// parseRange reads an inclusive from–to pair of days into a Period.
func parseRange(from, to, fromName, toName string) (Period, error) {
	if from == "" || to == "" {
		return Period{}, fmt.Errorf("%s and %s are required (YYYY-MM-DD)", fromName, toName)
	}
	start, err := ParseDay(from)
	if err != nil {
		return Period{}, fmt.Errorf("%s: want YYYY-MM-DD, got %q", fromName, from)
	}
	last, err := ParseDay(to)
	if err != nil {
		return Period{}, fmt.Errorf("%s: want YYYY-MM-DD, got %q", toName, to)
	}
	if last.Before(start) {
		return Period{}, fmt.Errorf("%s is before %s", toName, fromName)
	}
	p := Period{start, last.AddDate(0, 0, 1)}
	if p.Days() > MaxPeriodDays {
		return Period{}, fmt.Errorf("%s to %s is longer than %d days", fromName, toName, MaxPeriodDays)
	}
	return p, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Label names a period for a column heading: "November 2026" or "2026" for a
// whole month or year, otherwise its days ("Nov 16–22, 2026",
// "Oct 3 – Nov 21, 2026", "Dec 28, 2025 – Jan 3, 2026").
func (p Period) Label() string {
	last := p.End.AddDate(0, 0, -1)
	switch {
	case p.Start.Day() == 1 && p.Start.Month() == time.January && p.End.Equal(p.Start.AddDate(1, 0, 0)):
		return p.Start.Format("2006")
	case p.Start.Day() == 1 && p.End.Equal(p.Start.AddDate(0, 1, 0)):
		return p.Start.Format("January 2006")
	case p.Days() == 1:
		return p.Start.Format("Jan 2, 2006")
	case p.Start.Year() != last.Year():
		return p.Start.Format("Jan 2, 2006") + " – " + last.Format("Jan 2, 2006")
	case p.Start.Month() != last.Month():
		return p.Start.Format("Jan 2") + " – " + last.Format("Jan 2, 2006")
	}
	return p.Start.Format("Jan 2") + "–" + last.Format("2, 2006")
}
