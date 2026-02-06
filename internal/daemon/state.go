// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// DownloadedJob tracks a job that has been downloaded by the daemon.
type DownloadedJob struct {
	JobID        string    `json:"job_id"`
	JobName      string    `json:"job_name"`
	DownloadedAt time.Time `json:"downloaded_at"`
	OutputDir    string    `json:"output_dir"`
	FileCount    int       `json:"file_count"`
	TotalSize    int64     `json:"total_size"`
	Error        string    `json:"error,omitempty"`
}

// State maintains the daemon's persistent state.
type State struct {
	mu sync.RWMutex

	// Downloaded jobs keyed by job ID
	Downloaded map[string]*DownloadedJob `json:"downloaded"`

	// Version for state file format migration
	Version string `json:"version"`

	// LastPoll records the last successful poll time
	LastPoll time.Time `json:"last_poll"`

	// Path to the state file
	filePath string
}

// NewState creates a new state instance.
func NewState(filePath string) *State {
	return &State{
		Downloaded: make(map[string]*DownloadedJob),
		Version:    "1.0.0",
		filePath:   filePath,
	}
}

// Load reads state from the file system.
// If the file doesn't exist, returns an empty state.
// v4.5.8: Runs one-time migration from old Unix-style path on Windows.
func (s *State) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// v4.5.8: Migrate state file from old path if needed (Windows only)
	if runtime.GOOS == "windows" {
		if oldPath := oldStateFilePath(); oldPath != "" {
			migrateStateFile(oldPath, s.filePath)
		}
	}

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Fresh state
			s.Downloaded = make(map[string]*DownloadedJob)
			s.Version = "1.0.0"
			return nil
		}
		return fmt.Errorf("failed to read state file: %w", err)
	}

	if err := json.Unmarshal(data, s); err != nil {
		return fmt.Errorf("failed to parse state file: %w", err)
	}

	// Ensure map is initialized
	if s.Downloaded == nil {
		s.Downloaded = make(map[string]*DownloadedJob)
	}

	return nil
}

// Save writes state to the file system.
func (s *State) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Ensure directory exists
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	// Write to temp file first, then rename for atomicity
	tmpFile := s.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	if err := os.Rename(tmpFile, s.filePath); err != nil {
		os.Remove(tmpFile) // Clean up temp file
		return fmt.Errorf("failed to rename state file: %w", err)
	}

	return nil
}

// IsDownloaded checks if a job has already been downloaded.
func (s *State) IsDownloaded(jobID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	job, exists := s.Downloaded[jobID]
	if !exists {
		return false
	}
	// Consider downloaded if no error
	return job.Error == ""
}

// MarkDownloaded records a job as successfully downloaded.
func (s *State) MarkDownloaded(jobID, jobName, outputDir string, fileCount int, totalSize int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Downloaded[jobID] = &DownloadedJob{
		JobID:        jobID,
		JobName:      jobName,
		DownloadedAt: time.Now(),
		OutputDir:    outputDir,
		FileCount:    fileCount,
		TotalSize:    totalSize,
	}
}

// MarkFailed records a job download failure.
func (s *State) MarkFailed(jobID, jobName string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Downloaded[jobID] = &DownloadedJob{
		JobID:        jobID,
		JobName:      jobName,
		DownloadedAt: time.Now(),
		Error:        err.Error(),
	}
}

// ClearFailed removes failed status for a job, allowing retry.
func (s *State) ClearFailed(jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, exists := s.Downloaded[jobID]
	if exists && job.Error != "" {
		delete(s.Downloaded, jobID)
	}
}

// UpdateLastPoll records the last successful poll time.
func (s *State) UpdateLastPoll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastPoll = time.Now()
}

// GetLastPoll returns the last successful poll time.
func (s *State) GetLastPoll() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.LastPoll
}

// GetDownloadedCount returns the number of successfully downloaded jobs.
func (s *State) GetDownloadedCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for _, job := range s.Downloaded {
		if job.Error == "" {
			count++
		}
	}
	return count
}

// GetFailedCount returns the number of failed downloads.
func (s *State) GetFailedCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for _, job := range s.Downloaded {
		if job.Error != "" {
			count++
		}
	}
	return count
}

// GetRecentDownloads returns the most recent successfully downloaded jobs.
func (s *State) GetRecentDownloads(limit int) []*DownloadedJob {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var downloads []*DownloadedJob
	for _, job := range s.Downloaded {
		if job.Error == "" {
			downloads = append(downloads, job)
		}
	}

	// Sort by download time (most recent first)
	for i := 0; i < len(downloads)-1; i++ {
		for j := i + 1; j < len(downloads); j++ {
			if downloads[j].DownloadedAt.After(downloads[i].DownloadedAt) {
				downloads[i], downloads[j] = downloads[j], downloads[i]
			}
		}
	}

	if limit > 0 && len(downloads) > limit {
		return downloads[:limit]
	}
	return downloads
}

// GetFailedJobs returns all jobs that failed to download.
func (s *State) GetFailedJobs() []*DownloadedJob {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var failed []*DownloadedJob
	for _, job := range s.Downloaded {
		if job.Error != "" {
			failed = append(failed, job)
		}
	}
	return failed
}

// DefaultStateFilePath returns the default path for the daemon state file.
// v4.5.8: On Windows, uses %LOCALAPPDATA%\Rescale\Interlink\state\ (consistent with
// install/logs paths). Previously used Unix-style ~/.config/rescale-int/ on all platforms.
func DefaultStateFilePath() string {
	if runtime.GOOS == "windows" {
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData != "" {
			return filepath.Join(localAppData, "Rescale", "Interlink", "state", "daemon-state.json")
		}
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ".rescale-daemon-state.json"
	}
	return filepath.Join(homeDir, ".config", "rescale-int", "daemon-state.json")
}

// oldStateFilePath returns the legacy state file path for migration (Unix-style).
func oldStateFilePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".config", "rescale-int", "daemon-state.json")
}

// migrateStateFile moves state file from old path to new path if needed.
// v4.5.8: One-time migration from Unix-style path to Windows-native path.
func migrateStateFile(oldPath, newPath string) {
	if oldPath == "" || newPath == "" || oldPath == newPath {
		return
	}
	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		return // Nothing to migrate
	}
	if _, err := os.Stat(newPath); err == nil {
		return // New path already exists, don't overwrite
	}
	// Ensure target directory exists
	os.MkdirAll(filepath.Dir(newPath), 0700)
	// Try rename first (fast path)
	if err := os.Rename(oldPath, newPath); err != nil {
		// Cross-volume fallback: copy + delete
		data, err := os.ReadFile(oldPath)
		if err != nil {
			return
		}
		if err := os.WriteFile(newPath, data, 0600); err != nil {
			return
		}
		os.Remove(oldPath)
	}
}
