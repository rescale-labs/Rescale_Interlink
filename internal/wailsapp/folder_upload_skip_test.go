package wailsapp

import (
	"path/filepath"
	"testing"

	"github.com/rescale/rescale-int/internal/localfs"
)

// A folder upload in the app says why it left a link out, in the walk's own
// words, as the CLI does: most skipped links are no reparse point.
func TestSkippedLinkMessageSaysWhy(t *testing.T) {
	entry := localfs.FileEntry{
		Path: filepath.Join("tree", "loop"), Name: "loop", IsSymlink: true, IsDir: true,
		SkipReason: "it leads back into a folder that contains it",
	}
	want := "Skipped link " + entry.Path + ": it leads back into a folder that contains it"
	if got := skippedLinkMessage(entry); got != want {
		t.Errorf("skip message = %q, want %q", got, want)
	}
}
