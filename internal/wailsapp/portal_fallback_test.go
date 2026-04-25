package wailsapp

import (
	"context"
	"errors"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// enablePortal switches the cross-platform portal indirection vars to
// test-controlled closures and returns a restore. Use instead of
// stubDialogs for tests that WANT the portal path exercised.
func enablePortal(
	dir func(parent, title string) (string, error),
	file func(parent, title string) (string, error),
	multi func(parent, title string) ([]string, error),
	save func(parent, title, defaultName string, filters []runtime.FileFilter) (string, error),
	unavailable func(error) bool,
) func() {
	origEnabled := portalEnabledFunc
	origDir := portalOpenDirectoryFunc
	origFile := portalOpenFileFunc
	origMulti := portalOpenMultipleFilesFunc
	origSave := portalSaveFileFunc
	origUnavailable := isPortalUnavailableFunc

	portalEnabledFunc = func() bool { return true }
	if dir != nil {
		portalOpenDirectoryFunc = dir
	}
	if file != nil {
		portalOpenFileFunc = file
	}
	if multi != nil {
		portalOpenMultipleFilesFunc = multi
	}
	if save != nil {
		portalSaveFileFunc = save
	}
	if unavailable != nil {
		isPortalUnavailableFunc = unavailable
	}

	return func() {
		portalEnabledFunc = origEnabled
		portalOpenDirectoryFunc = origDir
		portalOpenFileFunc = origFile
		portalOpenMultipleFilesFunc = origMulti
		portalSaveFileFunc = origSave
		isPortalUnavailableFunc = origUnavailable
	}
}

// resetDialogPathLog clears the sync.Map that gates one-log-per-binding.
// Tests need this because the logger is cross-test shared state.
func resetDialogPathLog() {
	dialogPathLogged.Range(func(key, _ any) bool {
		dialogPathLogged.Delete(key)
		return true
	})
}

// TestSelectDirectory_portalSuccess — when portal returns a path, GTK
// must not be called.
func TestSelectDirectory_portalSuccess(t *testing.T) {
	testLogger(t)
	resetDialogPathLog()

	gtkCalled := false
	restoreGTK := stubDialogs(
		func(context.Context, runtime.OpenDialogOptions) (string, error) {
			gtkCalled = true
			return "gtk-should-not-be-called", nil
		}, nil, nil, nil,
	)
	defer restoreGTK()

	restorePortal := enablePortal(
		func(parent, title string) (string, error) { return "/tmp/from-portal", nil },
		nil, nil, nil, nil,
	)
	defer restorePortal()

	a := &App{ctx: context.Background()}
	got, err := a.SelectDirectory("pick a dir")
	if err != nil {
		t.Fatalf("SelectDirectory err: %v", err)
	}
	if got != "/tmp/from-portal" {
		t.Errorf("result = %q, want /tmp/from-portal", got)
	}
	if gtkCalled {
		t.Error("GTK dialog was invoked but portal succeeded")
	}
}

// TestSelectDirectory_portalFallbackOnUnavailable — when portal returns
// errPortalUnavailable, the GTK path must be called and its value
// forwarded.
func TestSelectDirectory_portalFallbackOnUnavailable(t *testing.T) {
	testLogger(t)
	resetDialogPathLog()

	gtkCalled := false
	restoreGTK := stubDialogs(
		func(context.Context, runtime.OpenDialogOptions) (string, error) {
			gtkCalled = true
			return "/tmp/from-gtk", nil
		}, nil, nil, nil,
	)
	defer restoreGTK()

	restorePortal := enablePortal(
		func(parent, title string) (string, error) { return "", errPortalUnavailable },
		nil, nil, nil,
		func(err error) bool { return errors.Is(err, errPortalUnavailable) },
	)
	defer restorePortal()

	a := &App{ctx: context.Background()}
	got, err := a.SelectDirectory("pick")
	if err != nil {
		t.Fatalf("SelectDirectory err: %v", err)
	}
	if got != "/tmp/from-gtk" {
		t.Errorf("result = %q, want /tmp/from-gtk", got)
	}
	if !gtkCalled {
		t.Error("portal reported unavailable but GTK fallback was not invoked")
	}
}

// TestSelectDirectory_portalTimeoutNoFallback — a non-unavailable error
// (timeout, response-code-2) must surface to the caller without GTK
// fallback (so users on broken portals see the actionable message).
func TestSelectDirectory_portalTimeoutNoFallback(t *testing.T) {
	testLogger(t)
	resetDialogPathLog()

	gtkCalled := false
	restoreGTK := stubDialogs(
		func(context.Context, runtime.OpenDialogOptions) (string, error) {
			gtkCalled = true
			return "/tmp/should-not-reach", nil
		}, nil, nil, nil,
	)
	defer restoreGTK()

	timeoutErr := errors.New("portal: timeout after 5m0s waiting for Response signal; try setting RESCALE_DISABLE_PORTAL=1")
	restorePortal := enablePortal(
		func(parent, title string) (string, error) { return "", timeoutErr },
		nil, nil, nil,
		func(err error) bool { return false }, // NOT unavailable
	)
	defer restorePortal()

	a := &App{ctx: context.Background()}
	_, err := a.SelectDirectory("pick")
	if err == nil {
		t.Fatal("expected error from portal timeout, got nil")
	}
	if gtkCalled {
		t.Error("GTK fallback was invoked for a non-unavailable portal error")
	}
}

// TestSaveFile_portalForwardsDefaultFilenameAndFilters — DefaultFilename
// and Filters must be passed to portalSaveFile (portal path) and are
// preserved on GTK fallback.
func TestSaveFile_portalForwardsDefaultFilenameAndFilters(t *testing.T) {
	testLogger(t)
	resetDialogPathLog()

	var portalGotName string
	var portalGotFilters []runtime.FileFilter
	restorePortal := enablePortal(
		nil, nil, nil,
		func(parent, title, defaultName string, filters []runtime.FileFilter) (string, error) {
			portalGotName = defaultName
			portalGotFilters = filters
			return "/tmp/from-portal.json", nil
		},
		nil,
	)
	defer restorePortal()

	a := &App{ctx: context.Background()}

	wantName := "rescale-error-report-2026-04-24T010203.json"
	wantFilters := []runtime.FileFilter{
		{DisplayName: "JSON Files (*.json)", Pattern: "*.json"},
	}
	got, err := portalAwareSaveFile(a.ctx, "SaveErrorReport", runtime.SaveDialogOptions{
		DefaultFilename: wantName,
		Title:           "Save Error Report",
		Filters:         wantFilters,
	})
	if err != nil {
		t.Fatalf("portalAwareSaveFile err: %v", err)
	}
	if got != "/tmp/from-portal.json" {
		t.Errorf("result = %q, want /tmp/from-portal.json", got)
	}
	if portalGotName != wantName {
		t.Errorf("portal got defaultName=%q, want %q", portalGotName, wantName)
	}
	if len(portalGotFilters) != 1 || portalGotFilters[0].Pattern != "*.json" {
		t.Errorf("portal got filters %+v, want [*.json]", portalGotFilters)
	}
}

// TestSaveErrorReport_participatesInDialogGuard — concurrent call while
// dialogMu is held returns the busy message, nil ctx returns
// appNotReadyError, and a panic inside the save path is recovered.
func TestSaveErrorReport_participatesInDialogGuard(t *testing.T) {
	testLogger(t)
	resetDialogPathLog()
	a := &App{ctx: context.Background()}

	// (1) busy mutex → dialogBusyMessage
	dialogMu.Lock()
	_, err := a.SaveErrorReport(`{}`)
	dialogMu.Unlock()
	if err == nil || err.Error() != dialogBusyMessage {
		t.Errorf("locked dialogMu err = %v, want %q", err, dialogBusyMessage)
	}

	// (2) nil ctx → appNotReadyError
	noCtx := &App{ctx: nil}
	if _, err := noCtx.SaveErrorReport(`{}`); err == nil || err.Error() != appNotReadyError {
		t.Errorf("nil-ctx err = %v, want %q", err, appNotReadyError)
	}

	// (3) panic inside portal path → recovered as error, not crash
	restorePortal := enablePortal(
		nil, nil, nil,
		func(parent, title, defaultName string, filters []runtime.FileFilter) (string, error) {
			panic("boom")
		},
		nil,
	)
	defer restorePortal()
	_, err = a.SaveErrorReport(`{}`)
	if err == nil {
		t.Error("expected recovered panic error, got nil")
	}
}

// TestSaveLogExport_participatesInDialogGuard — same contract for
// SaveLogExport as SaveErrorReport.
func TestSaveLogExport_participatesInDialogGuard(t *testing.T) {
	testLogger(t)
	resetDialogPathLog()
	a := &App{ctx: context.Background()}

	dialogMu.Lock()
	_, err := a.SaveLogExport("log content")
	dialogMu.Unlock()
	if err == nil || err.Error() != dialogBusyMessage {
		t.Errorf("locked dialogMu err = %v, want %q", err, dialogBusyMessage)
	}

	noCtx := &App{ctx: nil}
	if _, err := noCtx.SaveLogExport("log content"); err == nil || err.Error() != appNotReadyError {
		t.Errorf("nil-ctx err = %v, want %q", err, appNotReadyError)
	}

	restorePortal := enablePortal(
		nil, nil, nil,
		func(parent, title, defaultName string, filters []runtime.FileFilter) (string, error) {
			panic("boom")
		},
		nil,
	)
	defer restorePortal()
	_, err = a.SaveLogExport("log content")
	if err == nil {
		t.Error("expected recovered panic error, got nil")
	}
}
