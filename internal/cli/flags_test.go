package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// writeScanDeck creates one deck file under root, making parents as needed.
func writeScanDeck(t *testing.T, root, name string) {
	t.Helper()

	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// scanFilesFixture lays out two subdirectories holding identically named decks
// and writes the template CSV a scan-files run generates jobs from. It returns
// the root, the template path and the output path.
func scanFilesFixture(t *testing.T, jobNameTemplate string) (root, template, output string) {
	t.Helper()

	root = t.TempDir()
	writeScanDeck(t, root, filepath.Join("case1", "model.inp"))
	writeScanDeck(t, root, filepath.Join("case2", "model.inp"))

	return root, scanFilesTemplate(t, root, jobNameTemplate), filepath.Join(root, "jobs.csv")
}

// scanFilesTemplate writes the one-row template CSV a scan-files run generates
// its jobs from.
func scanFilesTemplate(t *testing.T, root, jobNameTemplate string) string {
	t.Helper()

	template := filepath.Join(root, "template.csv")
	if err := config.SaveJobsCSV(template, []models.JobSpec{{
		JobName:       jobNameTemplate,
		Command:       "solve {{file}}",
		AnalysisCode:  "user_included",
		CoreType:      "emerald",
		CoresPerSlot:  1,
		Slots:         1,
		WalltimeHours: 1.0,
		TarSubpath:    "results",
	}}); err != nil {
		t.Fatalf("write template: %v", err)
	}
	return template
}

// runScanFiles executes the scan-files command over the fixture, returning what
// it printed. The summary goes to stdout directly, not through cobra's writer.
func runScanFiles(t *testing.T, root, primary, template, output string) (string, error) {
	t.Helper()

	cmd := newScanFilesCmd()
	cmd.SetArgs([]string{
		"--root", root,
		"--primary", primary,
		"--template", template,
		"--output", output,
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	// Bind the shared CLI logger to the real stdout before the swap below: it
	// captures os.Stdout once, and a logger left holding a closed test pipe
	// fails every later write in the package.
	GetLogger()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w

	runErr := cmd.Execute()

	os.Stdout = orig
	_ = w.Close()
	out, _ := io.ReadAll(r)
	_ = r.Close()

	return string(out), runErr
}

// The summary was printed from the scan's match count, before the template loop
// rendered anything: a run that skipped every file still announced the full
// count as "Jobs created" and then generated none. The skip lines named the base
// name alone, which in this layout is the one thing the files have in common.
func TestScanFilesSummaryReportsRenderedJobs(t *testing.T) {
	root := t.TempDir()
	// A space in the name cannot be substituted into a command line, so these
	// two are skipped and the third is the only job generated.
	writeScanDeck(t, root, filepath.Join("case1", "my case.inp"))
	writeScanDeck(t, root, filepath.Join("case2", "my case.inp"))
	writeScanDeck(t, root, filepath.Join("case3", "good.inp"))

	output := filepath.Join(root, "jobs.csv")
	out, err := runScanFiles(t, root, filepath.Join("*", "*.inp"),
		scanFilesTemplate(t, root, "{{dir}}-{{base}}"), output)
	if err != nil {
		t.Fatalf("scan-files: %v", err)
	}

	jobs, err := config.LoadJobsCSV(output)
	if err != nil {
		t.Fatalf("load generated CSV: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("%d jobs generated, want 1", len(jobs))
	}
	// The template's subpath has no directory walk to apply to in files mode,
	// and reaching the CSV set it would fail the job at the tar stage.
	if jobs[0].TarSubpath != "" {
		t.Errorf("TarSubpath = %q, want it cleared", jobs[0].TarSubpath)
	}
	if !strings.Contains(out, "Jobs created: 1") {
		t.Errorf("summary does not report the 1 job actually generated:\n%s", out)
	}
	// Each skipped file is named by folder and base name, so the two lines are
	// distinguishable — and tell the user which deck to rename.
	for _, want := range []string{
		filepath.Join("case1", "my case.inp"),
		filepath.Join("case2", "my case.inp"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not name the skipped %s:\n%s", want, out)
		}
	}
}

// scan-files builds its jobs through filescan.BuildJobs, which is where the
// collision rule and its message are covered. What this pins is that the refusal
// reaches the command as a failure and, with it, that no jobs.csv is left behind
// for a batch that was never built.
func TestScanFilesRejectsDuplicateJobNames(t *testing.T) {
	root, template, output := scanFilesFixture(t, "{{base}}")

	if _, err := runScanFiles(t, root, filepath.Join("*", "model.inp"), template, output); err == nil {
		t.Fatal("expected an error for two files rendering to one job name")
	}
	if _, statErr := os.Stat(output); statErr == nil {
		t.Error("a jobs CSV was written despite the collision")
	}
}

// A CSV saved by a file scan carries LocalInputFiles. Reused as a folder-scan
// template it used to hand that list to every generated job, and the tar stage
// prefers the list over Directory — so every job archived the old template's
// files rather than the run directory it was generated for.
func TestMakeDirsCSVClearsInheritedFileList(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"Run_1", "Run_2"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	template := filepath.Join(root, "template.csv")
	if err := config.SaveJobsCSV(template, []models.JobSpec{{
		JobName:         "run_1",
		Command:         "./solve.sh",
		AnalysisCode:    "user_included",
		CoreType:        "emerald",
		CoresPerSlot:    1,
		Slots:           1,
		WalltimeHours:   1.0,
		LocalInputFiles: []string{filepath.Join(root, "old", "case.inp")},
	}}); err != nil {
		t.Fatalf("write template: %v", err)
	}

	// Guards the test itself: were the field not round-tripped by the CSV, the
	// template would reach the command empty and prove nothing.
	loaded, err := config.LoadJobsCSV(template)
	if err != nil {
		t.Fatalf("reload template: %v", err)
	}
	if len(loaded[0].LocalInputFiles) == 0 {
		t.Fatal("the template CSV did not round-trip LocalInputFiles")
	}

	output := filepath.Join(root, "jobs.csv")
	cmd := newMakeDirsCSVCmd()
	cmd.SetArgs([]string{
		"--template", template,
		"--output", output,
		"--pattern", "Run_*",
		"--cwd", root,
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("make-dirs-csv: %v", err)
	}

	jobs, err := config.LoadJobsCSV(output)
	if err != nil {
		t.Fatalf("load generated CSV: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("%d jobs generated, want 2", len(jobs))
	}
	for _, job := range jobs {
		if len(job.LocalInputFiles) != 0 {
			t.Errorf("%s kept the template's file list %v, so it would archive those "+
				"instead of %s", job.JobName, job.LocalInputFiles, job.Directory)
		}
		if job.Directory == "" {
			t.Errorf("%s has no directory to archive", job.JobName)
		}
	}
}

// TestJobsIDAliasParses covers the documented --id alias. It was unusable:
// --job-id and --id are two pflag entries sharing one variable, and cobra's
// MarkFlagRequired("job-id") rejected "--id X" as a missing required flag.
func TestJobsIDAliasParses(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "get with --job-id", args: []string{"get", "--job-id", "abc123"}},
		{name: "get with --id", args: []string{"get", "--id", "abc123"}},
		{name: "get with -j", args: []string{"get", "-j", "abc123"}},
		{name: "delete with --id", args: []string{"delete", "--id", "abc123", "--confirm"}},
		{name: "download with --id", args: []string{"download", "--id", "abc123"}},
		{name: "get with neither", args: []string{"get"}, wantErr: "is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse only: stop before RunE, which would need an API client.
			cmd := newJobsCmd()
			sub, flags, err := cmd.Find(tt.args)
			if err != nil {
				t.Fatalf("Find(%v): %v", tt.args, err)
			}
			if err := sub.ParseFlags(flags); err != nil {
				t.Fatalf("ParseFlags(%v): %v", flags, err)
			}

			// Cobra's own required-flag validation must not reject the alias.
			if err := sub.ValidateRequiredFlags(); err != nil {
				t.Fatalf("ValidateRequiredFlags: %v", err)
			}

			got := sub.Flags().Lookup("job-id")
			if got == nil {
				t.Fatal("no --job-id flag on the command")
			}
			if tt.wantErr != "" {
				if got.Value.String() != "" && got.Value.String() != "[]" {
					t.Errorf("expected no job id, got %q", got.Value.String())
				}
				return
			}
			if !strings.Contains(got.Value.String(), "abc123") {
				t.Errorf("--job-id holds %q, want it to carry abc123", got.Value.String())
			}
		})
	}
}

// TestFilesDeleteRejectsPositionalArgs covers silently discarded IDs:
// "files delete --fileid A B C" deleted A and ignored B and C.
func TestFilesDeleteRejectsPositionalArgs(t *testing.T) {
	cmd := newFilesCmd()
	sub, _, err := cmd.Find([]string{"delete"})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if sub.Args == nil {
		t.Fatal("files delete accepts any positional args")
	}
	if err := sub.Args(sub, []string{"B", "C"}); err == nil {
		t.Error("expected positional arguments to be rejected")
	}
	if err := sub.Args(sub, nil); err != nil {
		t.Errorf("no positional arguments must be fine, got %v", err)
	}
}

// TestConfirmDestructiveWithoutTerminal verifies that a destructive command with
// no terminal fails loudly instead of reading EOF, printing "Cancelled" and
// exiting 0 — which left scripts unable to tell a refusal from a completion.
func TestConfirmDestructiveWithoutTerminal(t *testing.T) {
	if IsTerminal() {
		t.Skip("test needs a non-interactive stdin")
	}

	ok, err := confirmDestructive("deleting files", "--confirm")
	if ok {
		t.Error("must not confirm without a terminal")
	}
	if err == nil {
		t.Fatal("expected an error explaining that --confirm is needed")
	}
	if !strings.Contains(err.Error(), "--confirm") {
		t.Errorf("error should name the flag to use: %v", err)
	}
}

// TestPromptsRefuseWithoutTerminal verifies every interactive prompt reachable
// from a batch worker reports why it cannot ask, instead of surfacing "EOF".
func TestPromptsRefuseWithoutTerminal(t *testing.T) {
	if IsTerminal() {
		t.Skip("test needs a non-interactive stdin")
	}

	checks := []struct {
		name string
		call func() error
	}{
		{"folder conflict", func() error { _, err := promptFolderConflict("data"); return err }},
		{"file conflict", func() error { _, err := promptFileConflict("a.txt", "data"); return err }},
		{"download conflict", func() error { _, err := promptDownloadConflict("a.txt", "/tmp/a.txt"); return err }},
		{"folder download conflict", func() error {
			_, err := promptFolderDownloadConflict("data", "/tmp/data")
			return err
		}},
		{"folder download mode", func() error { _, err := promptFolderDownloadMode(); return err }},
		{"upload duplicate mode", func() error { _, err := promptUploadDuplicateMode(); return err }},
		{"upload conflict", func() error { _, err := promptUploadConflict("a.txt", ""); return err }},
		{"upload error", func() error { _, err := promptUploadError("a.txt", errFake); return err }},
	}

	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			err := c.call()
			if err == nil {
				t.Fatal("expected an error without a terminal")
			}
			if !strings.Contains(err.Error(), "no interactive terminal") {
				t.Errorf("error should explain the missing terminal: %v", err)
			}
		})
	}
}

var errFake = fakeErr("upload failed")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

// TestJobsRejectsBothIDSpellings covers another silently-discarded-ID case:
// --job-id and --id are two pflag entries sharing one variable, so
// "--job-id A --id B" acted on B and dropped A without a word.
func TestJobsRejectsBothIDSpellings(t *testing.T) {
	for _, sub := range []string{"get", "delete", "download"} {
		t.Run(sub, func(t *testing.T) {
			cmd := newJobsCmd()
			target, flags, err := cmd.Find([]string{sub, "--job-id", "A", "--id", "B"})
			if err != nil {
				t.Fatalf("Find: %v", err)
			}
			if err := target.ParseFlags(flags); err != nil {
				t.Fatalf("ParseFlags: %v", err)
			}

			// Both spellings set: the command must refuse rather than pick one.
			if err := rejectBothIDSpellings(target); err == nil {
				t.Error("expected an error when both --job-id and --id are given")
			} else if !strings.Contains(err.Error(), "--job-id") || !strings.Contains(err.Error(), "--id") {
				t.Errorf("error should name both spellings: %v", err)
			}

			// One spelling alone stays fine.
			single := newJobsCmd()
			target2, flags2, err := single.Find([]string{sub, "--id", "B"})
			if err != nil {
				t.Fatalf("Find: %v", err)
			}
			if err := target2.ParseFlags(flags2); err != nil {
				t.Fatalf("ParseFlags: %v", err)
			}
			if err := rejectBothIDSpellings(target2); err != nil {
				t.Errorf("--id alone must be accepted: %v", err)
			}
		})
	}
}

// TestRejectBothIDSpellingsIgnoresCommandsWithoutAlias guards the helper against
// being called on a command that has no --id flag at all.
func TestRejectBothIDSpellingsIgnoresCommandsWithoutAlias(t *testing.T) {
	cmd := newJobsCmd()
	target, flags, err := cmd.Find([]string{"stop", "--job-id", "A"})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if err := target.ParseFlags(flags); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if err := rejectBothIDSpellings(target); err != nil {
		t.Errorf("no --id flag on this command, so nothing to reject: %v", err)
	}
}
