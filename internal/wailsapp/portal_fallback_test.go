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

// TestSelectDirectory_portalRouting covers the three-way routing between the
// portal and the GTK dialog: a portal success never reaches GTK, an
// "unavailable" portal falls back to GTK and forwards its value, and any other
// portal error surfaces to the caller unfallen-back (so users on broken portals
// see the actionable message instead of a second dialog).
func TestSelectDirectory_portalRouting(t *testing.T) {
	portalTimeout := errors.New("portal: timeout after 5m0s waiting for Response signal; try setting RESCALE_DISABLE_PORTAL=1")

	tests := []struct {
		name        string
		portalPath  string
		portalErr   error
		unavailable func(error) bool // nil keeps the real classifier
		gtkPath     string
		wantPath    string
		wantErr     bool
		wantGTK     bool
	}{
		{
			name:       "portal success does not reach GTK",
			portalPath: "/tmp/from-portal",
			gtkPath:    "gtk-should-not-be-called",
			wantPath:   "/tmp/from-portal",
		},
		{
			name:        "portal unavailable falls back to GTK",
			portalErr:   errPortalUnavailable,
			unavailable: func(err error) bool { return errors.Is(err, errPortalUnavailable) },
			gtkPath:     "/tmp/from-gtk",
			wantPath:    "/tmp/from-gtk",
			wantGTK:     true,
		},
		{
			name:        "portal timeout surfaces without fallback",
			portalErr:   portalTimeout,
			unavailable: func(err error) bool { return false },
			gtkPath:     "/tmp/should-not-reach",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testLogger(t)
			resetDialogPathLog()

			gtkCalled := false
			restoreGTK := stubDialogs(
				func(context.Context, runtime.OpenDialogOptions) (string, error) {
					gtkCalled = true
					return tt.gtkPath, nil
				}, nil, nil, nil,
			)
			defer restoreGTK()

			restorePortal := enablePortal(
				func(parent, title string) (string, error) { return tt.portalPath, tt.portalErr },
				nil, nil, nil, tt.unavailable,
			)
			defer restorePortal()

			a := &App{ctx: context.Background()}
			got, err := a.SelectDirectory("pick a dir")

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got result %q and nil err", got)
				}
			} else {
				if err != nil {
					t.Fatalf("SelectDirectory err: %v", err)
				}
				if got != tt.wantPath {
					t.Errorf("result = %q, want %q", got, tt.wantPath)
				}
			}
			if gtkCalled != tt.wantGTK {
				t.Errorf("GTK dialog invoked = %v, want %v", gtkCalled, tt.wantGTK)
			}
		})
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

// TestSaveBindings_participateInDialogGuard — every save binding shares one
// contract: a concurrent call while dialogMu is held returns the busy message,
// a nil ctx returns appNotReadyError, and a panic inside the save path is
// recovered rather than crashing the app.
func TestSaveBindings_participateInDialogGuard(t *testing.T) {
	bindings := []struct {
		name string
		call func(*App) (string, error)
	}{
		{"SaveErrorReport", func(a *App) (string, error) { return a.SaveErrorReport(`{}`) }},
		{"SaveLogExport", func(a *App) (string, error) { return a.SaveLogExport("log content") }},
	}

	for _, b := range bindings {
		t.Run(b.name, func(t *testing.T) {
			testLogger(t)
			resetDialogPathLog()
			a := &App{ctx: context.Background()}

			// (1) busy mutex → dialogBusyMessage
			dialogMu.Lock()
			_, err := b.call(a)
			dialogMu.Unlock()
			if err == nil || err.Error() != dialogBusyMessage {
				t.Errorf("locked dialogMu err = %v, want %q", err, dialogBusyMessage)
			}

			// (2) nil ctx → appNotReadyError
			if _, err := b.call(&App{ctx: nil}); err == nil || err.Error() != appNotReadyError {
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
			if _, err := b.call(a); err == nil {
				t.Error("expected recovered panic error, got nil")
			}
		})
	}
}
