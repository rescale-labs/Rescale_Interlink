package glob

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandPatterns_NonGlob(t *testing.T) {
	// Use a real file to test absolute path resolution
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("hello"), 0644)

	result, err := ExpandPatterns([]string{testFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}

	absExpected, _ := filepath.Abs(testFile)
	if result[0] != absExpected {
		t.Errorf("expected %q, got %q", absExpected, result[0])
	}
}

func TestExpandPatterns_Glob(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "b.txt"), []byte("b"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "c.dat"), []byte("c"), 0644)

	result, err := ExpandPatterns([]string{filepath.Join(tmpDir, "*.txt")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d: %v", len(result), result)
	}
}

func TestExpandPatterns_Dedup(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("hello"), 0644)

	result, err := ExpandPatterns([]string{testFile, testFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("expected 1 result (deduplicated), got %d", len(result))
	}
}

func TestExpandPatterns_NoMatch(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := ExpandPatterns([]string{filepath.Join(tmpDir, "*.nonexistent")})
	if err == nil {
		t.Fatal("expected error for no-match glob, got nil")
	}
}

func TestExpandPatterns_Empty(t *testing.T) {
	result, err := ExpandPatterns(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 results, got %d", len(result))
	}
}
