// Package daemon tests
// Version: 3.4.0
// Date: December 2025
package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestState_LoadAndSave(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "daemon-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	stateFile := filepath.Join(tmpDir, "state.json")

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

func TestState_IsDownloaded(t *testing.T) {
	state := NewState("")
	state.Downloaded = make(map[string]*DownloadedJob)

	// Test 1: Not downloaded
	if state.IsDownloaded("job1") {
		t.Error("job1 should not be marked as downloaded")
	}

	// Test 2: Successfully downloaded
	state.MarkDownloaded("job1", "Test Job 1", "/output", 1, 100)
	if !state.IsDownloaded("job1") {
		t.Error("job1 should be marked as downloaded")
	}

	// Test 3: Failed download should not be considered "downloaded"
	state.MarkFailed("job2", "Test Job 2", fmt.Errorf("network error"))
	if state.IsDownloaded("job2") {
		t.Error("job2 should not be considered downloaded (failed)")
	}
}

func TestState_MarkFailed(t *testing.T) {
	state := NewState("")
	state.Downloaded = make(map[string]*DownloadedJob)

	state.MarkFailed("job1", "Test Job 1", fmt.Errorf("connection timeout"))

	job := state.Downloaded["job1"]
	if job == nil {
		t.Fatal("job1 not found")
	}
	if job.Error == "" {
		t.Error("Expected error to be set")
	}
	if job.Error != "connection timeout" {
		t.Errorf("Expected error 'connection timeout', got %q", job.Error)
	}
}

func TestState_ClearFailed(t *testing.T) {
	state := NewState("")
	state.Downloaded = make(map[string]*DownloadedJob)

	// Mark job as failed
	state.MarkFailed("job1", "Test Job 1", fmt.Errorf("error"))

	// Clear failed status
	state.ClearFailed("job1")

	// Verify it's gone
	if _, exists := state.Downloaded["job1"]; exists {
		t.Error("job1 should have been removed after ClearFailed")
	}

	// Clearing successfully downloaded job should not remove it
	state.MarkDownloaded("job2", "Test Job 2", "/output", 1, 100)
	state.ClearFailed("job2")
	if _, exists := state.Downloaded["job2"]; !exists {
		t.Error("job2 should still exist (it wasn't failed)")
	}
}

func TestState_Counts(t *testing.T) {
	state := NewState("")
	state.Downloaded = make(map[string]*DownloadedJob)

	// Initial counts
	if state.GetDownloadedCount() != 0 {
		t.Errorf("Expected 0 downloads, got %d", state.GetDownloadedCount())
	}
	if state.GetFailedCount() != 0 {
		t.Errorf("Expected 0 failures, got %d", state.GetFailedCount())
	}

	// Add some downloads
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
	state.Downloaded = make(map[string]*DownloadedJob)

	// Add downloads with different times
	now := time.Now()
	state.Downloaded["job1"] = &DownloadedJob{
		JobID:        "job1",
		JobName:      "Job 1",
		DownloadedAt: now.Add(-3 * time.Hour),
	}
	state.Downloaded["job2"] = &DownloadedJob{
		JobID:        "job2",
		JobName:      "Job 2",
		DownloadedAt: now.Add(-1 * time.Hour),
	}
	state.Downloaded["job3"] = &DownloadedJob{
		JobID:        "job3",
		JobName:      "Job 3",
		DownloadedAt: now.Add(-2 * time.Hour),
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
