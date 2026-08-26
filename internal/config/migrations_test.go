package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCopyThenRemove covers the migration primitive: it copies then deletes the
// source, but never clobbers a destination that already exists, and a missing
// source is a no-op so re-running startup migrations is safe.
func TestCopyThenRemove(t *testing.T) {
	tests := []struct {
		name string
		// srcContent empty means the source file is not created.
		srcContent  string
		dstContent  string // when set, dst pre-exists with this content
		nestedDst   bool   // dst lives in a directory that must be created
		wantDst     string // "" means dst must not exist
		wantSrcKept bool
		twice       bool // call copyThenRemove a second time
	}{
		{
			name: "missing source is a no-op",
		},
		{
			name:       "copies then removes the source",
			srcContent: "hello", nestedDst: true,
			wantDst: "hello",
		},
		{
			name:       "existing destination is not overwritten",
			srcContent: "new", dstContent: "old",
			wantDst: "old", wantSrcKept: true,
		},
		{
			// The second call has no source left, so it must not error.
			name:       "idempotent",
			srcContent: "x", twice: true,
			wantDst: "x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "src")
			dst := filepath.Join(dir, "dst")
			if tt.nestedDst {
				dst = filepath.Join(dir, "subdir", "dst")
			}

			if tt.srcContent != "" {
				if err := os.WriteFile(src, []byte(tt.srcContent), 0600); err != nil {
					t.Fatalf("write src: %v", err)
				}
			}
			if tt.dstContent != "" {
				if err := os.WriteFile(dst, []byte(tt.dstContent), 0600); err != nil {
					t.Fatalf("write dst: %v", err)
				}
			}

			if err := copyThenRemove(src, dst); err != nil {
				t.Fatalf("copyThenRemove: %v", err)
			}
			if tt.twice {
				if err := copyThenRemove(src, dst); err != nil {
					t.Fatalf("second call: %v", err)
				}
			}

			if tt.wantDst == "" {
				if _, err := os.Stat(dst); !os.IsNotExist(err) {
					t.Fatalf("dst should not be created")
				}
			} else {
				got, err := os.ReadFile(dst)
				if err != nil {
					t.Fatalf("read dst: %v", err)
				}
				if string(got) != tt.wantDst {
					t.Fatalf("dst content = %q, want %q", got, tt.wantDst)
				}
			}

			_, srcErr := os.Stat(src)
			if tt.wantSrcKept && srcErr != nil {
				t.Fatalf("src should remain when dst pre-exists: %v", srcErr)
			}
			if !tt.wantSrcKept && tt.srcContent != "" && !os.IsNotExist(srcErr) {
				t.Fatalf("src should be removed, stat err = %v", srcErr)
			}
		})
	}
}

func TestRunStartupMigrations_NoLogDir(t *testing.T) {
	// Environment without LOCALAPPDATA/HOME set should not panic.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("LOCALAPPDATA", filepath.Join(tmp, "Local"))
	t.Setenv("APPDATA", filepath.Join(tmp, "Roaming"))
	RunStartupMigrations(nil, ScopeCurrentUser, nil)
}

func TestMigrateStartupLogFilename(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("LOCALAPPDATA", filepath.Join(tmp, "Local"))

	logDir := LogDirectory()
	if err := os.MkdirAll(logDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	oldFile := filepath.Join(logDir, LegacyStartupLogName)
	newFile := filepath.Join(logDir, StartupLogName)
	if err := os.WriteFile(oldFile, []byte("bootstrap"), 0600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	migrateStartupLogFilename(nil)

	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatalf("legacy file should be removed after migration")
	}
	data, err := os.ReadFile(newFile)
	if err != nil || string(data) != "bootstrap" {
		t.Fatalf("migrated content mismatch: %q err=%v", data, err)
	}
}
