//go:build windows

package pathutil

import (
	"fmt"
	"testing"

	"github.com/rescale/rescale-int/internal/ipc"
)

// TestValidateWritablePath_MappedDriveRefused exercises the SYSTEM-strict
// branch with a fake WNet resolver that simulates a user-session-only
// mapping (WNet returns ERROR_NOT_CONNECTED).
func TestValidateWritablePath_MappedDriveRefused(t *testing.T) {
	orig := wnetResolver
	t.Cleanup(func() { wnetResolver = orig })

	wnetResolver = func(localPath string) (string, error) {
		return "", fmt.Errorf("simulated ERROR_NOT_CONNECTED")
	}

	result := ValidateWritablePath(`Z:\Rescale\Downloads`, ConsumerWindowsService)
	if result.Reachable {
		t.Fatalf("mapped-drive refused scenario should not be Reachable, got %+v", result)
	}
	if result.ErrorCode != ipc.CodeDownloadFolderInaccessible {
		t.Fatalf("expected CodeDownloadFolderInaccessible, got %q", result.ErrorCode)
	}
	if result.WasUNC {
		t.Fatalf("WasUNC should be false when WNet failed")
	}
}

// TestValidateWritablePath_MappedDriveResolved exercises the success branch:
// WNet returns a UNC path, probe succeeds.
func TestValidateWritablePath_MappedDriveResolved(t *testing.T) {
	orig := wnetResolver
	t.Cleanup(func() { wnetResolver = orig })

	tmp := t.TempDir()
	wnetResolver = func(localPath string) (string, error) {
		return tmp, nil
	}

	result := ValidateWritablePath(`Z:\Rescale\Downloads`, ConsumerWindowsService)
	if !result.Reachable {
		t.Fatalf("WNet success + writable probe should be Reachable, got %+v", result)
	}
	if !result.WasUNC {
		t.Fatalf("WasUNC should be true when a drive letter was resolved")
	}
}
