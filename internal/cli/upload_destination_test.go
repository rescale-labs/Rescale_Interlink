package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/upload"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// An upload to the top of My Library names it as the destination on the
// progress and result lines, rather than leaving the arrow pointing at nothing.
func TestUploadToLibraryRootNamesMyLibrary(t *testing.T) {
	defer func(orig func(context.Context, upload.UploadParams) (*models.CloudFile, error)) { uploadFileFn = orig }(uploadFileFn)
	uploadFileFn = func(context.Context, upload.UploadParams) (*models.CloudFile, error) {
		return &models.CloudFile{ID: "file-1"}, nil
	}
	apiClient := api.NewClientForTest(&config.Config{APIBaseURL: "http://127.0.0.1:1", APIKey: "test"})
	var err error
	printed := captureStdout(t, func() {
		_, err = UploadFilesWithIDs(context.Background(), []string{writeUploadFixture(t, "a.bin", 16)},
			"", 1, false, nil, apiClient, GetLogger(), false)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"(0.0 MiB) → My Library\n", "→ My Library (FileID: file-1"} {
		if !strings.Contains(printed, want) {
			t.Errorf("printed\n%s\nwant %q", printed, want)
		}
	}
}
