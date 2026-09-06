// Package testsupport holds the fixtures the S3 and Azure pre-encrypt upload
// tests share. Both providers stage a pre-encrypted file in chunks against a
// fake endpoint, so they need the same redirecting HTTP client, the same
// position-dependent payload, the same multi-threaded transfer handle and the
// same on-disk resume state — stated here once instead of once per provider.
package testsupport

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	nethttp "net/http"
	"os"
	"testing"

	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/resources"
	internaltransfer "github.com/rescale/rescale-int/internal/transfer"
)

// RedirectingHTTPClient sends every request to addr whatever hostname the SDK
// resolved. The provider refreshes credentials before every attempt, and that
// rebuilds the SDK client against the real endpoint template, so overriding
// only the first client's endpoint would point the second call at the real
// storage service.
func RedirectingHTTPClient(addr string) *nethttp.Client {
	return &nethttp.Client{
		Transport: &nethttp.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

// WriteTestFile writes size bytes of position-dependent data, so a chunk that
// lands at the wrong offset does not hash the same as the right one.
func WriteTestFile(t *testing.T, path string, size int64) []byte {
	t.Helper()
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*31 + 7)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	return data
}

// MultiThreadedHandle returns a transfer handle with more than one thread. The
// thread count comes from the pool's view of a large file, which is independent
// of how many bytes the test actually pushes through the reader.
func MultiThreadedHandle(t *testing.T) *internaltransfer.Transfer {
	t.Helper()
	resourceMgr := resources.NewManager(resources.Config{
		MaxThreads:   8,
		AutoScale:    true,
		CPUCores:     8,
		MemoryBudget: 8 * 1024 * 1024 * 1024,
	})
	handle := internaltransfer.NewManager(resourceMgr).AllocateTransfer(2*constants.LargeFile1GB, 1)
	if handle.GetThreads() <= 1 {
		t.Fatalf("expected a multi-threaded handle, got %d thread(s)", handle.GetThreads())
	}
	return handle
}

// WriteResumeState writes resumeState next to localPath, where the provider
// looks for it.
func WriteResumeState(t *testing.T, localPath string, resumeState *state.UploadResumeState) {
	t.Helper()
	data, err := json.Marshal(resumeState)
	if err != nil {
		t.Fatalf("failed to marshal resume state: %v", err)
	}
	if err := os.WriteFile(localPath+".upload.resume", data, 0600); err != nil {
		t.Fatalf("failed to write resume state: %v", err)
	}
}
