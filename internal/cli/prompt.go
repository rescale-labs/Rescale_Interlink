package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// =============================================================================
// Unified Conflict Resolution System
// =============================================================================
//
// This package provides a consistent conflict resolution framework for all
// file and folder operations. The unified types use consistent naming:
//
//   - Skip:      Don't process this item (was also called "Ignore")
//   - Overwrite: Replace existing item (was also called "Anyway" for uploads)
//   - Merge:     For folders only - use existing folder, process contents
//   - Resume:    For downloads only - continue interrupted transfer
//   - Continue:  For errors only - skip the error and proceed
//   - Abort:     Stop the entire operation
//
// Suffixes:
//   - Once: Apply to this item only, prompt again for next conflict
//   - All:  Apply to all remaining conflicts of this type
//
// =============================================================================

// ConflictAction represents user choice for folder upload conflicts (remote folder exists)
type ConflictAction int

const (
	ConflictSkipOnce ConflictAction = iota
	ConflictSkipAll
	ConflictMergeOnce
	ConflictMergeAll
	ConflictAbort
)

// promptFolderConflict asks user what to do when folder already exists
func promptFolderConflict(folderName string) (ConflictAction, error) {
	fmt.Printf("\n⚠️  Folder '%s' already exists.\n", folderName)
	fmt.Println("What would you like to do?")
	fmt.Println("  1. Skip (once) - Skip this folder only")
	fmt.Println("  2. Skip (for all) - Skip all existing folders")
	fmt.Println("  3. Merge (once) - Use existing folder, prompt for next")
	fmt.Println("  4. Merge (for all) - Use all existing folders")
	fmt.Println("  5. Abort - Stop upload")
	fmt.Print("Choose [1-5]: ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return ConflictAbort, err
	}

	input = strings.TrimSpace(input)
	switch input {
	case "1":
		return ConflictSkipOnce, nil
	case "2":
		return ConflictSkipAll, nil
	case "3":
		return ConflictMergeOnce, nil
	case "4":
		return ConflictMergeAll, nil
	case "5":
		return ConflictAbort, nil
	default:
		fmt.Println("Invalid choice, please try again.")
		return promptFolderConflict(folderName)
	}
}

// FileConflictAction represents user choice for file upload conflicts (remote file exists)
// Note: Uses Skip (consistent naming) - FileIgnore* aliases retained for backward compatibility
type FileConflictAction int

const (
	FileSkipOnce FileConflictAction = iota
	FileSkipAll
	FileOverwriteOnce
	FileOverwriteAll
	FileAbort

	// Backward compatibility aliases (deprecated - use FileSkip* instead)
	FileIgnoreOnce = FileSkipOnce
	FileIgnoreAll  = FileSkipAll
)

// promptFileConflict asks user what to do when file already exists
func promptFileConflict(fileName, folderPath string) (FileConflictAction, error) {
	fmt.Printf("\n⚠️  File '%s' already exists in folder '%s'.\n", fileName, folderPath)
	fmt.Println("What would you like to do?")
	fmt.Println("  1. Skip (once) - Skip this file only")
	fmt.Println("  2. Skip (for all) - Skip all existing files")
	fmt.Println("  3. Overwrite (once) - Replace this file, prompt for next")
	fmt.Println("  4. Overwrite (for all) - Replace all existing files")
	fmt.Println("  5. Abort - Stop upload")
	fmt.Print("Choose [1-5]: ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return FileAbort, err
	}

	input = strings.TrimSpace(input)
	switch input {
	case "1":
		return FileSkipOnce, nil
	case "2":
		return FileSkipAll, nil
	case "3":
		return FileOverwriteOnce, nil
	case "4":
		return FileOverwriteAll, nil
	case "5":
		return FileAbort, nil
	default:
		fmt.Println("Invalid choice, please try again.")
		return promptFileConflict(fileName, folderPath)
	}
}

// DownloadConflictAction represents user choice for download file conflicts (local file exists)
type DownloadConflictAction int

const (
	DownloadSkipOnce DownloadConflictAction = iota
	DownloadSkipAll
	DownloadOverwriteOnce
	DownloadOverwriteAll
	DownloadResumeOnce
	DownloadResumeAll
	DownloadAbort
)

// promptDownloadConflict asks user what to do when download file already exists
func promptDownloadConflict(fileName, localPath string) (DownloadConflictAction, error) {
	fmt.Printf("\n⚠️  File '%s' already exists at '%s'.\n", fileName, localPath)
	fmt.Println("What would you like to do?")
	fmt.Println("  1. Skip (once) - Skip this file only")
	fmt.Println("  2. Skip (for all) - Skip all existing files")
	fmt.Println("  3. Overwrite (once) - Replace this file, prompt for next")
	fmt.Println("  4. Overwrite (for all) - Replace all existing files")
	fmt.Println("  5. Resume (once) - Try to resume download, prompt for next")
	fmt.Println("  6. Resume (for all) - Try to resume all downloads")
	fmt.Println("  7. Abort - Stop download")
	fmt.Print("Choose [1-7]: ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return DownloadAbort, err
	}

	input = strings.TrimSpace(input)
	switch input {
	case "1":
		return DownloadSkipOnce, nil
	case "2":
		return DownloadSkipAll, nil
	case "3":
		return DownloadOverwriteOnce, nil
	case "4":
		return DownloadOverwriteAll, nil
	case "5":
		return DownloadResumeOnce, nil
	case "6":
		return DownloadResumeAll, nil
	case "7":
		return DownloadAbort, nil
	default:
		fmt.Println("Invalid choice, please try again.")
		return promptDownloadConflict(fileName, localPath)
	}
}

// FolderDownloadConflictAction represents user choice for folder download conflicts
type FolderDownloadConflictAction int

const (
	FolderDownloadSkipOnce FolderDownloadConflictAction = iota
	FolderDownloadSkipAll
	FolderDownloadMergeOnce
	FolderDownloadMergeAll
	FolderDownloadAbort
)

// promptFolderDownloadConflict asks user what to do when a local folder already exists
func promptFolderDownloadConflict(folderName, localPath string) (FolderDownloadConflictAction, error) {
	fmt.Printf("\n⚠️  Folder '%s' already exists at '%s'.\n", folderName, localPath)
	fmt.Println("What would you like to do?")
	fmt.Println("  1. Skip folder (once) - Don't download this folder")
	fmt.Println("  2. Skip folder (for all) - Skip all existing folders")
	fmt.Println("  3. Merge folder (once) - Download into existing, skip existing files")
	fmt.Println("  4. Merge folder (for all) - Merge all existing folders")
	fmt.Println("  5. Abort - Stop download")
	fmt.Print("Choose [1-5]: ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return FolderDownloadAbort, err
	}

	input = strings.TrimSpace(input)
	switch input {
	case "1":
		return FolderDownloadSkipOnce, nil
	case "2":
		return FolderDownloadSkipAll, nil
	case "3":
		return FolderDownloadMergeOnce, nil
	case "4":
		return FolderDownloadMergeAll, nil
	case "5":
		return FolderDownloadAbort, nil
	default:
		fmt.Println("Invalid choice, please try again.")
		return promptFolderDownloadConflict(folderName, localPath)
	}
}

// FolderDownloadMode represents the overall conflict handling mode for folder downloads
type FolderDownloadMode int

const (
	FolderDownloadModePrompt FolderDownloadMode = iota // Prompt for each conflict
	FolderDownloadModeSkip                             // Skip all conflicts
	FolderDownloadModeOverwrite                        // Overwrite all conflicts
	FolderDownloadModeMerge                            // Merge into existing folders
)

// promptFolderDownloadMode asks user to select overall conflict handling mode
// Returns the selected mode or error. Used when no --skip/--overwrite/--merge flag provided.
func promptFolderDownloadMode() (FolderDownloadMode, error) {
	fmt.Println("\n⚠️  Conflict handling not specified.")
	fmt.Println("")
	fmt.Println("The download destination may contain existing files or folders.")
	fmt.Println("What should happen if conflicts are found?")
	fmt.Println("")
	fmt.Println("  1. Prompt for each conflict (interactive)")
	fmt.Println("  2. Skip existing files/folders automatically")
	fmt.Println("  3. Overwrite existing files automatically")
	fmt.Println("  4. Merge folders (download into existing, skip existing files)")
	fmt.Println("  5. Abort")
	fmt.Print("\nChoose [1-5]: ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return FolderDownloadModePrompt, err
	}

	input = strings.TrimSpace(input)
	switch input {
	case "1":
		return FolderDownloadModePrompt, nil
	case "2":
		return FolderDownloadModeSkip, nil
	case "3":
		return FolderDownloadModeOverwrite, nil
	case "4":
		return FolderDownloadModeMerge, nil
	case "5":
		return FolderDownloadModePrompt, fmt.Errorf("download aborted by user")
	default:
		fmt.Println("Invalid choice, please try again.")
		return promptFolderDownloadMode()
	}
}

// UploadDuplicateMode represents the overall duplicate handling mode for file uploads
type UploadDuplicateMode int

const (
	UploadDuplicateModeNoCheck   UploadDuplicateMode = iota // Don't check for duplicates (fast)
	UploadDuplicateModeCheck                                // Check and prompt for each duplicate
	UploadDuplicateModeSkipAll                              // Check and skip all duplicates
	UploadDuplicateModeUploadAll                            // Check and upload all anyway
)

// promptUploadDuplicateMode asks user to select duplicate handling mode for file uploads
// Returns the selected mode or error. Used when no --check-duplicates flag is provided.
func promptUploadDuplicateMode() (UploadDuplicateMode, error) {
	fmt.Println("\n⚠️  Duplicate checking mode not specified.")
	fmt.Println("")
	fmt.Println("Rescale allows uploading files with the same name (they become separate objects).")
	fmt.Println("Without checking, files may be uploaded multiple times.")
	fmt.Println("")
	fmt.Println("What would you like to do?")
	fmt.Println("  1. Check for duplicates (1 API call per destination folder, cached)")
	fmt.Println("  2. Upload without checking (faster, may create duplicates)")
	fmt.Println("  3. Abort")
	fmt.Print("\nChoose [1-3]: ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return UploadDuplicateModeNoCheck, err
	}

	input = strings.TrimSpace(input)
	switch input {
	case "1":
		return UploadDuplicateModeCheck, nil
	case "2":
		return UploadDuplicateModeNoCheck, nil
	case "3":
		return UploadDuplicateModeNoCheck, fmt.Errorf("upload aborted by user")
	default:
		fmt.Println("Invalid choice, please try again.")
		return promptUploadDuplicateMode()
	}
}

// UploadConflictAction represents user choice for individual file upload conflicts (duplicate exists)
// Note: Uses Overwrite (consistent naming) - UploadAnyway* aliases retained for backward compatibility
type UploadConflictAction int

const (
	UploadSkipOnce UploadConflictAction = iota
	UploadSkipAll
	UploadOverwriteOnce
	UploadOverwriteAll
	UploadAbort

	// Backward compatibility aliases (deprecated - use UploadOverwrite* instead)
	UploadAnyway    = UploadOverwriteOnce
	UploadAnywayAll = UploadOverwriteAll
)

// promptUploadConflict asks user what to do when a file already exists in the destination
func promptUploadConflict(fileName string, existingChecksum string) (UploadConflictAction, error) {
	fmt.Printf("\n⚠️  File '%s' already exists in destination", fileName)
	if existingChecksum != "" {
		fmt.Printf(" (matching SHA-512)")
	}
	fmt.Println(".")
	fmt.Println("What would you like to do?")
	fmt.Println("  1. Skip (once) - Don't upload this file")
	fmt.Println("  2. Skip (for all) - Skip all duplicates")
	fmt.Println("  3. Overwrite (once) - Replace existing file")
	fmt.Println("  4. Overwrite (for all) - Replace all duplicates")
	fmt.Println("  5. Abort - Stop upload")
	fmt.Print("Choose [1-5]: ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return UploadAbort, err
	}

	input = strings.TrimSpace(input)
	switch input {
	case "1":
		return UploadSkipOnce, nil
	case "2":
		return UploadSkipAll, nil
	case "3":
		return UploadOverwriteOnce, nil
	case "4":
		return UploadOverwriteAll, nil
	case "5":
		return UploadAbort, nil
	default:
		fmt.Println("Invalid choice, please try again.")
		return promptUploadConflict(fileName, existingChecksum)
	}
}

// ErrorAction represents user choice for error handling
type ErrorAction int

const (
	ErrorContinueOnce ErrorAction = iota
	ErrorContinueAll
	ErrorAbort
)

// promptUploadError asks user what to do when upload fails
func promptUploadError(fileName string, err error) (ErrorAction, error) {
	fmt.Printf("\n❌ Error uploading '%s': %v\n", fileName, err)
	fmt.Println("What would you like to do?")
	fmt.Println("  1. Continue (once) - Skip this file, prompt for next error")
	fmt.Println("  2. Continue (for all) - Skip all errors")
	fmt.Println("  3. Abort - Stop upload")
	fmt.Print("Choose [1-3]: ")

	reader := bufio.NewReader(os.Stdin)
	input, readErr := reader.ReadString('\n')
	if readErr != nil {
		return ErrorAbort, readErr
	}

	input = strings.TrimSpace(input)
	switch input {
	case "1":
		return ErrorContinueOnce, nil
	case "2":
		return ErrorContinueAll, nil
	case "3":
		return ErrorAbort, nil
	default:
		fmt.Println("Invalid choice, please try again.")
		return promptUploadError(fileName, err)
	}
}

// PromptProxyPassword prompts the user to enter their proxy password securely.
// The password is read without echoing characters to the terminal.
// Returns the entered password or an error if the prompt fails.
func PromptProxyPassword(proxyUser, proxyHost string) (string, error) {
	fmt.Printf("Proxy authentication required for %s@%s\n", proxyUser, proxyHost)
	fmt.Print("Enter proxy password: ")

	// Read password without echoing
	passwordBytes, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println() // Print newline after password entry

	if err != nil {
		return "", fmt.Errorf("failed to read password: %w", err)
	}

	password := string(passwordBytes)
	if password == "" {
		return "", fmt.Errorf("password cannot be empty")
	}

	return password, nil
}

// IsTerminal returns true if stdin is connected to a terminal.
// This can be used to determine if interactive prompts are possible.
func IsTerminal() bool {
	return term.IsTerminal(int(syscall.Stdin))
}
