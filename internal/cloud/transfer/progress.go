// Shared progress-reporting reader wrappers for S3 and Azure providers.
package transfer

import (
	"bytes"
	"io"
)

// ProgressReaderThreshold is the minimum bytes to accumulate before reporting
// progress to the callback. 1MB provides smooth progress without excessive updates.
const ProgressReaderThreshold = 1 * 1024 * 1024 // 1MB

// ProgressReader wraps an io.Reader and reports bytes read to a callback.
// Uses threshold-based reporting to avoid jumpy progress from many small reads.
// Flushes remaining accumulated bytes on EOF.
type ProgressReader struct {
	Reader      io.Reader
	Callback    func(bytesRead int64)
	Accumulated int64 // Bytes accumulated since last callback
	Threshold   int64 // Report every N bytes (use ProgressReaderThreshold)
}

func (pr *ProgressReader) Read(p []byte) (n int, err error) {
	n, err = pr.Reader.Read(p)
	if n > 0 && pr.Callback != nil {
		pr.Accumulated += int64(n)
		if pr.Accumulated >= pr.Threshold || err == io.EOF {
			pr.Callback(pr.Accumulated)
			pr.Accumulated = 0
		}
	}
	return n, err
}

// UploadProgressReader wraps a *bytes.Reader with progress tracking for uploads.
// Implements io.ReadSeeker so cloud SDKs can rewind the stream on retry.
// On Seek (retry), rolls back reported progress to prevent double-counting.
type UploadProgressReader struct {
	Reader      *bytes.Reader
	Callback    func(bytesRead int64)
	Accumulated int64 // Bytes accumulated since last callback
	Threshold   int64 // Report every N bytes (use ProgressReaderThreshold)
	Reported    int64 // Total bytes reported to callback (for rollback on retry)
}

func (pr *UploadProgressReader) Read(p []byte) (n int, err error) {
	n, err = pr.Reader.Read(p)
	if n > 0 && pr.Callback != nil {
		pr.Accumulated += int64(n)
		if pr.Accumulated >= pr.Threshold || err == io.EOF {
			pr.Reported += pr.Accumulated
			pr.Callback(pr.Accumulated)
			pr.Accumulated = 0
		}
	}
	// Flush remaining accumulated bytes on EOF even when n == 0
	if err == io.EOF && pr.Accumulated > 0 && pr.Callback != nil {
		pr.Reported += pr.Accumulated
		pr.Callback(pr.Accumulated)
		pr.Accumulated = 0
	}
	return n, err
}

func (pr *UploadProgressReader) Seek(offset int64, whence int) (int64, error) {
	// Roll back any progress reported during the failed attempt
	if pr.Reported > 0 && pr.Callback != nil {
		pr.Callback(-pr.Reported)
	}
	pr.Accumulated = 0
	pr.Reported = 0
	return pr.Reader.Seek(offset, whence)
}

// Close implements io.Closer (no-op for *bytes.Reader).
func (pr *UploadProgressReader) Close() error {
	return nil
}
