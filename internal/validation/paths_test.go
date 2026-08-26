package validation

import (
	"path/filepath"
	"testing"
)

// TestValidateFilename covers every input class ValidateFilename distinguishes:
// ordinary names, a leading dot, spaces, non-ASCII, an interior ".." substring
// (accepted; only the literal ".." is rejected), the empty string, the literal
// "..", both path separators, and a null byte.
func TestValidateFilename(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		wantErr  bool
	}{
		{"simple", "file.txt", false},
		{"punctuation_and_dots", "my-file_v1.2.3.txt", false},
		{"hidden", ".hidden", false},
		{"spaces", "my file.txt", false},
		{"unicode", "données_日本語.txt", false},
		{"interior_double_dot", "file..txt", false},
		{"empty", "", true},
		{"parent_dir", "..", true},
		{"unix_separator", "dir/file.txt", true},
		{"windows_separator", "dir\\file.txt", true},
		{"mixed_separators", "dir/sub\\file", true},
		{"traversal", "../etc/passwd", true},
		{"absolute", "/etc/passwd", true},
		{"null_byte", "file\x00.txt", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFilename(tc.filename)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateFilename(%q) = nil, want error", tc.filename)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateFilename(%q) = %v, want nil", tc.filename, err)
			}
		})
	}
}

// TestValidatePathInDirectory covers the containment decision through the
// ValidatePathInDirectory wrapper: one row per branch of the resolver
// (relative, interior "..", absolute, empty path, empty base, relative base)
// plus the escape classes. Symlinks are never resolved, so a path naming one
// is judged by its string form alone.
func TestValidatePathInDirectory(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		baseDir string
		wantErr bool
	}{
		{"simple_file", "file.txt", "/tmp/uploads", false},
		{"parent_then_back", "subdir/../file.txt", "/tmp/uploads", false},
		{"symlink_component_not_resolved", "link_dir/file.txt", "/tmp/uploads", false},
		{"relative_base_made_absolute", "file.txt", "uploads", false},
		{"escape_one_level", "../file.txt", "/tmp/uploads", true},
		{"escape_via_interior_parent", "subdir/../../../etc/passwd", "/tmp/uploads", true},
		{"absolute_outside_base", "/etc/passwd", "/tmp/uploads", true},
		{"empty_path", "", "/tmp/uploads", true},
		{"empty_base", "file.txt", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePathInDirectory(tc.path, tc.baseDir)
			if tc.wantErr && err == nil {
				t.Errorf("ValidatePathInDirectory(%q, %q) = nil, want error", tc.path, tc.baseDir)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidatePathInDirectory(%q, %q) = %v, want nil", tc.path, tc.baseDir, err)
			}
		})
	}
}

// TestResolvePathInDirectory pins the returned path, which the boolean
// ValidatePathInDirectory table cannot observe.
func TestResolvePathInDirectory(t *testing.T) {
	baseDir := t.TempDir()

	resolved, err := ResolvePathInDirectory("subdir/file.txt", baseDir)
	if err != nil {
		t.Fatalf("ResolvePathInDirectory returned error for valid path: %v", err)
	}
	if want := filepath.Join(baseDir, "subdir", "file.txt"); resolved != want {
		t.Fatalf("resolved path = %q, want %q", resolved, want)
	}

	if _, err := ResolvePathInDirectory("../escape.txt", baseDir); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}
