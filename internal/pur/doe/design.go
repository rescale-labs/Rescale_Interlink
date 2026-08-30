package doe

import (
	"math"
	"math/rand/v2"
)

// designPoint is one point of the design.
//
// The sampling methods work in unit coordinates: Coords holds one value in
// [0,1] per parameter, positionally matching Options.Parameters, and render.go
// maps each coordinate onto that parameter's domain. Keeping the designs
// dimensionless means a design is written once and works for both continuous
// and categorical parameters.
//
// MethodExplicit is the exception: the caller already has the values, so it
// fills Values and leaves Coords nil.
type designPoint struct {
	Coords []float64
	Values map[string]string
}

// absoluteMaxCases is the hard ceiling on a sweep, applied whatever MaxCases
// says. Every case carries a rendered command, a values map and a JobSpec, so
// the ceiling sits where the whole design still fits comfortably in memory while
// staying far above any sweep an account would actually submit.
const absoluteMaxCases = 100_000

// overflowProjection is what estimateCaseCount returns in place of a count it
// cannot compute exactly. It is one past the ceiling, so a single comparison
// rejects both an oversized design and an uncountable one.
const overflowProjection = absoluteMaxCases + 1

// maxParameters bounds the design's dimensionality independently of the case
// count. Every design allocates one coordinate per parameter per point, and the
// per-method count projections below stay exact only while the dimension is
// small; a sweep over more inputs than this is a mistake rather than a design.
const maxParameters = 128

// samplePoints builds the design points for opts.Method. It assumes opts has
// already passed validateOptions.
func samplePoints(opts Options) ([]designPoint, *Problem) {
	switch opts.Method {
	case MethodFullFactorial:
		return fullFactorial(opts.Parameters), nil
	case MethodOFAT:
		return ofat(opts.Parameters), nil
	case MethodLatinHypercube:
		return latinHypercube(opts.Parameters, opts.Samples, opts.Seed), nil
	case MethodSobol:
		return sobolPoints(opts.Parameters, opts.Samples), nil
	case MethodMonteCarlo:
		return monteCarlo(opts.Parameters, opts.Samples, opts.Seed), nil
	case MethodCentralComposite:
		return centralComposite(opts.Parameters, opts.CenterPoints), nil
	case MethodBoxBehnken:
		return boxBehnken(opts.Parameters, opts.CenterPoints), nil
	case MethodExplicit:
		return explicitPoints(opts.Cases), nil
	}

	return nil, &Problem{
		Code:    CodeBadMethod,
		Message: sprintf("unsupported method %q", opts.Method),
	}
}

// fullFactorial takes every combination of every parameter's levels. The last
// parameter varies fastest, so cases read in odometer order.
func fullFactorial(params []Parameter) []designPoint {
	levels := make([]int, len(params))
	total := 1
	for i, p := range params {
		levels[i] = effectiveLevels(p)
		total *= levels[i]
	}

	points := make([]designPoint, 0, total)
	idx := make([]int, len(params))

	for {
		coords := make([]float64, len(params))
		for i, p := range params {
			coords[i] = gridCoord(p, idx[i], levels[i])
		}
		points = append(points, designPoint{Coords: coords})

		// Odometer increment, rightmost digit first.
		pos := len(idx) - 1
		for pos >= 0 {
			idx[pos]++
			if idx[pos] < levels[pos] {
				break
			}
			idx[pos] = 0
			pos--
		}
		if pos < 0 {
			break
		}
	}

	return points
}

// ofat varies one factor at a time: a single baseline case with every parameter
// at its baseline level, then one case per off-baseline level of each parameter
// with the others held at baseline. Total cases are 1 + sum(levels-1), which
// grows linearly rather than multiplicatively.
//
// The baseline is the middle level of each parameter, so the sweep brackets the
// baseline rather than only walking upward from the minimum.
func ofat(params []Parameter) []designPoint {
	levels := make([]int, len(params))
	baseline := make([]int, len(params))
	for i, p := range params {
		levels[i] = effectiveLevels(p)
		baseline[i] = (levels[i] - 1) / 2
	}

	baselineCoords := func() []float64 {
		coords := make([]float64, len(params))
		for i, p := range params {
			coords[i] = gridCoord(p, baseline[i], levels[i])
		}
		return coords
	}

	points := []designPoint{{Coords: baselineCoords()}}

	for i, p := range params {
		for level := 0; level < levels[i]; level++ {
			if level == baseline[i] {
				continue
			}
			coords := baselineCoords()
			coords[i] = gridCoord(p, level, levels[i])
			points = append(points, designPoint{Coords: coords})
		}
	}

	return points
}

// latinHypercube splits each parameter into n equal bins and draws one value
// from each, permuting the bin order independently per parameter. Every
// parameter is therefore evenly covered no matter how many parameters there are,
// which is what makes it usable where a full factorial is not.
func latinHypercube(params []Parameter, n int, seed uint64) []designPoint {
	rng := newRNG(seed)

	// bins[i] is the bin assigned to sample i in dimension d.
	coords := make([][]float64, n)
	for i := range coords {
		coords[i] = make([]float64, len(params))
	}

	for d := range params {
		perm := rng.Perm(n)
		for i := 0; i < n; i++ {
			coords[i][d] = (float64(perm[i]) + rng.Float64()) / float64(n)
		}
	}

	points := make([]designPoint, n)
	for i := range points {
		points[i] = designPoint{Coords: coords[i]}
	}
	return points
}

// monteCarlo draws n independent uniform values per parameter.
func monteCarlo(params []Parameter, n int, seed uint64) []designPoint {
	rng := newRNG(seed)

	points := make([]designPoint, n)
	for i := range points {
		coords := make([]float64, len(params))
		for d := range params {
			coords[d] = rng.Float64()
		}
		points[i] = designPoint{Coords: coords}
	}
	return points
}

// centralComposite builds a central composite design: 2^k factorial corners,
// 2k axial points and CenterPoints repeats of the center.
//
// The coded design uses corners at +/-1 and axial points at +/-alpha with
// alpha = 2^(k/4), which makes the design rotatable. Min and Max are mapped to
// the *axial* extremes, so the factorial corners sit inward at +/-1/alpha and no
// point falls outside the range the caller gave.
func centralComposite(params []Parameter, centerPoints int) []designPoint {
	k := len(params)
	alpha := math.Pow(2, float64(k)/4)

	// coded maps a coded level in [-alpha, +alpha] to a unit coordinate.
	coded := func(c float64) float64 {
		return 0.5 + 0.5*c/alpha
	}

	corners := 1 << uint(k)
	points := make([]designPoint, 0, corners+2*k+centerPoints)

	// Factorial corners: bit d of mask selects the high level of parameter d.
	for mask := 0; mask < corners; mask++ {
		coords := make([]float64, k)
		for d := 0; d < k; d++ {
			if mask&(1<<uint(d)) != 0 {
				coords[d] = coded(1)
			} else {
				coords[d] = coded(-1)
			}
		}
		points = append(points, designPoint{Coords: coords})
	}

	// Axial points: one parameter at an extreme, the rest at center.
	for d := 0; d < k; d++ {
		for _, c := range []float64{-alpha, alpha} {
			coords := make([]float64, k)
			for j := range coords {
				coords[j] = 0.5
			}
			coords[d] = coded(c)
			points = append(points, designPoint{Coords: coords})
		}
	}

	points = append(points, centerReplicates(k, centerPoints)...)
	return points
}

// boxBehnken builds a Box-Behnken design: for every pair of parameters, the four
// combinations of their high and low levels with all other parameters at center,
// plus CenterPoints repeats of the center.
//
// Unlike a central composite design this never places a point at a corner of the
// space, which matters when the extreme combinations are physically invalid or
// simply will not converge.
//
// The pairwise construction is exactly the classical design for 3, 4 and 5
// parameters. For 6 or more it remains a valid three-level quadratic design but
// is larger than the classical balanced-incomplete-block construction, so
// prefer central-composite there if run count matters.
func boxBehnken(params []Parameter, centerPoints int) []designPoint {
	k := len(params)

	pairs := k * (k - 1) / 2
	points := make([]designPoint, 0, 4*pairs+centerPoints)

	for i := 0; i < k; i++ {
		for j := i + 1; j < k; j++ {
			for _, ci := range []float64{0, 1} {
				for _, cj := range []float64{0, 1} {
					coords := make([]float64, k)
					for d := range coords {
						coords[d] = 0.5
					}
					coords[i] = ci
					coords[j] = cj
					points = append(points, designPoint{Coords: coords})
				}
			}
		}
	}

	points = append(points, centerReplicates(k, centerPoints)...)
	return points
}

// centerReplicates returns n copies of the center point, used by the quadratic
// designs to estimate pure error.
func centerReplicates(k, n int) []designPoint {
	points := make([]designPoint, 0, n)
	for i := 0; i < n; i++ {
		coords := make([]float64, k)
		for d := range coords {
			coords[d] = 0.5
		}
		points = append(points, designPoint{Coords: coords})
	}
	return points
}

// explicitPoints passes the caller's cases through unchanged. Values are copied
// so a later mutation of the caller's maps cannot change a generated case.
func explicitPoints(cases []map[string]string) []designPoint {
	points := make([]designPoint, len(cases))
	for i, c := range cases {
		values := make(map[string]string, len(c))
		for name, value := range c {
			values[name] = value
		}
		points[i] = designPoint{Values: values}
	}
	return points
}

// newRNG returns a deterministic generator for seed.
//
// This is PCG rather than crypto/rand on purpose: a sweep must be reproducible
// from its seed, and nothing here is security-relevant.
func newRNG(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, 0))
}

// effectiveLevels is how many discrete levels a parameter contributes to a grid
// design. Categorical parameters have one level per value; numeric parameters
// use Levels, defaulting to the two range endpoints.
func effectiveLevels(p Parameter) int {
	if p.Values != nil {
		if len(p.Values) < 1 {
			return 1
		}
		return len(p.Values)
	}
	if p.Levels >= 2 {
		return p.Levels
	}
	return 2
}

// gridCoord is the unit coordinate of level index i out of n for parameter p.
//
// Numeric parameters place levels at the endpoints inclusive, so level 0 is
// exactly Min and level n-1 exactly Max. Categorical parameters place levels at
// bin centers, which is what valueIndex reads back and what lets the continuous
// samplers hit every value with equal probability.
func gridCoord(p Parameter, i, n int) float64 {
	if n <= 1 {
		return 0
	}
	if p.Values != nil {
		return (float64(i) + 0.5) / float64(n)
	}
	return float64(i) / float64(n-1)
}

// valueIndex maps a unit coordinate to one of n categorical bins.
func valueIndex(coord float64, n int) int {
	if n <= 1 {
		return 0
	}
	idx := int(math.Floor(coord * float64(n)))
	if idx < 0 {
		return 0
	}
	if idx >= n {
		return n - 1
	}
	return idx
}

// estimateCaseCount projects how many cases a design will produce, without
// building it. Used to reject an oversized sweep before it is materialized and
// to size the tag-volume warning.
//
// Every arithmetic step is guarded before it runs rather than after: a product
// or sum that wraps past the int range comes back negative, and a negative count
// passes every ceiling test there is on its way to a negative allocation.
func estimateCaseCount(opts Options) int {
	k := len(opts.Parameters)
	if k > maxParameters {
		return overflowProjection
	}

	switch opts.Method {
	case MethodFullFactorial:
		count := 1
		for _, p := range opts.Parameters {
			levels := effectiveLevels(p)
			if levels <= 0 || count > overflowProjection/levels {
				return overflowProjection
			}
			count *= levels
		}
		return count

	case MethodOFAT:
		count := 1
		for _, p := range opts.Parameters {
			levels := effectiveLevels(p)
			if levels <= 0 || count > overflowProjection-levels {
				return overflowProjection
			}
			count += levels - 1
		}
		return count

	case MethodLatinHypercube, MethodSobol, MethodMonteCarlo:
		if opts.Samples < 0 {
			return 0
		}
		return opts.Samples

	case MethodCentralComposite:
		// 2^k corners: exact only while k is well inside the int width.
		if k >= 30 {
			return overflowProjection
		}
		count := (1 << uint(k)) + 2*k
		if opts.CenterPoints > overflowProjection-count {
			return overflowProjection
		}
		return count + opts.CenterPoints

	case MethodBoxBehnken:
		count := 4 * (k * (k - 1) / 2)
		if opts.CenterPoints > overflowProjection-count {
			return overflowProjection
		}
		return count + opts.CenterPoints

	case MethodExplicit:
		return len(opts.Cases)
	}

	return 0
}

// checkCaseCount enforces the absolute ceiling and then MaxCases. Returns nil
// when count is acceptable.
func checkCaseCount(opts Options, count int) *Problem {
	if count > absoluteMaxCases {
		return &Problem{
			Code: CodeTooManyCases,
			Message: sprintf("design would produce more than %d cases, which is beyond what could be "+
				"submitted as one sweep; reduce the number of parameters, levels or samples", absoluteMaxCases),
		}
	}

	if count > opts.MaxCases {
		return &Problem{
			Code: CodeTooManyCases,
			Message: sprintf("design produces %d cases, which exceeds the limit of %d; "+
				"reduce levels or samples, or raise the limit", count, opts.MaxCases),
		}
	}

	return nil
}
