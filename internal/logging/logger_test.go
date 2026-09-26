package logging

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Colour codes are for a terminal. A log sent to a pipe or a file carried them
// anyway, as noise to whatever read it.
func TestConsoleOutputUncolouredOffATerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "") // zerolog treats empty as unset
	f, err := os.Create(filepath.Join(t.TempDir(), "out.log"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()

	orig := os.Stdout
	os.Stdout = f
	l := NewLogger("cli", nil)
	os.Stdout = orig

	l.Info().Str("k", "v").Msg("direct")
	// The CLI's logger writes through a redacting wrapper, which hides the file.
	l.SetOutput(struct{ io.Writer }{f})
	l.Info().Str("k", "v").Msg("wrapped")
	l.WithOutput(struct{ io.Writer }{f}).Info().Str("k", "v").Msg("routed")

	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, msg := range []string{"direct", "wrapped", "routed"} {
		if !strings.Contains(string(data), msg) {
			t.Fatalf("the %q line never reached the file:\n%q", msg, data)
		}
	}
	if strings.Contains(string(data), "\x1b[") {
		t.Errorf("colour codes written to a file:\n%q", data)
	}
}

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
