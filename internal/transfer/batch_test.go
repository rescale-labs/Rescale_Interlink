package transfer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/resources"
)

// testItem implements WorkItem for testing.
type testItem struct {
	size int64
	id   string
}

func (t testItem) FileSize() int64 { return t.size }

func newTestResourceMgr() *resources.Manager {
	return resources.NewManager(resources.Config{
		AutoScale:  true,
		MaxThreads: 32,
	})
}

// batchCfg is the config every batch test starts from; rows add their own knobs.
func batchCfg(maxWorkers int, label string) BatchConfig {
	return BatchConfig{
		MaxWorkers:  maxWorkers,
		ResourceMgr: newTestResourceMgr(),
		Label:       label,
	}
}

// sizedItems returns n work items of the given size.
func sizedItems(n int, size int64) []testItem {
	items := make([]testItem, n)
	for i := range items {
		items[i] = testItem{size: size, id: "file"}
	}
	return items
}

// filledChan returns a closed channel already holding n items of the given size.
func filledChan(n int, size int64) chan testItem {
	ch := make(chan testItem, n+1)
	for _, item := range sizedItems(n, size) {
		ch <- item
	}
	close(ch)
	return ch
}

// noopExec is the execute func for rows that only care about the batch result.
func noopExec(context.Context, testItem) error { return nil }

// trackConcurrency records the highest number of executions running at once.
func trackConcurrency(peak *atomic.Int32) func(context.Context, testItem) error {
	var current atomic.Int32
	return func(context.Context, testItem) error {
		c := current.Add(1)
		for {
			prev := peak.Load()
			if c <= prev || peak.CompareAndSwap(prev, c) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		current.Add(-1)
		return nil
	}
}

// TestRunBatch runs a slice of work items to completion. Rows differ only in
// the batch size, the item size and whether the executor fails.
func TestRunBatch(t *testing.T) {
	errTest := errors.New("test error")

	tests := []struct {
		name          string
		maxWorkers    int
		items         int
		size          int64
		execErr       error
		wantCompleted int
		wantFailed    int
	}{
		{name: "empty batch", maxWorkers: 10, items: 0},
		{name: "single item", maxWorkers: 10, items: 1, size: 1024, wantCompleted: 1},
		{name: "all succeed", maxWorkers: 5, items: 20, size: 1024 * 1024, wantCompleted: 20},
		{name: "all fail", maxWorkers: 5, items: 10, size: 1024, execErr: errTest, wantFailed: 10},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var executed atomic.Int32
			result := RunBatch(context.Background(), sizedItems(tc.items, tc.size),
				batchCfg(tc.maxWorkers, "TEST"),
				func(context.Context, testItem) error {
					executed.Add(1)
					return tc.execErr
				})

			if result.Completed != tc.wantCompleted {
				t.Errorf("Completed = %d, want %d", result.Completed, tc.wantCompleted)
			}
			if result.Failed != tc.wantFailed {
				t.Errorf("Failed = %d, want %d", result.Failed, tc.wantFailed)
			}
			if len(result.Errors) != tc.wantFailed {
				t.Errorf("len(Errors) = %d, want %d", len(result.Errors), tc.wantFailed)
			}
			for _, err := range result.Errors {
				if !errors.Is(err, tc.execErr) {
					t.Errorf("expected %v, got %v", tc.execErr, err)
				}
			}
			// Covers "execute must not be called for an empty batch" too.
			if int(executed.Load()) != tc.items {
				t.Errorf("executed %d times, want %d", executed.Load(), tc.items)
			}
		})
	}
}

// TestRunBatch_ComputedWorkers pins adaptive concurrency to file size: big
// files get few workers, small files get many.
func TestRunBatch_ComputedWorkers(t *testing.T) {
	tests := []struct {
		name        string
		maxWorkers  int
		items       int
		size        int64
		wantAtLeast int
		wantAtMost  int // 0: no upper bound asserted
	}{
		{
			name: "large files get few workers", maxWorkers: 20, items: 10,
			size: 2 * 1024 * 1024 * 1024, wantAtLeast: 1, wantAtMost: 5,
		},
		{
			name: "small files get many workers", maxWorkers: 20, items: 30,
			size: 1024 * 1024, wantAtLeast: 5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workers := ComputedWorkers(sizedItems(tc.items, tc.size), batchCfg(tc.maxWorkers, "TEST"))
			if workers < tc.wantAtLeast {
				t.Errorf("ComputedWorkers = %d, want >= %d", workers, tc.wantAtLeast)
			}
			if tc.wantAtMost > 0 && workers > tc.wantAtMost {
				t.Errorf("ComputedWorkers = %d, want <= %d", workers, tc.wantAtMost)
			}
		})
	}
}

// TestRunBatch_Concurrency pins the observed parallelism: never above
// MaxWorkers, and exactly one at a time under ForceSequential.
func TestRunBatch_Concurrency(t *testing.T) {
	tests := []struct {
		name           string
		maxWorkers     int
		items          int
		sequential     bool
		wantPeakAtMost int32
	}{
		{name: "force sequential", maxWorkers: 10, items: 5, sequential: true, wantPeakAtMost: 1},
		{name: "never exceeds max workers", maxWorkers: 3, items: 50, wantPeakAtMost: 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := batchCfg(tc.maxWorkers, "TEST")
			cfg.ForceSequential = tc.sequential

			var peak atomic.Int32
			result := RunBatch(context.Background(), sizedItems(tc.items, 1024), cfg,
				trackConcurrency(&peak))

			if result.Completed != tc.items {
				t.Errorf("Completed = %d, want %d", result.Completed, tc.items)
			}
			if peak.Load() > tc.wantPeakAtMost {
				t.Errorf("peak concurrency %d, want <= %d", peak.Load(), tc.wantPeakAtMost)
			}
		})
	}
}

func TestRunBatch_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var executed atomic.Int32
	result := RunBatch(ctx, sizedItems(100, 1024), batchCfg(3, "TEST"),
		func(context.Context, testItem) error {
			if executed.Add(1) == 5 {
				cancel()
			}
			// Simulate some work.
			time.Sleep(time.Millisecond)
			return nil
		})

	// Some items should have completed; not all 100.
	if total := result.Completed + result.Failed; total >= 100 {
		t.Errorf("cancellation should have stopped processing before all 100 items, got %d", total)
	}
}

func TestRunBatch_PanicOnNilResourceMgr(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil ResourceMgr")
		}
	}()
	RunBatch(context.Background(), []testItem{{size: 1}}, BatchConfig{
		MaxWorkers: 5,
		Label:      "TEST",
	}, noopExec)
}

// --- RunBatchFromChannel tests ---

// TestRunBatchFromChannel streams a closed, pre-filled channel to completion.
// Every row asserts the same three things — all items completed, none failed,
// and a positive adaptive worker count — and states its own deltas through
// setup, which wires config knobs and returns the row's extra assertion.
func TestRunBatchFromChannel(t *testing.T) {
	tests := []struct {
		name       string
		maxWorkers int
		label      string
		items      int
		size       int64
		setup      func(t *testing.T, cfg *BatchConfig) (exec func(context.Context, testItem) error, verify func(t *testing.T))
	}{
		{
			name: "empty channel", maxWorkers: 10, label: "TEST", items: 0,
			setup: func(t *testing.T, _ *BatchConfig) (func(context.Context, testItem) error, func(*testing.T)) {
				return func(context.Context, testItem) error {
					t.Error("execute should not be called for empty channel")
					return nil
				}, nil
			},
		},
		{name: "basic execution", maxWorkers: 10, label: "TEST", items: 15, size: 1024 * 1024},
		{
			// Channel closes before the sample size of 20 — should still
			// compute adaptive.
			name: "small stream below sample size", maxWorkers: 20, label: "TEST", items: 5, size: 1024,
			setup: func(_ *testing.T, cfg *BatchConfig) (func(context.Context, testItem) error, func(*testing.T)) {
				var checked atomic.Bool
				cfg.ScaleCheckHook = func() { checked.Store(true) }
				return nil, func(t *testing.T) {
					if !checked.Load() {
						t.Error("expected ScaleCheckHook to be called for small stream (< sample size)")
					}
				}
			},
		},
		{
			// Enough items to trigger the initial sample at 20 and a resample at 70.
			name: "dynamic scaling", maxWorkers: 20, label: "TEST", items: 100, size: 1024,
			setup: func(_ *testing.T, cfg *BatchConfig) (func(context.Context, testItem) error, func(*testing.T)) {
				var checks atomic.Int32
				cfg.ScaleCheckHook = func() { checks.Add(1) }
				return nil, func(t *testing.T) {
					if checks.Load() < 1 {
						t.Errorf("expected at least 1 scale check, got %d", checks.Load())
					}
				}
			},
		},
		{
			name: "force sequential", maxWorkers: 10, label: "TEST", items: 5, size: 1024,
			setup: func(_ *testing.T, cfg *BatchConfig) (func(context.Context, testItem) error, func(*testing.T)) {
				cfg.ForceSequential = true
				var peak atomic.Int32
				return trackConcurrency(&peak), func(t *testing.T) {
					if peak.Load() != 1 {
						t.Errorf("ForceSequential should allow max 1 concurrent, got %d", peak.Load())
					}
				}
			},
		},
		{
			// AdaptiveCount must be populated and readable from inside execute.
			name: "adaptive count exposed", maxWorkers: 10, label: "TEST-ADAPTIVE", items: 10, size: 1024 * 1024,
			setup: func(_ *testing.T, cfg *BatchConfig) (func(context.Context, testItem) error, func(*testing.T)) {
				var adaptive *AdaptiveWorkerCount
				var observed atomic.Int32
				cfg.AdaptiveCount = &adaptive
				exec := func(context.Context, testItem) error {
					if adaptive != nil {
						observed.Store(int32(adaptive.Load()))
					}
					return nil
				}
				return exec, func(t *testing.T) {
					if adaptive == nil {
						t.Fatal("expected AdaptiveCount to be populated, got nil")
					}
					if adaptive.Load() < 1 {
						t.Errorf("expected AdaptiveCount >= 1, got %d", adaptive.Load())
					}
					if observed.Load() < 1 {
						t.Errorf("expected closure to observe AdaptiveCount >= 1, got %d", observed.Load())
					}
				}
			},
		},
		{
			// AdaptiveCount left nil must not panic.
			name: "nil adaptive count", maxWorkers: 10, label: "TEST-NIL-ADAPTIVE", items: 5, size: 1024,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := batchCfg(tc.maxWorkers, tc.label)
			exec, verify := noopExec, func(*testing.T) {}
			if tc.setup != nil {
				rowExec, rowVerify := tc.setup(t, &cfg)
				if rowExec != nil {
					exec = rowExec
				}
				if rowVerify != nil {
					verify = rowVerify
				}
			}

			result, adaptive := RunBatchFromChannel(context.Background(),
				filledChan(tc.items, tc.size), cfg, exec)

			if result.Completed != tc.items {
				t.Errorf("Completed = %d, want %d", result.Completed, tc.items)
			}
			if result.Failed != 0 {
				t.Errorf("Failed = %d, want 0", result.Failed)
			}
			if adaptive.Load() <= 0 {
				t.Errorf("adaptive count should be positive, got %d", adaptive.Load())
			}
			verify(t)
		})
	}
}

// TestRunBatchFromChannel_CancelMidStream cancels while a feeder is still
// sending and checks the batch stops well short of the full stream.
func TestRunBatchFromChannel_CancelMidStream(t *testing.T) {
	const streamed = 100

	tests := []struct {
		name        string
		maxWorkers  int
		label       string
		cancelAfter int // cancel once this many items have been sent
		workPerItem time.Duration
		wantBelow   int
	}{
		{
			name: "cancel at item 10", maxWorkers: 5, label: "TEST",
			cancelAfter: 10, workPerItem: time.Millisecond, wantBelow: streamed,
		},
		{
			name: "cancel at item 5", maxWorkers: 2, label: "TEST-CANCEL-MID",
			cancelAfter: 5, workPerItem: 10 * time.Millisecond, wantBelow: streamed / 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch := make(chan testItem)

			go func() {
				defer close(ch)
				for i := 0; i < streamed; i++ {
					select {
					case ch <- testItem{size: 1024, id: "file"}:
					case <-ctx.Done():
						return
					}
					if i == tc.cancelAfter {
						cancel()
					}
				}
			}()

			result, _ := RunBatchFromChannel(ctx, ch, batchCfg(tc.maxWorkers, tc.label),
				func(context.Context, testItem) error {
					time.Sleep(tc.workPerItem)
					return nil
				})

			if total := result.Completed + result.Failed; total >= tc.wantBelow {
				t.Errorf("cancel after item %d: processed %d items, want fewer than %d",
					tc.cancelAfter, total, tc.wantBelow)
			}
		})
	}
}

func TestRunBatchFromChannel_CancelWhileBlocked(t *testing.T) {
	// Verify that cancelling context while dispatcher is blocked on an empty
	// input channel causes prompt return (not waiting for next item).
	ch := make(chan testItem) // Unbuffered, never sent to — dispatcher will block
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		RunBatchFromChannel(ctx, ch, batchCfg(5, "TEST-CANCEL-BLOCKED"),
			func(context.Context, testItem) error {
				t.Error("execute should not be called — no items were sent")
				return nil
			})
	}()

	// Cancel after 100ms
	time.Sleep(100 * time.Millisecond)
	cancel()

	// RunBatchFromChannel should return within 200ms of cancel
	select {
	case <-done:
		// Success — returned promptly after cancel
	case <-time.After(2 * time.Second):
		t.Fatal("RunBatchFromChannel did not return within 2s of context cancellation (dispatcher blocked on empty channel)")
	}
}

// --- CollectErrors tests ---

func TestCollectErrors(t *testing.T) {
	tests := []struct {
		name string
		errs []error
		want int
	}{
		{name: "empty", errs: nil, want: 0},
		{name: "nils filtered", errs: []error{nil, errors.New("real error"), nil}, want: 1},
		{
			name: "multiple",
			errs: []error{errors.New("err1"), errors.New("err2"), errors.New("err3")},
			want: 3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan error, len(tc.errs)+1)
			for _, err := range tc.errs {
				ch <- err
			}
			close(ch)
			if got := CollectErrors(ch); len(got) != tc.want {
				t.Errorf("CollectErrors returned %d errors, want %d", len(got), tc.want)
			}
		})
	}
}

// --- Concurrency safety tests (run with -race) ---

func TestRunBatch_RaceSafety(t *testing.T) {
	items := make([]testItem, 100)
	for i := range items {
		items[i] = testItem{size: int64(i * 1024), id: "file"}
	}
	var counter atomic.Int32
	result := RunBatch(context.Background(), items, batchCfg(10, "RACE"),
		func(context.Context, testItem) error {
			counter.Add(1)
			return nil
		})
	if result.Completed != 100 {
		t.Errorf("expected 100 completed, got %d", result.Completed)
	}
	if counter.Load() != 100 {
		t.Errorf("expected 100 executions, got %d", counter.Load())
	}
}

func TestRunBatchFromChannel_RaceSafety(t *testing.T) {
	ch := make(chan testItem, 100)

	var sendWg sync.WaitGroup
	sendWg.Add(1)
	go func() {
		defer sendWg.Done()
		defer close(ch)
		for i := 0; i < 100; i++ {
			ch <- testItem{size: int64(i * 1024), id: "file"}
		}
	}()

	var counter atomic.Int32
	result, _ := RunBatchFromChannel(context.Background(), ch, batchCfg(10, "RACE"),
		func(context.Context, testItem) error {
			counter.Add(1)
			return nil
		})
	sendWg.Wait()

	if result.Completed != 100 {
		t.Errorf("expected 100 completed, got %d", result.Completed)
	}
}

// hookItem fires a callback from FileSize(), i.e. on the dispatcher goroutine
// immediately before an item is dispatched and the scale check runs.
type hookItem struct {
	size int64
	hook func()
}

func (h hookItem) FileSize() int64 {
	if h.hook != nil {
		h.hook()
	}
	return h.size
}

// Cancelling mid-stream drives the worker WaitGroup counter to zero (workers
// exit at their loop top) while the dispatcher is still inside its scale check,
// where it would call workerWg.Add(). Adding to a zero counter concurrently
// with Wait is sync.WaitGroup misuse and panics in a worker goroutine that has
// no recover, killing the process. RunBatchFromChannel must not return until
// the dispatcher has stopped spawning.
func TestRunBatchFromChannel_NoWaitGroupReuseOnCancel(t *testing.T) {
	const attempts = 3

	for attempt := 0; attempt < attempts; attempt++ {
		ctx, cancel := context.WithCancel(context.Background())

		var (
			returned            atomic.Bool
			returnedWhileInHook atomic.Bool
			spawnAfterReturn    atomic.Bool
			release             = make(chan struct{})
			releaseOnce         sync.Once
			hookDone            = make(chan struct{})
		)

		// Runs on the dispatcher goroutine for the 20th item — the first
		// sampling point — right before that item is dispatched and the scale
		// check runs. Cancelling here and releasing the parked workers lets them
		// exit at their loop top (items are still buffered in dispatch), which is
		// what drives the worker counter to zero mid-dispatch.
		hook := func() {
			defer close(hookDone)
			cancel()
			releaseOnce.Do(func() { close(release) })
			deadline := time.Now().Add(150 * time.Millisecond)
			for time.Now().Before(deadline) {
				if returned.Load() {
					returnedWhileInHook.Store(true)
					return
				}
				time.Sleep(100 * time.Microsecond)
			}
		}

		ch := make(chan hookItem, 64)
		for i := 0; i < 20; i++ {
			it := hookItem{size: 1024}
			if i == 19 {
				it.hook = hook
			}
			ch <- it
		}

		cfg := BatchConfig{
			MaxWorkers:  20,
			ResourceMgr: newTestResourceMgr(),
			Label:       "TEST-WG-REUSE",
			ScaleCheckHook: func() {
				if returned.Load() {
					spawnAfterReturn.Store(true)
				}
			},
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			RunBatchFromChannel(ctx, ch, cfg, func(c context.Context, item hookItem) error {
				<-release
				return nil
			})
			returned.Store(true)
		}()

		select {
		case <-done:
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatal("RunBatchFromChannel did not return")
		}

		// The hook observes the return asynchronously, so let it finish before
		// reading its verdict.
		select {
		case <-hookDone:
		case <-time.After(10 * time.Second):
			t.Fatal("dispatcher hook never finished")
		}

		// The dispatcher was still mid-loop (parked in the hook) — a return from
		// workerWg.Wait() at that point is the window in which the next
		// workerWg.Add() is WaitGroup misuse.
		if returnedWhileInHook.Load() {
			t.Fatalf("attempt %d: RunBatchFromChannel returned while the dispatcher was "+
				"still dispatching (worker spawning not yet stopped)", attempt)
		}
		if spawnAfterReturn.Load() {
			t.Fatalf("attempt %d: dispatcher reached the scale check (workerWg.Add) after "+
				"RunBatchFromChannel returned from workerWg.Wait()", attempt)
		}
		cancel()
		close(ch)
	}
}
