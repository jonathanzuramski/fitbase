package models

import (
	"time"
)

// Workout holds the summary data for a single activity.
type Workout struct {
	ID                  string    `json:"id"`
	Filename            string    `json:"filename"`
	RecordedAt          time.Time `json:"recorded_at"`
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
	Date    time.Time `json:"date"`
	Fitness float64   `json:"fitness"` // Chronic Training Load (42-day EMA)
	Fatigue float64   `json:"fatigue"` // Acute Training Load (7-day EMA)
	Form    float64   `json:"form"`    // Fitness minus Fatigue
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

// WeeklyLoad holds aggregated training data for a single ISO week.
type WeeklyLoad struct {
	Week                string  `json:"week"`
	TSS                 float64 `json:"tss"`
	DurationSecs        int     `json:"duration_secs"`
	DistanceMeters      float64 `json:"distance_meters"`
	ElevationGainMeters float64 `json:"elevation_gain_meters"`
	WorkoutCount        int     `json:"workout_count"`
	LoadType            string  `json:"load_type"`
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
type WorkoutAnalysis struct {
	WorkoutID            string          `json:"workout_id"`
	PowerZones           []ZoneBreakdown `json:"power_zones"`
	HRZones              []ZoneBreakdown `json:"hr_zones"`
	VariabilityIndex     *float64        `json:"variability_index,omitempty"`
	EfficiencyFactor     *float64        `json:"efficiency_factor,omitempty"`
	AvgNP90Day           *float64        `json:"avg_np_90day_watts,omitempty"`
	AvgHR90Day           *float64        `json:"avg_hr_90day_bpm,omitempty"`
	AvgTSS90Day          *float64        `json:"avg_tss_90day,omitempty"`
	AvgIF90Day           *float64        `json:"avg_if_90day,omitempty"`
	AvgDuration90Day     *float64        `json:"avg_duration_90day_secs,omitempty"`
}

// ReadinessReport is a coaching snapshot: current form, load context, and a recommendation.
type ReadinessReport struct {
	Date                 string  `json:"date"`
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
	Date            string   `json:"date"`
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
	return WorkoutSummary{
		ID:              w.ID,
		Date:            w.RecordedAt.Format("2006-01-02"),
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
