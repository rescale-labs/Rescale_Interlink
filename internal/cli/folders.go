// Package cli provides folder operation commands.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/diskspace"
	"github.com/rescale/rescale-int/internal/progress"
)

// newFoldersCmd creates the 'folders' command group.
func newFoldersCmd() *cobra.Command {
	foldersCmd := &cobra.Command{
		Use:   "folders",
		Short: "Folder operations (create, list, upload-dir, download-dir, delete)",
		Long:  `Commands for managing folders on the Rescale platform.`,
	}

	// Add folder subcommands
	foldersCmd.AddCommand(newFoldersCreateCmd())
	foldersCmd.AddCommand(newFoldersListCmd())
	foldersCmd.AddCommand(newFoldersUploadDirCmd())
	foldersCmd.AddCommand(newFoldersDownloadDirCmd())
	foldersCmd.AddCommand(newFoldersDeleteCmd())

	return foldersCmd
}

// newFoldersCreateCmd creates the 'folders create' command.
func newFoldersCreateCmd() *cobra.Command {
	var name string
	var parentID string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new folder",
		Long: `Create a new folder in your Rescale library.

Example:
  # Create folder in root (My Library)
  rescale-int folders create --name "Project_A"

  # Create subfolder
  rescale-int folders create --name "Simulation_Results" --parent-id XxYyZz`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			if name == "" {
				return fmt.Errorf("--name is required")
			}

			logger.Info().Str("name", name).Str("parent", parentID).Msg("Creating folder")

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

			// Get root folders if no parent specified
			if parentID == "" {
				folders, err := apiClient.GetRootFolders(ctx)
				if err != nil {
					return fmt.Errorf("failed to get root folders: %w", err)
				}
				parentID = folders.MyLibrary
				logger.Info().Str("parent_id", parentID).Msg("Using My Library as parent")
			}

			// Create folder
			folderID, err := apiClient.CreateFolder(ctx, name, parentID)
			if err != nil {
				return fmt.Errorf("failed to create folder: %w", err)
			}

			logger.Info().Str("folder_id", folderID).Msg("Folder created")
			fmt.Printf("✓ Folder created successfully\n")
			fmt.Printf("  Name: %s\n", name)
			fmt.Printf("  ID: %s\n", folderID)

			return nil
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Folder name (required)")
	cmd.Flags().StringVar(&parentID, "parent-id", "", "Parent folder ID (default: My Library)")

	cmd.MarkFlagRequired("name")

	return cmd
}

// newFoldersListCmd creates the 'folders list' command.
func newFoldersListCmd() *cobra.Command {
	var folderID string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List folder contents",
		Long: `List files and subfolders in a folder.

Example:
  # List root folder (My Library)
  rescale-int folders list

  # List specific folder
  rescale-int folders list --folder-id XxYyZz`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

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

			// Get root folder if not specified
			if folderID == "" {
				folders, err := apiClient.GetRootFolders(ctx)
				if err != nil {
					return fmt.Errorf("failed to get root folders: %w", err)
				}
				folderID = folders.MyLibrary
				logger.Info().Str("folder_id", folderID).Msg("Listing My Library")
			}

			// List folder contents
			contents, err := apiClient.ListFolderContents(ctx, folderID)
			if err != nil {
				return fmt.Errorf("failed to list folder: %w", err)
			}

			// Display contents
			fmt.Printf("Folder contents:\n\n")

			if len(contents.Folders) > 0 {
				fmt.Println("Folders:")
				for _, folder := range contents.Folders {
					fmt.Printf("  📁 %s (ID: %s)\n", folder.Name, folder.ID)
				}
				fmt.Println()
			}

			if len(contents.Files) > 0 {
				fmt.Println("Files:")
				for _, file := range contents.Files {
					sizeMB := float64(file.DecryptedSize) / (1024 * 1024)
					fmt.Printf("  📄 %s (%.2f MB, ID: %s)\n", file.Name, sizeMB, file.ID)
				}
			}

			if len(contents.Folders) == 0 && len(contents.Files) == 0 {
				fmt.Println("  (empty)")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&folderID, "folder-id", "", "Folder ID to list (default: My Library)")

	return cmd
}

// newFoldersUploadDirCmd creates the 'folders upload-dir' command.
func newFoldersUploadDirCmd() *cobra.Command {
	var parentID string
	var continueOnError bool
	var includeHidden bool
	var maxConcurrent int
	var folderConcurrency int
	var sequential bool
	var skipExisting bool
	var skipFolderConflicts bool
	var mergeFolderConflicts bool
	var checkConflicts bool

	cmd := &cobra.Command{
		Use:   "upload-dir <directory>",
		Short: "Upload entire directory to Rescale",
		Long: `Upload an entire local directory to Rescale, preserving folder structure.

Folder conflict handling modes (mutually exclusive):
  --skip-folder-conflicts, -S   Skip folders that already exist on Rescale
  --merge-folder-conflicts, -m  Merge into existing folders (upload files to them)

If no conflict flag is provided, you will be prompted interactively.

Examples:
  # Upload directory to root (My Library) - will prompt for conflicts
  rescale-int folders upload-dir ./my_project

  # Upload to specific parent folder
  rescale-int folders upload-dir ./data --parent-id abc123

  # Merge into existing folders (skip existing files)
  rescale-int folders upload-dir ./data --merge-folder-conflicts

  # Continue on errors, include hidden files
  rescale-int folders upload-dir ./data --continue-on-error --include-hidden`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			localPath := filepath.Clean(args[0]) // Normalize path (removes trailing slash for consistency)
			logger := GetLogger()

			// Start timing
			startTime := time.Now()

			// Validate directory exists
			fileInfo, err := os.Stat(localPath)
			if err != nil {
				return fmt.Errorf("failed to access directory: %w", err)
			}
			if !fileInfo.IsDir() {
				return fmt.Errorf("path is not a directory: %s", localPath)
			}

			// Validate max-concurrent
			if maxConcurrent < constants.MinMaxConcurrent || maxConcurrent > constants.MaxMaxConcurrent {
				return fmt.Errorf("--max-concurrent must be between %d and %d, got %d",
					constants.MinMaxConcurrent, constants.MaxMaxConcurrent, maxConcurrent)
			}

			// Validate folder-concurrency
			if folderConcurrency < 1 || folderConcurrency > 30 {
				return fmt.Errorf("--folder-concurrency must be between 1 and 30, got %d", folderConcurrency)
			}

			// Validate folder conflict flags (only one can be set)
			conflictFlags := 0
			if skipFolderConflicts {
				conflictFlags++
			}
			if mergeFolderConflicts {
				conflictFlags++
			}
			if skipExisting {
				conflictFlags++
			}
			if conflictFlags > 1 {
				return fmt.Errorf("only one of --skip-folder-conflicts, --merge-folder-conflicts, or --skip-existing can be specified")
			}

			// Handle legacy --skip-existing flag (maps to merge-folder-conflicts behavior)
			if skipExisting {
				mergeFolderConflicts = true
			}

			// Load config
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Apply CLI flag to config
			cfg.CheckConflictsBeforeUpload = checkConflicts

			// Create API client
			apiClient, err := api.NewClient(cfg)
			if err != nil {
				return fmt.Errorf("failed to create API client: %w", err)
			}

			ctx := GetContext()

			// Get parent folder ID (default to My Library)
			if parentID == "" {
				folders, err := apiClient.GetRootFolders(ctx)
				if err != nil {
					return fmt.Errorf("failed to get root folders: %w", err)
				}
				parentID = folders.MyLibrary
				logger.Info().Str("parent_id", parentID).Msg("Using My Library as parent")
			}

			// Build directory tree
			logger.Info().Str("path", localPath).Bool("include_hidden", includeHidden).Msg("Scanning directory")
			directories, files, symlinks, err := BuildDirectoryTree(localPath, includeHidden)
			if err != nil {
				return fmt.Errorf("failed to scan directory: %w", err)
			}

			// Notify about skipped symlinks
			if len(symlinks) > 0 {
				fmt.Printf("\nℹ️  Skipped %d symbolic link(s):\n", len(symlinks))
				for _, link := range symlinks {
					relPath, _ := filepath.Rel(localPath, link)
					fmt.Printf("  - %s\n", relPath)
				}
			}

			fmt.Printf("\n📊 Scan complete:\n")
			fmt.Printf("  Directories: %d\n", len(directories))
			fmt.Printf("  Files: %d\n", len(files))
			if len(symlinks) > 0 {
				fmt.Printf("  Symlinks (skipped): %d\n", len(symlinks))
			}
			fmt.Println()

			// Initialize folder cache for API call optimization
			cache := NewFolderCache()

			// Create root folder
			rootFolderName := filepath.Base(localPath)
			logger.Info().Str("name", rootFolderName).Str("parent", parentID).Msg("Creating/checking root folder")

			rootFolderID, exists, err := CheckFolderExists(ctx, apiClient, cache, parentID, rootFolderName)
			if err != nil {
				return fmt.Errorf("failed to check root folder: %w", err)
			}

			rootFolderCreated := false
			if exists {
				fmt.Printf("📁 Root folder '%s' already exists\n", rootFolderName)

				// Handle root folder conflict based on flags
				if skipFolderConflicts {
					return fmt.Errorf("cannot skip root folder with --skip-folder-conflicts - upload cancelled")
				} else if mergeFolderConflicts {
					// Merge mode: use existing folder
					fmt.Printf("✓ Using existing root folder (ID: %s)\n\n", rootFolderID)
				} else {
					// No flag set - prompt user
					if !IsTerminal() {
						return fmt.Errorf("folder conflict handling required in non-interactive mode: use --skip-folder-conflicts or --merge-folder-conflicts")
					}
					action, err := promptFolderConflict(rootFolderName)
					if err != nil {
						return err
					}
					switch action {
					case ConflictAbort:
						return fmt.Errorf("upload cancelled by user")
					case ConflictSkipOnce, ConflictSkipAll:
						return fmt.Errorf("cannot skip root folder - upload cancelled")
					case ConflictMergeOnce:
						// Continue with merge for this folder only
						fmt.Printf("✓ Using existing root folder (ID: %s)\n\n", rootFolderID)
					case ConflictMergeAll:
						// Continue with merge and set flag for all subsequent folders
						mergeFolderConflicts = true
						fmt.Printf("✓ Using existing root folder (ID: %s)\n\n", rootFolderID)
					}
				}
			} else {
				fmt.Printf("📁 Creating root folder '%s'...\n", rootFolderName)
				rootFolderID, err = apiClient.CreateFolder(ctx, rootFolderName, parentID)
				if err != nil {
					return fmt.Errorf("failed to create root folder: %w", err)
				}
				// Populate cache for root folder
				_, err = cache.Get(ctx, apiClient, rootFolderID)
				if err != nil {
					logger.Warn().Str("folder_id", rootFolderID).Err(err).Msg("Failed to populate cache for root folder")
				}
				fmt.Printf("✓ Created root folder (ID: %s)\n\n", rootFolderID)
				rootFolderCreated = true
			}

			var result *UploadResult
			var foldersCreated int

			if sequential {
				// Sequential mode: create all folders first, then upload all files
				fmt.Println("📂 Creating folder structure...")

				// Determine folder conflict mode based on flags
				var folderConflictMode ConflictAction
				if skipFolderConflicts {
					folderConflictMode = ConflictSkipAll
				} else if mergeFolderConflicts {
					folderConflictMode = ConflictMergeAll
				} else {
					folderConflictMode = ConflictMergeOnce // Will prompt for each
				}

				mapping, created, err := CreateFolderStructure(
					ctx, apiClient, cache, localPath, directories, rootFolderID, &folderConflictMode, folderConcurrency, logger, nil, os.Stdout)
				if err != nil {
					return fmt.Errorf("failed to create folder structure: %w", err)
				}
				foldersCreated = created
				fmt.Printf("✓ Folder structure created (%d new folders)\n", foldersCreated)

				// Upload files
				fmt.Println("\n📤 Uploading files...")
				uploadUI := progress.NewUploadUI(len(files))
				defer uploadUI.Wait()

				// Cache folder paths for display
				for localDir, folderID := range mapping {
					relativePath, _ := filepath.Rel(localPath, localDir)
					if relativePath == "." {
						relativePath = filepath.Base(localPath)
					}
					uploadUI.SetFolderPath(folderID, relativePath)
				}

				// File conflict mode: merge-folder-conflicts means skip existing files
				fileConflictMode := FileOverwriteOnce
				if mergeFolderConflicts {
					fileConflictMode = FileIgnoreAll
				}
				errorMode := ErrorContinueOnce
				uploadResult, err := uploadFiles(
					ctx, localPath, files, mapping, apiClient, cache, uploadUI,
					&fileConflictMode, &errorMode, continueOnError, maxConcurrent, cfg, logger)
				if err != nil {
					return err
				}
				result = uploadResult
			} else {
				// Pipelined mode (default): create folders and upload files in parallel
				fmt.Println("📂 Starting pipelined folder creation and file upload...")

				// Convert flags to single skipExisting for pipelined mode
				// Note: pipelined mode currently only supports merge behavior
				effectiveSkipExisting := mergeFolderConflicts || skipFolderConflicts

				uploadResult, created, err := uploadDirectoryPipelined(
					ctx, apiClient, cache, localPath, directories, rootFolderID,
					files, folderConcurrency, maxConcurrent, continueOnError, effectiveSkipExisting, cfg, logger)
				if err != nil {
					return err
				}
				result = uploadResult
				foldersCreated = created
			}

			// Add root folder to count if it was created
			if rootFolderCreated {
				foldersCreated++
			}
			result.FoldersCreated = foldersCreated

			fmt.Println() // Add blank line after progress bars

			// Save symlinks list to result
			result.SymlinksSkipped = symlinks
			result.FoldersCreated = foldersCreated

			// Display summary
			fmt.Printf("\n%s\n", strings.Repeat("=", 60))
			fmt.Println("📊 Upload Summary")
			fmt.Println(strings.Repeat("=", 60))
			fmt.Printf("  Folders created:    %d\n", result.FoldersCreated)
			fmt.Printf("  Files uploaded:     %d\n", result.FilesUploaded)
			if result.FilesSkipped > 0 {
				fmt.Printf("  Files skipped:      %d (parent folder skipped)\n", result.FilesSkipped)
			}
			if result.FilesIgnored > 0 {
				fmt.Printf("  Files ignored:      %d (already existed)\n", result.FilesIgnored)
			}
			if len(symlinks) > 0 {
				fmt.Printf("  Symlinks skipped:   %d\n", len(symlinks))
			}
			if len(result.Errors) > 0 {
				// Categorize errors
				diskSpaceErrors := 0
				otherErrors := 0
				for _, e := range result.Errors {
					if diskspace.IsInsufficientSpaceError(e.Error) {
						diskSpaceErrors++
					} else {
						otherErrors++
					}
				}

				fmt.Printf("  Errors:             %d", len(result.Errors))
				if diskSpaceErrors > 0 {
					fmt.Printf(" (%d disk space, %d other)", diskSpaceErrors, otherErrors)
				}
				fmt.Println()

				// Show disk space errors first (if any)
				if diskSpaceErrors > 0 {
					fmt.Println("\n💾 Disk space errors:")
					for _, e := range result.Errors {
						if diskspace.IsInsufficientSpaceError(e.Error) {
							relPath, _ := filepath.Rel(localPath, e.FilePath)
							fmt.Printf("  - %s: %v\n", relPath, e.Error)
						}
					}
				}

				// Show other errors
				if otherErrors > 0 {
					fmt.Println("\n❌ Other upload failures:")
					for _, e := range result.Errors {
						if !diskspace.IsInsufficientSpaceError(e.Error) {
							relPath, _ := filepath.Rel(localPath, e.FilePath)
							fmt.Printf("  - %s: %v\n", relPath, e.Error)
						}
					}
				}
			}
			// Calculate metrics
			elapsed := time.Since(startTime)
			if result.TotalBytes > 0 {
				avgSpeedMBps := float64(result.TotalBytes) / elapsed.Seconds() / (1024 * 1024)
				fmt.Printf("  Total data:         %.2f MB\n", float64(result.TotalBytes)/(1024*1024))
				fmt.Printf("  Elapsed time:       %.2f seconds\n", elapsed.Seconds())
				fmt.Printf("  Average speed:      %.2f MB/s\n", avgSpeedMBps)
			} else {
				fmt.Printf("  Elapsed time:       %.2f seconds\n", elapsed.Seconds())
			}
			fmt.Println(strings.Repeat("=", 60))

			if result.FilesUploaded > 0 {
				fmt.Println("✓ Upload completed successfully")
			} else {
				fmt.Println("⚠️  No files were uploaded")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&parentID, "parent-id", "", "Parent folder ID (default: My Library root)")
	cmd.Flags().BoolVar(&continueOnError, "continue-on-error", false, "Continue uploading on errors without prompting")
	cmd.Flags().BoolVar(&includeHidden, "include-hidden", false, "Include hidden files (starting with .)")
	cmd.Flags().IntVar(&maxConcurrent, "max-concurrent", constants.DefaultMaxConcurrent,
		fmt.Sprintf("Maximum concurrent file uploads (%d-%d)", constants.MinMaxConcurrent, constants.MaxMaxConcurrent))
	cmd.Flags().IntVar(&folderConcurrency, "folder-concurrency", 15, "Maximum concurrent folder operations (1-30)")
	cmd.Flags().BoolVar(&sequential, "sequential", false, "Use sequential mode (create all folders, then upload all files)")
	cmd.Flags().BoolVarP(&skipFolderConflicts, "skip-folder-conflicts", "S", false, "Skip folders that already exist on Rescale")
	cmd.Flags().BoolVarP(&mergeFolderConflicts, "merge-folder-conflicts", "m", false, "Merge into existing folders (skip existing files)")
	cmd.Flags().BoolVar(&skipExisting, "skip-existing", false, "DEPRECATED: Use --merge-folder-conflicts instead")
	cmd.Flags().BoolVar(&checkConflicts, "check-conflicts", false, "Check for existing files before upload (slower but shows conflicts upfront)")
	cmd.Flags().MarkHidden("skip-existing") // Hide deprecated flag

	return cmd
}

// newFoldersDeleteCmd creates the 'folders delete' command.
func newFoldersDeleteCmd() *cobra.Command {
	var folderID string
	var confirm bool

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a folder",
		Long: `Delete a folder and optionally its contents.

WARNING: This operation cannot be undone!

Example:
  rescale-int folders delete --folder-id XxYyZz`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			if folderID == "" {
				return fmt.Errorf("--folder-id is required")
			}

			// Confirmation prompt
			if !confirm {
				fmt.Printf("You are about to delete folder %s. This cannot be undone.\n", folderID)
				fmt.Print("Are you sure? (yes/no): ")
				var response string
				fmt.Scanln(&response)
				if response != "yes" {
					fmt.Println("Deletion cancelled")
					return nil
				}
			}

			logger.Info().Str("folder_id", folderID).Msg("Deleting folder")

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

			// Delete folder
			if err := apiClient.DeleteFolder(ctx, folderID); err != nil {
				return fmt.Errorf("failed to delete folder: %w", err)
			}

			logger.Info().Str("folder_id", folderID).Msg("Folder deleted")
			fmt.Printf("✓ Folder deleted successfully\n")

			return nil
		},
	}

	cmd.Flags().StringVar(&folderID, "folder-id", "", "Folder ID to delete (required)")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "Skip confirmation prompt")

	cmd.MarkFlagRequired("folder-id")

	return cmd
}

// newFoldersDownloadDirCmd creates the 'folders download-dir' command.
func newFoldersDownloadDirCmd() *cobra.Command {
	var folderID string
	var outputDir string
	var continueOnError bool
	var maxConcurrent int
	var overwriteAll bool
	var skipAll bool
	var mergeAll bool
	var skipChecksum bool
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "download-dir <folder-id>",
		Short: "Download entire folder from Rescale",
		Long: `Download an entire Rescale folder recursively, preserving folder structure.

Conflict handling modes (mutually exclusive):
  --skip      Skip existing files and folders
  --overwrite Overwrite existing files (merge into existing folders)
  --merge     Merge into existing folders, skip existing files

If no conflict flag is provided, you will be prompted interactively.

Use --dry-run to preview what would happen without actually downloading.

Examples:
  # Download folder to current directory (will prompt for conflict handling)
  rescale-int folders download-dir abc123

  # Preview what would be downloaded (dry-run)
  rescale-int folders download-dir abc123 --dry-run --merge

  # Download to specific output directory
  rescale-int folders download-dir abc123 --outdir ./downloads

  # Continue on errors, overwrite all existing files
  rescale-int folders download-dir abc123 --continue-on-error --overwrite

  # Merge into existing folders (skip existing files)
  rescale-int folders download-dir abc123 --merge`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			folderID = args[0]
			logger := GetLogger()

			// Start timing
			startTime := time.Now()

			// Validate output directory
			if outputDir == "" {
				outputDir = "."
			}

			// Validate max-concurrent
			if maxConcurrent < constants.MinMaxConcurrent || maxConcurrent > constants.MaxMaxConcurrent {
				return fmt.Errorf("--max-concurrent must be between %d and %d, got %d",
					constants.MinMaxConcurrent, constants.MaxMaxConcurrent, maxConcurrent)
			}

			// Validate conflict flags (only one can be set)
			conflictFlags := 0
			if overwriteAll {
				conflictFlags++
			}
			if skipAll {
				conflictFlags++
			}
			if mergeAll {
				conflictFlags++
			}
			if conflictFlags > 1 {
				return fmt.Errorf("only one of --overwrite, --skip, or --merge can be specified")
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

			ctx := GetContext()

			// Use helper function for recursive download
			result, err := DownloadFolderRecursive(
				ctx, folderID, outputDir, overwriteAll, skipAll, mergeAll, continueOnError, maxConcurrent, skipChecksum, dryRun, apiClient, logger)
			if err != nil {
				return err
			}

			// Display summary
			fmt.Printf("\n%s\n", strings.Repeat("=", 60))
			fmt.Println("📊 Download Summary")
			fmt.Println(strings.Repeat("=", 60))
			fmt.Printf("  Folders created:    %d\n", result.FoldersCreated)
			fmt.Printf("  Files downloaded:   %d\n", result.FilesDownloaded)
			if result.FilesSkipped > 0 {
				fmt.Printf("  Files skipped:      %d (already existed)\n", result.FilesSkipped)
			}
			if result.FilesFailed > 0 {
				fmt.Printf("  Files failed:       %d\n", result.FilesFailed)
			}
			fmt.Printf("  Total data:         %.2f MB\n", float64(result.TotalBytes)/(1024*1024))
			fmt.Printf("  Elapsed time:       %s\n", time.Since(startTime).Round(time.Second))

			avgSpeed := float64(result.TotalBytes) / time.Since(startTime).Seconds() / (1024 * 1024)
			fmt.Printf("  Average speed:      %.1f MiB/s\n", avgSpeed)

			fmt.Println(strings.Repeat("=", 60))

			if result.FilesFailed > 0 {
				return fmt.Errorf("some files failed to download")
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&outputDir, "outdir", "o", ".", "Output directory for downloaded files")
	cmd.Flags().BoolVar(&continueOnError, "continue-on-error", false, "Continue downloading other files if one fails")
	cmd.Flags().IntVar(&maxConcurrent, "max-concurrent", constants.DefaultMaxConcurrent,
		fmt.Sprintf("Maximum concurrent downloads (%d-%d)", constants.MinMaxConcurrent, constants.MaxMaxConcurrent))
	cmd.Flags().BoolVarP(&overwriteAll, "overwrite", "w", false, "Overwrite existing files without prompting")
	cmd.Flags().BoolVarP(&skipAll, "skip", "S", false, "Skip existing files/folders without prompting")
	cmd.Flags().BoolVarP(&mergeAll, "merge", "m", false, "Merge into existing folders, skip existing files")
	cmd.Flags().BoolVar(&skipChecksum, "skip-checksum", false, "Skip checksum verification (not recommended, allows corrupted downloads)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview what would be downloaded without actually downloading")

	return cmd
}
