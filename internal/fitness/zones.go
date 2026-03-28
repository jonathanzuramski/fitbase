package fitness

import (
	"encoding/json"
	"fmt"

	"github.com/fitbase/fitbase/internal/models"
)

// PowerZones returns the 7 standard power zones plus Sweet Spot for the given FTP.
func PowerZones(ftp int) []models.PowerZone {
	zones := []models.PowerZone{
		{Label: "Z1", Name: "Active Recovery", PctLow: 0, PctHigh: 55},
		{Label: "Z2", Name: "Endurance", PctLow: 56, PctHigh: 75},
		{Label: "Z3", Name: "Tempo", PctLow: 76, PctHigh: 90},
		{Label: "Z4", Name: "Threshold", PctLow: 91, PctHigh: 105},
		{Label: "Z5", Name: "VO2 Max", PctLow: 106, PctHigh: 120},
		{Label: "Z6", Name: "Anaerobic", PctLow: 121, PctHigh: 150},
		{Label: "Z7", Name: "Neuromuscular", PctLow: 151, PctHigh: 0}, // 0 = open-ended
		{Label: "SS", Name: "Sweet Spot", PctLow: 84, PctHigh: 97},
	}
	prevHigh := 0
	for i := range zones {
		lo := prevHigh + 1
		if i == 0 {
			lo = 1
		}
		// SS is a standalone reference zone — compute directly from percentage
		if zones[i].Label == "SS" {
			lo = int(float64(zones[i].PctLow) * float64(ftp) / 100.0)
		}
		hi := 0
		if zones[i].PctHigh > 0 {
			hi = int(float64(zones[i].PctHigh) * float64(ftp) / 100.0)
		}
		zones[i].WattsLow = lo
		zones[i].WattsHigh = hi
		if zones[i].Label != "SS" {
			prevHigh = hi
		}
	}
	return zones
}

// HRZones returns the 5-zone Coggan heart-rate model for the given threshold HR.
func HRZones(thresholdHR int) []models.HRZone {
	zones := []models.HRZone{
		{Label: "Z1", Name: "Active Recovery", PctLow: 0, PctHigh: 68},
		{Label: "Z2", Name: "Endurance", PctLow: 69, PctHigh: 83},
		{Label: "Z3", Name: "Tempo", PctLow: 84, PctHigh: 94},
		{Label: "Z4", Name: "Lactate Threshold", PctLow: 95, PctHigh: 105},
		{Label: "Z5", Name: "VO2 Max", PctLow: 106, PctHigh: 0}, // 0 = open-ended
	}
	prevHigh := 0
	for i := range zones {
		bpmLow := prevHigh + 1
		if i == 0 {
			bpmLow = 0
		}
		bpmHigh := 0
		if thresholdHR > 0 && zones[i].PctHigh > 0 {
			bpmHigh = int(float64(zones[i].PctHigh) * float64(thresholdHR) / 100.0)
		}
		zones[i].BPMLow = bpmLow
		zones[i].BPMHigh = bpmHigh
		prevHigh = bpmHigh
	}
	return zones
}

// ResolveHRZones returns the effective HR zones for an athlete, using custom
// zones if configured, otherwise the default threshold-based model.
func ResolveHRZones(a *models.Athlete) []models.HRZone {
	if a.HRZonesJSON != "" {
		var maxBPMs [6]int
		if err := json.Unmarshal([]byte(a.HRZonesJSON), &maxBPMs); err == nil {
			return CustomHRZones(maxBPMs)
		}
	}
	return HRZones(a.ThresholdHR)
}

// ComputeZoneTimes returns seconds spent in each power zone [7] and HR zone [5].
// Zones with zero length are skipped (no FTP or no LTHR configured).
func ComputeZoneTimes(streams []models.Stream, powerZones []models.PowerZone, hrZones []models.HRZone) ([7]int, [5]int) {
	var power [7]int
	var hr [5]int
	for i, s := range streams {
		dt := 1
		if i > 0 {
			dt = int(s.Timestamp.Sub(streams[i-1].Timestamp).Seconds())
			if dt <= 0 || dt > 30 {
				dt = 1
			}
		}
		if s.PowerWatts != nil && len(powerZones) > 0 {
			if zi := powerZoneIdx(*s.PowerWatts, powerZones); zi >= 0 && zi < 7 {
				power[zi] += dt
			}
		}
		if s.HeartRateBPM != nil && len(hrZones) > 0 {
			if zi := hrZoneIdx(*s.HeartRateBPM, hrZones); zi >= 0 && zi < 5 {
				hr[zi] += dt
			}
		}
	}
	return power, hr
}

func powerZoneIdx(watts int, zones []models.PowerZone) int {
	for i, z := range zones {
		if z.WattsHigh == 0 || watts <= z.WattsHigh {
			return i
		}
	}
	return -1
}

func hrZoneIdx(bpm int, zones []models.HRZone) int {
	for i, z := range zones {
		if z.BPMHigh == 0 || bpm <= z.BPMHigh {
			return i
		}
	}
	return -1
}

// PowerZoneRangeLabels returns display strings like "< 140W", "140–188W", "≥ 378W"
// for each of the 7 power zones. Returns nil if ftp <= 0.
func PowerZoneRangeLabels(ftp int) []string {
	if ftp <= 0 {
		return nil
	}
	zones := PowerZones(ftp)[:7]
	labels := make([]string, 7)
	for i, z := range zones {
		switch {
		case i == 0:
			labels[i] = fmt.Sprintf("< %dW", z.WattsHigh+1)
		case z.WattsHigh == 0:
			labels[i] = fmt.Sprintf("≥ %dW", z.WattsLow)
		default:
			labels[i] = fmt.Sprintf("%d–%dW", z.WattsLow, z.WattsHigh)
		}
	}
	return labels
}

// HRZoneRangeLabels returns display strings like "< 139 bpm", "139–167 bpm", "≥ 168 bpm"
// for each HR zone. Returns nil if thresholdHR <= 0.
func HRZoneRangeLabels(zones []models.HRZone, thresholdHR int) []string {
	if thresholdHR <= 0 || len(zones) == 0 {
		return nil
	}
	labels := make([]string, len(zones))
	for i, z := range zones {
		switch {
		case i == 0:
			labels[i] = fmt.Sprintf("< %d bpm", z.BPMHigh+1)
		case z.BPMHigh == 0:
			labels[i] = fmt.Sprintf("≥ %d bpm", z.BPMLow)
		default:
			labels[i] = fmt.Sprintf("%d–%d bpm", z.BPMLow, z.BPMHigh)
		}
	}
	return labels
}

// CustomHRZones builds HR zones from user-supplied upper BPM bounds.
// maxBPMs[0]–[3] are Z1–Z4 upper bounds; Z5 is always open-ended.
func CustomHRZones(maxBPMs [6]int) []models.HRZone {
	zones := []models.HRZone{
		{Label: "Z1", Name: "Active Recovery"},
		{Label: "Z2", Name: "Endurance"},
		{Label: "Z3", Name: "Tempo"},
		{Label: "Z4", Name: "Lactate Threshold"},
		{Label: "Z5", Name: "VO2 Max"},
	}
	prevHigh := 0
	for i := range zones {
		lo := prevHigh
		if i > 0 {
			lo = prevHigh + 1
		}
		hi := 0
		if i < len(maxBPMs) {
			hi = maxBPMs[i]
		}
		zones[i].BPMLow = lo
		zones[i].BPMHigh = hi
		prevHigh = hi
	}
	return zones
}
