package fitparser_test

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/fitparser"
	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/proto"
)

// ── Parse ─────────────────────────────────────────────────────────────────────

// buildTestFIT creates a minimal but valid FIT activity binary using the
// muktihari encoder. All raw values follow FIT field scaling conventions.
func buildTestFIT(t *testing.T, o testFITOpts) []byte {
	t.Helper()

	startTime := time.Date(2024, 3, 15, 8, 0, 0, 0, time.UTC)

	fileId := mesgdef.NewFileId(nil)
	fileId.Type = typedef.FileActivity
	fileId.Manufacturer = typedef.ManufacturerGarmin
	fileId.TimeCreated = startTime

	session := mesgdef.NewSession(nil)
	session.Sport = o.sport
	session.StartTime = startTime
	session.Timestamp = startTime.Add(time.Duration(o.durationSecs) * time.Second)
	session.Event = typedef.EventSession
	session.EventType = typedef.EventTypeStop
	// Raw field values — must match FIT scaling:
	// TotalTimerTime: uint32 raw = seconds * 1000
	session.TotalTimerTime = uint32(o.durationSecs) * 1000
	// TotalDistance: uint32 raw = meters * 100
	session.TotalDistance = uint32(o.distanceMeters * 100)
	session.TotalAscent = uint16(o.elevationGain)
	session.AvgPower = uint16(o.avgPower)
	session.MaxPower = uint16(o.maxPower)
	session.AvgHeartRate = uint8(o.avgHR)
	session.MaxHeartRate = uint8(o.maxHR)
	session.AvgCadence = uint8(o.avgCadence)
	// EnhancedAvgSpeed: uint32 raw = m/s * 1000
	session.EnhancedAvgSpeed = uint32(o.avgSpeedMPS * 1000)

	activity := mesgdef.NewActivity(nil)
	activity.Timestamp = session.Timestamp
	activity.NumSessions = 1
	activity.Type = typedef.ActivityManual
	activity.Event = typedef.EventActivity
	activity.EventType = typedef.EventTypeStop

	msgs := []proto.Message{
		fileId.ToMesg(nil),
	}

	// Add record messages
	for i, r := range o.records {
		rec := mesgdef.NewRecord(nil)
		rec.Timestamp = startTime.Add(time.Duration(i+1) * time.Second)
		rec.Power = uint16(r.power)
		rec.HeartRate = uint8(r.hr)
		rec.Cadence = uint8(r.cadence)
		rec.EnhancedSpeed = uint32(r.speedMPS * 1000)
		// EnhancedAltitude: uint32 raw = (meters + 500) * 5
		rec.EnhancedAltitude = uint32((r.altitudeMeters + 500) * 5)
		// Distance: uint32 raw = meters * 100
		rec.Distance = uint32(r.distanceMeters * 100)
		if r.lat != 0 || r.lng != 0 {
			rec.PositionLat = int32(r.lat * float64(math.MaxInt32) / 180.0)
			rec.PositionLong = int32(r.lng * float64(math.MaxInt32) / 180.0)
		}
		msgs = append(msgs, rec.ToMesg(nil))
	}

	msgs = append(msgs, session.ToMesg(nil))
	msgs = append(msgs, activity.ToMesg(nil))

	fit := proto.FIT{Messages: msgs}

	var buf bytes.Buffer
	enc := encoder.New(&buf)
	if err := enc.Encode(&fit); err != nil {
		t.Fatalf("encode test FIT: %v", err)
	}
	return buf.Bytes()
}

type testRecord struct {
	power          int
	hr             int
	cadence        int
	speedMPS       float64
	altitudeMeters float64
	distanceMeters float64
	lat, lng       float64
}

type testFITOpts struct {
	sport          typedef.Sport
	durationSecs   int
	distanceMeters float64
	elevationGain  int
	avgPower       int
	maxPower       int
	avgHR          int
	maxHR          int
	avgCadence     int
	avgSpeedMPS    float64
	records        []testRecord
}

func defaultOpts() testFITOpts {
	return testFITOpts{
		sport:          typedef.SportCycling,
		durationSecs:   3600,
		distanceMeters: 36000,
		elevationGain:  500,
		avgPower:       200,
		maxPower:       400,
		avgHR:          150,
		maxHR:          180,
		avgCadence:     90,
		avgSpeedMPS:    10.0,
		records: []testRecord{
			{power: 200, hr: 150, cadence: 90, speedMPS: 10.0, altitudeMeters: 100.0, distanceMeters: 10.0},
			{power: 210, hr: 155, cadence: 92, speedMPS: 10.2, altitudeMeters: 105.0, distanceMeters: 20.0},
			{power: 190, hr: 148, cadence: 88, speedMPS: 9.8, altitudeMeters: 102.0, distanceMeters: 30.0},
		},
	}
}

func TestParse_BasicCyclingActivity(t *testing.T) {
	opts := defaultOpts()
	data := buildTestFIT(t, opts)

	result, err := fitparser.Parse(bytes.NewReader(data), "test.fit", 250, 0, 0)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	w := result.Workout

	if w.Sport != "cycling" {
		t.Errorf("Sport: got %q want %q", w.Sport, "cycling")
	}
	if w.DurationSecs != opts.durationSecs {
		t.Errorf("DurationSecs: got %d want %d", w.DurationSecs, opts.durationSecs)
	}
	if math.Abs(w.DistanceMeters-opts.distanceMeters) > 1.0 {
		t.Errorf("DistanceMeters: got %.1f want %.1f", w.DistanceMeters, opts.distanceMeters)
	}
	if w.ElevationGainMeters != float64(opts.elevationGain) {
		t.Errorf("ElevationGain: got %.0f want %d", w.ElevationGainMeters, opts.elevationGain)
	}
	if w.AvgPowerWatts == nil || *w.AvgPowerWatts != float64(opts.avgPower) {
		t.Errorf("AvgPower: got %v want %d", w.AvgPowerWatts, opts.avgPower)
	}
	if w.MaxPowerWatts == nil || *w.MaxPowerWatts != float64(opts.maxPower) {
		t.Errorf("MaxPower: got %v want %d", w.MaxPowerWatts, opts.maxPower)
	}
	if w.AvgHeartRate == nil || *w.AvgHeartRate != opts.avgHR {
		t.Errorf("AvgHR: got %v want %d", w.AvgHeartRate, opts.avgHR)
	}
	if w.MaxHeartRate == nil || *w.MaxHeartRate != opts.maxHR {
		t.Errorf("MaxHR: got %v want %d", w.MaxHeartRate, opts.maxHR)
	}
	if w.AvgCadenceRPM == nil || *w.AvgCadenceRPM != opts.avgCadence {
		t.Errorf("AvgCadence: got %v want %d", w.AvgCadenceRPM, opts.avgCadence)
	}
	if math.Abs(w.AvgSpeedMPS-opts.avgSpeedMPS) > 0.01 {
		t.Errorf("AvgSpeedMPS: got %.3f want %.3f", w.AvgSpeedMPS, opts.avgSpeedMPS)
	}
}

func TestParse_IDIsSHA256Prefix(t *testing.T) {
	data := buildTestFIT(t, defaultOpts())
	r1, err := fitparser.Parse(bytes.NewReader(data), "a.fit", 250, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := fitparser.Parse(bytes.NewReader(data), "b.fit", 250, 0, 0) // same data, different filename
	if err != nil {
		t.Fatal(err)
	}
	if r1.ID != r2.ID {
		t.Error("same file content should produce the same ID regardless of filename")
	}
	if len(r1.ID) != 16 {
		t.Errorf("ID should be 16 hex chars, got %d", len(r1.ID))
	}
}

func TestParse_Streams(t *testing.T) {
	opts := defaultOpts()
	data := buildTestFIT(t, opts)

	result, err := fitparser.Parse(bytes.NewReader(data), "test.fit", 250, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Streams) != len(opts.records) {
		t.Fatalf("stream count: got %d want %d", len(result.Streams), len(opts.records))
	}

	s0 := result.Streams[0]
	if s0.PowerWatts == nil || *s0.PowerWatts != opts.records[0].power {
		t.Errorf("stream[0] power: got %v want %d", s0.PowerWatts, opts.records[0].power)
	}
	if s0.HeartRateBPM == nil || *s0.HeartRateBPM != opts.records[0].hr {
		t.Errorf("stream[0] HR: got %v want %d", s0.HeartRateBPM, opts.records[0].hr)
	}
	if s0.CadenceRPM == nil || *s0.CadenceRPM != opts.records[0].cadence {
		t.Errorf("stream[0] cadence: got %v want %d", s0.CadenceRPM, opts.records[0].cadence)
	}
	if s0.SpeedMPS == nil || math.Abs(*s0.SpeedMPS-opts.records[0].speedMPS) > 0.01 {
		t.Errorf("stream[0] speed: got %v want %.2f", s0.SpeedMPS, opts.records[0].speedMPS)
	}
	if s0.AltitudeMeters == nil || math.Abs(*s0.AltitudeMeters-opts.records[0].altitudeMeters) > 0.5 {
		t.Errorf("stream[0] altitude: got %v want %.1f", s0.AltitudeMeters, opts.records[0].altitudeMeters)
	}
	if s0.DistanceMeters == nil || math.Abs(*s0.DistanceMeters-opts.records[0].distanceMeters) > 0.5 {
		t.Errorf("stream[0] distance: got %v want %.1f", s0.DistanceMeters, opts.records[0].distanceMeters)
	}
}

func TestParse_GPSCoordinates(t *testing.T) {
	opts := defaultOpts()
	opts.records = []testRecord{
		{
			power: 200, hr: 150, cadence: 90, speedMPS: 10.0,
			altitudeMeters: 100.0, distanceMeters: 10.0,
			lat: 51.5074, lng: -0.1278, // London
		},
	}
	data := buildTestFIT(t, opts)

	result, err := fitparser.Parse(bytes.NewReader(data), "test.fit", 250, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Streams) == 0 {
		t.Fatal("no streams")
	}
	s := result.Streams[0]
	if s.Lat == nil || s.Lng == nil {
		t.Fatal("expected GPS coordinates, got nil")
	}
	if math.Abs(*s.Lat-51.5074) > 0.001 {
		t.Errorf("lat: got %.4f want ~51.5074", *s.Lat)
	}
	if math.Abs(*s.Lng-(-0.1278)) > 0.001 {
		t.Errorf("lng: got %.4f want ~-0.1278", *s.Lng)
	}
}

func TestParse_NPAndTSS(t *testing.T) {
	// 30 identical power records → NP ≈ avg power
	recs := make([]testRecord, 60)
	for i := range recs {
		recs[i] = testRecord{power: 250, hr: 150, cadence: 90, speedMPS: 10.0, altitudeMeters: 100.0}
	}
	opts := defaultOpts()
	opts.durationSecs = 60
	opts.avgPower = 250
	opts.records = recs

	data := buildTestFIT(t, opts)
	result, err := fitparser.Parse(bytes.NewReader(data), "test.fit", 250, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	w := result.Workout
	if w.NormalizedPower == nil {
		t.Fatal("expected NP to be calculated")
	}
	// NP of constant 250W ≈ 250W
	if math.Abs(*w.NormalizedPower-250.0) > 1.0 {
		t.Errorf("NP: got %.1f want ~250.0", *w.NormalizedPower)
	}
	if w.IntensityFactor == nil {
		t.Fatal("expected IF to be calculated")
	}
	// IF = NP / FTP = 250/250 = 1.0
	if math.Abs(*w.IntensityFactor-1.0) > 0.01 {
		t.Errorf("IF: got %.3f want ~1.0", *w.IntensityFactor)
	}
	if w.TSS == nil {
		t.Fatal("expected TSS to be calculated")
	}
}

func TestParse_NoNPWithoutFTP(t *testing.T) {
	recs := make([]testRecord, 60)
	for i := range recs {
		recs[i] = testRecord{power: 250}
	}
	opts := defaultOpts()
	opts.records = recs
	data := buildTestFIT(t, opts)

	result, err := fitparser.Parse(bytes.NewReader(data), "test.fit", 0, 0, 0) // FTP=0
	if err != nil {
		t.Fatal(err)
	}
	if result.Workout.NormalizedPower != nil {
		t.Error("expected nil NP when FTP=0")
	}
	if result.Workout.TSS != nil {
		t.Error("expected nil TSS when FTP=0")
	}
}

func TestParse_SportTypes(t *testing.T) {
	tests := []struct {
		sport     typedef.Sport
		wantSport string
		wantSkip  bool
	}{
		{typedef.SportCycling, "cycling", false},
		{typedef.SportRunning, "running", false},
		{typedef.SportSwimming, "swimming", false},
		{typedef.SportHiking, "hiking", false},
		{typedef.Sport(99), "", true}, // unsupported → ErrSkipped
	}
	for _, tt := range tests {
		opts := defaultOpts()
		opts.sport = tt.sport
		data := buildTestFIT(t, opts)

		result, err := fitparser.Parse(bytes.NewReader(data), "test.fit", 250, 0, 0)
		if tt.wantSkip {
			if err != fitparser.ErrSkipped {
				t.Errorf("sport %v: want ErrSkipped, got err=%v result=%v", tt.sport, err, result)
			}
			continue
		}
		if err != nil {
			t.Errorf("sport %v: parse error: %v", tt.sport, err)
			continue
		}
		if result.Workout.Sport != tt.wantSport {
			t.Errorf("sport %v: got %q want %q", tt.sport, result.Workout.Sport, tt.wantSport)
		}
	}
}

func TestParse_InvalidData(t *testing.T) {
	_, err := fitparser.Parse(strings.NewReader("not a fit file"), "bad.fit", 250, 0, 0)
	if err == nil {
		t.Error("expected error parsing invalid data")
	}
}

func TestParse_EmptyReader(t *testing.T) {
	_, err := fitparser.Parse(bytes.NewReader(nil), "empty.fit", 250, 0, 0)
	if err == nil {
		t.Error("expected error parsing empty data")
	}
}

func TestParse_FilenamePreserved(t *testing.T) {
	data := buildTestFIT(t, defaultOpts())
	result, err := fitparser.Parse(bytes.NewReader(data), "my_ride.fit", 250, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Workout.Filename != "my_ride.fit" {
		t.Errorf("Filename: got %q want %q", result.Workout.Filename, "my_ride.fit")
	}
}
