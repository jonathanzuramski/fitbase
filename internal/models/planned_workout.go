package models

import (
	"errors"
	"fmt"
	"time"
)

// maxIntervalDepth caps repeat-group nesting. A group of groups (depth 2) covers
// anything real; deeper is almost certainly a malformed or looping payload, so we
// reject it rather than recurse without bound.
const maxIntervalDepth = 3

// PlannedInterval is one node in a structured planned workout. A node is either
// a *leaf step* (a duration with an optional power target) or a *repeat group*
// (a Steps sequence run Repeat times) — never both. This mirrors how Zwift
// (IntervalsT) and intervals.icu (Nx blocks) model repeated efforts: the repeat
// wraps a group, so "5×(1min hard / 1min easy)" is a single node holding two
// child steps, not ten flattened intervals.
type PlannedInterval struct {
	// ── Leaf fields (ignored on a repeat group) ──────────────────────────────
	Kind         string `json:"kind,omitempty"` // "warmup" | "work" | "recovery" | "cooldown" | "endurance" | "sweet_spot" | "threshold" | "vo2max"
	DurationSecs int    `json:"duration_secs,omitempty"`

	// Power target — all optional. A *flat* target sets one of TargetPctFTP /
	// TargetWatts / TargetZone. A *ramp* (for ERG / smart-trainer control) sets
	// TargetPctFTPLow + TargetPctFTPHigh instead, sweeping power linearly across
	// the step. Leave everything unset for a free/no-target step (e.g. "30 min
	// endurance, ride to feel"). A flat target and a ramp are mutually exclusive.
	TargetPctFTP     *int   `json:"target_pct_ftp,omitempty"`
	TargetPctFTPLow  *int   `json:"target_pct_ftp_low,omitempty"`
	TargetPctFTPHigh *int   `json:"target_pct_ftp_high,omitempty"`
	TargetWatts      *int   `json:"target_watts,omitempty"`
	TargetZone       string `json:"target_zone,omitempty"` // e.g. "Z2", "Z4"
	Note             string `json:"note,omitempty"`

	// ── Repeat-group fields ───────────────────────────────────────────────────
	// When Steps is non-empty this node is a group: its children run in order,
	// Repeat times. Repeat also applies to a bare leaf (repeat a single step).
	// Repeat of 0 or 1 means "once".
	Repeat int               `json:"repeat,omitempty"`
	Steps  []PlannedInterval `json:"steps,omitempty"`
}

// IsGroup reports whether this node is a repeat group (has child steps) rather
// than a leaf step.
func (iv PlannedInterval) IsGroup() bool { return len(iv.Steps) > 0 }

// reps normalizes Repeat so 0 means "once".
func (iv PlannedInterval) reps() int {
	if iv.Repeat < 1 {
		return 1
	}
	return iv.Repeat
}

// TotalSecs returns the wall-clock duration of this node including repeats: a
// leaf is DurationSecs × reps; a group is reps × the sum of its children. This
// is what feeds PlannedWorkout.DurationSecs so the calendar total never disagrees
// with the steps that make it up.
func (iv PlannedInterval) TotalSecs() int {
	if iv.IsGroup() {
		sum := 0
		for _, s := range iv.Steps {
			sum += s.TotalSecs()
		}
		return iv.reps() * sum
	}
	return iv.reps() * iv.DurationSecs
}

// hasTarget reports whether any power target is set — used to reject a repeat
// group that also carries leaf-only fields.
func (iv PlannedInterval) hasTarget() bool {
	return iv.TargetPctFTP != nil || iv.TargetPctFTPLow != nil ||
		iv.TargetPctFTPHigh != nil || iv.TargetWatts != nil || iv.TargetZone != ""
}

// Validate checks one interval node, recursing into a group's children. It is
// the single source of truth for interval shape, called from the API layer for
// both manual quick-add and coach-proposed workouts.
func (iv PlannedInterval) Validate() error { return iv.validate(1) }

func (iv PlannedInterval) validate(depth int) error {
	if depth > maxIntervalDepth {
		return fmt.Errorf("interval nesting too deep (max %d levels)", maxIntervalDepth)
	}

	if iv.IsGroup() {
		// A group only repeats its children; leaf fields here are ambiguous.
		if iv.DurationSecs != 0 || iv.hasTarget() {
			return errors.New("a repeat group cannot set duration/target — put those on its steps")
		}
		if iv.Repeat < 0 {
			return errors.New("repeat must be >= 0")
		}
		for _, s := range iv.Steps {
			if err := s.validate(depth + 1); err != nil {
				return err
			}
		}
		return nil
	}

	// Leaf step.
	if iv.DurationSecs <= 0 {
		return errors.New("each interval needs duration_secs > 0")
	}
	if iv.TargetPctFTP != nil && (*iv.TargetPctFTP < 0 || *iv.TargetPctFTP > 200) {
		return errors.New("target_pct_ftp must be 0–200")
	}

	// Ramp: both ends are required together, each in range, low <= high, and a
	// ramp cannot be combined with a flat %FTP target.
	low, high := iv.TargetPctFTPLow, iv.TargetPctFTPHigh
	if (low == nil) != (high == nil) {
		return errors.New("a ramp needs both target_pct_ftp_low and target_pct_ftp_high")
	}
	if low != nil {
		if iv.TargetPctFTP != nil {
			return errors.New("set either a flat target_pct_ftp or a ramp (low/high), not both")
		}
		if *low < 0 || *low > 200 || *high < 0 || *high > 200 {
			return errors.New("ramp targets must be 0–200")
		}
		if *low > *high {
			return errors.New("ramp target_pct_ftp_low must be <= target_pct_ftp_high")
		}
	}
	if iv.TargetWatts != nil && *iv.TargetWatts < 0 {
		return errors.New("target_watts must be >= 0")
	}
	return nil
}

// PlannedWorkout is a future-dated training target shown on the calendar.
// It is independent of the completed `workouts` table; nothing auto-links them
// in v1. Intervals is optional — a planned workout can be just a date + target.
type PlannedWorkout struct {
	ID              string            `json:"id"`
	PlannedDate     time.Time         `json:"planned_date"` // date only, stored as DATE in SQLite
	Sport           string            `json:"sport"`
	Title           string            `json:"title"`
	Description     string            `json:"description"`
	DurationSecs    int               `json:"duration_secs"`
	TSS             *float64          `json:"tss,omitempty"`
	IntensityFactor *float64          `json:"intensity_factor,omitempty"`
	Intervals       []PlannedInterval `json:"intervals,omitempty"`
	Source          string            `json:"source"` // "manual" | "coach"
	CreatedAt       time.Time         `json:"created_at"`
}
