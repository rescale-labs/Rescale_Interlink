//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/service"
)

// An action that fails, as a cancelled UAC prompt does, records why and
// returns: redrawing takes the lock the failure was recorded under.
func TestFailedActionsReturn(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir()) // the startup log and PID file
	t.Setenv("APPDATA", t.TempDir())      // no daemon.conf: defaults
	origUninstall, origRedraw := elevateUninstall, redraw
	t.Cleanup(func() { elevateUninstall, redraw = origUninstall, origRedraw })
	elevateUninstall = func() error { return errors.New("FAKE the operation was canceled by the user") }
	redraw = func(a *trayApp) { a.mu.RLock(); a.mu.RUnlock() } // as updateUI does

	a := &trayApp{}
	for name, action := range map[string]func(){
		"remove service": a.uninstallServiceElevated,
		"start":          a.startService, // no rescale-int.exe beside the test binary
	} {
		done := make(chan struct{})
		go func() { action(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not return after failing", name)
		}
		if a.failure == "" {
			t.Errorf("%s failed without saying why", name)
		}
		a.failure = ""
	}
}

// Start's refusals of daemon.conf are shown where the user looks: the status
// line carries the whole reason and the tooltip its start, until failureShown
// has passed and the state shows again.
func TestStartRefusalsAreShown(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("LOCALAPPDATA", dir)
	origRedraw := redraw
	t.Cleanup(func() { redraw = origRedraw })
	redraw = func(*trayApp) {}
	conf, err := config.DefaultDaemonConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(conf), 0o700)
	state := service.Presentation{TrayStatusLine: "FAKE state", TrayTooltip: "FAKE state"}
	for section, why := range map[string]string{
		"download_folder = rel": `download_folder in daemon.conf must be an absolute path, got "rel"`,
		"max_concurrent = 0":    "max_concurrent in daemon.conf must be between 1 and 20, got 0",
	} {
		if err := os.WriteFile(conf, []byte("[daemon]\r\n"+section+"\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		a := &trayApp{}
		a.startService()
		tooltip, status := trayText(state, a.failure, a.failedAt, a.failedAt)
		if !strings.Contains(status, why) || !strings.Contains(tooltip, why) || len([]rune(tooltip)) > 127 {
			t.Errorf("%s: shown %q / %q, want both to carry %q, the tooltip in 127 characters", section, status, tooltip, why)
		}
		if _, status := trayText(state, a.failure, a.failedAt, a.failedAt.Add(failureShown)); status != "FAKE state" {
			t.Errorf("%s: after %s the status line still shows %q", section, failureShown, status)
		}
	}
}
