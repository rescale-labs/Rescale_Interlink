package folder

// ConflictAction represents user choice for folder upload conflicts (remote folder exists).
// Moved from cli/prompt.go as part of v4.8.7 Plan 2b layering fix.
type ConflictAction int

const (
	ConflictSkipOnce ConflictAction = iota
	ConflictSkipAll
	ConflictMergeOnce
	ConflictMergeAll
	ConflictAbort
)

// ConflictPrompt resolves folder conflicts interactively.
// CLI: wraps promptFolderConflict. GUI: nil (always merges).
type ConflictPrompt func(folderName string) (ConflictAction, error)
