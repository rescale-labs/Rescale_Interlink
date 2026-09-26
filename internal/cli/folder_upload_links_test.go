package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/upload"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/resources"
)

// folders upload-dir leaves out a link to a file beside the chosen folder and a
// broken link, says so once per link with the reason, and counts them for the
// summary.
func TestUploadDirectoryPipelined_ReportsLinksLeftOut(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "tree")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"data.txt": root, "beside.txt": parent} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{"outside.txt": filepath.Join(parent, "beside.txt"), "broken": "missing"} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatalf("symlink %s: %v", link, err)
		}
	}

	origCheck, origUpload := checkFileExistsFn, uploadFileFn
	defer func() { checkFileExistsFn, uploadFileFn = origCheck, origUpload }()
	checkFileExistsFn = func(context.Context, *api.Client, *FolderCache, string, string) (string, bool, error) {
		return "", false, nil
	}
	var uploaded []string // one worker, so no lock
	uploadFileFn = func(_ context.Context, p upload.UploadParams) (*models.CloudFile, error) {
		uploaded = append(uploaded, filepath.Base(p.LocalPath))
		return &models.CloudFile{ID: "file-1"}, nil
	}

	var result *UploadResult
	said := captureStderr(t, func() {
		var err error
		result, _, err = uploadDirectoryPipelined(context.Background(), nil, NewFolderCache(), root, "root-id",
			true, 1, 1, false, &config.Config{}, GetLogger(),
			resources.NewManager(resources.Config{MaxThreads: 1}))
		if err != nil {
			t.Fatal(err)
		}
	})

	if result.SymlinksSkipped != 2 {
		t.Errorf("SymlinksSkipped = %d, want 2", result.SymlinksSkipped)
	}
	if len(uploaded) != 1 || uploaded[0] != "data.txt" {
		t.Errorf("uploaded %v, want only data.txt", uploaded)
	}
	for _, want := range []string{
		"Skipped link outside.txt: its target is outside the folder being uploaded\n",
		"Skipped link broken: its target is missing or cannot be read\n",
	} {
		if strings.Count(said, want) != 1 {
			t.Errorf("want %q once in:\n%s", want, said)
		}
	}
}
