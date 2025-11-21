// Package state provides unified state management for both CLI and GUI modes.
// State files are stored as CSV files in ~/.rescale/state/ for interoperability.
package state

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StateManager manages job state files for both CLI and GUI modes.
type StateManager struct {
	stateDir  string
	stateFile string
}

// JobState represents the state of a single job.
type JobState struct {
	JobName   string
	JobID     string
	Status    string
	Stage     string
	Timestamp time.Time
	FileID    string
	TarPath   string
	Error     string
}

// NewStateManager creates a new state manager for a job set.
// The state file will be stored in ~/.rescale/state/<jobSetName>_state.csv
func NewStateManager(jobSetName string) (*StateManager, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get user home directory: %w", err)
	}

	stateDir := filepath.Join(homeDir, ".rescale", "state")

	// Create state directory if it doesn't exist
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}

	return &StateManager{
		stateDir:  stateDir,
		stateFile: fmt.Sprintf("%s_state.csv", jobSetName),
	}, nil
}

// NewStateManagerWithPath creates a state manager with a specific state file path.
func NewStateManagerWithPath(stateFilePath string) (*StateManager, error) {
	absPath, err := filepath.Abs(stateFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	stateDir := filepath.Dir(absPath)
	stateFile := filepath.Base(absPath)

	// Create directory if it doesn't exist
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}

	return &StateManager{
		stateDir:  stateDir,
		stateFile: stateFile,
	}, nil
}

// GetStatePath returns the full path to the state file.
func (sm *StateManager) GetStatePath() string {
	return filepath.Join(sm.stateDir, sm.stateFile)
}

// LoadJobs loads all job states from the state file.
func (sm *StateManager) LoadJobs() ([]JobState, error) {
	statePath := sm.GetStatePath()

	// If file doesn't exist, return empty list
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		return []JobState{}, nil
	}

	file, err := os.Open(statePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open state file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	// Skip header if present
	startIdx := 0
	if len(records) > 0 && records[0][0] == "JobName" {
		startIdx = 1
	}

	jobs := make([]JobState, 0, len(records)-startIdx)
	for i := startIdx; i < len(records); i++ {
		record := records[i]
		if len(record) < 8 {
			continue // Skip invalid records
		}

		timestamp, _ := time.Parse(time.RFC3339, record[4])

		jobs = append(jobs, JobState{
			JobName:   record[0],
			JobID:     record[1],
			Status:    record[2],
			Stage:     record[3],
			Timestamp: timestamp,
			FileID:    record[5],
			TarPath:   record[6],
			Error:     record[7],
		})
	}

	return jobs, nil
}

// SaveJobs saves all job states to the state file.
func (sm *StateManager) SaveJobs(jobs []JobState) error {
	statePath := sm.GetStatePath()

	file, err := os.Create(statePath)
	if err != nil {
		return fmt.Errorf("failed to create state file: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write header
	if err := writer.Write([]string{
		"JobName", "JobID", "Status", "Stage", "Timestamp", "FileID", "TarPath", "ErrorMessage",
	}); err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}

	// Write records
	for _, job := range jobs {
		record := []string{
			job.JobName,
			job.JobID,
			job.Status,
			job.Stage,
			job.Timestamp.Format(time.RFC3339),
			job.FileID,
			job.TarPath,
			job.Error,
		}
		if err := writer.Write(record); err != nil {
			return fmt.Errorf("failed to write record: %w", err)
		}
	}

	return nil
}

// UpdateJob updates or adds a job state.
func (sm *StateManager) UpdateJob(job JobState) error {
	jobs, err := sm.LoadJobs()
	if err != nil {
		return err
	}

	// Find and update existing job, or append new one
	found := false
	for i, j := range jobs {
		if j.JobName == job.JobName {
			jobs[i] = job
			found = true
			break
		}
	}

	if !found {
		jobs = append(jobs, job)
	}

	return sm.SaveJobs(jobs)
}

// GetJob retrieves a specific job state by name.
func (sm *StateManager) GetJob(jobName string) (*JobState, error) {
	jobs, err := sm.LoadJobs()
	if err != nil {
		return nil, err
	}

	for _, job := range jobs {
		if job.JobName == jobName {
			return &job, nil
		}
	}

	return nil, fmt.Errorf("job not found: %s", jobName)
}
