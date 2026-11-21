package api

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
)

// Reusable data-shaping cores for the agent-oriented API. Each function below
// is the single source of truth for one curated shape; the HTTP handlers in
// handlers.go and the AI coach's tool executor (coach_tools.go) both call
// these so the REST surface and the coach can never drift apart.

// powerCurveReport returns all-time best power at the six standard durations
// with W/kg and %FTP. Backs GET /api/athlete/power-curve and the coach's
// power-curve tool.
func powerCurveReport(database *db.DB) (models.PowerCurveReport, error) {
	curve, err := database.GetAllTimePowerCurve()
	if err != nil {
		return models.PowerCurveReport{}, err
	}
	athlete, err := database.GetAthlete()
	if err != nil {
		return models.PowerCurveReport{}, err
	}

	displayDurations := []struct {
		secs  int
		label string
	}{
		{5, "5s"}, {30, "30s"}, {60, "1min"}, {300, "5min"}, {1200, "20min"}, {3600, "60min"},
	}

	entries := make([]models.PowerCurveEntry, 0, len(displayDurations))
	for _, d := range displayDurations {
		best, ok := curve[d.secs]
		if !ok {
			continue
		}
		entry := models.PowerCurveEntry{
			DurationSecs:  d.secs,
			DurationLabel: d.label,
			Watts:         best.Watts,
			WorkoutID:     best.WorkoutID,
		}
		entry.WattsPerKG = fitness.WPerKG(best.Watts, athlete.WeightKG)
		entry.PctFTP = fitness.PctFTP(best.Watts, athlete.FTPWatts)
		entries = append(entries, entry)
	}
	return models.PowerCurveReport{
		Entries:  entries,
		FTPWatts: athlete.FTPWatts,
		WeightKG: athlete.WeightKG,
	}, nil
}

// workoutAnalysis computes per-workout zone distribution, VI/EF, and 90-day
// context. Returns (nil, nil) when no workout has the given id. Backs
// GET /api/workouts/{id}/analysis and the coach's workout-detail tool.
func workoutAnalysis(database *db.DB, id string) (*models.WorkoutAnalysis, error) {
	workout, err := database.GetWorkout(id)
	if err != nil {
		return nil, err
	}
	if workout == nil {
		return nil, nil
	}

	athlete, err := database.GetAthlete()
	if err != nil {
		return nil, err
	}

	powerZoneDefs := fitness.PowerZones(athlete.FTPWatts)
	hrZoneDefs := fitness.ResolveHRZones(athlete)

	powerSecs, hrSecs, ssSecs, err := database.GetZoneTimes(id)
	if err != nil {
		return nil, err
	}

	analysis := &models.WorkoutAnalysis{
		WorkoutID:  id,
		PowerZones: []models.ZoneBreakdown{},
		HRZones:    []models.ZoneBreakdown{},
	}

	if powerSecs != nil {
		total := 0
		for _, s := range powerSecs {
			total += s
		}
		for i, z := range powerZoneDefs[:7] {
			pct := 0.0
			if total > 0 {
				pct = math.Round(float64(powerSecs[i])/float64(total)*1000) / 10
			}
			analysis.PowerZones = append(analysis.PowerZones, models.ZoneBreakdown{
				Label:     z.Label,
				Name:      z.Name,
				Seconds:   powerSecs[i],
				PctTime:   pct,
				WattsLow:  z.WattsLow,
				WattsHigh: z.WattsHigh,
			})
		}
		// Sweet Spot is reported as a parallel band — its pct uses the same
		// power-time denominator as the 7-zone breakdown so the model can read
		// "X% of the ride was in SS" without re-doing the math.
		if ssSecs != nil {
			var ssDef models.PowerZone
			for _, z := range powerZoneDefs {
				if z.Label == "SS" {
					ssDef = z
					break
				}
			}
			pct := 0.0
			if total > 0 {
				pct = math.Round(float64(*ssSecs)/float64(total)*1000) / 10
			}
			analysis.SweetSpot = &models.ZoneBreakdown{
				Label:     ssDef.Label,
				Name:      ssDef.Name,
				Seconds:   *ssSecs,
				PctTime:   pct,
				WattsLow:  ssDef.WattsLow,
				WattsHigh: ssDef.WattsHigh,
			}
		}
	}

	if hrSecs != nil {
		total := 0
		for _, s := range hrSecs {
			total += s
		}
		for i, z := range hrZoneDefs {
			if i >= len(hrSecs) {
				break
			}
			pct := 0.0
			if total > 0 {
				pct = math.Round(float64(hrSecs[i])/float64(total)*1000) / 10
			}
			analysis.HRZones = append(analysis.HRZones, models.ZoneBreakdown{
				Label:   z.Label,
				Name:    z.Name,
				Seconds: hrSecs[i],
				PctTime: pct,
				BPMLow:  z.BPMLow,
				BPMHigh: z.BPMHigh,
			})
		}
	}

	if vi, ok := fitness.VariabilityIndex(workout.NormalizedPower, workout.AvgPowerWatts); ok {
		r := math.Round(vi*1000) / 1000
		analysis.VariabilityIndex = &r
	}
	if ef, ok := fitness.EfficiencyFactor(workout.NormalizedPower, workout.AvgHeartRate); ok {
		r := math.Round(ef*1000) / 1000
		analysis.EfficiencyFactor = &r
	}

	if avgs, err := database.Get90DayAverages(workout.Sport); err == nil && avgs != nil {
		analysis.AvgNP90Day = avgs.AvgNP
		analysis.AvgHR90Day = avgs.AvgHR
		analysis.AvgTSS90Day = avgs.AvgTSS
		analysis.AvgIF90Day = avgs.AvgIF
		analysis.AvgDuration90Day = avgs.AvgDurationSecs
	}

	return analysis, nil
}

// maxShapeEfforts caps the effort list. A hilly outdoor ride can throw up
// dozens of 20s+ surges; past this many, the efforts with the least work are
// dropped from the list (sets still summarize every rep they contain).
const maxShapeEfforts = 40

// workoutShape detects the efforts and sets inside one ride, builds a coarse
// time-bucketed profile, and attaches the workout planned for that day so
// execution can be compared against the prescription. Returns (nil, nil) when
// no workout has the given id. Backs GET /api/workouts/{id}/shape and the
// coach's get_workout_intervals tool.
func workoutShape(database *db.DB, id string) (*models.WorkoutShape, error) {
	w, err := database.GetWorkout(id)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, nil
	}
	// The FTP of the ride's day, not today's: "118% FTP" should mean what it
	// meant when the rider did the work — the same FTP its TSS/IF used.
	ftp := database.GetFTPForRide(w.RecordedAt, w.IsIndoor)
	shape, err := database.RideShape(id, ftp)
	if err != nil {
		return nil, err
	}

	day := w.TrainingDay
	if day == "" {
		day = w.RecordedAt.Format("2006-01-02")
	}
	out := &models.WorkoutShape{
		WorkoutID:   id,
		TrainingDay: day,
		Sport:       w.Sport,
		ElapsedSecs: shape.ElapsedSecs,
		Efforts:     []models.EffortSummary{},
		Sets:        []models.EffortSetSummary{},
		Profile:     models.ShapeProfile{BucketSecs: shape.BucketSecs, Buckets: []models.ShapeBucket{}},
	}
	if ftp > 0 {
		out.FTPWatts = ftp
	}
	pctOf := func(watts float64) int {
		return int(math.Round(watts / float64(ftp) * 100))
	}
	intPtr := func(v float64) *int {
		r := int(math.Round(v))
		return &r
	}

	if shape.HasPower && ftp > 0 {
		out.Detection = &models.EffortDetection{
			StartWatts: int(math.Round(float64(ftp) * fitness.EffortStartPctFTP / 100)),
			FloorWatts: int(math.Round(float64(ftp) * fitness.EffortFloorPctFTP / 100)),
			MinSecs:    fitness.EffortMinSecs,
			Rule: fmt.Sprintf("An effort starts when power (9s smoothed) reaches %d%% FTP and lasts while it holds above the higher of %d%% FTP and %d%% of the effort's own average, bridging dips of up to %ds. Efforts under %ds are ignored, and work below %d%% FTP (tempo, endurance) is never an effort — read it from the profile.",
				fitness.EffortStartPctFTP, fitness.EffortFloorPctFTP, int(fitness.EffortRelativeFloor*100),
				fitness.EffortMaxDipSecs, fitness.EffortMinSecs, fitness.EffortStartPctFTP),
		}
	}

	shortWithHR := false
	all := make([]models.EffortSummary, len(shape.Efforts))
	for i, e := range shape.Efforts {
		s := models.EffortSummary{
			N:            i + 1,
			Start:        fitness.FormatClock(e.StartSecs),
			DurationSecs: e.DurationSecs,
			AvgWatts:     int(math.Round(e.AvgWatts)),
			PctFTP:       pctOf(e.AvgWatts),
			MaxWatts:     e.MaxWatts,
		}
		if e.NPWatts > 0 {
			s.NPWatts = intPtr(e.NPWatts)
		}
		if e.AvgHR > 0 {
			s.AvgHR = intPtr(e.AvgHR)
			s.MaxHR = intPtr(float64(e.MaxHR))
			if e.DurationSecs < 120 {
				shortWithHR = true
			}
		}
		if e.AvgCadence > 0 {
			s.AvgCadence = intPtr(e.AvgCadence)
		}
		if i < len(shape.Efforts)-1 {
			rec := e.RecoverySecs
			s.RecoveryAfterSecs = &rec
			if e.RecoveryAvgWatts > 0 {
				s.RecoveryAvgWatts = intPtr(e.RecoveryAvgWatts)
			}
		}
		all[i] = s
	}
	if len(all) > maxShapeEfforts {
		// Keep the efforts with the most work, then restore ride order. N keeps
		// the original numbering so sets and the rider's chart still line up.
		idx := make([]int, len(all))
		for i := range idx {
			idx[i] = i
		}
		work := func(i int) float64 { return shape.Efforts[i].AvgWatts * float64(shape.Efforts[i].DurationSecs) }
		sort.SliceStable(idx, func(a, b int) bool { return work(idx[a]) > work(idx[b]) })
		idx = idx[:maxShapeEfforts]
		sort.Ints(idx)
		kept := make([]models.EffortSummary, len(idx))
		for k, i := range idx {
			kept[k] = all[i]
		}
		out.OmittedEfforts = len(all) - len(kept)
		all = kept
	}
	out.Efforts = all

	for _, set := range shape.Sets {
		reps := shape.Efforts[set.First : set.First+set.Count]
		ss := models.EffortSetSummary{
			FirstEffort:     set.First + 1,
			Reps:            set.Count,
			RepDurationSecs: make([]int, len(reps)),
			RepAvgWatts:     make([]int, len(reps)),
		}
		var durSum, wattSum, recSecs, recWatts float64
		var recN, recWN int
		allHR := true
		for i, r := range reps {
			durSum += float64(r.DurationSecs)
			wattSum += r.AvgWatts
			ss.RepDurationSecs[i] = r.DurationSecs
			ss.RepAvgWatts[i] = int(math.Round(r.AvgWatts))
			if r.MaxHR == 0 {
				allHR = false
			}
			if i < len(reps)-1 { // recoveries inside the set
				recSecs += float64(r.RecoverySecs)
				recN++
				if r.RecoveryAvgWatts > 0 {
					recWatts += r.RecoveryAvgWatts
					recWN++
				}
			}
		}
		ss.AvgDurationSecs = int(math.Round(durSum / float64(len(reps))))
		avg := wattSum / float64(len(reps))
		ss.AvgWatts = int(math.Round(avg))
		ss.AvgPctFTP = pctOf(avg)
		first, last := reps[0], reps[len(reps)-1]
		if first.AvgWatts > 0 {
			ss.LastVsFirstRepPct = math.Round((last.AvgWatts-first.AvgWatts)/first.AvgWatts*1000) / 10
		}
		if allHR {
			ss.RepMaxHR = make([]int, len(reps))
			for i, r := range reps {
				ss.RepMaxHR[i] = r.MaxHR
			}
			creep := last.MaxHR - first.MaxHR
			ss.HRCreepBPM = &creep
		}
		if recN > 0 {
			ss.AvgRecoverySecs = intPtr(recSecs / float64(recN))
		}
		if recWN > 0 {
			ss.AvgRecoveryWatts = intPtr(recWatts / float64(recWN))
		}
		out.Sets = append(out.Sets, ss)
	}

	for _, b := range shape.Buckets {
		pb := models.ShapeBucket{Start: fitness.FormatClock(b.StartSecs)}
		if shape.HasPower {
			pb.AvgWatts = intPtr(b.AvgWatts)
			if ftp > 0 {
				p := pctOf(b.AvgWatts)
				pb.PctFTP = &p
			}
		}
		if b.AvgHR > 0 {
			pb.AvgHR = intPtr(b.AvgHR)
		}
		if b.AvgCadence > 0 {
			pb.AvgCadence = intPtr(b.AvgCadence)
		}
		if !shape.HasPower && shape.HasSpeed {
			kph := math.Round(b.AvgSpeedMPS*36) / 10
			pb.AvgSpeedKPH = &kph
		}
		out.Profile.Buckets = append(out.Profile.Buckets, pb)
	}

	if dayT, perr := time.Parse("2006-01-02", day); perr == nil {
		planned, err := database.ListPlannedWorkoutsBetween(dayT, dayT)
		if err != nil {
			return nil, err
		}
		for _, p := range planned {
			if strings.EqualFold(p.Sport, w.Sport) {
				out.Planned = append(out.Planned, p)
			}
		}
	}

	switch {
	case shape.ElapsedSecs == 0:
		out.Notes = append(out.Notes, "This workout has no recorded stream data, so its shape can't be analyzed.")
	case !shape.HasPower:
		out.Notes = append(out.Notes, "No power data on this ride — efforts can't be detected. The profile shows heart rate and speed only.")
	case ftp <= 0:
		out.Notes = append(out.Notes, "FTP is not set, so efforts can't be detected against a threshold. The profile still shows power and heart rate.")
	case len(shape.Efforts) == 0:
		out.Notes = append(out.Notes, fmt.Sprintf("No sustained efforts at or above %d%% FTP — a steady or endurance-type ride. Judge pacing and drift from the profile.", fitness.EffortStartPctFTP))
	}
	if out.OmittedEfforts > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d lower-work efforts were left out of the list to keep it readable; n keeps the ride's original numbering.", out.OmittedEfforts))
	}
	if shortWithHR {
		out.Notes = append(out.Notes, "Heart rate lags power by a minute or more: on efforts under ~2 min, max_hr_bpm understates the real response. Compare reps against each other rather than against HR zones.")
	}
	if len(out.Planned) > 0 {
		out.Notes = append(out.Notes, "A workout was planned for this day. Compare the efforts and sets with its intervals — target_pct_ftp is relative to ftp_watts, and start times run from the start of the recording, so the planned warmup is the time before effort 1.")
	}
	return out, nil
}
