package wailsapp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
)

// A folder listing or search that fails says why in its answer's warning,
// with no items and the folder it was for, whether the API failed it or the
// file service could not be reached, so the File Browser does not show the
// folder as empty.
func TestFolderListingsSayWhenTheyFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	a, eng := appWithEngine(t)
	eng.FileService().SetAPIClient(api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}))
	noService := &App{}

	for name, answer := range map[string]FolderContentsDTO{
		"listing":                 a.ListRemoteFolderPage("f1", "", 0),
		"search":                  a.SearchRemoteFolderContents("f1", "run", "", 0),
		"listing without service": noService.ListRemoteFolderPage("f1", "", 0),
		"search without service":  noService.SearchRemoteFolderContents("f1", "run", "", 0),
	} {
		if answer.Warning == "" || answer.Items == nil || len(answer.Items) != 0 || answer.FolderID != "f1" {
			t.Errorf("the failed %s answered %+v, want a warning, no items and folder f1", name, answer)
		}
	}
}
