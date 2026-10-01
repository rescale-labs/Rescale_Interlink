package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
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
	usePURConfig(t)
	halfPair := writeTempFile(t, "jobs.csv", "Directory,JobName,AnalysisCode,Command,CoreType,CoresPerSlot,WalltimeHours,"+
		"Slots,LicenseSettings,LicenseFeatureName,LicensesPerJob\nrun_1,run_1,user_included,./solve.sh,emerald,4,1.0,1,,ansys_hpc,\n")
	badSubmit := writePreflightJobsCSV(t, "maybe")
	state := writeTempFile(t, "state.csv", "")

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
		// a jobs CSV row with half a license pair, or a Submit value the
		// pipeline cannot read, refused as the CSV loads
		{"run", "--jobs-csv", halfPair},
		{"resume", "--jobs-csv", halfPair, "--state", state},
		{"run", "--jobs-csv", badSubmit},
		{"resume", "--jobs-csv", badSubmit, "--state", state},
		{"submit-existing", "--jobs-csv", badSubmit},
	} {
		err := runPURCommand(t, newPURCmd(), args...)
		if err == nil {
			t.Fatalf("pur %v succeeded, want it refused", args)
		}
		if saved := reporting.HandleCLIError(err, "cli", "rescale-int pur "+args[0], ""); saved != "" {
			t.Errorf("saved a report for %q at %s", err, saved)
		}
	}

	client := (&fakeJobsAPI{}).client(t) // answers the project lookup
	orig := getAPIClientFn
	getAPIClientFn = func() (*api.Client, error) { return client, nil }
	t.Cleanup(func() { getAPIClientFn = orig })
	script := func(line8 string) string { return writeTempFile(t, "job.sh", sgeScriptHead+line8+"\n") }
	for _, args := range [][]string{
		{"--job-file", "job.json", "--script", "run.sh"},
		{"--script", "run.sh", "--job-id", "JOB1"},
		// a job script's own mistakes: a directive Interlink cannot carry, a
		// walltime given in seconds, a license feature with no count and a
		// project the user does not have
		{"--script", script("#RESCALE_CORE_TYPE_SET=set-1"), "--create"},
		{"--script", script("#RESCALE_WALLTIME 3600"), "--create"},
		{"--script", script(`#RESCALE_USER_DEFINED_LICENSE_SETTINGS={"featureSets":[{"name":"S",` +
			`"features":[{"name":"ansys_hpc"}]}]}`), "--create"},
		{"--script", script("#RESCALE_PROJECT_ID=Nope"), "--create"},
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
