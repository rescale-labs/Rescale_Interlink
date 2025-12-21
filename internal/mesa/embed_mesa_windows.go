//go:build windows && mesa

package mesa

import (
	_ "embed"
)

// Embedded Mesa DLLs - these are included in the Windows binary when built with -tags mesa
// This adds ~25MB to the binary size but provides software rendering fallback.
//
//go:embed dlls/opengl32.dll
var opengl32DLL []byte

//go:embed dlls/libgallium_wgl.dll
var libgalliumDLL []byte

//go:embed dlls/libglapi.dll
var libglapiDLL []byte

// embeddedDLLs maps filenames to their embedded content
// This map is populated with actual DLL data when built with -tags mesa
var embeddedDLLs = map[string][]byte{
	"opengl32.dll":       opengl32DLL,
	"libgallium_wgl.dll": libgalliumDLL,
	"libglapi.dll":       libglapiDLL,
}

// mesaEmbedded indicates whether Mesa DLLs are embedded in this build
const mesaEmbedded = true
