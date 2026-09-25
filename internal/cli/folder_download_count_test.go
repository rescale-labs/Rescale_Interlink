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

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/resources"
)

// Every file of a folder download is counted once: downloaded, skipped, failed,
// or not started because a failure stopped the download first. A conflict it
// cannot settle fails like a failed download, and says why: without
// --continue-on-error the first one stops the rest, and an Abort always does,
// even for a worker already waiting to ask or about to replace a file, which
// keeps it; the command fails. --overwrite replaces an existing file however
// big it is.
func TestDownloadFolderRecursive_CountsEveryFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[`+
			`{"type":"file","item":{"id":"f1","name":"a.dat","decryptedSize":4}},`+
			`{"type":"file","item":{"id":"f2","name":"b.dat","decryptedSize":4}},`+
			`{"type":"file","item":{"id":"f3","name":"c.dat","decryptedSize":4}}]}`)
	}))
	defer server.Close()
	client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})
	origDownload, origRemove, origClient := downloadFileFn, removeFile, getAPIClientFn
	origMode, origFolder, origFile, origStep := askFolderDownloadMode, promptFolderDownloadConflictFn, promptDownloadConflictFn, replaceStep
	t.Cleanup(func() {
		downloadFileFn, removeFile, getAPIClientFn = origDownload, origRemove, origClient
		askFolderDownloadMode, promptFolderDownloadConflictFn, promptDownloadConflictFn, replaceStep = origMode, origFolder, origFile, origStep
	})
	refuse := func(string) error { return errors.New("FAKE removal refused") }
	getAPIClientFn = func() (*api.Client, error) { return client, nil }
	askFolderDownloadMode = func() (FolderDownloadMode, error) { return FolderDownloadModePrompt, nil }
	promptFolderDownloadConflictFn = func(string, string) (FolderDownloadConflictAction, error) { return FolderDownloadMergeOnce, nil }
	// What an earlier run left: a.dat, c.dat, and b.dat beside a folder of that
	// name, which it is saved as b.dat.file.
	leftovers := func(t *testing.T, dir, content string) {
		if err := os.MkdirAll(filepath.Join(dir, "b.dat"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a.dat", "b.dat.file", "c.dat"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	unexpected := func(context.Context, download.DownloadParams) error {
		t.Error("downloaded a file it could not replace")
		return errors.New("unexpected download")
	}
	answer := func(action DownloadConflictAction, err error) func(string, string) (DownloadConflictAction, error) {
		return func(string, string) (DownloadConflictAction, error) { return action, err }
	}
	// Two workers: b.dat's reaches its conflict once a.dat's is asking or about
	// to replace its file, which its warning about the folder named b.dat tells.
	// An Abort from a.dat's means b.dat's is not asked; one from b.dat's, that
	// a.dat is not replaced.
	var asking, waiting chan struct{}
	var passing func()
	logger := logging.NewLoggerWithWriter(writerFunc(func(p []byte) (int, error) {
		if passing != nil && strings.Contains(string(p), "renaming file") {
			passing()
		}
		return len(p), nil
	}))

	for _, tc := range []struct {
		name, flag, local           string // local: what an earlier run left of each file, "" for no folder
		ask                         func(name, path string) (DownloadConflictAction, error)
		continueOnError, removes    bool
		workers                     int
		downloaded, failed, stopped int
		said                        string // printed for the file that failed
	}{
		{"a failure without --continue-on-error", "--overwrite", "", nil, false, true, 1, 0, 1, 2, ""},
		{"existing files it cannot replace", "--merge", "x", nil, true, false, 1, 0, 3, 0, "b.dat: failed to remove incomplete existing file: FAKE removal refused"},
		{"--overwrite over files of the same size", "--overwrite", "old!", nil, false, true, 1, 3, 0, 0, ""},
		{"an Abort", "", "x", answer(DownloadAbort, nil), false, true, 1, 0, 1, 2, "a.dat: download aborted by user"},
		{"an Abort with --continue-on-error", "", "x", answer(DownloadAbort, nil), true, true, 1, 0, 1, 2, "a.dat: download aborted by user"},
		{"no answer", "", "x", answer(DownloadAbort, io.EOF), false, true, 1, 0, 1, 2, "a.dat: no answer (end of input)"},
		{"a prompt that fails", "", "x", answer(DownloadAbort, errors.New("FAKE prompt failure on https://example.invalid/?sig=FAKESIG")), false, true, 1, 0, 1, 2,
			"a.dat: FAKE prompt failure on https://example.invalid/?sig=REDACTED"},
		{"a file it cannot overwrite", "", "x", answer(DownloadOverwriteOnce, nil), false, false, 1, 0, 1, 2, "a.dat: failed to remove existing file: FAKE removal refused"},
		{"an Abort while another worker waits to ask", "", "x", func(name, _ string) (DownloadConflictAction, error) {
			if name != "a.dat" {
				t.Errorf("asked about %s after an Abort", name)
				return DownloadSkipOnce, nil
			}
			close(asking)
			<-waiting
			return DownloadAbort, nil
		}, false, true, 2, 0, 1, 2, "a.dat: download aborted by user"},
		{"an Abort before another worker replaces its file", "", "x", func(name, _ string) (DownloadConflictAction, error) {
			if name == "a.dat" {
				return DownloadOverwriteOnce, nil
			}
			return DownloadAbort, nil
		}, false, true, 2, 0, 1, 2, "b.dat: download aborted by user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outDir := t.TempDir()
			if tc.local != "" {
				leftovers(t, filepath.Join(outDir, "myfolder"), tc.local)
			}
			downloadFileFn = func(ctx context.Context, p download.DownloadParams) error {
				if tc.local == "x" {
					return unexpected(ctx, p)
				}
				if tc.flag == "--overwrite" && tc.local == "" {
					return errors.New("connection reset")
				}
				return os.WriteFile(p.LocalPath, []byte("new!"), 0o644)
			}
			removeFile, promptDownloadConflictFn, passing, replaceStep = refuse, tc.ask, nil, func(context.Context) {}
			if tc.removes {
				removeFile = os.Remove
			}
			if tc.workers > 1 {
				asking, waiting = make(chan struct{}), make(chan struct{})
				passing = func() { <-asking; close(waiting) }
				replaceStep = func(ctx context.Context) { close(asking); <-ctx.Done() }
			}

			var result *DownloadResult
			var err error
			said := captureStderr(t, func() {
				result, err = DownloadFolderRecursive(context.Background(), "folder123", "myfolder", outDir,
					tc.flag == "--overwrite", false, tc.flag == "--merge", tc.continueOnError, tc.workers, true, false, client, logger,
					resources.NewManager(resources.Config{AutoScale: true, MaxThreads: 4}))
			})
			if err != nil {
				t.Fatalf("DownloadFolderRecursive: %v", err)
			}
			if result.FilesDownloaded != tc.downloaded || result.FilesSkipped != 0 || result.FilesFailed != tc.failed || result.FilesNotStarted != tc.stopped {
				t.Errorf("counted %d downloaded, %d skipped, %d failed, %d not started; want %d, 0, %d, %d",
					result.FilesDownloaded, result.FilesSkipped, result.FilesFailed, result.FilesNotStarted, tc.downloaded, tc.failed, tc.stopped)
			}
			if !strings.Contains(said, tc.said) || strings.Contains(said, "FAKESIG") {
				t.Errorf("printed %q, want %q", said, tc.said)
			}
			want := tc.local
			if tc.downloaded > 0 {
				want = "new!"
			}
			if got, _ := os.ReadFile(filepath.Join(outDir, "myfolder", "a.dat")); tc.local != "" && string(got) != want {
				t.Errorf("a.dat holds %q, want %q", got, want)
			}
		})
	}

	removeFile, replaceStep = refuse, origStep
	out := t.TempDir()
	leftovers(t, filepath.Join(out, "folder123"), "x")
	var printed string
	var err error
	said := captureStderr(t, func() {
		printed, err = runWithCancel(t, newFoldersCmd(), unexpected, "download-dir", "folder123", "--outdir", out, "--merge", "--max-concurrent", "1")
	})
	if err == nil || !strings.Contains(printed, "Files failed:       1\n") || !strings.Contains(printed, "Files not started:  2\n") ||
		!strings.Contains(said, "failed to remove incomplete existing file: FAKE removal refused") {
		t.Errorf("folders download-dir returned %v after printing\n%s%s\nwant it to stop at the first file, fail and say why", err, printed, said)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// captureStderr returns what f writes to os.Stderr.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	file, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	defer func(orig *os.File) { os.Stderr = orig }(os.Stderr)
	os.Stderr = file
	f()
	said, _ := os.ReadFile(file.Name())
	return string(said)
}
