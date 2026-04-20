//go:build !windows

package pathutil

// validateWindowsStrict is unreachable on non-Windows. Included for symmetry
// so callers compile cross-platform.
func validateWindowsStrict(resolved string) PathValidationResult {
	return probeWritable(resolved)
}
