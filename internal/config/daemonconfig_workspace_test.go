package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The workspace folder settings are off unless daemon.conf turns them on, and
// what is saved is read back.
func TestDaemonConfigWorkspaceFolderSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.conf")
	if err := os.WriteFile(path, []byte("[daemon]\nenabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadDaemonConfig(path)
	if err != nil || cfg.Daemon.IncludeWorkspaceFolders || cfg.Daemon.FlattenFolderStructure {
		t.Fatalf("without the keys: %+v, %v; want both off", cfg.Daemon, err)
	}

	cfg.Daemon.IncludeWorkspaceFolders, cfg.Daemon.FlattenFolderStructure = true, true
	if err := SaveDaemonConfig(cfg, path); err != nil {
		t.Fatal(err)
	}
	if cfg, err = LoadDaemonConfig(path); err != nil || !cfg.Daemon.IncludeWorkspaceFolders || !cfg.Daemon.FlattenFolderStructure {
		t.Errorf("after saving both on: %+v, %v", cfg.Daemon, err)
	}
}
