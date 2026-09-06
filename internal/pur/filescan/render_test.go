package filescan

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/pattern"
	"github.com/rescale/rescale-int/internal/reporting"
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
	tests := []struct {
		name  string
		file  string
		index int
		want  map[string]string
	}{
		{
			name: "every token comes from the primary file",
			file: "scratch/inputs/case1.inp", index: 1,
			want: map[string]string{
				TokenFile: "case1.inp", TokenBase: "case1", TokenExt: "inp",
				TokenDir: "inputs", TokenIndex: "1",
			},
		},
		// The layout {{dir}} exists for: the filenames are identical, so only the
		// containing folder tells these two jobs apart.
		{
			name: "identical filenames, first folder",
			file: "runs/case1/model.inp", index: 1,
			want: map[string]string{
				TokenFile: "model.inp", TokenBase: "model", TokenExt: "inp",
				TokenDir: "case1", TokenIndex: "1",
			},
		},
		{
			name: "identical filenames, second folder",
			file: "runs/case2/model.inp", index: 2,
			want: map[string]string{
				TokenFile: "model.inp", TokenBase: "model", TokenExt: "inp",
				TokenDir: "case2", TokenIndex: "2",
			},
		},
		{
			name: "a file with no extension",
			file: "inputs/Makefile", index: 1,
			want: map[string]string{
				TokenFile: "Makefile", TokenBase: "Makefile", TokenExt: "",
				TokenDir: "inputs", TokenIndex: "1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Substitutions(jobFilesFor(tt.file), tt.index)

			for token, value := range tt.want {
				if got[token] != value {
					t.Errorf("{{%s}} = %q, want %q", token, got[token], value)
				}
			}
			// Every row states the whole set, so a token added without a value
			// here cannot slip past unasserted.
			if len(got) != len(tt.want) {
				t.Errorf("got %d tokens (%v), want %d", len(got), got, len(tt.want))
			}
		})
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

// Render is the whole per-file rendering path: substitution into the command and
// the job name, the value-safety check over the values it actually substitutes,
// and the residual-token and length post-conditions on both outputs.
func TestRender(t *testing.T) {
	// {{file}} expands to the primary file's name, so a long stem is the input
	// that grows the rendered result past a limit.
	longFile := func(stemLen int) JobFiles {
		return jobFilesFor("inputs/" + strings.Repeat("a", stemLen) + ".inp")
	}
	defaultFiles := jobFilesFor("inputs/case1.inp")

	tests := []struct {
		name        string
		command     string
		jobName     string
		files       JobFiles // zero value means defaultFiles
		index       int      // zero value means 1
		wantCommand string   // stated on every non-error row, and asserted exactly
		wantJobName string   // likewise
		wantErr     string   // substring the error must carry; empty means no error
	}{
		{
			name:        "every token reaches the command",
			command:     "solve --in {{file}} --job {{base}} --kind {{ext}} --set {{dir}} --n {{index}}",
			files:       jobFilesFor("scratch/inputs/case1.inp"),
			index:       3,
			wantCommand: "solve --in case1.inp --job case1 --kind inp --set inputs --n 3",
			wantJobName: "Job_3",
		},
		{
			// A command with no tokens still renders, unchanged: identical
			// commands are occasionally intended, which is why the validator
			// warns rather than refusing.
			name:        "a command with no tokens passes through",
			command:     "solve --in fixed.inp",
			wantCommand: "solve --in fixed.inp",
			wantJobName: "Job_1",
		},

		// Job names. The first three are the pre-token behavior, preserved so
		// existing setups are unaffected.
		{name: "no tokens is numbered", command: "solve {{file}}", jobName: "Crash Study", index: 2, wantCommand: "solve case1.inp", wantJobName: "Crash Study_2"},
		{name: "empty falls back", command: "solve {{file}}", index: 2, wantCommand: "solve case1.inp", wantJobName: "Job_2"},
		{name: "whitespace only falls back", command: "solve {{file}}", jobName: "   ", index: 2, wantCommand: "solve case1.inp", wantJobName: "Job_2"},
		{name: "tokens substitute", command: "solve {{file}}", jobName: "run-{{base}}", index: 2, wantCommand: "solve case1.inp", wantJobName: "run-case1"},
		{name: "index available", command: "solve {{file}}", jobName: "{{base}}-{{index}}", index: 2, wantCommand: "solve case1.inp", wantJobName: "case1-2"},
		{name: "numbering suppressed once tokens are used", command: "solve {{file}}", jobName: "{{base}}", index: 2, wantCommand: "solve case1.inp", wantJobName: "case1"},

		// A filename that would restructure the command is this file's problem,
		// not the batch's, so it comes back as an error the caller records as a
		// skip — naming the token, which is the only clue to which file it was.
		{name: "a space in the filename", command: "solve --in {{file}}", files: jobFilesFor("inputs/my case.inp"), wantErr: "{{file}}"},
		{name: "a substitution in the filename", command: "solve --in {{file}}", files: jobFilesFor("inputs/$(whoami).inp"), wantErr: "{{file}}"},
		{name: "a separator in the filename", command: "solve --in {{file}}", files: jobFilesFor("inputs/a;b.inp"), wantErr: "{{file}}"},
		{
			// The rule applies to the values actually being substituted: a folder
			// with a space in its name is only a problem if {{dir}} is used.
			name:  "an unsafe value in a token the command never uses",
			files: jobFilesFor("my inputs/case1.inp"), command: "solve --in {{file}}",
			wantCommand: "solve --in case1.inp",
			wantJobName: "Job_1",
		},
		{
			name:  "the same value once the command does use it",
			files: jobFilesFor("my inputs/case1.inp"), command: "solve --set {{dir}}",
			wantErr: "{{dir}}",
		},

		// Render does not depend on the caller having validated first: an unknown
		// token would otherwise reach Rescale verbatim, and one left in the name
		// leaves every job in the scan answering to a single literal identifier.
		{name: "an unresolved command token", command: "solve --job {{bse}}", wantErr: "{{bse}}"},
		{name: "an unresolved job name token", command: "solve {{file}}", jobName: "run-{{bse}}", wantErr: "{{bse}}"},

		// The rendered length limits are DOE's, and they apply here for the same
		// reason: a template multiplies its input, so a filename a byte too long
		// is one the caller must not submit. Checked at the boundary exactly,
		// since this path used to be bounded nowhere.
		{
			name:        "command exactly at the limit",
			command:     strings.Repeat("x", pattern.MaxCommandLength-len("case1.inp")) + "{{file}}",
			jobName:     "run",
			wantCommand: strings.Repeat("x", pattern.MaxCommandLength-len("case1.inp")) + "case1.inp",
			wantJobName: "run_1",
		},
		{
			name:    "command one byte over",
			command: strings.Repeat("x", pattern.MaxCommandLength-len("case1.inp")+1) + "{{file}}",
			jobName: "run",
			wantErr: "command",
		},
		{
			name:    "job name exactly at the limit",
			command: "solve {{file}}", jobName: "{{base}}",
			files:       longFile(pattern.MaxJobNameLength),
			wantCommand: "solve " + strings.Repeat("a", pattern.MaxJobNameLength) + ".inp",
			wantJobName: strings.Repeat("a", pattern.MaxJobNameLength),
		},
		{
			name:    "job name one byte over",
			command: "solve {{file}}", jobName: "{{base}}",
			files:   longFile(pattern.MaxJobNameLength + 1),
			wantErr: "job name",
		},
		{
			// The index suffix is part of the rendered name, so it counts.
			name:    "untokenized job name over the limit once numbered",
			command: "solve {{file}}", jobName: strings.Repeat("n", pattern.MaxJobNameLength),
			wantErr: "job name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := tt.files
			if files.PrimaryFile == "" {
				files = defaultFiles
			}
			index := tt.index
			if index == 0 {
				index = 1
			}

			command, jobName, err := Render(tt.command, tt.jobName, files, index)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Render(%q, %q) succeeded, want an error", tt.command, tt.jobName)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %v does not carry %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			// Both stated on every row, so no row can pass while silently
			// asserting nothing about one of the two outputs.
			if command != tt.wantCommand {
				t.Errorf("command = %q, want %q", command, tt.wantCommand)
			}
			if jobName != tt.wantJobName {
				t.Errorf("job name = %q, want %q", jobName, tt.wantJobName)
			}
		})
	}
}

// The motivating case: one template, one command per file. Kept apart from the
// table because what it pins is that repeated calls stay independent of each
// other, which no single-render row can show.
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
// command lines, so the message has to point at the typo and the valid set. The
// CLI prints it through the crash reporter, whose redactor replaces anything
// that looks like "token <value>" with [REDACTED] — phrasing that trips it
// strips out the very detail the user needs, so what survives is pinned too.
func TestValidateCommandTemplate_UnknownTokenMessage(t *testing.T) {
	_, err := ValidateCommandTemplate("abaqus job={{bse}} input={{file}}")
	if err == nil {
		t.Fatal("expected an error for an unknown token")
	}

	for _, stage := range []struct{ label, message string }{
		{"error", err.Error()},
		{"redacted error", reporting.RedactError(err.Error())},
	} {
		if !strings.Contains(stage.message, "{{bse}}") {
			t.Errorf("%s %q does not name the unknown token", stage.label, stage.message)
		}
		for _, token := range KnownTokens() {
			if !strings.Contains(stage.message, "{{"+token+"}}") {
				t.Errorf("%s %q does not list the valid token {{%s}}", stage.label, stage.message, token)
			}
		}
	}
}

// An unknown name token is fatal rather than advisory, for the reason
// ValidateJobNameTemplate gives: it leaves every job in the scan carrying one
// literal name.
func TestValidateJobNameTemplate(t *testing.T) {
	if err := ValidateJobNameTemplate("run-{{base}}-{{index}}"); err != nil {
		t.Errorf("known tokens rejected: %v", err)
	}
	if err := ValidateJobNameTemplate("Crash Study"); err != nil {
		t.Errorf("token-free name rejected: %v", err)
	}

	err := ValidateJobNameTemplate("run-{{bse}}")
	if err == nil {
		t.Fatal("expected an error for an unknown name token")
	}
	if !strings.Contains(err.Error(), "{{bse}}") {
		t.Errorf("error %q does not name the unknown token", err)
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

// BuildJobs is the whole of file-scan assembly, and the CLI's scan-files and the
// GUI's files mode both go through it — so what it refuses, what it skips and
// what it builds is stated here once, rather than once per caller.
func TestBuildJobs(t *testing.T) {
	// The layout the collision exists for: identical base names in sibling
	// folders, which is what a "*/model.inp" scan is written to find.
	colliding := []JobFiles{jobFilesFor("case1/model.inp"), jobFilesFor("case2/model.inp")}

	tests := []struct {
		name        string
		command     string
		jobName     string
		found       []JobFiles
		wantNames   []string // job names built, in order
		wantSkipped []string // one substring per skipped file, in order
		wantWarn    bool
		wantErr     []string // substrings the error must carry
	}{
		{
			name: "one job per file", command: "solve {{file}}", jobName: "{{dir}}-{{base}}",
			found: colliding, wantNames: []string{"case1-model", "case2-model"},
		},
		{
			// The name is what progress events and state records are matched by,
			// so two files answering to one fail the batch: building the first
			// alone would hand back fewer jobs than the scan found.
			name: "two files rendering to one name", command: "solve {{file}}", jobName: "{{base}}",
			found: colliding,
			wantErr: []string{
				filepath.Join("case1", "model.inp"), filepath.Join("case2", "model.inp"),
				`"model"`, "{{index}}", "{{dir}}",
			},
		},
		{
			// A template typo is wrong for every file, so it fails once instead
			// of skipping each file in turn.
			name: "an unknown command token", command: "solve --job {{bse}}", jobName: "{{base}}",
			found: colliding, wantErr: []string{"{{bse}}", "{{file}}"},
		},
		{
			name: "an unknown job name token", command: "solve {{file}}", jobName: "run-{{bse}}",
			found: colliding, wantErr: []string{"{{bse}}", "{{file}}"},
		},
		{
			// One filename that cannot be substituted safely costs that file, not
			// the batch — and the line names the folder, since in this layout the
			// base name is the one thing the files have in common.
			name: "a file that cannot be rendered", command: "solve {{file}}", jobName: "{{dir}}-{{base}}",
			found:       []JobFiles{jobFilesFor("case1/my case.inp"), jobFilesFor("case2/good.inp")},
			wantNames:   []string{"case2-good"},
			wantSkipped: []string{filepath.Join("case1", "my case.inp")},
		},
		{
			// Every job then runs the same command, which is occasionally what
			// the user wants, so it warns rather than refusing.
			name: "a command with no tokens", command: "solve fixed.inp", jobName: "{{dir}}-{{base}}",
			found: colliding, wantNames: []string{"case1-model", "case2-model"}, wantWarn: true,
		},
		{
			name: "an empty command", command: "  ", jobName: "{{base}}",
			found: colliding, wantErr: []string{"empty"},
		},
		{name: "nothing found", command: "solve {{file}}", jobName: "{{base}}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs, skipped, warnings, err := BuildJobs(
				models.JobSpec{Command: tt.command, JobName: tt.jobName}, tt.found)

			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatal("BuildJobs succeeded, want an error")
				}
				for _, want := range tt.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not carry %q", err, want)
					}
				}
				if len(jobs) > 0 {
					t.Errorf("%d jobs built from a refused scan", len(jobs))
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildJobs: %v", err)
			}

			var names []string
			for _, job := range jobs {
				names = append(names, job.JobName)
			}
			if !reflect.DeepEqual(names, tt.wantNames) {
				t.Errorf("job names = %v, want %v", names, tt.wantNames)
			}
			if len(skipped) != len(tt.wantSkipped) {
				t.Fatalf("skipped = %v, want %d entries", skipped, len(tt.wantSkipped))
			}
			for i, want := range tt.wantSkipped {
				if !strings.Contains(skipped[i], want) {
					t.Errorf("skipped[%d] = %q does not name %s", i, skipped[i], want)
				}
			}
			if (len(warnings) > 0) != tt.wantWarn {
				t.Errorf("warnings = %v, wantWarn %v", warnings, tt.wantWarn)
			}
		})
	}
}

// The assembled job: the template's own fields carry over untouched, and the
// ones the scan owns are taken from the file set.
func TestBuildJobs_AssemblesFromTheTemplate(t *testing.T) {
	template := models.JobSpec{
		Command:      "solve {{file}}",
		JobName:      "{{base}}",
		AnalysisCode: "user_included",
		CoreType:     "emerald",
		CoresPerSlot: 4,
		TarSubpath:   "results",
		InputFiles:   []string{"already-uploaded"},
	}

	jf := jobFilesFor("inputs/case1.inp")
	// A secondary pattern resolves outside the primary's folder, which is the
	// whole reason the job carries its own file list.
	jf.InputFiles = append(jf.InputFiles, filepath.FromSlash("meshes/case1.cfg"))

	jobs, _, _, err := BuildJobs(template, []JobFiles{jf})
	if err != nil {
		t.Fatalf("BuildJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("%d jobs, want 1", len(jobs))
	}

	job := jobs[0]
	if job.AnalysisCode != template.AnalysisCode || job.CoreType != template.CoreType ||
		job.CoresPerSlot != template.CoresPerSlot {
		t.Errorf("template fields not carried over: %+v", job)
	}
	if job.Command != "solve case1.inp" || job.JobName != "case1" {
		t.Errorf("command = %q, job name = %q", job.Command, job.JobName)
	}
	if job.Directory != jf.PrimaryDir {
		t.Errorf("Directory = %q, want %q", job.Directory, jf.PrimaryDir)
	}
	// A subpath inherited from a loaded template has no directory walk in this
	// mode to apply to, and would fail the job at the tar stage.
	if job.TarSubpath != "" {
		t.Errorf("TarSubpath = %q, want it cleared", job.TarSubpath)
	}
	if !reflect.DeepEqual(job.LocalInputFiles, jf.InputFiles) {
		t.Errorf("LocalInputFiles = %v, want %v", job.LocalInputFiles, jf.InputFiles)
	}
	// InputFiles means IDs of files already on Rescale, which these are not.
	if job.InputFiles != nil {
		t.Errorf("InputFiles = %v, want it cleared", job.InputFiles)
	}
}
