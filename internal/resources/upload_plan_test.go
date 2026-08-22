package resources

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/rescale/rescale-int/internal/constants"
)

const (
	mib = int64(constants.PartSizeAlignment)
	gib = 1024 * mib
	tib = 1024 * gib
)

// s3Limits and azureLimits mirror what the providers report from UploadLimits().
var (
	s3Limits = UploadLimits{
		StorageType: "S3Storage",
		MaxParts:    constants.MaxS3UploadParts,
		MaxPartSize: constants.MaxS3PlaintextPartSize,
	}
	azureLimits = UploadLimits{
		StorageType: "AzureStorage",
		MaxParts:    constants.MaxAzureUploadBlocks,
		MaxPartSize: constants.MaxAzurePlaintextBlockSize,
	}
)

// planTestManager builds a manager pinned to a synthetic machine so plan
// assertions describe the planner and not the host running the test.
func planTestManager(t *testing.T, budget int64) *Manager {
	t.Helper()
	return NewManager(Config{
		MaxThreads:   16,
		AutoScale:    true,
		CPUCores:     16,
		MemoryBudget: uint64(budget),
	})
}

// wantWorkerFloor restates the planner's rule independently: the throttled floor
// when the machine can hold that many parts, otherwise a single worker.
func wantWorkerFloor(threads int, partSize, budget int64) int {
	floor := threads
	if floor > constants.UploadMinThrottledWorkers {
		floor = constants.UploadMinThrottledWorkers
	}
	if int64(1+floor+constants.UploadPipelineTransientParts)*partSize > budget {
		floor = 1
	}
	return floor
}

// exhaustBudget plans large uploads until the manager has no budget left, so a
// following plan sees the worst contention the system can produce.
func exhaustBudget(t *testing.T, mgr *Manager) {
	t.Helper()
	for i := 0; i < 64; i++ {
		if mgr.GetAvailableUploadMemory() <= 0 {
			return
		}
		planOne(t, mgr, fmt.Sprintf("hog-%d", i), 20*gib, constants.MaxThreadsPerFile, s3Limits)
	}
	t.Fatal("could not exhaust the budget in 64 plans")
}

func planOne(t *testing.T, mgr *Manager, id string, fileSize int64, threads int, limits UploadLimits) UploadPlan {
	t.Helper()
	plan, err := mgr.PlanUpload(id, UploadPlanRequest{FileSize: fileSize, Threads: threads, Limits: limits})
	if err != nil {
		t.Fatalf("PlanUpload(%d bytes, %d threads): %v", fileSize, threads, err)
	}
	return plan
}

// TestPlanUploadPartSizeMatchesDynamicChunkSize is the differential check against
// the sizing the providers used before there was a plan: below the size where the
// part-count floor starts binding, the plan must not move part size at all.
func TestPlanUploadPartSizeMatchesDynamicChunkSize(t *testing.T) {
	// The exported entry point and the memory-parameterised one are the same
	// function, so a plan pinned to this machine's own reading is comparable to
	// CalculateDynamicChunkSize on any host.
	hostMemory := getAvailableMemory()
	if got, want := CalculateDynamicChunkSize(gib, 16), calculateDynamicChunkSize(gib, 16, hostMemory); got != want {
		t.Fatalf("CalculateDynamicChunkSize diverged from its parameterised form: got %d, want %d", got, want)
	}

	// Capped at 100 GB: on a 512 MB machine the dynamic size clamps to 16 MB, which
	// puts floor activation at 160 GB. Past that the two are meant to diverge, and
	// TestPlanUploadFloorActivation covers the crossover.
	sizes := []int64{
		1,
		1024,
		50 * mib,
		constants.MultipartThreshold,
		400 * mib,
		gib,
		4 * gib,
		20 * gib,
		100 * gib,
	}

	for _, budget := range []int64{int64(hostMemory), 2 * gib, 8 * gib, 64 * gib} {
		for _, size := range sizes {
			for _, threads := range []int{1, 4, 8, 16} {
				name := fmt.Sprintf("budget=%dMB/size=%d/threads=%d", budget/mib, size, threads)
				t.Run(name, func(t *testing.T) {
					mgr := planTestManager(t, budget)
					plan := planOne(t, mgr, "diff", size, threads, s3Limits)

					want := calculateDynamicChunkSize(size, constants.MaxThreadsPerFile, uint64(budget))
					if plan.PartSize != want {
						t.Errorf("PartSize = %d, want CalculateDynamicChunkSize = %d", plan.PartSize, want)
					}
					if floor := partCountFloor(size, s3Limits.MaxParts); floor > want {
						t.Fatalf("test size %d is past floor activation (floor %d > dynamic %d)", size, floor, want)
					}
				})
			}
		}
	}
}

// TestPlanUploadGeometryMatchesLegacyPipeline pins the pipeline shape the
// orchestrator ran before the plan existed: an encrypted-part queue of three
// slots per worker, workers starting at the transfer's thread count, and a
// ceiling equal to what the background scaler could already reach.
func TestPlanUploadGeometryMatchesLegacyPipeline(t *testing.T) {
	// Roomy enough that the memory invariant is not the binding constraint —
	// this test is about the shape, not the fit.
	const budget = 64 * gib

	for _, size := range []int64{gib, 5 * gib, 20 * gib, 500 * gib} {
		for _, concurrency := range []int{1, 4, 8, 16} {
			t.Run(fmt.Sprintf("size=%dGB/concurrency=%d", size/gib, concurrency), func(t *testing.T) {
				mgr := planTestManager(t, budget)
				plan := planOne(t, mgr, "geometry", size, concurrency, s3Limits)

				if want := concurrency * constants.UploadQueueDepthPerWorker; plan.QueueDepth != want {
					t.Errorf("QueueDepth = %d, want %d (%d x concurrency)", plan.QueueDepth, want, constants.UploadQueueDepthPerWorker)
				}

				// The orchestrator starts min(concurrency, WorkerCap) workers.
				startingWorkers := concurrency
				if plan.WorkerCap < startingWorkers {
					startingWorkers = plan.WorkerCap
				}
				if startingWorkers != concurrency {
					t.Errorf("starting workers = %d, want %d (unchanged from the pre-plan pipeline)", startingWorkers, concurrency)
				}

				// WorkerCap has to equal the ceiling the scaler already obeyed, or
				// capping the scaler at it would silently remove scale-up.
				scalerCeiling := mgr.GetMaxForFileSize(size)
				if scalerCeiling < concurrency {
					scalerCeiling = concurrency
				}
				if plan.WorkerCap != scalerCeiling {
					t.Errorf("WorkerCap = %d, want %d (the ceiling TryAcquireMore already enforced)", plan.WorkerCap, scalerCeiling)
				}
				if plan.WorkerCap > constants.MaxThreadsPerFile {
					t.Errorf("WorkerCap = %d exceeds MaxThreadsPerFile %d", plan.WorkerCap, constants.MaxThreadsPerFile)
				}
			})
		}
	}
}

// TestPlanUploadMultipartThresholdUnaffected keeps the small-file path exactly
// where it was: below the multipart threshold nothing about the sizing changes.
func TestPlanUploadMultipartThresholdUnaffected(t *testing.T) {
	mgr := planTestManager(t, 8*gib)

	for _, size := range []int64{0, 1, 1024, 50 * mib, constants.MultipartThreshold - 1, constants.MultipartThreshold} {
		id := fmt.Sprintf("small-%d", size)
		plan := planOne(t, mgr, id, size, 4, s3Limits)
		mgr.ReleaseUploadPlan(id)

		if want := calculateDynamicChunkSize(size, constants.MaxThreadsPerFile, uint64(8*gib)); plan.PartSize != want {
			t.Errorf("size %d: PartSize = %d, want the unchanged %d", size, plan.PartSize, want)
		}
		if floor := partCountFloor(size, s3Limits.MaxParts); floor > plan.PartSize {
			t.Errorf("size %d: part-count floor %d should not bind at or below the multipart threshold", size, floor)
		}
	}
}

// TestPlanUploadPartCountWithinLimit is the property the customer bug was about:
// whatever the file size and whatever the machine, the resulting part count fits
// the backend and the pipeline fits the budget.
func TestPlanUploadPartCountWithinLimit(t *testing.T) {
	budgets := []int64{512 * mib, gib, 2 * gib, 3 * gib, 8 * gib, 64 * gib}

	for _, limits := range []UploadLimits{s3Limits, azureLimits} {
		maxSupported := limits.MaxParts * limits.MaxPartSize

		sizes := []int64{
			0, 1, 100 * mib, gib, 64 * gib,
			limits.MaxParts * constants.MaxChunkSize, // exactly the old ceiling
			500 * gib, 2 * tib, 4 * tib, 10 * tib,
			maxSupported / 2,
			maxSupported,
		}

		for _, budget := range budgets {
			for _, size := range sizes {
				if size > maxSupported {
					continue
				}
				name := fmt.Sprintf("%s/budget=%dMB/size=%d", limits.StorageType, budget/mib, size)
				t.Run(name, func(t *testing.T) {
					mgr := planTestManager(t, budget)
					plan, err := mgr.PlanUpload("prop", UploadPlanRequest{FileSize: size, Threads: 8, Limits: limits})
					if err != nil {
						// The only legitimate refusal below the supported maximum is
						// a machine too small to hold one working set, and that can
						// only happen once the part-count floor has forced parts
						// past the normal range.
						if !strings.Contains(err.Error(), "not enough memory") {
							t.Fatalf("unexpected planning failure: %v", err)
						}
						// Only the part-count floor can push an upload past what a
						// machine can hold; the memory clamp sizes parts to fit by
						// construction, so nothing that plans today may be refused.
						dynamic := calculateDynamicChunkSize(size, constants.MaxThreadsPerFile, uint64(budget))
						if partCountFloor(size, limits.MaxParts) <= dynamic {
							t.Fatalf("an upload the part-count floor never touched was refused for memory: %v", err)
						}
						return
					}

					if plan.PartSize%int64(constants.PartSizeAlignment) != 0 {
						t.Errorf("PartSize %d is not MB-aligned; CBC needs block-aligned parts", plan.PartSize)
					}
					if plan.PartSize < constants.MinChunkSize {
						t.Errorf("PartSize %d below MinChunkSize %d", plan.PartSize, constants.MinChunkSize)
					}
					if plan.PartSize > limits.MaxPartSize {
						t.Errorf("PartSize %d above the backend limit %d", plan.PartSize, limits.MaxPartSize)
					}

					parts := (size + plan.PartSize - 1) / plan.PartSize
					if parts > limits.MaxParts {
						t.Errorf("%d parts of %d bytes exceeds the %d-part limit", parts, plan.PartSize, limits.MaxParts)
					}

					if got := plan.inFlightBytes(); got > budget {
						t.Errorf("in-flight budget %d exceeds the %d available (queue %d, workers %d, parts of %d)",
							got, budget, plan.QueueDepth, plan.WorkerCap, plan.PartSize)
					}
					if plan.QueueDepth < 1 || plan.WorkerCap < 1 {
						t.Errorf("plan is not runnable: queue %d, workers %d", plan.QueueDepth, plan.WorkerCap)
					}
					if want := wantWorkerFloor(8, plan.PartSize, budget); plan.WorkerCap < want {
						t.Errorf("WorkerCap %d is below the floor %d this budget affords", plan.WorkerCap, want)
					}
				})
			}
		}
	}
}

// TestPlanUploadBoundaries walks the exact sizes where the old fixed 64 MB part
// size ran out of part numbers.
func TestPlanUploadBoundaries(t *testing.T) {
	const budget = 64 * gib

	tests := []struct {
		name   string
		limits UploadLimits
		size   int64
	}{
		{"s3 at the 10,000-part ceiling", s3Limits, constants.MaxS3UploadParts * constants.MaxChunkSize},
		{"s3 one byte under", s3Limits, constants.MaxS3UploadParts*constants.MaxChunkSize - 1},
		{"s3 one byte over", s3Limits, constants.MaxS3UploadParts*constants.MaxChunkSize + 1},
		{"azure at the 50,000-block ceiling", azureLimits, constants.MaxAzureUploadBlocks * constants.MaxChunkSize},
		{"azure one byte under", azureLimits, constants.MaxAzureUploadBlocks*constants.MaxChunkSize - 1},
		{"azure one byte over", azureLimits, constants.MaxAzureUploadBlocks*constants.MaxChunkSize + 1},
		{"the 4.2 TB upload from the report (s3)", s3Limits, 4_200_000_000_000},
		{"the 4.2 TB upload from the report (azure)", azureLimits, 4_200_000_000_000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := planTestManager(t, budget)
			plan := planOne(t, mgr, "boundary", tt.size, 8, tt.limits)

			parts := (tt.size + plan.PartSize - 1) / plan.PartSize
			if parts > tt.limits.MaxParts {
				t.Fatalf("%d parts of %d bytes exceeds the %d-part limit", parts, plan.PartSize, tt.limits.MaxParts)
			}
			if got := plan.inFlightBytes(); got > budget {
				t.Errorf("in-flight budget %d exceeds %d", got, budget)
			}
			t.Logf("%s: part size %d MB, %d parts, queue %d, workers %d",
				tt.name, plan.PartSize/mib, parts, plan.QueueDepth, plan.WorkerCap)
		})
	}
}

// TestPlanUploadFloorActivation checks that the floor is inert right up to the
// old ceiling and takes over one byte later.
func TestPlanUploadFloorActivation(t *testing.T) {
	mgr := planTestManager(t, 64*gib)
	ceiling := int64(constants.MaxS3UploadParts) * constants.MaxChunkSize

	atCeiling := planOne(t, mgr, "at", ceiling, 8, s3Limits)
	mgr.ReleaseUploadPlan("at")
	if atCeiling.PartSize != constants.MaxChunkSize {
		t.Errorf("at the ceiling: PartSize = %d, want the unchanged %d", atCeiling.PartSize, int64(constants.MaxChunkSize))
	}

	overCeiling := planOne(t, mgr, "over", ceiling+1, 8, s3Limits)
	mgr.ReleaseUploadPlan("over")
	if overCeiling.PartSize <= constants.MaxChunkSize {
		t.Errorf("one byte over: PartSize = %d, want the floor to raise it above %d", overCeiling.PartSize, int64(constants.MaxChunkSize))
	}
	if overCeiling.PartSize != 65*mib {
		t.Errorf("one byte over: PartSize = %d, want 65 MB", overCeiling.PartSize)
	}
}

// TestPlanUploadFileTooLarge covers the size neither backend can store, at the
// exact byte where the floor outgrows the per-part limit.
func TestPlanUploadFileTooLarge(t *testing.T) {
	for _, limits := range []UploadLimits{s3Limits, azureLimits} {
		t.Run(limits.StorageType, func(t *testing.T) {
			maxSupported := limits.MaxParts * limits.MaxPartSize

			mgr := planTestManager(t, 64*gib)
			if _, err := mgr.PlanUpload("edge", UploadPlanRequest{FileSize: maxSupported, Threads: 8, Limits: limits}); err != nil {
				if strings.Contains(err.Error(), "too large") {
					t.Fatalf("the largest supported size was rejected as too large: %v", err)
				}
				t.Logf("largest supported size needs more memory than this budget: %v", err)
			}

			_, err := mgr.PlanUpload("over", UploadPlanRequest{FileSize: maxSupported + 1, Threads: 8, Limits: limits})
			if err == nil {
				t.Fatal("expected a file one byte past the supported maximum to be rejected")
			}
			for _, want := range []string{"too large", limits.StorageType} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
			if !strings.Contains(err.Error(), "largest file") {
				t.Errorf("error %q does not report the maximum supported size", err)
			}
		})
	}
}

// TestPlanUploadInsufficientMemory covers the machine that cannot hold even one
// minimal working set for the part size the file forces.
func TestPlanUploadInsufficientMemory(t *testing.T) {
	// A 4.2 TB file needs ~401 MB parts on S3; six of those do not fit in 512 MB.
	mgr := planTestManager(t, 512*mib)

	_, err := mgr.PlanUpload("tight", UploadPlanRequest{FileSize: 4_200_000_000_000, Threads: 8, Limits: s3Limits})
	if err == nil {
		t.Fatal("expected planning to fail on a machine too small for the required part size")
	}
	if !strings.Contains(err.Error(), "not enough memory") {
		t.Errorf("error %q does not identify memory as the problem", err)
	}
	if !strings.Contains(err.Error(), s3Limits.StorageType) {
		t.Errorf("error %q does not name the storage type", err)
	}

	// A refusal must not leave the budget consumed.
	if got := mgr.GetAvailableUploadMemory(); got != 512*mib {
		t.Errorf("failed plan consumed budget: %d available, want %d", got, 512*mib)
	}
}

// TestPlanUploadSharedBudget is the cross-transfer property: uploads planned at
// the same time draw from one budget instead of each claiming the machine, and
// the ones that arrive after it is spoken for run narrow rather than failing.
func TestPlanUploadSharedBudget(t *testing.T) {
	const budget = 8 * gib
	const transfers = 12
	mgr := planTestManager(t, budget)

	var reserved int64
	var first, last UploadPlan
	exhaustedAfter := -1

	for i := 0; i < transfers; i++ {
		id := fmt.Sprintf("transfer-%d", i)
		plan := planOne(t, mgr, id, 20*gib, 4, s3Limits)
		if i == 0 {
			first = plan
		}
		last = plan

		if exhaustedAfter < 0 {
			reserved += plan.inFlightBytes()
			if reserved > budget {
				t.Fatalf("after %d plans the reservations total %d, past the %d budget", i+1, reserved, int64(budget))
			}
			if got := mgr.GetAvailableUploadMemory(); got != budget-reserved {
				t.Fatalf("available memory = %d, want %d", got, budget-reserved)
			}
			if mgr.GetAvailableUploadMemory() <= 0 {
				exhaustedAfter = i
			}
		}
	}

	if exhaustedAfter < 0 {
		t.Fatalf("all %d uploads fit the budget; the budget is not constraining anything", transfers)
	}
	if exhaustedAfter == 0 {
		t.Fatal("the first upload alone exhausted the budget; the test is not exercising sharing")
	}

	// Once the budget is gone every further upload runs at the minimum working
	// set — narrowed, not refused, and nowhere near the first one's footprint.
	// These are normal-sized parts, so the workers floor at the throttled minimum
	// rather than at one; the cap is frozen at plan time and would otherwise leave
	// a late file single-threaded for its whole transfer.
	wantWorkers := 4
	if wantWorkers > constants.UploadMinThrottledWorkers {
		wantWorkers = constants.UploadMinThrottledWorkers
	}
	if last.PartSize > constants.MaxChunkSize {
		t.Fatalf("test setup: part size %d is in the floored regime; the throttled floor would be 1", last.PartSize)
	}
	if last.QueueDepth != 1 || last.WorkerCap != wantWorkers {
		t.Errorf("after the budget was exhausted a plan got queue %d / workers %d, want 1 / %d",
			last.QueueDepth, last.WorkerCap, wantWorkers)
	}
	if last.inFlightBytes() >= first.inFlightBytes() {
		t.Errorf("a late upload reserved %d, no less than the first upload's %d",
			last.inFlightBytes(), first.inFlightBytes())
	}
	if last.PartSize != first.PartSize {
		t.Errorf("part size moved with contention: %d vs %d", last.PartSize, first.PartSize)
	}

	for i := 0; i < transfers; i++ {
		mgr.ReleaseUploadPlan(fmt.Sprintf("transfer-%d", i))
	}
	if got := mgr.GetAvailableUploadMemory(); got != budget {
		t.Errorf("after releasing everything, available memory = %d, want the full %d", got, int64(budget))
	}
}

// TestPlanUploadSharedBudgetConcurrent runs the same sharing rule through
// simultaneous planners, which is how batch uploads actually arrive.
func TestPlanUploadSharedBudgetConcurrent(t *testing.T) {
	const budget = 8 * gib
	mgr := planTestManager(t, budget)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var reserved int64
	ids := make([]string, 0, 16)

	const transfers = 16
	fullPipeline := (4*constants.UploadQueueDepthPerWorker + constants.MaxThreadsPerFile +
		constants.UploadPipelineTransientParts) * int64(constants.MaxChunkSize)

	for i := 0; i < transfers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("concurrent-%d", i)
			plan, err := mgr.PlanUpload(id, UploadPlanRequest{FileSize: 20 * gib, Threads: 4, Limits: s3Limits})
			if err != nil {
				return
			}
			mu.Lock()
			reserved += plan.inFlightBytes()
			ids = append(ids, id)
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	if len(ids) != transfers {
		t.Fatalf("%d of %d concurrent plans failed; contention must narrow uploads, not refuse them", transfers-len(ids), transfers)
	}
	// The bookkeeping has to add up exactly under contention, which is the part
	// a race between planners would break.
	if got := mgr.GetAvailableUploadMemory(); got != budget-reserved {
		t.Errorf("available memory = %d, want %d", got, budget-reserved)
	}
	// Sixteen uploads each claiming a full pipeline would be well past the budget;
	// sharing it is what keeps the total near one machine's worth.
	if reserved >= transfers*fullPipeline {
		t.Errorf("concurrent plans reserved %d, as much as %d unshared pipelines (%d)",
			reserved, transfers, transfers*fullPipeline)
	}

	for _, id := range ids {
		mgr.ReleaseUploadPlan(id)
	}
	if got := mgr.GetAvailableUploadMemory(); got != budget {
		t.Errorf("after releasing everything, available memory = %d, want the full %d", got, int64(budget))
	}
}

// TestPlanUploadBatchOnConstrainedMachine is the regression guard for the shape
// of a real batch upload. getAvailableMemory() reports roughly 3 GB on any Unix
// host, and a batch of multi-GB files is the common case; none of them may be
// refused, and none of their part sizes may move from what shipped before.
func TestPlanUploadBatchOnConstrainedMachine(t *testing.T) {
	const budget = 3 * gib // what memory_unix.go reports on any Unix machine
	const fileSize = 2 * gib
	const perFileThreads = 3 // a 16-thread pool split across five files

	mgr := planTestManager(t, budget)
	wantPartSize := calculateDynamicChunkSize(fileSize, constants.MaxThreadsPerFile, uint64(budget))

	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("batch-%d", i)
		plan, err := mgr.PlanUpload(id, UploadPlanRequest{FileSize: fileSize, Threads: perFileThreads, Limits: s3Limits})
		if err != nil {
			t.Fatalf("file %d of a five-file batch was refused: %v", i, err)
		}
		if plan.PartSize != wantPartSize {
			t.Errorf("file %d: PartSize = %d, want the unchanged %d", i, plan.PartSize, wantPartSize)
		}
		if plan.QueueDepth < 1 {
			t.Errorf("file %d: plan is not runnable: queue %d", i, plan.QueueDepth)
		}
		// No file in the batch may be left single-threaded for its whole transfer:
		// the cap is frozen at plan time and cannot recover as the batch drains.
		wantMinWorkers := perFileThreads
		if wantMinWorkers > constants.UploadMinThrottledWorkers {
			wantMinWorkers = constants.UploadMinThrottledWorkers
		}
		if plan.WorkerCap < wantMinWorkers {
			t.Errorf("file %d: WorkerCap = %d, want at least %d", i, plan.WorkerCap, wantMinWorkers)
		}
		t.Logf("file %d: part size %d MB, queue %d, workers %d, %d MB reserved",
			i, plan.PartSize/mib, plan.QueueDepth, plan.WorkerCap, plan.inFlightBytes()/mib)
	}
}

// TestPlanUploadWorkerFloor pins the conditional floor from both sides: a
// contended transfer keeps a usable worker count where the machine can hold one,
// and falls back to a single worker where a wider floor would refuse an upload
// that works today.
func TestPlanUploadWorkerFloor(t *testing.T) {
	t.Run("contention cannot take the affordable floor away", func(t *testing.T) {
		const budget = 8 * gib
		for _, threads := range []int{1, 2, 3, 4, 8, 16} {
			// Zero remaining budget is the worst contention there is.
			mgr := planTestManager(t, budget)
			exhaustBudget(t, mgr)

			plan := planOne(t, mgr, "squeezed", 20*gib, threads, s3Limits)
			want := wantWorkerFloor(threads, plan.PartSize, budget)
			if threads > 1 && want == 1 {
				t.Fatalf("threads=%d: test setup does not afford a wider floor", threads)
			}
			if plan.WorkerCap != want {
				t.Errorf("threads=%d: WorkerCap = %d under full contention, want the floor %d", threads, plan.WorkerCap, want)
			}
			if plan.QueueDepth != 1 {
				t.Errorf("threads=%d: queue = %d, want it given up before the workers", threads, plan.QueueDepth)
			}
		}
	})

	t.Run("the floor gives way before an upload is refused", func(t *testing.T) {
		// The largest file each backend supports, and the customer's 4.2 TB, are
		// decided by the one-worker minimum. A wider floor would shrink both.
		for _, limits := range []UploadLimits{s3Limits, azureLimits} {
			mgr := planTestManager(t, 64*gib)
			maxSupported := limits.MaxParts * limits.MaxPartSize
			if _, err := mgr.PlanUpload("max", UploadPlanRequest{FileSize: maxSupported, Threads: 8, Limits: limits}); err != nil {
				t.Errorf("%s: the largest supported file no longer plans: %v", limits.StorageType, err)
			}
		}

		// The cases an unconditional throttled floor would refuse: the machine
		// holds six parts but not nine. Both plan today, so both must still plan.
		refusedByAWiderFloor := []struct {
			name   string
			budget int64
			size   int64
		}{
			// 4.2 TB needs ~401 MB parts on S3, on the ~3 GB budget
			// getAvailableMemory() reports on any Unix host.
			{"the 4.2 TB upload from the report", 3 * gib, 4_200_000_000_000},
			// 625 GB on a 512 MB machine: the memory clamp wants 16 MB parts and
			// the part-count floor raises them to 64 MB. Reading that as "normal
			// sized" purely from its size would refuse it.
			{"a floored part that lands below MaxChunkSize", 512 * mib, constants.MaxS3UploadParts * constants.MaxChunkSize},
		}

		for _, tt := range refusedByAWiderFloor {
			t.Run(tt.name, func(t *testing.T) {
				mgr := planTestManager(t, tt.budget)
				plan := planOne(t, mgr, "narrow", tt.size, 8, s3Limits)

				if minimumBytes(constants.UploadMinThrottledWorkers, plan.PartSize) <= tt.budget {
					t.Fatalf("test setup: %d MB affords the throttled floor at %d MB parts, so nothing is being proven",
						tt.budget/mib, plan.PartSize/mib)
				}
				if plan.QueueDepth < 1 || plan.WorkerCap < 1 {
					t.Errorf("plan is not runnable: queue %d, workers %d", plan.QueueDepth, plan.WorkerCap)
				}
				if got := plan.inFlightBytes(); got > tt.budget {
					t.Errorf("in-flight budget %d exceeds the %d available", got, tt.budget)
				}
			})
		}
	})
}

// TestReleaseTransferReleasesUploadPlan checks the backstop: a caller that only
// completes the transfer still gives the memory back.
func TestReleaseTransferReleasesUploadPlan(t *testing.T) {
	const budget = 8 * gib
	mgr := planTestManager(t, budget)

	mgr.AllocateForTransfer("t1", 20*gib, 1)
	planOne(t, mgr, "t1", 20*gib, 4, s3Limits)
	if mgr.GetAvailableUploadMemory() == budget {
		t.Fatal("planning did not reserve anything")
	}

	mgr.ReleaseTransfer("t1")
	if got := mgr.GetAvailableUploadMemory(); got != budget {
		t.Errorf("available memory = %d after ReleaseTransfer, want the full %d", got, int64(budget))
	}
}

// TestPlanUploadReplacesOwnReservation guards against a transfer that plans
// twice quietly double-charging the budget.
func TestPlanUploadReplacesOwnReservation(t *testing.T) {
	const budget = 8 * gib
	mgr := planTestManager(t, budget)

	first := planOne(t, mgr, "same", 20*gib, 4, s3Limits)
	second := planOne(t, mgr, "same", 20*gib, 4, s3Limits)

	if first != second {
		t.Errorf("replanning changed the plan: %+v then %+v", first, second)
	}
	if got, want := mgr.GetAvailableUploadMemory(), budget-second.inFlightBytes(); got != want {
		t.Errorf("available memory = %d, want %d (one reservation, not two)", got, want)
	}
}

// TestPlanUploadShrinkOrder pins which dimension gives way first when part size
// grows: the queue, then the workers, never the part size.
func TestPlanUploadShrinkOrder(t *testing.T) {
	const size = 4_200_000_000_000 // ~401 MB parts on S3

	mgr := planTestManager(t, 64*gib)
	roomy := planOne(t, mgr, "roomy", size, 8, s3Limits)

	tight := planTestManager(t, 8*gib)
	squeezed := planOne(t, tight, "squeezed", size, 8, s3Limits)

	if squeezed.PartSize != roomy.PartSize {
		t.Errorf("part size moved with the budget: %d vs %d — the floor must win", squeezed.PartSize, roomy.PartSize)
	}
	if squeezed.QueueDepth >= roomy.QueueDepth {
		t.Errorf("queue did not shrink first: %d vs %d", squeezed.QueueDepth, roomy.QueueDepth)
	}
	if squeezed.QueueDepth < 1 || squeezed.WorkerCap < 1 {
		t.Errorf("plan is not runnable: queue %d, workers %d", squeezed.QueueDepth, squeezed.WorkerCap)
	}
	if squeezed.QueueDepth > 1 && squeezed.WorkerCap < roomy.WorkerCap {
		t.Errorf("workers shrank while the queue still had %d slots to give up", squeezed.QueueDepth)
	}

	// Same order in the unfloored regime, except the workers stop at the
	// throttled floor rather than at one.
	small := planTestManager(t, 8*gib)
	exhaustBudget(t, small)
	smallSqueezed := planOne(t, small, "small", 20*gib, 8, s3Limits)

	if smallSqueezed.PartSize > constants.MaxChunkSize {
		t.Fatalf("test setup: part size %d is not in the unfloored regime", smallSqueezed.PartSize)
	}
	if smallSqueezed.QueueDepth != 1 {
		t.Errorf("unfloored: queue = %d, want it given up first", smallSqueezed.QueueDepth)
	}
	if smallSqueezed.WorkerCap != constants.UploadMinThrottledWorkers {
		t.Errorf("unfloored: WorkerCap = %d, want the throttled floor %d",
			smallSqueezed.WorkerCap, constants.UploadMinThrottledWorkers)
	}
}

// TestPlanUploadRejectsUnsetLimits guards the planner against a provider that
// reports no ceilings, which would silently reinstate the unbounded part count.
func TestPlanUploadRejectsUnsetLimits(t *testing.T) {
	mgr := planTestManager(t, 8*gib)
	if _, err := mgr.PlanUpload("unset", UploadPlanRequest{FileSize: gib, Threads: 4}); err == nil {
		t.Fatal("expected planning to fail when the backend reports no limits")
	}
}
