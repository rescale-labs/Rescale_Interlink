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
