package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyThenRemove_SourceMissing(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "dst")
	if err := copyThenRemove(filepath.Join(t.TempDir(), "missing"), dst); err != nil {
		t.Fatalf("missing source should be no-op, got %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("dst should not be created when source is missing")
	}
}

func TestCopyThenRemove_HappyPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "subdir", "dst")
	if err := os.WriteFile(src, []byte("hello"), 0600); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := copyThenRemove(src, dst); err != nil {
		t.Fatalf("copyThenRemove: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("src should be removed")
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("dst content = %q, want %q", got, "hello")
	}
}

func TestCopyThenRemove_DoesNotOverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("new"), 0600); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0600); err != nil {
		t.Fatalf("write dst: %v", err)
	}

	if err := copyThenRemove(src, dst); err != nil {
		t.Fatalf("copyThenRemove: %v", err)
	}

	// dst must retain its existing content; src must remain untouched.
	got, _ := os.ReadFile(dst)
	if string(got) != "old" {
		t.Fatalf("dst overwritten: got %q, want %q", got, "old")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("src should remain when dst pre-exists: %v", err)
	}
}

func TestCopyThenRemove_Idempotent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("x"), 0600); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := copyThenRemove(src, dst); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Second call with no source is a no-op.
	if err := copyThenRemove(src, dst); err != nil {
		t.Fatalf("second call: %v", err)
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
