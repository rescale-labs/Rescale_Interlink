package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/upload"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/progress"
	"github.com/rescale/rescale-int/internal/resources"
)

// folders upload-dir, pipelined and --sequential, uploads a file that a link
// leads to outside the chosen folder, leaves out a link back into the folder's
// parent and a broken link, says so once per link with the reason, and counts
// them for the summary.
func TestUploadDir_ReportsLinksLeftOut(t *testing.T) {
	parent, elsewhere := t.TempDir(), t.TempDir()
	root := filepath.Join(parent, "tree")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"data.txt": root, "beside.txt": parent, "far.txt": elsewhere} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{"far.txt": filepath.Join(elsewhere, "far.txt"), "loop": "..", "broken": "missing"} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatalf("symlink %s: %v", link, err)
		}
	}

	origCheck, origUpload := checkFileExistsFn, uploadFileFn
	defer func() { checkFileExistsFn, uploadFileFn = origCheck, origUpload }()
	checkFileExistsFn = func(context.Context, *api.Client, *FolderCache, string, string) (string, bool, error) {
		return "", false, nil
	}
	var mu sync.Mutex
	var uploaded []string
	uploadFileFn = func(_ context.Context, p upload.UploadParams) (*models.CloudFile, error) {
		mu.Lock()
		defer mu.Unlock()
		uploaded = append(uploaded, filepath.Base(p.LocalPath))
		return &models.CloudFile{ID: "file-1"}, nil
	}
	ctx, cfg, mgr := context.Background(), &config.Config{}, resources.NewManager(resources.Config{MaxThreads: 1})

	// Each mode returns what it printed about links and its skipped count.
	for name, run := range map[string]func(*testing.T) (string, int){
		"pipelined": func(t *testing.T) (said string, skipped int) {
			said = captureStderr(t, func() {
				result, _, err := uploadDirectoryPipelined(ctx, nil, NewFolderCache(), root, "root-id", true, 1, 1, false, cfg, GetLogger(), mgr)
				if err != nil {
					t.Fatal(err)
				}
				skipped = result.SymlinksSkipped
			})
			return said, skipped
		},
		// The steps --sequential takes around creating the remote folders; the
		// command itself needs an allowlisted API URL.
		"sequential": func(t *testing.T) (string, int) {
			dirs, files, links, err := BuildDirectoryTree(root, true)
			if err != nil || len(dirs) != 0 {
				t.Fatalf("scan: dirs %v, err %v", dirs, err)
			}
			var said strings.Builder
			for _, link := range links {
				printSkippedLink(&said, root, link)
			}
			if _, err := uploadFiles(ctx, root, files, map[string]string{root: "root-id"}, nil, NewFolderCache(),
				progress.NewUploadUI(len(files)), NewFileConflictResolver(FileOverwriteOnce),
				NewErrorActionResolver(ErrorContinueOnce), false, 1, cfg, GetLogger(), mgr); err != nil {
				t.Fatal(err)
			}
			return said.String(), len(links)
		},
	} {
		t.Run(name, func(t *testing.T) {
			uploaded = nil
			said, skipped := run(t)
			if skipped != 2 {
				t.Errorf("links skipped = %d, want 2", skipped)
			}
			if slices.Sort(uploaded); !slices.Equal(uploaded, []string{"data.txt", "far.txt"}) {
				t.Errorf("uploaded %v, want data.txt and far.txt", uploaded)
			}
			loop := "it leads back into a folder that contains it"
			if runtime.GOOS == "windows" {
				loop = "links to folders are not followed on Windows"
			}
			for _, want := range []string{
				"Skipped link loop: " + loop + "\n",
				"Skipped link broken: its target is missing or cannot be read\n",
			} {
				if strings.Count(said, want) != 1 {
					t.Errorf("want %q once in:\n%s", want, said)
				}
			}
		})
	}
}
