package folder

import (
	"context"
	"io"
	"path/filepath"
	"sync"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/localfs"
	"github.com/rescale/rescale-int/internal/logging"
)

// ProgressSnapshot is a full point-in-time snapshot of discovery counters.
// Both OnFileDiscovered and OnFolderReady carry the full snapshot so the
// frontend can overwrite all counts atomically (it treats each enumeration
// event as authoritative, see transferStore.ts:676).
type ProgressSnapshot struct {
	TotalDirs  int
	TotalFiles int
	TotalBytes int64
}

// OrchestratorCallbacks[T any] — caller-provided hooks.
// T is the backlog item type (services.TransferRequest for GUI,
// cliPipelinedUploadItem for CLI). No WorkItem constraint needed —
// the orchestrator just collects and forwards items.
type OrchestratorCallbacks[T any] struct {
	// OnFileDiscovered: called each time a file is found by the walk.
	// Carries FULL snapshot (dirs, files, bytes) — not just the file count.
	// GUI: UpdateBatchDiscovered + emit enumeration events with all counters.
	// CLI: uploadUI.IncrementTotal().
	OnFileDiscovered func(snap ProgressSnapshot)

	// OnFolderReady: called each time a folder is created/merged.
	// Carries FULL snapshot so GUI can emit events with all counters.
	// GUI: emit enumeration progress every 3 folders.
	// CLI: nil (no-op).
	OnFolderReady func(snap ProgressSnapshot, localPath, remoteID string)

	// BuildItem: constructs a backlog item from a discovered file
	// and its parent folder's remote ID.
	BuildItem func(file localfs.FileEntry, remoteFolderID, rootPath string) T

	// OnUnmappedFiles: called at shutdown for files whose parent was never created.
	OnUnmappedFiles func(parentDir string, count int)

	// OnOrchestratorDone: called when Part C (orchestrator) exits,
	// BEFORE Part B (dispatcher) finishes draining.
	// GUI: MarkBatchScanInProgress(false) + emitCompletion.
	// CLI: nil (waits synchronously via <-dispatchDone).
	OnOrchestratorDone func(result *OrchestratorResult)
}

// OrchestratorConfig holds the configuration for RunOrchestrator.
type OrchestratorConfig struct {
	RootPath          string
	RootRemoteID      string
	IncludeHidden     bool
	FolderConcurrency int
	ConflictMode      ConflictAction
	ConflictPrompt    ConflictPrompt   // nil = no interactive prompting (GUI path)
	Logger            *logging.Logger
	APIClient         *api.Client
	Cache             *FolderCache     // caller creates, orchestrator uses
	ProgressWriter    io.Writer        // nil for GUI, uploadUI.Writer() for CLI
}

// OrchestratorResult holds the final counters from the orchestration pipeline.
// Sole owner: Part C goroutine. Populated after both fileChan and folderReadyChan close.
type OrchestratorResult struct {
	FoldersCreated  int
	DiscoveredFiles int
	DiscoveredBytes int64
	DiscoveredDirs  int
	WalkError       error
	FolderError     error
}

// folderResultMsg communicates Part A's result to Part C via a channel.
// This avoids shared mutable state between goroutines.
type folderResultMsg struct {
	created int
	err     error
}

// RunOrchestrator runs the three-part streaming pipeline for folder uploads.
//
// Channel ownership contract:
//   - Part A: sole owner of folderReadyChan (closes it on exit)
//   - Part B (dispatcher): sole owner of outputCh (closes it on exit)
//   - Part C (orchestrator): sole owner of backlogDone (closes it on exit)
//     and sole owner of OrchestratorResult (no mutex needed)
//
// Returns immediately. All work runs in goroutines.
// dispatchDone closes when dispatcher has sent all items to outputCh
// and closed it. result is populated asynchronously by Part C.
func RunOrchestrator[T any](
	ctx context.Context,
	cfg OrchestratorConfig,
	callbacks OrchestratorCallbacks[T],
	outputCh chan<- T,
) (dispatchDone <-chan struct{}, result *OrchestratorResult) {
	orchResult := &OrchestratorResult{}

	// Start streaming walk — directories and files arrive as they're discovered.
	dirChan, fileChan, walkErrChan := localfs.WalkStream(ctx, cfg.RootPath, localfs.WalkOptions{
		IncludeHidden:  cfg.IncludeHidden,
		SkipHiddenDirs: true,
		FollowSymlinks: true, // v4.8.8: Follow symlinks with cycle detection
	})

	// Create folder ready channel (buffered to prevent blocking)
	folderReadyChan := make(chan FolderReadyEvent, constants.WorkChannelBuffer)

	// Part A result communicated to Part C via channel (no shared state).
	folderResultCh := make(chan folderResultMsg, 1)

	// === Part A: Folder creation goroutine ===
	// Uses CreateFolderStructureStreaming to process directories as they arrive from WalkStream.
	// Sole owner of folderReadyChan (closes it via defer).
	conflictMode := cfg.ConflictMode
	go func() {
		defer close(folderReadyChan)
		_, created, err := CreateFolderStructureStreaming(
			ctx, cfg.APIClient, cfg.Cache, cfg.RootPath, dirChan, cfg.RootRemoteID,
			&conflictMode, cfg.FolderConcurrency, cfg.Logger,
			folderReadyChan, cfg.ProgressWriter, cfg.ConflictPrompt,
		)
		folderResultCh <- folderResultMsg{created: created, err: err}
		close(folderResultCh)
		if cfg.Logger != nil {
			cfg.Logger.Info().Int("folders", created).Msg("Folder creation complete")
		}
	}()

	// === Part B: Unbounded backlog + dispatcher ===
	// Decouples discovery speed from outputCh consumption rate.
	// Sole owner of outputCh (closes it via defer).
	var readyBacklog []T
	var backlogMu sync.Mutex
	backlogReady := make(chan struct{}, 1)
	backlogDone := make(chan struct{}) // closed by Part C

	appendToBacklog := func(item T) {
		backlogMu.Lock()
		readyBacklog = append(readyBacklog, item)
		backlogMu.Unlock()
		select {
		case backlogReady <- struct{}{}:
		default:
		}
	}

	done := make(chan struct{})

	// Dispatcher goroutine: drains backlog into outputCh, then closes outputCh.
	go func() {
		defer close(done)
		defer close(outputCh)
		for {
			backlogMu.Lock()
			if len(readyBacklog) == 0 {
				backlogMu.Unlock()
				select {
				case <-backlogReady:
					continue
				case <-backlogDone:
					// Producer done — drain any remaining items
					backlogMu.Lock()
					remaining := readyBacklog
					readyBacklog = nil
					backlogMu.Unlock()
					for _, item := range remaining {
						select {
						case outputCh <- item:
						case <-ctx.Done():
							return
						}
					}
					return
				case <-ctx.Done():
					return
				}
			}
			item := readyBacklog[0]
			readyBacklog = readyBacklog[1:]
			backlogMu.Unlock()

			select {
			case outputCh <- item:
			case <-ctx.Done():
				return
			}
		}
	}()

	// === Part C: Orchestrator ===
	// Merges fileChan + folderReadyChan, appends to backlog via appendToBacklog.
	// Sole owner of backlogDone (closes it on exit).
	// Sole owner of OrchestratorResult (populated after select loop exits).
	go func() {
		var backlogDoneClosed bool
		defer func() {
			if !backlogDoneClosed {
				close(backlogDone)
			}
		}()

		// Folder mapping: populated by folderReadyChan events
		folderMapping := make(map[string]string) // localDirPath → remoteID
		folderMapping[cfg.RootPath] = cfg.RootRemoteID

		// Pending files: buffered until parent folder is ready
		pendingFiles := make(map[string][]localfs.FileEntry) // parentDir → files

		// Discovery counters (Part C is sole owner — no mutex needed)
		var discoveredFiles int
		var discoveredBytes int64
		var discoveredDirs int

		fileChClosed := false
		folderChClosed := false

		for !fileChClosed || !folderChClosed {
			select {
			case file, ok := <-fileChan:
				if !ok {
					fileChClosed = true
					continue
				}
				discoveredFiles++
				discoveredBytes += file.Size

				if callbacks.OnFileDiscovered != nil {
					callbacks.OnFileDiscovered(ProgressSnapshot{
						TotalDirs:  discoveredDirs,
						TotalFiles: discoveredFiles,
						TotalBytes: discoveredBytes,
					})
				}

				parentDir := filepath.Dir(file.Path)
				if remoteID, ready := folderMapping[parentDir]; ready {
					// Parent folder ready — build item and append to backlog
					if callbacks.BuildItem != nil {
						item := callbacks.BuildItem(file, remoteID, cfg.RootPath)
						appendToBacklog(item)
					}
				} else {
					// Parent not ready — buffer
					pendingFiles[parentDir] = append(pendingFiles[parentDir], file)
				}

			case event, ok := <-folderReadyChan:
				if !ok {
					folderChClosed = true
					continue
				}
				discoveredDirs++
				folderMapping[event.LocalPath] = event.RemoteID

				if callbacks.OnFolderReady != nil {
					callbacks.OnFolderReady(ProgressSnapshot{
						TotalDirs:  discoveredDirs,
						TotalFiles: discoveredFiles,
						TotalBytes: discoveredBytes,
					}, event.LocalPath, event.RemoteID)
				}

				// Flush any pending files for this folder
				if pending, has := pendingFiles[event.LocalPath]; has {
					if callbacks.BuildItem != nil {
						for _, file := range pending {
							item := callbacks.BuildItem(file, event.RemoteID, cfg.RootPath)
							appendToBacklog(item)
						}
					}
					delete(pendingFiles, event.LocalPath)
				}

			case <-ctx.Done():
				close(backlogDone)
				backlogDoneClosed = true
				if callbacks.OnOrchestratorDone != nil {
					callbacks.OnOrchestratorDone(orchResult)
				}
				return
			}
		}

		// Check for walk errors
		select {
		case walkErr := <-walkErrChan:
			if walkErr != nil && ctx.Err() == nil {
				orchResult.WalkError = walkErr
			}
		default:
		}

		// Read Part A result from channel (Part A has already sent and closed folderResultCh
		// because folderReadyChan is closed, which means Part A exited).
		if msg, ok := <-folderResultCh; ok {
			orchResult.FoldersCreated = msg.created
			if msg.err != nil && ctx.Err() == nil {
				orchResult.FolderError = msg.err
			}
		}

		// Warn about unmapped pending files
		if callbacks.OnUnmappedFiles != nil {
			for parentDir, pending := range pendingFiles {
				callbacks.OnUnmappedFiles(parentDir, len(pending))
			}
		}

		// Populate final counters
		orchResult.DiscoveredFiles = discoveredFiles
		orchResult.DiscoveredBytes = discoveredBytes
		orchResult.DiscoveredDirs = discoveredDirs

		// Signal dispatcher to drain remaining items and close outputCh
		close(backlogDone)
		backlogDoneClosed = true

		// Call OnOrchestratorDone AFTER closing backlogDone but BEFORE
		// dispatcher finishes draining. This preserves the v4.8.5 bugfix
		// timing: GUI's MarkBatchScanInProgress(false) fires when discovery
		// count is final, not after all items are sent through outputCh.
		if callbacks.OnOrchestratorDone != nil {
			callbacks.OnOrchestratorDone(orchResult)
		}
	}()

	return done, orchResult
}
