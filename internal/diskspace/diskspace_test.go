package diskspace

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckAvailableSpace(t *testing.T) {
	tmpDir := t.TempDir()
	tmpPath := filepath.Join(tmpDir, "test_disk_check.tmp")

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
		// GetAvailableSpace takes the directory, not the file that will go in it.
		available := GetAvailableSpace(tmpDir)
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

// TestGetAvailableSpace verifies that GetAvailableSpace reports the filesystem
// of the directory it is handed, not the directory's parent. Reporting the parent
// misstates free space by an unbounded factor whenever the target directory is a
// mount point, which is how a download of a file that fits got refused.
func TestGetAvailableSpace(t *testing.T) {
	tmpDir := t.TempDir()

	available := GetAvailableSpace(tmpDir)
	if available == 0 {
		t.Fatalf("Expected non-zero available space for existing directory %s", tmpDir)
	}
	t.Logf("Available space in %s: %.2f GB", tmpDir, float64(available)/(1024*1024*1024))

	// A directory that does not exist has no filesystem to report. Applying
	// filepath.Dir first would silently answer with tmpDir's filesystem instead.
	missing := filepath.Join(tmpDir, "no-such-directory")
	if got := GetAvailableSpace(missing); got != 0 {
		t.Errorf("GetAvailableSpace(%q) = %d, want 0 — it is reporting the parent %q instead of the path it was given",
			missing, got, tmpDir)
	}
}

// TestCheckAvailableSpaceErrorStatesEnforcedRequirement verifies that a refusal
// reports the requirement it actually enforced, margin included.
//
// The reported requirement used to be rebuilt at the call site without the safety
// margin, so a refusal that hinged on the margin printed "need N MB, have M MB
// available" with N below M: a message that contradicted its own decision.
func TestCheckAvailableSpaceErrorStatesEnforcedRequirement(t *testing.T) {
	tmpDir := t.TempDir()
	available := GetAvailableSpace(tmpDir)
	if available == 0 {
		t.Skip("Could not determine available space")
	}

	// The legacy download path holds the encrypted and decrypted copies at once,
	// so it asks for fileSize*2 with a 15% margin. Size the file so free space
	// lands between the two: fileSize*2 fits, fileSize*2*1.15 does not.
	fileSize := int64(float64(available) / 2.1)
	required := fileSize * 2
	if required >= available {
		t.Fatalf("test setup: unmargined requirement %d should fit in %d available", required, available)
	}
	wantRequired := int64(float64(required) * 1.15)

	err := CheckAvailableSpace(filepath.Join(tmpDir, "download.dat"), required, 1.15)
	if err == nil {
		t.Fatalf("expected refusal: %d bytes plus a 15%% margin exceeds %d available", required, available)
	}
	spaceErr, ok := err.(*InsufficientSpaceError)
	if !ok {
		t.Fatalf("expected *InsufficientSpaceError, got %T: %v", err, err)
	}

	if spaceErr.RequiredBytes != wantRequired {
		t.Errorf("RequiredBytes = %d, want the margined requirement %d that was enforced",
			spaceErr.RequiredBytes, wantRequired)
	}
	if spaceErr.RequiredBytes <= spaceErr.AvailableBytes {
		t.Errorf("message claims need %d <= have %d, contradicting its own refusal: %v",
			spaceErr.RequiredBytes, spaceErr.AvailableBytes, spaceErr)
	}
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
	if !strings.Contains(msg, "/tmp/test.txt") {
		t.Error("Error message should contain path")
	}
	if !strings.Contains(msg, "100.00") {
		t.Error("Error message should contain required space in MB")
	}
	if !strings.Contains(msg, "50.00") {
		t.Error("Error message should contain available space in MB")
	}

	t.Logf("Error message: %s", msg)
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
	// Output: Sufficient space available
}
