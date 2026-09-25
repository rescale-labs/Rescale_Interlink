package s3

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/rescale/rescale-int/internal/cloud/transfer"
)

// expiringOnceBackend refuses the first ListParts and the first
// AbortMultipartUpload with an expired token, then answers both. That is the
// transient failure the retry wrapper exists for: it refreshes the credential
// and tries again, where a bare SDK call fails the upload outright.
type expiringOnceBackend struct {
	mu    sync.Mutex
	calls map[string]int
}

func (b *expiringOnceBackend) ServeHTTP(w nethttp.ResponseWriter, r *nethttp.Request) {
	if !r.URL.Query().Has("uploadId") {
		w.WriteHeader(nethttp.StatusNotImplemented)
		return
	}
	b.mu.Lock()
	b.calls[r.Method]++
	first := b.calls[r.Method] == 1
	b.mu.Unlock()
	if first {
		writeXML(w, nethttp.StatusForbidden, `<Error><Code>ExpiredToken</Code><Message>The provided token has expired</Message></Error>`)
		return
	}
	if r.Method == nethttp.MethodDelete {
		w.WriteHeader(nethttp.StatusNoContent)
		return
	}
	writeXML(w, nethttp.StatusOK, `<ListPartsResult><Bucket>test-bucket</Bucket><Key>k</Key><UploadId>u</UploadId></ListPartsResult>`)
}

func TestResumeProbesAndAbortsRetryATransientFailure(t *testing.T) {
	ctx := context.Background()
	tests := map[string]func(t *testing.T, p *Provider, c *S3Client) error{
		"ValidateStreamingUploadExists": func(t *testing.T, p *Provider, _ *S3Client) error {
			live, err := p.ValidateStreamingUploadExists(ctx, "u", "k")
			if err == nil && !live {
				t.Error("the upload was reported gone")
			}
			return err
		},
		"multipartUploadExists": func(t *testing.T, _ *Provider, c *S3Client) error {
			live, err := multipartUploadExists(ctx, c, "k", "u")
			if err == nil && !live {
				t.Error("the upload was reported gone")
			}
			return err
		},
		"AbortStreamingUpload": func(_ *testing.T, p *Provider, c *S3Client) error {
			return p.AbortStreamingUpload(ctx, &transfer.StreamingUpload{UploadID: "u", StoragePath: "k",
				ProviderData: &s3ProviderData{bucket: testBucket, s3Client: c}})
		},
		"AbortUploadByID": func(_ *testing.T, p *Provider, _ *S3Client) error {
			return p.AbortUploadByID(ctx, "u", "k")
		},
		"abortS3Upload": func(_ *testing.T, _ *Provider, c *S3Client) error {
			abortS3Upload(ctx, c, "k", "u")
			return nil
		},
	}
	for name, call := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel() // each spends one backoff delay on its retry
			backend := &expiringOnceBackend{calls: map[string]int{}}
			server := httptest.NewTLSServer(backend)
			t.Cleanup(server.Close)
			client := newTestS3Client(t, server)
			provider := &Provider{storageInfo: client.storageInfo, apiClient: client.apiClient, s3Client: client}

			if err := call(t, provider, client); err != nil {
				t.Fatalf("one expired token failed the call: %v", err)
			}
			backend.mu.Lock()
			defer backend.mu.Unlock()
			if total := backend.calls[nethttp.MethodGet] + backend.calls[nethttp.MethodDelete]; total != 2 {
				t.Errorf("made %d requests, want the refused one and its retry", total)
			}
		})
	}
}
