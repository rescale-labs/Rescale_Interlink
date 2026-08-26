package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// libraryRoot is the myLibrary ID the fake account reports.
const libraryRoot = "lib-root"

// fakeLibrary is an httptest stand-in for the Rescale API that counts requests
// per endpoint class. The folder-listing count is the point of these tests: the
// trash-delete path resolves a file's parent by walking the library tree, so a
// file ID that does not exist must be rejected before any of that walk happens.
type fakeLibrary struct {
	t *testing.T

	// folders maps a folder ID to its subfolder IDs and files maps a file ID to
	// the folder holding it. Together they are the tree the parent-folder BFS
	// walks; a file absent from files is absent from the account.
	folders map[string][]string
	files   map[string]string

	mu             sync.Mutex
	getFileInfo    map[string]int
	deleteFile     map[string]int
	rootFolders    int
	folderListings int
	archived       []string // "<parentFolderID>:<comma-joined file IDs>"
}

// newFakeLibrary builds a three-level library: the root holds sub1 and sub2,
// sub2 holds sub3. Nesting keeps the parent lookup a real multi-call walk, so a
// test that expects zero listings is pinning something that would otherwise be
// plainly visible.
func newFakeLibrary(t *testing.T) *fakeLibrary {
	t.Helper()
	return &fakeLibrary{
		t: t,
		folders: map[string][]string{
			libraryRoot: {"sub1", "sub2"},
			"sub1":      nil,
			"sub2":      {"sub3"},
			"sub3":      nil,
		},
		files: map[string]string{
			"f1": "sub3",
			"f2": "sub1",
		},
		getFileInfo: map[string]int{},
		deleteFile:  map[string]int{},
	}
}

func (f *fakeLibrary) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	path := r.URL.Path
	switch {
	case path == "/api/v3/users/me/folders/":
		f.rootFolders++
		writeFakeJSON(w, map[string]string{"myJobs": "jobs-root", "myLibrary": libraryRoot})

	case strings.HasPrefix(path, "/api/v3/files/"):
		fileID := strings.Trim(strings.TrimPrefix(path, "/api/v3/files/"), "/")
		if r.Method == http.MethodDelete {
			f.deleteFile[fileID]++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		f.getFileInfo[fileID]++
		if _, ok := f.files[fileID]; !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"detail":"Not found."}`)
			return
		}
		writeFakeJSON(w, models.CloudFile{ID: fileID, Name: fileID + ".dat"})

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/contents/archive/"):
		folderID := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v3/folders/"), "/contents/archive/")
		var body struct {
			FileIDs []string `json:"fileIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("archive body decode: %v", err)
		}
		f.archived = append(f.archived, folderID+":"+strings.Join(body.FileIDs, ","))
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodGet && strings.HasSuffix(path, "/contents/"):
		folderID := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v3/folders/"), "/contents/")
		f.folderListings++
		writeFakeJSON(w, f.contentsOf(folderID))

	default:
		f.t.Errorf("unexpected request: %s %s", r.Method, path)
		w.WriteHeader(http.StatusNotFound)
	}
}

// contentsOf renders one folder in the API's folder-contents shape.
func (f *fakeLibrary) contentsOf(folderID string) map[string]interface{} {
	results := []map[string]interface{}{}
	for _, sub := range f.folders[folderID] {
		results = append(results, map[string]interface{}{
			"type": "folder",
			"item": map[string]interface{}{"id": sub, "name": sub},
		})
	}
	for fileID, parent := range f.files {
		if parent == folderID {
			results = append(results, map[string]interface{}{
				"type": "file",
				"item": map[string]interface{}{"id": fileID, "name": fileID + ".dat"},
			})
		}
	}
	return map[string]interface{}{"results": results}
}

func writeFakeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// useFakeLibrary points the CLI's API client at the fake for one test.
func useFakeLibrary(t *testing.T, lib *fakeLibrary) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(lib.handler))
	t.Cleanup(server.Close)

	orig := getAPIClientFn
	getAPIClientFn = func() (*api.Client, error) {
		return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), nil
	}
	t.Cleanup(func() { getAPIClientFn = orig })
}

// runFilesDelete executes the real 'files delete' command, capturing the
// progress output it writes straight to stdout.
func runFilesDelete(t *testing.T, args ...string) (string, error) {
	t.Helper()

	cmd := newFilesDeleteCmd()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true

	// Bind the shared CLI logger to the real stdout before the swap below. It
	// captures os.Stdout once, at construction, and a logger left holding a
	// closed test pipe fails every later write in the package.
	GetLogger()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w

	runErr := cmd.Execute()

	os.Stdout = orig
	_ = w.Close()
	out, _ := io.ReadAll(r)
	_ = r.Close()

	return string(out), runErr
}

// TestFilesDelete_MissingIDFailsBeforeParentHunt covers the trash path taking
// minutes and hundreds of API calls on a mistyped ID: the parent-folder lookup
// walks the whole library, because a file record carries no parent ID.
func TestFilesDelete_MissingIDFailsBeforeParentHunt(t *testing.T) {
	lib := newFakeLibrary(t)
	useFakeLibrary(t, lib)

	_, err := runFilesDelete(t, "--fileid", "nonexist9", "--confirm")
	if err == nil {
		t.Fatal("files delete accepted a file ID that does not exist")
	}

	// Same shape as the download path's failure on a bad ID.
	want := "failed to get file info for nonexist9: get file info failed: status 404:"
	if !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want prefix %q", err.Error(), want)
	}
	if !strings.Contains(err.Error(), "Not found.") {
		t.Errorf("error should carry the server's 404 body, got %q", err.Error())
	}

	if lib.getFileInfo["nonexist9"] != 1 {
		t.Errorf("GetFileInfo calls = %d, want exactly 1", lib.getFileInfo["nonexist9"])
	}
	// The regression itself: no part of the library walk may run.
	if lib.rootFolders != 0 {
		t.Errorf("library root resolved %d times, want 0", lib.rootFolders)
	}
	if lib.folderListings != 0 {
		t.Errorf("folder listings = %d, want 0 (the parent hunt must not start)", lib.folderListings)
	}
	if len(lib.archived) != 0 {
		t.Errorf("archived = %v, want none", lib.archived)
	}
}

// TestFilesDelete_ExistingIDTrashesUnchanged pins the success path: the
// pre-check passes and the parent lookup and archive run exactly as before.
func TestFilesDelete_ExistingIDTrashesUnchanged(t *testing.T) {
	lib := newFakeLibrary(t)
	useFakeLibrary(t, lib)

	out, err := runFilesDelete(t, "--fileid", "f1", "--confirm")
	if err != nil {
		t.Fatalf("files delete --fileid f1: %v", err)
	}

	if lib.getFileInfo["f1"] != 1 {
		t.Errorf("GetFileInfo calls = %d, want exactly 1", lib.getFileInfo["f1"])
	}
	if lib.rootFolders != 1 {
		t.Errorf("library root resolved %d times, want 1", lib.rootFolders)
	}
	// The parent hunt has to walk the library to find f1; the exact number of
	// listings is the fake's tree shape, not a contract of this command. The
	// upper bound guards against redundant re-listing of the same folders.
	if lib.folderListings < 1 || lib.folderListings > 4 {
		t.Errorf("folder listings = %d, want 1-4 (parent hunt runs, no redundant re-listing)", lib.folderListings)
	}
	if len(lib.archived) != 1 || lib.archived[0] != "sub3:f1" {
		t.Errorf("archived = %v, want [sub3:f1]", lib.archived)
	}
	if len(lib.deleteFile) != 0 {
		t.Errorf("trash path must not permanently delete, got %v", lib.deleteFile)
	}

	for _, want := range []string{
		"[1/1] Moving file f1 to Trash...",
		"✓ Moved to Trash",
		"✓ Moved 1 file(s) to Trash",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q, got:\n%s", want, out)
		}
	}
}

// TestFilesDelete_StopsOnFirstError pins the loop's existing semantics: the
// first failure ends the command, leaving later IDs untouched.
func TestFilesDelete_StopsOnFirstError(t *testing.T) {
	lib := newFakeLibrary(t)
	useFakeLibrary(t, lib)

	out, err := runFilesDelete(t, "--fileid", "f1", "--fileid", "nonexist9", "--fileid", "f2", "--confirm")
	if err == nil {
		t.Fatal("files delete should fail when one of the IDs does not exist")
	}
	if !strings.Contains(err.Error(), "nonexist9") {
		t.Errorf("error should name the failing ID, got %q", err.Error())
	}

	if len(lib.archived) != 1 || lib.archived[0] != "sub3:f1" {
		t.Errorf("archived = %v, want only the ID processed before the failure", lib.archived)
	}
	if lib.getFileInfo["f2"] != 0 {
		t.Errorf("f2 was looked up %d times, want 0 (the loop stops at the first error)", lib.getFileInfo["f2"])
	}
	if strings.Contains(out, "Moving file f2 to Trash") {
		t.Errorf("output should stop before the third ID, got:\n%s", out)
	}
	if strings.Contains(out, "✓ Moved 3 file(s) to Trash") {
		t.Errorf("output should not claim a completed batch, got:\n%s", out)
	}
}

// TestFilesDelete_PermanentSkipsPreCheck pins the --permanent path as untouched:
// it deletes by ID, so it needs neither the existence pre-check nor the parent
// lookup.
func TestFilesDelete_PermanentSkipsPreCheck(t *testing.T) {
	lib := newFakeLibrary(t)
	useFakeLibrary(t, lib)

	out, err := runFilesDelete(t, "--fileid", "f1", "--permanent", "--confirm")
	if err != nil {
		t.Fatalf("files delete --permanent: %v", err)
	}

	if lib.deleteFile["f1"] != 1 {
		t.Errorf("DeleteFile calls = %d, want exactly 1", lib.deleteFile["f1"])
	}
	if len(lib.getFileInfo) != 0 {
		t.Errorf("--permanent must not pre-check file info, got %v", lib.getFileInfo)
	}
	if lib.rootFolders != 0 || lib.folderListings != 0 {
		t.Errorf("--permanent resolved folders (root=%d, listings=%d), want none",
			lib.rootFolders, lib.folderListings)
	}
	if len(lib.archived) != 0 {
		t.Errorf("--permanent must not archive, got %v", lib.archived)
	}
	if !strings.Contains(out, "✓ Permanently deleted") {
		t.Errorf("output missing permanent-delete confirmation, got:\n%s", out)
	}
}
