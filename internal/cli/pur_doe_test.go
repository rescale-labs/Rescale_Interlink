package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/pur/doe"
)

// A domain containing ':' is a numeric range and anything else is a categorical
// list, so both forms are one table over the same parse.
func TestParseParamSpec(t *testing.T) {
	tests := []struct {
		name string
		spec string
		want doe.Parameter
	}{
		{"range with levels", "alpha=10:20:5", doe.Parameter{Name: "alpha", Min: 10, Max: 20, Levels: 5}},
		{"range without levels", "alpha=10:20", doe.Parameter{Name: "alpha", Min: 10, Max: 20}},
		{"decimals", "alpha=0.5:1.25:3", doe.Parameter{Name: "alpha", Min: 0.5, Max: 1.25, Levels: 3}},
		{"negatives", "alpha=-5:-1:2", doe.Parameter{Name: "alpha", Min: -5, Max: -1, Levels: 2}},
		{"exponents", "alpha=1e-3:1e-1", doe.Parameter{Name: "alpha", Min: 0.001, Max: 0.1}},
		{"whitespace tolerated", " alpha = 10 : 20 : 4 ", doe.Parameter{Name: "alpha", Min: 10, Max: 20, Levels: 4}},
		{"dotted name", "bc.inlet=1:2", doe.Parameter{Name: "bc.inlet", Min: 1, Max: 2}},
		{"categorical list", "model=kepsilon,komega,les",
			doe.Parameter{Name: "model", Values: []string{"kepsilon", "komega", "les"}}},
		{"categorical single value", "model=kepsilon", doe.Parameter{Name: "model", Values: []string{"kepsilon"}}},
		{"categorical whitespace trimmed", "model= a , b ", doe.Parameter{Name: "model", Values: []string{"a", "b"}}},
		{"categorical empty entries dropped", "model=a,,b,", doe.Parameter{Name: "model", Values: []string{"a", "b"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseParamSpec(tt.spec)
			if err != nil {
				t.Fatalf("parseParamSpec(%q) failed: %v", tt.spec, err)
			}

			if got.Name != tt.want.Name {
				t.Errorf("Name = %q, want %q", got.Name, tt.want.Name)
			}
			if got.Min != tt.want.Min || got.Max != tt.want.Max {
				t.Errorf("range = %v:%v, want %v:%v", got.Min, got.Max, tt.want.Min, tt.want.Max)
			}
			if got.Levels != tt.want.Levels {
				t.Errorf("Levels = %d, want %d", got.Levels, tt.want.Levels)
			}
			if len(got.Values) != len(tt.want.Values) {
				t.Fatalf("Values = %v, want %v", got.Values, tt.want.Values)
			}
			for i := range tt.want.Values {
				if got.Values[i] != tt.want.Values[i] {
					t.Errorf("Values[%d] = %q, want %q", i, got.Values[i], tt.want.Values[i])
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

// The parsing itself is the doe package's; what the CLI adds is opening the
// file, so that is what is checked here.
func TestParseCasesCSVFile(t *testing.T) {
	path := writeTempFile(t, "cases.csv", "alpha,beta\n10,15\n20,25\n")

	names, cases, err := parseCasesCSVFile(path)
	if err != nil {
		t.Fatalf("parseCasesCSVFile() failed: %v", err)
	}
	if len(names) != 2 || len(cases) != 2 || cases[0]["alpha"] != "10" {
		t.Errorf("names = %v, cases = %v", names, cases)
	}

	if _, _, err := parseCasesCSVFile(filepath.Join(t.TempDir(), "absent.csv")); err == nil {
		t.Error("parseCasesCSVFile() succeeded for a missing file")
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

// writeTempFile writes content to a fresh temp directory under name and
// returns the full path.
func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
	return path
}
