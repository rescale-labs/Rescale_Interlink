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
// tag, and the tag earlier versions applied, mean the job is downloaded; a
// started tag holds the job back from every client but the one that put it on.
func TestCheckEligibility_ReadsTheJobsTagsOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		tags []string
		ours bool // this client has put its started tag on too
		want SkipReasonCode
	}{
		{"done", []string{config.DownloadedTag}, false, ReasonHasDownloadedTag},
		{"done by an earlier version", []string{config.LegacyDownloadedTag}, false, ReasonHasDownloadedTag},
		{"started by another client", []string{otherClaim(time.Now()), "wanted"}, false, ReasonHasStartedTag},
		{"started by this client", []string{"wanted"}, true, ReasonNone},
		{"without the conditional tag", nil, true, ReasonConditionalMissingTag},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := NewState(filepath.Join(t.TempDir(), "state.json"))
			names := tc.tags
			if tc.ours {
				names = append(names, state.MarkStarted("j1", time.Now().Add(-time.Hour)))
			}
			var tagReads atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body any
				switch r.URL.Path {
				case "/api/v3/jobs/j1/tags/":
					tagReads.Add(1)
					tags := []api.JobTag{}
					for _, name := range names {
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
			client := api.NewClientForTest(&config.Config{APIKey: "test-key", APIBaseURL: srv.URL, ProxyMode: "no-proxy"})
			m := NewMonitorWithEligibility(client, state, nil, &EligibilityConfig{AutoDownloadTag: "wanted", LookbackDays: 7}, logging.NewLoggerWithWriter(io.Discard))

			got := m.CheckEligibility(context.Background(), &CompletedJob{ID: "j1"})
			if got.Reason.Code != tc.want || got.EligibleForDownload != (tc.want == ReasonNone) || tagReads.Load() != 1 {
				t.Errorf("reason %q (eligible %v) after %d reads of the tags; want %q after one", got.Reason.Code, got.EligibleForDownload, tagReads.Load(), tc.want)
			}
		})
	}
}

// The daemon takes its started tag off a job however the attempt ends: once
// the done tag is on, when the attempt fails, and when the daemon stops
// mid-download; the state file says so at once, since a stopping daemon may
// not live to save it again. A removal that fails, or that takes longer than
// the 5 s a stopping daemon has, leaves the tag the daemon's own, for the next
// poll to take off. A job this client has no started tag on loses none.
func TestDownloadJob_ReleasesTheStartedTag(t *testing.T) {
	payload := []byte("abc")
	present := models.JobFile{ID: "f1", Name: "present.txt", DecryptedSize: 3, FileChecksums: sha512Of(t, payload)}
	refused := models.JobFile{ID: "f1", Name: "../escape.txt", DecryptedSize: 1}
	downloading := models.JobFile{ID: "f1", Name: "out1.txt", DecryptedSize: 9}
	const removed = "-" // the removal of this client's started tag
	done := "+" + config.DownloadedTag
	for _, tc := range []struct {
		name   string
		files  []models.JobFile // nil: the listing fails
		remove string           // how a removal ends: "", "fails" or "hangs"
		want   []string         // the tag changes, in order; nil: the job is not this client's
		failed bool             // a failed attempt is recorded
	}{
		{"downloaded", []models.JobFile{present}, "", []string{done, removed}, false},
		{"failed", []models.JobFile{refused}, "", []string{removed}, true},
		{"the listing fails", nil, "", []string{removed}, true},
		{"the daemon stops", []models.JobFile{downloading}, "", []string{removed}, false},
		{"the removal fails", []models.JobFile{refused}, "fails", []string{removed}, true},
		{"the removal hangs as the daemon stops", []models.JobFile{downloading}, "hangs", []string{removed}, false},
		{"not this client's", nil, "", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const jobID = "tagged1"
			ctx, stopDaemon := context.WithCancel(context.Background())
			defer stopDaemon()
			var own string // this client's started tag
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
					mu.Lock()
					change := "+" + tag.Name
					if r.Method == http.MethodDelete {
						change = "-" + tag.Name
						if tag.Name == own {
							change = removed
						}
					}
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
			d := newDownloadTestDaemon(t, srv.URL, dir, &EligibilityConfig{LookbackDays: 7})
			if tc.want != nil { // claimed, as claim saves it
				mu.Lock()
				own = d.state.MarkStarted(jobID, time.Now())
				mu.Unlock()
				if err := d.state.Save(); err != nil {
					t.Fatal(err)
				}
			}
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
			err := saved.Load()
			_, kept := d.state.StartedTag(jobID)
			_, keptOnDisk := saved.StartedTag(jobID)
			if err != nil || kept != (tc.remove != "") || keptOnDisk != (tc.remove != "") {
				t.Errorf("after the attempt the started tag is this client's: %v, in the state file %v (%v); want %v",
					kept, keptOnDisk, err, tc.remove != "")
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
	own := d.state.MarkStarted(jobID, time.Now())

	d.poll(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"POST " + config.DownloadedTag, "DELETE " + own}; !slices.Equal(changes, want) {
		t.Errorf("tag changes %v, want %v", changes, want)
	}
	saved := NewState(d.cfg.StateFile)
	err := saved.Load()
	_, kept := d.state.StartedTag(jobID)
	_, keptOnDisk := saved.StartedTag(jobID)
	if err != nil || kept || keptOnDisk {
		t.Errorf("after the tag retry the started tag is this client's: %v, in the state file %v (%v)", kept, keptOnDisk, err)
	}
}
