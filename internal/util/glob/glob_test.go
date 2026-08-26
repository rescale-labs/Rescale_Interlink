package glob

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandPatterns(t *testing.T) {
	tests := []struct {
		name string
		// files created in the temp dir before expanding.
		files []string
		// patterns are joined onto the temp dir unless the case passes none.
		patterns []string
		wantLen  int
		wantErr  bool
		// wantAbsOf, when set, is the file whose absolute path the single
		// result must equal.
		wantAbsOf string
	}{
		{
			name:  "non-glob path resolves to absolute",
			files: []string{"test.txt"}, patterns: []string{"test.txt"},
			wantLen: 1, wantAbsOf: "test.txt",
		},
		{
			name:  "glob matches only the extension asked for",
			files: []string{"a.txt", "b.txt", "c.dat"}, patterns: []string{"*.txt"},
			wantLen: 2,
		},
		{
			name:  "repeated path is deduplicated",
			files: []string{"test.txt"}, patterns: []string{"test.txt", "test.txt"},
			wantLen: 1,
		},
		{
			name:     "glob with no matches is an error",
			patterns: []string{"*.nonexistent"}, wantErr: true,
		},
		{
			name: "no patterns yields no results",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0644); err != nil {
					t.Fatalf("WriteFile %s: %v", f, err)
				}
			}

			var patterns []string
			for _, p := range tt.patterns {
				patterns = append(patterns, filepath.Join(dir, p))
			}

			result, err := ExpandPatterns(patterns)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", result)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(result) != tt.wantLen {
				t.Fatalf("expected %d results, got %d: %v", tt.wantLen, len(result), result)
			}
			if tt.wantAbsOf != "" {
				want, _ := filepath.Abs(filepath.Join(dir, tt.wantAbsOf))
				if result[0] != want {
					t.Errorf("expected %q, got %q", want, result[0])
				}
			}
		})
	}
}
