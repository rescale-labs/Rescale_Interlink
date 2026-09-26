package service

import "fmt"

// RemovedMessage is the line a service installed by an earlier version logs
// once it has removed itself.
const RemovedMessage = "Multi-user service mode is not available in this version, so this service has removed itself. Auto-download runs in each user's session from the Interlink app."

// scmHost is what a service installed by an earlier version needs from
// Windows when Windows starts it. windows_service.go supplies the real one; a
// test stands in for it on any system.
type scmHost struct {
	// dispatch registers with the SCM and runs work, which may report the
	// service running; once work returns, dispatch reports it stopped with
	// exit code 0.
	dispatch func(work func(running func())) error
	// notService reports whether a dispatch error means that no SCM started
	// this process.
	notService  func(error) bool
	deleteSelf  func() error
	clearMarker func()
	log         func(string)
}

// retire is all a service installed by an earlier version does now: its
// registration names a binary in a folder the user can write, and runs it as
// LocalSystem, so it removes itself and stops. It reports whether an SCM
// started this process.
func retire(h scmHost) bool {
	err := h.dispatch(func(running func()) {
		running()
		if err := h.deleteSelf(); err != nil {
			h.log(fmt.Sprintf("Multi-user service mode is not available in this version, and this service could not remove itself: %v. "+
				"Remove it from the Interlink app or with 'rescale-int service uninstall' as administrator.", err))
			return
		}
		h.clearMarker()
		h.log(RemovedMessage)
	})
	return err == nil || !h.notService(err)
}
