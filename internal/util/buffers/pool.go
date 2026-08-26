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

// Pool monitoring counters. sync.Pool does not report whether a Get was served
// from cache, so only fresh allocations can be counted.
var (
	chunkAllocations int64 // Total chunk buffer allocations (new creates)
	smallAllocations int64 // Total small buffer allocations (new creates)
)

var (
	// chunkPool provides buffers for upload/download chunks
	// Size is set by constants.ChunkSize (default 32 MB)
	// These are the large buffers used for S3 multipart/Azure block operations
	chunkPool = &sync.Pool{
		New: func() interface{} {
			atomic.AddInt64(&chunkAllocations, 1)
			allocs := atomic.LoadInt64(&chunkAllocations)
			// Log every 10th allocation to avoid spam during heavy use
			if allocs%10 == 0 {
				log.Printf("Buffer pool: %d chunk allocations", allocs)
			}
			buf := make([]byte, constants.ChunkSize)
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

// GetChunkBuffer retrieves a constants.ChunkSize (32 MB) buffer from the pool.
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
	return chunkPool.Get().(*[]byte)
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

// GetPartBuffer returns a buffer of exactly size bytes, plus the function that
// releases it.
//
// Readers must size their buffer to the part size the upload actually planned,
// not to the pool's fixed size: a buffer shorter than the part size ends every
// read early, and an upload loop that treats a short read as the final part then
// stops with most of the file unsent. Only a part size that matches the pool can
// use the pool; anything else is allocated for the life of the transfer.
func GetPartBuffer(size int64) ([]byte, func()) {
	if size == constants.ChunkSize {
		bufPtr := GetChunkBuffer()
		return *bufPtr, func() { PutChunkBuffer(bufPtr) }
	}
	return make([]byte, size), func() {}
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
	ChunkBufferSize  int   // Size of chunk buffers (bytes)
	SmallBufferSize  int   // Size of small buffers (bytes)
	ChunkAllocations int64 // Total chunk buffer allocations (new creates)
	SmallAllocations int64 // Total small buffer allocations (new creates)
}
