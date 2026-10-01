package mesainit

import (
	"os"
	"runtime"
	"testing"
)

// The GUI opens for --gui wherever it is, or for no arguments, and never
// for --cli; any other argument runs the CLI, so Mesa is loaded exactly when
// main.go opens the GUI.
func TestGUIMode(t *testing.T) {
	t.Setenv("DISPLAY", ":0")
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{nil, true},
		{[]string{"--gui"}, true},
		{[]string{"jobs", "--gui"}, true},
		{[]string{"--cli"}, false},
		{[]string{"--gui", "--cli"}, false},
		{[]string{"-gui"}, false},
		{[]string{"jobs", "list"}, false},
		{[]string{"daemon", "run"}, false},
	} {
		os.Args = append([]string{"rescale-int"}, tc.args...)
		if got := GUIMode(); got != tc.want {
			t.Errorf("GUIMode() with %q = %v, want %v", tc.args, got, tc.want)
		}
	}
	if runtime.GOOS == "linux" {
		t.Setenv("DISPLAY", "")
		t.Setenv("WAYLAND_DISPLAY", "")
		os.Args = []string{"rescale-int"}
		if GUIMode() {
			t.Error("GUIMode() with no arguments and no display = true, want the CLI")
		}
	}
}
