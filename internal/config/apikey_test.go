package config

import (
	"os"
	"path/filepath"
	"runtime"
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

func TestResolveAPIKey_ServiceMode_BlocksDefaultSources(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a per-user token file
	userProfilePath := filepath.Join(tmpDir, "user")
	userTokenPath := GetUserTokenPath(userProfilePath)
	createTokenFile(t, userTokenPath, "user-key-123")

	// Create a default token file at a known location — we can't override
	// GetDefaultTokenPath, but we can verify service mode doesn't reach it
	// by also setting the env var (step 5) and confirming it's not used.
	t.Setenv("RESCALE_API_KEY", "env-key-should-not-be-used")

	// Service mode: should return the per-user key, NOT the env var
	key := ResolveAPIKey("", userProfilePath, true)
	if key != "user-key-123" {
		t.Errorf("service mode: expected user-key-123, got %q", key)
	}
}

func TestResolveAPIKey_ServiceMode_NoCredentials(t *testing.T) {
	tmpDir := t.TempDir()

	// No per-user token file, no apiconfig
	userProfilePath := filepath.Join(tmpDir, "nonexistent-user")

	// Set env var that would be found in non-service mode
	t.Setenv("RESCALE_API_KEY", "env-key-should-not-be-used")

	// Service mode with no per-user sources: should return empty
	key := ResolveAPIKey("", userProfilePath, true)
	if key != "" {
		t.Errorf("service mode with no credentials: expected empty, got %q", key)
	}
}

func TestResolveAPIKey_NonServiceMode_FullChain(t *testing.T) {
	// Non-service mode should reach steps 4-5 (unlike service mode).
	// The real default token file may exist on this machine, so we just
	// verify that a non-empty key is returned from *some* source.
	tmpDir := t.TempDir()
	userProfilePath := filepath.Join(tmpDir, "nonexistent-user")

	t.Setenv("RESCALE_API_KEY", "env-key-456")

	key := ResolveAPIKey("", userProfilePath, false)
	if key == "" {
		t.Error("non-service mode: expected non-empty key (from token-file or env), got empty")
	}

	// Verify service mode would return empty for the same inputs
	keyService := ResolveAPIKey("", userProfilePath, true)
	if keyService != "" {
		t.Errorf("service mode should return empty for same inputs, got %q", keyService)
	}
}

func TestResolveAPIKey_ExplicitKeyTakesPriority(t *testing.T) {
	// Explicit key always wins, regardless of service mode
	key := ResolveAPIKey("explicit-key", "/some/path", true)
	if key != "explicit-key" {
		t.Errorf("expected explicit-key, got %q", key)
	}

	key = ResolveAPIKey("explicit-key", "/some/path", false)
	if key != "explicit-key" {
		t.Errorf("expected explicit-key, got %q", key)
	}
}

func TestResolveAPIKeySource_ServiceMode_SourceLabels(t *testing.T) {
	tmpDir := t.TempDir()
	userProfilePath := filepath.Join(tmpDir, "user")
	userTokenPath := GetUserTokenPath(userProfilePath)
	createTokenFile(t, userTokenPath, "user-key-src")

	t.Setenv("RESCALE_API_KEY", "env-key-src")

	// Service mode: should find user-token-file, never token-file or environment
	key, source := ResolveAPIKeySource("", userProfilePath, true)
	if key != "user-key-src" {
		t.Errorf("expected user-key-src, got %q", key)
	}
	if source != "user-token-file" {
		t.Errorf("expected source 'user-token-file', got %q", source)
	}
}

func TestResolveAPIKeySource_ServiceMode_NoCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	userProfilePath := filepath.Join(tmpDir, "no-creds")

	t.Setenv("RESCALE_API_KEY", "env-should-be-blocked")

	key, source := ResolveAPIKeySource("", userProfilePath, true)
	if key != "" {
		t.Errorf("expected empty key, got %q", key)
	}
	if source != "" {
		t.Errorf("expected empty source, got %q", source)
	}
}

func TestResolveAPIKeySource_NonServiceMode_ReachesLaterSteps(t *testing.T) {
	// Non-service mode can reach token-file (step 4) or environment (step 5).
	// The real default token may exist, so we verify the source is one of the
	// later steps — anything beyond "apiconfig".
	tmpDir := t.TempDir()
	userProfilePath := filepath.Join(tmpDir, "no-token")

	t.Setenv("RESCALE_API_KEY", "env-key-789")

	key, source := ResolveAPIKeySource("", userProfilePath, false)
	if key == "" {
		t.Error("non-service mode: expected non-empty key, got empty")
	}
	if source != "token-file" && source != "environment" {
		t.Errorf("expected source 'token-file' or 'environment', got %q", source)
	}

	// Same inputs in service mode must return empty
	keyService, srcService := ResolveAPIKeySource("", userProfilePath, true)
	if keyService != "" || srcService != "" {
		t.Errorf("service mode should return empty, got (%q, %q)", keyService, srcService)
	}
}

func TestResolveAPIKeySource_FlagSource(t *testing.T) {
	key, source := ResolveAPIKeySource("flag-key", "", true)
	if key != "flag-key" || source != "flag" {
		t.Errorf("expected (flag-key, flag), got (%q, %q)", key, source)
	}
}

func TestGetUserTokenPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		path := GetUserTokenPath(`C:\Users\testuser`)
		expected := `C:\Users\testuser\AppData\Roaming\Rescale\Interlink\token`
		if path != expected {
			t.Errorf("Windows path: expected %q, got %q", expected, path)
		}
	} else {
		path := GetUserTokenPath("/home/testuser")
		expected := "/home/testuser/.config/rescale/token"
		if path != expected {
			t.Errorf("Unix path: expected %q, got %q", expected, path)
		}
	}

	if path := GetUserTokenPath(""); path != "" {
		t.Errorf("empty profile: expected empty, got %q", path)
	}
}
