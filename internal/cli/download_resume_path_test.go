package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/cloud/state"
	cloudtransfer "github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/crypto" // package name is 'encryption'
	"github.com/rescale/rescale-int/internal/models"
)

// legacyChunkedProvider serves a v0 object through the providers' shared
// chunked driver, recording the offset of every range it is asked for.
type legacyChunkedProvider struct {
	ciphertext, iv []byte
	mu             sync.Mutex
	opened         []int64
}

func (*legacyChunkedProvider) StorageType() string { return "S3Storage" }

func (p *legacyChunkedProvider) DetectFormat(context.Context, string) (int, string, int64, []byte, error) {
	return 0, "", 0, p.iv, nil
}

func (*legacyChunkedProvider) DownloadStreaming(context.Context, string, string, []byte, cloud.ProgressCallback) error {
	return nil
}

func (p *legacyChunkedProvider) DownloadEncryptedFile(ctx context.Context, params cloudtransfer.LegacyDownloadParams) error {
	return cloudtransfer.DownloadChunkedConcurrent(ctx, cloudtransfer.ChunkedConcurrentParams{
		LocalPath: params.EncryptedPath, RemotePath: params.RemotePath, TotalSize: int64(len(p.ciphertext)),
		ChunkSize: 64, Concurrency: 2, StorageType: "S3Storage",
		Retry: func(_ context.Context, _ string, fn func() error) error { return fn() },
		Open: func(_ context.Context, offset, length int64) (io.ReadCloser, error) {
			p.mu.Lock()
			p.opened = append(p.opened, offset)
			p.mu.Unlock()
			return io.NopCloser(bytes.NewReader(p.ciphertext[offset : offset+length])), nil
		},
	})
}

// Downloads now use absolute paths. A checkpoint an earlier version wrote for
// "--outdir results" holds the relative path of the same file, and was thrown
// away for not matching it character for character, so every chunk it
// recorded was fetched again. It is resumed.
func TestJobsDownloadResumesACheckpointThatHoldsARelativePath(t *testing.T) {
	plaintext := bytes.Repeat([]byte("interlink"), 64)
	enc, err := encryption.NewCBCStreamingEncryptor()
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := enc.EncryptPart(plaintext, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	const done = 4 * 64 // chunks 0-3 are on disk
	encrypted := filepath.Join("results", "out.dat.encrypted")
	if err := os.MkdirAll("results", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(encrypted, ciphertext[:done], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveDownloadState(&state.DownloadResumeState{
		LocalPath: encrypted, EncryptedPath: encrypted, TotalSize: int64(len(ciphertext)), DownloadedBytes: done,
		CreatedAt: time.Now(), ChunkSize: 64, CompletedChunks: []int64{0, 1, 2, 3}, StorageType: "S3Storage",
	}, encrypted); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	origList, origClient := listJobFilesFn, getAPIClientFn
	t.Cleanup(func() { listJobFilesFn, getAPIClientFn = origList, origClient })
	getAPIClientFn = func() (*api.Client, error) {
		return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), nil
	}
	listJobFilesFn = func(context.Context, *api.Client, string) ([]models.JobFile, error) {
		return []models.JobFile{{ID: "F1", Name: "out.dat", DecryptedSize: int64(len(plaintext)),
			EncodedEncryptionKey: base64.StdEncoding.EncodeToString(enc.GetKey())}}, nil
	}
	provider := &legacyChunkedProvider{ciphertext: ciphertext, iv: enc.GetInitialIV()}
	if _, err := runWithCancel(t, newJobsDownloadCmd(), func(ctx context.Context, p download.DownloadParams) error {
		_, err := cloudtransfer.NewDownloader(provider).Download(ctx, cloud.DownloadParams{RemotePath: "user/abc/out.dat", LocalPath: p.LocalPath, FileInfo: p.FileInfo})
		return err
	}, "--job-id", "JOB1", "--outdir", "results"); err != nil {
		t.Fatalf("jobs download: %v", err)
	}
	for _, offset := range provider.opened {
		if offset < done {
			t.Errorf("fetched the chunk at %d again; the checkpoint already held it (fetched %v)", offset, provider.opened)
		}
	}
	if got, _ := os.ReadFile(filepath.Join("results", "out.dat")); !bytes.Equal(got, plaintext) {
		t.Errorf("the download holds %d bytes, want the %d-byte file", len(got), len(plaintext))
	}
}
