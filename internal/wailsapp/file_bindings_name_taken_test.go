package wailsapp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/events"
)

// takenName is the platform's answer to a new folder whose name its parent
// already holds, even by a folder in Trash: a 400 with its refusal of a name
// already in use.
const takenName = `"duplicate key value violates unique constraint \"folder_name_parent\""`

// folderAPI stands in for the Rescale API's folder endpoints: it lists each
// folder's subfolders (a folder's ID is its name) and creates folders,
// answering a name in refusals with a 400 and that body. With raced, a refused
// name is listed from then on, as when another upload created it first.
type folderAPI struct {
	mu       sync.Mutex
	children map[string][]string
	refusals map[string]string
	raced    bool
}

func (f *folderAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, isFolder := strings.CutPrefix(r.URL.Path, "/api/v3/folders/")
	switch {
	case isFolder && r.Method == http.MethodGet && strings.HasSuffix(id, "/contents/"):
		results := []map[string]any{}
		for _, name := range f.children[strings.TrimSuffix(id, "/contents/")] {
			results = append(results, map[string]any{"type": "folder", "item": map[string]string{"id": name, "name": name}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	case isFolder && r.Method == http.MethodPost:
		var body struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		refusal, refused := f.refusals[body.Name]
		if !refused || f.raced {
			parent := strings.TrimSuffix(id, "/")
			f.children[parent] = append(f.children[parent], body.Name)
		}
		if refused {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, refusal)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": body.Name})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// uploadFolder uploads a local folder name, holding an empty "sub", into "lib"
// on f and returns the result, the error its row on the Transfers tab ended
// with, and every line it logged.
func uploadFolder(t *testing.T, name string, f *folderAPI) (FolderUploadResultDTO, string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(f)
	defer server.Close()
	client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})
	defer func(f func(*App) *api.Client) { folderUploadAPI = f }(folderUploadAPI)
	folderUploadAPI = func(*App) *api.Client { return client }
	a, eng := appWithEngine(t)
	eng.TransferService().SetAPIClient(client) // its credential warm-up goes here too, not to the platform
	scans, logs := eng.Events().Subscribe(events.EventEnumerationCompleted), eng.Events().Subscribe(events.EventLog)

	result := a.StartFolderUpload(root, "lib", nil)
	var row string
	select {
	case ev := <-scans:
		row = enumerationEventToDTO(ev.(*events.EnumerationEvent)).Error
	case <-time.After(10 * time.Second):
		t.Fatal("the folder upload never finished its row on the Transfers tab")
	}
	var logged strings.Builder
	for len(logs) > 0 {
		logged.WriteString((<-logs).(*events.LogEvent).Message + "\n")
	}
	return result, row, logged.String()
}

// A folder upload whose folder, or a subfolder of a folder it merges into, has
// a name the parent already holds, even by a folder in Trash, says what that
// means in its result, its row and its log, without the platform's text. A
// folder of that name that another upload created meanwhile is merged into,
// as before. A refusal that only repeats a name with "duplicate" in it is no
// such thing and reads as the platform wrote it.
func TestFolderUploadExplainsATakenName(t *testing.T) {
	const plain = "a folder of that name already exists in this location, possibly in Trash"

	result, row, logged := uploadFolder(t, "tree", &folderAPI{children: map[string][]string{}, refusals: map[string]string{"tree": takenName}})
	if want := "Failed to create folder 'tree': " + plain; !strings.HasPrefix(result.Error, want) || !strings.HasPrefix(row, want) ||
		strings.Contains(logged, "duplicate key") {
		t.Errorf("a taken folder name gave %q, row %q, log:\n%s\nwant %q and no platform text", result.Error, row, logged, want)
	}

	result, row, logged = uploadFolder(t, "tree", &folderAPI{children: map[string][]string{"lib": {"tree"}}, refusals: map[string]string{"sub": takenName}})
	if want := "Folder creation failed: failed to create folder sub: " + plain; result.MergedInto != "tree" || !strings.HasPrefix(row, want) ||
		strings.Contains(logged, "duplicate key") {
		t.Errorf("a taken subfolder name gave %+v, row %q, log:\n%s\nwant %q and no platform text", result, row, logged, want)
	}

	result, row, _ = uploadFolder(t, "tree", &folderAPI{children: map[string][]string{}, refusals: map[string]string{"tree": takenName}, raced: true})
	if result.Error != "" || result.MergedInto != "tree" || row != "" {
		t.Errorf("a folder another upload created meanwhile gave %+v, row %q, want the upload merged into it", result, row)
	}

	invalid := `{"name":["not a valid name: duplicates"]}`
	result, _, _ = uploadFolder(t, "duplicates", &folderAPI{children: map[string][]string{}, refusals: map[string]string{"duplicates": invalid}})
	if want := "Failed to create folder: API request failed with status 400: " + invalid; result.Error != want {
		t.Errorf("an invalid name gave %q, want %q", result.Error, want)
	}
}

// A refused "New Folder" returns the same plain account, for the File Browser
// to show.
func TestCreateRemoteFolderExplainsATakenName(t *testing.T) {
	server := httptest.NewServer(&folderAPI{children: map[string][]string{}, refusals: map[string]string{"tree": takenName}})
	defer server.Close()
	a, eng := appWithEngine(t)
	eng.FileService().SetAPIClient(api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}))
	_, err := a.CreateRemoteFolder("tree", "lib")
	if want := "failed to create folder: a folder of that name already exists in this location, possibly in Trash"; err == nil ||
		!strings.HasPrefix(err.Error(), want) {
		t.Errorf("a refused New Folder returned %v, want %q", err, want)
	}
}
