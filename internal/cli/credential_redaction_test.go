package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/logging"
)

// printedError runs a command that fails with err through the root command and
// returns what reached stderr: cobra prints the error itself, as "Error: ...".
func printedError(t *testing.T, err error) string {
	t.Helper()
	return captureStderr(t, func() {
		// NewRootCmd binds os.Stderr, so it is built inside the capture.
		rootCmd := NewRootCmd()
		rootCmd.PersistentPreRun = nil // replaces process-wide state; not under test
		rootCmd.SilenceUsage = true
		rootCmd.AddCommand(&cobra.Command{Use: "fail", RunE: func(*cobra.Command, []string) error { return err }})
		rootCmd.SetArgs([]string{"fail"})
		_ = rootCmd.Execute()
	})
}

// Every place the CLI prints a failure, cobra's Error: line, its logger and
// upload-dir's list of failed files, redacts the storage URLs in it.
func TestCLIOutputRedactsCredentials(t *testing.T) {
	defer func(orig *logging.Logger) { logger = orig }(logger)
	for _, err := range []error{
		errors.New(`Put "https://a.blob.core.windows.net/c/f?comp=block&sig=SECRET": EOF`),
		errors.New(`Get "https://b.s3.amazonaws.com/k?X-Amz-Signature=SECRET": EOF`),
	} {
		for sink, printed := range map[string]string{
			"error line": printedError(t, err),
			"logger":     captureStdout(t, func() { logger = nil; GetLogger().Error().Err(err).Msg("Failed to upload file") }),
			"failure list": captureStdout(t, func() {
				printUploadFailure("/in", UploadError{FilePath: "/in/a.bin", Error: err})
			}),
		} {
			if strings.Contains(printed, "SECRET") || !strings.Contains(printed, "=REDACTED") {
				t.Errorf("the %s printed %q, want the credentials redacted", sink, printed)
			}
		}
	}

	// A refusal that names the token file carries no credential and must read
	// as written.
	refusal := "--config /tmp/cfg/token has the name of the token file, which holds the API key; give the configuration file another name"
	if printed := printedError(t, errors.New(refusal)); printed != "Error: "+refusal+"\n" {
		t.Errorf("printed %q, want the refusal unchanged", printed)
	}
}
