package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/cloud/upload"
	"github.com/rescale/rescale-int/internal/models"
)

// upload-dir names each file that failed relative to the folder uploaded,
// however that folder was given: by its own path, through a link (macOS's /tmp
// is one) or relative to the working directory.
func TestUploadDirNamesFailedFilesWithinTheFolder(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "tree")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "bad.dat"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(parent, alias); err != nil {
		t.Fatal(err)
	}
	defer func(orig func(context.Context, upload.UploadParams) (*models.CloudFile, error)) { uploadFileFn = orig }(uploadFileFn)
	uploadFileFn = func(context.Context, upload.UploadParams) (*models.CloudFile, error) {
		return nil, errors.New("failed to open file: permission denied")
	}

	want := "  - " + filepath.Join("sub", "bad.dat") + ": failed to open file: permission denied\n"
	for name, dir := range map[string]string{"own path": root, "through a link": filepath.Join(alias, "tree"), "relative": "tree"} {
		t.Run(name, func(t *testing.T) {
			if name == "relative" {
				t.Chdir(parent)
			}
			useFolderTreeAPI(t, &folderTreeAPI{children: map[string][]string{}})
			printed, err := runWithCancel(t, newFoldersCmd(), nil, "upload-dir", dir, "--parent-id", "lib")
			if err == nil || !strings.Contains(printed, want) {
				t.Errorf("upload-dir %s returned %v after printing\n%s\nwant the failure listed as %q", dir, err, printed, want)
			}
		})
	}
}
