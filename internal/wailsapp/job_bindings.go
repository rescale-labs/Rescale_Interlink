// Package wailsapp provides job-related Wails bindings.
package wailsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/filescan"
	"github.com/rescale/rescale-int/internal/pur/parser"
)

// emitScanProgress publishes a scan progress event for software/hardware catalog scanning.
// v4.0.8: Added to provide feedback during potentially long API scans.
func (a *App) emitScanProgress(scanType string, page int, itemsFound int, isComplete bool, isCached bool, errMsg string) {
	if a.engine == nil || a.engine.Events() == nil {
		return
	}
	a.engine.Events().Publish(&events.ScanProgressEvent{
		BaseEvent: events.BaseEvent{
			EventType: events.EventScanProgress,
			Time:      time.Now(),
		},
		ScanType:   scanType,
		Page:       page,
		ItemsFound: itemsFound,
		IsComplete: isComplete,
		IsCached:   isCached,
		Error:      errMsg,
	})
}

// JobSpecDTO is the JSON-safe version of models.JobSpec.
type JobSpecDTO struct {
	Directory             string   `json:"directory"`
	JobName               string   `json:"jobName"`
	AnalysisCode          string   `json:"analysisCode"`
	AnalysisVersion       string   `json:"analysisVersion"`
	Command               string   `json:"command"`
	CoreType              string   `json:"coreType"`
	CoresPerSlot          int      `json:"coresPerSlot"`
	WalltimeHours         float64  `json:"walltimeHours"`
	Slots                 int      `json:"slots"`
	LicenseSettings       string   `json:"licenseSettings"`
	ExtraInputFileIDs     string   `json:"extraInputFileIds"`
	NoDecompress          bool     `json:"noDecompress"`
	SubmitMode            string   `json:"submitMode"`
	IsLowPriority         bool     `json:"isLowPriority"`
	OnDemandLicenseSeller string   `json:"onDemandLicenseSeller"`
	Tags                  []string `json:"tags"`
	ProjectID             string   `json:"projectId"`
	Automations           []string `json:"automations"`

	// v4.0.8: File-based job inputs (for file scanning mode)
	// When InputFiles is non-empty, these files are uploaded instead of tarring Directory
	InputFiles []string `json:"inputFiles,omitempty"`
}

// SecondaryPatternDTO represents a secondary file pattern for file-based scanning.
// v4.0.8: Added for PUR file scanning mode.
type SecondaryPatternDTO struct {
	Pattern  string `json:"pattern"`  // Glob pattern, may include subpath (e.g., "*.mesh", "../meshes/*.cfg")
	Required bool   `json:"required"` // If true, skip job when file missing; if false, warn and continue
}

// ScanOptionsDTO is the JSON-safe version of core.ScanOptions.
type ScanOptionsDTO struct {
	RootDir           string `json:"rootDir"`
	Pattern           string `json:"pattern"`
	ValidationPattern string `json:"validationPattern"`
	RunSubpath        string `json:"runSubpath"`
	Recursive         bool   `json:"recursive"`
	IncludeHidden     bool   `json:"includeHidden"`

	// v4.0.8: File scanning mode fields
	ScanMode          string                `json:"scanMode"`          // "folders" (default) or "files"
	PrimaryPattern    string                `json:"primaryPattern"`    // For file mode: e.g., "*.inp", "inputs/*.inp"
	SecondaryPatterns []SecondaryPatternDTO `json:"secondaryPatterns"` // For file mode: secondary files to attach
}

// ScanResultDTO is the result of a directory scan.
type ScanResultDTO struct {
	Jobs        []JobSpecDTO `json:"jobs"`
	TotalCount  int          `json:"totalCount"`
	MatchCount  int          `json:"matchCount"`
	InvalidDirs []string     `json:"invalidDirs"`
	Error       string       `json:"error,omitempty"`

	// v4.0.8: File scanning mode results
	SkippedFiles []string `json:"skippedFiles,omitempty"` // Primary files skipped due to missing required secondaries
	Warnings     []string `json:"warnings,omitempty"`     // Warnings for missing optional secondaries
}

// CoreTypeDTO represents a hardware core type.
type CoreTypeDTO struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	DisplayOrder int    `json:"displayOrder"`
	IsActive     bool   `json:"isActive"`
	Cores        []int  `json:"cores"`
}

// AnalysisCodeDTO represents a software analysis code.
type AnalysisCodeDTO struct {
	Code        string              `json:"code"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	VendorName  string              `json:"vendorName"`
	Versions    []AnalysisVersionDTO `json:"versions"`
}

// AnalysisVersionDTO represents a version of an analysis code.
type AnalysisVersionDTO struct {
	ID               string   `json:"id"`
	Version          string   `json:"version"`
	VersionCode      string   `json:"versionCode"`
	AllowedCoreTypes []string `json:"allowedCoreTypes"`
}

// AutomationDTO represents a Rescale automation.
type AutomationDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ExecuteOn   string `json:"executeOn"`
	ScriptName  string `json:"scriptName"`
}

// v4.0.6: Result DTOs with error propagation to fix silent API failures

// CoreTypesResultDTO wraps core type results with optional error.
type CoreTypesResultDTO struct {
	CoreTypes []CoreTypeDTO `json:"coreTypes"`
	Error     string        `json:"error,omitempty"`
}

// AnalysisCodesResultDTO wraps analysis code results with optional error.
type AnalysisCodesResultDTO struct {
	Codes []AnalysisCodeDTO `json:"codes"`
	Error string            `json:"error,omitempty"`
}

// AutomationsResultDTO wraps automation results with optional error.
type AutomationsResultDTO struct {
	Automations []AutomationDTO `json:"automations"`
	Error       string          `json:"error,omitempty"`
}

// RunStatusDTO represents the status of a pipeline run.
type RunStatusDTO struct {
	State       string `json:"state"` // "idle", "running", "completed", "failed", "cancelled"
	TotalJobs   int    `json:"totalJobs"`
	SuccessJobs int    `json:"successJobs"`
	FailedJobs  int    `json:"failedJobs"`
	DurationMs  int64  `json:"durationMs"`
	Error       string `json:"error,omitempty"`
}

// JobRowDTO represents a job row for the jobs table.
type JobRowDTO struct {
	Index          int     `json:"index"`
	Directory      string  `json:"directory"`
	JobName        string  `json:"jobName"`
	TarStatus      string  `json:"tarStatus"`
	UploadStatus   string  `json:"uploadStatus"`
	UploadProgress float64 `json:"uploadProgress"`
	CreateStatus   string  `json:"createStatus"`
	SubmitStatus   string  `json:"submitStatus"`
	Status         string  `json:"status"`
	JobID          string  `json:"jobId"`
	Progress       float64 `json:"progress"`
	Error          string  `json:"error"`
}

// SingleJobInputDTO represents input for single job submission.
type SingleJobInputDTO struct {
	Job           JobSpecDTO `json:"job"`
	InputMode     string     `json:"inputMode"` // "directory", "localFiles", "remoteFiles"
	Directory     string     `json:"directory,omitempty"`
	LocalFiles    []string   `json:"localFiles,omitempty"`
	RemoteFileIDs []string   `json:"remoteFileIds,omitempty"`
}

// v4.0.0: Removed runState package variable. Run state is now managed by Engine.
// See core.RunContext for run metadata and state.Manager for job state.

// ScanDirectory scans a directory for matching subdirectories or files.
// v4.0.8: Added file scanning mode with secondary file support.
func (a *App) ScanDirectory(opts ScanOptionsDTO, template JobSpecDTO) ScanResultDTO {
	if a.engine == nil {
		return ScanResultDTO{Error: ErrNoEngine.Error()}
	}

	// Validate root directory exists
	if opts.RootDir == "" {
		return ScanResultDTO{Error: "root directory is required"}
	}

	if _, err := os.Stat(opts.RootDir); os.IsNotExist(err) {
		return ScanResultDTO{Error: fmt.Sprintf("directory does not exist: %s", opts.RootDir)}
	}

	// v4.0.8: Handle file scanning mode
	if opts.ScanMode == "files" {
		return a.scanFilesMode(opts, template)
	}

	// Default: folder scanning mode
	// v4.5.9: Pass RootDir as PartDirs[0] so ScanToSpecs uses the GUI-selected
	// directory instead of falling back to os.Getwd() (the app install directory).
	scanOpts := core.ScanOptions{
		Pattern:           opts.Pattern,
		ValidationPattern: opts.ValidationPattern,
		RunSubpath:        opts.RunSubpath,
		Recursive:         opts.Recursive,
		IncludeHidden:     opts.IncludeHidden,
		PartDirs:          []string{opts.RootDir},
		StartIndex:        1, // Prevent job names starting at _0
	}

	templateSpec := dtoToJobSpec(template)

	// Perform the scan
	jobs, err := a.engine.ScanToSpecs(templateSpec, scanOpts)
	if err != nil {
		return ScanResultDTO{Error: err.Error()}
	}

	// v4.5.9: Return actionable error when no directories match in folder mode.
	// File mode has its own SkippedFiles/Warnings semantics and returns earlier.
	if len(jobs) == 0 {
		return ScanResultDTO{
			Error: fmt.Sprintf("No directories matching pattern '%s' found in %s", opts.Pattern, opts.RootDir),
		}
	}

	// Convert results to DTOs
	jobDTOs := make([]JobSpecDTO, len(jobs))
	for i, job := range jobs {
		jobDTOs[i] = jobSpecToDTO(job)
	}

	return ScanResultDTO{
		Jobs:       jobDTOs,
		TotalCount: len(jobDTOs),
		MatchCount: len(jobDTOs),
	}
}

// scanFilesMode handles file-based scanning for PUR.
// v4.0.8: Now uses unified filescan package shared with CLI.
func (a *App) scanFilesMode(opts ScanOptionsDTO, template JobSpecDTO) ScanResultDTO {
	// Convert DTO patterns to filescan patterns
	patterns := make([]filescan.SecondaryPattern, len(opts.SecondaryPatterns))
	for i, p := range opts.SecondaryPatterns {
		patterns[i] = filescan.SecondaryPattern{
			Pattern:  p.Pattern,
			Required: p.Required,
		}
	}

	// Use shared filescan package
	result := filescan.ScanFiles(filescan.ScanOptions{
		RootDir:           opts.RootDir,
		PrimaryPattern:    opts.PrimaryPattern,
		SecondaryPatterns: patterns,
	})

	if result.Error != "" {
		return ScanResultDTO{Error: result.Error}
	}

	// Convert filescan results to JobSpecDTO
	var jobs []JobSpecDTO
	for i, jobFiles := range result.Jobs {
		job := template
		job.InputFiles = jobFiles.InputFiles
		job.Directory = jobFiles.PrimaryDir

		// Generate job name
		if template.JobName != "" {
			job.JobName = fmt.Sprintf("%s_%d", template.JobName, i+1)
		} else {
			job.JobName = fmt.Sprintf("Job_%d", i+1)
		}

		jobs = append(jobs, job)
	}

	return ScanResultDTO{
		Jobs:         jobs,
		TotalCount:   result.TotalCount,
		MatchCount:   result.MatchCount,
		SkippedFiles: result.SkippedFiles,
		Warnings:     result.Warnings,
	}
}

// GetCoreTypes returns available hardware core types.
// v4.0.6: Changed to return CoreTypesResultDTO with error propagation.
// v4.0.8: Added caching and scan progress events for better UX during slow scans.
func (a *App) GetCoreTypes() CoreTypesResultDTO {
	if a.engine == nil || a.engine.API() == nil {
		return CoreTypesResultDTO{Error: "engine not initialized"}
	}

	// v4.0.8: Check cache first
	a.catalogCacheMu.RLock()
	if len(a.cachedCoreTypes) > 0 {
		cached := a.cachedCoreTypes
		a.catalogCacheMu.RUnlock()
		// Emit cached completion event
		a.emitScanProgress("hardware", 0, len(cached), true, true, "")
		return CoreTypesResultDTO{CoreTypes: cached}
	}
	a.catalogCacheMu.RUnlock()

	// Emit scan start event
	a.emitScanProgress("hardware", 0, 0, false, false, "")

	ctx, cancel := context.WithTimeout(context.Background(), constants.PaginatedAPITimeout)
	defer cancel()

	coreTypes, err := a.engine.API().GetCoreTypes(ctx, true)
	if err != nil {
		errMsg := fmt.Sprintf("failed to fetch core types: %v", err)
		a.emitScanProgress("hardware", 0, 0, true, false, errMsg)
		return CoreTypesResultDTO{Error: errMsg}
	}

	dtos := make([]CoreTypeDTO, len(coreTypes))
	for i, ct := range coreTypes {
		dtos[i] = CoreTypeDTO{
			Code:         ct.Code,
			Name:         ct.Name,
			DisplayOrder: ct.DisplayOrder,
			IsActive:     ct.IsActive,
			Cores:        ct.Cores,
		}
	}

	// v4.0.8: Cache results
	a.catalogCacheMu.Lock()
	a.cachedCoreTypes = dtos
	a.catalogCacheMu.Unlock()

	// Emit scan complete event
	a.emitScanProgress("hardware", 0, len(dtos), true, false, "")

	return CoreTypesResultDTO{CoreTypes: dtos}
}

// GetAnalysisCodes returns available software analysis codes.
// v4.0.6: Changed to return AnalysisCodesResultDTO with error propagation.
// v4.0.8: Added caching and scan progress events for better UX during slow scans.
func (a *App) GetAnalysisCodes(search string) AnalysisCodesResultDTO {
	if a.engine == nil {
		return AnalysisCodesResultDTO{Error: "engine not initialized"}
	}

	// v4.0.8: Check cache first (only if no search filter)
	// When search is empty, we can use the full cached list
	a.catalogCacheMu.RLock()
	hasCached := len(a.cachedAnalyses) > 0
	cached := a.cachedAnalyses
	a.catalogCacheMu.RUnlock()

	if hasCached {
		// Filter from cache
		dtos := filterAnalysisCodes(cached, search)
		// Emit cached completion event
		a.emitScanProgress("software", 0, len(cached), true, true, "")
		return AnalysisCodesResultDTO{Codes: dtos}
	}

	// Emit scan start event
	a.emitScanProgress("software", 0, 0, false, false, "")

	ctx, cancel := context.WithTimeout(context.Background(), constants.PaginatedAPITimeout)
	defer cancel()

	analyses, err := a.engine.GetAnalyses(ctx)
	if err != nil {
		errMsg := fmt.Sprintf("failed to fetch analysis codes: %v", err)
		a.emitScanProgress("software", 0, 0, true, false, errMsg)
		return AnalysisCodesResultDTO{Error: errMsg}
	}

	// Convert all analyses to DTOs for caching
	allDtos := make([]AnalysisCodeDTO, len(analyses))
	for i, an := range analyses {
		// Convert versions
		versions := make([]AnalysisVersionDTO, len(an.Versions))
		for j, v := range an.Versions {
			versions[j] = AnalysisVersionDTO{
				ID:               v.ID,
				Version:          v.Version,
				VersionCode:      v.VersionCode,
				AllowedCoreTypes: v.AllowedCoreTypes,
			}
		}
		allDtos[i] = AnalysisCodeDTO{
			Code:        an.Code,
			Name:        an.Name,
			Description: an.Description,
			VendorName:  an.VendorName,
			Versions:    versions,
		}
	}

	// v4.0.8: Cache results
	a.catalogCacheMu.Lock()
	a.cachedAnalyses = allDtos
	a.catalogCacheMu.Unlock()

	// Emit scan complete event
	a.emitScanProgress("software", 0, len(allDtos), true, false, "")

	// Apply search filter if provided
	return AnalysisCodesResultDTO{Codes: filterAnalysisCodes(allDtos, search)}
}

// filterAnalysisCodes filters analysis codes by search string.
func filterAnalysisCodes(codes []AnalysisCodeDTO, search string) []AnalysisCodeDTO {
	if search == "" {
		return codes
	}

	searchLower := strings.ToLower(search)
	filtered := make([]AnalysisCodeDTO, 0, len(codes))

	for _, an := range codes {
		nameLower := strings.ToLower(an.Name)
		codeLower := strings.ToLower(an.Code)
		if strings.Contains(nameLower, searchLower) || strings.Contains(codeLower, searchLower) {
			filtered = append(filtered, an)
		}
	}
	return filtered
}

// GetAutomations returns available automations.
// v4.0.6: Changed to return AutomationsResultDTO with error propagation.
// v4.0.8: Increased timeout to handle paginated API calls with rate limiting.
func (a *App) GetAutomations() AutomationsResultDTO {
	if a.engine == nil || a.engine.API() == nil {
		return AutomationsResultDTO{Error: "engine not initialized"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), constants.PaginatedAPITimeout)
	defer cancel()

	automations, err := a.engine.API().ListAutomations(ctx)
	if err != nil {
		return AutomationsResultDTO{Error: fmt.Sprintf("failed to fetch automations: %v", err)}
	}

	dtos := make([]AutomationDTO, len(automations))
	for i, auto := range automations {
		dtos[i] = AutomationDTO{
			ID:          auto.ID,
			Name:        auto.Name,
			Description: auto.Description,
			ExecuteOn:   auto.ExecuteOn,
			ScriptName:  auto.ScriptName,
		}
	}
	return AutomationsResultDTO{Automations: dtos}
}

// StartBulkRun starts a bulk job run (PUR pipeline).
// v4.0.0: Refactored to use Engine's run context for state synchronization.
func (a *App) StartBulkRun(jobs []JobSpecDTO) (string, error) {
	if a.engine == nil {
		return "", ErrNoEngine
	}

	if len(jobs) == 0 {
		return "", fmt.Errorf("no jobs provided")
	}

	// Check if a run is already active (v4.0.0: use Engine)
	if a.engine.IsRunActive() {
		return "", fmt.Errorf("a run is already in progress")
	}

	// Generate run ID and state file
	runID := fmt.Sprintf("run_%d", time.Now().UnixNano())
	stateFile := generateStateFilePath(runID)

	// Convert DTOs to job specs
	jobSpecs := make([]models.JobSpec, len(jobs))
	for i, job := range jobs {
		jobSpecs[i] = dtoToJobSpec(job)
	}

	// Register run with Engine (v4.0.0)
	if err := a.engine.StartRun(runID, stateFile, len(jobs)); err != nil {
		return "", err
	}

	// Create cancellable context
	// v4.0.5: Protected by runMu to prevent race conditions
	ctx, cancel := context.WithCancel(context.Background())
	a.runMu.Lock()
	a.runCancel = cancel
	a.runMu.Unlock()

	// Start the pipeline in background
	go func() {
		defer a.engine.EndRun()

		err := a.engine.RunFromSpecs(ctx, jobSpecs, stateFile)
		if err != nil && ctx.Err() == nil {
			// Real error, not cancellation - errors are already tracked in Engine's state
			wailsLogger.Error().Err(err).Msg("Pipeline run failed")
		}
	}()

	return runID, nil
}

// StartSingleJob starts a single job submission.
// v4.0.0: Refactored to use Engine's run context for state synchronization.
// v4.0.8: Added directory existence check to fail early with clear error.
func (a *App) StartSingleJob(input SingleJobInputDTO) (string, error) {
	if a.engine == nil {
		return "", ErrNoEngine
	}

	// Validate input
	switch input.InputMode {
	case "directory":
		if input.Directory == "" {
			return "", fmt.Errorf("directory is required for directory input mode")
		}
		// v4.0.8: Verify directory exists before starting job
		if _, err := os.Stat(input.Directory); os.IsNotExist(err) {
			return "", fmt.Errorf("directory does not exist: %s", input.Directory)
		}
	case "localFiles":
		if len(input.LocalFiles) == 0 {
			return "", fmt.Errorf("at least one local file is required")
		}
	case "remoteFiles":
		if len(input.RemoteFileIDs) == 0 {
			return "", fmt.Errorf("at least one remote file ID is required")
		}
	default:
		return "", fmt.Errorf("invalid input mode: %s", input.InputMode)
	}

	// Check if a run is already active (v4.0.0: use Engine)
	if a.engine.IsRunActive() {
		return "", fmt.Errorf("a run is already in progress")
	}

	runID := fmt.Sprintf("single_%d", time.Now().UnixNano())
	stateFile := generateStateFilePath(runID)

	// Convert DTO to job spec
	jobSpec := dtoToJobSpec(input.Job)

	// Set directory for directory mode
	if input.InputMode == "directory" {
		jobSpec.Directory = input.Directory
	}

	// For localFiles and remoteFiles modes, we need to handle file upload/selection
	// This will be handled by the frontend calling StartTransfers first

	// Register run with Engine (v4.0.0)
	if err := a.engine.StartRun(runID, stateFile, 1); err != nil {
		return "", err
	}

	// Create cancellable context
	// v4.0.5: Protected by runMu to prevent race conditions
	ctx, cancel := context.WithCancel(context.Background())
	a.runMu.Lock()
	a.runCancel = cancel
	a.runMu.Unlock()

	// Start the job in background
	go func() {
		defer a.engine.EndRun()

		err := a.engine.RunFromSpecs(ctx, []models.JobSpec{jobSpec}, stateFile)
		if err != nil && ctx.Err() == nil {
			// Real error, not cancellation - errors are already tracked in Engine's state
			wailsLogger.Error().Err(err).Msg("Single job run failed")
		}
	}()

	return runID, nil
}

// CancelRun cancels the current run.
// v4.0.0: Refactored to use Engine's run context.
// v4.0.5: Protected by runMu to prevent race conditions.
func (a *App) CancelRun() error {
	if a.engine == nil {
		return ErrNoEngine
	}

	if !a.engine.IsRunActive() {
		return fmt.Errorf("no run in progress")
	}

	// Cancel the context (v4.0.5: protected by mutex)
	a.runMu.Lock()
	if a.runCancel != nil {
		a.runCancel()
		a.runCancel = nil
	}
	a.runMu.Unlock()

	// Stop the engine
	a.engine.Stop()

	return nil
}

// GetRunStatus returns the current run status.
// v4.0.0: Refactored to read from Engine's state.
func (a *App) GetRunStatus() RunStatusDTO {
	if a.engine == nil {
		return RunStatusDTO{State: "idle"}
	}

	runCtx := a.engine.GetRunContext()
	total, completed, failed, pending := a.engine.GetRunStats()

	status := RunStatusDTO{
		TotalJobs:   total,
		SuccessJobs: completed,
		FailedJobs:  failed,
	}

	if runCtx != nil {
		// Run is active
		status.State = "running"
		status.DurationMs = time.Since(runCtx.StartTime).Milliseconds()
	} else if total == 0 {
		status.State = "idle"
	} else {
		// Run finished - determine final state
		if pending == 0 {
			if failed > 0 {
				status.State = "failed"
			} else {
				status.State = "completed"
			}
		} else {
			status.State = "idle"
		}
	}

	return status
}

// GetJobRows returns the current job rows for the jobs table.
// v4.0.0: Refactored to read from Engine's state manager.
// v4.0.6: Now returns actual UploadProgress from state instead of 0.
func (a *App) GetJobRows() []JobRowDTO {
	if a.engine == nil {
		return []JobRowDTO{}
	}

	st := a.engine.GetState()
	if st == nil {
		return []JobRowDTO{}
	}

	states := st.GetAllStates()
	rows := make([]JobRowDTO, len(states))
	for i, state := range states {
		rows[i] = JobRowDTO{
			Index:          state.Index,
			Directory:      state.Directory,
			JobName:        state.JobName,
			TarStatus:      state.TarStatus,
			UploadStatus:   state.UploadStatus,
			UploadProgress: state.UploadProgress, // v4.0.6: Use actual progress from state
			CreateStatus:   "",
			SubmitStatus:   state.SubmitStatus,
			Status:         state.SubmitStatus, // Use submit status as overall status
			JobID:          state.JobID,
			Progress:       0, // Transient - provided via events
			Error:          state.ErrorMessage,
		}
	}
	return rows
}

// UpdateJobRow updates a specific job row (for editing jobs).
// v4.0.0: This function is deprecated. Edit jobs in the frontend before submission.
func (a *App) UpdateJobRow(index int, job JobSpecDTO) error {
	// v4.0.0: With Engine-based state, jobs are only tracked during execution.
	// Editing should be done in the frontend's scannedJobs before calling StartBulkRun.
	return fmt.Errorf("UpdateJobRow is deprecated: edit jobs in the frontend before submission")
}

// ResetRun clears the current run state.
// v4.0.0: Refactored to use Engine's ResetRun.
// v4.0.5: Protected by runMu to prevent race conditions.
func (a *App) ResetRun() {
	if a.engine != nil {
		a.engine.ResetRun()
	}

	// Clear local cancel function (v4.0.5: protected by mutex)
	a.runMu.Lock()
	if a.runCancel != nil {
		a.runCancel()
		a.runCancel = nil
	}
	a.runMu.Unlock()
}

// ValidateJobSpec validates a job specification.
// v4.0.8: Added Slots validation (was missing, caused runtime failures).
func (a *App) ValidateJobSpec(job JobSpecDTO) []string {
	var errors []string

	if job.JobName == "" {
		errors = append(errors, "Job name is required")
	}
	if job.AnalysisCode == "" {
		errors = append(errors, "Analysis code is required")
	}
	if job.CoreType == "" {
		errors = append(errors, "Core type is required")
	}
	if job.Command == "" {
		errors = append(errors, "Command is required")
	}
	if job.CoresPerSlot <= 0 {
		errors = append(errors, "Cores per slot must be positive")
	}
	if job.Slots <= 0 {
		errors = append(errors, "Slots must be positive")
	}
	if job.WalltimeHours <= 0 {
		errors = append(errors, "Walltime must be positive")
	}

	return errors
}

// Helper functions

// jobSpecToDTO converts models.JobSpec to DTO.
func jobSpecToDTO(j models.JobSpec) JobSpecDTO {
	return JobSpecDTO{
		Directory:             j.Directory,
		JobName:               j.JobName,
		AnalysisCode:          j.AnalysisCode,
		AnalysisVersion:       j.AnalysisVersion,
		Command:               j.Command,
		CoreType:              j.CoreType,
		CoresPerSlot:          j.CoresPerSlot,
		WalltimeHours:         j.WalltimeHours,
		Slots:                 j.Slots,
		LicenseSettings:       j.LicenseSettings,
		ExtraInputFileIDs:     j.ExtraInputFileIDs,
		NoDecompress:          j.NoDecompress,
		SubmitMode:            j.SubmitMode,
		IsLowPriority:         j.IsLowPriority,
		OnDemandLicenseSeller: j.OnDemandLicenseSeller,
		Tags:                  j.Tags,
		ProjectID:             j.ProjectID,
		Automations:           j.Automations,
		InputFiles:            j.InputFiles, // v4.0.8: File-based inputs
	}
}

// dtoToJobSpec converts DTO to models.JobSpec.
func dtoToJobSpec(j JobSpecDTO) models.JobSpec {
	return models.JobSpec{
		Directory:             j.Directory,
		JobName:               j.JobName,
		AnalysisCode:          j.AnalysisCode,
		AnalysisVersion:       j.AnalysisVersion,
		Command:               j.Command,
		CoreType:              j.CoreType,
		CoresPerSlot:          j.CoresPerSlot,
		WalltimeHours:         j.WalltimeHours,
		Slots:                 j.Slots,
		LicenseSettings:       j.LicenseSettings,
		ExtraInputFileIDs:     j.ExtraInputFileIDs,
		NoDecompress:          j.NoDecompress,
		SubmitMode:            j.SubmitMode,
		IsLowPriority:         j.IsLowPriority,
		OnDemandLicenseSeller: j.OnDemandLicenseSeller,
		Tags:                  j.Tags,
		ProjectID:             j.ProjectID,
		Automations:           j.Automations,
		InputFiles:            j.InputFiles, // v4.0.8: File-based inputs
	}
}

// generateStateFilePath creates a unique state file path.
func generateStateFilePath(runID string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	stateDir := filepath.Join(homeDir, ".rescale-int", "states")
	os.MkdirAll(stateDir, 0755)
	return filepath.Join(stateDir, fmt.Sprintf("%s.state", runID))
}

// LoadJobsFromCSV loads job specifications from a CSV file.
func (a *App) LoadJobsFromCSV(path string) ([]JobSpecDTO, error) {
	if path == "" {
		return nil, fmt.Errorf("file path is required")
	}

	jobs, err := config.LoadJobsCSV(path)
	if err != nil {
		return nil, fmt.Errorf("failed to load CSV: %w", err)
	}

	dtos := make([]JobSpecDTO, len(jobs))
	for i, job := range jobs {
		dtos[i] = jobSpecToDTO(job)
	}
	return dtos, nil
}

// SaveJobsToCSV saves job specifications to a CSV file.
func (a *App) SaveJobsToCSV(path string, jobs []JobSpecDTO) error {
	if path == "" {
		return fmt.Errorf("file path is required")
	}

	// Ensure .csv extension
	if !strings.HasSuffix(strings.ToLower(path), ".csv") {
		path += ".csv"
	}

	specs := make([]models.JobSpec, len(jobs))
	for i, job := range jobs {
		specs[i] = dtoToJobSpec(job)
	}

	return config.SaveJobsCSV(path, specs)
}

// LoadJobFromJSON loads a single job specification from a JSON file.
func (a *App) LoadJobFromJSON(path string) (JobSpecDTO, error) {
	if path == "" {
		return JobSpecDTO{}, fmt.Errorf("file path is required")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return JobSpecDTO{}, fmt.Errorf("failed to read file: %w", err)
	}

	var job models.JobSpec
	if err := json.Unmarshal(data, &job); err != nil {
		return JobSpecDTO{}, fmt.Errorf("failed to parse JSON: %w", err)
	}

	return jobSpecToDTO(job), nil
}

// SaveJobToJSON saves a single job specification to a JSON file.
func (a *App) SaveJobToJSON(path string, job JobSpecDTO) error {
	if path == "" {
		return fmt.Errorf("file path is required")
	}

	// Ensure .json extension
	if !strings.HasSuffix(strings.ToLower(path), ".json") {
		path += ".json"
	}

	spec := dtoToJobSpec(job)
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	return os.WriteFile(path, data, 0644)
}

// LoadJobsFromJSON loads job specifications from a JSON file (array format).
func (a *App) LoadJobsFromJSON(path string) ([]JobSpecDTO, error) {
	if path == "" {
		return nil, fmt.Errorf("file path is required")
	}

	jobs, err := config.LoadJobsJSON(path)
	if err != nil {
		return nil, fmt.Errorf("failed to load JSON: %w", err)
	}

	dtos := make([]JobSpecDTO, len(jobs))
	for i, job := range jobs {
		dtos[i] = jobSpecToDTO(job)
	}
	return dtos, nil
}

// LoadJobFromSGE loads a job specification from an SGE script file.
func (a *App) LoadJobFromSGE(path string) (JobSpecDTO, error) {
	if path == "" {
		return JobSpecDTO{}, fmt.Errorf("file path is required")
	}

	sgeParser := parser.NewSGEParser()
	metadata, err := sgeParser.Parse(path)
	if err != nil {
		return JobSpecDTO{}, fmt.Errorf("failed to parse SGE script: %w", err)
	}

	spec := parser.SGEMetadataToJobSpec(metadata)
	return jobSpecToDTO(spec), nil
}

// SaveJobToSGE saves a job specification as an SGE script file.
func (a *App) SaveJobToSGE(path string, job JobSpecDTO) error {
	if path == "" {
		return fmt.Errorf("file path is required")
	}

	// Ensure script extension
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".sh" && ext != ".sge" && ext != ".bash" {
		path += ".sh"
	}

	spec := dtoToJobSpec(job)
	metadata := parser.JobSpecToSGEMetadata(spec)
	script := metadata.ToSGEScript()

	return os.WriteFile(path, []byte(script), 0755)
}

// GetJobsStats calculates statistics for the current job rows.
// v4.0.0: Refactored to use Engine's GetRunStats.
func (a *App) GetJobsStats() JobsStatsDTO {
	if a.engine == nil {
		return JobsStatsDTO{}
	}

	total, completed, failed, pending := a.engine.GetRunStats()

	// Check if run is active for in-progress count
	inProgress := 0
	if a.engine.IsRunActive() && pending > 0 {
		// If we're running and there are pending jobs, at least one is in progress
		inProgress = 1
		pending--
	}

	return JobsStatsDTO{
		Total:      total,
		Completed:  completed,
		InProgress: inProgress,
		Pending:    pending,
		Failed:     failed,
	}
}

// JobsStatsDTO represents aggregate statistics for jobs.
type JobsStatsDTO struct {
	Total      int `json:"total"`
	Completed  int `json:"completed"`
	InProgress int `json:"inProgress"`
	Pending    int `json:"pending"`
	Failed     int `json:"failed"`
}

// =============================================================================
// v4.0.0 G3: Template Library Functions
// =============================================================================

// TemplateInfoDTO represents metadata about a saved template.
type TemplateInfoDTO struct {
	Name        string     `json:"name"`
	Path        string     `json:"path"`
	Description string     `json:"description"`
	Software    string     `json:"software"`
	Hardware    string     `json:"hardware"`
	ModTime     string     `json:"modTime"`
	Job         JobSpecDTO `json:"job,omitempty"` // Full job spec (for preview)
}

// getTemplatesDir returns the path to the templates directory, creating it if needed.
func getTemplatesDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	templatesDir := filepath.Join(homeDir, ".config", "rescale", "templates")
	if err := os.MkdirAll(templatesDir, 0755); err != nil {
		return "", err
	}
	return templatesDir, nil
}

// ListSavedTemplates returns a list of saved templates.
func (a *App) ListSavedTemplates() []TemplateInfoDTO {
	templatesDir, err := getTemplatesDir()
	if err != nil {
		return []TemplateInfoDTO{}
	}

	entries, err := os.ReadDir(templatesDir)
	if err != nil {
		return []TemplateInfoDTO{}
	}

	templates := []TemplateInfoDTO{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		fullPath := filepath.Join(templatesDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		// Load the template to get details
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		var job JobSpecDTO
		if err := json.Unmarshal(data, &job); err != nil {
			continue
		}

		// Extract name from filename (without .json extension)
		name := strings.TrimSuffix(entry.Name(), ".json")

		templates = append(templates, TemplateInfoDTO{
			Name:     name,
			Path:     fullPath,
			Software: job.AnalysisCode,
			Hardware: job.CoreType,
			ModTime:  info.ModTime().Format(time.RFC3339),
			Job:      job,
		})
	}

	return templates
}

// SaveTemplate saves a job spec as a named template.
func (a *App) SaveTemplate(name string, job JobSpecDTO) error {
	templatesDir, err := getTemplatesDir()
	if err != nil {
		return err
	}

	// Sanitize name for filesystem
	safeName := strings.ReplaceAll(name, "/", "_")
	safeName = strings.ReplaceAll(safeName, "\\", "_")
	if !strings.HasSuffix(safeName, ".json") {
		safeName += ".json"
	}

	fullPath := filepath.Join(templatesDir, safeName)

	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(fullPath, data, 0644)
}

// LoadTemplate loads a template by name.
func (a *App) LoadTemplate(name string) (JobSpecDTO, error) {
	templatesDir, err := getTemplatesDir()
	if err != nil {
		return JobSpecDTO{}, err
	}

	safeName := name
	if !strings.HasSuffix(safeName, ".json") {
		safeName += ".json"
	}

	fullPath := filepath.Join(templatesDir, safeName)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return JobSpecDTO{}, err
	}

	var job JobSpecDTO
	if err := json.Unmarshal(data, &job); err != nil {
		return JobSpecDTO{}, err
	}

	return job, nil
}

// DeleteTemplate deletes a template by name.
func (a *App) DeleteTemplate(name string) error {
	templatesDir, err := getTemplatesDir()
	if err != nil {
		return err
	}

	safeName := name
	if !strings.HasSuffix(safeName, ".json") {
		safeName += ".json"
	}

	fullPath := filepath.Join(templatesDir, safeName)
	return os.Remove(fullPath)
}
