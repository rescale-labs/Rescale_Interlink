package wailsapp

import (
	"context"
	"errors"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"

	"github.com/rescale/rescale-int/internal/logging"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func testLogger(t *testing.T) {
	t.Helper()
	if wailsLogger == nil {
		wailsLogger = logging.NewLogger("wails-test", nil)
	}
}

// stubDialogs replaces the package-level runtime indirection with
// test-controlled functions. Returns a restore closure for defer.
func stubDialogs(
	dir func(context.Context, runtime.OpenDialogOptions) (string, error),
	file func(context.Context, runtime.OpenDialogOptions) (string, error),
	multi func(context.Context, runtime.OpenDialogOptions) ([]string, error),
	save func(context.Context, runtime.SaveDialogOptions) (string, error),
) func() {
	origDir := openDirectoryDialog
	origFile := openFileDialog
	origMulti := openMultipleFilesDialog
	origSave := saveFileDialog
	if dir != nil {
		openDirectoryDialog = dir
	}
	if file != nil {
		openFileDialog = file
	}
	if multi != nil {
		openMultipleFilesDialog = multi
	}
	if save != nil {
		saveFileDialog = save
	}
	return func() {
		openDirectoryDialog = origDir
		openFileDialog = origFile
		openMultipleFilesDialog = origMulti
		saveFileDialog = origSave
	}
}

// TestGetAppInfo_OSAndSessionScopedDaemon verifies spec §2.2/§2.3: on
// macOS and Linux the daemon is session-scoped, and the DTO exposes
// goruntime.GOOS directly so the frontend can render platform-specific
// messaging without duplicating the check in React.
func TestGetAppInfo_OSAndSessionScopedDaemon(t *testing.T) {
	a := &App{}
	info := a.GetAppInfo()

	if info.OS != goruntime.GOOS {
		t.Errorf("OS = %q, want %q", info.OS, goruntime.GOOS)
	}

	wantSessionScoped := goruntime.GOOS == "darwin" || goruntime.GOOS == "linux"
	if info.SessionScopedDaemon != wantSessionScoped {
		t.Errorf("SessionScopedDaemon = %v, want %v (GOOS=%s)",
			info.SessionScopedDaemon, wantSessionScoped, goruntime.GOOS)
	}
}

// TestDialogBinding_NilCtxReturnsError guards against the binding being
// invoked before OnStartup has captured a context.
func TestDialogBinding_NilCtxReturnsError(t *testing.T) {
	testLogger(t)
	a := &App{} // ctx intentionally nil

	if _, err := a.SelectDirectory("x"); err == nil {
		t.Error("SelectDirectory: expected error with nil ctx, got nil")
	}
	if _, err := a.SelectFile("x"); err == nil {
		t.Error("SelectFile: expected error with nil ctx, got nil")
	}
	if _, err := a.SelectMultipleFiles("x"); err == nil {
		t.Error("SelectMultipleFiles: expected error with nil ctx, got nil")
	}
	if _, err := a.SaveFile("x"); err == nil {
		t.Error("SaveFile: expected error with nil ctx, got nil")
	}
}

// TestDialogBinding_PanicRecovered proves the named-return + assign-in-recover
// contract: if the underlying dialog call panics, the wrapper returns a
// non-nil err rather than a zero-value false success like (nil, nil).
// Regression guard for the class of bug where unnamed returns would silently
// swallow panics as clean "no selection" results.
func TestDialogBinding_PanicRecovered(t *testing.T) {
	testLogger(t)
	restore := stubDialogs(
		func(context.Context, runtime.OpenDialogOptions) (string, error) {
			panic("simulated GTK crash")
		},
		func(context.Context, runtime.OpenDialogOptions) (string, error) {
			panic("simulated GTK crash")
		},
		func(context.Context, runtime.OpenDialogOptions) ([]string, error) {
			panic("simulated GTK crash")
		},
		func(context.Context, runtime.SaveDialogOptions) (string, error) {
			panic("simulated GTK crash")
		},
	)
	defer restore()

	a := &App{ctx: context.Background()}

	if _, err := a.SelectDirectory("x"); err == nil {
		t.Error("SelectDirectory: expected non-nil err on panic, got nil (false success)")
	}
	if _, err := a.SelectFile("x"); err == nil {
		t.Error("SelectFile: expected non-nil err on panic, got nil (false success)")
	}
	if got, err := a.SelectMultipleFiles("x"); err == nil {
		t.Errorf("SelectMultipleFiles: expected non-nil err on panic, got result=%v err=nil (false success)", got)
	}
	if _, err := a.SaveFile("x"); err == nil {
		t.Error("SaveFile: expected non-nil err on panic, got nil (false success)")
	}
}

// TestDialogBinding_MutexSerializesConcurrentCalls verifies that a second
// concurrent dialog attempt returns a clean busy error instead of stacking
// behind the first. Wails's Linux dialog path deadlocks under overlap.
func TestDialogBinding_MutexSerializesConcurrentCalls(t *testing.T) {
	testLogger(t)

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	restore := stubDialogs(
		func(context.Context, runtime.OpenDialogOptions) (string, error) {
			close(firstEntered)
			<-releaseFirst
			return "/tmp/first", nil
		},
		nil, nil, nil,
	)
	defer restore()

	a := &App{ctx: context.Background()}

	var wg sync.WaitGroup
	wg.Add(1)
	var firstResult string
	var firstErr error
	go func() {
		defer wg.Done()
		firstResult, firstErr = a.SelectDirectory("first")
	}()

	<-firstEntered
	// While the first call is blocked inside the stub (lock held), a second
	// attempt must return the busy error immediately.
	secondResult, secondErr := a.SelectDirectory("second")
	if secondErr == nil {
		t.Fatalf("second concurrent call: expected busy error, got result=%q err=nil", secondResult)
	}
	if !strings.Contains(secondErr.Error(), "already open") {
		t.Errorf("second concurrent call: expected 'already open' message, got %q", secondErr.Error())
	}

	// Release the first; it should complete cleanly.
	close(releaseFirst)
	wg.Wait()
	if firstErr != nil {
		t.Errorf("first call: expected success, got err=%v", firstErr)
	}
	if firstResult != "/tmp/first" {
		t.Errorf("first call: expected /tmp/first, got %q", firstResult)
	}
}

// TestDialogBinding_MutexReleasedAfterError confirms the lock is released
// after both success and error paths, so subsequent calls work.
func TestDialogBinding_MutexReleasedAfterError(t *testing.T) {
	testLogger(t)
	restore := stubDialogs(
		func(context.Context, runtime.OpenDialogOptions) (string, error) {
			return "", errors.New("dialog internal failure")
		},
		nil, nil, nil,
	)
	defer restore()

	a := &App{ctx: context.Background()}

	if _, err := a.SelectDirectory("first"); err == nil {
		t.Fatal("first call: expected error from stub, got nil")
	}
	// Second call must not see the busy error — the mutex should be released.
	if _, err := a.SelectDirectory("second"); err == nil {
		t.Error("second call: expected error from stub, got nil")
	} else if strings.Contains(err.Error(), "already open") {
		t.Errorf("second call: mutex not released after first errored; got busy error %q", err.Error())
	}
}
