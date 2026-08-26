package cli

// The folder-upload primitives live in internal/transfer/folder. This file
// gives the CLI package short names for the ones it uses, plus the one wrapper
// that threads the CLI's interactive conflict prompt into
// folder.CreateFolderStructure. Nothing outside internal/cli uses these names.

import (
	"context"
	"io"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/transfer/folder"
)

// Type aliases
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

// wrapPromptFolderConflict returns a folder.ConflictPrompt that delegates
// to the CLI's interactive promptFolderConflict function.
func wrapPromptFolderConflict() folder.ConflictPrompt {
	return func(folderName string) (folder.ConflictAction, error) {
		return promptFolderConflict(folderName)
	}
}
