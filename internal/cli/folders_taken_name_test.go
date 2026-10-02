package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/reporting"
	"github.com/rescale/rescale-int/internal/resources"
)

// takenNameRefusal is the platform's answer to a new folder whose name is
// already used in its parent, by a listed folder or by one in Trash: a 400
// with its refusal of a name already in use, as a JSON string.
const takenNameRefusal = `"duplicate key value violates unique constraint \"folder_name_parent\""`

// folderTreeAPI stands in for the Rescale API's folder endpoints: it lists each
// folder's subfolders, creates folders (a folder's ID is its name) and answers
// a name in refusals with a 400 and that body. Anything else, such as the
// credential warm-up before uploads, is not found.
type folderTreeAPI struct {
	mu       sync.Mutex
	children map[string][]string
	refusals map[string]string
}

func (f *folderTreeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, isFolder := strings.CutPrefix(r.URL.Path, "/api/v3/folders/")
	switch {
	case isFolder && r.Method == http.MethodGet && strings.HasSuffix(id, "/contents/"):
		results := []map[string]any{}
		for _, name := range f.children[strings.TrimSuffix(id, "/contents/")] {
			results = append(results, map[string]any{"type": "folder", "item": map[string]string{"id": name, "name": name}})
		}
		writeFakeJSON(w, map[string]any{"results": results})
	case isFolder && r.Method == http.MethodPost:
		var body struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if refusal, ok := f.refusals[body.Name]; ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, refusal)
			return
		}
		parent := strings.TrimSuffix(id, "/")
		f.children[parent] = append(f.children[parent], body.Name)
		writeFakeJSON(w, map[string]string{"id": body.Name})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// useFolderTreeAPI points the CLI's API client at f for one test.
func useFolderTreeAPI(t *testing.T, f *folderTreeAPI) {
	t.Helper()
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	orig := getAPIClientFn
	getAPIClientFn = func() (*api.Client, error) {
		return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), nil
	}
	t.Cleanup(func() { getAPIClientFn = orig })
}

// The platform refuses a folder whose name is used in its parent, even by a
// folder in Trash that no listing shows. folders create, and upload-dir for
// the folder it uploads and for a subfolder it merges into, say what that
// means without the platform's own text, and fail without an error
// report. Any other refusal reads as the platform wrote it, even one that
// repeats a name with "duplicate" in it.
func TestFoldersExplainATakenName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	const plain = "a folder of that name already exists in this location, possibly in Trash"
	for _, c := range []struct {
		args     []string
		inParent []string // folders the parent already lists
		refused  string   // the folder name the platform refuses
		want     string
		listed   bool // the refusal is in the list of failed files, not the error
	}{
		{[]string{"create", "--name", "tree"}, nil, "tree", "failed to create folder: " + plain, false},
		{[]string{"upload-dir", root}, nil, "tree", "failed to create root folder: " + plain, false},
		{[]string{"upload-dir", root, "-m", "--sequential"}, []string{"tree"}, "sub",
			"failed to create folder structure: failed to create folder sub: " + plain, false},
		{[]string{"upload-dir", root, "-m"}, []string{"tree"}, "sub", "failed to create folder sub: " + plain, true},
	} {
		useFolderTreeAPI(t, &folderTreeAPI{
			children: map[string][]string{"lib": c.inParent},
			refusals: map[string]string{c.refused: takenNameRefusal},
		})
		printed, err := runWithCancel(t, newFoldersCmd(), nil, append(c.args, "--parent-id", "lib")...)
		if err == nil {
			t.Errorf("%v succeeded after printing\n%s", c.args, printed)
			continue
		}
		got := err.Error()
		if c.listed {
			got = printed
		} else if reporting.IsReportable(err, reporting.CategoryTransfer) {
			t.Errorf("%v: %q would be filed as an error report", c.args, got)
		}
		if !strings.Contains(got, c.want) || strings.Contains(got, "duplicate key") {
			t.Errorf("%v gave:\n%s\nwant %q and none of the platform's text", c.args, got, c.want)
		}
	}

	invalid := `{"name":["not a valid name: duplicate?"]}`
	useFolderTreeAPI(t, &folderTreeAPI{children: map[string][]string{}, refusals: map[string]string{"duplicate?": invalid}})
	if _, err := runWithCancel(t, newFoldersCmd(), nil, "create", "--name", "duplicate?", "--parent-id", "lib"); err == nil ||
		err.Error() != "failed to create folder: API request failed with status 400: "+invalid {
		t.Errorf("folders create of an invalid name returned %v, want the platform's refusal as it is", err)
	}
}

// isolateUserFolders points every folder Interlink keeps per-user state in, on
// any system, at fresh temporary folders for one test, so that an error report
// a failing test files lands there, and returns the report folder.
func isolateUserFolders(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "XDG_CONFIG_HOME"} {
		t.Setenv(name, t.TempDir())
	}
	return config.ReportDirectory()
}

// Only the platform's answer decides, never the folder's name around it. On
// either upload path a taken subfolder named like an HTTP status is explained,
// and a refusal that quotes a name holding the refusal's words keeps the
// platform's text. Neither files an error report: the refusal returned by
// --sequential is not reportable, and the CLI's error handler, run as on exit,
// saves nothing.
func TestUploadDirReadsOnlyThePlatformsAnswer(t *testing.T) {
	reports := isolateUserFolders(t)
	const statusLike, wordy = "HTTP 500, rerun", "duplicate key value violates unique constraint"
	quoting := `{"name":["not a valid name: ` + wordy + `"]}`
	for _, mode := range [][]string{nil, {"--sequential"}} {
		for name, c := range map[string]struct{ refusal, want string }{
			statusLike: {takenNameRefusal, "failed to create folder " + statusLike + ": a folder of that name already exists in this location"},
			wordy:      {quoting, "failed to create folder " + wordy + ": API request failed with status 400: " + quoting},
		} {
			root := filepath.Join(t.TempDir(), "tree")
			if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
				t.Fatal(err)
			}
			useFolderTreeAPI(t, &folderTreeAPI{children: map[string][]string{"lib": {"tree"}}, refusals: map[string]string{name: c.refusal}})
			printed, err := runWithCancel(t, newFoldersCmd(), nil, append([]string{"upload-dir", root, "--parent-id", "lib", "-m"}, mode...)...)
			if err == nil || !strings.Contains(printed+err.Error(), c.want) || name == statusLike && strings.Contains(printed+err.Error(), "duplicate key") {
				t.Errorf("upload-dir %v of %q returned %v after printing\n%s\nwant %q", mode, name, err, printed, c.want)
			}
			if mode != nil && reporting.IsReportable(err, reporting.CategoryTransfer) {
				t.Errorf("upload-dir %v of %q: %v would be filed as an error report", mode, name, err)
			}
			reporting.HandleCLIError(err, "cli", "rescale-int folders upload-dir", "")
			if saved, _ := os.ReadDir(reports); len(saved) > 0 {
				t.Errorf("upload-dir %v of %q saved an error report in %s", mode, name, reports)
				_ = os.RemoveAll(reports) // so that the next run's check is its own
			}
		}
	}
}

// On upload-dir's default path a subfolder's taken name stops the folder
// creation, and its log line says what that means, as the failure list does.
func TestUploadDirLogsATakenSubfolderName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(&folderTreeAPI{children: map[string][]string{}, refusals: map[string]string{"sub": takenNameRefusal}})
	defer server.Close()
	var logged bytes.Buffer
	result, _, err := uploadDirectoryPipelined(context.Background(), api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}),
		NewFolderCache(), root, "tree", false, 1, 1, false, &config.Config{}, GetLogger().WithOutput(&logged),
		resources.NewManager(resources.Config{MaxThreads: 1}))
	if err != nil || len(result.Errors) != 1 || !strings.Contains(logged.String(), "failed to create folder sub: a folder of that name already exists") ||
		strings.Contains(logged.String()+result.Errors[0].Error.Error(), "duplicate key") {
		t.Errorf("upload returned %v and %v after logging\n%s\nwant one failure, logged and recorded in plain words", err, result.Errors, logged.String())
	}
}
