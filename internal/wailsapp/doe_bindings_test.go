package wailsapp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/pur/doe"
)

// doeTemplate is the base job the binding tests sweep from.
func doeTemplate() JobSpecDTO {
	return JobSpecDTO{
		JobName:         "sweep",
		Directory:       "/data/base",
		AnalysisCode:    "starccm_plus",
		AnalysisVersion: "18.06.006",
		Command:         "starccm+ -param alpha {{alpha}} -param beta {{beta}} -load input.sim",
		CoreType:        "emerald",
		CoresPerSlot:    36,
		WalltimeHours:   4,
		Slots:           1,
	}
}

func doeOptions() DOEOptionsDTO {
	return DOEOptionsDTO{
		Template: doeTemplate(),
		Parameters: []DOEParameterDTO{
			{Name: "alpha", Min: 10, Max: 20, Levels: 2},
			{Name: "beta", Min: 15, Max: 25, Levels: 2},
		},
		Method: string(doe.MethodFullFactorial),
	}
}

// Generation needs no engine: it is pure, which is what lets the frontend call
// it on every keystroke.
func TestGenerateDOE_NeedsNoEngine(t *testing.T) {
	app := &App{}

	result := app.GenerateDOE(doeOptions())

	if !result.OK {
		t.Fatalf("GenerateDOE failed: %v", result.Errors)
	}
	if result.CaseCount != 4 {
		t.Errorf("CaseCount = %d, want 4", result.CaseCount)
	}
	if len(result.Jobs) != 4 {
		t.Fatalf("got %d jobs, want 4", len(result.Jobs))
	}

	// The point of the feature: each job's own values are in its command line.
	want := "starccm+ -param alpha 10 -param beta 15 -load input.sim"
	if result.Jobs[0].Command != want {
		t.Errorf("job 1 command = %q, want %q", result.Jobs[0].Command, want)
	}
	if result.Jobs[0].JobName != "sweep_1" {
		t.Errorf("job 1 name = %q, want %q", result.Jobs[0].JobName, "sweep_1")
	}
}

func TestGenerateDOE_ReportsValidationProblems(t *testing.T) {
	app := &App{}

	opts := doeOptions()
	opts.Parameters = append(opts.Parameters, DOEParameterDTO{Name: "gamma", Min: 1, Max: 2, Levels: 2})

	result := app.GenerateDOE(opts)

	if result.OK {
		t.Fatal("expected a parameter with no command token to be rejected")
	}
	if len(result.Errors) == 0 {
		t.Fatal("expected at least one error")
	}
	if result.Errors[0].Code != doe.CodeMissingToken {
		t.Errorf("error code = %q, want %q", result.Errors[0].Code, doe.CodeMissingToken)
	}
	if result.Errors[0].Param != "gamma" {
		t.Errorf("error param = %q, want %q", result.Errors[0].Param, "gamma")
	}
	if len(result.Jobs) != 0 {
		t.Errorf("got %d jobs for a rejected sweep, want none", len(result.Jobs))
	}
}

// Shared input files are the reason a sweep does not re-upload its deck per
// case, so the binding has to pass them through.
func TestGenerateDOE_SharedFileIDsClearDirectory(t *testing.T) {
	app := &App{}

	opts := doeOptions()
	opts.BaseFileIDs = []string{"abcde", "fghij"}

	result := app.GenerateDOE(opts)
	if !result.OK {
		t.Fatalf("GenerateDOE failed: %v", result.Errors)
	}

	for i, job := range result.Jobs {
		if job.Directory != "" {
			t.Errorf("job %d has Directory %q, want empty so tar and upload are skipped", i+1, job.Directory)
		}
		if len(job.InputFiles) != 2 || job.InputFiles[0] != "abcde" {
			t.Errorf("job %d InputFiles = %v, want the shared IDs", i+1, job.InputFiles)
		}
	}
}

func TestGenerateDOE_RendersTagTemplates(t *testing.T) {
	app := &App{}

	opts := doeOptions()
	opts.TagTemplates = []string{"alpha={{alpha}}", "sweep-{{__index}}"}

	result := app.GenerateDOE(opts)
	if !result.OK {
		t.Fatalf("GenerateDOE failed: %v", result.Errors)
	}

	tags := strings.Join(result.Cases[0].Tags, ",")
	if !strings.Contains(tags, "alpha=10") || !strings.Contains(tags, "sweep-1") {
		t.Errorf("case 1 tags = %v, want the rendered templates", result.Cases[0].Tags)
	}
}

func TestPreviewDOECases_TruncatesAndOmitsJobs(t *testing.T) {
	app := &App{}

	opts := doeOptions()
	opts.Parameters[0].Levels = 5
	opts.Parameters[1].Levels = 5

	result := app.PreviewDOECases(opts, 3)
	if !result.OK {
		t.Fatalf("PreviewDOECases failed: %v", result.Errors)
	}

	if result.CaseCount != 25 {
		t.Errorf("CaseCount = %d, want the full design size 25", result.CaseCount)
	}
	if len(result.Cases) != 3 {
		t.Errorf("got %d cases, want the limit of 3", len(result.Cases))
	}
	if !result.Truncated {
		t.Error("Truncated = false, want true when cases were cut")
	}
	if len(result.Jobs) != 0 {
		t.Errorf("preview returned %d jobs, want none", len(result.Jobs))
	}
}

func TestPreviewDOECases_ZeroLimitReturnsEverything(t *testing.T) {
	app := &App{}

	result := app.PreviewDOECases(doeOptions(), 0)

	if len(result.Cases) != 4 {
		t.Errorf("got %d cases, want all 4", len(result.Cases))
	}
	if result.Truncated {
		t.Error("Truncated = true for an untruncated preview")
	}
}

// The method menu drives which fields the form shows, so it has to describe
// every method the package supports.
func TestGetDOEMethods_DescribesEveryMethod(t *testing.T) {
	app := &App{}

	methods := app.GetDOEMethods()
	if len(methods) != len(doe.AllMethods()) {
		t.Fatalf("got %d methods, want %d", len(methods), len(doe.AllMethods()))
	}

	byName := make(map[string]DOEMethodDTO, len(methods))
	for _, m := range methods {
		if m.Label == "" || m.Description == "" {
			t.Errorf("method %q is missing its label or description", m.Method)
		}
		byName[m.Method] = m
	}

	for _, want := range doe.AllMethods() {
		if _, ok := byName[string(want)]; !ok {
			t.Errorf("method %q is missing from GetDOEMethods()", want)
		}
	}

	if !byName[string(doe.MethodLatinHypercube)].UsesSamples {
		t.Error("latin-hypercube should report UsesSamples")
	}
	if byName[string(doe.MethodFullFactorial)].UsesSamples {
		t.Error("full-factorial should not report UsesSamples")
	}
	if byName[string(doe.MethodBoxBehnken)].MinParameters != 3 {
		t.Error("box-behnken should report a minimum of 3 parameters")
	}
}

func TestDefaultDOEMaxCases(t *testing.T) {
	app := &App{}

	if got := app.DefaultDOEMaxCases(); got != doe.DefaultMaxCases {
		t.Errorf("DefaultDOEMaxCases() = %d, want %d", got, doe.DefaultMaxCases)
	}
}

// The DTOs cross the Wails boundary as JSON, so an empty case list has to
// serialize as [] rather than null for the frontend to map over it.
func TestDOEResultDTO_SerializesEmptyCasesAsArray(t *testing.T) {
	app := &App{}

	opts := doeOptions()
	opts.Parameters = nil

	data, err := json.Marshal(app.GenerateDOE(opts))
	if err != nil {
		t.Fatalf("failed to marshal result: %v", err)
	}

	if !strings.Contains(string(data), `"cases":[]`) {
		t.Errorf("expected an empty cases array, got %s", data)
	}
}
