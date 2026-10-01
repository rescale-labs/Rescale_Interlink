package filescan

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/models"
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

// A bare pattern such as "*.xml" searches only the root folder, which is not
// how it reads, so that error says how to reach the subfolders.
func TestScanFiles_NoFilesFound(t *testing.T) {
	root := t.TempDir()
	writeScanFile(t, root, filepath.Join("sub", "a.xml"))

	for _, tc := range []struct{ pattern, hint string }{
		{"*.xml", "; to search subfolders too, use **/*.xml"},
		{"sub/*.inp", ""},
		{"**/*.inp", ""},
		{"**.inp", ""},
	} {
		result := ScanFiles(ScanOptions{RootDir: root, PrimaryPattern: tc.pattern})
		if want := "no files found matching pattern: " + tc.pattern + tc.hint; result.Error != want {
			t.Errorf("error %q, want %q", result.Error, want)
		}
	}

	// Recursive has searched the subfolders already, so the hint is what that
	// search passed over.
	if result := ScanFiles(ScanOptions{RootDir: root, PrimaryPattern: "*.inp", Recursive: true}); result.Error !=
		"no files found matching pattern: **/*.inp; a recursive scan does not go into hidden or linked folders" {
		t.Errorf("recursive: error %q, want it to name the pattern searched and what it passed over", result.Error)
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

			files, warning, skip := ResolveSecondaryPattern(dir, "model",
				SecondaryPattern{Pattern: "*.mesh", Required: tt.required})

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
		{
			name:    "absolute, searching subfolders",
			pattern: func(root string) string { return filepath.Join(root, "**", "*.inp") },
			wantErr: "absolute path",
		},
		{
			name:    "climbing out of the root, searching subfolders",
			pattern: func(string) string { return filepath.Join("..", "**", "*.inp") },
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

	for _, pattern := range []string{"[.inp", "**/[.inp"} {
		result := ScanFiles(ScanOptions{RootDir: root, PrimaryPattern: pattern})
		if !strings.Contains(result.Error, "invalid primary pattern") {
			t.Errorf("%s: error %q does not say the pattern is invalid", pattern, result.Error)
		}
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

	if got := notRegularReason(info, nil); got != "is not a regular file" {
		t.Errorf("notRegularReason(%s) = %q", os.DevNull, got)
	}
}

// The paths a scan returns go into a jobs CSV, and `pur run` resolves a relative
// one against its own working directory rather than the scan's. A relative root
// must still give paths that name the same files from anywhere.
func TestScanFiles_RelativeRootGivesAbsolutePaths(t *testing.T) {
	base := t.TempDir()
	writeScanFile(t, base, filepath.Join("scan", "a", "m1.inp"))
	writeScanFile(t, base, filepath.Join("scan", "a", "m1.mesh"))
	t.Chdir(base)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	result := ScanFiles(ScanOptions{
		RootDir:           "scan",
		PrimaryPattern:    "a/*.inp",
		SecondaryPatterns: []SecondaryPattern{{Pattern: "*.mesh", Required: true}},
	})
	if result.Error != "" || len(result.Jobs) != 1 {
		t.Fatalf("scan: error %q, %d jobs; want one job", result.Error, len(result.Jobs))
	}

	dir := filepath.Join(wd, "scan", "a")
	want := JobFiles{
		PrimaryFile: filepath.Join(dir, "m1.inp"),
		PrimaryRel:  filepath.Join("a", "m1.inp"),
		PrimaryDir:  dir,
		PrimaryBase: "m1",
		InputFiles:  []string{filepath.Join(dir, "m1.inp"), filepath.Join(dir, "m1.mesh")},
	}
	if !reflect.DeepEqual(result.Jobs[0], want) {
		t.Errorf("job files = %+v\nwant %+v", result.Jobs[0], want)
	}
}

// "**" is how a file scan reaches below the root folder. Each row lists what
// its pattern must find, in order; the folder named vasprun.xml is not a match.
func TestScanFiles_DoubleStarMatchesAtAnyDepth(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{
		"top.inp", "vasprun.xml", "a/vasprun.xml", "a/b/c/vasprun.xml", "a/b/vasprun.xml/notes.txt",
		"x/input/a.xml", "x/input/sub/a.xml", "run_1/a.inp", "run_2/deep/b.inp", "other/run_3/c.inp",
		"case [1]/in.lit", "case 1/in.lit", "case [1/in.lit",
	} {
		writeScanFile(t, root, filepath.FromSlash(f))
	}

	for _, tc := range []struct {
		pattern string
		want    []string
	}{
		{"**/vasprun.xml", []string{"a/b/c/vasprun.xml", "a/vasprun.xml", "vasprun.xml"}},
		{"**/input/*.xml", []string{"x/input/a.xml"}},
		{"run_*/**/*.inp", []string{"run_1/a.inp", "run_2/deep/b.inp"}},
		// A folder name matches literally too, brackets and all, with or without
		// "**", and even where it is not a valid glob.
		{"**/case [1]/*.lit", []string{"case 1/in.lit", "case [1]/in.lit"}},
		{"case [1]/*.lit", []string{"case 1/in.lit", "case [1]/in.lit"}},
		{"**/case [1/*.lit", []string{"case [1/in.lit"}},
		{"case [1/*.lit", []string{"case [1/in.lit"}},
		// Without "**", a pattern searches only the levels it names, as before.
		{"*.inp", []string{"top.inp"}},
		{"*/*.inp", []string{"run_1/a.inp"}},
	} {
		result := ScanFiles(ScanOptions{RootDir: root, PrimaryPattern: tc.pattern})
		if result.Error != "" || len(result.SkippedFiles) != 0 {
			t.Errorf("%s: error %q, skipped %v", tc.pattern, result.Error, result.SkippedFiles)
		}
		var got, want []string
		for _, jf := range result.Jobs {
			got = append(got, jf.PrimaryFile)
		}
		for _, w := range tc.want {
			want = append(want, filepath.Join(root, filepath.FromSlash(w)))
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: found %v, want %v", tc.pattern, got, want)
		}
	}

	// A trailing "/" is ignored, so "**/" finds what "**" does.
	all := ScanFiles(ScanOptions{RootDir: root, PrimaryPattern: "**"})
	slash := ScanFiles(ScanOptions{RootDir: root, PrimaryPattern: "**/"})
	if len(all.Jobs) == 0 || !reflect.DeepEqual(slash, all) {
		t.Errorf("**/ found %d files and ** found %d; want the same, and some", len(slash.Jobs), len(all.Jobs))
	}
}

// A part without wildcards is looked up, as UnderRoot looks it up, so where the
// file system ignores case, as macOS's and Windows's usually do, "CASES/*.inp"
// still finds the folder cases.
func TestScanFiles_PlainPartsKeepTheFileSystemsCaseRules(t *testing.T) {
	root := t.TempDir()
	writeScanFile(t, root, filepath.Join("cases", "a.inp"))
	if _, err := os.Stat(filepath.Join(root, "CASES")); err != nil {
		t.Skip("this file system tells case apart")
	}

	result := ScanFiles(ScanOptions{RootDir: root, PrimaryPattern: "CASES/*.inp"})
	if len(result.Jobs) != 1 {
		t.Errorf("error %q, %d jobs; want the one file in cases", result.Error, len(result.Jobs))
	}
}

// A "**" scan searches neither hidden folders, where PUR stages its own
// archives, nor linked folders, which can lead anywhere, back up the tree
// included. A hidden root is still searched, and a link to a file still matches.
func TestScanFiles_DoubleStarSkipsHiddenAndLinkedFolders(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, ".scan")
	for _, f := range []string{"keep/m.inp", ".rescale-int-0123abcd/m.inp", ".git/m.inp"} {
		writeScanFile(t, root, filepath.FromSlash(f))
	}
	writeScanFile(t, base, filepath.Join("elsewhere", "m.inp"))
	writeScanFile(t, base, "target.inp")
	for link, target := range map[string]string{"cases.inp": "elsewhere", "alias.inp": "target.inp"} {
		if err := os.Symlink(filepath.Join(base, target), filepath.Join(root, link)); err != nil {
			t.Skipf("cannot make a symbolic link here: %v", err)
		}
	}

	result := ScanFiles(ScanOptions{RootDir: root, PrimaryPattern: "**/*.inp"})

	if result.Error != "" || len(result.SkippedFiles) != 0 {
		t.Fatalf("error %q, skipped %v", result.Error, result.SkippedFiles)
	}
	var got []string
	for _, jf := range result.Jobs {
		got = append(got, jf.PrimaryFile)
	}
	if want := []string{filepath.Join(root, "alias.inp"), filepath.Join(root, "keep", "m.inp")}; !reflect.DeepEqual(got, want) {
		t.Errorf("found %v, want %v", got, want)
	}
}

// A link whose target is missing matched the pattern and became a job, which
// then failed at tar time. It is skipped with the reason instead, whichever
// way the pattern is matched.
func TestScanFiles_DanglingLinkIsSkipped(t *testing.T) {
	root := t.TempDir()
	writeScanFile(t, root, "good.inp")
	if err := os.Symlink(filepath.Join(root, "missing.inp"), filepath.Join(root, "gone.inp")); err != nil {
		t.Skipf("cannot make a symbolic link here: %v", err)
	}

	for _, pattern := range []string{"*.inp", "**/*.inp"} {
		result := ScanFiles(ScanOptions{RootDir: root, PrimaryPattern: pattern})
		if len(result.Jobs) != 1 || result.Jobs[0].PrimaryFile != filepath.Join(root, "good.inp") {
			t.Errorf("%s: jobs %v, want good.inp alone", pattern, result.Jobs)
		}
		// The cause, not the path again.
		if len(result.SkippedFiles) != 1 || !strings.Contains(result.SkippedFiles[0], "gone.inp: cannot be read") ||
			strings.Contains(result.SkippedFiles[0], root) {
			t.Errorf("%s: skipped %q, want gone.inp and why", pattern, result.SkippedFiles)
		}
	}
}

// "**" makes deep layouts the usual case, and there two files can share their
// folder's name as well as their own. Named from the scan root, no two of them
// read the same in a skip line, a GUI list key or the duplicate-name refusal.
func TestScanFiles_MessagesNameFilesFromTheRoot(t *testing.T) {
	root := t.TempDir()
	writeScanFile(t, root, filepath.Join("a", "input", "vasprun.xml"))
	writeScanFile(t, root, filepath.Join("b", "input", "vasprun.xml"))
	want := []string{filepath.Join("a", "input", "vasprun.xml"), filepath.Join("b", "input", "vasprun.xml")}

	result := ScanFiles(ScanOptions{
		RootDir:           root,
		PrimaryPattern:    "**/vasprun.xml",
		SecondaryPatterns: []SecondaryPattern{{Pattern: "OUTCAR", Required: true}},
	})
	if len(result.SkippedFiles) != 2 || !strings.HasPrefix(result.SkippedFiles[0], want[0]+": ") ||
		!strings.HasPrefix(result.SkippedFiles[1], want[1]+": ") {
		t.Errorf("skipped %q, want one line for each of %q", result.SkippedFiles, want)
	}

	result = ScanFiles(ScanOptions{RootDir: root, PrimaryPattern: "**/vasprun.xml"})
	_, _, _, err := BuildJobs(models.JobSpec{Command: "run {{file}}", JobName: "{{base}}"}, result.Jobs)
	if err == nil || !strings.Contains(err.Error(), want[0]+" and "+want[1]) {
		t.Errorf("BuildJobs error %v, want it to name %q", err, want)
	}
}

// Jobs are numbered in scan order ({{index}}, Name_N), so the order has to be
// the same on every platform and the same for "**" as for the plain pattern
// that finds the same files. Paths are compared a part at a time, as fs.Glob
// lists them level by level; comparing whole paths put m.dat before m/z.dat,
// and on Windows, where "\" sorts after the digits, job10 before job1.
func TestScanFiles_OrderIsTheSameEverywhere(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"job/x.inp", "job-2/x.inp", "job1/x.inp", "job10/x.inp", "job2/x.inp", "m.dat", "m/z.dat", "m0.dat"} {
		writeScanFile(t, root, filepath.FromSlash(f))
	}

	jobs := []string{"job/x.inp", "job-2/x.inp", "job1/x.inp", "job10/x.inp", "job2/x.inp"}
	for _, tc := range []struct {
		pattern string
		want    []string
	}{
		{"**/x.inp", jobs},
		{"*/x.inp", jobs},
		{"**/*.dat", []string{"m/z.dat", "m.dat", "m0.dat"}},
	} {
		var got []string
		for _, jf := range ScanFiles(ScanOptions{RootDir: root, PrimaryPattern: tc.pattern}).Jobs {
			got = append(got, filepath.ToSlash(jf.PrimaryRel))
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: order %q, want %q", tc.pattern, got, tc.want)
		}
	}
}
