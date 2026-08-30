package doe

import (
	"strings"
	"testing"
)

func TestParseCasesCSV(t *testing.T) {
	names, cases, err := ParseCasesCSV(strings.NewReader("alpha,beta\n10,15\n20,25\n"))
	if err != nil {
		t.Fatalf("ParseCasesCSV() failed: %v", err)
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

func TestParseCasesCSV_TrimsAndSkipsBlankLines(t *testing.T) {
	names, cases, err := ParseCasesCSV(strings.NewReader(" alpha , beta \n 10 , 15 \n\n20,25\n"))
	if err != nil {
		t.Fatalf("ParseCasesCSV() failed: %v", err)
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

// A quoted field carrying a comma is one value, not two columns. This is the
// divergence the GUI's own splitter used to introduce: the same text has to mean
// the same sweep whichever surface it was pasted or loaded into.
func TestParseCasesCSV_QuotedCommaIsOneValue(t *testing.T) {
	_, cases, err := ParseCasesCSV(strings.NewReader("alpha,beta\n\"x,y\",2\n"))
	if err != nil {
		t.Fatalf("ParseCasesCSV() failed: %v", err)
	}

	if len(cases) != 1 || cases[0]["alpha"] != "x,y" || cases[0]["beta"] != "2" {
		t.Errorf("cases = %v, want alpha=%q beta=%q", cases, "x,y", "2")
	}
}

func TestParseCasesCSV_Rejects(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"header only", "alpha,beta\n"},
		{"empty input", ""},
		{"unnamed column", "alpha,\n10,15\n"},
		{"duplicate column", "alpha,alpha\n10,15\n"},
		{"short row", "alpha,beta\n10\n"},
		{"past the byte ceiling", "alpha\n" + strings.Repeat("x", maxCasesCSVBytes)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := ParseCasesCSV(strings.NewReader(tt.content)); err == nil {
				t.Error("ParseCasesCSV() succeeded, want an error")
			}
		})
	}
}
