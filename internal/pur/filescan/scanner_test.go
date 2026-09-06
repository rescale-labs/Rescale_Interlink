package filescan

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeScanFile creates one file under dir, making parents as needed.
func writeScanFile(t *testing.T, dir, name string) {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

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

// Both cases below used to reach the tar stage instead, where every job in the
// batch died on "duplicate filename"; see ScanFiles for why they are caught here.
func TestScanFiles_SecondaryCollidesWithAnIncludedFile(t *testing.T) {
	t.Run("a secondary that resolves to the primary is dropped", func(t *testing.T) {
		root := t.TempDir()
		writeScanFile(t, root, "case1.inp")
		writeScanFile(t, root, "case1.mesh")

		result := ScanFiles(ScanOptions{
			RootDir:        root,
			PrimaryPattern: "*.inp",
			SecondaryPatterns: []SecondaryPattern{
				{Pattern: "*.inp", Required: true},
				{Pattern: "*.mesh", Required: true},
			},
		})

		if result.Error != "" {
			t.Fatalf("unexpected error: %s", result.Error)
		}
		if len(result.Jobs) != 1 {
			t.Fatalf("%d jobs, want 1 (skipped: %v)", len(result.Jobs), result.SkippedFiles)
		}
		want := []string{filepath.Join(root, "case1.inp"), filepath.Join(root, "case1.mesh")}
		if got := result.Jobs[0].InputFiles; !reflect.DeepEqual(got, want) {
			t.Errorf("InputFiles = %v, want %v", got, want)
		}
	})

	t.Run("a secondary sharing a base name with another path skips the job", func(t *testing.T) {
		root := t.TempDir()
		writeScanFile(t, root, filepath.Join("inputs", "case1.inp"))
		writeScanFile(t, root, filepath.Join("meshes", "case1.inp"))

		result := ScanFiles(ScanOptions{
			RootDir:        root,
			PrimaryPattern: filepath.Join("inputs", "*.inp"),
			SecondaryPatterns: []SecondaryPattern{
				{Pattern: filepath.Join("..", "meshes", "*.inp"), Required: true},
			},
		})

		if result.Error != "" {
			t.Fatalf("unexpected error: %s", result.Error)
		}
		if len(result.Jobs) != 0 {
			t.Fatalf("%d jobs built from a set that cannot be archived", len(result.Jobs))
		}
		if len(result.SkippedFiles) != 1 {
			t.Fatalf("skipped = %v, want one entry", result.SkippedFiles)
		}
		// Both paths, since a bare base name is what they have in common.
		for _, want := range []string{
			filepath.Join(root, "inputs", "case1.inp"),
			filepath.Join(root, "meshes", "case1.inp"),
		} {
			if !strings.Contains(result.SkippedFiles[0], want) {
				t.Errorf("skip reason %q does not name %s", result.SkippedFiles[0], want)
			}
		}
	})
}

// A scan of "case1/model.inp" and "case2/model.inp" is the layout displayPath
// exists for: a bare base name makes the two lines byte-identical.
func TestScanFiles_SkipsAndWarningsNameTheFolder(t *testing.T) {
	for _, tt := range []struct {
		name     string
		required bool
		lines    func(ScanResult) []string
	}{
		{"a missing required secondary", true, func(r ScanResult) []string { return r.SkippedFiles }},
		{"a missing optional secondary", false, func(r ScanResult) []string { return r.Warnings }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeScanFile(t, root, filepath.Join("case1", "model.inp"))
			writeScanFile(t, root, filepath.Join("case2", "model.inp"))

			result := ScanFiles(ScanOptions{
				RootDir:           root,
				PrimaryPattern:    filepath.Join("*", "model.inp"),
				SecondaryPatterns: []SecondaryPattern{{Pattern: "*.mesh", Required: tt.required}},
			})

			lines := tt.lines(result)
			if len(lines) != 2 {
				t.Fatalf("got %d lines, want 2: %v", len(lines), lines)
			}
			if lines[0] == lines[1] {
				t.Errorf("both files produced the same line %q", lines[0])
			}
			for i, want := range []string{
				filepath.Join("case1", "model.inp"),
				filepath.Join("case2", "model.inp"),
			} {
				if !strings.Contains(lines[i], want) {
					t.Errorf("line %q does not name %s", lines[i], want)
				}
			}
		})
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
