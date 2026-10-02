package wailsapp

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/reporting"
)

// An SSH public key of a type the platform does not accept, and half a license
// pair, are refused only as the job is created: after a GUI run has tarred and
// uploaded its inputs. A PUR run (the PUR tab's Load CSV starts one with no
// Validate step) and a single job are refused before they start instead, with
// the CLI's text and no error report, and nothing reaches the platform. A valid
// key and pair still run.
func TestStartRun_RefusesAKeyOrLicensePairBeforeAnyRequest(t *testing.T) {
	setIsolatedUserConfigEnv(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.RequestURI)
		mu.Unlock()
		http.Error(w, `{"detail":"not faked"}`, http.StatusBadRequest)
	}))
	defer server.Close()

	// A run's uploads go to the server, and the engine's own calls through it as
	// their proxy, which refuses them: nothing leaves the machine.
	cfg, err := config.LoadConfigCSV("")
	if err != nil {
		t.Fatal(err)
	}
	addr := server.Listener.Addr().(*net.TCPAddr)
	cfg.APIKey, cfg.ProxyMode, cfg.ProxyHost, cfg.ProxyPort = "test-key", "basic", addr.IP.String(), addr.Port
	eng, err := core.NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	eng.TransferService().SetAPIClient(api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test-key"}))
	a := &App{engine: eng, reporter: reporting.NewReporter(eng.Events()), runCancel: func() {}}
	reports := eng.Events().Subscribe(events.EventReportableError)
	inputs := t.TempDir()
	writeScanFile(t, inputs, "model.inp")

	for _, tc := range []struct {
		name, key, feature string
		count              int
		refusal            string
	}{
		{"unaccepted key", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 user@host", "", 0,
			`job 1 (job1): public key type "ssh-ed25519" is not one the platform accepts`},
		{"half a license pair", "", "abaqus", 0,
			`job 1 (job1): license feature "abaqus" needs a licenses-per-job count greater than zero`},
		{"valid key and license pair", "ssh-rsa AAAAB3NzaC1yc2E user@host", "abaqus", 4, ""},
	} {
		job := JobSpecDTO{Directory: inputs, JobName: "job1", AnalysisCode: "user_included", Command: "./run.sh",
			CoreType: "emerald", CoresPerSlot: 1, Slots: 1, WalltimeHours: 1,
			PublicKey: tc.key, LicenseFeatureName: tc.feature, LicensesPerJob: tc.count}
		for _, start := range []struct {
			name string
			run  func() (string, error)
		}{
			{"PUR", func() (string, error) { return a.StartBulkRunWithOptions([]JobSpecDTO{job}, PURRunOptionsDTO{}) }},
			{"single job", func() (string, error) {
				return a.StartSingleJob(SingleJobInputDTO{InputMode: "localFiles", LocalFiles: []string{filepath.Join(inputs, "model.inp")}, Job: job})
			}},
		} {
			mu.Lock()
			requests = nil
			mu.Unlock()
			runID, err := start.run()
			for deadline := time.Now().Add(10 * time.Second); eng.IsRunActive(); time.Sleep(10 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatalf("%s, %s: the run did not end", start.name, tc.name)
				}
			}
			a.runCancel() // a PUR run's analysis lookup outlives the run, still retrying
			mu.Lock()
			sent := requests
			mu.Unlock()
			if tc.refusal == "" && (err != nil || len(sent) == 0) {
				t.Errorf("%s, %s: run %q, error %v, requests %v; want it run", start.name, tc.name, runID, err, sent)
			}
			if tc.refusal != "" && (err == nil || !strings.Contains(err.Error(), tc.refusal) || len(sent) > 0) {
				t.Errorf("%s, %s: run %q, error %v, after requests %v; want %q before any",
					start.name, tc.name, runID, err, sent, tc.refusal)
			}
		}
	}
	select { // Report publishes synchronously
	case event := <-reports:
		t.Errorf("reported %s", event.(*events.ReportableErrorEvent).ErrorMessage)
	default:
	}
}
