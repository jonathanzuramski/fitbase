package fitness

// Readiness tones group the recommendations for display: fresh enough to go
// hard, normal training load, back off (or ease back in), and rest.
const (
	ToneGo    = "go"
	ToneTrain = "train"
	ToneEase  = "ease"
	ToneRest  = "rest"
)

// Recommend turns form (TSB), the 28-day ramp rate and the days since the
// last ride into a recommendation: a short label, one line of detail, and a
// tone. It is the one source for every surface (the dashboard badge, the
// readiness API and the coach), so they can't disagree.
func Recommend(form, rampRate float64, daysSince int) (label, detail, tone string) {
	switch {
	case daysSince > 7:
		return "Resume Training", "More than a week without training — fitness is declining. Ease back in with a light ride.", ToneEase
	case rampRate > 10:
		return "Ease Up", "Training load has increased sharply over the last 4 weeks. Prioritise sleep and monitor fatigue.", ToneEase
	case form > 15:
		return "Race Ready", "You're very fresh. Consider a peak effort or race — extended rest risks fitness loss.", ToneGo
	case form > 5:
		return "Go Ride", "Good form. Ready for a quality session or race effort.", ToneGo
	case form >= -10:
		return "Maintain", "Normal training load. Keep building consistency.", ToneTrain
	case form >= -25:
		return "Training Block", "Productive fatigue — you're adapting. A recovery day will pay off soon.", ToneTrain
	case form >= -40:
		return "Ease Up", "Heavy fatigue. Schedule a recovery ride or rest day before your next hard effort.", ToneEase
	default:
		return "Rest", "Significant overreach — rest now to avoid illness or injury.", ToneRest
	}
}
