//go:build !windows

package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// On macOS and Linux Start runs 'daemon run --background', which exits once it
// has started the daemon: waiting for it leaves no zombie, and a parent that
// exits with an error is reported. A script stands in for rescale-int.
func TestLaunchDetachedWaitsForTheBackgroundParent(t *testing.T) {
	dir := t.TempDir()
	cli, got := filepath.Join(dir, "cli"), filepath.Join(dir, "args")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nsleep 0.2\necho \"$@\" > '"+got+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := launchDetached(cli, "daemon", "run"); err != nil {
		t.Fatalf("launchDetached: %v", err)
	}
	if b, err := os.ReadFile(got); err != nil || strings.TrimSpace(string(b)) != "daemon run --background" {
		t.Errorf("ran %q, %v; want 'daemon run --background', waited for", b, err)
	}
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := launchDetached(cli); !errors.Is(err, ErrLaunch) {
		t.Errorf("a parent that exits 3: %v, want ErrLaunch", err)
	}
}
