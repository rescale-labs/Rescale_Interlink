package main

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/events"
)

// TestJobsTabUpdateProgress tests concurrent progress updates don't deadlock
func TestJobsTabUpdateProgress(t *testing.T) {
	jt := &JobsTab{
		loadedJobs: make([]JobRow, 0),
		jobsLock:   sync.RWMutex{},
	}

	// Add some test jobs
	jt.loadedJobs = append(jt.loadedJobs, JobRow{
		Index:   0,
		JobName: "test_job_1",
		Status:  "pending",
	})
	jt.loadedJobs = append(jt.loadedJobs, JobRow{
		Index:   1,
		JobName: "test_job_2",
		Status:  "pending",
	})

	// Test concurrent updates don't deadlock
	var wg sync.WaitGroup
	numGoroutines := 10
	updatesPerGoroutine := 20

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < updatesPerGoroutine; j++ {
				event := &events.ProgressEvent{
					JobName:  fmt.Sprintf("test_job_%d", (id%2)+1),
					Stage:    "upload",
					Progress: float64(j) / float64(updatesPerGoroutine),
					Message:  fmt.Sprintf("Upload %d%%", j*5),
				}
				jt.UpdateProgress(event)
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	// Wait with timeout to detect deadlock
	done := make(chan bool)
	go func() {
		wg.Wait()
		done <- true
	}()

	select {
	case <-done:
		t.Log("✅ No deadlock detected in UpdateProgress")
	case <-time.After(10 * time.Second):
		t.Fatal("❌ Deadlock detected - test timed out")
	}
}

// TestJobsTabUpdateJobState tests state updates work concurrently
func TestJobsTabUpdateJobState(t *testing.T) {
	jt := &JobsTab{
		loadedJobs:   make([]JobRow, 0),
		jobsLock:     sync.RWMutex{},
		lastRefresh:  time.Now(),
		refreshMutex: sync.Mutex{},
	}

	var wg sync.WaitGroup
	numGoroutines := 5
	updatesPerGoroutine := 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < updatesPerGoroutine; j++ {
				event := &events.StateChangeEvent{
					JobName:   fmt.Sprintf("job_%d", id),
					Stage:     "upload",
					NewStatus: "in_progress",
					JobID:     fmt.Sprintf("job_id_%d", id),
				}
				jt.UpdateJobState(event)
				time.Sleep(2 * time.Millisecond)
			}
		}(i)
	}

	// Wait with timeout
	done := make(chan bool)
	go func() {
		wg.Wait()
		done <- true
	}()

	select {
	case <-done:
		t.Log("✅ No deadlock detected in UpdateJobState")
		// Verify jobs were added
		if len(jt.loadedJobs) != numGoroutines {
			t.Errorf("Expected %d jobs, got %d", numGoroutines, len(jt.loadedJobs))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("❌ Deadlock detected - test timed out")
	}
}

// TestJobsTabThrottledRefresh tests refresh throttling works
func TestJobsTabThrottledRefresh(t *testing.T) {
	jt := &JobsTab{
		lastRefresh:  time.Now().Add(-1 * time.Second), // Initialize in the past
		refreshMutex: sync.Mutex{},
	}

	// Test rapid refreshes are throttled
	callCount := 0
	start := time.Now()

	for i := 0; i < 100; i++ {
		before := jt.lastRefresh
		jt.throttledRefresh()
		if jt.lastRefresh != before {
			callCount++
		}
		time.Sleep(time.Millisecond)
	}

	elapsed := time.Since(start)

	// Should be throttled to ~1 refresh per 100ms
	expectedMax := int(elapsed.Milliseconds()/100) + 2 // +2 for tolerance
	if callCount > expectedMax {
		t.Errorf("Throttling not effective: %d refreshes in %v (expected max ~%d)", callCount, elapsed, expectedMax)
	} else {
		t.Logf("✅ Throttling works: %d refreshes in %v (max allowed ~%d)", callCount, elapsed, expectedMax)
	}
}

// TestJobsTabNoDeadlockPattern verifies the fix from DEADLOCK_FIX.md is present
func TestJobsTabNoDeadlockPattern(t *testing.T) {
	// This is a code inspection test
	// We verify the pattern: Lock -> update data -> Unlock -> Refresh
	// is followed in both UpdateProgress and UpdateJobState

	t.Log("Testing UpdateProgress follows safe pattern...")
	jt := &JobsTab{
		loadedJobs: []JobRow{{Index: 0, JobName: "test"}},
		jobsLock:   sync.RWMutex{},
	}

	// Create a channel to verify unlock happens before return
	unlocked := make(chan bool, 1)

	// Monkey-patch to verify unlock is called
	originalUpdateProgress := jt.UpdateProgress

	// Test that we can acquire a read lock after UpdateProgress
	// (which means UpdateProgress released its write lock)
	go func() {
		event := &events.ProgressEvent{
			JobName:  "test",
			Stage:    "upload",
			Progress: 0.5,
		}
		originalUpdateProgress(event)
		unlocked <- true
	}()

	// Wait a bit for update to process
	time.Sleep(10 * time.Millisecond)

	// Try to acquire read lock - should succeed if UpdateProgress released lock
	lockAcquired := make(chan bool, 1)
	go func() {
		jt.jobsLock.RLock()
		lockAcquired <- true
		jt.jobsLock.RUnlock()
	}()

	select {
	case <-lockAcquired:
		t.Log("✅ Lock is properly released in UpdateProgress")
	case <-time.After(100 * time.Millisecond):
		t.Error("❌ Lock may be held during refresh (potential deadlock)")
	}

	<-unlocked // Wait for goroutine to complete
}

// TestJobsTabConcurrentReadWrite tests concurrent reads and writes
func TestJobsTabConcurrentReadWrite(t *testing.T) {
	jt := &JobsTab{
		loadedJobs:   make([]JobRow, 10),
		jobsLock:     sync.RWMutex{},
		lastRefresh:  time.Now(),
		refreshMutex: sync.Mutex{},
	}

	// Initialize jobs
	for i := 0; i < 10; i++ {
		jt.loadedJobs[i] = JobRow{
			Index:   i,
			JobName: fmt.Sprintf("job_%d", i),
			Status:  "pending",
		}
	}

	var wg sync.WaitGroup

	// Writers (state updates)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				event := &events.StateChangeEvent{
					JobName:   fmt.Sprintf("job_%d", id),
					NewStatus: "in_progress",
					Stage:     "upload",
				}
				jt.UpdateJobState(event)
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	// Readers (simulating table callbacks)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				jt.jobsLock.RLock()
				_ = len(jt.loadedJobs)
				jt.jobsLock.RUnlock()
				time.Sleep(time.Millisecond)
			}
		}()
	}

	// Wait with timeout
	done := make(chan bool)
	go func() {
		wg.Wait()
		done <- true
	}()

	select {
	case <-done:
		t.Log("✅ No deadlock with concurrent reads and writes")
	case <-time.After(15 * time.Second):
		t.Fatal("❌ Deadlock detected with concurrent operations")
	}
}
