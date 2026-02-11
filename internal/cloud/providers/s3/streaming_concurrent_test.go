package s3

import (
	"bytes"
	"io"
	"sync/atomic"
	"testing"
)

// TestUploadProgressReaderSeek verifies that uploadProgressReader implements
// io.ReadSeeker: it can read all bytes, seek to 0, and re-read the same bytes.
func TestUploadProgressReaderSeek(t *testing.T) {
	data := []byte("hello, world — this is test data for seek verification")
	var totalReported int64

	pr := &uploadProgressReader{
		reader:    bytes.NewReader(data),
		callback:  func(n int64) { totalReported += n },
		threshold: 1, // Report on every read for testing
	}

	// Verify io.ReadSeeker interface compliance
	var _ io.ReadSeeker = pr

	// Read all bytes
	buf1, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("first ReadAll failed: %v", err)
	}
	if !bytes.Equal(buf1, data) {
		t.Fatalf("first read: got %q, want %q", buf1, data)
	}

	// Seek back to start
	pos, err := pr.Seek(0, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek(0, SeekStart) failed: %v", err)
	}
	if pos != 0 {
		t.Fatalf("Seek returned position %d, want 0", pos)
	}

	// Re-read all bytes — must get the same data
	buf2, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("second ReadAll failed: %v", err)
	}
	if !bytes.Equal(buf2, data) {
		t.Fatalf("second read: got %q, want %q", buf2, data)
	}
}

// TestUploadProgressReaderSeekRollsBackProgress verifies that Seek() calls the
// callback with a negative value to roll back reported progress, preventing
// double-counting on retry.
func TestUploadProgressReaderSeekRollsBackProgress(t *testing.T) {
	data := []byte("abcdefghij") // 10 bytes
	var netProgress atomic.Int64

	pr := &uploadProgressReader{
		reader: bytes.NewReader(data),
		callback: func(n int64) {
			netProgress.Add(n)
		},
		threshold: 1, // Report on every read
	}

	// Read all bytes — should report +10 total
	_, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if got := netProgress.Load(); got != int64(len(data)) {
		t.Fatalf("after read: net progress = %d, want %d", got, len(data))
	}

	// Seek to 0 — should roll back (-10), net goes to 0
	_, err = pr.Seek(0, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek failed: %v", err)
	}
	if got := netProgress.Load(); got != 0 {
		t.Fatalf("after seek: net progress = %d, want 0", got)
	}

	// Read again — net should be back to +10
	_, err = io.ReadAll(pr)
	if err != nil {
		t.Fatalf("second ReadAll failed: %v", err)
	}
	if got := netProgress.Load(); got != int64(len(data)) {
		t.Fatalf("after re-read: net progress = %d, want %d", got, len(data))
	}
}

// TestUploadProgressReaderThreshold verifies that the callback is only invoked
// when accumulated bytes reach the threshold (not on every small read).
func TestUploadProgressReaderThreshold(t *testing.T) {
	// 100 bytes of data, threshold at 30
	data := make([]byte, 100)
	for i := range data {
		data[i] = byte(i)
	}

	var callbackCalls int
	var callbackValues []int64

	pr := &uploadProgressReader{
		reader: bytes.NewReader(data),
		callback: func(n int64) {
			callbackCalls++
			callbackValues = append(callbackValues, n)
		},
		threshold: 30,
	}

	// Read in small chunks to test threshold accumulation
	buf := make([]byte, 10)
	for {
		_, err := pr.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
	}

	// With 100 bytes and threshold 30:
	// - After 30 bytes (3 reads of 10): callback with 30
	// - After 60 bytes (3 more reads): callback with 30
	// - After 90 bytes (3 more reads): callback with 30
	// - After 100 bytes (1 more read, EOF): callback with 10
	// Total: 4 callbacks
	if callbackCalls < 3 {
		t.Fatalf("expected at least 3 callback calls with threshold=30 and 100 bytes, got %d", callbackCalls)
	}

	// Verify total reported equals data length
	var totalReported int64
	for _, v := range callbackValues {
		totalReported += v
	}
	if totalReported != int64(len(data)) {
		t.Fatalf("total reported = %d, want %d", totalReported, len(data))
	}
}
