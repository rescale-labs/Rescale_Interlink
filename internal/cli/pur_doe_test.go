package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/pur/doe"
)

func TestParseParamSpec_NumericRange(t *testing.T) {
	tests := []struct {
		name       string
		spec       string
		wantName   string
		wantMin    float64
		wantMax    float64
		wantLevels int
	}{
		{"range with levels", "alpha=10:20:5", "alpha", 10, 20, 5},
		{"range without levels", "alpha=10:20", "alpha", 10, 20, 0},
		{"decimals", "alpha=0.5:1.25:3", "alpha", 0.5, 1.25, 3},
		{"negatives", "alpha=-5:-1:2", "alpha", -5, -1, 2},
		{"exponents", "alpha=1e-3:1e-1", "alpha", 0.001, 0.1, 0},
		{"whitespace tolerated", " alpha = 10 : 20 : 4 ", "alpha", 10, 20, 4},
		{"dotted name", "bc.inlet=1:2", "bc.inlet", 1, 2, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseParamSpec(tt.spec)
			if err != nil {
				t.Fatalf("parseParamSpec(%q) failed: %v", tt.spec, err)
			}

			if got.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tt.wantName)
			}
			if got.Min != tt.wantMin || got.Max != tt.wantMax {
				t.Errorf("range = %v:%v, want %v:%v", got.Min, got.Max, tt.wantMin, tt.wantMax)
			}
			if got.Levels != tt.wantLevels {
				t.Errorf("Levels = %d, want %d", got.Levels, tt.wantLevels)
			}
			if got.Values != nil {
				t.Errorf("Values = %v, want nil for a numeric range", got.Values)
			}
		})
	}
}

func TestParseParamSpec_Categorical(t *testing.T) {
	tests := []struct {
		name       string
		spec       string
		wantName   string
		wantValues []string
	}{
		{"list", "model=kepsilon,komega,les", "model", []string{"kepsilon", "komega", "les"}},
		{"single value", "model=kepsilon", "model", []string{"kepsilon"}},
		{"whitespace trimmed", "model= a , b ", "model", []string{"a", "b"}},
		{"empty entries dropped", "model=a,,b,", "model", []string{"a", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseParamSpec(tt.spec)
			if err != nil {
				t.Fatalf("parseParamSpec(%q) failed: %v", tt.spec, err)
			}

			if got.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tt.wantName)
			}
			if len(got.Values) != len(tt.wantValues) {
				t.Fatalf("Values = %v, want %v", got.Values, tt.wantValues)
			}
			for i := range tt.wantValues {
				if got.Values[i] != tt.wantValues[i] {
					t.Errorf("Values[%d] = %q, want %q", i, got.Values[i], tt.wantValues[i])
				}
			}
		})
	}
}

func TestParseParamSpec_Rejects(t *testing.T) {
	specs := []string{
		"alpha",           // no domain
		"=10:20",          // no name
		"alpha=",          // empty domain
		"alpha=10:20:5:6", // too many range parts
		"alpha=x:20",      // non-numeric minimum
		"alpha=10:y",      // non-numeric maximum
		"alpha=10:20:many",
		"alpha=,",
	}

	for _, spec := range specs {
		t.Run(spec, func(t *testing.T) {
			if _, err := parseParamSpec(spec); err == nil {
				t.Errorf("parseParamSpec(%q) succeeded, want an error", spec)
			}
		})
	}
}

func TestParseParamSpecs_AppliesFormats(t *testing.T) {
	params, err := parseParamSpecs(
		[]string{"alpha=10:20:3", "beta=1:2"},
		map[string]string{"alpha": "%.3f"},
	)
	if err != nil {
		t.Fatalf("parseParamSpecs() failed: %v", err)
	}

	if len(params) != 2 {
		t.Fatalf("got %d parameters, want 2", len(params))
	}
	if params[0].Format != "%.3f" {
		t.Errorf("alpha Format = %q, want %q", params[0].Format, "%.3f")
	}
	if params[1].Format != "" {
		t.Errorf("beta Format = %q, want empty", params[1].Format)
	}
}

// Order matters: designs treat parameters in the order they were declared, so the
// flag order has to survive parsing.
func TestParseParamSpecs_PreservesFlagOrder(t *testing.T) {
	params, err := parseParamSpecs([]string{"c=1:2", "a=1:2", "b=1:2"}, nil)
	if err != nil {
		t.Fatalf("parseParamSpecs() failed: %v", err)
	}

	want := []string{"c", "a", "b"}
	for i := range want {
		if params[i].Name != want[i] {
			t.Errorf("parameter %d = %q, want %q", i, params[i].Name, want[i])
		}
	}
}

func TestParseParamFormats(t *testing.T) {
	formats, err := parseParamFormats([]string{"alpha=%.3f", "beta=%06.2f"})
	if err != nil {
		t.Fatalf("parseParamFormats() failed: %v", err)
	}

	if formats["alpha"] != "%.3f" || formats["beta"] != "%06.2f" {
		t.Errorf("formats = %v", formats)
	}
}

func TestParseParamFormats_Rejects(t *testing.T) {
	specs := [][]string{
		{"alpha"},                    // no format
		{"=%.3f"},                    // no name
		{"alpha="},                   // empty format
		{"alpha=%.3f", "alpha=%.1f"}, // duplicate
	}

	for _, spec := range specs {
		if _, err := parseParamFormats(spec); err == nil {
			t.Errorf("parseParamFormats(%v) succeeded, want an error", spec)
		}
	}
}

// A format naming a parameter that is not swept is a typo that would otherwise be
// silently ignored.
func TestCheckFormatsMatchParameters(t *testing.T) {
	params := []doe.Parameter{{Name: "alpha"}, {Name: "beta"}}

	if err := checkFormatsMatchParameters(map[string]string{"alpha": "%.3f"}, params); err != nil {
		t.Errorf("unexpected error for a matching format: %v", err)
	}
	if err := checkFormatsMatchParameters(map[string]string{"gamma": "%.3f"}, params); err == nil {
		t.Error("expected an error for a format naming an unswept parameter")
	}
}

func TestLoadCasesCSV(t *testing.T) {
	path := writeTempCSV(t, "cases.csv", "alpha,beta\n10,15\n20,25\n")

	names, cases, err := loadCasesCSV(path)
	if err != nil {
		t.Fatalf("loadCasesCSV() failed: %v", err)
	}

	if len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("names = %v, want [alpha beta]", names)
	}
	if len(cases) != 2 {
		t.Fatalf("got %d cases, want 2", len(cases))
	}
	if cases[0]["alpha"] != "10" || cases[0]["beta"] != "15" {
		t.Errorf("case 1 = %v", cases[0])
	}
	if cases[1]["alpha"] != "20" || cases[1]["beta"] != "25" {
		t.Errorf("case 2 = %v", cases[1])
	}
}

func TestLoadCasesCSV_TrimsAndSkipsBlankLines(t *testing.T) {
	path := writeTempCSV(t, "cases.csv", " alpha , beta \n 10 , 15 \n\n20,25\n")

	names, cases, err := loadCasesCSV(path)
	if err != nil {
		t.Fatalf("loadCasesCSV() failed: %v", err)
	}

	if names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("names = %v, want trimmed [alpha beta]", names)
	}
	if len(cases) != 2 {
		t.Fatalf("got %d cases, want 2", len(cases))
	}
	if cases[0]["alpha"] != "10" {
		t.Errorf("alpha = %q, want %q", cases[0]["alpha"], "10")
	}
}

func TestLoadCasesCSV_Rejects(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"header only", "alpha,beta\n"},
		{"empty file", ""},
		{"unnamed column", "alpha,\n10,15\n"},
		{"duplicate column", "alpha,alpha\n10,15\n"},
		{"short row", "alpha,beta\n10\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempCSV(t, "cases.csv", tt.content)

			if _, _, err := loadCasesCSV(path); err == nil {
				t.Error("loadCasesCSV() succeeded, want an error")
			}
		})
	}
}

func TestLoadCasesCSV_MissingFile(t *testing.T) {
	if _, _, err := loadCasesCSV(filepath.Join(t.TempDir(), "absent.csv")); err == nil {
		t.Error("loadCasesCSV() succeeded for a missing file")
	}
}

func TestTruncateField(t *testing.T) {
	tests := []struct {
		in    string
		width int
		want  string
	}{
		{"short", 10, "short"},
		{"exactly-10", 10, "exactly-10"},
		{"much-too-long-name", 10, "much-to..."},
		{"abc", 2, "ab"},
	}

	for _, tt := range tests {
		if got := truncateField(tt.in, tt.width); got != tt.want {
			t.Errorf("truncateField(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
		}
	}
}

// doeMethodList feeds the --method flag help, so it must stay in step with the
// methods the package actually supports.
func TestDOEMethodList_ListsEveryMethod(t *testing.T) {
	list := doeMethodList()

	for _, m := range doe.AllMethods() {
		if !strings.Contains(list, string(m)) {
			t.Errorf("method list %q is missing %q", list, m)
		}
	}
}

func writeTempCSV(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
	return path
}
