package db

import (
	"math"
	"testing"
)

// noisyPath builds a long zig-zag track. It is intentionally large enough to
// force deep RDP recursion and many 2-point base-case returns, which is the
// shape that exposed the original slice-aliasing corruption.
func noisyPath(n int) [][2]float64 {
	pts := make([][2]float64, n)
	for i := 0; i < n; i++ {
		pts[i] = [2]float64{
			float64(i) * 0.001,
			0.0005 * math.Sin(float64(i)*0.7),
		}
	}
	return pts
}

// rdpSimplify and simplifyCoords must be pure with respect to their input:
// the original 75-point downsample never mutated callers' data and several
// call sites (InsertWorkout, the backfill/rebuild jobs) reuse the point slice.
// A regression here previously corrupted every stored route.
func TestRDPSimplifyDoesNotMutateInput(t *testing.T) {
	pts := noisyPath(200)
	orig := make([][2]float64, len(pts))
	copy(orig, pts)

	_ = rdpSimplify(pts, 0.00005)

	for i := range pts {
		if pts[i] != orig[i] {
			t.Fatalf("input mutated at idx %d: got %v want %v", i, pts[i], orig[i])
		}
	}
}

// The simplified polyline must be an in-order subsequence of the input: every
// output point is an input point, and they appear in the original order. This
// fails loudly if the backing array is ever corrupted.
func TestRDPSimplifyIsOrderedSubsequence(t *testing.T) {
	in := noisyPath(300)
	out := rdpSimplify(in, 0.00005)

	if len(out) < 2 {
		t.Fatalf("expected at least the two endpoints, got %d", len(out))
	}
	j := 0
	for _, o := range out {
		matched := false
		for j < len(in) {
			if in[j] == o {
				matched = true
				j++
				break
			}
			j++
		}
		if !matched {
			t.Fatalf("output point %v is not an in-order member of the input", o)
		}
	}
}

func TestRDPSimplifyCollapsesCollinearPoints(t *testing.T) {
	// 50 perfectly collinear points must reduce to just the two endpoints.
	pts := make([][2]float64, 50)
	for i := range pts {
		pts[i] = [2]float64{float64(i), 2 * float64(i)}
	}
	out := rdpSimplify(pts, 1e-9)
	if len(out) != 2 {
		t.Fatalf("collinear path should reduce to 2 points, got %d", len(out))
	}
	if out[0] != pts[0] || out[1] != pts[len(pts)-1] {
		t.Fatalf("endpoints not preserved: %v", out)
	}
}

func TestSimplifyCoordsCapAndEndpoints(t *testing.T) {
	in := noisyPath(5000)
	const maxPts = 500
	out := simplifyCoords(in, maxPts)

	if len(out) > maxPts {
		t.Fatalf("output exceeds cap: %d > %d", len(out), maxPts)
	}
	if out[0] != in[0] {
		t.Fatalf("first point not preserved: got %v want %v", out[0], in[0])
	}
	if out[len(out)-1] != in[len(in)-1] {
		t.Fatalf("last point not preserved: got %v want %v", out[len(out)-1], in[len(in)-1])
	}
}

func TestSimplifyCoordsTooFewPoints(t *testing.T) {
	if got := simplifyCoords([][2]float64{{1, 2}}, 500); len(got) != 0 {
		t.Fatalf("expected empty slice for <2 points, got %v", got)
	}
	if got := simplifyCoords(nil, 500); len(got) != 0 {
		t.Fatalf("expected empty slice for nil input, got %v", got)
	}
}
