// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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

	// Started holds the jobs this client has put its started tag on, keyed by
	// job ID, with when; with Client, that names the tag. A job is in flight
	// here, not downloaded, so it is kept apart from Downloaded and never
	// counted as a download. Persisted so that a tag an attempt left on, by
	// crashing or failing to take it off, still comes off later; pruned with
	// the finished entries.
	Started map[string]time.Time `json:"started,omitempty"`

	// Client names this client in its started tags. It is random, made once,
	// so it names no person or machine.
	Client string `json:"client_id,omitempty"`

	// HeldElsewhere is how many jobs the last poll left to other clients
	// downloading them.
	HeldElsewhere int `json:"held_elsewhere,omitempty"`

	// SkippedFolders, OutsideLookback and Unchecked are how many workspace
	// folders the last poll skipped, how many jobs it left out as older than
	// the lookback window, and how many it left for the next poll.
	SkippedFolders  int `json:"skipped_folders,omitempty"`
	OutsideLookback int `json:"outside_lookback,omitempty"`
	Unchecked       int `json:"unchecked,omitempty"`

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

// open is Load for the daemon, which rewrites the file: under the file's lock,
// having made its folder, it also moves a legacy state file in and sets a
// corrupt one aside.
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
		s.Started, s.Client = nil, ""
		s.HeldElsewhere, s.SkippedFolders, s.OutsideLookback, s.Unchecked = 0, 0, 0, 0
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
	for id, at := range s.Started {
		if at.Before(cutoff) {
			delete(s.Started, id)
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

// MarkStarted records that this client is putting its started tag, made at
// at, on the job, and returns the tag.
func (s *State) MarkStarted(jobID string, at time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Started == nil {
		s.Started = make(map[string]time.Time)
	}
	s.Started[jobID] = at
	return startedTag(s.clientID(), at)
}

// ClearStarted forgets the job's started tag once it has been taken off.
func (s *State) ClearStarted(jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Started, jobID)
}

// StartedTag returns the started tag this client put on the job, if it has
// one there.
func (s *State) StartedTag(jobID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.Started[jobID]
	if !ok {
		return "", false
	}
	return startedTag(s.clientID(), at), true
}

// ClientID returns the name this client's started tags carry.
func (s *State) ClientID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clientID()
}

// clientID is ClientID for a caller holding s.mu. The name is made the first
// time it is asked for.
func (s *State) clientID() string {
	if s.Client == "" {
		b := make([]byte, 4)
		_, _ = rand.Read(b) // never fails
		s.Client = hex.EncodeToString(b)
	}
	return s.Client
}

// StartedToRemove returns the jobs whose started tag this client is to take
// off: every one it has a tag on but those still owed their done tag, which
// the tag holds until then.
func (s *State) StartedToRemove() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var ids []string
	for id := range s.Started {
		if job := s.Downloaded[id]; job == nil || !job.PendingTagApply {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// RemoveStarted takes this client's started tags off the jobs named, or off
// every job when none is, with remove, stopping at the first that fails, and
// forgets each one taken off. For 'daemon retry' while no daemon runs: a
// running daemon takes its own off at every poll.
func (s *State) RemoveStarted(remove func(jobID, tag string) error, jobIDs ...string) ([]string, error) {
	if err := s.Load(); err != nil {
		return nil, err
	}
	var removed []string
	tags := map[string]string{}
	var err error
	for _, id := range s.StartedToRemove() {
		if len(jobIDs) > 0 && !slices.Contains(jobIDs, id) {
			continue
		}
		tag, _ := s.StartedTag(id)
		if err = remove(id, tag); err != nil {
			err = fmt.Errorf("could not take the started tag %s off job %s: %w", tag, id, err)
			break
		}
		removed, tags[id] = append(removed, id), tag
	}
	if len(removed) == 0 {
		return nil, err
	}
	// Forgotten in the file as it is now, under its lock, as Retry does: a
	// daemon may have started and written it meanwhile, even claimed a job anew.
	unlock, lockErr := s.lock()
	if lockErr != nil {
		return removed, errors.Join(err, lockErr)
	}
	defer unlock()
	now := NewState(s.filePath)
	if loadErr := now.load(true); loadErr != nil {
		return removed, errors.Join(err, loadErr)
	}
	for id, tag := range tags {
		if t, ok := now.StartedTag(id); ok && t == tag {
			now.ClearStarted(id)
		}
	}
	return removed, errors.Join(err, now.write())
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

// UpdateLastPoll records the last successful poll time, and from its summary
// what the poll left to other clients or left out.
func (s *State) UpdateLastPoll(sum *ScanSummary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastPoll = time.Now()
	s.HeldElsewhere = sum.SkipBuckets[ReasonHasStartedTag]
	s.SkippedFolders = sum.SkippedFolders
	s.OutsideLookback = sum.SkipBuckets[ReasonTooOldCreationPrefilter] + sum.SkipBuckets[ReasonOutsideLookbackWindow]
	s.Unchecked = sum.Unchecked
}

// GetLastPoll returns the last successful poll time.
func (s *State) GetLastPoll() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.LastPoll
}

// GetHeldElsewhere returns how many jobs the last poll left to other clients.
func (s *State) GetHeldElsewhere() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.HeldElsewhere
}

// GetLeftOut returns how many workspace folders the last poll skipped, how
// many jobs it left out as older than the lookback window, and how many it
// left for the next poll.
func (s *State) GetLeftOut() (folders, outsideLookback, unchecked int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.SkippedFolders, s.OutsideLookback, s.Unchecked
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

	slices.SortFunc(downloads, func(a, b *DownloadedJob) int { return b.DownloadedAt.Compare(a.DownloadedAt) }) // most recent first

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
// install/logs paths). On Unix, uses ~/.config/rescale/. A state file where
// earlier versions kept it, under ~/.config/rescale-int/, is moved in by open.
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

// oldStateFilePath returns where earlier versions kept the state file on every
// system, under ~/.config/rescale-int/, or "" when the home folder is unknown.
func oldStateFilePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".config", "rescale-int", "daemon-state.json")
}

// migrateStateFile moves the state file from oldPath, where earlier versions
// kept it, to newPath, unless newPath already holds one.
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
