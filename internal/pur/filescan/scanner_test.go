package filescan

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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

// A scan root names one directory; it is not part of the pattern. Joining the
// two before globbing made the root's own characters syntax, so a root of
// "proj [v2]" read as a character class and the scan returned the sibling
// "proj v" instead — silently, since it found files either way.
func TestScanFiles_RootMetacharactersAreLiteral(t *testing.T) {
	for _, tt := range []struct {
		name  string
		root  string // the directory the scan is pointed at
		decoy string // the sibling the joined pattern matched instead
	}{
		{"character class", "proj [v2]", "proj v"},
		{"single-character wildcard", "proj?x", "projAx"},
		{"star", "proj*x", "projAx"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && strings.ContainsAny(tt.root, `?*`) {
				t.Skip("Windows filenames cannot contain ? or *")
			}

			base := t.TempDir()
			writeScanFile(t, base, filepath.Join(tt.root, "wanted.inp"))
			writeScanFile(t, base, filepath.Join(tt.decoy, "decoy.inp"))

			result := ScanFiles(ScanOptions{
				RootDir:        filepath.Join(base, tt.root),
				PrimaryPattern: "*.inp",
			})

			if result.Error != "" {
				t.Fatalf("unexpected error: %s", result.Error)
			}
			if len(result.Jobs) != 1 {
				t.Fatalf("%d jobs, want 1: %v", len(result.Jobs), result.Jobs)
			}
			want := filepath.Join(base, tt.root, "wanted.inp")
			if got := result.Jobs[0].PrimaryFile; got != want {
				t.Errorf("PrimaryFile = %s, want %s", got, want)
			}
		})
	}
}

// The pattern is matched inside the root, so one that names somewhere else
// cannot be honored. Saying so beats matching the wrong files or matching
// nothing with no explanation.
func TestScanFiles_PrimaryPatternMustStayUnderTheRoot(t *testing.T) {
	for _, tt := range []struct {
		name    string
		pattern func(root string) string
		wantErr string
	}{
		{
			name:    "absolute",
			pattern: func(root string) string { return filepath.Join(root, "cases", "*.inp") },
			wantErr: "absolute path",
		},
		{
			// The joined form reached outside the root and found loose.inp.
			name:    "climbing out of the root",
			pattern: func(string) string { return filepath.Join("..", "*.inp") },
			wantErr: "outside the scan root",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeScanFile(t, root, filepath.Join("cases", "model.inp"))
			writeScanFile(t, root, "loose.inp")

			pattern := tt.pattern(root)
			result := ScanFiles(ScanOptions{
				RootDir:        filepath.Join(root, "cases"),
				PrimaryPattern: pattern,
			})

			if result.Error == "" {
				t.Fatalf("pattern %q was accepted: %v", pattern, result.Jobs)
			}
			if !strings.Contains(result.Error, tt.wantErr) {
				t.Errorf("error %q does not say %q", result.Error, tt.wantErr)
			}
			if !strings.Contains(result.Error, pattern) {
				t.Errorf("error %q does not name the pattern %q", result.Error, pattern)
			}
		})
	}
}

// A glob matches directories as readily as files, and "model.mesh/" attached as
// an input used to fail the job at tar time — after the run had started, which
// is the expensive place to find out.
func TestScanFiles_DirectoriesAreNotInputFiles(t *testing.T) {
	t.Run("a directory matched as a primary is skipped", func(t *testing.T) {
		root := t.TempDir()
		writeScanFile(t, root, "case1.inp")
		if err := os.MkdirAll(filepath.Join(root, "case2.inp"), 0755); err != nil {
			t.Fatal(err)
		}

		result := ScanFiles(ScanOptions{RootDir: root, PrimaryPattern: "*.inp"})

		if result.Error != "" {
			t.Fatalf("unexpected error: %s", result.Error)
		}
		if len(result.Jobs) != 1 {
			t.Fatalf("%d jobs, want 1 (skipped: %v)", len(result.Jobs), result.SkippedFiles)
		}
		if got := result.Jobs[0].PrimaryFile; got != filepath.Join(root, "case1.inp") {
			t.Errorf("PrimaryFile = %s, want case1.inp", got)
		}
		if len(result.SkippedFiles) != 1 {
			t.Fatalf("skipped = %v, want one entry", result.SkippedFiles)
		}
		if !strings.Contains(result.SkippedFiles[0], "case2.inp") ||
			!strings.Contains(result.SkippedFiles[0], "directory") {
			t.Errorf("skip reason %q does not say case2.inp is a directory", result.SkippedFiles[0])
		}
	})

	t.Run("a directory matched as a required secondary skips the job", func(t *testing.T) {
		root := t.TempDir()
		writeScanFile(t, root, "case1.inp")
		if err := os.MkdirAll(filepath.Join(root, "case1.mesh"), 0755); err != nil {
			t.Fatal(err)
		}

		result := ScanFiles(ScanOptions{
			RootDir:           root,
			PrimaryPattern:    "*.inp",
			SecondaryPatterns: []SecondaryPattern{{Pattern: "*.mesh", Required: true}},
		})

		if len(result.Jobs) != 0 {
			t.Fatalf("%d jobs built with a directory as an input: %v",
				len(result.Jobs), result.Jobs[0].InputFiles)
		}
		if len(result.SkippedFiles) != 1 {
			t.Fatalf("skipped = %v, want one entry", result.SkippedFiles)
		}
		if !strings.Contains(result.SkippedFiles[0], "directory") {
			t.Errorf("skip reason %q does not say the secondary is a directory", result.SkippedFiles[0])
		}
	})

	t.Run("a directory matched as an optional secondary warns", func(t *testing.T) {
		root := t.TempDir()
		writeScanFile(t, root, "case1.inp")
		if err := os.MkdirAll(filepath.Join(root, "case1.mesh"), 0755); err != nil {
			t.Fatal(err)
		}

		result := ScanFiles(ScanOptions{
			RootDir:           root,
			PrimaryPattern:    "*.inp",
			SecondaryPatterns: []SecondaryPattern{{Pattern: "*.mesh", Required: false}},
		})

		if len(result.Jobs) != 1 {
			t.Fatalf("%d jobs, want 1 (skipped: %v)", len(result.Jobs), result.SkippedFiles)
		}
		if got := result.Jobs[0].InputFiles; len(got) != 1 {
			t.Errorf("InputFiles = %v, want the primary alone", got)
		}
		if len(result.Warnings) != 1 {
			t.Fatalf("warnings = %v, want one entry", result.Warnings)
		}
		if !strings.Contains(result.Warnings[0], "directory") {
			t.Errorf("warning %q does not say the secondary is a directory", result.Warnings[0])
		}
	})
}

// A pattern fs.Glob cannot parse is reported rather than read as "no matches",
// which is the same distinction the root checks above exist to keep.
func TestScanFiles_MalformedPrimaryPatternIsReported(t *testing.T) {
	root := t.TempDir()
	writeScanFile(t, root, "case1.inp")

	result := ScanFiles(ScanOptions{RootDir: root, PrimaryPattern: "[.inp"})

	if result.Error == "" {
		t.Fatalf("an unparseable pattern was accepted: %v", result.Jobs)
	}
	if !strings.Contains(result.Error, "invalid primary pattern") {
		t.Errorf("error %q does not say the pattern is invalid", result.Error)
	}
}

// The skip reason has to hold for a match that is neither a file nor a
// directory. os.DevNull is the one such path every platform has.
func TestNotRegularReason_NonDirectory(t *testing.T) {
	info, err := os.Stat(os.DevNull)
	if err != nil {
		t.Skipf("cannot stat %s: %v", os.DevNull, err)
	}
	if info.Mode().IsRegular() {
		t.Skipf("%s is a regular file here", os.DevNull)
	}

	if got := notRegularReason(info); got != "is not a regular file" {
		t.Errorf("notRegularReason(%s) = %q", os.DevNull, got)
	}
}
