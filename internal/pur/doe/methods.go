package doe

// MethodInfo describes a method well enough for a caller to build a form for it:
// which Options fields the method reads, and what it needs to be valid.
//
// This exists so the CLI help and the GUI method menu both describe a method the
// same way, and so a UI can grey out the fields a method ignores rather than
// offering a Samples box for a full factorial.
type MethodInfo struct {
	Method      Method
	Label       string
	Description string

	// UsesSamples reports whether the method reads Options.Samples, and hence
	// whether Samples must be set for it.
	UsesSamples bool

	// UsesLevels reports whether the method reads Parameter.Levels.
	UsesLevels bool

	// UsesCenterPoints reports whether the method reads Options.CenterPoints.
	UsesCenterPoints bool

	// UsesCases reports whether the method reads Options.Cases.
	UsesCases bool

	// MinParameters is the fewest parameters the method accepts.
	MinParameters int

	// MaxParameters is the most it accepts, or 0 when there is no limit.
	MaxParameters int
}

// Methods describes every supported method, in the same order as AllMethods.
func Methods() []MethodInfo {
	return []MethodInfo{
		{
			Method:        MethodFullFactorial,
			Label:         "Full factorial",
			Description:   "Every combination of every parameter's levels. Complete, but grows as the product of the level counts.",
			UsesLevels:    true,
			MinParameters: 1,
		},
		{
			Method:        MethodOFAT,
			Label:         "One factor at a time",
			Description:   "Vary one parameter at a time from a baseline. Cheap sensitivity check; sees no interactions.",
			UsesLevels:    true,
			MinParameters: 1,
		},
		{
			Method:        MethodLatinHypercube,
			Label:         "Latin hypercube",
			Description:   "A given number of points with every parameter evenly covered. The usual choice for a space-filling sweep.",
			UsesSamples:   true,
			MinParameters: 1,
		},
		{
			Method:        MethodSobol,
			Label:         "Sobol sequence",
			Description:   "Low-discrepancy points that cover the space more evenly than random sampling. Deterministic, so the seed has no effect.",
			UsesSamples:   true,
			MinParameters: 1,
			MaxParameters: sobolMaxDimensions,
		},
		{
			Method:        MethodMonteCarlo,
			Label:         "Monte Carlo",
			Description:   "Independent uniform random points. Simple, but clusters and leaves gaps at small sample counts.",
			UsesSamples:   true,
			MinParameters: 1,
		},
		{
			Method:           MethodCentralComposite,
			Label:            "Central composite",
			Description:      "Factorial corners, axial points and a center point, for fitting a quadratic response surface.",
			UsesCenterPoints: true,
			MinParameters:    1,
		},
		{
			Method:           MethodBoxBehnken,
			Label:            "Box-Behnken",
			Description:      "A quadratic design that avoids the extreme corners, so no case combines every parameter at its limit.",
			UsesCenterPoints: true,
			MinParameters:    3,
		},
		{
			Method:        MethodExplicit,
			Label:         "Explicit cases",
			Description:   "Your own list of cases, used verbatim. This is how a sweep loaded from a CSV is expressed.",
			UsesCases:     true,
			MinParameters: 1,
		},
	}
}

// MethodInfoFor returns the description of one method.
func MethodInfoFor(m Method) (MethodInfo, bool) {
	for _, info := range Methods() {
		if info.Method == m {
			return info, true
		}
	}
	return MethodInfo{}, false
}
