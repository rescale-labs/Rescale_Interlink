package gui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rescale/rescale-int/internal/models"
)

// WorkflowState represents the current state in the workflow
type WorkflowState int

const (
	StateInitial            WorkflowState = iota // No selection made
	StatePathChosen                              // User chose Load vs Create
	StateTemplateReady                           // Template selected/created
	StateDirectoriesScanned                      // Jobs CSV generated
	StateJobsValidated                           // Jobs passed validation
	StateExecuting                               // Pipeline running
	StateCompleted                               // All done
	StateError                                   // Error state
)

// String returns the string representation of the workflow state
func (s WorkflowState) String() string {
	switch s {
	case StateInitial:
		return "Initial"
	case StatePathChosen:
		return "PathChosen"
	case StateTemplateReady:
		return "TemplateReady"
	case StateDirectoriesScanned:
		return "DirectoriesScanned"
	case StateJobsValidated:
		return "JobsValidated"
	case StateExecuting:
		return "Executing"
	case StateCompleted:
		return "Completed"
	case StateError:
		return "Error"
	default:
		return "Unknown"
	}
}

// WorkflowPath represents which path the user chose
type WorkflowPath int

const (
	PathUnknown   WorkflowPath = iota
	PathLoadCSV                // Load existing complete CSV
	PathCreateNew              // Create new jobs from scanning
)

// WorkflowMemory stores values between sessions
// Note: Validation pattern and run subpath are now stored in Setup config only
type WorkflowMemory struct {
	LastTemplate     models.JobSpec `json:"last_template"`
	LastScanDir      string         `json:"last_scan_dir"`
	LastPattern      string         `json:"last_pattern"`
	LastCoreType     string         `json:"last_core_type"`
	LastOrgCode      string         `json:"last_org_code"`
	LastProjectID    string         `json:"last_project_id"`
	LastAnalysisCode string         `json:"last_analysis_code"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

// JobsWorkflow manages the workflow state and transitions
type JobsWorkflow struct {
	CurrentState  WorkflowState
	PreviousState WorkflowState // Track state before error for recovery
	CurrentPath   WorkflowPath
	Memory        *WorkflowMemory

	// Current session data
	SelectedCSV  string
	StateFile    string
	Template     *models.JobSpec
	ScannedJobs  []models.JobSpec
	ErrorMessage string
}

// NewJobsWorkflow creates a new workflow instance
func NewJobsWorkflow() *JobsWorkflow {
	memory := loadWorkflowMemory()
	return &JobsWorkflow{
		CurrentState: StateInitial,
		CurrentPath:  PathUnknown,
		Memory:       memory,
	}
}

// CanTransitionTo checks if a transition to the given state is valid
func (w *JobsWorkflow) CanTransitionTo(newState WorkflowState) bool {
	switch newState {
	case StateInitial:
		return true // Can always reset to initial

	case StatePathChosen:
		return w.CurrentState == StateInitial

	case StateTemplateReady:
		return w.CurrentState == StatePathChosen && w.CurrentPath == PathCreateNew

	case StateDirectoriesScanned:
		return w.CurrentState == StateTemplateReady

	case StateJobsValidated:
		return w.CurrentState == StateDirectoriesScanned ||
			(w.CurrentState == StatePathChosen && w.CurrentPath == PathLoadCSV)

	case StateExecuting:
		return w.CurrentState == StateJobsValidated

	case StateCompleted:
		return w.CurrentState == StateExecuting

	case StateError:
		return true // Can go to error from any state

	default:
		return false
	}
}

// TransitionTo moves to a new state if valid
func (w *JobsWorkflow) TransitionTo(newState WorkflowState) error {
	if !w.CanTransitionTo(newState) {
		return fmt.Errorf("invalid transition from %s to %s", w.CurrentState, newState)
	}
	w.CurrentState = newState
	return nil
}

// SetPath sets the workflow path (Load vs Create)
func (w *JobsWorkflow) SetPath(path WorkflowPath) error {
	if w.CurrentState != StateInitial {
		return fmt.Errorf("can only set path from initial state")
	}
	w.CurrentPath = path
	return w.TransitionTo(StatePathChosen)
}

// SetTemplate stores the template and transitions to TemplateReady
func (w *JobsWorkflow) SetTemplate(template models.JobSpec) error {
	if err := w.TransitionTo(StateTemplateReady); err != nil {
		return err
	}
	w.Template = &template

	// Update memory
	w.Memory.LastTemplate = template
	w.Memory.LastCoreType = template.CoreType
	w.Memory.LastAnalysisCode = template.AnalysisCode
	w.Memory.UpdatedAt = time.Now()
	w.saveMemory()

	return nil
}

// SetScannedJobs stores scanned jobs and transitions to DirectoriesScanned
func (w *JobsWorkflow) SetScannedJobs(jobs []models.JobSpec, csvPath string) error {
	if err := w.TransitionTo(StateDirectoriesScanned); err != nil {
		return err
	}
	w.ScannedJobs = jobs
	w.SelectedCSV = csvPath
	w.StateFile = csvPath + ".state"
	return nil
}

// SetLoadedCSV stores loaded CSV path and transitions to JobsValidated
func (w *JobsWorkflow) SetLoadedCSV(csvPath string) error {
	if err := w.TransitionTo(StateJobsValidated); err != nil {
		return err
	}
	w.SelectedCSV = csvPath
	w.StateFile = csvPath + ".state"
	return nil
}

// SetError transitions to error state with a message
func (w *JobsWorkflow) SetError(message string) {
	// Only set PreviousState if coming from a non-executing state
	// (to preserve the pre-execution state set by startExecution)
	if w.CurrentState != StateExecuting {
		w.PreviousState = w.CurrentState
	}
	// If CurrentState is Executing, keep existing PreviousState
	// (which should be JobsValidated, set by startExecution)

	w.CurrentState = StateError
	w.ErrorMessage = message
}

// Reset returns to initial state while preserving memory
func (w *JobsWorkflow) Reset() {
	w.CurrentState = StateInitial
	w.CurrentPath = PathUnknown
	w.SelectedCSV = ""
	w.StateFile = ""
	w.Template = nil
	w.ScannedJobs = nil
	w.ErrorMessage = ""
	// Memory is preserved
}

// UpdateScanSettings updates scan-related memory
func (w *JobsWorkflow) UpdateScanSettings(dir, pattern string) {
	w.Memory.LastScanDir = dir
	w.Memory.LastPattern = pattern
	w.Memory.UpdatedAt = time.Now()
	w.saveMemory()
}

// GetMemoryPath returns the path to the workflow memory file
func getMemoryPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ".pur-gui-memory.json"
	}
	purGuiDir := filepath.Join(homeDir, ".pur-gui")
	os.MkdirAll(purGuiDir, 0755)
	return filepath.Join(purGuiDir, "workflow_memory.json")
}

// loadWorkflowMemory loads memory from disk or returns defaults
func loadWorkflowMemory() *WorkflowMemory {
	path := getMemoryPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return getDefaultMemory()
	}

	var memory WorkflowMemory
	if err := json.Unmarshal(data, &memory); err != nil {
		return getDefaultMemory()
	}

	return &memory
}

// saveMemory saves memory to disk
func (w *JobsWorkflow) saveMemory() error {
	path := getMemoryPath()
	data, err := json.MarshalIndent(w.Memory, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// getDefaultMemory returns default memory values
func getDefaultMemory() *WorkflowMemory {
	return &WorkflowMemory{
		LastTemplate: models.JobSpec{
			Directory:       "./Run_${index}",
			JobName:         "Run_${index}",
			AnalysisCode:    "powerflow",
			AnalysisVersion: "6-2024-hf1 Intel MPI 2021.13",
			Command:         "pf2ens -c _Moving_Belt_CSYS -v p:Cp,pt:Cp Run_${index}.avg.fnc",
			CoreType:        "calcitev2",
			CoresPerSlot:    4,
			WalltimeHours:   48.0,
			Slots:           1,
			LicenseSettings: `{"RLM_LICENSE": "123@license-server"}`,
			SubmitMode:      "create_and_submit",
			Tags:            []string{"pur_test"},
		},
		LastPattern:      "Run_*",
		LastCoreType:     "calcitev2",
		LastAnalysisCode: "powerflow",
	}
}
