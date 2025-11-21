package gui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/state"
)

// debugf prints debug messages using the GUI logger at debug level
// Debug messages are only shown when RESCALE_DEBUG environment variable is set
func debugf(format string, args ...interface{}) {
	if guiLogger != nil {
		guiLogger.Debug().Msgf(format, args...)
	}
}

// performScan executes the directory scan and generates jobs CSV
func (jt *JobsTab) performScan(baseDir, pattern, validation, subpath, outputPath string) {
	// Show progress
	progressDialog := dialog.NewProgress("Scanning Directories",
		"Scanning directories and generating jobs CSV...", jt.window)
	progressDialog.Show()

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
				guiLogger.Error().Msgf("PANIC in scan goroutine: %v\n", r)
				fyne.Do(func() {
					dialog.ShowError(
						fmt.Errorf("An unexpected error occurred during scan: %v\n\nPlease check the console for details.", r),
						jt.window,
					)
					jt.workflow.SetError(fmt.Sprintf("Scan panic: %v", r))
					jt.updateView()
				})
			}
		}()

		// Prepare scan options
		opts := core.ScanOptions{
			OutputCSV:         outputPath,
			Pattern:           pattern,
			ValidationPattern: validation,
			RunSubpath:        subpath,
			Recursive:         false,
			IncludeHidden:     false,
			StartIndex:        1,
			IteratePatterns:   false,
			Overwrite:         true,
			MultiPartMode:     true,              // Use multi-part mode to specify base directory
			PartDirs:          []string{baseDir}, // Scan in user-specified directory
		}

		// We need to save the template to a temp file first
		// Create a temporary template CSV
		tempTemplate, err := jt.saveTemplateToTempFile()
		if err != nil {
			fyne.Do(func() {
				// Progress dialog hidden automatically by defer
				dialog.ShowError(fmt.Errorf("Failed to create template: %w", err), jt.window)
			})
			return
		}

		opts.TemplateCSV = tempTemplate

		// Execute scan
		err = jt.engine.Scan(opts)

		// Load the generated CSV (even if scan had errors, it might have created partial output)
		var jobs []models.JobSpec
		if err == nil {
			debugf("Scan completed successfully, loading CSV from: %s\n", outputPath)

			// Check if file exists and show its size
			if info, statErr := os.Stat(outputPath); statErr == nil {
				debugf("Output CSV exists, size: %d bytes\n", info.Size())
			} else {
				debugf("Output CSV stat error: %v\n", statErr)
			}

			jobs, err = config.LoadJobsCSV(outputPath)
			if err != nil {
				debugf("LoadJobsCSV failed: %v\n", err)
			} else {
				debugf("LoadJobsCSV succeeded, loaded %d jobs\n", len(jobs))
			}
		}

		fyne.Do(func() {
			// Progress dialog hidden automatically by defer

			if err != nil {
				dialog.ShowError(fmt.Errorf("Scan failed: %w", err), jt.window)
				jt.workflow.SetError(fmt.Sprintf("Scan failed: %v", err))
				jt.updateView()
				return
			}

			// Validate generated jobs
			if err := jt.validateAllJobs(jobs); err != nil {
				dialog.ShowError(err, jt.window)
				jt.workflow.SetError("Validation failed")
				jt.updateView()
				return
			}

			// Set scanned jobs in workflow
			if err := jt.workflow.SetScannedJobs(jobs, outputPath); err != nil {
				dialog.ShowError(err, jt.window)
				return
			}

			// Transition to validated state
			if err := jt.workflow.TransitionTo(StateJobsValidated); err != nil {
				dialog.ShowError(err, jt.window)
				return
			}

			// Load jobs into table
			jt.loadJobsFromSpecs(jobs)

			// Show success
			dialog.ShowInformation("Success",
				fmt.Sprintf("Generated %d jobs successfully!", len(jobs)),
				jt.window)

			jt.updateView()
		})
	}()
}

// saveTemplateToTempFile saves the current template to a temporary CSV file
func (jt *JobsTab) saveTemplateToTempFile() (string, error) {
	if jt.workflow.Template == nil {
		return "", fmt.Errorf("no template available")
	}

	// Use the config package to save the template
	tempPath := filepath.Join(os.TempDir(), fmt.Sprintf("pur_template_%d.csv", time.Now().Unix()))

	// Create a slice with just the template
	templates := []models.JobSpec{*jt.workflow.Template}

	// Save using the SaveJobsCSV function
	if err := config.SaveJobsCSV(tempPath, templates); err != nil {
		return "", fmt.Errorf("failed to save template CSV: %w", err)
	}

	return tempPath, nil
}

// startExecution begins job execution
func (jt *JobsTab) startExecution(submitJobs bool) {
	// Validate we have jobs
	if len(jt.loadedJobs) == 0 {
		dialog.ShowError(fmt.Errorf("No jobs loaded"), jt.window)
		return
	}

	// Save current state before transitioning to executing
	// (so we can return to it if there's an error)
	preExecutionState := jt.workflow.CurrentState

	// Transition to executing state
	if err := jt.workflow.TransitionTo(StateExecuting); err != nil {
		dialog.ShowError(err, jt.window)
		return
	}

	// Store the pre-execution state so errors can return to it
	jt.workflow.PreviousState = preExecutionState

	jt.updateView()

	// Create context for cancellation
	jt.ctx, jt.cancel = context.WithCancel(context.Background())

	// Update UI state
	jt.isRunning = true

	// Start job status monitoring (10 second interval)
	jt.engine.StartJobMonitoring(10 * time.Second)

	// Start pipeline in background
	go func() {
		defer func() {
			if r := recover(); r != nil {
				guiLogger.Error().Msgf("PANIC in pipeline goroutine: %v\n", r)
				jt.engine.StopJobMonitoring()
				fyne.Do(func() {
					jt.isRunning = false
					jt.workflow.SetError(fmt.Sprintf("Pipeline panic: %v", r))
					dialog.ShowError(
						fmt.Errorf("An unexpected error occurred during pipeline execution: %v\n\nPlease check the console for details.", r),
						jt.window,
					)
					jt.updateView()
				})
			}
		}()

		debugf("Pipeline starting, CSV=%s, StateFile=%s\n", jt.workflow.SelectedCSV, jt.workflow.StateFile)
		err := jt.engine.Run(jt.ctx, jt.workflow.SelectedCSV, jt.workflow.StateFile)
		debugf("Pipeline completed with err=%v\n", err)

		// Stop monitoring
		jt.engine.StopJobMonitoring()

		// Update UI on completion
		fyne.Do(func() {
			debugf("Setting isRunning=false\n")
			jt.isRunning = false

			if err != nil {
				if err == context.Canceled {
					// User stopped it
					debugf("Pipeline was canceled by user\n")
					jt.workflow.SetError("Pipeline stopped by user")
					dialog.ShowInformation("Stopped",
						"Pipeline has been stopped. State has been saved.",
						jt.window)
				} else {
					// Error
					debugf("Pipeline error: %v\n", err)
					debugf("About to call SetError, current PreviousState will be: %s\n", jt.workflow.CurrentState)
					jt.workflow.SetError(fmt.Sprintf("Pipeline error: %v", err))
					debugf("About to show error dialog\n")
					dialog.ShowError(err, jt.window)
					debugf("Error dialog shown (or queued)\n")
				}
			} else {
				// Pipeline finished - reload job states from state file to get updated statuses
				debugf("Pipeline completed, reloading job states from state file\n")

				// Load state from the state file to get actual job statuses
				stateMgr := state.NewManager(jt.workflow.StateFile)
				if err := stateMgr.Load(); err != nil {
					debugf("Failed to load state file: %v\n", err)
				} else {
					// Get all states and create a map by job name for quick lookup
					allStates := stateMgr.GetAllStates()
					stateMap := make(map[string]*models.JobState)
					for _, jobState := range allStates {
						stateMap[jobState.JobName] = jobState
					}

					// Update loadedJobs with status from state file
					jt.jobsLock.Lock()
					for i := range jt.loadedJobs {
						if jobState, exists := stateMap[jt.loadedJobs[i].JobName]; exists {
							jt.loadedJobs[i].Status = jobState.SubmitStatus
							jt.loadedJobs[i].Error = jobState.ErrorMessage
							jt.loadedJobs[i].JobID = jobState.JobID
							debugf("Updated job %d (%s): status=%q error=%q jobID=%q\n",
								i+1, jt.loadedJobs[i].JobName, jobState.SubmitStatus, jobState.ErrorMessage, jobState.JobID)
						}
					}
					jt.jobsLock.Unlock()

					// Refresh the table to show updated statuses
					jt.table.Refresh()
				}

				// Count failed jobs based on updated statuses
				jt.jobsLock.RLock()
				failedCount := 0
				successCount := 0
				for i, job := range jt.loadedJobs {
					debugf("Job %d final status=%q error=%q\n", i+1, job.Status, job.Error)
					if job.Error != "" || job.Status == "failed" {
						failedCount++
					} else if job.Status == "success" || job.Status == "completed" || job.Status == "submitted" {
						successCount++
					}
				}
				totalJobs := len(jt.loadedJobs)
				jt.jobsLock.RUnlock()

				debugf("Job results: %d succeeded, %d failed out of %d total\n",
					successCount, failedCount, totalJobs)

				if failedCount > 0 {
					// Some or all jobs failed
					jt.workflow.SetError(fmt.Sprintf("%d of %d jobs failed", failedCount, totalJobs))
					dialog.ShowError(
						fmt.Errorf("Pipeline completed but %d of %d jobs failed.\n\nCheck the jobs table below for details about what went wrong.", failedCount, totalJobs),
						jt.window,
					)
				} else if successCount == 0 && totalJobs > 0 {
					// No jobs completed successfully
					jt.workflow.SetError("Pipeline completed but no jobs finished successfully")
					dialog.ShowError(
						fmt.Errorf("Pipeline completed but no jobs finished successfully.\n\nCheck the jobs table and logs for details."),
						jt.window,
					)
				} else {
					// True success
					debugf("All jobs completed successfully\n")
					if err := jt.workflow.TransitionTo(StateCompleted); err == nil {
						dialog.ShowInformation("Complete",
							fmt.Sprintf("Pipeline completed successfully!\n\n%d jobs processed.", successCount),
							jt.window)
					}
				}
			}

			debugf("Updating view after pipeline completion\n")
			jt.updateView()
		})
	}()
}

// stopExecution stops the current execution
func (jt *JobsTab) stopExecution() {
	debugf("stopExecution called, isRunning=%v, cancel=%v\n", jt.isRunning, jt.cancel != nil)

	if !jt.isRunning {
		debugf("Pipeline not running, nothing to stop\n")
		dialog.ShowInformation("Not Running", "Pipeline is not currently running.", jt.window)
		return
	}

	if jt.cancel != nil {
		debugf("Calling cancel()\n")
		jt.cancel()
	}

	debugf("Calling engine.Stop()\n")
	jt.engine.Stop()

	// State will be updated in the completion handler
	debugf("stopExecution complete\n")
}

// loadJobsFromSpecs converts JobSpecs to JobRows for display
func (jt *JobsTab) loadJobsFromSpecs(specs []models.JobSpec) {
	jt.jobsLock.Lock()
	jt.loadedJobs = make([]JobRow, len(specs))
	for i, spec := range specs {
		jt.loadedJobs[i] = JobRow{
			Index:        i,
			Directory:    spec.Directory,
			JobName:      spec.JobName,
			TarStatus:    "pending",
			UploadStatus: "pending",
			CreateStatus: "pending",
			SubmitStatus: "pending",
			Status:       "Ready",
			JobID:        "",
			Progress:     0,
			Error:        "",
		}
	}
	jt.jobsLock.Unlock()

	// Refresh table AFTER releasing lock to avoid deadlock
	// (table.Refresh calls Update which tries to acquire RLock)
	if jt.table != nil {
		debugf("Refreshing table with %d jobs\n", len(specs))
		jt.table.Refresh()
	} else {
		debugf("WARNING: table is nil, cannot refresh!\n")
	}

	jt.updateStats()
}
