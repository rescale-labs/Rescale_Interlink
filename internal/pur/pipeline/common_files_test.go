package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/filescan"
)

// recordingUploader stands in for the transfer service, keeping each upload it
// is asked for and answering with an ID made from the file's name.
type recordingUploader struct {
	mu      sync.Mutex
	uploads []string
}

func (u *recordingUploader) UploadFileSync(_ context.Context, params SyncUploadParams) (*models.CloudFile, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.uploads = append(u.uploads, params.LocalPath)
	return &models.CloudFile{ID: "up-" + params.Name}, nil
}

// A folder among the common input files stands for the files under it: each
// is uploaded once, under its own name, and attached to every job beside the
// id: references, decompressed as --decompress-common says. The folder itself
// used to reach the upload, which refused it once the run had started.
func TestCommonInputFolderAttachesEachFileToEveryJob(t *testing.T) {
	root := namespaceTestRoot(t)
	deck, lib := filepath.Join(root, "deck.inp"), filepath.Join(root, "lib")
	a, b := filepath.Join(lib, "a.dat"), filepath.Join(lib, "sub", "b.dat")
	for _, f := range []string{deck, a, b} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	common, err := filescan.CommonFiles(deck + ",id:shared-1," + lib + "," + a)
	if err != nil {
		t.Fatal(err)
	}

	jobs := []models.JobSpec{{JobName: "sweep_1"}, {JobName: "sweep_2"}}
	p := newPipelineWith(t, jobs, PipelineOptions{CommonInputFiles: common, DecompressCommon: true})
	uploader := &recordingUploader{}
	p.SetSyncUploader(uploader)
	if err := p.ResolveSharedFiles(context.Background()); err != nil {
		t.Fatalf("ResolveSharedFiles: %v", err)
	}

	if want := []string{deck, a, b}; !reflect.DeepEqual(uploader.uploads, want) {
		t.Errorf("uploaded %q, want each file once: %q", uploader.uploads, want)
	}
	want := []models.InputFileRequest{
		{ID: "up-deck.inp", Decompress: true}, {ID: "shared-1", Decompress: true},
		{ID: "up-a.dat", Decompress: true}, {ID: "up-b.dat", Decompress: true},
	}
	for _, job := range p.jobs {
		req, err := BuildJobRequest(job, nil, p.sharedFileIDs, p.decompressCommon)
		if err != nil {
			t.Fatalf("BuildJobRequest(%s): %v", job.JobName, err)
		}
		if got := req.JobAnalyses[0].InputFiles; !reflect.DeepEqual(got, want) {
			t.Errorf("%s carries %+v, want %+v", job.JobName, got, want)
		}
	}
}
