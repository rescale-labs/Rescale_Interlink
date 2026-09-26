package cli

import (
	archivetar "archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/filescan"
	"github.com/rescale/rescale-int/internal/util/tar"
)

// scan-files wrote each job's Directory and LocalInputFiles relative to the
// folder it ran in, and `pur run` resolves them against its own: a CSV written
// elsewhere with -o, or run from another folder, pointed at nothing.
func TestScanFilesWritesAbsolutePaths(t *testing.T) {
	base := t.TempDir()
	writeScanDeck(t, base, filepath.Join("scan", "a", "m1.inp"))
	template := scanFilesTemplate(t, base, "{{base}}")
	if err := os.Mkdir(filepath.Join(base, "outdir"), 0755); err != nil {
		t.Fatalf("mkdir outdir: %v", err)
	}
	t.Chdir(base)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	if _, err := runScanFiles(t, "scan", filepath.Join("a", "*.inp"), template,
		filepath.Join("outdir", "j.csv")); err != nil {
		t.Fatalf("scan-files: %v", err)
	}

	// Read from the CSV's own folder, where a later `pur run j.csv` would start.
	t.Chdir(filepath.Join(base, "outdir"))
	jobs, err := config.LoadJobsCSV("j.csv")
	if err != nil || len(jobs) != 1 {
		t.Fatalf("load generated CSV: %d jobs, %v", len(jobs), err)
	}
	dir := filepath.Join(wd, "scan", "a")
	if jobs[0].Directory != dir {
		t.Errorf("Directory = %q, want %q", jobs[0].Directory, dir)
	}
	if want := []string{filepath.Join(dir, "m1.inp")}; !reflect.DeepEqual(jobs[0].LocalInputFiles, want) {
		t.Errorf("LocalInputFiles = %q, want %q", jobs[0].LocalInputFiles, want)
	}
}

// runLogged runs cmd with args and returns everything it printed, the CLI
// logger's lines included: the logger is rebuilt on the captured stdout, which
// is where the real one writes.
func runLogged(t *testing.T, cmd *cobra.Command, args ...string) string {
	t.Helper()
	defer func(orig *logging.Logger) { logger = orig }(logger)
	return captureStdout(t, func() {
		logger = nil
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Errorf("%s: %v", cmd.Name(), err)
		}
	})
}

// lineWith reports whether one line of out contains every one of subs.
func lineWith(out string, subs ...string) bool {
	for _, line := range strings.Split(out, "\n") {
		all := true
		for _, sub := range subs {
			all = all && strings.Contains(line, sub)
		}
		if all {
			return true
		}
	}
	return false
}

// makeDirsTemplate writes a one-row template CSV for make-dirs-csv.
func makeDirsTemplate(t *testing.T, root string) string {
	t.Helper()
	template := filepath.Join(root, "template.csv")
	if err := config.SaveJobsCSV(template, []models.JobSpec{{
		JobName: "run_1", Command: "./solve.sh", AnalysisCode: "user_included",
		CoreType: "emerald", CoresPerSlot: 1, Slots: 1, WalltimeHours: 1.0,
	}}); err != nil {
		t.Fatalf("write template: %v", err)
	}
	return template
}

// --json is for a program to read, but the "Scanning for files" log line went
// to stdout ahead of the JSON, so the output did not parse.
func TestScanFilesJSONPrintsOnlyJSON(t *testing.T) {
	root := t.TempDir()
	writeScanDeck(t, root, "m1.inp")

	out := runLogged(t, newScanFilesCmd(), "--root", root, "--primary", "*.inp", "--json")
	var result filescan.ScanResult
	if err := json.Unmarshal([]byte(out), &result); err != nil || len(result.Jobs) != 1 {
		t.Errorf("stdout is not the scan's JSON (%v):\n%s", err, out)
	}
}

// A failed --json scan leaves stdout empty and fails, as the other --json
// commands do; the message goes to stderr with the error.
func TestScanFilesJSONFailureLeavesStdoutEmpty(t *testing.T) {
	root := t.TempDir()
	var err error
	defer func(orig *logging.Logger) { logger = orig }(logger)
	out := captureStdout(t, func() {
		logger = nil
		cmd := newScanFilesCmd()
		cmd.SetArgs([]string{"--root", root, "--primary", "*.inp", "--json"})
		cmd.SilenceUsage = true
		cmd.SetErr(io.Discard)
		err = cmd.Execute()
	})
	if err == nil || out != "" {
		t.Errorf("a scan matching nothing: error %v, stdout %q; want an error and no output", err, out)
	}
}

// A symlinked input file is archived under the name the command uses for it,
// with the target's contents. Resolving the link, as is right for a run folder,
// would name the archive entry after the target, which the command never
// mentions.
func TestScanFilesArchivesSymlinkedInputUnderItsName(t *testing.T) {
	base := t.TempDir()
	writeScanDeck(t, base, filepath.Join("store", "blob.dat"))
	if err := os.Mkdir(filepath.Join(base, "scan"), 0755); err != nil {
		t.Fatalf("mkdir scan: %v", err)
	}
	if err := os.Symlink(filepath.Join(base, "store", "blob.dat"), filepath.Join(base, "scan", "m1.inp")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	output := filepath.Join(base, "jobs.csv")
	if _, err := runScanFiles(t, filepath.Join(base, "scan"), "*.inp",
		scanFilesTemplate(t, base, "{{base}}"), output); err != nil {
		t.Fatalf("scan-files: %v", err)
	}
	jobs, err := config.LoadJobsCSV(output)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("load generated CSV: %d jobs, %v", len(jobs), err)
	}

	archive := filepath.Join(base, "job.tar.gz")
	if err := tar.CreateTarGzFromFiles(jobs[0].LocalInputFiles, archive, "gzip"); err != nil {
		t.Fatalf("archive %v: %v", jobs[0].LocalInputFiles, err)
	}
	if got := archiveEntries(t, archive); jobs[0].Command != "solve m1.inp" || got["m1.inp"] != "data" {
		t.Errorf("command %q, archive %v; want m1.inp holding the target's data", jobs[0].Command, got)
	}
}

// archiveEntries reads a .tar.gz into entry name -> contents.
func archiveEntries(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip %s: %v", path, err)
	}
	entries := map[string]string{}
	for tr := archivetar.NewReader(gz); ; {
		h, err := tr.Next()
		if err == io.EOF {
			return entries
		}
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		data, _ := io.ReadAll(tr)
		entries[h.Name] = string(data)
	}
}

// --validation-pattern dropped the run directories without a matching file and
// said nothing: three jobs from five folders, and no word about the other two.
func TestMakeDirsCSVReportsValidationSkips(t *testing.T) {
	root := t.TempDir()
	for _, run := range []string{"Run_1", "Run_2", "Run_3", "Run_4", "Run_5"} {
		if err := os.Mkdir(filepath.Join(root, run), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", run, err)
		}
		if run <= "Run_3" {
			writeScanDeck(t, root, filepath.Join(run, "out.avg.fnc"))
		}
	}
	output := filepath.Join(root, "jobs.csv")

	out := runLogged(t, newMakeDirsCSVCmd(), "--template", makeDirsTemplate(t, root), "--output", output,
		"--pattern", "Run_*", "--cwd", root, "--validation-pattern", "*.avg.fnc")
	for _, run := range []string{"Run_4", "Run_5"} {
		if !lineWith(out, run, "*.avg.fnc") {
			t.Errorf("no line names the skipped %s and the pattern:\n%s", run, out)
		}
	}
	if !strings.Contains(out, "Generated 3 jobs in "+output+" (2 skipped") {
		t.Errorf("the final line does not count the 2 skipped directories:\n%s", out)
	}
}

// With --part-dirs, a project without the --run-subpath folder is passed over by
// design, but it went without a word, as the directories --validation-pattern
// dropped did.
func TestMakeDirsCSVReportsProjectsWithoutRunSubpath(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{filepath.Join("DOE_1", "Simcodes", "Run_1"), filepath.Join("DOE_2", "Run_1")} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	output := filepath.Join(root, "jobs.csv")
	doe2 := filepath.Join(root, "DOE_2")

	out := runLogged(t, newMakeDirsCSVCmd(), "--template", makeDirsTemplate(t, root), "--output", output,
		"--pattern", "Run_*", "--run-subpath", "Simcodes",
		"--part-dirs", filepath.Join(root, "DOE_1"), "--part-dirs", doe2)
	// By base name: on Windows the logger quotes a path, doubling its backslashes.
	if !lineWith(out, "DOE_2", `run subpath "Simcodes" not found`) {
		t.Errorf("no line names the project %s and the missing subpath:\n%s", doe2, out)
	}
	if !strings.Contains(out, "Generated 1 jobs in "+output+" (1 skipped") {
		t.Errorf("the final line does not count the skipped project:\n%s", out)
	}
}
