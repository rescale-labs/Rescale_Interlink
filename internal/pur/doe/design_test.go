package doe

import (
	"math"
	"testing"
)

// numericParams builds k continuous parameters over [0,1] with the given level
// count, for exercising the designs directly.
func numericParams(k, levels int) []Parameter {
	params := make([]Parameter, k)
	for i := range params {
		params[i] = Parameter{Name: "p" + string(rune('a'+i)), Min: 0, Max: 1, Levels: levels}
	}
	return params
}

// coordsInUnitInterval is the invariant every design must satisfy: a coordinate
// outside [0,1] would map to a value outside the range the caller gave.
func coordsInUnitInterval(t *testing.T, points []designPoint) {
	t.Helper()
	for i, p := range points {
		for d, c := range p.Coords {
			if c < 0 || c > 1 {
				t.Errorf("point %d dim %d coordinate %v is outside [0,1]", i, d, c)
			}
		}
	}
}

func TestFullFactorial_CountAndEndpoints(t *testing.T) {
	points := fullFactorial(numericParams(3, 3))

	if len(points) != 27 {
		t.Fatalf("got %d points, want 27", len(points))
	}
	coordsInUnitInterval(t, points)

	// Levels sit at the endpoints inclusive, so the first and last points are
	// opposite corners of the range.
	for d, c := range points[0].Coords {
		if c != 0 {
			t.Errorf("first point dim %d = %v, want 0", d, c)
		}
	}
	for d, c := range points[len(points)-1].Coords {
		if c != 1 {
			t.Errorf("last point dim %d = %v, want 1", d, c)
		}
	}
}

// The last parameter varies fastest, which is what makes generated cases read in
// a predictable order.
func TestFullFactorial_OdometerOrder(t *testing.T) {
	points := fullFactorial(numericParams(2, 2))

	want := [][]float64{{0, 0}, {0, 1}, {1, 0}, {1, 1}}
	for i, p := range points {
		for d := range want[i] {
			if p.Coords[d] != want[i][d] {
				t.Errorf("point %d = %v, want %v", i, p.Coords, want[i])
				break
			}
		}
	}
}

func TestFullFactorial_MixedLevelCounts(t *testing.T) {
	params := []Parameter{
		{Name: "a", Min: 0, Max: 1, Levels: 2},
		{Name: "b", Min: 0, Max: 1, Levels: 5},
		{Name: "c", Values: []string{"x", "y", "z"}},
	}

	points := fullFactorial(params)

	if len(points) != 2*5*3 {
		t.Errorf("got %d points, want %d", len(points), 2*5*3)
	}
	coordsInUnitInterval(t, points)
}

func TestOFAT_CountAndBaseline(t *testing.T) {
	// Three parameters at three levels: one baseline plus two off-baseline levels
	// each, so 1 + 3*2 = 7 rather than the 27 a full factorial would need.
	points := ofat(numericParams(3, 3))

	if len(points) != 7 {
		t.Fatalf("got %d points, want 7", len(points))
	}
	coordsInUnitInterval(t, points)

	// The first case is the baseline: every parameter at its middle level.
	for d, c := range points[0].Coords {
		if c != 0.5 {
			t.Errorf("baseline dim %d = %v, want 0.5", d, c)
		}
	}

	// Every later case differs from the baseline in exactly one dimension.
	for i, p := range points[1:] {
		changed := 0
		for _, c := range p.Coords {
			if c != 0.5 {
				changed++
			}
		}
		if changed != 1 {
			t.Errorf("point %d varies %d parameters, want exactly 1: %v", i+1, changed, p.Coords)
		}
	}
}

func TestOFAT_TwoLevelsUsesLowAsBaseline(t *testing.T) {
	points := ofat(numericParams(2, 2))

	// 1 + 2*(2-1) = 3.
	if len(points) != 3 {
		t.Fatalf("got %d points, want 3", len(points))
	}
	for d, c := range points[0].Coords {
		if c != 0 {
			t.Errorf("baseline dim %d = %v, want 0 for two levels", d, c)
		}
	}
}

// Latin hypercube's defining property: each parameter is split into n bins and
// every bin is used exactly once.
func TestLatinHypercube_StratifiesEveryDimension(t *testing.T) {
	const n = 16
	params := numericParams(4, 0)

	points := latinHypercube(params, n, 42)

	if len(points) != n {
		t.Fatalf("got %d points, want %d", len(points), n)
	}
	coordsInUnitInterval(t, points)

	for d := range params {
		counts := make([]int, n)
		for _, p := range points {
			counts[valueIndex(p.Coords[d], n)]++
		}
		for bin, count := range counts {
			if count != 1 {
				t.Errorf("dim %d bin %d has %d points, want exactly 1", d, bin, count)
			}
		}
	}
}

func TestLatinHypercube_IsDeterministicForASeed(t *testing.T) {
	params := numericParams(3, 0)

	a := latinHypercube(params, 8, 7)
	b := latinHypercube(params, 8, 7)
	c := latinHypercube(params, 8, 8)

	if !samePoints(a, b) {
		t.Error("the same seed produced different designs")
	}
	if samePoints(a, c) {
		t.Error("different seeds produced the same design")
	}
}

func TestMonteCarlo_CountAndRange(t *testing.T) {
	points := monteCarlo(numericParams(3, 0), 20, 1)

	if len(points) != 20 {
		t.Fatalf("got %d points, want 20", len(points))
	}
	coordsInUnitInterval(t, points)
}

func TestMonteCarlo_IsDeterministicForASeed(t *testing.T) {
	params := numericParams(2, 0)

	if !samePoints(monteCarlo(params, 10, 3), monteCarlo(params, 10, 3)) {
		t.Error("the same seed produced different draws")
	}
	if samePoints(monteCarlo(params, 10, 3), monteCarlo(params, 10, 4)) {
		t.Error("different seeds produced the same draws")
	}
}

// A central composite design is corners plus axial points plus center replicates,
// and Min/Max are the axial extremes so nothing leaves the caller's range.
func TestCentralComposite_Structure(t *testing.T) {
	const k = 3
	const centerPoints = 2

	points := centralComposite(numericParams(k, 0), centerPoints)

	want := (1 << k) + 2*k + centerPoints // 8 corners + 6 axial + 2 center
	if len(points) != want {
		t.Fatalf("got %d points, want %d", len(points), want)
	}
	coordsInUnitInterval(t, points)

	// The axial extremes reach the range endpoints exactly once per direction per
	// parameter, and the factorial corners sit strictly inside them.
	for d := 0; d < k; d++ {
		var atMin, atMax int
		for _, p := range points {
			if p.Coords[d] == 0 {
				atMin++
			}
			if p.Coords[d] == 1 {
				atMax++
			}
		}
		if atMin != 1 || atMax != 1 {
			t.Errorf("dim %d touches Min %d times and Max %d times, want once each", d, atMin, atMax)
		}
	}

	// Center replicates: exactly centerPoints points sit at the center.
	centers := 0
	for _, p := range points {
		isCenter := true
		for _, c := range p.Coords {
			if c != 0.5 {
				isCenter = false
				break
			}
		}
		if isCenter {
			centers++
		}
	}
	if centers != centerPoints {
		t.Errorf("got %d center points, want %d", centers, centerPoints)
	}
}

// Corners must be inward of the axial points, which is what keeps the design
// rotatable without exceeding the range.
func TestCentralComposite_CornersAreInsideAxialPoints(t *testing.T) {
	const k = 2
	alpha := math.Pow(2, float64(k)/4)
	wantCorner := 0.5 + 0.5/alpha

	points := centralComposite(numericParams(k, 0), 1)

	// The first 2^k points are the factorial corners.
	for i := 0; i < (1 << k); i++ {
		for d, c := range points[i].Coords {
			if math.Abs(c-wantCorner) > 1e-12 && math.Abs(c-(1-wantCorner)) > 1e-12 {
				t.Errorf("corner %d dim %d = %v, want +/-1 coded (%v or %v)", i, d, c, 1-wantCorner, wantCorner)
			}
		}
	}
}

// Box-Behnken's reason for existing: it never places a point at a corner of the
// space, where the extreme combination may be physically invalid.
func TestBoxBehnken_Structure(t *testing.T) {
	const k = 3
	const centerPoints = 3

	points := boxBehnken(numericParams(k, 0), centerPoints)

	want := 4*(k*(k-1)/2) + centerPoints // 12 pairwise + 3 center = the classical 15-run k=3 design
	if len(points) != want {
		t.Fatalf("got %d points, want %d", len(points), want)
	}
	coordsInUnitInterval(t, points)

	for i, p := range points {
		extremes := 0
		for _, c := range p.Coords {
			if c == 0 || c == 1 {
				extremes++
			}
		}
		if extremes == k {
			t.Errorf("point %d is a corner of the space (%v), which Box-Behnken must avoid", i, p.Coords)
		}
		if extremes != 0 && extremes != 2 {
			t.Errorf("point %d has %d extreme coordinates, want 0 or 2: %v", i, extremes, p.Coords)
		}
	}
}

func TestBoxBehnken_CountsForClassicalSizes(t *testing.T) {
	tests := []struct{ k, centerPoints, want int }{
		{3, 3, 15},
		{4, 3, 27},
		{5, 6, 46},
	}

	for _, tt := range tests {
		points := boxBehnken(numericParams(tt.k, 0), tt.centerPoints)
		if len(points) != tt.want {
			t.Errorf("k=%d: got %d points, want %d", tt.k, len(points), tt.want)
		}
	}
}

func TestBoxBehnken_NeedsThreeParameters(t *testing.T) {
	opts := twoLevelOptions()
	opts.Method = MethodBoxBehnken

	result := Generate(opts)

	if !hasCode(result.Errors, CodeTooFewParams) {
		t.Errorf("errors = %v, want a %s", problemStrings(result.Errors), CodeTooFewParams)
	}
}

func TestExplicitPoints_CarriesValuesNotCoords(t *testing.T) {
	points := explicitPoints([]map[string]string{{"a": "1"}, {"a": "2"}})

	if len(points) != 2 {
		t.Fatalf("got %d points, want 2", len(points))
	}
	for i, p := range points {
		if p.Coords != nil {
			t.Errorf("point %d has coordinates, want values only", i)
		}
		if p.Values == nil {
			t.Errorf("point %d has no values", i)
		}
	}
}

func TestEffectiveLevels(t *testing.T) {
	tests := []struct {
		name  string
		param Parameter
		want  int
	}{
		{"explicit levels", Parameter{Levels: 5}, 5},
		{"unset levels default to the endpoints", Parameter{}, 2},
		{"levels below two default to the endpoints", Parameter{Levels: 1}, 2},
		{"categorical uses its value count", Parameter{Values: []string{"a", "b", "c"}}, 3},
		{"categorical ignores levels", Parameter{Values: []string{"a", "b"}, Levels: 9}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveLevels(tt.param); got != tt.want {
				t.Errorf("effectiveLevels() = %d, want %d", got, tt.want)
			}
		})
	}
}

// Grid coordinates for a categorical parameter must round-trip back to the level
// they came from, or a full factorial would visit some values twice and others
// never.
func TestGridCoord_CategoricalRoundTrips(t *testing.T) {
	for n := 1; n <= 12; n++ {
		p := Parameter{Values: make([]string, n)}
		for i := 0; i < n; i++ {
			coord := gridCoord(p, i, n)
			if got := valueIndex(coord, n); got != i {
				t.Errorf("n=%d level %d: coord %v read back as %d", n, i, coord, got)
			}
		}
	}
}

func TestValueIndex_ClampsOutOfRangeCoordinates(t *testing.T) {
	if got := valueIndex(-0.5, 3); got != 0 {
		t.Errorf("valueIndex(-0.5, 3) = %d, want 0", got)
	}
	if got := valueIndex(1.0, 3); got != 2 {
		t.Errorf("valueIndex(1.0, 3) = %d, want 2", got)
	}
	if got := valueIndex(1.5, 3); got != 2 {
		t.Errorf("valueIndex(1.5, 3) = %d, want 2", got)
	}
	if got := valueIndex(0.5, 1); got != 0 {
		t.Errorf("valueIndex(0.5, 1) = %d, want 0", got)
	}
}

// The projection has to agree with the design it predicts, since it is what
// rejects an oversized sweep before the design is built.
func TestEstimateCaseCount_MatchesTheGeneratedDesign(t *testing.T) {
	tests := []struct {
		name string
		opts Options
	}{
		{"full factorial", Options{Method: MethodFullFactorial, Parameters: numericParams(3, 3)}},
		{"full factorial mixed", Options{Method: MethodFullFactorial, Parameters: []Parameter{
			{Name: "a", Levels: 2}, {Name: "b", Levels: 5}, {Name: "c", Values: []string{"x", "y", "z"}},
		}}},
		{"ofat", Options{Method: MethodOFAT, Parameters: numericParams(4, 3)}},
		{"latin hypercube", Options{Method: MethodLatinHypercube, Parameters: numericParams(3, 0), Samples: 12}},
		{"sobol", Options{Method: MethodSobol, Parameters: numericParams(3, 0), Samples: 12}},
		{"monte carlo", Options{Method: MethodMonteCarlo, Parameters: numericParams(3, 0), Samples: 12}},
		{"central composite", Options{Method: MethodCentralComposite, Parameters: numericParams(3, 0), CenterPoints: 2}},
		{"box behnken", Options{Method: MethodBoxBehnken, Parameters: numericParams(4, 0), CenterPoints: 3}},
		{"explicit", Options{Method: MethodExplicit, Parameters: numericParams(1, 0),
			Cases: []map[string]string{{"pa": "1"}, {"pa": "2"}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := withDefaults(tt.opts)

			points, problem := samplePoints(opts)
			if problem != nil {
				t.Fatalf("samplePoints() failed: %v", problem)
			}

			if got := estimateCaseCount(opts); got != len(points) {
				t.Errorf("estimateCaseCount() = %d, but the design has %d points", got, len(points))
			}
		})
	}
}

func TestSamplePoints_RejectsUnknownMethod(t *testing.T) {
	_, problem := samplePoints(Options{Method: "nope"})

	if problem == nil {
		t.Fatal("expected an unknown method to be rejected")
	}
	if problem.Code != CodeBadMethod {
		t.Errorf("code = %q, want %q", problem.Code, CodeBadMethod)
	}
}

// samePoints reports whether two designs are identical.
func samePoints(a, b []designPoint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i].Coords) != len(b[i].Coords) {
			return false
		}
		for d := range a[i].Coords {
			if a[i].Coords[d] != b[i].Coords[d] {
				return false
			}
		}
	}
	return true
}
