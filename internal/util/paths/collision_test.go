package paths

import (
	"testing"
)

func TestResolveCollisions_NoCollisions(t *testing.T) {
	files := []FileForDownload{
		{FileID: "ABC123", Name: "file1.zip", LocalPath: "/dest/file1.zip", Size: 100},
		{FileID: "DEF456", Name: "file2.zip", LocalPath: "/dest/file2.zip", Size: 200},
		{FileID: "GHI789", Name: "file3.zip", LocalPath: "/dest/file3.zip", Size: 300},
	}

	result, count := ResolveCollisions(files)

	if count != 0 {
		t.Errorf("expected 0 collisions, got %d", count)
	}
	// Paths should be unchanged
	if result[0].LocalPath != "/dest/file1.zip" {
		t.Errorf("expected /dest/file1.zip, got %s", result[0].LocalPath)
	}
	if result[1].LocalPath != "/dest/file2.zip" {
		t.Errorf("expected /dest/file2.zip, got %s", result[1].LocalPath)
	}
	if result[2].LocalPath != "/dest/file3.zip" {
		t.Errorf("expected /dest/file3.zip, got %s", result[2].LocalPath)
	}
}

func TestResolveCollisions_TwoDuplicates(t *testing.T) {
	files := []FileForDownload{
		{FileID: "ABC123", Name: "output.zip", LocalPath: "/dest/output.zip", Size: 100},
		{FileID: "DEF456", Name: "output.zip", LocalPath: "/dest/output.zip", Size: 200},
	}

	result, count := ResolveCollisions(files)

	if count != 2 {
		t.Errorf("expected 2 collisions, got %d", count)
	}
	// Both paths should now include FileID
	if result[0].LocalPath != "/dest/output_ABC123.zip" {
		t.Errorf("expected /dest/output_ABC123.zip, got %s", result[0].LocalPath)
	}
	if result[1].LocalPath != "/dest/output_DEF456.zip" {
		t.Errorf("expected /dest/output_DEF456.zip, got %s", result[1].LocalPath)
	}
}

func TestResolveCollisions_ThreeDuplicates(t *testing.T) {
	files := []FileForDownload{
		{FileID: "A", Name: "model.sim", LocalPath: "/out/model.sim", Size: 100},
		{FileID: "B", Name: "model.sim", LocalPath: "/out/model.sim", Size: 200},
		{FileID: "C", Name: "model.sim", LocalPath: "/out/model.sim", Size: 300},
	}

	result, count := ResolveCollisions(files)

	if count != 3 {
		t.Errorf("expected 3 collisions, got %d", count)
	}
	if result[0].LocalPath != "/out/model_A.sim" {
		t.Errorf("expected /out/model_A.sim, got %s", result[0].LocalPath)
	}
	if result[1].LocalPath != "/out/model_B.sim" {
		t.Errorf("expected /out/model_B.sim, got %s", result[1].LocalPath)
	}
	if result[2].LocalPath != "/out/model_C.sim" {
		t.Errorf("expected /out/model_C.sim, got %s", result[2].LocalPath)
	}
}

func TestResolveCollisions_MixedDuplicatesAndUnique(t *testing.T) {
	files := []FileForDownload{
		{FileID: "A", Name: "unique.txt", LocalPath: "/dest/unique.txt", Size: 100},
		{FileID: "B", Name: "duplicate.zip", LocalPath: "/dest/duplicate.zip", Size: 200},
		{FileID: "C", Name: "duplicate.zip", LocalPath: "/dest/duplicate.zip", Size: 300},
		{FileID: "D", Name: "another.dat", LocalPath: "/dest/another.dat", Size: 400},
	}

	result, count := ResolveCollisions(files)

	if count != 2 {
		t.Errorf("expected 2 collisions (only the duplicates), got %d", count)
	}
	// Unique files should be unchanged
	if result[0].LocalPath != "/dest/unique.txt" {
		t.Errorf("expected /dest/unique.txt, got %s", result[0].LocalPath)
	}
	// Duplicates should have FileID appended
	if result[1].LocalPath != "/dest/duplicate_B.zip" {
		t.Errorf("expected /dest/duplicate_B.zip, got %s", result[1].LocalPath)
	}
	if result[2].LocalPath != "/dest/duplicate_C.zip" {
		t.Errorf("expected /dest/duplicate_C.zip, got %s", result[2].LocalPath)
	}
	// Another unique
	if result[3].LocalPath != "/dest/another.dat" {
		t.Errorf("expected /dest/another.dat, got %s", result[3].LocalPath)
	}
}

func TestResolveCollisions_NoExtension(t *testing.T) {
	files := []FileForDownload{
		{FileID: "ABC", Name: "README", LocalPath: "/dest/README", Size: 100},
		{FileID: "DEF", Name: "README", LocalPath: "/dest/README", Size: 200},
	}

	result, count := ResolveCollisions(files)

	if count != 2 {
		t.Errorf("expected 2 collisions, got %d", count)
	}
	// Should append FileID even without extension
	if result[0].LocalPath != "/dest/README_ABC" {
		t.Errorf("expected /dest/README_ABC, got %s", result[0].LocalPath)
	}
	if result[1].LocalPath != "/dest/README_DEF" {
		t.Errorf("expected /dest/README_DEF, got %s", result[1].LocalPath)
	}
}

func TestResolveCollisions_DifferentDirectories(t *testing.T) {
	// Same filename but different directories = no collision
	files := []FileForDownload{
		{FileID: "A", Name: "data.txt", LocalPath: "/dest/dir1/data.txt", Size: 100},
		{FileID: "B", Name: "data.txt", LocalPath: "/dest/dir2/data.txt", Size: 200},
	}

	result, count := ResolveCollisions(files)

	if count != 0 {
		t.Errorf("expected 0 collisions (different directories), got %d", count)
	}
	// Paths should be unchanged
	if result[0].LocalPath != "/dest/dir1/data.txt" {
		t.Errorf("expected /dest/dir1/data.txt, got %s", result[0].LocalPath)
	}
	if result[1].LocalPath != "/dest/dir2/data.txt" {
		t.Errorf("expected /dest/dir2/data.txt, got %s", result[1].LocalPath)
	}
}

func TestResolveCollisions_EmptyList(t *testing.T) {
	files := []FileForDownload{}

	result, count := ResolveCollisions(files)

	if count != 0 {
		t.Errorf("expected 0 collisions for empty list, got %d", count)
	}
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d items", len(result))
	}
}

func TestResolveCollisions_SingleFile(t *testing.T) {
	files := []FileForDownload{
		{FileID: "ABC", Name: "single.zip", LocalPath: "/dest/single.zip", Size: 100},
	}

	result, count := ResolveCollisions(files)

	if count != 0 {
		t.Errorf("expected 0 collisions for single file, got %d", count)
	}
	if result[0].LocalPath != "/dest/single.zip" {
		t.Errorf("expected /dest/single.zip, got %s", result[0].LocalPath)
	}
}

func TestResolveCollisions_MultipleExtensions(t *testing.T) {
	// Files with multiple dots in name
	files := []FileForDownload{
		{FileID: "A", Name: "data.tar.gz", LocalPath: "/dest/data.tar.gz", Size: 100},
		{FileID: "B", Name: "data.tar.gz", LocalPath: "/dest/data.tar.gz", Size: 200},
	}

	result, count := ResolveCollisions(files)

	if count != 2 {
		t.Errorf("expected 2 collisions, got %d", count)
	}
	// Only last extension preserved
	if result[0].LocalPath != "/dest/data.tar_A.gz" {
		t.Errorf("expected /dest/data.tar_A.gz, got %s", result[0].LocalPath)
	}
	if result[1].LocalPath != "/dest/data.tar_B.gz" {
		t.Errorf("expected /dest/data.tar_B.gz, got %s", result[1].LocalPath)
	}
}
