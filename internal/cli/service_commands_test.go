package cli

import (
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/reporting"
	"github.com/rescale/rescale-int/internal/service"
)

// The service can no longer be installed or started, on any system: the
// commands say why before any system-specific step, and a refusal is a usage
// error, which files no error report.
func TestServiceInstallAndStartRefuse(t *testing.T) {
	for _, use := range []string{"install", "install-and-start", "start"} {
		t.Run(use, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			_, err := runDaemonCommand(t, newServiceCmd(), use)
			if err == nil || err.Error() != service.ModeUnavailable {
				t.Fatalf("service %s: %v, want %q", use, err, service.ModeUnavailable)
			}
			if saved := reporting.HandleCLIError(err, "cli", "rescale-int service "+use, ""); saved != "" {
				t.Errorf("the refusal saved an error report to %s", saved)
			}
		})
	}
}

// Elsewhere than Windows the remaining service commands are usage errors too.
func TestServiceCommandsOutsideWindowsAreUsageErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the commands reach the Service Control Manager on Windows")
	}
	for _, use := range []string{"uninstall", "stop", "status"} {
		t.Run(use, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			_, err := runDaemonCommand(t, newServiceCmd(), use)
			if err == nil || !strings.Contains(err.Error(), "only supported on Windows") {
				t.Fatalf("service %s: %v, want it refused as Windows-only", use, err)
			}
			if saved := reporting.HandleCLIError(err, "cli", "rescale-int service "+use, ""); saved != "" {
				t.Errorf("the refusal saved an error report to %s", saved)
			}
		})
	}
}

// The recovery a hint recommends is a command that exists.
func TestHintsNameRealCommands(t *testing.T) {
	root := NewRootCmd()
	AddCommands(root)
	hint := ipc.HintFor(ipc.CodeIPCNotResponding)
	m := regexp.MustCompile(`'rescale-int ([^']+)'`).FindStringSubmatch(hint)
	if m == nil {
		t.Fatalf("hint %q names no rescale-int command", hint)
	}
	if cmd, rest, err := root.Find(strings.Fields(m[1])); err != nil || len(rest) != 0 || cmd.Hidden || cmd.CommandPath() != "rescale-int "+m[1] {
		t.Errorf("hint names 'rescale-int %s', which is not a command (%v)", m[1], err)
	}
}
