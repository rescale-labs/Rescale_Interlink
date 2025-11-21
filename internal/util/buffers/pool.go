package buffers

import (
	"sync"

	"github.com/rescale/rescale-int/internal/constants"
)

// Pool provides reusable byte buffers to reduce heap allocations
// during upload/download operations. This significantly reduces GC pressure
// and improves overall performance.

var (
	// chunkPool provides 16MB buffers for upload/download chunks
	// These are the large buffers used for S3 multipart/Azure block operations
	chunkPool = &sync.Pool{
		New: func() interface{} {
			buf := make([]byte, constants.ChunkSize) // 16 MB
			return &buf
		},
	}

	// smallPool provides 16KB buffers for encryption operations
	// These are used during streaming encryption/decryption
	smallPool = &sync.Pool{
		New: func() interface{} {
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
	return chunkPool.Get().(*[]byte)
}

// PutChunkBuffer returns a buffer to the pool for reuse
// The buffer should not be used after calling this function.
// Only buffers of the correct size (ChunkSize) will be pooled.
func PutChunkBuffer(buf *[]byte) {
	if buf != nil && len(*buf) == constants.ChunkSize {
		// Clear any residual data for security (optional, adds overhead)
		// For performance, we skip this and trust callers to use [:n]
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
func PutSmallBuffer(buf *[]byte) {
	if buf != nil && len(*buf) == constants.EncryptionChunkSize {
		smallPool.Put(buf)
	}
}

// Stats returns current buffer pool statistics
// Useful for monitoring and debugging memory usage
type Stats struct {
	ChunkBufferSize int // Size of chunk buffers (bytes)
	SmallBufferSize int // Size of small buffers (bytes)
}

// GetStats returns statistics about the buffer pools
func GetStats() Stats {
	return Stats{
		ChunkBufferSize: constants.ChunkSize,
		SmallBufferSize: 16 * 1024,
	}
}
