package wailsapp

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
)

// newTestApp builds a minimal App with an in-memory config and redirects
// persistence to a temp directory.
func newTestApp(t *testing.T, cfg *config.Config) (*App, string, string) {
	t.Helper()
	testLogger(t)
	setIsolatedUserConfigEnv(t)

	configPath := config.GetDefaultConfigPath()
	tokenPath := config.GetDefaultTokenPath()
	_ = os.MkdirAll(filepath.Dir(configPath), 0700)
	_ = os.MkdirAll(filepath.Dir(tokenPath), 0700)

	return &App{config: cfg}, configPath, tokenPath
}

func TestEnsureAllConfigPersisted_WritesTokenAndCSV(t *testing.T) {
	cfg := &config.Config{
		APIKey:     "test-api-key",
		APIBaseURL: "https://platform.rescale.com",
	}
	a, configPath, tokenPath := newTestApp(t, cfg)

	if err := a.ensureAllConfigPersisted(); err != nil {
		t.Fatalf("first call failed: %v", err)
	}

	token, err := config.ReadTokenFile(tokenPath)
	if err != nil || token != "test-api-key" {
		t.Fatalf("token file wrong: got %q err=%v", token, err)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config.csv not written: %v", err)
	}
}

func TestEnsureAllConfigPersisted_Idempotent(t *testing.T) {
	cfg := &config.Config{APIKey: "same-key", APIBaseURL: "https://platform.rescale.com"}
	a, _, tokenPath := newTestApp(t, cfg)

	if err := a.ensureAllConfigPersisted(); err != nil {
		t.Fatalf("first: %v", err)
	}
	info1, _ := os.Stat(tokenPath)

	if err := a.ensureAllConfigPersisted(); err != nil {
		t.Fatalf("second: %v", err)
	}
	info2, _ := os.Stat(tokenPath)
	if info1.ModTime() != info2.ModTime() {
		t.Errorf("second call rewrote token file unchanged; expected no-op")
	}
}

func TestEnsureAllConfigPersisted_ClearedKeyRemovesToken(t *testing.T) {
	cfg := &config.Config{APIKey: "initial", APIBaseURL: "https://platform.rescale.com"}
	a, _, tokenPath := newTestApp(t, cfg)

	if err := a.ensureAllConfigPersisted(); err != nil {
		t.Fatalf("initial persist: %v", err)
	}
	if _, err := os.Stat(tokenPath); err != nil {
		t.Fatalf("token should exist: %v", err)
	}

	// User clears the key in memory.
	cfg.APIKey = ""

	if err := a.ensureAllConfigPersisted(); err != nil {
		t.Fatalf("cleared-key persist: %v", err)
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("token file should be removed when key cleared, err=%v", err)
	}
}

func TestEnsureAllConfigPersisted_ProxyPasswordNotPersisted(t *testing.T) {
	cfg := &config.Config{
		APIKey:        "some-key",
		APIBaseURL:    "https://platform.rescale.com",
		ProxyPassword: "SECRET-PROXY-PASSWORD",
	}
	a, configPath, _ := newTestApp(t, cfg)

	if err := a.ensureAllConfigPersisted(); err != nil {
		t.Fatalf("persist: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	if bytes.Contains(data, []byte("SECRET-PROXY-PASSWORD")) {
		t.Fatalf("config.csv leaked proxy password: %s", data)
	}
}

// The File Browser's split option saves on its own. config.csv takes that one
// value; what the App holds unsaved stays out of it, and the token file stays
// even though the App's API key field has been cleared.
func TestSetFlattenJobDownloadSavesOnlyThatOption(t *testing.T) {
	a, configPath, tokenPath := newTestApp(t, &config.Config{ProxyHost: "saved.example.invalid"})
	if err := config.SaveConfigCSV(a.config, configPath); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteTokenFile(tokenPath, "SAVED-KEY"); err != nil {
		t.Fatal(err)
	}
	a.config = &config.Config{ProxyHost: "unsaved.example.invalid"}

	if err := a.SetFlattenJobDownload(true); err != nil {
		t.Fatalf("SetFlattenJobDownload: %v", err)
	}
	saved, err := config.LoadConfigCSV(configPath)
	if err != nil || !saved.FlattenJobDownload || saved.ProxyHost != "saved.example.invalid" {
		t.Errorf("config.csv has flatten_job_download=%v and proxy host %q (err %v), want true and the saved host", saved.FlattenJobDownload, saved.ProxyHost, err)
	}
	if token, err := config.ReadTokenFile(tokenPath); err != nil || token != "SAVED-KEY" {
		t.Errorf("token file holds %q (err %v), want it untouched", token, err)
	}
	if !a.config.FlattenJobDownload || a.config.ProxyHost != "unsaved.example.invalid" {
		t.Errorf("the App holds flatten=%v and proxy host %q, want true and the unsaved host", a.config.FlattenJobDownload, a.config.ProxyHost)
	}
}
