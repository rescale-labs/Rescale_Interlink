// Package cli provides command shortcuts for common operations.
package cli

import (
	"fmt"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/spf13/cobra"
)

// AddShortcuts adds shortcut commands to the root command.
// Shortcuts provide convenient aliases for commonly-used operations.
func AddShortcuts(rootCmd *cobra.Command) {
	rootCmd.AddCommand(newUploadShortcut())
	rootCmd.AddCommand(newDownloadShortcut())
	rootCmd.AddCommand(newLsShortcut())
}

// newUploadShortcut is 'files upload' under a shorter name, so it takes the
// same flags and handles duplicates the same way.
func newUploadShortcut() *cobra.Command {
	cmd := newFilesUploadCmd()
	cmd.Short = "Upload files (shortcut for 'files upload')"
	cmd.Long = "Shortcut for 'files upload', with the same flags.\n\n" + cmd.Long
	return cmd
}

// newDownloadShortcut creates the 'download' shortcut command.
// Shortcut for: files download
func newDownloadShortcut() *cobra.Command {
	var outputDir string
	var maxConcurrent int

	cmd := &cobra.Command{
		Use:   "download <file-id> [file-id...]",
		Short: "Download files (shortcut for 'files download')",
		Long: `Shortcut for downloading files from Rescale.

Equivalent to: rescale-int files download <ids>

Examples:
  rescale-int download abc123
  rescale-int download abc123 def456 --outdir ./downloads
  rescale-int download abc123 --outdir .`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			if err := config.CheckMaxConcurrent(maxConcurrent, "--max-concurrent"); err != nil {
				return err
			}

			// Get API client
			apiClient, err := getAPIClient()
			if err != nil {
				return err
			}

			// Use shared helper function (no conflict flags for shortcut)
			return executeFileDownload(GetContext(), args, outputDir, maxConcurrent, false, false, false, false, apiClient, logger)
		},
	}

	cmd.Flags().StringVarP(&outputDir, "outdir", "o", ".", "Output directory for downloaded files")
	cmd.Flags().IntVarP(&maxConcurrent, "max-concurrent", "m", constants.DefaultMaxConcurrent,
		fmt.Sprintf("Maximum concurrent downloads (%d-%d)", constants.MinMaxConcurrent, constants.MaxMaxConcurrent))

	return cmd
}

// newLsShortcut creates the 'ls' shortcut command.
// Shortcut for: jobs list
func newLsShortcut() *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List jobs (shortcut for 'jobs list')",
		Long: `Shortcut for listing jobs.

Equivalent to: rescale-int jobs list

Examples:
  rescale-int ls
  rescale-int ls --limit 10
  rescale-int ls -n 20`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Run the jobs list logic directly instead of delegating to a new command
			return runJobsList(limit)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "Maximum number of jobs to list")

	return cmd
}
