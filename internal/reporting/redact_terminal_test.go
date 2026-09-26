package reporting

import (
	"testing"

	"github.com/rescale/rescale-int/internal/logging"
)

// ttyWriter stands for a destination that knows whether it is a terminal.
type ttyWriter struct{ tty bool }

func (ttyWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w ttyWriter) IsTerminal() bool          { return w.tty }

// The CLI's and the GUI's loggers write through RedactWriter, so the colour
// decision has to see through it to the destination.
func TestRedactWriterPassesOnWhetherItIsATerminal(t *testing.T) {
	for _, tty := range []bool{true, false} {
		if got := logging.IsTerminal(RedactWriter(ttyWriter{tty})); got != tty {
			t.Errorf("RedactWriter over a destination with terminal=%v reports %v", tty, got)
		}
	}
}
