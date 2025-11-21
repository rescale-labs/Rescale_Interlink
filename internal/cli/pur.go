// Package cli provides PUR (Parallel Uploader and Runner) commands.
package cli

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/pipeline"
	"github.com/rescale/rescale-int/internal/pur/validation"
)

// newPURCmd creates the 'pur' command group.
func newPURCmd() *cobra.Command {
	purCmd := &cobra.Command{
		Use:   "pur",
		Short: "PUR - Parallel Uploader and Runner for Rescale",
		Long:  `PUR (Parallel Uploader and Runner) commands for batch job submission.`,
	}

	// Add PUR subcommands
	purCmd.AddCommand(newMakeDirsCSVCmd())
	purCmd.AddCommand(newPlanCmd())
	purCmd.AddCommand(newRunCmd())
	purCmd.AddCommand(newResumeCmd())
	purCmd.AddCommand(newSubmitExistingCmd())

	return purCmd
}

// newMakeDirsCSVCmd creates the 'make-dirs-csv' command.
func newMakeDirsCSVCmd() *cobra.Command {
	var templatePath string
	var outputPath string
	var pattern string
	var overwrite bool

	cmd := &cobra.Command{
		Use:   "make-dirs-csv",
		Short: "Generate jobs CSV from directory pattern",
		Long: `Generate a jobs CSV file by scanning directories matching a pattern.

This command scans for directories matching the pattern and creates a jobs CSV
file with one job per directory, using the template CSV as a base.

Example:
  rescale-int pur make-dirs-csv --template template.csv --output jobs.csv --pattern "Run_*"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			if templatePath == "" {
				return fmt.Errorf("--template is required")
			}
			if outputPath == "" {
				return fmt.Errorf("--output is required")
			}
			if pattern == "" {
				return fmt.Errorf("--pattern is required")
			}

			// Check if output exists
			if !overwrite {
				if _, err := os.Stat(outputPath); err == nil {
					return fmt.Errorf("output file %s already exists (use --overwrite to replace)", outputPath)
				}
			}

			logger.Info().
				Str("template", templatePath).
				Str("output", outputPath).
				Str("pattern", pattern).
				Msg("Generating jobs CSV from directories")

			// Load template
			templateJobs, err := config.LoadJobsCSV(templatePath)
			if err != nil {
				return fmt.Errorf("failed to load template: %w", err)
			}

			if len(templateJobs) == 0 {
				return fmt.Errorf("template CSV is empty")
			}

			// Use first row as template
			template := templateJobs[0]

			// Find matching directories
			baseDir := filepath.Dir(pattern)
			if baseDir == "." {
				baseDir, _ = os.Getwd()
			}
			patternName := filepath.Base(pattern)

			entries, err := os.ReadDir(baseDir)
			if err != nil {
				return fmt.Errorf("failed to read directory %s: %w", baseDir, err)
			}

			var jobs []models.JobSpec
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}

				matched, err := filepath.Match(patternName, entry.Name())
				if err != nil {
					logger.Warn().Str("dir", entry.Name()).Err(err).Msg("Pattern match error")
					continue
				}

				if matched {
					// Create job from template
					job := template
					job.JobName = entry.Name()
					job.Directory = filepath.Join(baseDir, entry.Name())
					jobs = append(jobs, job)
					logger.Info().Str("dir", entry.Name()).Msg("Added job")
				}
			}

			if len(jobs) == 0 {
				return fmt.Errorf("no directories matched pattern: %s", pattern)
			}

			// Save jobs CSV
			if err := config.SaveJobsCSV(outputPath, jobs); err != nil {
				return fmt.Errorf("failed to save jobs CSV: %w", err)
			}

			logger.Info().
				Int("count", len(jobs)).
				Str("output", outputPath).
				Msg("Jobs CSV generated successfully")

			fmt.Printf("✓ Generated %d jobs in %s\n", len(jobs), outputPath)
			return nil
		},
	}

	cmd.Flags().StringVarP(&templatePath, "template", "t", "", "Template CSV file (required)")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output jobs CSV file (required)")
	cmd.Flags().StringVarP(&pattern, "pattern", "p", "", "Directory pattern, e.g., 'Run_*' (required)")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "Overwrite existing output file")

	cmd.MarkFlagRequired("template")
	cmd.MarkFlagRequired("output")
	cmd.MarkFlagRequired("pattern")

	return cmd
}

// newPlanCmd creates the 'plan' command.
func newPlanCmd() *cobra.Command {
	var jobsCSV string
	var validateCoretype bool

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Plan and validate job pipeline",
		Long: `Analyze and validate the job pipeline without executing it.

Example:
  rescale-int pur plan --jobs-csv jobs.csv --validate-coretype`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			if jobsCSV == "" {
				return fmt.Errorf("--jobs-csv is required")
			}

			logger.Info().Str("jobs", jobsCSV).Msg("Planning job pipeline")

			// Load config
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Load jobs
			jobs, err := config.LoadJobsCSV(jobsCSV)
			if err != nil {
				return fmt.Errorf("failed to load jobs CSV: %w", err)
			}

			logger.Info().Int("count", len(jobs)).Msg("Loaded jobs")

			// Validate core types if requested
			if validateCoretype {
				apiClient, err := api.NewClient(cfg)
				if err != nil {
					return fmt.Errorf("failed to create API client: %w", err)
				}

				validator := validation.NewCoreTypeValidator(apiClient)
				ctx := GetContext()

				// Fetch available core types
				if err := validator.FetchCoreTypes(ctx); err != nil {
					return fmt.Errorf("failed to fetch core types: %w", err)
				}

				// Validate each job's core type
				for i, job := range jobs {
					if err := validator.Validate(job.CoreType); err != nil {
						return fmt.Errorf("job %s (index %d): %w", job.JobName, i+1, err)
					}
					logger.Info().
						Int("index", i+1).
						Str("name", job.JobName).
						Str("coretype", job.CoreType).
						Msg("Core type validated")
				}
			}

			// Basic validation
			for i, job := range jobs {
				// Check directory exists
				if _, err := os.Stat(job.Directory); os.IsNotExist(err) {
					logger.Warn().
						Str("job", job.JobName).
						Str("dir", job.Directory).
						Msg("Directory does not exist")
				}

				fmt.Printf("[%d/%d] ✓ %s\n", i+1, len(jobs), job.JobName)
			}

			logger.Info().Msg("All jobs validated successfully")
			fmt.Println("\n✓ Pipeline plan is valid")
			return nil
		},
	}

	cmd.Flags().StringVarP(&jobsCSV, "jobs-csv", "j", "", "Jobs CSV file (required)")
	cmd.Flags().BoolVar(&validateCoretype, "validate-coretype", false, "Validate core type with Rescale API")

	cmd.MarkFlagRequired("jobs-csv")

	return cmd
}

// newRunCmd creates the 'run' command.
func newRunCmd() *cobra.Command {
	var jobsCSV string
	var stateFile string
	var multiPart bool

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the job pipeline",
		Long: `Execute the complete job pipeline: tar → upload → submit.

Example:
  rescale-int pur run --jobs-csv jobs.csv --state state.csv`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			if jobsCSV == "" {
				return fmt.Errorf("--jobs-csv is required")
			}

			logger.Info().
				Str("jobs", jobsCSV).
				Str("state", stateFile).
				Msg("Starting job pipeline")

			// Load config
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Load jobs
			jobs, err := config.LoadJobsCSV(jobsCSV)
			if err != nil {
				return fmt.Errorf("failed to load jobs CSV: %w", err)
			}

			logger.Info().Int("count", len(jobs)).Msg("Loaded jobs")

			// Create API client
			apiClient, err := api.NewClient(cfg)
			if err != nil {
				return fmt.Errorf("failed to create API client: %w", err)
			}

			// Create pipeline
			pipe, err := pipeline.NewPipeline(cfg, apiClient, jobs, stateFile, multiPart)
			if err != nil {
				return fmt.Errorf("failed to create pipeline: %w", err)
			}

			// Run pipeline
			ctx := GetContext()
			if err := pipe.Run(ctx); err != nil {
				return fmt.Errorf("pipeline failed: %w", err)
			}

			logger.Info().Msg("Pipeline completed successfully")
			fmt.Println("\n✓ Pipeline completed")
			return nil
		},
	}

	cmd.Flags().StringVarP(&jobsCSV, "jobs-csv", "j", "", "Jobs CSV file (required)")
	cmd.Flags().StringVarP(&stateFile, "state", "s", "", "State file for resume capability")
	cmd.Flags().BoolVar(&multiPart, "multipart", false, "Enable multi-part mode")

	cmd.MarkFlagRequired("jobs-csv")

	return cmd
}

// newResumeCmd creates the 'resume' command.
func newResumeCmd() *cobra.Command {
	var jobsCSV string
	var stateFile string
	var multiPart bool

	cmd := &cobra.Command{
		Use:   "resume",
		Short: "Resume a previously interrupted pipeline",
		Long: `Resume pipeline execution from saved state.

Example:
  rescale-int pur resume --jobs-csv jobs.csv --state state.csv`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			if stateFile == "" {
				return fmt.Errorf("--state is required")
			}
			if jobsCSV == "" {
				return fmt.Errorf("--jobs-csv is required")
			}

			logger.Info().
				Str("jobs", jobsCSV).
				Str("state", stateFile).
				Msg("Resuming pipeline")

			// Check that state file exists
			if _, err := os.Stat(stateFile); os.IsNotExist(err) {
				return fmt.Errorf("state file does not exist: %s", stateFile)
			}

			// Load config
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Load jobs
			jobs, err := config.LoadJobsCSV(jobsCSV)
			if err != nil {
				return fmt.Errorf("failed to load jobs CSV: %w", err)
			}

			logger.Info().Int("count", len(jobs)).Msg("Loaded jobs")

			// Create API client
			apiClient, err := api.NewClient(cfg)
			if err != nil {
				return fmt.Errorf("failed to create API client: %w", err)
			}

			// Create pipeline (will load existing state)
			pipe, err := pipeline.NewPipeline(cfg, apiClient, jobs, stateFile, multiPart)
			if err != nil {
				return fmt.Errorf("failed to create pipeline: %w", err)
			}

			// Run pipeline (will resume from state)
			ctx := GetContext()
			if err := pipe.Run(ctx); err != nil {
				return fmt.Errorf("pipeline failed: %w", err)
			}

			logger.Info().Msg("Pipeline resumed and completed")
			fmt.Println("\n✓ Pipeline completed")
			return nil
		},
	}

	cmd.Flags().StringVarP(&jobsCSV, "jobs-csv", "j", "", "Jobs CSV file (required)")
	cmd.Flags().StringVarP(&stateFile, "state", "s", "", "State file (required)")
	cmd.Flags().BoolVar(&multiPart, "multipart", false, "Enable multi-part mode")

	cmd.MarkFlagRequired("jobs-csv")
	cmd.MarkFlagRequired("state")

	return cmd
}

// newSubmitExistingCmd creates the 'submit-existing' command.
func newSubmitExistingCmd() *cobra.Command {
	var jobsCSV string
	var stateFile string

	cmd := &cobra.Command{
		Use:   "submit-existing",
		Short: "Submit jobs using existing uploaded file IDs",
		Long: `Create and submit jobs using file IDs that have already been uploaded to Rescale.
This skips the tar and upload phases.

The jobs CSV must contain the extrainputfileids column with existing file IDs.

Example:
  rescale-int pur submit-existing --jobs-csv jobs.csv --state state.csv`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			logger.Info().
				Str("jobs_csv", jobsCSV).
				Str("state_file", stateFile).
				Msg("Starting submit-existing command")

			fmt.Println("Submit-existing: Creating jobs from existing file IDs")
			fmt.Printf("  Jobs CSV: %s\n", jobsCSV)
			fmt.Printf("  State file: %s\n\n", stateFile)

			// Load configuration
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Load jobs from CSV
			logger.Info().Msg("Loading jobs from CSV")
			jobs, err := config.LoadJobsCSV(jobsCSV)
			if err != nil {
				return fmt.Errorf("failed to load jobs CSV: %w", err)
			}

			fmt.Printf("Loaded %d job(s) from %s\n\n", len(jobs), jobsCSV)

			// Create API client
			apiClient, err := api.NewClient(cfg)
			if err != nil {
				return fmt.Errorf("failed to create API client: %w", err)
			}

			// Create pipeline with skip-upload mode
			// The pipeline will skip tar and upload, only create and submit jobs
			logger.Info().Msg("Creating pipeline (submit-existing mode)")
			pipe, err := pipeline.NewPipeline(cfg, apiClient, jobs, stateFile, false)
			if err != nil {
				return fmt.Errorf("failed to create pipeline: %w", err)
			}

			// Note: The existing pipeline.Run() will handle the submit-existing logic
			// It checks if jobs have ExtraInputFileIDs and skips tar/upload accordingly
			ctx := GetContext()
			if err := pipe.Run(ctx); err != nil {
				return fmt.Errorf("submit-existing failed: %w", err)
			}

			logger.Info().Msg("Submit-existing completed")
			fmt.Println("\n✓ Jobs submitted successfully")
			return nil
		},
	}

	cmd.Flags().StringVar(&jobsCSV, "jobs-csv", "jobs.csv", "Jobs CSV file (must contain extrainputfileids column)")
	cmd.Flags().StringVar(&stateFile, "state", "submit_existing_state.csv", "State file")

	return cmd
}

// loadConfig loads the configuration file.
func loadConfig() (*config.Config, error) {
	configPath := cfgFile
	if configPath == "" {
		configPath = "config.csv"
	}

	cfg, err := config.LoadConfigCSV(configPath)
	if err != nil {
		// Try to create default config
		log.Printf("Warning: Could not load config from %s, using defaults", configPath)
		cfg = &config.Config{
			APIKey:     apiKey,
			APIBaseURL: apiBaseURL,
		}
	}

	// Merge with environment variables and flags
	// Priority: flags > environment > config file > defaults
	cfg.MergeWithFlags(apiKey, apiBaseURL, "", "", 0)

	// Validate required fields
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("API key is required (use --api-key flag, RESCALE_API_KEY env var, or config file)")
	}

	return cfg, nil
}
