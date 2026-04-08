package compat

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// ExecuteCompat runs the compat-mode command tree.
// Returns (error, exitCode) where exitCode is 0 on success or 33 on error.
func ExecuteCompat() (error, int) {
	rootCmd, cc := NewCompatRootCmd()

	// Signal handling — same pattern as cli.Execute()
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		for sig := range sigChan {
			if sig != nil {
				fmt.Fprintf(os.Stderr, "\nReceived signal %v, cancelling...\n", sig)
			}
		}
	}()

	// Normalize multi-char short flags and multi-value -f before Cobra parses
	rootCmd.SetArgs(NormalizeCompatArgs(os.Args[1:]))

	_, err := rootCmd.ExecuteC()

	// Clean up signal handler
	signal.Stop(sigChan)
	close(sigChan)

	if err != nil {
		if !cc.Quiet {
			// SLF4J one-liner only — no Interlink error report box in compat mode
			fmt.Fprintln(os.Stdout, FormatErrorMessage(err.Error()))
		}
		return err, ExitCodeCompatError
	}

	return nil, 0
}
