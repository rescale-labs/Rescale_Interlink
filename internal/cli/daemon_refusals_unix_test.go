//go:build !windows

package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/reporting"
)

// 'daemon retry' with neither --all nor --job-id, and 'daemon config edit'
// with no editor to run, are refusals the user can put right: no error report.
func TestDaemonRetryAndEditRefusalsSaveNoReport(t *testing.T) {
	isolateDaemonHome(t)
	t.Setenv("EDITOR", "")
	t.Setenv("PATH", t.TempDir()) // no vim, vi or nano
	for _, tc := range []struct {
		name string
		cmd  *cobra.Command
		want string
	}{
		{"retry", newDaemonRetryCmd(), "either --all or --job-id must be specified"},
		{"config edit", newDaemonConfigEditCmd(), "no editor found"},
	} {
		_, err := runDaemonCommand(t, tc.cmd)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("daemon %s: %v, want an error containing %q", tc.name, err, tc.want)
		}
		if saved := reporting.HandleCLIError(err, "cli", "rescale-int daemon "+tc.name, ""); saved != "" {
			t.Errorf("daemon %s: the refusal saved an error report to %s", tc.name, saved)
		}
	}
}
