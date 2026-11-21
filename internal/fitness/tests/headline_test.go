package fitness_test

import (
	"testing"

	"github.com/fitbase/fitbase/internal/fitness"
)

func TestShapeHeadline(t *testing.T) {
	set := func(watts []float64, hr []int, dur int) fitness.RideShape {
		s := fitness.RideShape{HasPower: true}
		for i, w := range watts {
			e := fitness.Effort{StartSecs: 600 + i*2*dur, DurationSecs: dur, AvgWatts: w}
			if hr != nil {
				e.MaxHR = hr[i]
			}
			s.Efforts = append(s.Efforts, e)
		}
		s.Sets = []fitness.EffortSet{{First: 0, Count: len(watts)}}
		return s
	}
	cases := []struct {
		name  string
		shape fitness.RideShape
		ftp   int
		want  string
	}{
		{"no power", fitness.RideShape{}, 250, ""},
		{"no ftp", fitness.RideShape{HasPower: true}, 0, ""},
		{"steady", fitness.RideShape{HasPower: true}, 250, "steady — no efforts at or above 85% FTP"},
		{
			"set with fade and HR creep",
			set([]float64{300, 298, 296, 290, 281}, []int{160, 162, 164, 166, 168}, 240), 250,
			"5×4:00 @ 117% FTP, faded 6.3% last rep vs first, max HR +8",
		},
		{
			"set without HR",
			set([]float64{230, 232}, nil, 1200), 250,
			"2×20:00 @ 92% FTP, held power across reps",
		},
		{
			"set plus a loose effort",
			func() fitness.RideShape {
				s := set([]float64{300, 300, 300}, nil, 240)
				s.Efforts = append(s.Efforts, fitness.Effort{StartSecs: 5000, DurationSecs: 600, AvgWatts: 230})
				return s
			}(), 250,
			"3×4:00 @ 120% FTP, held power across reps; 1 other effort: 10:00 @ 92%",
		},
		{
			"loose efforts only, capped",
			fitness.RideShape{HasPower: true, Efforts: []fitness.Effort{
				{DurationSecs: 300, AvgWatts: 240}, {DurationSecs: 30, AvgWatts: 400},
				{DurationSecs: 720, AvgWatts: 235}, {DurationSecs: 25, AvgWatts: 380},
				{DurationSecs: 480, AvgWatts: 250}, {DurationSecs: 3900, AvgWatts: 215},
			}}, 250,
			"6 efforts: 5:00 @ 96%, 12:00 @ 94%, 8:00 @ 100%, 1:05:00 @ 86% +2 more",
		},
		{
			// From the real-data eval: a 2×12 whose second rep stopped at 9:52.
			// Averaging to "2×11" hid the cut-short rep from the insights model.
			"uneven reps are listed, not averaged",
			func() fitness.RideShape {
				s := set([]float64{209, 209}, []int{165, 169}, 719)
				s.Efforts[1].DurationSecs = 592
				return s
			}(), 245,
			"2 reps 11:59/9:52 @ 85% FTP, held power across reps, max HR +4",
		},
		{
			// Also from the eval: "+2.1% vs first" was read as a fade.
			"a rider who built says so in words",
			set([]float64{210, 210, 214.5}, []int{154, 159, 168}, 717), 245,
			"3×11:57 @ 86% FTP, built 2.1% last rep vs first, max HR +14",
		},
	}
	for _, c := range cases {
		if got := fitness.ShapeHeadline(c.shape, c.ftp); got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}
