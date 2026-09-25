// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/reporting"
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

	RetryCount  int       `json:"retry_count,omitempty"`
	LastAttempt time.Time `json:"last_attempt,omitempty"`

	// PendingTagApply is true when the files downloaded successfully but the
	// 'downloaded' tag API call failed. The poll loop retries the tag call
	// on subsequent polls without re-downloading files; on success the flag
	// is cleared. Jobs with this flag set are skipped pre-eligibility so
	// they never re-enter the download path.
	PendingTagApply bool `json:"pending_tag_apply,omitempty"`
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

	// retention bounds how long finished entries are kept; see SetRetention.
	retention time.Duration

	// upgraded counts the failures Load gave one more attempt; see stateVersion.
	upgraded int
}

// stateVersion is the state file format written now. A 1.0.0 file comes from a
// daemon that retried a failed download at every poll; see Load.
const stateVersion = "1.1.0"

// NewState creates a new state instance.
func NewState(filePath string) *State {
	return &State{
		Downloaded: make(map[string]*DownloadedJob),
		Version:    stateVersion,
		filePath:   filePath,
	}
}

// Load reads state from the file system, and changes nothing there.
// If the file doesn't exist, returns an empty state, as it does for a corrupt
// one; the daemon sets that aside, and moves a legacy state file in (see open).
func (s *State) Load() error {
	// A read is whole without the lock, since the file is only ever replaced
	// whole. The lock, once a process that rewrites the file has made it, keeps
	// the file from being open here while another process renames over it,
	// which Windows refuses.
	if _, err := os.Stat(s.filePath + ".lock"); err == nil {
		if unlock, err := lockFile(s.filePath + ".lock"); err == nil {
			defer unlock()
		}
	}
	return s.load(false)
}

// open is Load for the daemon, which rewrites the file. Under the file's lock,
// having made its folder, it also runs the one-time migration from the legacy
// Unix-style path on any OS (Windows was already doing this; Plan 2 also
// consolidates Unix/macOS from ~/.config/rescale-int/ to ~/.config/rescale/),
// and sets a corrupt file aside.
func (s *State) open() error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return s.load(true)
}

// load reads the state file. Only a caller that holds its lock changes files:
// it moves a legacy state file in and sets a corrupt one aside.
func (s *State) load(locked bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if oldPath := oldStateFilePath(); locked && oldPath != "" && oldPath != s.filePath {
		migrateStateFile(oldPath, s.filePath)
	}

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Fresh state
			s.Downloaded = make(map[string]*DownloadedJob)
			s.Version = stateVersion
			return nil
		}
		return fmt.Errorf("failed to read state file: %w", err)
	}

	if err := json.Unmarshal(data, s); err != nil {
		// Corrupt state file — preserve and start fresh instead of failing.
		// Use timestamped suffix to avoid overwriting previous .corrupt files.
		if locked {
			corruptPath := fmt.Sprintf("%s.corrupt.%d", s.filePath, time.Now().Unix())
			os.Rename(s.filePath, corruptPath) // best-effort, ignore error
		}
		// Fully reinitialize — Unmarshal may have left partial state
		s.Version = stateVersion
		s.Downloaded = make(map[string]*DownloadedJob)
		s.LastPoll = time.Time{}
		return nil
	}

	// Ensure map is initialized
	if s.Downloaded == nil {
		s.Downloaded = make(map[string]*DownloadedJob)
	}
	// Failures are redacted as they are loaded, since 'daemon list --failed'
	// prints them.
	for _, job := range s.Downloaded {
		if job != nil {
			job.Error = reporting.RedactSecrets(job.Error)
		}
	}

	// A 1.0.0 count is of polls, not attempts under the backoff, so a job that
	// reached the limit that way gets one attempt before it is held.
	if s.Version == "1.0.0" {
		for _, job := range s.Downloaded {
			if job != nil && job.Error != "" && job.RetryCount >= MaxDownloadAttempts {
				job.RetryCount = MaxDownloadAttempts - 1
				s.upgraded++
			}
		}
		s.Version = stateVersion
	}

	return nil
}

// Save writes state to the file system.
func (s *State) Save() error {
	return s.update(nil)
}

// update rewrites the state file under its lock, having taken in the releases
// 'daemon retry' made in it and then applied change. 'daemon retry' rewrites
// the file under the same lock, so neither process writes over a change of the
// other's that it has not read.
func (s *State) update(change func()) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()

	s.pruneExpired()
	s.releaseRetried()
	if change != nil {
		change()
	}
	return s.write()
}

// lock takes the lock every process holds while it rewrites the state file,
// creating the file's folder first.
func (s *State) lock() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}
	unlock, err := lockFile(s.filePath + ".lock")
	if err != nil {
		return nil, fmt.Errorf("failed to lock state file: %w", err)
	}
	return unlock, nil
}

// stateFileStep runs when a rewrite of the state file, having read the file, is
// about to replace it: where another process's rewrite would be lost. Only a
// test sets it.
var stateFileStep = func() {}

// write writes state to the file system. The caller holds the state file's lock.
func (s *State) write() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	// Write to a uniquely named temp file in the same directory, then rename
	// for atomicity. A fixed ".tmp" name is not safe: two writers (a daemon
	// plus a `daemon retry` invocation, or two daemons) interleave writes into
	// the same temp file and the survivor renames a half-written mixture over
	// the real state.
	tmp, err := os.CreateTemp(filepath.Dir(s.filePath), filepath.Base(s.filePath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp state file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // No-op once the rename below succeeds.

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to set state file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temp state file: %w", err)
	}

	stateFileStep()
	if err := os.Rename(tmpName, s.filePath); err != nil {
		return fmt.Errorf("failed to rename state file: %w", err)
	}

	return nil
}

// releaseRetried takes in the releases 'daemon retry' has made in the file: a
// running daemon holds its state in memory, and would otherwise keep a released
// job waiting and write the failure back. Only a release of the very failure
// held here (same last attempt) counts, so an old release changes nothing. The
// caller holds the state file's lock.
func (s *State) releaseRetried() {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return
	}
	var onDisk struct {
		Downloaded map[string]*DownloadedJob `json:"downloaded"`
	}
	if err := json.Unmarshal(data, &onDisk); err != nil {
		return
	}
	for id, job := range s.Downloaded {
		if d := onDisk.Downloaded[id]; job != nil && job.Error != "" && d != nil && d.Error != "" &&
			d.RetryCount == 0 && d.LastAttempt.Equal(job.LastAttempt) {
			job.RetryCount = 0
		}
	}
}

// SetRetention bounds how long finished job entries are kept. Entries older
// than the retention window are dropped on the next Save. Zero (the default)
// keeps everything.
//
// The daemon sets this to its lookback window plus the API pre-filter buffer:
// beyond that, a job can no longer be selected by a scan, so its entry can
// only grow the file.
func (s *State) SetRetention(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retention = d
}

// pruneExpired drops job entries older than the retention window. Entries with
// a pending tag apply are always kept: the poll loop still owes them a tag
// call, and losing the flag would let the job be downloaded again.
func (s *State) pruneExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.retention <= 0 {
		return
	}
	cutoff := time.Now().Add(-s.retention)
	for id, job := range s.Downloaded {
		if job == nil {
			delete(s.Downloaded, id)
			continue
		}
		if job.PendingTagApply {
			continue
		}
		if job.DownloadedAt.Before(cutoff) {
			delete(s.Downloaded, id)
		}
	}
}

// MaxDownloadAttempts is how many failed attempts the daemon makes at a job's
// download before it stops trying; 'daemon retry' releases the job.
const MaxDownloadAttempts = 5

// NextAttempt reports when the daemon may try a failed download again, and
// whether it has stopped trying because MaxDownloadAttempts attempts failed. A
// job that has not failed may be tried at any time: the zero time.
func (j *DownloadedJob) NextAttempt() (at time.Time, gaveUp bool) {
	if j == nil || j.Error == "" || j.RetryCount <= 0 {
		return time.Time{}, false
	}
	if j.RetryCount >= MaxDownloadAttempts {
		return time.Time{}, true
	}
	// Exponential backoff: 5min, 10min, 20min, 30min cap
	backoff := 5 * time.Minute * time.Duration(1<<(j.RetryCount-1))
	if backoff > 30*time.Minute {
		backoff = 30 * time.Minute
	}
	return j.LastAttempt.Add(backoff), false
}

// InRetryBackoff reports whether a failed job must not be tried at now: it is
// waiting out its backoff, or its attempts are used up. A job downloaded before
// is for the 'downloaded' tag to judge, so removing the tag still re-downloads.
func (s *State) InRetryBackoff(jobID string, now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	at, gaveUp := s.Downloaded[jobID].NextAttempt()
	return gaveUp || now.Before(at)
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

// MarkPendingTagApply sets the pending-tag-apply flag on an existing
// downloaded-job entry. Called when AddJobTag fails after a successful
// download. The poll loop's tag-retry pass will retry the tag call on
// subsequent polls; on success ClearPendingTagApply flips the flag off.
// If the job is not in the Downloaded map (e.g. state was lost), this is
// a no-op.
func (s *State) MarkPendingTagApply(jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.Downloaded[jobID]; ok {
		entry.PendingTagApply = true
	}
}

// ClearPendingTagApply clears the pending-tag-apply flag after a successful
// tag retry.
func (s *State) ClearPendingTagApply(jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.Downloaded[jobID]; ok {
		entry.PendingTagApply = false
	}
}

// PendingTagApplyJobs returns the IDs of downloaded jobs whose tag call
// has not yet succeeded. Order is not guaranteed.
func (s *State) PendingTagApplyJobs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var ids []string
	for id, entry := range s.Downloaded {
		if entry != nil && entry.PendingTagApply {
			ids = append(ids, id)
		}
	}
	return ids
}

// timeNow stamps failed attempts. A variable so a test can stop it, as Windows'
// coarse clock can.
var timeNow = time.Now

// MarkFailed records a job download failure.
// Preserves and increments retry count from any existing failure entry.
func (s *State) MarkFailed(jobID, jobName string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	retryCount := 0
	now := timeNow().Round(0) // the wall clock alone, which is what the file keeps
	if existing, ok := s.Downloaded[jobID]; ok && existing.Error != "" {
		retryCount = existing.RetryCount
		// Each failure keeps a stamp of its own, which releaseRetried matches a
		// release against. Windows' clock can return the same time twice.
		if !now.After(existing.LastAttempt) {
			now = existing.LastAttempt.Add(time.Nanosecond)
		}
	}

	s.Downloaded[jobID] = &DownloadedJob{
		JobID:        jobID,
		JobName:      jobName,
		DownloadedAt: now,
		Error:        reporting.RedactSecrets(err.Error()),
		RetryCount:   retryCount + 1,
		LastAttempt:  now,
	}
}

// AttemptCount returns how many times this job's download has already failed.
// Zero for a job that has never been attempted, or whose last attempt succeeded
// (a success replaces the failure entry). Add one for the attempt about to run.
func (s *State) AttemptCount(jobID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	job, exists := s.Downloaded[jobID]
	if !exists || job == nil || job.Error == "" {
		return 0
	}
	return job.RetryCount
}

// Retry marks failed downloads for retry: the jobs named, or every failed job
// when none is. It rereads the state file under its lock and changes only those
// jobs, so a running daemon's newer records in the file are kept. It returns the
// jobs it marked.
func (s *State) Retry(jobIDs ...string) ([]*DownloadedJob, error) {
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := s.load(true); err != nil {
		return nil, err
	}

	var marked []*DownloadedJob
	for _, job := range s.GetFailedJobs() {
		if len(jobIDs) == 0 || slices.Contains(jobIDs, job.JobID) {
			s.ClearFailed(job.JobID)
			marked = append(marked, job)
		}
	}
	if len(marked) == 0 {
		return nil, nil
	}
	return marked, s.write()
}

// ClearFailed marks a failed job for retry at the next poll: its attempts start
// again from zero. The entry stays, for releaseRetried to recognise.
func (s *State) ClearFailed(jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, exists := s.Downloaded[jobID]
	if exists && job.Error != "" {
		job.RetryCount = 0
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

// GetFailedCount returns the number of failed downloads not marked for retry.
func (s *State) GetFailedCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for _, job := range s.Downloaded {
		if job.Error != "" && job.RetryCount > 0 {
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

// GetFailedJobs returns the failed downloads not marked for retry.
func (s *State) GetFailedJobs() []*DownloadedJob {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var failed []*DownloadedJob
	for _, job := range s.Downloaded {
		if job.Error != "" && job.RetryCount > 0 {
			failed = append(failed, job)
		}
	}
	return failed
}

// DefaultStateFilePath returns the default path for the daemon state file.
// On Windows, uses %LOCALAPPDATA%\Rescale\Interlink\state\ (consistent with
// install/logs paths). On Unix, uses ~/.config/rescale/ (was rescale-int/
// pre-Plan-2; migrated by Load()).
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
	return filepath.Join(homeDir, ".config", "rescale", "daemon-state.json")
}

// oldStateFilePath returns the legacy state file path used prior to Plan 2
// (Unix-style under ~/.config/rescale-int/). Returns empty when it is
// identical to DefaultStateFilePath (meaning no migration is applicable).
func oldStateFilePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".config", "rescale-int", "daemon-state.json")
}

// migrateStateFile moves state file from old path to new path if needed.
// One-time migration from Unix-style path to Windows-native path.
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
