// Package cli provides command shortcuts for common operations.
package cli

import (
	"fmt"

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

// newUploadShortcut creates the 'upload' shortcut command.
// Shortcut for: files upload
func newUploadShortcut() *cobra.Command {
	var folderID string
	var maxConcurrent int
	var preEncrypt bool

	cmd := &cobra.Command{
		Use:   "upload <file> [file...]",
		Short: "Upload files (shortcut for 'files upload')",
		Long: `Shortcut for uploading files to Rescale.

Equivalent to: rescale-int files upload <files>

By default, files are encrypted using streaming encryption (per-part, on-the-fly).
Use --pre-encrypt for compatibility with older Rescale clients.

Examples:
  rescale-int upload input.txt data.csv
  rescale-int upload model.tar.gz --folder-id abc123
  rescale-int upload *.dat --folder-id abc123
  rescale-int upload *.dat --max-concurrent 10
  rescale-int upload large_file.tar.gz --pre-encrypt`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			// Validate maxConcurrent
			if maxConcurrent < constants.MinMaxConcurrent || maxConcurrent > constants.MaxMaxConcurrent {
				return fmt.Errorf("--max-concurrent must be between %d and %d, got %d",
					constants.MinMaxConcurrent, constants.MaxMaxConcurrent, maxConcurrent)
			}

			// Get API client
			apiClient, err := getAPIClient()
			if err != nil {
				return err
			}

			// Use shared helper function
			return executeFileUpload(GetContext(), args, folderID, maxConcurrent, preEncrypt, apiClient, logger)
		},
	}

	cmd.Flags().StringVarP(&folderID, "folder-id", "d", "", "Upload to specific folder (optional, default: root)")
	cmd.Flags().IntVarP(&maxConcurrent, "max-concurrent", "m", constants.DefaultMaxConcurrent,
		fmt.Sprintf("Maximum concurrent file uploads (%d-%d)", constants.MinMaxConcurrent, constants.MaxMaxConcurrent))
	cmd.Flags().BoolVar(&preEncrypt, "pre-encrypt", false, "Use legacy pre-encryption (for compatibility with older Rescale clients)")

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

			// Validate max-concurrent
			if maxConcurrent < constants.MinMaxConcurrent || maxConcurrent > constants.MaxMaxConcurrent {
				return fmt.Errorf("--max-concurrent must be between %d and %d, got %d",
					constants.MinMaxConcurrent, constants.MaxMaxConcurrent, maxConcurrent)
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
