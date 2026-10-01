package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
)

// fakeJobsAPI answers job creates and submits, and the user's projects and the
// core types for a script that names them, and records every request, with the
// body of each create.
type fakeJobsAPI struct {
	mu       sync.Mutex
	requests []string
	creates  [][]byte
}

func (f *fakeJobsAPI) client(t *testing.T) *api.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/jobs/":
			f.creates = append(f.creates, body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"JOB1","name":"lic-job"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/jobs/JOB1/submit/":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/users/me/projects/":
			_, _ = w.Write([]byte(`{"results":[{"id":"PROJ0","name":"Other"},{"id":"PROJ1","name":"CFD Program"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/coretypes/":
			_, _ = w.Write([]byte(`{"results":[{"code":"emerald","name":"Emerald","cores":[1,2,4,8]}]}`))
		default:
			http.Error(w, `{"detail": "not faked"}`, http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})
}

func (f *fakeJobsAPI) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

// licenseSettingsSent returns the userDefinedLicenseSettings of the one job
// created, as sent.
func (f *fakeJobsAPI) licenseSettingsSent(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.creates) != 1 {
		t.Fatalf("%d job create(s), want 1 (requests: %v)", len(f.creates), f.requests)
	}
	var req struct {
		JobAnalyses []map[string]json.RawMessage `json:"jobanalyses"`
	}
	if err := json.Unmarshal(f.creates[0], &req); err != nil || len(req.JobAnalyses) != 1 {
		t.Fatalf("create body %s: %v", f.creates[0], err)
	}
	return string(req.JobAnalyses[0]["userDefinedLicenseSettings"])
}

// sgeScriptHead is a complete job script but for its license directive, which
// goes on line 8.
const sgeScriptHead = "#!/bin/bash\n#RESCALE_NAME lic-job\n#RESCALE_COMMAND ./run.sh\n" +
	"#RESCALE_ANALYSIS user_included\n#RESCALE_CORES emerald\n#RESCALE_CORES_PER_SLOT 1\n#RESCALE_WALLTIME 1\n"

// jobs submit --script reads the license directive in either spelling, and the
// create request carries it as the userDefinedLicenseSettings object, or null
// when there is none, as for rescale-cli's "=" with nothing after it. One that
// cannot be sent as written is refused, naming its line, before --files are
// uploaded or the job is created.
func TestJobsSubmitScriptLicenseDirective(t *testing.T) {
	const settings = `{"featureSets":[{"name":"USER_SPECIFIED_0","features":[{"name":"ansys_hpc","count":8}]}]}`
	var fake *fakeJobsAPI
	orig := getAPIClientFn
	getAPIClientFn = func() (*api.Client, error) { return fake.client(t), nil }
	t.Cleanup(func() { getAPIClientFn = orig })
	input := writeTempFile(t, "input.dat", "in")

	for _, tt := range []struct{ directive, want string }{
		{"#RESCALE_USER_DEFINED_LICENSE_SETTINGS " + settings, settings},
		{"#RESCALE_USER_DEFINED_LICENSE_SETTINGS=" + settings, settings},
		{"", "null"},
		{"#RESCALE_USER_DEFINED_LICENSE_SETTINGS=", "null"},
		{"#RESCALE_USER_DEFINED_LICENSE_SETTINGS ", ""},
		{`#RESCALE_USER_DEFINED_LICENSE_SETTINGS={"featureSets":[{"name":"USER_SPECIFIED_0",` +
			`"features":[{"name":"ansys_hpc"}]}]}`, ""},
	} {
		fake = &fakeJobsAPI{}
		args := []string{"--script", writeTempFile(t, "job.sh", sgeScriptHead+tt.directive+"\n"), "--create"}
		if tt.want == "" {
			err := runPURCommand(t, newJobsSubmitCmd(), append(args, "--files", input)...)
			if calls := fake.calls(); err == nil || !strings.Contains(err.Error(), "at line 8") || len(calls) != 0 {
				t.Errorf("%q: error %v after %v, want a refusal naming line 8 before any request", tt.directive, err, calls)
			}
			continue
		}
		if err := runPURCommand(t, newJobsSubmitCmd(), args...); err != nil {
			t.Fatalf("%q: jobs submit: %v", tt.directive, err)
		}
		if got := fake.licenseSettingsSent(t); got != tt.want {
			t.Errorf("%q: userDefinedLicenseSettings = %s\nwant %s", tt.directive, got, tt.want)
		}
	}
}

// pur run and pur resume refuse half a license pair as they load the jobs CSV,
// as pur plan does, instead of at the job's create request, once its archive
// has been built and uploaded.
func TestPURRunAndResumeRefuseHalfALicensePairBeforeAnyUpload(t *testing.T) {
	usePURConfig(t)
	t.Chdir(t.TempDir())
	if err := os.Mkdir("run_1", 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{filepath.Join("run_1", "input.dat"): "in", "state.csv": ""} {
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fake := &fakeJobsAPI{}
	client := fake.client(t)
	orig := newPipelineClientFn
	newPipelineClientFn = func(*config.Config) (*api.Client, error) { return client, nil }
	t.Cleanup(func() { newPipelineClientFn = orig })

	const header = "Directory,JobName,AnalysisCode,Command,CoreType,CoresPerSlot,WalltimeHours,Slots," +
		"LicenseSettings,LicenseFeatureName,LicensesPerJob\n"
	for _, tt := range []struct{ pair, want string }{
		{"ansys_hpc,", `job 1 (run_1): license feature "ansys_hpc" needs a licenses-per-job count greater than zero`},
		{",4", "job 1 (run_1): licenses per job is set to 4 but no license feature name was given"},
	} {
		row := "run_1,run_1,user_included,./solve.sh,emerald,4,1.0,1,," + tt.pair + "\n"
		if err := os.WriteFile("jobs.csv", []byte(header+row), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, cmd := range []*cobra.Command{newRunCmd(), newResumeCmd()} {
			err := runPURCommand(t, cmd, "--jobs-csv", "jobs.csv", "--state", "state.csv")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("pur %s with %q: error %v, want %q", cmd.Name(), tt.pair, err, tt.want)
			}
		}
	}
	if calls := fake.calls(); len(calls) != 0 {
		t.Errorf("the runs reached the API: %v", calls)
	}
}
