package s3

import (
	"context"
	"fmt"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rescale/rescale-int/internal/cloud/providers/testsupport"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/resources"
)

// strippedS3Backend answers like S3 but leaves out one response field, the way
// a proxy that strips headers does. The SDK then hands back a nil pointer for
// that field, which the provider dereferenced unguarded — in upload and
// download workers that have no recover, so the whole process died.
type strippedS3Backend struct {
	strip string // "Content-Length", "UploadId" or "ETag"

	// What reached it after the field went missing: parts, and completions.
	parts, completions atomic.Int32
}

func (b *strippedS3Backend) ServeHTTP(w nethttp.ResponseWriter, r *nethttp.Request) {
	query := r.URL.Query()
	_, _ = io.Copy(io.Discard, r.Body)
	switch {
	case r.Method == nethttp.MethodHead:
		if b.strip != "Content-Length" {
			w.Header().Set("Content-Length", "64")
		}
		w.Header().Set("ETag", `"version-one"`)
		w.WriteHeader(nethttp.StatusOK)
	case r.Method == nethttp.MethodPost && query.Has("uploads"):
		uploadID := "<UploadId>stripped-upload</UploadId>"
		if b.strip == "UploadId" {
			uploadID = ""
		}
		writeXML(w, nethttp.StatusOK, fmt.Sprintf(
			`<InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>k</Key>%s</InitiateMultipartUploadResult>`, testBucket, uploadID))
	case r.Method == nethttp.MethodPut && query.Has("partNumber"):
		b.parts.Add(1)
		if b.strip != "ETag" {
			w.Header().Set("ETag", `"part"`)
		}
		w.WriteHeader(nethttp.StatusOK)
	case r.Method == nethttp.MethodDelete:
		w.WriteHeader(nethttp.StatusNoContent)
	default:
		if r.Method == nethttp.MethodPost {
			b.completions.Add(1)
		}
		writeXML(w, nethttp.StatusNotImplemented,
			`<Error><Code>NotImplemented</Code><Message>unexpected request</Message></Error>`)
	}
}

func TestMissingResponseFieldsFailTheFileInsteadOfPanicking(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		strip    string
		maxParts int32 // parts it may send before it stops: the first, or one per worker
		call     func(t *testing.T, p *Provider, c *S3Client) error
	}{
		{"Content-Length", 0, func(_ *testing.T, p *Provider, _ *S3Client) error {
			_, _, err := p.GetEncryptedSize(ctx, "object.dat")
			return err
		}},
		{"Content-Length", 0, func(t *testing.T, p *Provider, _ *S3Client) error {
			return p.DownloadStreaming(ctx, "object.dat", filepath.Join(t.TempDir(), "out.dat"), make([]byte, 32), nil)
		}},
		{"Content-Length", 0, func(t *testing.T, p *Provider, _ *S3Client) error {
			return p.DownloadEncryptedFile(ctx, transfer.LegacyDownloadParams{
				RemotePath: "object.dat", EncryptedPath: filepath.Join(t.TempDir(), "out.enc")})
		}},
		{"UploadId", 0, func(t *testing.T, p *Provider, _ *S3Client) error {
			_, err := p.InitStreamingUpload(ctx, transfer.StreamingUploadInitParams{
				LocalPath: filepath.Join(t.TempDir(), "source.dat"), FileSize: 64,
				Plan: &resources.UploadPlan{PartSize: 64}})
			return err
		}},
		{"UploadId", 0, func(t *testing.T, p *Provider, c *S3Client) error {
			return uploadPreEncrypted(t, p, c, false)
		}},
		{"UploadId", 0, func(t *testing.T, p *Provider, c *S3Client) error {
			return uploadPreEncrypted(t, p, c, true)
		}},
		{"ETag", 1, func(_ *testing.T, p *Provider, c *S3Client) error {
			_, err := p.UploadCiphertext(ctx, &transfer.StreamingUpload{
				UploadID: "stripped-upload", StoragePath: "object.dat",
				ProviderData: &s3ProviderData{bucket: testBucket, s3Client: c}}, 0, make([]byte, 16))
			return err
		}},
		{"ETag", 2, func(t *testing.T, p *Provider, c *S3Client) error {
			return uploadPreEncrypted(t, p, c, true)
		}},
		{"ETag", 1, func(t *testing.T, p *Provider, c *S3Client) error {
			return uploadPreEncrypted(t, p, c, false)
		}},
	}
	for i, tc := range tests {
		t.Run(fmt.Sprintf("%d-%s", i, tc.strip), func(t *testing.T) {
			backend := &strippedS3Backend{strip: tc.strip}
			server := httptest.NewTLSServer(backend)
			t.Cleanup(server.Close)
			client := newTestS3Client(t, server)
			provider := &Provider{storageInfo: client.storageInfo, apiClient: client.apiClient, s3Client: client}

			err := tc.call(t, provider, client)
			if err == nil || !strings.Contains(err.Error(), tc.strip) {
				t.Errorf("got %v, want an error naming the missing %s", err, tc.strip)
			}
			if parts, completions := backend.parts.Load(), backend.completions.Load(); parts > tc.maxParts || completions > 0 {
				t.Errorf("it went on to send %d part(s) and %d completion(s), want at most %d part(s) and none", parts, completions, tc.maxParts)
			}
		})
	}
}

// uploadPreEncrypted runs one of the two pre-encrypt multipart paths over a
// two-part file.
func uploadPreEncrypted(t *testing.T, p *Provider, c *S3Client, concurrent bool) error {
	dir := t.TempDir()
	localPath, encryptedPath := filepath.Join(dir, "source.dat"), filepath.Join(dir, "source.dat.enc")
	testsupport.WriteTestFile(t, localPath, 128)
	testsupport.WriteTestFile(t, encryptedPath, 128)
	params := testUploadParams(t, localPath, encryptedPath, &resources.UploadPlan{PartSize: 64, WorkerCap: 2, QueueDepth: 2})
	objectKey := state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix)
	if !concurrent {
		return p.uploadEncryptedMultipart(context.Background(), c, params, objectKey, 128)
	}
	params.TransferHandle = testsupport.MultiThreadedHandle(t)
	return p.uploadEncryptedMultipartConcurrent(context.Background(), c, params, objectKey, 128)
}
