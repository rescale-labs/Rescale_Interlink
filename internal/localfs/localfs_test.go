package localfs

import (
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

func TestListDirectory(t *testing.T) {
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
		entries, err := ListDirectory(tmpDir, ListOptions{IncludeHidden: false})
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
		entries, err := ListDirectory(tmpDir, ListOptions{IncludeHidden: true})
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
		entries, err := ListDirectory(tmpDir, ListOptions{IncludeHidden: true})
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
		_, err := ListDirectory("/nonexistent/path", ListOptions{})
		if err == nil {
			t.Error("expected error for nonexistent directory")
		}
	})
}

func TestWalk(t *testing.T) {
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

	t.Run("exclude hidden", func(t *testing.T) {
		var files []string
		err := Walk(tmpDir, WalkOptions{IncludeHidden: false, SkipHiddenDirs: true}, func(e FileEntry) error {
			files = append(files, e.Name)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		// Should have: tmpDir (root), file1.txt, subdir, file2.txt
		// Should NOT have: .hidden_file, .hidden_dir, file3.txt
		expected := map[string]bool{
			filepath.Base(tmpDir): true,
			"file1.txt":           true,
			"subdir":              true,
			"file2.txt":           true,
		}

		if len(files) != len(expected) {
			t.Errorf("got %d files %v, want %d", len(files), files, len(expected))
		}

		for _, f := range files {
			if f == ".hidden_file" || f == ".hidden_dir" || f == "file3.txt" {
				t.Errorf("found hidden or nested-in-hidden file %q", f)
			}
		}
	})

	t.Run("include hidden", func(t *testing.T) {
		var files []string
		err := Walk(tmpDir, WalkOptions{IncludeHidden: true}, func(e FileEntry) error {
			files = append(files, e.Name)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		// Should have all 7 entries
		if len(files) != 7 {
			t.Errorf("got %d files %v, want 7", len(files), files)
		}
	})
}

func TestWalkFiles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "localfs_walkfiles_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create files and dirs
	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("1"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "subdir", "file2.txt"), []byte("2"), 0644)

	var files []string
	err = WalkFiles(tmpDir, WalkOptions{}, func(e FileEntry) error {
		files = append(files, e.Name)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Should only have files, not directories
	if len(files) != 2 {
		t.Errorf("got %d files %v, want 2", len(files), files)
	}

	for _, f := range files {
		if f == "subdir" || f == filepath.Base(tmpDir) {
			t.Errorf("WalkFiles should not return directories, got %q", f)
		}
	}
}

func TestDefaultWalkOptions(t *testing.T) {
	opts := DefaultWalkOptions()
	if opts.IncludeHidden {
		t.Error("default should not include hidden")
	}
	if !opts.SkipHiddenDirs {
		t.Error("default should skip hidden dirs")
	}
}
