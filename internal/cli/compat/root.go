package compat

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/version"
)

// NewCompatRootCmd creates the root Cobra command for compat mode.
// This mirrors rescale-cli's flag interface while using Interlink's backend.
func NewCompatRootCmd() *cobra.Command {
	cc := &CompatContext{}

	rootCmd := &cobra.Command{
		Use:   "rescale-cli",
		Short: "Rescale CLI compatibility mode",
		Long: fmt.Sprintf(`Rescale Interlink %s — rescale-cli compatibility mode

This mode provides drop-in compatibility with rescale-cli scripts.
Commands use rescale-cli's flag syntax and exit code conventions.

Exit codes:
  0   Success
  33  Error (matches rescale-cli convention)`, version.Version),

		// We format errors ourselves in ExecuteCompat
		SilenceErrors: true,
		SilenceUsage:  true,

		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Store compat context in command context for subcommands
			SetCompatContext(cmd, cc)

			// Skip auth for commands that don't need it (e.g., check-for-update)
			if cmd.Annotations != nil && cmd.Annotations["skipAuth"] == "true" {
				return nil
			}

			// Create API client eagerly so auth errors surface before command execution
			client, err := cc.GetAPIClient(cmd.Context())
			if err != nil {
				return err
			}

			// Print auth line (unless quiet mode)
			if !cc.Quiet {
				profile, err := client.GetUserProfile(cmd.Context())
				if err != nil {
					return fmt.Errorf("authentication failed: %w", err)
				}
				cc.AuthEmail = profile.Email
				fmt.Fprintln(os.Stdout, FormatAuthLine(profile.Email))
			}

			return nil
		},
	}

	// Initialize command context so SetCompatContext works
	rootCmd.SetContext(context.Background())

	// Global flags matching rescale-cli's interface
	rootCmd.PersistentFlags().StringVarP(&cc.APIKey, "api-token", "p", "", "API token for authentication")
	rootCmd.PersistentFlags().StringVarP(&cc.APIBaseURL, "api-base-url", "X", "", "Rescale API base URL")
	rootCmd.PersistentFlags().BoolVarP(&cc.Quiet, "quiet", "q", false, "Suppress informational output")
	rootCmd.PersistentFlags().BoolVar(&cc.NoPrompt, "no-prompt", false, "Disable interactive prompts (default behavior)")

	// Hidden flags accepted for compatibility but ignored
	var enableErrorTracking bool
	rootCmd.PersistentFlags().BoolVar(&enableErrorTracking, "enableErrorTracking", false, "Enable error tracking (ignored)")
	rootCmd.PersistentFlags().MarkHidden("enableErrorTracking")

	// Version output: set rootCmd.Version so Cobra's automatic --version works.
	// Also register -v as a shorthand for --version (rescale-cli uses -v for version, not verbose).
	rootCmd.Version = version.Version
	rootCmd.Flags().BoolP("version", "v", false, "Print version and exit")

	// Add implemented commands
	rootCmd.AddCommand(newStatusCmd())
	rootCmd.AddCommand(newStopCmd())
	rootCmd.AddCommand(newDeleteCmd())
	rootCmd.AddCommand(newCheckForUpdateCmd())
	rootCmd.AddCommand(newListInfoCmd())
	rootCmd.AddCommand(newUploadCmd())
	rootCmd.AddCommand(newDownloadFileCmd())
	rootCmd.AddCommand(newSubmitCmd())

	// Add remaining placeholder commands (not yet implemented)
	addPlaceholderCommands(rootCmd)

	return rootCmd
}
