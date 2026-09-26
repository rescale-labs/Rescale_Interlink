package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/reporting"
)

// 'config test' with a key the platform rejects fails with the platform's
// answer, an authentication failure the user fixes: no error report.
func TestConfigTestRejectedKeySavesNoReport(t *testing.T) {
	home := t.TempDir()
	for _, env := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "LOCALAPPDATA"} {
		t.Setenv(env, home)
	}
	t.Setenv("RESCALE_API_KEY", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"detail":"Invalid token."}`)
	}))
	defer server.Close()
	rejecting := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "FAKEKEY"})

	defer func(c, k, tf, u string, p func(*api.Client, context.Context) (*models.UserProfile, error)) {
		cfgFile, apiKey, tokenFile, apiBaseURL, getUserProfile = c, k, tf, u, p
	}(cfgFile, apiKey, tokenFile, apiBaseURL, getUserProfile)
	cfgFile, apiKey, tokenFile, apiBaseURL = filepath.Join(home, "config.csv"), "FAKEKEY", "", ""
	getUserProfile = func(_ *api.Client, ctx context.Context) (*models.UserProfile, error) {
		return rejecting.GetUserProfile(ctx)
	}

	cmd := newConfigTestCmd()
	var err error
	captureStdout(t, func() { err = cmd.RunE(cmd, nil) })
	if err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("config test returned %v, want the platform's 401", err)
	}
	if saved := reporting.HandleCLIError(err, "cli", "rescale-int config test", ""); saved != "" {
		t.Errorf("a rejected key saved an error report to %s", saved)
	}
}
