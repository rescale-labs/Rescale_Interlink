package filescan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanFiles_NoPrimaryPattern(t *testing.T) {
	result := ScanFiles(ScanOptions{
		RootDir:        "/tmp",
		PrimaryPattern: "",
	})

	if result.Error == "" {
		t.Error("Expected error for missing primary pattern")
	}
}

func TestScanFiles_NoFilesFound(t *testing.T) {
	result := ScanFiles(ScanOptions{
		RootDir:        "/tmp",
		PrimaryPattern: "nonexistent-*.xyz",
	})

	if result.Error == "" {
		t.Error("Expected error for no matching files")
	}
}

func TestScanFiles_BasicScan(t *testing.T) {
	// Create temp directory with test files
	tmpDir, err := os.MkdirTemp("", "filescan_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test files
	testFiles := []string{"model1.inp", "model2.inp", "other.txt"}
	for _, f := range testFiles {
		if err := os.WriteFile(filepath.Join(tmpDir, f), []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to create test file %s: %v", f, err)
		}
	}

	result := ScanFiles(ScanOptions{
		RootDir:        tmpDir,
		PrimaryPattern: "*.inp",
	})

	if result.Error != "" {
		t.Errorf("Unexpected error: %s", result.Error)
	}

	if result.TotalCount != 2 {
		t.Errorf("Expected 2 primary files, got %d", result.TotalCount)
	}

	if result.MatchCount != 2 {
		t.Errorf("Expected 2 jobs, got %d", result.MatchCount)
	}
}

func TestResolveSecondaryPattern_RequiredMissing(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "filescan_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create primary file only, no secondary
	primaryFile := filepath.Join(tmpDir, "model.inp")
	if err := os.WriteFile(primaryFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	files, warning, skip := ResolveSecondaryPattern(
		tmpDir, "model", primaryFile,
		SecondaryPattern{Pattern: "*.mesh", Required: true},
	)

	if skip == "" {
		t.Error("Expected skip reason for missing required file")
	}
	if len(files) > 0 {
		t.Error("Expected no files returned for missing required")
	}
	if warning != "" {
		t.Error("Expected no warning for skip")
	}
}

func TestResolveSecondaryPattern_OptionalMissing(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "filescan_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	primaryFile := filepath.Join(tmpDir, "model.inp")
	if err := os.WriteFile(primaryFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	files, warning, skip := ResolveSecondaryPattern(
		tmpDir, "model", primaryFile,
		SecondaryPattern{Pattern: "*.mesh", Required: false},
	)

	if skip != "" {
		t.Errorf("Unexpected skip reason: %s", skip)
	}
	if len(files) > 0 {
		t.Error("Expected no files for missing optional")
	}
	if warning == "" {
		t.Error("Expected warning for missing optional file")
	}
}

func TestResolveSecondaryPattern_WildcardResolution(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "filescan_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create both primary and secondary with matching names
	if err := os.WriteFile(filepath.Join(tmpDir, "model.inp"), []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create inp: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "model.mesh"), []byte("mesh"), 0644); err != nil {
		t.Fatalf("Failed to create mesh: %v", err)
	}

	files, warning, skip := ResolveSecondaryPattern(
		tmpDir, "model", filepath.Join(tmpDir, "model.inp"),
		SecondaryPattern{Pattern: "*.mesh", Required: true},
	)

	if skip != "" {
		t.Errorf("Unexpected skip: %s", skip)
	}
	if warning != "" {
		t.Errorf("Unexpected warning: %s", warning)
	}
	if len(files) != 1 {
		t.Errorf("Expected 1 file, got %d", len(files))
	}
	if len(files) > 0 && filepath.Base(files[0]) != "model.mesh" {
		t.Errorf("Expected model.mesh, got %s", files[0])
	}
}
