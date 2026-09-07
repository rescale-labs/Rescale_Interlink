package state

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	// Iterate over map keys to handle non-consecutive indices
	indices := make([]int, 0, len(m.states))
	for idx := range m.states {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	for _, idx := range indices {
		state := m.states[idx]
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

// FilePath is the file this manager persists to.
//
// It is what identifies a run: the pipeline derives its archive namespace from
// it so two batches over the same inputs do not write one archive, and a resume
// lands on the batch's own archives again. Set at construction and never
// written afterwards, so it needs no lock.
func (m *Manager) FilePath() string {
	return m.filePath
}

// GetState returns a snapshot of the state for a given job index, or nil when
// the index is unknown.
//
// A snapshot, not the manager's own object: handing out the stored pointer put
// it beyond the reach of this lock, so a worker recording an upload's file ID
// wrote fields that another worker's checkpoint was serializing at the same
// moment. Callers change their snapshot and hand it back through UpdateState,
// which is where the manager takes the change.
func (m *Manager) GetState(index int) *models.JobState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stored, ok := m.states[index]
	if !ok {
		return nil
	}
	snapshot := *stored
	return &snapshot
}

// UpdateState takes a caller's snapshot as the job's new state and checkpoints
// it. The snapshot is copied in, so the caller may go on using its own object
// without reaching what a later checkpoint serializes.
func (m *Manager) UpdateState(state *models.JobState) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	state.LastUpdated = time.Now()
	stored := *state

	// UploadProgress is the one field the manager owns rather than the caller:
	// UpdateUploadProgressByName writes it live while a transfer runs, and it is
	// never written to the CSV. A snapshot taken before that transfer started
	// carries zero, which used to be harmless because the caller held the very
	// object being updated. Zero therefore means "this snapshot says nothing
	// about progress" and leaves the live figure alone.
	if stored.UploadProgress == 0 {
		if previous, ok := m.states[state.Index]; ok {
			stored.UploadProgress = previous.UploadProgress
		}
	}

	m.states[state.Index] = &stored

	// Save immediately for persistence (while still holding lock to prevent race)
	return m.saveUnlocked()
}

// InitializeState initializes state for a new job and returns a snapshot of it.
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
	snapshot := *state
	return &snapshot
}

// GetAllStates returns snapshots of all job states, sorted by index. As with
// GetState, changing one changes nothing here until it is passed to
// UpdateState.
func (m *Manager) GetAllStates() []*models.JobState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Collect and sort indices for deterministic ordering
	indices := make([]int, 0, len(m.states))
	for idx := range m.states {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	states := make([]*models.JobState, 0, len(m.states))
	for _, idx := range indices {
		snapshot := *m.states[idx]
		states = append(states, &snapshot)
	}
	return states
}

// UpdateUploadProgressByName updates the upload progress for a job by name.
// This is a transient update - progress is not persisted to CSV (only status is).
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
