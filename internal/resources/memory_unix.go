//go:build darwin || linux

package resources

import (
	"runtime"

	"github.com/rescale/rescale-int/internal/constants"
)

// getAvailableMemory returns available system memory in bytes (Unix/Linux/macOS)
func getAvailableMemory() uint64 {
	// Get runtime memory stats
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// On Unix-like systems, we'll use a heuristic:
	// Assume we can use up to 75% of the total system memory minus what's currently allocated
	// This is a conservative estimate

	// Get total system memory (this is platform-specific, so we'll use a conservative default)
	// and estimate based on current heap allocation

	// Conservative estimate: 4GB total system memory minus current allocations
	totalSystemMemory := uint64(4 * 1024 * 1024 * 1024) // 4GB default
	currentlyAllocated := m.Alloc

	// Calculate available: 75% of (total - current allocations)
	if totalSystemMemory > currentlyAllocated {
		availableBytes := uint64(float64(totalSystemMemory-currentlyAllocated) * 0.75)

		// Ensure we have a reasonable minimum
		if availableBytes < constants.MinSystemMemory {
			availableBytes = constants.MinSystemMemory
		}

		// Cap at reasonable maximum
		if availableBytes > constants.MaxSystemMemory {
			availableBytes = constants.MaxSystemMemory
		}

		return availableBytes
	}

	// Fallback to conservative estimate
	return 2 * 1024 * 1024 * 1024 // 2GB
}
