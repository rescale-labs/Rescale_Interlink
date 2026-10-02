package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/cloud/download"
)

// Without --continue-on-error the first file that fails stops a folder
// download. The other files, cut off while downloading or never started, are
// not downloaded because of that stop, not failures of their own, and the
// summary says the download stopped, as a cancel's does.
func TestFoldersDownloadDirSaysAFailureStoppedIt(t *testing.T) {
	lib := newFakeLibrary(t)
	lib.files["f4"], lib.files["f5"] = "sub2", "sub2" // with sub3's f1, three files; f4 comes first
	useFakeLibrary(t, lib)
	failed := errors.New("refused")

	f5Downloading := make(chan struct{})
	cutOff := func(ctx context.Context, p download.DownloadParams) error {
		switch p.FileID {
		case "f4": // fails once f5 is downloading, so that the stop cuts f5 off
			select {
			case <-f5Downloading:
				return failed
			case <-time.After(time.Minute): // a watchdog fails the test, never the file
				t.Error("f5 never started downloading")
				cancelFunc()
				return context.Canceled
			}
		case "f5":
			close(f5Downloading)
		}
		<-ctx.Done()
		return fmt.Errorf("failed to refresh credentials: %w", ctx.Err())
	}
	notStarted := func(_ context.Context, p download.DownloadParams) error {
		if p.FileID == "f4" {
			return failed
		}
		return nil
	}

	for workers, fn := range map[string]func(context.Context, download.DownloadParams) error{"3": cutOff, "1": notStarted} {
		printed, err := runWithCancel(t, newFoldersCmd(), fn, "download-dir", "sub2", "--outdir", t.TempDir(), "--max-concurrent", workers)
		for _, want := range []string{"Files failed:       1\n", "Not downloaded:     2\n", "Stopped:            cancelled before every file was downloaded\n"} {
			if err == nil || !strings.Contains(printed, want) || strings.Contains(printed, "credentials") {
				t.Errorf("folders download-dir with %s worker(s) returned %v after printing\n%s\nwant it to fail, %q and no failure for the files it stopped",
					workers, err, printed, want)
			}
		}
		if workers == "3" && !strings.Contains(printed, "← f5.dat: not downloaded: the download stopped") {
			t.Errorf("a file the stop cut off is not named as such in:\n%s", printed)
		}
	}
}
