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
			if maxConcurrent < 1 || maxConcurrent > 10 {
				return fmt.Errorf("--max-concurrent must be between 1 and 10, got %d", maxConcurrent)
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
	var status string

	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List jobs (shortcut for 'jobs list')",
		Long: `Shortcut for listing jobs.

Equivalent to: rescale-int jobs list

Examples:
  rescale-int ls
  rescale-int ls --limit 10
  rescale-int ls --status Completed`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Directly call the jobs list logic instead of delegating to a new command
			logger := GetLogger()

			// Get API client
			apiClient, err := getAPIClient()
			if err != nil {
				return err
			}

			ctx := GetContext()

			// List jobs
			logger.Info().Msg("Fetching jobs")
			jobs, err := apiClient.ListJobs(ctx)
			if err != nil {
				return fmt.Errorf("failed to list jobs: %w", err)
			}

			if len(jobs) == 0 {
				fmt.Println("No jobs found")
				return nil
			}

			// Display jobs
			fmt.Printf("Found %d job(s):\n\n", len(jobs))

			displayCount := len(jobs)
			if limit > 0 && limit < len(jobs) {
				displayCount = limit
			}

			for i := 0; i < displayCount; i++ {
				job := jobs[i]
				fmt.Printf("Job #%d:\n", i+1)
				fmt.Printf("  ID: %s\n", job.ID)
				fmt.Printf("  Name: %s\n", job.Name)
				fmt.Printf("  Status: %s\n", job.JobStatus.Status)
				fmt.Printf("  Created: %s\n", job.CreatedAt)
				fmt.Printf("  Owner: %s\n", job.Owner)
				if job.JobStatus.Content != "" {
					fmt.Printf("  Status Reason: %s\n", job.JobStatus.Content)
				}
				fmt.Println()
			}

			if limit > 0 && limit < len(jobs) {
				fmt.Printf("(Showing %d of %d jobs. Use --limit to change)\n", displayCount, len(jobs))
			}

			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "Maximum number of jobs to list")
	cmd.Flags().StringVarP(&status, "status", "s", "", "Filter by job status")

	return cmd
}
