package diskspace

import (
	"fmt"
	"testing"
)

func TestCheckAvailableSpace(t *testing.T) {
	// Test with /tmp which should exist and have some space
	tmpPath := "/tmp/test_disk_check.tmp"

	// Test 1: Small file (should pass)
	t.Run("SmallFile", func(t *testing.T) {
		err := CheckAvailableSpace(tmpPath, 1024, 1.1) // 1KB
		if err != nil {
			t.Errorf("Expected no error for small file, got: %v", err)
		}
	})

	// Test 2: Very large file (likely to fail on most systems)
	t.Run("VeryLargeFile", func(t *testing.T) {
		// 100TB - should exceed available space on most systems
		err := CheckAvailableSpace(tmpPath, 100*1024*1024*1024*1024, 1.1)
		if err == nil {
			t.Log("Warning: 100TB file check passed - system has extraordinary disk space")
		} else if !IsInsufficientSpaceError(err) {
			t.Errorf("Expected InsufficientSpaceError, got: %T", err)
		}
	})

	// Test 3: Safety margin calculation
	t.Run("SafetyMargin", func(t *testing.T) {
		// Get actual available space
		available := GetAvailableSpace(tmpPath)
		if available == 0 {
			t.Skip("Could not determine available space")
		}

		// Request slightly more than half available space
		halfSpace := available / 2

		// Should succeed with space available
		err := CheckAvailableSpace(tmpPath, halfSpace, 1.1)
		if err != nil {
			t.Errorf("Expected to have space for half available (%d bytes), got error: %v", halfSpace, err)
		}

		// Should potentially fail with 90% of available space (with margin)
		ninetyPercent := (available * 9) / 10
		err = CheckAvailableSpace(tmpPath, ninetyPercent, 1.1)
		if err != nil {
			if !IsInsufficientSpaceError(err) {
				t.Errorf("Expected InsufficientSpaceError, got: %T", err)
			}
		}
	})
}

func TestGetAvailableSpace(t *testing.T) {
	// Test with /tmp
	available := GetAvailableSpace("/tmp/test.txt")
	if available == 0 {
		t.Error("Expected non-zero available space for /tmp")
	}

	t.Logf("Available space in /tmp: %.2f GB", float64(available)/(1024*1024*1024))
}

func TestIsInsufficientSpaceError(t *testing.T) {
	// Test with actual InsufficientSpaceError
	err := &InsufficientSpaceError{
		Path:           "/tmp/test.txt",
		RequiredBytes:  1000,
		AvailableBytes: 500,
	}

	if !IsInsufficientSpaceError(err) {
		t.Error("Expected IsInsufficientSpaceError to return true")
	}

	// Test with other error
	otherErr := fmt.Errorf("some other error")
	if IsInsufficientSpaceError(otherErr) {
		t.Error("Expected IsInsufficientSpaceError to return false for non-disk-space error")
	}

	// Test with nil
	if IsInsufficientSpaceError(nil) {
		t.Error("Expected IsInsufficientSpaceError to return false for nil")
	}
}

func TestInsufficientSpaceErrorMessage(t *testing.T) {
	err := &InsufficientSpaceError{
		Path:           "/tmp/test.txt",
		RequiredBytes:  1024 * 1024 * 100, // 100MB
		AvailableBytes: 1024 * 1024 * 50,  // 50MB
	}

	msg := err.Error()
	if msg == "" {
		t.Error("Expected non-empty error message")
	}

	// Check that message contains key information
	if !contains(msg, "/tmp/test.txt") {
		t.Error("Error message should contain path")
	}
	if !contains(msg, "100.00") {
		t.Error("Error message should contain required space in MB")
	}
	if !contains(msg, "50.00") {
		t.Error("Error message should contain available space in MB")
	}

	t.Logf("Error message: %s", msg)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && s != "" && substr != "" &&
		(s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Example test demonstrating usage
func ExampleCheckAvailableSpace() {
	// Check if there's space for a 10MB file
	err := CheckAvailableSpace("/tmp/myfile.dat", 10*1024*1024, 1.15)
	if err != nil {
		fmt.Printf("Not enough space: %v\n", err)
	} else {
		fmt.Println("Sufficient space available")
	}
}
