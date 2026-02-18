// Package cli provides PUR (Parallel Uploader and Runner) commands.
package cli

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/filescan"
	"github.com/rescale/rescale-int/internal/pur/pattern"
	"github.com/rescale/rescale-int/internal/pur/pipeline"
	"github.com/rescale/rescale-int/internal/pur/state"
	"github.com/rescale/rescale-int/internal/pur/validation"
	"github.com/rescale/rescale-int/internal/util/multipart"
)

// newPURCmd creates the 'pur' command group.
func newPURCmd() *cobra.Command {
	purCmd := &cobra.Command{
		Use:   "pur",
		Short: "PUR (Parallel Upload and Run) for Rescale",
		Long:  `PUR (Parallel Upload and Run) commands for batch job submission.`,
	}

	// Add PUR subcommands
	purCmd.AddCommand(newMakeDirsCSVCmd())
	purCmd.AddCommand(newScanFilesCmd())
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
	var dirPattern string
	var overwrite bool
	var iteratePatterns bool
	var commandPatternTest bool
	var cwd string
	var runSubpath string
	var validationPattern string
	var startIndex int
	var partDirs []string

	cmd := &cobra.Command{
		Use:   "make-dirs-csv",
		Short: "Generate jobs CSV from directory pattern",
		Long: `Generate a jobs CSV file by scanning directories matching a pattern.

This command scans for directories matching the pattern and creates a jobs CSV
file with one job per directory, using the template CSV as a base.

Use --part-dirs for multi-part mode to scan multiple project directories
(e.g., DOE_1 DOE_2 DOE_3). Job names will include a project suffix for uniqueness.

Use --iterate-command-patterns to automatically vary numeric patterns in the
template command across jobs (e.g., data_1.txt becomes data_2.txt for Run_2).

Use --command-pattern-test to preview detected patterns without generating CSV.

Examples:
  rescale-int pur make-dirs-csv --template template.csv --output jobs.csv --pattern "Run_*"
  rescale-int pur make-dirs-csv --template template.csv --output jobs.csv --pattern "Run_*" --iterate-command-patterns
  rescale-int pur make-dirs-csv --template template.csv --pattern "Run_*" --command-pattern-test
  rescale-int pur make-dirs-csv --template template.csv --output jobs.csv --pattern "Run_*" \
    --part-dirs /data/DOE_1 /data/DOE_2 /data/DOE_3 --validation-pattern "*.avg.fnc"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			if templatePath == "" {
				return fmt.Errorf("--template is required")
			}
			if dirPattern == "" {
				return fmt.Errorf("--pattern is required")
			}

			// Load template
			templateJobs, err := config.LoadJobsCSV(templatePath)
			if err != nil {
				return fmt.Errorf("failed to load template: %w", err)
			}

			if len(templateJobs) == 0 {
				return fmt.Errorf("template CSV is empty")
			}

			// Use first row as template
			tmpl := templateJobs[0]

			// --command-pattern-test: preview pattern detection and exit
			if commandPatternTest {
				patterns := pattern.DetectNumericPatterns(tmpl.Command)
				if len(patterns) == 0 {
					fmt.Println("No numeric patterns detected in command:")
					fmt.Printf("  %s\n", tmpl.Command)
					return nil
				}

				fmt.Printf("Command: %s\n\n", tmpl.Command)
				fmt.Printf("Detected %d pattern(s):\n", len(patterns))
				for i, p := range patterns {
					fmt.Printf("  [%d] %q  (prefix=%q number=%q suffix=%q)\n", i+1, p.FullMatch, p.Prefix, p.Number, p.Suffix)
				}

				templateIdx := pattern.ExtractIndexFromJobName(tmpl.JobName)
				fmt.Printf("\nTemplate index (from job name %q): %d\n", tmpl.JobName, templateIdx)
				fmt.Println("\nPreview of iteration:")
				for _, idx := range []int{1, 2, 3, 5, 10} {
					iterated := pattern.IterateCommandPatterns(tmpl.Command, templateIdx, idx)
					fmt.Printf("  index=%d: %s\n", idx, iterated)
				}
				return nil
			}

			if outputPath == "" {
				return fmt.Errorf("--output is required")
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
				Str("pattern", dirPattern).
				Bool("iteratePatterns", iteratePatterns).
				Msg("Generating jobs CSV from directories")

			// v4.6.5: Use shared ScanDirectories() for both single and multi-part modes
			baseJobName := strings.TrimSuffix(tmpl.JobName, "_1")
			if baseJobName == "" {
				baseJobName = "Job"
			}

			scanOpts := multipart.ScanOpts{
				Pattern:           filepath.Base(dirPattern),
				ValidationPattern: validationPattern,
				BaseJobName:       baseJobName,
				StartIndex:        startIndex,
				RunSubpath:        runSubpath,
			}

			if len(partDirs) > 0 {
				// Multi-part mode
				scanOpts.PartDirs = partDirs
				logger.Info().Int("partDirs", len(partDirs)).Msg("Multi-part mode enabled")
			} else {
				// Single-directory mode
				baseDir := cwd
				if baseDir == "" {
					baseDir = filepath.Dir(dirPattern)
					if baseDir == "." {
						baseDir, err = os.Getwd()
						if err != nil {
							return fmt.Errorf("failed to get current directory: %w", err)
						}
					}
				}
				scanOpts.SingleDir = baseDir
			}

			results, err := multipart.ScanDirectories(scanOpts)
			if err != nil {
				return fmt.Errorf("directory scan failed: %w", err)
			}

			// Convert ScanResults to JobSpecs
			templateIdx := pattern.ExtractIndexFromJobName(tmpl.JobName)
			var jobs []models.JobSpec
			for _, r := range results {
				job := tmpl
				job.JobName = r.JobName
				job.Directory = r.Directory

				// Iterate command patterns if requested
				if iteratePatterns {
					job.Command = pattern.IterateCommandPatterns(tmpl.Command, templateIdx, r.DirNumber)
				}

				jobs = append(jobs, job)
				logger.Info().Str("dir", filepath.Base(r.Directory)).Str("job", r.JobName).Msg("Added job")
			}

			// Save jobs CSV
			if err := config.SaveJobsCSV(outputPath, jobs); err != nil {
				return fmt.Errorf("failed to save jobs CSV: %w", err)
			}

			logger.Info().
				Int("count", len(jobs)).
				Str("output", outputPath).
				Msg("Jobs CSV generated successfully")

			fmt.Printf("Generated %d jobs in %s\n", len(jobs), outputPath)
			return nil
		},
	}

	cmd.Flags().StringVarP(&templatePath, "template", "t", "", "Template CSV file (required)")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output jobs CSV file (required unless --command-pattern-test)")
	cmd.Flags().StringVarP(&dirPattern, "pattern", "p", "", "Directory pattern, e.g., 'Run_*' (required)")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "Overwrite existing output file")
	cmd.Flags().BoolVar(&iteratePatterns, "iterate-command-patterns", false, "Vary command across runs by iterating numeric patterns")
	cmd.Flags().BoolVar(&commandPatternTest, "command-pattern-test", false, "Preview pattern detection without generating CSV")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Working directory (default: current directory)")
	cmd.Flags().StringVar(&runSubpath, "run-subpath", "", "Subdirectory path to navigate before finding runs")
	cmd.Flags().StringVar(&validationPattern, "validation-pattern", "", "File pattern to validate directories")
	cmd.Flags().IntVar(&startIndex, "start-index", 1, "Starting index for job numbering")
	cmd.Flags().StringSliceVar(&partDirs, "part-dirs", nil, "Project directories for multi-part mode (e.g., DOE_1 DOE_2 DOE_3)")

	cmd.MarkFlagRequired("template")
	cmd.MarkFlagRequired("pattern")

	return cmd
}

// newScanFilesCmd creates the 'scan-files' command.
// v4.0.8: Unified CLI for file-based scanning, shares logic with GUI.
func newScanFilesCmd() *cobra.Command {
	var rootDir string
	var primaryPattern string
	var secondaryPatterns []string
	var templatePath string
	var outputPath string
	var outputJSON bool
	var overwrite bool

	cmd := &cobra.Command{
		Use:   "scan-files",
		Short: "Scan for primary files and attach secondary files",
		Long: `Scan for primary files matching a pattern and attach secondary files to create jobs.

This command uses the unified file scanning backend shared with the GUI.
Each primary file becomes a job, with optional secondary files attached.

Secondary patterns support:
  - Wildcards: "*" is replaced with primary file's basename (e.g., "*.mesh" for "model.inp" becomes "model.mesh")
  - Subpaths: Relative paths like "../meshes/*.cfg" are resolved from primary file's directory
  - Required/Optional: Append ":required" or ":optional" (default: required)

Examples:
  # Scan for .inp files with required .mesh secondary
  rescale-int pur scan-files --root /data --primary "*.inp" --secondary "*.mesh"

  # With optional config from parent directory
  rescale-int pur scan-files --root /data --primary "inputs/*.inp" \
    --secondary "*.mesh:required" --secondary "../common.cfg:optional"

  # Generate jobs.csv from template
  rescale-int pur scan-files --root /data --primary "*.inp" \
    --template template.csv --output jobs.csv`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			if primaryPattern == "" {
				return fmt.Errorf("--primary is required")
			}

			// Parse secondary patterns
			patterns := make([]filescan.SecondaryPattern, 0, len(secondaryPatterns))
			for _, sp := range secondaryPatterns {
				required := true
				pattern := sp
				if strings.HasSuffix(sp, ":optional") {
					required = false
					pattern = strings.TrimSuffix(sp, ":optional")
				} else if strings.HasSuffix(sp, ":required") {
					pattern = strings.TrimSuffix(sp, ":required")
				}
				patterns = append(patterns, filescan.SecondaryPattern{
					Pattern:  pattern,
					Required: required,
				})
			}

			// Default root to current directory
			if rootDir == "" {
				var err error
				rootDir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("failed to get current directory: %w", err)
				}
			}

			logger.Info().
				Str("root", rootDir).
				Str("primary", primaryPattern).
				Int("secondaryCount", len(patterns)).
				Msg("Scanning for files")

			// Perform scan using shared backend
			result := filescan.ScanFiles(filescan.ScanOptions{
				RootDir:           rootDir,
				PrimaryPattern:    primaryPattern,
				SecondaryPatterns: patterns,
			})

			if result.Error != "" {
				return fmt.Errorf("scan failed: %s", result.Error)
			}

			// Output results
			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			// Print summary
			fmt.Printf("\nScan Results:\n")
			fmt.Printf("  Total primary files found: %d\n", result.TotalCount)
			fmt.Printf("  Jobs created: %d\n", result.MatchCount)
			if len(result.SkippedFiles) > 0 {
				fmt.Printf("  Skipped: %d\n", len(result.SkippedFiles))
				for _, skip := range result.SkippedFiles {
					fmt.Printf("    - %s\n", skip)
				}
			}
			if len(result.Warnings) > 0 {
				fmt.Printf("  Warnings:\n")
				for _, w := range result.Warnings {
					fmt.Printf("    - %s\n", w)
				}
			}

			// If template and output specified, generate jobs CSV
			if templatePath != "" && outputPath != "" {
				if !overwrite {
					if _, err := os.Stat(outputPath); err == nil {
						return fmt.Errorf("output file %s exists (use --overwrite)", outputPath)
					}
				}

				templateJobs, err := config.LoadJobsCSV(templatePath)
				if err != nil {
					return fmt.Errorf("failed to load template: %w", err)
				}
				if len(templateJobs) == 0 {
					return fmt.Errorf("template CSV is empty")
				}

				template := templateJobs[0]
				var jobs []models.JobSpec

				for i, jf := range result.Jobs {
					job := template
					job.Directory = jf.PrimaryDir
					if template.JobName != "" {
						job.JobName = fmt.Sprintf("%s_%d", template.JobName, i+1)
					} else {
						job.JobName = fmt.Sprintf("Job_%d", i+1)
					}
					// Store input files in extra field (comma-separated relative paths)
					relFiles := make([]string, len(jf.InputFiles))
					for j, f := range jf.InputFiles {
						rel, err := filepath.Rel(jf.PrimaryDir, f)
						if err != nil {
							relFiles[j] = f
						} else {
							relFiles[j] = rel
						}
					}
					// Note: InputFiles aren't directly in JobSpec, would need model update
					// For now, we set directory which is the primary use case
					jobs = append(jobs, job)
				}

				if err := config.SaveJobsCSV(outputPath, jobs); err != nil {
					return fmt.Errorf("failed to save jobs CSV: %w", err)
				}

				fmt.Printf("\n✓ Generated %d jobs in %s\n", len(jobs), outputPath)
			} else {
				// Print job details
				fmt.Printf("\nJobs:\n")
				for i, jf := range result.Jobs {
					fmt.Printf("  [%d] %s\n", i+1, filepath.Base(jf.PrimaryFile))
					fmt.Printf("      Dir: %s\n", jf.PrimaryDir)
					fmt.Printf("      Files: %d\n", len(jf.InputFiles))
					for _, f := range jf.InputFiles {
						fmt.Printf("        - %s\n", filepath.Base(f))
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&rootDir, "root", "r", "", "Root directory to scan (default: current dir)")
	cmd.Flags().StringVar(&primaryPattern, "primary", "", "Primary file pattern, e.g., '*.inp' (required)")
	cmd.Flags().StringArrayVar(&secondaryPatterns, "secondary", nil, "Secondary file patterns (can repeat), e.g., '*.mesh:required'")
	cmd.Flags().StringVarP(&templatePath, "template", "t", "", "Template CSV file for generating jobs")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output jobs CSV file")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output results as JSON")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "Overwrite existing output file")

	cmd.MarkFlagRequired("primary")

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

			// v4.6.0: Shared validation — same rules as GUI ValidateJobSpec
			hasErrors := false
			for i, job := range jobs {
				errs := validation.ValidateJobSpec(job)

				// Also check directory exists (warning, not fatal)
				if _, err := os.Stat(job.Directory); os.IsNotExist(err) {
					logger.Warn().
						Str("job", job.JobName).
						Str("dir", job.Directory).
						Msg("Directory does not exist")
				}

				if len(errs) > 0 {
					hasErrors = true
					fmt.Printf("[%d/%d] ✗ %s\n", i+1, len(jobs), job.JobName)
					for _, e := range errs {
						fmt.Printf("        - %s\n", e)
					}
				} else {
					fmt.Printf("[%d/%d] ✓ %s\n", i+1, len(jobs), job.JobName)
				}
			}

			if hasErrors {
				return fmt.Errorf("validation failed: one or more jobs have errors")
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
	var includePatterns []string
	var excludePatterns []string
	var flattenTar bool
	var tarCompression string
	var tarWorkers int
	var uploadWorkers int
	var jobWorkers int
	var rmTarOnSuccess bool
	var extraInputFiles string
	var decompressExtras bool
	var dryRun bool

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

			// Override config values when CLI flags are explicitly set
			if cmd.Flags().Changed("include-pattern") {
				cfg.IncludePatterns = includePatterns
			}
			if cmd.Flags().Changed("exclude-pattern") {
				cfg.ExcludePatterns = excludePatterns
			}
			if cmd.Flags().Changed("flatten-tar") {
				cfg.FlattenTar = flattenTar
			}
			if cmd.Flags().Changed("tar-compression") {
				cfg.TarCompression = tarCompression
			}
			if cmd.Flags().Changed("tar-workers") && tarWorkers > 0 {
				cfg.TarWorkers = tarWorkers
			}
			if cmd.Flags().Changed("upload-workers") && uploadWorkers > 0 {
				cfg.UploadWorkers = uploadWorkers
			}
			if cmd.Flags().Changed("job-workers") && jobWorkers > 0 {
				cfg.JobWorkers = jobWorkers
			}

			// Load jobs
			jobs, err := config.LoadJobsCSV(jobsCSV)
			if err != nil {
				return fmt.Errorf("failed to load jobs CSV: %w", err)
			}

			logger.Info().Int("count", len(jobs)).Msg("Loaded jobs")

			// v4.6.5: dry-run short-circuit — show plan without executing
			if dryRun {
				fmt.Printf("\n=== DRY RUN: %d jobs loaded ===\n\n", len(jobs))
				fmt.Printf("%-5s %-30s %-20s %-10s %-8s %s\n", "#", "Job Name", "Directory", "CoreType", "Hours", "Command (preview)")
				fmt.Println(strings.Repeat("-", 110))
				for i, job := range jobs {
					cmdPreview := job.Command
					if len(cmdPreview) > 40 {
						cmdPreview = cmdPreview[:37] + "..."
					}
					dirPreview := filepath.Base(job.Directory)
					fmt.Printf("%-5d %-30s %-20s %-10s %-8.1f %s\n",
						i+1, job.JobName, dirPreview, job.CoreType, job.WalltimeHours, cmdPreview)
				}
				fmt.Printf("\nTotal: %d jobs\n", len(jobs))
				fmt.Println("\n(dry-run mode: no jobs were created or submitted)")
				return nil
			}

			// Create API client
			apiClient, err := api.NewClient(cfg)
			if err != nil {
				return fmt.Errorf("failed to create API client: %w", err)
			}

			// Create pipeline
			pipe, err := pipeline.NewPipeline(cfg, apiClient, jobs, stateFile, multiPart, nil, false, extraInputFiles, decompressExtras)
			if err != nil {
				return fmt.Errorf("failed to create pipeline: %w", err)
			}

			// Apply rm-tar-on-success if set
			if rmTarOnSuccess {
				pipe.SetRmTarOnSuccess(true)
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
	cmd.Flags().StringArrayVar(&includePatterns, "include-pattern", nil, "Only tar files matching glob pattern (can repeat)")
	cmd.Flags().StringArrayVar(&excludePatterns, "exclude-pattern", nil, "Exclude files matching glob from tar (can repeat)")
	cmd.Flags().BoolVar(&flattenTar, "flatten-tar", false, "Remove subdirectory structure in tarball")
	cmd.Flags().StringVar(&tarCompression, "tar-compression", "", "Tar compression: 'none' or 'gz' (default from config)")
	cmd.Flags().IntVar(&tarWorkers, "tar-workers", 0, "Number of parallel tar workers (default from config)")
	cmd.Flags().IntVar(&uploadWorkers, "upload-workers", 0, "Number of parallel upload workers (default from config)")
	cmd.Flags().IntVar(&jobWorkers, "job-workers", 0, "Number of parallel job creation workers (default from config)")
	cmd.Flags().BoolVar(&rmTarOnSuccess, "rm-tar-on-success", false, "Delete local tar file after successful upload")
	cmd.Flags().StringVar(&extraInputFiles, "extra-input-files", "", "Comma-separated local paths and/or id:<fileId> references to share across all jobs")
	cmd.Flags().BoolVar(&decompressExtras, "decompress-extras", false, "Decompress extra input files on cluster")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Validate and show plan without executing")

	cmd.MarkFlagRequired("jobs-csv")

	return cmd
}

// newResumeCmd creates the 'resume' command.
func newResumeCmd() *cobra.Command {
	var jobsCSV string
	var stateFile string
	var multiPart bool
	var includePatterns []string
	var excludePatterns []string
	var flattenTar bool
	var tarCompression string
	var tarWorkers int
	var uploadWorkers int
	var jobWorkers int
	var rmTarOnSuccess bool
	var extraInputFiles string
	var decompressExtras bool
	var dryRun bool

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

			// Override config values when CLI flags are explicitly set
			if cmd.Flags().Changed("include-pattern") {
				cfg.IncludePatterns = includePatterns
			}
			if cmd.Flags().Changed("exclude-pattern") {
				cfg.ExcludePatterns = excludePatterns
			}
			if cmd.Flags().Changed("flatten-tar") {
				cfg.FlattenTar = flattenTar
			}
			if cmd.Flags().Changed("tar-compression") {
				cfg.TarCompression = tarCompression
			}
			if cmd.Flags().Changed("tar-workers") && tarWorkers > 0 {
				cfg.TarWorkers = tarWorkers
			}
			if cmd.Flags().Changed("upload-workers") && uploadWorkers > 0 {
				cfg.UploadWorkers = uploadWorkers
			}
			if cmd.Flags().Changed("job-workers") && jobWorkers > 0 {
				cfg.JobWorkers = jobWorkers
			}

			// Load jobs
			jobs, err := config.LoadJobsCSV(jobsCSV)
			if err != nil {
				return fmt.Errorf("failed to load jobs CSV: %w", err)
			}

			logger.Info().Int("count", len(jobs)).Msg("Loaded jobs")

			// v4.6.5: dry-run short-circuit — analyze state without executing
			if dryRun {
				// Load state file to analyze what's remaining
				stateMgr := state.NewManager(stateFile)
				if err := stateMgr.Load(); err != nil {
					return fmt.Errorf("failed to load state: %w", err)
				}

				needsTar, needsUpload, needsCreate, needsSubmit, complete := 0, 0, 0, 0, 0
				for i := range jobs {
					idx := i + 1
					st := stateMgr.GetState(idx)
					if st == nil {
						needsTar++
						continue
					}
					if st.TarStatus == "success" && st.UploadStatus == "success" && st.JobID != "" && st.SubmitStatus == "success" {
						complete++
					} else if st.TarStatus == "success" && st.UploadStatus == "success" && st.JobID != "" {
						needsSubmit++
					} else if st.TarStatus == "success" && st.UploadStatus == "success" {
						needsCreate++
					} else if st.TarStatus == "success" {
						needsUpload++
					} else {
						needsTar++
					}
				}

				fmt.Printf("\n=== DRY RUN: Resume Analysis ===\n\n")
				fmt.Printf("Total jobs:       %d\n", len(jobs))
				fmt.Printf("Already complete: %d\n", complete)
				fmt.Printf("Need tar:         %d\n", needsTar)
				fmt.Printf("Need upload:      %d\n", needsUpload)
				fmt.Printf("Need job create:  %d\n", needsCreate)
				fmt.Printf("Need submit:      %d\n", needsSubmit)
				fmt.Printf("Remaining:        %d\n", needsTar+needsUpload+needsCreate+needsSubmit)
				fmt.Println("\n(dry-run mode: no work was performed)")
				return nil
			}

			// Create API client
			apiClient, err := api.NewClient(cfg)
			if err != nil {
				return fmt.Errorf("failed to create API client: %w", err)
			}

			// Create pipeline (will load existing state)
			pipe, err := pipeline.NewPipeline(cfg, apiClient, jobs, stateFile, multiPart, nil, false, extraInputFiles, decompressExtras)
			if err != nil {
				return fmt.Errorf("failed to create pipeline: %w", err)
			}

			// Apply rm-tar-on-success if set
			if rmTarOnSuccess {
				pipe.SetRmTarOnSuccess(true)
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
	cmd.Flags().StringArrayVar(&includePatterns, "include-pattern", nil, "Only tar files matching glob pattern (can repeat)")
	cmd.Flags().StringArrayVar(&excludePatterns, "exclude-pattern", nil, "Exclude files matching glob from tar (can repeat)")
	cmd.Flags().BoolVar(&flattenTar, "flatten-tar", false, "Remove subdirectory structure in tarball")
	cmd.Flags().StringVar(&tarCompression, "tar-compression", "", "Tar compression: 'none' or 'gz' (default from config)")
	cmd.Flags().IntVar(&tarWorkers, "tar-workers", 0, "Number of parallel tar workers (default from config)")
	cmd.Flags().IntVar(&uploadWorkers, "upload-workers", 0, "Number of parallel upload workers (default from config)")
	cmd.Flags().IntVar(&jobWorkers, "job-workers", 0, "Number of parallel job creation workers (default from config)")
	cmd.Flags().BoolVar(&rmTarOnSuccess, "rm-tar-on-success", false, "Delete local tar file after successful upload")
	cmd.Flags().StringVar(&extraInputFiles, "extra-input-files", "", "Comma-separated local paths and/or id:<fileId> references to share across all jobs")
	cmd.Flags().BoolVar(&decompressExtras, "decompress-extras", false, "Decompress extra input files on cluster")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be resumed without executing")

	cmd.MarkFlagRequired("jobs-csv")
	cmd.MarkFlagRequired("state")

	return cmd
}

// newSubmitExistingCmd creates the 'submit-existing' command.
func newSubmitExistingCmd() *cobra.Command {
	var jobsCSV string
	var stateFile string
	var ids string

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

			// v4.6.5: --ids direct submission — skip CSV/pipeline entirely
			if ids != "" && cmd.Flags().Changed("jobs-csv") {
				return fmt.Errorf("cannot use both --ids and --jobs-csv")
			}

			if ids != "" {
				cfg, err := loadConfig()
				if err != nil {
					return fmt.Errorf("failed to load config: %w", err)
				}

				apiClient, err := api.NewClient(cfg)
				if err != nil {
					return fmt.Errorf("failed to create API client: %w", err)
				}

				ctx := GetContext()
				jobIDs := strings.Split(ids, ",")
				var failed int
				for _, id := range jobIDs {
					id = strings.TrimSpace(id)
					if id == "" {
						continue
					}
					err := apiClient.SubmitJob(ctx, id)
					if err != nil {
						fmt.Printf("[SUBMIT] %s -> FAILED: %v\n", id, err)
						failed++
					} else {
						fmt.Printf("[SUBMIT] %s -> OK\n", id)
					}
				}

				if failed > 0 {
					return fmt.Errorf("%d of %d submissions failed", failed, len(jobIDs))
				}
				fmt.Printf("\nAll %d jobs submitted successfully\n", len(jobIDs))
				return nil
			}

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

			// Preflight validation: submit-existing requires ExtraInputFileIDs
			for i, job := range jobs {
				if job.ExtraInputFileIDs == "" {
					return fmt.Errorf("job %d (%s): ExtraInputFileIDs is empty — submit-existing requires pre-uploaded file IDs", i+1, job.JobName)
				}
			}

			// Create API client
			apiClient, err := api.NewClient(cfg)
			if err != nil {
				return fmt.Errorf("failed to create API client: %w", err)
			}

			// Create pipeline with skip-upload mode
			// The pipeline will skip tar and upload, only create and submit jobs
			logger.Info().Msg("Creating pipeline (submit-existing mode)")
			pipe, err := pipeline.NewPipeline(cfg, apiClient, jobs, stateFile, false, nil, true, "", false)
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
	cmd.Flags().StringVar(&ids, "ids", "", "Comma-separated job IDs to submit directly")

	return cmd
}

// loadConfig loads the configuration file.
func loadConfig() (*config.Config, error) {
	configPath := cfgFile
	if configPath == "" {
		configPath = config.GetDefaultConfigPath()
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

	// Merge with environment variables, token file, and flags
	// Priority: flags > token-file > environment > defaults
	cfg.MergeWithFlagsAndTokenFile(apiKey, tokenFile, apiBaseURL, "", "", 0)

	// Validate required fields
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("API key is required (use --api-key flag, --token-file flag, or RESCALE_API_KEY env var)")
	}

	// Prompt for proxy password if needed (only in interactive terminal)
	if http.NeedsProxyPassword(cfg) {
		if IsTerminal() {
			password, err := PromptProxyPassword(cfg.ProxyUser, cfg.ProxyHost)
			if err != nil {
				return nil, fmt.Errorf("proxy authentication required but failed to get password: %w", err)
			}
			cfg.ProxyPassword = password
		} else {
			return nil, fmt.Errorf("proxy authentication required but no password provided and not running in interactive terminal")
		}
	}

	return cfg, nil
}
