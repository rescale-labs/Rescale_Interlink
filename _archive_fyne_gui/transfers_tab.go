// Package gui provides the graphical user interface for rescale-int.
// Transfers tab implementation - v3.6.3: Queue-based transfer management.
package gui

import (
	"context"
	"fmt"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/transfer"
)

// TransfersTab displays and manages the transfer queue.
// v3.6.3: New tab for centralized transfer management.
type TransfersTab struct {
	queue    *transfer.Queue
	eventBus *events.EventBus
	window   fyne.Window
	logger   *logging.Logger

	// UI components
	container    *fyne.Container
	statsLabel   *widget.Label
	taskList     *widget.List
	emptyLabel   *widget.Label // Shown when queue is empty

	// Action buttons
	cancelAllBtn       *widget.Button
	clearCompletedBtn  *widget.Button

	// Task display state (cached for list rendering)
	mu          sync.RWMutex
	cachedTasks []transfer.TransferTask

	// Event subscription lifecycle
	ctx    context.Context
	cancel context.CancelFunc
}

// NewTransfersTab creates a new transfers tab.
func NewTransfersTab(queue *transfer.Queue, eventBus *events.EventBus, window fyne.Window) *TransfersTab {
	ctx, cancel := context.WithCancel(context.Background())
	return &TransfersTab{
		queue:       queue,
		eventBus:    eventBus,
		window:      window,
		logger:      logging.NewLogger("transfers-tab", nil),
		cachedTasks: make([]transfer.TransferTask, 0),
		ctx:         ctx,
		cancel:      cancel,
	}
}

// Build creates the transfers tab UI.
func (tt *TransfersTab) Build() fyne.CanvasObject {
	// Stats label at top
	tt.statsLabel = widget.NewLabel("No transfers")
	tt.statsLabel.TextStyle = fyne.TextStyle{Bold: true}

	// Action buttons
	tt.cancelAllBtn = widget.NewButtonWithIcon("Cancel All", theme.CancelIcon(), tt.onCancelAll)
	tt.cancelAllBtn.Importance = widget.DangerImportance
	tt.cancelAllBtn.Disable()

	tt.clearCompletedBtn = widget.NewButtonWithIcon("Clear Completed", theme.DeleteIcon(), tt.onClearCompleted)
	tt.clearCompletedBtn.Importance = widget.MediumImportance
	tt.clearCompletedBtn.Disable()

	// Header row with stats and buttons
	buttonsRow := container.NewHBox(
		tt.cancelAllBtn,
		tt.clearCompletedBtn,
	)
	headerRow := container.NewBorder(
		nil, nil,
		container.NewHBox(HorizontalSpacer(8), tt.statsLabel),
		container.NewHBox(buttonsRow, HorizontalSpacer(8)),
		nil,
	)

	// Empty state label (shown when no tasks)
	tt.emptyLabel = widget.NewLabel("No transfers in queue.\n\nUse the File Browser to upload or download files.\nTransfers will appear here.")
	tt.emptyLabel.Alignment = fyne.TextAlignCenter

	// Task list - each row shows a transfer with progress
	tt.taskList = widget.NewList(
		// Length function
		func() int {
			tt.mu.RLock()
			defer tt.mu.RUnlock()
			return len(tt.cachedTasks)
		},
		// Create item template
		func() fyne.CanvasObject {
			return tt.createTaskRow()
		},
		// Update item with data
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			tt.updateTaskRow(id, obj)
		},
	)
	tt.taskList.Hide() // Hidden initially, shown when tasks exist

	// Main content area with scrollable task list
	content := container.NewStack(
		container.NewCenter(tt.emptyLabel),
		tt.taskList,
	)

	// Full layout
	tt.container = container.NewBorder(
		container.NewVBox(
			VerticalSpacer(8),
			headerRow,
			widget.NewSeparator(),
			VerticalSpacer(4),
		),
		nil, nil, nil,
		content,
	)

	// Initial refresh
	tt.refreshTasks()

	return tt.container
}

// createTaskRow creates a template row for a task.
// Layout: [Icon] [Name + Size] [Progress Bar] [Status/Speed] [Action Button]
func (tt *TransfersTab) createTaskRow() fyne.CanvasObject {
	// Direction icon (↑ or ↓)
	icon := widget.NewLabel("↑")
	icon.TextStyle = fyne.TextStyle{Bold: true}

	// Name and size label
	nameLabel := widget.NewLabel("filename.dat (10 MB)")

	// Progress bar
	progressBar := widget.NewProgressBar()
	progressBar.SetValue(0)

	// Status/speed label
	statusLabel := widget.NewLabel("Queued")

	// Action button (Cancel or Retry)
	actionBtn := widget.NewButtonWithIcon("", theme.CancelIcon(), nil)
	actionBtn.Importance = widget.LowImportance

	// Fixed widths for consistent layout
	iconContainer := container.NewGridWrap(fyne.NewSize(24, 20), icon)
	nameContainer := container.NewGridWrap(fyne.NewSize(200, 20), nameLabel)
	statusContainer := container.NewGridWrap(fyne.NewSize(120, 20), statusLabel)
	actionContainer := container.NewGridWrap(fyne.NewSize(80, 30), actionBtn)

	// Row layout: icon | name | progress | status | action
	row := container.NewBorder(
		nil, nil,
		container.NewHBox(iconContainer, nameContainer),
		container.NewHBox(statusContainer, actionContainer),
		progressBar,
	)

	return container.NewPadded(row)
}

// updateTaskRow updates a task row with current data.
func (tt *TransfersTab) updateTaskRow(id widget.ListItemID, obj fyne.CanvasObject) {
	tt.mu.RLock()
	if id >= len(tt.cachedTasks) {
		tt.mu.RUnlock()
		return
	}
	task := tt.cachedTasks[id]
	tt.mu.RUnlock()

	// Navigate to child widgets
	padded, ok := obj.(*fyne.Container)
	if !ok || len(padded.Objects) == 0 {
		return
	}
	row, ok := padded.Objects[0].(*fyne.Container)
	if !ok {
		return
	}

	// Extract widgets from the border layout
	// Left side: HBox with icon and name containers
	// Right side: HBox with status and action containers
	// Center: progress bar

	// Get the left HBox (icon + name)
	leftBox, ok := row.Objects[1].(*fyne.Container) // Objects[1] is left in Border
	if !ok || len(leftBox.Objects) < 2 {
		return
	}
	iconContainer, _ := leftBox.Objects[0].(*fyne.Container)
	nameContainer, _ := leftBox.Objects[1].(*fyne.Container)

	// Get the right HBox (status + action)
	rightBox, ok := row.Objects[2].(*fyne.Container) // Objects[2] is right in Border
	if !ok || len(rightBox.Objects) < 2 {
		return
	}
	statusContainer, _ := rightBox.Objects[0].(*fyne.Container)
	actionContainer, _ := rightBox.Objects[1].(*fyne.Container)

	// Get progress bar (center object)
	progressBar, _ := row.Objects[0].(*widget.ProgressBar) // Objects[0] is center in Border

	// Extract actual widgets from containers
	var iconLabel, nameLabel, statusLabel *widget.Label
	var actionBtn *widget.Button

	if iconContainer != nil && len(iconContainer.Objects) > 0 {
		iconLabel, _ = iconContainer.Objects[0].(*widget.Label)
	}
	if nameContainer != nil && len(nameContainer.Objects) > 0 {
		nameLabel, _ = nameContainer.Objects[0].(*widget.Label)
	}
	if statusContainer != nil && len(statusContainer.Objects) > 0 {
		statusLabel, _ = statusContainer.Objects[0].(*widget.Label)
	}
	if actionContainer != nil && len(actionContainer.Objects) > 0 {
		actionBtn, _ = actionContainer.Objects[0].(*widget.Button)
	}

	// Update icon based on task type
	if iconLabel != nil {
		if task.Type == transfer.TaskTypeUpload {
			iconLabel.SetText("↑")
		} else {
			iconLabel.SetText("↓")
		}
	}

	// Update name with size
	if nameLabel != nil {
		displayName := task.Name
		if len(displayName) > 25 {
			displayName = displayName[:22] + "..."
		}
		sizeStr := FormatFileSize(task.Size)
		nameLabel.SetText(fmt.Sprintf("%s (%s)", displayName, sizeStr))
	}

	// Update progress bar
	if progressBar != nil {
		progressBar.SetValue(task.Progress)
	}

	// Update status label based on state
	if statusLabel != nil {
		switch task.State {
		case transfer.TaskQueued:
			statusLabel.SetText("Queued")
		case transfer.TaskInitializing:
			statusLabel.SetText("Initializing...")
		case transfer.TaskActive:
			if task.Speed > 0 {
				statusLabel.SetText(fmt.Sprintf("%s", FormatTransferRate(task.Speed)))
			} else {
				statusLabel.SetText("Transferring...")
			}
		case transfer.TaskCompleted:
			statusLabel.SetText("✓ Complete")
		case transfer.TaskFailed:
			errMsg := "Failed"
			if task.Error != nil {
				errMsg = task.Error.Error()
				if len(errMsg) > 15 {
					errMsg = errMsg[:12] + "..."
				}
			}
			statusLabel.SetText("✗ " + errMsg)
		case transfer.TaskCancelled:
			statusLabel.SetText("Cancelled")
		case transfer.TaskPaused:
			statusLabel.SetText("Paused")
		default:
			statusLabel.SetText(string(task.State))
		}
	}

	// Update action button based on state
	if actionBtn != nil {
		taskID := task.ID // Capture for closure

		switch task.State {
		case transfer.TaskQueued, transfer.TaskInitializing, transfer.TaskActive, transfer.TaskPaused:
			// Can cancel
			actionBtn.SetIcon(theme.CancelIcon())
			actionBtn.SetText("")
			actionBtn.OnTapped = func() {
				tt.onCancelTask(taskID)
			}
			actionBtn.Enable()
			actionBtn.Show()
		case transfer.TaskFailed, transfer.TaskCancelled:
			// Can retry
			actionBtn.SetIcon(theme.ViewRefreshIcon())
			actionBtn.SetText("")
			actionBtn.OnTapped = func() {
				tt.onRetryTask(taskID)
			}
			actionBtn.Enable()
			actionBtn.Show()
		case transfer.TaskCompleted:
			// No action needed
			actionBtn.Hide()
		default:
			actionBtn.Hide()
		}
	}
}

// Start begins listening for transfer events.
func (tt *TransfersTab) Start() {
	if tt.eventBus == nil {
		return
	}

	// Subscribe to all transfer event types
	eventTypes := []events.EventType{
		events.EventTransferQueued,
		events.EventTransferInitializing,
		events.EventTransferStarted,
		events.EventTransferProgress,
		events.EventTransferCompleted,
		events.EventTransferFailed,
		events.EventTransferCancelled,
	}

	for _, eventType := range eventTypes {
		ch := tt.eventBus.Subscribe(eventType)
		go tt.processEvents(ch)
	}

	tt.logger.Debug().Msg("Transfers tab event listeners started")
}

// processEvents handles events from a subscription channel.
func (tt *TransfersTab) processEvents(ch <-chan events.Event) {
	// Panic recovery
	defer func() {
		if r := recover(); r != nil {
			tt.logger.Error().Msgf("PANIC in transfer event handler: %v", r)
		}
	}()

	// Throttle UI updates to prevent excessive refreshes
	// Batch progress events and update at most 4 times per second
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	pendingUpdate := false

	for {
		select {
		case <-tt.ctx.Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			// Mark that we need to refresh
			pendingUpdate = true

			// For non-progress events, refresh immediately
			if event.Type() != events.EventTransferProgress {
				tt.refreshTasks()
				pendingUpdate = false
			}
		case <-ticker.C:
			// Batch update for progress events
			if pendingUpdate {
				tt.refreshTasks()
				pendingUpdate = false
			}
		}
	}
}

// Stop stops listening for events.
func (tt *TransfersTab) Stop() {
	tt.cancel()
}

// refreshTasks updates the cached task list and refreshes the UI.
func (tt *TransfersTab) refreshTasks() {
	if tt.queue == nil {
		return
	}

	// Get current tasks from queue
	tasks := tt.queue.GetTasks()
	stats := tt.queue.GetStats()

	// Update cache
	tt.mu.Lock()
	tt.cachedTasks = tasks
	tt.mu.Unlock()

	// Update UI on main thread
	fyne.Do(func() {
		// Update stats label
		if stats.Total() == 0 {
			tt.statsLabel.SetText("No transfers")
		} else {
			parts := make([]string, 0, 4)
			if stats.Queued > 0 {
				parts = append(parts, fmt.Sprintf("%d queued", stats.Queued))
			}
			if stats.Active > 0 {
				parts = append(parts, fmt.Sprintf("%d active", stats.Active))
			}
			if stats.Completed > 0 {
				parts = append(parts, fmt.Sprintf("%d completed", stats.Completed))
			}
			if stats.Failed > 0 {
				parts = append(parts, fmt.Sprintf("%d failed", stats.Failed))
			}
			if stats.Cancelled > 0 {
				parts = append(parts, fmt.Sprintf("%d cancelled", stats.Cancelled))
			}

			statsText := ""
			for i, p := range parts {
				if i > 0 {
					statsText += " | "
				}
				statsText += p
			}
			tt.statsLabel.SetText(statsText)
		}

		// Update button states
		hasActiveOrQueued := stats.Queued > 0 || stats.Active > 0 || stats.Paused > 0
		hasCompleted := stats.Completed > 0 || stats.Failed > 0 || stats.Cancelled > 0

		if hasActiveOrQueued {
			tt.cancelAllBtn.Enable()
		} else {
			tt.cancelAllBtn.Disable()
		}

		if hasCompleted {
			tt.clearCompletedBtn.Enable()
		} else {
			tt.clearCompletedBtn.Disable()
		}

		// Show/hide empty state vs task list
		if len(tasks) == 0 {
			tt.emptyLabel.Show()
			tt.taskList.Hide()
		} else {
			tt.emptyLabel.Hide()
			tt.taskList.Show()
			tt.taskList.Refresh()
		}
	})
}

// onCancelAll cancels all queued and active tasks.
func (tt *TransfersTab) onCancelAll() {
	if tt.queue == nil {
		return
	}
	tt.queue.CancelAll()
	tt.logger.Info().Msg("Cancelled all transfers")
}

// onClearCompleted clears completed, failed, and cancelled tasks from history.
func (tt *TransfersTab) onClearCompleted() {
	if tt.queue == nil {
		return
	}
	tt.queue.ClearCompleted()
	tt.refreshTasks()
	tt.logger.Info().Msg("Cleared completed transfers")
}

// onCancelTask cancels a specific task.
func (tt *TransfersTab) onCancelTask(taskID string) {
	if tt.queue == nil {
		return
	}
	if err := tt.queue.Cancel(taskID); err != nil {
		tt.logger.Error().Err(err).Str("task_id", taskID).Msg("Failed to cancel task")
	}
}

// onRetryTask retries a failed or cancelled task.
func (tt *TransfersTab) onRetryTask(taskID string) {
	if tt.queue == nil {
		return
	}
	newID, err := tt.queue.Retry(taskID)
	if err != nil {
		tt.logger.Error().Err(err).Str("task_id", taskID).Msg("Failed to retry task")
		return
	}
	if newID != "" {
		tt.logger.Info().Str("old_id", taskID).Str("new_id", newID).Msg("Task retry queued")
	}
}

// SetQueue sets the transfer queue (allows late binding).
func (tt *TransfersTab) SetQueue(queue *transfer.Queue) {
	tt.queue = queue
	tt.refreshTasks()
}
