package transfer

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"testing"
	"testing/synctest"
	"time"
)

// goroutineBaseline samples the goroutine count after letting whatever earlier
// tests started wind down, so the number belongs to this test.
func goroutineBaseline(t *testing.T) int {
	t.Helper()
	runtime.Gosched()
	time.Sleep(20 * time.Millisecond)
	return runtime.NumGoroutine()
}

// waitForGoroutines waits for the count to fall back to baseline. A stage that
// returns while a goroutine of its own is still parked on a channel shows up
// here and nowhere else: the returned error looks the same either way.
func waitForGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%d goroutines are still running, want back down to the %d this started with",
		runtime.NumGoroutine(), baseline)
}

// TestRunPartPipelineJoinsProducerOnCancellation covers the leak a failed part
// used to leave behind. The producer's send into the job queue was not
// cancellation-aware and the wait group covered the workers only, so once a
// staging failure cancelled the operation and the workers exited, nobody was
// left to drain the queue — the producer stayed parked on its send, holding its
// part buffers, while the pipeline returned. Every retry of the upload left
// another one.
//
// The queue here is one deep and the only worker fails its first part, so the
// producer is certain to be blocked on a send when the cancellation lands.
func TestRunPartPipelineJoinsProducerOnCancellation(t *testing.T) {
	const partSize = int64(16)
	const totalParts = int64(64)
	source := bytes.NewReader(make([]byte, partSize*totalParts))

	staged := 0
	cfg := PartPipelineConfig{
		Reader:      source,
		PartSize:    partSize,
		TotalParts:  totalParts,
		Concurrency: 1,
		QueueDepth:  1,
		WorkerLabel: "test staging worker",
		StagePart: func(ctx context.Context, part PartAssignment) (string, error) {
			staged++
			// Long enough for the producer to fill the queue and park on the
			// send it cannot complete, which is the state the leak needs.
			time.Sleep(50 * time.Millisecond)
			return "", fmt.Errorf("part %d was refused by the backend", part.Index)
		},
		RecordPart: func(int64, string) {},
		SaveState:  func(int64, int) {},
	}

	baseline := goroutineBaseline(t)

	uploaded, err := RunPartPipeline(context.Background(), cfg)
	if err == nil {
		t.Fatalf("RunPartPipeline returned %d bytes, want the staging failure", uploaded)
	}
	if staged != 1 {
		t.Errorf("StagePart was called %d times, want the one failing part", staged)
	}

	waitForGoroutines(t, baseline)
}

// The success path has to keep working: the producer joins after the last part,
// and every part still reaches the backend in order.
func TestRunPartPipelineStagesEveryPart(t *testing.T) {
	const partSize = int64(16)
	const totalParts = int64(5)
	payload := objectOfSize(int(partSize * totalParts))

	staged := make(map[int64][]byte)
	recorded := make(map[int64]string)
	cfg := PartPipelineConfig{
		Reader:      bytes.NewReader(payload),
		PartSize:    partSize,
		TotalParts:  totalParts,
		Concurrency: 1, // one worker, so the map needs no lock
		QueueDepth:  2,
		WorkerLabel: "test staging worker",
		StagePart: func(ctx context.Context, part PartAssignment) (string, error) {
			data := make([]byte, len(part.Data))
			copy(data, part.Data)
			staged[part.Index] = data
			return fmt.Sprintf("tag-%d", part.Index), nil
		},
		RecordPart: func(index int64, tag string) { recorded[index] = tag },
		SaveState:  func(int64, int) {},
	}

	baseline := goroutineBaseline(t)

	uploaded, err := RunPartPipeline(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunPartPipeline: %v", err)
	}
	if uploaded != int64(len(payload)) {
		t.Errorf("uploaded = %d bytes, want %d", uploaded, len(payload))
	}
	if len(staged) != int(totalParts) || len(recorded) != int(totalParts) {
		t.Fatalf("staged %d parts and recorded %d, want %d of each", len(staged), len(recorded), totalParts)
	}
	for i := int64(0); i < totalParts; i++ {
		want := payload[i*partSize : (i+1)*partSize]
		if !bytes.Equal(staged[i], want) {
			t.Errorf("part %d does not carry its slice of the file", i)
		}
	}

	waitForGoroutines(t, baseline)
}

// The concurrent pipeline checkpoints through CheckpointThrottle, like the
// other upload paths: the first staged part at once, then no more than one per
// interval, and whatever is owed as it returns, a failed run included, since
// every part it recorded is one the backend accepted.
func TestRunPartPipelineCheckpointsThroughTheThrottle(t *testing.T) {
	defer func(interval time.Duration) { CheckpointInterval = interval }(CheckpointInterval)
	CheckpointInterval = time.Hour

	const partSize = int64(16)
	for _, tc := range []struct {
		name    string
		failAt  int64 // index of the part the backend refuses; -1 for none
		wantEnd int64 // bytes the last checkpoint records
	}{
		{"a run that completes", -1, 40 * partSize},
		{"a run that fails", 29, 29 * partSize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var saved []int64
			_, err := RunPartPipeline(context.Background(), PartPipelineConfig{
				Reader:      bytes.NewReader(make([]byte, 40*partSize)),
				PartSize:    partSize,
				TotalParts:  40,
				Concurrency: 1,
				QueueDepth:  1,
				WorkerLabel: "test staging worker",
				StagePart: func(_ context.Context, part PartAssignment) (string, error) {
					if part.Index == tc.failAt {
						return "", fmt.Errorf("part %d was refused", part.Index)
					}
					return fmt.Sprintf("tag-%d", part.Index), nil
				},
				RecordPart: func(int64, string) {},
				SaveState:  func(uploaded int64, _ int) { saved = append(saved, uploaded) },
			})
			if (err != nil) != (tc.failAt >= 0) {
				t.Fatalf("RunPartPipeline: %v", err)
			}
			if len(saved) != 2 || saved[0] != partSize || saved[1] != tc.wantEnd {
				t.Errorf("checkpoints recorded %v bytes, want [%d %d]: the first part, then what was owed at the end", saved, partSize, tc.wantEnd)
			}
		})
	}
}

// TestCheckpointThrottle pins the policy in fake time: writes are bounded, an
// upload that keeps moving is checkpointed again once the interval has passed,
// and one that stalls holds its owed checkpoint until the next Offer or Flush.
func TestCheckpointThrottle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		saves := 0
		throttle := NewCheckpointThrottle(func() { saves++ })
		for range 5 {
			throttle.Offer()
		}
		if saves != 1 {
			t.Fatalf("five offers at once wrote %d checkpoints, want the first only", saves)
		}
		time.Sleep(CheckpointInterval)
		throttle.Offer()
		if saves != 2 {
			t.Fatalf("an offer an interval later wrote %d checkpoints in all, want 2", saves)
		}
		throttle.Offer()
		time.Sleep(time.Hour) // a stall: nothing writes what is owed
		if saves != 2 {
			t.Fatalf("a stall wrote %d checkpoints in all, want 2", saves)
		}
		throttle.Flush()
		throttle.Flush()
		if saves != 3 {
			t.Fatalf("flushing one owed checkpoint twice wrote %d in all, want 3", saves)
		}
	})
}
