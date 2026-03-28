package fitness

import (
	"math"

	"github.com/fitbase/fitbase/internal/models"
)

// NormalizedPower computes NP from a raw power series (1Hz assumed).
// NP = 4th root of mean(30s rolling average of power^4)
func NormalizedPower(watts []float64) float64 {
	if len(watts) < 30 {
		sum := 0.0
		for _, w := range watts {
			sum += w
		}
		return sum / float64(len(watts))
	}

	rolling := make([]float64, 0, len(watts))
	for i := 29; i < len(watts); i++ {
		window := watts[i-29 : i+1]
		sum := 0.0
		for _, w := range window {
			sum += w
		}
		rolling = append(rolling, sum/30.0)
	}

	sum4 := 0.0
	for _, v := range rolling {
		sum4 += math.Pow(v, 4)
	}
	return math.Pow(sum4/float64(len(rolling)), 0.25)
}

// IntensityFactor returns NP/FTP, rounded to 3 decimal places.
func IntensityFactor(np, ftp float64) float64 {
	return math.Round(np/ftp*1000) / 1000
}

// PowerTSS returns training stress score from power data, rounded to 1 decimal place.
// TSS = (duration_secs × NP × IF) / (FTP × 3600) × 100
func PowerTSS(durationSecs int, np, ftp float64) float64 {
	ifactor := np / ftp
	return math.Round((float64(durationSecs)*np*ifactor)/(ftp*3600.0)*100*10) / 10
}

// HRTSS returns heart-rate based TSS for activities without a power meter.
// Returns (tss, true) on success; (0, false) if inputs are invalid.
// Formula: hrTSS = duration_hours × hrIF² × 100
// where hrIF = (avgHR − restHR) / (lthr − restHR)
func HRTSS(durationSecs int, avgHR, thresholdHR, restingHR int) (float64, bool) {
	if thresholdHR <= 0 || restingHR <= 0 || thresholdHR <= restingHR {
		return 0, false
	}
	hrReserve := float64(thresholdHR - restingHR)
	hrIF := (float64(avgHR) - float64(restingHR)) / hrReserve
	if hrIF <= 0 {
		return 0, false
	}
	tss := math.Round((float64(durationSecs)/3600.0)*hrIF*hrIF*100*10) / 10
	return tss, true
}

// standardDurations are the effort lengths stored in the power curve table.
var standardDurations = []int{1, 5, 10, 30, 60, 120, 300, 600, 1200, 1800, 2700, 3600, 5400, 7200, 10800, 14400, 18000, 21600}

// ComputePowerCurve returns best average power (rounded to nearest watt) for
// each standard duration that fits within the ride. Returns nil if no power data.
//
// The stream is resampled to 1Hz by forward-filling before computing, which
// handles smart-recording devices and guarantees monotonicity by construction.
func ComputePowerCurve(streams []models.Stream) map[int]int {
	powers := resampleTo1Hz(streams)
	if powers == nil {
		return nil
	}
	result := make(map[int]int)
	for _, dur := range standardDurations {
		if dur > len(powers) {
			break
		}
		if best := bestAvg1Hz(powers, dur); best > 0 {
			result[dur] = int(math.Round(best))
		}
	}
	return result
}

// BestRollingAvg returns the highest average power over any window of
// windowSecs seconds. The stream is resampled to 1Hz before computing.
func BestRollingAvg(streams []models.Stream, windowSecs float64) float64 {
	powers := resampleTo1Hz(streams)
	if powers == nil {
		return 0
	}
	return bestAvg1Hz(powers, int(math.Round(windowSecs)))
}

// resampleTo1Hz forward-fills stream power values into a uniform 1Hz slice.
// Each second t gets the power of the last stream record at or before t.
// Returns nil if the stream has no power data.
func resampleTo1Hz(streams []models.Stream) []float64 {
	if len(streams) < 2 {
		return nil
	}
	t0 := streams[0].Timestamp.Unix()
	tN := streams[len(streams)-1].Timestamp.Unix()
	totalSecs := int(tN - t0)
	if totalSecs < 1 {
		return nil
	}

	out := make([]float64, totalSecs+1)
	hasPower := false
	si := 0
	for sec := 0; sec <= totalSecs; sec++ {
		// Advance to the last stream record at or before this second.
		for si+1 < len(streams) && streams[si+1].Timestamp.Unix()-t0 <= int64(sec) {
			si++
		}
		if streams[si].PowerWatts != nil {
			out[sec] = float64(*streams[si].PowerWatts)
			hasPower = true
		}
	}
	if !hasPower {
		return nil
	}
	return out
}

// bestAvg1Hz finds the best n-sample rolling average in a uniform 1Hz power
// slice using an O(len) sliding window. Monotonicity is guaranteed: because
// every (n+1)-sample window contains an n-sample sub-window, best(n) >= best(n+1).
func bestAvg1Hz(powers []float64, n int) float64 {
	if n <= 0 || len(powers) < n {
		return 0
	}
	var sum float64
	for i := 0; i < n; i++ {
		sum += powers[i]
	}
	best := sum / float64(n)
	for i := n; i < len(powers); i++ {
		sum += powers[i] - powers[i-n]
		if avg := sum / float64(n); avg > best {
			best = avg
		}
	}
	return best
}

// StreamMetrics is derived per-stream metrics for a workout.
type StreamMetrics struct {
	WorkKJ     float64
	Calories   float64
	EFTP       *float64
	MaxCadence *int
	MaxPower1s *int
}

// ComputeStreamMetrics derives energy, peak power, peak cadence, and estimated
// FTP from a workout's time-series stream data.
func ComputeStreamMetrics(streams []models.Stream) StreamMetrics {
	var m StreamMetrics
	if len(streams) == 0 {
		return m
	}

	var maxCad, maxPow int
	var hasCad, hasPow bool
	var workJ float64

	for i, s := range streams {
		if s.PowerWatts != nil {
			dt := 1.0
			if i > 0 {
				dt = streams[i].Timestamp.Sub(streams[i-1].Timestamp).Seconds()
				if dt <= 0 || dt > 30 {
					dt = 1
				}
			}
			workJ += float64(*s.PowerWatts) * dt
			hasPow = true
			if *s.PowerWatts > maxPow {
				maxPow = *s.PowerWatts
			}
		}
		if s.CadenceRPM != nil {
			hasCad = true
			if *s.CadenceRPM > maxCad {
				maxCad = *s.CadenceRPM
			}
		}
	}

	m.WorkKJ = workJ / 1000.0
	m.Calories = m.WorkKJ // 1 kJ ≈ 1 kcal at ~25% mechanical efficiency
	if hasPow {
		m.MaxPower1s = &maxPow
	}
	if hasCad {
		m.MaxCadence = &maxCad
	}

	// eFTP: best 20-minute rolling average power × 0.95
	if hasPow {
		best := BestRollingAvg(streams, 1200)
		if best > 0 {
			eftp := best * 0.95
			m.EFTP = &eftp
		}
	}
	return m
}
