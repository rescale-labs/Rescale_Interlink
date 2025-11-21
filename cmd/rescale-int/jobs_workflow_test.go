package main

import (
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

func TestNewJobsWorkflow(t *testing.T) {
	workflow := NewJobsWorkflow()
	if workflow == nil {
		t.Fatal("NewJobsWorkflow returned nil")
	}

	if workflow.CurrentState != StateInitial {
		t.Errorf("Expected initial state, got %v", workflow.CurrentState)
	}

	if workflow.CurrentPath != PathUnknown {
		t.Errorf("Expected unknown path, got %v", workflow.CurrentPath)
	}

	if workflow.Memory == nil {
		t.Error("Expected memory to be initialized")
	}
}

func TestWorkflowStateTransitions(t *testing.T) {
	tests := []struct {
		name          string
		setupState    WorkflowState
		setupPath     WorkflowPath
		targetState   WorkflowState
		shouldSucceed bool
	}{
		{"Initial to PathChosen", StateInitial, PathUnknown, StatePathChosen, true},
		{"PathChosen to TemplateReady (CreateNew)", StatePathChosen, PathCreateNew, StateTemplateReady, true},
		{"PathChosen to TemplateReady (LoadCSV)", StatePathChosen, PathLoadCSV, StateTemplateReady, false},
		{"TemplateReady to DirectoriesScanned", StateTemplateReady, PathCreateNew, StateDirectoriesScanned, true},
		{"DirectoriesScanned to JobsValidated", StateDirectoriesScanned, PathCreateNew, StateJobsValidated, true},
		{"PathChosen to JobsValidated (LoadCSV)", StatePathChosen, PathLoadCSV, StateJobsValidated, true},
		{"JobsValidated to Executing", StateJobsValidated, PathCreateNew, StateExecuting, true},
		{"Executing to Completed", StateExecuting, PathCreateNew, StateCompleted, true},
		{"Any state to Error", StateJobsValidated, PathCreateNew, StateError, true},
		{"Any state to Initial (reset)", StateExecuting, PathCreateNew, StateInitial, true},
		{"Invalid: Initial to Executing", StateInitial, PathUnknown, StateExecuting, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflow := NewJobsWorkflow()
			workflow.CurrentState = tt.setupState
			workflow.CurrentPath = tt.setupPath

			canTransition := workflow.CanTransitionTo(tt.targetState)
			if canTransition != tt.shouldSucceed {
				t.Errorf("CanTransitionTo: expected %v, got %v", tt.shouldSucceed, canTransition)
			}

			err := workflow.TransitionTo(tt.targetState)
			if tt.shouldSucceed && err != nil {
				t.Errorf("TransitionTo failed: %v", err)
			}
			if !tt.shouldSucceed && err == nil {
				t.Error("TransitionTo should have failed but didn't")
			}

			if tt.shouldSucceed && workflow.CurrentState != tt.targetState {
				t.Errorf("Expected state %v, got %v", tt.targetState, workflow.CurrentState)
			}
		})
	}
}

func TestSetPath(t *testing.T) {
	workflow := NewJobsWorkflow()

	// Should succeed from Initial state
	err := workflow.SetPath(PathLoadCSV)
	if err != nil {
		t.Fatalf("SetPath failed: %v", err)
	}

	if workflow.CurrentPath != PathLoadCSV {
		t.Errorf("Expected PathLoadCSV, got %v", workflow.CurrentPath)
	}

	if workflow.CurrentState != StatePathChosen {
		t.Errorf("Expected StatePathChosen, got %v", workflow.CurrentState)
	}

	// Should fail from non-Initial state
	err = workflow.SetPath(PathCreateNew)
	if err == nil {
		t.Error("SetPath should fail from non-Initial state")
	}
}

func TestSetTemplate(t *testing.T) {
	workflow := NewJobsWorkflow()
	workflow.SetPath(PathCreateNew)

	template := models.JobSpec{
		JobName:      "TestJob",
		CoreType:     "test-core",
		AnalysisCode: "test-analysis",
	}

	err := workflow.SetTemplate(template)
	if err != nil {
		t.Fatalf("SetTemplate failed: %v", err)
	}

	if workflow.Template == nil {
		t.Error("Template should be set")
	}

	if workflow.Template.JobName != "TestJob" {
		t.Errorf("Expected JobName 'TestJob', got '%s'", workflow.Template.JobName)
	}

	if workflow.CurrentState != StateTemplateReady {
		t.Errorf("Expected StateTemplateReady, got %v", workflow.CurrentState)
	}

	// Check memory was updated
	if workflow.Memory.LastCoreType != "test-core" {
		t.Errorf("Memory CoreType not updated: got '%s'", workflow.Memory.LastCoreType)
	}

	if workflow.Memory.LastAnalysisCode != "test-analysis" {
		t.Errorf("Memory AnalysisCode not updated: got '%s'", workflow.Memory.LastAnalysisCode)
	}
}

func TestSetScannedJobs(t *testing.T) {
	workflow := NewJobsWorkflow()
	workflow.SetPath(PathCreateNew)
	workflow.SetTemplate(models.JobSpec{JobName: "Template"})

	jobs := []models.JobSpec{
		{JobName: "Job1"},
		{JobName: "Job2"},
	}

	err := workflow.SetScannedJobs(jobs, "/path/to/jobs.csv")
	if err != nil {
		t.Fatalf("SetScannedJobs failed: %v", err)
	}

	if len(workflow.ScannedJobs) != 2 {
		t.Errorf("Expected 2 jobs, got %d", len(workflow.ScannedJobs))
	}

	if workflow.SelectedCSV != "/path/to/jobs.csv" {
		t.Errorf("Expected CSV path '/path/to/jobs.csv', got '%s'", workflow.SelectedCSV)
	}

	if workflow.StateFile != "/path/to/jobs.csv.state" {
		t.Errorf("Expected state file '/path/to/jobs.csv.state', got '%s'", workflow.StateFile)
	}

	if workflow.CurrentState != StateDirectoriesScanned {
		t.Errorf("Expected StateDirectoriesScanned, got %v", workflow.CurrentState)
	}
}

func TestSetLoadedCSV(t *testing.T) {
	workflow := NewJobsWorkflow()
	workflow.SetPath(PathLoadCSV)

	err := workflow.SetLoadedCSV("/path/to/existing.csv")
	if err != nil {
		t.Fatalf("SetLoadedCSV failed: %v", err)
	}

	if workflow.SelectedCSV != "/path/to/existing.csv" {
		t.Errorf("Expected CSV path '/path/to/existing.csv', got '%s'", workflow.SelectedCSV)
	}

	if workflow.StateFile != "/path/to/existing.csv.state" {
		t.Errorf("Expected state file '/path/to/existing.csv.state', got '%s'", workflow.StateFile)
	}

	if workflow.CurrentState != StateJobsValidated {
		t.Errorf("Expected StateJobsValidated, got %v", workflow.CurrentState)
	}
}

func TestSetError(t *testing.T) {
	workflow := NewJobsWorkflow()
	workflow.SetError("Test error")

	if workflow.CurrentState != StateError {
		t.Errorf("Expected StateError, got %v", workflow.CurrentState)
	}

	if workflow.ErrorMessage != "Test error" {
		t.Errorf("Expected error message 'Test error', got '%s'", workflow.ErrorMessage)
	}
}

func TestReset(t *testing.T) {
	workflow := NewJobsWorkflow()
	workflow.SetPath(PathCreateNew)
	workflow.SetTemplate(models.JobSpec{JobName: "Template"})
	workflow.SetScannedJobs([]models.JobSpec{{JobName: "Job1"}}, "/path/to/jobs.csv")

	originalMemory := workflow.Memory

	workflow.Reset()

	if workflow.CurrentState != StateInitial {
		t.Errorf("Expected StateInitial after reset, got %v", workflow.CurrentState)
	}

	if workflow.CurrentPath != PathUnknown {
		t.Errorf("Expected PathUnknown after reset, got %v", workflow.CurrentPath)
	}

	if workflow.SelectedCSV != "" {
		t.Error("SelectedCSV should be empty after reset")
	}

	if workflow.Template != nil {
		t.Error("Template should be nil after reset")
	}

	if workflow.ScannedJobs != nil {
		t.Error("ScannedJobs should be nil after reset")
	}

	// Memory should be preserved
	if workflow.Memory != originalMemory {
		t.Error("Memory should be preserved after reset")
	}
}

func TestUpdateScanSettings(t *testing.T) {
	workflow := NewJobsWorkflow()

	workflow.UpdateScanSettings("/test/dir", "Run_*")

	if workflow.Memory.LastScanDir != "/test/dir" {
		t.Errorf("Expected LastScanDir '/test/dir', got '%s'", workflow.Memory.LastScanDir)
	}

	if workflow.Memory.LastPattern != "Run_*" {
		t.Errorf("Expected LastPattern 'Run_*', got '%s'", workflow.Memory.LastPattern)
	}
}

func TestWorkflowMemoryPersistence(t *testing.T) {
	// Test that memory is saved and can be loaded
	workflow1 := NewJobsWorkflow()

	// Update with custom values
	workflow1.UpdateScanSettings("/custom/dir", "Custom_*")

	// Force save
	if err := workflow1.saveMemory(); err != nil {
		t.Fatalf("Failed to save memory: %v", err)
	}

	// Load memory again
	memory := loadWorkflowMemory()

	if memory.LastScanDir != "/custom/dir" {
		t.Errorf("Memory not persisted: expected '/custom/dir', got '%s'", memory.LastScanDir)
	}

	if memory.LastPattern != "Custom_*" {
		t.Errorf("Memory not persisted: expected 'Custom_*', got '%s'", memory.LastPattern)
	}
}

func TestWorkflowStateString(t *testing.T) {
	tests := []struct {
		state    WorkflowState
		expected string
	}{
		{StateInitial, "Initial"},
		{StatePathChosen, "PathChosen"},
		{StateTemplateReady, "TemplateReady"},
		{StateDirectoriesScanned, "DirectoriesScanned"},
		{StateJobsValidated, "JobsValidated"},
		{StateExecuting, "Executing"},
		{StateCompleted, "Completed"},
		{StateError, "Error"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if tt.state.String() != tt.expected {
				t.Errorf("Expected '%s', got '%s'", tt.expected, tt.state.String())
			}
		})
	}
}
