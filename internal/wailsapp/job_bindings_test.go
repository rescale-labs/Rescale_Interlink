// Package wailsapp provides tests for job bindings.
package wailsapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestScanDirectoryNoEngine verifies ScanDirectory returns error when engine is nil.
func TestScanDirectoryNoEngine(t *testing.T) {
	app := &App{} // engine is nil

	result := app.ScanDirectory(ScanOptionsDTO{
		RootDir: "/tmp",
		Pattern: "Run_*",
	}, JobSpecDTO{})

	if result.Error == "" {
		t.Fatal("expected error when engine is nil")
	}
	if result.Error != ErrNoEngine.Error() {
		t.Errorf("expected ErrNoEngine, got: %s", result.Error)
	}
}

// =============================================================================
// Template and normalization tests
// =============================================================================

// TestNormalizeJobSpecDTO verifies nil slices are replaced with empty slices,
// zero numeric fields get sensible defaults, existing values are preserved, and
// the result stays marshalable (the Wails binding panic scenario).
func TestNormalizeJobSpecDTO(t *testing.T) {
	tests := []struct {
		name string
		json string // when set, the input is unmarshaled from this JSON instead of using in
		in   JobSpecDTO
		want JobSpecDTO
	}{
		{
			name: "nil slices and zero numerics get defaults",
			in:   JobSpecDTO{},
			want: JobSpecDTO{Tags: []string{}, Automations: []string{}, InputFiles: []string{}, CoresPerSlot: 1, Slots: 1, WalltimeHours: 1.0},
		},
		{
			name: "non-zero and non-nil values preserved",
			in:   JobSpecDTO{Tags: []string{"tag1", "tag2"}, Automations: []string{"auto1"}, InputFiles: []string{"file1.inp"}, CoresPerSlot: 4, Slots: 2, WalltimeHours: 8.5},
			want: JobSpecDTO{Tags: []string{"tag1", "tag2"}, Automations: []string{"auto1"}, InputFiles: []string{"file1.inp"}, CoresPerSlot: 4, Slots: 2, WalltimeHours: 8.5},
		},
		{
			name: "minimal JSON with missing fields",
			json: `{"jobName":"test","analysisCode":"openfoam"}`,
			want: JobSpecDTO{Tags: []string{}, Automations: []string{}, InputFiles: []string{}, CoresPerSlot: 1, Slots: 1, WalltimeHours: 1.0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := tt.in
			if tt.json != "" {
				job = JobSpecDTO{}
				if err := json.Unmarshal([]byte(tt.json), &job); err != nil {
					t.Fatalf("unmarshal failed: %v", err)
				}
			}

			normalizeJobSpecDTO(&job)

			if !reflect.DeepEqual(job.Tags, tt.want.Tags) {
				t.Errorf("Tags = %#v, want %#v", job.Tags, tt.want.Tags)
			}
			if !reflect.DeepEqual(job.Automations, tt.want.Automations) {
				t.Errorf("Automations = %#v, want %#v", job.Automations, tt.want.Automations)
			}
			if !reflect.DeepEqual(job.InputFiles, tt.want.InputFiles) {
				t.Errorf("InputFiles = %#v, want %#v", job.InputFiles, tt.want.InputFiles)
			}
			if job.CoresPerSlot != tt.want.CoresPerSlot {
				t.Errorf("CoresPerSlot = %d, want %d", job.CoresPerSlot, tt.want.CoresPerSlot)
			}
			if job.Slots != tt.want.Slots {
				t.Errorf("Slots = %d, want %d", job.Slots, tt.want.Slots)
			}
			if job.WalltimeHours != tt.want.WalltimeHours {
				t.Errorf("WalltimeHours = %f, want %f", job.WalltimeHours, tt.want.WalltimeHours)
			}

			out, err := json.Marshal(job)
			if err != nil {
				t.Fatalf("marshal after normalization failed: %v", err)
			}
			if len(out) == 0 {
				t.Error("marshal produced empty output")
			}
		})
	}
}

// withTemplatesHome redirects getTemplatesDir() at a temporary home so the
// template bindings can be exercised for real. It returns the templates dir.
func withTemplatesHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)        // os.UserHomeDir on unix
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on windows
	dir := filepath.Join(home, ".config", "rescale", "templates")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create templates dir: %v", err)
	}
	return dir
}

// TestListSavedTemplatesNormalizes verifies normalizeJobSpecDTO's nil-safe
// defaults are applied to on-disk template JSON (null tags, zero numerics), so
// the GUI never receives a nil slice or a zero core/slot/walltime value.
// Corrupt-file handling is covered by TestTemplateRoundTrip.
func TestListSavedTemplatesNormalizes(t *testing.T) {
	dir := withTemplatesHome(t)
	app := &App{}

	tests := []struct {
		name string
		json string
	}{
		{name: "nil-tags", json: `{"jobName":"test-job","analysisCode":"openfoam","tags":null,"coresPerSlot":0}`},
		{name: "minimal", json: `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, tt.name+".json"), []byte(tt.json), 0644); err != nil {
				t.Fatalf("failed to write template: %v", err)
			}

			// Subtests share the templates dir, so select by name rather than index.
			var job JobSpecDTO
			found := false
			for _, tpl := range app.ListSavedTemplates() {
				if tpl.Name == tt.name {
					job, found = tpl.Job, true
				}
			}
			if !found {
				t.Fatalf("template %q missing from ListSavedTemplates", tt.name)
			}

			if job.Tags == nil || job.Automations == nil || job.InputFiles == nil {
				t.Error("all slice fields should be non-nil after load and normalize")
			}
			if job.CoresPerSlot != 1 || job.Slots != 1 || job.WalltimeHours != 1.0 {
				t.Errorf("numeric defaults not applied: cores=%d slots=%d walltime=%f",
					job.CoresPerSlot, job.Slots, job.WalltimeHours)
			}
		})
	}
}

// TestTemplateRoundTrip verifies SaveTemplate writes atomically (no .tmp left
// behind) with the spec intact on disk, ListSavedTemplates reports it while
// skipping corrupt and non-JSON files, and DeleteTemplate removes it.
func TestTemplateRoundTrip(t *testing.T) {
	dir := withTemplatesHome(t)
	app := &App{}

	job := JobSpecDTO{
		JobName:       "atomic-test",
		AnalysisCode:  "openfoam",
		CoresPerSlot:  4,
		Slots:         2,
		WalltimeHours: 8.0,
		Tags:          []string{"test"},
	}
	if err := app.SaveTemplate("valid", job); err != nil {
		t.Fatalf("SaveTemplate failed: %v", err)
	}

	fullPath := filepath.Join(dir, "valid.json")
	if _, err := os.Stat(fullPath); err != nil {
		t.Errorf("final file should exist after atomic write: %v", err)
	}
	if _, err := os.Stat(fullPath + ".tmp"); !os.IsNotExist(err) {
		t.Error("tmp file should not exist after successful rename")
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("failed to read saved template: %v", err)
	}
	var saved JobSpecDTO
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("saved template is not valid JSON: %v", err)
	}
	if saved.JobName != "atomic-test" {
		t.Errorf("expected job name 'atomic-test', got '%s'", saved.JobName)
	}

	// Corrupt and non-JSON files must be skipped, not fatal.
	if err := os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte(`{broken`), 0644); err != nil {
		t.Fatalf("failed to write corrupt template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not a template"), 0644); err != nil {
		t.Fatalf("failed to write non-json file: %v", err)
	}

	templates := app.ListSavedTemplates()
	if len(templates) != 1 {
		t.Fatalf("expected 1 valid template, got %d", len(templates))
	}
	if templates[0].Name != "valid" {
		t.Errorf("expected template name 'valid', got '%s'", templates[0].Name)
	}
	if templates[0].Job.Tags == nil {
		t.Error("valid template Tags should be normalized to non-nil")
	}

	if err := app.DeleteTemplate("valid"); err != nil {
		t.Fatalf("DeleteTemplate failed: %v", err)
	}
	if remaining := app.ListSavedTemplates(); len(remaining) != 0 {
		t.Errorf("expected 0 templates after delete, got %d", len(remaining))
	}
}

// =============================================================================
// Run history and historical job row tests
// =============================================================================

func TestGetHistoricalJobRows_PathTraversal(t *testing.T) {
	app := &App{}

	// Test various path traversal attempts
	testCases := []string{
		"../../../etc/passwd",
		"../../secret",
		"foo/../bar",
		"/absolute/path",
		"normal/../sneaky",
	}

	for _, tc := range testCases {
		_, err := app.GetHistoricalJobRows(tc)
		if err == nil {
			t.Errorf("expected error for run ID %q, got nil", tc)
		}
		if err != nil && !strings.Contains(err.Error(), "invalid run ID") {
			// Absolute paths will fail at filepath.Base check differently
			// but still should not succeed
			t.Logf("run ID %q correctly rejected: %v", tc, err)
		}
	}
}

func TestGetHistoricalJobRows_MissingFile(t *testing.T) {
	app := &App{}
	_, err := app.GetHistoricalJobRows("nonexistent_run_12345")
	if err == nil {
		t.Error("expected error for missing state file")
	}
}

func TestGetRunHistory_EmptyDir(t *testing.T) {
	app := &App{}
	// This should return empty slice, not panic, even if states dir doesn't exist
	results := app.GetRunHistory()
	if results == nil {
		// nil is acceptable too, but empty slice is preferred
		t.Log("GetRunHistory returned nil for missing dir (acceptable)")
	}
}
