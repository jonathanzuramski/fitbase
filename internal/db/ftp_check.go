package db

import (
	"encoding/json"
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
// efforts over the last fitness.FTPCheckWindowDays, as of now.
func (db *DB) FTPCheck(now time.Time) (models.FTPCheck, error) {
	athlete, err := db.GetAthlete()
	if err != nil {
		return models.FTPCheck{}, err
	}
	curve, err := db.GetPowerCurveSince(now.AddDate(0, 0, -fitness.FTPCheckWindowDays))
	if err != nil {
		return models.FTPCheck{}, err
	}
	effort := func(secs int) *models.FTPEffort {
		best, ok := curve[secs]
		if !ok || best.Watts <= 0 {
			return nil
		}
		e := &models.FTPEffort{DurationSecs: secs, Watts: best.Watts, WorkoutID: best.WorkoutID}
		if w, err := db.GetWorkout(best.WorkoutID); err == nil && w != nil {
			e.Day = w.TrainingDay
			if e.Day == "" {
				e.Day = w.RecordedAt.Format("2006-01-02")
			}
		}
		return e
	}
	check := fitness.CheckFTP(athlete.FTPWatts, effort(1200), effort(3600))
	check.Dismissed = db.ftpCheckDismissed(check, now)
	return check, nil
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
// (ftp_history). A history entry is logged when the value changes, or when
// there is no history yet (so the FTP stops counting as the seeded default).
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
	hasHistory, err := db.HasFTPHistory()
	if err != nil {
		return err
	}
	if ftp != current || !hasHistory {
		return db.LogFTPChange(ftp)
	}
	return nil
}
