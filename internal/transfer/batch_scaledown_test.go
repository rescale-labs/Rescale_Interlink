package transfer

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestRunBatchFromChannel_ScalesDown covers F22. A stream that starts with small
// files scales up, and the large files sampled later reduce the target — but
// every worker already running kept taking items until the input closed, so the
// large-file phase ran at the small-file concurrency. Only the advertised count
// came down.
func TestRunBatchFromChannel_ScalesDown(t *testing.T) {
	const (
		smallFiles = 20             // enough to trigger the first sample
		largeFiles = 200            // enough to resample and then run a while
		largeSize  = int64(2) << 30 // 2 GiB: the large-file tier
		smallSize  = int64(1024)    // the small-file tier
		maxWorkers = 8              // what the small files scale up to
		settle     = 3 * maxWorkers // items the retiring workers may still run
		itemWork   = 2 * time.Millisecond
	)

	ch := make(chan testItem, smallFiles+largeFiles)
	for i := 0; i < smallFiles; i++ {
		ch <- testItem{size: smallSize, id: "small"}
	}
	for i := 0; i < largeFiles; i++ {
		ch <- testItem{size: largeSize, id: "large"}
	}
	close(ch)

	var adaptive *AdaptiveWorkerCount
	cfg := batchCfg(maxWorkers, "SCALEDOWN")
	cfg.AdaptiveCount = &adaptive

	var (
		running   atomic.Int32
		executed  atomic.Int32
		peakLate  atomic.Int32
		peakEarly atomic.Int32
	)
	execute := func(context.Context, testItem) error {
		concurrent := running.Add(1)
		defer running.Add(-1)

		// Workers retire between items, so the transfers already in flight when
		// the target drops still finish at the old concurrency. Those are the
		// settle window; what matters is the steady state after it.
		if executed.Add(1) > settle {
			for {
				peak := peakLate.Load()
				if concurrent <= peak || peakLate.CompareAndSwap(peak, concurrent) {
					break
				}
			}
		} else {
			for {
				peak := peakEarly.Load()
				if concurrent <= peak || peakEarly.CompareAndSwap(peak, concurrent) {
					break
				}
			}
		}

		time.Sleep(itemWork)
		return nil
	}

	result, adaptiveOut := RunBatchFromChannel(context.Background(), ch, cfg, execute)

	if result.Completed != smallFiles+largeFiles {
		t.Fatalf("completed %d of %d items", result.Completed, smallFiles+largeFiles)
	}

	target := adaptiveOut.Load()
	if target >= maxWorkers {
		t.Fatalf("adaptive target is %d — the large files did not reduce it, so this "+
			"test is not exercising a scale-down", target)
	}
	if got := peakLate.Load(); int(got) > target {
		t.Errorf("%d transfers ran at once after the target fell to %d (peak before the "+
			"target changed: %d)", got, target, peakEarly.Load())
	}
}
