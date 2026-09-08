package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/logging"
)

// uploadRecorder stands in for UploadFilesWithIDs and records every batch it is
// handed. A --dry-run that reaches it has already decided to move bytes: the
// real function warms cloud credentials and starts the transfer before anything
// else can intervene.
type uploadRecorder struct {
	mu      sync.Mutex
	batches [][]string
}

// install swaps the recorder in for the upload seam for one test.
func (r *uploadRecorder) install(t *testing.T) {
	t.Helper()
	orig := uploadFilesWithIDsFn
	uploadFilesWithIDsFn = func(_ context.Context, filePatterns []string, _ string, _ int, _ bool,
		_ []string, _ *api.Client, _ *logging.Logger, _ bool) ([]string, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.batches = append(r.batches, append([]string(nil), filePatterns...))
		ids := make([]string, len(filePatterns))
		for i := range ids {
			ids[i] = "uploaded-file-id"
		}
		return ids, nil
	}
	t.Cleanup(func() { uploadFilesWithIDsFn = orig })
}

// uploaded returns the flattened list of paths handed to the upload function.
func (r *uploadRecorder) uploaded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []string
	for _, batch := range r.batches {
		all = append(all, batch...)
	}
	return all
}

// writeUploadFixture creates a file of n bytes and returns its path.
func writeUploadFixture(t *testing.T, name string, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, make([]byte, n), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
// The dry-run preview goes straight to stdout, so that is the only place the
// promise "no files were uploaded" is visible to the user.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	// Bind the shared CLI logger to the real stdout before the swap: it captures
	// os.Stdout once, at construction, and a logger left holding a closed test
	// pipe fails every later write in the package.
	GetLogger()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()

	var out string
	func() {
		// Deferred, so a Fatal, Skip or panic inside fn still restores stdout
		// and drains the pipe instead of leaving the package's stdout redirected.
		defer func() {
			os.Stdout = orig
			_ = w.Close()
			out = <-done
			_ = r.Close()
		}()
		fn()
	}()
	return out
}

// fakeLibraryClient points an API client at the fake library from
// files_delete_test.go, which serves the root-folder and folder-contents
// endpoints the duplicate check calls.
func fakeLibraryClient(t *testing.T) *api.Client {
	t.Helper()
	lib := newFakeLibrary(t)
	server := httptest.NewServer(http.HandlerFunc(lib.handler))
	t.Cleanup(server.Close)
	return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})
}

// assertDryRunPreview checks the preview names the file with its size, states
// the destination, and promises nothing was uploaded.
func assertDryRunPreview(t *testing.T, out, fileName string) {
	t.Helper()
	if !strings.Contains(out, "Would upload: ") || !strings.Contains(out, fileName) {
		t.Errorf("dry-run preview does not name %s; got:\n%s", fileName, out)
	}
	for _, want := range []string{
		"DRY-RUN MODE",
		"Destination: root (My Library)",
		"MB)",
		"Dry-Run Summary",
		"No files were uploaded",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run preview missing %q; got:\n%s", want, out)
		}
	}
}

// TestFileUploadDryRunNoCheckDoesNotUpload is the reported defect: with
// duplicate checking off — either --no-check-duplicates or the non-interactive
// default chosen in files.go — the fast path ran the upload before dryRun was
// ever consulted, so a scripted 'files upload --dry-run' really uploaded.
// The API client is nil on purpose: a dry run must reach no API at all.
func TestFileUploadDryRunNoCheckDoesNotUpload(t *testing.T) {
	rec := &uploadRecorder{}
	rec.install(t)

	path := writeUploadFixture(t, "payload.dat", 2048)

	var err error
	out := captureStdout(t, func() {
		err = executeFileUploadWithDuplicateCheck(context.Background(), []string{path}, "",
			constants.DefaultMaxConcurrent, UploadDuplicateModeNoCheck, true, false, nil, nil, GetLogger())
	})
	if err != nil {
		t.Fatalf("dry run returned error: %v", err)
	}

	if got := rec.uploaded(); len(got) != 0 {
		t.Errorf("--dry-run uploaded %d file(s): %v", len(got), got)
	}
	assertDryRunPreview(t, out, "payload.dat")
}

// TestFileUploadDryRunCheckedModeDoesNotUpload pins the behaviour of the checked
// modes, which already honoured --dry-run, so the fix keeps them working and
// both paths print the same preview.
func TestFileUploadDryRunCheckedModeDoesNotUpload(t *testing.T) {
	rec := &uploadRecorder{}
	rec.install(t)

	path := writeUploadFixture(t, "checked.dat", 4096)

	var err error
	out := captureStdout(t, func() {
		err = executeFileUploadWithDuplicateCheck(context.Background(), []string{path}, "",
			constants.DefaultMaxConcurrent, UploadDuplicateModeSkipAll, true, false, nil,
			fakeLibraryClient(t), GetLogger())
	})
	if err != nil {
		t.Fatalf("dry run returned error: %v", err)
	}

	if got := rec.uploaded(); len(got) != 0 {
		t.Errorf("--dry-run uploaded %d file(s): %v", len(got), got)
	}
	assertDryRunPreview(t, out, "checked.dat")
}

// TestFileUploadNoCheckWithoutDryRunUploads guards the other direction: without
// --dry-run the fast path must still upload every file it was given.
func TestFileUploadNoCheckWithoutDryRunUploads(t *testing.T) {
	rec := &uploadRecorder{}
	rec.install(t)

	path := writeUploadFixture(t, "real.dat", 128)

	err := executeFileUploadWithDuplicateCheck(context.Background(), []string{path}, "",
		constants.DefaultMaxConcurrent, UploadDuplicateModeNoCheck, false, false, nil, nil, GetLogger())
	if err != nil {
		t.Fatalf("upload returned error: %v", err)
	}

	got := rec.uploaded()
	if len(got) != 1 || got[0] != path {
		t.Errorf("upload without --dry-run handed %v to the uploader, want [%s]", got, path)
	}
}

// TestFilesUploadCommandDryRunNonInteractive drives the real cobra command with
// no duplicate flag. Under 'go test' stdin is not a terminal, which is exactly
// the scripted case files.go answers with UploadDuplicateModeNoCheck — the mode
// that used to upload despite --dry-run.
func TestFilesUploadCommandDryRunNonInteractive(t *testing.T) {
	if IsTerminal() {
		t.Skip("test needs a non-interactive stdin to reach the no-check default")
	}

	rec := &uploadRecorder{}
	rec.install(t)
	// No client may be built for this run: constructing one can already reach
	// the network, and a scripted preview must not.
	previous := getAPIClientFn
	getAPIClientFn = func() (*api.Client, error) {
		t.Error("files upload --dry-run built an API client on the no-check path")
		return nil, errors.New("no client on a dry run")
	}
	t.Cleanup(func() { getAPIClientFn = previous })

	path := writeUploadFixture(t, "scripted.dat", 512)

	cmd := newFilesUploadCmd()
	cmd.SetArgs([]string{path, "--dry-run"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true

	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	if err != nil {
		t.Fatalf("files upload --dry-run returned error: %v", err)
	}

	if got := rec.uploaded(); len(got) != 0 {
		t.Errorf("files upload --dry-run uploaded %d file(s): %v", len(got), got)
	}
	assertDryRunPreview(t, out, "scripted.dat")
	if !strings.Contains(out, "NO-CHECK") {
		t.Errorf("the scripted default is no longer the no-check mode; preview:\n%s", out)
	}
}
