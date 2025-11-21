package gui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// rescaleTheme is a custom theme for Rescale Interlink
type rescaleTheme struct{}

func (t *rescaleTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNamePrimary:
		return color.NRGBA{R: 0x00, G: 0x7A, B: 0xCC, A: 0xFF} // Rescale blue
	case theme.ColorNameButton:
		return color.NRGBA{R: 0x00, G: 0x7A, B: 0xCC, A: 0xFF}
	case theme.ColorNameSuccess:
		return color.NRGBA{R: 0x4C, G: 0xAF, B: 0x50, A: 0xFF}
	case theme.ColorNameError:
		return color.NRGBA{R: 0xF4, G: 0x43, B: 0x36, A: 0xFF}
	case theme.ColorNameWarning:
		return color.NRGBA{R: 0xFF, G: 0x98, B: 0x00, A: 0xFF}
	default:
		return theme.DefaultTheme().Color(name, variant)
	}
}

func (t *rescaleTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

func (t *rescaleTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (t *rescaleTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameText:
		return 13
	case theme.SizeNameHeadingText:
		return 18
	default:
		return theme.DefaultTheme().Size(name)
	}
}
