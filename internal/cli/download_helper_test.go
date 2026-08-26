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
)

// --- sanitizeErrorString tests ---

func TestSanitizeErrorString(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantAbsent  []string
		wantPresent []string
		wantExact   bool // result must equal input, unchanged
	}{
		{
			name:        "SAS tokens",
			input:       "request failed: https://account.blob.core.windows.net/?sig=abc123secret&se=2026-01-01&sp=r&sv=2021-06-08&sr=b",
			wantAbsent:  []string{"abc123secret"},
			wantPresent: []string{"sig=REDACTED", "se=REDACTED"},
		},
		{
			name:       "Azure account key",
			input:      "DefaultEndpointsProtocol=https;AccountName=myaccount;AccountKey=abc123secret456+base64==;EndpointSuffix=core.windows.net",
			wantAbsent: []string{"abc123secret456"},
			// AccountName is not a secret and must survive redaction.
			wantPresent: []string{"AccountKey=REDACTED", "AccountName=myaccount"},
		},
		{
			name:        "bearer token",
			input:       `Authorization: Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.payload.signature`,
			wantAbsent:  []string{"eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9"},
			wantPresent: []string{"Bearer REDACTED"},
		},
		{
			name:        "token scheme",
			input:       `Token abc123def456`,
			wantAbsent:  []string{"abc123def456"},
			wantPresent: []string{"Token REDACTED"},
		},
		{
			name:        "AWS access key ID",
			input:       "credentials error: AKIAIOSFODNN7EXAMPLE used for request",
			wantAbsent:  []string{"AKIAIOSFODNN7EXAMPLE"},
			wantPresent: []string{"[REDACTED_AWS_KEY]"},
		},
		{
			name:      "no secrets",
			input:     "connection timeout after 30 seconds",
			wantExact: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeErrorString(tt.input)
			if tt.wantExact && got != tt.input {
				t.Errorf("sanitizeErrorString() should pass through unchanged, got %q", got)
			}
			for _, secret := range tt.wantAbsent {
				if strings.Contains(got, secret) {
					t.Errorf("sanitizeErrorString() should redact %q, got %q", secret, got)
				}
			}
			for _, want := range tt.wantPresent {
				if !strings.Contains(got, want) {
					t.Errorf("sanitizeErrorString() should contain %q, got %q", want, got)
				}
			}
		})
	}
}

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
	tests := []struct {
		name     string
		fileName string
		keep     bool
	}{
		{"plain name", "results.dat", true},
		{"dots inside the name", "data..v2.csv", true},
		{"unix traversal", "../../etc/passwd", false},
		{"windows separator", `..\..\Windows\System32\evil.dll`, false},
		{"bare parent directory", "..", false},
		{"empty name", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kept, errs := filterValidJobFiles([]models.JobFile{{ID: "file123", Name: tt.fileName}})

			if tt.keep {
				if len(kept) != 1 || len(errs) != 0 {
					t.Fatalf("kept %d files with %d errors, want 1 file and no errors", len(kept), len(errs))
				}
				return
			}

			if len(kept) != 0 {
				t.Errorf("kept %d files, want 0", len(kept))
			}
			if len(errs) != 1 {
				t.Fatalf("got %d errors, want 1", len(errs))
			}
			if !strings.Contains(errs[0].Error(), "invalid filename from API for file file123") {
				t.Errorf("error = %q, want the shared invalid-filename wording", errs[0])
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
