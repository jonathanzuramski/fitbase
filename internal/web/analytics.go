package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/models"
	"github.com/fitbase/fitbase/internal/timeutil"
)

// analyticsView is the Analytics page: one period against another, the years
// against each other, and every marker over time. The page is a shell: the
// first comparison, trends and years are rendered into it as JSON, and
// changing a period or range fetches /api/analytics/* instead of reloading.
// The URL carries the same parameters as the API, so a view can be
// bookmarked and a reload keeps it.
type analyticsView struct {
	Imperial bool
	FTPCheck models.FTPCheck

	Compare models.PeriodComparison
	Trends  models.AnalyticsTrends
	Years   models.YearOverYear
	State   analyticsState

	Ranges     []analyticsOption
	ZoneLegend []zoneShare
}

// analyticsState is what the controls show.
type analyticsState struct {
	Period      string
	Date        string // a day in the period (its first)
	From, To    string // the period's first and last days
	Against     string
	AgainstFrom string // the comparison period's first and last days
	AgainstTo   string
	Scope       string
	Range       string
	Today       string
}

type analyticsOption struct {
	Value, Label string
}

type zoneShare struct {
	N    int // 1-based zone number
	Name string
}

// powerZoneNames are the seven power zones' short names.
var powerZoneNames = [7]string{"Recovery", "Endurance", "Tempo", "Threshold", "VO2 max", "Anaerobic", "Neuromuscular"}

var trendRangeLabels = map[string]string{"3m": "3 months", "6m": "6 months", "1y": "1 year", "2y": "2 years", "all": "all"}

func (th *templateHandler) analytics(w http.ResponseWriter, r *http.Request) {
	tz := timeutil.ViewerLocation(r, th.db.AthleteLocation())
	now := time.Now().In(tz)
	q := r.URL.Query()

	// A bad parameter in a hand-edited URL falls back to the default rather
	// than failing the page; the API answers 400 for the same input.
	c, err := timeutil.ParseComparison(q, now)
	if err != nil {
		c, _ = timeutil.ParseComparison(url.Values{}, now)
	}
	cmp, err := th.db.ComparePeriods(c, q.Get("scope"))
	if errors.Is(err, db.ErrInvalidParam) {
		cmp, err = th.db.ComparePeriods(c, "")
	}
	if err != nil {
		slog.Error("analytics: compare", "err", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	trends, err := th.db.AnalyticsTrends(q.Get("range"), now)
	if errors.Is(err, db.ErrInvalidParam) {
		trends, err = th.db.AnalyticsTrends("", now)
	}
	if err != nil {
		slog.Error("analytics: trends", "err", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	years, err := th.db.YearOverYear(cmp.Scope, now)
	if err != nil {
		slog.Error("analytics: years", "err", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	check, _ := th.db.FTPCheck(now) // the FTP card degrades to no note

	view := analyticsView{
		Imperial: th.isImperial(),
		FTPCheck: check,
		Compare:  cmp,
		Trends:   trends,
		Years:    years,
		State: analyticsState{
			Period: c.Kind, Date: c.A.FirstDay(), From: c.A.FirstDay(), To: c.A.LastDay(),
			Against: c.Against, AgainstFrom: c.B.FirstDay(), AgainstTo: c.B.LastDay(),
			Scope: cmp.Scope, Range: trends.Range, Today: timeutil.DateOf(now).Format("2006-01-02"),
		},
	}
	for _, rk := range db.TrendRanges {
		view.Ranges = append(view.Ranges, analyticsOption{rk, trendRangeLabels[rk]})
	}
	for i, n := range powerZoneNames {
		view.ZoneLegend = append(view.ZoneLegend, zoneShare{N: i + 1, Name: n})
	}
	renderTemplate(w, th.templates().analytics, "base", view)
}
