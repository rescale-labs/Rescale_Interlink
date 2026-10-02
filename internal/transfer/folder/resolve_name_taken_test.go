package folder

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
)

// A segment whose name its parent already holds, by a folder in Trash that no
// listing shows, fails in plain words, so PUR's upload folder and the engine's
// pass on none of the platform's own text.
func TestResolveOrCreatePath_ExplainsATakenName(t *testing.T) {
	fake := &fakeFolders{t: t, children: map[string]map[string]string{"root": {}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			fake.handler(w, r)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `"duplicate key value violates unique constraint \"folder_name_parent\""`)
	}))
	defer server.Close()

	_, err := ResolveOrCreatePath(context.Background(), api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), "root", "sweeps")
	want := `failed to create folder "sweeps": a folder of that name already exists in this location, possibly in Trash`
	if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "duplicate key") {
		t.Errorf("ResolveOrCreatePath returned %v, want %q and none of the platform's text", err, want)
	}
}
