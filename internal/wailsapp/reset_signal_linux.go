//go:build linux

package wailsapp

import wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

// resetLinuxSignalHandlers re-installs SIGSEGV/SIGBUS/SIGFPE/SIGABRT handlers
// with the SA_ONSTACK flag. WebKit2GTK installs signal handlers without
// SA_ONSTACK, which breaks Go's panic recovery for bound methods called from
// JavaScript. Call this immediately before a CGo call that could panic (e.g.
// a GTK dialog call); WebKit may re-install its handlers at any time, so a
// one-shot startup reset is insufficient.
//
// Upstream: wailsapp/wails#3965, #4855, #4921.
func resetLinuxSignalHandlers() {
	wailsruntime.ResetSignalHandlers()
}
