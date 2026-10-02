package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/pur/state"
	"github.com/rescale/rescale-int/internal/reporting"
)

const (
	outcomeJobsHeader = "Directory,JobName,AnalysisCode,Command,CoreType,CoresPerSlot,WalltimeHours,Slots," +
		"LicenseSettings,ExtraInputFileIDs,Submit\n"
	outcomeStateHeader = "Index,JobName,Directory,TarPath,TarStatus,FileID,UploadStatus,JobID,SubmitStatus," +
		"ExtraFileIDs,ErrorMessage,LastUpdated\n"
)

// writeOutcomeFiles writes the jobs CSV and the state file of a batch, each
// from its header and rows, into the working directory.
func writeOutcomeFiles(t *testing.T, jobs, states string) {
	t.Helper()
	if os.WriteFile("jobs.csv", []byte(outcomeJobsHeader+jobs), 0o600) != nil ||
		os.WriteFile("state.csv", []byte(outcomeStateHeader+states), 0o600) != nil {
		t.Fatal("write the batch's jobs and state")
	}
}

// fakePlatform creates and submits any job and answers the analyses lookup; it
// refuses every other request, which stops an upload at its first one. The
// first request starting with cancelOn ("METHOD /path") cancels the CLI's root
// context, as a Ctrl-C does, and is held until the client gives up on it.
type fakePlatform struct {
	cancelOn      string
	refuseCreates int // creates refused before any is answered

	mu               sync.Mutex
	creates, submits int
}

// use makes the fake the API of the commands that run a pipeline, under a root
// context of their own.
func (f *fakePlatform) use(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body) // until it is read, the server does not see the client hang up
		request := r.Method + " " + r.URL.Path
		f.mu.Lock()
		create := request == "POST /api/v3/jobs/"
		if create {
			f.creates++
		}
		refused := create && f.creates <= f.refuseCreates
		cancel := !refused && f.cancelOn != "" && strings.HasPrefix(request, f.cancelOn)
		if cancel {
			f.cancelOn = ""
		}
		if strings.HasSuffix(request, "/submit/") {
			f.submits++
		}
		f.mu.Unlock()
		switch {
		case refused:
			http.Error(w, `{"detail": "refused"}`, http.StatusBadRequest)
		case cancel:
			cancelFunc()
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		case create:
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"NEWJOB"}`)
		case strings.HasSuffix(request, "/submit/"):
		case strings.HasPrefix(request, "GET /api/v3/analyses/"):
			_, _ = io.WriteString(w, `{"count":0,"next":null,"results":[]}`)
		default:
			http.Error(w, `{"detail": "refused"}`, http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	origClient, origCtx, origCancel := newPipelineClientFn, rootContext, cancelFunc
	newPipelineClientFn = func(*config.Config) (*api.Client, error) {
		return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), nil
	}
	rootContext, cancelFunc = context.WithCancel(context.Background())
	t.Cleanup(func() { newPipelineClientFn, rootContext, cancelFunc = origClient, origCtx, origCancel })
}

// The resume dry run says what a real resume does, row by row, for every
// state a row can be in: with an archive of its own or without, and with the
// row's Submit as it was when the job was created or changed since. Each row
// is resumed for real against the fake platform afterwards, and both are held
// to the stage named here. Rows without an archive go to the job worker
// whatever their state, which submits whatever is not submitted when the row
// asks for it; a dry run that judged them by the archive rule called a draft
// whose Submit is now yes complete, while the resume submitted it.
func TestPURResumeDryRunMatchesAResume(t *testing.T) {
	for _, row := range []struct {
		name    string
		archive bool
		submit  string
		state   string // TarStatus/UploadStatus/JobID/SubmitStatus; "" for no row
		want    string
	}{
		{"submitted", true, "yes", "success/success/J1/success", "complete"},
		{"created as a draft", true, "no", "success/success/J1/skipped", "complete"},
		{"created as a draft, Submit now yes", true, "yes", "success/success/J1/skipped", "complete"},
		{"created, submit pending", true, "yes", "success/success/J1/pending", "submit"},
		{"created, submit pending, Submit now no", true, "no", "success/success/J1/pending", "complete"},
		{"submit failed", true, "yes", "success/success/J1/failed", "submit failed"},
		{"submit failed, Submit now no", true, "no", "success/success/J1/failed", "submit failed"},
		{"uploaded", true, "yes", "success/success//pending", "create"},
		{"creation failed, draft", true, "no", "success/success//failed", "create"},
		{"creation failed", true, "yes", "success/success//failed", "create"},
		{"archived", true, "yes", "success/pending//pending", "upload"},
		{"upload failed", true, "yes", "success/failed//failed", "upload"},
		{"pending", true, "yes", "pending/pending//pending", "tar"},
		{"archive failed", true, "yes", "failed/pending//failed", "tar"},
		{"no row", true, "yes", "", "tar"},
		{"creation unconfirmed", true, "yes", "success/success//creating", "unconfirmed"},
		{"no archive: submitted", false, "yes", "skipped/skipped/J1/success", "complete"},
		{"no archive: created as a draft", false, "no", "skipped/skipped/J1/skipped", "complete"},
		{"no archive: created as a draft, Submit now yes", false, "yes", "skipped/skipped/J1/skipped", "submit"},
		{"no archive: created, submit pending", false, "yes", "skipped/skipped/J1/pending", "submit"},
		{"no archive: created, submit pending, Submit now no", false, "no", "skipped/skipped/J1/pending", "complete"},
		{"no archive: submit failed", false, "yes", "skipped/skipped/J1/failed", "submit"},
		{"no archive: submit failed, Submit now no", false, "no", "skipped/skipped/J1/failed", "complete"},
		{"no archive: not created", false, "yes", "skipped/skipped//pending", "create"},
		{"no archive: creation failed, draft", false, "no", "skipped/skipped//failed", "create"},
		{"no archive: pending", false, "yes", "pending/pending//pending", "create"},
		{"no archive: no row", false, "yes", "", "create"},
		{"no archive: creation unconfirmed", false, "yes", "skipped/skipped//indeterminate", "unconfirmed"},
	} {
		t.Run(row.name, func(t *testing.T) {
			usePURConfig(t)
			t.Chdir(t.TempDir())
			dir, files := "", "deck1"
			if row.archive {
				dir, files = "run_1", ""
				writeScanDeck(t, ".", filepath.Join("run_1", "input.dat"))
			}
			states := ""
			if row.state != "" {
				s := strings.Split(row.state, "/")
				fileID, reason := "", ""
				if s[1] == "success" {
					fileID = "F1"
				}
				if strings.Contains(row.state, "failed") {
					reason = "an earlier failure"
				}
				states = fmt.Sprintf("1,run_1,%s,,%s,%s,%s,%s,%s,,%s,\n", dir, s[0], fileID, s[1], s[2], s[3], reason)
			}
			writeOutcomeFiles(t, fmt.Sprintf("%s,run_1,user_included,./solve.sh,emerald,4,1.0,1,,%s,%s\n", dir, files, row.submit), states)
			fake := &fakePlatform{}
			fake.use(t)

			dry := captureStdout(t, func() {
				if err := runPURCommand(t, newResumeCmd(), "--jobs-csv", "jobs.csv", "--state", "state.csv", "--dry-run"); err != nil {
					t.Errorf("dry run: %v", err)
				}
			})
			var err error
			captureStderr(t, func() {
				captureStdout(t, func() { err = runPURCommand(t, newResumeCmd(), "--jobs-csv", "jobs.csv", "--state", "state.csv") })
			})

			if got := dryRunStage(t, dry); got != row.want {
				t.Errorf("the dry run reports %q, want %q:\n%s", got, row.want, dry)
			}
			if got := resumedStage(t, fake, row.state, err); got != row.want {
				t.Errorf("the resume did %q (error %v), want %q", got, err, row.want)
			}
		})
	}
}

var dryRunLine = regexp.MustCompile(`(?m)^(Already complete|Need tar|Need upload|Need job create|Need submit|Submit failed|Could not be confirmed as created):\s+(\d+)`)

// dryRunStage is the one count a dry run of a one-job batch reports.
func dryRunStage(t *testing.T, printed string) string {
	t.Helper()
	names := map[string]string{"Already complete": "complete", "Need tar": "tar", "Need upload": "upload",
		"Need job create": "create", "Need submit": "submit", "Submit failed": "submit failed",
		"Could not be confirmed as created": "unconfirmed"}
	var stages []string
	for _, m := range dryRunLine.FindAllStringSubmatch(printed, -1) {
		if m[2] != "0" {
			stages = append(stages, names[m[1]])
		}
	}
	if len(stages) != 1 {
		t.Fatalf("the dry run reports %v, want one stage", stages)
	}
	return stages[0]
}

// resumedStage is what a resume of a one-job batch did first, from the fake's
// requests, the state it left and its error.
func resumedStage(t *testing.T, fake *fakePlatform, before string, err error) string {
	t.Helper()
	mgr := state.NewManager("state.csv")
	if loadErr := mgr.Load(); loadErr != nil {
		t.Fatal(loadErr)
	}
	after := mgr.GetState(1)
	fake.mu.Lock()
	creates, submits := fake.creates, fake.submits
	fake.mu.Unlock()
	switch {
	case creates > 0:
		return "create"
	case submits > 0:
		return "submit"
	case !strings.HasPrefix(before, "success/") && after.TarStatus == "success":
		return "tar"
	case after.UploadStatus == "failed":
		return "upload"
	case err != nil && strings.Contains(err.Error(), "could not be confirmed"):
		return "unconfirmed"
	case err != nil:
		return "submit failed"
	}
	return "complete"
}

// A run the user cancels says so, with how many of its jobs finished and how
// many did not, counting the jobs a previous run finished, and never that it
// completed. It fails, as a cancelled transfer does, without an error report.
func TestPURRunCancelledSaysHowFarItGot(t *testing.T) {
	usePURConfig(t)
	t.Chdir(t.TempDir())
	(&fakePlatform{}).use(t)
	cancelFunc()

	// Rows whose archives are uploaded: the feeder writes no state for them.
	for _, cmd := range []*cobra.Command{newRunCmd(), newResumeCmd(), newSubmitExistingCmd()} {
		writeOutcomeFiles(t,
			"run_1,run_1,user_included,./solve.sh,emerald,4,1.0,1,,deck1,no\n"+
				"run_2,run_2,user_included,./solve.sh,emerald,4,1.0,1,,deck1,no\n",
			"1,run_1,run_1,,success,f1,success,J1,skipped,,,\n"+
				"2,run_2,run_2,,success,f2,success,,pending,,,\n")

		var err error
		printed := captureStdout(t, func() {
			err = runPURCommand(t, cmd, "--jobs-csv", "jobs.csv", "--state", "state.csv")
		})
		if !errors.Is(err, context.Canceled) || reporting.IsReportable(err, reporting.CategoryPURPipeline) ||
			!strings.Contains(printed, "Cancelled: 1 of 2 job(s) finished, 1 did not\n") || strings.Contains(printed, "✓") {
			t.Errorf("pur %s, cancelled: returned %v after printing\n%s\nwant it to fail without a report, "+
				"the cancel stated and no success line", cmd.Name(), err, printed)
		}
	}
}

// A cancel that cuts off the upload of a common input file fails the run
// itself, which always exited 1: the cancel line is added and the exit code
// kept. That error names no job, so a job an earlier run left unconfirmed is
// named apart from it.
func TestPURCancelDuringACommonFileUploadKeepsTheRunsError(t *testing.T) {
	for _, cmd := range []*cobra.Command{newRunCmd(), newResumeCmd()} {
		t.Run(cmd.Name(), func(t *testing.T) {
			usePURConfig(t)
			t.Chdir(t.TempDir())
			writeOutcomeFiles(t, ",sweep_1,user_included,./solve.sh,emerald,4,1.0,1,,deck1,no\n"+
				",sweep_2,user_included,./solve.sh,emerald,4,1.0,1,,deck1,no\n",
				"1,sweep_1,,,skipped,,skipped,,indeterminate,,the answer was lost,\n")
			if err := os.WriteFile("common.dat", []byte("shared"), 0o600); err != nil {
				t.Fatal(err)
			}
			(&fakePlatform{cancelOn: "GET /api/v3/users/me/"}).use(t) // the upload's first request

			var err error
			var printed string
			named := captureStderr(t, func() {
				printed = captureStdout(t, func() {
					err = runPURCommand(t, cmd, "--jobs-csv", "jobs.csv", "--state", "state.csv", "--common-input-files", "common.dat")
				})
			})
			if err == nil || !strings.Contains(err.Error(), "pipeline failed: failed to resolve shared files") ||
				!strings.Contains(printed, "Cancelled: 0 of 2 job(s) finished, 2 did not\n") ||
				!strings.Contains(named, "1 job(s) could not be confirmed as created: sweep_1") {
				t.Errorf("returned %v after printing\n%s%s\nwant the run's own error, the cancel line and sweep_1 named",
					err, named, printed)
			}
		})
	}
}

// A cancel does not hide the jobs a run leaves failed or unconfirmed: they are
// named as when a run ends that way without one. Here the first creation is
// refused, and the cancel cuts off the second, which may have been created.
func TestPURCancelNamesTheJobsItLeaves(t *testing.T) {
	usePURConfig(t, "job_workers,1")
	t.Chdir(t.TempDir())
	writeOutcomeFiles(t, ",run_1,user_included,./solve.sh,emerald,4,1.0,1,,deck1,no\n"+
		",run_2,user_included,./solve.sh,emerald,4,1.0,1,,deck1,no\n", "")
	(&fakePlatform{refuseCreates: 1, cancelOn: "POST /api/v3/jobs/"}).use(t)

	var err error
	var printed string
	named := captureStderr(t, func() {
		printed = captureStdout(t, func() {
			err = runPURCommand(t, newRunCmd(), "--jobs-csv", "jobs.csv", "--state", "state.csv")
		})
	})
	for _, want := range []string{"✗ run_1: create job failed: status 400", "1 job(s) could not be confirmed as created: run_2"} {
		if !strings.Contains(named, want) {
			t.Errorf("the cancelled run printed\n%s\nwithout %q", named, want)
		}
	}
	if !errors.Is(err, context.Canceled) || !strings.Contains(printed, "Cancelled: 0 of 2 job(s) finished, 2 did not\n") {
		t.Errorf("returned %v after printing\n%s\nwant the cancel line and exit 1", err, printed)
	}
}

// Each job a run creates is named with its ID as it is created, and the run's
// common input files with theirs: a run without --state records them nowhere
// else.
func TestPURRunNamesTheIDsItCreates(t *testing.T) {
	usePURConfig(t)
	t.Chdir(t.TempDir())
	writeOutcomeFiles(t, ",sweep_1,user_included,./solve.sh,emerald,4,1.0,1,,deck1,no\n", "")
	fake := &fakeJobsAPI{}
	client := fake.client(t)
	orig := newPipelineClientFn
	newPipelineClientFn = func(*config.Config) (*api.Client, error) { return client, nil }
	t.Cleanup(func() { newPipelineClientFn = orig })

	var err error
	printed := captureStdout(t, func() {
		err = runPURCommand(t, newRunCmd(), "--jobs-csv", "jobs.csv", "--common-input-files", "id:shared-1")
	})
	for _, want := range []string{"✓ sweep_1: created job JOB1\n", "Common input file IDs: shared-1\n", "✓ Pipeline completed\n"} {
		if err != nil || !strings.Contains(printed, want) {
			t.Errorf("pur run returned %v after printing\n%s\nwant %q", err, printed, want)
		}
	}
}
