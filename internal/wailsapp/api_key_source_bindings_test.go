package wailsapp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
)

func TestCredentialSourceEnvironmentIsNotSaved(t *testing.T) {
	setIsolatedUserConfigEnv(t)
	t.Setenv("RESCALE_API_KEY", "env-key")

	apiKey, source := resolveGUIAPIKeySource()
	if apiKey != "env-key" || source != "environment" {
		t.Fatalf("expected environment source, got (%q, %q)", apiKey, source)
	}

	app := &App{config: &config.Config{APIKey: apiKey}}
	dto := app.GetCredentialSource()
	if dto.Source != "environment" {
		t.Fatalf("expected credential source environment, got %q", dto.Source)
	}
	if dto.HasSavedToken {
		t.Fatal("environment-sourced API key should not imply a saved token")
	}
	if _, err := os.Stat(config.GetDefaultTokenPath()); !os.IsNotExist(err) {
		t.Fatalf("environment source created token file unexpectedly: %v", err)
	}
}

func TestClearSavedAPIKeyFallsBackToEnvironment(t *testing.T) {
	setIsolatedUserConfigEnv(t)
	t.Setenv("RESCALE_API_KEY", "env-key")

	tokenPath := config.GetDefaultTokenPath()
	if err := config.WriteTokenFile(tokenPath, "saved-key"); err != nil {
		t.Fatalf("WriteTokenFile: %v", err)
	}

	app := &App{config: &config.Config{APIKey: "saved-key"}}
	result, err := app.ClearSavedAPIKey()
	if err != nil {
		t.Fatalf("ClearSavedAPIKey: %v", err)
	}
	if !result.Removed {
		t.Fatal("expected saved token removal")
	}
	if app.config.APIKey != "env-key" {
		t.Fatalf("expected config to fall back to env key, got %q", app.config.APIKey)
	}
	if result.CredentialSource.Source != "environment" {
		t.Fatalf("expected environment source after clear, got %q", result.CredentialSource.Source)
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("expected token file to be removed, stat err: %v", err)
	}
}

func setIsolatedUserConfigEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
}
