package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// TestMonitorJobUntilComplete_TerminalStatuses covers the unified terminal set:
// a Stopped or Force Stopped job must end the monitor loop, and must not be
// reported as a success.
func TestMonitorJobUntilComplete_TerminalStatuses(t *testing.T) {
	orig := jobMonitorInterval
	jobMonitorInterval = 5 * time.Millisecond
	t.Cleanup(func() { jobMonitorInterval = orig })

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
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"results": []models.JobStatusEntry{{Status: tt.status}},
				})
			}))
			defer server.Close()

			client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			err := monitorJobUntilComplete(ctx, "job123", client, GetLogger())

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

// runJobsTail executes the real 'jobs tail' command against a fake statuses
// endpoint. Tail loops until the job is terminal, so the call is bounded: a
// regression makes it poll forever rather than return, and the timeout turns
// that into a fast failure instead of a hung suite.
func runJobsTail(t *testing.T, statuses []models.JobStatusEntry) error {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"results": statuses})
	}))
	t.Cleanup(server.Close)

	orig := getAPIClientFn
	getAPIClientFn = func() (*api.Client, error) {
		return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), nil
	}
	t.Cleanup(func() { getAPIClientFn = orig })

	cmd := newJobsTailCmd()
	cmd.SetArgs([]string{"--job-id", "job123", "--interval", "1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true

	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()

	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("jobs tail never returned — it is waiting for a status transition that will not come")
		return nil
	}
}

// TestJobsTail_TerminalOnFirstPoll covers the two tail bugs together: the
// terminal check now runs on the initial poll, and the current status is read
// from statuses[0] because the API returns entries newest-first.
func TestJobsTail_TerminalOnFirstPoll(t *testing.T) {
	tests := []struct {
		name     string
		statuses []models.JobStatusEntry
	}{
		{
			// Old code had no terminal check on the initial poll, so it set
			// lastStatus and then waited for a change that never came.
			name: "already terminal",
			statuses: []models.JobStatusEntry{
				{Status: "Completed", StatusDate: "2026-08-26T00:00:10Z"},
			},
		},
		{
			// Old code read statuses[len-1] ("Pending") and kept polling.
			name: "newest first with older non-terminal entry",
			statuses: []models.JobStatusEntry{
				{Status: "Completed", StatusDate: "2026-08-26T00:00:10Z"},
				{Status: "Pending", StatusDate: "2026-08-26T00:00:00Z"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := runJobsTail(t, tt.statuses); err != nil {
				t.Errorf("jobs tail returned %v, want nil", err)
			}
		})
	}
}
