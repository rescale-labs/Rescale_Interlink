package logging

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file on a terminal is coloured, and so is a writer marked with Via as ending
// up on one; a writer ending up on a plain file is not.
func TestConsoleOutputColouredOnATerminal(t *testing.T) {
	dir := t.TempDir()
	tty, err := os.Create(filepath.Join(dir, "tty"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer tty.Close()
	plain, err := os.Create(filepath.Join(dir, "plain"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer plain.Close()
	defer func(orig func(uintptr) bool) { terminalFd = orig }(terminalFd)
	terminalFd = func(fd uintptr) bool { return fd == tty.Fd() }

	for name, w := range map[string]io.Writer{"terminal": tty, "via a terminal": Via(io.Discard, tty)} {
		if NewConsoleWriter(w).NoColor {
			t.Errorf("%s: no colour", name)
		}
	}
	if !NewConsoleWriter(Via(io.Discard, plain)).NoColor {
		t.Error("via a plain file: coloured")
	}
}

// An ordinary pipe is no terminal on any platform, whatever the MSYS and Cygwin
// check makes of it, and a regular file is never a console.
func TestPipeAndFileAreNotTerminals(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()
	f, err := os.Create(filepath.Join(t.TempDir(), "f"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()

	if IsTerminal(w) || IsTerminal(f) || consoleColor(f.Fd()) {
		t.Errorf("pipe %v, file %v, file as console %v; want all false",
			IsTerminal(w), IsTerminal(f), consoleColor(f.Fd()))
	}
}

// ttySink stands for a wrapper that knows where its output lands: the progress
// display, which draws on stderr, or a redacting writer.
type ttySink struct {
	io.Writer
	tty bool
}

func (s ttySink) IsTerminal() bool { return s.tty }

// Logs routed to the progress display colour as its terminal allows, not as
// stdout does: with `upload ... > run.log` on a terminal they had lost colour.
// Off a terminal, colour codes are noise to whatever reads the log.
func TestRoutedOutputColoursForItsDestination(t *testing.T) {
	stdout, err := os.Create(filepath.Join(t.TempDir(), "run.log"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer stdout.Close()
	orig := os.Stdout
	os.Stdout = stdout
	l := NewLogger("cli", nil)
	os.Stdout = orig

	// The logger's own output, here a file, and a wrapper that cannot say where
	// it lands, as the CLI's redacting writer hides the file, are no terminal.
	t.Setenv("NO_COLOR", "") // zerolog treats empty as unset
	l.Info().Msg("direct")
	var wrapped bytes.Buffer
	l.SetOutput(struct{ io.Writer }{&wrapped})
	l.Info().Msg("wrapped")
	if direct, _ := os.ReadFile(stdout.Name()); !strings.Contains(string(direct), "direct") || !strings.Contains(wrapped.String(), "wrapped") ||
		strings.Contains(string(direct)+wrapped.String(), "\x1b[") {
		t.Errorf("logged %q to stdout and %q to the wrapper, want both lines uncoloured", direct, wrapped.String())
	}

	for _, tt := range []struct {
		name, noColor string
		tty, want     bool
	}{
		{"terminal", "", true, true},
		{"terminal under NO_COLOR", "1", true, false},
		{"file", "", false, false},
	} {
		t.Setenv("NO_COLOR", tt.noColor)
		var buf bytes.Buffer
		l.WithOutput(ttySink{&buf, tt.tty}).Info().Msg("routed")
		if got := strings.Contains(buf.String(), "\x1b["); got != tt.want {
			t.Errorf("%s: coloured = %v, want %v: %q", tt.name, got, tt.want, buf.String())
		}
	}
}
