// Package daemon tests
package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/validation"
)

func TestJobFilter_MatchesFilter(t *testing.T) {
	tests := []struct {
		name     string
		filter   *JobFilter
		job      models.JobResponse
		expected bool
	}{
		{
			name:     "nil filter matches all",
			filter:   nil,
			job:      models.JobResponse{Name: "Any Job Name"},
			expected: true,
		},
		{
			name:     "empty filter matches all",
			filter:   &JobFilter{},
			job:      models.JobResponse{Name: "Any Job Name"},
			expected: true,
		},
		{
			name:     "name prefix match",
			filter:   &JobFilter{NamePrefix: "Test"},
			job:      models.JobResponse{Name: "Test Job 1"},
			expected: true,
		},
		{
			name:     "name prefix no match",
			filter:   &JobFilter{NamePrefix: "Test"},
			job:      models.JobResponse{Name: "Production Job 1"},
			expected: false,
		},
		{
			name:     "name prefix case insensitive",
			filter:   &JobFilter{NamePrefix: "test"},
			job:      models.JobResponse{Name: "TEST Job 1"},
			expected: true,
		},
		{
			name:     "name contains match",
			filter:   &JobFilter{NameContains: "simulation"},
			job:      models.JobResponse{Name: "CFD Simulation Run 1"},
			expected: true,
		},
		{
			name:     "name contains no match",
			filter:   &JobFilter{NameContains: "simulation"},
			job:      models.JobResponse{Name: "CFD Analysis Run 1"},
			expected: false,
		},
		{
			name:     "exclude match",
			filter:   &JobFilter{ExcludeNames: []string{"Debug"}},
			job:      models.JobResponse{Name: "Debug Test Run"},
			expected: false,
		},
		{
			name:     "exclude no match",
			filter:   &JobFilter{ExcludeNames: []string{"Debug"}},
			job:      models.JobResponse{Name: "Production Run 1"},
			expected: true,
		},
		{
			name: "combined filters - all match",
			filter: &JobFilter{
				NamePrefix:   "Sim",
				NameContains: "CFD",
			},
			job:      models.JobResponse{Name: "Simulation CFD Run 1"},
			expected: true,
		},
		{
			name: "combined filters - prefix fails",
			filter: &JobFilter{
				NamePrefix:   "Sim",
				NameContains: "CFD",
			},
			job:      models.JobResponse{Name: "Test CFD Run 1"},
			expected: false,
		},
		{
			name: "combined filters - contains fails",
			filter: &JobFilter{
				NamePrefix:   "Sim",
				NameContains: "CFD",
			},
			job:      models.JobResponse{Name: "Simulation FEA Run 1"},
			expected: false,
		},
		{
			name: "combined filters with exclude",
			filter: &JobFilter{
				NamePrefix:   "Sim",
				ExcludeNames: []string{"SimDebug"},
			},
			job:      models.JobResponse{Name: "SimDebug Test"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create monitor with filter
			m := &Monitor{filter: tt.filter}
			result := m.matchesFilter(tt.job)
			if result != tt.expected {
				t.Errorf("matchesFilter() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestComputeOutputDir(t *testing.T) {
	// baseDir/leaf are joined with filepath.Join so expectations are correct on
	// both Windows (\) and Unix (/).
	baseDir := filepath.Join("downloads")
	tests := []struct {
		name       string
		jobID      string
		jobName    string
		useJobName bool
		leaf       string
	}{
		{
			name:       "use job ID",
			jobID:      "abc123",
			jobName:    "Test Job",
			useJobName: false,
			leaf:       "job_abc123",
		},
		{
			name:       "use job name (no ID suffix; ID goes in .jobid file)",
			jobID:      "abc123xyz",
			jobName:    "Test Job",
			useJobName: true,
			leaf:       "Test Job",
		},
		{
			name:       "job name sanitized for directory",
			jobID:      "abc",
			jobName:    "Test/Job:Name",
			useJobName: true,
			leaf:       "Test_Job_Name",
		},
		{
			name:       "empty job name falls back to job ID",
			jobID:      "abc123",
			jobName:    "",
			useJobName: true,
			leaf:       "job_abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expected := filepath.Join(baseDir, tt.leaf)
			result := ComputeOutputDir(baseDir, tt.jobID, tt.jobName, tt.useJobName)
			if result != expected {
				t.Errorf("ComputeOutputDir() = %q, want %q", result, expected)
			}
		})
	}
}

// TestComputeOutputDir_Collision verifies the on-disk collision handling:
// the ID suffix is appended only when the job-name folder already exists for a
// DIFFERENT job; a fresh name, or the same job's own folder, uses the plain name.
func TestComputeOutputDir_Collision(t *testing.T) {
	base := t.TempDir()

	// 1. No existing folder -> plain name.
	if got, want := ComputeOutputDir(base, "job1", "My Run", true), filepath.Join(base, "My Run"); got != want {
		t.Fatalf("fresh: got %q, want %q", got, want)
	}

	// Simulate job1 having been downloaded (folder + matching .jobid).
	dir := filepath.Join(base, "My Run")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteJobIDFile(dir, "job1"); err != nil {
		t.Fatal(err)
	}

	// 2. Same job re-download -> reuse the plain folder (matching .jobid).
	if got, want := ComputeOutputDir(base, "job1", "My Run", true), dir; got != want {
		t.Errorf("same job: got %q, want %q", got, want)
	}

	// 3. Different job, same name -> append the job ID.
	if got, want := ComputeOutputDir(base, "job2", "My Run", true), dir+"_job2"; got != want {
		t.Errorf("collision: got %q, want %q", got, want)
	}

	// 4. Existing folder but no .jobid (e.g. pre-existing/user folder) ->
	//    treat as a different owner and disambiguate.
	bare := filepath.Join(base, "Bare")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := ComputeOutputDir(base, "job3", "Bare", true), bare+"_job3"; got != want {
		t.Errorf("no-jobid collision: got %q, want %q", got, want)
	}

	// 5. A file at the name is not this job's folder either.
	writeFile(t, filepath.Join(base, "Plain"), "")
	if got, want := ComputeOutputDir(base, "job4", "Plain", true), filepath.Join(base, "Plain_job4"); got != want {
		t.Errorf("file at the name: got %q, want %q", got, want)
	}

	// 6. The job's own folder is reused under a suffixed name too: the folder an
	//    earlier version named "<name>_<first six characters of the ID>", and
	//    one this version suffixed with the whole ID, even with the name free.
	for _, prior := range []string{"Legacy Run_abcdef", "Legacy Run_abcdefgh"} {
		t.Run(prior, func(t *testing.T) {
			base := t.TempDir()
			want := filepath.Join(base, prior)
			if err := os.Mkdir(want, 0o755); err != nil {
				t.Fatal(err)
			}
			if got := ComputeOutputDir(base, "abcdefgh", "Legacy Run", true); got != want {
				t.Errorf("got %q, want the existing %q", got, want)
			}
		})
	}

	// 7. Only a real folder whose .jobid, if any, names this job is reused
	//    under a suffixed name. Another job's folder of that name is not, nor a
	//    link; the job takes the next name nothing is at, and finds it again.
	t.Run("suffixed names held by something else", func(t *testing.T) {
		base := t.TempDir()
		for name, id := range map[string]string{"Run_abcdef": "other1", "Run_abcdefgh": "other2"} {
			if err := os.Mkdir(filepath.Join(base, name), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := WriteJobIDFile(filepath.Join(base, name), id); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(t.TempDir(), filepath.Join(base, "Link_abcdef")); err != nil {
			t.Skipf("cannot make a symbolic link here: %v", err)
		}
		if got, want := ComputeOutputDir(base, "abcdefgh", "Run", true), filepath.Join(base, "Run"); got != want {
			t.Errorf("got %q, want %q: the suffixed folders are other jobs'", got, want)
		}
		if got, want := ComputeOutputDir(base, "abcdefgh", "Link", true), filepath.Join(base, "Link"); got != want {
			t.Errorf("got %q, want %q: a link is not the job's folder", got, want)
		}
		writeFile(t, filepath.Join(base, "Run", JobIDFileName), "other3\n")
		next := filepath.Join(base, "Run_abcdefgh_2")
		if got := ComputeOutputDir(base, "abcdefgh", "Run", true); got != next {
			t.Errorf("with every name taken, got %q, want %q", got, next)
		}
		if err := os.Mkdir(next, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := WriteJobIDFile(next, "abcdefgh"); err != nil {
			t.Fatal(err)
		}
		if got := ComputeOutputDir(base, "abcdefgh", "Run", true); got != next {
			t.Errorf("the job's own numbered folder: got %q, want %q", got, next)
		}
	})
}

// In a folder named after its job, a job file cannot take the marker's place:
// the job would lose its folder, and a marker naming another job would hand
// the folder over. The file is refused like any other that cannot be placed,
// whatever its case, since Windows and macOS ignore case.
func TestDownloadJob_RefusesAFileWhereTheMarkerIs(t *testing.T) {
	for _, name := range []string{JobIDFileName, strings.ToUpper(JobIDFileName)} {
		t.Run(name, func(t *testing.T) {
			const jobID = "abcdefgh"
			dir := t.TempDir()
			d := newPlatform(t, &fakeJob{id: jobID, files: []models.JobFile{{ID: "m", Name: name, DecryptedSize: 6}}}).daemon(dir, EligibilityConfig{})
			d.cfg.UseJobNameDir = true

			outcome := runDownloadJob(t, d, &CompletedJob{ID: jobID, Name: "My Run"}, 20*time.Second)
			entry := d.state.Downloaded[jobID]
			if outcome == OutcomeDownloaded || entry == nil ||
				!strings.HasPrefix(entry.Error, "1 of 1 file could not be downloaded: refusing to download to ") {
				t.Errorf("outcome %s, state %+v: want the file refused", outcome, entry)
			}
			if id, _ := readJobIDFile(filepath.Join(dir, "My Run")); id != jobID {
				t.Errorf("the marker holds %q, want %q", id, jobID)
			}
		})
	}
}

// The marker is read only when it is short and holds nothing but an ID.
func TestReadJobIDFile_TakesOnlyAnID(t *testing.T) {
	for content, want := range map[string]string{
		"abc123\n":               "abc123",
		"":                       "",
		"two words":              "",
		"../abc":                 "",
		strings.Repeat("a", 100): "",
	} {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, JobIDFileName), content)
		if got, ok := readJobIDFile(dir); got != want || ok != (want != "") {
			t.Errorf("readJobIDFile with %q = %q, %t; want %q", content, got, ok, want)
		}
	}
}

// An upgrade must not download a job a second time: a job already downloaded
// into the folder an earlier version named "<name>_<first six characters of
// the ID>" is found there, and no second folder is made.
func TestDownloadJob_ReusesAnEarlierVersionsFolder(t *testing.T) {
	const jobID = "abcdefgh"
	dir := t.TempDir()
	d := newPlatform(t, &fakeJob{id: jobID, files: []models.JobFile{{ID: "f1", Name: "out1.txt", DecryptedSize: 5}}}).daemon(dir, EligibilityConfig{})
	d.cfg.UseJobNameDir = true
	legacy := filepath.Join(dir, "My Run_abcdef")
	writeFile(t, filepath.Join(legacy, "out1.txt"), "hello")

	if outcome := runDownloadJob(t, d, &CompletedJob{ID: jobID, Name: "My Run"}, 20*time.Second); outcome != OutcomeDownloaded {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeDownloaded)
	}
	if entry := d.state.Downloaded[jobID]; entry == nil || entry.OutputDir != legacy {
		t.Errorf("state %+v, want the job recorded in %q", entry, legacy)
	}
	if id, _ := readJobIDFile(legacy); id != jobID {
		t.Errorf("the earlier folder's .jobid holds %q, want %q", id, jobID)
	}
	if _, err := os.Lstat(filepath.Join(dir, "My Run")); !os.IsNotExist(err) {
		t.Errorf("a second folder was made for the job: %v", err)
	}
}

func TestSanitizeDirectoryName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple name",
			input:    "Test Job",
			expected: "Test Job",
		},
		{
			name:     "name with slashes",
			input:    "Test/Job\\Path",
			expected: "Test_Job_Path",
		},
		{
			name:     "name with special chars",
			input:    "Test:Job*File?Name",
			expected: "Test_Job_File_Name",
		},
		{
			name:     "name with quotes and pipes",
			input:    `Test"Job|Name`,
			expected: "Test_Job_Name",
		},
		{
			name:     "name with angle brackets",
			input:    "Test<Job>Name",
			expected: "Test_Job_Name",
		},
		{
			name:     "leading/trailing spaces",
			input:    "  Test Job  ",
			expected: "Test Job",
		},
		{
			name:     "leading/trailing dots",
			input:    "..Test Job..",
			expected: "Test Job",
		},
		{
			name:     "long name gets truncated",
			input:    "This is a very long job name that exceeds the maximum allowed length for directory names and should be truncated to a reasonable size",
			expected: "This is a very long job name that exceeds the maximum allowed length for directory names and should",
		},
		{
			name:     "empty name",
			input:    "",
			expected: "unnamed_job",
		},
		{
			name:     "only special chars",
			input:    "...",
			expected: "unnamed_job",
		},
		{
			name:     "control characters replaced",
			input:    "Test\tJob\nName",
			expected: "Test_Job_Name",
		},
		{
			name:     "windows reserved name gets prefixed",
			input:    "CON",
			expected: "_CON",
		},
		{
			name:     "windows reserved name with extension gets prefixed",
			input:    "nul.txt",
			expected: "_nul.txt",
		},
		{
			name:     "reserved-looking substring is fine",
			input:    "CONFIG",
			expected: "CONFIG",
		},
		{name: "dots and spaces mixed at the end", input: "abc. .", expected: "abc"},
		{name: "spaces and dots mixed at both ends", input: " . abc . . ", expected: "abc"},
		{name: "console input device", input: "CONIN$", expected: "_CONIN$"},
		{name: "console output device with extension", input: "conout$.log", expected: "_conout$.log"},
		{name: "superscript port", input: "COM\u00b9", expected: "_COM\u00b9"},
		{name: "device name with spaces before the extension", input: "LPT3 .txt", expected: "_LPT3 .txt"},
		{name: "truncation keeps a two-byte character whole", input: strings.Repeat("a", 99) + "\u00e9\u00e9", expected: strings.Repeat("a", 99)},
		{name: "truncation keeps three-byte characters whole", input: strings.Repeat("\u65e5", 40), expected: strings.Repeat("\u65e5", 33)},
		{name: "truncation then trims what it exposes", input: strings.Repeat("a", 97) + " . " + strings.Repeat("b", 10), expected: strings.Repeat("a", 97)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeDirectoryName(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeDirectoryName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
			if err := validation.ValidateFilename(result); err != nil || !utf8.ValidString(result) {
				t.Errorf("sanitizeDirectoryName(%q) = %q, which is not a usable name: %v", tt.input, result, err)
			}
		})
	}
}

// TestWriteJobIDFile verifies the .jobid marker is written with the job ID and
// lands at the expected path inside the output directory.
func TestWriteJobIDFile(t *testing.T) {
	dir := t.TempDir()
	if err := WriteJobIDFile(dir, "abc123xyz"); err != nil {
		t.Fatalf("WriteJobIDFile: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, JobIDFileName))
	if err != nil {
		t.Fatalf("reading .jobid: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "abc123xyz" {
		t.Errorf(".jobid contents = %q, want %q", got, "abc123xyz")
	}
	if err := WriteJobIDFile(dir, "def456"); err != nil {
		t.Fatalf("rewriting .jobid: %v", err)
	}
	if got, _ := readJobIDFile(dir); got != "def456" {
		t.Errorf(".jobid after a rewrite = %q, want %q", got, "def456")
	}
}

// A symbolic link at the marker path is refused, not followed: the write would
// otherwise land wherever the link points. Reading through one is refused too.
func TestWriteJobIDFile_RefusesALink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	writeFile(t, victim, "abc123")
	marker := filepath.Join(dir, JobIDFileName)
	if err := os.Symlink(victim, marker); err != nil {
		t.Skipf("cannot make a symbolic link here: %v", err)
	}

	if err := WriteJobIDFile(dir, "def456"); err == nil {
		t.Error("WriteJobIDFile wrote through a symbolic link")
	}
	if got, _ := os.ReadFile(victim); string(got) != "abc123" {
		t.Errorf("the link's target now holds %q", got)
	}
	if info, err := os.Lstat(marker); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was not left in place: %v", err)
	}
	if id, ok := readJobIDFile(dir); ok {
		t.Errorf("readJobIDFile followed the link and read %q", id)
	}
}

// =============================================================================
// Eligibility Engine Tests
// =============================================================================

// TestSkipReasonCodeIsSilent asserts that every SkipReasonCode used by the
// daemon has a deterministic silent-vs-logged classification. If a new code
// is added and not classified in IsSilent, add it here and in the switch.
func TestSkipReasonCodeIsSilent(t *testing.T) {
	silent := map[SkipReasonCode]bool{
		ReasonNotCompleted:             true,
		ReasonAlreadyDownloadedLocal:   true,
		ReasonTooOldCreationPrefilter:  true,
		ReasonNameFilter:               true,
		ReasonAutoDownloadUnset:        true,
		ReasonAutoDownloadDisabled:     true,
		ReasonAutoDownloadUnrecognized: true,
		ReasonFieldCheckAPIError:       true,
		ReasonInRetryBackoff:           true,
		ReasonOutsideLookbackWindow:    false,
		// ReasonHasDownloadedTag is the common case on every poll, so it is
		// silent to avoid log noise; ReasonPendingTagApply is a transient
		// retry state.
		ReasonHasDownloadedTag:           true,
		ReasonPendingTagApply:            true,
		ReasonHasStartedTag:              false,
		ReasonClaimFailed:                false,
		ReasonConditionalMissingTag:      false,
		ReasonDownloadedTagCheckAPIError: false,
		ReasonCompletionTimeAPIError:     false,
		ReasonCompletionTimeDeferred:     true,
	}
	for code, want := range silent {
		if got := code.IsSilent(); got != want {
			t.Errorf("SkipReasonCode(%q).IsSilent() = %v, want %v", code, got, want)
		}
	}
	// ReasonNone is the zero value; classify it as silent (no reason = nothing
	// to log about).
	if !ReasonNone.IsSilent() {
		t.Errorf("ReasonNone.IsSilent() = false, want true")
	}
}

// A job whose files are down but whose downloaded tag is still being applied is
// skipped before any eligibility lookup, so the poll's tag retry is not raced
// by a second download of the same job.
func TestFindCompletedJobsSkipsJobsPendingTheirTag(t *testing.T) {
	p := newPlatform(t, &fakeJob{id: "pending-job", name: "pending"}, &fakeJob{id: "ready-job", name: "ready"})
	m := p.monitor(nil, EligibilityConfig{LookbackDays: 7})

	result, err := m.FindCompletedJobs(context.Background(), map[string]struct{}{"pending-job": {}})
	if err != nil {
		t.Fatalf("FindCompletedJobs: %v", err)
	}
	var candidates []string
	for _, job := range result.Candidates {
		candidates = append(candidates, job.ID)
	}
	if !slices.Equal(candidates, []string{"ready-job"}) {
		t.Errorf("candidates = %v, want only ready-job", candidates)
	}
	if n := result.Summary.SkipBuckets[ReasonPendingTagApply]; n != 1 {
		t.Errorf("skipped %d jobs as pending their tag, want 1 (buckets %v)", n, result.Summary.SkipBuckets)
	}
	// The ready job's completion time was looked up, so the pending one would have been.
	if lookups, tagReads := p.read(); !slices.Equal(lookups, []string{"ready-job"}) || len(tagReads) != 0 {
		t.Errorf("completion times looked up for %v, tags read for %v: want ready-job's completion time only, and nothing asked about pending-job", lookups, tagReads)
	}
}
