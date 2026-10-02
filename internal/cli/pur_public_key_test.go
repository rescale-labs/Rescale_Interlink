package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
)

// The platform refuses a job's SSH public key of a type it does not accept
// only as it creates the job, after the job's inputs are uploaded. pur run, pur
// resume and pur submit-existing refuse it as they load the jobs CSV, pur plan
// reports it, and jobs submit --script refuses it before any request, its
// --files included.
func TestPublicKeyTypeIsRefusedBeforeAnyUpload(t *testing.T) {
	usePURConfig(t)
	t.Chdir(t.TempDir())
	if err := os.Mkdir("run_1", 0o755); err != nil {
		t.Fatal(err)
	}
	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 user@host"
	for name, body := range map[string]string{
		filepath.Join("run_1", "input.dat"): "in",
		"state.csv":                         "",
		"jobs.csv": "Directory,JobName,AnalysisCode,Command,CoreType,CoresPerSlot,WalltimeHours,Slots," +
			"LicenseSettings,ExtraInputFileIDs,PublicKey\nrun_1,run_1,user_included,./solve.sh,emerald,4,1.0,1,,deck1," + key + "\n",
	} {
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fake := &fakeJobsAPI{}
	client := fake.client(t)
	origPipeline, origAPI := newPipelineClientFn, getAPIClientFn
	newPipelineClientFn = func(*config.Config) (*api.Client, error) { return client, nil }
	getAPIClientFn = func() (*api.Client, error) { return client, nil }
	t.Cleanup(func() { newPipelineClientFn, getAPIClientFn = origPipeline, origAPI })

	const want = `public key type "ssh-ed25519" is not one the platform accepts`
	for _, cmd := range []*cobra.Command{newRunCmd(), newResumeCmd(), newSubmitExistingCmd()} {
		err := runPURCommand(t, cmd, "--jobs-csv", "jobs.csv", "--state", "state.csv")
		if err == nil || !strings.Contains(err.Error(), "job 1 (run_1): "+want) {
			t.Errorf("pur %s: error %v, want %q for job 1", cmd.Name(), err, want)
		}
	}

	var err error
	printed := captureStdout(t, func() { err = runPURCommand(t, newPlanCmd(), "--jobs-csv", "jobs.csv") })
	if err == nil || !strings.Contains(printed, want) {
		t.Errorf("pur plan returned %v after printing\n%s\nwant %q", err, printed, want)
	}

	script := writeTempFile(t, "job.sh", sgeScriptHead+"#RESCALE_PUBLIC_KEY "+key+"\n")
	err = runPURCommand(t, newJobsSubmitCmd(), "--script", script, "--create", "--files", filepath.Join("run_1", "input.dat"))
	if err == nil || !strings.Contains(err.Error(), "RESCALE_PUBLIC_KEY") || !strings.Contains(err.Error(), want) {
		t.Errorf("jobs submit --script: error %v, want %q naming the directive", err, want)
	}

	if calls := fake.calls(); len(calls) != 0 {
		t.Errorf("the commands reached the API: %v", calls)
	}
}
