package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/upload"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/localfs"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/progress"
	"github.com/rescale/rescale-int/internal/resources"
	"github.com/rescale/rescale-int/internal/transfer/folder"
)

// TestCreateFolderStructureStreaming_RootEvent verifies the root FolderReadyEvent
// is emitted first even with an empty directory (no sub-dirs to create).
func TestCreateFolderStructureStreaming_RootEvent(t *testing.T) {
	root := t.TempDir()
	// Empty directory — no sub-dirs, so no API calls needed

	ctx := context.Background()
	dirChan, _, _, _ := localfs.WalkStream(ctx, root, localfs.WalkOptions{IncludeHidden: true})

	folderReadyChan := make(chan FolderReadyEvent, 100)
	conflictMode := ConflictMergeAll

	mapping, created, err := folder.CreateFolderStructureStreaming(
		ctx, nil, NewFolderCache(), root, dirChan, "root-id",
		&conflictMode, 4, nil, folderReadyChan, nil,
		wrapPromptFolderConflict(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	close(folderReadyChan)
	var events []FolderReadyEvent
	for e := range folderReadyChan {
		events = append(events, e)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event (root only), got %d", len(events))
	}
	if events[0].LocalPath != root || events[0].RemoteID != "root-id" {
		t.Errorf("root event: got path=%q id=%q, want path=%q id=%q",
			events[0].LocalPath, events[0].RemoteID, root, "root-id")
	}
	if mapping[root] != "root-id" {
		t.Errorf("mapping[root] = %q, want %q", mapping[root], "root-id")
	}
	if created != 0 {
		t.Errorf("created = %d, want 0 (no sub-dirs)", created)
	}
}

// TestUploadDirOutcome verifies that upload-dir's exit status matches what the
// summary printed: failures and user aborts must not exit 0 (GitHub issue: a
// failing upload-dir printed its failure list and exited 0).
func TestUploadDirOutcome(t *testing.T) {
	tests := []struct {
		name      string
		result    *UploadResult
		wantErr   bool
		wantMatch string
	}{
		{
			name:    "clean run",
			result:  &UploadResult{FilesUploaded: 3},
			wantErr: false,
		},
		{
			name:    "nothing uploaded but nothing failed",
			result:  &UploadResult{FilesIgnored: 2},
			wantErr: false,
		},
		{
			name: "one failure",
			result: &UploadResult{
				FilesUploaded: 1,
				Errors:        []UploadError{{FilePath: "a.txt", Error: errors.New("boom")}},
			},
			wantErr:   true,
			wantMatch: "1 file(s) failed to upload",
		},
		{
			name: "prompt failure recorded as an error",
			result: &UploadResult{
				Errors: []UploadError{{FilePath: "a.txt", Error: io.EOF}},
			},
			wantErr:   true,
			wantMatch: "1 file(s) failed to upload",
		},
		{
			name:      "user abort outranks the error count",
			result:    &UploadResult{Aborted: true, Errors: []UploadError{{FilePath: "a.txt", Error: context.Canceled}}},
			wantErr:   true,
			wantMatch: "aborted by user",
		},
		{
			name:    "nil result",
			result:  nil,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := uploadDirOutcome(tt.result)
			if tt.wantErr != (err != nil) {
				t.Fatalf("uploadDirOutcome() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantMatch != "" && !strings.Contains(err.Error(), tt.wantMatch) {
				t.Errorf("error %q does not contain %q", err, tt.wantMatch)
			}
		})
	}
}

// uploadFilesTestArgs builds the argument set for uploadFiles with one existing
// file, no API client, and conflict checking enabled.
func uploadFilesTestArgs(t *testing.T, fileCount int) (rootPath string, files []string, mapping map[string]string, cfg *config.Config, mgr *resources.Manager) {
	t.Helper()
	rootPath = t.TempDir()
	mapping = map[string]string{rootPath: "folder-1"}
	for i := 0; i < fileCount; i++ {
		p := filepath.Join(rootPath, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		files = append(files, p)
	}
	return rootPath, files, mapping, &config.Config{CheckConflictsBeforeUpload: true}, resources.NewManager(resources.Config{AutoScale: true, MaxThreads: 4})
}

// TestUploadFilesRecordsPromptFailure verifies that a conflict prompt that cannot
// run (no terminal, so the read fails immediately) is recorded as a failure.
// Previously the file was silently dropped: not uploaded, not skipped, exit 0.
func TestUploadFilesRecordsPromptFailure(t *testing.T) {
	if IsTerminal() {
		t.Skip("test needs a non-interactive stdin to make the prompt fail")
	}

	rootPath, files, mapping, cfg, mgr := uploadFilesTestArgs(t, 1)

	orig := checkFileExistsFn
	checkFileExistsFn = func(ctx context.Context, apiClient *api.Client, cache *FolderCache, folderID, fileName string) (string, bool, error) {
		return "existing-file-id", true, nil
	}
	defer func() { checkFileExistsFn = orig }()

	// FileOverwriteOnce is a "once" mode, so the resolver prompts — and the prompt
	// fails because stdin is not a terminal.
	result, err := uploadFiles(context.Background(), rootPath, files, mapping,
		nil, NewFolderCache(), progress.NewUploadUI(len(files)),
		NewFileConflictResolver(FileOverwriteOnce), NewErrorActionResolver(ErrorContinueOnce),
		false, 2, cfg, GetLogger(), mgr)
	if err != nil {
		t.Fatalf("uploadFiles returned error: %v", err)
	}

	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 recorded error, got %d (%+v)", len(result.Errors), result.Errors)
	}
	if result.FilesUploaded != 0 || result.FilesIgnored != 0 {
		t.Errorf("file must not count as uploaded or ignored: uploaded=%d ignored=%d",
			result.FilesUploaded, result.FilesIgnored)
	}
	if err := uploadDirOutcome(result); err == nil {
		t.Error("expected a non-zero exit status for a dropped file")
	}
}

// TestUploadFilesAbortStopsBatch verifies that choosing Abort marks the result and
// stops the batch instead of continuing with the remaining files.
func TestUploadFilesAbortStopsBatch(t *testing.T) {
	rootPath, files, mapping, cfg, mgr := uploadFilesTestArgs(t, 4)

	orig := checkFileExistsFn
	checkFileExistsFn = func(ctx context.Context, apiClient *api.Client, cache *FolderCache, folderID, fileName string) (string, bool, error) {
		return "existing-file-id", true, nil
	}
	defer func() { checkFileExistsFn = orig }()

	// FileAbort is not a "once" mode, so the resolver returns it without prompting.
	result, err := uploadFiles(context.Background(), rootPath, files, mapping,
		nil, NewFolderCache(), progress.NewUploadUI(len(files)),
		NewFileConflictResolver(FileAbort), NewErrorActionResolver(ErrorContinueOnce),
		false, 2, cfg, GetLogger(), mgr)
	if err != nil {
		t.Fatalf("uploadFiles returned error: %v", err)
	}

	if !result.Aborted {
		t.Error("expected result.Aborted to be set")
	}
	if result.FilesUploaded != 0 {
		t.Errorf("no file should have been uploaded after abort, got %d", result.FilesUploaded)
	}
	if err := uploadDirOutcome(result); err == nil || !strings.Contains(err.Error(), "aborted by user") {
		t.Errorf("expected an abort error, got %v", err)
	}
}

// TestUploadFilesAbortSkipsRemainingFiles is the claim behind the abort fix:
// choosing Abort stops the batch. Before, the worker logged "Upload aborted by
// user", returned nil, and the batch carried on uploading everything else.
//
// Single worker so ordering is deterministic: the first file hits the conflict
// prompt and aborts, and the rest must never reach the upload call.
func TestUploadFilesAbortSkipsRemainingFiles(t *testing.T) {
	rootPath, files, mapping, cfg, mgr := uploadFilesTestArgs(t, 6)

	origCheck := checkFileExistsFn
	origUpload := uploadFileFn
	defer func() {
		checkFileExistsFn = origCheck
		uploadFileFn = origUpload
	}()

	// Only the first file conflicts; the others would upload normally.
	checkFileExistsFn = func(ctx context.Context, apiClient *api.Client, cache *FolderCache, folderID, fileName string) (string, bool, error) {
		return "existing-file-id", fileName == filepath.Base(files[0]), nil
	}

	var uploadMu sync.Mutex
	uploaded := []string{}
	uploadFileFn = func(ctx context.Context, params upload.UploadParams) (*models.CloudFile, error) {
		uploadMu.Lock()
		uploaded = append(uploaded, params.LocalPath)
		uploadMu.Unlock()
		return &models.CloudFile{ID: "new-file-id"}, nil
	}

	// FileAbort is not a "once" mode, so the resolver returns it without prompting.
	result, err := uploadFiles(context.Background(), rootPath, files, mapping,
		nil, NewFolderCache(), progress.NewUploadUI(len(files)),
		NewFileConflictResolver(FileAbort), NewErrorActionResolver(ErrorContinueOnce),
		false, 1, cfg, GetLogger(), mgr)
	if err != nil {
		t.Fatalf("uploadFiles returned error: %v", err)
	}

	if !result.Aborted {
		t.Error("expected result.Aborted to be set")
	}
	uploadMu.Lock()
	got := append([]string(nil), uploaded...)
	uploadMu.Unlock()
	if len(got) != 0 {
		t.Errorf("abort must stop the batch, but %d file(s) were still uploaded: %v", len(got), got)
	}
	if result.FilesUploaded != 0 {
		t.Errorf("FilesUploaded = %d, want 0", result.FilesUploaded)
	}
	if err := uploadDirOutcome(result); err == nil || !strings.Contains(err.Error(), "aborted by user") {
		t.Errorf("expected an abort error, got %v", err)
	}
}

// TestReportableErrorsDropsCancellations verifies the abort summary does not list
// the cancellations the abort itself caused.
func TestReportableErrorsDropsCancellations(t *testing.T) {
	realFailure := UploadError{FilePath: "a.dat", Error: errors.New("500 internal server error")}
	cancelled := UploadError{FilePath: "b.dat", Error: fmt.Errorf("S3Storage upload failed: %w", context.Canceled)}
	cancelledFlat := UploadError{FilePath: "c.dat", Error: errors.New("upload failed: context canceled")}

	aborted := &UploadResult{Aborted: true, Errors: []UploadError{realFailure, cancelled, cancelledFlat}}
	got := aborted.reportableErrors()
	if len(got) != 1 || got[0].FilePath != "a.dat" {
		t.Errorf("expected only the real failure, got %+v", got)
	}

	// Without an abort, nothing is filtered: a cancellation then means Ctrl-C or a
	// timeout, which the user should see.
	notAborted := &UploadResult{Errors: []UploadError{realFailure, cancelled}}
	if len(notAborted.reportableErrors()) != 2 {
		t.Errorf("expected both errors kept when not aborted, got %+v", notAborted.reportableErrors())
	}

	var nilResult *UploadResult
	if nilResult.reportableErrors() != nil {
		t.Error("nil result must yield no errors")
	}
}
