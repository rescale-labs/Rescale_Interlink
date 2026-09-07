package transfer

import (
	"errors"
	"io"
	"testing"
)

// TestUploadAttemptProgressCountsEachByteOnce is the shape F18 describes: a
// part reports part of itself, fails, and is retried with a reader the failed
// one knows nothing about. The whole part must end up reported once.
func TestUploadAttemptProgressCountsEachByteOnce(t *testing.T) {
	part := make([]byte, 1000)
	var reported int64
	attempt := NewUploadAttemptProgress(func(n int64) { reported += n })

	// First attempt: 60% of the part goes out, then the connection drops.
	failing := attempt.NewReader(part)
	failing.Threshold = 1 // report every read, so the test does not depend on the 1MB threshold
	if _, err := io.CopyN(io.Discard, failing, 600); err != nil {
		t.Fatalf("partial read failed: %v", err)
	}
	if reported != 600 {
		t.Fatalf("the failed attempt reported %d bytes, want 600", reported)
	}

	// Second attempt: a new reader, because the SDK cannot re-send a drained body.
	succeeding := attempt.NewReader(part)
	succeeding.Threshold = 1
	if _, err := io.Copy(io.Discard, succeeding); err != nil {
		t.Fatalf("retry read failed: %v", err)
	}

	if reported != int64(len(part)) {
		t.Errorf("progress reported %d bytes for a %d-byte part, want each byte counted once",
			reported, len(part))
	}
}

// TestUploadAttemptProgressRollbackAfterExhaustedRetries covers the part that
// never goes up: nothing of it may be left in the total.
func TestUploadAttemptProgressRollbackAfterExhaustedRetries(t *testing.T) {
	part := make([]byte, 1000)
	var reported int64
	attempt := NewUploadAttemptProgress(func(n int64) { reported += n })

	reader := attempt.NewReader(part)
	reader.Threshold = 1
	if _, err := io.CopyN(io.Discard, reader, 400); err != nil {
		t.Fatalf("partial read failed: %v", err)
	}

	attempt.Rollback()

	if reported != 0 {
		t.Errorf("a part that was never uploaded left %d bytes in the total", reported)
	}
	// Rollback is idempotent: the caller may roll back and then be unwound again.
	attempt.Rollback()
	if reported != 0 {
		t.Errorf("a second rollback withdrew %d bytes that were never reported", reported)
	}
}

// TestUploadAttemptProgressWithoutCallback pins the no-progress path: the
// providers hand the same reader to the SDK whether or not anyone is watching.
func TestUploadAttemptProgressWithoutCallback(t *testing.T) {
	part := []byte("some ciphertext")
	attempt := NewUploadAttemptProgress(nil)

	reader := attempt.NewReader(part)
	var _ io.ReadSeekCloser = reader

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(got) != string(part) {
		t.Errorf("read %q, want %q", got, part)
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek failed: %v", err)
	}
	if err := reader.Close(); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("close failed: %v", err)
	}
	attempt.Rollback() // must not panic with no callback
}
