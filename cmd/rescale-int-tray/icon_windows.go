//go:build windows

package main

import (
	_ "embed"
)

// iconData contains the Rescale brand icon for the system tray.
// v4.0.8: Replaced placeholder with actual Rescale brand icon (64x64 PNG).
// The fyne.io/systray library handles PNG format on Windows.
// This icon is derived from build/appicon.png (the main Wails app icon).
//
//go:embed assets/icon.png
var iconData []byte
