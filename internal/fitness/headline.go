package fitness

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// FormatClock renders elapsed seconds as h:mm:ss.
func FormatClock(secs int) string {
	return fmt.Sprintf("%d:%02d:%02d", secs/3600, secs/60%60, secs%60)
}

// FormatDur renders a duration the way riders write intervals: "4:00",
// "20:00", or "1:05:00" past an hour.
func FormatDur(secs int) string {
	if secs >= 3600 {
		return FormatClock(secs)
	}
	return fmt.Sprintf("%d:%02d", secs/60, secs%60)
}

// maxHeadlineEfforts bounds how many loose efforts ShapeHeadline names.
const maxHeadlineEfforts = 4

// ShapeHeadline condenses a ride's shape into one line, for the insights
// payload and the ride page header, where every recent ride appears and the full per-effort detail
// would swamp the prompt: "5×4:00 @ 117% FTP, faded 6.3% last rep vs first,
// max HR +8". Words, not a signed number: in eval a model read "+2.1% vs
// first" as a fade. Reps of uneven length are listed ("2 reps 12:00/9:52"),
// so a cut-short last rep isn't averaged away.
// Returns "" when there is nothing to say (no power or no FTP).
func ShapeHeadline(s RideShape, ftp int) string {
	if !s.HasPower || ftp <= 0 {
		return ""
	}
	if len(s.Efforts) == 0 {
		return fmt.Sprintf("steady — no efforts at or above %d%% FTP", EffortStartPctFTP)
	}
	pct := func(w float64) int { return int(math.Round(w / float64(ftp) * 100)) }

	inSet := make([]bool, len(s.Efforts))
	var parts []string
	for _, set := range s.Sets {
		for i := set.First; i < set.First+set.Count; i++ {
			inSet[i] = true
		}
		parts = append(parts, SetHeadline(s.Efforts[set.First:set.First+set.Count], ftp))
	}

	// Efforts outside any set: name the ones with the most work, in ride order.
	var loose []int
	for i := range s.Efforts {
		if !inSet[i] {
			loose = append(loose, i)
		}
	}
	if len(loose) > 0 {
		shown := append([]int(nil), loose...)
		work := func(i int) float64 { return s.Efforts[i].AvgWatts * float64(s.Efforts[i].DurationSecs) }
		sort.SliceStable(shown, func(a, b int) bool { return work(shown[a]) > work(shown[b]) })
		if len(shown) > maxHeadlineEfforts {
			shown = shown[:maxHeadlineEfforts]
		}
		sort.Ints(shown)
		names := make([]string, len(shown))
		for k, i := range shown {
			names[k] = fmt.Sprintf("%s @ %d%%", FormatDur(s.Efforts[i].DurationSecs), pct(s.Efforts[i].AvgWatts))
		}
		label := "efforts"
		if len(loose) == 1 {
			label = "effort"
		}
		if len(parts) > 0 {
			label = "other " + label
		}
		part := fmt.Sprintf("%d %s: %s", len(loose), label, strings.Join(names, ", "))
		if more := len(loose) - len(shown); more > 0 {
			part += fmt.Sprintf(" +%d more", more)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

// SetHeadline describes one set of reps the way the headline does: "5×4:00 @
// 117% FTP, faded 6.3% last rep vs first, max HR +8", or "2 reps 12:00/9:52
// @ 88% FTP, ..." when the reps differ in length. reps must not be empty.
func SetHeadline(reps []Effort, ftp int) string {
	pct := func(w float64) int { return int(math.Round(w / float64(ftp) * 100)) }
	var dur, watts float64
	minD, maxD := reps[0].DurationSecs, reps[0].DurationSecs
	durs := make([]string, len(reps))
	for i := range reps {
		dur += float64(reps[i].DurationSecs)
		watts += reps[i].AvgWatts
		minD, maxD = min(minD, reps[i].DurationSecs), max(maxD, reps[i].DurationSecs)
		durs[i] = FormatDur(reps[i].DurationSecs)
	}
	n := float64(len(reps))
	var part string
	if maxD-minD <= max(10, maxD/10) {
		part = fmt.Sprintf("%d×%s @ %d%% FTP", len(reps), FormatDur(int(math.Round(dur/n))), pct(watts/n))
	} else {
		part = fmt.Sprintf("%d reps %s @ %d%% FTP", len(reps), strings.Join(durs, "/"), pct(watts/n))
	}
	first, last := reps[0], reps[len(reps)-1]
	if first.AvgWatts > 0 {
		switch chg := (last.AvgWatts - first.AvgWatts) / first.AvgWatts * 100; {
		case math.Abs(chg) < 1:
			part += ", held power across reps"
		case chg < 0:
			part += fmt.Sprintf(", faded %.1f%% last rep vs first", -chg)
		default:
			part += fmt.Sprintf(", built %.1f%% last rep vs first", chg)
		}
	}
	if first.MaxHR > 0 && last.MaxHR > 0 {
		part += fmt.Sprintf(", max HR %+d", last.MaxHR-first.MaxHR)
	}
	return part
}
