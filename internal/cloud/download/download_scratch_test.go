package download

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// fakeStreamingProvider serves a v1 object whose sequential download writes
// the plaintext straight to the destination, as the real providers do.
type fakeStreamingProvider struct{ plaintext []byte }

func (fakeStreamingProvider) StorageType() string { return "S3Storage" }

func (fakeStreamingProvider) DetectFormat(context.Context, string) (int, string, int64, []byte, error) {
	return 1, base64.StdEncoding.EncodeToString([]byte("FAKEFILEID")), 1 << 20, nil, nil
}

func (p fakeStreamingProvider) DownloadStreaming(_ context.Context, _, localPath string, _ []byte, _ cloud.ProgressCallback) error {
	return os.WriteFile(localPath, p.plaintext, 0o644)
}

// Only the v0 format writes <file>.encrypted, and it removes its own copy. A
// download of any format removed whatever had that name once it finished: a
// user's own "results.encrypted" beside "results" was lost. The timing log
// quotes a server name that is not a safe file name.
func TestDownloadFileLeavesASiblingEncryptedFileAlone(t *testing.T) {
	orig := newProvider
	t.Cleanup(func() { newProvider = orig })
	newProvider = func(context.Context, *models.StorageInfo, *api.Client) (cloud.CloudTransfer, error) {
		return fakeStreamingProvider{plaintext: []byte("results")}, nil
	}

	t.Setenv("RESCALE_TIMING", "1")
	var timing bytes.Buffer
	dir := t.TempDir()
	localPath := filepath.Join(dir, "results")
	if err := os.WriteFile(localPath+".encrypted", []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := DownloadFile(context.Background(), DownloadParams{
		FileInfo: &models.CloudFile{
			Name:                 "evil\x1b[31m",
			Path:                 "user/abc/results",
			EncodedEncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
			DecryptedSize:        int64(len("results")),
			Storage:              &models.CloudFileStorage{StorageType: "S3Storage"},
		},
		LocalPath:    localPath,
		APIClient:    api.NewClientForTest(&config.Config{APIBaseURL: "http://127.0.0.1:1", APIKey: "test"}),
		OutputWriter: &timing,
	})
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if got, _ := os.ReadFile(localPath + ".encrypted"); string(got) != "mine" {
		t.Errorf("results.encrypted now holds %q, want it left alone", got)
	}
	if !strings.Contains(timing.String(), `File: "evil\x1b[31m"`) || strings.Contains(timing.String(), "\x1b") {
		t.Errorf("the timing log reads %q, want the name quoted", timing.String())
	}
}
