package pathutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Only ~ on its own or before a separator is the home folder; ~foo is a name,
// which the caller's absolute-path check then refuses.
func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cases := map[string]string{
		"~":                      home,
		"~/dl":                   filepath.Join(home, "dl"),
		"~foo":                   "~foo",
		"~foo/dl":                "~foo/dl",
		"relative":               "relative",
		filepath.Join(home, "x"): filepath.Join(home, "x"),
	}
	if runtime.GOOS == "windows" {
		cases[`~\dl`] = filepath.Join(home, "dl")
	}
	for in, want := range cases {
		got, err := ExpandHome(in)
		if err != nil || filepath.Clean(got) != filepath.Clean(want) {
			t.Errorf("ExpandHome(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// ResolveAbsolutePath no longer turns ~foo into <home>foo.
	cwd := t.TempDir()
	t.Chdir(cwd)
	if got, err := ResolveAbsolutePath("~foo"); err != nil || filepath.Base(got) != "~foo" {
		t.Errorf("ResolveAbsolutePath(~foo) = %q, %v; want ~foo in the working folder", got, err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "~foo")); !os.IsNotExist(err) {
		t.Errorf("ResolveAbsolutePath created ~foo (stat: %v)", err)
	}
}
