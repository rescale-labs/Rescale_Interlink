package doe

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

// baseTemplate is the job every test sweeps from.
func baseTemplate() models.JobSpec {
	return models.JobSpec{
		JobName:         "sweep",
		Directory:       "/data/base",
		AnalysisCode:    "starccm_plus",
		AnalysisVersion: "18.06.006",
		Command:         "starccm+ -param alpha {{alpha}} -param beta {{beta}} -load input.sim",
		CoreType:        "emerald",
		CoresPerSlot:    36,
		WalltimeHours:   4,
		Slots:           1,
		SubmitMode:      "no",
	}
}

func twoLevelOptions() Options {
	return Options{
		Template: baseTemplate(),
		Parameters: []Parameter{
			{Name: "alpha", Min: 10, Max: 20, Levels: 2},
			{Name: "beta", Min: 15, Max: 25, Levels: 2},
		},
		Method: MethodFullFactorial,
	}
}

// A preview must be an exact prefix of the full sweep, and truncation must not
// hide a fatal problem whose trigger value sits past the limit — otherwise the
// GUI's Create button goes green on a sweep that full generation then rejects.
func TestGenerate_RenderLimitIsAnExactPrefixAndStillValidates(t *testing.T) {
	full := mustGenerate(t, twoLevelOptions())

	limited := twoLevelOptions()
	limited.RenderLimit = 2
	preview := mustGenerate(t, limited)

	if preview.CaseCount != full.CaseCount || len(preview.Cases) != 2 {
		t.Fatalf("CaseCount = %d, len(Cases) = %d; want the full design size %d and 2 rendered",
			preview.CaseCount, len(preview.Cases), full.CaseCount)
	}
	if !reflect.DeepEqual(preview.Cases, full.Cases[:2]) {
		t.Errorf("rendered cases are not an exact prefix of the full sweep")
	}

	bad := Options{
		Template: models.JobSpec{JobName: "base", Command: "run {{m}}"},
		Parameters: []Parameter{
			{Name: "m", Values: []string{"ok", strings.Repeat("x", maxJobNameLength+1)}},
		},
		Method:          MethodFullFactorial,
		JobNameTemplate: "n_{{m}}",
		RenderLimit:     1,
	}
	result := Generate(bad)
	if result.OK() {
		t.Fatal("a name past the length bound in case 2 went undetected under RenderLimit 1")
	}
}

func mustGenerate(t *testing.T, opts Options) Result {
	t.Helper()
	result := Generate(opts)
	if !result.OK() {
		t.Fatalf("Generate() failed: %v", problemStrings(result.Errors))
	}
	return result
}

func problemStrings(problems []Problem) []string {
	out := make([]string, len(problems))
	for i, p := range problems {
		out[i] = p.Error()
	}
	return out
}

func hasCode(problems []Problem, code string) bool {
	for _, p := range problems {
		if p.Code == code {
			return true
		}
	}
	return false
}

// The motivating case: a sweep's parameter values must land in the command line
// so each case's configuration is visible on its Rescale job page.
func TestGenerate_RendersValuesIntoCommand(t *testing.T) {
	result := mustGenerate(t, twoLevelOptions())

	if len(result.Cases) != 4 {
		t.Fatalf("got %d cases, want 4", len(result.Cases))
	}

	// Full factorial runs as an odometer with the last parameter varying fastest.
	want := []string{
		"starccm+ -param alpha 10 -param beta 15 -load input.sim",
		"starccm+ -param alpha 10 -param beta 25 -load input.sim",
		"starccm+ -param alpha 20 -param beta 15 -load input.sim",
		"starccm+ -param alpha 20 -param beta 25 -load input.sim",
	}
	for i, c := range result.Cases {
		if c.Command != want[i] {
			t.Errorf("case %d command = %q, want %q", i+1, c.Command, want[i])
		}
	}

	// Jobs are parallel to cases and carry the same rendered command.
	if len(result.Jobs) != len(result.Cases) {
		t.Fatalf("got %d jobs for %d cases", len(result.Jobs), len(result.Cases))
	}
	for i := range result.Jobs {
		if result.Jobs[i].Command != result.Cases[i].Command {
			t.Errorf("job %d command = %q, want %q", i, result.Jobs[i].Command, result.Cases[i].Command)
		}
	}
}

func TestGenerate_NamesCasesFromTemplate(t *testing.T) {
	result := mustGenerate(t, twoLevelOptions())

	want := []string{"sweep_1", "sweep_2", "sweep_3", "sweep_4"}
	for i, c := range result.Cases {
		if c.JobName != want[i] {
			t.Errorf("case %d name = %q, want %q", i+1, c.JobName, want[i])
		}
		if result.Jobs[i].JobName != want[i] {
			t.Errorf("job %d name = %q, want %q", i, result.Jobs[i].JobName, want[i])
		}
	}
}

// The job name template decides both what a case is called and whether two cases
// can end up sharing a name, so all of it is one table over the same generation.
func TestGenerate_JobNames(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Options)
		wantName string // The first case's name, when generation succeeds.
		wantCode string // Set instead when generation must fail.
	}{
		{
			name:     "a base name already carrying an index suffix does not accumulate another",
			mutate:   func(o *Options) { o.Template.JobName = "sweep_7" },
			wantName: "sweep_1",
		},
		{
			name:     "a custom template renders parameter values into the name",
			mutate:   func(o *Options) { o.JobNameTemplate = "{{__base}}_a{{alpha}}_b{{beta}}" },
			wantName: "sweep_a10_b15",
		},
		{
			name:     "a template without the index collides once two cases share what it uses",
			mutate:   func(o *Options) { o.JobNameTemplate = "{{__base}}_a{{alpha}}" },
			wantCode: CodeDupJobName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := twoLevelOptions()
			tt.mutate(&opts)

			if tt.wantCode != "" {
				result := Generate(opts)
				if result.OK() {
					t.Fatalf("expected rejection with %s", tt.wantCode)
				}
				if !hasCode(result.Errors, tt.wantCode) {
					t.Errorf("errors = %v, want a %s", problemStrings(result.Errors), tt.wantCode)
				}
				return
			}

			result := mustGenerate(t, opts)
			if result.Cases[0].JobName != tt.wantName {
				t.Errorf("first case name = %q, want %q", result.Cases[0].JobName, tt.wantName)
			}
		})
	}
}

func TestGenerate_RendersTagsPerCase(t *testing.T) {
	opts := twoLevelOptions()
	opts.Template.Tags = []string{"doe"}
	opts.TagTemplates = []string{"alpha={{alpha}}", "beta={{beta}}", "case={{__index}}"}

	result := mustGenerate(t, opts)

	want := []string{"doe", "alpha=10", "beta=15", "case=1"}
	got := result.Cases[0].Tags
	if len(got) != len(want) {
		t.Fatalf("tags = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tag %d = %q, want %q", i, got[i], want[i])
		}
	}

	// The JobSpec carries them too, which is what the pipeline applies after the
	// job is created.
	if len(result.Jobs[0].Tags) != len(want) {
		t.Errorf("job tags = %v, want %v", result.Jobs[0].Tags, want)
	}
}

// A sweep never tars a per-case directory. Cases carry no Directory either way,
// which puts them on the pipeline's skip path; what changes with shared file IDs
// is only where the one shared deck comes from — those IDs, referenced directly,
// or batch-level Common Files.
func TestGenerate_CasesCarryNoDirectory(t *testing.T) {
	withIDs := twoLevelOptions()
	withIDs.BaseFileIDs = []string{"file_abc", "file_def"}

	shared := mustGenerate(t, withIDs)
	bare := mustGenerate(t, twoLevelOptions())

	for i, job := range shared.Jobs {
		if job.Directory != "" {
			t.Errorf("job %d Directory = %q, want empty so the pipeline skips tar/upload", i, job.Directory)
		}
		if len(job.InputFiles) != 2 || job.InputFiles[0] != "file_abc" || job.InputFiles[1] != "file_def" {
			t.Errorf("job %d InputFiles = %v, want the shared file IDs", i, job.InputFiles)
		}
	}

	for i, job := range bare.Jobs {
		if job.Directory != "" {
			t.Errorf("job %d Directory = %q, want empty so the pipeline skips tar/upload", i, job.Directory)
		}
		if len(job.InputFiles) != 0 {
			t.Errorf("job %d InputFiles = %v, want none", i, job.InputFiles)
		}
	}

	// The returned slices must not alias the caller's, or mutating one case's
	// inputs would change every other case.
	shared.Jobs[0].InputFiles[0] = "mutated"
	if withIDs.BaseFileIDs[0] != "file_abc" {
		t.Error("job InputFiles aliases Options.BaseFileIDs")
	}
	if shared.Jobs[1].InputFiles[0] != "file_abc" {
		t.Error("jobs share one InputFiles backing array")
	}
}

func TestGenerate_PreservesTemplateJobSettings(t *testing.T) {
	result := mustGenerate(t, twoLevelOptions())

	job := result.Jobs[0]
	template := baseTemplate()

	if job.AnalysisCode != template.AnalysisCode ||
		job.AnalysisVersion != template.AnalysisVersion ||
		job.CoreType != template.CoreType ||
		job.CoresPerSlot != template.CoresPerSlot ||
		job.WalltimeHours != template.WalltimeHours ||
		job.Slots != template.Slots ||
		job.SubmitMode != template.SubmitMode {
		t.Errorf("job settings diverged from the template: %+v", job)
	}
}

// --- Bidirectional parameter/token validation ---

func TestGenerate_RejectsParameterWithNoTokenInCommand(t *testing.T) {
	opts := twoLevelOptions()
	opts.Parameters = append(opts.Parameters, Parameter{Name: "gamma", Min: 1, Max: 2, Levels: 2})

	result := Generate(opts)

	if result.OK() {
		t.Fatal("expected a swept parameter with no command token to be rejected")
	}
	if !hasCode(result.Errors, CodeMissingToken) {
		t.Fatalf("errors = %v, want a %s", problemStrings(result.Errors), CodeMissingToken)
	}
	if len(result.Cases) != 0 || len(result.Jobs) != 0 {
		t.Error("a failed sweep must generate nothing")
	}
}

func TestGenerate_RejectsCommandTokenWithNoParameter(t *testing.T) {
	opts := twoLevelOptions()
	opts.Template.Command += " -param gamma {{gamma}}"

	result := Generate(opts)

	if result.OK() {
		t.Fatal("expected an undeclared command token to be rejected")
	}
	if !hasCode(result.Errors, CodeUndeclaredToken) {
		t.Errorf("errors = %v, want a %s", problemStrings(result.Errors), CodeUndeclaredToken)
	}
}

// The common typo is a capitalization mismatch, so the message should name the
// near miss rather than leaving the user to spot it.
func TestGenerate_MismatchedCaseNamesTheNearMiss(t *testing.T) {
	opts := twoLevelOptions()
	opts.Template.Command = "starccm+ -param alpha {{Alpha}} -param beta {{beta}} -load input.sim"

	result := Generate(opts)

	if result.OK() {
		t.Fatal("expected a capitalization mismatch to be rejected")
	}

	joined := strings.Join(problemStrings(result.Errors), "\n")
	if !strings.Contains(joined, "capitalization") {
		t.Errorf("errors did not mention capitalization:\n%s", joined)
	}
	if !hasCode(result.Errors, CodeMissingToken) || !hasCode(result.Errors, CodeUndeclaredToken) {
		t.Errorf("expected both directions to be reported, got %v", problemStrings(result.Errors))
	}
}

// {{__index}} is supplied by Generate, so a command may use it without declaring
// a parameter of that name.
func TestGenerate_BuiltinTokensAllowedInCommand(t *testing.T) {
	opts := twoLevelOptions()
	opts.Template.Command += " -tag run{{__index}}"

	result := mustGenerate(t, opts)

	if !strings.HasSuffix(result.Cases[0].Command, "-tag run1") {
		t.Errorf("command = %q, want it to end with -tag run1", result.Cases[0].Command)
	}
}

// An unknown token in a name or tag is fatal for the same reason it is in the
// command: substitution leaves it alone, so the placeholder text itself reaches
// Rescale as the job's name or one of its tags.
func TestGenerate_RejectsUnknownTokenInNamesAndTags(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*Options)
	}{
		{"job name template", func(o *Options) { o.JobNameTemplate = "{{__base}}_{{__index}}_{{nope}}" }},
		{"tag template", func(o *Options) { o.TagTemplates = []string{"t={{nope}}"} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := twoLevelOptions()
			tt.mutate(&opts)

			result := Generate(opts)

			if result.OK() {
				t.Fatal("expected an unknown token to be rejected")
			}
			if !hasCode(result.Errors, CodeUndeclaredToken) {
				t.Errorf("errors = %v, want a %s", problemStrings(result.Errors), CodeUndeclaredToken)
			}
		})
	}
}

func TestValidateOptions_Rejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Options)
		code   string
	}{
		{
			name:   "empty command",
			mutate: func(o *Options) { o.Template.Command = "  " },
			code:   CodeNoCommand,
		},
		{
			name:   "no parameters",
			mutate: func(o *Options) { o.Parameters = nil },
			code:   CodeNoParameters,
		},
		{
			name:   "unknown method",
			mutate: func(o *Options) { o.Method = "bogus" },
			code:   CodeBadMethod,
		},
		{
			name:   "duplicate parameter",
			mutate: func(o *Options) { o.Parameters = append(o.Parameters, o.Parameters[0]) },
			code:   CodeDuplicateParam,
		},
		{
			name: "parameters differing only by case",
			mutate: func(o *Options) {
				o.Parameters = append(o.Parameters, Parameter{Name: "Alpha", Min: 1, Max: 2, Levels: 2})
			},
			code: CodeCaseCollision,
		},
		{
			name: "invalid parameter name",
			mutate: func(o *Options) {
				o.Parameters = []Parameter{{Name: "1alpha", Min: 1, Max: 2, Levels: 2}}
			},
			code: CodeBadParamName,
		},
		{
			name:   "inverted range",
			mutate: func(o *Options) { o.Parameters[0].Min, o.Parameters[0].Max = 20, 10 },
			code:   CodeBadRange,
		},
		{
			name:   "negative levels",
			mutate: func(o *Options) { o.Parameters[0].Levels = -1 },
			code:   CodeBadLevels,
		},
		{
			name:   "single level never varies",
			mutate: func(o *Options) { o.Parameters[0].Levels = 1 },
			code:   CodeSingleLevel,
		},
		{
			name:   "empty explicit Values",
			mutate: func(o *Options) { o.Parameters[0].Values = []string{} },
			code:   CodeNoValues,
		},
		{
			name:   "value with shell metacharacter",
			mutate: func(o *Options) { o.Parameters[0].Values = []string{"10", "20; rm -rf /"} },
			code:   CodeUnsafeValue,
		},
		{
			name:   "value with whitespace",
			mutate: func(o *Options) { o.Parameters[0].Values = []string{"10", "2 0"} },
			code:   CodeValueHasSpace,
		},
		{
			name:   "empty value",
			mutate: func(o *Options) { o.Parameters[0].Values = []string{"10", ""} },
			code:   CodeEmptyValue,
		},
		{
			name:   "unnamed base job with a name template that needs it",
			mutate: func(o *Options) { o.Template.JobName = "" },
			code:   CodeEmptyJobName,
		},
		{
			name:   "NaN bound",
			mutate: func(o *Options) { o.Parameters[0].Min = math.NaN() },
			code:   CodeBadRange,
		},
		{
			name:   "infinite bound",
			mutate: func(o *Options) { o.Parameters[0].Max = math.Inf(1) },
			code:   CodeBadRange,
		},
		{
			name:   "negative MaxCases",
			mutate: func(o *Options) { o.MaxCases = -1 },
			code:   CodeBadMaxCases,
		},
		{
			name: "a parameter named after a built-in token",
			mutate: func(o *Options) {
				o.Parameters[0].Name = tokenIndex
				o.Template.Command = "run {{__index}} {{beta}}"
			},
			code: CodeReservedParam,
		},
		{
			name:   "a categorical level listed twice",
			mutate: func(o *Options) { o.Parameters[0].Values = []string{"a", "b", "a"} },
			code:   CodeDuplicateValue,
		},
		{
			name: "a categorical parameter in a quadratic design",
			mutate: func(o *Options) {
				o.Method = MethodCentralComposite
				o.Parameters[0].Values = []string{"a", "b"}
			},
			code: CodeBadMethod,
		},
		{
			name:   "a format with a runaway precision",
			mutate: func(o *Options) { o.Parameters[0].Format = "%.1000000f" },
			code:   CodeBadFormat,
		},
		{
			name:   "a categorical value with a glob metacharacter",
			mutate: func(o *Options) { o.Parameters[0].Values = []string{"10", "*"} },
			code:   CodeUnsafeValue,
		},
		{
			name:   "a value with a NUL byte",
			mutate: func(o *Options) { o.Parameters[0].Values = []string{"10", "2\x000"} },
			code:   CodeUnsafeValue,
		},
		{
			name:   "a value with a non-breaking space",
			mutate: func(o *Options) { o.Parameters[0].Values = []string{"10", "2 0"} },
			code:   CodeValueHasSpace,
		},
		{
			name:   "a rendered command past the length bound",
			mutate: func(o *Options) { o.Template.Command += " " + strings.Repeat("x", maxCommandLength) },
			code:   CodeTooLong,
		},
		{
			name:   "a rendered job name past the length bound",
			mutate: func(o *Options) { o.JobNameTemplate = strings.Repeat("x", maxJobNameLength) + "{{__index}}" },
			code:   CodeTooLong,
		},
		{
			name:   "a rendered tag past the length bound",
			mutate: func(o *Options) { o.TagTemplates = []string{strings.Repeat("x", maxTagLength) + "{{__index}}"} },
			code:   CodeTooLong,
		},
		{
			name: "explicit case missing a parameter",
			mutate: func(o *Options) {
				o.Method = MethodExplicit
				o.Cases = []map[string]string{{"alpha": "10"}}
			},
			code: CodeMissingToken,
		},
		{
			name: "explicit case supplying an undeclared parameter",
			mutate: func(o *Options) {
				o.Method = MethodExplicit
				o.Cases = []map[string]string{{"alpha": "10", "beta": "15", "gamma": "1"}}
			},
			code: CodeUndeclaredToken,
		},
		{
			name: "explicit case with an unsafe value",
			mutate: func(o *Options) {
				o.Method = MethodExplicit
				o.Cases = []map[string]string{{"alpha": "10", "beta": "$(whoami)"}}
			},
			code: CodeUnsafeValue,
		},
		{
			name:   "explicit method with no cases",
			mutate: func(o *Options) { o.Method = MethodExplicit },
			code:   CodeNoCases,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := twoLevelOptions()
			tt.mutate(&opts)

			result := Generate(opts)

			if result.OK() {
				t.Fatalf("expected rejection with %s", tt.code)
			}
			if !hasCode(result.Errors, tt.code) {
				t.Errorf("errors = %v, want a %s", problemStrings(result.Errors), tt.code)
			}
			if len(result.Jobs) != 0 {
				t.Error("a failed sweep must generate no jobs")
			}
		})
	}
}

func TestGenerate_SampleCountRequiredForContinuousMethods(t *testing.T) {
	for _, method := range []Method{MethodLatinHypercube, MethodSobol, MethodMonteCarlo} {
		t.Run(string(method), func(t *testing.T) {
			opts := twoLevelOptions()
			opts.Method = method
			opts.Samples = 0

			result := Generate(opts)

			if !hasCode(result.Errors, CodeBadSamples) {
				t.Errorf("errors = %v, want a %s", problemStrings(result.Errors), CodeBadSamples)
			}
		})
	}
}

// Size is rejected by arithmetic, before the design is built. The projection has
// to survive the counts that overflow it as well as the ones that merely exceed
// a limit, because a wrapped count is negative and a negative count passes every
// ceiling test on its way to a negative allocation.
func TestGenerate_EnforcesCaseLimits(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{
			name: "100 cases against an explicit limit of 50",
			mutate: func(o *Options) {
				o.Parameters[0].Levels, o.Parameters[1].Levels = 10, 10
				o.MaxCases = 50
			},
		},
		{
			name: "1600 cases against the default limit",
			mutate: func(o *Options) {
				o.Parameters[0].Levels, o.Parameters[1].Levels = 40, 40
			},
		},
		{
			// MaxCases well past the ceiling, so only the absolute-ceiling check
			// can reject this one — the row fails if that guard is removed.
			name: "a design past the absolute ceiling however high the limit is raised",
			mutate: func(o *Options) {
				o.MaxCases = 1_000_000_000
				o.Template.Command = "run"
				o.Parameters = nil
				for i := 0; i < 40; i++ {
					name := "p" + string(rune('a'+i%26)) + string(rune('a'+i/26))
					o.Parameters = append(o.Parameters, Parameter{Name: name, Min: 0, Max: 1, Levels: 4})
					o.Template.Command += " {{" + name + "}}"
				}
			},
		},
		{
			name: "a level count whose product overflows",
			mutate: func(o *Options) {
				o.Parameters[0].Levels = 3
				o.Parameters[1].Levels = 1 << 62
			},
		},
		{
			name: "center points whose sum overflows",
			mutate: func(o *Options) {
				o.Method = MethodCentralComposite
				o.CenterPoints = math.MaxInt64
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := twoLevelOptions()
			tt.mutate(&opts)

			result := Generate(opts)

			if result.OK() {
				t.Fatalf("expected rejection, got %d cases", len(result.Cases))
			}
			if !hasCode(result.Errors, CodeTooManyCases) {
				t.Errorf("errors = %v, want a %s", problemStrings(result.Errors), CodeTooManyCases)
			}
		})
	}
}

// --- Explicit cases ---

func TestGenerate_Explicit(t *testing.T) {
	opts := twoLevelOptions()
	opts.Method = MethodExplicit
	opts.Cases = []map[string]string{
		{"alpha": "10", "beta": "15"},
		{"alpha": "12.5", "beta": "17"},
	}

	result := mustGenerate(t, opts)

	if len(result.Cases) != 2 {
		t.Fatalf("got %d cases, want 2", len(result.Cases))
	}
	if result.Cases[1].Command != "starccm+ -param alpha 12.5 -param beta 17 -load input.sim" {
		t.Errorf("case 2 command = %q", result.Cases[1].Command)
	}
}

// Explicit cases must be copied, or a caller mutating its own maps afterwards
// would change what was generated.
func TestGenerate_ExplicitCopiesCallerValues(t *testing.T) {
	opts := twoLevelOptions()
	opts.Method = MethodExplicit
	caseValues := map[string]string{"alpha": "10", "beta": "15"}
	opts.Cases = []map[string]string{caseValues}

	result := mustGenerate(t, opts)
	caseValues["alpha"] = "999"

	if result.Cases[0].Values["alpha"] != "10" {
		t.Errorf("generated case aliases the caller's map: alpha = %q", result.Cases[0].Values["alpha"])
	}
}

// --- Categorical parameters ---

func TestGenerate_CategoricalParameter(t *testing.T) {
	opts := Options{
		Template: models.JobSpec{
			JobName: "sweep",
			Command: "solve -model {{model}} -alpha {{alpha}}",
		},
		Parameters: []Parameter{
			{Name: "model", Values: []string{"kepsilon", "komega", "les"}},
			{Name: "alpha", Min: 0, Max: 1, Levels: 2},
		},
		Method: MethodFullFactorial,
	}

	result := mustGenerate(t, opts)

	if len(result.Cases) != 6 {
		t.Fatalf("got %d cases, want 6 (3 models x 2 alphas)", len(result.Cases))
	}

	seen := map[string]bool{}
	for _, c := range result.Cases {
		seen[c.Values["model"]] = true
	}
	for _, want := range []string{"kepsilon", "komega", "les"} {
		if !seen[want] {
			t.Errorf("model %q never appeared", want)
		}
	}
}

// --- Formatting ---

func TestGenerate_ParameterFormat(t *testing.T) {
	opts := twoLevelOptions()
	opts.Parameters[0].Format = "%.3f"

	result := mustGenerate(t, opts)

	if result.Cases[0].Values["alpha"] != "10.000" {
		t.Errorf("alpha = %q, want %q", result.Cases[0].Values["alpha"], "10.000")
	}
}

// A Format can introduce characters the raw value never had, so the formatted
// result is validated rather than just the inputs.
func TestGenerate_RejectsFormatThatProducesABadValue(t *testing.T) {
	tests := []struct {
		name   string
		format string
		code   string
	}{
		{"verb that cannot render a float", "%d", CodeBadFormat},
		{"format that quotes the value", "'%g'", CodeUnsafeValue},
		{"format that pads the value", "%8.2f", CodeValueHasSpace},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := twoLevelOptions()
			opts.Parameters[0].Format = tt.format

			result := Generate(opts)

			if result.OK() {
				t.Fatalf("expected Format %q to be rejected, got command %q",
					tt.format, result.Cases[0].Command)
			}
			if !hasCode(result.Errors, tt.code) {
				t.Errorf("errors = %v, want a %s", problemStrings(result.Errors), tt.code)
			}
		})
	}
}

// --- Warnings ---

func TestGenerate_WarnsOnTagCallVolume(t *testing.T) {
	opts := twoLevelOptions()
	opts.MaxCases = -1
	opts.Parameters[0].Levels = 30
	opts.Parameters[1].Levels = 30 // 900 cases
	opts.TagTemplates = []string{"alpha={{alpha}}"}

	result := Generate(opts)

	if !hasCode(result.Warnings, CodeTagCallVolume) {
		t.Errorf("warnings = %v, want a %s", problemStrings(result.Warnings), CodeTagCallVolume)
	}
}

func TestGenerate_NoTagWarningForSmallSweeps(t *testing.T) {
	opts := twoLevelOptions()
	opts.TagTemplates = []string{"alpha={{alpha}}"}

	result := mustGenerate(t, opts)

	if hasCode(result.Warnings, CodeTagCallVolume) {
		t.Errorf("unexpected tag volume warning for a 4-case sweep: %v", problemStrings(result.Warnings))
	}
}

// --- API surface ---

func TestAllMethods_AreAllRecognized(t *testing.T) {
	if len(AllMethods()) != 8 {
		t.Errorf("AllMethods() has %d entries, want 8", len(AllMethods()))
	}
	for _, m := range AllMethods() {
		if !isKnownMethod(m) {
			t.Errorf("method %q is listed but not recognized", m)
		}
	}
}

func TestWithDefaults(t *testing.T) {
	tests := []struct {
		name string
		in   Options
		want Options
	}{
		{
			name: "zero values take the defaults",
			in:   Options{},
			want: Options{MaxCases: DefaultMaxCases, CenterPoints: 1, JobNameTemplate: "{{__base}}_{{__index}}"},
		},
		{
			// A negative MaxCases is rejected by validation rather than here, so
			// withDefaults must pass it through: filling it in would replace the
			// rejection with a silently different sweep.
			name: "explicit values are left alone",
			in:   Options{MaxCases: 7, CenterPoints: 5, JobNameTemplate: "x{{__index}}"},
			want: Options{MaxCases: 7, CenterPoints: 5, JobNameTemplate: "x{{__index}}"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := withDefaults(tt.in)

			if got.MaxCases != tt.want.MaxCases {
				t.Errorf("MaxCases = %d, want %d", got.MaxCases, tt.want.MaxCases)
			}
			if got.CenterPoints != tt.want.CenterPoints {
				t.Errorf("CenterPoints = %d, want %d", got.CenterPoints, tt.want.CenterPoints)
			}
			if got.JobNameTemplate != tt.want.JobNameTemplate {
				t.Errorf("JobNameTemplate = %q, want %q", got.JobNameTemplate, tt.want.JobNameTemplate)
			}
		})
	}
}
