//go:build windows

package main

import (
	_ "embed"
)

// iconData contains the Rescale brand icon for the system tray.
// v4.1.1: Changed from PNG to ICO format with multiple resolutions
// for proper display at various DPI settings. ICO file contains
// 16x16, 24x24, 32x32, 48x48, and 256x256 sizes.
//
//go:embed assets/icon.ico
var iconData []byte
