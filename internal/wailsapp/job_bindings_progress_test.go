package wailsapp

import (
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// makeFilesInDir writes each file to tmp with content of the named size
// and returns the slice of absolute paths in the same order as sizes.
func makeFilesInDir(t *testing.T, dir string, sizes map[string]int64) []string {
	t.Helper()
	var paths []string
	for name, sz := range sizes {
		p := filepath.Join(dir, name)
		buf := make([]byte, sz)
		if err := os.WriteFile(p, buf, 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", p, err)
		}
		paths = append(paths, p)
	}
	return paths
}

// TestBuildLocalFilesProgressCallbacks_weightedAggregation verifies the
// aggregation closure weights per-file fractions by byte size.
// File1=1 MiB, File2=3 MiB: 0.5*1 + 0.25*3 = 1.25 of 4 ⇒ 0.3125.
func TestBuildLocalFilesProgressCallbacks_weightedAggregation(t *testing.T) {
	dir := t.TempDir()
	const mib = int64(1 << 20)
	paths := makeFilesInDir(t, dir, map[string]int64{
		"file1": 1 * mib,
		"file2": 3 * mib,
	})
	// Make order deterministic for the test.
	var file1, file2 string
	for _, p := range paths {
		if filepath.Base(p) == "file1" {
			file1 = p
		} else {
			file2 = p
		}
	}

	var last float64
	var lastMu sync.Mutex
	cbFactory, totalBytes := buildLocalFilesProgressCallbacks(paths, func(total float64) {
		lastMu.Lock()
		defer lastMu.Unlock()
		last = total
	})
	if totalBytes != 4*mib {
		t.Fatalf("totalBytes = %d, want %d", totalBytes, 4*mib)
	}

	cbFactory(file1)(0.5)
	cbFactory(file2)(0.25)

	lastMu.Lock()
	got := last
	lastMu.Unlock()
	if math.Abs(got-0.3125) > 1e-9 {
		t.Errorf("aggregate = %v, want 0.3125", got)
	}
}

// TestBuildLocalFilesProgressCallbacks_monotonic verifies that monotonic
// per-file fractions yield a monotonic aggregate (never decreases).
func TestBuildLocalFilesProgressCallbacks_monotonic(t *testing.T) {
	dir := t.TempDir()
	const mib = int64(1 << 20)
	paths := makeFilesInDir(t, dir, map[string]int64{"a": 2 * mib, "b": 2 * mib})

	var maxSeen float64
	var mu sync.Mutex
	cbFactory, _ := buildLocalFilesProgressCallbacks(paths, func(total float64) {
		mu.Lock()
		defer mu.Unlock()
		if total+1e-9 < maxSeen {
			t.Errorf("aggregate decreased: saw %v, previous max %v", total, maxSeen)
		}
		if total > maxSeen {
			maxSeen = total
		}
	})

	for _, p := range paths {
		cb := cbFactory(p)
		for _, frac := range []float64{0.1, 0.2, 0.3, 0.5, 0.8, 1.0} {
			cb(frac)
		}
	}
}

// TestBuildLocalFilesProgressCallbacks_clamping verifies out-of-range
// per-file fractions are clamped (1.5 → 1.0; -0.1 → 0). The aggregate
// must never exceed 1.0.
func TestBuildLocalFilesProgressCallbacks_clamping(t *testing.T) {
	dir := t.TempDir()
	const mib = int64(1 << 20)
	paths := makeFilesInDir(t, dir, map[string]int64{"only": 1 * mib})

	var last float64
	var mu sync.Mutex
	cbFactory, _ := buildLocalFilesProgressCallbacks(paths, func(total float64) {
		mu.Lock()
		defer mu.Unlock()
		last = total
	})

	cbFactory(paths[0])(1.5)
	if last > 1.0+1e-9 {
		t.Errorf("aggregate after over-1 call = %v, want <= 1.0", last)
	}
	cbFactory(paths[0])(-0.1)
	if last < 0 {
		t.Errorf("aggregate after negative call = %v, want >= 0", last)
	}
}

// TestBuildLocalFilesProgressCallbacks_concurrent runs the callbacks from
// multiple goroutines to ensure the mutex prevents races. Run with
// -race to catch any data-race regressions.
func TestBuildLocalFilesProgressCallbacks_concurrent(t *testing.T) {
	dir := t.TempDir()
	const mib = int64(1 << 20)
	paths := makeFilesInDir(t, dir, map[string]int64{
		"f1": 1 * mib, "f2": 1 * mib, "f3": 1 * mib, "f4": 1 * mib,
	})

	var publishCount int64
	cbFactory, _ := buildLocalFilesProgressCallbacks(paths, func(total float64) {
		atomic.AddInt64(&publishCount, 1)
		if total < 0 || total > 1.0 {
			t.Errorf("total out of [0,1]: %v", total)
		}
	})

	var wg sync.WaitGroup
	for _, p := range paths {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			cb := cbFactory(p)
			for i := 0; i < 100; i++ {
				cb(float64(i) / 100.0)
			}
		}()
	}
	wg.Wait()

	if atomic.LoadInt64(&publishCount) == 0 {
		t.Error("publish callback never called")
	}
}
