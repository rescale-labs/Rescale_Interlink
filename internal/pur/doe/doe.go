// Package doe generates parameter-sweep job specs from a single base job.
//
// A sweep takes one template JobSpec whose Command carries {{name}} placeholders,
// a set of swept Parameters, and a sampling Method, then emits one JobSpec per
// design point with that point's values rendered into the command line. Values
// go in the command rather than into environment variables so each case's
// configuration is visible on its Rescale job page.
//
// The result is a plain []models.JobSpec, which is the existing ingress to the
// PUR pipeline. That means a sweep inherits tar/upload/create/submit, resume,
// state tracking and progress reporting without any pipeline changes.
//
// Generate is pure: it performs no I/O and makes no API calls, so callers can
// preview a sweep before committing to it. It mirrors filescan.ScanFiles in
// shape — one options struct in, one result struct out, errors reported in the
// result rather than returned.
package doe

import (
	"github.com/rescale/rescale-int/internal/models"
)

// DefaultMaxCases bounds a sweep unless the caller raises it. Full factorial
// designs grow as the product of level counts, so an unbounded sweep can ask
// for millions of jobs from a handful of parameters.
const DefaultMaxCases = 1000

// Method selects the sampling strategy.
type Method string

const (
	// MethodFullFactorial takes every combination of every parameter's levels.
	MethodFullFactorial Method = "full-factorial"

	// MethodOFAT (one factor at a time) varies each parameter across its levels
	// while holding the others at their baseline.
	MethodOFAT Method = "ofat"

	// MethodLatinHypercube stratifies each parameter into Samples bins and draws
	// one value from each, permuting dimensions independently.
	MethodLatinHypercube Method = "latin-hypercube"

	// MethodSobol draws a low-discrepancy Sobol sequence, which covers the space
	// more evenly than random sampling at the same Samples count.
	MethodSobol Method = "sobol"

	// MethodMonteCarlo draws Samples independent uniform values per parameter.
	MethodMonteCarlo Method = "monte-carlo"

	// MethodCentralComposite is a central composite design: factorial corners,
	// axial points and a center point, for fitting quadratic response surfaces.
	MethodCentralComposite Method = "central-composite"

	// MethodBoxBehnken is a Box-Behnken design: a quadratic design that avoids
	// the extreme corners of the space. Requires at least three parameters.
	MethodBoxBehnken Method = "box-behnken"

	// MethodExplicit uses the caller's own list of cases verbatim, which is how
	// a sweep loaded from a CSV is expressed.
	MethodExplicit Method = "explicit"
)

// AllMethods lists every supported method, for CLI help and GUI menus.
func AllMethods() []Method {
	return []Method{
		MethodFullFactorial,
		MethodOFAT,
		MethodLatinHypercube,
		MethodSobol,
		MethodMonteCarlo,
		MethodCentralComposite,
		MethodBoxBehnken,
		MethodExplicit,
	}
}

// Parameter is one swept input. Name must match a {{Name}} token in the
// template command.
//
// A parameter is either continuous (Min/Max, with Levels used by the designs
// that need discrete levels) or categorical (Values). Setting Values makes the
// parameter categorical and Min/Max/Levels are then ignored.
type Parameter struct {
	Name string

	// Continuous range.
	Min float64
	Max float64

	// Levels is how many evenly spaced values to take from [Min, Max] for the
	// designs that sample on a grid. Ignored for categorical parameters and for
	// the continuous samplers, which draw from the range directly.
	Levels int

	// Values makes this parameter categorical: these strings are used verbatim.
	Values []string

	// Format is the fmt verb used to render numeric values, defaulting to "%g".
	// Set it for fixed-width output, e.g. "%.3f".
	Format string
}

// Options configures a sweep.
type Options struct {
	// Template is the base job every case is derived from. Its Command must
	// contain a {{name}} token for each declared parameter.
	Template models.JobSpec

	// Parameters are the swept inputs, in the order designs should treat them.
	Parameters []Parameter

	// Method selects the sampling strategy. Required.
	Method Method

	// Samples is the number of design points for the continuous samplers
	// (latin-hypercube, sobol, monte-carlo). Ignored by the others, which derive
	// their count from the design.
	Samples int

	// Seed makes the randomized samplers reproducible. The zero value is a valid
	// seed and still deterministic; change it to draw a different sample.
	Seed uint64

	// Cases supplies the design points for MethodExplicit: one map of parameter
	// name to rendered value per case. Ignored by every other method.
	Cases []map[string]string

	// CenterPoints is how many times the center point is repeated in the
	// central-composite and Box-Behnken designs. Zero means one center point.
	CenterPoints int

	// BaseFileIDs are already-uploaded Rescale file IDs shared by every case.
	//
	// When set, cases carry no Directory and reference these IDs directly. The
	// pipeline's skip path then sends each case straight to job creation, so the
	// shared input deck is transferred once rather than once per case — the whole
	// point of sweeping parameters over one unchanged set of inputs.
	//
	// When empty, every case keeps Template.Directory, and the pipeline tars and
	// uploads that directory once per case.
	BaseFileIDs []string

	// JobNameTemplate names each case. It may use parameter tokens plus the
	// built-in {{__index}} (1-based case number) and {{__base}} (the template
	// job name with any trailing _<n> removed).
	//
	// Empty means "{{__base}}_{{__index}}", matching how PUR's directory scan
	// names iterated jobs.
	JobNameTemplate string

	// TagTemplates are rendered per case and added to the template's tags, so a
	// sweep can label jobs with their own parameter values. Each entry may use
	// the same tokens as JobNameTemplate, e.g. "alpha={{alpha}}".
	TagTemplates []string

	// MaxCases caps the sweep. Zero means DefaultMaxCases. Negative means no cap.
	MaxCases int
}

// Case is one generated design point.
type Case struct {
	// Index is the 1-based case number, matching the job name suffix.
	Index int

	// Values maps parameter name to the rendered value used for this case.
	Values map[string]string

	// JobName and Command are the rendered results for this case.
	JobName string
	Command string

	// Tags are the template's tags plus this case's rendered tags.
	Tags []string
}

// Result is the outcome of Generate.
type Result struct {
	// Cases and Jobs are parallel: Jobs[i] is the JobSpec for Cases[i]. Both are
	// empty when Errors is non-empty.
	Cases []Case
	Jobs  []models.JobSpec

	// Errors are fatal: nothing is generated while any remain. Warnings are
	// advisory and do not block generation.
	Errors   []Problem
	Warnings []Problem
}

// OK reports whether generation succeeded.
func (r Result) OK() bool { return len(r.Errors) == 0 }

// Generate builds the sweep described by opts.
//
// Validation runs first and is bidirectional over parameters and command
// tokens: a declared parameter with no matching token, and a token with no
// matching parameter, are both fatal. Nothing is generated unless every check
// passes, so a caller never gets a partially valid sweep.
func Generate(opts Options) Result {
	opts = withDefaults(opts)

	result := Result{}

	// Pre-flight checks on the options themselves, including the bidirectional
	// parameter/token comparison and the projected case count.
	problems, warnings := validateOptions(opts)
	result.Errors = append(result.Errors, problems...)
	result.Warnings = append(result.Warnings, warnings...)
	if len(result.Errors) > 0 {
		return result
	}

	// Check the projected size before building the design. A full factorial grows
	// as the product of its level counts, so an oversized sweep has to be caught
	// by arithmetic rather than by allocating it first.
	if problem := checkCaseCount(opts, estimateCaseCount(opts)); problem != nil {
		result.Errors = append(result.Errors, *problem)
		return result
	}

	points, err := samplePoints(opts)
	if err != nil {
		result.Errors = append(result.Errors, *err)
		return result
	}

	// Belt and braces: the projection above should already agree with the design.
	if problem := checkCaseCount(opts, len(points)); problem != nil {
		result.Errors = append(result.Errors, *problem)
		return result
	}

	cases, jobs, problems, warnings := renderCases(opts, points)
	result.Errors = append(result.Errors, problems...)
	result.Warnings = append(result.Warnings, warnings...)
	if len(result.Errors) > 0 {
		return result
	}

	result.Cases = cases
	result.Jobs = jobs
	return result
}

// withDefaults fills in the zero values that have a meaningful default, so the
// rest of the package can read opts without repeating the fallbacks.
func withDefaults(opts Options) Options {
	if opts.MaxCases == 0 {
		opts.MaxCases = DefaultMaxCases
	}
	if opts.CenterPoints <= 0 {
		opts.CenterPoints = 1
	}
	if opts.JobNameTemplate == "" {
		opts.JobNameTemplate = "{{__base}}_{{__index}}"
	}
	return opts
}
