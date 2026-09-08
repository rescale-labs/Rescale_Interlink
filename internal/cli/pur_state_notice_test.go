package cli

import (
	"bytes"
	"testing"
)

// TestWarnNoStateFile covers the notice `pur run` prints when it is given no
// --state: such a run keeps its record in memory, so nothing survives it and a
// resume has nothing to read. The run itself is legitimate and must not be
// blocked, which is why the user is told rather than stopped.
//
// The notice is tested here rather than through newRunCmd because the command
// resolves its configuration before the pipeline starts: loadConfig reads the
// developer's own config file and RESCALE_* environment, so a machine with a
// key present would carry the test past the notice and into a real run.
func TestWarnNoStateFile(t *testing.T) {
	const want = "No --state file given: this run cannot be resumed and its progress is not recorded.\n"

	var f purPipelineFlags
	var out bytes.Buffer
	f.warnNoStateFile(&out)
	if got := out.String(); got != want {
		t.Errorf("notice = %q, want %q", got, want)
	}

	f.stateFile = "state.csv"
	out.Reset()
	f.warnNoStateFile(&out)
	if got := out.String(); got != "" {
		t.Errorf("a run with --state printed %q, want nothing", got)
	}
}
