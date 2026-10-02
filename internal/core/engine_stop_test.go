package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/models"
)

// A run the user stops ends its log as stopped: not as completed, and not as
// an error, as when the stop cut off the upload of a common input file.
func TestStoppedRunLogsThatItWasStopped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail":"refused"}`, http.StatusBadRequest)
	}))
	defer server.Close()
	common := filepath.Join(t.TempDir(), "common.dat")
	if err := os.WriteFile(common, []byte("shared"), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, files := range map[string][]string{"stopped": nil, "stopped during the common file's upload": {common}} {
		t.Run(name, func(t *testing.T) {
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
			logs := engine.Events().Subscribe(events.EventLog)
			job := models.JobSpec{JobName: "job1", Command: "run", CoreType: "emerald", CoresPerSlot: 1, Slots: 1,
				ExtraInputFileIDs: "file1"}

			ctx, stop := context.WithCancel(context.Background())
			stop()
			_ = engine.RunFromSpecsWithOptions(ctx, []models.JobSpec{job}, filepath.Join(t.TempDir(), "state.csv"),
				RunOptions{CommonInputFiles: files})

			var logged []string
			for len(logs) > 0 {
				logged = append(logged, (<-logs).(*events.LogEvent).Message)
			}
			all := strings.Join(logged, "\n")
			if !strings.Contains(all, "Pipeline stopped by user") ||
				strings.Contains(all, "Pipeline completed successfully") || strings.Contains(all, "Pipeline error") {
				t.Errorf("a stopped run logged\n%s\nwant it stopped, and neither completed nor an error", all)
			}
		})
	}
}
