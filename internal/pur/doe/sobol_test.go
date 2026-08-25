package doe

import (
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

// The property that justifies Sobol over plain Monte Carlo: for the first 2^m
// points, every parameter is split into 2^m equal intervals with exactly one
// point in each.
//
// This is also the test oracle for the direction-number table. A wrong entry
// still produces plausible-looking output, but it breaks stratification, so this
// catches a bad table where eyeballing the points would not.
func TestSobol_StratifiesInPowersOfTwo(t *testing.T) {
	for k := 1; k <= sobolMaxDimensions; k++ {
		for m := 1; m <= 6; m++ {
			n := 1 << uint(m)

			points, problem := sobolPoints(numericParams(k, 0), n)
			if problem != nil {
				t.Fatalf("k=%d n=%d: sobolPoints() failed: %v", k, n, problem)
			}
			if len(points) != n {
				t.Fatalf("k=%d: got %d points, want %d", k, len(points), n)
			}

			for d := 0; d < k; d++ {
				counts := make([]int, n)
				for _, p := range points {
					counts[valueIndex(p.Coords[d], n)]++
				}
				for bin, count := range counts {
					if count != 1 {
						t.Errorf("k=%d n=%d dim %d: bin %d has %d points, want exactly 1",
							k, n, d, bin, count)
					}
				}
			}
		}
	}
}

func TestSobol_CoordinatesAreInUnitInterval(t *testing.T) {
	points, problem := sobolPoints(numericParams(sobolMaxDimensions, 0), 100)
	if problem != nil {
		t.Fatalf("sobolPoints() failed: %v", problem)
	}
	coordsInUnitInterval(t, points)
}

// The sequence is deterministic and unscrambled, so it starts at the origin —
// every parameter at its Min. Callers who need the corner excluded should use
// latin-hypercube instead.
func TestSobol_StartsAtTheOrigin(t *testing.T) {
	points, problem := sobolPoints(numericParams(3, 0), 4)
	if problem != nil {
		t.Fatalf("sobolPoints() failed: %v", problem)
	}

	for d, c := range points[0].Coords {
		if c != 0 {
			t.Errorf("first point dim %d = %v, want 0", d, c)
		}
	}

	// The second point halves every dimension, which is the first bisection of
	// the sequence.
	for d, c := range points[1].Coords {
		if c != 0.5 {
			t.Errorf("second point dim %d = %v, want 0.5", d, c)
		}
	}
}

// Points must be distinct: a repeated point would mean a duplicated run.
func TestSobol_PointsAreDistinct(t *testing.T) {
	const n = 64

	points, problem := sobolPoints(numericParams(3, 0), n)
	if problem != nil {
		t.Fatalf("sobolPoints() failed: %v", problem)
	}

	seen := make(map[[3]float64]int, n)
	for i, p := range points {
		key := [3]float64{p.Coords[0], p.Coords[1], p.Coords[2]}
		if first, dup := seen[key]; dup {
			t.Errorf("points %d and %d are identical: %v", first, i, p.Coords)
		}
		seen[key] = i
	}
}

// The sequence is reproducible without a seed, which is what makes a Sobol sweep
// resumable and comparable across runs.
func TestSobol_IsReproducible(t *testing.T) {
	a, _ := sobolPoints(numericParams(4, 0), 32)
	b, _ := sobolPoints(numericParams(4, 0), 32)

	if !samePoints(a, b) {
		t.Error("two runs of the same Sobol sequence differ")
	}
}

// A prefix of a longer run must match a shorter run exactly, since the sequence
// is defined by index rather than by sample count.
func TestSobol_PrefixIsStableAcrossSampleCounts(t *testing.T) {
	short, _ := sobolPoints(numericParams(3, 0), 8)
	long, _ := sobolPoints(numericParams(3, 0), 64)

	if !samePoints(short, long[:8]) {
		t.Error("the first 8 points differ between an 8-point and a 64-point run")
	}
}

func TestSobol_RejectsTooManyDimensions(t *testing.T) {
	_, problem := sobolPoints(numericParams(sobolMaxDimensions+1, 0), 8)

	if problem == nil {
		t.Fatal("expected more dimensions than the table covers to be rejected")
	}
	if problem.Code != CodeBadMethod {
		t.Errorf("code = %q, want %q", problem.Code, CodeBadMethod)
	}
}

// Generate must reject the same case, rather than only sobolPoints doing so.
func TestGenerate_SobolRejectsTooManyParameters(t *testing.T) {
	opts := Options{
		Template: models.JobSpec{JobName: "sweep", Command: "run"},
		Method:   MethodSobol,
		Samples:  8,
	}
	for i := 0; i <= sobolMaxDimensions; i++ {
		name := "p" + string(rune('a'+i))
		opts.Parameters = append(opts.Parameters, Parameter{Name: name, Min: 0, Max: 1})
		opts.Template.Command += " {{" + name + "}}"
	}

	result := Generate(opts)

	if result.OK() {
		t.Fatal("expected sobol beyond its dimension limit to be rejected")
	}
	if !hasCode(result.Errors, CodeBadMethod) {
		t.Errorf("errors = %v, want a %s", problemStrings(result.Errors), CodeBadMethod)
	}
}

// directionNumbers must produce odd-numbered leading bits: every direction
// number has its own bit set, which is what makes the generator matrices
// non-singular and the sequence full-period.
func TestDirectionNumbers_HaveTheirLeadingBitSet(t *testing.T) {
	for d, dim := range sobolDims {
		v := directionNumbers(dim)
		for j := 0; j < sobolBits; j++ {
			if v[j]&(1<<uint(sobolBits-1-j)) == 0 {
				t.Errorf("dim %d bit %d: direction number %#x is missing its leading bit", d, j, v[j])
			}
		}
	}
}

func TestLowestZeroBit(t *testing.T) {
	tests := []struct {
		x    uint32
		want int
	}{
		{0b0000, 0},
		{0b0001, 1},
		{0b0011, 2},
		{0b0111, 3},
		{0b1011, 2},
		{0b1110, 0},
	}

	for _, tt := range tests {
		if got := lowestZeroBit(tt.x); got != tt.want {
			t.Errorf("lowestZeroBit(%04b) = %d, want %d", tt.x, got, tt.want)
		}
	}
}
