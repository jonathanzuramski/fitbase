// Package timeutil holds the calendar math shared by the db, api, and web
// layers. Training days and weeks are always reckoned in the athlete's local
// timezone (see db.AthleteLocation) — never UTC — so a late-evening ride lands
// on the day the athlete actually rode it. Callers convert to that location
// first, then use these helpers.
package timeutil

import (
	"fmt"
	"time"
)

// LocalMidnight returns midnight on the calendar day of t, in t's location.
func LocalMidnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// DaysSinceMonday returns how many days have elapsed since the Monday that
// opened t's ISO week: 0 for Monday through 6 for Sunday. Go's Weekday is
// Sunday-based (Sunday=0), so the rotation moves Sunday to the end of the week
// where ISO puts it.
func DaysSinceMonday(t time.Time) int {
	return (int(t.Weekday()) + 6) % 7
}

// MondayOf returns midnight on the Monday that opens t's ISO week, in t's
// location. Calendar-day arithmetic keeps it correct across DST boundaries.
func MondayOf(t time.Time) time.Time {
	d := LocalMidnight(t)
	return d.AddDate(0, 0, -DaysSinceMonday(d))
}

// ISOWeekLabel formats t's ISO week as "2026-W33". The ISO year is not always
// the calendar year — 2027-01-01 falls in 2026-W53 — which is why the year
// comes from ISOWeek and never from t.Year().
func ISOWeekLabel(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, w)
}
