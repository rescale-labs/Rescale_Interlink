package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rescale/rescale-int/internal/reporting"
)

// The three commands that write a jobs CSV refuse to replace one in the same
// words.
func TestPUROutputExistsRefusalIsOneSentence(t *testing.T) {
	root := t.TempDir()
	writeScanDeck(t, root, "case1.inp")
	template := scanFilesTemplate(t, root, "{{base}}")
	output := filepath.Join(root, "jobs.csv")
	if err := os.WriteFile(output, []byte("previous contents"), 0o600); err != nil {
		t.Fatal(err)
	}

	want := "output file " + output + " already exists (use --overwrite to replace)"
	for _, args := range [][]string{
		{"scan-files", "--root", root, "--primary", "*.inp", "--template", template, "--output", output},
		{"make-dirs-csv", "--template", template, "--output", output, "--pattern", "Run_*"},
		{"doe", "--template", template, "--output", output, "--param", "alpha=1:2:1"},
	} {
		if err := runPURCommand(t, newPURCmd(), args...); err == nil || err.Error() != want {
			t.Errorf("pur %s: error %v, want %q", args[0], err, want)
		}
	}

	// doe --preview writes no file, so it has none to refuse.
	base := writeTempFile(t, "base.csv", "Directory,JobName,AnalysisCode,Command,CoreType,CoresPerSlot,"+
		"WalltimeHours,Slots,LicenseSettings\n,base,user_included,solve {{alpha}},emerald,1,1.0,1,\n")
	if err := runPURCommand(t, newPURCmd(), "doe", "--template", base, "--preview", "--param", "alpha=1:2:2"); err != nil {
		t.Errorf("pur doe --preview: %v", err)
	}
}

// A worker count below one is the user's own setting, refused like every other
// value the PUR commands read before they start: no report, which would file
// the refusal as an internal fault for Rescale support.
func TestPURWorkerCountRefusalSavesNoReport(t *testing.T) {
	for _, line := range []string{"tar_workers,0", "upload_workers,-4", "job_workers,0"} {
		usePURConfig(t, line)

		err := runPURCommand(t, newRunCmd(), "--jobs-csv", writePreflightJobsCSV(t, "yes"), "--dry-run")
		if err == nil {
			t.Fatalf("%s was accepted", line)
		}
		if reporting.IsReportable(err, reporting.CategoryPURPipeline) {
			t.Errorf("%s: %q would be saved as an error report", line, err)
		}
	}
}
