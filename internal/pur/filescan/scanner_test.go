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

func TestResolveSecondaryPattern(t *testing.T) {
	tests := []struct {
		name string
		// files are created in the temp dir before resolving.
		files       []string
		required    bool
		wantSkip    bool
		wantWarning bool
		wantFiles   []string
	}{
		{
			// A missing required secondary skips the whole entry; a skip is not
			// also reported as a warning.
			name:  "required secondary missing",
			files: []string{"model.inp"}, required: true,
			wantSkip: true,
		},
		{
			// A missing optional secondary warns instead of skipping.
			name:  "optional secondary missing",
			files: []string{"model.inp"}, required: false,
			wantWarning: true,
		},
		{
			name:  "wildcard resolves to the matching sibling",
			files: []string{"model.inp", "model.mesh"}, required: true,
			wantFiles: []string{"model.mesh"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("test"), 0644); err != nil {
					t.Fatalf("failed to create %s: %v", f, err)
				}
			}

			files, warning, skip := ResolveSecondaryPattern(
				dir, "model", filepath.Join(dir, "model.inp"),
				SecondaryPattern{Pattern: "*.mesh", Required: tt.required},
			)

			if (skip != "") != tt.wantSkip {
				t.Errorf("skip = %q, wantSkip %v", skip, tt.wantSkip)
			}
			if (warning != "") != tt.wantWarning {
				t.Errorf("warning = %q, wantWarning %v", warning, tt.wantWarning)
			}
			if len(files) != len(tt.wantFiles) {
				t.Fatalf("got %d files, want %d: %v", len(files), len(tt.wantFiles), files)
			}
			for i, want := range tt.wantFiles {
				if got := filepath.Base(files[i]); got != want {
					t.Errorf("files[%d] = %s, want %s", i, got, want)
				}
			}
		})
	}
}
