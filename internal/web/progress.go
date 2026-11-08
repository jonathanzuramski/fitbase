package web

import (
	"math"
	"net/http"
	"time"

	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
	"github.com/fitbase/fitbase/internal/timeutil"
)

// progressView is the Progress page: whether the rider is getting fitter, not
// just training more. FTP against what the rides say, the power curve now
// against all-time, aerobic durability, weekly volume against plan, and how
// the hard and easy riding is split.
type progressView struct {
	Imperial bool
	FTP      int
	FTPCheck models.FTPCheck

	FTPChart   ftpChart
	Curve      curveChart
	Weeks      []progressWeek
	WeekMaxTSS float64
	Durability []durabilityPoint
	Zones      []zoneWindow
	ZoneLegend []zoneShare
}

// ftpChart is the FTP history (general and indoor lines, as step points) and,
// every two weeks, the FTP the previous 90 days' best efforts imply.
type ftpChart struct {
	General  []ftpPoint `json:"general"`
	Indoor   []ftpPoint `json:"indoor"`
	Estimate []ftpPoint `json:"estimate"`
}

type ftpPoint struct {
	T int64 `json:"t"` // unix seconds
	W int   `json:"w"`
}

// curveChart is best power by duration: all-time, last 90 days, last 28 days.
type curveChart struct {
	Durations []int   `json:"durations"`
	AllTime   []*int  `json:"all_time"`
	Days90    []*int  `json:"days_90"`
	Days28    []*int  `json:"days_28"`
	WeightKG  float64 `json:"weight_kg"`
}

// progressWeek is one week of the volume chart.
type progressWeek struct {
	Monday     time.Time
	TSS        float64
	PlannedTSS float64
	Secs       int
	Rides      int
	IsCurrent  bool
}

func (w progressWeek) DoneHeightPct(maxTSS float64) float64    { return pctOf(w.TSS, maxTSS) }
func (w progressWeek) PlannedHeightPct(maxTSS float64) float64 { return pctOf(w.PlannedTSS, maxTSS) }

// durabilityPoint is one week of aerobic markers: EF (NP ÷ avg HR) averaged
// over endurance rides, so weeks compare at similar intensity, and Pw:HR
// decoupling averaged over long steady rides.
type durabilityPoint struct {
	T          int64    `json:"t"`
	EF         *float64 `json:"ef"`
	Decoupling *float64 `json:"dec"`
}

// zoneWindow is the share of power-zone time over a window, with the easy /
// middle / hard split that says whether training is polarized.
type zoneWindow struct {
	Label    string
	Workouts int
	Zones    []zoneShare
	Easy     float64 // Z1–Z2
	Middle   float64 // Z3–Z4
	Hard     float64 // Z5+
}

type zoneShare struct {
	N    int // 1-based zone number
	Name string
	Pct  float64
}

const (
	progressWeeks = 16
	ftpChartWeeks = 52

	// Endurance rides for EF: steady enough that weeks compare at a similar
	// intensity, long enough for HR to settle.
	efMinIF, efMaxIF = 0.55, 0.80
	efMinSecs        = 45 * 60
	// Long steady rides for decoupling, where drift is meaningful.
	decouplingMinSecs = 90 * 60
)

// curveDurations are the power-curve points charted, short to long.
var curveDurations = []int{5, 10, 30, 60, 120, 300, 600, 1200, 1800, 3600, 5400, 7200}

func (th *templateHandler) progress(w http.ResponseWriter, r *http.Request) {
	athlete, err := th.db.GetAthlete()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	tz := timeutil.ViewerLocation(r, th.db.AthleteLocation())
	now := time.Now().In(tz)

	// Every section degrades on its own: a failed query leaves it empty.
	check, _ := th.db.FTPCheck(now)
	view := progressView{
		Imperial: th.isImperial(),
		FTP:      athlete.FTPWatts,
		FTPCheck: check,
		FTPChart: th.buildFTPChart(now),
		Curve:    th.buildCurve(now, athlete.WeightKG),
	}
	view.Weeks, view.Durability = th.buildProgressWeeks(now)
	for _, wk := range view.Weeks {
		view.WeekMaxTSS = max(view.WeekMaxTSS, wk.TSS, wk.PlannedTSS)
	}
	for _, win := range []struct {
		label string
		days  int
	}{{"last 4 weeks", 28}, {"last 12 weeks", 84}} {
		if totals, err := th.db.GetRecentZoneTotals(win.days); err == nil {
			view.Zones = append(view.Zones, zoneWindowFrom(win.label, totals.Workouts, totals.Power))
		}
	}
	for i, n := range powerZoneNames {
		view.ZoneLegend = append(view.ZoneLegend, zoneShare{N: i + 1, Name: n})
	}
	renderTemplate(w, th.templates().progress, "base", view)
}

func (th *templateHandler) buildFTPChart(now time.Time) ftpChart {
	var c ftpChart
	start := now.AddDate(0, 0, -7*ftpChartWeeks)
	hist, err := th.db.AllFTPHistory() // newest first
	if err != nil {
		return c
	}
	// Each line starts with the value in effect at the window's start, then
	// its changes, then holds to now.
	line := func(indoor bool) []ftpPoint {
		var pts []ftpPoint
		before := 0 // the FTP in effect when the window opens, if any
		for i := len(hist) - 1; i >= 0; i-- {
			e := hist[i]
			if e.Indoor != indoor {
				continue
			}
			if e.EffectiveFrom.Before(start) {
				before = e.FTPWatts
				continue
			}
			if len(pts) == 0 && before > 0 {
				pts = append(pts, ftpPoint{start.Unix(), before})
			}
			pts = append(pts, ftpPoint{e.EffectiveFrom.Unix(), e.FTPWatts})
		}
		if len(pts) == 0 && before > 0 {
			pts = append(pts, ftpPoint{start.Unix(), before})
		}
		if len(pts) > 0 {
			pts = append(pts, ftpPoint{now.Unix(), pts[len(pts)-1].W})
		}
		return pts
	}
	c.General = line(false)
	c.Indoor = line(true)

	for d := start; !d.After(now); d = d.AddDate(0, 0, 14) {
		curve, err := th.db.GetPowerCurveBetween(d.AddDate(0, 0, -fitness.FTPCheckWindowDays), d)
		if err != nil {
			continue
		}
		var best20, best60 *models.FTPEffort
		if b, ok := curve[1200]; ok {
			best20 = &models.FTPEffort{DurationSecs: 1200, Watts: b.Watts}
		}
		if b, ok := curve[3600]; ok {
			best60 = &models.FTPEffort{DurationSecs: 3600, Watts: b.Watts}
		}
		if est, basis := fitness.EstimateFTP(best20, best60); basis != nil {
			c.Estimate = append(c.Estimate, ftpPoint{d.Unix(), int(math.Round(est))})
		}
	}
	return c
}

func (th *templateHandler) buildCurve(now time.Time, weightKG float64) curveChart {
	c := curveChart{Durations: curveDurations, WeightKG: weightKG}
	pick := func(curve map[int]models.AllTimeBest) []*int {
		out := make([]*int, len(curveDurations))
		for i, d := range curveDurations {
			if b, ok := curve[d]; ok && b.Watts > 0 {
				w := b.Watts
				out[i] = &w
			}
		}
		return out
	}
	if all, err := th.db.GetAllTimePowerCurve(); err == nil {
		c.AllTime = pick(all)
	}
	if d90, err := th.db.GetPowerCurveSince(now.AddDate(0, 0, -90)); err == nil {
		c.Days90 = pick(d90)
	}
	if d28, err := th.db.GetPowerCurveSince(now.AddDate(0, 0, -28)); err == nil {
		c.Days28 = pick(d28)
	}
	return c
}

// buildProgressWeeks buckets the last progressWeeks weeks of rides (and
// planned workouts) by training week, and computes each week's EF and
// decoupling.
func (th *templateHandler) buildProgressWeeks(now time.Time) ([]progressWeek, []durabilityPoint) {
	thisMonday := timeutil.MondayOf(now)
	first := thisMonday.AddDate(0, 0, -7*(progressWeeks-1))
	weeks := make([]progressWeek, progressWeeks)
	byMonday := map[string]int{}
	for i := range weeks {
		m := first.AddDate(0, 0, 7*i)
		weeks[i] = progressWeek{Monday: m, IsCurrent: i == progressWeeks-1}
		byMonday[m.Format("2006-01-02")] = i
	}
	weekOf := func(day string) (int, bool) {
		d, err := time.ParseInLocation("2006-01-02", day, now.Location())
		if err != nil {
			return 0, false
		}
		i, ok := byMonday[timeutil.MondayOf(d).Format("2006-01-02")]
		return i, ok
	}

	type agg struct {
		efSum, decSum float64
		efN, decN     int
	}
	aggs := make([]agg, progressWeeks)
	end := thisMonday.AddDate(0, 0, 7).Format("2006-01-02")
	if rides, err := th.db.GetWorkoutsBetweenDays(first.Format("2006-01-02"), end); err == nil {
		for _, w := range rides {
			i, ok := weekOf(w.TrainingDay)
			if !ok {
				continue
			}
			wk := &weeks[i]
			wk.Rides++
			wk.Secs += w.DurationSecs
			if w.TSS != nil {
				wk.TSS += *w.TSS
			}
			if w.IntensityFactor == nil {
				continue
			}
			ifac := *w.IntensityFactor
			if ifac >= efMinIF && ifac <= efMaxIF && w.DurationSecs >= efMinSecs {
				if ef, ok := fitness.EfficiencyFactor(w.NormalizedPower, w.AvgHeartRate); ok {
					aggs[i].efSum += ef
					aggs[i].efN++
				}
			}
			if ifac <= efMaxIF && w.DurationSecs >= decouplingMinSecs && w.AvgHeartRate != nil {
				if dec, ok := th.db.AerobicDecoupling(w.ID); ok {
					aggs[i].decSum += dec
					aggs[i].decN++
				}
			}
		}
	}
	firstDate := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, time.UTC)
	if planned, err := th.db.ListPlannedWorkoutsBetween(firstDate, firstDate.AddDate(0, 0, 7*progressWeeks-1)); err == nil {
		for _, p := range planned {
			if i, ok := weekOf(p.PlannedDate.Format("2006-01-02")); ok {
				weeks[i].PlannedTSS += plannedTSS(p)
			}
		}
	}

	var points []durabilityPoint
	for i, a := range aggs {
		p := durabilityPoint{T: weeks[i].Monday.Unix()}
		if a.efN > 0 {
			v := math.Round(a.efSum/float64(a.efN)*100) / 100
			p.EF = &v
		}
		if a.decN > 0 {
			v := math.Round(a.decSum/float64(a.decN)*10) / 10
			p.Decoupling = &v
		}
		points = append(points, p)
	}
	return weeks, points
}

// powerZoneNames are the seven power zones' short names.
var powerZoneNames = [7]string{"Recovery", "Endurance", "Tempo", "Threshold", "VO2 max", "Anaerobic", "Neuromuscular"}

func zoneWindowFrom(label string, workouts int, secs [7]int) zoneWindow {
	z := zoneWindow{Label: label, Workouts: workouts}
	var total int
	for _, s := range secs {
		total += s
	}
	if total == 0 {
		return z
	}
	pct := func(s int) float64 { return math.Round(float64(s)/float64(total)*1000) / 10 }
	for i, s := range secs {
		z.Zones = append(z.Zones, zoneShare{N: i + 1, Name: powerZoneNames[i], Pct: pct(s)})
	}
	z.Easy = pct(secs[0] + secs[1])
	z.Middle = pct(secs[2] + secs[3])
	z.Hard = pct(secs[4] + secs[5] + secs[6])
	return z
}
