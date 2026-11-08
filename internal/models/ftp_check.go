package models

// FTP check statuses: how the athlete's FTP compares with what their recent
// rides show. fitness.CheckFTP holds the rules.
const (
	FTPCheckOK         = "ok"         // a recent effort backs the FTP
	FTPCheckRaise      = "raise"      // rides show more than the FTP allows: it is set low
	FTPCheckLower      = "lower"      // the best recent efforts fall short: it may be set high
	FTPCheckUnverified = "unverified" // no effort hard enough to judge either way
)

// FTPEffort is a best average power from the power curve, and the ride it
// came from.
type FTPEffort struct {
	DurationSecs int    `json:"duration_secs"`
	Watts        int    `json:"watts"`
	WorkoutID    string `json:"workout_id"`
	Day          string `json:"day,omitempty"` // the ride's training day, YYYY-MM-DD
}

// FTPCheck compares the athlete's FTP with an estimate from their best recent
// efforts. Every number fitbase derives from power (zones, IF, TSS, fitness,
// and the coach's targets) is scaled by FTP, so a stale one skews them all.
type FTPCheck struct {
	Status     string `json:"status"`
	FTPWatts   int    `json:"ftp_watts"`
	WindowDays int    `json:"window_days"`
	// EstimateWatts is what the best effort implies (0 when no effort
	// qualifies), and PctOfFTP is that estimate as a percentage of FTP.
	EstimateWatts int     `json:"estimate_watts,omitempty"`
	PctOfFTP      float64 `json:"pct_of_ftp,omitempty"`
	// Basis is the effort the estimate comes from.
	Basis *FTPEffort `json:"basis,omitempty"`
	// Dismissed is set when the rider chose to keep this FTP despite the
	// check; the prompt stays hidden for a while (see db.DismissFTPCheck).
	Dismissed bool `json:"dismissed"`
}

// Actionable reports whether the check suggests a different FTP, which is
// when the app offers to change it.
func (c FTPCheck) Actionable() bool {
	return c.Status == FTPCheckRaise || c.Status == FTPCheckLower
}
