package state

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/models"
)

// Manager manages job state persistence
type Manager struct {
	filePath string
	states   map[int]*models.JobState // Index -> JobState
	mu       sync.RWMutex
}

// NewManager creates a new state manager
func NewManager(filePath string) *Manager {
	return &Manager{
		filePath: filePath,
		states:   make(map[int]*models.JobState),
	}
}

// Load loads state from CSV file
func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, err := os.Stat(m.filePath); os.IsNotExist(err) {
		return nil // No state file yet, that's OK
	}

	file, err := os.Open(m.filePath)
	if err != nil {
		return fmt.Errorf("failed to open state file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("failed to read state CSV: %w", err)
	}

	if len(records) < 2 {
		return nil // Empty state file
	}

	// Expected header: Index,JobName,Directory,TarPath,TarStatus,FileID,UploadStatus,JobID,SubmitStatus,ExtraFileIDs,ErrorMessage,LastUpdated
	for i := 1; i < len(records); i++ {
		record := records[i]
		if len(record) < 12 {
			continue
		}

		var index int
		fmt.Sscanf(record[0], "%d", &index)

		lastUpdated, _ := time.Parse(time.RFC3339, record[11])

		state := &models.JobState{
			Index:        index,
			JobName:      record[1],
			Directory:    record[2],
			TarPath:      record[3],
			TarStatus:    record[4],
			FileID:       record[5],
			UploadStatus: record[6],
			JobID:        record[7],
			SubmitStatus: record[8],
			ExtraFileIDs: record[9],
			ErrorMessage: record[10],
			LastUpdated:  lastUpdated,
		}

		m.states[index] = state
	}

	return nil
}

// Save saves state to CSV file (atomic write)
func (m *Manager) Save() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.saveUnlocked()
}

// saveUnlocked saves state to CSV file without acquiring locks.
// Caller must hold at least RLock on m.mu.
func (m *Manager) saveUnlocked() error {
	// Create directory if it doesn't exist
	dir := filepath.Dir(m.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Write to temporary file first
	tempFile := m.filePath + ".tmp"
	file, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("failed to create temp state file: %w", err)
	}

	// Use a flag to track successful completion for cleanup
	success := false
	defer func() {
		if !success {
			file.Close()
			os.Remove(tempFile) // Clean up temp file on error
		}
	}()

	writer := csv.NewWriter(file)

	// Write header
	header := []string{"Index", "JobName", "Directory", "TarPath", "TarStatus", "FileID",
		"UploadStatus", "JobID", "SubmitStatus", "ExtraFileIDs", "ErrorMessage", "LastUpdated"}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("failed to write state header: %w", err)
	}

	// Write data rows (sorted by index)
	for i := 1; i <= len(m.states); i++ {
		if state, ok := m.states[i]; ok {
			record := []string{
				fmt.Sprintf("%d", state.Index),
				state.JobName,
				state.Directory,
				state.TarPath,
				state.TarStatus,
				state.FileID,
				state.UploadStatus,
				state.JobID,
				state.SubmitStatus,
				state.ExtraFileIDs,
				state.ErrorMessage,
				state.LastUpdated.Format(time.RFC3339),
			}
			if err := writer.Write(record); err != nil {
				return fmt.Errorf("failed to write state record: %w", err)
			}
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("failed to flush state writer: %w", err)
	}

	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to close temp state file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tempFile, m.filePath); err != nil {
		return fmt.Errorf("failed to rename state file: %w", err)
	}

	success = true
	return nil
}

// GetState returns the state for a given job index
func (m *Manager) GetState(index int) *models.JobState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.states[index]
}

// UpdateState updates the state for a given job
func (m *Manager) UpdateState(state *models.JobState) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	state.LastUpdated = time.Now()
	m.states[state.Index] = state

	// Save immediately for persistence (while still holding lock to prevent race)
	return m.saveUnlocked()
}

// InitializeState initializes state for a new job
func (m *Manager) InitializeState(index int, jobName, directory string) *models.JobState {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := &models.JobState{
		Index:        index,
		JobName:      jobName,
		Directory:    directory,
		TarStatus:    "pending",
		UploadStatus: "pending",
		SubmitStatus: "pending",
		LastUpdated:  time.Now(),
	}

	m.states[index] = state
	return state
}

// GetAllStates returns all job states
func (m *Manager) GetAllStates() []*models.JobState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	states := make([]*models.JobState, 0, len(m.states))
	for i := 1; i <= len(m.states); i++ {
		if state, ok := m.states[i]; ok {
			states = append(states, state)
		}
	}
	return states
}

// CountByStatus counts jobs by their status
func (m *Manager) CountByStatus(statusField string, status string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, state := range m.states {
		switch statusField {
		case "tar":
			if state.TarStatus == status {
				count++
			}
		case "upload":
			if state.UploadStatus == status {
				count++
			}
		case "submit":
			if state.SubmitStatus == status {
				count++
			}
		}
	}
	return count
}

// UpdateUploadProgress updates the upload progress for a job by index.
// v4.0.6: Added to support real-time upload progress display in GUI.
// This is a transient update - progress is not persisted to CSV (only status is).
func (m *Manager) UpdateUploadProgress(index int, progress float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if state, ok := m.states[index]; ok {
		state.UploadProgress = progress
	}
}

// UpdateUploadProgressByName updates the upload progress for a job by name.
// v4.0.6: Added to support looking up jobs by name when index is not available.
func (m *Manager) UpdateUploadProgressByName(jobName string, progress float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, state := range m.states {
		if state.JobName == jobName {
			state.UploadProgress = progress
			return
		}
	}
}
