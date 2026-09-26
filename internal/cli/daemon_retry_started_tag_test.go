package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/daemon"
)

// writeStateFile writes a daemon state file holding raw, in the format the
// daemon writes.
func writeStateFile(t *testing.T, raw string) string {
	t.Helper()
	stateFile := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(stateFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return stateFile
}

// With no daemon running, 'daemon retry' also takes off the started tags this
// client left on jobs, and forgets them, so other clients may take the jobs. A
// job whose done tag is still to go on keeps its tag: it holds the job until
// then.
func TestDaemonRetryRemovesThisClientsStartedTags(t *testing.T) {
	home := t.TempDir()
	for _, v := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME"} {
		t.Setenv(v, home)
	}
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	stamp, _ := json.Marshal(at)
	stateFile := writeStateFile(t, fmt.Sprintf(`{
  "version": "1.1.0",
  "client_id": "c0ffee01",
  "started": {"held1": %[1]s, "pending1": %[1]s},
  "downloaded": {"pending1": {"job_id": "pending1", "downloaded_at": %[1]s, "pending_tag_apply": true}}
}`, stamp))

	var mu sync.Mutex
	var removed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var tag api.JobTag
		_ = json.NewDecoder(r.Body).Decode(&tag)
		mu.Lock()
		removed = append(removed, r.Method+" "+r.URL.Path+" "+tag.Name)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	orig := getAPIClientFn
	getAPIClientFn = func() (*api.Client, error) {
		return api.NewClientForTest(&config.Config{APIKey: "test-key", APIBaseURL: srv.URL, ProxyMode: "no-proxy"}), nil
	}
	t.Cleanup(func() { getAPIClientFn = orig })

	// While a daemon runs, which may be downloading the jobs, their tags stay.
	if err := daemon.WritePIDFile(); err != nil {
		t.Fatal(err)
	}
	out, err := runDaemonCommand(t, newDaemonRetryCmd(), "--all", "--state-file", stateFile)
	daemon.RemovePIDFile()
	mu.Lock()
	early := slices.Clone(removed)
	mu.Unlock()
	if err != nil || len(early) != 0 {
		t.Fatalf("daemon retry --all with a daemon running: %v, requests %v\n%s", err, early, out)
	}

	out, err = runDaemonCommand(t, newDaemonRetryCmd(), "--all", "--state-file", stateFile)
	if err != nil {
		t.Fatalf("daemon retry --all: %v\n%s", err, out)
	}
	tag := fmt.Sprintf("%s:c0ffee01:%d", config.StartedTag, at.UnixMilli())
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"DELETE /api/v3/jobs/held1/tags/ " + tag}; !slices.Equal(removed, want) {
		t.Errorf("requests %v, want %v", removed, want)
	}
	if !strings.Contains(out, "held1") {
		t.Errorf("output does not name the job whose started tag was taken off:\n%s", out)
	}
	saved := daemon.NewState(stateFile)
	if err := saved.Load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := saved.Started["held1"]; ok {
		t.Error("the state file still holds held1's started tag")
	}
	if _, ok := saved.Started["pending1"]; !ok {
		t.Error("the state file lost the started tag of a job still owed its done tag")
	}
}

// 'daemon status' says how many jobs the last poll left to other clients.
func TestDaemonStatusCountsJobsOtherClientsAreDownloading(t *testing.T) {
	home, err := os.MkdirTemp("", "daemon-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	for _, v := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME"} {
		t.Setenv(v, home)
	}
	stateFile := writeStateFile(t, `{"version": "1.1.0", "downloaded": {}, "held_elsewhere": 2}`)

	out, err := runDaemonCommand(t, newDaemonStatusCmd(), "--state-file", stateFile)
	if err != nil {
		t.Fatalf("daemon status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Jobs Other Clients Are Downloading (last poll): 2") {
		t.Errorf("daemon status does not count the jobs other clients are downloading:\n%s", out)
	}
}
