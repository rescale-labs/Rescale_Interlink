package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/daemon"
)

// runDaemonCommand runs a 'daemon' subcommand with only its own arguments and
// returns what it printed and its error.
func runDaemonCommand(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	return out, err
}

// 'daemon list --failed' says when the daemon will try each failed job again,
// or that it has stopped trying and how to release the job; 'daemon retry'
// then releases it, saying so, and names an ID it was given that was not a
// failed download.
func TestDaemonListFailedSaysWhenEachJobIsRetried(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	stateFile := filepath.Join(t.TempDir(), "state.json")

	now := time.Now()
	waiting := now.Add(-2 * time.Minute)
	s := daemon.NewState(stateFile)
	for _, j := range []*daemon.DownloadedJob{
		{JobID: "waiting", JobName: "Waiting", Error: "boom", RetryCount: 2, LastAttempt: waiting},
		{JobID: "due", JobName: "Due", Error: "boom", RetryCount: 1, LastAttempt: now.Add(-time.Hour)},
		{JobID: "stopped", JobName: "Stopped", Error: "boom", RetryCount: 5, LastAttempt: now.Add(-time.Hour)},
	} {
		j.DownloadedAt = j.LastAttempt
		s.Downloaded[j.JobID] = j
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out, err := runDaemonCommand(t, newDaemonListCmd(), "--failed", "--state-file", stateFile)
	if err != nil {
		t.Fatalf("daemon list --failed: %v", err)
	}
	for _, want := range []string{
		"Next attempt: after " + waiting.Add(10*time.Minute).Format(time.RFC3339) + " (2 of 5 attempts failed)",
		"Next attempt: at the next poll (1 of 5 attempts failed)",
		"Next attempt: none, 5 attempts failed; run 'rescale-int daemon retry --job-id stopped' to try again",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	out, err = runDaemonCommand(t, newDaemonRetryCmd(), "--job-id", "stopped", "--job-id", "nosuch", "--state-file", stateFile)
	if err != nil {
		t.Fatalf("daemon retry: %v", err)
	}
	for _, want := range []string{"Marked for retry: Stopped (stopped)\n", "Not a failed download: nosuch\n", "1 job(s) marked for retry."} {
		if !strings.Contains(out, want) {
			t.Errorf("daemon retry output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Marked for retry: nosuch") {
		t.Errorf("daemon retry says it marked a job that had not failed:\n%s", out)
	}
	out, err = runDaemonCommand(t, newDaemonListCmd(), "--failed", "--state-file", stateFile)
	if err != nil {
		t.Fatalf("daemon list --failed: %v", err)
	}
	if strings.Contains(out, "Stopped (stopped)") {
		t.Errorf("the job 'daemon retry' released is still listed as failed:\n%s", out)
	}
}
