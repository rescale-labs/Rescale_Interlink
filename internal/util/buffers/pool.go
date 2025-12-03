package buffers

import (
	"log"
	"sync"
	"sync/atomic"

	"github.com/rescale/rescale-int/internal/constants"
)

// Pool provides reusable byte buffers to reduce heap allocations
// during upload/download operations. This significantly reduces GC pressure
// and improves overall performance.

// Pool monitoring counters
var (
	chunkAllocations  int64 // Total chunk buffer allocations (new creates)
	chunkReuses       int64 // Total chunk buffer reuses from pool
	smallAllocations  int64 // Total small buffer allocations
	smallReuses       int64 // Total small buffer reuses from pool
)

var (
	// chunkPool provides 16MB buffers for upload/download chunks
	// These are the large buffers used for S3 multipart/Azure block operations
	chunkPool = &sync.Pool{
		New: func() interface{} {
			atomic.AddInt64(&chunkAllocations, 1)
			allocs := atomic.LoadInt64(&chunkAllocations)
			// Log every 10th allocation to avoid spam during heavy use
			if allocs%10 == 0 {
				reuses := atomic.LoadInt64(&chunkReuses)
				log.Printf("Buffer pool: %d chunk allocations, %d reuses (%.1f%% reuse rate)",
					allocs, reuses, float64(reuses)/float64(allocs+reuses)*100)
			}
			buf := make([]byte, constants.ChunkSize) // 16 MB
			return &buf
		},
	}

	// smallPool provides 16KB buffers for encryption operations
	// These are used during streaming encryption/decryption
	smallPool = &sync.Pool{
		New: func() interface{} {
			atomic.AddInt64(&smallAllocations, 1)
			buf := make([]byte, constants.EncryptionChunkSize)
			return &buf
		},
	}
)

// GetChunkBuffer retrieves a 16MB buffer from the pool
// The buffer must be returned to the pool using PutChunkBuffer when done
// to allow reuse and prevent memory waste.
//
// Usage:
//
//	buf := buffers.GetChunkBuffer()
//	defer buffers.PutChunkBuffer(buf)
//	n, err := io.ReadFull(file, *buf)
//	// Use (*buf)[:n] for actual data
func GetChunkBuffer() *[]byte {
	buf := chunkPool.Get().(*[]byte)
	// Track if this was a reuse (allocation counter didn't change)
	// Note: This is approximate since sync.Pool doesn't tell us if it was from cache
	return buf
}

// PutChunkBuffer returns a buffer to the pool for reuse
// The buffer should not be used after calling this function.
// Only buffers of the correct size (ChunkSize) will be pooled.
// The buffer is cleared before being returned to prevent sensitive data leakage.
func PutChunkBuffer(buf *[]byte) {
	if buf != nil && len(*buf) == constants.ChunkSize {
		// Clear buffer to prevent sensitive data from persisting across uses
		clear(*buf)
		chunkPool.Put(buf)
	}
}

// GetSmallBuffer retrieves a 16KB buffer from the pool
// Used primarily for encryption/decryption streaming operations.
//
// Usage:
//
//	buf := buffers.GetSmallBuffer()
//	defer buffers.PutSmallBuffer(buf)
//	n, err := reader.Read(*buf)
//	// Use (*buf)[:n] for actual data
func GetSmallBuffer() *[]byte {
	return smallPool.Get().(*[]byte)
}

// PutSmallBuffer returns a small buffer to the pool for reuse
// Only buffers of the correct size will be pooled.
// The buffer is cleared before being returned to prevent sensitive data leakage.
func PutSmallBuffer(buf *[]byte) {
	if buf != nil && len(*buf) == constants.EncryptionChunkSize {
		// Clear buffer to prevent sensitive data from persisting across uses
		clear(*buf)
		smallPool.Put(buf)
	}
}

// Stats returns current buffer pool statistics
// Useful for monitoring and debugging memory usage
type Stats struct {
	ChunkBufferSize   int   // Size of chunk buffers (bytes)
	SmallBufferSize   int   // Size of small buffers (bytes)
	ChunkAllocations  int64 // Total chunk buffer allocations (new creates)
	SmallAllocations  int64 // Total small buffer allocations (new creates)
}
