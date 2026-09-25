package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/services"
	"github.com/rescale/rescale-int/internal/transfer"
)

// Every place the daemon keeps or shows an error's text: its log file and the
// IPC log buffer, the state file behind 'daemon list --failed', the scan error
// behind 'daemon status', and the IPC transfer snapshot.
func TestDaemonSinksQuoteNoCredentials(t *testing.T) {
	leak := errors.New(`Get "https://acct.blob.core.windows.net/c/f?sv=2020-10-02&sig=FAKESIG": unexpected EOF {"secretKey":"FAKEKEY"}`)
	dir := t.TempDir()

	for _, tc := range []struct {
		name string
		text func(t *testing.T) string
	}{
		{"log file and IPC buffer", func(t *testing.T) string {
			path := filepath.Join(dir, "daemon.log")
			w := NewDaemonLogWriter(DaemonLogConfig{LogFile: path, BufferSize: 10})
			logging.NewLoggerWithWriter(w).Error().Err(leak).Msgf("Failed to find completed jobs: %v", leak)
			w.Close()
			entries := w.GetBuffer().GetRecent(10)
			if len(entries) != 1 {
				t.Fatalf("IPC buffer holds %d entries, want 1", len(entries))
			}
			file, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			return fmt.Sprint(entries) + string(file)
		}},
		{"state file and failed list", func(t *testing.T) string {
			s := NewState(filepath.Join(dir, "state.json"))
			s.MarkFailed("job1", "Job 1", leak)
			if err := s.Save(); err != nil {
				t.Fatal(err)
			}
			file, err := os.ReadFile(filepath.Join(dir, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			return string(file) + s.GetFailedJobs()[0].Error
		}},
		{"state file with an unredacted error", func(t *testing.T) string {
			path := filepath.Join(dir, "old-state.json")
			old := fmt.Sprintf(`{"version":%q,"downloaded":{"job1":{"job_id":"job1","error":%q,"retry_count":1}}}`, stateVersion, leak.Error())
			if err := os.WriteFile(path, []byte(old), 0600); err != nil {
				t.Fatal(err)
			}
			s := NewState(path)
			if err := s.Load(); err != nil {
				t.Fatal(err)
			}
			return s.GetFailedJobs()[0].Error
		}},
		{"scan error", func(t *testing.T) string {
			d := &Daemon{}
			d.recordScanError(leak)
			text, _ := d.LastScanError()
			return text
		}},
		{"transfer snapshot", func(t *testing.T) string {
			cfg := &config.Config{APIKey: "test-key", APIBaseURL: "http://127.0.0.1:0", ProxyMode: "no-proxy"}
			ts := services.NewTransferService(api.NewClientForTest(cfg), events.NewEventBus(0), services.TransferServiceConfig{MaxConcurrent: 1})
			task := ts.GetQueue().TrackTransferWithLabel("f", 1, transfer.TaskTypeDownload, "file1", "/tmp/f", services.SourceLabelDaemon)
			ts.GetQueue().Fail(task.ID, leak)
			return fmt.Sprint((&Daemon{ts: ts}).DaemonTransferSnapshot().Tasks)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if text := tc.text(t); strings.Contains(text, "FAKE") || !strings.Contains(text, "sig=REDACTED") {
				t.Errorf("quotes a credential: %s", text)
			}
		})
	}
}
