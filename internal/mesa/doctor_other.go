//go:build !windows

package mesa

import "fmt"

// Doctor runs Mesa diagnostics. On non-Windows platforms, Mesa is not used.
func Doctor() {
	fmt.Println("Mesa diagnostics are only available on Windows.")
	fmt.Println("On this platform, Mesa software rendering is not needed.")
}
