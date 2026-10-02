package cli

import (
	"context"
	"errors"
	"testing"
	"time"
)

// onePoll is a daemon whose single poll ends as the test says.
type onePoll struct {
	err     error  // what the poll returns
	scanErr string // the scan failure it leaves behind
}

func (p onePoll) RunOnce(context.Context) error      { return p.err }
func (p onePoll) LastScanError() (string, time.Time) { return p.scanErr, time.Time{} }

// A script or cron job has only the exit code of 'daemon run --once' to go on:
// the run fails when its scan did, with the scan's error, and only then. A scan
// that found nothing to download is a success.
func TestDaemonRunOnceFailsWhenItsScanDid(t *testing.T) {
	ctx := context.Background()
	if err := runSinglePoll(ctx, onePoll{}); err != nil {
		t.Errorf("a scan that worked: %v, want success", err)
	}
	const scanErr = "failed to list jobs: FAKE refusal"
	if err := runSinglePoll(ctx, onePoll{scanErr: scanErr}); err == nil || err.Error() != scanErr {
		t.Errorf("a scan that failed: %v, want its error %q", err, scanErr)
	}
	saveErr := errors.New("failed to save state: FAKE")
	if err := runSinglePoll(ctx, onePoll{err: saveErr, scanErr: scanErr}); !errors.Is(err, saveErr) {
		t.Errorf("a poll that failed: %v, want %v", err, saveErr)
	}
}
