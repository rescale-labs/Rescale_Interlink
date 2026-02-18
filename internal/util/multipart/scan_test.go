package multipart

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanDirectories_SingleDir(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()
	for _, name := range []string{"Run_1", "Run_2", "Run_3"} {
		os.MkdirAll(filepath.Join(tmpDir, name), 0755)
	}

	results, err := ScanDirectories(ScanOpts{
		SingleDir:   tmpDir,
		Pattern:     "Run_*",
		BaseJobName: "TestJob",
		StartIndex:  1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Verify sorted order and job names
	for _, r := range results {
		if r.ProjectName != "" {
			t.Errorf("expected empty project name for single-dir mode, got %q", r.ProjectName)
		}
	}
}

func TestScanDirectories_WithValidation(t *testing.T) {
	tmpDir := t.TempDir()

	// Create Run_1 with validation file, Run_2 without
	os.MkdirAll(filepath.Join(tmpDir, "Run_1"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "Run_2"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "Run_1", "output.avg.fnc"), []byte("valid"), 0644)

	results, err := ScanDirectories(ScanOpts{
		SingleDir:         tmpDir,
		Pattern:           "Run_*",
		ValidationPattern: "*.avg.fnc",
		BaseJobName:       "TestJob",
		StartIndex:        1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (only validated), got %d", len(results))
	}
	if filepath.Base(results[0].Directory) != "Run_1" {
		t.Errorf("expected Run_1, got %s", filepath.Base(results[0].Directory))
	}
}

func TestScanDirectories_RecursiveValidation(t *testing.T) {
	tmpDir := t.TempDir()

	// Create Run_1 with validation file in a subdirectory (tests recursive walk)
	os.MkdirAll(filepath.Join(tmpDir, "Run_1", "subdir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "Run_1", "subdir", "result.avg.fnc"), []byte("valid"), 0644)

	results, err := ScanDirectories(ScanOpts{
		SingleDir:         tmpDir,
		Pattern:           "Run_*",
		ValidationPattern: "*.avg.fnc",
		BaseJobName:       "TestJob",
		StartIndex:        1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (recursively validated), got %d", len(results))
	}
}

func TestScanDirectories_MultiPart(t *testing.T) {
	tmpDir := t.TempDir()

	// Create two project directories
	doe1 := filepath.Join(tmpDir, "DOE_1")
	doe2 := filepath.Join(tmpDir, "DOE_2")

	os.MkdirAll(filepath.Join(doe1, "Run_1"), 0755)
	os.MkdirAll(filepath.Join(doe1, "Run_2"), 0755)
	os.MkdirAll(filepath.Join(doe2, "Run_1"), 0755)
	os.MkdirAll(filepath.Join(doe2, "Run_4"), 0755)

	results, err := ScanDirectories(ScanOpts{
		PartDirs:    []string{doe1, doe2},
		Pattern:     "Run_*",
		BaseJobName: "TestJob",
		StartIndex:  1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}

	// Verify project names are set
	for _, r := range results {
		if r.ProjectName == "" {
			t.Errorf("expected non-empty project name for multi-part mode")
		}
	}
}

func TestScanDirectories_MultiPartWithValidation(t *testing.T) {
	tmpDir := t.TempDir()

	doe1 := filepath.Join(tmpDir, "DOE_1")
	doe2 := filepath.Join(tmpDir, "DOE_2")

	os.MkdirAll(filepath.Join(doe1, "Run_1"), 0755)
	os.MkdirAll(filepath.Join(doe1, "Run_2"), 0755)
	os.MkdirAll(filepath.Join(doe2, "Run_1"), 0755)
	os.MkdirAll(filepath.Join(doe2, "Run_4"), 0755)

	// Only DOE_1/Run_1 and DOE_2/Run_4 have validation files
	os.WriteFile(filepath.Join(doe1, "Run_1", "output.avg.fnc"), []byte("valid"), 0644)
	os.WriteFile(filepath.Join(doe2, "Run_4", "output.avg.fnc"), []byte("valid"), 0644)

	results, err := ScanDirectories(ScanOpts{
		PartDirs:          []string{doe1, doe2},
		Pattern:           "Run_*",
		ValidationPattern: "*.avg.fnc",
		BaseJobName:       "TestJob",
		StartIndex:        1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 validated results, got %d", len(results))
	}

	// Verify the correct runs survived validation
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
}

func TestScanDirectories_WithRunSubpath(t *testing.T) {
	tmpDir := t.TempDir()

	// Create structure with subpath
	os.MkdirAll(filepath.Join(tmpDir, "Simcodes", "Powerflow", "Run_1"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "Simcodes", "Powerflow", "Run_2"), 0755)

	results, err := ScanDirectories(ScanOpts{
		SingleDir:   tmpDir,
		RunSubpath:  filepath.Join("Simcodes", "Powerflow"),
		Pattern:     "Run_*",
		BaseJobName: "TestJob",
		StartIndex:  1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestScanDirectories_EmptyPattern(t *testing.T) {
	_, err := ScanDirectories(ScanOpts{
		SingleDir:   "/tmp",
		BaseJobName: "TestJob",
	})
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
}

func TestScanDirectories_NoMatches(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := ScanDirectories(ScanOpts{
		SingleDir:   tmpDir,
		Pattern:     "Run_*",
		BaseJobName: "TestJob",
		StartIndex:  1,
	})
	if err == nil {
		t.Fatal("expected error for no matches")
	}
}
