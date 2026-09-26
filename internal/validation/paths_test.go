package validation

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateFilename covers every input class ValidateFilename distinguishes:
// ordinary names, a leading dot, spaces, non-ASCII, long names, an interior ".."
// substring (accepted), the empty string, names of only dots, both path
// separators, control characters, and the names Windows would not store as
// given: device names, alternate data streams, the characters < > " | ? *, and
// a trailing dot or space. Those are refused on every platform, because the
// name comes from the server.
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
		{"null_byte", "file\x00.txt", true},
		{"current_dir", ".", true},
		{"only_dots", "...", true},
		{"device", "CON", true},
		{"device_lower_case", "nul", true},
		{"device_with_extension", "NUL.txt", true},
		{"device_space_before_extension", "aux .log", true},
		{"com_port", "COM1", true},
		{"lpt_superscript", "LPT\u00b2", true},
		{"console_input", "CONIN$", true},
		{"alternate_stream", "a.txt:stream", true},
		{"trailing_dot", "trail.", true},
		{"trailing_space", "trail ", true},
		{"device_prefix", "CONSOLE.txt", false},
		{"com_ten", "COM10", false},
		{"device_inside_name", "my-NUL.txt", false},
		{"leading_space", " lead.txt", false},
		{"long_ascii", strings.Repeat("a", 251) + ".txt", false},
		{"long_multibyte", strings.Repeat("\u00e9", 125) + ".txt", false},
		{"less_than", "a<b.txt", true},
		{"greater_than", "a>b.txt", true},
		{"double_quote", `say "hi".txt`, true},
		{"pipe", "a|b.txt", true},
		{"question_mark", "why?.txt", true},
		{"asterisk", "all*.txt", true},
		{"newline", "a\nb.txt", true},
		{"escape", "a\x1b[31mb.txt", true},
		{"delete", "a\x7fb.txt", true},
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

// A refusal quotes the name, so a trailing space or a control character shows,
// and leaves a backslash single, so a Windows-style name reads as the server
// sent it.
func TestValidateFilenameQuotesTheName(t *testing.T) {
	for name, want := range map[string]string{
		"trail ":       `"trail "`,
		"a\x1b[31mb":   `"a\x1b[31mb"`,
		`dir\file.txt`: `"dir\file.txt"`,
	} {
		if err := ValidateFilename(name); err == nil || !strings.HasSuffix(err.Error(), ": "+want) {
			t.Errorf("ValidateFilename(%q) = %v, want the name quoted as %s", name, err, want)
		}
	}
}

// TestValidatePathInDirectory covers the containment decision through the
// ValidatePathInDirectory wrapper: one row per branch of the resolver
// (relative, interior "..", absolute, empty path, empty base, relative base)
// plus the escape classes. Symlinks are never resolved, so a path naming one
// is judged by its string form alone.
func TestValidatePathInDirectory(t *testing.T) {
	// "/etc/passwd" is only rooted on Windows; Abs puts it on the current drive,
	// the one "/tmp/uploads" is resolved on, so it is absolute everywhere.
	outside, err := filepath.Abs("/etc/passwd")
	if err != nil {
		t.Fatal(err)
	}

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
		{"absolute_outside_base", outside, "/tmp/uploads", true},
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

// Job and file IDs from the server end up in local paths (collision suffixes,
// per-job folders), so anything beyond the ID charset is refused.
func TestValidateID(t *testing.T) {
	for _, id := range []string{"NoPqRs", "abc123", "A", "job-1_b"} {
		if err := ValidateID(id); err != nil {
			t.Errorf("ValidateID(%q) = %v, want nil", id, err)
		}
	}
	for _, id := range []string{"", "x/../../../../tmp/escaped", `..\x`, "a.b", "C:x", "a b", "a\x00b"} {
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID(%q) = nil, want error", id)
		}
	}
}

// DownloadPath places a server file at its relative path only when every
// component passes the name rules, with outputDir joined once, whether it is
// relative or absolute.
func TestDownloadPath(t *testing.T) {
	abs := func(parts ...string) string {
		p, err := filepath.Abs(filepath.Join(parts...))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		dir, relativePath, want string
	}{
		{"results", "", abs("results", "victim.txt")},
		{"results", "sub/victim.txt", abs("results", "sub", "victim.txt")},
		{"results", "../victim.txt", ""},
		{"results", "run:1/victim.txt", ""},
		{"results", "dots./victim.txt", ""},
		{"results", "/abs/victim.txt", ""},
		{"results", "sub//victim.txt", ""},
		{"results", `sub\victim.txt`, ""},
	}
	for _, tt := range tests {
		got, err := DownloadPath(tt.dir, "victim.txt", tt.relativePath)
		if got != tt.want || (err != nil) != (tt.want == "") {
			t.Errorf("DownloadPath(%q, %q) = %q, %v; want %q", tt.dir, tt.relativePath, got, err, tt.want)
		}
	}
}
