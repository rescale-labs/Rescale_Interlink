package cli

import (
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/reporting"
)

// Scripts and installers of earlier versions still call the service commands
// those versions had. Each, and any other word, fails the same way on every
// OS, as a usage error saying where auto-download went; the group alone, or
// with --help, prints its help with a usage line naming what remains.
func TestServiceRefusesTheRemovedCommands(t *testing.T) {
	home := t.TempDir()
	for _, env := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "LOCALAPPDATA"} {
		t.Setenv(env, home)
	}
	run := func(args ...string) (string, error) {
		root := &cobra.Command{Use: "rescale-int"} // a group's arguments are read as they are under the real root
		root.AddCommand(newServiceCmd())
		return runDaemonCommand(t, root, append([]string{"service"}, args...)...)
	}
	for _, word := range []string{"install", "status", "start", "stop", "install-and-start", "remove"} {
		_, err := run(word)
		if err == nil || !reporting.IsUsageError(err) {
			t.Errorf("service %s: %v, want a usage error", word, err)
			continue
		}
		for _, want := range []string{"service mode was removed", "'rescale-int daemon run'", "only 'rescale-int service uninstall' remains"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("service %s: %q, want it to say %q", word, err, want)
			}
		}
		if saved := reporting.HandleCLIError(err, "cli", "rescale-int service", ""); saved != "" {
			t.Errorf("service %s saved an error report to %s", word, saved)
		}
	}
	for _, args := range [][]string{nil, {"--help"}} {
		if out, err := run(args...); err != nil || !strings.Contains(out, "Usage:\n  rescale-int service uninstall") {
			t.Errorf("service %v: %v, want its help with the usage line\n%s", args, err, out)
		}
	}
}

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
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home) // where a report would go on Linux
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
