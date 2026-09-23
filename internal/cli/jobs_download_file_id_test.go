package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// jobFileSize is the size the fake platform reports for results.dat.
const jobFileSize = 1024

// jobTransfer is one download the command started, as downloadFileFn saw it.
type jobTransfer struct {
	skipChecksum     bool
	targetExisted    bool // a file was still at the target when the transfer began
	encryptedExisted bool // so was <target>.encrypted
}

// runJobsDownload runs 'jobs download --job-id job123' with args against a fake
// platform whose job has one output file, file123 "results.dat", reachable both
// through the job's file listing and by ID. Transfers are recorded rather than
// performed; each one writes the full-size file.
func runJobsDownload(t *testing.T, args ...string) ([]jobTransfer, error) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v3/files/file123/" {
			_, _ = fmt.Fprintf(w, `{"id":"file123","name":"results.dat","decryptedSize":%d}`, jobFileSize)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	origClient, origList, origDownload := getAPIClientFn, listJobFilesFn, downloadFileFn
	t.Cleanup(func() { getAPIClientFn, listJobFilesFn, downloadFileFn = origClient, origList, origDownload })

	getAPIClientFn = func() (*api.Client, error) {
		return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), nil
	}
	listJobFilesFn = func(context.Context, *api.Client, string) ([]models.JobFile, error) {
		return []models.JobFile{{ID: "file123", Name: "results.dat", DecryptedSize: jobFileSize}}, nil
	}
	var mu sync.Mutex
	var transfers []jobTransfer
	downloadFileFn = func(_ context.Context, p download.DownloadParams) error {
		_, targetErr := os.Stat(p.LocalPath)
		_, encErr := os.Stat(p.LocalPath + ".encrypted")
		mu.Lock()
		transfers = append(transfers, jobTransfer{p.SkipChecksum, targetErr == nil, encErr == nil})
		mu.Unlock()
		return os.WriteFile(p.LocalPath, make([]byte, jobFileSize), 0o644)
	}

	cmd := newJobsDownloadCmd()
	cmd.SetArgs(append([]string{"--job-id", "job123"}, args...))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true

	err := cmd.Execute()
	return transfers, err
}

// With --file-id the command took a path of its own that ignored --skip,
// --overwrite, --resume and --skip-checksum and always wrote over the target.
// Each case runs in both modes and has to come out the same.
func TestJobsDownloadFileIDHonoursConflictAndChecksumFlags(t *testing.T) {
	tests := []struct {
		name         string
		flags        []string
		onDisk       int  // bytes already at the target; 0 for no file
		encrypted    bool // a complete <target>.encrypted from an interrupted run is beside it
		wantErr      string
		wantTransfer bool
	}{
		{name: "no flag keeps a complete file", onDisk: jobFileSize},
		{name: "--skip keeps a complete file", flags: []string{"--skip"}, onDisk: jobFileSize},
		{name: "--skip replaces a wrong-size file", flags: []string{"--skip"}, onDisk: 9, wantTransfer: true},
		{name: "--overwrite replaces a complete file", flags: []string{"--overwrite"}, onDisk: jobFileSize, wantTransfer: true},
		{name: "--resume keeps the encrypted copy to retry from", flags: []string{"--resume"}, onDisk: jobFileSize, encrypted: true, wantTransfer: true},
		{name: "--skip-checksum reaches the transfer", flags: []string{"--skip-checksum"}, wantTransfer: true},
		{name: "--skip with --overwrite is refused", flags: []string{"--skip", "--overwrite"},
			wantErr: "only one of --overwrite, --skip, or --resume can be specified"},
	}

	for _, tt := range tests {
		for _, mode := range []string{"all files", "file-id"} {
			t.Run(tt.name+"/"+mode, func(t *testing.T) {
				dir := t.TempDir()
				target := filepath.Join(dir, "results.dat")
				if tt.onDisk > 0 {
					if err := os.WriteFile(target, make([]byte, tt.onDisk), 0o644); err != nil {
						t.Fatalf("seed target: %v", err)
					}
				}
				if tt.encrypted {
					// Within the 1-16 bytes of padding a complete encrypted copy carries.
					if err := os.WriteFile(target+".encrypted", make([]byte, jobFileSize+16), 0o644); err != nil {
						t.Fatalf("seed encrypted copy: %v", err)
					}
				}
				args := []string{"--outdir", dir}
				if mode == "file-id" {
					args = []string{"--file-id", "file123", "--output", target}
				}

				transfers, err := runJobsDownload(t, append(args, tt.flags...)...)

				if tt.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
						t.Fatalf("error = %v, want %q", err, tt.wantErr)
					}
					if len(transfers) != 0 {
						t.Errorf("a refused command started %d transfer(s)", len(transfers))
					}
					return
				}
				if err != nil {
					t.Fatalf("jobs download: %v", err)
				}
				if !tt.wantTransfer {
					if len(transfers) != 0 {
						t.Fatalf("the complete file on disk was downloaded again (%d transfer(s)), want it kept", len(transfers))
					}
					if _, err := os.Stat(target); err != nil {
						t.Errorf("the kept file is gone: %v", err)
					}
					return
				}
				if len(transfers) != 1 {
					t.Fatalf("%d transfers, want 1", len(transfers))
				}
				got := transfers[0]
				if got.targetExisted {
					t.Error("the transfer began on top of the existing file: the conflict handling did not run")
				}
				if got.encryptedExisted != tt.encrypted {
					t.Errorf("encrypted copy present when the transfer began = %v, want %v", got.encryptedExisted, tt.encrypted)
				}
				if want := slices.Contains(tt.flags, "--skip-checksum"); got.skipChecksum != want {
					t.Errorf("SkipChecksum = %v, want %v", got.skipChecksum, want)
				}
			})
		}
	}
}

// runRefusedJobsDownload runs 'jobs download --job-id job123' with args, which
// must be refused before any work starts: building an API client fails the test.
func runRefusedJobsDownload(t *testing.T, args ...string) error {
	t.Helper()
	orig := getAPIClientFn
	getAPIClientFn = func() (*api.Client, error) {
		t.Errorf("jobs download %s got as far as building an API client", strings.Join(args, " "))
		return nil, errors.New("no client for a refused command")
	}
	t.Cleanup(func() { getAPIClientFn = orig })

	cmd := newJobsDownloadCmd()
	cmd.SetArgs(append([]string{"--job-id", "job123"}, args...))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	return cmd.Execute()
}

// Each mode ignored the other's flags without a word, and --file-id with a
// directory as --output, however spelled, wrote <dir>.file or <dir>/.file. All
// are refused before any work starts, and nothing is written.
func TestJobsDownloadRefusesTheOtherModesFlags(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("results", 0o755); err != nil {
		t.Fatal(err)
	}
	sep := string(filepath.Separator)
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"--file-id", "f1", "--outdir", "results"}, "--outdir"},
		{[]string{"--file-id", "f1", "--max-concurrent", "3"}, "--max-concurrent"},
		{[]string{"--file-id", "f1", "--filter", "*.dat"}, "--filter"},
		{[]string{"--file-id", "f1", "--exclude", "debug*"}, "--exclude"},
		{[]string{"--file-id", "f1", "--search", "final"}, "--search"},
		{[]string{"--file-id", "f1", "--path-filter", "run_1/*"}, "--path-filter"},
		{[]string{"--file-id", "f1", "--output", "results"}, "names a directory"},
		{[]string{"--file-id", "f1", "--output", "new" + sep}, "names a directory"},
		{[]string{"--file-id", "f1", "--output", "new" + sep + "."}, "names a directory"},
		{[]string{"--file-id", "f1", "--output", "new" + sep + ".."}, "names a directory"},
		{[]string{"-o", "results"}, "use --outdir"},
		{[]string{"-m", "0"}, "--max-concurrent must be between 1 and 20, got 0"},
		{[]string{"-m", "21"}, "got 21"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			if err := runRefusedJobsDownload(t, tt.args...); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want %q", err, tt.want)
			}
		})
	}
	_ = filepath.WalkDir(".", func(p string, _ fs.DirEntry, err error) error {
		if err == nil && p != "." && p != "results" {
			t.Errorf("a refused download wrote %s", p)
		}
		return nil
	})
}

// A cancel that lands before the batch starts a file (with --file-id, between
// fetching its metadata and the transfer) printed the success summary and
// exited 0.
func TestDownloadBatchCancelledBeforeStartFails(t *testing.T) {
	orig := downloadFileFn
	t.Cleanup(func() { downloadFileFn = orig })
	downloadFileFn = func(context.Context, download.DownloadParams) error {
		t.Error("a transfer started after the cancel")
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runDownloadBatch(ctx, []cliDownloadItem{{fileID: "file123", name: "results.dat", size: jobFileSize,
		localPath: filepath.Join(t.TempDir(), "results.dat")}}, downloadBatchOptions{maxConcurrent: 1, logger: GetLogger()})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want the cancellation", err)
	}
}

// The flags --file-id refuses still work when downloading all of a job's files.
func TestJobsDownloadAllFilesKeepsItsFlags(t *testing.T) {
	transfers, err := runJobsDownload(t, "--outdir", t.TempDir(), "--max-concurrent", "3", "--filter", "*.dat",
		"--exclude", "debug*", "--search", "results", "--path-filter", "results.*")
	if err != nil || len(transfers) != 1 {
		t.Fatalf("jobs download: %d transfer(s), error %v; want 1 and no error", len(transfers), err)
	}
}
