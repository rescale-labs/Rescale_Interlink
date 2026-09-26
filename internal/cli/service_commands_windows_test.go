//go:build windows

package cli

import (
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/service"
)

// On a machine without the service, 'service status' says it is not
// installed, not that it is stopped.
func TestServiceStatusSaysNotInstalled(t *testing.T) {
	if service.IsInstalled() {
		t.Skip("a Rescale Interlink service is installed on this machine")
	}
	out, err := runDaemonCommand(t, newServiceCmd(), "status")
	if err != nil || !strings.Contains(out, "Status:  Not installed") {
		t.Errorf("service status: %q, %v; want it to say the service is not installed", out, err)
	}
}
