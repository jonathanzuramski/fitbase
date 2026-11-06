package fitness_test

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/models"
)

const shapeFTP = 250

// seg is a block of constant target power at 1Hz. hr 0 records no HR.
type seg struct {
	secs, watts, hr int
}

// rideBuilder assembles a synthetic 1Hz ride from blocks, optionally with
// deterministic noise and coasting so tests can model outdoor power.
type rideBuilder struct {
	streams []models.Stream
	t       time.Time
	rng     *rand.Rand
	noise   float64 // ± fraction of target applied per second
	coastEv int     // coast for coastLen seconds every coastEv seconds (0 = never)
	coastLn int
	elapsed int
}

func newRide() *rideBuilder {
	return &rideBuilder{t: time.Unix(1_700_000_000, 0).UTC(), rng: rand.New(rand.NewSource(7))}
}

func (b *rideBuilder) add(segs ...seg) *rideBuilder {
	for _, s := range segs {
		for i := 0; i < s.secs; i++ {
			w := float64(s.watts)
			if b.noise > 0 {
				w *= 1 + (b.rng.Float64()*2-1)*b.noise
			}
			if b.coastEv > 0 && b.elapsed%b.coastEv < b.coastLn {
				w = 0
			}
			wi := int(math.Round(w))
			rec := models.Stream{Timestamp: b.t, PowerWatts: &wi}
			if s.hr > 0 {
				hr := s.hr
				rec.HeartRateBPM = &hr
			}
			cad := 90
			if wi == 0 {
				cad = 0
			}
			rec.CadenceRPM = &cad
			b.streams = append(b.streams, rec)
			b.t = b.t.Add(time.Second)
			b.elapsed++
		}
	}
	return b
}

// gap skips time without writing records — an auto-pause or a stop.
func (b *rideBuilder) gap(secs int) *rideBuilder {
	b.t = b.t.Add(time.Duration(secs) * time.Second)
	b.elapsed += secs
	return b
}

func pct(p int) int { return shapeFTP * p / 100 }

func within(got, want, tol int) bool { return got >= want-tol && got <= want+tol }

// The canonical structured session: five clean ERG steps must come back as
// five efforts of the right length and power, grouped into one set, with the
// recoveries between them measured.
func TestAnalyzeShape_ERGVO2Set(t *testing.T) {
	b := newRide().add(seg{600, pct(60), 120})
	for i := 0; i < 5; i++ {
		b.add(seg{240, pct(120), 165 + i*2})
		if i < 4 {
			b.add(seg{240, pct(50), 130})
		}
	}
	b.add(seg{300, pct(50), 125}, seg{300, pct(40), 115})

	shape := fitness.AnalyzeShape(b.streams, shapeFTP)
	if len(shape.Efforts) != 5 {
		t.Fatalf("got %d efforts, want 5: %+v", len(shape.Efforts), shape.Efforts)
	}
	for i, e := range shape.Efforts {
		if !within(e.DurationSecs, 240, 2) {
			t.Errorf("effort %d duration = %ds, want ~240", i+1, e.DurationSecs)
		}
		if !within(int(math.Round(e.AvgWatts)), pct(120), 2) {
			t.Errorf("effort %d avg = %.0fW, want ~%dW", i+1, e.AvgWatts, pct(120))
		}
		if wantStart := 600 + i*480; !within(e.StartSecs, wantStart, 2) {
			t.Errorf("effort %d start = %ds, want ~%d", i+1, e.StartSecs, wantStart)
		}
		if i < 4 {
			if !within(e.RecoverySecs, 240, 3) {
				t.Errorf("effort %d recovery = %ds, want ~240", i+1, e.RecoverySecs)
			}
			if !within(int(math.Round(e.RecoveryAvgWatts)), pct(50), 3) {
				t.Errorf("effort %d recovery avg = %.0fW, want ~%d", i+1, e.RecoveryAvgWatts, pct(50))
			}
		}
	}
	if last := shape.Efforts[4]; last.RecoverySecs != 0 {
		t.Errorf("last effort recovery = %d, want 0 (nothing follows)", last.RecoverySecs)
	}
	if shape.Efforts[4].MaxHR != 173 {
		t.Errorf("rep 5 max HR = %d, want 173", shape.Efforts[4].MaxHR)
	}
	if len(shape.Sets) != 1 || shape.Sets[0].First != 0 || shape.Sets[0].Count != 5 {
		t.Errorf("sets = %+v, want one set of 5 starting at 0", shape.Sets)
	}
}

// A cooldown that starts at 75% FTP right after the last rep is above the
// absolute floor (70%) — only the effort-relative floor ends the rep at the
// step. Otherwise minutes of cooldown dilute the last rep and read as fade.
func TestAnalyzeShape_CooldownDoesNotExtendLastRep(t *testing.T) {
	b := newRide().add(seg{300, pct(55), 0})
	for i := 0; i < 3; i++ {
		b.add(seg{480, pct(105), 0})
		if i < 2 {
			b.add(seg{180, pct(50), 0})
		}
	}
	b.add(seg{240, pct(75), 0}, seg{240, pct(60), 0})

	shape := fitness.AnalyzeShape(b.streams, shapeFTP)
	if len(shape.Efforts) != 3 {
		t.Fatalf("got %d efforts, want 3: %+v", len(shape.Efforts), shape.Efforts)
	}
	last := shape.Efforts[2]
	if !within(last.DurationSecs, 480, 2) {
		t.Errorf("last rep duration = %ds, want ~480 (cooldown leaked in)", last.DurationSecs)
	}
	if !within(int(math.Round(last.AvgWatts)), pct(105), 2) {
		t.Errorf("last rep avg = %.0fW, want ~%d", last.AvgWatts, pct(105))
	}
}

// Outdoor power is noisy and has coasting gaps; a 2×20 sweet-spot session
// must still come back as two long efforts, not dozens of fragments.
func TestAnalyzeShape_NoisyOutdoorSweetSpot(t *testing.T) {
	b := newRide()
	b.noise, b.coastEv, b.coastLn = 0.15, 90, 4
	b.add(seg{900, pct(60), 125}, seg{1200, pct(91), 155}, seg{300, pct(50), 130},
		seg{1200, pct(91), 158}, seg{900, pct(55), 125})

	shape := fitness.AnalyzeShape(b.streams, shapeFTP)
	if len(shape.Efforts) != 2 {
		t.Fatalf("got %d efforts, want 2: %+v", len(shape.Efforts), shape.Efforts)
	}
	for i, e := range shape.Efforts {
		if !within(e.DurationSecs, 1200, 20) {
			t.Errorf("effort %d duration = %ds, want ~1200", i+1, e.DurationSecs)
		}
		// Coasting seconds count toward the average like any avg-power figure,
		// so the effort reads a little under target.
		if got := e.AvgWatts / shapeFTP * 100; got < 84 || got > 92 {
			t.Errorf("effort %d avg = %.1f%% FTP, want ~86–91", i+1, got)
		}
		if e.NPWatts <= e.AvgWatts {
			t.Errorf("effort %d NP %.0f should exceed avg %.0f on variable power", i+1, e.NPWatts, e.AvgWatts)
		}
	}
	if len(shape.Sets) != 1 || shape.Sets[0].Count != 2 {
		t.Errorf("sets = %+v, want one set of 2", shape.Sets)
	}
}

// Short-rest VO2 work (30/30s) must split into individual reps.
func TestAnalyzeShape_ThirtyThirties(t *testing.T) {
	b := newRide().add(seg{600, pct(60), 0})
	for i := 0; i < 10; i++ {
		b.add(seg{30, pct(130), 0}, seg{30, pct(50), 0})
	}
	b.add(seg{600, pct(55), 0})

	shape := fitness.AnalyzeShape(b.streams, shapeFTP)
	if len(shape.Efforts) != 10 {
		t.Fatalf("got %d efforts, want 10: %+v", len(shape.Efforts), shape.Efforts)
	}
	for i, e := range shape.Efforts {
		if !within(e.DurationSecs, 30, 2) {
			t.Errorf("rep %d duration = %ds, want ~30", i+1, e.DurationSecs)
		}
	}
	if len(shape.Sets) != 1 || shape.Sets[0].Count != 10 {
		t.Errorf("sets = %+v, want one set of 10", shape.Sets)
	}
}

// A steady endurance ride has no efforts — surges inside the noise must not
// be reported as intervals.
func TestAnalyzeShape_SteadyEnduranceHasNoEfforts(t *testing.T) {
	b := newRide()
	b.noise = 0.12
	b.add(seg{7200, pct(65), 135})

	shape := fitness.AnalyzeShape(b.streams, shapeFTP)
	if len(shape.Efforts) != 0 {
		t.Errorf("got %d efforts on a steady Z2 ride, want 0: %+v", len(shape.Efforts), shape.Efforts)
	}
	if len(shape.Buckets) == 0 {
		t.Error("profile is empty; a steady ride still needs its pacing profile")
	}
}

// A recording gap is a stop, not a held effort: two efforts either side of a
// café stop stay separate, and the stop contributes nothing to averages.
func TestAnalyzeShape_RecordingGapSplitsEfforts(t *testing.T) {
	b := newRide().add(seg{300, pct(60), 0}, seg{180, pct(100), 0})
	b.gap(600)
	b.add(seg{180, pct(100), 0}, seg{300, pct(60), 0})

	shape := fitness.AnalyzeShape(b.streams, shapeFTP)
	if len(shape.Efforts) != 2 {
		t.Fatalf("got %d efforts across a stop, want 2: %+v", len(shape.Efforts), shape.Efforts)
	}
	first := shape.Efforts[0]
	if !within(first.DurationSecs, 180, 2) {
		t.Errorf("effort before stop = %ds, want ~180 (forward-filled across the gap?)", first.DurationSecs)
	}
	if first.RecoveryAvgWatts != 0 {
		t.Errorf("recovery avg across a stop = %.0fW, want 0 (no recorded seconds)", first.RecoveryAvgWatts)
	}
}

// Reps of a different length are a different set — a 2×20 followed by sprints
// is two sets, not one.
func TestAnalyzeShape_SetsSplitOnDuration(t *testing.T) {
	b := newRide().add(seg{600, pct(55), 0})
	b.add(seg{1200, pct(95), 0}, seg{300, pct(50), 0}, seg{1200, pct(95), 0}, seg{600, pct(50), 0})
	for i := 0; i < 4; i++ {
		b.add(seg{60, pct(150), 0}, seg{120, pct(45), 0})
	}

	shape := fitness.AnalyzeShape(b.streams, shapeFTP)
	if len(shape.Efforts) != 6 {
		t.Fatalf("got %d efforts, want 6: %+v", len(shape.Efforts), shape.Efforts)
	}
	if len(shape.Sets) != 2 {
		t.Fatalf("got %d sets, want 2: %+v", len(shape.Sets), shape.Sets)
	}
	if s := shape.Sets[0]; s.First != 0 || s.Count != 2 {
		t.Errorf("set 1 = %+v, want first=0 count=2", s)
	}
	if s := shape.Sets[1]; s.First != 2 || s.Count != 4 {
		t.Errorf("set 2 = %+v, want first=2 count=4", s)
	}
}

// Without FTP there is no threshold to detect against, but the profile still
// describes the ride.
func TestAnalyzeShape_NoFTPStillBuildsProfile(t *testing.T) {
	b := newRide().add(seg{600, 150, 120}, seg{240, 300, 165}, seg{600, 150, 125})

	shape := fitness.AnalyzeShape(b.streams, 0)
	if len(shape.Efforts) != 0 {
		t.Errorf("got %d efforts with no FTP, want 0", len(shape.Efforts))
	}
	if len(shape.Buckets) == 0 || !shape.HasPower || !shape.HasHR {
		t.Errorf("profile = %+v (power=%v hr=%v), want buckets with power and HR", shape.Buckets, shape.HasPower, shape.HasHR)
	}
}

// Bucket size scales with ride length so the profile stays at most 30 rows.
func TestAnalyzeShape_ProfileBucketSize(t *testing.T) {
	cases := []struct {
		secs, wantBucket int
	}{
		{1800, 60},
		{3600, 120},
		{4 * 3600, 600},
	}
	for _, c := range cases {
		b := newRide().add(seg{c.secs, pct(60), 130})
		shape := fitness.AnalyzeShape(b.streams, shapeFTP)
		if shape.BucketSecs != c.wantBucket {
			t.Errorf("%ds ride: bucket = %ds, want %d", c.secs, shape.BucketSecs, c.wantBucket)
		}
		if len(shape.Buckets) > 30 {
			t.Errorf("%ds ride: %d buckets, want <= 30", c.secs, len(shape.Buckets))
		}
		if got := shape.Buckets[0].AvgWatts; !within(int(got), pct(60), 1) {
			t.Errorf("%ds ride: bucket avg = %.0f, want ~%d", c.secs, got, pct(60))
		}
	}
}
