package db

import (
	"database/sql"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
	"github.com/fitbase/fitbase/internal/timeutil"
)

// TrendRanges are the ranges AnalyticsTrends covers: three or six months in
// weeks, one or two years or everything in months.
var TrendRanges = []string{"3m", "6m", "1y", "2y", "all"}

// DefaultTrendRange is the range shown first.
const DefaultTrendRange = "1y"

const (
	bucketWeek  = "week"
	bucketMonth = "month"
	// ftpEstimateWindowDays is how far back each FTP estimate looks, as the
	// FTP check does (fitness.FTPCheckWindowDays).
	ftpEstimateWindowDays = fitness.FTPCheckWindowDays
)

// trendWindow is the first day a range covers and how it is bucketed. Ranges
// start on a bucket boundary, so every bucket is whole but the current one.
func trendWindow(rangeKey string, today, first time.Time) (time.Time, string, error) {
	monthStart := timeutil.Date(today.Year(), today.Month(), 1)
	switch rangeKey {
	case "3m":
		return timeutil.MondayOf(today).AddDate(0, 0, -7*12), bucketWeek, nil
	case "6m":
		return timeutil.MondayOf(today).AddDate(0, 0, -7*25), bucketWeek, nil
	case "1y":
		return monthStart.AddDate(0, -11, 0), bucketMonth, nil
	case "2y":
		return monthStart.AddDate(0, -23, 0), bucketMonth, nil
	case "all":
		if first.IsZero() || first.After(today) {
			return monthStart.AddDate(0, -11, 0), bucketMonth, nil
		}
		return timeutil.Date(first.Year(), first.Month(), 1), bucketMonth, nil
	}
	return time.Time{}, "", fmt.Errorf("%w: range: want one of 3m, 6m, 1y, 2y, all; got %q", ErrInvalidParam, rangeKey)
}

// bucketOf is the first day of the week or month day falls in.
func bucketOf(day time.Time, bucket string) time.Time {
	if bucket == bucketWeek {
		return timeutil.MondayOf(day)
	}
	return timeutil.Date(day.Year(), day.Month(), 1)
}

func nextBucket(start time.Time, bucket string) time.Time {
	if bucket == bucketWeek {
		return start.AddDate(0, 0, 7)
	}
	return start.AddDate(0, 1, 0)
}

// bucketStarts lists the buckets from the one holding from to the one
// holding today.
func bucketStarts(from, today time.Time, bucket string) []time.Time {
	var out []time.Time
	for b := bucketOf(from, bucket); !b.After(today); b = nextBucket(b, bucket) {
		out = append(out, b)
	}
	return out
}

func (db *DB) firstTrainingDay() (time.Time, error) {
	var first sql.NullString
	if err := db.QueryRow(`SELECT MIN(training_day) FROM workouts WHERE training_day IS NOT NULL`).Scan(&first); err != nil {
		return time.Time{}, err
	}
	if !first.Valid {
		return time.Time{}, nil
	}
	return timeutil.ParseDay(first.String)
}

// trendAcc gathers one bucket's rides.
type trendAcc struct {
	ef          [2]mean
	match       matchSamples
	dec, rpe    mean
	cadence     [2]weighted
	ifBands     [5]int
	zones       [7]int
	tss         float64
	cycling     map[string]bool // day → a ride that day was cycling
	firstInside time.Time       // the bucket's first day inside the range
	endInside   time.Time       // the day after its last day inside the range
}

// AnalyticsTrends charts every marker over a range (TrendRanges; "" for the
// default) ending today, now's date in the viewer's timezone (now's
// location). Load and consistency count every sport; power-derived markers
// read cycling rides only.
func (db *DB) AnalyticsTrends(rangeKey string, now time.Time) (models.AnalyticsTrends, error) {
	if rangeKey == "" {
		rangeKey = DefaultTrendRange
	}
	tz := now.Location()
	today := timeutil.DateOf(now)
	first, err := db.firstTrainingDay()
	if err != nil {
		return models.AnalyticsTrends{}, err
	}
	from, bucket, err := trendWindow(rangeKey, today, first)
	if err != nil {
		return models.AnalyticsTrends{}, err
	}
	end := today.AddDate(0, 0, 1)
	out := models.AnalyticsTrends{
		Range: rangeKey, FirstDay: from.Format(dayFmt), LastDay: today.Format(dayFmt), Bucket: bucket,
		Buckets: []models.TrendBucket{}, Rides: []models.TrendRide{}, Ramp: []models.RampPoint{},
	}
	if a, err := db.GetAthlete(); err == nil {
		out.WeightKG = a.WeightKG
	}

	// Rides from the start of the first week and of the first month, so the
	// weekly and monthly load totals are whole.
	loadFrom := timeutil.MondayOf(from)
	if m := bucketOf(from, bucketMonth); m.Before(loadFrom) {
		loadFrom = m
	}
	rides, err := db.analyticsRides(loadFrom.Format(dayFmt), end.Format(dayFmt))
	if err != nil {
		return out, err
	}

	// ── Markers by bucket ──
	starts := bucketStarts(from, today, bucket)
	accs := make([]trendAcc, len(starts))
	index := map[string]int{}
	for i, s := range starts {
		index[s.Format(dayFmt)] = i
		accs[i].cycling = map[string]bool{}
		accs[i].firstInside = maxTime(s, from)
		accs[i].endInside = minTime(nextBucket(s, bucket), end)
	}
	var rangeSamples matchSamples
	activeDays := map[string]bool{}
	for i := range rides {
		r := &rides[i]
		day, err := timeutil.ParseDay(r.Day)
		if err != nil || day.Before(from) {
			continue
		}
		bi, ok := index[bucketOf(day, bucket).Format(dayFmt)]
		if !ok {
			continue
		}
		acc := &accs[bi]
		activeDays[r.Day] = true
		acc.cycling[r.Day] = acc.cycling[r.Day] || r.Sport == "cycling"
		if r.TSS != nil {
			acc.tss += *r.TSS
		}
		tr := models.TrendRide{ID: r.ID, Day: r.Day, Indoor: r.Indoor, TSS: r.TSS, RPE: r.RPE}
		if r.RPE != nil {
			acc.rpe.add(float64(*r.RPE))
		}
		if r.power() {
			if r.IF != nil {
				acc.ifBands[fitness.IFBand(*r.IF)]++
			}
			if r.PowerZones != nil {
				for z, secs := range r.PowerZones {
					acc.zones[z] += secs
				}
			}
			if r.Cadence != nil && *r.Cadence > 0 {
				acc.cadence[r.env()].add(float64(*r.Cadence), r.Secs)
			}
			if ef, ok := r.ef(); ok {
				acc.ef[r.env()].add(ef)
				tr.EF = ptrFloat(round(ef, 3))
			}
			if d, ok := r.durability(); ok {
				acc.dec.add(d)
				tr.DecouplingPct = ptrFloat(round(d, 1))
			}
			acc.match.add(r)
			rangeSamples.add(r)
		}
		if tr.EF != nil || tr.DecouplingPct != nil || tr.RPE != nil {
			out.Rides = append(out.Rides, tr)
		}
	}
	refs := rangeSamples.refs()
	out.Reference = refs.model()
	for i := range accs {
		acc := &accs[i]
		b := models.TrendBucket{
			Start:         starts[i].Format(dayFmt),
			EF:            models.EnvMarkers{Outdoor: acc.ef[0].marker(3), Indoor: acc.ef[1].marker(3)},
			DecouplingPct: acc.dec.marker(1),
			IFBands:       acc.ifBands,
			Split:         split(acc.zones),
			AvgRPE:        acc.rpe.marker(2),
			TSS:           round(acc.tss, 1),
			Days:          int(acc.endInside.Sub(acc.firstInside).Hours()/24 + 0.5),
		}
		b.HRAtPower, b.PowerAtHR = acc.match.markers(refs)
		for env := range 2 {
			if v := acc.cadence[env].value(1); v != nil {
				m := &models.Marker{Value: *v, Rides: acc.cadence[env].n}
				if env == 0 {
					b.CadenceRPM.Outdoor = m
				} else {
					b.CadenceRPM.Indoor = m
				}
			}
		}
		for _, cyc := range acc.cycling {
			if cyc {
				b.CyclingDays++
			} else {
				b.OtherDays++
			}
		}
		out.Buckets = append(out.Buckets, b)
	}

	// ── Load, weekly and monthly, against what was planned ──
	planEnd := timeutil.MondayOf(today).AddDate(0, 0, 6)
	if m := bucketOf(today, bucketMonth).AddDate(0, 1, -1); m.After(planEnd) {
		planEnd = m
	}
	planned, err := db.ListPlannedWorkoutsBetween(loadFrom, planEnd)
	if err != nil {
		return out, err
	}
	out.LoadWeeks = loadBuckets(rides, planned, timeutil.MondayOf(from), today, bucketWeek)
	out.LoadMonths = loadBuckets(rides, planned, bucketOf(from, bucketMonth), today, bucketMonth)

	// ── Fitness and ramp ──
	days := int(today.Sub(from).Hours()/24 + 0.5)
	fitnessPts, err := db.GetFitnessHistory(days, tz)
	if err != nil {
		return out, err
	}
	out.Fitness = fitnessPts
	if out.Fitness == nil {
		out.Fitness = []models.FitnessPoint{}
	}
	out.Ramp = weeklyRamp(fitnessPts, from, today)

	// ── FTP ──
	if out.FTP, err = db.ftpTrend(from, today, now); err != nil {
		return out, err
	}

	// ── Power curve, now against all-time ──
	out.Curve = models.PowerCurveTrend{Durations: CurveDurations}
	allTime, err := db.GetAllTimePowerCurve()
	if err != nil {
		return out, err
	}
	out.Curve.AllTime = curveAt(allTime, CurveDurations)
	for _, w := range []struct {
		days int
		dst  *[]*int
	}{{90, &out.Curve.Days90}, {28, &out.Curve.Days28}} {
		c, err := db.cyclingBestsBetweenDays(today.AddDate(0, 0, -w.days).Format(dayFmt), end.Format(dayFmt))
		if err != nil {
			return out, err
		}
		*w.dst = curveAt(c, CurveDurations)
	}

	// ── Best efforts by month ──
	if out.BestEfforts, err = db.monthlyBests(bucketOf(from, bucketMonth), end); err != nil {
		return out, err
	}

	// ── Consistency ──
	if out.Consistency, err = db.consistency(today); err != nil {
		return out, err
	}
	if days > 0 {
		v := round(float64(len(activeDays))/(float64(days+1)/7), 1)
		out.Consistency.AvgDaysPerWeek = &v
	}
	return out, nil
}

const dayFmt = "2006-01-02"

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// loadBuckets totals every activity by week or month from first (a bucket
// start) to today's bucket, with the TSS and time planned for each.
func loadBuckets(rides []analyticsRide, planned []models.PlannedWorkout, first, today time.Time, bucket string) []models.LoadBucket {
	starts := bucketStarts(first, today, bucket)
	out := make([]models.LoadBucket, len(starts))
	index := map[string]int{}
	for i, s := range starts {
		out[i].Start = s.Format(dayFmt)
		index[out[i].Start] = i
	}
	at := func(day time.Time) *models.LoadBucket {
		if i, ok := index[bucketOf(day, bucket).Format(dayFmt)]; ok {
			return &out[i]
		}
		return nil
	}
	for i := range rides {
		r := &rides[i]
		day, err := timeutil.ParseDay(r.Day)
		if err != nil || day.Before(first) {
			continue
		}
		if b := at(day); b != nil {
			b.Activities++
			b.DurationSecs += r.Secs
			b.DistanceMeters += r.Meters
			b.ElevationGainMeters += r.Climb
			if r.TSS != nil {
				b.TSS += *r.TSS
			}
		}
	}
	for _, p := range planned {
		day := timeutil.DateOf(p.PlannedDate)
		if day.Before(first) {
			continue
		}
		if b := at(day); b != nil {
			b.PlannedTSS += plannedTSS(p)
			b.PlannedSecs += p.DurationSecs
		}
	}
	for i := range out {
		out[i].TSS = round(out[i].TSS, 1)
		out[i].PlannedTSS = round(out[i].PlannedTSS, 1)
		out[i].DistanceMeters = round(out[i].DistanceMeters, 1)
		out[i].ElevationGainMeters = round(out[i].ElevationGainMeters, 1)
	}
	return out
}

// plannedTSS is a planned workout's TSS, estimated from its duration and IF
// when it has none of its own.
func plannedTSS(p models.PlannedWorkout) float64 {
	if p.TSS != nil {
		return *p.TSS
	}
	if p.IntensityFactor != nil {
		return fitness.EstimateTSS(p.DurationSecs, *p.IntensityFactor)
	}
	return 0
}

// weeklyRamp is how far fitness (CTL) moved each week from the one holding
// from to this one: the CTL at the week's last settled day against the day
// before it began.
func weeklyRamp(points []models.FitnessPoint, from, today time.Time) []models.RampPoint {
	ctl := map[string]float64{}
	var last time.Time
	for _, p := range points {
		d := timeutil.DateOf(p.Date)
		ctl[d.Format(dayFmt)] = p.Fitness
		last = d
	}
	out := []models.RampPoint{}
	for m := timeutil.MondayOf(from); !m.After(today); m = m.AddDate(0, 0, 7) {
		endDay := minTime(m.AddDate(0, 0, 6), last)
		before, ok1 := ctl[m.AddDate(0, 0, -1).Format(dayFmt)]
		after, ok2 := ctl[endDay.Format(dayFmt)]
		if !ok1 || !ok2 || endDay.Before(m) {
			continue
		}
		out = append(out, models.RampPoint{WeekStart: m.Format(dayFmt), CTLChange: round(after-before, 1)})
	}
	return out
}

// ftpEffort is one ride's best 20 or 60 minutes, in its own watts and in
// general-line watts.
type ftpEffortRow struct {
	day     time.Time
	indoor  bool
	secs    int
	watts   int
	general float64
}

// ftpTrend is the FTP set over the range (both lines) against what the rides
// implied each week (models.FTPTrend).
func (db *DB) ftpTrend(from, today, now time.Time) (models.FTPTrend, error) {
	tz := now.Location()
	trend := models.FTPTrend{
		General: []models.FTPPoint{}, Indoor: []models.FTPPoint{},
		Estimate: []models.FTPPoint{}, IndoorEstimate: []models.FTPPoint{},
	}
	lines, err := db.loadFTPLines()
	if err != nil {
		return trend, err
	}
	start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, tz)
	// The general line starts with the value in effect when the range opens,
	// then its changes, then holds to now.
	line := func(entries []FTPHistoryEntry) []models.FTPPoint {
		pts := []models.FTPPoint{}
		before := 0
		for _, e := range entries {
			if e.EffectiveFrom.Before(start) {
				before = e.FTPWatts
				continue
			}
			if len(pts) == 0 && before > 0 {
				pts = append(pts, models.FTPPoint{T: start.Unix(), W: before})
			}
			pts = append(pts, models.FTPPoint{T: e.EffectiveFrom.Unix(), W: e.FTPWatts})
		}
		if len(pts) == 0 && before > 0 {
			pts = append(pts, models.FTPPoint{T: start.Unix(), W: before})
		}
		if len(pts) > 0 {
			pts = append(pts, models.FTPPoint{T: now.Unix(), W: pts[len(pts)-1].W})
		}
		return pts
	}
	trend.General = line(lines.general)
	trend.Indoor = lines.indoorLine(start, now)

	rows, err := db.Query(`
		SELECT w.training_day, w.recorded_at, w.is_indoor, wpc.duration_secs, wpc.watts
		FROM workout_power_curve wpc
		JOIN workouts w ON w.id = wpc.workout_id
		WHERE w.sport = 'cycling' AND wpc.duration_secs IN (1200, 3600)
		  AND w.training_day >= ? AND w.training_day < ?`,
		from.AddDate(0, 0, -ftpEstimateWindowDays).Format(dayFmt), today.AddDate(0, 0, 1).Format(dayFmt))
	if err != nil {
		return trend, err
	}
	defer rows.Close() //nolint:errcheck
	var efforts []ftpEffortRow
	for rows.Next() {
		var day, at string
		var e ftpEffortRow
		if err := rows.Scan(&day, &at, &e.indoor, &e.secs, &e.watts); err != nil {
			return trend, err
		}
		if e.day, err = timeutil.ParseDay(day); err != nil {
			continue
		}
		t, _ := time.Parse(time.RFC3339, at)
		e.general = lines.generalEquivalent(e.watts, t, e.indoor)
		efforts = append(efforts, e)
	}
	if err := rows.Err(); err != nil {
		return trend, err
	}

	// estimate is the FTP implied by the efforts in the window ending d, read
	// in general-line watts or (indoorOnly) indoor rides in their own.
	estimate := func(d time.Time, indoorOnly bool) (int, bool) {
		var best [2]float64 // 20 min, 60 min
		windowStart := d.AddDate(0, 0, -ftpEstimateWindowDays)
		for _, e := range efforts {
			if e.day.Before(windowStart) || e.day.After(d) || (indoorOnly && !e.indoor) {
				continue
			}
			w := e.general
			if indoorOnly {
				w = float64(e.watts)
			}
			i := 0
			if e.secs == 3600 {
				i = 1
			}
			best[i] = math.Max(best[i], w)
		}
		var b20, b60 *models.FTPEffort
		if best[0] > 0 {
			b20 = &models.FTPEffort{DurationSecs: 1200, Watts: int(math.Round(best[0]))}
		}
		if best[1] > 0 {
			b60 = &models.FTPEffort{DurationSecs: 3600, Watts: int(math.Round(best[1]))}
		}
		est, basis := fitness.EstimateFTP(b20, b60)
		return int(math.Round(est)), basis != nil
	}
	var days []time.Time
	for d := today; !d.Before(from); d = d.AddDate(0, 0, -7) {
		days = append(days, d)
	}
	slices.Reverse(days)
	for _, d := range days {
		t := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, tz).Unix()
		if w, ok := estimate(d, false); ok {
			trend.Estimate = append(trend.Estimate, models.FTPPoint{T: t, W: w})
		}
		if len(lines.indoor) > 0 {
			if w, ok := estimate(d, true); ok {
				trend.IndoorEstimate = append(trend.IndoorEstimate, models.FTPPoint{T: t, W: w})
			}
		}
	}
	return trend, nil
}

// monthlyBests is the best cycling power each month at BestDurations, for
// training days in [first, end).
func (db *DB) monthlyBests(first, end time.Time) (models.BestEffortTrend, error) {
	out := models.BestEffortTrend{Durations: BestDurations, Months: []models.BestEffortMonth{}}
	rows, err := db.Query(`
		SELECT substr(w.training_day, 1, 7) AS month, wpc.duration_secs, MAX(wpc.watts)
		FROM workout_power_curve wpc
		JOIN workouts w ON w.id = wpc.workout_id
		WHERE w.sport = 'cycling' AND wpc.duration_secs IN (5, 60, 300, 1200, 3600)
		  AND w.training_day >= ? AND w.training_day < ?
		GROUP BY month, wpc.duration_secs`, first.Format(dayFmt), end.Format(dayFmt))
	if err != nil {
		return out, err
	}
	defer rows.Close() //nolint:errcheck
	byMonth := map[string][]*int{}
	for rows.Next() {
		var month string
		var secs, watts int
		if err := rows.Scan(&month, &secs, &watts); err != nil {
			return out, err
		}
		i := slices.Index(BestDurations, secs)
		if i < 0 || watts <= 0 {
			continue
		}
		if byMonth[month] == nil {
			byMonth[month] = make([]*int, len(BestDurations))
		}
		byMonth[month][i] = &watts
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	for m := first; m.Before(end); m = m.AddDate(0, 1, 0) {
		key := m.Format("2006-01")
		watts := byMonth[key]
		if watts == nil {
			watts = make([]*int, len(BestDurations))
		}
		out.Months = append(out.Months, models.BestEffortMonth{Month: key, Watts: watts})
	}
	return out, nil
}

// consistency is the rider's streaks over all their history.
func (db *DB) consistency(today time.Time) (models.Consistency, error) {
	rows, err := db.Query(`SELECT DISTINCT training_day FROM workouts WHERE training_day IS NOT NULL`)
	if err != nil {
		return models.Consistency{}, err
	}
	defer rows.Close() //nolint:errcheck
	var days []time.Time
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return models.Consistency{}, err
		}
		if d, err := timeutil.ParseDay(s); err == nil && !d.After(today) {
			days = append(days, d)
		}
	}
	if err := rows.Err(); err != nil {
		return models.Consistency{}, err
	}
	s := fitness.DayStreaks(days, today)
	c := models.Consistency{
		CurrentStreakDays: s.CurrentDays, LongestStreakDays: s.LongestDays,
		CurrentWeekStreak: s.CurrentWeeks, LongestWeekStreak: s.LongestWeeks,
	}
	if s.LongestDays > 0 {
		c.LongestStreakEnd = s.LongestEnd.Format(dayFmt)
	}
	return c, nil
}

// ── Year over year ────────────────────────────────────────────────────────────

// YearsCompared is how many calendar years YearOverYear covers, this one
// included.
const YearsCompared = 5

// YearOverYear returns each of the last YearsCompared calendar years'
// training, day by day, for the activities in scope ("" for the default:
// cycling when there are rides). Years without any are left out.
func (db *DB) YearOverYear(scope string, now time.Time) (models.YearOverYear, error) {
	scopes, err := db.AnalyticsScopes()
	if err != nil {
		return models.YearOverYear{}, err
	}
	if scope, err = resolveScope(scope, scopes); err != nil {
		return models.YearOverYear{}, err
	}
	today := timeutil.DateOf(now)
	out := models.YearOverYear{
		Scope: scope, Scopes: scopes, Years: []models.YearOfDays{},
		Today: models.YearDayOfYear{Year: today.Year(), Day: leapDayOfYear(today)},
	}
	firstYear := today.Year() - YearsCompared + 1
	q := `
		SELECT training_day, SUM(duration_secs), SUM(distance_meters),
		       SUM(elevation_gain_meters), COALESCE(SUM(tss), 0)
		FROM workouts
		WHERE training_day >= ? AND training_day < ?`
	args := []any{timeutil.Date(firstYear, 1, 1).Format(dayFmt), today.AddDate(0, 0, 1).Format(dayFmt)}
	if scope != models.ScopeAll {
		q += ` AND sport = ?`
		args = append(args, scope)
	}
	rows, err := db.Query(q+` GROUP BY training_day ORDER BY training_day`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var day string
		var t models.DayTotals
		if err := rows.Scan(&day, &t.DurationSecs, &t.DistanceMeters, &t.ElevationGainMeters, &t.TSS); err != nil {
			return out, err
		}
		d, err := timeutil.ParseDay(day)
		if err != nil {
			continue
		}
		t.DayOfYear = leapDayOfYear(d)
		t.DistanceMeters = round(t.DistanceMeters, 1)
		t.ElevationGainMeters = round(t.ElevationGainMeters, 1)
		t.TSS = round(t.TSS, 1)
		if n := len(out.Years); n == 0 || out.Years[n-1].Year != d.Year() {
			out.Years = append(out.Years, models.YearOfDays{Year: d.Year()})
		}
		y := &out.Years[len(out.Years)-1]
		y.Days = append(y.Days, t)
	}
	return out, rows.Err()
}

// leapDayOfYear places a date on a leap-year calendar, so March 1 is day 61
// in every year and years line up date for date.
func leapDayOfYear(d time.Time) int {
	return time.Date(2000, d.Month(), d.Day(), 0, 0, 0, 0, time.UTC).YearDay()
}
