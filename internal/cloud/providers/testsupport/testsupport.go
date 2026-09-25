// Package testsupport holds the fixtures the S3 and Azure pre-encrypt upload
// tests share. Both providers stage a pre-encrypted file in chunks against a
// fake endpoint, so they need the same redirecting HTTP client, the same
// position-dependent payload, the same multi-threaded transfer handle and the
// same on-disk resume state — stated here once instead of once per provider.
package testsupport

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/tls"
	"encoding/json"
	"net"
	nethttp "net/http"
	"os"
	"testing"

	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto" // package name is 'encryption'
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

// HKDFObject encrypts plaintext in the legacy per-part HKDF format (format
// version 1), partSize bytes to a part, under a fresh master key and file ID.
// Nothing uploads that format any more, but downloads still have to read it.
func HKDFObject(t *testing.T, plaintext []byte, partSize int64) (ciphertext, masterKey, fileID []byte) {
	t.Helper()
	masterKey, _ = encryption.GenerateKey()
	fileID, _ = encryption.GenerateKey() // same size as a key
	for index := int64(0); index*partSize < int64(len(plaintext)); index++ {
		part := plaintext[index*partSize : min((index+1)*partSize, int64(len(plaintext)))]
		key, iv, err := encryption.DerivePartKeyIV(masterKey, fileID, index)
		if err != nil {
			t.Fatalf("DerivePartKeyIV(%d): %v", index, err)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			t.Fatalf("aes.NewCipher: %v", err)
		}
		pad := aes.BlockSize - len(part)%aes.BlockSize
		padded := append(bytes.Clone(part), bytes.Repeat([]byte{byte(pad)}, pad)...)
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(padded, padded)
		ciphertext = append(ciphertext, padded...)
	}
	return ciphertext, masterKey, fileID
}
