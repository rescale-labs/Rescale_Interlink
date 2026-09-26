//go:build windows

package logging

import "golang.org/x/sys/windows"

// consoleColor reports whether fd is a console that renders escape codes,
// switching on virtual terminal processing where it is off. zerolog adapts
// os.Stdout and os.Stderr for an older console itself, but not a writer wrapped
// around them (redaction, the progress display), so a console where processing
// cannot be switched on gets no colour rather than raw escape codes.
func consoleColor(fd uintptr) bool {
	h := windows.Handle(fd)
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return false
	}
	return mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 ||
		windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
