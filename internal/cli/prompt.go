package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ConflictAction represents user choice for folder conflicts
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
	fmt.Println("  2. Skip (do for all) - Skip all existing folders")
	fmt.Println("  3. Merge (once) - Use existing folder, prompt for next")
	fmt.Println("  4. Merge (do for all) - Use all existing folders")
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

// FileConflictAction represents user choice for file conflicts
type FileConflictAction int

const (
	FileIgnoreOnce FileConflictAction = iota
	FileIgnoreAll
	FileOverwriteOnce
	FileOverwriteAll
	FileAbort
)

// promptFileConflict asks user what to do when file already exists
func promptFileConflict(fileName, folderPath string) (FileConflictAction, error) {
	fmt.Printf("\n⚠️  File '%s' already exists in folder '%s'.\n", fileName, folderPath)
	fmt.Println("What would you like to do?")
	fmt.Println("  1. Ignore (once) - Skip this file only")
	fmt.Println("  2. Ignore (do for all) - Skip all existing files")
	fmt.Println("  3. Overwrite (once) - Replace this file, prompt for next")
	fmt.Println("  4. Overwrite (do for all) - Replace all existing files")
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
		return FileIgnoreOnce, nil
	case "2":
		return FileIgnoreAll, nil
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

// DownloadConflictAction represents user choice for download file conflicts
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
	fmt.Println("  2. Skip (do for all) - Skip all existing files")
	fmt.Println("  3. Overwrite (once) - Replace this file, prompt for next")
	fmt.Println("  4. Overwrite (do for all) - Replace all existing files")
	fmt.Println("  5. Resume (once) - Try to resume download, prompt for next")
	fmt.Println("  6. Resume (do for all) - Try to resume all downloads")
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
	fmt.Println("  2. Continue (do for all) - Skip all errors")
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
