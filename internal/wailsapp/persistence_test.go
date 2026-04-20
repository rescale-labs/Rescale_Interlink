package wailsapp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/logging"
)

// newTestApp builds a minimal App with an in-memory config and redirects
// persistence to a temp directory.
func newTestApp(t *testing.T, cfg *config.Config) (*App, string, string) {
	t.Helper()
	ensureTestLogger()
	home := t.TempDir()
	t.Setenv("HOME", home)               // Unix home — drives config/token paths
	t.Setenv("USERPROFILE", home)        // Windows
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))

	configPath := config.GetDefaultConfigPath()
	tokenPath := config.GetDefaultTokenPath()
	_ = os.MkdirAll(filepath.Dir(configPath), 0700)
	_ = os.MkdirAll(filepath.Dir(tokenPath), 0700)

	return &App{config: cfg}, configPath, tokenPath
}

func ensureTestLogger() {
	if wailsLogger == nil {
		wailsLogger = logging.NewLogger("wails", nil)
	}
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
	if contains(data, []byte("SECRET-PROXY-PASSWORD")) {
		t.Fatalf("config.csv leaked proxy password: %s", data)
	}
}

func contains(haystack, needle []byte) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
