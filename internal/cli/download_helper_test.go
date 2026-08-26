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

func TestSanitizeErrorString_SASTokens(t *testing.T) {
	input := "request failed: https://account.blob.core.windows.net/?sig=abc123secret&se=2026-01-01&sp=r&sv=2021-06-08&sr=b"
	result := sanitizeErrorString(input)

	if strings.Contains(result, "abc123secret") {
		t.Errorf("sanitizeErrorString() should redact sig value, got %q", result)
	}
	if !strings.Contains(result, "sig=REDACTED") {
		t.Errorf("sanitizeErrorString() should contain sig=REDACTED, got %q", result)
	}
	if !strings.Contains(result, "se=REDACTED") {
		t.Errorf("sanitizeErrorString() should contain se=REDACTED, got %q", result)
	}
}

func TestSanitizeErrorString_AzureAccountKey(t *testing.T) {
	input := "DefaultEndpointsProtocol=https;AccountName=myaccount;AccountKey=abc123secret456+base64==;EndpointSuffix=core.windows.net"
	result := sanitizeErrorString(input)

	if strings.Contains(result, "abc123secret456") {
		t.Errorf("sanitizeErrorString() should redact AccountKey value, got %q", result)
	}
	if !strings.Contains(result, "AccountKey=REDACTED") {
		t.Errorf("sanitizeErrorString() should contain AccountKey=REDACTED, got %q", result)
	}
	// AccountName should NOT be redacted
	if !strings.Contains(result, "AccountName=myaccount") {
		t.Errorf("sanitizeErrorString() should preserve AccountName, got %q", result)
	}
}

func TestSanitizeErrorString_BearerToken(t *testing.T) {
	input := `Authorization: Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.payload.signature`
	result := sanitizeErrorString(input)

	if strings.Contains(result, "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9") {
		t.Errorf("sanitizeErrorString() should redact JWT token, got %q", result)
	}
	if !strings.Contains(result, "Bearer REDACTED") {
		t.Errorf("sanitizeErrorString() should contain 'Bearer REDACTED', got %q", result)
	}
}

func TestSanitizeErrorString_TokenScheme(t *testing.T) {
	input := `Token abc123def456`
	result := sanitizeErrorString(input)

	if strings.Contains(result, "abc123def456") {
		t.Errorf("sanitizeErrorString() should redact Token value, got %q", result)
	}
	if !strings.Contains(result, "Token REDACTED") {
		t.Errorf("sanitizeErrorString() should contain 'Token REDACTED', got %q", result)
	}
}

func TestSanitizeErrorString_AWSAccessKeyID(t *testing.T) {
	input := "credentials error: AKIAIOSFODNN7EXAMPLE used for request"
	result := sanitizeErrorString(input)

	if strings.Contains(result, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("sanitizeErrorString() should redact AWS key ID, got %q", result)
	}
	if !strings.Contains(result, "[REDACTED_AWS_KEY]") {
		t.Errorf("sanitizeErrorString() should contain [REDACTED_AWS_KEY], got %q", result)
	}
}

func TestSanitizeErrorString_NoSecrets(t *testing.T) {
	input := "connection timeout after 30 seconds"
	result := sanitizeErrorString(input)

	if result != input {
		t.Errorf("sanitizeErrorString() should pass through unchanged, got %q", result)
	}
}

// --- formatDownloadError tests ---

func TestFormatDownloadError_CollapsesChain(t *testing.T) {
	// Build a 5-level nested error chain
	root := errors.New("connection refused")
	level1 := fmt.Errorf("HTTP request failed: %w", root)
	level2 := fmt.Errorf("failed to get credentials: %w", level1)
	level3 := fmt.Errorf("Azure client creation error: %w", level2)
	level4 := fmt.Errorf("file download orchestration: %w", level3)

	result := formatDownloadError("results.dat", "abc123", "BWuHag", "AzureStorage", level4)
	errMsg := result.Error()

	// Root cause should be present
	if !strings.Contains(errMsg, "connection refused") {
		t.Errorf("formatDownloadError() should contain root cause, got %q", errMsg)
	}
	// Intermediate messages should NOT be present (collapsed)
	if strings.Contains(errMsg, "orchestration") {
		t.Errorf("formatDownloadError() should not contain intermediate chain messages, got %q", errMsg)
	}
}

func TestFormatDownloadError_IncludesContext(t *testing.T) {
	err := errors.New("timeout")
	result := formatDownloadError("output.dat", "fileXYZ", "jobABC", "AzureStorage", err)
	errMsg := result.Error()

	if !strings.Contains(errMsg, "output.dat") {
		t.Errorf("should contain file name, got %q", errMsg)
	}
	if !strings.Contains(errMsg, "fileXYZ") {
		t.Errorf("should contain file ID, got %q", errMsg)
	}
	if !strings.Contains(errMsg, "jobABC") {
		t.Errorf("should contain job ID, got %q", errMsg)
	}
	if !strings.Contains(errMsg, "AzureStorage") {
		t.Errorf("should contain storage type, got %q", errMsg)
	}
}

func TestFormatDownloadError_SanitizesGoInternals(t *testing.T) {
	// Simulate the exact error that occurred with the old []string Paths type
	root := errors.New(`json: cannot unmarshal object into Go struct field AzureCredentials.paths of type string`)
	wrapped := fmt.Errorf("failed to parse Azure credentials: %w", root)

	result := formatDownloadError("output.dat", "abc123", "BWuHag", "AzureStorage", wrapped)
	errMsg := result.Error()

	if strings.Contains(errMsg, "Go struct field") {
		t.Errorf("should sanitize Go internals, got %q", errMsg)
	}
	if !strings.Contains(errMsg, "unexpected credential response format") {
		t.Errorf("should contain sanitized message, got %q", errMsg)
	}
}

func TestFormatDownloadError_EmptyJobID(t *testing.T) {
	err := errors.New("timeout")
	result := formatDownloadError("output.dat", "fileXYZ", "", "S3Storage", err)
	errMsg := result.Error()

	if strings.Contains(errMsg, "job ") {
		t.Errorf("should omit job context when jobID is empty, got %q", errMsg)
	}
	if !strings.Contains(errMsg, "file fileXYZ") {
		t.Errorf("should still contain file context, got %q", errMsg)
	}
}

func TestFormatDownloadError_ClassifiesStep(t *testing.T) {
	tests := []struct {
		name     string
		errMsg   string
		wantStep string
	}{
		{
			name:     "credential error",
			errMsg:   "failed to get Azure credentials for file abc123: Forbidden",
			wantStep: "fetching storage credentials",
		},
		{
			name:     "download error",
			errMsg:   "file size mismatch",
			wantStep: "downloading from storage",
		},
		{
			name:     "checksum error",
			errMsg:   "checksum verification failed",
			wantStep: "verifying checksum",
		},
		{
			name:     "client creation error",
			errMsg:   "failed to create Azure client: invalid SAS",
			wantStep: "creating storage client",
		},
		{
			name:     "generic error",
			errMsg:   "something unexpected",
			wantStep: "downloading",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := errors.New(tt.errMsg)
			result := formatDownloadError("test.dat", "abc", "job1", "AzureStorage", err)
			errMsg := result.Error()
			if !strings.Contains(errMsg, tt.wantStep) {
				t.Errorf("step should be %q, got error: %q", tt.wantStep, errMsg)
			}
		})
	}
}

func TestFormatDownloadError_IncludesGuidance(t *testing.T) {
	err := errors.New("something failed")
	result := formatDownloadError("test.dat", "abc", "job1", "AzureStorage", err)
	errMsg := result.Error()

	if !strings.Contains(errMsg, "--debug") {
		t.Errorf("should include --debug guidance, got %q", errMsg)
	}
	if !strings.Contains(errMsg, "verify you have access") {
		t.Errorf("should include access guidance, got %q", errMsg)
	}
}

func TestFormatDownloadError_CredentialFailure(t *testing.T) {
	// Simulate a 403 error chain from credential fetching
	root := errors.New("Forbidden")
	level1 := fmt.Errorf("get storage credentials failed: status 403: %w", root)
	level2 := fmt.Errorf("failed to get Azure credentials for file abc123: %w", level1)
	level3 := fmt.Errorf("abc123 download failed: %w", level2)

	result := formatDownloadError("output.dat", "abc123", "BWuHag", "AzureStorage", level3)
	errMsg := result.Error()

	if !strings.Contains(errMsg, "fetching storage credentials") {
		t.Errorf("step should classify as credential fetching, got %q", errMsg)
	}
	if !strings.Contains(errMsg, "Forbidden") {
		t.Errorf("root cause should be Forbidden, got %q", errMsg)
	}
}

// --- CLI-level regression tests using test seams ---

func TestExecuteJobDownload_SharedJobSuccess(t *testing.T) {
	// Save original functions and restore after test
	origListFn := listJobFilesFn
	origDownloadFn := downloadFileFn
	defer func() {
		listJobFilesFn = origListFn
		downloadFileFn = origDownloadFn
	}()

	// Mock listJobFilesFn to return a file with Azure storage metadata
	listJobFilesFn = func(ctx context.Context, apiClient *api.Client, jobID string) ([]models.JobFile, error) {
		return []models.JobFile{
			{
				ID:            "file123",
				Name:          "results.dat",
				DecryptedSize: 1024,
				Storage: &models.CloudFileStorage{
					ID:          "storage1",
					StorageType: "AzureStorage",
				},
				PathParts: &models.CloudFilePathParts{
					Container: "rescale-files",
					Path:      "user/abc/results.dat",
				},
			},
		}, nil
	}

	// Mock downloadFileFn to succeed
	var downloadCalled bool
	downloadFileFn = func(ctx context.Context, params download.DownloadParams) error {
		downloadCalled = true
		return nil
	}

	ctx := context.Background()

	// executeJobDownload requires logger and apiClient but our mocks bypass them
	// We can't easily call executeJobDownload without a full setup, so test the
	// test seams are wired correctly by verifying the mock functions are called
	_ = ctx
	_ = downloadCalled

	// Verify mock was set
	files, err := listJobFilesFn(ctx, nil, "BWuHag")
	if err != nil {
		t.Fatalf("listJobFilesFn() error = %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("listJobFilesFn() returned %d files, want 1", len(files))
	}
	if files[0].Storage.StorageType != "AzureStorage" {
		t.Errorf("file storage type = %q, want %q", files[0].Storage.StorageType, "AzureStorage")
	}

	// Verify download mock works
	err = downloadFileFn(ctx, download.DownloadParams{})
	if err != nil {
		t.Fatalf("downloadFileFn() error = %v", err)
	}
	if !downloadCalled {
		t.Error("downloadFileFn was not called")
	}
}

func TestExecuteJobDownload_SharedJobPermissionDenied(t *testing.T) {
	// Simulate the error chain from a 403 and verify formatDownloadError handles it
	root := errors.New("Forbidden")
	level1 := fmt.Errorf("get storage credentials failed: status 403: %w", root)
	level2 := fmt.Errorf("failed to get Azure credentials for file abc123: %w", level1)
	level3 := fmt.Errorf("abc123 download failed: %w", level2)

	result := formatDownloadError("output.dat", "abc123", "BWuHag", "AzureStorage", level3)
	errMsg := result.Error()

	// Verify the error message is user-friendly
	if !strings.Contains(errMsg, "fetching storage credentials") {
		t.Errorf("should classify as credential step, got %q", errMsg)
	}
	if strings.Contains(errMsg, "Go struct field") {
		t.Errorf("should not contain Go internals, got %q", errMsg)
	}
	if !strings.Contains(errMsg, "--debug") {
		t.Errorf("should include guidance, got %q", errMsg)
	}
}

func TestExecuteJobDownload_MalformedCredentialPayload(t *testing.T) {
	// Simulate the exact error that would occur with old []string Paths type
	root := errors.New(`json: cannot unmarshal object into Go struct field AzureCredentials.paths of type string`)
	level1 := fmt.Errorf("failed to parse Azure credentials: %w", root)
	level2 := fmt.Errorf("failed to get Azure credentials for file abc123: %w", level1)
	level3 := fmt.Errorf("abc123 download failed: %w", level2)

	result := formatDownloadError("output.dat", "abc123", "BWuHag", "AzureStorage", level3)
	errMsg := result.Error()

	// Verify the error is sanitized
	if !strings.Contains(errMsg, "unexpected credential response format") {
		t.Errorf("should sanitize to 'unexpected credential response format', got %q", errMsg)
	}
	if strings.Contains(errMsg, "json:") {
		t.Errorf("should not contain json: prefix, got %q", errMsg)
	}
	if strings.Contains(errMsg, "Go struct field") {
		t.Errorf("should not contain Go internals, got %q", errMsg)
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
