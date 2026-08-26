package pathutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/rescale/rescale-int/internal/ipc"
)

func TestValidateWritablePath(t *testing.T) {
	tests := []struct {
		name string
		// skip reports why the case cannot run here, or "" to run it.
		skip func() string
		// path returns the path to validate, doing any setup it needs.
		path          func(t *testing.T) string
		wantReachable bool
		wantErrorCode ipc.ErrorCode
		// extra runs after the probe, for case-specific checks.
		extra func(t *testing.T, path string)
	}{
		{
			name:          "empty path is reachable",
			path:          func(t *testing.T) string { return "" },
			wantReachable: true,
		},
		{
			name:          "existing dir",
			path:          func(t *testing.T) string { return t.TempDir() },
			wantReachable: true,
			extra: func(t *testing.T, path string) {
				// The write probe must leave nothing behind.
				if entries, _ := os.ReadDir(path); len(entries) != 0 {
					t.Fatalf("probe did not clean up marker file: %v", entries)
				}
			},
		},
		{
			name: "nonexistent dir is created",
			path: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "new", "nested")
			},
			wantReachable: true,
			extra: func(t *testing.T, path string) {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("expected dir to be created: %v", err)
				}
			},
		},
		{
			name: "path is a file",
			path: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "not-a-dir")
				if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
					t.Fatalf("setup: %v", err)
				}
				return p
			},
			wantErrorCode: ipc.CodeDownloadFolderInaccessible,
		},
		{
			name: "read-only parent",
			skip: func() string {
				if runtime.GOOS == "windows" {
					return "chmod-based read-only test is POSIX-only"
				}
				if os.Getuid() == 0 {
					return "running as root bypasses permission checks"
				}
				return ""
			},
			path: func(t *testing.T) string {
				parent := t.TempDir()
				if err := os.Chmod(parent, 0500); err != nil {
					t.Fatalf("chmod: %v", err)
				}
				t.Cleanup(func() { _ = os.Chmod(parent, 0700) })
				return filepath.Join(parent, "child")
			},
			wantErrorCode: ipc.CodeDownloadFolderInaccessible,
		},
		{
			name: "symlink to a dir",
			skip: func() string {
				if runtime.GOOS == "windows" {
					return "os.Symlink requires developer mode on Windows"
				}
				return ""
			},
			path: func(t *testing.T) string {
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(t.TempDir(), link); err != nil {
					t.Fatalf("symlink: %v", err)
				}
				return link
			},
			wantReachable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skip != nil {
				if reason := tt.skip(); reason != "" {
					t.Skip(reason)
				}
			}

			path := tt.path(t)
			result := ValidateWritablePath(path, ConsumerCurrentUser)

			if result.Reachable != tt.wantReachable {
				t.Fatalf("Reachable = %v, want %v: %+v", result.Reachable, tt.wantReachable, result)
			}
			if result.ErrorCode != tt.wantErrorCode {
				t.Fatalf("ErrorCode = %q, want %q", result.ErrorCode, tt.wantErrorCode)
			}
			if tt.extra != nil {
				tt.extra(t, path)
			}
		})
	}
}
