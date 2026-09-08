package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// An --interval below one reached time.NewTicker, which panics on a non-positive
// duration: the command took the user's job ID, built a client, made the first
// API call and only then crashed. The value has to be refused before any of that.
func TestJobsTailRefusesIntervalBelowOne(t *testing.T) {
	for _, interval := range []string{"0", "-5"} {
		t.Run("interval "+interval, func(t *testing.T) {
			orig := getAPIClientFn
			getAPIClientFn = func() (*api.Client, error) {
				t.Errorf("jobs tail --interval %s built an API client before rejecting the interval", interval)
				return nil, errors.New("no client for an invalid interval")
			}
			t.Cleanup(func() { getAPIClientFn = orig })

			cmd := newJobsTailCmd()
			cmd.SetArgs([]string{"--job-id", "job123", "--interval", interval})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SilenceUsage = true

			err := cmd.Execute()
			if err == nil {
				t.Fatalf("--interval %s was accepted; time.NewTicker panics on it", interval)
			}
			if !strings.Contains(err.Error(), "--interval") {
				t.Errorf("error %q does not name --interval", err)
			}
		})
	}
}

// Ctrl+C cancels the CLI's shared context, and the tail loop ignored it: every
// poll after the cancellation failed, was logged and retried, so the command
// kept running until the process was killed. The loop must end when the context
// is done.
//
// The fake platform cancels on the second poll rather than up front, so the
// cancellation lands while the loop is running rather than during the initial
// poll that precedes it.
func TestJobsTailStopsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var once sync.Once
	polls := 0
	var pollMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pollMu.Lock()
		polls++
		second := polls == 2
		pollMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		// Never terminal: only the cancellation can end this loop.
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []models.JobStatusEntry{{Status: "Executing", StatusDate: "2026-09-05T00:00:00Z"}},
		})
		if second {
			once.Do(cancel)
		}
	}))
	defer server.Close()

	origClient := getAPIClientFn
	getAPIClientFn = func() (*api.Client, error) {
		return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), nil
	}
	t.Cleanup(func() { getAPIClientFn = origClient })

	// Stand in for the context Execute() installs and the signal handler cancels.
	origCtx, origCancel := rootContext, cancelFunc
	rootContext, cancelFunc = ctx, cancel
	t.Cleanup(func() { rootContext, cancelFunc = origCtx, origCancel })

	cmd := newJobsTailCmd()
	cmd.SetArgs([]string{"--job-id", "job123", "--interval", "1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true

	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("jobs tail returned %v, want the cancellation", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("jobs tail is still polling after the context was cancelled — Ctrl+C does not stop it")
	}
}
