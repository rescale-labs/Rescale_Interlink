package progress

import (
	"time"
)

// DownloadUI manages multiple concurrent download progress bars using mpb
type DownloadUI struct {
	*barGroup
}

// DownloadFileBar represents a single file download progress bar
type DownloadFileBar struct {
	barBase
	fileID     string
	remoteName string
	localPath  string
}

// NewDownloadUI creates a new download UI with the given number of total files
func NewDownloadUI(totalFiles int) *DownloadUI {
	return &DownloadUI{barGroup: newBarGroup(totalFiles)}
}

// AddFileBar creates a new progress bar for a file download
func (u *DownloadUI) AddFileBar(index int, fileID, remoteName, localPath string, size int64) *DownloadFileBar {
	fb := &DownloadFileBar{
		barBase: barBase{
			group:      u.barGroup,
			index:      index,
			size:       size,
			startTime:  time.Now(),
			lastUpdate: time.Now(),
		},
		fileID:     fileID,
		remoteName: remoteName,
		localPath:  localPath,
	}

	fb.start("Downloading", "←", localPath, remoteName)

	u.bars.Store(fileID, fb)
	return fb
}

// UpdateProgress updates the progress bar based on a fraction (0.0 to 1.0)
func (f *DownloadFileBar) UpdateProgress(fraction float64) {
	f.advance(fraction)
}

// Complete marks the download as finished and prints a summary
func (f *DownloadFileBar) Complete(err error) {
	f.finish("←", f.localPath, f.remoteName, f.fileID, err)
}
