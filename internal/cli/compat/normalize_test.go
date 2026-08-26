package compat

import (
	"slices"
	"testing"
)

func TestNormalizeCompatArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		// rescale-cli multi-character short flags map to long flags.
		{"fid to file-id", []string{"download-file", "-fid", "ABC123"}, []string{"download-file", "--file-id", "ABC123"}},
		{"lh to load-hours", []string{"status", "-lh", "24", "-j", "JOB1"}, []string{"status", "--load-hours", "24", "-j", "JOB1"}},
		{"no substitution needed", []string{"status", "-j", "JOB1"}, []string{"status", "-j", "JOB1"}},
		{"both fid and lh", []string{"-lh", "24", "download-file", "-fid", "ABC"}, []string{"--load-hours", "24", "download-file", "--file-id", "ABC"}},

		// upload -f is multi-value: each bare value gets its own -f.
		{"upload: multiple files after -f", []string{"upload", "-f", "a.txt", "b.txt", "c.txt"}, []string{"upload", "-f", "a.txt", "-f", "b.txt", "-f", "c.txt"}},
		{"upload: multi-value terminated by a flag", []string{"upload", "-f", "a.txt", "b.txt", "-d", "DIR"}, []string{"upload", "-f", "a.txt", "-f", "b.txt", "-d", "DIR"}},
		{"upload: single value unchanged", []string{"upload", "-f", "a.txt"}, []string{"upload", "-f", "a.txt"}},
		{"upload: root flags before the subcommand", []string{"-p", "TOKEN", "upload", "-f", "a.txt", "b.txt"}, []string{"-p", "TOKEN", "upload", "-f", "a.txt", "-f", "b.txt"}},
		{"upload: -f at end of args", []string{"upload", "-f"}, []string{"upload", "-f"}},

		{"submit: -f is multi-value too", []string{"submit", "-f", "a.txt", "b.txt", "c.txt"}, []string{"submit", "-f", "a.txt", "-f", "b.txt", "-f", "c.txt"}},

		// download-file's -f is single-value, so it must not be expanded.
		{"download-file: -f is not expanded", []string{"download-file", "-f", "name", "-j", "JOB1"}, []string{"download-file", "-f", "name", "-j", "JOB1"}},

		{
			"realistic combined invocation",
			[]string{"-p", "TOKEN", "upload", "-f", "a.txt", "b.txt", "c.txt", "-d", "DIR"},
			[]string{"-p", "TOKEN", "upload", "-f", "a.txt", "-f", "b.txt", "-f", "c.txt", "-d", "DIR"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeCompatArgs(tt.args)
			if !slices.Equal(got, tt.want) {
				t.Errorf("NormalizeCompatArgs(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestDetectSubcommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"simple", []string{"upload"}, "upload"},
		{"with root flag", []string{"-p", "TOKEN", "upload"}, "upload"},
		{"with two root flags", []string{"-X", "URL", "-p", "TOKEN", "submit"}, "submit"},
		{"flag only", []string{"-p", "TOKEN"}, ""},
		{"empty", nil, ""},
		{"boolean flags before", []string{"-q", "status"}, "status"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectSubcommand(tt.args)
			if got != tt.want {
				t.Errorf("detectSubcommand(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestExpandMultiValueFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		flag string
		want []string
	}{
		{
			"basic expansion",
			[]string{"-f", "a", "b", "c"},
			"-f",
			[]string{"-f", "a", "-f", "b", "-f", "c"},
		},
		{
			"terminated by flag",
			[]string{"-f", "a", "b", "-d", "dir"},
			"-f",
			[]string{"-f", "a", "-f", "b", "-d", "dir"},
		},
		{
			"single value",
			[]string{"-f", "a"},
			"-f",
			[]string{"-f", "a"},
		},
		{
			"no flag present",
			[]string{"-d", "dir"},
			"-f",
			[]string{"-d", "dir"},
		},
		{
			"flag at end",
			[]string{"-f"},
			"-f",
			[]string{"-f"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandMultiValueFlag(tt.args, tt.flag)
			if len(got) != len(tt.want) {
				t.Fatalf("expandMultiValueFlag(%v, %q) len = %d, want %d\ngot: %v", tt.args, tt.flag, len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestContainsInsensitive(t *testing.T) {
	if !containsInsensitive("RESULTS.dat", "results") {
		t.Error("expected case-insensitive match")
	}
	if containsInsensitive("output.log", "results") {
		t.Error("expected no match")
	}
}

func TestMatchesE2EFilters(t *testing.T) {
	// Include filter
	if !matchesE2EFilters("output.dat", []string{"*.dat"}, "", "") {
		t.Error("expected *.dat to match output.dat")
	}
	if matchesE2EFilters("output.log", []string{"*.dat"}, "", "") {
		t.Error("expected *.dat to not match output.log")
	}

	// Exclude filter (substring match, not glob)
	if matchesE2EFilters("debug.log", nil, "debug", "") {
		t.Error("expected 'debug' exclude to filter debug.log")
	}
	if matchesE2EFilters("build.log", nil, "log", "") {
		t.Error("expected 'log' exclude to filter build.log")
	}
	if !matchesE2EFilters("results.dat", nil, "log", "") {
		t.Error("expected 'log' exclude to NOT filter results.dat")
	}

	// Search filter
	if !matchesE2EFilters("final_results.dat", nil, "", "results") {
		t.Error("expected search 'results' to match")
	}
	if matchesE2EFilters("output.dat", nil, "", "results") {
		t.Error("expected search 'results' to not match output.dat")
	}

	// No filters = pass all
	if !matchesE2EFilters("anything.xyz", nil, "", "") {
		t.Error("expected no filters to pass everything")
	}
}
