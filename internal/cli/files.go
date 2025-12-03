// Package cli provides file operation commands.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/util/filter"
)

// newFilesCmd creates the 'files' command group.
func newFilesCmd() *cobra.Command {
	filesCmd := &cobra.Command{
		Use:   "files",
		Short: "File operations (upload, download, list, delete)",
		Long:  `Commands for managing files on the Rescale platform.`,
	}

	// Add file subcommands
	filesCmd.AddCommand(newFilesUploadCmd())
	filesCmd.AddCommand(newFilesDownloadCmd())
	filesCmd.AddCommand(newFilesListCmd())
	filesCmd.AddCommand(newFilesDeleteCmd())

	return filesCmd
}

// newFilesUploadCmd creates the 'files upload' command.
func newFilesUploadCmd() *cobra.Command {
	var folderID string
	var maxConcurrent int
	var checkDuplicates bool
	var noCheckDuplicates bool
	var skipDuplicates bool
	var allowDuplicates bool
	var dryRun bool
	var preEncrypt bool

	cmd := &cobra.Command{
		Use:   "upload <file> [file...]",
		Short: "Upload files to Rescale",
		Long: `Upload one or more files to Rescale cloud storage.

Encryption modes:
  By default, files are encrypted using streaming encryption (per-part, on-the-fly).
  This saves disk space as no temporary encrypted file is created.

  --pre-encrypt          Use legacy encryption (pre-encrypts entire file to temp file)
                         This mode is compatible with older Rescale clients (e.g., Python).

Duplicate handling modes:
  --check-duplicates     Check for existing files before uploading (prompts on conflict)
  --no-check-duplicates  Skip duplicate checking (fast, may create duplicates)
  --skip-duplicates      Check and automatically skip files that already exist
  --allow-duplicates     Check but upload anyway (creates duplicates with same name)

If no duplicate flag is provided, you will be prompted interactively.
Use --dry-run to preview what would happen without actually uploading.

Examples:
  # Upload single file to root (will prompt for duplicate handling)
  rescale-int files upload data.tar.gz

  # Preview what would be uploaded (dry-run with skip-duplicates)
  rescale-int files upload *.dat --dry-run --skip-duplicates

  # Upload with glob pattern, skip any duplicates
  rescale-int files upload *.dat --skip-duplicates

  # Fast upload without duplicate checking
  rescale-int files upload *.dat --no-check-duplicates

  # Upload to specific folder
  rescale-int files upload *.zip --folder-id abc123

  # Use legacy pre-encryption for compatibility with older clients
  rescale-int files upload large_file.tar.gz --pre-encrypt`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			// Validate maxConcurrent
			if maxConcurrent < constants.MinMaxConcurrent || maxConcurrent > constants.MaxMaxConcurrent {
				return fmt.Errorf("--max-concurrent must be between %d and %d, got %d",
					constants.MinMaxConcurrent, constants.MaxMaxConcurrent, maxConcurrent)
			}

			// Validate duplicate flags (mutually exclusive)
			duplicateFlags := 0
			if checkDuplicates {
				duplicateFlags++
			}
			if noCheckDuplicates {
				duplicateFlags++
			}
			if skipDuplicates {
				duplicateFlags++
			}
			if allowDuplicates {
				duplicateFlags++
			}
			if duplicateFlags > 1 {
				return fmt.Errorf("only one of --check-duplicates, --no-check-duplicates, --skip-duplicates, or --allow-duplicates can be specified")
			}

			// Determine duplicate handling mode
			var duplicateMode UploadDuplicateMode
			if noCheckDuplicates {
				duplicateMode = UploadDuplicateModeNoCheck
			} else if skipDuplicates {
				duplicateMode = UploadDuplicateModeSkipAll
			} else if allowDuplicates {
				duplicateMode = UploadDuplicateModeUploadAll
			} else if checkDuplicates {
				duplicateMode = UploadDuplicateModeCheck
			} else {
				// No flag specified - prompt user
				if !IsTerminal() {
					// Non-interactive mode: default to no-check with warning
					fmt.Fprintln(os.Stderr, "⚠️  Warning: Duplicate checking disabled (non-interactive mode). Use --check-duplicates to enable.")
					duplicateMode = UploadDuplicateModeNoCheck
				} else {
					var err error
					duplicateMode, err = promptUploadDuplicateMode()
					if err != nil {
						return err
					}
				}
			}

			// Load config
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Create API client
			apiClient, err := api.NewClient(cfg)
			if err != nil {
				return fmt.Errorf("failed to create API client: %w", err)
			}

			// Use helper function with duplicate mode
			return executeFileUploadWithDuplicateCheck(GetContext(), args, folderID, maxConcurrent, duplicateMode, dryRun, preEncrypt, apiClient, logger)
		},
	}

	cmd.Flags().StringVarP(&folderID, "folder-id", "d", "", "Upload to specific folder (optional, default: root)")
	cmd.Flags().IntVar(&maxConcurrent, "max-concurrent", constants.DefaultMaxConcurrent,
		fmt.Sprintf("Maximum concurrent file uploads (%d-%d)", constants.MinMaxConcurrent, constants.MaxMaxConcurrent))
	cmd.Flags().BoolVar(&checkDuplicates, "check-duplicates", false, "Check for existing files before uploading")
	cmd.Flags().BoolVar(&noCheckDuplicates, "no-check-duplicates", false, "Skip duplicate checking (fast, may create duplicates)")
	cmd.Flags().BoolVar(&skipDuplicates, "skip-duplicates", false, "Check and automatically skip files that already exist")
	cmd.Flags().BoolVar(&allowDuplicates, "allow-duplicates", false, "Check but upload anyway (explicitly allows duplicates)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview what would be uploaded without actually uploading")
	cmd.Flags().BoolVar(&preEncrypt, "pre-encrypt", false, "Use legacy pre-encryption (for compatibility with older Rescale clients)")

	return cmd
}

// newFilesDownloadCmd creates the 'files download' command.
func newFilesDownloadCmd() *cobra.Command {
	var outputDir string
	var maxConcurrent int
	var overwriteAll bool
	var skipAll bool
	var resumeAll bool
	var skipChecksum bool

	cmd := &cobra.Command{
		Use:   "download <file-id> [file-id...]",
		Short: "Download files from Rescale",
		Long: `Download one or more files from Rescale cloud storage.

Examples:
  # Download single file
  rescale-int files download XxYyZz --outdir ./downloads

  # Download multiple files
  rescale-int files download ABC123 DEF456 --outdir ./results

  # Download to current directory
  rescale-int files download XxYyZz`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			// Validate max-concurrent
			if maxConcurrent < constants.MinMaxConcurrent || maxConcurrent > constants.MaxMaxConcurrent {
				return fmt.Errorf("--max-concurrent must be between %d and %d, got %d",
					constants.MinMaxConcurrent, constants.MaxMaxConcurrent, maxConcurrent)
			}

			// Load config
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Create API client
			apiClient, err := api.NewClient(cfg)
			if err != nil {
				return fmt.Errorf("failed to create API client: %w", err)
			}

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

			// Use helper function
			return executeFileDownload(GetContext(), args, outputDir, maxConcurrent, overwriteAll, skipAll, resumeAll, skipChecksum, apiClient, logger)
		},
	}

	cmd.Flags().StringVarP(&outputDir, "outdir", "o", ".", "Output directory for downloaded files")
	cmd.Flags().IntVarP(&maxConcurrent, "max-concurrent", "m", constants.DefaultMaxConcurrent,
		fmt.Sprintf("Maximum concurrent downloads (%d-%d)", constants.MinMaxConcurrent, constants.MaxMaxConcurrent))
	cmd.Flags().BoolVarP(&overwriteAll, "overwrite", "w", false, "Overwrite existing files without prompting")
	cmd.Flags().BoolVarP(&skipAll, "skip", "S", false, "Skip existing files without prompting")
	cmd.Flags().BoolVarP(&resumeAll, "resume", "r", false, "Resume interrupted downloads without prompting")
	cmd.Flags().BoolVar(&skipChecksum, "skip-checksum", false, "Skip checksum verification (not recommended, allows corrupted downloads)")

	return cmd
}

// newFilesListCmd creates the 'files list' command.
func newFilesListCmd() *cobra.Command {
	var limit int
	var filterName string // Deprecated, kept for backward compatibility
	var filterPatterns string
	var excludePatterns string
	var searchTerms string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List files in your Rescale library",
		Long: `List files stored in your Rescale library with filtering support.

Examples:
  # List recent files
  rescale-int files list

  # List more files
  rescale-int files list --limit 50

  # Filter by file type
  rescale-int files list --filter "*.tar.gz,*.dat"

  # Exclude temporary files
  rescale-int files list --exclude "temp*,debug*"

  # Search for files containing "results"
  rescale-int files list --search "results"

  # Combined filters
  rescale-int files list --filter "*.dat" --exclude "debug*" --search "final"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			logger.Info().Msg("Listing files")

			// Load config
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Create API client
			apiClient, err := api.NewClient(cfg)
			if err != nil {
				return fmt.Errorf("failed to create API client: %w", err)
			}

			ctx := GetContext()

			// Get file list
			allFiles, err := apiClient.ListFiles(ctx, limit)
			if err != nil {
				return fmt.Errorf("failed to list files: %w", err)
			}

			// Parse filter patterns
			// Support legacy --filter flag or new flags
			var filterList, excludeList, searchList []string
			if filterName != "" {
				// Legacy single pattern support
				filterList = []string{filterName}
			}
			if filterPatterns != "" {
				filterList = filter.ParsePatternList(filterPatterns)
			}
			if excludePatterns != "" {
				excludeList = filter.ParsePatternList(excludePatterns)
			}
			if searchTerms != "" {
				searchList = filter.ParsePatternList(searchTerms)
			}

			// Apply filters if any are specified
			files := allFiles
			if len(filterList) > 0 || len(excludeList) > 0 || len(searchList) > 0 {
				var filtered []interface{}
				for _, file := range allFiles {
					if fileMap, ok := file.(map[string]interface{}); ok {
						if name, ok := fileMap["name"].(string); ok {
							// Use filter package's matchesFilter logic
							filterCfg := filter.Config{
								Include: filterList,
								Exclude: excludeList,
								Search:  searchList,
							}
							// Create a temporary helper to reuse filter logic
							if matchesFileFilter(name, filterCfg) {
								filtered = append(filtered, file)
							}
						}
					}
				}
				files = filtered

				if len(files) < len(allFiles) {
					fmt.Printf("Filtered: %d of %d files match filters\n", len(files), len(allFiles))
				}
			}

			if len(files) == 0 {
				fmt.Println("No files found")
				return nil
			}

			// Display files
			fmt.Printf("Found %d file(s):\n\n", len(files))
			fmt.Printf("%-20s %-40s %-15s %s\n", "FILE ID", "NAME", "SIZE", "CREATED")
			fmt.Println(strings.Repeat("-", 100))

			for _, file := range files {
				if fileMap, ok := file.(map[string]interface{}); ok {
					id := fileMap["id"].(string)
					name := fileMap["name"].(string)
					size := int64(0)
					if s, ok := fileMap["decryptedSize"].(float64); ok {
						size = int64(s)
					}
					created := ""
					if c, ok := fileMap["dateUploaded"].(string); ok {
						created = c[:10] // Just the date part
					}

					sizeMB := float64(size) / (1024 * 1024)
					fmt.Printf("%-20s %-40s %10.2f MB   %s\n", id, name, sizeMB, created)
				}
			}

			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "Maximum number of files to list")
	cmd.Flags().StringVar(&filterName, "filter", "", "Filter files by name pattern (e.g., '*.tar.gz')")
	cmd.Flags().MarkDeprecated("filter", "use --include instead")
	cmd.Flags().StringVar(&filterPatterns, "include", "", "Include only files matching these patterns (comma-separated glob patterns, e.g. \"*.dat,*.log\")")
	cmd.Flags().StringVarP(&excludePatterns, "exclude", "x", "", "Exclude files matching these patterns (comma-separated glob patterns, e.g. \"debug*,temp*\")")
	cmd.Flags().StringVarP(&searchTerms, "search", "s", "", "Include only files containing these terms in filename (comma-separated, case-insensitive)")

	return cmd
}

// matchesFileFilter checks if a filename matches the filter configuration.
// This is a helper to reuse the filter package logic for file lists.
func matchesFileFilter(filename string, config filter.Config) bool {
	// 1. Check exclude patterns first (highest priority)
	for _, pattern := range config.Exclude {
		if matched, _ := filepath.Match(pattern, filename); matched {
			return false // Excluded
		}
		if matched, _ := filepath.Match(pattern, filepath.Base(filename)); matched {
			return false // Excluded
		}
	}

	// 2. Check include patterns
	if len(config.Include) > 0 {
		included := false
		for _, pattern := range config.Include {
			if matched, _ := filepath.Match(pattern, filename); matched {
				included = true
				break
			}
			if matched, _ := filepath.Match(pattern, filepath.Base(filename)); matched {
				included = true
				break
			}
		}
		if !included {
			return false // Not included by any pattern
		}
	}

	// 3. Check search terms (case-insensitive substring match)
	if len(config.Search) > 0 {
		lowerFilename := strings.ToLower(filename)
		for _, term := range config.Search {
			lowerTerm := strings.ToLower(term)
			if !strings.Contains(lowerFilename, lowerTerm) {
				return false // Must match ALL search terms
			}
		}
	}

	return true // Passed all filters
}

// newFilesDeleteCmd creates the 'files delete' command.
func newFilesDeleteCmd() *cobra.Command {
	var fileIDs []string
	var confirm bool

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete files from Rescale",
		Long: `Delete one or more files from Rescale cloud storage.

WARNING: This operation cannot be undone!

Example:
  # Delete single file (will prompt for confirmation)
  rescale-int files delete --fileid XxYyZz

  # Delete multiple files
  rescale-int files delete --fileid ABC123 --fileid DEF456

  # Delete without confirmation prompt
  rescale-int files delete --fileid XxYyZz --confirm`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			if len(fileIDs) == 0 {
				return fmt.Errorf("at least one --fileid is required")
			}

			// Confirmation prompt if not using --confirm flag
			if !confirm {
				fmt.Printf("You are about to delete %d file(s). This cannot be undone.\n", len(fileIDs))
				fmt.Print("Are you sure? (yes/no): ")
				var response string
				fmt.Scanln(&response)
				if response != "yes" {
					fmt.Println("Deletion cancelled")
					return nil
				}
			}

			logger.Info().
				Int("count", len(fileIDs)).
				Msg("Deleting files")

			// Load config
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Create API client
			apiClient, err := api.NewClient(cfg)
			if err != nil {
				return fmt.Errorf("failed to create API client: %w", err)
			}

			ctx := GetContext()

			// Delete each file
			for i, fileID := range fileIDs {
				fmt.Printf("[%d/%d] Deleting file %s...\n", i+1, len(fileIDs), fileID)

				if err := apiClient.DeleteFile(ctx, fileID); err != nil {
					logger.Error().Str("file_id", fileID).Err(err).Msg("Failed to delete file")
					return fmt.Errorf("failed to delete %s: %w", fileID, err)
				}

				logger.Info().Str("file_id", fileID).Msg("File deleted")
				fmt.Printf("✓ Deleted successfully\n")
			}

			fmt.Printf("\n✓ Successfully deleted %d file(s)\n", len(fileIDs))

			return nil
		},
	}

	cmd.Flags().StringArrayVarP(&fileIDs, "fileid", "i", []string{}, "File ID to delete (can be specified multiple times)")
	cmd.Flags().BoolVarP(&confirm, "confirm", "y", false, "Skip confirmation prompt")

	return cmd
}
