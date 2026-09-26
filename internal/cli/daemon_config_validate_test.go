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
	"github.com/rescale/rescale-int/internal/reporting"
)

// 'daemon config validate' on a workspace without the Auto Download field
// fails with what is missing, a setup the user completes: no error report. A
// workspace it cannot read fails with the platform's answer, said once, which
// decides the report: a rejected key saves none, a server failure one.
func TestDaemonConfigValidateReportsOnlyFailures(t *testing.T) {
	home := t.TempDir()
	for _, env := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "LOCALAPPDATA"} {
		t.Setenv(env, home)
	}
	t.Setenv("RESCALE_API_KEY", "")
	defer func(c, k, tf, u string, v func(*api.Client, context.Context) (*api.AutoDownloadValidation, error)) {
		cfgFile, apiKey, tokenFile, apiBaseURL, validateAutoDownloadSetup = c, k, tf, u, v
	}(cfgFile, apiKey, tokenFile, apiBaseURL, validateAutoDownloadSetup)
	cfgFile, apiKey, tokenFile, apiBaseURL = filepath.Join(home, "config.csv"), "FAKEKEY", "", ""

	for _, tc := range []struct {
		name     string
		failing  string // the request answered with status
		status   int
		want     string
		reported bool
	}{
		{"no Auto Download field", "", 0, "validation failed with 1 error(s)", false},
		{"rejected key", "/api/v3/users/me/", http.StatusUnauthorized, "status 401", false},
		{"server failure", "/api/v2/organizations/example/workspaces/ws1/custom-fields/", http.StatusInternalServerError, "status 500", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case tc.failing:
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, `{"detail":"FAKE"}`)
				case "/api/v3/users/me/":
					_, _ = io.WriteString(w, `{"company":{"code":"example"},"workspace":{"id":"ws1"}}`)
				case "/api/v2/organizations/example/workspaces/ws1/custom-fields/":
					_, _ = io.WriteString(w, `{"isEnabled":true,"fields":{"compute":{"Context":[]}}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			fake := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "FAKEKEY"})
			validateAutoDownloadSetup = func(_ *api.Client, ctx context.Context) (*api.AutoDownloadValidation, error) {
				return fake.ValidateAutoDownloadSetup(ctx)
			}

			cmd := newDaemonConfigValidateCmd()
			cmd.SetContext(context.Background())
			var err error
			out := captureStdout(t, func() { err = cmd.RunE(cmd, nil) })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("daemon config validate returned %v, want %q", err, tc.want)
			}
			if strings.Contains(out, tc.want) {
				t.Errorf("daemon config validate printed %q as well as returning it:\n%s", tc.want, out)
			}
			if saved := reporting.HandleCLIError(err, "cli", "rescale-int daemon config validate", ""); (saved != "") != tc.reported {
				t.Errorf("%v: report saved to %q, want one: %v", err, saved, tc.reported)
			}
		})
	}
}
