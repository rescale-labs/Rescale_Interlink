package progress

import (
	"sync"
	"sync/atomic"
	"time"
)

// UploadUI manages multiple concurrent upload progress bars using mpb
type UploadUI struct {
	*barGroup
	pathCache sync.Map // folderID -> human path
	started   int32    // Atomic counter for file index (1, 2, 3, ...)
	completed int32
}

// FileBar represents a single file upload progress bar
type FileBar struct {
	barBase
	ui         *UploadUI
	filepath   string
	folderPath string
	folderID   string
}

// NewUploadUI creates a new upload UI with the given number of total files
func NewUploadUI(totalFiles int) *UploadUI {
	return &UploadUI{barGroup: newBarGroup(totalFiles)}
}

// IncrementTotal atomically increments the total file count.
// Used by streaming uploads where the total is unknown at start and grows as files are discovered.
func (u *UploadUI) IncrementTotal() {
	atomic.AddInt32(&u.totalFiles, 1)
}

// SetFolderPath caches a human-readable path for a folder ID
func (u *UploadUI) SetFolderPath(folderID, path string) {
	u.pathCache.Store(folderID, path)
}

// GetFolderPath retrieves the cached human-readable path for a folder ID
func (u *UploadUI) GetFolderPath(folderID string) string {
	if path, ok := u.pathCache.Load(folderID); ok {
		return path.(string)
	}
	return folderID // fallback to ID if no path cached
}

// AddFileBar creates a new progress bar for a file upload
func (u *UploadUI) AddFileBar(localPath, folderID string, size int64) *FileBar {
	folderPath := u.GetFolderPath(folderID)

	// Atomic increment to get unique file index across all concurrent uploads
	index := int(atomic.AddInt32(&u.started, 1))

	fb := &FileBar{
		barBase: barBase{
			group:      u.barGroup,
			index:      index,
			size:       size,
			startTime:  time.Now(),
			lastUpdate: time.Now(),
		},
		ui:         u,
		filepath:   localPath,
		folderPath: folderPath,
		folderID:   folderID,
	}

	fb.start("Uploading", "→", localPath, folderPath)

	u.bars.Store(localPath, fb)
	return fb
}

// UpdateProgress updates the progress bar based on a fraction (0.0 to 1.0)
func (f *FileBar) UpdateProgress(fraction float64) {
	// Special value -1.0 means "reset start time" (used to exclude encryption time)
	if fraction < 0 {
		f.ResetStartTime()
		return
	}

	f.advance(fraction)
}

// Complete marks the upload as finished and prints a summary
func (f *FileBar) Complete(fileID string, err error) {
	f.finish("→", f.filepath, f.folderPath, fileID, err)

	atomic.AddInt32(&f.ui.completed, 1)
}
