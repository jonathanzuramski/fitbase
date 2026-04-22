// Package aicoach sends structured training data to an LLM and returns
// personalized coaching insights in three sections. Providers implement the
// Provider interface and self-register in init(); adding a new backend is a
// single new file.
package aicoach

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ── Data structures sent to the LLM ──────────────────────────────────────────

// CoachingData is the rich dataset sent to the LLM. All fields are optional so
// the coach degrades gracefully when the athlete has limited history.
type CoachingData struct {
	GeneratedAt    string         `json:"generated_at"`
	Athlete        AthleteProfile `json:"athlete"`
	CurrentFitness FitnessMetrics `json:"current_fitness"`
	FitnessTrend   []FitnessPoint `json:"fitness_trend"`
	RecentWorkouts []WorkoutBrief `json:"recent_workouts_56d"`
	WeeklyLoads    []WeeklyLoad   `json:"weekly_loads_8w"`
	PowerCurve     []PowerBest    `json:"power_curve_key_efforts"`
	ZoneTotals     ZoneDist       `json:"zone_distribution_56d"`
}

type AthleteProfile struct {
	FTPWatts    int     `json:"ftp_watts"`
	WeightKG    float64 `json:"weight_kg"`
	ThresholdHR int     `json:"threshold_hr_bpm,omitempty"`
	MaxHR       int     `json:"max_hr_bpm,omitempty"`
}

type FitnessMetrics struct {
	Fitness float64 `json:"fitness_ctl"`
	Fatigue float64 `json:"fatigue_atl"`
	Form    float64 `json:"form_tsb"`
}

type FitnessPoint struct {
	Date    string  `json:"date"`
	Fitness float64 `json:"ctl"`
	Fatigue float64 `json:"atl"`
	Form    float64 `json:"form"`
}

type WorkoutBrief struct {
	Date            string   `json:"date"`
	Sport           string   `json:"sport"`
	DurationMins    float64  `json:"duration_mins"`
	DistanceKM      float64  `json:"distance_km,omitempty"`
	ElevationM      float64  `json:"elevation_gain_m,omitempty"`
	AvgPowerWatts   *float64 `json:"avg_power_w,omitempty"`
	NormalizedPower *float64 `json:"normalized_power_w,omitempty"`
	AvgHR           *int     `json:"avg_hr_bpm,omitempty"`
	TSS             *float64 `json:"tss,omitempty"`
	IntensityFactor *float64 `json:"intensity_factor,omitempty"`
	EF              *float64 `json:"efficiency_factor,omitempty"`      // NP ÷ avgHR — rising EF at matched IF = aerobic gain
	VI              *float64 `json:"variability_index,omitempty"`      // NP ÷ avgPower — 1.00 steady, >1.05 surgey
	DecouplingPct   *float64 `json:"aerobic_decoupling_pct,omitempty"` // Pw:HR drift 1st→2nd half; long rides only; <5% good
	Indoor          bool     `json:"indoor,omitempty"`
}

type WeeklyLoad struct {
	Week          string  `json:"week"`
	TSS           float64 `json:"tss"`
	HoursTraining float64 `json:"hours_training"`
	WorkoutCount  int     `json:"workout_count"`
	LoadType      string  `json:"load_type"`
}

type PowerBest struct {
	Duration string  `json:"duration"`
	Watts    int     `json:"watts"`
	WPerKG   float64 `json:"w_per_kg"`
	PctFTP   float64 `json:"pct_ftp"`
}

// ZoneValues carries a zone distribution with an explicit unit. Unit flips
// between "hours" and "minutes" per array based on total volume so the numbers
// stay readable — a 56-day window where Z5 holds ~1.5h reads much better as
// 90 min than as 1.5h.
type ZoneValues struct {
	Unit   string    `json:"unit"` // "hours" (1 decimal) or "minutes" (whole)
	Values []float64 `json:"values"`
}

type ZoneDist struct {
	PowerZones ZoneValues `json:"power_zones"`
	HRZones    ZoneValues `json:"hr_zones"`
}

// ── Prompt ────────────────────────────────────────────────────────────────────

const SystemPrompt = `You are the rider's personal cycling coach. Give an informative breakdown of the riders recent rides. Focus on the positive, but also give constructive criticism if you have to.

The rider opens this on their dashboard daily. They want signal, not a report. Target total length: 350–500 words across all three sections. Every sentence must earn its place — if you can delete it and lose nothing, delete it.

Respond in Markdown with exactly these three sections (## headings, no subheadings):

## Training
3-5 sentences on the current training block. Lead with the single most important observation — a volume trend, a missing workout type, or a red flag. Anchor it with specific numbers.

## Performance
3-5 sentences on metrics that tangible performance improvements/drops. This includes all power outputs, HR, and other stats that would show the athlete is gaining strong fitness! If the user rode today, make sure you use that as one of the key metrics when talking about performance improvements.

## Fitness
One line with current **CTL**, **ATL**, **TSB**. Then 3-4 sentences on what's happening physiologically and what the rider should do this week — keep building, back off, add intensity, take a recovery day. End with a concrete next action, not a platitude.

Formatting:
- **Bold** the key numbers (**FTP 240W**, **CTL 44.6**, **TSB -12**).
- Bullets only for 3+ discrete items worth scanning.
- Tables only when comparing the same metric across weeks, durations, or sessions — not for 2-row comparisons that read better as prose.
- Always include units (W, W/kg, bpm, %, TSS) and round sensibly.

Voice: a coach who knows this rider. Short complete sentences. Direct, not effusive. Banned phrases: "let's", "it's worth noting", "significant", "noteworthy", "bottom line", "going forward", "take the recovery you've earned". No preamble, no sign-off, no motivational closers. If something is fine, say so in one sentence and move on. If something needs attention, say exactly what to do about it.`

// MaxTokens caps the generated markdown length. Three 2–4 paragraph sections
// comfortably fit in 4096 tokens.
const MaxTokens = 4096

// ── Coach ─────────────────────────────────────────────────────────────────────

// Coach resolves a Provider at construction time and calls it with coaching data.
type Coach struct {
	provider Provider
	model    string
	apiKey   string
}

// New resolves the provider from the registry. Returns an error if the provider
// is unknown — callers should surface this to the user to prompt re-configuration.
func New(providerName, model, apiKey string) (*Coach, error) {
	p, ok := Get(providerName)
	if !ok {
		return nil, fmt.Errorf("unknown AI provider %q", providerName)
	}
	return &Coach{provider: p, model: model, apiKey: apiKey}, nil
}

// StreamInsights sends the training data to the provider and invokes onChunk
// for each markdown text delta as it arrives. The full concatenated response
// is returned on success so callers can cache it.
func (c *Coach) StreamInsights(ctx context.Context, data *CoachingData, onChunk func(string) error) (string, error) {
	dataJSON, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal coaching data: %w", err)
	}
	userMsg := "Here is the rider's training data:\n\n" + string(dataJSON) +
		"\n\nRespond in Markdown with the three ## sections. Keep it terse — 200–350 words total."

	var full strings.Builder
	err = c.provider.Stream(ctx, CallConfig{
		Model:     c.model,
		APIKey:    c.apiKey,
		System:    SystemPrompt,
		User:      userMsg,
		MaxTokens: MaxTokens,
	}, func(chunk string) error {
		full.WriteString(chunk)
		if onChunk != nil {
			return onChunk(chunk)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return full.String(), nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// KeyDurations lists the effort durations (in seconds) reported in the power curve.
var KeyDurations = []struct {
	Secs  int
	Label string
}{
	{5, "5s"},
	{30, "30s"},
	{60, "1min"},
	{300, "5min"},
	{1200, "20min"},
	{3600, "60min"},
}

// sampleEvery picks every Nth element from a slice (always including the last).
func sampleEvery[T any](items []T, n int) []T {
	if n <= 1 || len(items) <= n {
		return items
	}
	var out []T
	for i, v := range items {
		if i%n == 0 || i == len(items)-1 {
			out = append(out, v)
		}
	}
	return out
}

// BuildData converts raw DB results into the CoachingData sent to the LLM.
func BuildData(
	athlete AthleteProfile,
	fitnessHistory []FitnessPoint,
	workouts []WorkoutBrief,
	weekly []WeeklyLoad,
	powerBests []PowerBest,
	zoneTotals ZoneDist,
) *CoachingData {
	var current FitnessMetrics
	if len(fitnessHistory) > 0 {
		last := fitnessHistory[len(fitnessHistory)-1]
		current = FitnessMetrics{Fitness: last.Fitness, Fatigue: last.Fatigue, Form: last.Form}
	}
	return &CoachingData{
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
		Athlete:        athlete,
		CurrentFitness: current,
		FitnessTrend:   sampleEvery(fitnessHistory, 7),
		RecentWorkouts: workouts,
		WeeklyLoads:    weekly,
		PowerCurve:     powerBests,
		ZoneTotals:     zoneTotals,
	}
}
