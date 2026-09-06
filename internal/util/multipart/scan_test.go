package multipart

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// mkdirs creates each path under root, and writes an empty file for any path
// ending in a filename rather than a directory name.
func mkdirs(t *testing.T, root string, dirs []string, files []string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0755); err != nil {
			t.Fatalf("MkdirAll %s: %v", d, err)
		}
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(root, f), []byte("valid"), 0644); err != nil {
			t.Fatalf("WriteFile %s: %v", f, err)
		}
	}
}

func TestScanDirectories(t *testing.T) {
	tests := []struct {
		name string
		dirs []string
		// files are validation-pattern files created after dirs.
		files []string
		// opts is built from the temp root: singleDir true uses it as SingleDir,
		// otherwise partDirs are joined onto it.
		singleDir         bool
		partDirs          []string
		pattern           string
		validationPattern string
		runSubpath        string
		wantCount         int
		wantErr           bool
		validate          func(t *testing.T, results []ScanResult)
	}{
		{
			name:      "single dir matches every run",
			dirs:      []string{"Run_1", "Run_2", "Run_3"},
			singleDir: true, pattern: "Run_*", wantCount: 3,
			validate: func(t *testing.T, results []ScanResult) {
				for _, r := range results {
					if r.ProjectName != "" {
						t.Errorf("expected empty project name for single-dir mode, got %q", r.ProjectName)
					}
				}
			},
		},
		{
			name:      "validation pattern filters unvalidated runs",
			dirs:      []string{"Run_1", "Run_2"},
			files:     []string{filepath.Join("Run_1", "output.avg.fnc")},
			singleDir: true, pattern: "Run_*", validationPattern: "*.avg.fnc", wantCount: 1,
			validate: func(t *testing.T, results []ScanResult) {
				if got := filepath.Base(results[0].Directory); got != "Run_1" {
					t.Errorf("expected Run_1, got %s", got)
				}
			},
		},
		{
			// The validation walk is recursive, so a file in a subdirectory counts.
			name:      "validation file in a subdirectory",
			dirs:      []string{filepath.Join("Run_1", "subdir")},
			files:     []string{filepath.Join("Run_1", "subdir", "result.avg.fnc")},
			singleDir: true, pattern: "Run_*", validationPattern: "*.avg.fnc", wantCount: 1,
		},
		{
			name: "multi-part scan across project dirs",
			dirs: []string{
				filepath.Join("DOE_1", "Run_1"), filepath.Join("DOE_1", "Run_2"),
				filepath.Join("DOE_2", "Run_1"), filepath.Join("DOE_2", "Run_4"),
			},
			partDirs: []string{"DOE_1", "DOE_2"}, pattern: "Run_*", wantCount: 4,
			validate: func(t *testing.T, results []ScanResult) {
				for _, r := range results {
					if r.ProjectName == "" {
						t.Error("expected non-empty project name for multi-part mode")
					}
				}
			},
		},
		{
			name: "multi-part scan with validation",
			dirs: []string{
				filepath.Join("DOE_1", "Run_1"), filepath.Join("DOE_1", "Run_2"),
				filepath.Join("DOE_2", "Run_1"), filepath.Join("DOE_2", "Run_4"),
			},
			files: []string{
				filepath.Join("DOE_1", "Run_1", "output.avg.fnc"),
				filepath.Join("DOE_2", "Run_4", "output.avg.fnc"),
			},
			partDirs: []string{"DOE_1", "DOE_2"}, pattern: "Run_*", validationPattern: "*.avg.fnc", wantCount: 2,
			validate: func(t *testing.T, results []ScanResult) {
				names := make(map[string]bool)
				for _, r := range results {
					names[filepath.Base(r.Directory)+"_"+r.ProjectName] = true
				}
				if !names["Run_1_DOE_1"] {
					t.Error("expected Run_1 from DOE_1 to be validated")
				}
				if !names["Run_4_DOE_2"] {
					t.Error("expected Run_4 from DOE_2 to be validated")
				}
			},
		},
		{
			name: "run subpath",
			dirs: []string{
				filepath.Join("Simcodes", "Powerflow", "Run_1"),
				filepath.Join("Simcodes", "Powerflow", "Run_2"),
			},
			singleDir: true, runSubpath: filepath.Join("Simcodes", "Powerflow"),
			pattern: "Run_*", wantCount: 2,
		},
		{
			name:      "empty pattern is an error",
			singleDir: true, wantErr: true,
		},
		{
			name:      "no matching directories is an error",
			singleDir: true, pattern: "Run_*", wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			mkdirs(t, root, tt.dirs, tt.files)

			opts := ScanOpts{
				Pattern:           tt.pattern,
				ValidationPattern: tt.validationPattern,
				RunSubpath:        tt.runSubpath,
				BaseJobName:       "TestJob",
				StartIndex:        1,
			}
			if tt.singleDir {
				opts.SingleDir = root
			}
			for _, p := range tt.partDirs {
				opts.PartDirs = append(opts.PartDirs, filepath.Join(root, p))
			}

			results, err := ScanDirectories(opts)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %d results", len(results))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(results) != tt.wantCount {
				t.Fatalf("expected %d results, got %d", tt.wantCount, len(results))
			}
			if tt.validate != nil {
				tt.validate(t, results)
			}
		})
	}
}

// The scan root names one directory; it is not part of the pattern. Joining the
// two before globbing made the root's own characters syntax, so a project
// folder called "proj [v2]" read as a character class and the scan returned run
// directories from the sibling "proj v" instead.
func TestScanDirectories_RootMetacharactersAreLiteral(t *testing.T) {
	for _, tt := range []struct {
		name  string
		root  string // the project directory the scan is pointed at
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
			mkdirs(t, base, []string{
				filepath.Join(tt.root, "Run_1"),
				filepath.Join(tt.decoy, "Run_9"),
			}, nil)

			results, err := ScanDirectories(ScanOpts{
				SingleDir:   filepath.Join(base, tt.root),
				Pattern:     "Run_*",
				BaseJobName: "job",
				StartIndex:  1,
			})
			if err != nil {
				t.Fatalf("ScanDirectories failed: %v", err)
			}
			if len(results) != 1 {
				t.Fatalf("%d results, want 1: %v", len(results), results)
			}
			want := filepath.Join(base, tt.root, "Run_1")
			if results[0].Directory != want {
				t.Errorf("Directory = %s, want %s", results[0].Directory, want)
			}
		})
	}
}

// The multi-directory scan is the path `pur make-dirs-csv --part-dirs` takes,
// and it joined the part directory onto the pattern just as the single-directory
// scan did; the same bracketed folder must not scan its sibling here either.
func TestCollectAllRunDirectories_RootMetacharactersAreLiteral(t *testing.T) {
	base := t.TempDir()
	mkdirs(t, base, []string{
		filepath.Join("proj [v2]", "Run_1"),
		filepath.Join("proj v", "Run_9"),
	}, nil)

	runs, err := CollectAllRunDirectories([]string{filepath.Join(base, "proj [v2]")}, "", "Run_*")
	if err != nil {
		t.Fatalf("CollectAllRunDirectories: %v", err)
	}
	if len(runs) != 1 || runs[0].RunName != "Run_1" {
		t.Fatalf("runs = %+v, want only Run_1 under the bracketed project", runs)
	}
}

// The pattern is matched inside the scan root, so one that names somewhere else
// cannot be honored; the joined form used to reach outside the root silently.
func TestScanDirectories_PatternMustStayUnderTheRoot(t *testing.T) {
	for _, tt := range []struct {
		name    string
		pattern func(base string) string
		wantErr string
	}{
		{
			name:    "absolute",
			pattern: func(base string) string { return filepath.Join(base, "Run_*") },
			wantErr: "absolute path",
		},
		{
			name:    "climbing out of the root",
			pattern: func(string) string { return filepath.Join("..", "Run_*") },
			wantErr: "outside the scan root",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			mkdirs(t, base, []string{"Run_7", filepath.Join("proj", "Run_1")}, nil)

			pattern := tt.pattern(base)
			results, err := ScanDirectories(ScanOpts{
				SingleDir:   filepath.Join(base, "proj"),
				Pattern:     pattern,
				BaseJobName: "job",
				StartIndex:  1,
			})
			if err == nil {
				t.Fatalf("pattern %q was accepted: %v", pattern, results)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not say %q", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), pattern) {
				t.Errorf("error %q does not name the pattern %q", err, pattern)
			}
		})
	}
}
