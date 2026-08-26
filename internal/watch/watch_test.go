package watch

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// mockStatusSequence returns statuses in order, then repeats the last one.
func mockStatusSequence(statuses ...string) StatusFunc {
	var mu sync.Mutex
	idx := 0
	return func(_ context.Context, _ string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if idx >= len(statuses) {
			return statuses[len(statuses)-1], nil
		}
		s := statuses[idx]
		idx++
		return s, nil
	}
}

// mockStatusError returns an error every call.
func mockStatusError(err error) StatusFunc {
	return func(_ context.Context, _ string) (string, error) {
		return "", err
	}
}

// mockStatusErrorThenRecover returns errors for the first n calls, then the given status.
func mockStatusErrorThenRecover(n int, status string) StatusFunc {
	var mu sync.Mutex
	calls := 0
	return func(_ context.Context, _ string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls <= n {
			return "", fmt.Errorf("transient error %d", calls)
		}
		return status, nil
	}
}

// noopDownload is a download function that does nothing.
func noopDownload(_ context.Context, _ string) error { return nil }

// errorDownload returns an error on each download pass.
func errorDownload(_ context.Context, _ string) error {
	return fmt.Errorf("download failed")
}

// countingDownload counts how many times it was called.
func countingDownload(counter *int) DownloadFunc {
	var mu sync.Mutex
	return func(_ context.Context, _ string) error {
		mu.Lock()
		defer mu.Unlock()
		*counter++
		return nil
	}
}

// shortConfig returns a Config with a tiny interval for fast tests.
func shortConfig() Config {
	return Config{
		Interval:             10 * time.Millisecond,
		MaxConsecutiveErrors: 5,
	}
}

// collectCallbacks returns a Callbacks that records events.
type callbackLog struct {
	mu             sync.Mutex
	StatusChanges  [][3]string // [jobID, old, new]
	DownloadPasses []string    // jobID
	Terminals      [][2]string // [jobID, status]
	Errors         []string    // jobID
}

func (cl *callbackLog) callbacks() *Callbacks {
	return &Callbacks{
		OnStatusChange: func(jobID, old, new string) {
			cl.mu.Lock()
			defer cl.mu.Unlock()
			cl.StatusChanges = append(cl.StatusChanges, [3]string{jobID, old, new})
		},
		OnDownloadPass: func(jobID string, _ error) {
			cl.mu.Lock()
			defer cl.mu.Unlock()
			cl.DownloadPasses = append(cl.DownloadPasses, jobID)
		},
		OnTerminal: func(jobID, status string) {
			cl.mu.Lock()
			defer cl.mu.Unlock()
			cl.Terminals = append(cl.Terminals, [2]string{jobID, status})
		},
		OnError: func(jobID string, _ error) {
			cl.mu.Lock()
			defer cl.mu.Unlock()
			cl.Errors = append(cl.Errors, jobID)
		},
	}
}

// --- WatchNewerThan tests ---

func mockLister(jobs ...JobInfo) JobLister {
	return func(_ context.Context, _ string) ([]JobInfo, error) {
		return jobs, nil
	}
}

// growingLister returns an empty list on first call, then the full list on subsequent calls.
func growingLister(firstCall []JobInfo, laterCalls []JobInfo) JobLister {
	var mu sync.Mutex
	callCount := 0
	return func(_ context.Context, _ string) ([]JobInfo, error) {
		mu.Lock()
		defer mu.Unlock()
		callCount++
		if callCount == 1 {
			return firstCall, nil
		}
		return laterCalls, nil
	}
}

func simpleFactory(dlFn DownloadFunc) DownloadFuncFactory {
	return func(_ string) DownloadFunc { return dlFn }
}

// completeOnCall reports "Running" until each job has been polled n times,
// then "Completed" for that job.
func completeOnCall(n int) StatusFunc {
	var mu sync.Mutex
	calls := map[string]int{}
	return func(_ context.Context, jobID string) (string, error) {
		mu.Lock()
		calls[jobID]++
		seen := calls[jobID]
		mu.Unlock()
		if seen >= n {
			return "Completed", nil
		}
		return "Running", nil
	}
}

// TestWatchJob drives WatchJob over the shared fixture: shortConfig(), a
// background context, a counting download and a recording callbackLog. Each
// row states only its deltas — a config tweak, a status source, a download
// func, a nil Callbacks pointer, or a cancel deadline.
func TestWatchJob(t *testing.T) {
	tests := []struct {
		name        string
		tuneCfg     func(*Config)
		status      StatusFunc
		download    DownloadFunc // nil: countingDownload(&dlCount)
		nilCB       bool         // pass a nil *Callbacks
		cancelAfter time.Duration
		wantErr     error // exact error required
		wantAnyErr  bool  // any non-nil error required
		check       func(t *testing.T, log *callbackLog, dlCount int)
	}{
		{
			name:   "already terminal",
			status: mockStatusSequence("Completed"),
			check: func(t *testing.T, log *callbackLog, dlCount int) {
				if dlCount != 1 {
					t.Errorf("expected 1 download pass, got %d", dlCount)
				}
				if len(log.Terminals) != 1 {
					t.Errorf("expected 1 terminal callback, got %d", len(log.Terminals))
				}
				if len(log.Terminals) > 0 && log.Terminals[0][1] != "Completed" {
					t.Errorf("expected terminal status Completed, got %s", log.Terminals[0][1])
				}
			},
		},
		{
			name:   "transitions to completed",
			status: mockStatusSequence("Queued", "Running", "Completed"),
			check: func(t *testing.T, log *callbackLog, dlCount int) {
				// Downloads: initial tick + each subsequent tick until terminal + final sweep
				if dlCount < 3 {
					t.Errorf("expected at least 3 download passes, got %d", dlCount)
				}
				if len(log.StatusChanges) < 3 {
					t.Errorf("expected at least 3 status changes (empty->Queued, Queued->Running, Running->Completed), got %d", len(log.StatusChanges))
				}
			},
		},
		{
			name:       "transitions to failed",
			status:     mockStatusSequence("Running", "Failed"),
			download:   noopDownload,
			wantAnyErr: true,
			check: func(t *testing.T, log *callbackLog, _ int) {
				if len(log.Terminals) == 0 || log.Terminals[len(log.Terminals)-1][1] != "Failed" {
					t.Errorf("expected terminal callback with Failed status")
				}
			},
		},
		{
			name:        "context cancel",
			status:      mockStatusSequence("Running"), // never terminal
			download:    noopDownload,
			nilCB:       true,
			cancelAfter: 30 * time.Millisecond,
			wantErr:     context.Canceled,
		},
		{
			name:       "max consecutive errors",
			tuneCfg:    func(c *Config) { c.MaxConsecutiveErrors = 3 },
			status:     mockStatusError(fmt.Errorf("api unavailable")),
			download:   noopDownload,
			nilCB:      true,
			wantAnyErr: true,
		},
		{
			name:     "error recovery",
			status:   mockStatusErrorThenRecover(2, "Completed"), // 2 errors then "Completed"
			download: noopDownload,
			nilCB:    true,
		},
		{
			// Download always fails, but the loop should continue until terminal.
			name:     "download error continues",
			status:   mockStatusSequence("Running", "Completed"),
			download: errorDownload,
			check: func(t *testing.T, log *callbackLog, _ int) {
				// Should have recorded download errors via callback
				if len(log.DownloadPasses) < 2 {
					t.Errorf("expected at least 2 download passes, got %d", len(log.DownloadPasses))
				}
			},
		},
		{
			// Verifies a nil Callbacks pointer doesn't panic.
			name:     "nil callbacks",
			status:   mockStatusSequence("Completed"),
			download: noopDownload,
			nilCB:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := shortConfig()
			if tc.tuneCfg != nil {
				tc.tuneCfg(&cfg)
			}

			ctx := context.Background()
			if tc.cancelAfter > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				go func() {
					time.Sleep(tc.cancelAfter)
					cancel()
				}()
			}

			var log callbackLog
			cb := log.callbacks()
			if tc.nilCB {
				cb = nil
			}
			var dlCount int
			download := tc.download
			if download == nil {
				download = countingDownload(&dlCount)
			}

			err := WatchJob(ctx, "job1", cfg, tc.status, download, cb)

			switch {
			case tc.wantErr != nil:
				if err != tc.wantErr {
					t.Fatalf("expected %v, got: %v", tc.wantErr, err)
				}
			case tc.wantAnyErr:
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
			default:
				if err != nil {
					t.Fatalf("expected nil error, got: %v", err)
				}
			}
			if tc.check != nil {
				tc.check(t, &log, dlCount)
			}
		})
	}
}

// TestWatchNewerThan drives WatchNewerThan over the same fixture as
// TestWatchJob, with the job list supplied per row.
func TestWatchNewerThan(t *testing.T) {
	tests := []struct {
		name     string
		lister   JobLister
		status   StatusFunc
		download DownloadFunc // nil: countingDownload(&dlCount)
		nilCB    bool
		timeout  time.Duration
		check    func(t *testing.T, log *callbackLog, dlCount int)
	}{
		{
			name:     "empty list",
			lister:   mockLister(), // empty
			status:   mockStatusSequence("Completed"),
			download: noopDownload,
			nilCB:    true,
		},
		{
			name: "all terminal",
			lister: mockLister(
				JobInfo{ID: "j1", Name: "Job1", Status: "Completed"},
				JobInfo{ID: "j2", Name: "Job2", Status: "Completed"},
			),
			// Both jobs return Completed immediately.
			status: mockStatusSequence("Completed"),
			check: func(t *testing.T, log *callbackLog, dlCount int) {
				if dlCount < 2 {
					t.Errorf("expected at least 2 download passes, got %d", dlCount)
				}
				if len(log.Terminals) != 2 {
					t.Errorf("expected 2 terminal callbacks, got %d", len(log.Terminals))
				}
			},
		},
		{
			name: "mixed states",
			lister: mockLister(
				JobInfo{ID: "j1", Name: "Job1"},
				JobInfo{ID: "j2", Name: "Job2"},
			),
			// j1 is terminal at once; j2 transitions to Completed on its 2nd call.
			status: func() StatusFunc {
				running := completeOnCall(2)
				return func(ctx context.Context, jobID string) (string, error) {
					if jobID == "j1" {
						return "Completed", nil
					}
					return running(ctx, jobID)
				}
			}(),
			download: noopDownload,
			nilCB:    true,
		},
		{
			name: "new job discovery",
			lister: growingLister(
				[]JobInfo{{ID: "j1", Name: "Job1"}},
				[]JobInfo{{ID: "j1", Name: "Job1"}, {ID: "j2", Name: "Job2"}},
			),
			status:   completeOnCall(2), // all jobs complete on 2nd status check
			download: noopDownload,
			check: func(t *testing.T, log *callbackLog, _ int) {
				// j2 should have been discovered and tracked
				foundJ2 := false
				for _, sc := range log.StatusChanges {
					if sc[0] == "j2" {
						foundJ2 = true
						break
					}
				}
				if !foundJ2 {
					t.Error("expected j2 to appear in status changes (discovered on 2nd tick)")
				}
			},
		},
		{
			// All jobs terminal + no new jobs on re-discovery -> exits.
			name:     "exit condition",
			lister:   mockLister(JobInfo{ID: "j1", Name: "Job1"}),
			status:   mockStatusSequence("Completed"),
			download: noopDownload,
			nilCB:    true,
			timeout:  500 * time.Millisecond,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}

			var log callbackLog
			cb := log.callbacks()
			if tc.nilCB {
				cb = nil
			}
			var dlCount int
			download := tc.download
			if download == nil {
				download = countingDownload(&dlCount)
			}

			err := WatchNewerThan(ctx, "ref1", shortConfig(), tc.lister, tc.status,
				simpleFactory(download), cb)
			if err != nil {
				t.Fatalf("expected nil, got: %v", err)
			}
			if tc.check != nil {
				tc.check(t, &log, dlCount)
			}
		})
	}
}
