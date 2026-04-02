//go:build windows

package main

import (
	_ "embed"
)

// iconData contains the Rescale brand icon for the system tray.
// ICO format with multiple resolutions (16x16, 24x24, 32x32, 48x48, 256x256)
// for proper display at various DPI settings.
//
//go:embed assets/icon.ico
var iconData []byte
