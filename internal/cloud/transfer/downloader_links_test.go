package transfer

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/crypto" // package name is 'encryption'
	"github.com/rescale/rescale-int/internal/models"
)

// writingStreamingDownloader stands in for a provider's sequential v1 download,
// which creates LocalPath itself.
type writingStreamingDownloader struct {
	mockStreamingDownloader
}

func (m *writingStreamingDownloader) DownloadStreaming(ctx context.Context, remotePath, localPath string, masterKey []byte, progressCallback cloud.ProgressCallback) error {
	return os.WriteFile(localPath, []byte("downloaded"), 0644)
}

// Every format refuses a symbolic link or a FIFO at a download path and leaves
// it, and a link's target, as they were.
func TestDownloadRefusesALinkAtAnyPathItWrites(t *testing.T) {
	plaintext := bytes.Repeat([]byte("interlink"), 64)
	enc, err := encryption.NewCBCStreamingEncryptor()
	if err != nil {
		t.Fatalf("NewCBCStreamingEncryptor: %v", err)
	}
	cbc, err := enc.EncryptPart(plaintext, true)
	if err != nil {
		t.Fatalf("EncryptPart: %v", err)
	}
	cbcInfo := &models.CloudFile{
		EncodedEncryptionKey: base64.StdEncoding.EncodeToString(enc.GetKey()),
		IV:                   base64.StdEncoding.EncodeToString(enc.GetInitialIV()),
		DecryptedSize:        int64(len(plaintext)),
	}
	download := func(provider cloud.CloudTransfer, localPath string) error {
		_, err := NewDownloader(provider).Download(context.Background(), cloud.DownloadParams{
			RemotePath: "user/abc/results.dat",
			LocalPath:  localPath,
			FileInfo:   cbcInfo,
		})
		return err
	}

	const partSize = int64(64)
	hkdf, masterKey, fileID := hkdfObject(t, plaintext, partSize)
	concurrentV1 := func(localPath string) error {
		mock := &mockHKDFPartDownloader{ciphertext: hkdf, failFrom: -1}
		prep := &DownloadPrep{
			Params: cloud.DownloadParams{
				RemotePath: "user/abc/results.dat",
				LocalPath:  localPath,
				FileInfo:   &models.CloudFile{DecryptedSize: int64(len(plaintext))},
			},
			FormatVersion: 1,
			PartSize:      partSize,
			EncryptionKey: masterKey,
		}
		return NewDownloader(mock).downloadStreamingConcurrent(context.Background(), prep, 4, mock, fileID)
	}

	cbcV2 := func(p string) error {
		mock := &mockCBCPartDownloader{ciphertext: cbc}
		mock.formatVersion, mock.partSize = 2, int64(len(cbc))
		return download(mock, p)
	}
	tests := []struct {
		name   string
		suffix string // appended to the destination to name the path that is a link
		fifo   bool   // a FIFO there instead
		run    func(localPath string) error
	}{
		{"v2 destination", "", false, cbcV2},
		{"v2 destination, a FIFO", "", true, cbcV2},
		{"v1 sequential destination", "", false, func(p string) error {
			mock := &writingStreamingDownloader{}
			mock.formatVersion, mock.partSize = 1, partSize
			mock.fileID = base64.StdEncoding.EncodeToString(fileID)
			return download(mock, p)
		}},
		{"v1 concurrent destination", "", false, concurrentV1},
		{"v1 concurrent .partial", ".partial", false, concurrentV1},
		{"v0 destination", "", false, func(p string) error {
			mock := &mockLegacyDownloader{ciphertext: cbc}
			return download(mock, p)
		}},
		{"v0 .encrypted", ".encrypted", false, func(p string) error {
			mock := &mockLegacyDownloader{ciphertext: cbc}
			return download(mock, p)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			victim := filepath.Join(t.TempDir(), "victim.txt")
			if err := os.WriteFile(victim, []byte("keep me"), 0644); err != nil {
				t.Fatal(err)
			}
			localPath := filepath.Join(t.TempDir(), "results.dat")
			at, want := localPath+tt.suffix, "symbolic link"
			if tt.fifo {
				if err := exec.Command("mkfifo", at).Run(); err != nil {
					t.Skipf("mkfifo: %v", err)
				}
				want = "not a regular file"
			} else if err := os.Symlink(victim, at); err != nil {
				t.Skipf("cannot create a symbolic link here: %v", err)
			}

			done := make(chan error, 1)
			go func() { done <- tt.run(localPath) }()
			var err error
			select {
			case err = <-done:
			case <-time.After(3 * time.Second):
				t.Fatalf("the download blocked opening %s", filepath.Base(at))
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("download to %s: err = %v, want a refusal saying it is a %s", filepath.Base(at), err, want)
			}
			if got, _ := os.ReadFile(victim); string(got) != "keep me" {
				t.Errorf("the link's target was overwritten with %d bytes", len(got))
			}
			if info, err := os.Lstat(at); err != nil || info.Mode().IsRegular() {
				t.Errorf("what was at %s was not left in place: %v", filepath.Base(at), err)
			}
		})
	}
}

// plantingLegacyDownloader and plantingHKDFPartDownloader put a link at the
// destination while the download runs, as another process could.
type plantingLegacyDownloader struct {
	mockLegacyDownloader
	plant func()
}

func (m *plantingLegacyDownloader) DownloadEncryptedFile(ctx context.Context, params LegacyDownloadParams) error {
	m.plant()
	return m.mockLegacyDownloader.DownloadEncryptedFile(ctx, params)
}

type plantingHKDFPartDownloader struct {
	mockHKDFPartDownloader
	once  sync.Once
	plant func()
}

func (m *plantingHKDFPartDownloader) DownloadEncryptedRange(ctx context.Context, remotePath string, offset, length int64, version string, progressCallback func(int64)) ([]byte, string, error) {
	m.once.Do(m.plant)
	return m.mockHKDFPartDownloader.DownloadEncryptedRange(ctx, remotePath, offset, length, version, progressCallback)
}

// The check before a download starts is a transfer's length ahead of the
// write it protects. A link that appears at the destination in between is
// refused before the plaintext is written (v0) or the finished file is renamed
// over it (concurrent v1), and it and its target are left alone.
func TestDownloadRechecksTheDestinationBeforeWritingIt(t *testing.T) {
	plaintext := bytes.Repeat([]byte("interlink"), 64)
	enc, err := encryption.NewCBCStreamingEncryptor()
	if err != nil {
		t.Fatalf("NewCBCStreamingEncryptor: %v", err)
	}
	cbc, err := enc.EncryptPart(plaintext, true)
	if err != nil {
		t.Fatalf("EncryptPart: %v", err)
	}
	const partSize = int64(64)
	hkdf, masterKey, fileID := hkdfObject(t, plaintext, partSize)

	for name, run := range map[string]func(localPath string, plant func()) error{
		"v0": func(localPath string, plant func()) error {
			_, err := NewDownloader(&plantingLegacyDownloader{mockLegacyDownloader{ciphertext: cbc}, plant}).Download(context.Background(), cloud.DownloadParams{
				RemotePath: "user/abc/results.dat",
				LocalPath:  localPath,
				FileInfo: &models.CloudFile{
					EncodedEncryptionKey: base64.StdEncoding.EncodeToString(enc.GetKey()),
					IV:                   base64.StdEncoding.EncodeToString(enc.GetInitialIV()),
					DecryptedSize:        int64(len(plaintext)),
				},
			})
			return err
		},
		"v1 concurrent": func(localPath string, plant func()) error {
			mock := &plantingHKDFPartDownloader{mockHKDFPartDownloader: mockHKDFPartDownloader{ciphertext: hkdf, failFrom: -1}, plant: plant}
			return NewDownloader(mock).downloadStreamingConcurrent(context.Background(), &DownloadPrep{
				Params: cloud.DownloadParams{
					RemotePath: "user/abc/results.dat",
					LocalPath:  localPath,
					FileInfo:   &models.CloudFile{DecryptedSize: int64(len(plaintext))},
				},
				FormatVersion: 1,
				PartSize:      partSize,
				EncryptionKey: masterKey,
			}, 4, mock, fileID)
		},
	} {
		t.Run(name, func(t *testing.T) {
			victim := filepath.Join(t.TempDir(), "victim.txt")
			if err := os.WriteFile(victim, []byte("keep me"), 0644); err != nil {
				t.Fatal(err)
			}
			localPath := filepath.Join(t.TempDir(), "results.dat")
			var linkErr error
			err := run(localPath, func() { linkErr = os.Symlink(victim, localPath) })
			if linkErr != nil {
				t.Skipf("cannot create a symbolic link here: %v", linkErr)
			}
			if err == nil || !strings.Contains(err.Error(), "symbolic link") {
				t.Errorf("err = %v, want a refusal naming the link", err)
			}
			if got, _ := os.ReadFile(victim); string(got) != "keep me" {
				t.Errorf("the link's target now holds %d bytes", len(got))
			}
			if info, err := os.Lstat(localPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Errorf("the link was not left in place: %v", err)
			}
		})
	}
}

// exitingHKDFPartDownloader kills the process when the second part is asked
// for. With one worker the first part is on disk by then, so this is a process
// killed after writing, whose deferred cleanup never runs.
type exitingHKDFPartDownloader struct {
	mockHKDFPartDownloader
	calls int
}

func (m *exitingHKDFPartDownloader) DownloadEncryptedRange(ctx context.Context, remotePath string, offset, length int64, version string, progressCallback func(int64)) ([]byte, string, error) {
	if m.calls++; m.calls > 1 {
		os.Exit(3)
	}
	return m.mockHKDFPartDownloader.DownloadEncryptedRange(ctx, remotePath, offset, length, version, progressCallback)
}

// A process killed while a concurrent v1 download is writing leaves its
// scratch file behind, full-size. The next attempt uses the same name, so it
// takes that file over and publishes it, instead of leaving it to fill the disk
// beside a new one.
func TestDownloadStreamingConcurrentReclaimsAKilledAttemptsScratchFile(t *testing.T) {
	const partSize = int64(64)
	plaintext := bytes.Repeat([]byte("interlink"), 40)
	ciphertext, masterKey, fileID := hkdfObject(t, plaintext, partSize)
	download := func(dir string, mock StreamingPartDownloader, threads int) error {
		return NewDownloader(mock).downloadStreamingConcurrent(context.Background(), &DownloadPrep{
			Params: cloud.DownloadParams{
				RemotePath: "user/abc/results.dat",
				LocalPath:  filepath.Join(dir, "results.dat"),
				FileInfo:   &models.CloudFile{DecryptedSize: int64(len(plaintext))},
			},
			FormatVersion: 1,
			PartSize:      partSize,
			EncryptionKey: masterKey,
		}, threads, mock, fileID)
	}
	if dir := os.Getenv("INTERLINK_TEST_KILLED_DOWNLOAD_DIR"); dir != "" {
		_ = download(dir, &exitingHKDFPartDownloader{mockHKDFPartDownloader: mockHKDFPartDownloader{ciphertext: ciphertext, failFrom: -1}}, 1)
		return
	}

	dir := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestDownloadStreamingConcurrentReclaimsAKilledAttemptsScratchFile$")
	child.Env = append(os.Environ(), "INTERLINK_TEST_KILLED_DOWNLOAD_DIR="+dir)
	if err := child.Run(); child.ProcessState == nil || child.ProcessState.ExitCode() != 3 {
		t.Fatalf("the first attempt was not killed mid-download: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("the killed attempt left %d files, want its scratch file", len(entries))
	}

	if err := download(dir, &mockHKDFPartDownloader{ciphertext: ciphertext, failFrom: -1}, 4); err != nil {
		t.Fatalf("the next attempt: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "results.dat")); !bytes.Equal(got, plaintext) {
		t.Errorf("the download holds %d bytes, want the %d-byte file", len(got), len(plaintext))
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the folder holds %d files after the next attempt, want only the download: the killed attempt's scratch file was not taken over", len(entries))
	}
}
