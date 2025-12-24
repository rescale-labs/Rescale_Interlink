// Package cli provides job operation commands.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/progress"
	"github.com/rescale/rescale-int/internal/util/filter"
	"github.com/rescale/rescale-int/internal/pur/parser"
	"github.com/rescale/rescale-int/internal/transfer"
	"github.com/rescale/rescale-int/internal/validation"
)

// newJobsCmd creates the 'jobs' command group.
func newJobsCmd() *cobra.Command {
	jobsCmd := &cobra.Command{
		Use:   "jobs",
		Short: "Job operations (list, get, stop, tail, download, listfiles)",
		Long:  `Commands for managing jobs on the Rescale platform.`,
	}

	// Add job subcommands
	jobsCmd.AddCommand(newJobsListCmd())
	jobsCmd.AddCommand(newJobsGetCmd())
	jobsCmd.AddCommand(newJobsDeleteCmd())
	jobsCmd.AddCommand(newJobsSubmitCmd())
	jobsCmd.AddCommand(newJobsStopCmd())
	jobsCmd.AddCommand(newJobsTailCmd())
	jobsCmd.AddCommand(newJobsListFilesCmd())
	jobsCmd.AddCommand(newJobsDownloadCmd())

	return jobsCmd
}

// newJobsListCmd creates the 'jobs list' command.
func newJobsListCmd() *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all jobs",
		Long: `List all jobs associated with your account.

Example:
  # List all jobs
  rescale-int jobs list

  # List first 10 jobs
  rescale-int jobs list --limit 10`,
		RunE: func(cmd *cobra.Command, args []string) error {
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

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Limit number of jobs displayed (0 = all)")

	return cmd
}

// newJobsGetCmd creates the 'jobs get' command.
func newJobsGetCmd() *cobra.Command {
	var jobID string

	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get job details and status",
		Long: `Get detailed information about a specific job.

Example:
  rescale-int jobs get --job-id XxYyZz`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			if jobID == "" {
				return fmt.Errorf("--job-id is required")
			}

			// Get API client
			apiClient, err := getAPIClient()
			if err != nil {
				return err
			}

			ctx := GetContext()

			// Get job
			logger.Info().Str("job_id", jobID).Msg("Fetching job details")
			job, err := apiClient.GetJob(ctx, jobID)
			if err != nil {
				return fmt.Errorf("failed to get job: %w", err)
			}

			// Get job statuses to retrieve current status
			// Note: v3 API /jobs/{id}/ endpoint doesn't include status, so we need to fetch it separately
			statuses, err := apiClient.GetJobStatuses(ctx, jobID)
			if err != nil {
				return fmt.Errorf("failed to get job statuses: %w", err)
			}

			// Display job details
			fmt.Printf("Job Details:\n")
			fmt.Printf("  ID: %s\n", job.ID)
			fmt.Printf("  Name: %s\n", job.Name)

			// Get latest status from statuses array (API returns newest first)
			if len(statuses) > 0 {
				latestStatus := statuses[0]
				fmt.Printf("  Status: %s\n", latestStatus.Status)
				if latestStatus.StatusReason != "" {
					fmt.Printf("  Status Reason: %s\n", latestStatus.StatusReason)
				}
			} else {
				fmt.Printf("  Status: Unknown\n")
			}

			fmt.Printf("  Created: %s\n", job.CreatedAt)
			fmt.Printf("  Owner: %s\n", job.Owner)

			return nil
		},
	}

	cmd.Flags().StringVarP(&jobID, "job-id", "j", "", "Job ID (required)")
	cmd.Flags().StringVar(&jobID, "id", "", "Job ID (alias for --job-id)")
	cmd.MarkFlagRequired("job-id")

	return cmd
}

// newJobsDeleteCmd creates the 'jobs delete' command.
func newJobsDeleteCmd() *cobra.Command {
	var jobIDs []string
	var confirm bool

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete one or more jobs",
		Long: `Delete one or more jobs from the Rescale platform.

WARNING: This operation cannot be undone!

Example:
  # Delete single job
  rescale-int jobs delete --job-id XxYyZz

  # Delete multiple jobs (short form)
  rescale-int jobs delete -j XxYyZz -j AaBbCc -j DdEeFf`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			if len(jobIDs) == 0 {
				return fmt.Errorf("at least one --job-id is required")
			}

			// Confirmation prompt
			if !confirm {
				fmt.Printf("You are about to delete %d job(s). This cannot be undone.\n", len(jobIDs))
				for i, id := range jobIDs {
					fmt.Printf("  %d. %s\n", i+1, id)
				}
				fmt.Print("\nAre you sure? (yes/no): ")
				var response string
				fmt.Scanln(&response)
				if response != "yes" {
					fmt.Println("Deletion cancelled")
					return nil
				}
			}

			// Get API client
			apiClient, err := getAPIClient()
			if err != nil {
				return err
			}

			ctx := GetContext()

			// Delete jobs
			var successCount int
			var failures []string

			for _, jobID := range jobIDs {
				logger.Info().Str("job_id", jobID).Msg("Deleting job")
				if err := apiClient.DeleteJob(ctx, jobID); err != nil {
					logger.Error().Err(err).Str("job_id", jobID).Msg("Failed to delete job")
					failures = append(failures, fmt.Sprintf("%s: %v", jobID, err))
				} else {
					successCount++
					fmt.Printf("✓ Deleted job: %s\n", jobID)
				}
			}

			// Summary
			fmt.Printf("\n")
			if successCount > 0 {
				fmt.Printf("✓ Successfully deleted %d job(s)\n", successCount)
			}
			if len(failures) > 0 {
				fmt.Printf("❌ Failed to delete %d job(s):\n", len(failures))
				for _, failure := range failures {
					fmt.Printf("  - %s\n", failure)
				}
				return fmt.Errorf("%d deletion(s) failed", len(failures))
			}

			return nil
		},
	}

	cmd.Flags().StringArrayVarP(&jobIDs, "job-id", "j", []string{}, "Job ID to delete (can be specified multiple times, required)")
	cmd.Flags().StringArrayVar(&jobIDs, "id", []string{}, "Job ID to delete (alias for --job-id)")
	cmd.Flags().BoolVarP(&confirm, "confirm", "y", false, "Skip confirmation prompt")
	cmd.MarkFlagRequired("job-id")

	return cmd
}

// newJobsSubmitCmd creates the 'jobs submit' command.
func newJobsSubmitCmd() *cobra.Command {
	var jobFile string
	var scriptFile string
	var jobID string
	var inputFiles []string
	var automations []string // v3.6.1: Automation IDs to attach
	var createOnly bool
	var submitMode bool
	var endToEnd bool
	var autoDownload bool
	var noTar bool
	var maxConcurrent int

	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Create and/or submit jobs from JSON or SGE script",
		Long: `Create and/or submit jobs to the Rescale platform from either a JSON job specification
or an SGE-style script with #RESCALE_* metadata comments.

JSON Format:
  The job file should contain a complete job specification including job name,
  analysis configuration, hardware requirements, input files, and command.

SGE Script Format:
  Shell script with metadata in #RESCALE_* comments:
    #RESCALE_NAME my_job
    #RESCALE_COMMAND ./run.sh
    #RESCALE_ANALYSIS openfoam
    #RESCALE_ANALYSIS_VERSION 8.0
    #RESCALE_CORES emerald
    #RESCALE_CORES_PER_SLOT 16
    #RESCALE_SLOTS 2
    #RESCALE_WALLTIME 86400
    #RESCALE_TAGS simulation,cfd
    #RESCALE_PROJECT_ID proj_123

Workflow Modes:
  --create       Create job only (don't submit). Files are uploaded if --files specified.
  --submit       Create and submit job (or submit existing job if --job-id provided).
                 Files are uploaded if --files specified. This is the default mode.
  --end-to-end   Full workflow: upload files → create → submit → monitor → download.
                 Use --download to enable result download after completion.

File Uploads:
  The --files flag uploads input files with any workflow mode. Files are associated
  with the job automatically.

Examples:
  # Create job only (don't submit)
  rescale-int jobs submit --script run.sh --create

  # Create and submit job (default behavior)
  rescale-int jobs submit --script run.sh --submit
  rescale-int jobs submit --script run.sh  # same as above

  # Upload files and submit
  rescale-int jobs submit --script run.sh --files input.dat config.txt

  # Create job with files (don't submit yet)
  rescale-int jobs submit --script run.sh --files *.dat --create

  # Submit existing job by ID
  rescale-int jobs submit --job-id XxYyZz --submit

  # End-to-end with monitoring
  rescale-int jobs submit --script run.sh --files *.dat --end-to-end

  # End-to-end with auto-download
  rescale-int jobs submit --script run.sh --files *.dat --end-to-end --download`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			// Validate input: need either job spec OR job ID
			hasJobSpec := jobFile != "" || scriptFile != ""
			hasJobID := jobID != ""

			if !hasJobSpec && !hasJobID {
				return fmt.Errorf("either --job-file/--script OR --job-id is required")
			}
			if jobFile != "" && scriptFile != "" {
				return fmt.Errorf("cannot specify both --job-file and --script")
			}
			if hasJobSpec && hasJobID {
				return fmt.Errorf("cannot specify both job specification (--job-file/--script) and --job-id")
			}

			// Count workflow flags
			workflowFlagCount := 0
			if createOnly {
				workflowFlagCount++
			}
			if submitMode {
				workflowFlagCount++
			}
			if endToEnd {
				workflowFlagCount++
			}

			// Validate workflow flags
			if workflowFlagCount > 1 {
				return fmt.Errorf("only one of --create, --submit, or --end-to-end can be specified")
			}

			// Default to --submit if no workflow flag specified
			if workflowFlagCount == 0 {
				submitMode = true
			}

			// Validate flag combinations
			if autoDownload && !endToEnd {
				return fmt.Errorf("--download requires --end-to-end")
			}
			if createOnly && hasJobID {
				return fmt.Errorf("--create cannot be used with --job-id (job already exists)")
			}
			if endToEnd && hasJobID {
				return fmt.Errorf("--end-to-end cannot be used with --job-id (requires job creation)")
			}
			if hasJobID && len(inputFiles) > 0 {
				return fmt.Errorf("--files cannot be used with --job-id (files must be uploaded before job creation)")
			}
			if noTar && len(inputFiles) == 0 {
				logger.Warn().Msg("--no-tar has no effect without --files")
			}
			if maxConcurrent < constants.MinMaxConcurrent || maxConcurrent > constants.MaxMaxConcurrent {
				return fmt.Errorf("--max-concurrent must be between %d and %d, got %d",
					constants.MinMaxConcurrent, constants.MaxMaxConcurrent, maxConcurrent)
			}

			// Get API client
			apiClient, err := getAPIClient()
			if err != nil {
				return err
			}

			ctx := GetContext()

			// Handle submit existing job by ID
			if hasJobID {
				logger.Info().Str("job_id", jobID).Msg("Submitting existing job")
				if err := apiClient.SubmitJob(ctx, jobID); err != nil {
					return fmt.Errorf("failed to submit job: %w", err)
				}
				fmt.Printf("✓ Job submitted successfully\n")
				fmt.Printf("  Job ID: %s\n", jobID)
				return nil
			}

			// Parse job specification
			var jobReq *models.JobRequest

			if scriptFile != "" {
				// Parse SGE script
				logger.Info().Str("script", scriptFile).Msg("Parsing SGE script")

				sgeParser := parser.NewSGEParser()
				metadata, err := sgeParser.Parse(scriptFile)
				if err != nil {
					return fmt.Errorf("failed to parse SGE script: %w", err)
				}

				// Display parsed metadata
				fmt.Println("Parsed SGE script metadata:")
				fmt.Println(strings.Repeat("-", 60))
				fmt.Print(metadata.String())
				fmt.Println(strings.Repeat("-", 60))

				// Convert to job request
				jobReq = metadata.ToJobRequest()
			} else {
				// Read JSON file
				fileData, err := os.ReadFile(jobFile)
				if err != nil {
					return fmt.Errorf("failed to read job file: %w", err)
				}

				// Parse job request
				var req models.JobRequest
				if err := json.Unmarshal(fileData, &req); err != nil {
					return fmt.Errorf("failed to parse job file: %w", err)
				}
				jobReq = &req
			}

			// Resolve analysis version names to version codes
			// The API expects versionCode (like "0") not version name (like "CPU")
			for i := range jobReq.JobAnalyses {
				if jobReq.JobAnalyses[i].Analysis.Version != "" {
					resolved := resolveAnalysisVersion(ctx, apiClient, jobReq.JobAnalyses[i].Analysis.Code, jobReq.JobAnalyses[i].Analysis.Version)
					if resolved != jobReq.JobAnalyses[i].Analysis.Version {
						logger.Debug().
							Str("code", jobReq.JobAnalyses[i].Analysis.Code).
							Str("from", jobReq.JobAnalyses[i].Analysis.Version).
							Str("to", resolved).
							Msg("Resolved analysis version")
					}
					jobReq.JobAnalyses[i].Analysis.Version = resolved
				}
			}

			// v3.6.1: Add CLI-specified automations to job request
			if len(automations) > 0 {
				for _, autoID := range automations {
					jobReq.JobAutomations = append(jobReq.JobAutomations, models.JobAutomationRequest{
						AutomationID: autoID,
					})
				}
				logger.Info().Strs("automations", automations).Msg("Attaching automations to job")
			}

			// Route to appropriate workflow
			if endToEnd {
				return runEndToEndJobWorkflow(ctx, jobReq, inputFiles, autoDownload, noTar, maxConcurrent, apiClient, logger)
			} else if createOnly {
				return runCreateOnlyWorkflow(ctx, jobReq, inputFiles, noTar, maxConcurrent, apiClient, logger)
			} else {
				// submitMode (default)
				return runSubmitWorkflow(ctx, jobReq, inputFiles, noTar, maxConcurrent, apiClient, logger)
			}
		},
	}

	cmd.Flags().StringVarP(&jobFile, "job-file", "f", "", "Path to job specification JSON file")
	cmd.Flags().StringVarP(&scriptFile, "script", "s", "", "Path to SGE-style script with #RESCALE_* metadata")
	cmd.Flags().StringVarP(&jobID, "job-id", "j", "", "Existing job ID to submit (use with --submit only)")
	cmd.Flags().StringSliceVar(&inputFiles, "files", nil, "Input files to upload (supports glob patterns)")
	cmd.Flags().BoolVar(&createOnly, "create", false, "Create job only (don't submit)")
	cmd.Flags().BoolVar(&submitMode, "submit", false, "Create and submit job (default behavior)")
	cmd.Flags().BoolVarP(&endToEnd, "end-to-end", "E", false, "Full workflow: upload → create → submit → monitor → download")
	cmd.Flags().BoolVar(&autoDownload, "download", false, "Auto-download results after job completes (requires --end-to-end)")
	cmd.Flags().BoolVar(&noTar, "no-tar", false, "Skip tarball creation for single file uploads")
	cmd.Flags().IntVarP(&maxConcurrent, "max-concurrent", "m", constants.DefaultMaxConcurrent,
		fmt.Sprintf("Maximum concurrent file uploads (%d-%d)", constants.MinMaxConcurrent, constants.MaxMaxConcurrent))
	cmd.Flags().StringSliceVar(&automations, "automation", nil, "Automation ID(s) to attach (can specify multiple)")

	return cmd
}

// newJobsStopCmd creates the 'jobs stop' command.
func newJobsStopCmd() *cobra.Command {
	var jobID string
	var confirm bool

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop a running job",
		Long: `Stop a running or queued job.

WARNING: This operation cannot be undone!

Example:
  rescale-int jobs stop --job-id XxYyZz`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			if jobID == "" {
				return fmt.Errorf("--job-id is required")
			}

			// Confirmation prompt
			if !confirm {
				fmt.Printf("You are about to stop job %s. This cannot be undone.\n", jobID)
				fmt.Print("Are you sure? (yes/no): ")
				var response string
				fmt.Scanln(&response)
				if response != "yes" {
					fmt.Println("Stop cancelled")
					return nil
				}
			}

			// Get API client
			apiClient, err := getAPIClient()
			if err != nil {
				return err
			}

			ctx := GetContext()

			// Stop job
			logger.Info().Str("job_id", jobID).Msg("Stopping job")
			if err := apiClient.StopJob(ctx, jobID); err != nil {
				return fmt.Errorf("failed to stop job: %w", err)
			}

			fmt.Printf("✓ Job stop request sent successfully\n")
			fmt.Printf("  Job ID: %s\n", jobID)
			fmt.Println("\nNote: It may take a few moments for the job to fully stop.")

			return nil
		},
	}

	cmd.Flags().StringVarP(&jobID, "job-id", "j", "", "Job ID (required)")
	cmd.Flags().BoolVarP(&confirm, "confirm", "y", false, "Skip confirmation prompt")
	cmd.MarkFlagRequired("job-id")

	return cmd
}

// newJobsTailCmd creates the 'jobs tail' command for real-time log monitoring.
func newJobsTailCmd() *cobra.Command {
	var jobID string
	var pollInterval int

	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Tail job status logs in real-time",
		Long: `Monitor job status changes in real-time.

This command polls the job status at regular intervals and displays updates.
Press Ctrl+C to stop monitoring.

Example:
  # Monitor job with 5-second polling
  rescale-int jobs tail --job-id XxYyZz --interval 5`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			if jobID == "" {
				return fmt.Errorf("--job-id is required")
			}

			// Get API client
			apiClient, err := getAPIClient()
			if err != nil {
				return err
			}

			ctx := GetContext()

			fmt.Printf("Monitoring job %s (polling every %d seconds, Ctrl+C to stop)...\n\n", jobID, pollInterval)

			// Track last status to avoid duplicate prints
			var lastStatus string
			var lastDate string

			ticker := time.NewTicker(time.Duration(pollInterval) * time.Second)
			defer ticker.Stop()

			// Poll immediately on start
			statuses, err := apiClient.GetJobStatuses(ctx, jobID)
			if err != nil {
				return fmt.Errorf("failed to get job statuses: %w", err)
			}

			if len(statuses) > 0 {
				latest := statuses[len(statuses)-1]
				lastStatus = latest.Status
				lastDate = latest.StatusDate
				fmt.Printf("[%s] %s", latest.StatusDate, latest.Status)
				if latest.StatusReason != "" {
					fmt.Printf(" - %s", latest.StatusReason)
				}
				fmt.Println()
			}

			// Poll for updates
			for range ticker.C {
				statuses, err := apiClient.GetJobStatuses(ctx, jobID)
				if err != nil {
					logger.Error().Err(err).Msg("Failed to get job statuses")
					continue
				}

				if len(statuses) > 0 {
					latest := statuses[len(statuses)-1]

					// Only print if status or date changed
					if latest.Status != lastStatus || latest.StatusDate != lastDate {
						lastStatus = latest.Status
						lastDate = latest.StatusDate

						fmt.Printf("[%s] %s", latest.StatusDate, latest.Status)
						if latest.StatusReason != "" {
							fmt.Printf(" - %s", latest.StatusReason)
						}
						fmt.Println()

						// Stop monitoring if job reached terminal state
						if latest.Status == "Completed" || latest.Status == "Failed" {
							fmt.Printf("\nJob reached terminal state: %s\n", latest.Status)
							return nil
						}
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&jobID, "job-id", "j", "", "Job ID (required)")
	cmd.Flags().IntVarP(&pollInterval, "interval", "i", 10, "Polling interval in seconds")
	cmd.MarkFlagRequired("job-id")

	return cmd
}

// newJobsListFilesCmd creates the 'jobs listfiles' command.
func newJobsListFilesCmd() *cobra.Command {
	var jobID string

	cmd := &cobra.Command{
		Use:   "listfiles",
		Short: "List output files for a job",
		Long: `List all output files generated by a job.

Example:
  rescale-int jobs listfiles --job-id XxYyZz`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			if jobID == "" {
				return fmt.Errorf("--job-id is required")
			}

			// Get API client
			apiClient, err := getAPIClient()
			if err != nil {
				return err
			}

			ctx := GetContext()

			// List job files
			fmt.Printf("Fetching output files for job %s...\n", jobID)
			logger.Info().Str("job_id", jobID).Msg("Fetching job files")
			files, err := apiClient.ListJobFiles(ctx, jobID)
			if err != nil {
				return fmt.Errorf("failed to list job files: %w", err)
			}

			if len(files) == 0 {
				fmt.Println("No output files found for this job")
				return nil
			}

			// Display files
			fmt.Printf("Output files for job %s:\n\n", jobID)

			for i, file := range files {
				sizeMB := float64(file.DecryptedSize) / (1024 * 1024)
				fmt.Printf("File #%d:\n", i+1)
				fmt.Printf("  ID: %s\n", file.ID)
				fmt.Printf("  Name: %s\n", file.Name)
				fmt.Printf("  Size: %.2f MB\n", sizeMB)
				fmt.Printf("  Uploaded: %s\n", file.DateUploaded)
				if file.RelativePath != "" {
					fmt.Printf("  Path: %s\n", file.RelativePath)
				}
				fmt.Println()
			}

			fmt.Printf("Total: %d file(s)\n", len(files))

			return nil
		},
	}

	cmd.Flags().StringVarP(&jobID, "job-id", "j", "", "Job ID (required)")
	cmd.MarkFlagRequired("job-id")

	return cmd
}

// newJobsDownloadCmd creates the 'jobs download' command.
func newJobsDownloadCmd() *cobra.Command {
	var jobID string
	var fileID string
	var outputPath string
	var outputDir string
	var maxConcurrent int
	var overwriteAll bool
	var skipAll bool
	var resumeAll bool
	var skipChecksum bool
	var filterPatterns string
	var excludePatterns string
	var searchTerms string
	var pathFilterPatterns string

	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download job output files",
		Long: `Download job output files with concurrent download support and filtering.

MODE 1: Download all job output files (default)
  Download all output files from a job to a directory, preserving relative paths.
  Supports concurrent downloads, filtering, and conflict resolution.

MODE 2: Download specific file
  Download a single file by file ID.

Examples:
  # Download all job output files to directory (concurrent)
  rescale-int jobs download -j XxYyZz -d ./results

  # Download with high concurrency
  rescale-int jobs download -j XxYyZz --max-concurrent 10

  # Download only specific file types
  rescale-int jobs download -j XxYyZz --filter "*.dat,*.log"

  # Download all except debug files
  rescale-int jobs download -j XxYyZz --exclude "debug*,temp*"

  # Download files containing "results" in filename
  rescale-int jobs download -j XxYyZz --search "results"

  # Combined filters (include .dat files, exclude debug files, must contain "final")
  rescale-int jobs download -j XxYyZz --filter "*.dat" --exclude "debug*" --search "final"

  # Download files from specific folder paths (--path-filter)
  rescale-int jobs download -j XxYyZz --path-filter "run_1/*.dat"
  rescale-int jobs download -j XxYyZz --path-filter "run_*/output/*"
  rescale-int jobs download -j XxYyZz --path-filter "**/results/*.csv"

  # Download all files, overwriting existing
  rescale-int jobs download --job-id XxYyZz --overwrite

  # Download specific file by ID
  rescale-int jobs download --job-id XxYyZz --file-id AbCdEf --output /path/to/file.dat`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			if jobID == "" {
				return fmt.Errorf("--job-id is required")
			}

			// Get API client
			apiClient, err := getAPIClient()
			if err != nil {
				return err
			}

			ctx := GetContext()

			// MODE 1: Download all files if no specific file ID provided
			if fileID == "" {
				// Validate conflict flags (only one can be set)
				conflictFlags := 0
				if overwriteAll {
					conflictFlags++
				}
				if skipAll {
					conflictFlags++
				}
				if resumeAll {
					conflictFlags++
				}
				if conflictFlags > 1 {
					return fmt.Errorf("only one of --overwrite, --skip, or --resume can be specified")
				}

				// Determine output directory
				if outputDir == "" {
					outputDir = "."
				}

				// Parse filter patterns
				filterList := filter.ParsePatternList(filterPatterns)
				excludeList := filter.ParsePatternList(excludePatterns)
				searchList := filter.ParsePatternList(searchTerms)
				pathFilterList := filter.ParsePatternList(pathFilterPatterns)

				// Use helper function for symmetry with files download
				return executeJobDownload(ctx, jobID, outputDir, maxConcurrent, overwriteAll, skipAll, resumeAll, skipChecksum, filterList, excludeList, searchList, pathFilterList, apiClient, logger)
			}

			// MODE 2: Download specific file
			logger.Info().Str("file_id", fileID).Msg("Downloading specific file")

			// Get file info
			fileInfo, err := apiClient.GetFileInfo(ctx, fileID)
			if err != nil {
				return fmt.Errorf("failed to get file info: %w", err)
			}

			// Validate filename from API to prevent path traversal
			if err := validation.ValidateFilename(fileInfo.Name); err != nil {
				return fmt.Errorf("invalid filename from API for file %s: %w", fileID, err)
			}

			// Determine output path
			if outputPath == "" {
				outputPath = filepath.Join(".", fileInfo.Name)
			}

			// Ensure output directory exists
			outDir := filepath.Dir(outputPath)
			if err := os.MkdirAll(outDir, 0755); err != nil {
				return fmt.Errorf("failed to create output directory: %w", err)
			}

			fmt.Printf("Downloading file: %s\n", fileInfo.Name)
			fmt.Printf("  Size: %.2f MB\n", float64(fileInfo.DecryptedSize)/(1024*1024))
			fmt.Printf("  Output: %s\n\n", outputPath)

			// Use modern download infrastructure with DownloadUI
			downloadUI := progress.NewDownloadUI(1)

			// NOTE: Do NOT redirect zerolog through downloadUI.Writer()
			// Zerolog outputs JSON which causes "invalid character '\x1b'" errors

			defer downloadUI.Wait()

			// Create resource manager and transfer manager
			resourceMgr := CreateResourceManager()
			transferMgr := transfer.NewManager(resourceMgr)

			// Allocate transfer handle
			transferHandle := transferMgr.AllocateTransfer(fileInfo.DecryptedSize, 1)

			// Print thread info if multi-threaded
			if transferHandle.GetThreads() > 1 && fileInfo.DecryptedSize > 100*1024*1024 {
				fmt.Fprintf(downloadUI.Writer(), "Using %d concurrent threads\n", transferHandle.GetThreads())
			}

			// Create progress bar
			var fileBar *progress.DownloadFileBar
			var barOnce sync.Once

			// Download file with progress tracking and transfer manager
			// Use strict checksum verification (skipChecksum=false) for job downloads
			err = download.DownloadFile(ctx, download.DownloadParams{
				FileID:    fileID,
				LocalPath: outputPath,
				APIClient: apiClient,
				ProgressCallback: func(fraction float64) {
					// Create progress bar on first progress update
					barOnce.Do(func() {
						fileBar = downloadUI.AddFileBar(1, fileID, fileInfo.Name, outputPath, fileInfo.DecryptedSize)
					})
					if fileBar != nil {
						fileBar.UpdateProgress(fraction)
					}
				},
				TransferHandle: transferHandle,
				SkipChecksum:   false,
			})

			// Ensure progress bar exists before completing
			if fileBar == nil {
				fileBar = downloadUI.AddFileBar(1, fileID, fileInfo.Name, outputPath, fileInfo.DecryptedSize)
			}

			if err != nil {
				fileBar.Complete(err)
				return fmt.Errorf("failed to download file: %w", err)
			}

			fileBar.Complete(nil)

			fmt.Printf("\n✓ File downloaded successfully\n")
			fmt.Printf("  Path: %s\n", outputPath)

			return nil
		},
	}

	cmd.Flags().StringVarP(&jobID, "job-id", "j", "", "Job ID (required)")
	cmd.Flags().StringVar(&jobID, "id", "", "Job ID (alias for --job-id)")
	cmd.Flags().StringVar(&fileID, "file-id", "", "Specific file ID to download (optional, downloads all files if not specified)")
	cmd.Flags().StringVarP(&outputDir, "outdir", "d", "", "Output directory for batch download (default: current directory)")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output file path for single file download")
	cmd.Flags().IntVarP(&maxConcurrent, "max-concurrent", "m", constants.DefaultMaxConcurrent,
		fmt.Sprintf("Maximum concurrent downloads (%d-%d)", constants.MinMaxConcurrent, constants.MaxMaxConcurrent))
	cmd.Flags().BoolVarP(&overwriteAll, "overwrite", "w", false, "Overwrite existing files without prompting")
	cmd.Flags().BoolVarP(&skipAll, "skip", "S", false, "Skip existing files without prompting")
	cmd.Flags().BoolVarP(&resumeAll, "resume", "r", false, "Resume interrupted downloads without prompting")
	cmd.Flags().BoolVar(&skipChecksum, "skip-checksum", false, "Skip checksum verification (not recommended, allows corrupted downloads)")
	cmd.Flags().StringVar(&filterPatterns, "filter", "", "Include only files matching these patterns (comma-separated glob patterns, e.g. \"*.dat,*.log\")")
	cmd.Flags().StringVarP(&excludePatterns, "exclude", "x", "", "Exclude files matching these patterns (comma-separated glob patterns, e.g. \"debug*,temp*\")")
	cmd.Flags().StringVarP(&searchTerms, "search", "s", "", "Include only files containing these terms in filename (comma-separated, case-insensitive)")
	cmd.Flags().StringVar(&pathFilterPatterns, "path-filter", "", "Include only files matching these path patterns (supports ** for recursive matching, e.g. \"run_1/*.dat\" or \"**/results/*\")")

	cmd.MarkFlagRequired("job-id")

	return cmd
}

// runCreateOnlyWorkflow handles creating a job without submitting
func runCreateOnlyWorkflow(
	ctx context.Context,
	jobReq *models.JobRequest,
	inputFiles []string,
	noTar bool,
	maxConcurrent int,
	apiClient *api.Client,
	logger *logging.Logger,
) error {
	// Upload files if specified
	var uploadedFileIDs []string
	if len(inputFiles) > 0 {
		logger.Info().Int("count", len(inputFiles)).Msg("Uploading input files")
		fileIDs, err := UploadFilesWithIDs(ctx, inputFiles, "", maxConcurrent, false, apiClient, logger, false)
		if err != nil {
			return fmt.Errorf("file upload failed: %w", err)
		}
		uploadedFileIDs = fileIDs

		// Associate files with job
		if len(jobReq.JobAnalyses) > 0 {
			inputFileRequests := make([]models.InputFileRequest, len(uploadedFileIDs))
			for i, fileID := range uploadedFileIDs {
				inputFileRequests[i] = models.InputFileRequest{ID: fileID}
			}
			jobReq.JobAnalyses[0].InputFiles = inputFileRequests
			logger.Info().Int("count", len(uploadedFileIDs)).Msg("Associated files with job")
		}
		fmt.Printf("\n✓ Uploaded %d file(s)\n\n", len(uploadedFileIDs))
	}

	// Create job (don't submit)
	logger.Info().Str("name", jobReq.Name).Msg("Creating job")
	jobResp, err := apiClient.CreateJob(ctx, *jobReq)
	if err != nil {
		return fmt.Errorf("failed to create job: %w", err)
	}

	fmt.Printf("✓ Job created (not submitted)\n")
	fmt.Printf("  Job ID: %s\n", jobResp.ID)
	fmt.Printf("  Name: %s\n", jobResp.Name)
	fmt.Printf("\nTo submit this job later, run:\n")
	fmt.Printf("  rescale-int jobs submit --job-id %s\n", jobResp.ID)

	return nil
}

// runSubmitWorkflow handles creating and submitting a job
func runSubmitWorkflow(
	ctx context.Context,
	jobReq *models.JobRequest,
	inputFiles []string,
	noTar bool,
	maxConcurrent int,
	apiClient *api.Client,
	logger *logging.Logger,
) error {
	// Upload files if specified
	var uploadedFileIDs []string
	if len(inputFiles) > 0 {
		logger.Info().Int("count", len(inputFiles)).Msg("Uploading input files")
		fileIDs, err := UploadFilesWithIDs(ctx, inputFiles, "", maxConcurrent, false, apiClient, logger, false)
		if err != nil {
			return fmt.Errorf("file upload failed: %w", err)
		}
		uploadedFileIDs = fileIDs

		// Associate files with job
		if len(jobReq.JobAnalyses) > 0 {
			inputFileRequests := make([]models.InputFileRequest, len(uploadedFileIDs))
			for i, fileID := range uploadedFileIDs {
				inputFileRequests[i] = models.InputFileRequest{ID: fileID}
			}
			jobReq.JobAnalyses[0].InputFiles = inputFileRequests
			logger.Info().Int("count", len(uploadedFileIDs)).Msg("Associated files with job")
		}
		fmt.Printf("\n✓ Uploaded %d file(s)\n\n", len(uploadedFileIDs))
	}

	// Create job
	logger.Info().Str("name", jobReq.Name).Msg("Creating job")
	jobResp, err := apiClient.CreateJob(ctx, *jobReq)
	if err != nil {
		return fmt.Errorf("failed to create job: %w", err)
	}

	fmt.Printf("✓ Job created: %s\n", jobResp.ID)
	fmt.Printf("  Name: %s\n", jobResp.Name)

	// Submit job automatically
	logger.Info().Str("job_id", jobResp.ID).Msg("Submitting job")
	if err := apiClient.SubmitJob(ctx, jobResp.ID); err != nil {
		return fmt.Errorf("failed to submit job: %w", err)
	}

	fmt.Printf("✓ Job submitted successfully\n")
	fmt.Printf("  Job ID: %s\n", jobResp.ID)

	return nil
}

// runEndToEndJobWorkflow handles the complete end-to-end workflow
func runEndToEndJobWorkflow(
	ctx context.Context,
	jobReq *models.JobRequest,
	inputFiles []string,
	autoDownload bool,
	noTar bool,
	maxConcurrent int,
	apiClient *api.Client,
	logger *logging.Logger,
) error {
	fmt.Println("\n" + strings.Repeat("=", 70))
	fmt.Println("  END-TO-END JOB WORKFLOW")
	fmt.Println(strings.Repeat("=", 70))

	// Step 1: Upload input files if specified
	var uploadedFileIDs []string
	if len(inputFiles) > 0 {
		fmt.Println("\n[1/4] Uploading input files...")
		fmt.Println(strings.Repeat("-", 70))

		fileIDs, err := UploadFilesWithIDs(ctx, inputFiles, "", maxConcurrent, false, apiClient, logger, false)
		if err != nil {
			return fmt.Errorf("file upload failed: %w", err)
		}
		uploadedFileIDs = fileIDs

		// Associate files with job
		if len(jobReq.JobAnalyses) > 0 {
			inputFileRequests := make([]models.InputFileRequest, len(uploadedFileIDs))
			for i, fileID := range uploadedFileIDs {
				inputFileRequests[i] = models.InputFileRequest{ID: fileID}
			}
			jobReq.JobAnalyses[0].InputFiles = inputFileRequests
			logger.Info().Int("count", len(uploadedFileIDs)).Msg("Associated files with job")
		}
	} else {
		fmt.Println("\n[1/4] No input files to upload (skipped)")
	}

	// Step 2: Create and submit job
	fmt.Println("\n[2/4] Creating and submitting job...")
	fmt.Println(strings.Repeat("-", 70))

	logger.Info().Str("name", jobReq.Name).Msg("Creating job")
	jobResp, err := apiClient.CreateJob(ctx, *jobReq)
	if err != nil {
		return fmt.Errorf("failed to create job: %w", err)
	}

	fmt.Printf("✓ Job created: %s\n", jobResp.ID)
	fmt.Printf("  Name: %s\n", jobResp.Name)

	logger.Info().Str("job_id", jobResp.ID).Msg("Submitting job")
	if err := apiClient.SubmitJob(ctx, jobResp.ID); err != nil {
		return fmt.Errorf("failed to submit job: %w", err)
	}

	fmt.Printf("✓ Job submitted successfully\n")

	// Step 3: Monitor job status
	fmt.Println("\n[3/4] Monitoring job status...")
	fmt.Println(strings.Repeat("-", 70))
	fmt.Println("Press Ctrl+C to stop monitoring (job will continue running)")

	if err := monitorJobUntilComplete(ctx, jobResp.ID, apiClient, logger); err != nil {
		logger.Warn().Err(err).Msg("Monitoring stopped")
		fmt.Printf("\n⚠️  Monitoring stopped: %v\n", err)
		fmt.Printf("Job ID: %s (use 'rescale-int jobs get --id %s' to check status)\n", jobResp.ID, jobResp.ID)
		return nil // Not a fatal error - job is still running
	}

	// Step 4: Download results if requested
	if autoDownload {
		fmt.Println("\n[4/4] Downloading job results...")
		fmt.Println(strings.Repeat("-", 70))

		if err := downloadJobResults(ctx, jobResp.ID, apiClient, logger); err != nil {
			return fmt.Errorf("failed to download results: %w", err)
		}
	} else {
		fmt.Println("\n[4/4] Download skipped (use --download to auto-download results)")
		fmt.Printf("\nTo download results manually:\n")
		fmt.Printf("  rescale-int jobs download --id %s\n", jobResp.ID)
	}

	fmt.Println("\n" + strings.Repeat("=", 70))
	fmt.Println("  END-TO-END WORKFLOW COMPLETE")
	fmt.Println(strings.Repeat("=", 70))
	fmt.Printf("\nJob ID: %s\n", jobResp.ID)

	return nil
}

// monitorJobUntilComplete monitors job status with live updates until completion
func monitorJobUntilComplete(ctx context.Context, jobID string, apiClient *api.Client, logger *logging.Logger) error {
	lastStatus := ""
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	consecutiveErrors := 0
	lastProgressMsg := time.Now()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// Show periodic "still monitoring" message
			if time.Since(lastProgressMsg) > 30*time.Second {
				fmt.Printf("⏳ Still monitoring job %s...\n", jobID)
				lastProgressMsg = time.Now()
			}

			// Add per-request timeout
			// Note: GetJob() doesn't return jobStatus - use GetJobStatuses() instead
			// See models/job.go comment: "The /jobs/{id}/ GET endpoint does NOT include jobStatus"
			reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			statuses, err := apiClient.GetJobStatuses(reqCtx, jobID)
			cancel()

			if err != nil {
				consecutiveErrors++
				logger.Warn().Err(err).Int("consecutiveErrors", consecutiveErrors).
					Msg("Failed to get job status")

				// Abort after too many consecutive errors
				if consecutiveErrors >= 5 {
					return fmt.Errorf("failed to get job status after %d attempts: %w",
						consecutiveErrors, err)
				}
				continue // Retry
			}

			consecutiveErrors = 0 // Reset on success

			// Statuses are returned newest-first; first entry is current status
			if len(statuses) == 0 {
				logger.Warn().Msg("No status entries returned for job")
				continue
			}
			currentStatus := statuses[0].Status
			if currentStatus != lastStatus {
				// Status changed, show update
				timestamp := time.Now().Format("15:04:05")
				fmt.Printf("[%s] Status: %s\n", timestamp, currentStatus)
				lastStatus = currentStatus
			}

			// Check if job is complete
			switch currentStatus {
			case "Completed":
				fmt.Printf("\n✓ Job completed successfully\n")
				return nil
			case "Failed", "Terminated":
				fmt.Printf("\n✗ Job ended with status: %s\n", currentStatus)
				return fmt.Errorf("job ended with status: %s", currentStatus)
			}
		}
	}
}

// resolveAnalysisVersion resolves a version name (like "CPU") to its versionCode (like "0").
// The Rescale API accepts versionCode in the "version" field for job creation.
// This function queries the API to look up the correct versionCode.
// If the version is already a valid versionCode or if resolution fails, returns the original value.
func resolveAnalysisVersion(ctx context.Context, apiClient *api.Client, analysisCode, versionInput string) string {
	if versionInput == "" {
		return versionInput
	}

	// Fetch analyses from API
	analyses, err := apiClient.GetAnalyses(ctx)
	if err != nil {
		// If we can't fetch analyses, return the original value and let the API handle it
		return versionInput
	}

	// Find the matching analysis by code
	for _, analysis := range analyses {
		if analysis.Code == analysisCode {
			// Search versions for a match
			for _, v := range analysis.Versions {
				// Match by version name (e.g., "CPU")
				if v.Version == versionInput {
					// Return versionCode if available, otherwise keep the version name
					if v.VersionCode != "" {
						return v.VersionCode
					}
					return versionInput
				}
				// Also match by versionCode directly (in case user already used the correct format)
				if v.VersionCode == versionInput {
					return versionInput // Already correct
				}
			}
			break // Found the analysis, no need to continue
		}
	}

	// No match found, return original value
	return versionInput
}

// downloadJobResults downloads all output files from a completed job
// Uses the same modern infrastructure as the jobs download command
func downloadJobResults(ctx context.Context, jobID string, apiClient *api.Client, logger *logging.Logger) error {
	fmt.Println()
	fmt.Println("======================================================================")
	fmt.Println("  DOWNLOADING JOB RESULTS")
	fmt.Println("======================================================================")
	fmt.Println()

	// Create output directory: job_<jobID>_results/
	outputDir := fmt.Sprintf("job_%s_results", jobID)

	// Use the same helper as jobs download command for consistency
	// This provides concurrent downloads, modern progress UI, and fixes zerolog warnings
	// No filters for E2E workflow - download all files
	// Use strict checksum verification (skipChecksum=false)
	return executeJobDownload(ctx, jobID, outputDir, 5, false, false, false, false, nil, nil, nil, nil, apiClient, logger)
}
