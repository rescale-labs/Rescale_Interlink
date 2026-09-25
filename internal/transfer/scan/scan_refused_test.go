package scan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// A server name that cannot be written safely fails that entry alone: both
// scanners report it with its location and reason and go on with its
// siblings, and a refused folder is one entry for its whole subtree, which is
// never listed.
func TestScansReportRefusedNamesAndKeepTheRest(t *testing.T) {
	type contents struct {
		folders []map[string]string
		files   []map[string]interface{}
	}
	tree := map[string]contents{
		"root": {
			folders: []map[string]string{{"id": "ok", "name": "ok"}, {"id": "bad1", "name": "run:1"}, {"id": "bad2", "name": "dots."}},
			files:   []map[string]interface{}{makeFile("f1", "a.dat", 1), makeFile("f2", "run 10:30.log", 1)},
		},
		"ok":   {folders: []map[string]string{{"id": "deep", "name": "deep"}}, files: []map[string]interface{}{makeFile("f3", "inner.dat", 1), makeFile("f4", "CON.txt", 1)}},
		"deep": {files: []map[string]interface{}{makeFile("f5", "x.dat", 1)}},
		"bad1": {files: []map[string]interface{}{makeFile("f6", "inner.dat", 1)}},
		"bad2": {files: []map[string]interface{}{makeFile("f7", "inner.dat", 1)}},
	}
	var mu sync.Mutex
	var listed []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.Split(strings.Trim(r.URL.Path, "/"), "/")[3]
		mu.Lock()
		listed = append(listed, id)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(folderContentsJSON(tree[id].folders, tree[id].files, ""))
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)

	want := []string{
		"a.dat",
		filepath.Join("ok", "CON.txt") + `: file in "ok" not downloaded: filename is a reserved Windows device name: "CON.txt"`,
		filepath.Join("ok", "deep", "x.dat"),
		filepath.Join("ok", "inner.dat"),
		`dots.: folder at the top level and its contents not downloaded: filename cannot end in a dot or a space: "dots."`,
		`run 10:30.log: file at the top level not downloaded: filename cannot contain ':': "run 10:30.log"`,
		`run:1: folder at the top level and its contents not downloaded: filename cannot contain ':': "run:1"`,
	}
	slices.Sort(want)
	describe := func(task RemoteFileTask) string {
		if task.Err != nil {
			return task.RelativePath + ": " + task.Err.Error()
		}
		return task.RelativePath
	}
	check := func(scanner string, folders []string, files []string) {
		t.Helper()
		slices.Sort(folders)
		slices.Sort(files)
		slices.Sort(listed)
		if want := []string{"ok", filepath.Join("ok", "deep")}; !slices.Equal(folders, want) {
			t.Errorf("%s: folders %q, want %q", scanner, folders, want)
		}
		if !slices.Equal(files, want) {
			t.Errorf("%s: files\n%s\nwant\n%s", scanner, strings.Join(files, "\n"), strings.Join(want, "\n"))
		}
		if !slices.Equal(listed, []string{"deep", "ok", "root"}) {
			t.Errorf("%s: listed folders %q, want only root, ok and deep", scanner, listed)
		}
		listed = nil
	}

	folders, files, err := ScanRemoteFolderRecursive(context.Background(), client, "root", "")
	if err != nil {
		t.Errorf("recursive scan: %v", err)
	}
	var gotFolders, gotFiles []string
	for _, f := range folders {
		gotFolders = append(gotFolders, f.RelativePath)
	}
	for _, f := range files {
		gotFiles = append(gotFiles, describe(f))
	}
	check("recursive", gotFolders, gotFiles)

	events, errs := ScanRemoteFolderStreaming(context.Background(), client, "root", nil)
	gotFolders, gotFiles = nil, nil
	for event := range events {
		if event.Folder != nil {
			gotFolders = append(gotFolders, event.Folder.RelativePath)
		}
		if event.File != nil {
			gotFiles = append(gotFiles, describe(*event.File))
		}
	}
	if err := <-errs; err != nil {
		t.Fatalf("streaming scan: %v", err)
	}
	check("streaming", gotFolders, gotFiles)
}
