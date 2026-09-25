//go:build windows

package wailsapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
)

// Start refuses a max_concurrent 'daemon run' refuses before it writes a
// startup log or spawns anything, and a child that stops says why.
func TestStartDaemonSaysWhyItCannotStart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("LOCALAPPDATA", dir)
	conf, err := config.DefaultDaemonConfigPath()
	if err == nil {
		os.MkdirAll(filepath.Dir(conf), 0o700)
		err = os.WriteFile(conf, []byte("[daemon]\nmax_concurrent = 50\n"), 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
	const want = "max_concurrent in daemon.conf must be between 1 and 20, got 50"
	if err := (&App{}).startDaemonSubprocess(); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("startDaemonSubprocess: %v, want an error containing %q", err, want)
	}
	if _, err := os.Stat(config.LogDirectory()); !os.IsNotExist(err) {
		t.Errorf("the refused start wrote diagnostics in %s", config.LogDirectory())
	}

	for stderr, want := range map[string]string{
		"Error: " + want + "\r\nUsage:\r\n  rescale-int daemon run [flags]\r\n\r\nFlags:\r\n  -v, --verbose   verbose\r\n": "Error: " + want,
		"one\ntwo\n\nthree\nfour\n": "two | three | four",
	} {
		if got := childStderr(stderr); got != want {
			t.Errorf("childStderr(%q) = %q, want %q", stderr, got, want)
		}
	}
}
