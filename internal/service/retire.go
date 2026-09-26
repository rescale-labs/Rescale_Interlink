package service

import "fmt"

// RemovedMessage is the line a service installed by an earlier version logs
// once it has removed itself.
const RemovedMessage = "Multi-user service mode is not available in this version, so this service has removed itself. Auto-download runs in each user's session from the Interlink app."

// OldServiceRunning is what the app, the tray and the CLI say while a service
// installed by an earlier version runs. It serves only the shared pipe this
// version no longer uses, so nothing here can reach it.
const OldServiceRunning = "A Windows service from an earlier version is running; restart Windows, or run 'rescale-int service uninstall' as administrator"

// EarlierDaemonRunning says how to end a daemon that is already running,
// whether or not it answers on this user's pipe. One an earlier version started
// listens on that shared pipe, where the app cannot reach it. 'daemon stop
// --force' asks a daemon over IPC to stop, and ends by its process one that
// cannot be asked or does not stop.
const EarlierDaemonRunning = "To end it, including one an earlier version of Interlink started, run 'rescale-int daemon stop --force' or end the rescale-int process in Task Manager"

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

// retire is all a service installed by an earlier version does now: this
// version has no service mode, so it removes itself and stops. It reports
// whether an SCM started this process.
func retire(h scmHost) bool {
	err := h.dispatch(func(running func()) {
		running()
		if err := h.deleteSelf(); err != nil {
			h.log(fmt.Sprintf("Multi-user service mode is not available in this version, and this service could not remove itself: %v. "+
				"Remove it with 'rescale-int service uninstall' as administrator.", err))
			return
		}
		h.clearMarker()
		h.log(RemovedMessage)
	})
	return err == nil || !h.notService(err)
}
