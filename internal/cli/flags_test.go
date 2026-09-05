package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// scanFilesFixture lays out two subdirectories holding identically named decks
// and writes the template CSV a scan-files run generates jobs from. It returns
// the root, the template path and the output path.
func scanFilesFixture(t *testing.T, jobNameTemplate string) (root, template, output string) {
	t.Helper()

	root = t.TempDir()
	for _, dir := range []string{"case1", "case2"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "model.inp"), []byte("data"), 0644); err != nil {
			t.Fatalf("write %s deck: %v", dir, err)
		}
	}

	template = filepath.Join(root, "template.csv")
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

	return root, template, filepath.Join(root, "jobs.csv")
}

// runScanFiles executes the scan-files command over the fixture.
func runScanFiles(t *testing.T, root, template, output string) error {
	t.Helper()

	cmd := newScanFilesCmd()
	cmd.SetArgs([]string{
		"--root", root,
		"--primary", filepath.Join("*", "model.inp"),
		"--template", template,
		"--output", output,
	})
	cmd.SetOut(os.NewFile(0, os.DevNull))
	return cmd.Execute()
}

// Two directories holding "model.inp" render to one job name under {{base}}.
// The name is what the pipeline records state by, so the run fails: writing the
// CSV without the second file would submit a batch short of the one scanned.
func TestScanFilesRejectsDuplicateJobNames(t *testing.T) {
	root, template, output := scanFilesFixture(t, "{{base}}")

	err := runScanFiles(t, root, template, output)
	if err == nil {
		t.Fatal("expected an error for two files rendering to one job name")
	}
	// Both colliding files are named by folder and basename: under {{base}} the
	// basenames are identical, so the folder is the only thing that tells the
	// user which two files to look at.
	for _, want := range []string{filepath.Join("case1", "model.inp"), filepath.Join("case2", "model.inp")} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if !strings.Contains(err.Error(), `"model"`) {
		t.Errorf("error %q does not name the job name they share", err)
	}
	if _, statErr := os.Stat(output); statErr == nil {
		t.Error("a jobs CSV was written despite the collision")
	}

	// {{dir}} is one of the remedies the error offers, so it must work.
	root, template, output = scanFilesFixture(t, "{{dir}}-{{base}}")
	if err := runScanFiles(t, root, template, output); err != nil {
		t.Fatalf("scan-files with {{dir}}: %v", err)
	}
	jobs, err := config.LoadJobsCSV(output)
	if err != nil {
		t.Fatalf("load generated CSV: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("%d jobs generated with {{dir}} in the name, want 2", len(jobs))
	}
	// The template's subpath has no directory walk to apply to in files mode.
	if jobs[0].TarSubpath != "" {
		t.Errorf("TarSubpath = %q, want it cleared", jobs[0].TarSubpath)
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
