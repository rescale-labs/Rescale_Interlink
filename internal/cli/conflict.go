// Shared conflict resolution state machine.
//
// Replaces inline mutex+prompt+update patterns across download_helper.go,
// folder_download_helper.go, and folder_upload_helper.go with a single generic
// implementation.
package cli

import "sync"

// ConflictResolver manages concurrent conflict resolution with a shared mode
// that can escalate from "once" actions (prompt each time) to "all" actions
// (apply automatically). Thread-safe.
//
// Type parameter A is the action enum type (e.g., DownloadConflictAction,
// FileConflictAction, FolderDownloadConflictAction).
type ConflictResolver[A comparable] struct {
	mu      sync.Mutex
	mode    A
	isOnce  func(A) bool   // returns true for actions that require prompting
	isAll   func(A) bool   // returns true for "apply to all" actions
	prompt  func() (A, error) // prompts user; caller sets up closure with context
}

// NewConflictResolver creates a resolver with the given initial mode.
//
//   - isOnce: returns true for "Once" actions that need user prompting.
//     When the current mode is a "Once" action, Resolve() calls the prompt.
//   - isAll: returns true for "All" actions that should be remembered.
//     When the user picks an "All" action, the resolver stores it as the new mode
//     so future calls skip prompting.
func NewConflictResolver[A comparable](
	initialMode A,
	isOnce func(A) bool,
	isAll func(A) bool,
) *ConflictResolver[A] {
	return &ConflictResolver[A]{
		mode:   initialMode,
		isOnce: isOnce,
		isAll:  isAll,
	}
}

// Resolve returns the action to take for a conflict.
//
// If the current mode is an "All" action, it returns immediately without prompting.
// If the current mode is a "Once" action, it calls promptFn (holding the mutex to
// serialize prompts), and if the user selects an "All" action, updates the mode.
//
// promptFn is passed per-call (not stored) because each call site needs different
// context (file name, path, etc.) for the prompt message.
func (cr *ConflictResolver[A]) Resolve(promptFn func() (A, error)) (A, error) {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	// If mode is already an "All" action, return it without prompting.
	if !cr.isOnce(cr.mode) {
		return cr.mode, nil
	}

	// Mode is a "Once" action — prompt the user.
	action, err := promptFn()
	if err != nil {
		return action, err
	}

	// If user selected an "All" action, escalate the mode.
	if cr.isAll(action) {
		cr.mode = action
	}

	return action, nil
}

// Mode returns the current conflict mode (thread-safe).
func (cr *ConflictResolver[A]) Mode() A {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	return cr.mode
}

// SetMode updates the conflict mode (thread-safe).
// Useful when a folder conflict decision cascades to file conflict mode.
func (cr *ConflictResolver[A]) SetMode(mode A) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	cr.mode = mode
}

// --- Convenience constructors for each conflict type ---

// NewDownloadConflictResolver creates a resolver for download file conflicts.
func NewDownloadConflictResolver(initialMode DownloadConflictAction) *ConflictResolver[DownloadConflictAction] {
	return NewConflictResolver(
		initialMode,
		func(a DownloadConflictAction) bool {
			return a == DownloadSkipOnce || a == DownloadOverwriteOnce || a == DownloadResumeOnce
		},
		func(a DownloadConflictAction) bool {
			return a == DownloadSkipAll || a == DownloadOverwriteAll || a == DownloadResumeAll
		},
	)
}

// NewFolderDownloadConflictResolver creates a resolver for folder download conflicts.
func NewFolderDownloadConflictResolver(initialMode FolderDownloadConflictAction) *ConflictResolver[FolderDownloadConflictAction] {
	return NewConflictResolver(
		initialMode,
		func(a FolderDownloadConflictAction) bool {
			return a == FolderDownloadSkipOnce || a == FolderDownloadMergeOnce
		},
		func(a FolderDownloadConflictAction) bool {
			return a == FolderDownloadSkipAll || a == FolderDownloadMergeAll
		},
	)
}

// NewFileConflictResolver creates a resolver for file upload conflicts.
func NewFileConflictResolver(initialMode FileConflictAction) *ConflictResolver[FileConflictAction] {
	return NewConflictResolver(
		initialMode,
		func(a FileConflictAction) bool {
			return a == FileSkipOnce || a == FileOverwriteOnce
		},
		func(a FileConflictAction) bool {
			return a == FileSkipAll || a == FileOverwriteAll
		},
	)
}

// NewErrorActionResolver creates a resolver for error handling prompts.
func NewErrorActionResolver(initialMode ErrorAction) *ConflictResolver[ErrorAction] {
	return NewConflictResolver(
		initialMode,
		func(a ErrorAction) bool {
			return a == ErrorContinueOnce
		},
		func(a ErrorAction) bool {
			return a == ErrorContinueAll
		},
	)
}
