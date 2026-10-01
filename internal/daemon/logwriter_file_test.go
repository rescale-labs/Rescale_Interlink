package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/logging"
)

// The log file is what support reads when the app or the tray started the
// daemon, so each line keeps the entry's fields after its message, sorted:
// why a job was refused, and which job.
func TestDaemonLogFileKeepsTheEntrysFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	w := NewDaemonLogWriter(DaemonLogConfig{LogFile: path, BufferSize: 10})
	logging.NewLoggerWithWriter(w).Error().Err(errors.New(`cannot mirror workspace folder "Q1: results"`)).
		Str("job_id", "abc123").Strs("tags", []string{"a", "b"}).Int("files", 2).Msg("Refusing to download job")
	w.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `[error] daemon: Refusing to download job error="cannot mirror workspace folder \"Q1: results\"" files=2 job_id=abc123 tags=["a","b"]` + "\n"
	if !strings.HasSuffix(string(data), want) {
		t.Errorf("the log file holds\n%q\nwant a line ending\n%q", data, want)
	}
	if entries := w.GetBuffer().GetRecent(10); len(entries) != 1 || entries[0].Fields["job_id"] != "abc123" {
		t.Errorf("IPC buffer entries %+v, want the one entry with its fields", entries)
	}
}
