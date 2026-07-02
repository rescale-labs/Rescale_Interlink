package wailsapp

import (
	"path/filepath"
	"testing"
)

func TestResolveSafeDownloadPath_Normal(t *testing.T) {
	result, err := resolveSafeDownloadPath("subdir/file.txt", "/tmp/output")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join("/tmp/output", "subdir/file.txt")
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestResolveSafeDownloadPath_TraversalRejected(t *testing.T) {
	_, err := resolveSafeDownloadPath("../../.ssh/authorized_keys", "/tmp/output")
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}

func TestResolveSafeDownloadPath_DotDotInMiddle(t *testing.T) {
	_, err := resolveSafeDownloadPath("subdir/../../etc/passwd", "/tmp/output")
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}

func TestResolveSafeDownloadPath_SimpleFilename(t *testing.T) {
	result, err := resolveSafeDownloadPath("file.txt", "/tmp/output")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join("/tmp/output", "file.txt")
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
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
