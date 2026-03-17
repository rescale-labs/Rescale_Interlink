package daemon

import (
	"testing"
	"time"
)

func TestDaemonTransferTracker_BasicLifecycle(t *testing.T) {
	tracker := NewDaemonTransferTracker()

	// Start a batch
	tracker.StartBatch("job1", "Test Job", 5, 5000)

	// Verify active batch
	status := tracker.GetStatus()
	if len(status) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(status))
	}
	if status[0].BatchID != "job1" {
		t.Errorf("expected batchID 'job1', got '%s'", status[0].BatchID)
	}
	if status[0].BatchLabel != "Auto: Test Job" {
		t.Errorf("expected batchLabel 'Auto: Test Job', got '%s'", status[0].BatchLabel)
	}
	if status[0].Total != 5 {
		t.Errorf("expected total 5, got %d", status[0].Total)
	}
	if status[0].TotalBytes != 5000 {
		t.Errorf("expected totalBytes 5000, got %d", status[0].TotalBytes)
	}

	// Complete some files
	tracker.CompleteFile("job1", 1000)
	tracker.CompleteFile("job1", 1500)

	status = tracker.GetStatus()
	if status[0].Completed != 2 {
		t.Errorf("expected 2 completed, got %d", status[0].Completed)
	}
	if status[0].BytesDone != 2500 {
		t.Errorf("expected 2500 bytesDone, got %d", status[0].BytesDone)
	}

	// Fail a file
	tracker.FailFile("job1", 500)
	status = tracker.GetStatus()
	if status[0].Failed != 1 {
		t.Errorf("expected 1 failed, got %d", status[0].Failed)
	}

	// Skip a file (invalid filename)
	tracker.SkipFile("job1", 1000)
	status = tracker.GetStatus()
	if status[0].Total != 4 {
		t.Errorf("expected total 4 after skip, got %d", status[0].Total)
	}
	if status[0].TotalBytes != 4000 {
		t.Errorf("expected totalBytes 4000 after skip, got %d", status[0].TotalBytes)
	}

	// Complete remaining
	tracker.CompleteFile("job1", 1000)
	status = tracker.GetStatus()
	if status[0].Completed != 3 {
		t.Errorf("expected 3 completed, got %d", status[0].Completed)
	}

	// Finalize
	tracker.FinalizeBatch("job1")

	status = tracker.GetStatus()
	if len(status) != 1 {
		t.Fatalf("expected 1 batch in recent, got %d", len(status))
	}
	if status[0].CompletedAt.IsZero() {
		t.Error("expected non-zero CompletedAt after finalization")
	}
}

func TestDaemonTransferTracker_SkipDecrementsBoth(t *testing.T) {
	tracker := NewDaemonTransferTracker()
	tracker.StartBatch("job1", "Skip Test", 10, 10000)

	tracker.SkipFile("job1", 1000)
	tracker.SkipFile("job1", 2000)

	status := tracker.GetStatus()
	if status[0].Total != 8 {
		t.Errorf("expected total 8, got %d", status[0].Total)
	}
	if status[0].TotalBytes != 7000 {
		t.Errorf("expected totalBytes 7000, got %d", status[0].TotalBytes)
	}
}

func TestDaemonTransferTracker_SkipNeverGoesNegative(t *testing.T) {
	tracker := NewDaemonTransferTracker()
	tracker.StartBatch("job1", "Negative Test", 1, 100)

	// Skip more than total
	tracker.SkipFile("job1", 100)
	tracker.SkipFile("job1", 100) // extra skip

	status := tracker.GetStatus()
	if status[0].Total < 0 {
		t.Errorf("total should not be negative, got %d", status[0].Total)
	}
	if status[0].TotalBytes < 0 {
		t.Errorf("totalBytes should not be negative, got %d", status[0].TotalBytes)
	}
}

func TestDaemonTransferTracker_MultipleBatches(t *testing.T) {
	tracker := NewDaemonTransferTracker()

	tracker.StartBatch("job1", "Job 1", 3, 3000)
	tracker.StartBatch("job2", "Job 2", 5, 5000)

	status := tracker.GetStatus()
	if len(status) != 2 {
		t.Fatalf("expected 2 active batches, got %d", len(status))
	}

	tracker.CompleteFile("job1", 1000)
	tracker.FinalizeBatch("job1")

	// Job1 should be in recent, job2 still active
	status = tracker.GetStatus()
	if len(status) != 2 {
		t.Fatalf("expected 2 total batches (1 active + 1 recent), got %d", len(status))
	}
}

func TestDaemonTransferTracker_RecentHistoryCapped(t *testing.T) {
	tracker := NewDaemonTransferTracker()
	tracker.maxRecent = 3

	for i := 0; i < 5; i++ {
		id := "job" + string(rune('A'+i))
		tracker.StartBatch(id, "Job "+id, 1, 100)
		tracker.CompleteFile(id, 100)
		tracker.FinalizeBatch(id)
	}

	status := tracker.GetStatus()
	if len(status) != 3 {
		t.Errorf("expected 3 recent batches (capped), got %d", len(status))
	}
}

func TestDaemonTransferTracker_FinalizeSetsClearsActive(t *testing.T) {
	tracker := NewDaemonTransferTracker()
	tracker.StartBatch("job1", "Test", 2, 2000)

	// Simulate in-progress file
	tracker.UpdateFileProgress("job1", 1000, 0.5)

	status := tracker.GetStatus()
	if status[0].Active != 1 {
		t.Errorf("expected active=1 during progress, got %d", status[0].Active)
	}

	tracker.FinalizeBatch("job1")

	status = tracker.GetStatus()
	if status[0].Active != 0 {
		t.Errorf("expected active=0 after finalize, got %d", status[0].Active)
	}
	if status[0].Speed != 0 {
		t.Errorf("expected speed=0 after finalize, got %f", status[0].Speed)
	}
}

func TestDaemonTransferTracker_FinalizeMissingBatch(t *testing.T) {
	tracker := NewDaemonTransferTracker()

	// Should not panic
	tracker.FinalizeBatch("nonexistent")
	tracker.CompleteFile("nonexistent", 100)
	tracker.FailFile("nonexistent", 100)
	tracker.SkipFile("nonexistent", 100)
	tracker.UpdateFileProgress("nonexistent", 100, 0.5)
}

func TestDaemonTransferTracker_BytesDoneIncludesPartialProgress(t *testing.T) {
	tracker := NewDaemonTransferTracker()
	tracker.StartBatch("job1", "Partial Test", 3, 3000)

	// Complete first file (1000 bytes)
	tracker.CompleteFile("job1", 1000)

	// Start downloading second file (1000 bytes), 50% done
	tracker.UpdateFileProgress("job1", 1000, 0.5)

	status := tracker.GetStatus()
	// BytesDone should be: 1000 (completed) + 500 (50% of 1000) = 1500
	if status[0].BytesDone != 1500 {
		t.Errorf("expected BytesDone=1500 (1000 completed + 500 partial), got %d", status[0].BytesDone)
	}

	// Complete the second file
	tracker.CompleteFile("job1", 1000)

	status = tracker.GetStatus()
	// BytesDone should be: 2000 (2 completed files), partial cleared
	if status[0].BytesDone != 2000 {
		t.Errorf("expected BytesDone=2000 after completing second file, got %d", status[0].BytesDone)
	}
}

func TestDaemonTransferTracker_StartedAtIsSet(t *testing.T) {
	tracker := NewDaemonTransferTracker()
	before := time.Now()
	tracker.StartBatch("job1", "Test", 1, 100)
	after := time.Now()

	status := tracker.GetStatus()
	if status[0].StartedAt.Before(before) || status[0].StartedAt.After(after) {
		t.Error("StartedAt should be between before and after timestamps")
	}
}
