package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// TestPrintFailedJobs pins the lines a failed PUR run ends with at default
// verbosity: each failed job with its reason, one line each and capped.
func TestPrintFailedJobs(t *testing.T) {
	t.Setenv("RESCALE_DEBUG", "") // not --verbose
	printed := func(failed ...*models.JobState) string {
		var out strings.Builder
		printFailedJobs(&out, failed)
		return out.String()
	}

	// A failed job's reason is printed redacted, as every other output is.
	t.Run("each failed job and its reason, one line each, without secrets", func(t *testing.T) {
		got := printed(
			&models.JobState{JobName: "job_1", ErrorMessage: "could not record upload state: " +
				"failed to create temp state file: open /ro/state.csv.tmp: permission denied"},
			&models.JobState{JobName: "job_2", ErrorMessage: "create job failed: status 502: " +
				"<html>\r\n<body>Bad Gateway</body>\r\n</html>"},
			&models.JobState{JobName: "job_3", ErrorMessage: `failed to stage block 0: Put "https://acct.blob.core.windows.net/` +
				`c/job_3%2Finput.tar.gz?comp=block&se=2026-09-24T00%3A00%3A00Z&sig=FAKE%2BSIG%3D` +
				`&sp=rwac&sv=2022-11-02": read tcp 192.0.2.2:50522->192.0.2.1:443: read: connection reset by peer`},
		)
		want := "✗ job_1: could not record upload state: failed to create temp state file: " +
			"open /ro/state.csv.tmp: permission denied\n" +
			"✗ job_2: create job failed: status 502: <html> <body>Bad Gateway</body> </html>\n" +
			`✗ job_3: failed to stage block 0: Put "https://acct.blob.core.windows.net/c/job_3%2Finput.tar.gz?comp=block` +
			`&se=REDACTED&sig=REDACTED&sp=REDACTED&sv=REDACTED": read tcp 192.0.2.2:50522->192.0.2.1:443: read: connection reset by peer` + "\n"
		if got != want {
			t.Errorf("printed\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("a reason quoting a large response body", func(t *testing.T) {
		got := printed(&models.JobState{JobName: "job_1",
			ErrorMessage: "create job failed: status 502: " + strings.Repeat("x", 1<<20)})
		if len(got) > 1100 || !strings.HasSuffix(got, "...\n") {
			t.Errorf("printed %d bytes for one job, want its reason cut short", len(got))
		}
	})

	// A name is a CSV cell or a state-file field, which can hold anything. A
	// DOE sweep names jobs after its parameters, which must read as written.
	t.Run("names over several lines or longer than any job's", func(t *testing.T) {
		got := printed(
			&models.JobState{JobName: "run_1\r\ncopy\n?sig=FAKE%2BSIG%3D", ErrorMessage: "reason"},
			&models.JobState{JobName: strings.Repeat("n", 1<<12), ErrorMessage: "reason"},
			&models.JobState{JobName: "mass=5_1_phase=2_case=3", ErrorMessage: "response=500"},
		)
		want := "✗ run_1 copy ?sig=REDACTED: reason\n✗ " + strings.Repeat("n", 125) + "...: reason\n" +
			"✗ mass=5_1_phase=2_case=3: response=500\n"
		if got != want {
			t.Errorf("printed\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("many failed jobs", func(t *testing.T) {
		var failed []*models.JobState
		for i := 1; i <= 12; i++ {
			failed = append(failed, &models.JobState{JobName: fmt.Sprintf("job_%d", i), ErrorMessage: "reason"})
		}
		lines := strings.Split(strings.TrimSuffix(printed(failed...), "\n"), "\n")
		if len(lines) != 11 || lines[9] != "✗ job_10: reason" ||
			lines[10] != "... and 2 more (--verbose lists them all)" {
			t.Errorf("printed %d line(s), ending %q; want ten jobs and a count of the other two",
				len(lines), lines[len(lines)-1])
		}
	})
}

// TestPURCommandsNameFailedJobs runs both commands that print those lines
// against an API that refuses every call: pur run cannot archive run_1, whose
// directory is missing, and submit-existing cannot create it. Then pur run
// resumes a state file holding eleven failed submits: it retries none of them,
// so nothing logs them, and with --verbose the lines name every one.
func TestPURCommandsNameFailedJobs(t *testing.T) {
	usePURConfig(t)
	jobsCSV := writePreflightJobsCSV(t, "yes")
	t.Chdir(t.TempDir()) // run_1 is missing here, and the state files land here

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail": "refused"}`, http.StatusBadRequest)
	}))
	defer server.Close()
	defer func(orig func(*config.Config) (*api.Client, error)) { newPipelineClientFn = orig }(newPipelineClientFn)
	newPipelineClientFn = func(*config.Config) (*api.Client, error) {
		return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), nil
	}

	run := func(cmd *cobra.Command, args ...string) (string, error) {
		stderr, err := os.Create(cmd.Name() + ".stderr")
		if err != nil {
			t.Fatal(err)
		}
		defer func(orig *os.File) { os.Stderr = orig }(os.Stderr)
		os.Stderr = stderr
		runErr := runPURCommand(t, cmd, args...)
		_ = stderr.Close()
		printed, _ := os.ReadFile(stderr.Name())
		return string(printed), runErr
	}
	for _, cmd := range []*cobra.Command{newRunCmd(), newSubmitExistingCmd()} {
		printed, err := run(cmd, "--jobs-csv", jobsCSV, "--state", cmd.Name()+".csv")
		if err == nil || !strings.Contains(printed, "✗ run_1: ") {
			t.Errorf("pur %s returned %v and printed %q, want run_1 named with its reason", cmd.Name(), err, printed)
		}
	}

	const reason = "submit job failed: status 400: no credit"
	jobs := "Directory,JobName,AnalysisCode,Command,CoreType,CoresPerSlot,WalltimeHours,Slots,LicenseSettings,ExtraInputFileIDs,Submit\n"
	states := "Index,JobName,Directory,TarPath,TarStatus,FileID,UploadStatus,JobID,SubmitStatus,ExtraFileIDs,ErrorMessage,LastUpdated\n"
	for i := 1; i <= 11; i++ {
		jobs += fmt.Sprintf("run_%d,run_%d,user_included,./solve.sh,emerald,4,1.0,1,,,yes\n", i, i)
		states += fmt.Sprintf("%d,run_%d,run_%d,,success,f%d,success,j%d,failed,,%s,\n", i, i, i, i, i, reason)
	}
	if os.WriteFile("jobs.csv", []byte(jobs), 0o600) != nil || os.WriteFile("resumed.csv", []byte(states), 0o600) != nil {
		t.Fatal("write the resumed run's jobs and state")
	}
	t.Setenv("RESCALE_DEBUG", "1")
	printed, _ := run(newRunCmd(), "--jobs-csv", "jobs.csv", "--state", "resumed.csv")
	for i := 1; i <= 11; i++ {
		if want := fmt.Sprintf("✗ run_%d: %s\n", i, reason); !strings.Contains(printed, want) {
			t.Fatalf("a resumed pur run --verbose printed\n%s\nwithout %q", printed, want)
		}
	}
}
