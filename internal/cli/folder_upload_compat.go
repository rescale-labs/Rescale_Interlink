package cli

// v4.8.7 Plan 2b: Compatibility layer preserving the old cli.* API surface.
// All folder-upload primitives now live in internal/transfer/folder/.
// These aliases and wrappers keep existing callers (cli/folders.go, services/file_service.go,
// wailsapp/file_bindings.go) compiling without changes during migration.

import (
	"context"
	"io"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/localfs"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/transfer/folder"
)

// Type aliases — callers can use cli.FolderReadyEvent etc. unchanged.
type FolderReadyEvent = folder.FolderReadyEvent
type FolderCache = folder.FolderCache
type ConflictAction = folder.ConflictAction

// Constant aliases
const (
	ConflictSkipOnce  = folder.ConflictSkipOnce
	ConflictSkipAll   = folder.ConflictSkipAll
	ConflictMergeOnce = folder.ConflictMergeOnce
	ConflictMergeAll  = folder.ConflictMergeAll
	ConflictAbort     = folder.ConflictAbort
)

// Function aliases — no wrapping needed for these.
var NewFolderCache = folder.NewFolderCache
var CheckFolderExists = folder.CheckFolderExists
var BuildDirectoryTree = folder.BuildDirectoryTree

// CreateFolderStructure wraps folder.CreateFolderStructure, threading
// promptFolderConflict as the ConflictPrompt callback.
func CreateFolderStructure(
	ctx context.Context,
	apiClient *api.Client,
	cache *FolderCache,
	rootPath string,
	directories []string,
	rootRemoteID string,
	folderConflictMode *ConflictAction,
	maxConcurrent int,
	logger *logging.Logger,
	folderReadyChan chan<- FolderReadyEvent,
	progressWriter io.Writer,
) (map[string]string, int, error) {
	return folder.CreateFolderStructure(
		ctx, apiClient, cache, rootPath, directories, rootRemoteID,
		folderConflictMode, maxConcurrent, logger, folderReadyChan, progressWriter,
		wrapPromptFolderConflict(),
	)
}

// CreateFolderStructureStreaming wraps folder.CreateFolderStructureStreaming,
// threading promptFolderConflict as the ConflictPrompt callback.
func CreateFolderStructureStreaming(
	ctx context.Context,
	apiClient *api.Client,
	cache *FolderCache,
	rootPath string,
	dirChan <-chan localfs.FileEntry,
	rootRemoteID string,
	folderConflictMode *ConflictAction,
	maxConcurrent int,
	logger *logging.Logger,
	folderReadyChan chan<- FolderReadyEvent,
	progressWriter io.Writer,
) (map[string]string, int, error) {
	return folder.CreateFolderStructureStreaming(
		ctx, apiClient, cache, rootPath, dirChan, rootRemoteID,
		folderConflictMode, maxConcurrent, logger, folderReadyChan, progressWriter,
		wrapPromptFolderConflict(),
	)
}

// wrapPromptFolderConflict returns a folder.ConflictPrompt that delegates
// to the CLI's interactive promptFolderConflict function.
func wrapPromptFolderConflict() folder.ConflictPrompt {
	return func(folderName string) (folder.ConflictAction, error) {
		return promptFolderConflict(folderName)
	}
}
