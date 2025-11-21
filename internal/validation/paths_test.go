package validation

import (
	"strings"
	"testing"
)

// TestValidateFilePath tests lenient validation for user-provided CLI paths
func TestValidateFilePath(t *testing.T) {
	testCases := []struct {
		name        string
		path        string
		expectValid bool
		description string
	}{
		// Valid paths - should all be allowed for CLI input
		{
			name:        "simple_relative",
			path:        "file.txt",
			expectValid: true,
			description: "Simple relative file path",
		},
		{
			name:        "relative_with_subdir",
			path:        "subdir/file.txt",
			expectValid: true,
			description: "Relative path with subdirectory",
		},
		{
			name:        "relative_parent",
			path:        "../file.txt",
			expectValid: true,
			description: "Relative path with parent reference (OK for CLI)",
		},
		{
			name:        "multiple_parents",
			path:        "../../file.txt",
			expectValid: true,
			description: "Multiple parent references (OK for CLI)",
		},
		{
			name:        "absolute_unix",
			path:        "/tmp/file.txt",
			expectValid: true,
			description: "Absolute Unix path",
		},
		{
			name:        "absolute_home",
			path:        "/Users/test/file.txt",
			expectValid: true,
			description: "Absolute home directory path",
		},
		{
			name:        "complex_traversal",
			path:        "subdir/../../../etc/passwd",
			expectValid: true,
			description: "Even paths that traverse are OK for CLI (user has full access)",
		},

		// Invalid paths
		{
			name:        "empty",
			path:        "",
			expectValid: false,
			description: "Empty path should be rejected",
		},
		{
			name:        "null_byte",
			path:        "file\x00.txt",
			expectValid: false,
			description: "Path with null byte should be rejected",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFilePath(tc.path)

			if tc.expectValid {
				if err != nil {
					t.Errorf("Expected path '%s' to be valid, but got error: %v\nDescription: %s",
						tc.path, err, tc.description)
				}
			} else {
				if err == nil {
					t.Errorf("Expected path '%s' to be invalid, but validation passed\nDescription: %s",
						tc.path, tc.description)
				}
			}
		})
	}
}

// TestValidateFilename tests strict validation for API-provided filenames
func TestValidateFilename(t *testing.T) {
	testCases := []struct {
		name        string
		filename    string
		expectValid bool
		description string
	}{
		// Valid filenames
		{
			name:        "simple",
			filename:    "file.txt",
			expectValid: true,
			description: "Simple filename",
		},
		{
			name:        "with_dash",
			filename:    "my-file.txt",
			expectValid: true,
			description: "Filename with dash",
		},
		{
			name:        "with_underscore",
			filename:    "my_file.txt",
			expectValid: true,
			description: "Filename with underscore",
		},
		{
			name:        "with_dots",
			filename:    "file.v1.2.3.txt",
			expectValid: true,
			description: "Filename with version dots",
		},
		{
			name:        "hidden_file",
			filename:    ".hidden",
			expectValid: true,
			description: "Hidden file (starts with single dot)",
		},
		{
			name:        "spaces",
			filename:    "my file.txt",
			expectValid: true,
			description: "Filename with spaces",
		},

		// Invalid filenames - path traversal attempts
		{
			name:        "empty",
			filename:    "",
			expectValid: false,
			description: "Empty filename",
		},
		{
			name:        "parent_dir",
			filename:    "..",
			expectValid: false,
			description: "Parent directory reference",
		},
		{
			name:        "contains_dots",
			filename:    "file..txt",
			expectValid: false,
			description: "Filename containing double dots",
		},
		{
			name:        "unix_separator",
			filename:    "dir/file.txt",
			expectValid: false,
			description: "Contains Unix path separator",
		},
		{
			name:        "windows_separator",
			filename:    "dir\\file.txt",
			expectValid: false,
			description: "Contains Windows path separator",
		},
		{
			name:        "traversal_attempt",
			filename:    "../etc/passwd",
			expectValid: false,
			description: "Path traversal attempt",
		},
		{
			name:        "null_byte",
			filename:    "file\x00.txt",
			expectValid: false,
			description: "Filename with null byte",
		},
		{
			name:        "absolute_path",
			filename:    "/etc/passwd",
			expectValid: false,
			description: "Absolute path (not just a filename)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFilename(tc.filename)

			if tc.expectValid {
				if err != nil {
					t.Errorf("Expected filename '%s' to be valid, but got error: %v\nDescription: %s",
						tc.filename, err, tc.description)
				}
			} else {
				if err == nil {
					t.Errorf("Expected filename '%s' to be invalid, but validation passed\nDescription: %s",
						tc.filename, tc.description)
				}
			}
		})
	}
}

// TestValidatePathInDirectory tests context-aware path validation
func TestValidatePathInDirectory(t *testing.T) {
	testCases := []struct {
		name        string
		path        string
		baseDir     string
		expectValid bool
		description string
	}{
		// Valid paths within base directory
		{
			name:        "simple_file",
			path:        "file.txt",
			baseDir:     "/tmp/uploads",
			expectValid: true,
			description: "Simple file in base directory",
		},
		{
			name:        "subdirectory",
			path:        "subdir/file.txt",
			baseDir:     "/tmp/uploads",
			expectValid: true,
			description: "File in subdirectory",
		},
		{
			name:        "deep_nesting",
			path:        "a/b/c/d/file.txt",
			baseDir:     "/tmp/uploads",
			expectValid: true,
			description: "Deeply nested file",
		},
		{
			name:        "parent_then_back",
			path:        "subdir/../file.txt",
			baseDir:     "/tmp/uploads",
			expectValid: true,
			description: "Goes to parent then back (stays within base)",
		},

		// Invalid paths - escape base directory
		{
			name:        "escape_one_level",
			path:        "../file.txt",
			baseDir:     "/tmp/uploads",
			expectValid: false,
			description: "Escapes one level up",
		},
		{
			name:        "escape_multiple",
			path:        "../../file.txt",
			baseDir:     "/tmp/uploads",
			expectValid: false,
			description: "Escapes multiple levels up",
		},
		{
			name:        "escape_to_etc",
			path:        "../../../etc/passwd",
			baseDir:     "/tmp/uploads",
			expectValid: false,
			description: "Attempts to access /etc/passwd",
		},
		{
			name:        "complex_escape",
			path:        "subdir/../../../etc/passwd",
			baseDir:     "/tmp/uploads",
			expectValid: false,
			description: "Complex path that escapes base",
		},
		{
			name:        "absolute_outside",
			path:        "/etc/passwd",
			baseDir:     "/tmp/uploads",
			expectValid: false,
			description: "Absolute path outside base directory",
		},

		// Edge cases
		{
			name:        "empty_path",
			path:        "",
			baseDir:     "/tmp/uploads",
			expectValid: false,
			description: "Empty path",
		},
		{
			name:        "empty_base",
			path:        "file.txt",
			baseDir:     "",
			expectValid: false,
			description: "Empty base directory",
		},
		{
			name:        "relative_base",
			path:        "file.txt",
			baseDir:     "uploads",
			expectValid: true,
			description: "Relative base directory (should be made absolute)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePathInDirectory(tc.path, tc.baseDir)

			if tc.expectValid {
				if err != nil {
					t.Errorf("Expected path '%s' in base '%s' to be valid, but got error: %v\nDescription: %s",
						tc.path, tc.baseDir, err, tc.description)
				}
			} else {
				if err == nil {
					t.Errorf("Expected path '%s' in base '%s' to be invalid, but validation passed\nDescription: %s",
						tc.path, tc.baseDir, tc.description)
				}
			}
		})
	}
}

// TestValidatePathInDirectoryRealWorld tests realistic scenarios
func TestValidatePathInDirectoryRealWorld(t *testing.T) {
	// Simulate download directory structure
	baseDir := "/Users/test/rescale-downloads"

	testCases := []struct {
		name        string
		path        string
		expectValid bool
	}{
		// Valid downloads
		{
			name:        "job_output",
			path:        "job_12345/output.dat",
			expectValid: true,
		},
		{
			name:        "nested_results",
			path:        "project/run_1/results.csv",
			expectValid: true,
		},

		// Attack attempts
		{
			name:        "attack_ssh_key",
			path:        "../../.ssh/id_rsa",
			expectValid: false,
		},
		{
			name:        "attack_passwd",
			path:        "../../../etc/passwd",
			expectValid: false,
		},
		{
			name:        "attack_home",
			path:        "../../Documents/secrets.txt",
			expectValid: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePathInDirectory(tc.path, baseDir)

			if tc.expectValid && err != nil {
				t.Errorf("Expected valid download path, got error: %v", err)
			} else if !tc.expectValid && err == nil {
				t.Errorf("Expected attack path to be rejected: %s", tc.path)
			}
		})
	}
}

func TestValidateFilePaths(t *testing.T) {
	testCases := []struct {
		name        string
		paths       []string
		expectValid bool
		description string
	}{
		{
			name:        "all_valid",
			paths:       []string{"file1.txt", "dir/file2.txt", "/tmp/file3.txt"},
			expectValid: true,
			description: "All paths are valid",
		},
		{
			name:        "empty_list",
			paths:       []string{},
			expectValid: true,
			description: "Empty list is valid",
		},
		{
			name:        "one_empty",
			paths:       []string{"file1.txt", "", "file3.txt"},
			expectValid: false,
			description: "One path is empty",
		},
		{
			name:        "one_null_byte",
			paths:       []string{"file1.txt", "file\x00.txt", "file3.txt"},
			expectValid: false,
			description: "One path has null byte",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFilePaths(tc.paths)

			if tc.expectValid {
				if err != nil {
					t.Errorf("Expected paths to be valid, but got error: %v\nPaths: %v\nDescription: %s",
						err, tc.paths, tc.description)
				}
			} else {
				if err == nil {
					t.Errorf("Expected paths to be invalid, but validation passed\nPaths: %v\nDescription: %s",
						tc.paths, tc.description)
				}
				// Verify error message includes index
				if !strings.Contains(err.Error(), "index") {
					t.Errorf("Error message should include 'index', got: %v", err)
				}
			}
		})
	}
}

func TestValidateFilenames(t *testing.T) {
	testCases := []struct {
		name        string
		filenames   []string
		expectValid bool
		description string
	}{
		{
			name:        "all_valid",
			filenames:   []string{"file1.txt", "file2.csv", "data.json"},
			expectValid: true,
			description: "All filenames are valid",
		},
		{
			name:        "empty_list",
			filenames:   []string{},
			expectValid: true,
			description: "Empty list is valid",
		},
		{
			name:        "one_with_slash",
			filenames:   []string{"file1.txt", "dir/file2.txt", "file3.txt"},
			expectValid: false,
			description: "One filename contains slash",
		},
		{
			name:        "one_with_traversal",
			filenames:   []string{"file1.txt", "../file2.txt", "file3.txt"},
			expectValid: false,
			description: "One filename has traversal attempt",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFilenames(tc.filenames)

			if tc.expectValid {
				if err != nil {
					t.Errorf("Expected filenames to be valid, but got error: %v\nFilenames: %v\nDescription: %s",
						err, tc.filenames, tc.description)
				}
			} else {
				if err == nil {
					t.Errorf("Expected filenames to be invalid, but validation passed\nFilenames: %v\nDescription: %s",
						tc.filenames, tc.description)
				}
				// Verify error message includes index
				if !strings.Contains(err.Error(), "index") {
					t.Errorf("Error message should include 'index', got: %v", err)
				}
			}
		})
	}
}

func TestValidateDirectoryPath(t *testing.T) {
	// ValidateDirectoryPath currently uses same logic as ValidateFilePath
	testCases := []struct {
		name        string
		path        string
		expectValid bool
	}{
		{
			name:        "valid_relative_dir",
			path:        "my_directory",
			expectValid: true,
		},
		{
			name:        "valid_absolute_dir",
			path:        "/tmp/my_directory",
			expectValid: true,
		},
		{
			name:        "valid_with_parent",
			path:        "../my_directory",
			expectValid: true,
		},
		{
			name:        "empty",
			path:        "",
			expectValid: false,
		},
		{
			name:        "null_byte",
			path:        "dir\x00ectory",
			expectValid: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateDirectoryPath(tc.path)

			if tc.expectValid && err != nil {
				t.Errorf("Directory path '%s' should be valid, got error: %v", tc.path, err)
			} else if !tc.expectValid && err == nil {
				t.Errorf("Directory path '%s' should be invalid", tc.path)
			}
		})
	}
}

// TestCrossplatformPathSeparators tests handling of different path separators
func TestCrossplatformPathSeparators(t *testing.T) {
	testCases := []struct {
		name     string
		filename string
		invalid  bool
	}{
		{
			name:     "unix_separator",
			filename: "dir/file",
			invalid:  true,
		},
		{
			name:     "windows_separator",
			filename: "dir\\file",
			invalid:  true,
		},
		{
			name:     "mixed_separators",
			filename: "dir/sub\\file",
			invalid:  true,
		},
		{
			name:     "no_separator",
			filename: "file.txt",
			invalid:  false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFilename(tc.filename)
			if tc.invalid && err == nil {
				t.Errorf("Expected filename with separator to be invalid: %s", tc.filename)
			} else if !tc.invalid && err != nil {
				t.Errorf("Expected filename without separator to be valid: %s, got error: %v", tc.filename, err)
			}
		})
	}
}

// TestValidatePathInDirectorySymlinkScenarios documents symlink behavior
// Note: Actual symlink resolution is not implemented in current version
func TestValidatePathInDirectorySymlinkScenarios(t *testing.T) {
	// This test documents expected behavior, even though current implementation
	// doesn't handle symlinks specially
	baseDir := "/tmp/uploads"

	// These tests show what WOULD happen with symlinks
	// Current implementation doesn't resolve symlinks, so these paths
	// are validated based on their string representation only
	testCases := []struct {
		name        string
		path        string
		expectValid bool
		note        string
	}{
		{
			name:        "symlink_within_base",
			path:        "link_to_file.txt",
			expectValid: true,
			note:        "Symlink within base dir (not resolved)",
		},
		{
			name:        "path_with_symlink_component",
			path:        "link_dir/file.txt",
			expectValid: true,
			note:        "Path through symlink (not resolved)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePathInDirectory(tc.path, baseDir)
			if tc.expectValid && err != nil {
				t.Errorf("Path should be valid (note: %s), got error: %v", tc.note, err)
			} else if !tc.expectValid && err == nil {
				t.Errorf("Path should be invalid (note: %s)", tc.note)
			}
		})
	}
}
