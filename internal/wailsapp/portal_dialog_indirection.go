package wailsapp

// Cross-platform function-pointer indirection for the portal layer.
//
// portalEnabled() returns true by default on Linux (the primary bug #41
// target), so existing dialog binding tests would try to hit a real
// D-Bus portal at test time. Tests override these vars via t.Cleanup to
// simulate portal success / fallback / timeout without network.
//
// All portalAware* helpers in config_bindings.go call these vars rather
// than the underlying portal* functions directly. Build-tagged files
// (portal_dialog_linux.go / portal_dialog_stub.go) provide matching
// signatures on each OS.

import (
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

var (
	portalEnabledFunc           func() bool                                                  = portalEnabled
	portalOpenFileFunc          func(parent, title string) (string, error)                   = portalOpenFile
	portalOpenDirectoryFunc     func(parent, title string) (string, error)                   = portalOpenDirectory
	portalOpenMultipleFilesFunc func(parent, title string) ([]string, error)                 = portalOpenMultipleFiles
	portalSaveFileFunc          func(parent, title, defaultName string, filters []runtime.FileFilter) (string, error) = portalSaveFile
	isPortalUnavailableFunc     func(error) bool                                             = isPortalUnavailable
)
