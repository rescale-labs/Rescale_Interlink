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

func TestRunBatch_Empty(t *testing.T) {
	cfg := BatchConfig{
		MaxWorkers:  10,
		ResourceMgr: newTestResourceMgr(),
		Label:       "TEST",
	}
	result := RunBatch[testItem](context.Background(), nil, cfg, func(ctx context.Context, item testItem) error {
		t.Fatal("execute should not be called for empty batch")
		return nil
	})
	if result.Completed != 0 || result.Failed != 0 || len(result.Errors) != 0 {
		t.Errorf("empty batch should return zero result, got %+v", result)
	}
}

func TestRunBatch_SingleItem(t *testing.T) {
	cfg := BatchConfig{
		MaxWorkers:  10,
		ResourceMgr: newTestResourceMgr(),
		Label:       "TEST",
	}
	items := []testItem{{size: 1024, id: "file1"}}
	result := RunBatch(context.Background(), items, cfg, func(ctx context.Context, item testItem) error {
		return nil
	})
	if result.Completed != 1 {
		t.Errorf("expected 1 completed, got %d", result.Completed)
	}
}

func TestRunBatch_AllSucceed(t *testing.T) {
	cfg := BatchConfig{
		MaxWorkers:  5,
		ResourceMgr: newTestResourceMgr(),
		Label:       "TEST",
	}
	items := make([]testItem, 20)
	for i := range items {
		items[i] = testItem{size: 1024 * 1024, id: "file"} // 1MB each (small files)
	}
	var executed atomic.Int32
	result := RunBatch(context.Background(), items, cfg, func(ctx context.Context, item testItem) error {
		executed.Add(1)
		return nil
	})
	if result.Completed != 20 {
		t.Errorf("expected 20 completed, got %d", result.Completed)
	}
	if result.Failed != 0 {
		t.Errorf("expected 0 failed, got %d", result.Failed)
	}
	if int(executed.Load()) != 20 {
		t.Errorf("expected 20 executed, got %d", executed.Load())
	}
}

func TestRunBatch_ErrorPropagation(t *testing.T) {
	cfg := BatchConfig{
		MaxWorkers:  5,
		ResourceMgr: newTestResourceMgr(),
		Label:       "TEST",
	}
	items := make([]testItem, 10)
	for i := range items {
		items[i] = testItem{size: 1024, id: "file"}
	}
	errTest := errors.New("test error")
	result := RunBatch(context.Background(), items, cfg, func(ctx context.Context, item testItem) error {
		return errTest
	})
	if result.Failed != 10 {
		t.Errorf("expected 10 failed, got %d", result.Failed)
	}
	if len(result.Errors) != 10 {
		t.Errorf("expected 10 errors, got %d", len(result.Errors))
	}
	for _, err := range result.Errors {
		if !errors.Is(err, errTest) {
			t.Errorf("expected test error, got %v", err)
		}
	}
}

func TestRunBatch_ContextCancellation(t *testing.T) {
	cfg := BatchConfig{
		MaxWorkers:  3,
		ResourceMgr: newTestResourceMgr(),
		Label:       "TEST",
	}
	items := make([]testItem, 100)
	for i := range items {
		items[i] = testItem{size: 1024, id: "file"}
	}

	ctx, cancel := context.WithCancel(context.Background())
	var executed atomic.Int32
	result := RunBatch(ctx, items, cfg, func(ctx context.Context, item testItem) error {
		if executed.Add(1) == 5 {
			cancel()
		}
		// Simulate some work.
		time.Sleep(time.Millisecond)
		return nil
	})

	// Some items should have completed; not all 100.
	total := result.Completed + result.Failed
	if total >= 100 {
		t.Errorf("cancellation should have stopped processing before all 100 items, got %d", total)
	}
}

func TestRunBatch_ForceSequential(t *testing.T) {
	cfg := BatchConfig{
		MaxWorkers:      10,
		ResourceMgr:     newTestResourceMgr(),
		Label:           "TEST",
		ForceSequential: true,
	}
	items := make([]testItem, 5)
	for i := range items {
		items[i] = testItem{size: 1024, id: "file"}
	}

	var maxConcurrent atomic.Int32
	var current atomic.Int32
	result := RunBatch(context.Background(), items, cfg, func(ctx context.Context, item testItem) error {
		c := current.Add(1)
		for {
			prev := maxConcurrent.Load()
			if c <= prev || maxConcurrent.CompareAndSwap(prev, c) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		current.Add(-1)
		return nil
	})

	if result.Completed != 5 {
		t.Errorf("expected 5 completed, got %d", result.Completed)
	}
	if maxConcurrent.Load() != 1 {
		t.Errorf("ForceSequential should allow max 1 concurrent, got %d", maxConcurrent.Load())
	}
}

func TestRunBatch_AdaptiveConcurrency_LargeFiles(t *testing.T) {
	cfg := BatchConfig{
		MaxWorkers:  20,
		ResourceMgr: newTestResourceMgr(),
		Label:       "TEST",
	}
	// Large files (2GB each) should get fewer workers.
	items := make([]testItem, 10)
	for i := range items {
		items[i] = testItem{size: 2 * 1024 * 1024 * 1024, id: "largefile"}
	}

	workers := ComputedWorkers(items, cfg)
	if workers > 5 {
		t.Errorf("large files (2GB) should get <= 5 workers, got %d", workers)
	}
	if workers < 1 {
		t.Errorf("should get at least 1 worker, got %d", workers)
	}
}

func TestRunBatch_AdaptiveConcurrency_SmallFiles(t *testing.T) {
	cfg := BatchConfig{
		MaxWorkers:  20,
		ResourceMgr: newTestResourceMgr(),
		Label:       "TEST",
	}
	// Small files (1MB each) should get more workers.
	items := make([]testItem, 30)
	for i := range items {
		items[i] = testItem{size: 1024 * 1024, id: "smallfile"}
	}

	workers := ComputedWorkers(items, cfg)
	if workers < 5 {
		t.Errorf("small files (1MB) should get >= 5 workers, got %d", workers)
	}
}

func TestRunBatch_WorkerCountNeverExceedsMax(t *testing.T) {
	cfg := BatchConfig{
		MaxWorkers:  3,
		ResourceMgr: newTestResourceMgr(),
		Label:       "TEST",
	}
	items := make([]testItem, 50)
	for i := range items {
		items[i] = testItem{size: 1024, id: "file"}
	}

	var maxConcurrent atomic.Int32
	var current atomic.Int32
	RunBatch(context.Background(), items, cfg, func(ctx context.Context, item testItem) error {
		c := current.Add(1)
		for {
			prev := maxConcurrent.Load()
			if c <= prev || maxConcurrent.CompareAndSwap(prev, c) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		current.Add(-1)
		return nil
	})

	if maxConcurrent.Load() > 3 {
		t.Errorf("worker count should never exceed MaxWorkers=3, observed %d", maxConcurrent.Load())
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
	}, func(ctx context.Context, item testItem) error {
		return nil
	})
}

// --- RunBatchFromChannel tests ---

func TestRunBatchFromChannel_Empty(t *testing.T) {
	cfg := BatchConfig{
		MaxWorkers:  10,
		ResourceMgr: newTestResourceMgr(),
		Label:       "TEST",
	}
	ch := make(chan testItem)
	close(ch)
	result, adaptive := RunBatchFromChannel(context.Background(), ch, cfg, func(ctx context.Context, item testItem) error {
		t.Fatal("execute should not be called for empty channel")
		return nil
	})
	if result.Completed != 0 || result.Failed != 0 {
		t.Errorf("empty channel should return zero result, got %+v", result)
	}
	if adaptive.Load() <= 0 {
		t.Errorf("adaptive count should be positive, got %d", adaptive.Load())
	}
}

func TestRunBatchFromChannel_BasicExecution(t *testing.T) {
	cfg := BatchConfig{
		MaxWorkers:  10,
		ResourceMgr: newTestResourceMgr(),
		Label:       "TEST",
	}
	ch := make(chan testItem, 15)
	for i := 0; i < 15; i++ {
		ch <- testItem{size: 1024 * 1024, id: "file"}
	}
	close(ch)

	result, _ := RunBatchFromChannel(context.Background(), ch, cfg, func(ctx context.Context, item testItem) error {
		return nil
	})
	if result.Completed != 15 {
		t.Errorf("expected 15 completed, got %d", result.Completed)
	}
}

func TestRunBatchFromChannel_SmallStream(t *testing.T) {
	// Channel closes before sample size of 20 — should still compute adaptive.
	cfg := BatchConfig{
		MaxWorkers:  20,
		ResourceMgr: newTestResourceMgr(),
		Label:       "TEST",
	}
	var scaleChecked atomic.Bool
	cfg.ScaleCheckHook = func() {
		scaleChecked.Store(true)
	}

	ch := make(chan testItem, 5)
	for i := 0; i < 5; i++ {
		ch <- testItem{size: 1024, id: "small"}
	}
	close(ch)

	result, _ := RunBatchFromChannel(context.Background(), ch, cfg, func(ctx context.Context, item testItem) error {
		return nil
	})
	if result.Completed != 5 {
		t.Errorf("expected 5 completed, got %d", result.Completed)
	}
	if !scaleChecked.Load() {
		t.Error("expected ScaleCheckHook to be called for small stream (< sample size)")
	}
}

func TestRunBatchFromChannel_DynamicScaling(t *testing.T) {
	// Send enough items to trigger initial sample + resample.
	cfg := BatchConfig{
		MaxWorkers:  20,
		ResourceMgr: newTestResourceMgr(),
		Label:       "TEST",
	}

	var scaleCheckCount atomic.Int32
	cfg.ScaleCheckHook = func() {
		scaleCheckCount.Add(1)
	}

	ch := make(chan testItem, 100)
	// 100 small files: should trigger sample at 20 + resample at 70.
	for i := 0; i < 100; i++ {
		ch <- testItem{size: 1024, id: "small"}
	}
	close(ch)

	result, adaptive := RunBatchFromChannel(context.Background(), ch, cfg, func(ctx context.Context, item testItem) error {
		return nil
	})
	if result.Completed != 100 {
		t.Errorf("expected 100 completed, got %d", result.Completed)
	}
	if adaptive.Load() < 1 {
		t.Errorf("adaptive count should be positive, got %d", adaptive.Load())
	}
	// Should have at least 1 scale check (initial sample at 20).
	if scaleCheckCount.Load() < 1 {
		t.Errorf("expected at least 1 scale check, got %d", scaleCheckCount.Load())
	}
}

func TestRunBatchFromChannel_ContextCancellation(t *testing.T) {
	cfg := BatchConfig{
		MaxWorkers:  5,
		ResourceMgr: newTestResourceMgr(),
		Label:       "TEST",
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan testItem)

	// Feed items in a goroutine; cancel after a few.
	go func() {
		defer close(ch)
		for i := 0; i < 100; i++ {
			select {
			case ch <- testItem{size: 1024, id: "file"}:
			case <-ctx.Done():
				return
			}
			if i == 10 {
				cancel()
			}
		}
	}()

	result, _ := RunBatchFromChannel(ctx, ch, cfg, func(ctx context.Context, item testItem) error {
		time.Sleep(time.Millisecond)
		return nil
	})
	total := result.Completed + result.Failed
	if total >= 100 {
		t.Errorf("cancellation should have stopped before all 100, got %d", total)
	}
}

func TestRunBatchFromChannel_ForceSequential(t *testing.T) {
	cfg := BatchConfig{
		MaxWorkers:      10,
		ResourceMgr:     newTestResourceMgr(),
		Label:           "TEST",
		ForceSequential: true,
	}

	ch := make(chan testItem, 5)
	for i := 0; i < 5; i++ {
		ch <- testItem{size: 1024, id: "file"}
	}
	close(ch)

	var maxConcurrent atomic.Int32
	var current atomic.Int32
	result, _ := RunBatchFromChannel(context.Background(), ch, cfg, func(ctx context.Context, item testItem) error {
		c := current.Add(1)
		for {
			prev := maxConcurrent.Load()
			if c <= prev || maxConcurrent.CompareAndSwap(prev, c) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		current.Add(-1)
		return nil
	})

	if result.Completed != 5 {
		t.Errorf("expected 5 completed, got %d", result.Completed)
	}
	if maxConcurrent.Load() != 1 {
		t.Errorf("ForceSequential should allow max 1 concurrent, got %d", maxConcurrent.Load())
	}
}

func TestRunBatchFromChannel_AdaptiveCountExposed(t *testing.T) {
	// Verify that AdaptiveCount is populated and readable from within the execute closure.
	var adaptive *AdaptiveWorkerCount
	cfg := BatchConfig{
		MaxWorkers:    10,
		ResourceMgr:   newTestResourceMgr(),
		Label:         "TEST-ADAPTIVE",
		AdaptiveCount: &adaptive,
	}

	ch := make(chan testItem, 10)
	for i := 0; i < 10; i++ {
		ch <- testItem{size: 1024 * 1024, id: "file"}
	}
	close(ch)

	var observedFromClosure atomic.Int32
	result, _ := RunBatchFromChannel(context.Background(), ch, cfg, func(ctx context.Context, item testItem) error {
		// Read adaptive count from within the closure — should be > 0.
		if adaptive != nil {
			observedFromClosure.Store(int32(adaptive.Load()))
		}
		return nil
	})

	if adaptive == nil {
		t.Fatal("expected AdaptiveCount to be populated, got nil")
	}
	if adaptive.Load() < 1 {
		t.Errorf("expected AdaptiveCount >= 1, got %d", adaptive.Load())
	}
	if observedFromClosure.Load() < 1 {
		t.Errorf("expected closure to observe AdaptiveCount >= 1, got %d", observedFromClosure.Load())
	}
	if result.Completed != 10 {
		t.Errorf("expected 10 completed, got %d", result.Completed)
	}
}

func TestRunBatchFromChannel_AdaptiveCountNilIgnored(t *testing.T) {
	// Verify that nil AdaptiveCount doesn't cause a panic.
	cfg := BatchConfig{
		MaxWorkers:  10,
		ResourceMgr: newTestResourceMgr(),
		Label:       "TEST-NIL-ADAPTIVE",
		// AdaptiveCount not set (nil)
	}

	ch := make(chan testItem, 5)
	for i := 0; i < 5; i++ {
		ch <- testItem{size: 1024, id: "file"}
	}
	close(ch)

	result, _ := RunBatchFromChannel(context.Background(), ch, cfg, func(ctx context.Context, item testItem) error {
		return nil
	})
	if result.Completed != 5 {
		t.Errorf("expected 5 completed, got %d", result.Completed)
	}
}

func TestRunBatchFromChannel_CancelWhileBlocked(t *testing.T) {
	// Verify that cancelling context while dispatcher is blocked on an empty
	// input channel causes prompt return (not waiting for next item).
	cfg := BatchConfig{
		MaxWorkers:  5,
		ResourceMgr: newTestResourceMgr(),
		Label:       "TEST-CANCEL-BLOCKED",
	}

	ch := make(chan testItem) // Unbuffered, never sent to — dispatcher will block
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		RunBatchFromChannel(ctx, ch, cfg, func(ctx context.Context, item testItem) error {
			t.Error("execute should not be called — no items were sent")
			return nil
		})
		close(done)
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

func TestRunBatchFromChannel_CancelMidStream(t *testing.T) {
	// Send some items, cancel context, verify not all items were processed.
	cfg := BatchConfig{
		MaxWorkers:  2,
		ResourceMgr: newTestResourceMgr(),
		Label:       "TEST-CANCEL-MID",
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan testItem)

	go func() {
		defer close(ch)
		for i := 0; i < 100; i++ {
			select {
			case ch <- testItem{size: 1024, id: "file"}:
			case <-ctx.Done():
				return
			}
			if i == 5 {
				cancel()
			}
		}
	}()

	result, _ := RunBatchFromChannel(ctx, ch, cfg, func(ctx context.Context, item testItem) error {
		time.Sleep(10 * time.Millisecond)
		return nil
	})

	total := result.Completed + result.Failed
	if total >= 50 {
		t.Errorf("expected fewer than 50 items processed after cancel at item 5, got %d", total)
	}
}

// --- CollectErrors tests ---

func TestCollectErrors_Empty(t *testing.T) {
	ch := make(chan error)
	close(ch)
	errs := CollectErrors(ch)
	if len(errs) != 0 {
		t.Errorf("expected 0 errors, got %d", len(errs))
	}
}

func TestCollectErrors_WithNils(t *testing.T) {
	ch := make(chan error, 5)
	ch <- nil
	ch <- errors.New("real error")
	ch <- nil
	close(ch)
	errs := CollectErrors(ch)
	if len(errs) != 1 {
		t.Errorf("expected 1 error (nils filtered), got %d", len(errs))
	}
}

func TestCollectErrors_Multiple(t *testing.T) {
	ch := make(chan error, 3)
	ch <- errors.New("err1")
	ch <- errors.New("err2")
	ch <- errors.New("err3")
	close(ch)
	errs := CollectErrors(ch)
	if len(errs) != 3 {
		t.Errorf("expected 3 errors, got %d", len(errs))
	}
}

// --- Concurrency safety tests (run with -race) ---

func TestRunBatch_RaceSafety(t *testing.T) {
	cfg := BatchConfig{
		MaxWorkers:  10,
		ResourceMgr: newTestResourceMgr(),
		Label:       "RACE",
	}
	items := make([]testItem, 100)
	for i := range items {
		items[i] = testItem{size: int64(i * 1024), id: "file"}
	}
	var counter atomic.Int32
	result := RunBatch(context.Background(), items, cfg, func(ctx context.Context, item testItem) error {
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
	cfg := BatchConfig{
		MaxWorkers:  10,
		ResourceMgr: newTestResourceMgr(),
		Label:       "RACE",
	}
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
	result, _ := RunBatchFromChannel(context.Background(), ch, cfg, func(ctx context.Context, item testItem) error {
		counter.Add(1)
		return nil
	})
	sendWg.Wait()

	if result.Completed != 100 {
		t.Errorf("expected 100 completed, got %d", result.Completed)
	}
}
