package cli

import (
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/reporting"
)

// The service group holds only the hidden command that removes a service an
// earlier version installed; elsewhere than Windows it is a usage error, which
// files no error report.
func TestServiceUninstallIsHiddenAndWindowsOnly(t *testing.T) {
	group := newServiceCmd()
	if cmds := group.Commands(); !group.Hidden || len(cmds) != 1 || cmds[0].Name() != "uninstall" || !cmds[0].Hidden {
		t.Errorf("service group hidden %v with commands %v, want it hidden with only a hidden uninstall", group.Hidden, cmds)
	}
	if runtime.GOOS == "windows" {
		t.Skip("the command reaches the Service Control Manager on Windows")
	}
	t.Setenv("HOME", t.TempDir())
	_, err := runDaemonCommand(t, newServiceCmd(), "uninstall")
	if err == nil || !strings.Contains(err.Error(), "only supported on Windows") {
		t.Fatalf("service uninstall: %v, want it refused as Windows-only", err)
	}
	if saved := reporting.HandleCLIError(err, "cli", "rescale-int service uninstall", ""); saved != "" {
		t.Errorf("the refusal saved an error report to %s", saved)
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
