package config

import (
	"os"
	"path/filepath"
	"testing"
)

// createTokenFile creates a token file with the given key and permissions.
func createTokenFile(t *testing.T, path, key string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
	if err := os.WriteFile(path, []byte(key+"\n"), 0600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// TestResolveAPIKey pins the resolution chain: an explicit key, then the token
// file, then an older version's apiconfig, then the environment.
func TestResolveAPIKey(t *testing.T) {
	dir := t.TempDir()
	for _, env := range []string{"HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA"} {
		t.Setenv(env, dir)
	}
	t.Setenv("RESCALE_API_KEY", "env-key-456")
	if key := ResolveAPIKey("explicit-key"); key != "explicit-key" {
		t.Errorf("explicit key: got %q", key)
	}
	if key := ResolveAPIKey(""); key != "env-key-456" {
		t.Errorf("only the environment: got %q, want env-key-456", key)
	}
	apiconfig, err := DefaultAPIConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	createTokenFile(t, apiconfig, "[rescale]\napi_key = legacy-key-789")
	if key := ResolveAPIKey(""); key != "legacy-key-789" {
		t.Errorf("apiconfig and the environment: got %q, want legacy-key-789", key)
	}
	createTokenFile(t, GetDefaultTokenPath(), "token-key-123")
	if key := ResolveAPIKey(""); key != "token-key-123" {
		t.Errorf("every source: got %q, want token-key-123", key)
	}
}
