package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// Global variable to track if a scan preview dialog is already showing
// This prevents multiple dialogs from being opened simultaneously
var (
	scanPreviewDialogOpen bool
	scanPreviewMutex      sync.Mutex
)

// ScanPreviewResult holds the results of a directory scan preview
type ScanPreviewResult struct {
	TotalDirs       int
	MatchingDirs    []string
	ValidationFiles map[string][]string // dir -> matching validation files
	EstimatedSizes  map[string]int64    // dir -> estimated tar size
	Errors          []string            // validation failures
}

// ScanPreviewDialog shows a preview of what will be scanned
type ScanPreviewDialog struct {
	window            fyne.Window
	baseDir           string
	pattern           string
	validationPattern string
	runSubpath        string
	recursive         bool
	onConfirm         func(outputPath string) // Called when user confirms
	dialog            dialog.Dialog           // Store dialog reference to close it
}

// NewScanPreviewDialog creates a new scan preview dialog
func NewScanPreviewDialog(
	window fyne.Window,
	baseDir string,
	pattern string,
	validationPattern string,
	runSubpath string,
	recursive bool,
	onConfirm func(string),
) *ScanPreviewDialog {
	return &ScanPreviewDialog{
		window:            window,
		baseDir:           baseDir,
		pattern:           pattern,
		validationPattern: validationPattern,
		runSubpath:        runSubpath,
		recursive:         recursive,
		onConfirm:         onConfirm,
	}
}

// Show displays the preview dialog
func (sp *ScanPreviewDialog) Show() {
	fmt.Printf("DEBUG: ScanPreviewDialog.Show() called, baseDir=%s, pattern=%s\n", sp.baseDir, sp.pattern)

	// Check if a dialog is already open
	scanPreviewMutex.Lock()
	if scanPreviewDialogOpen {
		scanPreviewMutex.Unlock()
		fmt.Println("DEBUG: Scan preview dialog already open, ignoring duplicate request")
		return
	}
	scanPreviewDialogOpen = true
	scanPreviewMutex.Unlock()

	// Show progress dialog immediately
	progressDialog := dialog.NewProgress("Scanning Directories", "Scanning for matching directories...", sp.window)
	progressDialog.Show()

	// Perform scan in background to avoid blocking UI
	go func() {
		// Ensure we reset the flag when goroutine completes
		defer func() {
			scanPreviewMutex.Lock()
			scanPreviewDialogOpen = false
			scanPreviewMutex.Unlock()
		}()
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("PANIC in scan goroutine: %v\n", r)
				fyne.Do(func() {
					progressDialog.Hide()
					dialog.ShowError(
						fmt.Errorf("An unexpected error occurred during directory scan: %v\n\nPlease check the console for details.", r),
						sp.window,
					)
				})
			}
		}()

		fmt.Printf("DEBUG: Starting directory scan\n")
		// Perform scan
		result := sp.scanDirectories()
		fmt.Printf("DEBUG: Scan complete, found %d directories, %d errors\n", result.TotalDirs, len(result.Errors))
		if len(result.Errors) > 0 {
			fmt.Printf("DEBUG: Errors:\n")
			for _, err := range result.Errors {
				fmt.Printf("  - %s\n", err)
			}
		}

		// Build preview UI on main thread
		fyne.Do(func() {
			fmt.Printf("DEBUG: Building preview UI\n")
			sp.buildPreviewUI(result, progressDialog)
			fmt.Printf("DEBUG: Preview UI built\n")
		})
	}()
}

// scanDirectories performs the actual directory scan
func (sp *ScanPreviewDialog) scanDirectories() ScanPreviewResult {
	result := ScanPreviewResult{
		ValidationFiles: make(map[string][]string),
		EstimatedSizes:  make(map[string]int64),
	}

	// Get the scan directory (baseDir + optional subpath)
	scanDir := sp.baseDir
	if sp.runSubpath != "" {
		scanDir = filepath.Join(scanDir, sp.runSubpath)
	}

	// Check if scan directory exists
	if _, err := os.Stat(scanDir); os.IsNotExist(err) {
		result.Errors = append(result.Errors, fmt.Sprintf("Scan directory does not exist: %s", scanDir))
		return result
	}

	// Find matching directories
	entries, err := os.ReadDir(scanDir)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("Failed to read directory: %v", err))
		return result
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		// Skip hidden directories
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		// Check if directory matches pattern
		matched, err := filepath.Match(sp.pattern, entry.Name())
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("Pattern error: %v", err))
			continue
		}

		if !matched {
			continue
		}

		dirPath := filepath.Join(scanDir, entry.Name())
		result.MatchingDirs = append(result.MatchingDirs, entry.Name())

		// Check validation pattern if specified
		if sp.validationPattern != "" {
			validationFiles, err := sp.findValidationFiles(dirPath, sp.validationPattern)
			if err != nil {
				result.Errors = append(result.Errors,
					fmt.Sprintf("%s: validation error: %v", entry.Name(), err))
			} else if len(validationFiles) == 0 {
				result.Errors = append(result.Errors,
					fmt.Sprintf("%s: no files matching validation pattern '%s'", entry.Name(), sp.validationPattern))
			} else {
				result.ValidationFiles[entry.Name()] = validationFiles
			}
		} else {
			// No validation required
			result.ValidationFiles[entry.Name()] = []string{"(no validation)"}
		}

		// Estimate tar size
		size, _ := sp.estimateDirectorySize(dirPath)
		result.EstimatedSizes[entry.Name()] = size
	}

	result.TotalDirs = len(result.MatchingDirs)

	// Sort directories for consistent display
	sort.Strings(result.MatchingDirs)

	return result
}

// findValidationFiles finds files matching the validation pattern in a directory
func (sp *ScanPreviewDialog) findValidationFiles(dirPath string, pattern string) ([]string, error) {
	var matches []string

	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		// Get relative path from directory
		relPath, err := filepath.Rel(dirPath, path)
		if err != nil {
			return err
		}

		// Check if file matches pattern
		matched, err := filepath.Match(pattern, filepath.Base(path))
		if err != nil {
			return err
		}

		if matched {
			matches = append(matches, relPath)
		}

		return nil
	})

	return matches, err
}

// estimateDirectorySize estimates the total size of a directory
func (sp *ScanPreviewDialog) estimateDirectorySize(dirPath string) (int64, error) {
	var totalSize int64

	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			totalSize += info.Size()
		}

		return nil
	})

	return totalSize, err
}

// buildPreviewUI creates the preview dialog UI
func (sp *ScanPreviewDialog) buildPreviewUI(result ScanPreviewResult, progressDialog dialog.Dialog) {
	// If no directories found, show error and return
	if result.TotalDirs == 0 {
		// Hide progress dialog before showing error
		progressDialog.Hide()

		errorMsg := fmt.Sprintf("No directories found matching pattern '%s'\n\nSearched in: %s", sp.pattern, sp.baseDir)
		if sp.runSubpath != "" {
			errorMsg += fmt.Sprintf("\nSubpath: %s", sp.runSubpath)
		}
		errorMsg += "\n\nTips:\n• Check that the base directory path is correct\n• Verify the pattern matches your directory names\n• Make sure directories are not hidden (starting with .)"

		content := widget.NewLabel(errorMsg)
		content.Wrapping = fyne.TextWrapWord

		errorDialog := dialog.NewCustom(
			"No Directories Found",
			"OK",
			container.NewVBox(content),
			sp.window,
		)
		errorDialog.Resize(fyne.NewSize(500, 300))
		errorDialog.Show()
		return
	}

	// If ALL directories have validation errors, show special error dialog
	if len(result.Errors) == result.TotalDirs && len(result.Errors) > 0 {
		fmt.Printf("DEBUG: All %d directories failed validation, showing error dialog\n", result.TotalDirs)

		// CRITICAL: Hide progress dialog BEFORE showing error dialog
		// Otherwise error dialog will disappear immediately
		progressDialog.Hide()

		errorMsg := fmt.Sprintf("Found %d directories matching '%s', but ALL failed validation:\n\n", result.TotalDirs, sp.pattern)
		for _, err := range result.Errors {
			errorMsg += "  • " + err + "\n"
		}
		errorMsg += fmt.Sprintf("\n💡 Your template requires files matching '%s' in each directory.\n", sp.validationPattern)
		errorMsg += "\nOptions:\n"
		errorMsg += "1. Add the required files to your directories, OR\n"
		errorMsg += "2. Edit your template to remove or change the validation pattern, OR\n"
		errorMsg += "3. Disable validation in Setup tab"

		fmt.Printf("DEBUG: About to show validation error dialog\n")

		// Create a custom dialog instead of ShowError to ensure it stays visible
		content := widget.NewLabel(errorMsg)
		content.Wrapping = fyne.TextWrapWord

		errorDialog := dialog.NewCustom(
			"Validation Failed",
			"OK",
			container.NewVBox(
				content,
			),
			sp.window,
		)
		errorDialog.Resize(fyne.NewSize(600, 400))
		errorDialog.Show()

		fmt.Printf("DEBUG: Validation error dialog shown\n")
		return
	}

	// Summary header
	summaryText := fmt.Sprintf("Found %d directories matching \"%s\"", result.TotalDirs, sp.pattern)
	if sp.runSubpath != "" {
		summaryText += fmt.Sprintf(" (in subpath: %s)", sp.runSubpath)
	}

	summaryLabel := widget.NewLabel(summaryText)
	summaryLabel.Wrapping = fyne.TextWrapWord

	// Build table data
	var validCount, invalidCount int
	var totalSize int64

	tableData := [][]string{
		{"Directory", "Validation", "Est. Size"},
	}

	for _, dir := range result.MatchingDirs {
		validationFiles := result.ValidationFiles[dir]
		size := result.EstimatedSizes[dir]
		totalSize += size

		// Check if valid
		validationStr := ""
		if sp.validationPattern != "" {
			if len(validationFiles) == 0 {
				validationStr = "✗ No files"
				invalidCount++
			} else {
				validationStr = fmt.Sprintf("✓ %d file(s)", len(validationFiles))
				validCount++
			}
		} else {
			validationStr = "-"
			validCount++
		}

		sizeStr := formatBytes(size)

		tableData = append(tableData, []string{dir, validationStr, sizeStr})
	}

	// Create scrollable table
	table := widget.NewTable(
		func() (int, int) {
			return len(tableData), 3
		},
		func() fyne.CanvasObject {
			return widget.NewLabel("")
		},
		func(cell widget.TableCellID, obj fyne.CanvasObject) {
			label := obj.(*widget.Label)
			if cell.Row < len(tableData) && cell.Col < len(tableData[cell.Row]) {
				label.SetText(tableData[cell.Row][cell.Col])
				if cell.Row == 0 {
					label.TextStyle = fyne.TextStyle{Bold: true}
				}
			}
		},
	)

	table.SetColumnWidth(0, 200)
	table.SetColumnWidth(1, 150)
	table.SetColumnWidth(2, 100)

	scrollContainer := container.NewScroll(table)
	scrollContainer.SetMinSize(fyne.NewSize(500, 300))

	// Summary statistics
	statsText := fmt.Sprintf("Summary: %d valid, %d invalid\nTotal estimated size: %s",
		validCount, invalidCount, formatBytes(totalSize))
	statsLabel := widget.NewLabel(statsText)

	// Error display (if any)
	var errorContainer *fyne.Container
	if len(result.Errors) > 0 {
		errorText := "Errors:\n" + strings.Join(result.Errors, "\n")
		errorLabel := widget.NewLabel(errorText)
		errorLabel.Wrapping = fyne.TextWrapWord
		errorContainer = container.NewVBox(
			widget.NewSeparator(),
			widget.NewLabel("Issues Found:"),
			errorLabel,
		)
	}

	// Output CSV path - default to base directory where Run_* dirs are located
	defaultFilename := fmt.Sprintf("jobs_%s.csv", formatTimestamp())
	defaultPath := filepath.Join(sp.baseDir, defaultFilename)

	outputEntry := widget.NewEntry()
	outputEntry.SetText(defaultPath)
	outputEntry.SetPlaceHolder(filepath.Join(sp.baseDir, "jobs.csv"))

	outputForm := container.NewBorder(
		nil, nil,
		widget.NewLabel("Output CSV:"),
		nil,
		outputEntry,
	)

	// Build content
	content := container.NewVBox(
		summaryLabel,
		widget.NewSeparator(),
		scrollContainer,
		widget.NewSeparator(),
		statsLabel,
		outputForm,
	)

	if errorContainer != nil {
		content.Add(errorContainer)
	}

	// Create buttons
	var confirmButton *widget.Button
	var cancelButton *widget.Button

	confirmButton = widget.NewButton("Generate Jobs CSV", func() {
		outputPath := strings.TrimSpace(outputEntry.Text)
		if outputPath == "" {
			dialog.ShowError(fmt.Errorf("Please specify an output CSV file name"), sp.window)
			return
		}

		// Close dialog first
		if sp.dialog != nil {
			sp.dialog.Hide()
		}

		// Call confirmation callback
		if sp.onConfirm != nil {
			sp.onConfirm(outputPath)
		}
	})
	confirmButton.Importance = widget.HighImportance

	// Disable if there are validation errors
	if invalidCount > 0 {
		confirmButton.Disable()
		confirmButton.SetText("Cannot Generate (Validation Errors)")
	}

	cancelButton = widget.NewButton("Cancel", func() {
		if sp.dialog != nil {
			sp.dialog.Hide()
		}
	})

	buttons := container.NewHBox(
		cancelButton,
		confirmButton,
	)

	// Hide progress dialog before showing preview
	progressDialog.Hide()

	// Create dialog and store reference
	fmt.Printf("DEBUG: Creating scan preview dialog with %d valid, %d invalid\n", validCount, invalidCount)
	sp.dialog = dialog.NewCustom(
		"Scan Preview",
		"Close",
		container.NewBorder(nil, buttons, nil, nil, content),
		sp.window,
	)
	sp.dialog.Resize(fyne.NewSize(700, 600))
	fmt.Printf("DEBUG: Calling dialog.Show()\n")
	sp.dialog.Show()
	fmt.Printf("DEBUG: dialog.Show() completed\n")
}

// formatBytes formats a byte count as a human-readable string
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// formatTimestamp returns a timestamp string for file naming
func formatTimestamp() string {
	return time.Now().Format("20060102_150405")
}
