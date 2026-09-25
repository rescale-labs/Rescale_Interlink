package progress

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
)

func TestSinkWriterFollowsTheActiveSink(t *testing.T) {
	fallback := &strings.Builder{}
	sink := &strings.Builder{}

	w := SinkWriter(fallback)

	if _, err := w.Write([]byte("before\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	SetLogSink(sink)
	if _, err := w.Write([]byte("during\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	ClearLogSink(sink)

	if _, err := w.Write([]byte("after\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got, want := fallback.String(), "before\nafter\n"; got != want {
		t.Errorf("fallback got %q, want %q", got, want)
	}
	if got, want := sink.String(), "during\n"; got != want {
		t.Errorf("sink got %q, want %q", got, want)
	}
}

func TestClearLogSinkIgnoresStaleWriter(t *testing.T) {
	older := &strings.Builder{}
	newer := &strings.Builder{}

	SetLogSink(older)
	SetLogSink(newer)
	// A UI finishing late must not take the sink away from the one still running.
	ClearLogSink(older)

	if LogSink() != newer {
		t.Error("a stale clear removed the active sink")
	}
	ClearLogSink(newer)
	if LogSink() != nil {
		t.Error("sink not released")
	}
}

func TestSinkWriterWithNilFallback(t *testing.T) {
	w := SinkWriter(nil)
	n, err := w.Write([]byte("dropped"))
	if err != nil || n != len("dropped") {
		t.Fatalf("write to a nil fallback: n=%d err=%v", n, err)
	}
}

// TestStdlibLogRoutesThroughSink is the wiring the CLI relies on: the standard
// logger is pointed at a SinkWriter once, and transfer diagnostics then follow
// the bars as they come and go without any further log setup.
func TestStdlibLogRoutesThroughSink(t *testing.T) {
	fallback := &strings.Builder{}
	sink := &strings.Builder{}

	prevFlags := log.Flags()
	prevOut := log.Writer()
	log.SetFlags(0)
	log.SetOutput(SinkWriter(fallback))
	defer func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}()

	SetLogSink(sink)
	log.Printf("[BATCH] CLI-UPLOAD: scaled 5 → 7 workers")
	ClearLogSink(sink)
	log.Printf("[BATCH] after")

	if !strings.Contains(sink.String(), "scaled 5 → 7 workers") {
		t.Errorf("log line did not reach the progress display: %q", sink.String())
	}
	if strings.Contains(fallback.String(), "scaled") {
		t.Errorf("log line also hit the terminal: %q", fallback.String())
	}
	if !strings.Contains(fallback.String(), "after") {
		t.Errorf("log line after the bars should hit the terminal: %q", fallback.String())
	}
}

// Every line a transfer prints can quote a storage error, and with it a signed
// URL's credentials: its failure line, the log lines passed to the terminal, and
// the notices written above the bars.
func TestTransferOutputRedactsCredentials(t *testing.T) {
	capture := func(std **os.File, fn func()) string {
		f, err := os.CreateTemp(t.TempDir(), "out")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		orig := *std
		*std = f
		defer func() { *std = orig }()
		fn()
		out, _ := os.ReadFile(f.Name())
		return string(out)
	}
	cause := errors.New(`failed to stage block 0: Put "https://a.blob.core.windows.net/c/f?comp=block&sig=SECRET": EOF`)
	var logged strings.Builder
	fmt.Fprintln(SinkWriter(&logged), cause)
	for sink, out := range map[string]string{
		"log line": logged.String(),
		"notice":   capture(&os.Stderr, func() { fmt.Fprintln((&barGroup{}).LogWriter(), cause) }),
		"failure line": capture(&os.Stdout, func() {
			(&barBase{group: &barGroup{}}).finish("→", "/in/data.bin", "folder", "", cause)
		}),
	} {
		if strings.Contains(out, "SECRET") || !strings.Contains(out, "sig=REDACTED") {
			t.Errorf("the %s printed %q, want the signature redacted", sink, out)
		}
	}
}

func TestNewUploadUIClaimsSinkOnlyOnTerminal(t *testing.T) {
	// Tests do not run on a terminal, so mpb writes to io.Discard and claiming the
	// sink would swallow every log line.
	ui := NewUploadUI(1)
	defer ui.Wait()

	if ui.IsTerminal() {
		t.Skip("test stderr is a terminal; the non-TTY guard cannot be checked here")
	}
	if LogSink() != nil {
		t.Error("a non-terminal UI must not claim the log sink")
	}
}
