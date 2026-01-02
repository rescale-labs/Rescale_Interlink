package localfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIsHidden(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{".hidden", true},
		{".gitignore", true},
		{"visible.txt", false},
		{"normal", false},
		{"/path/to/.hidden", true},
		{"/path/to/visible.txt", false},
		{"../.hidden", true},
		{"../visible.txt", false},
		{"..", false}, // Special case: parent dir reference
		{".", false},  // Special case: current dir reference
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := IsHidden(tt.path)
			if result != tt.expected {
				t.Errorf("IsHidden(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestIsHiddenName(t *testing.T) {
	tests := []struct {
		name     string
		expected bool
	}{
		{".hidden", true},
		{".gitignore", true},
		{"visible.txt", false},
		{"normal", false},
		{"..", false}, // Parent dir reference starts with . but is special
		{".", false},  // Current dir reference
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsHiddenName(tt.name)
			if result != tt.expected {
				t.Errorf("IsHiddenName(%q) = %v, want %v", tt.name, result, tt.expected)
			}
		})
	}
}

// TestListDirectoryEx tests the v4.0.4 ListDirectoryEx function
// (replaces legacy ListDirectory tests)
func TestListDirectoryEx(t *testing.T) {
	// Create temp directory with test files
	tmpDir, err := os.MkdirTemp("", "localfs_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test files
	testFiles := []string{"visible.txt", ".hidden", "another.txt", ".gitignore"}
	for _, f := range testFiles {
		if err := os.WriteFile(filepath.Join(tmpDir, f), []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Create a subdirectory
	if err := os.Mkdir(filepath.Join(tmpDir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tmpDir, ".hiddendir"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Run("exclude hidden", func(t *testing.T) {
		entries, err := ListDirectoryEx(context.Background(), tmpDir, ListDirectoryExOptions{IncludeHidden: false})
		if err != nil {
			t.Fatal(err)
		}

		// Should have: visible.txt, another.txt, subdir (3 items)
		if len(entries) != 3 {
			t.Errorf("got %d entries, want 3", len(entries))
		}

		// Verify no hidden files
		for _, e := range entries {
			if IsHiddenName(e.Name) {
				t.Errorf("found hidden entry %q when IncludeHidden=false", e.Name)
			}
		}
	})

	t.Run("include hidden", func(t *testing.T) {
		entries, err := ListDirectoryEx(context.Background(), tmpDir, ListDirectoryExOptions{IncludeHidden: true})
		if err != nil {
			t.Fatal(err)
		}

		// Should have all 6 items
		if len(entries) != 6 {
			t.Errorf("got %d entries, want 6", len(entries))
		}

		// Verify we have hidden files
		hasHidden := false
		for _, e := range entries {
			if IsHiddenName(e.Name) {
				hasHidden = true
				break
			}
		}
		if !hasHidden {
			t.Error("expected hidden entries when IncludeHidden=true")
		}
	})

	t.Run("entry properties", func(t *testing.T) {
		entries, err := ListDirectoryEx(context.Background(), tmpDir, ListDirectoryExOptions{IncludeHidden: true})
		if err != nil {
			t.Fatal(err)
		}

		for _, e := range entries {
			// Check Path is correctly joined
			expectedPath := filepath.Join(tmpDir, e.Name)
			if e.Path != expectedPath {
				t.Errorf("entry %q has Path=%q, want %q", e.Name, e.Path, expectedPath)
			}

			// Check IsDir flag
			if e.Name == "subdir" || e.Name == ".hiddendir" {
				if !e.IsDir {
					t.Errorf("entry %q should be a directory", e.Name)
				}
			} else {
				if e.IsDir {
					t.Errorf("entry %q should not be a directory", e.Name)
				}
			}
		}
	})

	t.Run("nonexistent directory", func(t *testing.T) {
		_, err := ListDirectoryEx(context.Background(), "/nonexistent/path", ListDirectoryExOptions{})
		if err == nil {
			t.Error("expected error for nonexistent directory")
		}
	})
}

// TestWalkCollect tests the v4.0.4 WalkCollect function
// (replaces legacy Walk and WalkFiles tests)
func TestWalkCollect(t *testing.T) {
	// Create temp directory structure
	tmpDir, err := os.MkdirTemp("", "localfs_walk_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create structure:
	// tmpDir/
	//   file1.txt
	//   .hidden_file
	//   subdir/
	//     file2.txt
	//   .hidden_dir/
	//     file3.txt

	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("1"), 0644)
	os.WriteFile(filepath.Join(tmpDir, ".hidden_file"), []byte("h"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "subdir", "file2.txt"), []byte("2"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, ".hidden_dir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, ".hidden_dir", "file3.txt"), []byte("3"), 0644)

	t.Run("exclude hidden files only", func(t *testing.T) {
		result, err := WalkCollect(tmpDir, WalkOptions{IncludeHidden: false, SkipHiddenDirs: true})
		if err != nil {
			t.Fatal(err)
		}

		// Should have: file1.txt, file2.txt (files only, no hidden)
		// Should NOT have: .hidden_file, file3.txt (in hidden dir)
		if len(result.Files) != 2 {
			t.Errorf("got %d files, want 2 (file1.txt, file2.txt)", len(result.Files))
		}

		for _, e := range result.Files {
			if IsHiddenName(e.Name) {
				t.Errorf("found hidden file %q when IncludeHidden=false", e.Name)
			}
		}
	})

	t.Run("include hidden files", func(t *testing.T) {
		result, err := WalkCollect(tmpDir, WalkOptions{IncludeHidden: true})
		if err != nil {
			t.Fatal(err)
		}

		// Should have all 4 files: file1.txt, .hidden_file, file2.txt, file3.txt
		if len(result.Files) != 4 {
			t.Errorf("got %d files, want 4", len(result.Files))
		}
	})

	t.Run("directories collected separately", func(t *testing.T) {
		result, err := WalkCollect(tmpDir, WalkOptions{IncludeHidden: true})
		if err != nil {
			t.Fatal(err)
		}

		// Should have directories: subdir, .hidden_dir
		if len(result.Directories) < 2 {
			t.Errorf("got %d directories, want at least 2", len(result.Directories))
		}
	})
}
