package pathutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/rescale/rescale-int/internal/ipc"
)

func TestValidateWritablePath_Empty(t *testing.T) {
	result := ValidateWritablePath("", ConsumerCurrentUser)
	if !result.Reachable {
		t.Fatalf("empty path should be Reachable=true, got %+v", result)
	}
}

func TestValidateWritablePath_ExistingDir(t *testing.T) {
	dir := t.TempDir()
	result := ValidateWritablePath(dir, ConsumerCurrentUser)
	if !result.Reachable {
		t.Fatalf("existing dir should be Reachable, got %+v", result)
	}
	if result.ErrorCode != "" {
		t.Fatalf("existing dir should have empty ErrorCode, got %q", result.ErrorCode)
	}
	// Marker must be cleaned up.
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("probe did not clean up marker file: %v", entries)
	}
}

func TestValidateWritablePath_NonexistentCreated(t *testing.T) {
	parent := t.TempDir()
	sub := filepath.Join(parent, "new", "nested")

	result := ValidateWritablePath(sub, ConsumerCurrentUser)
	if !result.Reachable {
		t.Fatalf("nonexistent subdir should be created and Reachable, got %+v", result)
	}
	if _, err := os.Stat(sub); err != nil {
		t.Fatalf("expected dir to be created: %v", err)
	}
}

func TestValidateWritablePath_PathIsFile(t *testing.T) {
	parent := t.TempDir()
	filePath := filepath.Join(parent, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	result := ValidateWritablePath(filePath, ConsumerCurrentUser)
	if result.Reachable {
		t.Fatalf("file path should not be Reachable, got %+v", result)
	}
	if result.ErrorCode != ipc.CodeDownloadFolderInaccessible {
		t.Fatalf("expected CodeDownloadFolderInaccessible, got %q", result.ErrorCode)
	}
}

func TestValidateWritablePath_ReadOnlyParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based read-only test is POSIX-only")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root bypasses permission checks")
	}

	parent := t.TempDir()
	if err := os.Chmod(parent, 0500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0700) })

	sub := filepath.Join(parent, "child")
	result := ValidateWritablePath(sub, ConsumerCurrentUser)
	if result.Reachable {
		t.Fatalf("read-only parent should refuse, got %+v", result)
	}
	if result.ErrorCode != ipc.CodeDownloadFolderInaccessible {
		t.Fatalf("expected CodeDownloadFolderInaccessible, got %q", result.ErrorCode)
	}
}

func TestValidateWritablePath_SymlinkToDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink requires developer mode on Windows")
	}
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	result := ValidateWritablePath(link, ConsumerCurrentUser)
	if !result.Reachable {
		t.Fatalf("symlink to dir should be Reachable, got %+v", result)
	}
}
