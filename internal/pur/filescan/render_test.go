package filescan

import (
	"path/filepath"
	"strings"
	"testing"
)

// jobFilesFor builds the JobFiles a scan would produce for one primary file,
// so the tests exercise the same derivation ScanFiles performs.
func jobFilesFor(primaryFile string) JobFiles {
	primaryFile = filepath.FromSlash(primaryFile)
	base := filepath.Base(primaryFile)
	return JobFiles{
		PrimaryFile: primaryFile,
		PrimaryDir:  filepath.Dir(primaryFile),
		PrimaryBase: strings.TrimSuffix(base, filepath.Ext(base)),
		InputFiles:  []string{primaryFile},
	}
}

func TestSubstitutions(t *testing.T) {
	got := Substitutions(jobFilesFor("scratch/inputs/case1.inp"), 1)

	want := map[string]string{
		TokenFile:  "case1.inp",
		TokenBase:  "case1",
		TokenExt:   "inp",
		TokenDir:   "inputs",
		TokenIndex: "1",
	}

	for token, value := range want {
		if got[token] != value {
			t.Errorf("{{%s}} = %q, want %q", token, got[token], value)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d tokens (%v), want %d", len(got), got, len(want))
	}
}

// The layout {{dir}} exists for: the filenames are identical, so only the
// containing folder tells the jobs apart.
func TestSubstitutions_DirDistinguishesIdenticalFilenames(t *testing.T) {
	one := Substitutions(jobFilesFor("runs/case1/model.inp"), 1)
	two := Substitutions(jobFilesFor("runs/case2/model.inp"), 2)

	if one[TokenFile] != two[TokenFile] {
		t.Fatalf("test premise: filenames differ (%q, %q)", one[TokenFile], two[TokenFile])
	}
	if one[TokenDir] != "case1" || two[TokenDir] != "case2" {
		t.Errorf("{{dir}} = %q and %q, want case1 and case2", one[TokenDir], two[TokenDir])
	}
}

func TestSubstitutions_NoExtension(t *testing.T) {
	got := Substitutions(jobFilesFor("inputs/Makefile"), 1)
	if got[TokenExt] != "" {
		t.Errorf("{{ext}} = %q, want empty", got[TokenExt])
	}
	if got[TokenBase] != "Makefile" {
		t.Errorf("{{base}} = %q, want Makefile", got[TokenBase])
	}
}

// A pattern like "*.inp" against the current directory leaves PrimaryDir as ".",
// whose basename is not a name. It must resolve to the real folder instead.
func TestSubstitutions_DirFromRelativeScan(t *testing.T) {
	got := Substitutions(jobFilesFor("case1.inp"), 1)
	if dir := got[TokenDir]; dir == "" || dir == "." {
		t.Errorf("{{dir}} = %q, want the working directory's name", dir)
	}
}

func TestRender_EachTokenReachesTheCommand(t *testing.T) {
	command, _, err := Render(
		"solve --in {{file}} --job {{base}} --kind {{ext}} --set {{dir}} --n {{index}}",
		"", jobFilesFor("scratch/inputs/case1.inp"), 3,
	)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	want := "solve --in case1.inp --job case1 --kind inp --set inputs --n 3"
	if command != want {
		t.Errorf("command = %q, want %q", command, want)
	}
}

// The motivating case: one template, one command per file.
func TestRender_DistinctCommandPerFile(t *testing.T) {
	template := "abaqus job={{base}} input={{file}} cpus=8"
	files := []string{"inputs/case1.inp", "inputs/case2.inp", "inputs/case3.inp"}

	seen := make(map[string]bool, len(files))
	for i, file := range files {
		command, _, err := Render(template, "", jobFilesFor(file), i+1)
		if err != nil {
			t.Fatalf("Render(%s): %v", file, err)
		}
		if seen[command] {
			t.Errorf("command %q rendered for more than one file", command)
		}
		seen[command] = true
	}

	if len(seen) != len(files) {
		t.Errorf("got %d distinct commands, want %d", len(seen), len(files))
	}
}

func TestRender_JobName(t *testing.T) {
	tests := []struct {
		name     string
		template string
		want     string
	}{
		// Pre-token behavior, preserved so existing setups are unaffected.
		{"no tokens is numbered", "Crash Study", "Crash Study_2"},
		{"empty falls back", "", "Job_2"},
		{"whitespace only falls back", "   ", "Job_2"},

		{"tokens substitute", "run-{{base}}", "run-case1"},
		{"index available", "{{base}}-{{index}}", "case1-2"},
		{"numbering suppressed once tokens are used", "{{base}}", "case1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, jobName, err := Render("solve {{file}}", tt.template, jobFilesFor("inputs/case1.inp"), 2)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if jobName != tt.want {
				t.Errorf("job name = %q, want %q", jobName, tt.want)
			}
		})
	}
}

// A filename that would restructure the command is this file's problem, not the
// batch's, so it comes back as an error the caller records as a skip.
func TestRender_RejectsUnsafeFilename(t *testing.T) {
	tests := map[string]string{
		"space":        "inputs/my case.inp",
		"substitution": "inputs/$(whoami).inp",
		"separator":    "inputs/a;b.inp",
	}

	for name, file := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := Render("solve --in {{file}}", "", jobFilesFor(file), 1)
			if err == nil {
				t.Fatalf("Render(%q) succeeded, want an error", file)
			}
			if !strings.Contains(err.Error(), "{{file}}") {
				t.Errorf("error %v does not name the offending token", err)
			}
		})
	}
}

// The unsafe-value rule applies to the values actually being substituted. A
// folder with a space in its name is only a problem if {{dir}} is used.
func TestRender_UnusedTokenValueNotChecked(t *testing.T) {
	jf := jobFilesFor("my inputs/case1.inp")

	if _, _, err := Render("solve --in {{file}}", "", jf, 1); err != nil {
		t.Errorf("Render rejected a job over a {{dir}} value it never substituted: %v", err)
	}
	if _, _, err := Render("solve --set {{dir}}", "", jf, 1); err == nil {
		t.Error("Render accepted a {{dir}} value containing a space")
	}
}

func TestValidateCommandTemplate(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		wantErr  bool
		wantWarn bool
	}{
		{"all known tokens", "solve {{file}} {{base}} {{ext}} {{dir}} {{index}}", false, false},
		{"no tokens warns", "solve --in fixed.inp", false, true},
		{"empty is an error", "", true, false},
		{"whitespace only is an error", "  ", true, false},
		{"unknown token is fatal", "solve --job {{bse}}", true, false},
		{"DOE token is not a file-scan token", "solve --job {{__base}}", true, false},
		{"one bad among good is fatal", "solve {{file}} {{nope}}", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings, err := ValidateCommandTemplate(tt.command)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateCommandTemplate(%q) err = %v, wantErr %v", tt.command, err, tt.wantErr)
			}
			if (len(warnings) > 0) != tt.wantWarn {
				t.Errorf("warnings = %v, wantWarn %v", warnings, tt.wantWarn)
			}
		})
	}
}

// A typo must not submit a batch of jobs carrying a literal "{{bse}}" on their
// command lines, so the message has to point at the typo and the valid set.
func TestValidateCommandTemplate_UnknownTokenMessage(t *testing.T) {
	_, err := ValidateCommandTemplate("abaqus job={{bse}} input={{file}}")
	if err == nil {
		t.Fatal("expected an error for an unknown token")
	}
	if !strings.Contains(err.Error(), "{{bse}}") {
		t.Errorf("error %v does not name the unknown token", err)
	}
	for _, token := range KnownTokens() {
		if !strings.Contains(err.Error(), "{{"+token+"}}") {
			t.Errorf("error %v does not list the valid token {{%s}}", err, token)
		}
	}
}

// A command with no tokens still renders, unchanged: identical commands are
// occasionally intended, which is why this is a warning and not an error.
func TestRender_NoTokensPassesThrough(t *testing.T) {
	template := "solve --in fixed.inp"
	command, _, err := Render(template, "", jobFilesFor("inputs/case1.inp"), 1)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if command != template {
		t.Errorf("command = %q, want it unchanged as %q", command, template)
	}
}

// Render does not depend on the caller having validated first: an unknown token
// would otherwise reach Rescale verbatim.
func TestRender_RejectsResidualToken(t *testing.T) {
	_, _, err := Render("solve --job {{bse}}", "", jobFilesFor("inputs/case1.inp"), 1)
	if err == nil {
		t.Fatal("expected an error for an unresolved token")
	}
	if !strings.Contains(err.Error(), "{{bse}}") {
		t.Errorf("error %v does not name the unresolved token", err)
	}
}

func TestValidateJobNameTemplate(t *testing.T) {
	if warnings := ValidateJobNameTemplate("run-{{base}}-{{index}}"); len(warnings) > 0 {
		t.Errorf("known tokens warned: %v", warnings)
	}
	if warnings := ValidateJobNameTemplate("Crash Study"); len(warnings) > 0 {
		t.Errorf("token-free name warned: %v", warnings)
	}

	warnings := ValidateJobNameTemplate("run-{{bse}}")
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	if !strings.Contains(warnings[0], "{{bse}}") {
		t.Errorf("warning %q does not name the unknown token", warnings[0])
	}
}

// Every token the renderer substitutes must be one ValidateCommandTemplate
// accepts, or a documented token would be rejected before it could be used.
func TestKnownTokensAgreeWithSubstitutions(t *testing.T) {
	values := Substitutions(jobFilesFor("inputs/case1.inp"), 1)

	for _, token := range KnownTokens() {
		if _, ok := values[token]; !ok {
			t.Errorf("KnownTokens lists {{%s}} but Substitutions has no value for it", token)
		}
		if _, err := ValidateCommandTemplate("solve {{" + token + "}}"); err != nil {
			t.Errorf("ValidateCommandTemplate rejected the known token {{%s}}: %v", token, err)
		}
	}
	for token := range values {
		if !isKnownToken(token) {
			t.Errorf("Substitutions supplies {{%s}}, which KnownTokens does not list", token)
		}
	}
}
