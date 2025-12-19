//go:build windows && !mesa

package mesa

// embeddedDLLs is empty when built without -tags mesa
// This produces a smaller binary (~25MB smaller) but requires hardware GPU/OpenGL support.
//
// Users on systems without GPU (VMs, RDP, etc.) should use the "-mesa" build variant.
var embeddedDLLs = map[string][]byte{}

// mesaEmbedded indicates whether Mesa DLLs are embedded in this build
const mesaEmbedded = false
