package cli

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/rescale/rescale-int/internal/config"
)

// initAnswers answers every 'config init' prompt: an API key, the default
// platform, 7 tar workers (the value the tests look for), then the defaults and
// no proxy.
const initAnswers = "test-key\n\n7\n\n\n\n"

// runConfigInit runs 'rescale-int config init' with args as if on a terminal,
// answering its prompts with initAnswers. HOME and the Windows profile variables
// point at a fresh directory, so the default location is inside it; that
// default path is returned.
func runConfigInit(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runConfigInitFrom(t, strings.NewReader(initAnswers), args...)
}

// runConfigInitFrom is runConfigInit reading the answers from stdin.
func runConfigInitFrom(t *testing.T, stdin io.Reader, args ...string) (string, error) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))

	origTerminal, origCfgFile := configInitTerminalFn, cfgFile
	configInitTerminalFn = func() bool { return true }
	t.Cleanup(func() { configInitTerminalFn, cfgFile = origTerminal, origCfgFile })

	rootCmd := NewRootCmd()
	AddCommands(rootCmd)
	// The hook replaces the process-wide logger and rate-limit callbacks, and
	// is not what is under test.
	rootCmd.PersistentPreRun = nil
	rootCmd.SetArgs(append([]string{"config", "init"}, args...))
	rootCmd.SetIn(stdin)
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SilenceUsage = true

	err := rootCmd.Execute()
	return config.GetDefaultConfigPath(), err
}

// 'config init' wrote the default location whatever --config said, while
// 'config show', 'config test' and every other command read the --config file.
func TestConfigInitHonoursConfigFlag(t *testing.T) {
	tests := []struct {
		name     string
		existing bool // a configuration is already at the --config path
		force    bool
		wantTar  int // tar workers in the --config file afterwards; 7 means init wrote it
	}{
		{"writes a new file there", false, false, 7},
		{"--force replaces the file there", true, true, 7},
		{"a file there stops it without --force", true, false, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "team", "interlink.csv")
			if tt.existing {
				if err := config.SaveConfigCSV(&config.Config{TarWorkers: 2, ProxyMode: "no-proxy"}, path); err != nil {
					t.Fatalf("seed config: %v", err)
				}
			}
			args := []string{"--config", path}
			if tt.force {
				args = append(args, "--force")
			}

			defaultPath, err := runConfigInit(t, args...)
			if err != nil {
				t.Fatalf("config init: %v", err)
			}

			if _, err := os.Stat(path); err != nil {
				t.Fatalf("nothing at the --config path: %v", err)
			}
			cfg, err := config.LoadConfigCSV(path)
			if err != nil {
				t.Fatalf("load %s: %v", path, err)
			}
			if cfg.TarWorkers != tt.wantTar {
				t.Errorf("tar workers in the --config file = %d, want %d", cfg.TarWorkers, tt.wantTar)
			}
			if _, err := os.Stat(defaultPath); !os.IsNotExist(err) {
				t.Errorf("config init wrote the default %s although --config was given", defaultPath)
			}
			// The API key goes to a file named token beside the configuration.
			_, err = os.Stat(filepath.Join(filepath.Dir(path), "token"))
			if wrote := tt.wantTar == 7; wrote != (err == nil) {
				t.Errorf("token file beside the --config file present = %v, want %v", err == nil, wrote)
			}
			// That directory is owner-only, as everywhere else a token is kept.
			if info, err := os.Stat(filepath.Dir(path)); err == nil && runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
				t.Errorf("config directory mode = %o, want no group or other access", info.Mode().Perm())
			}
		})
	}
}

// A token file beside the --config file was replaced without --force, though
// it may belong to another configuration in the same directory.
func TestConfigInitKeepsAnExistingTokenWithoutForce(t *testing.T) {
	dir := t.TempDir()
	path, token := filepath.Join(dir, "b.csv"), filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("key-for-a\n"), 0o600); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	var err error
	out := captureStdout(t, func() { _, err = runConfigInit(t, "--config", path) })
	if err != nil {
		t.Fatalf("config init: %v", err)
	}
	if got, _ := os.ReadFile(token); string(got) != "key-for-a\n" {
		t.Errorf("token file holds %q, want it left alone", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("config init wrote %s beside a token it would not replace", path)
	}
	for _, want := range []string{"already exists at: " + token, "view the current config with: rescale-int --config '" + path + "' config show"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not include %q:\n%s", want, out)
		}
	}

	if _, err := runConfigInit(t, "--config", path, "--force"); err != nil {
		t.Fatalf("config init --force: %v", err)
	}
	if got, _ := os.ReadFile(token); string(got) != "test-key\n" {
		t.Errorf("after --force the token file holds %q, want the new key", got)
	}
}

// readerFunc is an io.Reader made from a function.
type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// Without --force, a token that appeared while the prompts were answered (from
// another 'config init', say) was replaced: only the check before them looked.
func TestConfigInitKeepsATokenThatAppearsDuringThePrompts(t *testing.T) {
	dir := t.TempDir()
	path, token := filepath.Join(dir, "b.csv"), filepath.Join(dir, "token")
	answers := strings.NewReader(initAnswers)
	stdin := readerFunc(func(p []byte) (int, error) {
		if answers.Len() == len(initAnswers) { // the first read, after the check
			if err := os.WriteFile(token, []byte("key-for-a\n"), 0o600); err != nil {
				t.Error(err)
			}
		}
		return answers.Read(p)
	})

	_, err := runConfigInitFrom(t, stdin, "--config", path)

	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("error = %v, want a refusal of the token that appeared", err)
	}
	if got, _ := os.ReadFile(token); string(got) != "key-for-a\n" {
		t.Errorf("token file holds %q, want it left alone", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("config init wrote %s beside a token it would not replace", path)
	}
}

// The check before the prompts treats a token path that is a link to a missing
// file, or a path it cannot look at, as a refusal, so the prompts never run
// and nothing is written.
func TestConfigInitStopsAtTheCheckBeforePrompting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reports a path through a file as missing, and links need privileges")
	}
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), filepath.Join(dir, "token")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "a.csv"), filepath.Join(dir, "file", "a.csv")} {
		_, err := runConfigInitFrom(t, iotest.ErrReader(errors.New("prompted")), "--config", path)
		if err != nil && strings.Contains(err.Error(), "prompted") {
			t.Errorf("config init --config %s prompted", path)
		}
	}
}

// After 'config init --config X' the printed commands left out --config X, so
// following them used the default configuration instead. They also left paths
// unquoted, and one with a space or a quote fell apart in the shell. Quoted,
// a token path beginning with "-" was still taken by cat for an option.
func TestConfigInitNextStepsNameTheConfigFile(t *testing.T) {
	root := t.TempDir()
	// Each path as one POSIX shell word. TempDir paths hold no quote of their own.
	quoted := func(name string) string { return "'" + filepath.Join(root, `Team'\''s Settings`, name) + "'" }
	path, token := quoted("eu.csv"), quoted("token")

	var err error
	saved := captureStdout(t, func() { _, err = runConfigInit(t, "--config", filepath.Join(root, "Team's Settings", "eu.csv")) })
	if err != nil {
		t.Fatalf("config init: %v", err)
	}

	for _, want := range []string{
		"rescale-int --config " + path + " --token-file " + token + " <command>",   // Option 1
		"export RESCALE_API_KEY=$(cat < " + token + ")",                            // Option 2
		"rescale-int --config " + path + " <command>",                              // Option 2
		"rescale-int --config " + path + " --token-file " + token + " config test", // the test line
	} {
		if !strings.Contains(saved, want) {
			t.Errorf("the next steps do not include %q:\n%s", want, saved)
		}
	}

	t.Chdir(root)
	saved = captureStdout(t, func() { _, err = runConfigInit(t, "--config", filepath.Join("-team", "eu.csv")) })
	if want := "export RESCALE_API_KEY=$(cat < '" + filepath.Join("-team", "token") + "')"; !strings.Contains(saved, want) {
		t.Errorf("error = %v; the next steps do not include %q:\n%s", err, want, saved)
	}
}

// --config naming a directory, however spelled, wrote the API key beside it or
// into it and then failed to save the configuration. Naming the token file, in
// any letter case (macOS and Windows filesystems ignore it) or with trailing
// dots and spaces (Windows drops them), wrote the configuration over the key.
// Each is refused before any prompt or write.
func TestConfigInitRefusesADirectoryOrTheTokenFile(t *testing.T) {
	sep := string(filepath.Separator)
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"settings"}, "names a directory"},
		{[]string{"new" + sep, "--force"}, "names a directory"},
		{[]string{"new" + sep + ".", "--force"}, "names a directory"},
		{[]string{"new" + sep + "..", "--force"}, "names a directory"},
		{[]string{"new" + sep + "token", "--force"}, "name of the token file"},
		{[]string{"TOKEN"}, "name of the token file"},
		{[]string{"token."}, "name of the token file"},
		{[]string{"token ", "--force"}, "name of the token file"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			work := t.TempDir()
			if err := os.Mkdir(filepath.Join(work, "settings"), 0o700); err != nil {
				t.Fatal(err)
			}

			_, err := runConfigInitFrom(t, iotest.ErrReader(errors.New("prompted")), append([]string{"--config", work + sep + tt.args[0]}, tt.args[1:]...)...)

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want %q", err, tt.want)
			}
			_ = filepath.WalkDir(work, func(p string, _ fs.DirEntry, err error) error {
				if err == nil && p != work && p != filepath.Join(work, "settings") {
					t.Errorf("config init wrote %s", p)
				}
				return nil
			})
		})
	}
}

// With --force, a configuration that is the token file through a link was
// written over the key. With or without it, that is refused before any prompt
// or write.
func TestConfigInitRefusesAConfigurationLinkedToTheToken(t *testing.T) {
	links := map[string]func(string, string) error{"hard link": os.Link, "symlink": os.Symlink}
	if runtime.GOOS == "windows" {
		delete(links, "symlink") // creating one needs privileges there
	}
	for kind, link := range links {
		for _, args := range [][]string{nil, {"--force"}} {
			dir := t.TempDir()
			path, token := filepath.Join(dir, "a.csv"), filepath.Join(dir, "token")
			if err := os.WriteFile(token, []byte("key-for-a\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := link(token, path); err != nil {
				t.Fatal(err)
			}

			_, err := runConfigInitFrom(t, iotest.ErrReader(errors.New("prompted")), append([]string{"--config", path}, args...)...)

			if err == nil || !strings.Contains(err.Error(), "is the same file as "+token) {
				t.Errorf("%s %v: error = %v, want a refusal of the token file", kind, args, err)
			}
			if got, _ := os.ReadFile(token); string(got) != "key-for-a\n" {
				t.Errorf("%s %v: token file holds %q, want it left alone", kind, args, got)
			}
		}
	}
}

// Without --config, 'config init' still writes the default location.
func TestConfigInitWithoutConfigFlagWritesDefault(t *testing.T) {
	defaultPath, err := runConfigInit(t)
	if err != nil {
		t.Fatalf("config init: %v", err)
	}

	if _, err := os.Stat(defaultPath); err != nil {
		t.Fatalf("nothing at the default path: %v", err)
	}
	cfg, err := config.LoadConfigCSV(defaultPath)
	if err != nil {
		t.Fatalf("load %s: %v", defaultPath, err)
	}
	if cfg.TarWorkers != 7 {
		t.Errorf("tar workers in the default file = %d, want 7", cfg.TarWorkers)
	}
}
