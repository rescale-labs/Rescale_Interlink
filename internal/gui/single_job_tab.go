// Package gui provides the Single Job Tab for submitting individual jobs (v3.2.0)
package gui

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"github.com/rescale/rescale-int/internal/cloud/upload"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/parser"
	"github.com/rescale/rescale-int/internal/pur/pipeline"
	"github.com/rescale/rescale-int/internal/pur/state"
)

// SingleJobState represents the current state in the single job workflow
type SingleJobState int

const (
	SJStateInitial       SingleJobState = iota // No job configured
	SJStateJobConfigured                       // Job template ready
	SJStateInputsReady                         // Input files selected
	SJStateExecuting                           // Pipeline running
	SJStateCompleted                           // Job submitted successfully
	SJStateError                               // Error state
)

// String returns the string representation of the single job state
func (s SingleJobState) String() string {
	switch s {
	case SJStateInitial:
		return "Initial"
	case SJStateJobConfigured:
		return "JobConfigured"
	case SJStateInputsReady:
		return "InputsReady"
	case SJStateExecuting:
		return "Executing"
	case SJStateCompleted:
		return "Completed"
	case SJStateError:
		return "Error"
	default:
		return "Unknown"
	}
}

// InputMode represents how input files are provided
type InputMode int

const (
	InputModeNone        InputMode = iota // No input mode selected yet
	InputModeDirectory                    // Tar+upload a directory
	InputModeLocalFiles                   // Upload individual files (no tar)
	InputModeRemoteFiles                  // Use already-uploaded files
)

// String returns the string representation of the input mode
func (m InputMode) String() string {
	switch m {
	case InputModeNone:
		return "None"
	case InputModeDirectory:
		return "Directory"
	case InputModeLocalFiles:
		return "LocalFiles"
	case InputModeRemoteFiles:
		return "RemoteFiles"
	default:
		return "Unknown"
	}
}

// SingleJobTab manages the single job submission interface
type SingleJobTab struct {
	// Core dependencies
	engine *core.Engine
	window fyne.Window
	app    fyne.App

	// API cache (shared with jobs tab)
	apiCache *APICache

	// Private workflow for template builder compatibility
	// (TemplateBuilderDialog requires a *JobsWorkflow for memory access)
	workflow *JobsWorkflow

	// State management
	state         SingleJobState
	previousState SingleJobState // For error recovery
	errorMessage  string

	// Job configuration
	job *models.JobSpec

	// Input configuration
	inputMode     InputMode
	directory     string   // For InputModeDirectory
	localFiles    []string // For InputModeLocalFiles
	remoteFileIDs []string // For InputModeRemoteFiles

	// UI containers
	mainContainer      *fyne.Container
	progressContainer  *fyne.Container // Container for progress indicator (updateable)
	contentContainer   *fyne.Container
	statusContainer    *fyne.Container

	// Status display
	statusLabel *widget.Label
	progressBar *widget.ProgressBar

	// Execution state
	ctx       context.Context
	cancel    context.CancelFunc
	isRunning bool
	runLock   sync.Mutex

	// Result
	submittedJobID string
}

// NewSingleJobTab creates a new single job tab
func NewSingleJobTab(engine *core.Engine, window fyne.Window, app fyne.App, apiCache *APICache) *SingleJobTab {
	sjt := &SingleJobTab{
		engine:      engine,
		window:      window,
		app:         app,
		apiCache:    apiCache,
		workflow:    NewJobsWorkflow(), // For template builder compatibility
		state:       SJStateInitial,
		inputMode:   InputModeNone,
		statusLabel: widget.NewLabel("Ready to configure a job"),
		progressBar: widget.NewProgressBar(),
	}

	return sjt
}

// Build creates the single job tab UI
func (sjt *SingleJobTab) Build() fyne.CanvasObject {
	// Create content container (will change based on state)
	sjt.contentContainer = container.NewVBox()

	// Create status section
	statusHeader := widget.NewLabel("Status:")
	statusHeader.TextStyle = fyne.TextStyle{Bold: true}

	sjt.statusContainer = container.NewVBox(
		VerticalSpacer(8),
		widget.NewSeparator(),
		VerticalSpacer(8),
		container.NewHBox(
			statusHeader,
			HorizontalSpacer(8),
			sjt.statusLabel,
		),
		VerticalSpacer(4),
		sjt.progressBar,
		VerticalSpacer(8),
	)
	sjt.progressBar.Hide() // Initially hidden

	// Create progress container (updateable)
	sjt.progressContainer = container.NewVBox(
		VerticalSpacer(8),
		sjt.createProgressIndicator(),
		VerticalSpacer(8),
	)

	// Main container layout
	sjt.mainContainer = container.NewBorder(
		sjt.progressContainer,
		sjt.statusContainer,
		nil,
		nil,
		container.NewPadded(sjt.contentContainer),
	)

	// Initialize view
	sjt.updateView()

	return sjt.mainContainer
}

// createProgressIndicator creates a visual indicator of workflow progress
func (sjt *SingleJobTab) createProgressIndicator() fyne.CanvasObject {
	steps := []string{"Configure Job", "Select Inputs", "Execute", "Complete"}

	// Create step indicators
	var stepWidgets []fyne.CanvasObject

	for i, step := range steps {
		var style fyne.TextStyle
		text := step

		// Determine step state
		stepState := sjt.getStepState(i)
		switch stepState {
		case "completed":
			text = "✓ " + step
			style = fyne.TextStyle{}
		case "current":
			text = "● " + step
			style = fyne.TextStyle{Bold: true}
		case "pending":
			text = "○ " + step
			style = fyne.TextStyle{}
		}

		label := widget.NewLabel(text)
		label.TextStyle = style
		stepWidgets = append(stepWidgets, label)

		// Add connector between steps (except after last)
		if i < len(steps)-1 {
			stepWidgets = append(stepWidgets, widget.NewLabel(" → "))
		}
	}

	return container.NewHBox(stepWidgets...)
}

// getStepState returns the state of a step (completed, current, or pending)
func (sjt *SingleJobTab) getStepState(stepIndex int) string {
	// Map workflow state to step index
	var currentStep int
	switch sjt.state {
	case SJStateInitial:
		currentStep = 0
	case SJStateJobConfigured:
		currentStep = 1
	case SJStateInputsReady:
		currentStep = 2
	case SJStateExecuting:
		currentStep = 2
	case SJStateCompleted:
		currentStep = 4 // All complete
	case SJStateError:
		// Error stays at the step where it occurred
		currentStep = sjt.getErrorStep()
	}

	if stepIndex < currentStep {
		return "completed"
	} else if stepIndex == currentStep {
		return "current"
	}
	return "pending"
}

// getErrorStep returns the step index where an error occurred
func (sjt *SingleJobTab) getErrorStep() int {
	switch sjt.previousState {
	case SJStateInitial:
		return 0
	case SJStateJobConfigured:
		return 1
	case SJStateInputsReady, SJStateExecuting:
		return 2
	default:
		return 0
	}
}

// updateView updates the UI based on current state
func (sjt *SingleJobTab) updateView() {
	debugf("SingleJobTab.updateView: state=%s, inputMode=%s\n", sjt.state, sjt.inputMode)

	// Update progress indicator
	sjt.progressContainer.Objects = []fyne.CanvasObject{
		VerticalSpacer(8),
		sjt.createProgressIndicator(),
		VerticalSpacer(8),
	}
	sjt.progressContainer.Refresh()

	// Update content based on state
	var newContent fyne.CanvasObject

	switch sjt.state {
	case SJStateInitial:
		newContent = sjt.createJobConfigView()
		sjt.statusLabel.SetText("Configure your job to get started")
		sjt.progressBar.Hide()

	case SJStateJobConfigured:
		newContent = sjt.createInputSelectionView()
		sjt.statusLabel.SetText("Job configured. Select input files.")
		sjt.progressBar.Hide()

	case SJStateInputsReady:
		newContent = sjt.createReadyToExecuteView()
		sjt.statusLabel.SetText("Ready to submit job")
		sjt.progressBar.Hide()

	case SJStateExecuting:
		newContent = sjt.createExecutingView()
		sjt.statusLabel.SetText("Submitting job...")
		sjt.progressBar.Show()

	case SJStateCompleted:
		newContent = sjt.createCompletedView()
		sjt.statusLabel.SetText("Job submitted successfully!")
		sjt.progressBar.Hide()

	case SJStateError:
		newContent = sjt.createErrorView()
		sjt.statusLabel.SetText("Error: " + sjt.errorMessage)
		sjt.progressBar.Hide()

	default:
		newContent = widget.NewLabel("Unknown state")
	}

	// Update content container
	sjt.contentContainer.Objects = []fyne.CanvasObject{newContent}
	sjt.contentContainer.Refresh()
	sjt.mainContainer.Refresh()
}

// createJobConfigView creates the initial job configuration view
func (sjt *SingleJobTab) createJobConfigView() fyne.CanvasObject {
	title := widget.NewLabel("Step 1: Configure Job")
	title.TextStyle = fyne.TextStyle{Bold: true}

	description := widget.NewLabel("Create a new job configuration or load from an existing file (CSV or SGE script).")
	description.Wrapping = fyne.TextWrapWord

	// Configure New Job button
	createNewBtn := widget.NewButton("Configure New Job...", func() {
		sjt.showTemplateBuilder()
	})
	createNewBtn.Importance = widget.HighImportance

	// Load from CSV button
	loadCSVBtn := widget.NewButton("Load from CSV...", func() {
		sjt.showLoadFromCSV()
	})
	loadCSVBtn.Importance = widget.HighImportance

	// Load from SGE Script button
	loadSGEBtn := widget.NewButton("Load from SGE Script...", func() {
		sjt.showLoadFromSGE()
	})
	loadSGEBtn.Importance = widget.HighImportance

	// Load from JSON button
	loadJSONBtn := widget.NewButton("Load from JSON...", func() {
		sjt.showLoadFromJSON()
	})
	loadJSONBtn.Importance = widget.HighImportance

	// Job summary (shown after configuration)
	var summaryWidget fyne.CanvasObject
	if sjt.job != nil {
		summaryWidget = sjt.createJobSummary()
	} else {
		summaryWidget = widget.NewLabel("No job configured yet")
	}

	// Build content
	content := container.NewVBox(
		title,
		VerticalSpacer(8),
		description,
		VerticalSpacer(16),
		container.NewHBox(
			createNewBtn,
			HorizontalSpacer(8),
			loadCSVBtn,
			HorizontalSpacer(8),
			loadJSONBtn,
			HorizontalSpacer(8),
			loadSGEBtn,
		),
		VerticalSpacer(16),
		widget.NewSeparator(),
		VerticalSpacer(8),
		widget.NewLabel("Current Configuration:"),
		summaryWidget,
	)

	// Add save buttons if job is configured
	if sjt.job != nil {
		content.Add(VerticalSpacer(8))
		content.Add(sjt.createSaveButtonsRow())
	}

	return content
}

// createInputSelectionView creates the input file selection view
func (sjt *SingleJobTab) createInputSelectionView() fyne.CanvasObject {
	title := widget.NewLabel("Step 2: Select Input Files")
	title.TextStyle = fyne.TextStyle{Bold: true}

	description := widget.NewLabel("Choose how to provide input files for your job.")
	description.Wrapping = fyne.TextWrapWord

	// Option 1: Upload Directory (tar + upload)
	dirBtn := widget.NewButton("Upload Directory...", func() {
		sjt.inputMode = InputModeDirectory
		sjt.showDirectoryPicker()
	})
	dirBtn.Importance = widget.HighImportance
	dirDesc := widget.NewLabel("Tar and upload an entire directory")

	// Option 2: Upload Local Files (individual files)
	filesBtn := widget.NewButton("Upload Local Files...", func() {
		sjt.inputMode = InputModeLocalFiles
		sjt.showLocalFilesPicker()
	})
	filesBtn.Importance = widget.HighImportance
	filesDesc := widget.NewLabel("Upload individual files without tarring")

	// Option 3: Use Remote Files (already on Rescale)
	remoteBtn := widget.NewButton("Use Remote Files...", func() {
		sjt.inputMode = InputModeRemoteFiles
		sjt.showRemoteFileBrowser()
	})
	remoteBtn.Importance = widget.HighImportance
	remoteDesc := widget.NewLabel("Select files already uploaded to Rescale")

	// Back button
	backBtn := widget.NewButton("← Back", func() {
		sjt.goBack()
	})
	backBtn.Importance = widget.HighImportance

	// Input summary
	var inputSummary fyne.CanvasObject
	inputSummary = sjt.createInputSummary()

	return container.NewVBox(
		container.NewHBox(
			backBtn,
			HorizontalSpacer(16),
			title,
		),
		VerticalSpacer(8),
		description,
		VerticalSpacer(16),
		container.NewGridWithColumns(3,
			container.NewVBox(dirBtn, dirDesc),
			container.NewVBox(filesBtn, filesDesc),
			container.NewVBox(remoteBtn, remoteDesc),
		),
		VerticalSpacer(16),
		widget.NewSeparator(),
		VerticalSpacer(8),
		widget.NewLabel("Selected Inputs:"),
		inputSummary,
		VerticalSpacer(8),
		sjt.createSaveButtonsRow(),
	)
}

// createReadyToExecuteView creates the pre-execution confirmation view
func (sjt *SingleJobTab) createReadyToExecuteView() fyne.CanvasObject {
	title := widget.NewLabel("Step 3: Review and Submit")
	title.TextStyle = fyne.TextStyle{Bold: true}

	// Job summary
	jobSummary := sjt.createJobSummary()

	// Input summary
	inputSummary := sjt.createInputSummary()

	// Back button
	backBtn := widget.NewButton("← Back", func() {
		sjt.state = SJStateJobConfigured
		sjt.updateView()
	})
	backBtn.Importance = widget.HighImportance

	// Submit button
	submitBtn := widget.NewButton("Submit Job", func() {
		sjt.startExecution()
	})
	submitBtn.Importance = widget.HighImportance

	return container.NewVBox(
		container.NewHBox(
			backBtn,
			HorizontalSpacer(16),
			title,
		),
		VerticalSpacer(16),
		widget.NewLabel("Job Configuration:"),
		jobSummary,
		VerticalSpacer(8),
		widget.NewSeparator(),
		VerticalSpacer(8),
		widget.NewLabel("Input Files:"),
		inputSummary,
		VerticalSpacer(8),
		sjt.createSaveButtonsRow(),
		VerticalSpacer(16),
		container.NewCenter(submitBtn),
	)
}

// createExecutingView creates the execution progress view
func (sjt *SingleJobTab) createExecutingView() fyne.CanvasObject {
	title := widget.NewLabel("Submitting Job...")
	title.TextStyle = fyne.TextStyle{Bold: true}

	description := widget.NewLabel("Please wait while your job is being submitted.")

	// Stop button
	stopBtn := widget.NewButton("Stop", func() {
		sjt.stopExecution()
	})
	stopBtn.Importance = widget.DangerImportance

	return container.NewVBox(
		title,
		VerticalSpacer(8),
		description,
		VerticalSpacer(16),
		container.NewCenter(stopBtn),
	)
}

// createCompletedView creates the completion view
func (sjt *SingleJobTab) createCompletedView() fyne.CanvasObject {
	title := widget.NewLabel("Job Submitted Successfully!")
	title.TextStyle = fyne.TextStyle{Bold: true}

	jobIDText := "Job ID: " + sjt.submittedJobID
	if sjt.submittedJobID == "" {
		jobIDText = "Job submitted"
	}
	jobIDLabel := widget.NewLabel(jobIDText)

	// Submit Another button
	anotherBtn := widget.NewButton("Submit Another Job", func() {
		sjt.reset()
	})
	anotherBtn.Importance = widget.HighImportance

	return container.NewVBox(
		title,
		VerticalSpacer(8),
		jobIDLabel,
		VerticalSpacer(16),
		container.NewCenter(anotherBtn),
	)
}

// createErrorView creates the error display view
func (sjt *SingleJobTab) createErrorView() fyne.CanvasObject {
	title := widget.NewLabel("Error")
	title.TextStyle = fyne.TextStyle{Bold: true}

	errorLabel := widget.NewLabel(sjt.errorMessage)
	errorLabel.Wrapping = fyne.TextWrapWord

	// Retry button
	retryBtn := widget.NewButton("Retry", func() {
		sjt.state = sjt.previousState
		sjt.errorMessage = ""
		sjt.updateView()
	})
	retryBtn.Importance = widget.HighImportance

	// Start Over button
	startOverBtn := widget.NewButton("Start Over", func() {
		sjt.reset()
	})
	startOverBtn.Importance = widget.HighImportance

	return container.NewVBox(
		title,
		VerticalSpacer(8),
		errorLabel,
		VerticalSpacer(16),
		container.NewHBox(
			retryBtn,
			HorizontalSpacer(8),
			startOverBtn,
		),
	)
}

// createJobSummary creates a summary widget for the current job configuration
func (sjt *SingleJobTab) createJobSummary() fyne.CanvasObject {
	if sjt.job == nil {
		return widget.NewLabel("No job configured")
	}

	return container.NewVBox(
		widget.NewLabel("Name: "+sjt.job.JobName),
		widget.NewLabel("Software: "+sjt.job.AnalysisCode+" "+sjt.job.AnalysisVersion),
		widget.NewLabel("Hardware: "+sjt.job.CoreType),
		widget.NewLabel("Cores: "+fmt.Sprintf("%d", sjt.job.CoresPerSlot)),
		widget.NewLabel("Walltime: "+fmt.Sprintf("%.1f hours", sjt.job.WalltimeHours)),
	)
}

// createInputSummary creates a summary widget for selected inputs
func (sjt *SingleJobTab) createInputSummary() fyne.CanvasObject {
	switch sjt.inputMode {
	case InputModeDirectory:
		if sjt.directory != "" {
			return widget.NewLabel("Directory: " + sjt.directory)
		}
	case InputModeLocalFiles:
		if len(sjt.localFiles) > 0 {
			return widget.NewLabel(fmt.Sprintf("%d local files selected", len(sjt.localFiles)))
		}
	case InputModeRemoteFiles:
		if len(sjt.remoteFileIDs) > 0 {
			return widget.NewLabel(fmt.Sprintf("%d remote files selected", len(sjt.remoteFileIDs)))
		}
	}
	return widget.NewLabel("No inputs selected")
}

// State transition methods

// goBack navigates back one step
func (sjt *SingleJobTab) goBack() {
	switch sjt.state {
	case SJStateJobConfigured:
		sjt.state = SJStateInitial
	case SJStateInputsReady:
		sjt.state = SJStateJobConfigured
	}
	sjt.updateView()
}

// setError transitions to error state
func (sjt *SingleJobTab) setError(message string) {
	sjt.previousState = sjt.state
	sjt.state = SJStateError
	sjt.errorMessage = message
	sjt.updateView()
}

// reset returns to initial state
func (sjt *SingleJobTab) reset() {
	sjt.state = SJStateInitial
	sjt.previousState = SJStateInitial
	sjt.errorMessage = ""
	sjt.job = nil
	sjt.inputMode = InputModeNone
	sjt.directory = ""
	sjt.localFiles = nil
	sjt.remoteFileIDs = nil
	sjt.submittedJobID = ""
	sjt.updateView()
}

// Job configuration methods (CHUNK 7)

// showTemplateBuilder shows the template builder dialog for creating a new job
func (sjt *SingleJobTab) showTemplateBuilder() {
	debugf("SingleJobTab: showTemplateBuilder called\n")

	builder := NewTemplateBuilderDialog(
		sjt.window,
		sjt.engine,
		sjt.apiCache,
		sjt.engine.GetConfig(),
		sjt.workflow,
		func(template models.JobSpec) {
			debugf("SingleJobTab: Template created: %s\n", template.JobName)

			// Store the job configuration
			sjt.job = &template

			// Transition to JobConfigured state
			sjt.state = SJStateJobConfigured

			// Update memory for next time
			sjt.workflow.Memory.LastTemplate = template
			sjt.workflow.Memory.LastCoreType = template.CoreType
			sjt.workflow.Memory.LastAnalysisCode = template.AnalysisCode

			sjt.updateView()

			dialog.ShowInformation("Job Configured",
				fmt.Sprintf("Job '%s' configured successfully.\n\nNow select your input files.", template.JobName),
				sjt.window)
		},
	)
	builder.Show()
}

// showLoadFromCSV shows a file picker to load a job from an existing CSV file
func (sjt *SingleJobTab) showLoadFromCSV() {
	debugf("SingleJobTab: showLoadFromCSV called\n")

	// Create file dialog for CSV files
	fileDialog := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(fmt.Errorf("Failed to open file: %w", err), sjt.window)
			return
		}
		if reader == nil {
			// User cancelled
			return
		}
		defer reader.Close()

		filePath := reader.URI().Path()
		debugf("SingleJobTab: Loading CSV from: %s\n", filePath)

		// Load jobs from CSV
		jobs, err := config.LoadJobsCSV(filePath)
		if err != nil {
			dialog.ShowError(fmt.Errorf("Failed to load jobs CSV: %w", err), sjt.window)
			return
		}

		if len(jobs) == 0 {
			dialog.ShowError(fmt.Errorf("No jobs found in CSV file"), sjt.window)
			return
		}

		// For Single Job tab, use the first job as template
		// (or let user choose if multiple jobs)
		var selectedJob models.JobSpec
		if len(jobs) == 1 {
			selectedJob = jobs[0]
		} else {
			// Show selection dialog for multiple jobs
			sjt.showJobSelectionDialog(jobs)
			return
		}

		// Store the job configuration
		sjt.job = &selectedJob

		// Transition to JobConfigured state
		sjt.state = SJStateJobConfigured
		sjt.updateView()

		dialog.ShowInformation("Job Loaded",
			fmt.Sprintf("Job '%s' loaded from CSV.\n\nNow select your input files.", selectedJob.JobName),
			sjt.window)
	}, sjt.window)

	// Filter for CSV files
	fileDialog.SetFilter(storage.NewExtensionFileFilter([]string{".csv"}))
	fileDialog.Show()
}

// showJobSelectionDialog shows a dialog to select one job from multiple jobs in a CSV
func (sjt *SingleJobTab) showJobSelectionDialog(jobs []models.JobSpec) {
	// Create list of job names for selection
	jobNames := make([]string, len(jobs))
	for i, job := range jobs {
		jobNames[i] = fmt.Sprintf("%d. %s (%s)", i+1, job.JobName, job.AnalysisCode)
	}

	// Create selection dialog
	selectedIndex := 0
	selectWidget := widget.NewSelect(jobNames, func(selected string) {
		for i, name := range jobNames {
			if name == selected {
				selectedIndex = i
				break
			}
		}
	})
	selectWidget.SetSelectedIndex(0)

	content := container.NewVBox(
		widget.NewLabel(fmt.Sprintf("The CSV contains %d jobs. Select one to use:", len(jobs))),
		VerticalSpacer(8),
		selectWidget,
	)

	dialog.ShowCustomConfirm("Select Job", "Use Selected", "Cancel", content, func(confirmed bool) {
		if !confirmed {
			return
		}

		// Store the selected job
		sjt.job = &jobs[selectedIndex]

		// Transition to JobConfigured state
		sjt.state = SJStateJobConfigured
		sjt.updateView()

		dialog.ShowInformation("Job Loaded",
			fmt.Sprintf("Job '%s' loaded from CSV.\n\nNow select your input files.", sjt.job.JobName),
			sjt.window)
	}, sjt.window)
}

// showLoadFromSGE shows a file picker to load a job from an SGE script
func (sjt *SingleJobTab) showLoadFromSGE() {
	debugf("SingleJobTab: showLoadFromSGE called\n")

	fileDialog := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(fmt.Errorf("Failed to open file: %w", err), sjt.window)
			return
		}
		if reader == nil {
			// User cancelled
			return
		}
		defer reader.Close()

		filePath := reader.URI().Path()
		debugf("SingleJobTab: Loading SGE script from: %s\n", filePath)

		// Use existing SGE parser
		sgeParser := parser.NewSGEParser()
		metadata, err := sgeParser.Parse(filePath)
		if err != nil {
			dialog.ShowError(fmt.Errorf("Failed to parse SGE script: %w", err), sjt.window)
			return
		}

		// Convert to JobSpec
		job := parser.SGEMetadataToJobSpec(metadata)
		sjt.job = &job

		// Try to extract input file references from script
		if len(metadata.InputFiles) > 0 {
			sjt.localFiles = metadata.InputFiles
			sjt.inputMode = InputModeLocalFiles
			debugf("SingleJobTab: Found %d input files in script\n", len(metadata.InputFiles))
		}

		// Transition to JobConfigured state
		sjt.state = SJStateJobConfigured
		sjt.updateView()

		dialog.ShowInformation("Job Loaded",
			fmt.Sprintf("Job '%s' loaded from SGE script.\n\nNow select your input files.", job.JobName),
			sjt.window)
	}, sjt.window)

	// Filter for shell/SGE files
	fileDialog.SetFilter(storage.NewExtensionFileFilter([]string{".sh", ".sge", ".bash"}))
	fileDialog.Show()
}

// showLoadFromJSON shows a file picker to load a job from a JSON file
func (sjt *SingleJobTab) showLoadFromJSON() {
	debugf("SingleJobTab: showLoadFromJSON called\n")

	fileDialog := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(fmt.Errorf("Failed to open file: %w", err), sjt.window)
			return
		}
		if reader == nil {
			// User cancelled
			return
		}
		defer reader.Close()

		filePath := reader.URI().Path()
		debugf("SingleJobTab: Loading JSON from: %s\n", filePath)

		// Load jobs from JSON
		jobs, err := config.LoadJobsJSON(filePath)
		if err != nil {
			dialog.ShowError(fmt.Errorf("Failed to load jobs JSON: %w", err), sjt.window)
			return
		}

		if len(jobs) == 0 {
			dialog.ShowError(fmt.Errorf("No jobs found in JSON file"), sjt.window)
			return
		}

		// For Single Job tab, use the first job as template
		// (or let user choose if multiple jobs)
		var selectedJob models.JobSpec
		if len(jobs) == 1 {
			selectedJob = jobs[0]
		} else {
			// Show selection dialog for multiple jobs
			sjt.showJobSelectionDialog(jobs)
			return
		}

		// Store the job configuration
		sjt.job = &selectedJob

		// Transition to JobConfigured state
		sjt.state = SJStateJobConfigured
		sjt.updateView()

		dialog.ShowInformation("Job Loaded",
			fmt.Sprintf("Job '%s' loaded from JSON.\n\nNow select your input files.", selectedJob.JobName),
			sjt.window)
	}, sjt.window)

	// Filter for JSON files
	fileDialog.SetFilter(storage.NewExtensionFileFilter([]string{".json"}))
	fileDialog.Show()
}

// showSaveAsCSV saves the current job configuration as a CSV file
func (sjt *SingleJobTab) showSaveAsCSV() {
	debugf("SingleJobTab: showSaveAsCSV called\n")

	if sjt.job == nil {
		dialog.ShowError(fmt.Errorf("No job configured"), sjt.window)
		return
	}

	saveDialog := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
		if err != nil {
			dialog.ShowError(fmt.Errorf("Failed to save file: %w", err), sjt.window)
			return
		}
		if writer == nil {
			// User cancelled
			return
		}
		defer writer.Close()

		filePath := writer.URI().Path()
		debugf("SingleJobTab: Saving CSV to: %s\n", filePath)

		// Save job as CSV
		jobs := []models.JobSpec{*sjt.job}
		if err := config.SaveJobsCSV(filePath, jobs); err != nil {
			dialog.ShowError(fmt.Errorf("Failed to save CSV: %w", err), sjt.window)
			return
		}

		dialog.ShowInformation("Saved", fmt.Sprintf("Job saved to:\n%s", filePath), sjt.window)
	}, sjt.window)

	// Set default filename
	saveDialog.SetFileName(sjt.job.JobName + ".csv")
	saveDialog.Show()
}

// showSaveAsSGE saves the current job configuration as an SGE script
func (sjt *SingleJobTab) showSaveAsSGE() {
	debugf("SingleJobTab: showSaveAsSGE called\n")

	if sjt.job == nil {
		dialog.ShowError(fmt.Errorf("No job configured"), sjt.window)
		return
	}

	saveDialog := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
		if err != nil {
			dialog.ShowError(fmt.Errorf("Failed to save file: %w", err), sjt.window)
			return
		}
		if writer == nil {
			// User cancelled
			return
		}
		defer writer.Close()

		filePath := writer.URI().Path()
		debugf("SingleJobTab: Saving SGE script to: %s\n", filePath)

		// Convert JobSpec to SGEMetadata and generate script
		metadata := parser.JobSpecToSGEMetadata(*sjt.job)
		script := metadata.ToSGEScript()

		if _, err := writer.Write([]byte(script)); err != nil {
			dialog.ShowError(fmt.Errorf("Failed to write SGE script: %w", err), sjt.window)
			return
		}

		dialog.ShowInformation("Saved", fmt.Sprintf("SGE script saved to:\n%s", filePath), sjt.window)
	}, sjt.window)

	// Set default filename
	saveDialog.SetFileName(sjt.job.JobName + ".sh")
	saveDialog.Show()
}

// showSaveAsJSON saves the current job configuration as a JSON file
func (sjt *SingleJobTab) showSaveAsJSON() {
	debugf("SingleJobTab: showSaveAsJSON called\n")

	if sjt.job == nil {
		dialog.ShowError(fmt.Errorf("No job configured"), sjt.window)
		return
	}

	saveDialog := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
		if err != nil {
			dialog.ShowError(fmt.Errorf("Failed to save file: %w", err), sjt.window)
			return
		}
		if writer == nil {
			// User cancelled
			return
		}
		defer writer.Close()

		filePath := writer.URI().Path()
		debugf("SingleJobTab: Saving JSON to: %s\n", filePath)

		// Save job as JSON
		if err := config.SaveJobJSON(filePath, *sjt.job); err != nil {
			dialog.ShowError(fmt.Errorf("Failed to save JSON: %w", err), sjt.window)
			return
		}

		dialog.ShowInformation("Saved", fmt.Sprintf("Job saved to:\n%s", filePath), sjt.window)
	}, sjt.window)

	// Set default filename
	saveDialog.SetFileName(sjt.job.JobName + ".json")
	saveDialog.Show()
}

// createSaveButtonsRow creates a row of save buttons (shown when job is configured)
func (sjt *SingleJobTab) createSaveButtonsRow() fyne.CanvasObject {
	if sjt.job == nil {
		return widget.NewLabel("") // Empty placeholder when no job
	}

	saveCSVBtn := widget.NewButton("Save as CSV...", func() {
		sjt.showSaveAsCSV()
	})
	saveCSVBtn.Importance = widget.HighImportance

	saveJSONBtn := widget.NewButton("Save as JSON...", func() {
		sjt.showSaveAsJSON()
	})
	saveJSONBtn.Importance = widget.HighImportance

	saveSGEBtn := widget.NewButton("Save as SGE Script...", func() {
		sjt.showSaveAsSGE()
	})
	saveSGEBtn.Importance = widget.HighImportance

	return container.NewHBox(
		widget.NewLabel("Export:"),
		HorizontalSpacer(8),
		saveCSVBtn,
		HorizontalSpacer(8),
		saveJSONBtn,
		HorizontalSpacer(8),
		saveSGEBtn,
	)
}

// Input selection methods (CHUNK 8)

// showDirectoryPicker shows a folder picker dialog to select a directory for tar+upload
func (sjt *SingleJobTab) showDirectoryPicker() {
	debugf("SingleJobTab: showDirectoryPicker called\n")

	dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil {
			dialog.ShowError(fmt.Errorf("Failed to open folder: %w", err), sjt.window)
			return
		}
		if uri == nil {
			// User cancelled
			return
		}

		dirPath := uri.Path()
		debugf("SingleJobTab: Directory selected: %s\n", dirPath)

		// Store the directory
		sjt.directory = dirPath
		sjt.inputMode = InputModeDirectory

		// Clear other input types
		sjt.localFiles = nil
		sjt.remoteFileIDs = nil

		// Transition to InputsReady state
		sjt.state = SJStateInputsReady
		sjt.updateView()

		dialog.ShowInformation("Directory Selected",
			fmt.Sprintf("Directory: %s\n\nThis directory will be tarred and uploaded when you submit the job.", dirPath),
			sjt.window)
	}, sjt.window)
}

// showLocalFilesPicker shows a dialog to select multiple local files for upload
func (sjt *SingleJobTab) showLocalFilesPicker() {
	debugf("SingleJobTab: showLocalFilesPicker called\n")

	// Initialize local files list if needed
	if sjt.localFiles == nil {
		sjt.localFiles = []string{}
	}

	// Show the file selection manager dialog
	sjt.showFileSelectionManager()
}

// showFileSelectionManager shows a dialog to manage selected local files
// Since Fyne doesn't support multi-file selection natively, we use a dialog
// where users can add files one at a time
func (sjt *SingleJobTab) showFileSelectionManager() {
	// Create a list widget to show selected files
	fileListData := sjt.localFiles

	// Create list of file basenames for display
	var listWidget *widget.List
	listWidget = widget.NewList(
		func() int { return len(fileListData) },
		func() fyne.CanvasObject {
			removeBtn := widget.NewButton("Remove", nil)
			removeBtn.Importance = widget.HighImportance
			return container.NewHBox(
				widget.NewLabel("filename.txt"),
				removeBtn,
			)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			box := obj.(*fyne.Container)
			label := box.Objects[0].(*widget.Label)
			removeBtn := box.Objects[1].(*widget.Button)

			if id < len(fileListData) {
				label.SetText(filepath.Base(fileListData[id]))
				removeBtn.OnTapped = func() {
					// Remove this file from the list
					fileListData = append(fileListData[:id], fileListData[id+1:]...)
					sjt.localFiles = fileListData
					listWidget.Refresh()
				}
			}
		},
	)
	listWidget.Resize(fyne.NewSize(400, 200))

	// Add file button
	addFileBtn := widget.NewButton("Add File...", func() {
		fileDialog := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if err != nil {
				dialog.ShowError(err, sjt.window)
				return
			}
			if reader == nil {
				return // User cancelled
			}
			defer reader.Close()

			filePath := reader.URI().Path()

			// Check if already added
			for _, f := range fileListData {
				if f == filePath {
					dialog.ShowInformation("Already Added",
						"This file is already in the list.", sjt.window)
					return
				}
			}

			fileListData = append(fileListData, filePath)
			sjt.localFiles = fileListData
			listWidget.Refresh()
		}, sjt.window)
		fileDialog.Show()
	})
	addFileBtn.Importance = widget.HighImportance

	// Clear all button
	clearAllBtn := widget.NewButton("Clear All", func() {
		fileListData = []string{}
		sjt.localFiles = fileListData
		listWidget.Refresh()
	})
	clearAllBtn.Importance = widget.HighImportance

	// Instructions
	instructions := widget.NewLabel("Add files to upload. Files will be uploaded individually (not tarred).")
	instructions.Wrapping = fyne.TextWrapWord

	// File count label
	fileCountLabel := widget.NewLabel(fmt.Sprintf("%d files selected", len(fileListData)))

	content := container.NewVBox(
		instructions,
		VerticalSpacer(8),
		container.NewHBox(addFileBtn, HorizontalSpacer(8), clearAllBtn),
		VerticalSpacer(8),
		fileCountLabel,
		container.NewScroll(listWidget),
	)

	// Custom dialog with confirm/cancel
	dialog.ShowCustomConfirm("Select Local Files", "Use Selected Files", "Cancel", content, func(confirmed bool) {
		if !confirmed {
			return
		}

		if len(sjt.localFiles) == 0 {
			dialog.ShowError(fmt.Errorf("No files selected"), sjt.window)
			return
		}

		debugf("SingleJobTab: %d local files selected\n", len(sjt.localFiles))

		// Set input mode
		sjt.inputMode = InputModeLocalFiles

		// Clear other input types
		sjt.directory = ""
		sjt.remoteFileIDs = nil

		// Transition to InputsReady state
		sjt.state = SJStateInputsReady
		sjt.updateView()

		dialog.ShowInformation("Files Selected",
			fmt.Sprintf("%d files selected for upload.", len(sjt.localFiles)),
			sjt.window)
	}, sjt.window)
}

// Remote file browser methods (CHUNK 9)

// showRemoteFileBrowser shows a dialog to browse and select remote Rescale files
func (sjt *SingleJobTab) showRemoteFileBrowser() {
	debugf("SingleJobTab: showRemoteFileBrowser called\n")

	// Create a remote browser widget
	remoteBrowser := NewRemoteBrowser(sjt.engine, sjt.window)

	// Track selected files
	var selectedFiles []FileItem

	// Set up selection callback
	remoteBrowser.OnSelectionChanged = func(selected []FileItem) {
		selectedFiles = selected
		debugf("SingleJobTab: Remote selection changed: %d files\n", len(selected))
	}

	// Create the dialog content
	instructions := widget.NewLabel("Browse your Rescale files and select files to use as job inputs.")
	instructions.Wrapping = fyne.TextWrapWord

	selectionLabel := widget.NewLabel("0 files selected")

	// Update selection label when browser selection changes
	originalCallback := remoteBrowser.OnSelectionChanged
	remoteBrowser.OnSelectionChanged = func(selected []FileItem) {
		if originalCallback != nil {
			originalCallback(selected)
		}
		// Count only files (not folders)
		fileCount := 0
		for _, item := range selected {
			if !item.IsFolder {
				fileCount++
			}
		}
		selectionLabel.SetText(fmt.Sprintf("%d files selected", fileCount))
	}

	// Build browser UI - the RemoteBrowser widget can be used directly as a CanvasObject
	// Set minimum size for the browser to display properly in dialog
	remoteBrowser.Resize(fyne.NewSize(750, 400))

	content := container.NewBorder(
		container.NewVBox(
			instructions,
			VerticalSpacer(4),
			selectionLabel,
			VerticalSpacer(8),
		),
		nil, nil, nil,
		remoteBrowser,
	)

	// Custom dialog
	customDialog := dialog.NewCustomConfirm("Select Remote Files", "Use Selected", "Cancel", content, func(confirmed bool) {
		if !confirmed {
			return
		}

		// Filter to only files (not folders) and extract IDs
		var fileIDs []string
		for _, item := range selectedFiles {
			if !item.IsFolder {
				fileIDs = append(fileIDs, item.ID)
			}
		}

		if len(fileIDs) == 0 {
			dialog.ShowError(fmt.Errorf("No files selected (only files, not folders, can be used)"), sjt.window)
			return
		}

		debugf("SingleJobTab: %d remote files selected\n", len(fileIDs))

		// Store selected file IDs
		sjt.remoteFileIDs = fileIDs
		sjt.inputMode = InputModeRemoteFiles

		// Clear other input types
		sjt.directory = ""
		sjt.localFiles = nil

		// Transition to InputsReady state
		sjt.state = SJStateInputsReady
		sjt.updateView()

		dialog.ShowInformation("Files Selected",
			fmt.Sprintf("%d remote files selected.", len(fileIDs)),
			sjt.window)
	}, sjt.window)

	// Make dialog larger to fit browser
	customDialog.Resize(fyne.NewSize(800, 600))
	customDialog.Show()
}

// Execution methods (CHUNK 10)

// startExecution begins job submission based on the selected input mode
func (sjt *SingleJobTab) startExecution() {
	debugf("SingleJobTab: startExecution called, inputMode=%s\n", sjt.inputMode)

	// Validate we have a job configured
	if sjt.job == nil {
		dialog.ShowError(fmt.Errorf("No job configured"), sjt.window)
		return
	}

	// Validate we have inputs
	switch sjt.inputMode {
	case InputModeDirectory:
		if sjt.directory == "" {
			dialog.ShowError(fmt.Errorf("No directory selected"), sjt.window)
			return
		}
	case InputModeLocalFiles:
		if len(sjt.localFiles) == 0 {
			dialog.ShowError(fmt.Errorf("No local files selected"), sjt.window)
			return
		}
	case InputModeRemoteFiles:
		if len(sjt.remoteFileIDs) == 0 {
			dialog.ShowError(fmt.Errorf("No remote files selected"), sjt.window)
			return
		}
	default:
		dialog.ShowError(fmt.Errorf("No input mode selected"), sjt.window)
		return
	}

	// Transition to Executing state
	sjt.previousState = sjt.state
	sjt.state = SJStateExecuting
	sjt.updateView()

	// Create cancellable context
	sjt.runLock.Lock()
	sjt.ctx, sjt.cancel = context.WithCancel(context.Background())
	sjt.isRunning = true
	sjt.runLock.Unlock()

	// Start execution in background
	go func() {
		var err error
		var jobID string

		defer func() {
			sjt.runLock.Lock()
			sjt.isRunning = false
			sjt.runLock.Unlock()

			// Update UI on completion
			fyne.Do(func() {
				if err != nil {
					sjt.setError(err.Error())
				} else {
					sjt.submittedJobID = jobID
					sjt.state = SJStateCompleted
					sjt.updateView()
					dialog.ShowInformation("Success",
						fmt.Sprintf("Job submitted successfully!\n\nJob ID: %s", jobID),
						sjt.window)
				}
			})
		}()

		switch sjt.inputMode {
		case InputModeDirectory:
			jobID, err = sjt.executeDirectoryMode()
		case InputModeLocalFiles:
			jobID, err = sjt.executeLocalFilesMode()
		case InputModeRemoteFiles:
			jobID, err = sjt.executeRemoteFilesMode()
		}
	}()
}

// executeDirectoryMode handles directory tar+upload+submit using the pipeline
func (sjt *SingleJobTab) executeDirectoryMode() (string, error) {
	debugf("SingleJobTab: executeDirectoryMode for directory: %s\n", sjt.directory)

	// Create a job spec with the directory
	job := *sjt.job
	job.Directory = sjt.directory

	// Use RunFromSpecs with a single job
	jobs := []models.JobSpec{job}

	// Generate a state file for tracking
	stateFile := generateStateFilePath()

	// Run the pipeline
	err := sjt.engine.RunFromSpecs(sjt.ctx, jobs, stateFile)
	if err != nil {
		return "", fmt.Errorf("pipeline failed: %w", err)
	}

	// Extract job ID from state file
	stateMgr := state.NewManager(stateFile)
	if loadErr := stateMgr.Load(); loadErr != nil {
		debugf("SingleJobTab: Warning - could not load state file: %v\n", loadErr)
		return "Submitted (job ID unavailable)", nil
	}

	// Get the first (and only) job state
	states := stateMgr.GetAllStates()
	if len(states) > 0 && states[0].JobID != "" {
		debugf("SingleJobTab: Extracted job ID: %s\n", states[0].JobID)
		return states[0].JobID, nil
	}

	debugf("SingleJobTab: No job ID found in state file\n")
	return "Submitted (job ID unavailable)", nil
}

// executeLocalFilesMode uploads local files individually then submits job
func (sjt *SingleJobTab) executeLocalFilesMode() (string, error) {
	debugf("SingleJobTab: executeLocalFilesMode with %d files\n", len(sjt.localFiles))

	apiClient := sjt.engine.API()
	if apiClient == nil {
		return "", fmt.Errorf("API client not available")
	}

	totalFiles := len(sjt.localFiles)

	// Upload each file and collect file IDs
	var fileIDs []string
	for i, localPath := range sjt.localFiles {
		select {
		case <-sjt.ctx.Done():
			return "", sjt.ctx.Err()
		default:
		}

		fileName := filepath.Base(localPath)
		debugf("SingleJobTab: Uploading file: %s\n", localPath)

		// Update status to show which file is being uploaded
		// All widget updates must be on main thread (Fyne 2.5+ requirement)
		fyne.Do(func() {
			sjt.statusLabel.SetText(fmt.Sprintf("Uploading %s (%d/%d)...", fileName, i+1, totalFiles))
		})

		// Create progress callback that updates the progress bar
		// Progress within current file is scaled to overall progress across all files
		fileIndex := i
		progressCallback := func(progress float64) {
			// Handle timer reset signal (negative progress)
			if progress < 0 {
				return
			}
			// Calculate overall progress: completed files + current file's progress
			overallProgress := (float64(fileIndex) + progress) / float64(totalFiles)
			// All widget updates must be on main thread (Fyne 2.5+ requirement)
			fyne.Do(func() {
				sjt.progressBar.SetValue(overallProgress)
			})
		}

		// Upload the file with progress callback
		cloudFile, err := upload.UploadFile(sjt.ctx, upload.UploadParams{
			LocalPath:        localPath,
			APIClient:        apiClient,
			ProgressCallback: progressCallback,
		})
		if err != nil {
			return "", fmt.Errorf("failed to upload %s: %w", fileName, err)
		}

		fileIDs = append(fileIDs, cloudFile.ID)
		debugf("SingleJobTab: Uploaded file %s -> ID: %s\n", localPath, cloudFile.ID)

		// Update progress bar to show this file complete
		// All widget updates must be on main thread (Fyne 2.5+ requirement)
		fyne.Do(func() {
			sjt.progressBar.SetValue(float64(i+1) / float64(totalFiles))
		})
	}

	// Create and submit job with uploaded files
	return sjt.createAndSubmitJobWithFiles(fileIDs)
}

// executeRemoteFilesMode submits job using already-uploaded remote files
func (sjt *SingleJobTab) executeRemoteFilesMode() (string, error) {
	debugf("SingleJobTab: executeRemoteFilesMode with %d file IDs\n", len(sjt.remoteFileIDs))

	// Create and submit job with selected remote files
	return sjt.createAndSubmitJobWithFiles(sjt.remoteFileIDs)
}

// createAndSubmitJobWithFiles creates and submits a job using file IDs as inputs.
// Uses the shared pipeline.BuildJobRequest for JobSpec → JobRequest conversion
// to ensure consistency with PUR pipeline job creation.
func (sjt *SingleJobTab) createAndSubmitJobWithFiles(fileIDs []string) (string, error) {
	apiClient := sjt.engine.API()
	if apiClient == nil {
		return "", fmt.Errorf("API client not available")
	}

	// Use shared BuildJobRequest for JobSpec → JobRequest conversion
	// This ensures consistency with PUR pipeline job creation
	jobReq, err := pipeline.BuildJobRequest(*sjt.job, fileIDs)
	if err != nil {
		return "", fmt.Errorf("failed to build job request: %w", err)
	}

	// Create the job
	debugf("SingleJobTab: Creating job: %s\n", sjt.job.JobName)
	jobResp, err := apiClient.CreateJob(sjt.ctx, *jobReq)
	if err != nil {
		return "", fmt.Errorf("failed to create job: %w", err)
	}

	debugf("SingleJobTab: Job created with ID: %s\n", jobResp.ID)

	// Submit the job if submit mode requires it
	if sjt.job.SubmitMode == "" || sjt.job.SubmitMode == "create_and_submit" {
		debugf("SingleJobTab: Submitting job: %s\n", jobResp.ID)
		err = apiClient.SubmitJob(sjt.ctx, jobResp.ID)
		if err != nil {
			return jobResp.ID, fmt.Errorf("job created but submit failed: %w", err)
		}
		debugf("SingleJobTab: Job submitted successfully\n")
	}

	return jobResp.ID, nil
}

// stopExecution stops the current execution
func (sjt *SingleJobTab) stopExecution() {
	debugf("SingleJobTab: stopExecution called\n")

	sjt.runLock.Lock()
	defer sjt.runLock.Unlock()

	if !sjt.isRunning {
		debugf("SingleJobTab: Not running, nothing to stop\n")
		return
	}

	if sjt.cancel != nil {
		sjt.cancel()
	}

	// Also stop the engine if using pipeline mode
	sjt.engine.Stop()
}
