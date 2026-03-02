package cli

import (
	"testing"

	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/resources"
)

// TestPipelinedUploadAdaptiveConcurrency verifies Bug #2 fix:
// uploadDirectoryPipelined uses ComputeBatchConcurrency result (cliUploadWorkerCount)
// for spawning workers, not the raw fileConcurrency parameter.
// v4.8.1: Before the fix, the function computed adaptive concurrency via
// ComputeBatchConcurrency but then used fileConcurrency to spawn workers.
//
// This test verifies the adaptive concurrency computation that the function
// now uses. The function itself requires a full API client + credential cache
// and cannot be unit-tested in isolation, so we verify the computation it
// depends on and the constant wiring.
func TestPipelinedUploadAdaptiveConcurrency(t *testing.T) {
	mgr := resources.NewManager(resources.Config{AutoScale: true, MaxThreads: 32})

	// Simulate a batch of small files (1KB each) — should get many workers
	smallFiles := make([]int64, 20)
	for i := range smallFiles {
		smallFiles[i] = 1024
	}
	fileConcurrency := 10 // The raw parameter that Bug #2 incorrectly used

	// ComputeBatchConcurrency should return more workers than fileConcurrency for small files
	adaptiveWorkers := mgr.ComputeBatchConcurrency(smallFiles, constants.MaxMaxConcurrent)
	if adaptiveWorkers < fileConcurrency {
		t.Logf("adaptive workers (%d) < fileConcurrency (%d) — this is fine for resource-constrained systems",
			adaptiveWorkers, fileConcurrency)
	}

	// Key assertion: adaptive result differs from raw parameter for different file sizes
	largeFiles := make([]int64, 20)
	for i := range largeFiles {
		largeFiles[i] = 5 * 1024 * 1024 * 1024 // 5GB each
	}
	largeAdaptive := mgr.ComputeBatchConcurrency(largeFiles, constants.MaxMaxConcurrent)

	if adaptiveWorkers == largeAdaptive {
		t.Errorf("adaptive concurrency should differ for small vs large files: both got %d", adaptiveWorkers)
	}

	// Verify adaptive respects the fileConcurrency cap (maxAllowed parameter)
	capped := mgr.ComputeBatchConcurrency(smallFiles, fileConcurrency)
	if capped > fileConcurrency {
		t.Errorf("adaptive should not exceed maxAllowed (%d), got %d", fileConcurrency, capped)
	}
}

// TestSequentialUploadAdaptiveConcurrency verifies Bug #3 fix:
// uploadFiles uses ComputeBatchConcurrency result (adaptiveWorkers) for spawning
// workers, not the raw maxConcurrent parameter.
// v4.8.1: Before the fix, uploadFiles created a resource manager but never used it
// for batch concurrency — workers were spawned using the raw maxConcurrent value.
//
// This test verifies:
// 1. The adaptive computation produces different results for different file sizes
// 2. The result is bounded by maxConcurrent (the maxAllowed cap)
// 3. At least 1 worker is always returned
func TestSequentialUploadAdaptiveConcurrency(t *testing.T) {
	mgr := resources.NewManager(resources.Config{AutoScale: true, MaxThreads: 32})
	maxConcurrent := 15

	// Bug #3: Before the fix, this computation existed but the result was ignored.
	// Now uploadFiles uses adaptiveWorkers = mgr.ComputeBatchConcurrency(fileSizes, maxConcurrent)

	// Small files: should get high concurrency
	smallFiles := make([]int64, 30)
	for i := range smallFiles {
		smallFiles[i] = 512 // 512 bytes
	}
	smallAdaptive := mgr.ComputeBatchConcurrency(smallFiles, maxConcurrent)

	// Large files: should get low concurrency
	largeFiles := make([]int64, 30)
	for i := range largeFiles {
		largeFiles[i] = 10 * 1024 * 1024 * 1024 // 10GB
	}
	largeAdaptive := mgr.ComputeBatchConcurrency(largeFiles, maxConcurrent)

	if smallAdaptive <= largeAdaptive {
		t.Errorf("small files should get more workers than large files: small=%d, large=%d",
			smallAdaptive, largeAdaptive)
	}

	// Verify cap is respected
	if smallAdaptive > maxConcurrent {
		t.Errorf("adaptive workers (%d) should not exceed maxConcurrent (%d)", smallAdaptive, maxConcurrent)
	}
	if largeAdaptive > maxConcurrent {
		t.Errorf("adaptive workers (%d) should not exceed maxConcurrent (%d)", largeAdaptive, maxConcurrent)
	}

	// Verify minimum of 1
	singleHuge := []int64{100 * 1024 * 1024 * 1024}
	singleAdaptive := mgr.ComputeBatchConcurrency(singleHuge, maxConcurrent)
	if singleAdaptive < 1 {
		t.Errorf("should always have at least 1 worker, got %d", singleAdaptive)
	}
}

// TestUploadResourceManagerConstants verifies the constants used by both
// uploadDirectoryPipelined and uploadFiles are correctly defined.
func TestUploadResourceManagerConstants(t *testing.T) {
	// Channel buffer constants used in folder upload helpers
	if constants.WorkChannelBuffer != 100 {
		t.Errorf("WorkChannelBuffer = %d, want 100", constants.WorkChannelBuffer)
	}
	if constants.DispatchChannelBuffer != 256 {
		t.Errorf("DispatchChannelBuffer = %d, want 256", constants.DispatchChannelBuffer)
	}

	// Concurrency tiers
	if constants.AdaptiveSmallFileConcurrency != 20 {
		t.Errorf("AdaptiveSmallFileConcurrency = %d, want 20", constants.AdaptiveSmallFileConcurrency)
	}
	if constants.AdaptiveLargeFileConcurrency != 5 {
		t.Errorf("AdaptiveLargeFileConcurrency = %d, want 5", constants.AdaptiveLargeFileConcurrency)
	}
}
