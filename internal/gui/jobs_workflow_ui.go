package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// validateCSVExtension checks if a file URI has a .csv extension
func validateCSVExtension(uri fyne.URI, window fyne.Window) error {
	fileName := uri.Name()
	if !strings.HasSuffix(strings.ToLower(fileName), ".csv") {
		return fmt.Errorf("Please select a CSV file.\n\nYou selected: %s\n\nOnly .csv files are supported.", fileName)
	}
	return nil
}

// WorkflowUIComponents holds all UI components for the jobs workflow
type WorkflowUIComponents struct {
	jobsTab        *JobsTab
	scanInProgress bool       // Prevents multiple simultaneous scans
	scanMutex      sync.Mutex // Protects scanInProgress
}

// NewWorkflowUIComponents creates a new workflow UI components helper
func NewWorkflowUIComponents(jt *JobsTab) *WorkflowUIComponents {
	return &WorkflowUIComponents{
		jobsTab: jt,
	}
}

// CreateProgressBar creates the breadcrumb progress indicator
func (w *WorkflowUIComponents) CreateProgressBar() *fyne.Container {
	steps := []struct {
		state WorkflowState
		label string
	}{
		{StateInitial, "Start"},
		{StatePathChosen, "Path"},
		{StateTemplateReady, "Template"},
		{StateDirectoriesScanned, "Scan"},
		{StateJobsValidated, "Validate"},
		{StateExecuting, "Run"},
		{StateCompleted, "Done"},
	}

	var stepLabels []*widget.Label
	for _, step := range steps {
		label := widget.NewLabel(step.label)

		// Highlight current state
		if w.jobsTab.workflow.CurrentState == step.state {
			label.TextStyle = fyne.TextStyle{Bold: true}
		}

		// Gray out future states
		if step.state > w.jobsTab.workflow.CurrentState {
			label.Importance = widget.LowImportance
		}

		stepLabels = append(stepLabels, label)
	}

	// Create breadcrumb with arrows
	breadcrumb := container.NewHBox()
	for i, label := range stepLabels {
		breadcrumb.Add(label)
		if i < len(stepLabels)-1 {
			breadcrumb.Add(widget.NewLabel("→"))
		}
	}

	return container.NewVBox(
		breadcrumb,
		widget.NewSeparator(),
	)
}

// CreatePathSelectionView creates the initial path selection UI
func (w *WorkflowUIComponents) CreatePathSelectionView() fyne.CanvasObject {
	loadCSVBtn := widget.NewButton("Load Existing Complete Jobs CSV", func() {
		w.handleLoadCSVPath()
	})
	loadCSVBtn.Importance = widget.HighImportance

	loadDesc := widget.NewLabel("Use this if you already have a complete jobs CSV file ready to run.")
	loadDesc.Wrapping = fyne.TextWrapWord

	createNewBtn := widget.NewButton("Create New Jobs & Directories CSV", func() {
		w.handleCreateNewPath()
	})
	createNewBtn.Importance = widget.HighImportance

	createDesc := widget.NewLabel("Use this to build a jobs CSV by scanning run directories.")
	createDesc.Wrapping = fyne.TextWrapWord

	return container.NewVBox(
		widget.NewLabel("Select how you want to configure jobs:"),
		widget.NewSeparator(),
		container.NewVBox(
			loadCSVBtn,
			loadDesc,
		),
		widget.NewSeparator(),
		container.NewVBox(
			createNewBtn,
			createDesc,
		),
	)
}

// CreateTemplateSelectionView creates the template selection UI
func (w *WorkflowUIComponents) CreateTemplateSelectionView() fyne.CanvasObject {
	selectTemplateBtn := widget.NewButton("Select Template Jobs & Directories CSV", func() {
		w.handleSelectTemplate()
	})
	selectTemplateBtn.Importance = widget.HighImportance

	selectDesc := widget.NewLabel("Use an existing template CSV with one job configuration row.")
	selectDesc.Wrapping = fyne.TextWrapWord

	createTemplateBtn := widget.NewButton("Create New Template", func() {
		w.handleCreateTemplate()
	})
	createTemplateBtn.Importance = widget.HighImportance

	createDesc := widget.NewLabel("Build a template from scratch with default values.")
	createDesc.Wrapping = fyne.TextWrapWord

	return container.NewVBox(
		widget.NewLabel("Configure your job template:"),
		widget.NewSeparator(),
		container.NewVBox(
			selectTemplateBtn,
			selectDesc,
		),
		widget.NewSeparator(),
		container.NewVBox(
			createTemplateBtn,
			createDesc,
		),
	)
}

// CreateDirectoryScanView creates the directory scanning UI
func (w *WorkflowUIComponents) CreateDirectoryScanView() fyne.CanvasObject {
	templateInfo := widget.NewLabel(fmt.Sprintf("Template ready: %s",
		w.jobsTab.workflow.Template.AnalysisCode))
	templateInfo.TextStyle = fyne.TextStyle{Bold: true}

	// Directory selection
	dirEntry := widget.NewEntry()
	if w.jobsTab.workflow.Memory.LastScanDir != "" {
		dirEntry.SetText(w.jobsTab.workflow.Memory.LastScanDir)
	}
	dirEntry.SetPlaceHolder("/path/to/project")

	dirBrowseBtn := widget.NewButton("Browse", func() {
		dialog.ShowFolderOpen(func(dir fyne.ListableURI, err error) {
			if err != nil || dir == nil {
				return
			}
			dirEntry.SetText(dir.Path())
		}, w.jobsTab.window)
	})

	// Pattern
	patternEntry := widget.NewEntry()
	patternEntry.SetPlaceHolder("Run_* or Test_* or Sim_*")
	patternEntry.SetText(w.jobsTab.workflow.Memory.LastPattern)
	if patternEntry.Text == "" {
		patternEntry.SetText("Run_*")
	}

	// Get validation pattern and run subpath from Setup config
	cfg := w.jobsTab.engine.GetConfig()
	validationPattern := ""
	runSubpath := ""
	if cfg != nil {
		validationPattern = cfg.ValidationPattern
		runSubpath = cfg.RunSubpath
	}

	// Info about settings from Setup
	settingsInfo := widget.NewLabel(fmt.Sprintf("Using settings from Setup: Validation=%q, Run Subpath=%q",
		validationPattern, runSubpath))
	settingsInfo.Wrapping = fyne.TextWrapWord
	settingsInfo.Importance = widget.LowImportance

	// Scan button - create first so we can reference it in handler
	var scanBtn *widget.Button
	scanBtn = widget.NewButton("Scan Run Directories + Create Jobs CSV", func() {
		// Disable button immediately to prevent double-clicks
		scanBtn.Disable()

		// Call handleScan (which will re-enable button when done)
		w.handleScanWithButton(
			strings.TrimSpace(dirEntry.Text),
			strings.TrimSpace(patternEntry.Text),
			scanBtn,
		)
	})
	scanBtn.Importance = widget.HighImportance

	form := container.NewVBox(
		templateInfo,
		widget.NewSeparator(),
		widget.NewLabel("Scan for Run Directories:"),
		widget.NewForm(
			widget.NewFormItem("Base Directory", container.NewBorder(nil, nil, nil, dirBrowseBtn, dirEntry)),
			widget.NewFormItem("Pattern", patternEntry),
		),
		settingsInfo,
		scanBtn,
	)

	return form
}

// CreateExecutionView creates the execution options UI
func (w *WorkflowUIComponents) CreateExecutionView() fyne.CanvasObject {
	// Show loaded jobs info
	jobsInfo := widget.NewLabel(fmt.Sprintf("Jobs CSV: %s (%d jobs loaded)",
		filepath.Base(w.jobsTab.workflow.SelectedCSV),
		len(w.jobsTab.loadedJobs)))
	jobsInfo.TextStyle = fyne.TextStyle{Bold: true}

	stateInfo := widget.NewLabel(fmt.Sprintf("State File: %s",
		filepath.Base(w.jobsTab.workflow.StateFile)))

	// Execution buttons - using HighImportance for better text contrast
	runSubmitBtn := widget.NewButton("▶ START PIPELINE: Create + Upload + Submit", func() {
		w.handleRunJobs(true)
	})
	runSubmitBtn.Importance = widget.HighImportance

	runUploadOnlyBtn := widget.NewButton("Upload Only (Don't Submit)", func() {
		w.handleRunJobs(false)
	})
	runUploadOnlyBtn.Importance = widget.LowImportance

	resetBtn := widget.NewButton("Reset Workflow", func() {
		w.handleReset()
	})

	return container.NewVBox(
		widget.NewLabel("Ready to Execute:"),
		widget.NewSeparator(),
		jobsInfo,
		stateInfo,
		widget.NewSeparator(),
		widget.NewLabel("Choose execution mode:"),
		container.NewGridWithColumns(2,
			runSubmitBtn,
			runUploadOnlyBtn,
		),
		widget.NewSeparator(),
		resetBtn,
	)
}

// CreateProgressView creates the execution progress UI
func (w *WorkflowUIComponents) CreateProgressView() fyne.CanvasObject {
	statusLabel := widget.NewLabel("Pipeline running...")
	statusLabel.TextStyle = fyne.TextStyle{Bold: true}

	progressInfo := widget.NewLabel("Jobs are being processed. Status updates every 15 seconds.")
	progressInfo.Wrapping = fyne.TextWrapWord

	stopBtn := widget.NewButton("Stop Pipeline", func() {
		w.handleStop()
	})
	stopBtn.Importance = widget.DangerImportance

	return container.NewVBox(
		statusLabel,
		progressInfo,
		widget.NewSeparator(),
		stopBtn,
		widget.NewSeparator(),
		widget.NewLabel("Check the jobs table below for detailed status."),
	)
}

// CreateCompletedView creates the completion UI
func (w *WorkflowUIComponents) CreateCompletedView() fyne.CanvasObject {
	statusLabel := widget.NewLabel("✓ Pipeline completed successfully!")
	statusLabel.TextStyle = fyne.TextStyle{Bold: true}

	statsLabel := widget.NewLabel(fmt.Sprintf("All %d jobs have been processed.",
		len(w.jobsTab.loadedJobs)))

	resetBtn := widget.NewButton("Start New Workflow", func() {
		w.handleReset()
	})
	resetBtn.Importance = widget.HighImportance

	return container.NewVBox(
		statusLabel,
		statsLabel,
		widget.NewSeparator(),
		resetBtn,
	)
}

// CreateErrorView creates the error state UI
func (w *WorkflowUIComponents) CreateErrorView() fyne.CanvasObject {
	errorLabel := widget.NewLabel("✗ An error occurred")
	errorLabel.TextStyle = fyne.TextStyle{Bold: true}

	errorMsg := widget.NewLabel(w.jobsTab.workflow.ErrorMessage)
	errorMsg.Wrapping = fyne.TextWrapWord

	retryBtn := widget.NewButton("Try Again", func() {
		debugf("'Try Again' button clicked! PreviousState=%s\n", w.jobsTab.workflow.PreviousState)
		// Go back to previous state so user can fix the issue
		// Use Reset to safely return to initial state, since we can't validate
		// that the previous state is still valid
		w.jobsTab.workflow.ErrorMessage = ""
		w.jobsTab.workflow.CurrentState = w.jobsTab.workflow.PreviousState
		w.jobsTab.updateView()
	})
	retryBtn.Importance = widget.HighImportance

	resetBtn := widget.NewButton("Start Over", func() {
		w.handleReset()
	})

	return container.NewVBox(
		errorLabel,
		errorMsg,
		widget.NewSeparator(),
		container.NewHBox(retryBtn, resetBtn),
	)
}

// Event Handlers

func (w *WorkflowUIComponents) handleLoadCSVPath() {
	dialog.ShowFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, w.jobsTab.window)
			return
		}
		if reader == nil {
			return
		}
		defer reader.Close()

		// Validate file extension
		if err := validateCSVExtension(reader.URI(), w.jobsTab.window); err != nil {
			dialog.ShowError(err, w.jobsTab.window)
			return
		}

		csvPath := reader.URI().Path()

		// Show progress dialog
		progressDialog := dialog.NewProgress("Loading CSV", "Loading and validating jobs...", w.jobsTab.window)
		progressDialog.Show()

		// Do heavy I/O work in background to avoid freezing UI
		go func() {
			// CRITICAL: Hide progress dialog no matter what happens
			defer func() {
				fyne.Do(func() {
					progressDialog.Hide()
				})
			}()

			// Panic recovery
			defer func() {
				if r := recover(); r != nil {
					guiLogger.Error().Msgf("PANIC in load CSV goroutine: %v\n", r)
					fyne.Do(func() {
						dialog.ShowError(
							fmt.Errorf("An unexpected error occurred while loading CSV: %v\n\nPlease check the console for details.", r),
							w.jobsTab.window,
						)
					})
				}
			}()

			debugf("Starting CSV load for: %s\n", csvPath)

			// Load and validate CSV
			jobs, err := config.LoadJobsCSV(csvPath)
			debugf("LoadJobsCSV returned %d jobs, err=%v\n", len(jobs), err)

			// Progress dialog hidden automatically by defer

			if err != nil {
				debugf("CSV load failed, showing error\n")
				fyne.Do(func() {
					dialog.ShowError(fmt.Errorf("Failed to load CSV: %w", err), w.jobsTab.window)
				})
				return
			}

			// Validate jobs
			debugf("About to validate %d jobs\n", len(jobs))
			if err := w.jobsTab.validateAllJobs(jobs); err != nil {
				debugf("Validation failed: %v\n", err)
				// IMPORTANT: Hide progress dialog FIRST, then show error
				// Otherwise error dialog may not appear or may be hidden immediately
				fyne.Do(func() {
					progressDialog.Hide()
					dialog.ShowError(err, w.jobsTab.window)
				})
				return
			}

			debugf("Validation passed, about to update UI\n")
			// Update UI on main thread
			fyne.Do(func() {
				// Set in workflow
				debugf("About to SetPath, current state=%s\n", w.jobsTab.workflow.CurrentState)
				if err := w.jobsTab.workflow.SetPath(PathLoadCSV); err != nil {
					debugf("SetPath failed: %v\n", err)
					dialog.ShowError(err, w.jobsTab.window)
					return
				}
				debugf("SetPath succeeded, now calling SetLoadedCSV\n")

				if err := w.jobsTab.workflow.SetLoadedCSV(csvPath); err != nil {
					debugf("SetLoadedCSV failed: %v\n", err)
					dialog.ShowError(err, w.jobsTab.window)
					return
				}
				debugf("SetLoadedCSV succeeded, loading jobs into table\n")

				// Load jobs into table
				w.jobsTab.loadJobsFromSpecs(jobs)
				debugf("Jobs loaded into table, updating view\n")

				w.jobsTab.updateView()
				debugf("CSV load complete!\n")
			})
		}()
	}, w.jobsTab.window)
}

func (w *WorkflowUIComponents) handleCreateNewPath() {
	if err := w.jobsTab.workflow.SetPath(PathCreateNew); err != nil {
		dialog.ShowError(err, w.jobsTab.window)
		return
	}
	w.jobsTab.updateView()
}

func (w *WorkflowUIComponents) handleSelectTemplate() {
	dialog.ShowFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, w.jobsTab.window)
			return
		}
		if reader == nil {
			return
		}
		defer reader.Close()

		// Validate file extension
		if err := validateCSVExtension(reader.URI(), w.jobsTab.window); err != nil {
			dialog.ShowError(err, w.jobsTab.window)
			return
		}

		templatePath := reader.URI().Path()

		// Show progress
		progressDialog := dialog.NewProgress("Loading Template", "Loading template CSV...", w.jobsTab.window)
		progressDialog.Show()

		// Load in background
		go func() {
			// CRITICAL: Hide progress dialog no matter what happens
			defer func() {
				fyne.Do(func() {
					progressDialog.Hide()
				})
			}()

			// Panic recovery
			defer func() {
				if r := recover(); r != nil {
					guiLogger.Error().Msgf("PANIC in load template goroutine: %v\n", r)
					fyne.Do(func() {
						dialog.ShowError(
							fmt.Errorf("An unexpected error occurred while loading template: %v\n\nPlease check the console for details.", r),
							w.jobsTab.window,
						)
					})
				}
			}()

			// Load template
			jobs, err := config.LoadJobsCSV(templatePath)
			if err != nil {
				fyne.Do(func() {
					// Progress dialog hidden automatically by defer
					dialog.ShowError(fmt.Errorf("Failed to load template: %w", err), w.jobsTab.window)
				})
				return
			}

			if len(jobs) == 0 {
				fyne.Do(func() {
					// Progress dialog hidden automatically by defer
					dialog.ShowError(fmt.Errorf("Template CSV is empty"), w.jobsTab.window)
				})
				return
			}

			// Use first job as template
			template := jobs[0]

			fyne.Do(func() {
				// Progress dialog hidden automatically by defer

				if err := w.jobsTab.workflow.SetTemplate(template); err != nil {
					dialog.ShowError(err, w.jobsTab.window)
					return
				}

				w.jobsTab.updateView()
			})
		}()
	}, w.jobsTab.window)
}

func (w *WorkflowUIComponents) handleCreateTemplate() {
	builder := NewTemplateBuilderDialog(
		w.jobsTab.window,
		w.jobsTab.apiCache,
		w.jobsTab.engine.GetConfig(),
		w.jobsTab.workflow,
		func(template models.JobSpec) {
			if err := w.jobsTab.workflow.SetTemplate(template); err != nil {
				dialog.ShowError(err, w.jobsTab.window)
				return
			}
			w.jobsTab.updateView()
		},
	)
	builder.Show()
}

func (w *WorkflowUIComponents) handleScanWithButton(baseDir, pattern string, scanBtn *widget.Button) {
	// Ensure button is re-enabled when we're done
	defer func() {
		if scanBtn != nil {
			scanBtn.Enable()
		}
	}()

	w.handleScan(baseDir, pattern)
}

func (w *WorkflowUIComponents) handleScan(baseDir, pattern string) {
	// Prevent multiple simultaneous scans
	w.scanMutex.Lock()
	if w.scanInProgress {
		w.scanMutex.Unlock()
		debugf("Scan already in progress, ignoring duplicate request")
		return
	}
	w.scanInProgress = true
	w.scanMutex.Unlock()

	// Ensure we reset the flag when done
	defer func() {
		w.scanMutex.Lock()
		w.scanInProgress = false
		w.scanMutex.Unlock()
	}()

	// Validate inputs before proceeding
	if baseDir == "" {
		dialog.ShowError(fmt.Errorf("Please select a base directory"), w.jobsTab.window)
		return
	}

	if pattern == "" {
		dialog.ShowError(fmt.Errorf("Please enter a directory pattern (e.g., Run_*)"), w.jobsTab.window)
		return
	}

	// Check if base directory exists
	if _, err := os.Stat(baseDir); os.IsNotExist(err) {
		dialog.ShowError(
			fmt.Errorf("Base directory does not exist:\n%s\n\nPlease check the path and try again.", baseDir),
			w.jobsTab.window,
		)
		return
	}

	// Validate pattern is not a path (common mistake)
	if strings.Contains(pattern, "/") || strings.Contains(pattern, "\\") {
		dialog.ShowError(
			fmt.Errorf("Pattern should be a name pattern (e.g., 'Run_*'), not a path.\n\nYou entered: %s", pattern),
			w.jobsTab.window,
		)
		return
	}

	// Get validation and subpath from Setup config
	cfg := w.jobsTab.engine.GetConfig()
	validation := ""
	subpath := ""
	if cfg != nil {
		validation = cfg.ValidationPattern
		subpath = cfg.RunSubpath
	}

	// Update workflow memory (only baseDir and pattern)
	w.jobsTab.workflow.UpdateScanSettings(baseDir, pattern)

	// Show preview
	preview := NewScanPreviewDialog(
		w.jobsTab.window,
		baseDir,
		pattern,
		validation,
		subpath,
		false, // recursive
		func(outputPath string) {
			w.jobsTab.performScan(baseDir, pattern, validation, subpath, outputPath)
		},
	)
	preview.Show()
}

func (w *WorkflowUIComponents) handleRunJobs(submitJobs bool) {
	// Check if already running
	if w.jobsTab.isRunning {
		dialog.ShowInformation("Already Running",
			"Pipeline is already running. Please wait for it to complete or stop it first.",
			w.jobsTab.window)
		return
	}

	w.jobsTab.startExecution(submitJobs)
}

func (w *WorkflowUIComponents) handleStop() {
	w.jobsTab.stopExecution()
}

func (w *WorkflowUIComponents) handleReset() {
	// Prevent reset during execution
	if w.jobsTab.isRunning {
		dialog.ShowError(
			fmt.Errorf("Cannot reset workflow while pipeline is running. Please stop the pipeline first."),
			w.jobsTab.window)
		return
	}

	dialog.ShowConfirm("Reset Workflow",
		"This will reset the workflow to the beginning. Continue?",
		func(confirmed bool) {
			if confirmed {
				w.jobsTab.workflow.Reset()
				w.jobsTab.loadedJobs = nil
				w.jobsTab.updateView()
			}
		},
		w.jobsTab.window)
}
