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

func TestWatchJob_AlreadyTerminal(t *testing.T) {
	var log callbackLog
	var dlCount int

	err := WatchJob(
		context.Background(),
		"job1",
		shortConfig(),
		mockStatusSequence("Completed"),
		countingDownload(&dlCount),
		log.callbacks(),
	)

	if err != nil {
		t.Fatalf("expected nil error for Completed, got: %v", err)
	}
	if dlCount != 1 {
		t.Errorf("expected 1 download pass, got %d", dlCount)
	}
	if len(log.Terminals) != 1 {
		t.Errorf("expected 1 terminal callback, got %d", len(log.Terminals))
	}
	if len(log.Terminals) > 0 && log.Terminals[0][1] != "Completed" {
		t.Errorf("expected terminal status Completed, got %s", log.Terminals[0][1])
	}
}

func TestWatchJob_TransitionsToCompleted(t *testing.T) {
	var log callbackLog
	var dlCount int

	err := WatchJob(
		context.Background(),
		"job1",
		shortConfig(),
		mockStatusSequence("Queued", "Running", "Completed"),
		countingDownload(&dlCount),
		log.callbacks(),
	)

	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	// Downloads: initial tick + each subsequent tick until terminal + final sweep
	if dlCount < 3 {
		t.Errorf("expected at least 3 download passes, got %d", dlCount)
	}
	if len(log.StatusChanges) < 3 {
		t.Errorf("expected at least 3 status changes (empty->Queued, Queued->Running, Running->Completed), got %d", len(log.StatusChanges))
	}
}

func TestWatchJob_TransitionsToFailed(t *testing.T) {
	var log callbackLog

	err := WatchJob(
		context.Background(),
		"job1",
		shortConfig(),
		mockStatusSequence("Running", "Failed"),
		noopDownload,
		log.callbacks(),
	)

	if err == nil {
		t.Fatal("expected error for Failed status, got nil")
	}
	if len(log.Terminals) == 0 || log.Terminals[len(log.Terminals)-1][1] != "Failed" {
		t.Errorf("expected terminal callback with Failed status")
	}
}

func TestWatchJob_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	err := WatchJob(
		ctx,
		"job1",
		shortConfig(),
		mockStatusSequence("Running"), // never terminal
		noopDownload,
		nil,
	)

	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

func TestWatchJob_MaxConsecutiveErrors(t *testing.T) {
	cfg := shortConfig()
	cfg.MaxConsecutiveErrors = 3

	err := WatchJob(
		context.Background(),
		"job1",
		cfg,
		mockStatusError(fmt.Errorf("api unavailable")),
		noopDownload,
		nil,
	)

	if err == nil {
		t.Fatal("expected error after max consecutive errors")
	}
}

func TestWatchJob_ErrorRecovery(t *testing.T) {
	// 2 errors then "Completed"
	statusFn := mockStatusErrorThenRecover(2, "Completed")

	err := WatchJob(
		context.Background(),
		"job1",
		shortConfig(),
		statusFn,
		noopDownload,
		nil,
	)

	if err != nil {
		t.Fatalf("expected nil after recovery, got: %v", err)
	}
}

func TestWatchJob_DownloadErrorContinues(t *testing.T) {
	// Download always fails, but loop should continue until terminal
	var log callbackLog

	err := WatchJob(
		context.Background(),
		"job1",
		shortConfig(),
		mockStatusSequence("Running", "Completed"),
		errorDownload,
		log.callbacks(),
	)

	if err != nil {
		t.Fatalf("expected nil (Completed), got: %v", err)
	}
	// Should have recorded download errors via callback
	if len(log.DownloadPasses) < 2 {
		t.Errorf("expected at least 2 download passes, got %d", len(log.DownloadPasses))
	}
}

func TestWatchJob_NilCallbacks(t *testing.T) {
	// Verifies nil Callbacks pointer doesn't panic
	err := WatchJob(
		context.Background(),
		"job1",
		shortConfig(),
		mockStatusSequence("Completed"),
		noopDownload,
		nil,
	)

	if err != nil {
		t.Fatalf("expected nil, got: %v", err)
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

func TestWatchNewerThan_EmptyList(t *testing.T) {
	err := WatchNewerThan(
		context.Background(),
		"ref1",
		shortConfig(),
		mockLister(), // empty
		mockStatusSequence("Completed"),
		simpleFactory(noopDownload),
		nil,
	)
	if err != nil {
		t.Fatalf("expected nil for empty list, got: %v", err)
	}
}

func TestWatchNewerThan_AllTerminal(t *testing.T) {
	var log callbackLog
	var dlCount int

	// Both jobs return Completed immediately
	statusFn := func(_ context.Context, _ string) (string, error) {
		return "Completed", nil
	}

	err := WatchNewerThan(
		context.Background(),
		"ref1",
		shortConfig(),
		mockLister(
			JobInfo{ID: "j1", Name: "Job1", Status: "Completed"},
			JobInfo{ID: "j2", Name: "Job2", Status: "Completed"},
		),
		statusFn,
		simpleFactory(countingDownload(&dlCount)),
		log.callbacks(),
	)

	if err != nil {
		t.Fatalf("expected nil, got: %v", err)
	}
	if dlCount < 2 {
		t.Errorf("expected at least 2 download passes, got %d", dlCount)
	}
	if len(log.Terminals) != 2 {
		t.Errorf("expected 2 terminal callbacks, got %d", len(log.Terminals))
	}
}

func TestWatchNewerThan_MixedStates(t *testing.T) {
	var mu sync.Mutex
	callCounts := map[string]int{}

	statusFn := func(_ context.Context, jobID string) (string, error) {
		mu.Lock()
		callCounts[jobID]++
		n := callCounts[jobID]
		mu.Unlock()

		if jobID == "j1" {
			return "Completed", nil
		}
		// j2 transitions to Completed on 2nd call
		if n >= 2 {
			return "Completed", nil
		}
		return "Running", nil
	}

	err := WatchNewerThan(
		context.Background(),
		"ref1",
		shortConfig(),
		mockLister(
			JobInfo{ID: "j1", Name: "Job1"},
			JobInfo{ID: "j2", Name: "Job2"},
		),
		statusFn,
		simpleFactory(noopDownload),
		nil,
	)

	if err != nil {
		t.Fatalf("expected nil, got: %v", err)
	}
}

func TestWatchNewerThan_NewJobDiscovery(t *testing.T) {
	var log callbackLog
	var mu sync.Mutex
	callCounts := map[string]int{}

	statusFn := func(_ context.Context, jobID string) (string, error) {
		mu.Lock()
		callCounts[jobID]++
		n := callCounts[jobID]
		mu.Unlock()
		// All jobs complete on 2nd status check
		if n >= 2 {
			return "Completed", nil
		}
		return "Running", nil
	}

	first := []JobInfo{{ID: "j1", Name: "Job1"}}
	later := []JobInfo{
		{ID: "j1", Name: "Job1"},
		{ID: "j2", Name: "Job2"},
	}

	err := WatchNewerThan(
		context.Background(),
		"ref1",
		shortConfig(),
		growingLister(first, later),
		statusFn,
		simpleFactory(noopDownload),
		log.callbacks(),
	)

	if err != nil {
		t.Fatalf("expected nil, got: %v", err)
	}
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
}

func TestWatchNewerThan_ExitCondition(t *testing.T) {
	// All jobs terminal + no new jobs on re-discovery -> exits
	statusFn := func(_ context.Context, _ string) (string, error) {
		return "Completed", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := WatchNewerThan(
		ctx,
		"ref1",
		shortConfig(),
		mockLister(JobInfo{ID: "j1", Name: "Job1"}),
		statusFn,
		simpleFactory(noopDownload),
		nil,
	)

	if err != nil {
		t.Fatalf("expected nil (all terminal), got: %v", err)
	}
}
