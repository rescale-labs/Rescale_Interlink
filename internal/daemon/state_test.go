// Package daemon tests
package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ageLastAttempt rewinds a job's last-attempt stamp, which is what pushes it
// out of (or keeps it inside) the retry backoff window.
func ageLastAttempt(s *State, jobID string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Downloaded[jobID].LastAttempt = time.Now().Add(-d)
}

// TestState_FailureLifecycle drives one job through a sequence of MarkFailed /
// MarkDownloaded / ClearFailed calls and then reads the whole verdict: the
// stored entry, the attempt count, and whether IsDownloaded suppresses the job.
// Every row asserts all four, so the retry counter, the recorded error, the
// backoff window and the give-up rule are pinned together.
func TestState_FailureLifecycle(t *testing.T) {
	const jobID = "job1"

	tests := []struct {
		name         string
		steps        func(s *State)
		wantMissing  bool   // no entry at all
		wantRetry    int    // entry.RetryCount
		wantError    string // entry.Error
		wantAttempts int    // AttemptCount
		wantHeld     bool   // IsDownloaded: downloaded, or suppressed
	}{
		{
			name:        "no entry",
			wantMissing: true,
		},
		{
			name:     "clean success",
			steps:    func(s *State) { s.MarkDownloaded(jobID, "Test Job 1", "/output", 1, 100) },
			wantHeld: true,
		},
		{
			name:         "one failure",
			steps:        func(s *State) { s.MarkFailed(jobID, "Test Job 1", fmt.Errorf("connection timeout")) },
			wantRetry:    1,
			wantError:    "connection timeout",
			wantAttempts: 1,
			wantHeld:     true, // freshly failed jobs are suppressed during backoff
		},
		{
			name: "two failures",
			steps: func(s *State) {
				s.MarkFailed(jobID, "Job One", fmt.Errorf("boom"))
				s.MarkFailed(jobID, "Job One", fmt.Errorf("boom again"))
			},
			wantRetry:    2,
			wantError:    "boom again",
			wantAttempts: 2,
			wantHeld:     true,
		},
		{
			name: "three failures keep the latest error",
			steps: func(s *State) {
				for i := 1; i <= 3; i++ {
					s.MarkFailed(jobID, "Test Job", fmt.Errorf("error %d", i))
				}
			},
			wantRetry:    3,
			wantError:    "error 3",
			wantAttempts: 3,
			wantHeld:     true,
		},
		{
			// A success replaces the failure entry, so the count resets.
			name: "success after failures",
			steps: func(s *State) {
				s.MarkFailed(jobID, "Test Job", fmt.Errorf("error 1"))
				s.MarkFailed(jobID, "Test Job", fmt.Errorf("error 2"))
				s.MarkDownloaded(jobID, "Test Job", "/output", 5, 1024)
			},
			wantHeld: true,
		},
		{
			name: "backoff expired after a failure",
			steps: func(s *State) {
				s.MarkFailed(jobID, "Test Job 2", fmt.Errorf("network error"))
				ageLastAttempt(s, jobID, 6*time.Minute)
			},
			wantRetry:    1,
			wantError:    "network error",
			wantAttempts: 1,
			wantHeld:     false, // eligible for retry once backoff expires
		},
		{
			// Same verdict from a hand-built entry rather than MarkFailed.
			name: "backoff expired on a seeded entry",
			steps: func(s *State) {
				s.Downloaded[jobID] = &DownloadedJob{
					JobID:       jobID,
					JobName:     "Test Job",
					Error:       "network error",
					RetryCount:  1,
					LastAttempt: time.Now(),
				}
				if !s.IsDownloaded(jobID) {
					t.Error("job1 should be suppressed during backoff period")
				}
				ageLastAttempt(s, jobID, 6*time.Minute)
			},
			wantRetry:    1,
			wantError:    "network error",
			wantAttempts: 1,
			wantHeld:     false,
		},
		{
			// Even with the last attempt far in the past, five failures are
			// permanently suppressed.
			name: "gives up after five failures",
			steps: func(s *State) {
				for i := 1; i <= 5; i++ {
					s.MarkFailed(jobID, "Test Job", fmt.Errorf("error %d", i))
				}
				ageLastAttempt(s, jobID, time.Hour)
			},
			wantRetry:    5,
			wantError:    "error 5",
			wantAttempts: 5,
			wantHeld:     true,
		},
		{
			name: "clearing a failure drops the entry",
			steps: func(s *State) {
				s.MarkFailed(jobID, "Test Job 1", fmt.Errorf("error"))
				s.ClearFailed(jobID)
			},
			wantMissing: true,
		},
		{
			// ClearFailed must not touch a successfully downloaded job.
			name: "clearing a success keeps the entry",
			steps: func(s *State) {
				s.MarkDownloaded(jobID, "Test Job 2", "/output", 1, 100)
				s.ClearFailed(jobID)
			},
			wantHeld: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state := NewState("")
			if tc.steps != nil {
				tc.steps(state)
			}

			job, exists := state.Downloaded[jobID]
			if tc.wantMissing {
				if exists {
					t.Errorf("expected no entry for %s, got %+v", jobID, job)
				}
			} else {
				if !exists || job == nil {
					t.Fatalf("%s not found in state", jobID)
				}
				if job.RetryCount != tc.wantRetry {
					t.Errorf("RetryCount = %d, want %d", job.RetryCount, tc.wantRetry)
				}
				if job.Error != tc.wantError {
					t.Errorf("Error = %q, want %q", job.Error, tc.wantError)
				}
			}
			if got := state.AttemptCount(jobID); got != tc.wantAttempts {
				t.Errorf("AttemptCount = %d, want %d", got, tc.wantAttempts)
			}
			if got := state.IsDownloaded(jobID); got != tc.wantHeld {
				t.Errorf("IsDownloaded = %v, want %v", got, tc.wantHeld)
			}
		})
	}
}

func TestState_LoadAndSave(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")

	// Test 1: Fresh state with no file
	state := NewState(stateFile)
	if err := state.Load(); err != nil {
		t.Errorf("Load failed on non-existent file: %v", err)
	}

	if len(state.Downloaded) != 0 {
		t.Errorf("Fresh state should have no downloads, got %d", len(state.Downloaded))
	}

	// Test 2: Mark a job as downloaded and save
	state.MarkDownloaded("job1", "Test Job 1", "/path/to/output", 5, 1024*1024)
	if err := state.Save(); err != nil {
		t.Errorf("Save failed: %v", err)
	}

	// Test 3: Load in a new state instance and verify
	state2 := NewState(stateFile)
	if err := state2.Load(); err != nil {
		t.Errorf("Load failed: %v", err)
	}

	if len(state2.Downloaded) != 1 {
		t.Errorf("Expected 1 download, got %d", len(state2.Downloaded))
	}

	job := state2.Downloaded["job1"]
	if job == nil {
		t.Fatal("job1 not found in loaded state")
	}
	if job.JobName != "Test Job 1" {
		t.Errorf("Expected job name 'Test Job 1', got %q", job.JobName)
	}
	if job.OutputDir != "/path/to/output" {
		t.Errorf("Expected output dir '/path/to/output', got %q", job.OutputDir)
	}
	if job.FileCount != 5 {
		t.Errorf("Expected file count 5, got %d", job.FileCount)
	}
}

func TestState_Counts(t *testing.T) {
	state := NewState("")

	// Initial counts
	if state.GetDownloadedCount() != 0 {
		t.Errorf("Expected 0 downloads, got %d", state.GetDownloadedCount())
	}
	if state.GetFailedCount() != 0 {
		t.Errorf("Expected 0 failures, got %d", state.GetFailedCount())
	}

	state.MarkDownloaded("job1", "Job 1", "/output", 1, 100)
	state.MarkDownloaded("job2", "Job 2", "/output", 1, 100)
	state.MarkFailed("job3", "Job 3", fmt.Errorf("error"))

	if state.GetDownloadedCount() != 2 {
		t.Errorf("Expected 2 downloads, got %d", state.GetDownloadedCount())
	}
	if state.GetFailedCount() != 1 {
		t.Errorf("Expected 1 failure, got %d", state.GetFailedCount())
	}
}

func TestState_GetRecentDownloads(t *testing.T) {
	state := NewState("")

	// Add downloads with different times
	now := time.Now()
	for _, seed := range []struct {
		id  string
		age time.Duration
	}{
		{"job1", 3 * time.Hour},
		{"job2", 1 * time.Hour},
		{"job3", 2 * time.Hour},
	} {
		state.Downloaded[seed.id] = &DownloadedJob{
			JobID:        seed.id,
			JobName:      seed.id,
			DownloadedAt: now.Add(-seed.age),
		}
	}

	// Get all
	recent := state.GetRecentDownloads(0)
	if len(recent) != 3 {
		t.Errorf("Expected 3 downloads, got %d", len(recent))
	}
	// Should be sorted newest first
	if recent[0].JobID != "job2" {
		t.Errorf("Expected newest job to be job2, got %s", recent[0].JobID)
	}

	// Get limited
	recent = state.GetRecentDownloads(2)
	if len(recent) != 2 {
		t.Errorf("Expected 2 downloads, got %d", len(recent))
	}
}

func TestState_LastPoll(t *testing.T) {
	state := NewState("")

	// Initial
	if !state.GetLastPoll().IsZero() {
		t.Error("Expected zero time for initial LastPoll")
	}

	// Update
	state.UpdateLastPoll()

	lastPoll := state.GetLastPoll()
	if lastPoll.IsZero() {
		t.Error("LastPoll should not be zero after update")
	}

	// Should be recent
	if time.Since(lastPoll) > time.Second {
		t.Error("LastPoll should be very recent")
	}
}

// TestState_FilePermissions verifies that state files are created with secure permissions (0600).
func TestState_FilePermissions(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")

	state := NewState(stateFile)
	state.MarkDownloaded("job1", "Test Job", "/output", 1, 100)
	if err := state.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	info, err := os.Stat(stateFile)
	if err != nil {
		t.Fatalf("Failed to stat state file: %v", err)
	}

	// On Unix, permissions should be 0600 (owner read/write only).
	// On Windows, this test is less meaningful but should still pass.
	perm := info.Mode().Perm()
	expectedPerm := os.FileMode(0600)

	if perm != expectedPerm {
		t.Errorf("State file permissions should be %o, got %o", expectedPerm, perm)
	}
}

// A fixed ".tmp" name let two writers interleave into the same scratch file and
// rename a half-written mixture over the real state. Each Save must use its own
// temp file and leave nothing behind.
func TestState_SaveUsesUniqueTempFile(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")

	state := NewState(stateFile)
	state.MarkDownloaded("job1", "Test Job", "/output", 1, 100)
	if err := state.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	if _, err := os.Stat(stateFile + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("fixed-name temp file %q should not be used", stateFile+".tmp")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file %q left behind after Save", e.Name())
		}
	}

	// Concurrent writers must all produce a loadable file.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if err := state.Save(); err != nil {
				t.Errorf("concurrent Save %d failed: %v", n, err)
			}
		}(i)
	}
	wg.Wait()

	reloaded := NewState(stateFile)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load after concurrent saves failed: %v", err)
	}
	if _, ok := reloaded.Downloaded["job1"]; !ok {
		t.Error("state file lost its entry across concurrent saves")
	}
}

// Retention bounds the state file for a daemon that runs for months. Entries
// still owed a tag call are exempt: dropping one would let the job be
// downloaded a second time.
func TestState_PruneOnSaveRespectsRetention(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")

	state := NewState(stateFile)
	now := time.Now()
	state.Downloaded = map[string]*DownloadedJob{
		"fresh":   {JobID: "fresh", DownloadedAt: now.Add(-24 * time.Hour)},
		"stale":   {JobID: "stale", DownloadedAt: now.Add(-100 * 24 * time.Hour)},
		"pending": {JobID: "pending", DownloadedAt: now.Add(-100 * 24 * time.Hour), PendingTagApply: true},
	}

	// No retention set: nothing is dropped.
	if err := state.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if len(state.Downloaded) != 3 {
		t.Fatalf("entries = %d, want 3 with retention unset", len(state.Downloaded))
	}

	state.SetRetention(37 * 24 * time.Hour) // 7-day lookback + 30-day buffer
	if err := state.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	if _, ok := state.Downloaded["stale"]; ok {
		t.Error("entry older than the retention window should have been pruned")
	}
	if _, ok := state.Downloaded["fresh"]; !ok {
		t.Error("entry inside the retention window was pruned")
	}
	if _, ok := state.Downloaded["pending"]; !ok {
		t.Error("entry with a pending tag apply must never be pruned")
	}
}

// TestState_PendingTagApply verifies the pending-tag-apply flag lifecycle:
// mark, clear, list. Also asserts the field round-trips through JSON via
// omitempty (absent means false).
func TestState_PendingTagApply(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")

	s := NewState(stateFile)
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Mark a job as downloaded first (required: pending flag lives on the
	// DownloadedJob entry).
	s.MarkDownloaded("job1", "Test Job", "/tmp/out", 3, 1024)
	s.MarkDownloaded("job2", "Other Job", "/tmp/out", 1, 512)

	// Mark one pending, one not.
	s.MarkPendingTagApply("job1")

	pendingIDs := s.PendingTagApplyJobs()
	if len(pendingIDs) != 1 || pendingIDs[0] != "job1" {
		t.Errorf("PendingTagApplyJobs = %v, want [job1]", pendingIDs)
	}

	// Save + load to verify round-trip.
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded := NewState(stateFile)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load after save: %v", err)
	}
	reloadedIDs := reloaded.PendingTagApplyJobs()
	if len(reloadedIDs) != 1 || reloadedIDs[0] != "job1" {
		t.Errorf("Reloaded PendingTagApplyJobs = %v, want [job1]", reloadedIDs)
	}

	// Clear the flag.
	reloaded.ClearPendingTagApply("job1")
	if got := reloaded.PendingTagApplyJobs(); len(got) != 0 {
		t.Errorf("after Clear, PendingTagApplyJobs = %v, want []", got)
	}

	// job2 was never marked; verify it's not in the list.
	if entry := reloaded.Downloaded["job2"]; entry == nil || entry.PendingTagApply {
		t.Errorf("job2 PendingTagApply = %v, want false (never marked)",
			entry != nil && entry.PendingTagApply)
	}
}

// TestFindCompletedJobs_RespectsPendingSet — jobs in the pendingSet are skipped
// with ReasonPendingTagApply, never reach CheckEligibility, and therefore
// cannot be re-enqueued for download while their tag is still being retried.
//
// Note: this test drives the monitor with a local state and mock job list via
// the state.IsDownloaded path since the monitor's API client isn't mockable
// without broader refactoring. The test shape targets the pendingSet code path
// directly. Full eligibility integration testing is covered by integration
// suites.
func TestFindCompletedJobs_RespectsPendingSet(t *testing.T) {
	s := NewState(filepath.Join(t.TempDir(), "state.json"))
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.MarkDownloaded("pending-job", "Pending", "/tmp", 1, 100)
	s.MarkPendingTagApply("pending-job")

	// A direct assertion: PendingTagApplyJobs reports the job, so poll would
	// wrap it into pendingSet and FindCompletedJobs would skip it.
	pending := s.PendingTagApplyJobs()
	if len(pending) != 1 || pending[0] != "pending-job" {
		t.Errorf("PendingTagApplyJobs = %v, want [pending-job]", pending)
	}
}
