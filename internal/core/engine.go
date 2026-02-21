package core

import (
	"context"
	"encoding/csv"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/localfs"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pathutil"
	"github.com/rescale/rescale-int/internal/pur/pattern"
	"github.com/rescale/rescale-int/internal/pur/pipeline"
	"github.com/rescale/rescale-int/internal/pur/state"
	"github.com/rescale/rescale-int/internal/pur/validation"
	"github.com/rescale/rescale-int/internal/services"
	"github.com/rescale/rescale-int/internal/util/multipart"
)

// RunContext tracks metadata about an active pipeline run.
// v4.0.0: Added for GUI job state synchronization.
type RunContext struct {
	RunID     string    // Unique identifier for this run
	StartTime time.Time // When the run started
	StateFile string    // Path to state CSV file for persistence
	TotalJobs int       // Number of jobs in this run
}

// Engine is the main orchestrator for PUR operations with GUI support
type Engine struct {
	config    *config.Config
	state     *state.Manager
	eventBus  *events.EventBus
	apiClient *api.Client
	pipeline  *pipeline.Pipeline
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.RWMutex

	// v4.0.0: Run context for GUI state synchronization
	runCtx   *RunContext
	runCtxMu sync.RWMutex

	// v3.6.4: Service layer for frontend-agnostic business logic
	transferService *services.TransferService
	fileService     *services.FileService

	// Job monitoring
	monitorTicker *time.Ticker
	monitorStop   chan struct{}
	monitorWg     sync.WaitGroup // v3.4.0: WaitGroup to ensure goroutine exits before restart

	// Event publishing control (to prevent deadlocks)
	publishEvents bool
	eventMu       sync.RWMutex
}

// NewEngine creates a new engine instance
func NewEngine(cfg *config.Config) (*Engine, error) {
	if cfg == nil {
		// LoadConfigCSV with empty string returns default config
		var err error
		cfg, err = config.LoadConfigCSV("")
		if err != nil {
			return nil, fmt.Errorf("failed to create default config: %w", err)
		}
	}

	// Create API client
	apiClient, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}

	// Create event bus
	eventBus := events.NewEventBus(10000) // Increased buffer for rapid events

	// v3.6.4: Create service layer
	transferService := services.NewTransferService(apiClient, eventBus, services.TransferServiceConfig{})
	fileService := services.NewFileService(apiClient, eventBus)

	return &Engine{
		config:          cfg,
		eventBus:        eventBus,
		apiClient:       apiClient,
		transferService: transferService,
		fileService:     fileService,
		monitorStop:     make(chan struct{}),
		publishEvents:   true, // Enable by default
	}, nil
}

// GetConfig returns a copy of the current configuration
func (e *Engine) GetConfig() *config.Config {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.config
}

// UpdateConfig updates the engine configuration
// v3.4.3: Fixed mutex contention - create API client BEFORE acquiring lock
// Previously, the lock was held during api.NewClient() which can take 15+ seconds
// for proxy warmup. This caused GUI freezes when other code tried to call
// GetConfig() or API() while UpdateConfig was in progress.
// v3.6.3: Now publishes EventConfigChanged to notify subscribers (e.g., File Browser)
func (e *Engine) UpdateConfig(cfg *config.Config) error {
	// Create new API client FIRST (this can be slow due to proxy warmup)
	// Do NOT hold the mutex during this operation
	apiClient, err := api.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("failed to create API client: %w", err)
	}

	// Now briefly acquire lock to swap the config and client
	e.mu.Lock()
	e.config = cfg
	e.apiClient = apiClient
	// v3.6.4: Update services with new API client
	if e.transferService != nil {
		e.transferService.SetAPIClient(apiClient)
	}
	if e.fileService != nil {
		e.fileService.SetAPIClient(apiClient)
	}
	e.mu.Unlock()

	e.publishLog(events.InfoLevel, "Configuration updated", "", "")

	// v3.6.3: Publish config changed event so File Browser can refresh
	// Try to get user email for the event (non-blocking, best effort)
	var email string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if profile, err := apiClient.GetUserProfile(ctx); err == nil {
		email = profile.Email
	}

	e.eventBus.Publish(&events.ConfigChangedEvent{
		BaseEvent: events.BaseEvent{
			EventType: events.EventConfigChanged,
			Time:      time.Now(),
		},
		Source: "config_update",
		Email:  email,
	})

	return nil
}

// Events returns the event bus for subscriptions
func (e *Engine) Events() *events.EventBus {
	return e.eventBus
}

// API returns the API client for direct API calls
// This is used by the GUI file browser and other components that need direct API access
func (e *Engine) API() *api.Client {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.apiClient
}

// TransferService returns the transfer service for upload/download operations.
// v3.6.4: Added as part of service layer refactoring.
func (e *Engine) TransferService() *services.TransferService {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.transferService
}

// FileService returns the file service for file/folder operations.
// v3.6.4: Added as part of service layer refactoring.
func (e *Engine) FileService() *services.FileService {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.fileService
}

// TestConnection verifies API connectivity
// This is a lightweight test that only checks authentication - no additional data fetching
func (e *Engine) TestConnection() error {
	// Check if API key is configured
	e.mu.RLock()
	hasAPIKey := e.config.APIKey != ""
	e.mu.RUnlock()

	if !hasAPIKey {
		return fmt.Errorf("API key not configured - please enter your API key in the Setup tab")
	}

	e.publishLog(events.InfoLevel, "Testing API connection...", "", "")

	// Use shorter timeout for connection test (10 seconds should be plenty)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Test user profile endpoint - this verifies authentication
	profile, err := e.apiClient.GetUserProfile(ctx)
	if err != nil {
		e.publishLog(events.ErrorLevel, fmt.Sprintf("Connection failed: %v", err), "", "")
		return fmt.Errorf("failed to connect to Rescale API: %w", err)
	}

	e.publishLog(events.InfoLevel, fmt.Sprintf("Connected as: %s", profile.Email), "", "")
	return nil
}

// GetAnalyses retrieves all available software analyses from Rescale API
func (e *Engine) GetAnalyses(ctx context.Context) ([]models.Analysis, error) {
	if e.apiClient == nil {
		return nil, fmt.Errorf("API client not initialized")
	}
	return e.apiClient.GetAnalyses(ctx)
}

// LoadConfig loads configuration from a CSV file
func (e *Engine) LoadConfig(path string) error {
	cfg, err := config.LoadConfigCSV(path)
	if err != nil {
		return err
	}

	return e.UpdateConfig(cfg)
}

// SaveConfig saves configuration to a CSV file
func (e *Engine) SaveConfig(path string) error {
	e.mu.RLock()
	cfg := e.config
	e.mu.RUnlock()

	if err := config.SaveConfigCSV(cfg, path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	e.publishLog(events.InfoLevel, fmt.Sprintf("Configuration saved to %s", path), "config", "")
	return nil
}

// ScanOptions contains options for directory scanning
type ScanOptions struct {
	TemplateCSV       string
	OutputCSV         string
	Pattern           string
	Recursive         bool
	IncludeHidden     bool
	StartIndex        int
	IteratePatterns   bool
	ValidationPattern string
	Overwrite         bool
	RunSubpath        string   // Subpath to navigate before finding runs (e.g., "Simcodes/Powerflow")
	MultiPartMode     bool     // Enable multi-part mode (scan multiple project directories)
	PartDirs          []string // Project directories for multi-part mode
	TarSubpath        string   // v4.6.0: Subdirectory within each Run_* to tar (optional)
}

// Scan generates a jobs CSV from directory scan
func (e *Engine) Scan(opts ScanOptions) error {
	e.publishLog(events.InfoLevel, "Starting directory scan...", "scan", "")

	// Load template CSV
	jobs, err := config.LoadJobsCSV(opts.TemplateCSV)
	if err != nil {
		e.publishLog(events.ErrorLevel, fmt.Sprintf("Failed to load template: %v", err), "scan", "")
		return fmt.Errorf("failed to load template: %w", err)
	}

	if len(jobs) == 0 {
		return fmt.Errorf("template CSV is empty")
	}

	template := jobs[0]
	e.publishLog(events.InfoLevel, fmt.Sprintf("Using template: %s", template.JobName), "scan", "")

	// v4.7.1: Use validation pattern from scan options only (no config fallback)
	validationPattern := opts.ValidationPattern

	// Structure for directory entries
	type dirEntry struct {
		path        string
		projectName string // For multi-part mode
	}
	var dirEntries []dirEntry

	// Multi-part mode or normal mode
	if opts.MultiPartMode {
		// Multi-part mode: scan multiple project directories
		e.publishLog(events.InfoLevel, fmt.Sprintf("Multi-part mode enabled with %d project directories", len(opts.PartDirs)), "scan", "")
		e.publishLog(events.InfoLevel, fmt.Sprintf("Subdirectory pattern: '%s'", opts.Pattern), "scan", "")
		if opts.RunSubpath != "" {
			e.publishLog(events.InfoLevel, fmt.Sprintf("Run subpath: '%s'", opts.RunSubpath), "scan", "")
		}
		if validationPattern != "" {
			e.publishLog(events.InfoLevel, fmt.Sprintf("Validation pattern: '%s'", validationPattern), "scan", "")
		}

		// Collect all run directories from all projects
		allRuns, err := multipart.CollectAllRunDirectories(opts.PartDirs, opts.RunSubpath, opts.Pattern)
		if err != nil {
			e.publishLog(events.ErrorLevel, fmt.Sprintf("Failed to collect run directories: %v", err), "scan", "")
			return err
		}

		if len(allRuns) == 0 {
			return fmt.Errorf("no run directories found in any project (pattern='%s')", opts.Pattern)
		}

		e.publishLog(events.InfoLevel, fmt.Sprintf("Found %d total run directories across all projects", len(allRuns)), "scan", "")

		// Validate each run directory
		validRuns := 0
		for _, run := range allRuns {
			if validationPattern == "" || multipart.ValidateRunDirectory(run.RunPath, validationPattern) {
				dirEntries = append(dirEntries, dirEntry{
					path:        run.RunPath,
					projectName: run.ProjectName,
				})
				validRuns++
				e.publishLog(events.InfoLevel, fmt.Sprintf("✓ Valid: %s from %s", run.RunName, run.ProjectName), "scan", "")
			} else {
				e.publishLog(events.DebugLevel, fmt.Sprintf("✗ Skipped: %s from %s (no %s file)", run.RunName, run.ProjectName, validationPattern), "scan", "")
			}
		}

		if len(dirEntries) == 0 {
			return fmt.Errorf("no valid run directories found (validation: %s)", validationPattern)
		}

		e.publishLog(events.InfoLevel, fmt.Sprintf("Total valid runs after validation: %d/%d", validRuns, len(allRuns)), "scan", "")
	} else {
		// Normal mode - scan current directory
		cwd, _ := os.Getwd()

		// Navigate through run_subpath if specified
		scanRoot := cwd
		if opts.RunSubpath != "" {
			scanRoot = filepath.Join(cwd, opts.RunSubpath)
			if _, err := os.Stat(scanRoot); os.IsNotExist(err) {
				return fmt.Errorf("subpath '%s' not found under %s", opts.RunSubpath, cwd)
			}
			e.publishLog(events.InfoLevel, fmt.Sprintf("Scanning run directories under subpath: %s", opts.RunSubpath), "scan", "")
		}

		e.publishLog(events.InfoLevel, fmt.Sprintf("Scanning directory: %s (pattern: %s)", scanRoot, opts.Pattern), "scan", "")

		var dirs []string
		if opts.Recursive {
			// Recursive mode - walk directory tree.
			// Uses WalkDir instead of Walk to avoid per-entry os.Stat syscalls.
			err := filepath.WalkDir(scanRoot, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					// Skip hidden directories unless specified
					if !opts.IncludeHidden && localfs.IsHidden(path) && path != scanRoot {
						return filepath.SkipDir
					}
					// Check if this directory matches the pattern
					matched, matchErr := filepath.Match(opts.Pattern, filepath.Base(path))
					if matchErr == nil && matched && path != scanRoot {
						dirs = append(dirs, path)
						// SkipDir: intentionally stop descending into matched directories.
						// Run directories (e.g., Run_*) are expected to be siblings, not nested.
						// This avoids walking the full contents of each matched directory,
						// which is the primary source of slow recursive scans.
						return filepath.SkipDir
					}
				}
				return nil
			})
			if err != nil {
				e.publishLog(events.ErrorLevel, fmt.Sprintf("Failed to walk directory: %v", err), "scan", "")
				return fmt.Errorf("failed to walk directory tree: %w", err)
			}
		} else {
			// Non-recursive mode - single level glob
			matches, err := filepath.Glob(filepath.Join(scanRoot, opts.Pattern))
			if err != nil {
				e.publishLog(events.ErrorLevel, fmt.Sprintf("Failed to glob: %v", err), "scan", "")
				return err
			}
			// Filter to directories only
			for _, match := range matches {
				info, err := os.Stat(match)
				if err == nil && info.IsDir() {
					if !opts.IncludeHidden && localfs.IsHidden(match) {
						continue
					}
					dirs = append(dirs, match)
				}
			}
		}

		e.publishLog(events.InfoLevel, fmt.Sprintf("Found %d directories matching pattern", len(dirs)), "scan", "")

		// Validate directories if validation pattern specified
		var validDirs []string
		if validationPattern != "" {
			e.publishLog(events.InfoLevel, fmt.Sprintf("Validating directories (pattern: %s)", validationPattern), "scan", "")
			for _, dir := range dirs {
				if multipart.ValidateRunDirectory(dir, validationPattern) {
					validDirs = append(validDirs, dir)
				} else {
					e.publishLog(events.DebugLevel, fmt.Sprintf("Skipped %s (no matching file)", filepath.Base(dir)), "scan", "")
				}
			}
			if len(validDirs) == 0 {
				return fmt.Errorf("no directories found matching validation pattern '%s'", validationPattern)
			}
			e.publishLog(events.InfoLevel, fmt.Sprintf("Valid directories after validation: %d/%d", len(validDirs), len(dirs)), "scan", "")
		} else {
			validDirs = dirs
		}

		// Sort directories
		sort.Strings(validDirs)

		// Convert to dirEntries (no project name in normal mode)
		for _, dir := range validDirs {
			dirEntries = append(dirEntries, dirEntry{
				path:        dir,
				projectName: "",
			})
		}
	}

	// Extract template job index for pattern iteration
	templateIdx := pattern.ExtractIndexFromJobName(template.JobName)
	baseJobName := strings.TrimSuffix(template.JobName, "_1")

	// Sort dirEntries by path
	sort.Slice(dirEntries, func(i, j int) bool {
		return dirEntries[i].path < dirEntries[j].path
	})

	// Generate CSV rows
	var rows [][]string
	for i, entry := range dirEntries {
		dirNum := i + opts.StartIndex

		// Extract directory number if using default start index
		if opts.StartIndex == 1 {
			baseName := filepath.Base(entry.path)
			if match := regexp.MustCompile(`\d+`).FindString(baseName); match != "" {
				if num, err := strconv.Atoi(match); err == nil {
					dirNum = num
				}
			}
		}

		// Create job name with project suffix for multi-part mode
		var jobName string
		if opts.MultiPartMode && entry.projectName != "" {
			// Multi-part mode: append project name to make unique
			jobName = fmt.Sprintf("%s_%d_%s", baseJobName, dirNum, entry.projectName)
		} else {
			// Normal mode: just use base name and number
			jobName = fmt.Sprintf("%s_%d", baseJobName, dirNum)
		}

		// Iterate command patterns if requested
		command := template.Command
		if opts.IteratePatterns {
			command = pattern.IterateCommandPatterns(command, templateIdx, dirNum)
		}

		// Normalize directory path to absolute
		dirPath := entry.path
		if absPath, err := pathutil.ResolveAbsolutePath(entry.path); err == nil {
			dirPath = absPath
		}

		// Build row
		row := []string{
			dirPath,
			jobName,
			template.AnalysisCode,
			template.AnalysisVersion,
			command,
			template.CoreType,
			strconv.Itoa(template.CoresPerSlot),
			fmt.Sprintf("%.1f", template.WalltimeHours),
			strconv.Itoa(template.Slots),
			template.LicenseSettings,
			template.ExtraInputFileIDs,
			template.SubmitMode,
		}
		rows = append(rows, row)
	}

	e.publishLog(events.InfoLevel, fmt.Sprintf("Generated %d job entries", len(rows)), "scan", "")

	// Check if output file exists
	if !opts.Overwrite {
		if _, err := os.Stat(opts.OutputCSV); err == nil {
			return fmt.Errorf("output file '%s' already exists (use overwrite option to replace)", opts.OutputCSV)
		}
	}

	// Write CSV
	file, err := os.Create(opts.OutputCSV)
	if err != nil {
		e.publishLog(events.ErrorLevel, fmt.Sprintf("Failed to create output: %v", err), "scan", "")
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write header
	header := []string{"Directory", "JobName", "AnalysisCode", "AnalysisVersion", "Command", "CoreType", "CoresPerSlot", "WalltimeHours", "Slots", "LicenseSettings", "ExtraInputFileIDs", "SubmitMode"}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}

	// Write rows
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("failed to write row: %w", err)
		}
	}

	e.publishLog(events.InfoLevel, fmt.Sprintf("Jobs CSV written to %s", opts.OutputCSV), "scan", "")
	return nil
}

// ScanToSpecs generates job specs from directory scan without writing CSV
// Returns job list directly for in-memory GUI use (v2.7.1)
// This is the CSV-less alternative to Scan() for GUI workflows
func (e *Engine) ScanToSpecs(template models.JobSpec, opts ScanOptions) ([]models.JobSpec, error) {
	e.publishLog(events.InfoLevel, "Starting in-memory directory scan...", "scan", "")
	e.publishLog(events.InfoLevel, fmt.Sprintf("Using template: %s", template.JobName), "scan", "")

	// v4.7.1: Use validation pattern from scan options only (no config fallback)
	validationPattern := opts.ValidationPattern

	// Structure for directory entries
	type dirEntry struct {
		path        string
		projectName string
	}
	var dirEntries []dirEntry

	// Multi-part mode or normal mode
	if opts.MultiPartMode {
		// Multi-part mode: scan multiple project directories
		e.publishLog(events.InfoLevel, fmt.Sprintf("Multi-part mode enabled with %d project directories", len(opts.PartDirs)), "scan", "")
		e.publishLog(events.InfoLevel, fmt.Sprintf("Subdirectory pattern: '%s'", opts.Pattern), "scan", "")
		if opts.RunSubpath != "" {
			e.publishLog(events.InfoLevel, fmt.Sprintf("Run subpath: '%s'", opts.RunSubpath), "scan", "")
		}
		if validationPattern != "" {
			e.publishLog(events.InfoLevel, fmt.Sprintf("Validation pattern: '%s'", validationPattern), "scan", "")
		}

		// Collect all run directories from all projects
		allRuns, err := multipart.CollectAllRunDirectories(opts.PartDirs, opts.RunSubpath, opts.Pattern)
		if err != nil {
			e.publishLog(events.ErrorLevel, fmt.Sprintf("Failed to collect run directories: %v", err), "scan", "")
			return nil, err
		}

		if len(allRuns) == 0 {
			return nil, fmt.Errorf("no run directories found in any project (pattern='%s')", opts.Pattern)
		}

		e.publishLog(events.InfoLevel, fmt.Sprintf("Found %d total run directories across all projects", len(allRuns)), "scan", "")

		// Validate each run directory
		validRuns := 0
		for _, run := range allRuns {
			if validationPattern == "" || multipart.ValidateRunDirectory(run.RunPath, validationPattern) {
				dirEntries = append(dirEntries, dirEntry{
					path:        run.RunPath,
					projectName: run.ProjectName,
				})
				validRuns++
				e.publishLog(events.InfoLevel, fmt.Sprintf("✓ Valid: %s from %s", run.RunName, run.ProjectName), "scan", "")
			} else {
				e.publishLog(events.DebugLevel, fmt.Sprintf("✗ Skipped: %s from %s (no %s file)", run.RunName, run.ProjectName, validationPattern), "scan", "")
			}
		}

		if len(dirEntries) == 0 {
			return nil, fmt.Errorf("no valid run directories found (validation: %s)", validationPattern)
		}

		e.publishLog(events.InfoLevel, fmt.Sprintf("Total valid runs after validation: %d/%d", validRuns, len(allRuns)), "scan", "")
	} else {
		// Normal mode - scan specified directory or current directory
		scanRoot := ""
		if len(opts.PartDirs) > 0 {
			scanRoot = opts.PartDirs[0]
		} else {
			scanRoot, _ = os.Getwd()
		}

		// Navigate through run_subpath if specified
		if opts.RunSubpath != "" {
			scanRoot = filepath.Join(scanRoot, opts.RunSubpath)
			if _, err := os.Stat(scanRoot); os.IsNotExist(err) {
				return nil, fmt.Errorf("subpath '%s' not found", opts.RunSubpath)
			}
			e.publishLog(events.InfoLevel, fmt.Sprintf("Scanning run directories under subpath: %s", opts.RunSubpath), "scan", "")
		}

		e.publishLog(events.InfoLevel, fmt.Sprintf("Scanning directory: %s (pattern: %s)", scanRoot, opts.Pattern), "scan", "")

		var dirs []string
		if opts.Recursive {
			// Recursive mode - walk directory tree.
			// Uses WalkDir instead of Walk to avoid per-entry os.Stat syscalls.
			err := filepath.WalkDir(scanRoot, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					// Skip hidden directories unless specified
					if !opts.IncludeHidden && localfs.IsHidden(path) && path != scanRoot {
						return filepath.SkipDir
					}
					// Check if this directory matches the pattern
					matched, matchErr := filepath.Match(opts.Pattern, filepath.Base(path))
					if matchErr == nil && matched && path != scanRoot {
						dirs = append(dirs, path)
						// SkipDir: intentionally stop descending into matched directories.
						// Run directories (e.g., Run_*) are expected to be siblings, not nested.
						// This avoids walking the full contents of each matched directory,
						// which is the primary source of slow recursive scans.
						return filepath.SkipDir
					}
				}
				return nil
			})
			if err != nil {
				e.publishLog(events.ErrorLevel, fmt.Sprintf("Failed to walk directory: %v", err), "scan", "")
				return nil, fmt.Errorf("failed to walk directory tree: %w", err)
			}
		} else {
			// Non-recursive mode - single level glob
			matches, err := filepath.Glob(filepath.Join(scanRoot, opts.Pattern))
			if err != nil {
				e.publishLog(events.ErrorLevel, fmt.Sprintf("Failed to glob: %v", err), "scan", "")
				return nil, err
			}
			// Filter to directories only
			for _, match := range matches {
				info, err := os.Stat(match)
				if err == nil && info.IsDir() {
					if !opts.IncludeHidden && localfs.IsHidden(match) {
						continue
					}
					dirs = append(dirs, match)
				}
			}
		}

		e.publishLog(events.InfoLevel, fmt.Sprintf("Found %d directories matching pattern", len(dirs)), "scan", "")

		// Validate directories if validation pattern specified
		var validDirs []string
		if validationPattern != "" {
			e.publishLog(events.InfoLevel, fmt.Sprintf("Validating directories (pattern: %s)", validationPattern), "scan", "")
			for _, dir := range dirs {
				if multipart.ValidateRunDirectory(dir, validationPattern) {
					validDirs = append(validDirs, dir)
				} else {
					e.publishLog(events.DebugLevel, fmt.Sprintf("Skipped %s (no matching file)", filepath.Base(dir)), "scan", "")
				}
			}
			if len(validDirs) == 0 {
				return nil, fmt.Errorf("no directories found matching validation pattern '%s'", validationPattern)
			}
			e.publishLog(events.InfoLevel, fmt.Sprintf("Valid directories after validation: %d/%d", len(validDirs), len(dirs)), "scan", "")
		} else {
			validDirs = dirs
		}

		// Sort directories
		sort.Strings(validDirs)

		// Convert to dirEntries (no project name in normal mode)
		for _, dir := range validDirs {
			dirEntries = append(dirEntries, dirEntry{
				path:        dir,
				projectName: "",
			})
		}
	}

	// Extract template job index for pattern iteration
	templateIdx := pattern.ExtractIndexFromJobName(template.JobName)
	baseJobName := strings.TrimSuffix(template.JobName, "_1")

	// Sort dirEntries by path
	sort.Slice(dirEntries, func(i, j int) bool {
		return dirEntries[i].path < dirEntries[j].path
	})

	// Generate JobSpecs directly (instead of CSV rows)
	var jobs []models.JobSpec
	for i, entry := range dirEntries {
		dirNum := i + opts.StartIndex

		// Extract directory number if using default start index
		if opts.StartIndex == 1 {
			baseName := filepath.Base(entry.path)
			if match := regexp.MustCompile(`\d+`).FindString(baseName); match != "" {
				if num, err := strconv.Atoi(match); err == nil {
					dirNum = num
				}
			}
		}

		// Create job from template
		job := template

		// Normalize directory path to absolute
		if absPath, err := pathutil.ResolveAbsolutePath(entry.path); err == nil {
			job.Directory = absPath
		} else {
			job.Directory = entry.path
		}

		// Create job name with project suffix for multi-part mode
		if opts.MultiPartMode && entry.projectName != "" {
			job.JobName = fmt.Sprintf("%s_%d_%s", baseJobName, dirNum, entry.projectName)
		} else {
			job.JobName = fmt.Sprintf("%s_%d", baseJobName, dirNum)
		}

		// Iterate command patterns if requested
		if opts.IteratePatterns {
			job.Command = pattern.IterateCommandPatterns(template.Command, templateIdx, dirNum)
		}

		// v4.6.0: Set TarSubpath from scan options
		if opts.TarSubpath != "" {
			job.TarSubpath = opts.TarSubpath
		}

		jobs = append(jobs, job)
	}

	e.publishLog(events.InfoLevel, fmt.Sprintf("Generated %d jobs in memory", len(jobs)), "scan", "")
	return jobs, nil
}

// Plan validates a jobs CSV file
func (e *Engine) Plan(jobsCSVPath string, validateCoreType bool) (*PlanResult, error) {
	e.publishLog(events.InfoLevel, "Starting plan validation...", "plan", "")

	// Load jobs
	jobs, err := config.LoadJobsCSV(jobsCSVPath)
	if err != nil {
		e.publishLog(events.ErrorLevel, fmt.Sprintf("Failed to load jobs: %v", err), "plan", "")
		return nil, err
	}

	e.publishLog(events.InfoLevel, fmt.Sprintf("Loaded %d jobs", len(jobs)), "plan", "")

	// Validate each job
	result := &PlanResult{
		TotalJobs:   len(jobs),
		ValidJobs:   0,
		InvalidJobs: 0,
		Errors:      []string{},
	}

	// Optionally validate core types (only if API key is configured)
	var validator *validation.CoreTypeValidator
	if validateCoreType {
		// Check if API key is configured before attempting API calls
		e.mu.RLock()
		hasAPIKey := e.config.APIKey != ""
		e.mu.RUnlock()

		if !hasAPIKey {
			e.publishLog(events.WarnLevel, "API key not configured - skipping core type validation", "plan", "")
			e.publishLog(events.InfoLevel, "Configure API key in Setup tab for full validation", "plan", "")
		} else {
			e.publishLog(events.InfoLevel, "Fetching available core types...", "plan", "")
			validator = validation.NewCoreTypeValidator(e.apiClient)

			// Use context with timeout to prevent hanging on network issues
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			if err := validator.FetchCoreTypes(ctx); err != nil {
				e.publishLog(events.WarnLevel, fmt.Sprintf("Could not fetch core types: %v", err), "plan", "")
				e.publishLog(events.InfoLevel, "Continuing validation without core type check", "plan", "")
				validator = nil
			} else {
				e.publishLog(events.InfoLevel, "Loaded core types for validation", "plan", "")
			}
		}
	}

	for i, job := range jobs {
		errors := e.validateJob(&job, validator)
		if len(errors) > 0 {
			result.InvalidJobs++
			for _, err := range errors {
				errMsg := fmt.Sprintf("Job %d (%s): %s", i+1, job.JobName, err)
				result.Errors = append(result.Errors, errMsg)
				e.publishLog(events.WarnLevel, errMsg, "plan", job.JobName)
			}
		} else {
			result.ValidJobs++
			e.publishLog(events.DebugLevel, fmt.Sprintf("Job %s validated", job.JobName), "plan", job.JobName)
		}

		// Publish progress
		e.publishProgress("plan", "plan", float64(i+1)/float64(len(jobs)),
			fmt.Sprintf("Validating job %d of %d", i+1, len(jobs)))
	}

	if result.InvalidJobs > 0 {
		e.publishLog(events.WarnLevel,
			fmt.Sprintf("Plan complete: %d valid, %d invalid", result.ValidJobs, result.InvalidJobs),
			"plan", "")
	} else {
		e.publishLog(events.InfoLevel,
			fmt.Sprintf("Plan complete: all %d jobs valid", result.ValidJobs),
			"plan", "")
	}

	return result, nil
}

// Run executes the full pipeline
func (e *Engine) Run(ctx context.Context, jobsCSVPath string, stateFile string) error {
	// Check if API key is configured before starting pipeline
	e.mu.RLock()
	hasAPIKey := e.config.APIKey != ""
	e.mu.RUnlock()

	if !hasAPIKey {
		return fmt.Errorf("API key not configured - please enter your API key in the Setup tab and click 'Apply Changes' before running jobs")
	}

	e.mu.Lock()

	// Create cancellable context
	e.ctx, e.cancel = context.WithCancel(ctx)
	ctx = e.ctx

	e.publishLog(events.InfoLevel, "Starting pipeline run...", "run", "")

	// Load jobs
	jobs, err := config.LoadJobsCSV(jobsCSVPath)
	if err != nil {
		e.mu.Unlock()
		return err
	}

	e.publishLog(events.InfoLevel, fmt.Sprintf("Loaded %d jobs from %s", len(jobs), jobsCSVPath), "run", "")

	// v4.6.0: Safety fallback — if state wasn't already initialized by StartRun()
	// (e.g., CLI callers that don't call StartRun), create it here.
	if e.state == nil {
		e.state = state.NewManager(stateFile)
	}

	// Create pipeline with shared state manager
	pip, err := pipeline.NewPipeline(e.config, e.apiClient, jobs, stateFile, false, e.state, false, "", false)
	if err != nil {
		e.mu.Unlock()
		e.publishLog(events.ErrorLevel, fmt.Sprintf("Failed to create pipeline: %v", err), "run", "")
		return err
	}

	// Set up callbacks to publish to event bus
	pip.SetLogCallback(func(level, message, stage, jobName string) {
		var eventLevel events.LogLevel
		switch level {
		case "DEBUG":
			eventLevel = events.DebugLevel
		case "INFO":
			eventLevel = events.InfoLevel
		case "WARN":
			eventLevel = events.WarnLevel
		case "ERROR":
			eventLevel = events.ErrorLevel
		default:
			eventLevel = events.InfoLevel
		}
		e.publishLog(eventLevel, message, stage, jobName)
	})

	pip.SetProgressCallback(func(completed, total int, stage, jobName string) {
		progress := 0.0
		if total > 0 {
			progress = float64(completed) / float64(total)
		}
		e.eventBus.Publish(&events.ProgressEvent{
			BaseEvent: events.BaseEvent{
				EventType: events.EventProgress,
				Time:      time.Now(),
			},
			Progress: progress,
			Stage:    stage,
			JobName:  jobName,
			Message:  fmt.Sprintf("%d/%d jobs complete", completed, total),
		})
	})

	pip.SetStateChangeCallback(func(jobName, stage, newStatus, jobID, errorMessage string, uploadProgress float64) {
		e.publishLog(events.DebugLevel, fmt.Sprintf("[DEBUG] StateChangeCallback: job=%s, stage=%s, status=%s, progress=%.2f",
			jobName, stage, newStatus, uploadProgress), "engine", "")

		// v4.0.6: Update state manager with upload progress for GetJobRows to return
		if stage == "upload" && uploadProgress > 0 {
			e.state.UpdateUploadProgressByName(jobName, uploadProgress)
		}

		e.eventBus.Publish(&events.StateChangeEvent{
			BaseEvent: events.BaseEvent{
				EventType: events.EventStateChange,
				Time:      time.Now(),
			},
			JobName:        jobName,
			Stage:          stage,
			NewStatus:      newStatus,
			JobID:          jobID,
			ErrorMessage:   errorMessage,
			UploadProgress: uploadProgress,
		})
	})

	e.mu.Unlock()

	// Run the pipeline
	startTime := time.Now()
	err = pip.Run(ctx)
	duration := time.Since(startTime)

	// Stop monitoring
	e.stopMonitoring()

	// Get final stats
	stats := e.getJobStats()

	// Emit completion event
	e.eventBus.Publish(&events.CompleteEvent{
		BaseEvent: events.BaseEvent{
			EventType: events.EventComplete,
			Time:      time.Now(),
		},
		TotalJobs:   stats.Total,
		SuccessJobs: stats.Completed,
		FailedJobs:  stats.Failed,
		Duration:    duration,
	})

	if err != nil {
		if err == context.Canceled {
			e.publishLog(events.InfoLevel, "Pipeline stopped by user", "run", "")
		} else {
			e.publishLog(events.ErrorLevel, fmt.Sprintf("Pipeline error: %v", err), "run", "")
		}
		return err
	}

	e.publishLog(events.InfoLevel, fmt.Sprintf("Pipeline completed successfully in %s", duration.Round(time.Second)), "run", "")
	return nil
}

// Resume resumes a pipeline from state file
// Note: Resume is essentially the same as Run but uses existing state
// The jobs CSV must still be provided as the source of truth for job specs
func (e *Engine) Resume(ctx context.Context, jobsCSVPath string, stateFile string) error {
	e.publishLog(events.InfoLevel, "Resuming from state file...", "resume", "")

	// Resume is just Run with an existing state file
	// The pipeline will automatically skip completed jobs
	return e.Run(ctx, jobsCSVPath, stateFile)
}

// RunFromSpecs executes the pipeline from an in-memory job list
// This is the primary GUI entry point for CSV-less operation (v2.7.1)
func (e *Engine) RunFromSpecs(ctx context.Context, jobs []models.JobSpec, stateFile string) error {
	// Check if API key is configured before starting pipeline
	e.mu.RLock()
	hasAPIKey := e.config.APIKey != ""
	e.mu.RUnlock()

	if !hasAPIKey {
		return fmt.Errorf("API key not configured - please enter your API key in the Setup tab and click 'Apply Changes' before running jobs")
	}

	if len(jobs) == 0 {
		return fmt.Errorf("no jobs provided")
	}

	e.mu.Lock()

	// Create cancellable context
	e.ctx, e.cancel = context.WithCancel(ctx)
	ctx = e.ctx

	e.publishLog(events.InfoLevel, fmt.Sprintf("Starting pipeline with %d jobs (in-memory)...", len(jobs)), "run", "")

	// v4.6.0: Safety fallback — if state wasn't already initialized by StartRun()
	// (e.g., CLI callers that don't call StartRun), create it here.
	if e.state == nil {
		e.state = state.NewManager(stateFile)
	}

	// Create pipeline directly from JobSpecs with shared state manager
	pip, err := pipeline.NewPipeline(e.config, e.apiClient, jobs, stateFile, false, e.state, false, "", false)
	if err != nil {
		e.mu.Unlock()
		e.publishLog(events.ErrorLevel, fmt.Sprintf("Failed to create pipeline: %v", err), "run", "")
		return err
	}

	// Set up callbacks to publish to event bus (identical to Run method)
	pip.SetLogCallback(func(level, message, stage, jobName string) {
		var eventLevel events.LogLevel
		switch level {
		case "DEBUG":
			eventLevel = events.DebugLevel
		case "INFO":
			eventLevel = events.InfoLevel
		case "WARN":
			eventLevel = events.WarnLevel
		case "ERROR":
			eventLevel = events.ErrorLevel
		default:
			eventLevel = events.InfoLevel
		}
		e.publishLog(eventLevel, message, stage, jobName)
	})

	pip.SetProgressCallback(func(completed, total int, stage, jobName string) {
		progress := 0.0
		if total > 0 {
			progress = float64(completed) / float64(total)
		}
		e.eventBus.Publish(&events.ProgressEvent{
			BaseEvent: events.BaseEvent{
				EventType: events.EventProgress,
				Time:      time.Now(),
			},
			Progress: progress,
			Stage:    stage,
			JobName:  jobName,
			Message:  fmt.Sprintf("%d/%d jobs complete", completed, total),
		})
	})

	pip.SetStateChangeCallback(func(jobName, stage, newStatus, jobID, errorMessage string, uploadProgress float64) {
		e.publishLog(events.DebugLevel, fmt.Sprintf("[DEBUG] StateChangeCallback: job=%s, stage=%s, status=%s, progress=%.2f",
			jobName, stage, newStatus, uploadProgress), "engine", "")

		// v4.0.6: Update state manager with upload progress for GetJobRows to return
		if stage == "upload" && uploadProgress > 0 {
			e.state.UpdateUploadProgressByName(jobName, uploadProgress)
		}

		e.eventBus.Publish(&events.StateChangeEvent{
			BaseEvent: events.BaseEvent{
				EventType: events.EventStateChange,
				Time:      time.Now(),
			},
			JobName:        jobName,
			Stage:          stage,
			NewStatus:      newStatus,
			JobID:          jobID,
			ErrorMessage:   errorMessage,
			UploadProgress: uploadProgress,
		})
	})

	e.mu.Unlock()

	// Run the pipeline
	startTime := time.Now()
	err = pip.Run(ctx)
	duration := time.Since(startTime)

	// Stop monitoring
	e.stopMonitoring()

	// Get final stats
	stats := e.getJobStats()

	// Emit completion event
	e.eventBus.Publish(&events.CompleteEvent{
		BaseEvent: events.BaseEvent{
			EventType: events.EventComplete,
			Time:      time.Now(),
		},
		TotalJobs:   stats.Total,
		SuccessJobs: stats.Completed,
		FailedJobs:  stats.Failed,
		Duration:    duration,
	})

	if err != nil {
		if err == context.Canceled {
			e.publishLog(events.InfoLevel, "Pipeline stopped by user", "run", "")
		} else {
			e.publishLog(events.ErrorLevel, fmt.Sprintf("Pipeline error: %v", err), "run", "")
		}
		return err
	}

	e.publishLog(events.InfoLevel, fmt.Sprintf("Pipeline completed successfully in %s", duration.Round(time.Second)), "run", "")
	return nil
}

// RunFromSpecsWithOptions executes the pipeline from an in-memory job list with
// additional PUR options (extra input files, decompress flag). This is the GUI
// entry point when extra input files are configured.
func (e *Engine) RunFromSpecsWithOptions(ctx context.Context, jobs []models.JobSpec, stateFile string, extraInputFiles string, decompressExtras bool) error {
	// Check if API key is configured before starting pipeline
	e.mu.RLock()
	hasAPIKey := e.config.APIKey != ""
	e.mu.RUnlock()

	if !hasAPIKey {
		return fmt.Errorf("API key not configured - please enter your API key in the Setup tab and click 'Apply Changes' before running jobs")
	}

	if len(jobs) == 0 {
		return fmt.Errorf("no jobs provided")
	}

	e.mu.Lock()

	// Create cancellable context
	e.ctx, e.cancel = context.WithCancel(ctx)
	ctx = e.ctx

	e.publishLog(events.InfoLevel, fmt.Sprintf("Starting pipeline with %d jobs (in-memory, with options)...", len(jobs)), "run", "")

	// Safety fallback — if state wasn't already initialized by StartRun()
	if e.state == nil {
		e.state = state.NewManager(stateFile)
	}

	// Create pipeline directly from JobSpecs with shared state manager and extra input files
	pip, err := pipeline.NewPipeline(e.config, e.apiClient, jobs, stateFile, false, e.state, false, extraInputFiles, decompressExtras)
	if err != nil {
		e.mu.Unlock()
		e.publishLog(events.ErrorLevel, fmt.Sprintf("Failed to create pipeline: %v", err), "run", "")
		return err
	}

	// Set up callbacks to publish to event bus (identical to RunFromSpecs)
	pip.SetLogCallback(func(level, message, stage, jobName string) {
		var eventLevel events.LogLevel
		switch level {
		case "DEBUG":
			eventLevel = events.DebugLevel
		case "INFO":
			eventLevel = events.InfoLevel
		case "WARN":
			eventLevel = events.WarnLevel
		case "ERROR":
			eventLevel = events.ErrorLevel
		default:
			eventLevel = events.InfoLevel
		}
		e.publishLog(eventLevel, message, stage, jobName)
	})

	pip.SetProgressCallback(func(completed, total int, stage, jobName string) {
		progress := 0.0
		if total > 0 {
			progress = float64(completed) / float64(total)
		}
		e.eventBus.Publish(&events.ProgressEvent{
			BaseEvent: events.BaseEvent{
				EventType: events.EventProgress,
				Time:      time.Now(),
			},
			Progress: progress,
			Stage:    stage,
			JobName:  jobName,
			Message:  fmt.Sprintf("%d/%d jobs complete", completed, total),
		})
	})

	pip.SetStateChangeCallback(func(jobName, stage, newStatus, jobID, errorMessage string, uploadProgress float64) {
		e.publishLog(events.DebugLevel, fmt.Sprintf("[DEBUG] StateChangeCallback: job=%s, stage=%s, status=%s, progress=%.2f",
			jobName, stage, newStatus, uploadProgress), "engine", "")

		if stage == "upload" && uploadProgress > 0 {
			e.state.UpdateUploadProgressByName(jobName, uploadProgress)
		}

		e.eventBus.Publish(&events.StateChangeEvent{
			BaseEvent: events.BaseEvent{
				EventType: events.EventStateChange,
				Time:      time.Now(),
			},
			JobName:        jobName,
			Stage:          stage,
			NewStatus:      newStatus,
			JobID:          jobID,
			ErrorMessage:   errorMessage,
			UploadProgress: uploadProgress,
		})
	})

	e.mu.Unlock()

	// Run the pipeline
	startTime := time.Now()
	err = pip.Run(ctx)
	duration := time.Since(startTime)

	// Stop monitoring
	e.stopMonitoring()

	// Get final stats
	stats := e.getJobStats()

	// Emit completion event
	e.eventBus.Publish(&events.CompleteEvent{
		BaseEvent: events.BaseEvent{
			EventType: events.EventComplete,
			Time:      time.Now(),
		},
		TotalJobs:   stats.Total,
		SuccessJobs: stats.Completed,
		FailedJobs:  stats.Failed,
		Duration:    duration,
	})

	if err != nil {
		if err == context.Canceled {
			e.publishLog(events.InfoLevel, "Pipeline stopped by user", "run", "")
		} else {
			e.publishLog(events.ErrorLevel, fmt.Sprintf("Pipeline error: %v", err), "run", "")
		}
		return err
	}

	e.publishLog(events.InfoLevel, fmt.Sprintf("Pipeline completed successfully in %s", duration.Round(time.Second)), "run", "")
	return nil
}

// Stop cancels any running operations
func (e *Engine) Stop() {
	e.mu.RLock()
	cancel := e.cancel
	e.mu.RUnlock()

	if cancel != nil {
		e.publishLog(events.InfoLevel, "Stopping pipeline...", "", "")
		cancel()
	}

	e.stopMonitoring()
}

// GetJobStatus gets current status of a specific job from Rescale
func (e *Engine) GetJobStatus(jobID string) (string, error) {
	ctx := context.Background()
	job, err := e.apiClient.GetJob(ctx, jobID)
	if err != nil {
		return "", err
	}
	return job.JobStatus.Status, nil
}

// StartJobMonitoring starts monitoring job statuses on Rescale
// v3.4.0: Uses WaitGroup to ensure clean goroutine lifecycle management
func (e *Engine) StartJobMonitoring(interval time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.monitorTicker != nil {
		return // Already monitoring
	}

	e.monitorTicker = time.NewTicker(interval)
	tickerC := e.monitorTicker.C   // Capture channel to avoid race with StopJobMonitoring
	stopC := e.monitorStop         // Capture stop channel as well

	e.monitorWg.Add(1)
	go func() {
		defer e.monitorWg.Done()
		for {
			select {
			case <-tickerC:
				e.checkJobStatuses()
			case <-stopC:
				return
			}
		}
	}()

	e.publishLog(events.InfoLevel, fmt.Sprintf("Started job monitoring (interval: %v)", interval), "", "")
}

// StopJobMonitoring stops job status monitoring
// v3.4.0: Waits for goroutine to exit before returning to prevent race conditions
func (e *Engine) StopJobMonitoring() {
	e.mu.Lock()
	if e.monitorTicker == nil {
		e.mu.Unlock()
		return
	}

	e.monitorTicker.Stop()
	e.monitorTicker = nil
	close(e.monitorStop)
	e.mu.Unlock()

	// v3.4.0: Wait for goroutine to actually exit before creating new channel
	// This prevents race conditions when start is called immediately after stop
	e.monitorWg.Wait()

	e.mu.Lock()
	e.monitorStop = make(chan struct{})
	e.mu.Unlock()

	e.publishLog(events.InfoLevel, "Stopped job monitoring", "", "")
}

// GetState returns the current state
func (e *Engine) GetState() *state.Manager {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.state
}

// LoadState loads jobs from a state file
func (e *Engine) LoadState(stateFile string) ([]*models.JobState, error) {
	st := state.NewManager(stateFile)
	// Load must be called manually
	if err := st.Load(); err != nil {
		return nil, fmt.Errorf("failed to load state: %w", err)
	}

	e.mu.Lock()
	e.state = st
	e.mu.Unlock()

	jobs := st.GetAllStates()
	e.publishLog(events.InfoLevel, fmt.Sprintf("Loaded %d jobs from state", len(jobs)), "", "")

	// Emit state change events for each job
	for _, job := range jobs {
		e.eventBus.PublishStateChange(job.JobName, "", job.SubmitStatus, "", job.JobID, job.ErrorMessage)
	}

	return jobs, nil
}

// ============================================================================
// Run Context Management (v4.0.0)
// These methods provide GUI state synchronization for pipeline runs.
// ============================================================================

// StartRun initializes a new run context. Returns error if a run is already active.
// The caller is responsible for starting the actual pipeline execution.
// v4.6.0: Also initializes the state manager here (before the goroutine) so that
// GetState()/GetRunStats() is never nil while a run is active. This fixes the race
// where polling started before the goroutine had set e.state.
func (e *Engine) StartRun(runID, stateFile string, totalJobs int) error {
	e.runCtxMu.Lock()
	defer e.runCtxMu.Unlock()

	if e.runCtx != nil {
		return fmt.Errorf("a run is already in progress (ID: %s)", e.runCtx.RunID)
	}

	e.runCtx = &RunContext{
		RunID:     runID,
		StartTime: time.Now(),
		StateFile: stateFile,
		TotalJobs: totalJobs,
	}

	// v4.6.0: Initialize state manager in StartRun so it's available before
	// the pipeline goroutine starts. Prevents GetState() returning nil.
	e.mu.Lock()
	e.state = state.NewManager(stateFile)
	e.mu.Unlock()

	e.publishLog(events.InfoLevel, fmt.Sprintf("Started run %s with %d jobs", runID, totalJobs), "run", "")
	return nil
}

// GetRunContext returns a copy of the current run context, or nil if no run is active.
func (e *Engine) GetRunContext() *RunContext {
	e.runCtxMu.RLock()
	defer e.runCtxMu.RUnlock()

	if e.runCtx == nil {
		return nil
	}

	// Return a copy to prevent external modification
	return &RunContext{
		RunID:     e.runCtx.RunID,
		StartTime: e.runCtx.StartTime,
		StateFile: e.runCtx.StateFile,
		TotalJobs: e.runCtx.TotalJobs,
	}
}

// IsRunActive returns true if a pipeline run is currently active.
func (e *Engine) IsRunActive() bool {
	e.runCtxMu.RLock()
	defer e.runCtxMu.RUnlock()
	return e.runCtx != nil
}

// EndRun clears the run context. Called when a run completes or is cancelled.
func (e *Engine) EndRun() {
	e.runCtxMu.Lock()
	defer e.runCtxMu.Unlock()

	if e.runCtx != nil {
		e.publishLog(events.InfoLevel, fmt.Sprintf("Ended run %s", e.runCtx.RunID), "run", "")
		e.runCtx = nil
	}
}

// ResetRun cancels any active run and clears all run state.
func (e *Engine) ResetRun() {
	// Cancel any running context first
	e.mu.RLock()
	cancel := e.cancel
	e.mu.RUnlock()
	if cancel != nil {
		cancel()
	}

	// Clear run context
	e.runCtxMu.Lock()
	e.runCtx = nil
	e.runCtxMu.Unlock()

	// Clear state manager
	e.mu.Lock()
	e.state = nil
	e.mu.Unlock()

	e.publishLog(events.InfoLevel, "Run state reset", "run", "")
}

// GetRunStats returns current job statistics for the active run.
// Returns zeros if no run is active.
// v4.6.0: Updated to handle upstream failures (tar/upload) and skipped jobs.
func (e *Engine) GetRunStats() (total, completed, failed, pending int) {
	e.mu.RLock()
	st := e.state
	e.mu.RUnlock()

	if st == nil {
		return 0, 0, 0, 0
	}

	jobs := st.GetAllStates()
	total = len(jobs)

	for _, job := range jobs {
		switch job.SubmitStatus {
		case "success", "completed":
			completed++
		case "failed":
			failed++
		case "skipped":
			completed++ // create-only mode: skipped submit counts as completed
		default:
			// Belt-and-suspenders for upstream failures that didn't set SubmitStatus
			if job.TarStatus == "failed" || job.UploadStatus == "failed" {
				failed++
			} else {
				pending++
			}
		}
	}

	return total, completed, failed, pending
}

// Private helper methods

func (e *Engine) publishLog(level events.LogLevel, message, stage, jobName string) {
	// Always log to console
	log.Printf("[%s] %s", level.String(), message)

	// Only publish events if enabled (to prevent deadlocks)
	e.eventMu.RLock()
	enabled := e.publishEvents
	e.eventMu.RUnlock()

	if enabled {
		e.eventBus.PublishLog(level, message, stage, jobName, nil)
	}
}

func (e *Engine) publishProgress(jobName, stage string, progress float64, message string) {
	// Only publish events if enabled (to prevent deadlocks)
	e.eventMu.RLock()
	enabled := e.publishEvents
	e.eventMu.RUnlock()

	if enabled {
		e.eventBus.PublishProgress(jobName, stage, progress, message)
	}
}

func (e *Engine) validateJob(job *models.JobSpec, validator *validation.CoreTypeValidator) []string {
	var errors []string

	// Basic validation
	if job.JobName == "" {
		errors = append(errors, "job name is required")
	}
	if job.Directory == "" {
		errors = append(errors, "directory is required")
	} else {
		// Check directory exists
		if _, err := os.Stat(job.Directory); os.IsNotExist(err) {
			errors = append(errors, fmt.Sprintf("directory does not exist: %s", job.Directory))
		}
	}
	if job.Command == "" {
		errors = append(errors, "command is required")
	}
	if job.CoresPerSlot <= 0 {
		errors = append(errors, "cores per slot must be positive")
	}
	if job.Slots <= 0 {
		errors = append(errors, "slots must be positive")
	}
	if job.WalltimeHours <= 0 {
		errors = append(errors, "walltime hours must be positive")
	}

	// Validate core type if we have a validator
	if validator != nil && job.CoreType != "" {
		if err := validator.Validate(job.CoreType); err != nil {
			errors = append(errors, fmt.Sprintf("invalid core type: %s", err))
		}
	}

	return errors
}

func (e *Engine) stopMonitoring() {
	// Already handled by context cancellation
}

func (e *Engine) checkJobStatuses() {
	e.mu.RLock()
	st := e.state
	e.mu.RUnlock()

	if st == nil {
		return
	}

	jobs := st.GetAllStates()
	for _, job := range jobs {
		if job.JobID != "" && job.SubmitStatus == "success" {
			// Check status on Rescale
			status, err := e.GetJobStatus(job.JobID)
			if err != nil {
				e.publishLog(events.WarnLevel,
					fmt.Sprintf("Failed to get status for job %s: %v", job.JobName, err),
					"monitor", job.JobName)
				continue
			}

			// Emit status update event (this will update the UI table)
			e.eventBus.Publish(&events.StateChangeEvent{
				BaseEvent: events.BaseEvent{
					EventType: events.EventStateChange,
					Time:      time.Now(),
				},
				JobName:      job.JobName,
				Stage:        "status",
				NewStatus:    status,
				JobID:        job.JobID,
				ErrorMessage: "",
			})

			e.publishLog(events.DebugLevel,
				fmt.Sprintf("Job %s status: %s", job.JobName, status),
				"monitor", job.JobName)
		}
	}
}

type jobStats struct {
	Total     int
	Completed int
	Failed    int
	Pending   int
}

// v4.6.0: Aligned with GetRunStats() to use consistent SubmitStatus-based logic.
func (e *Engine) getJobStats() jobStats {
	e.mu.RLock()
	st := e.state
	e.mu.RUnlock()

	stats := jobStats{}
	if st == nil {
		return stats
	}

	jobs := st.GetAllStates()
	stats.Total = len(jobs)

	for _, job := range jobs {
		switch job.SubmitStatus {
		case "success", "completed":
			stats.Completed++
		case "failed":
			stats.Failed++
		case "skipped":
			stats.Completed++ // create-only mode
		default:
			if job.TarStatus == "failed" || job.UploadStatus == "failed" {
				stats.Failed++
			} else {
				stats.Pending++
			}
		}
	}

	return stats
}

// PlanResult represents the result of plan validation
type PlanResult struct {
	TotalJobs   int
	ValidJobs   int
	InvalidJobs int
	Errors      []string
}
