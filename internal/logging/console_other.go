//go:build !windows

package logging

import "github.com/mattn/go-isatty"

// consoleColor reports whether fd is a terminal; every terminal here renders
// escape codes.
func consoleColor(fd uintptr) bool { return isatty.IsTerminal(fd) }
