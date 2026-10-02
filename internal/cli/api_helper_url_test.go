package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/reporting"
)

// isolateClientConfig gives the test a home of its own and no key, URL or proxy
// from the environment, and puts the global flags back when it ends.
func isolateClientConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, env := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "LOCALAPPDATA", "APPDATA"} {
		t.Setenv(env, home)
	}
	for _, env := range []string{"RESCALE_API_KEY", "RESCALE_API_URL", "HTTPS_PROXY"} {
		t.Setenv(env, "")
	}
	c, k, tf, u := cfgFile, apiKey, tokenFile, apiBaseURL
	t.Cleanup(func() { cfgFile, apiKey, tokenFile, apiBaseURL = c, k, tf, u })
	return home
}

// An API URL that is not a Rescale platform, from --api-url or from
// RESCALE_API_URL, is refused before any request, by 'config test' and by every
// command that builds a client: the user's to fix, so no error report.
func TestRefusedPlatformURLSavesNoReport(t *testing.T) {
	home := isolateClientConfig(t)
	for _, tc := range []struct{ from, flag, env string }{
		{"--api-url", "https://example.com", ""},
		{"RESCALE_API_URL", "", "https://example.com"},
	} {
		cfgFile, apiKey, tokenFile, apiBaseURL = filepath.Join(home, "config.csv"), "FAKEKEY", "", tc.flag
		t.Setenv("RESCALE_API_URL", tc.env)
		for _, run := range []struct {
			op   string
			cmd  *cobra.Command
			args []string
		}{{"hardware list", newHardwareCmd(), []string{"list"}}, {"config test", newConfigTestCmd(), nil}} {
			_, err := runDaemonCommand(t, run.cmd, run.args...)
			if err == nil || !strings.Contains(err.Error(), `invalid platform URL "https://example.com"`) {
				t.Fatalf("%s, URL from %s: %v, want the URL refused", run.op, tc.from, err)
			}
			if saved := reporting.HandleCLIError(err, "cli", "rescale-int "+run.op, ""); saved != "" {
				t.Errorf("%s, URL from %s: the refusal saved an error report to %s", run.op, tc.from, saved)
			}
		}
	}
}

// The refusal's words quoted inside another failure are not a refusal: a job
// listing whose server answer quotes them, and a client that cannot be built
// for a setting whose value holds them, still save reports of their own class.
func TestFailuresQuotingAURLRefusalSaveTheirReports(t *testing.T) {
	home := isolateClientConfig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, `invalid platform URL "https://example.com": unrecognized platform URL.`)
	}))
	defer server.Close()
	_, listErr := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "FAKEKEY"}).ListJobs(context.Background())

	cfgFile, apiKey, tokenFile, apiBaseURL = filepath.Join(home, "config.csv"), "FAKEKEY", "", ""
	if err := os.WriteFile(cfgFile, []byte(`proxy_mode,"invalid platform URL ""https://example.com"""`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, clientErr := getAPIClient() // refused while the client is built: nothing is sent

	for _, tc := range []struct {
		op    string
		err   error
		class string
	}{{"jobs list", listErr, "server_error"}, {"hardware list", clientErr, "internal"}} {
		saved := reporting.HandleCLIError(tc.err, "cli", "rescale-int "+tc.op, "")
		if data, _ := os.ReadFile(saved); saved == "" || !strings.Contains(string(data), `"errorClass": "`+tc.class+`"`) {
			t.Errorf("%s failing with %v: report %q, want one classed %s", tc.op, tc.err, saved, tc.class)
		}
	}
}
