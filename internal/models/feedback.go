package models

import "time"

// RPE bounds: the rider's rating of how hard a ride felt, on the 1–10 scale.
const (
	MinRPE = 1
	MaxRPE = 10
	// MaxFeedbackNote caps the rider's note on a ride.
	MaxFeedbackNote = 2000
)

// WorkoutFeedback is what the rider says about a ride: how hard it felt (RPE)
// and a note. Numbers can't tell "felt awful" from "felt easy", or why a ride
// was cut short; this can.
type WorkoutFeedback struct {
	WorkoutID string    `json:"workout_id"`
	RPE       *int      `json:"rpe,omitempty"` // 1–10; nil = not rated
	Note      string    `json:"note,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RPELabel names an RPE rating in words.
func RPELabel(rpe int) string {
	switch {
	case rpe <= 2:
		return "very easy"
	case rpe <= 4:
		return "easy"
	case rpe <= 6:
		return "moderate"
	case rpe <= 8:
		return "hard"
	default:
		return "max effort"
	}
}
