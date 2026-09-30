package filter

import (
	"strconv"
	"strings"
	"testing"
)

// A PUR file scan and --path-filter both hand this matcher slash-separated
// paths, so "*" must stop at a "/" on every platform, Windows included.
func TestMatchPathPattern(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"**/vasprun.xml", "vasprun.xml", true},
		{"**/vasprun.xml", "a/vasprun.xml", true},
		{"**/vasprun.xml", "a/b/c/vasprun.xml", true},
		{"**/vasprun.xml", "a/vasprun.xml.bak", false},
		{"**/input/*.xml", "x/input/a.xml", true},
		{"**/input/*.xml", "x/input/sub/a.xml", false},
		{"run_*/**/*.inp", "run_1/a.inp", true},
		{"run_*/**/*.inp", "run_1/x/y/a.inp", true},
		{"run_*/**/*.inp", "other/run_1/a.inp", false},
		{"run_*/**/*.inp", "a.inp", false},
		{"run_*/*.dat", "run_1/sub/a.dat", false},
		// A "**" matches no folders, one or several, wherever it is.
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/b", true},
		{"a/**/b", "a/x/y/z/b", true},
		{"a/**/b", "a/x/c", false},
		{"a/**/b/**", "a/b/x", true},
		{"a/**/b/**", "a/p/q/b/x", true},
		{"a/**/b/**", "a/p/q/c/x", false},
		{"run_1/**", "run_1/a/b.txt", true},
		{"run_1/**", "run_2/a", false},
		{"**", "a/b/c", true},
		{"**/**/x", "x", true},
		{"**/**/x", "a/b/x", true},
		{"**/**/x", "a/b/y", false},
		{"*/**/x", "x", false},
		{"*/**/x", "a/x", true},
		{"*/**/x", "a/b/c/x", true},
		// A trailing "/" is ignored.
		{"run_1/**/", "run_1/a/b", true},
		{"run_1/**/", "run_2/a", false},
		// A segment also matches a name that is literally the same, which is
		// how a Windows user, who cannot escape a bracket, names such a folder.
		{"case [1]/**", "case [1]/a.dat", true},
		{"**/case [1]/*.dat", "runs/case [1]/a.dat", true},
		{"[ab]", "[ab]", true},
		// A bracket keeps its glob meaning as well.
		{"case [12]/x", "case 1/x", true},
		{"case [12]/x", "case 3/x", false},
		// Each "**" used to retry every split of the path: these took up to seconds.
		{strings.Repeat("**/", 8) + "x", strings.Repeat("d/", 24) + "y", false},
		{strings.Repeat("**/a/", 8) + "x", strings.Repeat("a/", 24) + "y", false},
	} {
		if got := MatchPathPattern(tc.name, tc.pattern); got != tc.want {
			t.Errorf("MatchPathPattern(%q, %q) = %v, want %v", tc.name, tc.pattern, got, tc.want)
		}
	}
}

// A non-match is the costly case for a "**" pattern: every split of the path
// has to fail. The time per call should grow with the number of "**", not
// multiply by it.
func BenchmarkMatchPathPatternNoMatch(b *testing.B) {
	name := strings.Repeat("d/", 24) + "y"
	for _, n := range []int{1, 2, 4, 8} {
		pattern := strings.Repeat("**/", n) + "x"
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			for b.Loop() {
				if MatchPathPattern(name, pattern) {
					b.Fatal("matched")
				}
			}
		})
	}
}
