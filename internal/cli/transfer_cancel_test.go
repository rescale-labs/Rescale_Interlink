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
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/cloud/upload"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/reporting"
)

// transferStep is what one file's transfer does, given the run's context and
// the user's cancel. A file without a step transfers.
type transferStep func(ctx context.Context, cancel func()) error

var (
	// stepCancels is a cancel that lands during the transfer, which fails with it.
	stepCancels transferStep = func(ctx context.Context, cancel func()) error {
		cancel()
		return fmt.Errorf("S3Storage transfer failed: %w", ctx.Err())
	}
	// stepFinishesAsCancelled is a cancel that lands as the transfer completes.
	stepFinishesAsCancelled transferStep = func(_ context.Context, cancel func()) error { cancel(); return nil }
	// stepFails is a failure of the transfer's own, the kind a report is filed for.
	stepFails transferStep = func(context.Context, func()) error { return errors.New("unexpected response from storage") }
)

// fakeTransferAPI is an API client whose server gives fixed answers, so the
// credential warm-up reaches no port the test does not own. A file looked up by
// its ID is <ID>.dat, one byte long; any other request gets an empty object.
func fakeTransferAPI(t *testing.T) *api.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if id, ok := strings.CutPrefix(r.URL.Path, "/api/v3/files/"); ok {
			id = strings.Trim(id, "/")
			_, _ = fmt.Fprintf(w, `{"id":%q,"name":%q,"decryptedSize":1}`, id, id+".dat")
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})
}

// transferRun is a run of a transfer command that the user may cancel.
type transferRun struct {
	files     []string                // in the order given or listed; for files download, the IDs of <ID>.dat
	steps     map[string]transferStep // what a file's transfer does, by name
	before    map[string]string       // "file" or "link": what is at a file's target before the run
	flags     []string
	workers   int  // files at once; 0 is one
	preCancel bool // the cancel came before the command started
}

// ended is how a run ended for the user: what it printed, its error, whether
// the CLI filed an error report for that error, and what it printed with it.
type ended struct {
	printed  string
	err      error
	report   bool
	reported string
}

func (r transferRun) upload(t *testing.T) ended {
	client := fakeTransferAPI(t)
	defer func(c func() (*api.Client, error), u func(context.Context, upload.UploadParams) (*models.CloudFile, error)) {
		getAPIClientFn, uploadFileFn = c, u
	}(getAPIClientFn, uploadFileFn)
	getAPIClientFn = func() (*api.Client, error) { return client, nil }
	uploadFileFn = func(ctx context.Context, p upload.UploadParams) (*models.CloudFile, error) {
		if err := r.step(ctx, p.LocalPath); err != nil {
			return nil, err
		}
		return &models.CloudFile{ID: "FAKEID"}, nil
	}
	args := []string{"--no-check-duplicates", "--max-concurrent", fmt.Sprint(max(r.workers, 1))}
	for _, name := range r.files {
		args = append(args, writeUploadFixture(t, name, 1))
	}
	return r.runCommand(t, "rescale-int files upload", newFilesUploadCmd(), args...)
}

func (r transferRun) jobsDownload(t *testing.T) ended {
	client := fakeTransferAPI(t)
	defer func(c func() (*api.Client, error), l func(context.Context, *api.Client, string) ([]models.JobFile, error),
		d func(context.Context, download.DownloadParams) error) {
		getAPIClientFn, listJobFilesFn, downloadFileFn = c, l, d
	}(getAPIClientFn, listJobFilesFn, downloadFileFn)
	getAPIClientFn = func() (*api.Client, error) { return client, nil }
	listJobFilesFn = func(context.Context, *api.Client, string) ([]models.JobFile, error) {
		listed := make([]models.JobFile, len(r.files))
		for i, name := range r.files {
			listed[i] = models.JobFile{ID: fmt.Sprintf("file%d", i), Name: name, DecryptedSize: 1}
		}
		return listed, nil
	}
	downloadFileFn = r.download
	return r.runCommand(t, "rescale-int jobs download", newJobsDownloadCmd(),
		append([]string{"--job-id", "job123", "--outdir", r.outdir(t), "--max-concurrent", fmt.Sprint(max(r.workers, 1))}, r.flags...)...)
}

// filesDownload runs files download's helper as the command does: the command
// builds its own API client, which a test cannot replace.
func (r transferRun) filesDownload(t *testing.T) ended {
	client := fakeTransferAPI(t)
	defer func(d func(context.Context, download.DownloadParams) error) { downloadFileFn = d }(downloadFileFn)
	downloadFileFn = r.download
	out := r.outdir(t)
	return r.run(t, "rescale-int files download", func() error {
		return executeFileDownload(GetContext(), r.files, out, 1, false, false, false, false, client, GetLogger())
	})
}

func (r transferRun) download(ctx context.Context, p download.DownloadParams) error {
	if err := r.step(ctx, p.LocalPath); err != nil {
		return err
	}
	return os.WriteFile(p.LocalPath, []byte("x"), 0o644)
}

func (r transferRun) step(ctx context.Context, path string) error {
	if step := r.steps[filepath.Base(path)]; step != nil {
		return step(ctx, cancelFunc)
	}
	return nil
}

// outdir is a new output folder holding what r has in it before the run.
func (r transferRun) outdir(t *testing.T) string {
	t.Helper()
	out := t.TempDir()
	for name, what := range r.before {
		target := filepath.Join(out, name)
		if what == "link" {
			if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), target); err != nil {
				t.Skipf("cannot make a symbolic link here: %v", err)
			}
		} else if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func (r transferRun) runCommand(t *testing.T, operation string, cmd *cobra.Command, args ...string) ended {
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	return r.run(t, operation, cmd.Execute)
}

// run runs fn under a new root context, cancelled first if r says so.
func (r transferRun) run(t *testing.T, operation string, fn func() error) ended {
	t.Helper()
	defer func(ctx context.Context, cancel context.CancelFunc) { rootContext, cancelFunc = ctx, cancel }(rootContext, cancelFunc)
	rootContext, cancelFunc = context.WithCancel(context.Background())
	if r.preCancel {
		cancelFunc()
	}
	var e ended
	said := captureStderr(t, func() { e.printed = captureStdout(t, func() { e.err = fn() }) })
	e.printed += said
	if e.err != nil {
		home := t.TempDir() // a report, if the CLI files one, lands here
		for _, env := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "LOCALAPPDATA"} {
			t.Setenv(env, home)
		}
		e.reported = captureStderr(t, func() { e.report = reporting.HandleCLIError(e.err, "cli", operation, "") != "" })
	}
	return e
}

// A cancelled transfer says once that it was cancelled, after output that
// counts every file. When nothing but the cancel stopped the files that did
// not finish, it ends there, with the count of files never started and no
// advice. A failure before the cancel is what it ends with instead, as it
// would without the cancel, advice included. Either way the exit status and
// whether an error report is filed are what they were before cancelled
// transfers said so: a download is judged on its first error, an upload on
// all of them.
func TestCancelledTransfersSayHowTheyEnded(t *testing.T) {
	three := []string{"f0.dat", "f1.dat", "f2.dat"}
	four := append(three, "f3.dat")
	refuses := func(context.Context, func()) error { return errors.New("open f0.dat: permission denied") }
	cancelsSecond := map[string]transferStep{"f1.dat": stepCancels}
	for _, tc := range []struct {
		name      string
		end       func(*testing.T) ended
		want      string   // the error, "…" standing for any text in it; "" for none
		printed   []string // printed, in any order
		unstarted []string // files never started, which nothing printed may name
		report    bool
		advice    bool // the error names access to the job as a cause to check
	}{
		{name: "download, cancel before it starts", end: transferRun{files: three, preCancel: true}.jobsDownload,
			want:    "download cancelled: 3 file(s) not started: context canceled",
			printed: []string{"✓ Successfully downloaded 0 file(s)\n", "✗ Failed to download 3 file(s)\n"}, unstarted: three},
		{name: "download, cancel during a file", end: transferRun{files: three, steps: cancelsSecond}.jobsDownload,
			want:    "download cancelled: 1 file(s) not started: context canceled",
			printed: []string{"✓ Successfully downloaded 1 file(s)\n", "✗ Failed to download 2 file(s)\n"}, unstarted: three[2:]},
		{name: "download, cancel as a file finishes",
			end:     transferRun{files: three, steps: map[string]transferStep{"f1.dat": stepFinishesAsCancelled}}.jobsDownload,
			want:    "download cancelled: 1 file(s) not started: context canceled",
			printed: []string{"✓ Successfully downloaded 2 file(s)\n", "✗ Failed to download 1 file(s)\n"}, unstarted: three[2:]},
		{name: "download, cancel during its last file", end: transferRun{files: three[:2], steps: cancelsSecond}.jobsDownload,
			want:    "download cancelled: context canceled",
			printed: []string{"✓ Successfully downloaded 1 file(s)\n", "✗ Failed to download 1 file(s)\n"}},
		{name: "download, cancel as its last file finishes",
			end:     transferRun{files: three, steps: map[string]transferStep{"f2.dat": stepFinishesAsCancelled}}.jobsDownload,
			printed: []string{"✓ Successfully downloaded 3 file(s)\n"}},
		{name: "download with a skip and a refused name, cancel during a file",
			end: transferRun{files: []string{"run:1.dat", "f1.dat", "f2.dat", "f3.dat"}, before: map[string]string{"f1.dat": "file"},
				steps: map[string]transferStep{"f2.dat": stepCancels}, flags: []string{"--skip"}}.jobsDownload,
			want: `download cancelled after an earlier failure: invalid filename from API for file file0: filename cannot contain ':': "run:1.dat"`,
			printed: []string{`✗ invalid filename from API for file file0: filename cannot contain ':': "run:1.dat"` + "\n",
				"⊘ Skipping existing file: f1.dat\n", "✓ Successfully downloaded 0 file(s)\n", "⊘ Skipped 1 file(s)\n", "✗ Failed to download 3 file(s)\n"},
			unstarted: four[3:], report: true},
		{name: "download, cancel after a failure",
			end: transferRun{files: three, steps: map[string]transferStep{"f0.dat": stepFails, "f1.dat": stepCancels}}.jobsDownload,
			want: "download cancelled after an earlier failure: download failed for \"f0.dat\" (file file0, job job123, storage: unknown)\n" +
				"  Step: downloading\n  Cause: unexpected response from storage\n  Try: rerun with --debug for details, or verify you have access to this job",
			printed:   []string{"✓ Successfully downloaded 0 file(s)\n", "✗ Failed to download 3 file(s)\n"},
			unstarted: three[2:], report: true, advice: true},
		{name: "download, cancel after a prompt refused for want of a terminal",
			end: transferRun{files: []string{"AAA", "BBB"}, before: map[string]string{"AAA.dat": "file"},
				steps: map[string]transferStep{"BBB.dat": stepCancels}}.filesDownload,
			want: "download cancelled after an earlier failure: conflict prompt failed: cannot prompt for a download conflict: " +
				"no interactive terminal (stdin is not a TTY) — decide up front with this command's conflict flags (--overwrite or --skip; see --help for the rest)",
			printed: []string{"✓ Successfully downloaded 0 file(s)\n", "✗ Failed to download 2 file(s)\n"}},
		{name: "download, cancel after refusing a planted link",
			end: transferRun{files: three[:2], before: map[string]string{"f0.dat": "link"}, steps: cancelsSecond,
				flags: []string{"--overwrite"}}.jobsDownload,
			want:    `download cancelled after an earlier failure: refusing to download to …f0.dat": it is a symbolic link`,
			printed: []string{"✓ Successfully downloaded 0 file(s)\n", "✗ Failed to download 2 file(s)\n"}},
		{name: "download, cancel after a refusal and a failure a report is filed for",
			end: transferRun{files: four, before: map[string]string{"f0.dat": "link"},
				steps: map[string]transferStep{"f1.dat": stepFails, "f2.dat": stepCancels}}.jobsDownload,
			want:      `download cancelled after an earlier failure: refusing to download to …f0.dat": it is a symbolic link`,
			printed:   []string{"✓ Successfully downloaded 0 file(s)\n", "✗ Failed to download 4 file(s)\n"},
			unstarted: four[3:]},

		{name: "upload, cancel before it starts", end: transferRun{files: three, preCancel: true}.upload,
			want: "upload cancelled: 3 file(s) not started: context canceled", unstarted: three},
		{name: "upload, cancel during a file", end: transferRun{files: three, steps: cancelsSecond}.upload,
			want:      "upload cancelled: 1 file(s) not started: context canceled",
			printed:   []string{"f0.dat → My Library (FileID: FAKEID", "f1.dat → My Library: S3Storage transfer failed: context canceled (after 0 retries)\n"},
			unstarted: three[2:]},
		{name: "upload, cancel as a file finishes",
			end:       transferRun{files: three, steps: map[string]transferStep{"f1.dat": stepFinishesAsCancelled}}.upload,
			want:      "upload cancelled: 1 file(s) not started: context canceled",
			printed:   []string{"f0.dat → My Library (FileID: FAKEID", "f1.dat → My Library (FileID: FAKEID"},
			unstarted: three[2:]},
		{name: "upload, cancel during its last file", end: transferRun{files: three[:2], steps: cancelsSecond}.upload,
			want:    "upload cancelled: context canceled",
			printed: []string{"f0.dat → My Library (FileID: FAKEID", "f1.dat → My Library: S3Storage transfer failed: context canceled (after 0 retries)\n"}},
		{name: "upload, cancel as its last file finishes",
			end:     transferRun{files: three, steps: map[string]transferStep{"f2.dat": stepFinishesAsCancelled}}.upload,
			printed: []string{"✓ Successfully uploaded 3 file(s)\n"}},
		{name: "upload, cancel after a failure",
			end:  transferRun{files: three, steps: map[string]transferStep{"f0.dat": stepFails, "f1.dat": stepCancels}}.upload,
			want: "upload cancelled after an earlier failure: upload failed: 3 file(s) failed (first error: failed to upload …f0.dat: unexpected response from storage)",
			printed: []string{"f0.dat → My Library: unexpected response from storage (after 0 retries)\n",
				"f1.dat → My Library: S3Storage transfer failed: context canceled (after 0 retries)\n"},
			unstarted: three[2:], report: true},
		{name: "upload, cancel after a refusal and a failure a report is filed for",
			end:  transferRun{files: four, steps: map[string]transferStep{"f0.dat": refuses, "f1.dat": stepFails, "f2.dat": stepCancels}}.upload,
			want: "upload cancelled after an earlier failure: upload failed: 4 file(s) failed (first error: failed to upload …f0.dat: open f0.dat: permission denied)",
			printed: []string{"f0.dat → My Library: open f0.dat: permission denied (after 0 retries)\n",
				"f1.dat → My Library: unexpected response from storage (after 0 retries)\n",
				"f2.dat → My Library: S3Storage transfer failed: context canceled (after 0 retries)\n"},
			unstarted: four[3:], report: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Contains(tc.name, "terminal") && IsTerminal() {
				t.Skip("needs a stdin that is not a terminal")
			}
			e := tc.end(t)
			got := ""
			if e.err != nil {
				got = e.err.Error()
			}
			before, after, cut := strings.Cut(tc.want, "…")
			if !cut && got != tc.want || cut && !(strings.HasPrefix(got, before) && strings.HasSuffix(got, after) && len(got) >= len(before)+len(after)) {
				t.Errorf("ended with %q, want %q", got, tc.want)
			}
			if e.report != tc.report {
				t.Errorf("error report filed: %v, want %v", e.report, tc.report)
			}
			// The run says cancelled once, in its closing line. An error report,
			// where one is filed, adds its own lines, checked for what they name.
			said := e.printed + got
			if n, want := strings.Count(said, "cancelled"), min(len(tc.want), 1); n != want {
				t.Errorf("said cancelled %d times, want %d", n, want)
			}
			said += "\n" + e.reported
			for _, line := range tc.printed {
				if !strings.Contains(said, line) {
					t.Errorf("printed no %q", line)
				}
			}
			for _, name := range tc.unstarted {
				if strings.Contains(said, name) {
					t.Errorf("printed a line for %s, which never started", name)
				}
			}
			if strings.Contains(said, "verify you have access") != tc.advice {
				t.Errorf("access advice given: %v, want %v", !tc.advice, tc.advice)
			}
			if t.Failed() {
				t.Logf("printed:\n%s", said)
			}
		})
	}
}

// With two files at once the cancel can cut one while the other fails for a
// reason of its own, and either error can be recorded first. An upload weighs
// every error, so whichever came first it files a report and ends naming an
// earlier failure. A download is judged on its first error alone, so its ending
// never shows a cut file's error, nor the advice that comes with it.
func TestCancelledTransfersAtTwoWorkersKeepTheirRules(t *testing.T) {
	// both is a run in which the two files are in flight together, whatever the
	// processor count: f0's transfer cancels only once f1's has started, and
	// f1's then fails for a reason of its own. Each run has its own signal.
	both := func() transferRun {
		started := make(chan struct{})
		return transferRun{files: []string{"f0.dat", "f1.dat"}, workers: 2, steps: map[string]transferStep{
			"f0.dat": func(ctx context.Context, cancel func()) error {
				select {
				case <-started:
				case <-time.After(10 * time.Second):
					t.Error("f1's transfer did not start while f0's ran")
				}
				return stepCancels(ctx, cancel)
			},
			"f1.dat": func(ctx context.Context, _ func()) error {
				close(started)
				<-ctx.Done()
				time.Sleep(50 * time.Millisecond) // the cut file's error is recorded first, the order a wrong rule fails in
				return errors.New("unexpected response from storage")
			},
		}}
	}

	up := both().upload(t)
	if got := fmt.Sprint(up.err); !up.report ||
		!strings.HasPrefix(got, "upload cancelled after an earlier failure: upload failed: 2 file(s) failed (first error: ") {
		t.Errorf("upload ended %q, report filed %v; want an earlier failure named, and a report", got, up.report)
	}
	down := both().jobsDownload(t)
	if got := fmt.Sprint(down.err); down.err == nil || strings.Contains(got, "Cause: context canceled") {
		t.Errorf("download ended %q; want no cut file's error in it", got)
	}
}
