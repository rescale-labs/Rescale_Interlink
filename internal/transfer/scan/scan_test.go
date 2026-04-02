package scan

import (
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

func TestRemoteFolderInfo_Fields(t *testing.T) {
	info := RemoteFolderInfo{
		FolderID:     "folder-123",
		Name:         "my-folder",
		RelativePath: "parent/my-folder",
	}
	if info.FolderID != "folder-123" {
		t.Errorf("FolderID = %q, want %q", info.FolderID, "folder-123")
	}
	if info.Name != "my-folder" {
		t.Errorf("Name = %q, want %q", info.Name, "my-folder")
	}
	if info.RelativePath != "parent/my-folder" {
		t.Errorf("RelativePath = %q, want %q", info.RelativePath, "parent/my-folder")
	}
}

func TestRemoteFileTask_Fields(t *testing.T) {
	cf := &models.CloudFile{ID: "cloud-1", Name: "test.dat"}
	task := RemoteFileTask{
		FileID:       "file-456",
		Name:         "test.dat",
		RelativePath: "subdir/test.dat",
		Size:         1024,
		CloudFile:    cf,
	}
	if task.FileID != "file-456" {
		t.Errorf("FileID = %q, want %q", task.FileID, "file-456")
	}
	if task.Size != 1024 {
		t.Errorf("Size = %d, want %d", task.Size, 1024)
	}
	if task.CloudFile == nil || task.CloudFile.ID != "cloud-1" {
		t.Error("CloudFile not set correctly")
	}
}

func TestRemoteFileTask_NilCloudFile(t *testing.T) {
	task := RemoteFileTask{
		FileID: "file-789",
		Name:   "no-cloud.txt",
		Size:   0,
	}
	if task.CloudFile != nil {
		t.Error("expected nil CloudFile")
	}
}

func TestScanEvent_FolderDiscovery(t *testing.T) {
	info := &RemoteFolderInfo{FolderID: "f1", Name: "docs"}
	event := ScanEvent{Folder: info}

	if event.Folder == nil {
		t.Fatal("expected non-nil Folder")
	}
	if event.File != nil {
		t.Error("expected nil File for folder discovery event")
	}
	if event.Folder.FolderID != "f1" {
		t.Errorf("Folder.FolderID = %q, want %q", event.Folder.FolderID, "f1")
	}
}

func TestScanEvent_FileDiscovery(t *testing.T) {
	task := &RemoteFileTask{FileID: "f2", Name: "data.csv", Size: 2048}
	event := ScanEvent{File: task}

	if event.File == nil {
		t.Fatal("expected non-nil File")
	}
	if event.Folder != nil {
		t.Error("expected nil Folder for file discovery event")
	}
	if event.File.Size != 2048 {
		t.Errorf("File.Size = %d, want %d", event.File.Size, 2048)
	}
}

func TestScanProgress_ZeroValue(t *testing.T) {
	var p ScanProgress
	if p.FoldersFound != 0 || p.FilesFound != 0 || p.BytesFound != 0 {
		t.Error("zero-value ScanProgress should have all zero fields")
	}
}

func TestScanProgress_Accumulation(t *testing.T) {
	p := ScanProgress{FoldersFound: 3, FilesFound: 10, BytesFound: 1048576}
	if p.FoldersFound != 3 {
		t.Errorf("FoldersFound = %d, want 3", p.FoldersFound)
	}
	if p.FilesFound != 10 {
		t.Errorf("FilesFound = %d, want 10", p.FilesFound)
	}
	if p.BytesFound != 1048576 {
		t.Errorf("BytesFound = %d, want 1048576", p.BytesFound)
	}
}
