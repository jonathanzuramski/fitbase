package models

import (
	"time"
)

// Workout holds the summary data for a single activity.
//
// Time model: RecordedAt is the UTC instant the ride started — the immutable
// truth used for ordering, duplicate detection, and training-load math.
// UTCOffsetSecs is the UTC offset in effect where/when the ride happened,
// captured once at import (device-declared → GPS timezone → athlete profile);
// rows read from the DB carry RecordedAt already re-homed into that offset,
// so formatting it yields the ride's own wall-clock time. TrainingDay is the
// ride-local calendar day derived from that offset — all day/week analytics
// bucket by it, never by per-query timezone math.
type Workout struct {
	ID                  string    `json:"id"`
	Filename            string    `json:"filename"`
	TrainingDay         string    `json:"training_day"`
	RecordedAt          time.Time `json:"recorded_at"`
	UTCOffsetSecs       *int      `json:"utc_offset_secs,omitempty"` // nil = not yet resolved
	StartLat            *float64  `json:"start_lat,omitempty"`       // first GPS fix; nil for indoor
	StartLng            *float64  `json:"start_lng,omitempty"`
	County              string    `json:"county,omitempty"` // e.g. "Boulder County"; "" outside US or indoor
	State               string    `json:"state,omitempty"`  // USPS code, e.g. "CO"
	Sport               string    `json:"sport"`
	DurationSecs        int       `json:"duration_secs"`
	ElapsedSecs         int       `json:"elapsed_secs"`
	DistanceMeters      float64   `json:"distance_meters"`
	ElevationGainMeters float64   `json:"elevation_gain_meters"`
	AvgPowerWatts       *float64  `json:"avg_power_watts,omitempty"`
	MaxPowerWatts       *float64  `json:"max_power_watts,omitempty"`
	NormalizedPower     *float64  `json:"normalized_power_watts,omitempty"`
	AvgHeartRate        *int      `json:"avg_heart_rate_bpm,omitempty"`
	MaxHeartRate        *int      `json:"max_heart_rate_bpm,omitempty"`
	AvgCadenceRPM       *int      `json:"avg_cadence_rpm,omitempty"`
	AvgSpeedMPS         float64   `json:"avg_speed_mps"`
	TSS                 *float64  `json:"tss,omitempty"`
	IntensityFactor     *float64  `json:"intensity_factor,omitempty"`
	IsIndoor            bool      `json:"is_indoor"`
	RouteID             *string   `json:"route_id,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

// Achievement is a personal-best trophy earned by a workout: rank 1–3 against
// every strictly earlier workout in the same category (route time/power,
// duration power bests, longest ride, most climbing). Computed once at import
// so a trophy records what was true when the ride happened.
type Achievement struct {
	WorkoutID string  `json:"workout_id"`
	Kind      string  `json:"kind"`
	Rank      int     `json:"rank"`
	Value     float64 `json:"value"`
}

// Stream is a single time-series data point within a workout.
type Stream struct {
	Timestamp      time.Time `json:"timestamp"`
	PowerWatts     *int      `json:"power_watts,omitempty"`
	HeartRateBPM   *int      `json:"heart_rate_bpm,omitempty"`
	CadenceRPM     *int      `json:"cadence_rpm,omitempty"`
	SpeedMPS       *float64  `json:"speed_mps,omitempty"`
	AltitudeMeters *float64  `json:"altitude_meters,omitempty"`
	Lat            *float64  `json:"lat,omitempty"`
	Lng            *float64  `json:"lng,omitempty"`
	DistanceMeters *float64  `json:"distance_meters,omitempty"`
}

// Athlete holds the athlete profile used for power calculations.
type Athlete struct {
	FTPWatts      int       `json:"ftp_watts"`
	WeightKG      float64   `json:"weight_kg"`
	ThresholdHR   int       `json:"threshold_hr_bpm"`
	MaxHR         int       `json:"max_hr_bpm"`
	RestingHR     int       `json:"resting_hr_bpm"`
	Age           int       `json:"age"`
	Location      string    `json:"location"`
	Language      string    `json:"language"`
	Timezone      string    `json:"timezone"`
	Units         string    `json:"units"` // "imperial" or "metric"
	SetupComplete bool      `json:"setup_complete"`
	HRZonesJSON   string    `json:"hr_zones_json,omitempty"` // JSON array of 4 upper BPM bounds (Z1–Z4 max; Z5 open-ended); empty = use calculated
	UpdatedAt     time.Time `json:"updated_at"`
}

// PowerZone represents a single power training zone.
type PowerZone struct {
	Label     string
	Name      string
	PctLow    int
	PctHigh   int // 0 = open-ended
	WattsLow  int
	WattsHigh int // 0 = open-ended
}

// HRZone represents a single heart-rate training zone.
type HRZone struct {
	Label   string
	Name    string
	PctLow  int
	PctHigh int // 0 = open-ended
	BPMLow  int
	BPMHigh int // 0 = open-ended
}

// FitnessPoint is a single day's Fitness/Fatigue/Form training load values.
type FitnessPoint struct {
	Date         time.Time `json:"date"`
	Fitness      float64   `json:"fitness"` // Chronic Training Load (42-day EMA)
	Fatigue      float64   `json:"fatigue"` // Acute Training Load (7-day EMA)
	Form         float64   `json:"form"`    // Fitness minus Fatigue
	IsProjection bool      `json:"is_projection,omitempty"`
}

// ZonesReport contains the athlete's current power and HR training zones.
type ZonesReport struct {
	FTPWatts    int         `json:"ftp_watts"`
	ThresholdHR int         `json:"threshold_hr_bpm"`
	PowerZones  []PowerZone `json:"power_zones"`
	HRZones     []HRZone    `json:"hr_zones"`
}

// PowerCurveEntry is a best effort for a single duration.
type PowerCurveEntry struct {
	DurationSecs  int     `json:"duration_secs"`
	DurationLabel string  `json:"duration_label"`
	Watts         int     `json:"watts"`
	WattsPerKG    float64 `json:"watts_per_kg"`
	PctFTP        float64 `json:"pct_ftp"`
	WorkoutID     string  `json:"workout_id"`
}

// PowerCurveReport is the response for the all-time power curve endpoint.
type PowerCurveReport struct {
	Entries  []PowerCurveEntry `json:"entries"`
	FTPWatts int               `json:"ftp_watts"`
	WeightKG float64           `json:"weight_kg"`
}

// WorkoutRef is a compact workout reference: high-level info plus the ID, so a
// consumer of WeeklyLoad can fetch the full workout when a drill-down is necessary.
type WorkoutRef struct {
	ID                  string  `json:"id"`
	TrainingDay         string  `json:"training_day"` // YYYY-MM-DD, ride local
	TSS                 float64 `json:"tss"`
	DurationSecs        int     `json:"duration_secs"`
	DistanceMeters      float64 `json:"distance_meters"`
	ElevationGainMeters float64 `json:"elevation_gain_meters"`
}

// WeeklyLoad holds aggregated training data for a single ISO week.
type WeeklyLoad struct {
	Week                string       `json:"week"`
	TSS                 float64      `json:"tss"`
	DurationSecs        int          `json:"duration_secs"`
	DistanceMeters      float64      `json:"distance_meters"`
	ElevationGainMeters float64      `json:"elevation_gain_meters"`
	WorkoutCount        int          `json:"workout_count"`
	LoadType            string       `json:"load_type"`
	WorkoutRefs         []WorkoutRef `json:"workout_refs"`
}

// ZoneBreakdown is time spent in a single training zone.
type ZoneBreakdown struct {
	Label     string  `json:"label"`
	Name      string  `json:"name"`
	Seconds   int     `json:"seconds"`
	PctTime   float64 `json:"pct_time"`
	WattsLow  int     `json:"watts_low,omitempty"`
	WattsHigh int     `json:"watts_high,omitempty"`
	BPMLow    int     `json:"bpm_low,omitempty"`
	BPMHigh   int     `json:"bpm_high,omitempty"`
}

// WorkoutAnalysis contains zone distribution, effort quality metrics, and 90-day context.
// SweetSpot, when present, is a reference band (88–94% FTP) reported alongside
// the 7-zone partition. Its time overlaps Z3/Z4 and is NOT subtracted from them
// — consumers should treat SweetSpotPct as a parallel slice, not an 8th zone.
type WorkoutAnalysis struct {
	WorkoutID        string          `json:"workout_id"`
	PowerZones       []ZoneBreakdown `json:"power_zones"`
	HRZones          []ZoneBreakdown `json:"hr_zones"`
	SweetSpot        *ZoneBreakdown  `json:"sweet_spot,omitempty"`
	VariabilityIndex *float64        `json:"variability_index,omitempty"`
	EfficiencyFactor *float64        `json:"efficiency_factor,omitempty"`
	AvgNP90Day       *float64        `json:"avg_np_90day_watts,omitempty"`
	AvgHR90Day       *float64        `json:"avg_hr_90day_bpm,omitempty"`
	AvgTSS90Day      *float64        `json:"avg_tss_90day,omitempty"`
	AvgIF90Day       *float64        `json:"avg_if_90day,omitempty"`
	AvgDuration90Day *float64        `json:"avg_duration_90day_secs,omitempty"`
}

// ReadinessReport is a coaching snapshot: current form, load context, and a recommendation.
type ReadinessReport struct {
	Date string `json:"date"`
	// AsOf is the day the load values are settled through: today once a
	// workout with TSS has been logged for today, otherwise yesterday. Form is
	// then the freshness the rider carries into today, not a forecast of a day off.
	AsOf                 string  `json:"as_of"`
	Fitness              float64 `json:"fitness"`
	Fatigue              float64 `json:"fatigue"`
	Form                 float64 `json:"form"`
	DaysSinceLastWorkout int     `json:"days_since_last_workout"`
	RampRate             float64 `json:"ramp_rate"`
	Recommendation       string  `json:"recommendation"`
	RecommendationDetail string  `json:"recommendation_detail"`
}

// AllTimeBest holds the best power for a given duration and which workout set it.
type AllTimeBest struct {
	Watts     int    `json:"watts"`
	WorkoutID string `json:"workout_id"`
}

// WorkoutSummary is a compact, prose-friendly representation for LLM consumption.
type WorkoutSummary struct {
	ID              string   `json:"id"`
	TrainingDay     string   `json:"training_day"`
	Sport           string   `json:"sport"`
	DurationMins    float64  `json:"duration_mins"`
	DistanceKM      float64  `json:"distance_km"`
	ElevationGainM  float64  `json:"elevation_gain_meters"`
	AvgPowerWatts   *float64 `json:"avg_power_watts,omitempty"`
	NormalizedPower *float64 `json:"normalized_power_watts,omitempty"`
	AvgHeartRate    *int     `json:"avg_heart_rate_bpm,omitempty"`
	TSS             *float64 `json:"tss,omitempty"`
	IntensityFactor *float64 `json:"intensity_factor,omitempty"`
}

func (w *Workout) ToSummary() WorkoutSummary {
	day := w.TrainingDay
	if day == "" {
		// Rows predating the training_day backfill fall back to the recorded
		// instant's calendar day.
		day = w.RecordedAt.Format("2006-01-02")
	}
	return WorkoutSummary{
		ID:              w.ID,
		TrainingDay:     day,
		Sport:           w.Sport,
		DurationMins:    float64(w.DurationSecs) / 60.0,
		DistanceKM:      w.DistanceMeters / 1000.0,
		ElevationGainM:  w.ElevationGainMeters,
		AvgPowerWatts:   w.AvgPowerWatts,
		NormalizedPower: w.NormalizedPower,
		AvgHeartRate:    w.AvgHeartRate,
		TSS:             w.TSS,
		IntensityFactor: w.IntensityFactor,
	}
}

// WorkoutShape is the interval-level view of one ride: efforts auto-detected
// from the power stream, repeated efforts grouped into sets, a coarse
// time-bucketed profile, and whatever was planned for that day. It exists so a
// consumer (the AI coach above all) can judge how a structured session was
// executed — whole-ride averages make 5×4min VO2 and steady tempo look alike.
type WorkoutShape struct {
	WorkoutID   string `json:"workout_id"`
	TrainingDay string `json:"training_day"`
	Sport       string `json:"sport"`
	ElapsedSecs int    `json:"elapsed_secs"`
	// FTPWatts is the FTP in effect on the ride's date; every pct_ftp and the
	// detection thresholds are relative to it.
	FTPWatts       int                `json:"ftp_watts,omitempty"`
	Detection      *EffortDetection   `json:"detection,omitempty"`
	Efforts        []EffortSummary    `json:"efforts"`
	OmittedEfforts int                `json:"omitted_efforts,omitempty"`
	Sets           []EffortSetSummary `json:"sets"`
	Profile        ShapeProfile       `json:"profile"`
	Planned        []PlannedWorkout   `json:"planned,omitempty"`
	Notes          []string           `json:"notes,omitempty"`
}

// EffortDetection states the thresholds an effort was detected against, so a
// reader knows what was (and wasn't) counted.
type EffortDetection struct {
	StartWatts int    `json:"start_watts"`
	FloorWatts int    `json:"floor_watts"`
	MinSecs    int    `json:"min_secs"`
	Rule       string `json:"rule"`
}

// EffortSummary is one detected effort. N is its 1-based position in the
// ride; Start is elapsed time from the start of the recording (h:mm:ss).
type EffortSummary struct {
	N                 int    `json:"n"`
	Start             string `json:"start"`
	DurationSecs      int    `json:"duration_secs"`
	AvgWatts          int    `json:"avg_watts"`
	PctFTP            int    `json:"pct_ftp"`
	NPWatts           *int   `json:"np_watts,omitempty"`
	MaxWatts          int    `json:"max_watts"`
	AvgHR             *int   `json:"avg_hr_bpm,omitempty"`
	MaxHR             *int   `json:"max_hr_bpm,omitempty"`
	AvgCadence        *int   `json:"avg_cadence_rpm,omitempty"`
	RecoveryAfterSecs *int   `json:"recovery_after_secs,omitempty"`
	RecoveryAvgWatts  *int   `json:"recovery_avg_watts,omitempty"`
}

// EffortSetSummary condenses a run of similar efforts ("5×4min") into the
// numbers a coach reads first: per-rep power and HR, fade, and HR creep.
type EffortSetSummary struct {
	FirstEffort      int     `json:"first_effort"` // N of the first rep
	Reps             int     `json:"reps"`
	AvgDurationSecs  int     `json:"avg_duration_secs"`
	AvgWatts         int     `json:"avg_watts"`
	AvgPctFTP        int     `json:"avg_pct_ftp"`
	RepDurationSecs  []int   `json:"rep_duration_secs"`
	RepAvgWatts      []int   `json:"rep_avg_watts"`
	RepMaxHR         []int   `json:"rep_max_hr_bpm,omitempty"`
	// LastVsFirstRepPct is the last rep's avg power relative to the first's:
	// negative means the rider faded, positive that they built. Named for the
	// comparison, not "fade", so a positive value can't be read as a fade.
	LastVsFirstRepPct float64 `json:"last_vs_first_rep_pct"`
	HRCreepBPM        *int    `json:"hr_creep_bpm,omitempty"` // last rep max HR minus first rep's
	AvgRecoverySecs  *int    `json:"avg_recovery_secs,omitempty"`
	AvgRecoveryWatts *int    `json:"avg_recovery_watts,omitempty"`
}

// ShapeProfile is the ride cut into fixed-length buckets (≤ 30). Buckets that
// fall entirely inside a stop are omitted.
type ShapeProfile struct {
	BucketSecs int           `json:"bucket_secs"`
	Buckets    []ShapeBucket `json:"buckets"`
}

// ShapeBucket averages one slice of the ride over its recorded seconds.
// AvgSpeedKPH is only set on rides without power.
type ShapeBucket struct {
	Start       string   `json:"start"`
	AvgWatts    *int     `json:"avg_watts,omitempty"`
	PctFTP      *int     `json:"pct_ftp,omitempty"`
	AvgHR       *int     `json:"avg_hr_bpm,omitempty"`
	AvgCadence  *int     `json:"avg_cadence_rpm,omitempty"`
	AvgSpeedKPH *float64 `json:"avg_speed_kph,omitempty"`
}
