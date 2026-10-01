package localfs

// ListOptions configures the behavior of ListDirectory.
type ListOptions struct {
	// IncludeHidden includes hidden files (starting with .) in results.
	// Default is false (hidden files excluded).
	IncludeHidden bool
}

// WalkOptions configures WalkStream and WalkCollect.
type WalkOptions struct {
	// IncludeHidden includes hidden files and directories in the walk.
	// Default is false: hidden items are excluded, and a hidden directory is
	// not walked into.
	IncludeHidden bool
}
