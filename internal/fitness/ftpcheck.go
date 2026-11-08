package fitness

import (
	"math"

	"github.com/fitbase/fitbase/internal/models"
)

// FTPCheckWindowDays is how far back the FTP check looks for best efforts.
// Long enough that a few easy weeks don't read as lost fitness; short enough
// that last season's peak doesn't vouch for today's FTP.
const FTPCheckWindowDays = 90

// The FTP check compares an estimate from best efforts with the set FTP. The
// bands are asymmetric because the evidence is:
//
//   - Producing more power than FTP allows proves FTP is low, so any estimate
//     clearly above it (past ftpRaiseAbove) says raise.
//   - Not producing it proves nothing on its own: the rider may simply not have
//     ridden hard. A modest shortfall (down to ftpLowerFrom) after 90 days is
//     worth a "may be set high"; a large one more likely means no hard efforts
//     than a real 15%+ loss, so the check stays unverified and suggests no
//     number rather than a wrong one.
const (
	ftpRaiseAbove = 1.03
	ftpOKFrom     = 0.95
	ftpLowerFrom  = 0.85
)

// EstimateFTP returns the FTP implied by best efforts, and the effort it came
// from: the higher of 95% of the best 20-minute power (the standard field
// estimate) and the best 60-minute power (FTP by definition). Either effort
// may be nil; with neither it returns 0, nil.
func EstimateFTP(best20, best60 *models.FTPEffort) (float64, *models.FTPEffort) {
	var est float64
	var basis *models.FTPEffort
	if best20 != nil && best20.Watts > 0 {
		est, basis = 0.95*float64(best20.Watts), best20
	}
	if best60 != nil && float64(best60.Watts) > est {
		est, basis = float64(best60.Watts), best60
	}
	return est, basis
}

// EstimateRaisesFTP reports whether an FTP estimate is clearly above the set
// FTP, which is proof the FTP is low. Shared by the check and the single-ride
// "new FTP detected" banner so both use the same bar.
func EstimateRaisesFTP(estimate float64, ftp int) bool {
	return ftp > 0 && estimate > ftpRaiseAbove*float64(ftp)
}

// CheckFTP compares ftp with the estimate from the window's best 20- and
// 60-minute efforts (either may be nil). See the thresholds above.
func CheckFTP(ftp int, best20, best60 *models.FTPEffort) models.FTPCheck {
	check := models.FTPCheck{Status: models.FTPCheckUnverified, FTPWatts: ftp, WindowDays: FTPCheckWindowDays}
	est, basis := EstimateFTP(best20, best60)
	if ftp <= 0 || basis == nil {
		return check
	}
	check.EstimateWatts = int(math.Round(est))
	check.PctOfFTP = math.Round(est/float64(ftp)*1000) / 10
	check.Basis = basis

	ratio := est / float64(ftp)
	switch {
	case EstimateRaisesFTP(est, ftp):
		check.Status = models.FTPCheckRaise
	case ratio >= ftpOKFrom:
		check.Status = models.FTPCheckOK
	case ratio >= ftpLowerFrom:
		check.Status = models.FTPCheckLower
	}
	return check
}
