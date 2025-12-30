// Package gui provides the graphical user interface for rescale-int.
// This file contains embedded resources (logos, icons).
package gui

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

//go:embed assets/logo_left_1.png
var logoLeft1Data []byte

//go:embed assets/logo_left_2.png
var logoLeft2Data []byte

// LogoLeft1 returns the Rescale logo resource (wider, with text)
func LogoLeft1() fyne.Resource {
	return fyne.NewStaticResource("logo_left_1.png", logoLeft1Data)
}

// LogoLeft2 returns the Interlink logo resource
func LogoLeft2() fyne.Resource {
	return fyne.NewStaticResource("logo_left_2.png", logoLeft2Data)
}
