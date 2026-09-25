package upload

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
)

// Only a directory was refused as a source. A FIFO passed, and opening it
// blocks until something writes to it, which no cancel can interrupt; a device
// is no file to upload either. A link to a regular file is still followed.
func TestUploadFileRefusesSourcesThatAreNotRegularFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no FIFOs or device paths to test with on Windows")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})

	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := exec.Command("mkfifo", fifo).Run(); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	regular := filepath.Join(dir, "data.bin")
	if err := os.WriteFile(regular, []byte("payload"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.bin")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		path    string
		refused bool
	}{{fifo, true}, {"/dev/null", true}, {link, false}} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		done := make(chan error, 1)
		go func() {
			_, err := UploadFile(ctx, UploadParams{LocalPath: tt.path, APIClient: client, FolderID: "FAKEID"})
			done <- err
		}()
		select {
		case err := <-done:
			refused := err != nil && strings.Contains(err.Error(), "not a regular file")
			if refused != tt.refused {
				t.Errorf("%s: err = %v, want refused = %v", tt.path, err, tt.refused)
			}
		case <-time.After(10 * time.Second):
			t.Errorf("%s: the upload blocked", tt.path)
		}
		cancel()
	}
}
