package doe

// Sobol low-discrepancy sequence.
//
// A Sobol sequence fills the unit cube far more evenly than independent random
// draws at the same sample count: for the first 2^m points, every parameter is
// split into 2^m equal intervals with exactly one point in each. That property
// is what makes it worth the direction-number table over plain Monte Carlo, and
// sobol_test.go asserts it directly, since a wrong direction number shows up as
// a stratification failure rather than as visibly bad-looking output.
//
// The sequence is deterministic and unscrambled, so Options.Seed does not affect
// it. Point 0 is the origin, which lands every parameter at its Min.

// sobolBits is the fixed-point width of the generator. 32 bits gives a
// resolution of 2^-32 per parameter, far finer than any sweep needs.
const sobolBits = 32

// sobolMaxDimensions is how many parameters the direction-number table covers.
const sobolMaxDimensions = 6

// sobolDim is one dimension's generating polynomial and initial direction
// numbers.
//
// degree is the degree of the primitive polynomial over GF(2). poly encodes its
// interior coefficients a_1..a_(degree-1), least significant bit holding
// a_(degree-1); the leading and trailing coefficients are always 1 and are
// implied. m holds the initial direction numbers, one per bit up to degree, each
// an odd integer less than 2^bit.
//
// Table values are the classical Bratley-Fox / Numerical Recipes set for the
// first six dimensions.
type sobolDim struct {
	degree int
	poly   uint32
	m      []uint32
}

var sobolDims = [sobolMaxDimensions]sobolDim{
	{degree: 1, poly: 0, m: []uint32{1}},
	{degree: 2, poly: 1, m: []uint32{1, 1}},
	{degree: 3, poly: 1, m: []uint32{1, 3, 7}},
	{degree: 3, poly: 2, m: []uint32{1, 3, 3}},
	{degree: 4, poly: 1, m: []uint32{1, 1, 3, 13}},
	{degree: 4, poly: 4, m: []uint32{1, 1, 5, 9}},
}

// sobolPoints generates n Sobol points in len(params) dimensions. Validation
// rejects more dimensions than sobolDims covers before generation is reached.
func sobolPoints(params []Parameter, n int) []designPoint {
	gen := newSobolGenerator(len(params))

	points := make([]designPoint, n)
	for i := range points {
		points[i] = designPoint{Coords: gen.next()}
	}
	return points
}

// sobolGenerator walks the sequence in Gray-code order, which lets each point be
// derived from the previous one with a single XOR per dimension.
type sobolGenerator struct {
	// v[d][j] is dimension d's direction number for bit j, pre-shifted into
	// fixed point.
	v [][]uint32

	// state[d] is the current fixed-point value for dimension d.
	state []uint32

	// index counts points already emitted, so the first call returns point 0.
	index uint32
}

func newSobolGenerator(k int) *sobolGenerator {
	g := &sobolGenerator{
		v:     make([][]uint32, k),
		state: make([]uint32, k),
	}
	for d := 0; d < k; d++ {
		g.v[d] = directionNumbers(sobolDims[d])
	}
	return g
}

// directionNumbers expands one dimension's initial values into a full set of
// sobolBits pre-shifted direction numbers.
//
// The recurrence in fixed point is
//
//	v_j = v_(j-s) ^ (v_(j-s) >> s) ^ (selected v_(j-l) for l in 1..s-1)
//
// where s is the polynomial degree and the selection is by the polynomial's
// interior coefficient bits.
func directionNumbers(dim sobolDim) []uint32 {
	v := make([]uint32, sobolBits)
	s := dim.degree

	// Seed values: m_j scaled so bit j of the fraction is m_j's low bit.
	for j := 0; j < s && j < sobolBits; j++ {
		v[j] = dim.m[j] << uint(sobolBits-1-j)
	}

	for j := s; j < sobolBits; j++ {
		prev := v[j-s]
		next := prev ^ (prev >> uint(s))

		coeffs := dim.poly
		for l := s - 1; l >= 1; l-- {
			if coeffs&1 != 0 {
				next ^= v[j-l]
			}
			coeffs >>= 1
		}

		v[j] = next
	}

	return v
}

// next returns the next point of the sequence as unit coordinates.
func (g *sobolGenerator) next() []float64 {
	coords := make([]float64, len(g.state))

	// Point 0 is the untouched zero state; every later point flips the direction
	// number for the position of the lowest zero bit of the previous index.
	if g.index > 0 {
		bit := lowestZeroBit(g.index - 1)
		for d := range g.state {
			g.state[d] ^= g.v[d][bit]
		}
	}
	g.index++

	for d, s := range g.state {
		coords[d] = float64(s) / (1 << sobolBits)
	}
	return coords
}

// lowestZeroBit returns the position of the least significant zero bit of x.
func lowestZeroBit(x uint32) int {
	bit := 0
	for x&1 != 0 && bit < sobolBits-1 {
		x >>= 1
		bit++
	}
	return bit
}
