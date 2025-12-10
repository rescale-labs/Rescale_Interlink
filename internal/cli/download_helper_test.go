package cli

import (
	"path/filepath"
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

// TestBuildJobFileOutputPaths tests the filename collision detection and disambiguation.
// v3.2.2: Added to test fix for concurrent download corruption bug.
func TestBuildJobFileOutputPaths(t *testing.T) {
	tests := []struct {
		name       string
		files      []models.JobFile
		outputDir  string
		wantPaths  map[string]string // fileID -> expected path
		wantDupes  bool               // expect duplicate warning
	}{
		{
			name: "no collisions - unique names",
			files: []models.JobFile{
				{ID: "ABC123", Name: "model.sim"},
				{ID: "DEF456", Name: "output.log"},
				{ID: "GHI789", Name: "results.csv"},
			},
			outputDir: "/tmp/download",
			wantPaths: map[string]string{
				"ABC123": "/tmp/download/model.sim",
				"DEF456": "/tmp/download/output.log",
				"GHI789": "/tmp/download/results.csv",
			},
			wantDupes: false,
		},
		{
			name: "two files with same name - collision detected",
			files: []models.JobFile{
				{ID: "ABC123", Name: "model.sim"},
				{ID: "DEF456", Name: "model.sim"},
			},
			outputDir: "/tmp/download",
			wantPaths: map[string]string{
				"ABC123": "/tmp/download/model_ABC123.sim",
				"DEF456": "/tmp/download/model_DEF456.sim",
			},
			wantDupes: true,
		},
		{
			name: "three files with same name - all disambiguated",
			files: []models.JobFile{
				{ID: "ABC123", Name: "model.sim"},
				{ID: "DEF456", Name: "model.sim"},
				{ID: "GHI789", Name: "model.sim"},
			},
			outputDir: "/tmp/download",
			wantPaths: map[string]string{
				"ABC123": "/tmp/download/model_ABC123.sim",
				"DEF456": "/tmp/download/model_DEF456.sim",
				"GHI789": "/tmp/download/model_GHI789.sim",
			},
			wantDupes: true,
		},
		{
			name: "mixed - some collisions some unique",
			files: []models.JobFile{
				{ID: "ABC123", Name: "model.sim"},
				{ID: "DEF456", Name: "model.sim"},
				{ID: "GHI789", Name: "output.log"},
			},
			outputDir: "/tmp/download",
			wantPaths: map[string]string{
				"ABC123": "/tmp/download/model_ABC123.sim",
				"DEF456": "/tmp/download/model_DEF456.sim",
				"GHI789": "/tmp/download/output.log", // unique - no suffix
			},
			wantDupes: true,
		},
		{
			name: "file with no extension",
			files: []models.JobFile{
				{ID: "ABC123", Name: "README"},
				{ID: "DEF456", Name: "README"},
			},
			outputDir: "/tmp/download",
			wantPaths: map[string]string{
				"ABC123": "/tmp/download/README_ABC123",
				"DEF456": "/tmp/download/README_DEF456",
			},
			wantDupes: true,
		},
		{
			name: "files with relative paths - different dirs no collision",
			files: []models.JobFile{
				{ID: "ABC123", Name: "model.sim", RelativePath: "run1/model.sim"},
				{ID: "DEF456", Name: "model.sim", RelativePath: "run2/model.sim"},
			},
			outputDir: "/tmp/download",
			wantPaths: map[string]string{
				"ABC123": "/tmp/download/run1/model.sim",
				"DEF456": "/tmp/download/run2/model.sim",
			},
			wantDupes: false,
		},
		{
			name: "files with same relative path - collision",
			files: []models.JobFile{
				{ID: "ABC123", Name: "model.sim", RelativePath: "output/model.sim"},
				{ID: "DEF456", Name: "model.sim", RelativePath: "output/model.sim"},
			},
			outputDir: "/tmp/download",
			wantPaths: map[string]string{
				"ABC123": "/tmp/download/output/model_ABC123.sim",
				"DEF456": "/tmp/download/output/model_DEF456.sim",
			},
			wantDupes: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildJobFileOutputPaths(tt.files, tt.outputDir)

			// Check each expected path
			for fileID, wantPath := range tt.wantPaths {
				// Normalize paths for comparison
				wantPath = filepath.Clean(wantPath)
				gotPath := filepath.Clean(got[fileID])

				if gotPath != wantPath {
					t.Errorf("file %s: got path %q, want %q", fileID, gotPath, wantPath)
				}
			}

			// Check we got the right number of paths
			if len(got) != len(tt.wantPaths) {
				t.Errorf("got %d paths, want %d", len(got), len(tt.wantPaths))
			}
		})
	}
}
