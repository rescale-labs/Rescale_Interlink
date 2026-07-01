package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
)

// Eligibility reads a job's tags once, whichever of them it checks. The done
// tag, and the tag earlier versions applied, mean the job is downloaded; the
// started tag holds the job back from every client but the one that put it on.
func TestCheckEligibility_ReadsTheJobsTagsOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		tags []string
		ours bool // this client put the started tag on
		want SkipReasonCode
	}{
		{"done", []string{config.DownloadedTag}, false, ReasonHasDownloadedTag},
		{"done by an earlier version", []string{config.LegacyDownloadedTag}, false, ReasonHasDownloadedTag},
		{"started by another client", []string{config.StartedTag, "wanted"}, false, ReasonHasStartedTag},
		{"started by this client", []string{config.StartedTag, "wanted"}, true, ReasonNone},
		{"without the conditional tag", []string{config.StartedTag}, true, ReasonConditionalMissingTag},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tagReads atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body any
				switch r.URL.Path {
				case "/api/v3/jobs/j1/tags/":
					tagReads.Add(1)
					tags := []api.JobTag{}
					for _, name := range tc.tags {
						tags = append(tags, api.JobTag{Name: name})
					}
					body = tags
				case "/api/v3/jobs/j1/custom-fields/":
					body = map[string]any{config.AutoDownloadFieldName: map[string]any{"value": "Conditional"}}
				default:
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(body)
			}))
			t.Cleanup(srv.Close)
			state := NewState(filepath.Join(t.TempDir(), "state.json"))
			if tc.ours {
				state.MarkStarted("j1")
			}
			client := api.NewClientForTest(&config.Config{APIKey: "test-key", APIBaseURL: srv.URL, ProxyMode: "no-proxy"})
			m := NewMonitorWithEligibility(client, state, nil, &EligibilityConfig{AutoDownloadTag: "wanted", LookbackDays: 7}, logging.NewLoggerWithWriter(io.Discard))

			got := m.CheckEligibility(context.Background(), "j1")
			if got.Reason.Code != tc.want || got.EligibleForDownload != (tc.want == ReasonNone) || tagReads.Load() != 1 {
				t.Errorf("reason %q (eligible %v) after %d reads of the tags; want %q after one", got.Reason.Code, got.EligibleForDownload, tagReads.Load(), tc.want)
			}
		})
	}
}

// The daemon puts the started tag on a job once it has files to fetch, having
// saved that the tag is its own, and takes it off again however the attempt
// ends: once the done tag is on, when the attempt fails, and when the daemon
// stops mid-download; the state file says so at once, since a stopping daemon
// may not live to save it again. A removal that fails, or that takes longer
// than the 5 s a stopping daemon has, leaves the tag the daemon's own, so it
// can still resume the job; an attempt that fails before the tag goes on
// removes nothing, since a started tag there would be another client's.
func TestDownloadJob_ReleasesTheStartedTag(t *testing.T) {
	payload := []byte("abc")
	present := models.JobFile{ID: "f1", Name: "present.txt", DecryptedSize: 3, FileChecksums: sha512Of(t, payload)}
	refused := models.JobFile{ID: "f1", Name: "../escape.txt", DecryptedSize: 1}
	downloading := models.JobFile{ID: "f1", Name: "out1.txt", DecryptedSize: 9}
	started, done := "+"+config.StartedTag, "+"+config.DownloadedTag
	for _, tc := range []struct {
		name   string
		files  []models.JobFile // nil: the listing fails
		remove string           // how a removal ends: "", "fails" or "hangs"
		want   []string         // the tag changes, in order
		failed bool             // a failed attempt is recorded
	}{
		{"downloaded", []models.JobFile{present}, "", []string{started, done, "-" + config.StartedTag}, false},
		{"failed", []models.JobFile{refused}, "", []string{started, "-" + config.StartedTag}, true},
		{"the daemon stops", []models.JobFile{downloading}, "", []string{started, "-" + config.StartedTag}, false},
		{"the removal fails", []models.JobFile{refused}, "fails", []string{started, "-" + config.StartedTag}, true},
		{"the removal hangs as the daemon stops", []models.JobFile{downloading}, "hangs", []string{started, "-" + config.StartedTag}, false},
		{"failed before the tag", nil, "", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const jobID = "tagged1"
			ctx, stopDaemon := context.WithCancel(context.Background())
			defer stopDaemon()
			var d *Daemon
			var mu sync.Mutex
			var changes []string
			var stoppedAt time.Time
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == fmt.Sprintf("/api/v2/jobs/%s/files/", jobID) && tc.files != nil:
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"results": tc.files})
				case r.URL.Path == fmt.Sprintf("/api/v3/jobs/%s/tags/", jobID) && r.Method != http.MethodGet:
					var tag api.JobTag
					_ = json.NewDecoder(r.Body).Decode(&tag)
					change := "+" + tag.Name
					if r.Method == http.MethodDelete {
						change = "-" + tag.Name
					}
					if change == started {
						if saved := NewState(d.cfg.StateFile); saved.Load() != nil || !saved.IsStartedByUs(jobID) {
							t.Error("the started tag went on before the state file said it was this client's")
						}
					}
					mu.Lock()
					changes = append(changes, change)
					mu.Unlock()
					switch {
					case r.Method == http.MethodPost:
						w.WriteHeader(http.StatusCreated)
					case tc.remove == "fails":
						w.WriteHeader(http.StatusInternalServerError)
					case tc.remove == "hangs":
						<-r.Context().Done()
					default:
						w.WriteHeader(http.StatusNoContent)
					}
				case r.URL.Path == "/api/v3/files/f1/":
					mu.Lock()
					stoppedAt = time.Now()
					mu.Unlock()
					stopDaemon() // the file is downloading when the daemon is told to stop
					<-r.Context().Done()
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(srv.Close)
			dir := t.TempDir()
			d = newDownloadTestDaemon(t, srv.URL, dir, &EligibilityConfig{LookbackDays: 7})
			writeFile(t, filepath.Join(ComputeOutputDir(dir, jobID, "job", false), present.Name), string(payload))

			outcome := make(chan DownloadOutcome, 1)
			go func() { outcome <- d.downloadJob(ctx, &CompletedJob{ID: jobID, Name: "job"}) }()
			select {
			case got := <-outcome:
				if (got == OutcomeDownloaded) != (tc.name == "downloaded") {
					t.Errorf("outcome %q", got)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("downloadJob did not return")
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(changes, tc.want) {
				t.Errorf("tag changes %v, want %v", changes, tc.want)
			}
			if tc.remove == "hangs" && time.Since(stoppedAt) >= 5*time.Second {
				t.Errorf("the stopping daemon spent %s releasing the tag, beyond its 5 s", time.Since(stoppedAt))
			}
			saved := NewState(d.cfg.StateFile)
			if err := saved.Load(); err != nil || d.state.IsStartedByUs(jobID) != (tc.remove != "") || saved.IsStartedByUs(jobID) != (tc.remove != "") {
				t.Errorf("after the attempt the started tag is this client's: %v, in the state file %v (%v); want %v",
					d.state.IsStartedByUs(jobID), saved.IsStartedByUs(jobID), err, tc.remove != "")
			}
			if got := d.state.AttemptCount(jobID) == 1; got != tc.failed {
				t.Errorf("a failed attempt recorded: %v, want %v", got, tc.failed)
			}
			if n := d.state.GetDownloadedCount(); n != 0 && tc.name != "downloaded" {
				t.Errorf("%d jobs counted as downloaded", n)
			}
		})
	}
}

// A job whose done tag did not go on keeps its started tag, as this client's,
// until the poll's tag retry puts the done tag on; the retry then takes the
// started tag off, and the state file forgets it.
func TestPoll_TagRetryReleasesTheStartedTag(t *testing.T) {
	const jobID = "pending1"
	var mu sync.Mutex
	var changes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/jobs/":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []models.JobResponse{}})
		case r.URL.Path == fmt.Sprintf("/api/v3/jobs/%s/tags/", jobID) && r.Method != http.MethodGet:
			var tag api.JobTag
			_ = json.NewDecoder(r.Body).Decode(&tag)
			mu.Lock()
			changes = append(changes, r.Method+" "+tag.Name)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	d := newDownloadTestDaemon(t, srv.URL, t.TempDir(), &EligibilityConfig{LookbackDays: 7})
	d.state.MarkDownloaded(jobID, "job", "", 1, 1)
	d.state.MarkPendingTagApply(jobID)
	d.state.MarkStarted(jobID)

	d.poll(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"POST " + config.DownloadedTag, "DELETE " + config.StartedTag}; !slices.Equal(changes, want) {
		t.Errorf("tag changes %v, want %v", changes, want)
	}
	saved := NewState(d.cfg.StateFile)
	if err := saved.Load(); err != nil || d.state.IsStartedByUs(jobID) || saved.IsStartedByUs(jobID) {
		t.Errorf("after the tag retry the started tag is this client's: %v, in the state file %v (%v)", d.state.IsStartedByUs(jobID), saved.IsStartedByUs(jobID), err)
	}
}
