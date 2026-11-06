package fitness

import (
	"math"

	"github.com/fitbase/fitbase/internal/models"
)

// Ride shape: what the rider actually did inside a workout, as opposed to the
// whole-ride averages (NP, IF, zone totals) that make a 5×4min VO2 session and
// a steady tempo ride look alike.
//
// Efforts are found on the power stream with a hysteresis detector:
//
//   - An effort STARTS when 9s-centered-smoothed power reaches EffortStartPctFTP.
//   - It CONTINUES while smoothed power stays at or above its floor — the
//     higher of EffortFloorPctFTP and EffortRelativeFloor × the effort's own
//     running average — tolerating dips of up to EffortMaxDipSecs (a coasted
//     corner, a gear change).
//   - Edges are then trimmed on the raw 1Hz series so the effort's average
//     isn't diluted by the smoothing ramp.
//
// The relative floor is what separates an interval from what follows it: a
// 105% effort ends when power drops toward a 75% cooldown, while an outdoor
// sweet-spot climb with noisy power is held together by the absolute floor.
// Thresholds are FTP-relative, so efforts below ~85% FTP (tempo) are not
// detected — they show up in the time-bucketed profile instead.
// ShapeVersion identifies the detector's behavior. Cached shapes
// (db.workout_shape) computed with a different version are recomputed, so bump
// it whenever a change here would alter any RideShape output.
const ShapeVersion = 1

const (
	EffortStartPctFTP   = 85
	EffortFloorPctFTP   = 70
	EffortRelativeFloor = 0.80
	EffortMaxDipSecs    = 10
	EffortMinSecs       = 20

	effortSmoothHalf = 4 // 9s centered window
	// gapFillMaxSecs is the largest recording gap forward-filled (smart
	// recording writes every 1–5s). Longer gaps are auto-pause or a stop and
	// are treated as no data, so a café stop never reads as a held effort.
	gapFillMaxSecs = 5
	// maxShapeSecs bounds the 1Hz arrays against corrupt timestamps.
	maxShapeSecs = 72 * 3600
	// profileMaxBuckets caps the coarse profile so a long ride stays compact.
	profileMaxBuckets = 30
	// npMinSecs: NP uses a 30s rolling window, so it only means something on
	// efforts comfortably longer than that.
	npMinSecs = 60
)

// Effort is one sustained hard stretch detected in the power stream. Zero
// values mean "no data" (no HR strap, no cadence sensor, last effort has no
// recovery after it).
type Effort struct {
	StartSecs    int // elapsed seconds from the first record
	DurationSecs int
	AvgWatts     float64
	NPWatts      float64 // 0 when shorter than npMinSecs
	MaxWatts     int
	AvgHR        float64
	MaxHR        int
	AvgCadence   float64 // pedaling samples only (zeros excluded)

	// Recovery from the end of this effort to the start of the next.
	RecoverySecs     int
	RecoveryAvgWatts float64
}

// EffortSet is a run of consecutive efforts of similar duration with
// reasonably short recoveries between them — "5×4min", "2×20", "10×30/30".
// First indexes RideShape.Efforts; Count is the number of reps (≥ 2).
type EffortSet struct {
	First int
	Count int
}

// ShapeBucket is one fixed-length slice of the ride. Averages cover only the
// seconds with recorded data (ValidSecs); a bucket entirely inside a stop is
// omitted from RideShape.Buckets.
type ShapeBucket struct {
	StartSecs   int
	ValidSecs   int
	AvgWatts    float64
	AvgHR       float64
	AvgCadence  float64
	AvgSpeedMPS float64
}

// RideShape is the interval-level analysis of one ride.
type RideShape struct {
	ElapsedSecs int
	HasPower    bool
	HasHR       bool
	HasCadence  bool
	HasSpeed    bool

	Efforts []Effort
	Sets    []EffortSet

	BucketSecs int
	Buckets    []ShapeBucket
}

// rideSeries is a ride resampled onto a 1Hz grid. valid[i] is false inside
// recording gaps longer than gapFillMaxSecs; every channel is 0 there.
type rideSeries struct {
	power, hr, cad, speed                 []float64
	valid                                 []bool
	hasPower, hasHR, hasCadence, hasSpeed bool
}

func resampleWithGaps(streams []models.Stream) *rideSeries {
	if len(streams) < 2 {
		return nil
	}
	t0 := streams[0].Timestamp.Unix()
	span := streams[len(streams)-1].Timestamp.Unix() - t0
	if span < 1 || span > maxShapeSecs {
		return nil
	}
	n := int(span) + 1
	rs := &rideSeries{
		power: make([]float64, n),
		hr:    make([]float64, n),
		cad:   make([]float64, n),
		speed: make([]float64, n),
		valid: make([]bool, n),
	}
	si := 0
	for sec := 0; sec < n; sec++ {
		for si+1 < len(streams) && streams[si+1].Timestamp.Unix()-t0 <= int64(sec) {
			si++
		}
		rec := streams[si]
		recSec := rec.Timestamp.Unix() - t0
		// Forward-fill only across a short gap to the next record. At the start
		// of a stop the device simply stops writing, so carrying the last value
		// forward would invent seconds of riding that never happened.
		if int64(sec) != recSec {
			if si+1 >= len(streams) || streams[si+1].Timestamp.Unix()-t0-recSec > gapFillMaxSecs {
				continue // stopped / auto-paused
			}
		}
		rs.valid[sec] = true
		if rec.PowerWatts != nil {
			rs.power[sec] = float64(*rec.PowerWatts)
			rs.hasPower = true
		}
		if rec.HeartRateBPM != nil && *rec.HeartRateBPM > 0 {
			rs.hr[sec] = float64(*rec.HeartRateBPM)
			rs.hasHR = true
		}
		if rec.CadenceRPM != nil {
			rs.cad[sec] = float64(*rec.CadenceRPM)
			rs.hasCadence = true
		}
		if rec.SpeedMPS != nil {
			rs.speed[sec] = *rec.SpeedMPS
			rs.hasSpeed = true
		}
	}
	return rs
}

// AnalyzeShape detects efforts and sets in a ride's power stream and builds a
// coarse time-bucketed profile. ftp is the FTP in effect on the ride's date;
// with ftp <= 0 or no power data, Efforts and Sets are empty but the profile
// is still built from whatever channels exist.
func AnalyzeShape(streams []models.Stream, ftp int) RideShape {
	rs := resampleWithGaps(streams)
	if rs == nil {
		return RideShape{}
	}
	shape := RideShape{
		ElapsedSecs: len(rs.valid) - 1,
		HasPower:    rs.hasPower,
		HasHR:       rs.hasHR,
		HasCadence:  rs.hasCadence,
		HasSpeed:    rs.hasSpeed,
	}
	if rs.hasPower && ftp > 0 {
		shape.Efforts = detectEfforts(rs, float64(ftp))
		shape.Sets = groupSets(shape.Efforts)
	}
	shape.BucketSecs, shape.Buckets = buildProfile(rs)
	return shape
}

// centeredMean returns the mean of xs over a (2·half+1)-sample window centered
// on each index, shrinking at the edges.
func centeredMean(xs []float64, half int) []float64 {
	prefix := make([]float64, len(xs)+1)
	for i, v := range xs {
		prefix[i+1] = prefix[i] + v
	}
	out := make([]float64, len(xs))
	for i := range xs {
		lo := max(0, i-half)
		hi := min(len(xs)-1, i+half)
		out[i] = (prefix[hi+1] - prefix[lo]) / float64(hi-lo+1)
	}
	return out
}

func detectEfforts(rs *rideSeries, ftp float64) []Effort {
	p := rs.power
	n := len(p)
	smooth := centeredMean(p, effortSmoothHalf)
	startW := ftp * EffortStartPctFTP / 100
	floorW := ftp * EffortFloorPctFTP / 100

	type span struct{ start, end int } // inclusive
	var spans []span
	prevEnd := -1

	for i := 0; i < n; {
		if !rs.valid[i] || smooth[i] < startW {
			i++
			continue
		}
		start := i
		// Smoothing centers the crossing on the true edge, but a ragged
		// outdoor start can lead it by a few seconds.
		for k := 0; k < effortSmoothHalf && start-1 > prevEnd && rs.valid[start-1] && p[start-1] >= startW; k++ {
			start--
		}

		var sum float64
		lastAbove := start
		j := start
		for ; j < n; j++ {
			sum += p[j]
			floor := math.Max(floorW, EffortRelativeFloor*sum/float64(j-start+1))
			if rs.valid[j] && smooth[j] >= floor {
				lastAbove = j
				continue
			}
			if j-lastAbove > EffortMaxDipSecs {
				break
			}
		}

		// Trim the smoothing ramp off both ends on raw power. The end can also
		// sit a few seconds early when the window reaches into a stop (no data
		// reads as 0W), so let it run forward on raw power by up to half a window.
		end := lastAbove
		floor := math.Max(floorW, EffortRelativeFloor*meanRange(p, start, end))
		for end > start && (p[end] < floor || !rs.valid[end]) {
			end--
		}
		for k := 0; k < effortSmoothHalf && end+1 < n && rs.valid[end+1] && p[end+1] >= floor; k++ {
			end++
		}
		for start < end && p[start] < startW {
			start++
		}

		if end-start+1 >= EffortMinSecs {
			spans = append(spans, span{start, end})
			prevEnd = end
		}
		i = max(j, end+1)
	}

	efforts := make([]Effort, len(spans))
	for k, s := range spans {
		efforts[k] = summarizeEffort(rs, s.start, s.end)
		if k > 0 {
			prev := &efforts[k-1]
			gapLo := spans[k-1].end + 1
			gapHi := s.start - 1
			prev.RecoverySecs = s.start - gapLo
			if gapHi >= gapLo {
				prev.RecoveryAvgWatts = meanValid(rs.power, rs.valid, gapLo, gapHi)
			}
		}
	}
	return efforts
}

func summarizeEffort(rs *rideSeries, start, end int) Effort {
	e := Effort{StartSecs: start, DurationSecs: end - start + 1}
	e.AvgWatts = meanValid(rs.power, rs.valid, start, end)
	if e.DurationSecs >= npMinSecs {
		e.NPWatts = NormalizedPower(rs.power[start : end+1])
	}
	var hrSum, cadSum float64
	var hrN, cadN int
	for i := start; i <= end; i++ {
		if !rs.valid[i] {
			continue
		}
		if w := int(rs.power[i]); w > e.MaxWatts {
			e.MaxWatts = w
		}
		if h := rs.hr[i]; h > 0 {
			hrSum += h
			hrN++
			if int(h) > e.MaxHR {
				e.MaxHR = int(h)
			}
		}
		if c := rs.cad[i]; c > 0 {
			cadSum += c
			cadN++
		}
	}
	if hrN > 0 {
		e.AvgHR = hrSum / float64(hrN)
	}
	if cadN > 0 {
		e.AvgCadence = cadSum / float64(cadN)
	}
	return e
}

// groupSets folds consecutive efforts into sets: each rep within
// max(10s, 20%) of the first rep's duration, and each recovery no longer than
// max(2× rep duration, 3 min). A longer gap or a different-length effort
// starts a new set; singletons are not sets.
func groupSets(efforts []Effort) []EffortSet {
	var sets []EffortSet
	for i := 0; i < len(efforts); {
		ref := efforts[i].DurationSecs
		tol := max(10, ref/5)
		maxRec := max(2*ref, 180)
		j := i + 1
		for j < len(efforts) {
			d := efforts[j].DurationSecs
			if d < ref-tol || d > ref+tol || efforts[j-1].RecoverySecs > maxRec {
				break
			}
			j++
		}
		if j-i >= 2 {
			sets = append(sets, EffortSet{First: i, Count: j - i})
		}
		i = j
	}
	return sets
}

// profileBucketSizes are the candidate bucket lengths; the smallest that
// keeps the profile within profileMaxBuckets wins.
var profileBucketSizes = []int{60, 120, 300, 600, 900, 1200, 1800, 3600}

func buildProfile(rs *rideSeries) (int, []ShapeBucket) {
	n := len(rs.valid)
	size := profileBucketSizes[len(profileBucketSizes)-1]
	for _, s := range profileBucketSizes {
		if (n+s-1)/s <= profileMaxBuckets {
			size = s
			break
		}
	}
	var buckets []ShapeBucket
	for lo := 0; lo < n; lo += size {
		hi := min(n, lo+size) - 1
		b := ShapeBucket{StartSecs: lo}
		var pSum, hSum, cSum, sSum float64
		var hN, cN int
		for i := lo; i <= hi; i++ {
			if !rs.valid[i] {
				continue
			}
			b.ValidSecs++
			pSum += rs.power[i]
			sSum += rs.speed[i]
			if rs.hr[i] > 0 {
				hSum += rs.hr[i]
				hN++
			}
			if rs.cad[i] > 0 {
				cSum += rs.cad[i]
				cN++
			}
		}
		if b.ValidSecs == 0 {
			continue
		}
		b.AvgWatts = pSum / float64(b.ValidSecs)
		b.AvgSpeedMPS = sSum / float64(b.ValidSecs)
		if hN > 0 {
			b.AvgHR = hSum / float64(hN)
		}
		if cN > 0 {
			b.AvgCadence = cSum / float64(cN)
		}
		buckets = append(buckets, b)
	}
	return size, buckets
}

func meanRange(xs []float64, lo, hi int) float64 {
	if hi < lo {
		return 0
	}
	var sum float64
	for i := lo; i <= hi; i++ {
		sum += xs[i]
	}
	return sum / float64(hi-lo+1)
}

func meanValid(xs []float64, valid []bool, lo, hi int) float64 {
	var sum float64
	var n int
	for i := lo; i <= hi; i++ {
		if valid[i] {
			sum += xs[i]
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}
