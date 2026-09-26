package progress

import (
	"os"
	"testing"

	"github.com/vbauerster/mpb/v8"

	"github.com/rescale/rescale-int/internal/logging"
)

// Logs routed above the bars are drawn on stderr, so they colour as stderr
// does, whatever stdout is. The answer only differs from a plain wrapper's when
// stderr is a terminal; run under a pty to see this fail without the fix.
func TestLogWriterAnswersForStderr(t *testing.T) {
	g := &barGroup{progress: new(mpb.Progress), isTerminal: true}
	if got, want := logging.IsTerminal(g.LogWriter()), logging.IsTerminal(os.Stderr); got != want {
		t.Errorf("the bars' log writer reports terminal=%v, stderr is %v", got, want)
	}
}
