package transfer

import (
	"context"
	"log"
	"sync"
	"sync/atomic"

	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/resources"
)

// WorkItem must provide FileSize() for adaptive concurrency computation.
type WorkItem interface {
	FileSize() int64
}

// BatchConfig controls how RunBatch/RunBatchFromChannel dispatch work.
type BatchConfig struct {
	// MaxWorkers is the upper bound on concurrent workers.
	// Typically from CLI --max-concurrent flag or semaphore cap.
	MaxWorkers int

	// ResourceMgr computes adaptive concurrency; required.
	ResourceMgr *resources.Manager

	// Label for logging ("UPLOAD", "DOWNLOAD").
	Label string

	// ForceSequential forces 1 worker (daemon mode).
	ForceSequential bool

	// ScaleCheckHook is called after each scaling decision in RunBatchFromChannel.
	// Tests can use this to force deterministic scaling checks instead of relying
	// on item-count thresholds that race with goroutine scheduling.
	// If nil, no hook is called.
	ScaleCheckHook func()
}

// BatchResult tracks the outcome of a batch execution.
type BatchResult struct {
	Completed int
	Failed    int
	Skipped   int
	Errors    []error
}

// ExecuteFunc is the per-item execution function called by RunBatch/RunBatchFromChannel.
type ExecuteFunc[T WorkItem] func(ctx context.Context, item T) error

// RunBatch executes items with adaptive-concurrency worker pool.
// Extracts file sizes → ComputeBatchConcurrency → spawns workers.
// Returns after all items have been processed.
func RunBatch[T WorkItem](ctx context.Context, items []T, cfg BatchConfig, execute ExecuteFunc[T]) *BatchResult {
	if len(items) == 0 {
		return &BatchResult{}
	}

	if cfg.ResourceMgr == nil {
		panic("transfer.RunBatch: ResourceMgr is required")
	}

	// Compute adaptive concurrency from file sizes.
	numWorkers := computeWorkers(items, cfg)

	log.Printf("[BATCH] %s: %d items, %d adaptive workers (max %d)",
		cfg.Label, len(items), numWorkers, cfg.MaxWorkers)

	// Feed items into a buffered work channel.
	work := make(chan T, len(items))
	for _, item := range items {
		work <- item
	}
	close(work)

	// Track results.
	var (
		completed atomic.Int32
		failed    atomic.Int32
		errMu     sync.Mutex
		errs      []error
	)

	// Spawn workers.
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range work {
				if ctx.Err() != nil {
					return
				}
				if err := execute(ctx, item); err != nil {
					failed.Add(1)
					errMu.Lock()
					errs = append(errs, err)
					errMu.Unlock()
				} else {
					completed.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	return &BatchResult{
		Completed: int(completed.Load()),
		Failed:    int(failed.Load()),
		Errors:    errs,
	}
}

// AdaptiveWorkerCount exposes the current adaptive worker count for
// RunBatchFromChannel. Execute closures read this to pass the correct
// totalFiles to AllocateTransfer, ensuring per-file thread allocation
// uses the current adaptive target rather than a stale initial value.
type AdaptiveWorkerCount struct {
	value atomic.Int32
}

// Load returns the current adaptive worker count.
func (a *AdaptiveWorkerCount) Load() int {
	return int(a.value.Load())
}

// RunBatchFromChannel consumes items from a channel (streaming mode).
//
// Dynamic worker scaling behavior:
//   - Initial workers: min(MaxWorkers, DefaultMaxConcurrent=5)
//   - Sample size: first 20 items consumed (or all items if channel closes before 20)
//   - After sampling: compute adaptive target via ComputeBatchConcurrency on sampled sizes
//   - Scale-up: spawn additional workers immediately (up to adaptive target)
//   - Scale-down: excess workers drain naturally (no kill, just don't refill)
//   - Resample cadence: every 50 additional items
//   - Fallback when FileSize()==0: treat as small file (use DefaultMaxConcurrent)
//   - Max scale-up per interval: double current workers (capped at MaxWorkers)
//   - Current adaptive target exposed via AdaptiveWorkerCount for AllocateTransfer calls
func RunBatchFromChannel[T WorkItem](ctx context.Context, ch <-chan T, cfg BatchConfig, execute ExecuteFunc[T]) (*BatchResult, *AdaptiveWorkerCount) {
	if cfg.ResourceMgr == nil {
		panic("transfer.RunBatchFromChannel: ResourceMgr is required")
	}

	maxWorkers := cfg.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = constants.MaxMaxConcurrent
	}

	initialWorkers := constants.DefaultMaxConcurrent
	if cfg.ForceSequential {
		initialWorkers = 1
	}
	if initialWorkers > maxWorkers {
		initialWorkers = maxWorkers
	}

	adaptive := &AdaptiveWorkerCount{}
	adaptive.value.Store(int32(initialWorkers))

	log.Printf("[BATCH] %s: streaming mode, initial %d workers (max %d)",
		cfg.Label, initialWorkers, maxWorkers)

	// Internal dispatch channel — workers consume from this.
	dispatch := make(chan T, constants.DispatchChannelBuffer)

	var (
		completed atomic.Int32
		failed    atomic.Int32
		errMu     sync.Mutex
		errs      []error
	)

	// Active worker count for scaling decisions.
	var activeWorkers atomic.Int32
	activeWorkers.Store(int32(initialWorkers))

	// WaitGroup tracks all workers.
	var workerWg sync.WaitGroup

	// Worker function.
	workerFn := func() {
		defer workerWg.Done()
		for item := range dispatch {
			if ctx.Err() != nil {
				return
			}
			if err := execute(ctx, item); err != nil {
				failed.Add(1)
				errMu.Lock()
				errs = append(errs, err)
				errMu.Unlock()
			} else {
				completed.Add(1)
			}
		}
	}

	// Spawn initial workers.
	for i := 0; i < initialWorkers; i++ {
		workerWg.Add(1)
		go workerFn()
	}

	// spawnMore adds additional workers up to the given target.
	// No-op in ForceSequential mode.
	spawnMore := func(target int) {
		if cfg.ForceSequential {
			return
		}
		current := int(activeWorkers.Load())
		if target <= current {
			return
		}
		toSpawn := target - current
		activeWorkers.Store(int32(target))
		adaptive.value.Store(int32(target))
		for i := 0; i < toSpawn; i++ {
			workerWg.Add(1)
			go workerFn()
		}
		log.Printf("[BATCH] %s: scaled %d → %d workers", cfg.Label, current, target)
	}

	// Dispatcher goroutine: reads from input channel, samples file sizes,
	// and dispatches to workers with periodic rescaling.
	go func() {
		defer close(dispatch)

		const sampleSize = 20
		const resampleInterval = 50

		var sampled []int64
		itemsSinceSample := 0

		for item := range ch {
			if ctx.Err() != nil {
				return
			}

			// Collect file size for sampling.
			sampled = append(sampled, item.FileSize())
			itemsSinceSample++

			// Dispatch to workers.
			select {
			case dispatch <- item:
			case <-ctx.Done():
				return
			}

			// Check if we should rescale.
			shouldScale := false
			if len(sampled) == sampleSize && itemsSinceSample == sampleSize {
				// First sample complete.
				shouldScale = true
			} else if itemsSinceSample >= resampleInterval {
				// Periodic resample.
				shouldScale = true
			}

			if shouldScale {
				target := cfg.ResourceMgr.ComputeBatchConcurrency(sampled, maxWorkers)
				current := int(activeWorkers.Load())

				// Cap scale-up to 2x current.
				maxTarget := current * 2
				if maxTarget > maxWorkers {
					maxTarget = maxWorkers
				}
				if target > maxTarget {
					target = maxTarget
				}

				if target > current {
					spawnMore(target)
				} else if target < current {
					// Scale-down: just update the adaptive count;
					// workers drain naturally when dispatch channel closes.
					adaptive.value.Store(int32(target))
					log.Printf("[BATCH] %s: adaptive target reduced to %d (active workers: %d, will drain)",
						cfg.Label, target, current)
				}

				itemsSinceSample = 0

				if cfg.ScaleCheckHook != nil {
					cfg.ScaleCheckHook()
				}
			}
		}

		// Channel closed — if we never hit sample size, still compute adaptive
		// from whatever we collected.
		if len(sampled) > 0 && len(sampled) < sampleSize {
			target := cfg.ResourceMgr.ComputeBatchConcurrency(sampled, maxWorkers)
			current := int(activeWorkers.Load())
			if target > current && target <= maxWorkers {
				spawnMore(target)
			} else {
				adaptive.value.Store(int32(target))
			}

			if cfg.ScaleCheckHook != nil {
				cfg.ScaleCheckHook()
			}
		}
	}()

	// Wait for all workers to complete.
	workerWg.Wait()

	return &BatchResult{
		Completed: int(completed.Load()),
		Failed:    int(failed.Load()),
		Errors:    errs,
	}, adaptive
}

// CollectErrors drains an error channel into a slice.
// Replaces 4+ inline patterns across the codebase.
func CollectErrors(errChan <-chan error) []error {
	var errs []error
	for err := range errChan {
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// computeWorkers computes the adaptive worker count for a known set of items.
func computeWorkers[T WorkItem](items []T, cfg BatchConfig) int {
	if cfg.ForceSequential {
		return 1
	}

	maxWorkers := cfg.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = constants.MaxMaxConcurrent
	}

	sizes := make([]int64, len(items))
	for i, item := range items {
		sizes[i] = item.FileSize()
	}

	numWorkers := cfg.ResourceMgr.ComputeBatchConcurrency(sizes, maxWorkers)

	if numWorkers > len(items) {
		numWorkers = len(items)
	}
	if numWorkers < 1 {
		numWorkers = 1
	}

	return numWorkers
}

// ComputedWorkers returns the adaptive worker count that RunBatch would use
// for the given items and config, without actually executing anything.
// Useful for callers that need the worker count before batch execution
// (e.g., to pass to AllocateTransfer).
func ComputedWorkers[T WorkItem](items []T, cfg BatchConfig) int {
	if len(items) == 0 {
		return 1
	}
	return computeWorkers(items, cfg)
}
