package folder

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
)

// walkDirChannelBuffer mirrors the directory channel localfs.WalkStream
// allocates. The tree below has to outgrow it: the hang only appears once the
// walker has more directories to hand over than the buffer will hold.
const walkDirChannelBuffer = 1000

// TestRunOrchestrator_FolderCreationFailureTerminates covers F13. Folder
// creation stops consuming directories on its first failure. Nothing else reads
// that channel, so the walker filled its buffer and blocked — and a blocked
// walker never closes the file channel the orchestrator is waiting on, so the
// upload never finished and never failed.
func TestRunOrchestrator_FolderCreationFailureTerminates(t *testing.T) {
	root := t.TempDir()

	// More directories than the walker can hold, so it is still producing when
	// folder creation gives up.
	for i := 0; i < walkDirChannelBuffer*3; i++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("dir-%04d", i)), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	writeFiles(t, root, 1, 2, 3)

	// Every folder call fails, which is what stops the creator consuming.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"folder service unavailable"}`, http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := orchConfig(root)
	cfg.APIClient = api.NewClientForTest(&config.Config{
		APIBaseURL: server.URL,
		APIKey:     "test-key",
		ProxyMode:  "no-proxy",
	})

	done := make(chan *OrchestratorResult, 1)
	outputCh := make(chan testItem, 16)
	dispatchDone, result := RunOrchestrator(context.Background(), cfg, OrchestratorCallbacks[testItem]{
		BuildItem:          buildTestItem,
		OnOrchestratorDone: func(r *OrchestratorResult) { done <- r },
	}, outputCh)

	go drainItems(outputCh)

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the orchestrator never finished after folder creation failed")
	}
	awaitClose(t, dispatchDone, "dispatch")

	if result.FolderError == nil {
		t.Error("the folder-creation failure was not reported")
	}
	if result.Cancelled {
		t.Error("a failed upload was reported as cancelled by the caller")
	}
}
