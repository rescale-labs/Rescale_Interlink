package config

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
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

// TestResolveAPIKeySource covers both ResolveAPIKey and ResolveAPIKeySource,
// which share the resolution chain. The point of service mode is that it stops
// at per-user sources: it must never fall through to the process environment or
// the invoking user's own token file, because the Windows service resolves
// credentials on behalf of somebody else.
func TestResolveAPIKeySource(t *testing.T) {
	tests := []struct {
		name string
		// explicitKey is the --api-key equivalent.
		explicitKey string
		// userToken, when set, is written to the profile's per-user token path.
		userToken   string
		env         string
		serviceMode bool
		wantKey     string   // exact expected key ("" means: expect empty)
		wantSource  string   // exact expected source ("" means: expect empty)
		anyKey      bool     // only require a non-empty key
		anySource   []string // the source must be one of these
	}{
		{
			// The env var would win in non-service mode; here the per-user token must.
			name:      "service mode uses the per-user token file",
			userToken: "user-key-123", env: "env-key-should-not-be-used", serviceMode: true,
			wantKey: "user-key-123", wantSource: "user-token-file",
		},
		{
			name: "service mode with no per-user sources returns empty",
			env:  "env-key-should-not-be-used", serviceMode: true,
		},
		{
			// The real default token file may exist on the machine running this,
			// so only the step reached is asserted, not the value.
			name:   "non-service mode reaches the token file or environment",
			env:    "env-key-456",
			anyKey: true, anySource: []string{"token-file", "environment"},
		},
		{
			name:        "explicit key wins in service mode",
			explicitKey: "explicit-key", serviceMode: true,
			wantKey: "explicit-key", wantSource: "flag",
		},
		{
			name:        "explicit key wins in non-service mode",
			explicitKey: "explicit-key",
			wantKey:     "explicit-key", wantSource: "flag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := filepath.Join(t.TempDir(), "profile")
			if tt.userToken != "" {
				createTokenFile(t, GetUserTokenPath(profile), tt.userToken)
			}
			t.Setenv("RESCALE_API_KEY", tt.env)

			key, source := ResolveAPIKeySource(tt.explicitKey, profile, tt.serviceMode)

			// ResolveAPIKey is the same chain without the source label.
			if plain := ResolveAPIKey(tt.explicitKey, profile, tt.serviceMode); plain != key {
				t.Errorf("ResolveAPIKey = %q, ResolveAPIKeySource key = %q; must agree", plain, key)
			}

			switch {
			case tt.anyKey:
				if key == "" {
					t.Error("expected a non-empty key, got empty")
				}
			default:
				if key != tt.wantKey {
					t.Errorf("key = %q, want %q", key, tt.wantKey)
				}
			}

			if tt.anySource != nil {
				if !slices.Contains(tt.anySource, source) {
					t.Errorf("source = %q, want one of %v", source, tt.anySource)
				}
			} else if source != tt.wantSource {
				t.Errorf("source = %q, want %q", source, tt.wantSource)
			}

			// Non-service inputs must go empty under service mode: that is the
			// isolation guarantee the Windows service depends on.
			if !tt.serviceMode && tt.explicitKey == "" {
				if k, s := ResolveAPIKeySource("", profile, true); k != "" || s != "" {
					t.Errorf("service mode should return empty for the same inputs, got (%q, %q)", k, s)
				}
			}
		})
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
