package paths

import (
	"testing"
)

func TestResolveCollisions(t *testing.T) {
	tests := []struct {
		name      string
		files     []FileForDownload
		wantCount int
		wantPaths []string
	}{
		{
			name: "no collisions",
			files: []FileForDownload{
				{FileID: "ABC123", Name: "file1.zip", LocalPath: "/dest/file1.zip", Size: 100},
				{FileID: "DEF456", Name: "file2.zip", LocalPath: "/dest/file2.zip", Size: 200},
				{FileID: "GHI789", Name: "file3.zip", LocalPath: "/dest/file3.zip", Size: 300},
			},
			wantCount: 0,
			wantPaths: []string{"/dest/file1.zip", "/dest/file2.zip", "/dest/file3.zip"},
		},
		{
			name: "two duplicates both get the file ID",
			files: []FileForDownload{
				{FileID: "ABC123", Name: "output.zip", LocalPath: "/dest/output.zip", Size: 100},
				{FileID: "DEF456", Name: "output.zip", LocalPath: "/dest/output.zip", Size: 200},
			},
			wantCount: 2,
			wantPaths: []string{"/dest/output_ABC123.zip", "/dest/output_DEF456.zip"},
		},
		{
			name: "three duplicates",
			files: []FileForDownload{
				{FileID: "A", Name: "model.sim", LocalPath: "/out/model.sim", Size: 100},
				{FileID: "B", Name: "model.sim", LocalPath: "/out/model.sim", Size: 200},
				{FileID: "C", Name: "model.sim", LocalPath: "/out/model.sim", Size: 300},
			},
			wantCount: 3,
			wantPaths: []string{"/out/model_A.sim", "/out/model_B.sim", "/out/model_C.sim"},
		},
		{
			name: "only the duplicates are renamed",
			files: []FileForDownload{
				{FileID: "A", Name: "unique.txt", LocalPath: "/dest/unique.txt", Size: 100},
				{FileID: "B", Name: "duplicate.zip", LocalPath: "/dest/duplicate.zip", Size: 200},
				{FileID: "C", Name: "duplicate.zip", LocalPath: "/dest/duplicate.zip", Size: 300},
				{FileID: "D", Name: "another.dat", LocalPath: "/dest/another.dat", Size: 400},
			},
			wantCount: 2,
			wantPaths: []string{"/dest/unique.txt", "/dest/duplicate_B.zip", "/dest/duplicate_C.zip", "/dest/another.dat"},
		},
		{
			name: "no extension still gets the file ID",
			files: []FileForDownload{
				{FileID: "ABC", Name: "README", LocalPath: "/dest/README", Size: 100},
				{FileID: "DEF", Name: "README", LocalPath: "/dest/README", Size: 200},
			},
			wantCount: 2,
			wantPaths: []string{"/dest/README_ABC", "/dest/README_DEF"},
		},
		{
			name: "same name in different directories is not a collision",
			files: []FileForDownload{
				{FileID: "A", Name: "data.txt", LocalPath: "/dest/dir1/data.txt", Size: 100},
				{FileID: "B", Name: "data.txt", LocalPath: "/dest/dir2/data.txt", Size: 200},
			},
			wantCount: 0,
			wantPaths: []string{"/dest/dir1/data.txt", "/dest/dir2/data.txt"},
		},
		{
			name:      "empty list",
			files:     []FileForDownload{},
			wantCount: 0,
			wantPaths: []string{},
		},
		{
			name: "single file",
			files: []FileForDownload{
				{FileID: "ABC", Name: "single.zip", LocalPath: "/dest/single.zip", Size: 100},
			},
			wantCount: 0,
			wantPaths: []string{"/dest/single.zip"},
		},
		{
			name: "multiple dots keep only the last extension",
			files: []FileForDownload{
				{FileID: "A", Name: "data.tar.gz", LocalPath: "/dest/data.tar.gz", Size: 100},
				{FileID: "B", Name: "data.tar.gz", LocalPath: "/dest/data.tar.gz", Size: 200},
			},
			wantCount: 2,
			wantPaths: []string{"/dest/data.tar_A.gz", "/dest/data.tar_B.gz"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, count := ResolveCollisions(tt.files)

			if count != tt.wantCount {
				t.Errorf("collisions = %d, want %d", count, tt.wantCount)
			}
			if len(result) != len(tt.wantPaths) {
				t.Fatalf("result has %d items, want %d", len(result), len(tt.wantPaths))
			}
			for i, want := range tt.wantPaths {
				if result[i].LocalPath != want {
					t.Errorf("result[%d].LocalPath = %s, want %s", i, result[i].LocalPath, want)
				}
			}
		})
	}
}
