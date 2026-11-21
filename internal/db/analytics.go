package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
	"github.com/fitbase/fitbase/internal/timeutil"
)

// Analytics (models.PeriodComparison, models.AnalyticsTrends,
// models.YearOverYear) reads per-ride aggregates only: the workouts columns,
// stored zone times, ride ratings, cached decoupling and stored power curves.
// Nothing here reads a stream, so years of rides cost a few indexed queries.
// Periods are runs of training days (the day the rider rode, in their own
// timezone), and power-derived numbers read cycling rides only.

// ErrInvalidParam wraps a request the analytics can't answer as asked (an
// unknown scope or range): the caller's mistake, not a database failure.
var ErrInvalidParam = errors.New("invalid analytics parameter")

// BestDurations are the efforts compared and charted by month.
var BestDurations = []int{5, 60, 300, 1200, 3600}

// CurveDurations are the power-curve points charted, short to long.
var CurveDurations = []int{5, 10, 30, 60, 120, 300, 600, 1200, 1800, 3600, 5400, 7200}

// analyticsRide is one activity's per-ride aggregates: its workouts row plus
// its stored zone times, the rider's rating and its cached decoupling.
type analyticsRide struct {
	ID, Day, Sport string
	At             time.Time // the start instant
	Indoor         bool
	Secs           int
	Meters, Climb  float64
	AvgPower, NP   *float64
	AvgHR, Cadence *int
	TSS, IF        *float64
	PowerZones     *[7]int
	HRZones        *[5]int
	RPE            *int
	Decoupling     *float64
}

// power reports whether this is a cycling ride with power: the only rides
// power-derived numbers read.
func (r *analyticsRide) power() bool {
	return r.Sport == "cycling" && r.AvgPower != nil && *r.AvgPower > 0
}

// steady reports whether the ride feeds the aerobic markers (EF, HR at
// matched power, power at matched HR): a steady cycling ride with power and
// heart rate (fitness.IsSteady).
func (r *analyticsRide) steady() bool {
	return r.power() && r.NP != nil && r.AvgHR != nil && *r.AvgHR > 0 &&
		r.IF != nil && fitness.IsSteady(*r.IF, r.Secs)
}

// ef is the ride's efficiency factor, for a steady ride.
func (r *analyticsRide) ef() (float64, bool) {
	if !r.steady() {
		return 0, false
	}
	return fitness.EfficiencyFactor(r.NP, r.AvgHR)
}

// durability is the ride's cached decoupling, for a long steady ride.
func (r *analyticsRide) durability() (float64, bool) {
	if !r.power() || r.AvgHR == nil || r.IF == nil || r.Decoupling == nil ||
		!fitness.IsDecouplingRide(*r.IF, r.Secs) {
		return 0, false
	}
	return *r.Decoupling, true
}

// env is 0 for an outdoor ride, 1 for an indoor one.
func (r *analyticsRide) env() int {
	if r.Indoor {
		return 1
	}
	return 0
}

// analyticsRides returns every activity whose training day is in
// [fromDay, endDay), oldest first.
func (db *DB) analyticsRides(fromDay, endDay string) ([]analyticsRide, error) {
	rows, err := db.Query(`
		SELECT w.id, w.training_day, w.recorded_at, w.sport, w.is_indoor, w.duration_secs,
		       w.distance_meters, w.elevation_gain_meters, w.avg_power_watts, w.normalized_power,
		       w.avg_heart_rate, w.avg_cadence, w.tss, w.intensity_factor,
		       z.power_secs, z.hr_secs, f.rpe, d.decoupling_pct
		FROM workouts w
		LEFT JOIN workout_zone_times z ON z.workout_id = w.id
		LEFT JOIN workout_feedback f ON f.workout_id = w.id
		LEFT JOIN workout_decoupling d ON d.workout_id = w.id
		WHERE w.training_day >= ? AND w.training_day < ?
		ORDER BY w.training_day, w.recorded_at`, fromDay, endDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var out []analyticsRide
	for rows.Next() {
		var r analyticsRide
		var at string
		var powerSecs, hrSecs sql.NullString
		var rpe sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Day, &at, &r.Sport, &r.Indoor, &r.Secs,
			&r.Meters, &r.Climb, &r.AvgPower, &r.NP,
			&r.AvgHR, &r.Cadence, &r.TSS, &r.IF,
			&powerSecs, &hrSecs, &rpe, &r.Decoupling); err != nil {
			return nil, err
		}
		r.At, _ = time.Parse(time.RFC3339, at)
		if powerSecs.Valid {
			var p [7]int
			if json.Unmarshal([]byte(powerSecs.String), &p) == nil {
				r.PowerZones = &p
			}
		}
		if hrSecs.Valid {
			var h [5]int
			if json.Unmarshal([]byte(hrSecs.String), &h) == nil {
				r.HRZones = &h
			}
		}
		if rpe.Valid {
			v := int(rpe.Int64)
			r.RPE = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ── Scopes ────────────────────────────────────────────────────────────────────

// AnalyticsScopes returns the sports the rider has workouts in, most workouts
// first: what a comparison can be narrowed to besides "all".
func (db *DB) AnalyticsScopes() ([]string, error) {
	rows, err := db.Query(`SELECT sport FROM workouts GROUP BY sport ORDER BY COUNT(*) DESC, sport`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	scopes := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		scopes = append(scopes, s)
	}
	return scopes, rows.Err()
}

// knownSports are the sports fitbase imports (fitparser.sportString).
var knownSports = []string{"cycling", "running", "swimming", "hiking"}

// resolveScope checks a requested scope: "" picks the default — cycling
// when there are rides, otherwise all — and anything else must be "all" or a
// sport.
func resolveScope(scope string, scopes []string) (string, error) {
	switch {
	case scope == "":
		if slices.Contains(scopes, "cycling") {
			return "cycling", nil
		}
		return models.ScopeAll, nil
	case scope == models.ScopeAll, slices.Contains(knownSports, scope), slices.Contains(scopes, scope):
		return scope, nil
	}
	return "", fmt.Errorf("%w: scope: want all or a sport (%s), got %q", ErrInvalidParam, strings.Join(knownSports, ", "), scope)
}

// inScope reports whether a ride counts toward a scope.
func inScope(r *analyticsRide, scope string) bool {
	return scope == models.ScopeAll || r.Sport == scope
}

// ── FTP lines ─────────────────────────────────────────────────────────────────

// ftpLines answers GetFTPForRide for many rides from one read of the FTP
// history, with the same rules: an indoor ride uses the latest indoor entry
// on or before it unless a general entry is newer; everything else (and an
// indoor ride without such an entry) the latest general entry, falling back
// to the profile FTP.
type ftpLines struct {
	general, indoor []FTPHistoryEntry // oldest first
	profile         int
}

func (db *DB) loadFTPLines() (ftpLines, error) {
	hist, err := db.AllFTPHistory() // newest first
	if err != nil {
		return ftpLines{}, err
	}
	var l ftpLines
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Indoor {
			l.indoor = append(l.indoor, hist[i])
		} else {
			l.general = append(l.general, hist[i])
		}
	}
	_ = db.QueryRow("SELECT ftp_watts FROM athlete WHERE id=1").Scan(&l.profile)
	return l, nil
}

// latest is the last entry on or before t (ties: the last inserted, as the
// history query orders by id), or nil.
func latestFTP(line []FTPHistoryEntry, t time.Time) *FTPHistoryEntry {
	var found *FTPHistoryEntry
	for i := range line {
		if line[i].EffectiveFrom.After(t) {
			break
		}
		found = &line[i]
	}
	return found
}

// general is the general-line FTP at t (GetFTPAtDate).
func (l ftpLines) generalAt(t time.Time) int {
	t = t.UTC().Truncate(time.Second)
	if e := latestFTP(l.general, t); e != nil && e.FTPWatts > 0 {
		return e.FTPWatts
	}
	return l.profile
}

// forRide is the FTP a ride at t was trained against (GetFTPForRide).
func (l ftpLines) forRide(t time.Time, indoor bool) int {
	t = t.UTC().Truncate(time.Second)
	if indoor {
		g := latestFTP(l.general, t)
		if e := latestFTP(l.indoor, t); e != nil && e.FTPWatts > 0 &&
			(g == nil || !e.EffectiveFrom.Before(g.EffectiveFrom)) {
			return e.FTPWatts
		}
	}
	return l.generalAt(t)
}

// generalEquivalent is an indoor effort in general-line watts: 238 W on a
// 230 W indoor line is what a 250 W rider holds indoors (the FTP check's
// ftpEffort). Outdoor efforts, and riders with one line, pass through.
func (l ftpLines) generalEquivalent(watts int, t time.Time, indoor bool) float64 {
	if !indoor {
		return float64(watts)
	}
	general, line := l.generalAt(t), l.forRide(t, true)
	if general <= 0 || line <= 0 || general == line {
		return float64(watts)
	}
	return float64(watts) * float64(general) / float64(line)
}

// indoorLine is the FTP indoor rides were scored against from start to now,
// as chart steps: it begins when the first indoor entry takes effect (or at
// start, if one already had), then is read with forRide at every indoor and
// general change. A newer general entry outranks an older indoor one, so the
// line follows it there rather than holding the stale indoor value. Repeated
// values are dropped; the last holds to now. Empty without an indoor entry.
func (l ftpLines) indoorLine(start, now time.Time) []models.FTPPoint {
	pts := []models.FTPPoint{}
	if len(l.indoor) == 0 || l.indoor[0].EffectiveFrom.After(now) {
		return pts
	}
	first := l.indoor[0].EffectiveFrom
	if first.Before(start) {
		first = start
	}
	times := []time.Time{first}
	for _, line := range [][]FTPHistoryEntry{l.indoor, l.general} {
		for _, e := range line {
			if e.EffectiveFrom.After(first) && !e.EffectiveFrom.After(now) {
				times = append(times, e.EffectiveFrom)
			}
		}
	}
	slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
	for _, t := range times {
		w := l.forRide(t, true)
		if n := len(pts); w <= 0 || (n > 0 && pts[n-1].W == w) {
			continue
		}
		pts = append(pts, models.FTPPoint{T: t.Unix(), W: w})
	}
	if len(pts) > 0 {
		pts = append(pts, models.FTPPoint{T: now.Unix(), W: pts[len(pts)-1].W})
	}
	return pts
}

// ── Power bests ───────────────────────────────────────────────────────────────

// cyclingBestsBetweenDays returns the best watts per stored duration across
// cycling rides whose training day is in [fromDay, endDay), with the ride
// each came from. SQLite takes a bare column (workout_id) from the row that
// holds the MAX when the query has a single MAX, so each best keeps its ride.
func (db *DB) cyclingBestsBetweenDays(fromDay, endDay string) (map[int]models.AllTimeBest, error) {
	rows, err := db.Query(`
		SELECT wpc.duration_secs, MAX(wpc.watts), wpc.workout_id
		FROM workout_power_curve wpc
		JOIN workouts w ON w.id = wpc.workout_id
		WHERE w.sport = 'cycling' AND w.training_day >= ? AND w.training_day < ?
		GROUP BY wpc.duration_secs`, fromDay, endDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	out := map[int]models.AllTimeBest{}
	for rows.Next() {
		var dur int
		var b models.AllTimeBest
		if err := rows.Scan(&dur, &b.Watts, &b.WorkoutID); err != nil {
			return nil, err
		}
		out[dur] = b
	}
	return out, rows.Err()
}

// curveAt picks a curve's watts at durations: null where no ride was long
// enough.
func curveAt(curve map[int]models.AllTimeBest, durations []int) []*int {
	out := make([]*int, len(durations))
	for i, d := range durations {
		if b, ok := curve[d]; ok && b.Watts > 0 {
			w := b.Watts
			out[i] = &w
		}
	}
	return out
}

// ── Matched markers ───────────────────────────────────────────────────────────

// matchSamples collects, per environment (0 outdoor, 1 indoor), each steady
// ride's (NP, HR) for HR at matched power and (HR, NP) for power at matched
// HR.
type matchSamples struct {
	hrAtPower, powerAtHR [2][]fitness.MatchSample
}

func (m *matchSamples) add(r *analyticsRide) {
	if !r.steady() {
		return
	}
	np, hr := *r.NP, float64(*r.AvgHR)
	m.hrAtPower[r.env()] = append(m.hrAtPower[r.env()], fitness.MatchSample{X: np, Y: hr})
	m.powerAtHR[r.env()] = append(m.powerAtHR[r.env()], fitness.MatchSample{X: hr, Y: np})
}

// matchRefs is where matched markers are read, per environment: power (W)
// and heart rate (bpm); 0 where there were no steady rides.
type matchRefs struct {
	power, hr [2]float64
}

func (m *matchSamples) refs() matchRefs {
	var refs matchRefs
	for env := range 2 {
		refs.power[env], _ = fitness.MatchReference(m.hrAtPower[env], 5)
		refs.hr[env], _ = fitness.MatchReference(m.powerAtHR[env], 1)
	}
	return refs
}

func (refs matchRefs) model() models.MatchReference {
	ptr := func(v float64) *int {
		if v <= 0 {
			return nil
		}
		i := int(v)
		return &i
	}
	return models.MatchReference{
		PowerOutdoorWatts: ptr(refs.power[0]), PowerIndoorWatts: ptr(refs.power[1]),
		HROutdoorBPM: ptr(refs.hr[0]), HRIndoorBPM: ptr(refs.hr[1]),
	}
}

// markers reads the matched markers from a set of samples at refs.
func (m *matchSamples) markers(refs matchRefs) (hrAtPower, powerAtHR models.EnvMarkers) {
	read := func(samples []fitness.MatchSample, ref, tol float64, digits int) *models.Marker {
		y, x, n := fitness.MatchedMean(samples, ref, tol)
		if n == 0 {
			return nil
		}
		at := round(x, 1)
		return &models.Marker{Value: round(y, digits), Rides: n, MatchedAt: &at}
	}
	hrAtPower.Outdoor = read(m.hrAtPower[0], refs.power[0], fitness.MatchPowerTolerance, 1)
	hrAtPower.Indoor = read(m.hrAtPower[1], refs.power[1], fitness.MatchPowerTolerance, 1)
	powerAtHR.Outdoor = read(m.powerAtHR[0], refs.hr[0], fitness.MatchHRTolerance, 1)
	powerAtHR.Indoor = read(m.powerAtHR[1], refs.hr[1], fitness.MatchHRTolerance, 1)
	return hrAtPower, powerAtHR
}

// mean accumulates a per-ride average.
type mean struct {
	sum float64
	n   int
}

func (m *mean) add(v float64) { m.sum += v; m.n++ }

func (m mean) marker(digits int) *models.Marker {
	if m.n == 0 {
		return nil
	}
	return &models.Marker{Value: round(m.sum/float64(m.n), digits), Rides: m.n}
}

// weighted accumulates a duration-weighted average.
type weighted struct {
	sum, w float64
	n      int
}

func (a *weighted) add(v float64, secs int) {
	a.sum += v * float64(secs)
	a.w += float64(secs)
	a.n++
}

func (a weighted) value(digits int) *float64 {
	if a.w <= 0 {
		return nil
	}
	v := round(a.sum/a.w, digits)
	return &v
}

func round(v float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.Round(v*p) / p
}

func ptrFloat(v float64) *float64 { return &v }

func split(secs [7]int) *models.IntensitySplit {
	easy, middle, hard, ok := fitness.IntensitySplit(secs)
	if !ok {
		return nil
	}
	return &models.IntensitySplit{EasyPct: easy, MiddlePct: middle, HardPct: hard}
}

// ── Period comparison ─────────────────────────────────────────────────────────

// ComparePeriods compares the activities of c.A with those of c.B, narrowed
// to scope ("all", a sport, or "" for the default: cycling when there are
// rides). Either period may be empty, and a rider without a power meter or a
// heart-rate strap gets the rows their data supports.
func (db *DB) ComparePeriods(c timeutil.Comparison, scope string) (models.PeriodComparison, error) {
	scopes, err := db.AnalyticsScopes()
	if err != nil {
		return models.PeriodComparison{}, err
	}
	if scope, err = resolveScope(scope, scopes); err != nil {
		return models.PeriodComparison{}, err
	}
	out := models.PeriodComparison{
		Period: c.Kind, Against: c.Against, ToDate: c.ToDate,
		Scope: scope, Scopes: scopes, CurveDurations: CurveDurations,
	}
	if a, err := db.GetAthlete(); err == nil {
		out.WeightKG = a.WeightKG
	}

	var rides [2][]analyticsRide
	var samples [2]matchSamples
	var both matchSamples
	for i, p := range []timeutil.Period{c.A, c.B} {
		all, err := db.analyticsRides(p.FirstDay(), p.EndDay())
		if err != nil {
			return out, err
		}
		rides[i] = all
		for j := range all {
			if inScope(&all[j], scope) {
				samples[i].add(&all[j])
				both.add(&all[j])
			}
		}
	}
	refs := both.refs()
	out.Reference = refs.model()

	var stats [2]models.PeriodStats
	for i, p := range []timeutil.Period{c.A, c.B} {
		stats[i] = periodStats(p, rides[i], scope)
		stats[i].HRAtPower, stats[i].PowerAtHR = samples[i].markers(refs)
		stats[i].Bests = []models.PowerBest{}
		stats[i].Curve = make([]*int, len(CurveDurations))
		if scope == models.ScopeAll || scope == "cycling" {
			bests, err := db.cyclingBestsBetweenDays(p.FirstDay(), p.EndDay())
			if err != nil {
				return out, err
			}
			for _, d := range BestDurations {
				if b, ok := bests[d]; ok && b.Watts > 0 {
					stats[i].Bests = append(stats[i].Bests, models.PowerBest{DurationSecs: d, Watts: b.Watts, WorkoutID: b.WorkoutID})
				}
			}
			stats[i].Curve = curveAt(bests, CurveDurations)
		}
	}
	out.A, out.B = stats[0], stats[1]
	out.Rows = comparisonRows(out.A, out.B, refs)
	return out, nil
}

// periodStats totals one period's activities in scope. The matched markers
// and power bests are filled in by the caller, which knows the reference and
// reads the curves.
func periodStats(p timeutil.Period, rides []analyticsRide, scope string) models.PeriodStats {
	s := models.PeriodStats{
		Label: p.Label(), FirstDay: p.FirstDay(), LastDay: p.LastDay(), Days: p.Days(),
		RPE: []models.RPEPoint{}, Sports: []models.SportTotals{},
	}
	days := map[string]bool{}
	sports := map[string]*models.SportTotals{}
	var ifAvg, npAvg, hrAvg weighted
	var kj float64
	var efs [2]mean
	var dec, rpe mean
	for i := range rides {
		r := &rides[i]
		sp := sports[r.Sport]
		if sp == nil {
			sp = &models.SportTotals{Sport: r.Sport}
			sports[r.Sport] = sp
		}
		sp.Activities++
		sp.DurationSecs += r.Secs
		sp.DistanceMeters += r.Meters
		if r.TSS != nil {
			sp.TSS += *r.TSS
		}
		if !inScope(r, scope) {
			continue
		}

		s.Activities++
		days[r.Day] = true
		s.DurationSecs += r.Secs
		s.DistanceMeters += r.Meters
		s.ElevationGainMeters += r.Climb
		if r.TSS != nil {
			s.TSS += *r.TSS
		}
		env := &s.Outdoor
		if r.Indoor {
			env = &s.Indoor
		}
		env.Activities++
		env.DurationSecs += r.Secs

		if r.AvgHR != nil && *r.AvgHR > 0 {
			hrAvg.add(float64(*r.AvgHR), r.Secs)
		}
		if r.HRZones != nil {
			for z, secs := range r.HRZones {
				s.HRZoneSecs[z] += secs
			}
		}
		if r.RPE != nil {
			rpe.add(float64(*r.RPE))
			s.RPE = append(s.RPE, models.RPEPoint{WorkoutID: r.ID, Day: r.Day, RPE: *r.RPE, TSS: r.TSS})
		}

		if !r.power() {
			continue
		}
		s.PowerRides++
		kj += *r.AvgPower * float64(r.Secs) / 1000
		if r.IF != nil {
			ifAvg.add(*r.IF, r.Secs)
		}
		if r.NP != nil {
			npAvg.add(*r.NP, r.Secs)
		}
		if r.PowerZones != nil {
			for z, secs := range r.PowerZones {
				s.PowerZoneSecs[z] += secs
			}
		}
		if ef, ok := r.ef(); ok {
			efs[r.env()].add(ef)
		}
		if d, ok := r.durability(); ok {
			dec.add(d)
		}
	}

	s.ActiveDays = len(days)
	s.DistanceMeters = round(s.DistanceMeters, 1)
	s.ElevationGainMeters = round(s.ElevationGainMeters, 1)
	s.TSS = round(s.TSS, 1)
	if s.PowerRides > 0 {
		s.WorkKJ = ptrFloat(math.Round(kj))
	}
	s.AvgIF = ifAvg.value(3)
	s.AvgNPWatts = npAvg.value(1)
	s.AvgHRBPM = hrAvg.value(1)
	s.Split = split(s.PowerZoneSecs)
	s.EF = models.EnvMarkers{Outdoor: efs[0].marker(3), Indoor: efs[1].marker(3)}
	s.DecouplingPct = dec.marker(1)
	if m := rpe.marker(2); m != nil {
		s.AvgRPE, s.RatedRides = &m.Value, m.Rides
	}
	for _, sp := range sports {
		sp.DistanceMeters = round(sp.DistanceMeters, 1)
		sp.TSS = round(sp.TSS, 1)
		s.Sports = append(s.Sports, *sp)
	}
	slices.SortFunc(s.Sports, func(a, b models.SportTotals) int { return b.DurationSecs - a.DurationSecs })
	return s
}

// unitDigits is how many decimals each unit's values carry, so a delta is
// taken between the numbers shown.
var unitDigits = map[string]int{
	"duration_secs": 0, "distance_meters": 1, "elevation_meters": 1, "count": 0,
	"tss": 1, "kj": 0, "if": 3, "watts": 1, "bpm": 1, "ef": 3, "pct": 1, "rpe": 2,
}

// comparisonRows lays the two periods side by side, one row per metric
// either has, with the change from B to A.
func comparisonRows(a, b models.PeriodStats, refs matchRefs) []models.ComparisonRow {
	var rows []models.ComparisonRow
	add := func(group, key, label, unit, better string, va, vb *float64) *models.ComparisonRow {
		if va == nil && vb == nil {
			return nil
		}
		row := models.ComparisonRow{Group: group, Key: key, Label: label, Unit: unit, Better: better}
		digits := unitDigits[unit]
		if va != nil {
			row.A = ptrFloat(round(*va, digits))
		}
		if vb != nil {
			row.B = ptrFloat(round(*vb, digits))
		}
		if row.A != nil && row.B != nil {
			row.Delta = ptrFloat(round(*row.A-*row.B, digits))
			if *row.B != 0 && unit != "pct" {
				row.DeltaPct = ptrFloat(round((*row.A-*row.B)/math.Abs(*row.B)*100, 1))
			}
		}
		rows = append(rows, row)
		return &rows[len(rows)-1]
	}
	num := func(v float64) *float64 { return &v }
	marker := func(m *models.Marker) *float64 {
		if m == nil {
			return nil
		}
		return &m.Value
	}
	rides := func(m *models.Marker) *int {
		if m == nil {
			return nil
		}
		return &m.Rides
	}
	addMarker := func(group, key, label, unit, better string, ma, mb *models.Marker) {
		if row := add(group, key, label, unit, better, marker(ma), marker(mb)); row != nil {
			row.RidesA, row.RidesB = rides(ma), rides(mb)
		}
	}

	add("volume", "duration_secs", "time", "duration_secs", "", num(float64(a.DurationSecs)), num(float64(b.DurationSecs)))
	add("volume", "distance_meters", "distance", "distance_meters", "", num(a.DistanceMeters), num(b.DistanceMeters))
	add("volume", "elevation_gain_meters", "elevation", "elevation_meters", "", num(a.ElevationGainMeters), num(b.ElevationGainMeters))
	add("volume", "activities", "activities", "count", "", num(float64(a.Activities)), num(float64(b.Activities)))
	add("volume", "active_days", "days active", "count", "", num(float64(a.ActiveDays)), num(float64(b.ActiveDays)))
	add("volume", "tss", "TSS", "tss", "", num(a.TSS), num(b.TSS))
	add("volume", "work_kj", "work", "kj", "", a.WorkKJ, b.WorkKJ)

	add("intensity", "avg_if", "average IF", "if", "", a.AvgIF, b.AvgIF)
	add("intensity", "avg_np_watts", "average NP", "watts", "", a.AvgNPWatts, b.AvgNPWatts)
	if a.Split != nil || b.Split != nil {
		pick := func(s *models.IntensitySplit, f func(*models.IntensitySplit) float64) *float64 {
			if s == nil {
				return nil
			}
			return num(f(s))
		}
		add("intensity", "easy_pct", "easy (Z1–Z2)", "pct", "", pick(a.Split, func(s *models.IntensitySplit) float64 { return s.EasyPct }), pick(b.Split, func(s *models.IntensitySplit) float64 { return s.EasyPct }))
		add("intensity", "middle_pct", "middle (Z3–Z4)", "pct", "", pick(a.Split, func(s *models.IntensitySplit) float64 { return s.MiddlePct }), pick(b.Split, func(s *models.IntensitySplit) float64 { return s.MiddlePct }))
		add("intensity", "hard_pct", "hard (Z5+)", "pct", "", pick(a.Split, func(s *models.IntensitySplit) float64 { return s.HardPct }), pick(b.Split, func(s *models.IntensitySplit) float64 { return s.HardPct }))
	}

	addMarker("efficiency", "ef_outdoor", "EF · outdoor", "ef", "higher", a.EF.Outdoor, b.EF.Outdoor)
	addMarker("efficiency", "ef_indoor", "EF · indoor", "ef", "higher", a.EF.Indoor, b.EF.Indoor)
	addMarker("efficiency", "decoupling_pct", "decoupling, long rides", "pct", "lower", a.DecouplingPct, b.DecouplingPct)

	add("heart_rate", "avg_hr_bpm", "average HR", "bpm", "", a.AvgHRBPM, b.AvgHRBPM)
	envName := [2]string{"outdoor", "indoor"}
	envHR := func(m models.EnvMarkers, env int) *models.Marker { return [2]*models.Marker{m.Outdoor, m.Indoor}[env] }
	for env := range 2 {
		if refs.power[env] > 0 {
			addMarker("heart_rate", "hr_at_power_"+envName[env], fmt.Sprintf("HR at %.0f W · %s", refs.power[env], envName[env]),
				"bpm", "lower", envHR(a.HRAtPower, env), envHR(b.HRAtPower, env))
		}
	}
	for env := range 2 {
		if refs.hr[env] > 0 {
			addMarker("heart_rate", "power_at_hr_"+envName[env], fmt.Sprintf("power at %.0f bpm · %s", refs.hr[env], envName[env]),
				"watts", "higher", envHR(a.PowerAtHR, env), envHR(b.PowerAtHR, env))
		}
	}

	bestOf := func(s models.PeriodStats, secs int) *float64 {
		for _, best := range s.Bests {
			if best.DurationSecs == secs {
				return num(float64(best.Watts))
			}
		}
		return nil
	}
	for _, secs := range BestDurations {
		add("power", fmt.Sprintf("best_%d", secs), "best "+durationLabel(secs), "watts", "higher", bestOf(a, secs), bestOf(b, secs))
	}

	if a.AvgRPE != nil || b.AvgRPE != nil {
		row := add("rpe", "avg_rpe", "average RPE", "rpe", "", a.AvgRPE, b.AvgRPE)
		ra, rb := a.RatedRides, b.RatedRides
		row.RidesA, row.RidesB = &ra, &rb
	}

	share := func(s models.PeriodStats) *float64 {
		if s.DurationSecs == 0 {
			return nil
		}
		return num(float64(s.Indoor.DurationSecs) / float64(s.DurationSecs) * 100)
	}
	add("mix", "indoor_pct", "indoor share of time", "pct", "", share(a), share(b))
	return rows
}

// durationLabel names an effort length: "5 s", "1 min", "20 min", "1 h".
func durationLabel(secs int) string {
	switch {
	case secs >= 3600 && secs%3600 == 0:
		return fmt.Sprintf("%d h", secs/3600)
	case secs >= 60 && secs%60 == 0:
		return fmt.Sprintf("%d min", secs/60)
	}
	return fmt.Sprintf("%d s", secs)
}
