package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/rescale/rescale-int/internal/progress"
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

// promptWriter returns where an interactive prompt should write. While progress
// bars are live mpb owns the terminal, so the menu has to go through mpb's writer
// or the next frame paints over the question the user is answering.
func promptWriter() io.Writer {
	if w := progress.LogSink(); w != nil {
		return w
	}
	return os.Stdout
}

// promptf writes prompt text to the right place — see promptWriter.
func promptf(format string, args ...interface{}) {
	fmt.Fprintf(promptWriter(), format, args...)
}

// errPromptNeedsTerminal explains that a choice cannot be asked for, and names
// the flags that make it up front. Without this the caller only sees "EOF".
func errPromptNeedsTerminal(what, flags string) error {
	return fmt.Errorf("cannot prompt for %s: no interactive terminal (stdin is not a TTY) — decide up front with %s", what, flags)
}

// ConflictAction type and constants live in internal/transfer/folder/conflict.go;
// folder_upload_compat.go re-exports them for use within this package.

// promptFolderConflict asks user what to do when folder already exists
func promptFolderConflict(folderName string) (ConflictAction, error) {
	if !IsTerminal() {
		return ConflictAbort, errPromptNeedsTerminal("a folder conflict",
			"--merge-folder-conflicts or --skip-folder-conflicts")
	}

	promptf("\n⚠️  Folder '%s' already exists.\n", folderName)
	promptf("What would you like to do?\n")
	promptf("  1. Skip (once) - Skip this folder only\n")
	promptf("  2. Skip (for all) - Skip all existing folders\n")
	promptf("  3. Merge (once) - Use existing folder, prompt for next\n")
	promptf("  4. Merge (for all) - Use all existing folders\n")
	promptf("  5. Abort - Stop upload\n")
	promptf("Choose [1-5]: ")

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
		promptf("Invalid choice, please try again.\n")
		return promptFolderConflict(folderName)
	}
}

// FileConflictAction represents user choice for file upload conflicts (remote file exists)
type FileConflictAction int

const (
	FileSkipOnce FileConflictAction = iota
	FileSkipAll
	FileOverwriteOnce
	FileOverwriteAll
	FileAbort
)

// promptFileConflict asks user what to do when file already exists
func promptFileConflict(fileName, folderPath string) (FileConflictAction, error) {
	if !IsTerminal() {
		return FileAbort, errPromptNeedsTerminal("a file conflict",
			"--merge-folder-conflicts (keep existing files) or --skip-folder-conflicts")
	}

	promptf("\n⚠️  File '%s' already exists in folder '%s'.\n", fileName, folderPath)
	promptf("What would you like to do?\n")
	promptf("  1. Skip (once) - Skip this file only\n")
	promptf("  2. Skip (for all) - Skip all existing files\n")
	promptf("  3. Overwrite (once) - Replace this file, prompt for next\n")
	promptf("  4. Overwrite (for all) - Replace all existing files\n")
	promptf("  5. Abort - Stop upload\n")
	promptf("Choose [1-5]: ")

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
		promptf("Invalid choice, please try again.\n")
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
	if !IsTerminal() {
		return DownloadAbort, errPromptNeedsTerminal("a download conflict",
			"this command's conflict flags (--overwrite or --skip; see --help for the rest)")
	}

	promptf("\n⚠️  File '%s' already exists at '%s'.\n", fileName, localPath)
	promptf("What would you like to do?\n")
	promptf("  1. Skip (once) - Skip this file only\n")
	promptf("  2. Skip (for all) - Skip all existing files\n")
	promptf("  3. Overwrite (once) - Replace this file, prompt for next\n")
	promptf("  4. Overwrite (for all) - Replace all existing files\n")
	promptf("  5. Resume (once) - Try to resume download, prompt for next\n")
	promptf("  6. Resume (for all) - Try to resume all downloads\n")
	promptf("  7. Abort - Stop download\n")
	promptf("Choose [1-7]: ")

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
		promptf("Invalid choice, please try again.\n")
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
	if !IsTerminal() {
		return FolderDownloadAbort, errPromptNeedsTerminal("a folder download conflict",
			"--overwrite, --skip or --merge")
	}

	promptf("\n⚠️  Folder '%s' already exists at '%s'.\n", folderName, localPath)
	promptf("What would you like to do?\n")
	promptf("  1. Skip folder (once) - Don't download this folder\n")
	promptf("  2. Skip folder (for all) - Skip all existing folders\n")
	promptf("  3. Merge folder (once) - Download into existing, skip existing files\n")
	promptf("  4. Merge folder (for all) - Merge all existing folders\n")
	promptf("  5. Abort - Stop download\n")
	promptf("Choose [1-5]: ")

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
		promptf("Invalid choice, please try again.\n")
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
	if !IsTerminal() {
		return FolderDownloadModePrompt, errPromptNeedsTerminal("conflict handling",
			"--overwrite, --skip or --merge")
	}

	promptf("\n⚠️  Conflict handling not specified.\n")
	promptf("\n")
	promptf("The download destination may contain existing files or folders.\n")
	promptf("What should happen if conflicts are found?\n")
	promptf("\n")
	promptf("  1. Prompt for each conflict (interactive)\n")
	promptf("  2. Skip existing files/folders automatically\n")
	promptf("  3. Overwrite existing files automatically\n")
	promptf("  4. Merge folders (download into existing, skip existing files)\n")
	promptf("  5. Abort\n")
	promptf("\nChoose [1-5]: ")

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
		promptf("Invalid choice, please try again.\n")
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
	if !IsTerminal() {
		return UploadDuplicateModeNoCheck, errPromptNeedsTerminal("duplicate handling",
			"--check-duplicates, --no-check-duplicates, --skip-duplicates or --allow-duplicates")
	}

	promptf("\n⚠️  Duplicate checking mode not specified.\n")
	promptf("\n")
	promptf("Rescale allows uploading files with the same name (they become separate objects).\n")
	promptf("Without checking, files may be uploaded multiple times.\n")
	promptf("\n")
	promptf("What would you like to do?\n")
	promptf("  1. Check for duplicates (1 API call per destination folder, cached)\n")
	promptf("  2. Upload without checking (faster, may create duplicates)\n")
	promptf("  3. Abort\n")
	promptf("\nChoose [1-3]: ")

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
		promptf("Invalid choice, please try again.\n")
		return promptUploadDuplicateMode()
	}
}

// UploadConflictAction represents user choice for individual file upload conflicts (duplicate exists)
type UploadConflictAction int

const (
	UploadSkipOnce UploadConflictAction = iota
	UploadSkipAll
	UploadOverwriteOnce
	UploadOverwriteAll
	UploadAbort
)

// promptUploadConflict asks user what to do when a file already exists in the destination
func promptUploadConflict(fileName string, existingChecksum string) (UploadConflictAction, error) {
	if !IsTerminal() {
		return UploadAbort, errPromptNeedsTerminal("a duplicate file",
			"--skip-duplicates or --allow-duplicates")
	}

	promptf("\n⚠️  File '%s' already exists in destination", fileName)
	if existingChecksum != "" {
		promptf(" (matching SHA-512)")
	}
	promptf(".\n")
	promptf("What would you like to do?\n")
	promptf("  1. Skip (once) - Don't upload this file\n")
	promptf("  2. Skip (for all) - Skip all duplicates\n")
	promptf("  3. Overwrite (once) - Replace existing file\n")
	promptf("  4. Overwrite (for all) - Replace all duplicates\n")
	promptf("  5. Abort - Stop upload\n")
	promptf("Choose [1-5]: ")

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
		promptf("Invalid choice, please try again.\n")
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
	if !IsTerminal() {
		return ErrorAbort, errPromptNeedsTerminal("how to handle an upload error",
			"--continue-on-error")
	}

	promptf("\n❌ Error uploading '%s': %v\n", fileName, err)
	promptf("What would you like to do?\n")
	promptf("  1. Continue (once) - Skip this file, prompt for next error\n")
	promptf("  2. Continue (for all) - Skip all errors\n")
	promptf("  3. Abort - Stop upload\n")
	promptf("Choose [1-3]: ")

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
		promptf("Invalid choice, please try again.\n")
		return promptUploadError(fileName, err)
	}
}

// confirmDestructive asks the user to type "yes" before something irreversible.
//
// Returns false with no error when the user declines — that is their decision,
// not a failure. Without a terminal there is nobody to ask, so it errors and
// names the flag that skips the question: reading EOF and treating it as "no"
// made a scripted delete print "Cancelled" and exit 0, so a pipeline could not
// tell a refused delete from a completed one.
func confirmDestructive(action, flagHint string) (bool, error) {
	if !IsTerminal() {
		return false, fmt.Errorf("%s needs confirmation but stdin is not a terminal — re-run with %s", action, flagHint)
	}

	promptf("Are you sure? (yes/no): ")
	answer, err := readPromptLine(bufio.NewReader(os.Stdin))
	if err != nil {
		return false, err
	}
	return strings.EqualFold(answer, "yes"), nil
}

// PromptProxyPassword prompts the user to enter their proxy password securely.
// The password is read without echoing characters to the terminal.
// Returns the entered password or an error if the prompt fails.
func PromptProxyPassword(proxyUser, proxyHost string) (string, error) {
	promptf("Proxy authentication required for %s@%s\n", proxyUser, proxyHost)
	promptf("Enter proxy password: ")

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
