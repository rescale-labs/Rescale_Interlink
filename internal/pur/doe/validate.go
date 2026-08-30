package doe

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/rescale/rescale-int/internal/pur/pattern"
)

// sprintf is a local alias, so the design files can build problem messages
// without each importing fmt for a single call.
func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// Problem codes. Stable strings so the CLI and GUI can branch on them and tests
// can assert on them without matching prose.
const (
	CodeNoCommand       = "no_command"
	CodeNoParameters    = "no_parameters"
	CodeBadMethod       = "bad_method"
	CodeBadSamples      = "bad_samples"
	CodeBadParamName    = "bad_param_name"
	CodeDuplicateParam  = "duplicate_param"
	CodeCaseCollision   = "case_collision"
	CodeBadRange        = "bad_range"
	CodeBadLevels       = "bad_levels"
	CodeNoValues        = "no_values"
	CodeMissingToken    = "missing_token"
	CodeUndeclaredToken = "undeclared_token"
	CodeResidualToken   = "residual_token"
	CodeUnsafeValue     = "unsafe_value"
	CodeEmptyValue      = "empty_value"
	CodeValueHasSpace   = "value_has_space"
	CodeTooManyCases    = "too_many_cases"
	CodeTooFewParams    = "too_few_params"
	CodeNoCases         = "no_cases"
	CodeBadTag          = "bad_tag"
	CodeTagCallVolume   = "tag_call_volume"
	CodeDupJobName      = "dup_job_name"
	CodeEmptyJobName    = "empty_job_name"
	CodeSingleLevel     = "single_level"
	CodeDuplicateCase   = "duplicate_case"
	CodeBadFormat       = "bad_format"
	CodeBadMaxCases     = "bad_max_cases"
	CodeTooManyParams   = "too_many_params"
	CodeReservedParam   = "reserved_param"
	CodeDuplicateValue  = "duplicate_value"
	CodeTooLong         = "too_long"
)

// Problem is one validation finding. Errors block generation; warnings do not.
type Problem struct {
	Code    string // One of the Code* constants
	Param   string // Parameter or token the finding concerns, when applicable
	Message string
}

func (p Problem) Error() string {
	if p.Param != "" {
		return fmt.Sprintf("%s (%s): %s", p.Code, p.Param, p.Message)
	}
	return fmt.Sprintf("%s: %s", p.Code, p.Message)
}

// unsafeValueChars are characters that would change the structure of a rendered
// command rather than just supply a value: redirection, command separators,
// substitution and quoting. A parameter value containing one of these is
// rejected, since a sweep value is meant to be a datum, not syntax.
const unsafeValueChars = "`$;|&><\n\r\"'\\"

// globValueChars are characters a shell expands against the filesystem or the
// argument list. Numeric output never contains them, but a categorical level or
// an explicit case value is written by hand, and one that expands is no longer
// the value the user wrote.
const globValueChars = "*?[](){}~"

// Rendered output is bounded at the one boundary every surface goes through.
// A command legitimately gets long — solver flags, paths, mesh names — so it has
// room to spare; a job name and a tag are labels, and one that runs past these
// lengths is a runaway template rather than a label.
const (
	maxCommandLength = 32 << 10
	maxJobNameLength = 128
	maxTagLength     = 64
)

// maxFormatDigits bounds both the width and the precision of Parameter.Format.
// fmt honours either to any size it is given, so "%.1000000f" renders a
// megabyte-long argument from a single-digit value.
const maxFormatDigits = 32

// maxTagCallsBeforeWarning is where per-job tagging starts to be a meaningful
// share of a sweep's API traffic. Tags are applied one POST per tag per job.
const maxTagCallsBeforeWarning = 500

// validateOptions checks a sweep before anything is generated. Returns fatal
// problems and advisory warnings separately.
func validateOptions(opts Options) (errs []Problem, warns []Problem) {
	command := opts.Template.Command
	if strings.TrimSpace(command) == "" {
		errs = append(errs, Problem{
			Code:    CodeNoCommand,
			Message: "template command is empty; a sweep needs a command containing {{name}} tokens",
		})
	}

	// Every case's name is derived from the template's, so an unnamed template
	// would yield names that are nothing but an index.
	if strings.TrimSpace(opts.Template.JobName) == "" && templateUsesToken(opts.JobNameTemplate, tokenBase) {
		errs = append(errs, Problem{
			Code:    CodeEmptyJobName,
			Message: "template job name is empty, but the job name template uses {{__base}}; name the base job",
		})
	}

	if !isKnownMethod(opts.Method) {
		errs = append(errs, Problem{
			Code:    CodeBadMethod,
			Message: fmt.Sprintf("unknown method %q; expected one of %s", opts.Method, methodList()),
		})
	}

	// A cap that switches itself off is not a cap. Raising the limit is the
	// supported way to run a large sweep; removing it is not, since every stage
	// past this one allocates per case.
	if opts.MaxCases < 0 {
		errs = append(errs, Problem{
			Code: CodeBadMaxCases,
			Message: fmt.Sprintf("MaxCases is %d; it must be positive, and 0 selects the default of %d",
				opts.MaxCases, DefaultMaxCases),
		})
	}

	errs = append(errs, validateParameterSet(opts)...)

	// Bidirectional parameter/token check. Only meaningful once names themselves
	// are known-good, so it is skipped when the parameter set is already broken.
	if len(errs) == 0 {
		errs = append(errs, validateTokenAgreement(opts)...)
	}

	errs = append(errs, validateMethodRequirements(opts)...)
	warns = append(warns, warnTagVolume(opts)...)

	return errs, warns
}

// validateParameterSet checks names and per-parameter ranges.
func validateParameterSet(opts Options) []Problem {
	var errs []Problem

	if len(opts.Parameters) == 0 {
		return []Problem{{
			Code:    CodeNoParameters,
			Message: "no parameters declared; a sweep needs at least one",
		}}
	}

	if len(opts.Parameters) > maxParameters {
		return []Problem{{
			Code: CodeTooManyParams,
			Message: fmt.Sprintf("%d parameters declared; at most %d are supported",
				len(opts.Parameters), maxParameters),
		}}
	}

	seen := make(map[string]bool, len(opts.Parameters))
	seenFolded := make(map[string]string, len(opts.Parameters))

	for _, p := range opts.Parameters {
		name := p.Name

		if !isValidTokenName(name) {
			errs = append(errs, Problem{
				Code:  CodeBadParamName,
				Param: name,
				Message: "invalid parameter name; must start with a letter or underscore and " +
					"contain only letters, digits, underscores, hyphens and dots",
			})
			continue
		}

		// Built-ins are added to the substitution map after the parameters, so a
		// parameter of the same name loses the command but still shows its own
		// value in the preview: the two would disagree with nothing to say so.
		if isBuiltinToken(name) {
			errs = append(errs, Problem{
				Code:  CodeReservedParam,
				Param: name,
				Message: fmt.Sprintf("{{%s}} is supplied by the sweep itself and cannot be a parameter; "+
					"rename it", name),
			})
			continue
		}

		if seen[name] {
			errs = append(errs, Problem{
				Code:    CodeDuplicateParam,
				Param:   name,
				Message: "parameter declared more than once",
			})
			continue
		}
		seen[name] = true

		// Tokens are case-sensitive, so "Alpha" and "alpha" are different tokens.
		// Declaring both is legal but near-certainly a mistake, and it makes the
		// rendered command impossible to read, so it is rejected.
		folded := strings.ToLower(name)
		if other, clash := seenFolded[folded]; clash {
			errs = append(errs, Problem{
				Code:  CodeCaseCollision,
				Param: name,
				Message: fmt.Sprintf("parameter differs from %q only in capitalization; "+
					"tokens are case-sensitive, so this is almost certainly a mistake", other),
			})
			continue
		}
		seenFolded[folded] = name

		errs = append(errs, validateParameterDomain(p, opts.Method)...)
	}

	return errs
}

// validateParameterDomain checks one parameter's value domain.
func validateParameterDomain(p Parameter, method Method) []Problem {
	var errs []Problem

	// Categorical: Values wins and the numeric range is not consulted.
	if p.Values != nil {
		if len(p.Values) == 0 {
			errs = append(errs, Problem{
				Code:    CodeNoValues,
				Param:   p.Name,
				Message: "Values is set but empty; give at least one value or use Min/Max",
			})
		}
		seen := make(map[string]bool, len(p.Values))
		for _, v := range p.Values {
			errs = append(errs, validateValue(p.Name, v, policyLiteral)...)

			// A repeated level is a design that runs the same case twice. It is
			// caught after the fact by the duplicate-case warning, but the input is
			// wrong here, where it can be named.
			if seen[v] {
				errs = append(errs, Problem{
					Code:    CodeDuplicateValue,
					Param:   p.Name,
					Message: fmt.Sprintf("value %q is listed more than once, so the design would repeat it", v),
				})
				continue
			}
			seen[v] = true
		}
		return errs
	}

	// NaN fails every comparison, so an unchecked NaN bound slips past the range
	// test below and renders "NaN" into the command line; an infinite bound
	// renders "+Inf" or, at the midpoint of an infinite range, "NaN" again.
	if nonFinite(p.Min) || nonFinite(p.Max) {
		errs = append(errs, Problem{
			Code:    CodeBadRange,
			Param:   p.Name,
			Message: fmt.Sprintf("Min (%g) and Max (%g) must both be finite numbers", p.Min, p.Max),
		})
	}

	if p.Min > p.Max {
		errs = append(errs, Problem{
			Code:    CodeBadRange,
			Param:   p.Name,
			Message: fmt.Sprintf("Min (%g) is greater than Max (%g)", p.Min, p.Max),
		})
	}

	errs = append(errs, validateFormat(p)...)

	// Only the grid designs consult Levels; the continuous samplers draw from the
	// range directly, so an unset Levels is fine for them.
	if usesLevels(method) {
		if p.Levels < 0 {
			errs = append(errs, Problem{
				Code:    CodeBadLevels,
				Param:   p.Name,
				Message: fmt.Sprintf("Levels is negative (%d)", p.Levels),
			})
		} else if p.Levels == 1 {
			errs = append(errs, Problem{
				Code:    CodeSingleLevel,
				Param:   p.Name,
				Message: "Levels is 1, so this parameter never varies; use at least 2 or drop it",
			})
		}
	}

	return errs
}

// numericFormat is the shape Parameter.Format must have: literal text with no
// second conversion in it, around one numeric verb whose width and precision are
// at most two digits each. Two digits is the bound itself — a longer run cannot
// be under maxFormatDigits — which is what keeps "%.1000000f" from matching at
// all rather than matching and being rejected afterwards.
var numericFormat = regexp.MustCompile(`^[^%]*%[+\-#0]*([0-9]{0,2})(?:\.([0-9]{0,2}))?[eEfFgG][^%]*$`)

// validateFormat checks Parameter.Format before it reaches fmt.Sprintf.
//
// fmt reports an inapplicable verb inside its output, which render.go catches,
// but it applies a width or precision of any size without complaint. Checking
// the syntax here rejects the sweep once, on its inputs, rather than once per
// case on its results.
func validateFormat(p Parameter) []Problem {
	if p.Format == "" {
		return nil
	}

	reject := func(reason string) []Problem {
		return []Problem{{
			Code:    CodeBadFormat,
			Param:   p.Name,
			Message: fmt.Sprintf("Format %q %s; use a numeric verb such as %%g, %%f or %%.3f", p.Format, reason),
		}}
	}

	groups := numericFormat.FindStringSubmatch(p.Format)
	if groups == nil {
		return reject(fmt.Sprintf("is not a numeric format with a width and precision of at most %d",
			maxFormatDigits))
	}

	for i, field := range []string{"width", "precision"} {
		if digits := groups[i+1]; digits != "" {
			if n, _ := strconv.Atoi(digits); n > maxFormatDigits {
				return reject(fmt.Sprintf("asks for a %s of %d, above the limit of %d", field, n, maxFormatDigits))
			}
		}
	}

	return nil
}

// valuePolicy selects how strict validateValue is about a rendered value.
type valuePolicy int

const (
	// policyNumeric applies to output produced by a numeric Format: its shape is
	// fmt's, so only what would restructure the command is rejected. A leading
	// "-" stays legal, since negative values are the point of a numeric range.
	policyNumeric valuePolicy = iota

	// policyLiteral applies to a categorical level or an explicit case value,
	// which the user writes verbatim and the shell would happily expand.
	policyLiteral
)

// validateValue rejects a value that would restructure the command rather than
// fill a slot in it.
func validateValue(param, value string, policy valuePolicy) []Problem {
	if value == "" {
		return []Problem{{
			Code:  CodeEmptyValue,
			Param: param,
			Message: "value is empty, which would leave a hole in the rendered command " +
				"instead of an argument",
		}}
	}

	unsafe := unsafeValueChars
	if policy == policyLiteral {
		unsafe += globValueChars
	}
	if idx := strings.IndexAny(value, unsafe); idx >= 0 {
		return []Problem{{
			Code:  CodeUnsafeValue,
			Param: param,
			Message: fmt.Sprintf("value %q contains %q, which would change the structure of the "+
				"rendered command rather than supply a value", value, string(value[idx])),
		}}
	}

	// Quoting is not available (quote characters are rejected above), so any
	// space splits one argument into two — and that is every kind of space, not
	// just the plain one: a non-breaking space, a vertical tab and a NUL all
	// reach the API verbatim and mean something else there.
	for _, r := range value {
		switch {
		case unicode.IsSpace(r):
			return []Problem{{
				Code:  CodeValueHasSpace,
				Param: param,
				Message: fmt.Sprintf("value %q contains whitespace (%U), which would split into separate "+
					"command arguments", value, r),
			}}
		case unicode.IsControl(r):
			return []Problem{{
				Code:  CodeUnsafeValue,
				Param: param,
				Message: fmt.Sprintf("value %q contains the control character %U, which does not survive "+
					"the trip to a command line", value, r),
			}}
		}
	}

	return nil
}

// validateTokenAgreement is the bidirectional check between declared parameters
// and the tokens actually present in the template command.
//
// Both directions are fatal. A declared parameter with no token would be
// silently dropped, producing identical jobs that differ only in name; a token
// with no parameter would survive substitution and reach Rescale as a literal
// "{{alpha}}" in the command line. Neither is recoverable by guessing, so
// neither is a warning.
func validateTokenAgreement(opts Options) []Problem {
	var errs []Problem

	declared := make(map[string]bool, len(opts.Parameters))
	for _, p := range opts.Parameters {
		declared[p.Name] = true
	}

	present := make(map[string]bool)
	for _, name := range pattern.ExtractTokens(opts.Template.Command) {
		present[name] = true
	}

	// Declared but absent from the command.
	for _, p := range opts.Parameters {
		if present[p.Name] {
			continue
		}

		msg := fmt.Sprintf("parameter %q is swept but {{%s}} does not appear in the command, "+
			"so its value would never reach the job", p.Name, p.Name)
		if near := closestFold(p.Name, present); near != "" {
			msg += fmt.Sprintf("; the command has {{%s}}, which differs only in capitalization", near)
		}

		errs = append(errs, Problem{Code: CodeMissingToken, Param: p.Name, Message: msg})
	}

	// Present in the command but not declared. Built-ins are exempt: a command may
	// legitimately reference {{__index}}, which Generate supplies itself.
	for _, name := range pattern.ExtractTokens(opts.Template.Command) {
		if declared[name] || isBuiltinToken(name) {
			continue
		}

		msg := fmt.Sprintf("command contains {{%s}} but no parameter of that name is declared, "+
			"so the job would run with a literal \"{{%s}}\" in its command", name, name)
		if near := closestFold(name, declared); near != "" {
			msg += fmt.Sprintf("; parameter %q differs only in capitalization", near)
		}

		errs = append(errs, Problem{Code: CodeUndeclaredToken, Param: name, Message: msg})
	}

	// Tokens in the job name and tag templates resolve against the same value set
	// plus the built-ins, and an unknown one there is fatal for the same reason it
	// is in the command: substitution leaves it alone, so the literal "{{alpha}}"
	// reaches Rescale as part of the job's name or one of its tags.
	for _, name := range pattern.ExtractTokens(opts.JobNameTemplate) {
		if !declared[name] && !isBuiltinToken(name) {
			errs = append(errs, Problem{
				Code:    CodeUndeclaredToken,
				Param:   name,
				Message: fmt.Sprintf("job name template references unknown token {{%s}}", name),
			})
		}
	}
	for _, tagTemplate := range opts.TagTemplates {
		for _, name := range pattern.ExtractTokens(tagTemplate) {
			if !declared[name] && !isBuiltinToken(name) {
				errs = append(errs, Problem{
					Code:    CodeUndeclaredToken,
					Param:   name,
					Message: fmt.Sprintf("tag template %q references unknown token {{%s}}", tagTemplate, name),
				})
			}
		}
	}

	return errs
}

// validateMethodRequirements checks the per-method preconditions.
func validateMethodRequirements(opts Options) []Problem {
	var errs []Problem

	switch opts.Method {
	case MethodLatinHypercube, MethodSobol, MethodMonteCarlo:
		if opts.Samples < 1 {
			errs = append(errs, Problem{
				Code:    CodeBadSamples,
				Message: fmt.Sprintf("method %q needs Samples >= 1, got %d", opts.Method, opts.Samples),
			})
		}
		if opts.Method == MethodSobol && len(opts.Parameters) > sobolMaxDimensions {
			errs = append(errs, Problem{
				Code: CodeBadMethod,
				Message: fmt.Sprintf("sobol supports up to %d parameters, got %d; use latin-hypercube instead",
					sobolMaxDimensions, len(opts.Parameters)),
			})
		}

	case MethodBoxBehnken:
		if len(opts.Parameters) < 3 {
			errs = append(errs, Problem{
				Code: CodeTooFewParams,
				Message: fmt.Sprintf("box-behnken needs at least 3 parameters, got %d; "+
					"use central-composite or full-factorial instead", len(opts.Parameters)),
			})
		}

	case MethodExplicit:
		if len(opts.Cases) == 0 {
			errs = append(errs, Problem{
				Code:    CodeNoCases,
				Message: "method \"explicit\" needs at least one case",
			})
		}
		errs = append(errs, validateExplicitCases(opts)...)
	}

	// The quadratic designs place points at coded coordinates and read them back
	// through the category bins, which clamp: the axial points collapse onto the
	// corners and the design degenerates into repeats rather than a response
	// surface. Rejecting the combination says so, where a duplicate-case warning
	// would not.
	if opts.Method == MethodCentralComposite || opts.Method == MethodBoxBehnken {
		for _, p := range opts.Parameters {
			if p.Values != nil {
				errs = append(errs, Problem{
					Code:  CodeBadMethod,
					Param: p.Name,
					Message: fmt.Sprintf("method %q fits a quadratic response surface and needs numeric "+
						"parameters, but %q is categorical; use full-factorial or ofat instead",
						opts.Method, p.Name),
				})
			}
		}
	}

	return errs
}

// validateExplicitCases checks that every explicit case covers every parameter
// and introduces none of its own.
func validateExplicitCases(opts Options) []Problem {
	var errs []Problem

	declared := make(map[string]bool, len(opts.Parameters))
	for _, p := range opts.Parameters {
		declared[p.Name] = true
	}

	for i, c := range opts.Cases {
		caseNum := i + 1

		for _, p := range opts.Parameters {
			value, ok := c[p.Name]
			if !ok {
				errs = append(errs, Problem{
					Code:    CodeMissingToken,
					Param:   p.Name,
					Message: fmt.Sprintf("case %d has no value for parameter %q", caseNum, p.Name),
				})
				continue
			}
			for _, problem := range validateValue(p.Name, value, policyLiteral) {
				problem.Message = fmt.Sprintf("case %d: %s", caseNum, problem.Message)
				errs = append(errs, problem)
			}
		}

		for name := range c {
			if !declared[name] {
				errs = append(errs, Problem{
					Code:    CodeUndeclaredToken,
					Param:   name,
					Message: fmt.Sprintf("case %d supplies %q, which is not a declared parameter", caseNum, name),
				})
			}
		}
	}

	return errs
}

// warnTagVolume flags a sweep whose tagging will dominate its API traffic.
// Tags are applied one POST per tag per job after creation.
func warnTagVolume(opts Options) []Problem {
	if len(opts.TagTemplates) == 0 && len(opts.Template.Tags) == 0 {
		return nil
	}

	tagsPerJob := len(opts.TagTemplates) + len(opts.Template.Tags)
	estimatedJobs := estimateCaseCount(opts)
	calls := tagsPerJob * estimatedJobs

	if calls <= maxTagCallsBeforeWarning {
		return nil
	}

	return []Problem{{
		Code: CodeTagCallVolume,
		Message: fmt.Sprintf("about %d jobs x %d tags is roughly %d extra API calls, applied one per tag "+
			"after each job is created; consider fewer tags", estimatedJobs, tagsPerJob, calls),
	}}
}

// isValidTokenName reports whether name is usable as a {{name}} token. Defined
// by round-tripping through the pattern package so the two cannot drift.
func isValidTokenName(name string) bool {
	if name == "" {
		return false
	}
	tokens := pattern.ExtractTokens("{{" + name + "}}")
	return len(tokens) == 1 && tokens[0] == name
}

// nonFinite reports whether v is NaN or an infinity, neither of which is usable
// as a sweep bound.
func nonFinite(v float64) bool { return math.IsNaN(v) || math.IsInf(v, 0) }

// isBuiltinToken reports whether name is one of the tokens Generate supplies
// itself rather than one drawn from a parameter.
func isBuiltinToken(name string) bool {
	return name == tokenIndex || name == tokenBase
}

// templateUsesToken reports whether template references the named token, in any
// of the whitespace forms the token syntax allows.
func templateUsesToken(template, name string) bool {
	for _, token := range pattern.ExtractTokens(template) {
		if token == name {
			return true
		}
	}
	return false
}

// closestFold returns a key of candidates that matches name apart from
// capitalization, or "" if there is none. Used to turn a bare "missing token"
// into a message that names the likely typo.
func closestFold(name string, candidates map[string]bool) string {
	folded := strings.ToLower(name)
	var matches []string
	for candidate := range candidates {
		if candidate != name && strings.ToLower(candidate) == folded {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		return ""
	}
	// Sorted so the message is stable across map iteration order.
	sort.Strings(matches)
	return matches[0]
}

func isKnownMethod(m Method) bool {
	for _, known := range AllMethods() {
		if m == known {
			return true
		}
	}
	return false
}

func methodList() string {
	all := AllMethods()
	names := make([]string, len(all))
	for i, m := range all {
		names[i] = string(m)
	}
	return strings.Join(names, ", ")
}

// usesLevels reports whether a method samples on a grid of discrete levels.
func usesLevels(m Method) bool {
	return m == MethodFullFactorial || m == MethodOFAT
}
