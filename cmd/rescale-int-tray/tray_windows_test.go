//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/service"
)

// An action that fails records why and returns: redrawing takes the lock the
// failure was recorded under.
func TestFailedActionsReturn(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir()) // the startup log and PID file
	t.Setenv("APPDATA", t.TempDir())      // no daemon.conf: defaults
	origRedraw := redraw
	t.Cleanup(func() { redraw = origRedraw })
	redraw = func(a *trayApp) { a.mu.RLock(); a.mu.RUnlock() } // as updateUI does

	a := &trayApp{}
	for name, action := range map[string]func(){
		"start": a.startService, // no API key, and no rescale-int.exe beside the test binary
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

// At tray launch auto-download starts only when daemon.conf enables it, and a
// start that daemon.conf refuses, that cannot read it, or that has no API key,
// as the app's Start refuses, is shown as Start's refusals are.
func TestStartupStartsOnlyWhenEnabledAndShowsWhy(t *testing.T) {
	dir := t.TempDir()
	for _, env := range []string{"APPDATA", "LOCALAPPDATA", "USERPROFILE", "HOME"} {
		t.Setenv(env, dir) // no token file anywhere
	}
	t.Setenv("RESCALE_API_KEY", "")
	origRedraw := redraw
	t.Cleanup(func() { redraw = origRedraw })
	redraw = func(*trayApp) {}
	conf, err := config.DefaultDaemonConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(conf), 0o700)
	for _, tc := range []struct{ name, conf, why string }{
		{"not enabled", "[daemon]\r\nenabled = false\r\ndownload_folder = rel\r\n", ""},
		{"enabled, refused", "[daemon]\r\nenabled = true\r\ndownload_folder = rel\r\n", `download_folder in daemon.conf must be an absolute path, got "rel"`},
		{"unreadable", "[daemon\r\nenabled = true\r\n", "Configuration error"},
		{"enabled, no API key", "[daemon]\r\nenabled = true\r\ndownload_folder = " + filepath.ToSlash(dir) + "\r\n", ipc.CanonicalText[ipc.CodeNoAPIKey]},
	} {
		if err := os.WriteFile(conf, []byte(tc.conf), 0o600); err != nil {
			t.Fatal(err)
		}
		a := &trayApp{}
		a.startupTasks()
		if (tc.why == "") != (a.failure == "") || !strings.Contains(a.failure, tc.why) {
			t.Errorf("%s: startup showed %q, want %q", tc.name, a.failure, tc.why)
		}
	}
}
