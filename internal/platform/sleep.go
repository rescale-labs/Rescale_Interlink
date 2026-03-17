// Package platform provides cross-platform OS integration for Rescale Interlink.
// v4.8.7: Sleep prevention during active file transfers.
package platform

// InhibitSleep prevents the OS from sleeping/suspending while file transfers are active.
// Returns a release function that must be called when the transfer completes.
// The release function is safe to call multiple times (idempotent).
// If sleep inhibition is not supported or fails, returns a no-op release function and an error.
func InhibitSleep(reason string) (release func(), err error) {
	return inhibitSleep(reason)
}
