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

// TestResolveAPIKey pins the resolution chain: an explicit key, then the
// profile's own token file, before the default token file and the environment.
func TestResolveAPIKey(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "profile")
	t.Setenv("RESCALE_API_KEY", "env-key-456")
	if key := ResolveAPIKey("explicit-key", profile); key != "explicit-key" {
		t.Errorf("explicit key: got %q", key)
	}
	// The real default token file may exist on the machine running this, so
	// only that some later source answered is asserted.
	if key := ResolveAPIKey("", profile); key == "" {
		t.Error("no per-user sources: got no key, want the token file's or the environment's")
	}
	createTokenFile(t, GetUserTokenPath(profile), "user-key-123")
	if key := ResolveAPIKey("", profile); key != "user-key-123" {
		t.Errorf("per-user token file: got %q, want user-key-123", key)
	}
}

func TestGetUserTokenPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		// With neither location on disk, the current (Local) path is returned.
		profile := t.TempDir()
		expected := filepath.Join(profile, "AppData", "Local", "Rescale", "Interlink", "token")
		if path := GetUserTokenPath(profile); path != expected {
			t.Errorf("Windows path: expected %q, got %q", expected, path)
		}

		// Transition window: an existing Roaming token still takes precedence.
		oldDir := filepath.Join(profile, "AppData", "Roaming", "Rescale", "Interlink")
		if err := os.MkdirAll(oldDir, 0700); err != nil {
			t.Fatalf("MkdirAll %s: %v", oldDir, err)
		}
		oldToken := filepath.Join(oldDir, "token")
		if err := os.WriteFile(oldToken, []byte("k"), 0600); err != nil {
			t.Fatalf("WriteFile %s: %v", oldToken, err)
		}
		if path := GetUserTokenPath(profile); path != oldToken {
			t.Errorf("Windows Roaming fallback: expected %q, got %q", oldToken, path)
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
