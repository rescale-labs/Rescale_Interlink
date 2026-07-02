package wailsapp

import (
	"path/filepath"
	"testing"
)

func TestResolveSafeDownloadPath(t *testing.T) {
	dest := t.TempDir() // absolute everywhere; Windows puts "/tmp/output" on a drive

	tests := []struct {
		name    string
		rel     string
		wantErr bool
	}{
		{name: "nested path", rel: "subdir/file.txt"},
		{name: "simple filename", rel: "file.txt"},
		{name: "leading traversal is rejected", rel: "../../.ssh/authorized_keys", wantErr: true},
		{name: "traversal in the middle is rejected", rel: "subdir/../../etc/passwd", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveSafeDownloadPath(tt.rel, dest)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %q", tt.rel, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if want := filepath.Join(dest, tt.rel); got != want {
				t.Errorf("expected %q, got %q", want, got)
			}
		})
	}
}

func TestStripJobIOPrefix(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"output file", filepath.FromSlash("Output/results.dat"), "results.dat"},
		{"input file", filepath.FromSlash("Input/model.inp"), "model.inp"},
		{"nested under output", filepath.FromSlash("Output/run1/a.dat"), filepath.FromSlash("run1/a.dat")},
		{"bare output segment", "Output", ""},
		{"bare input segment", "Input", ""},
		{"case insensitive", filepath.FromSlash("output/x.txt"), "x.txt"},
		{"no io prefix unchanged", filepath.FromSlash("data/x.txt"), filepath.FromSlash("data/x.txt")},
		{"nested output unchanged", filepath.FromSlash("data/Output/x.txt"), filepath.FromSlash("data/Output/x.txt")},
		{"split inside the split kept", filepath.FromSlash("Output/input/x.txt"), filepath.FromSlash("Output/input/x.txt")},
		{"split folder inside the split kept", filepath.FromSlash("Input/Output"), filepath.FromSlash("Input/Output")},
		{"prefix substring not stripped", filepath.FromSlash("Outputs/x.txt"), filepath.FromSlash("Outputs/x.txt")},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripJobIOPrefix(tt.input); got != tt.want {
				t.Errorf("stripJobIOPrefix(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
