package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A foreground `daemon run` logs to stdout, which is often a file or a pipe
// (`daemon run > daemon.log`), where colour codes are noise.
func TestDaemonConsoleUncolouredOffATerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "") // zerolog treats empty as unset
	f, err := os.Create(filepath.Join(t.TempDir(), "daemon.log"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()

	orig := os.Stdout
	os.Stdout = f
	w := NewDaemonLogWriter(DaemonLogConfig{Console: true, BufferSize: 10})
	os.Stdout = orig

	if _, err := w.Write([]byte(`{"level":"info","message":"hello","k":"v"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Fatalf("the line never reached the file:\n%q", data)
	}
	if strings.Contains(string(data), "\x1b[") {
		t.Errorf("colour codes written to a file:\n%q", data)
	}
}
