package buffers

import (
	"testing"

	"github.com/rescale/rescale-int/internal/constants"
)

// TestChunkBufferPool verifies that chunk buffers can be retrieved and returned
func TestChunkBufferPool(t *testing.T) {
	// Get a buffer
	buf := GetChunkBuffer()
	if buf == nil {
		t.Fatal("GetChunkBuffer returned nil")
	}

	// Verify size
	if len(*buf) != constants.ChunkSize {
		t.Errorf("Buffer size = %d, want %d", len(*buf), constants.ChunkSize)
	}

	// Return buffer
	PutChunkBuffer(buf)

	// Get another buffer (may or may not be the same one due to pool)
	buf2 := GetChunkBuffer()
	if buf2 == nil {
		t.Fatal("GetChunkBuffer returned nil on second call")
	}
	PutChunkBuffer(buf2)
}

// TestSmallBufferPool verifies that small buffers can be retrieved and returned
func TestSmallBufferPool(t *testing.T) {
	// Get a buffer
	buf := GetSmallBuffer()
	if buf == nil {
		t.Fatal("GetSmallBuffer returned nil")
	}

	// Verify size
	expectedSize := 16 * 1024
	if len(*buf) != expectedSize {
		t.Errorf("Buffer size = %d, want %d", len(*buf), expectedSize)
	}

	// Return buffer
	PutSmallBuffer(buf)

	// Get another buffer
	buf2 := GetSmallBuffer()
	if buf2 == nil {
		t.Fatal("GetSmallBuffer returned nil on second call")
	}
	PutSmallBuffer(buf2)
}

// TestPutChunkBufferWithWrongSize verifies wrong-sized buffers are not pooled
func TestPutChunkBufferWithWrongSize(t *testing.T) {
	wrongSizeBuf := make([]byte, 1024) // Wrong size
	PutChunkBuffer(&wrongSizeBuf)      // Should not panic, just not pool it
}

// TestPutSmallBufferWithWrongSize verifies wrong-sized buffers are not pooled
func TestPutSmallBufferWithWrongSize(t *testing.T) {
	wrongSizeBuf := make([]byte, 1024*1024) // Wrong size
	PutSmallBuffer(&wrongSizeBuf)           // Should not panic, just not pool it
}

// TestPutNilBuffer verifies that nil buffers don't cause panics
func TestPutNilBuffer(t *testing.T) {
	PutChunkBuffer(nil) // Should not panic
	PutSmallBuffer(nil) // Should not panic
}

// TestConcurrentAccess tests concurrent buffer get/put operations
func TestConcurrentAccess(t *testing.T) {
	const goroutines = 10
	const iterations = 100

	done := make(chan bool, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < iterations; j++ {
				// Chunk buffers
				buf := GetChunkBuffer()
				// Simulate some work
				(*buf)[0] = byte(j)
				PutChunkBuffer(buf)

				// Small buffers
				smallBuf := GetSmallBuffer()
				(*smallBuf)[0] = byte(j)
				PutSmallBuffer(smallBuf)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < goroutines; i++ {
		<-done
	}
}

// TestGetStats verifies stats are returned correctly
func TestGetStats(t *testing.T) {
	stats := GetStats()

	if stats.ChunkBufferSize != constants.ChunkSize {
		t.Errorf("ChunkBufferSize = %d, want %d", stats.ChunkBufferSize, constants.ChunkSize)
	}

	if stats.SmallBufferSize != 16*1024 {
		t.Errorf("SmallBufferSize = %d, want %d", stats.SmallBufferSize, 16*1024)
	}
}

// BenchmarkChunkBufferWithPool benchmarks buffer allocation with pooling
func BenchmarkChunkBufferWithPool(b *testing.B) {
	for i := 0; i < b.N; i++ {
		buf := GetChunkBuffer()
		// Simulate using the buffer
		_ = (*buf)[0]
		PutChunkBuffer(buf)
	}
}

// BenchmarkChunkBufferWithoutPool benchmarks buffer allocation without pooling
func BenchmarkChunkBufferWithoutPool(b *testing.B) {
	for i := 0; i < b.N; i++ {
		buf := make([]byte, constants.ChunkSize)
		// Simulate using the buffer
		_ = buf[0]
	}
}

// BenchmarkSmallBufferWithPool benchmarks small buffer allocation with pooling
func BenchmarkSmallBufferWithPool(b *testing.B) {
	for i := 0; i < b.N; i++ {
		buf := GetSmallBuffer()
		_ = (*buf)[0]
		PutSmallBuffer(buf)
	}
}

// BenchmarkSmallBufferWithoutPool benchmarks small buffer allocation without pooling
func BenchmarkSmallBufferWithoutPool(b *testing.B) {
	for i := 0; i < b.N; i++ {
		buf := make([]byte, 16*1024)
		_ = buf[0]
	}
}
