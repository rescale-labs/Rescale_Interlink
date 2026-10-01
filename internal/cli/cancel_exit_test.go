package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/models"
)

// runWithCancel runs cmd with args under a fresh root context, each file's
// download replaced by fn, and returns what it printed and its error.
func runWithCancel(t *testing.T, cmd *cobra.Command, fn func(context.Context, download.DownloadParams) error, args ...string) (string, error) {
	t.Helper()
	defer func(ctx context.Context, cancel context.CancelFunc, dl func(context.Context, download.DownloadParams) error) {
		rootContext, cancelFunc, downloadFileFn = ctx, cancel, dl
	}(rootContext, cancelFunc, downloadFileFn)
	rootContext, cancelFunc = context.WithCancel(context.Background())
	downloadFileFn = fn
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	var err error
	printed := captureStdout(t, func() { err = cmd.Execute() })
	return printed, err
}

// A cancel that lands while a file downloads fails that file and stops the
// batch before the next starts: the command fails, and its summary counts the
// file already there (f4, first in path order), the interrupted one and the
// one never started, and says it stopped.
func TestFoldersDownloadDirFailsWhenCancelled(t *testing.T) {
	lib := newFakeLibrary(t)
	lib.files["f4"], lib.files["f5"] = "sub2", "sub2"
	useFakeLibrary(t, lib)
	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(out, "sub2"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "sub2", "f4.dat"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	printed, err := runWithCancel(t, newFoldersCmd(), func(ctx context.Context, _ download.DownloadParams) error {
		cancelFunc()
		return ctx.Err()
	}, "download-dir", "sub2", "--outdir", out, "--merge", "--max-concurrent", "1")
	if !errors.Is(err, context.Canceled) || !strings.Contains(printed, "Files skipped:      1\n") ||
		!strings.Contains(printed, "Files failed:       1\n") || !strings.Contains(printed, "Files not started:  1\n") ||
		!strings.Contains(printed, "Stopped:            cancelled before every file was downloaded\n") {
		t.Errorf("folders download-dir returned %v after printing\n%s\nwant it to fail, every file counted and the stop stated", err, printed)
	}
}

// A job that completed while its results failed to download, or while the
// user cancelled that download, has not been watched to a good end.
func TestJobsWatchFailsWhenTheLastPassDoes(t *testing.T) {
	useFakeStatuses(t, []models.JobStatusEntry{{Status: "Completed"}})
	defer func(list func(context.Context, *api.Client, string) ([]models.JobFile, error)) { listJobFilesFn = list }(listJobFilesFn)
	listJobFilesFn = func(context.Context, *api.Client, string) ([]models.JobFile, error) {
		return []models.JobFile{{ID: "file123", Name: "results.dat", DecryptedSize: 4}}, nil
	}

	for pass, fn := range map[string]func(context.Context, download.DownloadParams) error{
		"failed": func(context.Context, download.DownloadParams) error {
			return errors.New("read: connection reset by peer")
		},
		"cancelled": func(ctx context.Context, _ download.DownloadParams) error { cancelFunc(); return ctx.Err() },
	} {
		if _, err := runWithCancel(t, newJobsWatchCmd(), fn, "--job-id", "job123", "--outdir", t.TempDir()); err == nil {
			t.Errorf("jobs watch exited 0 after its last download pass %s", pass)
		}
	}
}
