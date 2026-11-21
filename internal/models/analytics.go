package models

// Analytics: two periods side by side (PeriodComparison), markers over time
// (AnalyticsTrends) and years against each other (YearOverYear). Everything
// is built from per-ride aggregates — the workouts columns, the stored power
// curves, zone times and decoupling — never from streams, so it stays fast on
// years of rides. Days are training days, in the rider's own timezone.
//
// Power-derived numbers (NP, IF, kJ, power zones, best efforts, EF, matched
// power) only ever read cycling rides: running watts aren't cycling watts.

// Comparison scopes besides a sport name.
const ScopeAll = "all"

// PeriodComparison is one period against another: their stats and, for every
// metric either has, a row with the change.
type PeriodComparison struct {
	Period  string `json:"period"`  // week, month, year or custom
	Against string `json:"against"` // previous, year or custom
	// ToDate: the period is still running and was cut at today, and the
	// period it is compared against was cut to match.
	ToDate bool `json:"to_date"`
	// Scope is "all" or a sport: the activities counted. Power-derived
	// numbers read the cycling rides within it (none for running).
	Scope  string   `json:"scope"`
	Scopes []string `json:"scopes"` // the sports the rider has, for a scope picker
	// Reference is the power and heart rate the matched markers are read at,
	// chosen from both periods' steady rides so both have rides near it.
	Reference MatchReference  `json:"reference"`
	A         PeriodStats     `json:"a"`
	B         PeriodStats     `json:"b"`
	Rows      []ComparisonRow `json:"rows"`
	// CurveDurations are the durations of each period's curve_watts.
	CurveDurations []int   `json:"curve_durations_secs"`
	WeightKG       float64 `json:"weight_kg"`
}

// MatchReference is the power and heart rate matched markers are read at,
// per environment; nil where no steady ride had the data.
type MatchReference struct {
	PowerOutdoorWatts *int `json:"power_outdoor_watts,omitempty"`
	PowerIndoorWatts  *int `json:"power_indoor_watts,omitempty"`
	HROutdoorBPM      *int `json:"hr_outdoor_bpm,omitempty"`
	HRIndoorBPM       *int `json:"hr_indoor_bpm,omitempty"`
}

// PeriodStats is everything about one period's activities.
type PeriodStats struct {
	Label    string `json:"label"`
	FirstDay string `json:"first_day"`
	LastDay  string `json:"last_day"`
	Days     int    `json:"days"`

	// Volume, over the activities in scope.
	Activities          int      `json:"activities"`
	ActiveDays          int      `json:"active_days"`
	DurationSecs        int      `json:"duration_secs"`
	DistanceMeters      float64  `json:"distance_meters"`
	ElevationGainMeters float64  `json:"elevation_gain_meters"`
	TSS                 float64  `json:"tss"`
	WorkKJ              *float64 `json:"work_kj,omitempty"` // cycling rides with power: avg power × moving time

	// Intensity. IF, NP and HR are averaged over rides weighted by duration.
	PowerRides    int             `json:"power_rides"` // cycling rides with power
	AvgIF         *float64        `json:"avg_if,omitempty"`
	AvgNPWatts    *float64        `json:"avg_np_watts,omitempty"`
	AvgHRBPM      *float64        `json:"avg_hr_bpm,omitempty"`
	PowerZoneSecs [7]int          `json:"power_zone_secs"` // cycling
	HRZoneSecs    [5]int          `json:"hr_zone_secs"`
	Split         *IntensitySplit `json:"intensity_split,omitempty"`

	// Aerobic markers, each averaged per ride over steady rides (see
	// fitness.IsSteady); decoupling over long steady rides.
	EF            EnvMarkers `json:"ef"`
	HRAtPower     EnvMarkers `json:"hr_at_power_bpm"`
	PowerAtHR     EnvMarkers `json:"power_at_hr_watts"`
	DecouplingPct *Marker    `json:"decoupling_pct,omitempty"`

	// Power: best efforts at the key durations and the curve at
	// PeriodComparison.CurveDurations (null where no ride was long enough).
	Bests []PowerBest `json:"bests"`
	Curve []*int      `json:"curve_watts"`

	// How hard it felt: rides the rider rated.
	AvgRPE     *float64   `json:"avg_rpe,omitempty"`
	RatedRides int        `json:"rated_rides"`
	RPE        []RPEPoint `json:"rpe"`

	Indoor  EnvTotals     `json:"indoor"`
	Outdoor EnvTotals     `json:"outdoor"`
	Sports  []SportTotals `json:"sports"` // every sport, whatever the scope
}

// Marker is an average over rides, with how many rides it is from.
type Marker struct {
	Value float64 `json:"value"`
	Rides int     `json:"rides"`
	// MatchedAt, for a matched marker, is the mean power or heart rate of the
	// rides that fell near the reference: how closely they really matched it.
	MatchedAt *float64 `json:"matched_at,omitempty"`
}

// EnvMarkers keeps a marker's outdoor and indoor rides apart: indoors runs
// hotter, so heart rate and EF differ at the same power.
type EnvMarkers struct {
	Outdoor *Marker `json:"outdoor,omitempty"`
	Indoor  *Marker `json:"indoor,omitempty"`
}

// IntensitySplit is power-zone time as easy (Z1–Z2), middle (Z3–Z4) and hard
// (Z5+) percentages.
type IntensitySplit struct {
	EasyPct   float64 `json:"easy_pct"`
	MiddlePct float64 `json:"middle_pct"`
	HardPct   float64 `json:"hard_pct"`
}

// PowerBest is the best power held for a duration, and the ride it was in.
type PowerBest struct {
	DurationSecs int    `json:"duration_secs"`
	Watts        int    `json:"watts"`
	WorkoutID    string `json:"workout_id"`
}

// RPEPoint is one rated ride: how hard it felt against its load.
type RPEPoint struct {
	WorkoutID string   `json:"workout_id"`
	Day       string   `json:"day"`
	RPE       int      `json:"rpe"`
	TSS       *float64 `json:"tss,omitempty"`
}

// EnvTotals is the activities done indoors or outdoors.
type EnvTotals struct {
	Activities   int `json:"activities"`
	DurationSecs int `json:"duration_secs"`
}

// SportTotals is one sport's volume.
type SportTotals struct {
	Sport          string  `json:"sport"`
	Activities     int     `json:"activities"`
	DurationSecs   int     `json:"duration_secs"`
	DistanceMeters float64 `json:"distance_meters"`
	TSS            float64 `json:"tss"`
}

// ComparisonRow is one metric of two periods and the change from B to A.
type ComparisonRow struct {
	Group string `json:"group"` // volume, intensity, efficiency, heart_rate, power, rpe, mix
	Key   string `json:"key"`
	Label string `json:"label"`
	// Unit says how to read A, B and Delta: duration_secs, distance_meters,
	// elevation_meters, count, tss, kj, if, watts, bpm, ef, pct or rpe.
	Unit     string   `json:"unit"`
	A        *float64 `json:"a"`
	B        *float64 `json:"b"`
	Delta    *float64 `json:"delta,omitempty"`     // A − B, when both are present
	DeltaPct *float64 `json:"delta_pct,omitempty"` // the change as a % of B (not for percentages)
	// Better is "higher" or "lower" when that direction is plainly better —
	// more power at a duration, lower HR at the same power — and empty when
	// it isn't: more volume or intensity is neither better nor worse.
	Better string `json:"better,omitempty"`
	// RidesA and RidesB are how many rides an averaged marker is from.
	RidesA *int `json:"rides_a,omitempty"`
	RidesB *int `json:"rides_b,omitempty"`
}

// AnalyticsTrends is every marker over a range of time.
type AnalyticsTrends struct {
	Range    string `json:"range"` // 3m, 6m, 1y, 2y or all
	FirstDay string `json:"first_day"`
	LastDay  string `json:"last_day"`
	// Bucket is what Buckets are: week for ranges up to six months, month
	// beyond.
	Bucket    string         `json:"bucket"`
	WeightKG  float64        `json:"weight_kg"`
	Reference MatchReference `json:"reference"`

	FTP     FTPTrend        `json:"ftp"`
	Curve   PowerCurveTrend `json:"power_curve"`
	Fitness []FitnessPoint  `json:"fitness"` // daily, all sports
	Ramp    []RampPoint     `json:"ramp"`    // weekly CTL change

	LoadWeeks  []LoadBucket `json:"load_weeks"`
	LoadMonths []LoadBucket `json:"load_months"`

	Buckets     []TrendBucket   `json:"buckets"`
	Rides       []TrendRide     `json:"rides"` // per-ride points for the scatter charts
	BestEfforts BestEffortTrend `json:"best_efforts"`
	Consistency Consistency     `json:"consistency"`
}

// FTPTrend is the FTP set (the general line and the indoor line, as steps)
// against the FTP the rides implied: weekly, the higher of 95% of the best 20
// minutes and the best hour over the previous 90 days. The general estimate
// counts indoor efforts against the general line, as the FTP check does; the
// indoor estimate is from indoor rides alone, against the indoor line, and
// only when the rider has one.
type FTPTrend struct {
	General        []FTPPoint `json:"general"`
	Indoor         []FTPPoint `json:"indoor"`
	Estimate       []FTPPoint `json:"estimate"`
	IndoorEstimate []FTPPoint `json:"indoor_estimate"`
}

// FTPPoint is an FTP at a time (unix seconds).
type FTPPoint struct {
	T int64 `json:"t"`
	W int   `json:"w"`
}

// PowerCurveTrend is best power by duration: all-time, the last 90 days, the
// last 28 days.
type PowerCurveTrend struct {
	Durations []int  `json:"durations"`
	AllTime   []*int `json:"all_time"`
	Days90    []*int `json:"days_90"`
	Days28    []*int `json:"days_28"`
}

// RampPoint is how far fitness (CTL) moved in the week starting WeekStart.
type RampPoint struct {
	WeekStart string  `json:"week_start"`
	CTLChange float64 `json:"ctl_change"`
}

// LoadBucket is one week's or month's training, all sports, with what was
// planned for it.
type LoadBucket struct {
	Start               string  `json:"start"`
	Activities          int     `json:"activities"`
	DurationSecs        int     `json:"duration_secs"`
	DistanceMeters      float64 `json:"distance_meters"`
	ElevationGainMeters float64 `json:"elevation_gain_meters"`
	TSS                 float64 `json:"tss"`
	PlannedTSS          float64 `json:"planned_tss"`
	PlannedSecs         int     `json:"planned_secs"`
}

// TrendBucket is one week or month of markers.
type TrendBucket struct {
	Start         string     `json:"start"`
	EF            EnvMarkers `json:"ef"`
	HRAtPower     EnvMarkers `json:"hr_at_power_bpm"`
	PowerAtHR     EnvMarkers `json:"power_at_hr_watts"`
	DecouplingPct *Marker    `json:"decoupling_pct,omitempty"`
	CadenceRPM    EnvMarkers `json:"cadence_rpm"`
	// IFBands counts cycling rides by intensity factor band
	// (fitness.IFBandEdges).
	IFBands [5]int          `json:"if_bands"`
	Split   *IntensitySplit `json:"intensity_split,omitempty"`
	AvgRPE  *Marker         `json:"avg_rpe,omitempty"`
	TSS     float64         `json:"tss"`
	// Days with a ride, and days with only other workouts; Days is how many
	// days the bucket has inside the range.
	CyclingDays int `json:"cycling_days"`
	OtherDays   int `json:"other_days"`
	Days        int `json:"days"`
}

// TrendRide is one ride's points for the scatter charts.
type TrendRide struct {
	ID            string   `json:"id"`
	Day           string   `json:"day"`
	Indoor        bool     `json:"indoor"`
	EF            *float64 `json:"ef,omitempty"`             // steady rides
	DecouplingPct *float64 `json:"decoupling_pct,omitempty"` // long steady rides
	TSS           *float64 `json:"tss,omitempty"`
	RPE           *int     `json:"rpe,omitempty"`
}

// BestEffortTrend is the best power each month at the key durations.
type BestEffortTrend struct {
	Durations []int             `json:"durations_secs"`
	Months    []BestEffortMonth `json:"months"`
}

// BestEffortMonth is one month's bests, in Durations order (null: no effort
// that long).
type BestEffortMonth struct {
	Month string `json:"month"` // YYYY-MM
	Watts []*int `json:"watts"`
}

// Consistency is how regularly the rider trains: streaks of days and of
// weeks with a workout, over all their history.
type Consistency struct {
	CurrentStreakDays int      `json:"current_streak_days"`
	LongestStreakDays int      `json:"longest_streak_days"`
	LongestStreakEnd  string   `json:"longest_streak_end,omitempty"`
	CurrentWeekStreak int      `json:"current_week_streak"`
	LongestWeekStreak int      `json:"longest_week_streak"`
	AvgDaysPerWeek    *float64 `json:"avg_days_per_week,omitempty"` // over the range
}

// YearOverYear is each year's training day by day, to accumulate against
// each other.
type YearOverYear struct {
	Scope  string        `json:"scope"`
	Scopes []string      `json:"scopes"`
	Years  []YearOfDays  `json:"years"`
	Today  YearDayOfYear `json:"today"`
}

// YearOfDays is one calendar year's training days.
type YearOfDays struct {
	Year int         `json:"year"`
	Days []DayTotals `json:"days"`
}

// YearDayOfYear places a day on a leap-year calendar (1–366), so March 1 is
// day 61 in every year and years line up date for date.
type YearDayOfYear struct {
	Year int `json:"year"`
	Day  int `json:"day_of_year"`
}

// DayTotals is one day's training.
type DayTotals struct {
	DayOfYear           int     `json:"day_of_year"`
	DurationSecs        int     `json:"duration_secs"`
	DistanceMeters      float64 `json:"distance_meters"`
	ElevationGainMeters float64 `json:"elevation_gain_meters"`
	TSS                 float64 `json:"tss"`
}
