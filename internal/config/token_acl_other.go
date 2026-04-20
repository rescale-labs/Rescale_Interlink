//go:build !windows

package config

// applyTokenFileACL is a no-op on non-Windows platforms. Unix systems rely
// on mode 0600 from os.WriteFile (enforced at the call site) as the sole
// protection for the token file. Per spec §11.2, this is the industry-
// standard posture for developer tools.
func applyTokenFileACL(_ string, _ string) error {
	return nil
}

// currentUserSID returns an empty string on non-Windows. Callers that need
// an SID use this to decide whether to invoke applyTokenFileACL at all.
func currentUserSID() (string, error) {
	return "", nil
}
