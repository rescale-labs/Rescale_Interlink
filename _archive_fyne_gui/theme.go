package gui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// rescaleTheme is a custom theme for Rescale Interlink.
// This theme forces a light appearance regardless of OS dark/light mode preference
// to ensure cross-platform visual consistency (macOS, Linux, Windows).
type rescaleTheme struct{}

// =============================================================================
// Color Definitions - Forced Light Mode
// =============================================================================
// These colors ensure consistent appearance across all platforms by explicitly
// defining all key colors rather than delegating to DefaultTheme (which would
// change based on OS dark/light mode preference).

var (
	// Brand colors
	rescaleBlue    = color.NRGBA{R: 0x00, G: 0x7A, B: 0xCC, A: 0xFF} // #007ACC - Primary brand color
	rescaleSuccess = color.NRGBA{R: 0x4C, G: 0xAF, B: 0x50, A: 0xFF} // #4CAF50 - Green
	rescaleError   = color.NRGBA{R: 0xF4, G: 0x43, B: 0x36, A: 0xFF} // #F44336 - Red
	rescaleWarning = color.NRGBA{R: 0xFF, G: 0x98, B: 0x00, A: 0xFF} // #FF9800 - Orange

	// Light mode background colors
	lightBackground        = color.NRGBA{R: 0xFA, G: 0xFA, B: 0xFA, A: 0xFF} // #FAFAFA - Main background
	lightInputBackground   = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF} // #FFFFFF - Input fields
	lightMenuBackground    = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF} // #FFFFFF - Menus
	lightOverlayBackground = color.NRGBA{R: 0xF5, G: 0xF5, B: 0xF5, A: 0xFF} // #F5F5F5 - Dialogs/overlays
	lightHeaderBackground  = color.NRGBA{R: 0xF0, G: 0xF0, B: 0xF0, A: 0xFF} // #F0F0F0 - Table headers

	// Light mode text colors
	lightForeground  = color.NRGBA{R: 0x21, G: 0x21, B: 0x21, A: 0xFF} // #212121 - Primary text
	lightDisabled    = color.NRGBA{R: 0x9E, G: 0x9E, B: 0x9E, A: 0xFF} // #9E9E9E - Disabled text
	lightPlaceholder = color.NRGBA{R: 0x75, G: 0x75, B: 0x75, A: 0xFF} // #757575 - Placeholder text

	// Light mode UI element colors
	lightSeparator      = color.NRGBA{R: 0xE0, G: 0xE0, B: 0xE0, A: 0xFF} // #E0E0E0 - Separators
	lightInputBorder    = color.NRGBA{R: 0xBD, G: 0xBD, B: 0xBD, A: 0xFF} // #BDBDBD - Input borders
	lightScrollBar      = color.NRGBA{R: 0xBD, G: 0xBD, B: 0xBD, A: 0xFF} // #BDBDBD - Scrollbar
	lightScrollBarBg    = color.NRGBA{R: 0xEE, G: 0xEE, B: 0xEE, A: 0xFF} // #EEEEEE - Scrollbar background
	lightHover          = color.NRGBA{R: 0x90, G: 0xCA, B: 0xF9, A: 0xFF} // #90CAF9 - Hover highlight (medium blue, better contrast)
	lightPressed        = color.NRGBA{R: 0xBB, G: 0xDE, B: 0xFB, A: 0xFF} // #BBDEFB - Pressed state
	lightSelection      = color.NRGBA{R: 0xBB, G: 0xDE, B: 0xFB, A: 0xFF} // #BBDEFB - Text selection
	lightFocus          = color.NRGBA{R: 0x00, G: 0x7A, B: 0xCC, A: 0x40} // Primary with alpha for focus ring
	lightShadow         = color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0x33} // Shadow with transparency
	lightDisabledButton = color.NRGBA{R: 0xE0, G: 0xE0, B: 0xE0, A: 0xFF} // #E0E0E0 - Disabled buttons

	// Contrast colors for colored backgrounds
	lightOnPrimary = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF} // White text on primary
	lightOnSuccess = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF} // White text on success
	lightOnError   = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF} // White text on error
	lightOnWarning = color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0xFF} // Black text on warning (orange)
)

// Color returns the color for the specified name, forcing light mode appearance.
// IMPORTANT: We always return light mode colors regardless of the variant parameter.
// This ensures consistent appearance on Linux systems where the OS may report dark mode,
// but we want to force a light UI appearance.
// Fyne uses separate color names (ForegroundOnPrimary, etc.) for text on colored backgrounds.
func (t *rescaleTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	// Explicitly ignore variant - we force light mode for all colors.
	// Without this, Linux systems in dark mode would get white foreground on our light background.
	_ = variant

	switch name {
	// Brand colors
	case theme.ColorNamePrimary:
		return rescaleBlue
	case theme.ColorNameButton:
		return rescaleBlue
	case theme.ColorNameSuccess:
		return rescaleSuccess
	case theme.ColorNameError:
		return rescaleError
	case theme.ColorNameWarning:
		return rescaleWarning
	case theme.ColorNameHyperlink:
		return rescaleBlue

	// Background colors
	case theme.ColorNameBackground:
		return lightBackground
	case theme.ColorNameInputBackground:
		return lightInputBackground
	case theme.ColorNameMenuBackground:
		return lightMenuBackground
	case theme.ColorNameOverlayBackground:
		return lightOverlayBackground
	case theme.ColorNameHeaderBackground:
		return lightHeaderBackground

	// Text colors
	case theme.ColorNameForeground:
		return lightForeground
	case theme.ColorNameDisabled:
		return lightDisabled
	case theme.ColorNamePlaceHolder:
		return lightPlaceholder

	// UI element colors
	case theme.ColorNameSeparator:
		return lightSeparator
	case theme.ColorNameInputBorder:
		return lightInputBorder
	case theme.ColorNameScrollBar:
		return lightScrollBar

	// Interaction colors
	case theme.ColorNameHover:
		return lightHover
	case theme.ColorNamePressed:
		return lightPressed
	case theme.ColorNameSelection:
		return lightSelection
	case theme.ColorNameFocus:
		return lightFocus
	case theme.ColorNameShadow:
		return lightShadow
	case theme.ColorNameDisabledButton:
		return lightDisabledButton

	// Contrast colors for semantic backgrounds
	case theme.ColorNameForegroundOnPrimary:
		return lightOnPrimary
	case theme.ColorNameForegroundOnSuccess:
		return lightOnSuccess
	case theme.ColorNameForegroundOnError:
		return lightOnError
	case theme.ColorNameForegroundOnWarning:
		return lightOnWarning

	default:
		// For any colors not explicitly defined, use light variant from default theme
		return theme.DefaultTheme().Color(name, theme.VariantLight)
	}
}

// Font returns the font resource for the given text style.
func (t *rescaleTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

// Icon returns the icon resource for the given icon name.
func (t *rescaleTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

// =============================================================================
// Size Definitions - Improved Spacing and Typography
// =============================================================================
// Spacing increased by ~50-75% from Fyne defaults for a less crowded appearance.
// Typography adjusted for better visual hierarchy.

// Size returns the size value for the specified name.
func (t *rescaleTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	// Typography - improved hierarchy
	case theme.SizeNameText:
		return 14 // Up from 13 - slightly larger base text
	case theme.SizeNameHeadingText:
		return 20 // Up from 18 - more prominent headings
	case theme.SizeNameSubHeadingText:
		return 16 // Sub-headings for section titles
	case theme.SizeNameCaptionText:
		return 12 // Smaller helper/caption text

	// Spacing - moderate increase for less crowded feel
	case theme.SizeNamePadding:
		return 6 // Up from 4 - standard outer padding
	case theme.SizeNameInnerPadding:
		return 6 // Up from 4 - widget internal padding
	case theme.SizeNameLineSpacing:
		return 6 // Up from 4 - space between text lines

	// Separators - slightly more visible
	case theme.SizeNameSeparatorThickness:
		return 2 // Up from 1 - more visible separators

	// Input elements - slightly more rounded
	case theme.SizeNameInputBorder:
		return 2 // Slightly thicker input borders
	case theme.SizeNameInputRadius:
		return 5 // Rounded corners on inputs
	case theme.SizeNameSelectionRadius:
		return 4 // Rounded selection highlights

	// Scrollbars
	case theme.SizeNameScrollBar:
		return 12 // Standard scrollbar width
	case theme.SizeNameScrollBarSmall:
		return 4 // Minimized scrollbar
	case theme.SizeNameScrollBarRadius:
		return 4 // Rounded scrollbar ends

	default:
		return theme.DefaultTheme().Size(name)
	}
}
