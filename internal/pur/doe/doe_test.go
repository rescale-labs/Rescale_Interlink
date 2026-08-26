package doe

import (
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

// A base job already carrying an index suffix must not accumulate another.
func TestGenerate_StripsIndexSuffixFromBaseName(t *testing.T) {
	opts := twoLevelOptions()
	opts.Template.JobName = "sweep_7"

	result := mustGenerate(t, opts)

	if result.Cases[0].JobName != "sweep_1" {
		t.Errorf("first case name = %q, want %q", result.Cases[0].JobName, "sweep_1")
	}
}

func TestGenerate_CustomJobNameTemplate(t *testing.T) {
	opts := twoLevelOptions()
	opts.JobNameTemplate = "{{__base}}_a{{alpha}}_b{{beta}}"

	result := mustGenerate(t, opts)

	if result.Cases[0].JobName != "sweep_a10_b15" {
		t.Errorf("name = %q, want %q", result.Cases[0].JobName, "sweep_a10_b15")
	}
}

// A job name template that leaves out the index collides as soon as two cases
// share the values it does use.
func TestGenerate_RejectsCollidingJobNames(t *testing.T) {
	opts := twoLevelOptions()
	opts.JobNameTemplate = "{{__base}}_a{{alpha}}"

	result := Generate(opts)

	if result.OK() {
		t.Fatal("expected colliding job names to be rejected")
	}
	if !hasCode(result.Errors, CodeDupJobName) {
		t.Errorf("errors = %v, want a %s", problemStrings(result.Errors), CodeDupJobName)
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

// Shared inputs are the point of a sweep: one input deck, many parameter sets.
// Referencing already-uploaded file IDs puts every case on the pipeline's skip
// path so the deck transfers once rather than once per case.
func TestGenerate_SharedFileIDsBypassTarAndUpload(t *testing.T) {
	opts := twoLevelOptions()
	opts.BaseFileIDs = []string{"file_abc", "file_def"}

	result := mustGenerate(t, opts)

	for i, job := range result.Jobs {
		if job.Directory != "" {
			t.Errorf("job %d Directory = %q, want empty so the pipeline skips tar/upload", i, job.Directory)
		}
		if len(job.InputFiles) != 2 || job.InputFiles[0] != "file_abc" || job.InputFiles[1] != "file_def" {
			t.Errorf("job %d InputFiles = %v, want the shared file IDs", i, job.InputFiles)
		}
	}

	// The returned slices must not alias the caller's, or mutating one case's
	// inputs would change every other case.
	result.Jobs[0].InputFiles[0] = "mutated"
	if opts.BaseFileIDs[0] != "file_abc" {
		t.Error("job InputFiles aliases Options.BaseFileIDs")
	}
	if result.Jobs[1].InputFiles[0] != "file_abc" {
		t.Error("jobs share one InputFiles backing array")
	}
}

// A sweep never tars a per-case directory: even without shared file IDs, cases
// carry no Directory so the pipeline skips tar/upload and the shared deck is
// supplied once via batch-level Common Files instead of zipped per case.
func TestGenerate_NeverCarriesTemplateDirectory(t *testing.T) {
	result := mustGenerate(t, twoLevelOptions())

	for i, job := range result.Jobs {
		if job.Directory != "" {
			t.Errorf("job %d Directory = %q, want empty so the pipeline skips tar/upload", i, job.Directory)
		}
		if len(job.InputFiles) != 0 {
			t.Errorf("job %d InputFiles = %v, want none", i, job.InputFiles)
		}
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

// An unknown token in a name or tag is cosmetic, so it warns rather than blocks.
func TestGenerate_UnknownTokenInJobNameWarnsOnly(t *testing.T) {
	opts := twoLevelOptions()
	opts.JobNameTemplate = "{{__base}}_{{__index}}_{{nope}}"

	result := Generate(opts)

	if !result.OK() {
		t.Fatalf("expected only a warning, got errors: %v", problemStrings(result.Errors))
	}
	if !hasCode(result.Warnings, CodeUndeclaredToken) {
		t.Errorf("warnings = %v, want a %s", problemStrings(result.Warnings), CodeUndeclaredToken)
	}
	// The unresolved token survives into the name rather than being dropped.
	if !strings.Contains(result.Cases[0].JobName, "{{nope}}") {
		t.Errorf("name = %q, want the unresolved token left visible", result.Cases[0].JobName)
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

func TestGenerate_EnforcesMaxCases(t *testing.T) {
	opts := twoLevelOptions()
	opts.Parameters[0].Levels = 10
	opts.Parameters[1].Levels = 10
	opts.MaxCases = 50

	result := Generate(opts)

	if result.OK() {
		t.Fatal("expected 100 cases to exceed a limit of 50")
	}
	if !hasCode(result.Errors, CodeTooManyCases) {
		t.Errorf("errors = %v, want a %s", problemStrings(result.Errors), CodeTooManyCases)
	}
}

func TestGenerate_DefaultMaxCasesApplies(t *testing.T) {
	opts := twoLevelOptions()
	opts.Parameters[0].Levels = 40
	opts.Parameters[1].Levels = 40 // 1600 cases

	result := Generate(opts)

	if result.OK() {
		t.Fatalf("expected the default limit of %d to reject 1600 cases", DefaultMaxCases)
	}
}

func TestGenerate_NegativeMaxCasesDisablesTheLimit(t *testing.T) {
	opts := twoLevelOptions()
	opts.Parameters[0].Levels = 40
	opts.Parameters[1].Levels = 40
	opts.MaxCases = -1

	result := mustGenerate(t, opts)

	if len(result.Cases) != 1600 {
		t.Errorf("got %d cases, want 1600", len(result.Cases))
	}
}

// Even with the limit disabled, a design that could never be submitted is
// rejected by arithmetic rather than by trying to allocate it.
func TestGenerate_RejectsAbsurdDesignEvenWithoutALimit(t *testing.T) {
	opts := twoLevelOptions()
	opts.MaxCases = -1
	opts.Template.Command = "run"
	opts.Parameters = nil
	for i := 0; i < 40; i++ {
		name := "p" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		opts.Parameters = append(opts.Parameters, Parameter{Name: name, Min: 0, Max: 1, Levels: 4})
		opts.Template.Command += " {{" + name + "}}"
	}

	result := Generate(opts)

	if result.OK() {
		t.Fatal("expected an unsubmittable design to be rejected")
	}
	if !hasCode(result.Errors, CodeTooManyCases) {
		t.Errorf("errors = %v, want a %s", problemStrings(result.Errors), CodeTooManyCases)
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

func TestGenerate_ExplicitRejectsIncompleteCase(t *testing.T) {
	opts := twoLevelOptions()
	opts.Method = MethodExplicit
	opts.Cases = []map[string]string{{"alpha": "10"}}

	result := Generate(opts)

	if !hasCode(result.Errors, CodeMissingToken) {
		t.Errorf("errors = %v, want a %s", problemStrings(result.Errors), CodeMissingToken)
	}
}

func TestGenerate_ExplicitRejectsUndeclaredParameter(t *testing.T) {
	opts := twoLevelOptions()
	opts.Method = MethodExplicit
	opts.Cases = []map[string]string{{"alpha": "10", "beta": "15", "gamma": "1"}}

	result := Generate(opts)

	if !hasCode(result.Errors, CodeUndeclaredToken) {
		t.Errorf("errors = %v, want a %s", problemStrings(result.Errors), CodeUndeclaredToken)
	}
}

func TestGenerate_ExplicitRejectsUnsafeValue(t *testing.T) {
	opts := twoLevelOptions()
	opts.Method = MethodExplicit
	opts.Cases = []map[string]string{{"alpha": "10", "beta": "$(whoami)"}}

	result := Generate(opts)

	if !hasCode(result.Errors, CodeUnsafeValue) {
		t.Errorf("errors = %v, want a %s", problemStrings(result.Errors), CodeUnsafeValue)
	}
}

func TestGenerate_ExplicitNeedsAtLeastOneCase(t *testing.T) {
	opts := twoLevelOptions()
	opts.Method = MethodExplicit

	result := Generate(opts)

	if !hasCode(result.Errors, CodeNoCases) {
		t.Errorf("errors = %v, want a %s", problemStrings(result.Errors), CodeNoCases)
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
	opts := withDefaults(Options{})

	if opts.MaxCases != DefaultMaxCases {
		t.Errorf("MaxCases = %d, want %d", opts.MaxCases, DefaultMaxCases)
	}
	if opts.CenterPoints != 1 {
		t.Errorf("CenterPoints = %d, want 1", opts.CenterPoints)
	}
	if opts.JobNameTemplate != "{{__base}}_{{__index}}" {
		t.Errorf("JobNameTemplate = %q", opts.JobNameTemplate)
	}
}

func TestWithDefaults_LeavesExplicitValuesAlone(t *testing.T) {
	opts := withDefaults(Options{MaxCases: -1, CenterPoints: 5, JobNameTemplate: "x{{__index}}"})

	if opts.MaxCases != -1 || opts.CenterPoints != 5 || opts.JobNameTemplate != "x{{__index}}" {
		t.Errorf("withDefaults overwrote explicit values: %+v", opts)
	}
}
