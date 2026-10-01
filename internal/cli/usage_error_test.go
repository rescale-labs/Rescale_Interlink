package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rescale/rescale-int/internal/reporting"
)

// A refusal of what the user typed is theirs to fix, so it prints no
// "share this report with Rescale support" block and saves no report.
func TestRefusalsSaveNoReport(t *testing.T) {
	home := t.TempDir()
	for _, env := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "LOCALAPPDATA"} {
		t.Setenv(env, home)
	}
	root := t.TempDir()
	writeScanDeck(t, root, "case1.inp")
	output := filepath.Join(root, "jobs.csv")
	if err := os.WriteFile(output, []byte("previous contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	template := scanFilesTemplate(t, root, "{{base}}")
	empty := filepath.Join(root, "empty.csv") // a header and no job
	if err := os.WriteFile(empty, []byte("Directory,JobName,AnalysisCode,Command,CoreType\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		// a template with no job in it
		{"scan-files", "--root", root, "--primary", "*.inp", "--template", empty, "--output", filepath.Join(root, "new.csv")},
		{"make-dirs-csv", "--template", empty, "--output", filepath.Join(root, "new.csv"), "--pattern", "Run_*"},
		{"doe", "--template", empty, "--output", filepath.Join(root, "new.csv"), "--param", "alpha=1:2:1"},
		// a job name template with a token scan-files does not know
		{"scan-files", "--root", root, "--primary", "*.inp", "--output", filepath.Join(root, "new.csv"),
			"--template", scanFilesTemplate(t, t.TempDir(), "{{stem}}")},
		// an output file that only --overwrite may replace
		{"scan-files", "--root", root, "--primary", "*.inp", "--template", template, "--output", output},
		{"make-dirs-csv", "--template", template, "--output", output, "--pattern", "Run_*"},
		{"doe", "--template", template, "--output", output, "--param", "alpha=1:2:1"},
	} {
		err := runPURCommand(t, newPURCmd(), args...)
		if err == nil {
			t.Fatalf("pur %v succeeded, want it refused", args)
		}
		if saved := reporting.HandleCLIError(err, "cli", "rescale-int pur "+args[0], ""); saved != "" {
			t.Errorf("saved a report for %q at %s", err, saved)
		}
	}

	for _, args := range [][]string{
		{"--job-file", "job.json", "--script", "run.sh"},
		{"--script", "run.sh", "--job-id", "JOB1"},
	} {
		err := runPURCommand(t, newJobsSubmitCmd(), args...)
		if err == nil {
			t.Fatalf("jobs submit %v succeeded, want it refused", args)
		}
		if saved := reporting.HandleCLIError(err, "cli", "rescale-int jobs submit", ""); saved != "" {
			t.Errorf("saved a report for %q at %s", err, saved)
		}
	}
}
