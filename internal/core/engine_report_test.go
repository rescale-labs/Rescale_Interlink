package core

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
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/models"
)

// A run whose every job failed ends in the pipeline's roll-up, "N of M job(s)
// failed", which names no job's cause. The GUI offers a report on the first job
// error that warrants one: none for jobs refused by their own folders, the
// server's own words for jobs it failed, and none for a run in which a job ran.
func TestFailedRunIsReportedOnItsJobsErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, _ := io.ReadAll(r.Body); strings.Contains(r.URL.Path+string(body), "ran") {
			_, _ = io.WriteString(w, `{"id":"ran"}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"detail":"FAKE server failure"}`)
	}))
	defer server.Close()
	job := models.JobSpec{JobName: "job1", Command: "run", CoreType: "emerald", CoresPerSlot: 1, Slots: 1}
	missing, remote := job, job
	missing.Directory = filepath.Join(t.TempDir(), "missing")
	remote.ExtraInputFileIDs = "file1"
	ran := remote
	ran.JobName = "ran"

	for _, tc := range []struct {
		name string
		job  models.JobSpec
		want string // in the report; "" for none
	}{
		{"jobs refused by their own folders", missing, ""},
		{"jobs the server failed", remote, "FAKE server failure"},
		{"a job the server failed beside one it ran", ran, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			// The engine's own client goes to a proxy on a closed local port, and
			// the run's to the local server: nothing leaves the machine.
			cfg, _ := config.LoadConfigCSV("")
			cfg.APIKey, cfg.ProxyMode, cfg.ProxyHost, cfg.ProxyPort = "test-key", "basic", "127.0.0.1", 9
			engine, err := NewEngine(cfg)
			if err != nil {
				t.Fatal(err)
			}
			engine.apiClient = api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test-key"})
			reports := engine.Events().Subscribe(events.EventReportableError)
			second := tc.job
			second.JobName = "job2"

			if err := engine.RunFromSpecsWithOptions(context.Background(), []models.JobSpec{tc.job, second}, filepath.Join(t.TempDir(), "state.csv"), RunOptions{}); err == nil {
				t.Fatal("the run succeeded, want a job failed")
			}
			got := ""
			select {
			case ev := <-reports:
				got = ev.(*events.ReportableErrorEvent).ErrorMessage
			default:
			}
			if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
				t.Errorf("the failed run offered a report on %q, want one on %q", got, tc.want)
			}
		})
	}
}
