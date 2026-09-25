package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/resources"
)

// --- formatDownloadError tests ---

func TestFormatDownloadError(t *testing.T) {
	// A deep chain the user must not be shown verbatim.
	connRefused := fmt.Errorf("file download orchestration: %w",
		fmt.Errorf("Azure client creation error: %w",
			fmt.Errorf("failed to get credentials: %w",
				fmt.Errorf("HTTP request failed: %w", errors.New("connection refused")))))

	// The 403 chain a shared job produces when the caller lacks access.
	forbidden := fmt.Errorf("abc123 download failed: %w",
		fmt.Errorf("failed to get Azure credentials for file abc123: %w",
			fmt.Errorf("get storage credentials failed: status 403: %w", errors.New("Forbidden"))))

	// The chain produced by a credential payload we cannot unmarshal.
	badPayload := fmt.Errorf("abc123 download failed: %w",
		fmt.Errorf("failed to get Azure credentials for file abc123: %w",
			fmt.Errorf("failed to parse Azure credentials: %w",
				errors.New(`json: cannot unmarshal object into Go struct field AzureCredentials.paths of type string`))))

	tests := []struct {
		name        string
		fileName    string
		fileID      string
		jobID       string
		noJobID     bool // exercise the empty-job-ID path instead of defaulting
		storageType string
		err         error
		wantPresent []string
		wantAbsent  []string
	}{
		{
			name: "collapses the chain to the root cause",
			err:  connRefused,
			// Intermediate wrapper messages are noise to the user.
			wantPresent: []string{"connection refused"},
			wantAbsent:  []string{"orchestration"},
		},
		{
			name:        "includes file, job and storage context",
			fileName:    "output.dat",
			fileID:      "fileXYZ",
			jobID:       "jobABC",
			err:         errors.New("timeout"),
			wantPresent: []string{"output.dat", "fileXYZ", "jobABC", "AzureStorage"},
		},
		{
			name:        "omits job context when the job ID is empty",
			fileName:    "output.dat",
			fileID:      "fileXYZ",
			noJobID:     true,
			storageType: "S3Storage",
			err:         errors.New("timeout"),
			wantPresent: []string{"file fileXYZ"},
			wantAbsent:  []string{"job "},
		},
		{
			name:        "sanitizes Go internals out of a credential parse failure",
			err:         fmt.Errorf("failed to parse Azure credentials: %w", errors.New(`json: cannot unmarshal object into Go struct field AzureCredentials.paths of type string`)),
			wantPresent: []string{"unexpected credential response format"},
			wantAbsent:  []string{"Go struct field"},
		},
		{
			name:        "sanitizes the full malformed-payload chain",
			err:         badPayload,
			wantPresent: []string{"unexpected credential response format"},
			wantAbsent:  []string{"Go struct field", "json:"},
		},
		{
			name:        "includes actionable guidance",
			err:         errors.New("something failed"),
			wantPresent: []string{"--debug", "verify you have access"},
		},
		{
			name:        "403 classifies as credential fetching and keeps the root cause",
			err:         forbidden,
			wantPresent: []string{"fetching storage credentials", "Forbidden", "--debug"},
			wantAbsent:  []string{"Go struct field"},
		},

		// Step classification: the phrase the user sees for where it broke.
		{name: "step: credentials", err: errors.New("failed to get Azure credentials for file abc123: Forbidden"), wantPresent: []string{"fetching storage credentials"}},
		{name: "step: download", err: errors.New("file size mismatch"), wantPresent: []string{"downloading from storage"}},
		{name: "step: checksum", err: errors.New("checksum verification failed"), wantPresent: []string{"verifying checksum"}},
		{name: "step: client creation", err: errors.New("failed to create Azure client: invalid SAS"), wantPresent: []string{"creating storage client"}},
		{name: "step: generic", err: errors.New("something unexpected"), wantPresent: []string{"downloading"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fileName, fileID, jobID := tt.fileName, tt.fileID, tt.jobID
			if fileName == "" {
				fileName = "results.dat"
			}
			if fileID == "" {
				fileID = "abc123"
			}
			if jobID == "" && !tt.noJobID {
				jobID = "BWuHag"
			}
			storageType := tt.storageType
			if storageType == "" {
				storageType = "AzureStorage"
			}

			errMsg := formatDownloadError(fileName, fileID, jobID, storageType, tt.err).Error()

			for _, want := range tt.wantPresent {
				if !strings.Contains(errMsg, want) {
					t.Errorf("error should contain %q, got %q", want, errMsg)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(errMsg, absent) {
					t.Errorf("error should not contain %q, got %q", absent, errMsg)
				}
			}
		})
	}
}

// --- skip-existing size gate ---

// TestExistingFileIsComplete covers the gate that stops --skip and jobs watch
// from accepting a partial or corrupt leftover as an already-downloaded file.
func TestExistingFileIsComplete(t *testing.T) {
	tests := []struct {
		name         string
		onDisk       int64
		expectedSize int64
		want         bool
	}{
		{"size matches metadata", 1024, 1024, true},
		{"truncated leftover", 512, 1024, false},
		{"oversized leftover", 2048, 1024, false},
		{"empty leftover", 0, 1024, false},
		{"expected size unknown falls back to existence", 512, 0, true},
	}

	dir := t.TempDir()
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, fmt.Sprintf("case%d.dat", i))
			if err := os.WriteFile(path, make([]byte, tt.onDisk), 0o644); err != nil {
				t.Fatalf("seed file: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if got := existingFileIsComplete(info, tt.expectedSize); got != tt.want {
				t.Errorf("existingFileIsComplete(%d bytes on disk, expected %d) = %v, want %v",
					tt.onDisk, tt.expectedSize, got, tt.want)
			}
		})
	}
}

// --- server-supplied filename validation ---

// TestFilterValidJobFiles covers the guard on the Name fallback used to build
// local paths in executeJobDownload.
func TestFilterValidJobFiles(t *testing.T) {
	const name = "invalid filename from API for file file123"
	tests := []struct {
		name, id, fileName string
		refusal            string // how the refusal reads; "" keeps the file
	}{
		{"plain name", "file123", "results.dat", ""},
		{"dots inside the name", "file123", "data..v2.csv", ""},
		{"unix traversal", "file123", "../../etc/passwd", name},
		{"windows separator", "file123", `..\..\Windows\System32\evil.dll`, name},
		{"bare parent directory", "file123", "..", name},
		{"empty name", "file123", "", name},
		{"windows device name", "file123", "NUL.txt", name},
		{"alternate data stream", "file123", "results.dat:hidden", name},
		{"trailing dot", "file123", "results.", name},
		{"an ID that is not one", "x/../../../../tmp/escaped", "results.dat", "invalid file ID from API for results.dat"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kept, errs := filterValidJobFiles([]models.JobFile{{ID: tt.id, Name: tt.fileName}})
			if tt.refusal == "" {
				if len(kept) != 1 || len(errs) != 0 {
					t.Fatalf("kept %d files with %d errors, want 1 file and no errors", len(kept), len(errs))
				}
				return
			}
			if len(kept) != 0 || len(errs) != 1 || !strings.Contains(errs[0].Error(), tt.refusal) {
				t.Errorf("kept %d files with errors %v, want it refused: %s", len(kept), errs, tt.refusal)
			}
		})
	}
}

// TestExecuteJobDownload_SkipExistingSizeGate is the wiring test for the
// skip-existing gate: --skip (and jobs watch, which passes the same flag) must
// keep a file that matches the expected size and replace one that does not,
// because a wrong-size leftover is a partial or corrupt artifact rather than a
// download already in hand.
func TestExecuteJobDownload_SkipExistingSizeGate(t *testing.T) {
	const expectedSize = 1024

	tests := []struct {
		name             string
		existingContents int
		wantDownloads    int32
	}{
		{"wrong-size leftover is replaced", 9, 1},
		{"matching size is skipped", expectedSize, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origList, origDownload := listJobFilesFn, downloadFileFn
			t.Cleanup(func() { listJobFilesFn, downloadFileFn = origList, origDownload })

			listJobFilesFn = func(_ context.Context, _ *api.Client, _ string) ([]models.JobFile, error) {
				return []models.JobFile{{ID: "file123", Name: "results.dat", DecryptedSize: expectedSize}}, nil
			}

			var downloads int32
			downloadFileFn = func(_ context.Context, params download.DownloadParams) error {
				atomic.AddInt32(&downloads, 1)
				return os.WriteFile(params.LocalPath, make([]byte, expectedSize), 0o644)
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})

			outDir := t.TempDir()
			target := filepath.Join(outDir, "results.dat")
			if err := os.WriteFile(target, make([]byte, tt.existingContents), 0o644); err != nil {
				t.Fatalf("seed existing file: %v", err)
			}

			// skipAll=true is what --skip and jobs watch pass.
			err := executeJobDownload(context.Background(), "job123", outDir, 1,
				false, true, false, false, nil, nil, nil, nil, client, GetLogger())
			if err != nil {
				t.Fatalf("executeJobDownload: %v", err)
			}

			if got := atomic.LoadInt32(&downloads); got != tt.wantDownloads {
				t.Errorf("downloadFileFn called %d times, want %d", got, tt.wantDownloads)
			}
			info, statErr := os.Stat(target)
			if statErr != nil {
				t.Fatalf("stat target: %v", statErr)
			}
			if info.Size() != expectedSize && tt.wantDownloads == 1 {
				t.Errorf("target is %d bytes after re-download, want %d", info.Size(), expectedSize)
			}
		})
	}
}

// TestDownloadFolderRecursive_SkipExistingSizeGate is the wiring test for the
// folder download's skip-existing gate: a file whose size matches the scan
// stands, and one whose size does not is replaced, because a wrong-size file is
// a partial or corrupt leftover rather than a download already in hand.
//
// Driven with --merge rather than --skip because --skip stops at the root folder
// conflict — an output folder that already exists ends the whole download before
// any file is looked at — so --merge (and a per-file Skip answer at the prompt)
// is how this branch is reached.
func TestDownloadFolderRecursive_SkipExistingSizeGate(t *testing.T) {
	const expectedSize = 1024

	tests := []struct {
		name            string
		onDisk          int
		wantDownloads   int32
		wantSkipped     int
		wantDownloadedN int
	}{
		{"wrong-size leftover is replaced", 9, 1, 0, 1},
		{"matching size is skipped", expectedSize, 0, 1, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := downloadFileFn
			t.Cleanup(func() { downloadFileFn = orig })

			var downloads int32
			downloadFileFn = func(_ context.Context, params download.DownloadParams) error {
				atomic.AddInt32(&downloads, 1)
				return os.WriteFile(params.LocalPath, make([]byte, expectedSize), 0o644)
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w,
					`{"results":[{"type":"file","item":{"id":"file123","name":"results.dat","decryptedSize":%d}}]}`,
					expectedSize)
			}))
			defer server.Close()
			client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})

			// The download nests under outputDir/folderName, and that directory
			// already existing is what sends --merge into the per-file branch.
			outDir := t.TempDir()
			rootDir := filepath.Join(outDir, "myfolder")
			if err := os.MkdirAll(rootDir, 0o755); err != nil {
				t.Fatalf("create root folder: %v", err)
			}
			target := filepath.Join(rootDir, "results.dat")
			if err := os.WriteFile(target, make([]byte, tt.onDisk), 0o644); err != nil {
				t.Fatalf("seed existing file: %v", err)
			}

			result, err := DownloadFolderRecursive(context.Background(), "folder123", "myfolder", outDir,
				false, false, true, true, 1, true, false, client, GetLogger(),
				resources.NewManager(resources.Config{AutoScale: true, MaxThreads: 4}))
			if err != nil {
				t.Fatalf("DownloadFolderRecursive: %v", err)
			}

			if got := atomic.LoadInt32(&downloads); got != tt.wantDownloads {
				t.Errorf("downloadFileFn called %d times, want %d", got, tt.wantDownloads)
			}
			if result.FilesSkipped != tt.wantSkipped {
				t.Errorf("FilesSkipped = %d, want %d", result.FilesSkipped, tt.wantSkipped)
			}
			if result.FilesDownloaded != tt.wantDownloadedN {
				t.Errorf("FilesDownloaded = %d, want %d", result.FilesDownloaded, tt.wantDownloadedN)
			}
			info, statErr := os.Stat(target)
			if statErr != nil {
				t.Fatalf("stat target: %v", statErr)
			}
			if info.Size() != expectedSize {
				t.Errorf("target is %d bytes after the run, want the complete %d", info.Size(), expectedSize)
			}
		})
	}
}
