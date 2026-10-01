package installer

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestToolchainPinsAgree: every workflow and build script installs Go, Node,
// the Wails CLI and WiX itself, so each holds its own copy of their versions,
// and several hold the same Go archive's SHA-256. A bump that misses a copy
// fails here instead of in a release build. go.mod counts for Wails: wails
// build warns when the library there is not the CLI's version.
func TestToolchainPinsAgree(t *testing.T) {
	const (
		goMod      = "../go.mod"
		release    = "../.github/workflows/release.yml"
		tests      = "../.github/workflows/test.yml"
		buildDist  = "../build/build_dist.ps1"
		linuxBuild = "../build/linux/build-release.sh"
		localEnv   = "../build/windows_local_build/_env.ps1"
	)
	files := []string{goMod, release, tests, buildDist, linuxBuild, localEnv}
	text := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text[f] = string(b)
	}

	// set: the keys a file sets the pin with, a YAML key or a variable.
	// Whatever follows one, quoted or not, must have the pin's shape, so a
	// spelling this test does not know fails instead of hiding behind the
	// file's other copies. also: where the pin only appears, as in archive and
	// step names, captured by the one group that matches. All the values must
	// agree, and each file in "in" must hold one.
	const version, sum = `v?\d+\.\d+\.\d+`, `[0-9a-f]{64}`
	for _, pin := range []struct {
		what, shape, set, also string
		in                     []string
	}{
		{"Go", version, `\bGO_VERSION=|\$(?:Script:)?GoVersion\s*=|\bgo-version:`,
			`\bgo(1\.\d+\.\d+)|\bGo (1\.\d+\.\d+)`,
			[]string{release, tests, buildDist, linuxBuild, localEnv}},
		{"Node", version, `\bNODE_VERSION=|\$(?:Script:)?NodeVersion\s*=|\bnode-version:`,
			`node-v(\d+\.\d+\.\d+)|\bNode (\d+\.\d+\.\d+)`,
			[]string{release, tests, buildDist, linuxBuild, localEnv}},
		{"Wails", version, `\bWAILS_VERSION=|\$(?:Script:)?WailsVersion\s*=|wailsapp/wails/v2 `,
			`wails@(v\d+\.\d+\.\d+)|Wails CLI (v\d+\.\d+\.\d+)`,
			[]string{goMod, release, buildDist, linuxBuild, localEnv}},
		{"WiX", version, `\$(?:Script:)?WixVersion\s*=|wixext/`,
			`wix --version (\d+\.\d+\.\d+)|\bWiX (\d+\.\d+\.\d+)`,
			[]string{buildDist, localEnv}},
		{"Go darwin-arm64 SHA-256", sum, ``,
			`["'](` + sum + `)  go[\d.]+\.darwin-arm64\.tar\.gz`,
			[]string{release, tests}},
		{"Go linux-amd64 SHA-256", sum, `\bGO_SHA256=`,
			`["'](` + sum + `)  go[\d.]+\.linux-amd64\.tar\.gz`,
			[]string{tests, linuxBuild}},
		{"Go windows-amd64 SHA-256", sum, `\$GoZipSha256\s*=`,
			`-ne ["'](` + sum + `)|["']go[\d.]+\.windows-amd64\.zip["']\s*=\s*["'](` + sum + `)`,
			[]string{tests, buildDist, localEnv}},
	} {
		shape := regexp.MustCompile(`^(?:` + pin.shape + `)$`)
		also := regexp.MustCompile(pin.also)
		var set *regexp.Regexp
		if pin.set != "" {
			set = regexp.MustCompile(`(?:` + pin.set + `)\s*["']?([^\s"']*)`)
		}
		byValue := map[string][]string{} // each value found, and the files holding it
		for _, f := range files {
			var values []string
			if set != nil {
				for _, m := range set.FindAllStringSubmatch(text[f], -1) {
					if !shape.MatchString(m[1]) {
						t.Errorf("%s: %s sets it to %q, which this test cannot read", pin.what, f, m[1])
						continue
					}
					values = append(values, m[1])
				}
			}
			for _, m := range also.FindAllStringSubmatch(text[f], -1) {
				values = append(values, strings.Join(m[1:], ""))
			}
			if len(values) == 0 && slices.Contains(pin.in, f) {
				t.Errorf("%s: no pin found in %s", pin.what, f)
			}
			slices.Sort(values)
			for _, v := range slices.Compact(values) {
				byValue[v] = append(byValue[v], f)
			}
		}
		if len(byValue) > 1 {
			t.Errorf("%s pins disagree: %v", pin.what, byValue)
		}
	}
}
