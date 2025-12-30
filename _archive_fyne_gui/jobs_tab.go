package gui

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/models"
)

// JobsTab manages the jobs interface with workflow-based UI
type JobsTab struct {
	// Core dependencies
	engine *core.Engine
	window fyne.Window
	app    fyne.App

	// State management
	workflow   *JobsWorkflow
	apiCache   *APICache
	workflowUI *WorkflowUIComponents

	// UI containers
	mainContainer        *fyne.Container
	progressBarContainer *fyne.Container
	contentContainer     *fyne.Container
	tableContainer       *fyne.Container
	statusContainer      *fyne.Container

	// Jobs table
	table       *widget.Table
	progressBar *widget.ProgressBar
	statsLabel  *widget.Label

	// Data
	loadedJobs      []JobRow
	jobIndexByName  map[string]int // PERFORMANCE: O(1) lookup of job index by name
	jobsLock        sync.RWMutex

	// Execution state
	ctx       context.Context
	cancel    context.CancelFunc
	isRunning bool

	// Refresh throttling
	lastRefresh  time.Time
	refreshMutex sync.Mutex

	// Double-click detection for job editing
	doubleClickDetector *DoubleClickDetector
}

// JobRow represents a row in the jobs table
type JobRow struct {
	Index          int
	Directory      string
	JobName        string
	TarStatus      string
	UploadStatus   string
	UploadProgress float64 // 0.0 to 1.0, for displaying upload percentage
	CreateStatus   string
	SubmitStatus   string
	Status         string
	JobID          string
	Progress       float64
	Error          string
}

// NewJobsTab creates a new jobs tab
func NewJobsTab(engine *core.Engine, window fyne.Window, app fyne.App) *JobsTab {
	jt := &JobsTab{
		engine:              engine,
		window:              window,
		app:                 app,
		workflow:            NewJobsWorkflow(),
		apiCache:            NewAPICache(),
		loadedJobs:          []JobRow{},
		jobIndexByName:      make(map[string]int),
		progressBar:         widget.NewProgressBar(),
		statsLabel:          widget.NewLabel(""),
		doubleClickDetector: NewDoubleClickDetector(),
	}

	jt.workflowUI = NewWorkflowUIComponents(jt)

	// Start fetching core types in background
	go jt.fetchCoreTypes()

	// v3.6.1: Fetch automations in background
	go jt.fetchAutomations()

	return jt
}

// Build creates the jobs tab UI
func (jt *JobsTab) Build() fyne.CanvasObject {
	// Create progress bar (breadcrumb)
	jt.progressBarContainer = jt.workflowUI.CreateProgressBar()

	// Create content container (will change based on state)
	jt.contentContainer = container.NewVBox()

	// Create jobs table
	jt.createTable()

	// Create scroll container with minimum size so table is visible
	// Uses AcceleratedScroll for faster scroll speed (3x Fyne default)
	scrollContainer := NewAcceleratedScroll(jt.table)
	scrollContainer.SetMinSize(fyne.NewSize(800, 300)) // Set minimum size for table visibility

	// Jobs label with bold styling
	jobsLabel := widget.NewLabel("Jobs:")
	jobsLabel.TextStyle = fyne.TextStyle{Bold: true}

	jt.tableContainer = container.NewVBox(
		VerticalSpacer(8),
		widget.NewSeparator(),
		VerticalSpacer(8),
		jobsLabel,
		VerticalSpacer(4),
		scrollContainer,
	)
	jt.tableContainer.Hide() // Hidden until jobs are loaded

	// Create status container with improved spacing
	statusLabel := widget.NewLabel("Status:")
	statusLabel.TextStyle = fyne.TextStyle{Bold: true}

	jt.statusContainer = container.NewVBox(
		VerticalSpacer(8),
		widget.NewSeparator(),
		VerticalSpacer(8),
		container.NewHBox(
			statusLabel,
			HorizontalSpacer(8),
			jt.statsLabel,
		),
		VerticalSpacer(4),
		jt.progressBar,
		VerticalSpacer(8),
	)

	// Main container layout with improved spacing
	jt.mainContainer = container.NewBorder(
		container.NewVBox(
			VerticalSpacer(4),
			container.NewPadded(jt.progressBarContainer),
			jt.contentContainer,
		),
		jt.statusContainer,
		nil,
		nil,
		jt.tableContainer,
	)

	// Initialize view
	jt.updateView()

	return jt.mainContainer
}

// updateView updates the UI based on current workflow state
func (jt *JobsTab) updateView() {
	debugf("updateView called, CurrentState=%s, isRunning=%v\n", jt.workflow.CurrentState, jt.isRunning)

	// Update progress bar
	jt.progressBarContainer.Objects = []fyne.CanvasObject{
		jt.workflowUI.CreateProgressBar(),
	}
	jt.progressBarContainer.Refresh()

	// Update content based on state
	var newContent fyne.CanvasObject

	switch jt.workflow.CurrentState {
	case StateInitial:
		newContent = jt.workflowUI.CreatePathSelectionView()
		jt.tableContainer.Hide()

	case StatePathChosen:
		if jt.workflow.CurrentPath == PathCreateNew {
			newContent = jt.workflowUI.CreateTemplateSelectionView()
		} else {
			// For LoadCSV path, we go directly to validation after loading
			newContent = widget.NewLabel("Loading jobs...")
		}
		jt.tableContainer.Hide()

	case StateTemplateReady:
		newContent = jt.workflowUI.CreateDirectoryScanView()
		jt.tableContainer.Hide()

	case StateDirectoriesScanned, StateJobsValidated:
		debugf("JobsValidated state, loaded jobs count=%d\n", len(jt.loadedJobs))
		debugf("Calling tableContainer.Show()\n")
		newContent = jt.workflowUI.CreateExecutionView()
		jt.tableContainer.Show()
		debugf("tableContainer.Show() completed, table should be visible=%v\n", jt.tableContainer.Visible())

	case StateExecuting:
		// Only show ProgressView if actually running
		// If stopped (isRunning=false), show ExecutionView instead
		if jt.isRunning {
			debugf("Creating ProgressView (pipeline is running)\n")
			newContent = jt.workflowUI.CreateProgressView()
		} else {
			debugf("Creating ExecutionView (pipeline stopped but state still Executing)\n")
			newContent = jt.workflowUI.CreateExecutionView()
		}
		jt.tableContainer.Show()

	case StateCompleted:
		debugf("Creating CompletedView\n")
		newContent = jt.workflowUI.CreateCompletedView()
		jt.tableContainer.Show()

	case StateError:
		debugf("Creating ErrorView\n")
		newContent = jt.workflowUI.CreateErrorView()
		jt.tableContainer.Show()

	default:
		newContent = widget.NewLabel("Unknown state")
	}

	// Update content container
	jt.contentContainer.Objects = []fyne.CanvasObject{newContent}
	jt.contentContainer.Refresh()

	jt.mainContainer.Refresh()
}

// createTable creates the jobs table
func (jt *JobsTab) createTable() {
	headers := []string{
		"#", "Job Name", "Directory", "Tar", "Upload",
		"Create", "Submit", "Status", "Job ID",
	}

	jt.table = widget.NewTable(
		// Length
		func() (int, int) {
			jt.jobsLock.RLock()
			defer jt.jobsLock.RUnlock()
			rows := len(jt.loadedJobs) + 1
			cols := len(headers)
			return rows, cols
		},
		// Create cell
		func() fyne.CanvasObject {
			return widget.NewLabel("cell")
		},
		// Update cell
		func(cell widget.TableCellID, obj fyne.CanvasObject) {
			label := obj.(*widget.Label)

			if cell.Row == 0 {
				// Header row
				if cell.Col < len(headers) {
					label.SetText(headers[cell.Col])
					label.TextStyle = fyne.TextStyle{Bold: true}
				}
				return
			}

			// Data row
			jt.jobsLock.RLock()
			defer jt.jobsLock.RUnlock()

			rowIndex := cell.Row - 1
			if rowIndex >= len(jt.loadedJobs) {
				return
			}

			job := jt.loadedJobs[rowIndex]

			switch cell.Col {
			case 0: // Index
				label.SetText(fmt.Sprintf("%d", job.Index+1))
			case 1: // Job Name
				label.SetText(job.JobName)
			case 2: // Directory
				label.SetText(filepath.Base(job.Directory))
			case 3: // Tar
				label.SetText(jt.formatStatus(job.TarStatus))
			case 4: // Upload
				// v3.2.3: Show percentage with 2 decimal places for consistency
				if job.UploadStatus == "in_progress" && job.UploadProgress > 0 {
					label.SetText(fmt.Sprintf("Uploading %.2f%%", job.UploadProgress*100))
				} else {
					label.SetText(jt.formatStatus(job.UploadStatus))
				}
			case 5: // Create
				label.SetText(jt.formatStatus(job.CreateStatus))
			case 6: // Submit
				label.SetText(jt.formatStatus(job.SubmitStatus))
			case 7: // Status
				status := job.Status
				// v3.2.3: Show percentage with 2 decimal places for consistency
				if job.Progress > 0 && job.Progress < 1.0 {
					status += fmt.Sprintf(" (%.2f%%)", job.Progress*100)
				}
				label.SetText(status)
			case 8: // Job ID
				if job.JobID != "" {
					label.SetText(job.JobID)
				} else {
					label.SetText("-")
				}
			}

			// Color coding for errors
			if job.Error != "" {
				label.TextStyle = fyne.TextStyle{Bold: true}
			} else {
				label.TextStyle = fyne.TextStyle{}
			}
		},
	)

	// Set column widths
	jt.table.SetColumnWidth(0, 40)  // Index
	jt.table.SetColumnWidth(1, 180) // Job Name
	jt.table.SetColumnWidth(2, 200) // Directory
	jt.table.SetColumnWidth(3, 90)  // Tar (wider for "Working...")
	jt.table.SetColumnWidth(4, 120) // Upload (wider for "Uploading 100%")
	jt.table.SetColumnWidth(5, 90)  // Create (wider for "Working...")
	jt.table.SetColumnWidth(6, 90)  // Submit (wider for "Working...")
	jt.table.SetColumnWidth(7, 150) // Status
	jt.table.SetColumnWidth(8, 120) // Job ID

	// Add selection handler with double-click detection for job editing
	jt.table.OnSelected = func(id widget.TableCellID) {
		if id.Row == 0 {
			// Header row - ignore
			return
		}

		rowIndex := id.Row - 1

		jt.jobsLock.RLock()
		if rowIndex < 0 || rowIndex >= len(jt.loadedJobs) {
			jt.jobsLock.RUnlock()
			return
		}
		job := jt.loadedJobs[rowIndex]
		jt.jobsLock.RUnlock()

		// Check for double-click to edit job
		if jt.doubleClickDetector.IsDoubleClick(rowIndex) {
			// Don't allow editing during execution
			if jt.isRunning {
				dialog.ShowInformation("Cannot Edit",
					"Cannot edit jobs while pipeline is running.",
					jt.window)
				return
			}

			// Open job editor
			jt.editJob(rowIndex)
			return
		}

		// Single click: If job has an error, show it in a dialog
		if job.Error != "" {
			dialog.ShowInformation(
				fmt.Sprintf("Job Error: %s", job.JobName),
				fmt.Sprintf("This job failed with the following error:\n\n%s", job.Error),
				jt.window,
			)
		}
	}
}

// formatStatus formats status for display
func (jt *JobsTab) formatStatus(status string) string {
	switch status {
	case "pending":
		return "Pending"
	case "in_progress":
		return "Working..."
	case "completed":
		return "Done"
	case "failed":
		return "Failed"
	case "success":
		return "Done"
	default:
		return "-"
	}
}

// UpdateProgress updates job progress from events
// PERFORMANCE: Uses O(1) index map lookup instead of O(n) linear search
func (jt *JobsTab) UpdateProgress(event *events.ProgressEvent) {
	if event.Stage == "overall" {
		// v3.4.0 fix: All widget updates must be on main thread (Fyne 2.5+ requirement)
		fyne.Do(func() {
			jt.progressBar.SetValue(event.Progress)
		})
		jt.updateStats()
		return
	}

	// Update specific job using O(1) index lookup
	jt.jobsLock.Lock()

	if idx, ok := jt.jobIndexByName[event.JobName]; ok && idx < len(jt.loadedJobs) {
		jt.loadedJobs[idx].Progress = event.Progress

		// Update stage-specific status
		switch event.Stage {
		case "tar":
			if event.Progress >= 1.0 {
				jt.loadedJobs[idx].TarStatus = "completed"
			} else {
				jt.loadedJobs[idx].TarStatus = "in_progress"
			}
		case "upload":
			if event.Progress >= 1.0 {
				jt.loadedJobs[idx].UploadStatus = "completed"
			} else {
				jt.loadedJobs[idx].UploadStatus = "in_progress"
			}
		case "create":
			if event.Progress >= 1.0 {
				jt.loadedJobs[idx].CreateStatus = "completed"
			} else {
				jt.loadedJobs[idx].CreateStatus = "in_progress"
			}
		case "submit":
			if event.Progress >= 1.0 {
				jt.loadedJobs[idx].SubmitStatus = "completed"
			} else {
				jt.loadedJobs[idx].SubmitStatus = "in_progress"
			}
		}

		// Update overall status
		jt.loadedJobs[idx].Status = event.Message
	}

	// CRITICAL FIX: Release lock BEFORE calling refresh to avoid deadlock
	jt.jobsLock.Unlock()

	// v3.4.0 fix: Refresh table on main thread (Fyne 2.5+ requirement)
	// This prevents crashes on Linux/Wayland when called from event monitor goroutines
	fyne.Do(func() {
		if jt.table != nil {
			jt.table.Refresh()
		}
	})
}

// UpdateJobState updates job state from state change events
// PERFORMANCE: Uses O(1) index map lookup instead of O(n) linear search
func (jt *JobsTab) UpdateJobState(event *events.StateChangeEvent) {
	debugf("UpdateJobState called: job=%s, stage=%s, status=%s, progress=%.2f\n",
		event.JobName, event.Stage, event.NewStatus, event.UploadProgress)

	// Update data with lock held
	jt.jobsLock.Lock()

	// Debug: print loaded jobs
	debugf("Looking for job '%s' in %d loaded jobs\n", event.JobName, len(jt.loadedJobs))

	// PERFORMANCE: Use O(1) index lookup instead of linear search
	if idx, ok := jt.jobIndexByName[event.JobName]; ok && idx < len(jt.loadedJobs) {
		debugf("Found job at index %d, updating status\n", idx)
		jt.loadedJobs[idx].Status = event.NewStatus
		if event.JobID != "" {
			jt.loadedJobs[idx].JobID = event.JobID
		}
		if event.ErrorMessage != "" {
			jt.loadedJobs[idx].Error = event.ErrorMessage
			jt.loadedJobs[idx].Status = "failed"
		}

		// Update stage status
		if event.Stage != "" {
			switch event.Stage {
			case "tar":
				jt.loadedJobs[idx].TarStatus = event.NewStatus
				debugf("Updated TarStatus to '%s'\n", event.NewStatus)
			case "upload":
				jt.loadedJobs[idx].UploadStatus = event.NewStatus
				jt.loadedJobs[idx].UploadProgress = event.UploadProgress
				debugf("Updated UploadStatus to '%s', progress=%.2f\n", event.NewStatus, event.UploadProgress)
			case "create":
				jt.loadedJobs[idx].CreateStatus = event.NewStatus
				debugf("Updated CreateStatus to '%s'\n", event.NewStatus)
			case "submit":
				jt.loadedJobs[idx].SubmitStatus = event.NewStatus
				debugf("Updated SubmitStatus to '%s'\n", event.NewStatus)
			}
		}
	} else {
		// Job not found, add it (from state load)
		debugf("Job '%s' not found in loadedJobs, adding it\n", event.JobName)
		newIdx := len(jt.loadedJobs)
		jt.loadedJobs = append(jt.loadedJobs, JobRow{
			Index:   newIdx,
			JobName: event.JobName,
			Status:  event.NewStatus,
			JobID:   event.JobID,
			Error:   event.ErrorMessage,
		})
		// Update index map
		jt.jobIndexByName[event.JobName] = newIdx
	}

	// CRITICAL FIX: Release lock BEFORE calling refresh
	// table.Refresh() will call back into our callbacks which need to acquire RLock
	// Calling refresh while holding the write lock causes deadlock!
	jt.jobsLock.Unlock()

	// Now safe to refresh - callbacks can acquire RLock without deadlocking
	jt.throttledRefresh()
}

// throttledRefresh refreshes the table but limits frequency to once per 100ms
// v3.4.0 fix: Uses fyne.Do() to ensure Refresh() is called on main thread
func (jt *JobsTab) throttledRefresh() {
	jt.refreshMutex.Lock()
	defer jt.refreshMutex.Unlock()

	now := time.Now()
	if now.Sub(jt.lastRefresh) < 100*time.Millisecond {
		// Too soon, skip this refresh
		return
	}

	jt.lastRefresh = now
	// v3.4.0 fix: All widget updates must be on main thread (Fyne 2.5+ requirement)
	fyne.Do(func() {
		if jt.table != nil {
			jt.table.Refresh()
		}
	})
}

// updateStats updates the statistics label
func (jt *JobsTab) updateStats() {
	jt.jobsLock.RLock()

	total := len(jt.loadedJobs)
	completed := 0
	failed := 0
	inProgress := 0

	for _, job := range jt.loadedJobs {
		if job.SubmitStatus == "completed" && job.Error == "" {
			completed++
		} else if job.Error != "" || job.Status == "failed" {
			failed++
		} else if job.TarStatus == "in_progress" || job.UploadStatus == "in_progress" ||
			job.CreateStatus == "in_progress" || job.SubmitStatus == "in_progress" {
			inProgress++
		}
	}

	pending := total - completed - failed - inProgress

	// Release lock BEFORE UI update to avoid deadlock
	jt.jobsLock.RUnlock()

	// v3.4.0 fix: All widget updates must be on main thread (Fyne 2.5+ requirement)
	fyne.Do(func() {
		jt.statsLabel.SetText(fmt.Sprintf("Total: %d | Completed: %d | In Progress: %d | Pending: %d | Failed: %d",
			total, completed, inProgress, pending, failed))
	})
}

// fetchCoreTypes fetches core types from API in background
func (jt *JobsTab) fetchCoreTypes() {
	// v3.4.0: Panic recovery for background goroutine
	defer func() {
		if r := recover(); r != nil {
			guiLogger.Error().Msgf("PANIC in fetchCoreTypes: %v", r)
		}
	}()

	// Only fetch if not already cached
	if _, isLoading, _ := jt.apiCache.GetCoreTypes(); isLoading {
		return
	}

	// Note: CoreTypes are fetched from the API cache which is populated
	// during initialization. No additional fetch is needed here.
}

// fetchAutomations fetches automations from API in background (v3.6.1)
func (jt *JobsTab) fetchAutomations() {
	defer func() {
		if r := recover(); r != nil {
			guiLogger.Error().Msgf("PANIC in fetchAutomations: %v", r)
		}
	}()

	// Only fetch if not already cached
	if len(jt.apiCache.GetAutomations()) > 0 {
		return
	}

	apiClient := jt.engine.API()
	if apiClient == nil {
		guiLogger.Debug().Msg("fetchAutomations: API client not available")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	automations, err := apiClient.ListAutomations(ctx)
	if err != nil {
		guiLogger.Debug().Err(err).Msg("Failed to fetch automations")
		return
	}

	jt.apiCache.SetAutomations(automations)
	guiLogger.Debug().Int("count", len(automations)).Msg("Cached automations")
}

// editJob opens the job editor dialog for the specified job index
// Version 2.7.1 - CSV-less PUR GUI feature
func (jt *JobsTab) editJob(rowIndex int) {
	// Get the job spec from workflow.ScannedJobs
	if rowIndex < 0 || rowIndex >= len(jt.workflow.ScannedJobs) {
		dialog.ShowError(
			fmt.Errorf("Invalid job index: %d", rowIndex),
			jt.window,
		)
		return
	}

	job := &jt.workflow.ScannedJobs[rowIndex]

	// Create and show the job editor
	editor := NewJobEditorDialog(jt.window, jt.apiCache, rowIndex, job, func(index int, updatedJob models.JobSpec) {
		// Update the job in workflow
		jt.workflow.ScannedJobs[index] = updatedJob

		// Update the display row
		jt.jobsLock.Lock()
		if index < len(jt.loadedJobs) {
			jt.loadedJobs[index].JobName = updatedJob.JobName
			jt.loadedJobs[index].Directory = updatedJob.Directory
		}
		jt.jobsLock.Unlock()

		// v3.4.0 fix: Refresh table on main thread (Fyne 2.5+ requirement)
		fyne.Do(func() {
			jt.table.Refresh()
		})

		// Show confirmation
		dialog.ShowInformation("Job Updated",
			fmt.Sprintf("Job '%s' has been updated successfully.", updatedJob.JobName),
			jt.window)
	})
	editor.Show()
}
