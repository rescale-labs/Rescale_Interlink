//go:build !windows

package config

// applyTokenFileACL is a no-op on non-Windows platforms. Unix systems rely
// on mode 0600 from os.WriteFile (enforced at the call site) as the sole
// protection for the token file, the usual posture for developer tools.
func applyTokenFileACL(_ string, _ string) error {
	return nil
}
