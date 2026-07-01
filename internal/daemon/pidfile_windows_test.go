package daemon

import (
	"bufio"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// processInfo reads another process's command line, argument for argument
// and with its quoting undone: on Windows that is how KillDaemon tells the
// daemon from any other Interlink command.
func TestProcessInfo_ReadsAnotherProcesssCommandLine(t *testing.T) {
	cmd := startHelper(t, t.TempDir(), "rescale-int", "run", "daemon", "run", "--download-dir", `C:\a folder\"quoted"`)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	if line, _ := bufio.NewReader(stdout).ReadString('\n'); line != "ready\n" {
		t.Fatalf("the helper said %q", line)
	}

	image, args, err := processInfo(cmd.Process.Pid)
	if err != nil || !strings.EqualFold(filepath.Base(image), "rescale-int.exe") || !slices.Equal(args, cmd.Args) {
		t.Errorf("processInfo: %q %q (%v), want rescale-int.exe started with %q", image, args, err, cmd.Args)
	}
}
