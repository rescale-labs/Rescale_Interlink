package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/pur/pipeline"
)

// usePURConfig writes a config.csv holding the given "key,value" lines and
// points the CLI's config loader at it for the rest of the test.
//
// Two things keep the run offline. The API key comes from the environment, so
// loadConfig gets past its required-field check without reading the developer's
// own key; and the platform URL is deliberately off the allowlist, so any
// command that got past the preflight under test would be stopped by
// api.NewClient rather than reach the network.
func usePURConfig(t *testing.T, lines ...string) {
	t.Helper()

	body := "key,value\napi_base_url,https://example.invalid\n"
	for _, line := range lines {
		body += line + "\n"
	}

	path := filepath.Join(t.TempDir(), "config.csv")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config.csv: %v", err)
	}

	orig := cfgFile
	cfgFile = path
	t.Cleanup(func() { cfgFile = orig })

	t.Setenv("RESCALE_API_KEY", "preflight-test-key")
	t.Setenv("RESCALE_API_URL", "")
	t.Setenv("HTTPS_PROXY", "")
}

// writePreflightJobsCSV writes a one-row jobs CSV whose Submit column holds
// submitMode. The row is otherwise complete, so the only thing a command can
// object to is the value under test.
func writePreflightJobsCSV(t *testing.T, submitMode string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "jobs.csv")
	body := "Directory,JobName,AnalysisCode,Command,CoreType,CoresPerSlot," +
		"WalltimeHours,Slots,LicenseSettings,ExtraInputFileIDs,Submit\n" +
		"run_1,run_1,user_included,./solve.sh,emerald,4,1.0,1,,abc123," + submitMode + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write jobs.csv: %v", err)
	}
	return path
}

// runPURCommand executes one PUR command offline and returns its error.
func runPURCommand(t *testing.T, cmd *cobra.Command, args ...string) error {
	t.Helper()

	// Bind the shared CLI logger before the command builds its own reference:
	// every PUR command logs through it.
	GetLogger()

	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	return cmd.Execute()
}

// assertSubmitModeRefused checks the error is the one 'pur plan' reports for an
// unusable Submit value, and that it names the offending row and value.
func assertSubmitModeRefused(t *testing.T, err error, value string) {
	t.Helper()

	if err == nil {
		t.Fatalf("Submit=%q was accepted; the run would have silently created every job without submitting it", value)
	}
	for _, want := range []string{
		"job 1 (run_1)",
		"Invalid submit mode",
		`unrecognized submitMode: "` + value + `"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

// A Submit value the pipeline cannot normalize is refused by 'pur plan' but was
// swallowed by shouldSubmit, which turned every unrecognized value into
// create-only: the batch was created and never submitted, with nothing said.
// 'run' is the command that does that work, so it must refuse the CSV first.
func TestPURRunRefusesUnrecognizedSubmitMode(t *testing.T) {
	usePURConfig(t)

	err := runPURCommand(t, newRunCmd(),
		"--jobs-csv", writePreflightJobsCSV(t, "maybe"), "--dry-run")
	assertSubmitModeRefused(t, err, "maybe")
}

// Resume reads the same CSV and runs the same pipeline, so it has to refuse the
// same value — a resume of a batch whose CSV was edited is exactly how an
// unusable value reaches a run that has already created jobs.
func TestPURResumeRefusesUnrecognizedSubmitMode(t *testing.T) {
	usePURConfig(t)

	stateFile := filepath.Join(t.TempDir(), "state.csv")
	if err := os.WriteFile(stateFile, nil, 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	err := runPURCommand(t, newResumeCmd(),
		"--jobs-csv", writePreflightJobsCSV(t, "maybe"), "--state", stateFile, "--dry-run")
	assertSubmitModeRefused(t, err, "maybe")
}

// submit-existing is the command whose whole purpose is submitting, so an
// unrecognized Submit value silently turning into create-only is at its most
// misleading here.
func TestPURSubmitExistingRefusesUnrecognizedSubmitMode(t *testing.T) {
	usePURConfig(t)

	err := runPURCommand(t, newSubmitExistingCmd(),
		"--jobs-csv", writePreflightJobsCSV(t, "maybe"))
	assertSubmitModeRefused(t, err, "maybe")
}

// The gate must accept exactly what the pipeline accepts: a value refused here
// that shouldSubmit would have honoured is a working CSV the CLI now rejects.
func TestPURRunAcceptsEverySubmitModeThePipelineAccepts(t *testing.T) {
	for _, mode := range []string{"", "yes", "true", "submit", "create_and_submit", "no", "false", "create_only", "draft", "  Draft  "} {
		t.Run(strings.TrimSpace(mode), func(t *testing.T) {
			if _, err := pipeline.NormalizeSubmitMode(mode); err != nil {
				t.Fatalf("test case %q is not a value the pipeline accepts: %v", mode, err)
			}

			usePURConfig(t)
			if err := runPURCommand(t, newRunCmd(),
				"--jobs-csv", writePreflightJobsCSV(t, mode), "--dry-run"); err != nil {
				t.Errorf("Submit=%q was refused: %v", mode, err)
			}
		})
	}
}

// A worker count below one is refused by Config.Validate, which only 'config
// test' calls: zero left a pipeline stage with no workers at all, and a
// negative count panicked in make(chan …) once the pipeline was built. Both
// have to be answered before there is a pipeline.
func TestPURRunRefusesWorkerCountBelowOne(t *testing.T) {
	for _, tt := range []struct {
		name  string
		line  string
		want  string
		value string
	}{
		{"tar_workers zero", "tar_workers,0", "tar_workers must be at least 1", "0"},
		{"upload_workers negative", "upload_workers,-4", "upload_workers must be at least 1", "-4"},
		{"job_workers zero", "job_workers,0", "job_workers must be at least 1", "0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			usePURConfig(t, tt.line)

			err := runPURCommand(t, newRunCmd(),
				"--jobs-csv", writePreflightJobsCSV(t, "yes"), "--dry-run")
			if err == nil {
				t.Fatalf("%s was accepted; the pipeline would have been built with it", tt.line)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), tt.value) {
				t.Errorf("error %q does not name the offending value %q", err, tt.value)
			}
		})
	}
}

// The same config reaches submit-existing, which builds a pipeline of its own.
func TestPURSubmitExistingRefusesWorkerCountBelowOne(t *testing.T) {
	usePURConfig(t, "upload_workers,-4")

	err := runPURCommand(t, newSubmitExistingCmd(),
		"--jobs-csv", writePreflightJobsCSV(t, "yes"))
	if err == nil {
		t.Fatal("upload_workers,-4 was accepted; the pipeline would have panicked in make(chan …)")
	}
	if !strings.Contains(err.Error(), "upload_workers must be at least 1") {
		t.Errorf("error %q does not report the worker count", err)
	}
}

// The defaults, and any count of one or more, must still be accepted.
func TestPURRunAcceptsValidWorkerCounts(t *testing.T) {
	usePURConfig(t, "tar_workers,1", "upload_workers,8", "job_workers,2")

	if err := runPURCommand(t, newRunCmd(),
		"--jobs-csv", writePreflightJobsCSV(t, "yes"), "--dry-run"); err != nil {
		t.Errorf("valid worker counts were refused: %v", err)
	}
}
