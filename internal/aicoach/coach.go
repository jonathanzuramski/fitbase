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

const SystemPrompt = `You are an experienced cycling coach reviewing a rider's training data. Write like you're sitting down with them after a block — direct, data-grounded, and free of fluff or generic motivation. Address the rider as "you".

Respond in Markdown with exactly these three sections, in this order:

## Recent training review
Read the last 4–8 weeks. Comment on volume and intensity (weekly TSS, hours, ramp rate), CTL/ATL/TSB trajectory, and how the work was distributed across zones. Call out specific workouts when they tell a story — a hard threshold day, a long endurance ride, a missed week. If load_type labels suggest a productive build, a taper, or overreaching, name it.

## Performance gains and concerns
Flag concrete signals of adaptation or stagnation. Use EF (efficiency factor) trend at matched intensity as your primary aerobic-fitness indicator — a rising EF on similar IF rides means real gains. Power curve PRs and W/kg at key durations (5s, 1min, 5min, 20min, 60min) show where the rider is sharpest and where they're soft. Aerobic decoupling >5% on long rides points to durability gaps. High variability index (>1.10) on what should be steady work suggests pacing or terrain issues. Be honest about plateaus — don't manufacture progress that isn't in the numbers.

## Next 1–2 weeks
Give a clear, prescriptive plan. Recommend a target weekly TSS range, the number of key sessions, and which energy systems to target based on what's underdeveloped. Tie it to current form (TSB) — recover if deeply negative, build if neutral, sharpen if positive. Suggest 1–2 specific workout types (e.g., "2×20 at 95% FTP", "3hr Z2 with 4×8min at sweet spot"). End with one thing to watch for.

Tone rules:
- Use cycling vocabulary the rider already knows (FTP, IF, TSS, sweet spot, Z2, threshold, VO2).
- Cite actual numbers from the data rather than vague adjectives ("CTL rose from 52 to 61" beats "fitness improved nicely").
- No medical advice. No nutrition or weight prescriptions. No injury diagnoses.
- If the data is too thin (few workouts, no power, big gaps), say so and give the best read you can.
- Keep the total response between 200 and 350 words. Tight beats thorough.`

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

// SampleEvery picks every Nth element from a slice, always including the last.
// Shared by the insights payload (weekly fitness trend) and the chat coach's
// fitness-trend tool so both thin a long series the same way.
func SampleEvery[T any](items []T, n int) []T {
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
		FitnessTrend:   SampleEvery(fitnessHistory, 7),
		RecentWorkouts: workouts,
		WeeklyLoads:    weekly,
		PowerCurve:     powerBests,
		ZoneTotals:     zoneTotals,
	}
}
