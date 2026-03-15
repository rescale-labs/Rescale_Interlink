package localfs

// ListOptions configures the behavior of ListDirectory.
type ListOptions struct {
	// IncludeHidden includes hidden files (starting with .) in results.
	// Default is false (hidden files excluded).
	IncludeHidden bool
}

// WalkOptions configures the behavior of Walk.
type WalkOptions struct {
	// IncludeHidden includes hidden files and directories in the walk.
	// Default is false (hidden items excluded).
	IncludeHidden bool

	// SkipHiddenDirs skips descending into hidden directories entirely.
	// Only meaningful when IncludeHidden is false.
	// Default is true (hidden directories are skipped).
	SkipHiddenDirs bool

	// FollowSymlinks follows symbolic links with cycle detection.
	// When true, symlinked directories and files are followed (resolved to their targets).
	// Cycle detection uses device+inode ancestry tracking on Unix.
	// On Windows, symlinks are NOT followed (getDirIdentity returns false).
	// Default is false (symlinks are skipped entirely, matching pre-v4.8.8 behavior).
	FollowSymlinks bool
}
