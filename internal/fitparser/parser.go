package fitparser

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/profile/basetype"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/typedef"
)

// ErrSkipped is returned when a FIT file is valid but not a supported cardio sport
// (e.g. weight training, yoga). Callers should silently discard these files.
var ErrSkipped = fmt.Errorf("activity skipped: unsupported sport")

// Result is the output of parsing a FIT file.
type Result struct {
	ID      string // SHA-256 of file content (first 16 hex chars)
	Workout models.Workout
	Streams []models.Stream
}

// Parse decodes a FIT file and returns a Result.
// ftpWatts, thresholdHR, and restingHR are used for TSS/IF calculations.
func Parse(r io.Reader, filename string, ftpWatts, thresholdHR, restingHR int) (*Result, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read fit: %w", err)
	}

	sum := sha256.Sum256(data)
	id := fmt.Sprintf("%x", sum[:8]) // 16-char hex ID

	lis := filedef.NewListener()
	defer lis.Close()

	dec := decoder.New(bytes.NewReader(data), decoder.WithMesgListener(lis), decoder.WithIgnoreChecksum())
	if _, err := dec.Decode(); err != nil {
		return nil, fmt.Errorf("decode fit: %w", err)
	}

	file := lis.File()
	activity, ok := file.(*filedef.Activity)
	if !ok {
		return nil, fmt.Errorf("not an activity file")
	}

	if len(activity.Sessions) == 0 {
		return nil, fmt.Errorf("no sessions in activity")
	}

	session := activity.Sessions[0]
	sport := sportString(session.Sport)
	if sport == "other" {
		return nil, ErrSkipped
	}

	workout := models.Workout{
		ID:        id,
		Filename:  filename,
		Sport:     sport,
		IsIndoor:  isIndoor(session.SubSport),
		CreatedAt: time.Now().UTC(),
	}

	// StartTime is already time.Time
	workout.RecordedAt = session.StartTime

	// TotalTimerTime: active (moving) time, excludes auto-pause periods.
	if t := session.TotalTimerTimeScaled(); !math.IsNaN(t) {
		workout.DurationSecs = int(t)
	}
	// TotalElapsedTime: wall-clock time from start to end, includes stops.
	if t := session.TotalElapsedTimeScaled(); !math.IsNaN(t) {
		workout.ElapsedSecs = int(t)
	}

	// TotalDistance: scale=100 (raw is cm), Scaled() returns meters as float64
	if d := session.TotalDistanceScaled(); !math.IsNaN(d) {
		workout.DistanceMeters = d
	}

	// TotalAscent: raw uint16, meters, no scaling
	if session.TotalAscent != basetype.Uint16Invalid {
		workout.ElevationGainMeters = float64(session.TotalAscent)
	}

	// AvgSpeed: prefer Enhanced (higher precision), fall back to standard
	if spd := session.EnhancedAvgSpeedScaled(); !math.IsNaN(spd) {
		workout.AvgSpeedMPS = spd
	} else if spd := session.AvgSpeedScaled(); !math.IsNaN(spd) {
		workout.AvgSpeedMPS = spd
	}

	if session.AvgPower != basetype.Uint16Invalid {
		p := float64(session.AvgPower)
		workout.AvgPowerWatts = &p
	}
	if session.MaxPower != basetype.Uint16Invalid {
		p := float64(session.MaxPower)
		workout.MaxPowerWatts = &p
	}
	if session.AvgHeartRate != basetype.Uint8Invalid {
		hr := int(session.AvgHeartRate)
		workout.AvgHeartRate = &hr
	}
	if session.MaxHeartRate != basetype.Uint8Invalid {
		hr := int(session.MaxHeartRate)
		workout.MaxHeartRate = &hr
	}
	if session.AvgCadence != basetype.Uint8Invalid {
		c := int(session.AvgCadence)
		workout.AvgCadenceRPM = &c
	}

	// Parse time-series records
	streams := make([]models.Stream, 0, len(activity.Records))
	var powerSeries []float64

	for _, rec := range activity.Records {
		s := models.Stream{Timestamp: rec.Timestamp}

		if rec.Power != basetype.Uint16Invalid {
			p := int(rec.Power)
			s.PowerWatts = &p
			powerSeries = append(powerSeries, float64(p))
		}
		if rec.HeartRate != basetype.Uint8Invalid {
			hr := int(rec.HeartRate)
			s.HeartRateBPM = &hr
		}
		if rec.Cadence != basetype.Uint8Invalid {
			c := int(rec.Cadence)
			s.CadenceRPM = &c
		}

		// Prefer Enhanced speed/altitude (used by Zwift and modern devices)
		if spd := rec.EnhancedSpeedScaled(); !math.IsNaN(spd) {
			s.SpeedMPS = &spd
		} else if spd := rec.SpeedScaled(); !math.IsNaN(spd) {
			s.SpeedMPS = &spd
		}

		if alt := rec.EnhancedAltitudeScaled(); !math.IsNaN(alt) {
			s.AltitudeMeters = &alt
		} else if alt := rec.AltitudeScaled(); !math.IsNaN(alt) {
			s.AltitudeMeters = &alt
		}

		if rec.PositionLat != basetype.Sint32Invalid && rec.PositionLong != basetype.Sint32Invalid {
			lat := semicirclesToDegrees(rec.PositionLat)
			lng := semicirclesToDegrees(rec.PositionLong)
			s.Lat = &lat
			s.Lng = &lng
		}

		if d := rec.DistanceScaled(); !math.IsNaN(d) {
			s.DistanceMeters = &d
		}

		streams = append(streams, s)
	}

	// Distance fallback 1: some devices omit TotalDistance in the session record
	// but accumulate it per record. Use the last non-nil stream value.
	if workout.DistanceMeters == 0 {
		for i := len(streams) - 1; i >= 0; i-- {
			if streams[i].DistanceMeters != nil {
				workout.DistanceMeters = *streams[i].DistanceMeters
				break
			}
		}
	}
	// Distance fallback 2: indoor trainers (e.g. Wahoo KICKR) often record
	// virtual speed but no distance. Compute from avg speed × duration.
	if workout.DistanceMeters == 0 && workout.AvgSpeedMPS > 0 {
		workout.DistanceMeters = workout.AvgSpeedMPS * float64(workout.DurationSecs)
	}

	// Calculate Normalized Power and power-based TSS.
	if len(powerSeries) > 0 && ftpWatts > 0 {
		np := math.Round(fitness.NormalizedPower(powerSeries)*10) / 10
		workout.NormalizedPower = &np

		ftp := float64(ftpWatts)
		ifactor := fitness.IntensityFactor(np, ftp)
		tss := fitness.PowerTSS(workout.DurationSecs, np, ftp)
		workout.IntensityFactor = &ifactor
		workout.TSS = &tss
	}

	// Heart-rate TSS for activities without a power meter (running, hiking, etc.).
	if workout.TSS == nil && workout.AvgHeartRate != nil {
		if tss, ok := fitness.HRTSS(workout.DurationSecs, *workout.AvgHeartRate, thresholdHR, restingHR); ok {
			workout.TSS = &tss
		}
	}

	return &Result{
		ID:      id,
		Workout: workout,
		Streams: streams,
	}, nil
}

// semicirclesToDegrees converts a FIT semicircle position value to degrees.
func semicirclesToDegrees(v int32) float64 {
	return float64(v) * (180.0 / math.MaxInt32)
}

// isIndoor returns true for activities performed on a trainer, treadmill, or
// virtual platform (Zwift, RGT, etc.) based on the FIT sub_sport field.
func isIndoor(s typedef.SubSport) bool {
	switch s {
	case typedef.SubSportVirtualActivity, // Zwift, RGT
		typedef.SubSportIndoorCycling, // generic indoor trainer
		typedef.SubSportSpin,          // spin class
		typedef.SubSportTreadmill:     // treadmill run
		return true
	}
	return false
}

func sportString(s typedef.Sport) string {
	switch s {
	case typedef.SportCycling:
		return "cycling"
	case typedef.SportRunning:
		return "running"
	case typedef.SportSwimming:
		return "swimming"
	case typedef.SportHiking:
		return "hiking"
	default:
		return "other"
	}
}
