package timeutil_test

import (
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/timeutil"
)

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestDaysSinceMonday(t *testing.T) {
	// 2026-08-10 is a Monday. Sunday must land at 6, not 0 — Go's Weekday is
	// Sunday-based, which is the whole reason this helper exists.
	cases := []struct {
		in   time.Time
		want int
	}{
		{date(2026, 8, 10), 0}, // Mon
		{date(2026, 8, 11), 1}, // Tue
		{date(2026, 8, 15), 5}, // Sat
		{date(2026, 8, 16), 6}, // Sun
	}
	for _, c := range cases {
		if got := timeutil.DaysSinceMonday(c.in); got != c.want {
			t.Errorf("DaysSinceMonday(%s /%s): got %d want %d",
				c.in.Format("2006-01-02"), c.in.Weekday(), got, c.want)
		}
	}
}

func TestMondayOf(t *testing.T) {
	monday := date(2026, 8, 10)
	// Every day of that ISO week resolves to the same Monday, Sunday included.
	for i := 0; i < 7; i++ {
		d := monday.AddDate(0, 0, i)
		if got := timeutil.MondayOf(d); !got.Equal(monday) {
			t.Errorf("MondayOf(%s /%s): got %s want %s",
				d.Format("2006-01-02"), d.Weekday(),
				got.Format("2006-01-02"), monday.Format("2006-01-02"))
		}
	}
}

func TestMondayOfTruncatesTime(t *testing.T) {
	// A mid-afternoon timestamp must still yield midnight.
	in := time.Date(2026, 8, 12, 15, 47, 3, 0, time.UTC)
	got := timeutil.MondayOf(in)
	if !got.Equal(date(2026, 8, 10)) {
		t.Errorf("got %s want 2026-08-10T00:00:00Z", got.Format(time.RFC3339))
	}
}

func TestMondayOfPreservesLocation(t *testing.T) {
	tz, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	// 21:00 EDT on Wed 2026-08-12 is already Thu in UTC. Bucketing must follow
	// the athlete's local week, so this belongs to the Monday of Aug 10 local.
	in := time.Date(2026, 8, 12, 21, 0, 0, 0, tz)
	got := timeutil.MondayOf(in)
	if got.Location() != tz {
		t.Errorf("location: got %v want %v", got.Location(), tz)
	}
	if y, m, d := got.Date(); y != 2026 || m != time.August || d != 10 {
		t.Errorf("got %s want 2026-08-10 local", got.Format(time.RFC3339))
	}
}

func TestISOWeekLabel(t *testing.T) {
	cases := []struct {
		in   time.Time
		want string
	}{
		{date(2026, 8, 15), "2026-W33"},
		{date(2026, 1, 5), "2026-W02"}, // zero-padded, not "2026-W2"
		// ISO year diverges from calendar year across the New Year boundary.
		{date(2026, 12, 28), "2026-W53"},
		{date(2027, 1, 1), "2026-W53"},
		{date(2027, 1, 4), "2027-W01"},
	}
	for _, c := range cases {
		if got := timeutil.ISOWeekLabel(c.in); got != c.want {
			t.Errorf("ISOWeekLabel(%s): got %q want %q", c.in.Format("2006-01-02"), got, c.want)
		}
	}
}

func TestLocalMidnight(t *testing.T) {
	in := time.Date(2026, 8, 12, 23, 59, 59, 999, time.UTC)
	got := timeutil.LocalMidnight(in)
	if !got.Equal(date(2026, 8, 12)) {
		t.Errorf("got %s want 2026-08-12T00:00:00Z", got.Format(time.RFC3339))
	}
}
