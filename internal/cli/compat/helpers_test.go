package compat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// statusServer serves a fixed current status from the job statuses endpoint.
func statusServer(t *testing.T, status string) *api.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []models.JobStatusEntry{{Status: status}},
		})
	}))
	t.Cleanup(server.Close)
	return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})
}

// TestCompatMonitorJob_TerminalStatuses covers the unified terminal set: a
// Stopped or Force Stopped job must end the poll loop, and must not be reported
// as a success.
func TestCompatMonitorJob_TerminalStatuses(t *testing.T) {
	orig := compatMonitorInterval
	compatMonitorInterval = 5 * time.Millisecond
	t.Cleanup(func() { compatMonitorInterval = orig })

	tests := []struct {
		status  string
		wantErr bool
	}{
		{"Completed", false},
		{"Failed", true},
		{"Terminated", true},
		{"Stopped", true},
		{"Force Stopped", true},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			err := compatMonitorJob(ctx, "job123", statusServer(t, tt.status), &CompatContext{Quiet: true})

			if ctx.Err() != nil {
				t.Fatalf("status %q did not terminate the monitor loop", tt.status)
			}
			if tt.wantErr {
				if err == nil {
					t.Fatalf("status %q returned nil, want a failure", tt.status)
				}
				if !strings.Contains(err.Error(), tt.status) {
					t.Errorf("error = %q, want it to name status %q", err, tt.status)
				}
				return
			}
			if err != nil {
				t.Errorf("status %q returned %v, want nil", tt.status, err)
			}
		})
	}
}

// TestRunCompatDownloadBatch_RejectsUnsafeNames covers the guard on the
// server-supplied name. It sits ahead of the MkdirAll so a traversal name never
// creates a directory outside the output directory.
func TestRunCompatDownloadBatch_RejectsUnsafeNames(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
	}{
		{"unix traversal", "../../escaped/evil.dat"},
		{"windows separator", `..\..\escaped\evil.dat`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			outputDir := filepath.Join(root, "out")
			if err := os.MkdirAll(outputDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}

			items := []compatDownloadItem{{
				idx:       0,
				fileID:    "file123",
				name:      tt.fileName,
				size:      10,
				localPath: filepath.Join(outputDir, tt.fileName),
			}}

			err := runCompatDownloadBatch(context.Background(), items, "TEST",
				func(int) *models.CloudFile { return &models.CloudFile{} },
				statusServer(t, "Completed"), &CompatContext{Quiet: true})

			if err == nil {
				t.Fatal("runCompatDownloadBatch returned nil, want an invalid-filename error")
			}
			if !strings.Contains(err.Error(), "invalid filename from API for file file123") {
				t.Errorf("error = %q, want the shared invalid-filename wording", err)
			}
			if _, statErr := os.Stat(filepath.Join(root, "escaped")); !os.IsNotExist(statErr) {
				t.Errorf("traversal directory was created outside the output dir (stat error = %v)", statErr)
			}
		})
	}
}
