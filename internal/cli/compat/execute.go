package compat

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/rescale/rescale-int/internal/reporting"
)

// ExecuteCompat runs the compat-mode command tree.
// Returns (error, exitCode) where exitCode is 0 on success or 33 on error.
func ExecuteCompat() (error, int) {
	rootCmd := NewCompatRootCmd()

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

	executedCmd, err := rootCmd.ExecuteC()

	// Clean up signal handler
	signal.Stop(sigChan)
	close(sigChan)

	if err != nil {
		// Preserve Interlink's crash report auto-save (writes to stderr)
		operation := ""
		if executedCmd != nil {
			operation = executedCmd.CommandPath()
		}
		reporting.HandleCLIError(err, "cli", operation, "")

		// Format error to stdout (matching rescale-cli's SLF4J stdout logging)
		fmt.Fprintln(os.Stdout, FormatErrorMessage(err.Error()))

		return err, ExitCodeCompatError
	}

	return nil, 0
}
