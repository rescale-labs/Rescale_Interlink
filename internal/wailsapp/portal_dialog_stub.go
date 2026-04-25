//go:build !linux

package wailsapp

// Non-Linux stubs for the portal dialog symbols referenced by the
// cross-platform config_bindings.go. portalEnabled always returns false,
// so these are never actually invoked in production darwin/windows
// builds — they exist only to satisfy compilation across build tags.

import (
	"errors"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

var errPortalUnavailable = errors.New("portal: unavailable")

func portalEnabled() bool                      { return false }
func isPortalUnavailable(err error) bool       { return false }
func portalOpenFile(parent, title string) (string, error) {
	return "", errPortalUnavailable
}
func portalOpenDirectory(parent, title string) (string, error) {
	return "", errPortalUnavailable
}
func portalOpenMultipleFiles(parent, title string) ([]string, error) {
	return nil, errPortalUnavailable
}
func portalSaveFile(parent, title, defaultName string, filters []runtime.FileFilter) (string, error) {
	return "", errPortalUnavailable
}
