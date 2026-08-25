package pattern

import (
	"strings"
	"testing"
)

func TestExtractTokens(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{"empty", "", nil},
		{"no tokens", "starccm+ -load input.sim", nil},
		{
			"two tokens",
			"starccm+ -param alpha {{alpha}} -param beta {{beta}} -load input.sim",
			[]string{"alpha", "beta"},
		},
		{
			"repeat collapses to one, first-appearance order",
			"run {{beta}} {{alpha}} {{beta}}",
			[]string{"beta", "alpha"},
		},
		{"inner whitespace tolerated", "run {{ alpha }} {{beta }}", []string{"alpha", "beta"}},
		{"underscores, hyphens and dots", "run {{inlet_velocity}} {{mesh-size}} {{bc.inlet}}", []string{"inlet_velocity", "mesh-size", "bc.inlet"}},
		{"case sensitive", "run {{Alpha}} {{alpha}}", []string{"Alpha", "alpha"}},
		{"digits allowed after first char", "run {{x1}} {{x2}}", []string{"x1", "x2"}},

		// Malformed placeholders are not tokens.
		{"single braces", "run {alpha}", nil},
		{"unclosed", "run {{alpha", nil},
		{"leading digit rejected", "run {{1alpha}}", nil},
		{"empty name rejected", "run {{}}", nil},
		{"spaces only rejected", "run {{   }}", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractTokens(tt.command)
			if len(got) != len(tt.want) {
				t.Fatalf("ExtractTokens(%q) = %v, want %v", tt.command, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ExtractTokens(%q)[%d] = %q, want %q", tt.command, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestHasTokens(t *testing.T) {
	if HasTokens("starccm+ -load input.sim") {
		t.Error("HasTokens reported true for a command with no tokens")
	}
	if !HasTokens("run {{alpha}}") {
		t.Error("HasTokens reported false for a command with a token")
	}
}

// The motivating case: parameter values must land in the command line so they
// are visible on the Rescale job page.
func TestSubstituteTokens_RendersIntoCommand(t *testing.T) {
	template := "starccm+ -param alpha {{alpha}} -param beta {{beta}} -load input.sim"
	got := SubstituteTokens(template, map[string]string{"alpha": "10", "beta": "15"})
	want := "starccm+ -param alpha 10 -param beta 15 -load input.sim"

	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestSubstituteTokens(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		values   map[string]string
		want     string
		residual []string
	}{
		{
			name:    "empty command",
			command: "",
			values:  map[string]string{"alpha": "10"},
			want:    "",
		},
		{
			name:    "no values leaves command untouched",
			command: "run {{alpha}}",
			values:  nil,
			want:    "run {{alpha}}",
			// Residual, so the caller can reject the render.
			residual: []string{"alpha"},
		},
		{
			name:    "repeated token replaced everywhere",
			command: "run --in {{alpha}} --out {{alpha}}",
			values:  map[string]string{"alpha": "10"},
			want:    "run --in 10 --out 10",
		},
		{
			name:    "inner whitespace form substitutes",
			command: "run {{ alpha }}",
			values:  map[string]string{"alpha": "10"},
			want:    "run 10",
		},
		{
			name:     "unknown token left verbatim",
			command:  "run {{alpha}} {{gamma}}",
			values:   map[string]string{"alpha": "10"},
			want:     "run 10 {{gamma}}",
			residual: []string{"gamma"},
		},
		{
			name:    "empty value substitutes to nothing",
			command: "run -a {{alpha}} -b",
			values:  map[string]string{"alpha": ""},
			want:    "run -a  -b",
		},
		{
			name:    "unused values are ignored",
			command: "run {{alpha}}",
			values:  map[string]string{"alpha": "10", "unused": "99"},
			want:    "run 10",
		},
		{
			name:    "negative and decimal values",
			command: "run {{alpha}} {{beta}}",
			values:  map[string]string{"alpha": "-1.5", "beta": "1e-3"},
			want:    "run -1.5 1e-3",
		},
		{
			name:    "case sensitive lookup",
			command: "run {{Alpha}} {{alpha}}",
			values:  map[string]string{"Alpha": "big", "alpha": "small"},
			want:    "run big small",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SubstituteTokens(tt.command, tt.values)
			if got != tt.want {
				t.Errorf("SubstituteTokens() = %q, want %q", got, tt.want)
			}

			residual := ExtractTokens(got)
			if len(residual) != len(tt.residual) {
				t.Fatalf("residual tokens = %v, want %v", residual, tt.residual)
			}
			for i := range residual {
				if residual[i] != tt.residual[i] {
					t.Errorf("residual[%d] = %q, want %q", i, residual[i], tt.residual[i])
				}
			}
		})
	}
}

// Substitution must not rescan what it inserted, or a value could smuggle in
// another token and make the render depend on map iteration order.
func TestSubstituteTokens_IsSinglePass(t *testing.T) {
	got := SubstituteTokens("run {{alpha}}", map[string]string{
		"alpha": "{{beta}}",
		"beta":  "should-not-appear",
	})

	if got != "run {{beta}}" {
		t.Errorf("SubstituteTokens() = %q, want %q", got, "run {{beta}}")
	}
	if strings.Contains(got, "should-not-appear") {
		t.Error("substitution recursed into an inserted value")
	}
}

// A value containing braces is inserted literally rather than becoming a token.
func TestSubstituteTokens_ValueWithBracesNotReparsed(t *testing.T) {
	got := SubstituteTokens("run {{alpha}}", map[string]string{"alpha": "{{alpha}}"})
	if got != "run {{alpha}}" {
		t.Errorf("SubstituteTokens() = %q, want %q", got, "run {{alpha}}")
	}
}

// Named tokens and numeric iteration are independent: substituting tokens must
// not disturb the numeric patterns that IterateCommandPatterns looks for.
func TestSubstituteTokens_LeavesNumericPatternsAlone(t *testing.T) {
	got := SubstituteTokens("solve -case run_1 -alpha {{alpha}}", map[string]string{"alpha": "10"})
	want := "solve -case run_1 -alpha 10"
	if got != want {
		t.Errorf("SubstituteTokens() = %q, want %q", got, want)
	}
}
