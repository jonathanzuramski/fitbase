package db

import (
	"encoding/json"
	"math"
	"time"

	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
)

// metaFTPCheckDismissed records the rider choosing to keep their FTP despite
// the check (ftpCheckDismissal, as JSON).
const metaFTPCheckDismissed = "ftp_check_dismissed"

// ftpCheckSnooze is how long "keep my FTP" hides the prompt. The verdict is
// still shown in settings; it just stops asking until then.
const ftpCheckSnooze = 30 * 24 * time.Hour

type ftpCheckDismissal struct {
	FTPWatts int       `json:"ftp_watts"`
	Status   string    `json:"status"`
	At       time.Time `json:"at"`
}

// FTPCheck compares the athlete's FTP with their best 20- and 60-minute
// cycling efforts over the last fitness.FTPCheckWindowDays, as of now.
//
// The FTP is the general line. Indoor rides of a rider with a separate indoor
// FTP line are scored against that line, so an indoor effort is compared in
// general-line terms (ftpEffort). Outdoor and indoor rides each keep their own
// best: once scaled, an indoor effort below the best outdoor one can win.
func (db *DB) FTPCheck(now time.Time) (models.FTPCheck, error) {
	athlete, err := db.GetAthlete()
	if err != nil {
		return models.FTPCheck{}, err
	}
	from, to := now.AddDate(0, 0, -fitness.FTPCheckWindowDays), time.Now().AddDate(1, 0, 0)
	var curves []map[int]models.AllTimeBest
	for _, rides := range []int{outdoorRides, indoorRides} {
		curve, err := db.cyclingPowerCurve(from, to, rides)
		if err != nil {
			return models.FTPCheck{}, err
		}
		curves = append(curves, curve)
	}
	effort := func(secs int) *models.FTPEffort {
		var best *models.FTPEffort
		for _, curve := range curves {
			b, ok := curve[secs]
			if !ok || b.Watts <= 0 {
				continue
			}
			if e := db.ftpEffort(secs, b); best == nil || e.CheckWatts() > best.CheckWatts() {
				best = e
			}
		}
		return best
	}
	check := fitness.CheckFTP(athlete.FTPWatts, effort(1200), effort(3600))
	check.Dismissed = db.ftpCheckDismissed(check, now)
	return check, nil
}

// ftpEffort describes a best effort for the FTP check: the ride's day and, for
// an indoor ride on a separate indoor FTP line, the effort against the general
// line. 238 W for 20 minutes on a 230 W indoor line is what a 250 W rider
// holds indoors, not a sign their FTP is set high.
func (db *DB) ftpEffort(secs int, best models.AllTimeBest) *models.FTPEffort {
	e := &models.FTPEffort{DurationSecs: secs, Watts: best.Watts, WorkoutID: best.WorkoutID}
	w, err := db.GetWorkout(best.WorkoutID)
	if err != nil || w == nil {
		return e
	}
	e.Day = w.TrainingDay
	if e.Day == "" {
		e.Day = w.RecordedAt.Format("2006-01-02")
	}
	if w.IsIndoor {
		e.Indoor = true
		general, line := db.GetFTPAtDate(w.RecordedAt), db.GetFTPForRide(w.RecordedAt, true)
		if general > 0 && line > 0 && line != general {
			e.IndoorFTPWatts = line
			e.EquivalentWatts = int(math.Round(float64(best.Watts) * float64(general) / float64(line)))
		}
	}
	return e
}

// DismissFTPCheck records that the rider is keeping their FTP despite check.
// The prompt stays hidden for ftpCheckSnooze, or until the FTP or the verdict
// changes (a "may be high" dismissal doesn't hide later proof it is low).
func (db *DB) DismissFTPCheck(check models.FTPCheck, now time.Time) error {
	raw, err := json.Marshal(ftpCheckDismissal{FTPWatts: check.FTPWatts, Status: check.Status, At: now.UTC()})
	if err != nil {
		return err
	}
	return db.SetMeta(metaFTPCheckDismissed, string(raw))
}

func (db *DB) ftpCheckDismissed(check models.FTPCheck, now time.Time) bool {
	raw, err := db.GetMeta(metaFTPCheckDismissed)
	if err != nil || raw == "" {
		return false
	}
	var d ftpCheckDismissal
	if json.Unmarshal([]byte(raw), &d) != nil {
		return false
	}
	return d.FTPWatts == check.FTPWatts && d.Status == check.Status && now.Sub(d.At) < ftpCheckSnooze
}

// SetFTP changes the athlete's FTP alone, effective now, leaving the rest of
// the profile as it is. Past rides keep the FTP that was active on their date
// (ftp_history), logged only when the value changes (logFTPIfChanged).
func (db *DB) SetFTP(ftp int) error {
	var current int
	if err := db.QueryRow("SELECT ftp_watts FROM athlete WHERE id=1").Scan(&current); err != nil {
		return err
	}
	if _, err := db.Exec(
		"UPDATE athlete SET ftp_watts=?, updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id=1", ftp,
	); err != nil {
		return err
	}
	return db.logFTPIfChanged(current, ftp)
}

// IndoorByFilename maps the filename of every workout whose filename starts
// with prefix to its indoor flag (e.g. "intervals-" for rides synced from
// intervals.icu, named intervals-<activity id>.fit).
func (db *DB) IndoorByFilename(prefix string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT filename, is_indoor FROM workouts WHERE filename LIKE ? || '%'`, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	out := map[string]bool{}
	for rows.Next() {
		var name string
		var indoor bool
		if err := rows.Scan(&name, &indoor); err != nil {
			return nil, err
		}
		out[name] = indoor
	}
	return out, rows.Err()
}
