package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

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

// useOneFileJobAndFolder serves results.dat, 4 bytes, as JOB1's one output
// file and as FOLDER1's one file.
func useOneFileJobAndFolder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"type": "file", "item": map[string]any{"id": "F1", "name": "results.dat", "decryptedSize": 4}}}})
	}))
	t.Cleanup(server.Close)
	origList, origClient := listJobFilesFn, getAPIClientFn
	t.Cleanup(func() { listJobFilesFn, getAPIClientFn = origList, origClient })
	getAPIClientFn = func() (*api.Client, error) {
		return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), nil
	}
	listJobFilesFn = func(context.Context, *api.Client, string) ([]models.JobFile, error) {
		return []models.JobFile{{ID: "F1", Name: "results.dat", DecryptedSize: 4}}, nil
	}
}

// saveChunkedState writes the resume record the chunked download keeps beside
// <file>.encrypted, claiming the first two bytes of a 20-byte ciphertext.
func saveChunkedState(t *testing.T, encrypted string, created time.Time) {
	t.Helper()
	if err := state.SaveDownloadState(&state.DownloadResumeState{
		LocalPath: encrypted, EncryptedPath: encrypted, TotalSize: 20, DownloadedBytes: 2,
		ChunkSize: 2, CompletedChunks: []int64{0}, CreatedAt: created,
	}, encrypted); err != nil {
		t.Fatal(err)
	}
}

// A download that fails partway and leaves a resume record says the rest can
// be fetched later. The record is kept beside <file>.encrypted, and both
// commands looked for it beside <file>, so neither ever said so.
func TestFailedDownloadsSayTheyCanBeResumed(t *testing.T) {
	useOneFileJobAndFolder(t)
	interrupted := func(_ context.Context, p download.DownloadParams) error {
		if err := os.WriteFile(p.LocalPath+".encrypted", []byte("ab"), 0o600); err != nil {
			t.Fatal(err)
		}
		saveChunkedState(t, p.LocalPath+".encrypted", time.Now())
		return errors.New("connection reset")
	}
	for _, tc := range []struct {
		cmd  func() *cobra.Command
		args []string
		want string
	}{
		{newJobsDownloadCmd, []string{"--job-id", "JOB1", "--outdir", t.TempDir()},
			"💡 Resume state saved for results.dat. To resume this download, run the same command again.\n"},
		{newFoldersCmd, []string{"download-dir", "FOLDER1", "--outdir", t.TempDir(), "--merge"},
			"💡 Resume state saved for results.dat. To resume, run the download again with --merge.\n"},
	} {
		var err error
		said := captureStderr(t, func() { _, err = runWithCancel(t, tc.cmd(), interrupted, tc.args...) })
		if err == nil || !strings.Contains(said, tc.want) {
			t.Errorf("%v returned %v after printing\n%s\nwant it to fail and say %q", tc.args, err, said, tc.want)
		}
	}
}

// --resume over a file already there, with an interrupted legacy download
// beside it, says how far that download got, or, when its record is too old to
// use, that it starts again, and then clears the record and the partial copy.
func TestJobsDownloadResumeSaysWhereItResumesFrom(t *testing.T) {
	useOneFileJobAndFolder(t)
	for _, tc := range []struct {
		created time.Time
		said    string
		kept    bool // the partial copy and its record are still there
	}{
		{time.Now(), "↻ Resuming download for results.dat from 10.0% (2/20 bytes)...\n", true},
		{time.Now().Add(-state.MaxResumeAge - time.Hour),
			"Resume state invalid for results.dat (reason: resume state expired). Starting fresh download...\n", false},
	} {
		out := t.TempDir()
		dest := filepath.Join(out, "results.dat")
		for path, content := range map[string]string{dest: "old", dest + ".encrypted": "ab"} {
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		saveChunkedState(t, dest+".encrypted", tc.created)
		var kept bool
		said := captureStderr(t, func() {
			if _, err := runWithCancel(t, newJobsDownloadCmd(), func(_ context.Context, p download.DownloadParams) error {
				kept = state.DownloadResumeStateExists(p.LocalPath + ".encrypted")
				if _, err := os.Stat(p.LocalPath + ".encrypted"); (err == nil) != kept {
					t.Errorf("the partial copy and its record parted: copy there %v, record there %v", err == nil, kept)
				}
				return os.WriteFile(p.LocalPath, []byte("new!"), 0o644)
			}, "--job-id", "JOB1", "--outdir", out, "--resume"); err != nil {
				t.Fatalf("jobs download --resume: %v", err)
			}
		})
		if !strings.Contains(said, tc.said) || kept != tc.kept {
			t.Errorf("printed\n%s\nwith the partial copy kept %v; want %q and kept %v", said, kept, tc.said, tc.kept)
		}
	}
}

// With --timing, a download's [TIMING] lines go through the progress display,
// as an upload's do, instead of straight to stderr inside the bars; only the
// display's writer redacts what they quote. Without it no writer is handed
// over: the transfer layer prints its format and thread choices to one, three
// lines a file.
func TestDownloadsTimeThroughTheProgressDisplay(t *testing.T) {
	useOneFileJobAndFolder(t)
	for _, timing := range []string{"", "1"} {
		t.Setenv("RESCALE_TIMING", timing)
		for _, tc := range []struct {
			cmd  func() *cobra.Command
			args []string
		}{
			{newJobsDownloadCmd, []string{"--job-id", "JOB1", "--outdir", t.TempDir()}},
			{newFoldersCmd, []string{"download-dir", "FOLDER1", "--outdir", t.TempDir(), "--merge"}},
		} {
			handed := false
			said := captureStderr(t, func() {
				if _, err := runWithCancel(t, tc.cmd(), func(_ context.Context, p download.DownloadParams) error {
					if handed = p.OutputWriter != nil; handed {
						fmt.Fprintln(p.OutputWriter, "[TIMING] FAKE line https://example.invalid/?sig=FAKESIG")
					}
					return os.WriteFile(p.LocalPath, []byte("new!"), 0o644)
				}, tc.args...); err != nil {
					t.Fatalf("%v: %v", tc.args, err)
				}
			})
			if handed != (timing == "1") || handed && !strings.Contains(said, "[TIMING] FAKE line https://example.invalid/?sig=REDACTED\n") {
				t.Errorf("RESCALE_TIMING=%q, %v: writer handed over %v, printed\n%s", timing, tc.args, handed, said)
			}
		}
	}
}
