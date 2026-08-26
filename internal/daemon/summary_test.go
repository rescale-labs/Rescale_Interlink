package daemon

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/logging"
)

// newTestDaemon returns a Daemon bare enough to drive the summary/warn helpers
// without touching polling, state, or API clients.
func newTestDaemon() (*Daemon, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := logging.NewLoggerWithWriter(&buf)
	return &Daemon{logger: logger}, &buf
}

// TestEmitScanSummary pins the single canonical summary line the daemon emits
// after every poll: the field set, the totals, and the field order.
func TestEmitScanSummary(t *testing.T) {
	tests := []struct {
		name        string
		summary     *ScanSummary
		elapsed     time.Duration
		interrupted bool
		err         error
		wantSubstrs []string
	}{
		{
			name: "empty scan",
			summary: &ScanSummary{
				TotalScanned:     0,
				SkipBuckets:      map[SkipReasonCode]int{},
				DownloadOutcomes: map[string]int{},
			},
			elapsed:     500 * time.Millisecond,
			wantSubstrs: []string{"scanned=0", "downloaded=0", "silent-skipped=0 (none)", "logged-skipped=0 (none)"},
		},
		{
			name: "mixed skip reasons and outcomes",
			summary: &ScanSummary{
				TotalScanned:       20,
				EligibilityChecked: 12,
				SkipBuckets: map[SkipReasonCode]int{
					ReasonNotCompleted:           5,
					ReasonAlreadyDownloadedLocal: 3,
					ReasonAutoDownloadUnset:      4,
					ReasonHasDownloadedTag:       2,
					ReasonConditionalMissingTag:  1,
				},
				DownloadOutcomes: map[string]int{
					string(OutcomeDownloaded):      3,
					string(OutcomePartialFailure):  1,
					string(OutcomeListFilesFailed): 1,
				},
			},
			elapsed: 2 * time.Second,
			wantSubstrs: []string{
				"scanned=20", "eligibility-checked=12", "downloaded=3", "failed=2",
				"partial=1", "list-failed=1", "not_completed=5", "already_downloaded_local=3",
				"auto_download_unset=4", "has_downloaded_tag=2", "conditional_missing_tag=1",
				"duration=2.0s",
				// ReasonHasDownloadedTag is silent (tag-first semantics make it the
				// common case every poll): silent = 5+3+4+2, logged = 1.
				"silent-skipped=14", "logged-skipped=1",
			},
		},
		{
			// The scan budget running out is both interrupted and an error, and
			// the field order must still hold. Quotes arrive escaped because the
			// buffer holds zerolog's JSON.
			name: "interrupted with an error",
			summary: &ScanSummary{
				TotalScanned:     3,
				SkipBuckets:      map[SkipReasonCode]int{},
				DownloadOutcomes: map[string]int{string(OutcomeDownloaded): 1},
			},
			elapsed:     600 * time.Second,
			interrupted: true,
			err:         errors.New("scan did not finish within 10m0s"),
			wantSubstrs: []string{`interrupted=true, duration=600.0s, error=\"scan did not finish within 10m0s\"`},
		},
		{
			name: "interrupted",
			summary: &ScanSummary{
				TotalScanned:     10,
				SkipBuckets:      map[SkipReasonCode]int{ReasonNotCompleted: 2},
				DownloadOutcomes: map[string]int{string(OutcomeInterrupted): 1},
			},
			elapsed:     1 * time.Second,
			interrupted: true,
			wantSubstrs: []string{"interrupted=true", "interrupted-jobs=1"},
		},
		{
			// A poll that fails before the job loop still emits the one canonical
			// line, with an error tag rather than a second bespoke format. The
			// error comes last and is quoted: errors routinely contain commas and
			// every other field is comma-delimited for grep/awk pipelines.
			name: "scan error before the job loop",
			summary: &ScanSummary{
				SkipBuckets:      map[SkipReasonCode]int{},
				DownloadOutcomes: map[string]int{},
			},
			elapsed: 3 * time.Second,
			err:     errors.New("list jobs: 503, retry exhausted"),
			wantSubstrs: []string{
				"Poll complete: scanned=0", "eligibility-checked=0", "downloaded=0",
				"no_files=0", "failed=0", "silent-skipped=0 (none)", "logged-skipped=0 (none)",
				`duration=3.0s, error=\"list jobs: 503, retry exhausted\"`,
			},
		},
		{
			// no_files must not fold into downloaded: a completed job with an
			// empty output set is not a download.
			name: "no_files is separate from downloaded",
			summary: &ScanSummary{
				TotalScanned:       4,
				EligibilityChecked: 4,
				SkipBuckets:        map[SkipReasonCode]int{},
				DownloadOutcomes: map[string]int{
					string(OutcomeDownloaded): 1,
					string(OutcomeNoFiles):    3,
				},
			},
			elapsed:     time.Second,
			wantSubstrs: []string{"downloaded=1", "no_files=3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, buf := newTestDaemon()
			d.emitScanSummary(tt.summary, tt.elapsed, tt.interrupted, tt.err)

			out := buf.String()
			for _, want := range tt.wantSubstrs {
				if !strings.Contains(out, want) {
					t.Errorf("summary missing %q: %s", want, out)
				}
			}
		})
	}
}

func TestScanErrorRecordAndClear(t *testing.T) {
	d, _ := newTestDaemon()

	if got, at := d.LastScanError(); got != "" || !at.IsZero() {
		t.Fatalf("fresh daemon reports scan error %q at %v, want none", got, at)
	}

	d.recordScanError(errors.New("list jobs failed: 503"))
	got, at := d.LastScanError()
	if got != "list jobs failed: 503" {
		t.Errorf("LastScanError = %q, want the recorded error", got)
	}
	if at.IsZero() {
		t.Error("LastScanError timestamp is zero, want the time of the failure")
	}

	d.recordScanError(nil)
	if got, _ = d.LastScanError(); got != "list jobs failed: 503" {
		t.Errorf("a nil error must not clear the recorded failure; got %q", got)
	}

	d.clearScanError()
	if got, at = d.LastScanError(); got != "" || !at.IsZero() {
		t.Errorf("after clearScanError: %q at %v, want none", got, at)
	}
}

// The scan budget covers scan work — listing and eligibility — not transfers. A
// download that legitimately runs longer than the whole budget must not be read
// as a stalled scan: doing so raised a standing "scan failed" error for a poll
// that was working correctly, which is what makes a real error easy to ignore.
func TestScanBudgetExceeded(t *testing.T) {
	const budget = 10 * time.Minute

	tests := []struct {
		name         string
		elapsed      time.Duration
		downloadTime time.Duration
		want         bool
	}{
		{
			name:         "one long download, brief scan work",
			elapsed:      35 * time.Minute,
			downloadTime: 34 * time.Minute,
			want:         false,
		},
		{
			name:         "scan work genuinely overran, downloads incidental",
			elapsed:      35 * time.Minute,
			downloadTime: 20 * time.Minute,
			want:         true,
		},
		{
			name:    "quick poll, no downloads",
			elapsed: 5 * time.Minute,
			want:    false,
		},
		{
			name:    "exactly at the budget is not over it",
			elapsed: budget,
			want:    false,
		},
		{
			name:    "a hair over the budget",
			elapsed: budget + time.Second,
			want:    true,
		},
		{
			name:         "every second spent downloading",
			elapsed:      30 * time.Minute,
			downloadTime: 30 * time.Minute,
			want:         false,
		},
		{
			name:         "download time exceeding elapsed is not trusted negative",
			elapsed:      time.Minute,
			downloadTime: time.Hour,
			want:         false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scanBudgetExceeded(tc.elapsed, tc.downloadTime, budget); got != tc.want {
				t.Errorf("scanBudgetExceeded(%s, %s, %s) = %v, want %v",
					tc.elapsed, tc.downloadTime, budget, got, tc.want)
			}
		})
	}
}

// A poll that was cut short still did work, so its progress is written and its
// timestamp advances; the recorded scan error is what marks it incomplete. If
// this did not persist, a budget-killed poll would silently lose the timestamp
// while its downloads had already mutated state.
func TestPersistPollProgress(t *testing.T) {
	var buf bytes.Buffer
	stateFile := filepath.Join(t.TempDir(), "state.json")
	d := &Daemon{
		logger: logging.NewLoggerWithWriter(&buf),
		state:  NewState(stateFile),
	}
	d.state.MarkDownloaded("job1", "Job One", "/out", 1, 10)

	before := time.Now()
	d.persistPollProgress()

	if last := d.state.GetLastPoll(); last.Before(before) {
		t.Errorf("LastPoll = %v, want at or after %v", last, before)
	}

	reloaded := NewState(stateFile)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := reloaded.Downloaded["job1"]; !ok {
		t.Error("state was not written to disk")
	}
	if reloaded.GetLastPoll().IsZero() {
		t.Error("LastPoll was not persisted")
	}
}

// TriggerPoll must say when it did not start a scan. Reporting success for a
// scan that never ran is what made a wedged poll invisible from the GUI.
func TestTriggerPollReportsWhyItDidNotRun(t *testing.T) {
	d, _ := newTestDaemon()

	if err := d.TriggerPoll(); err == nil {
		t.Error("TriggerPoll on a stopped daemon returned nil, want an error")
	}

	d.mu.Lock()
	d.running = true
	d.mu.Unlock()

	d.SetPaused(true)
	if err := d.TriggerPoll(); err == nil {
		t.Error("TriggerPoll while paused returned nil, want an error")
	}
	d.SetPaused(false)

	// Simulate a poll in progress (also what a wedged poll looks like).
	if !d.polling.CompareAndSwap(false, true) {
		t.Fatal("polling flag was already set")
	}
	if err := d.TriggerPoll(); err == nil {
		t.Error("TriggerPoll with a poll in progress returned nil, want an error")
	}
	d.polling.Store(false)
}

// TestCheckAllUnsetWarning fires only when every eligibility-checked job had
// the custom field unset, which is the signature of a misconfigured account.
func TestCheckAllUnsetWarning(t *testing.T) {
	const warnPhrase = "had 'Auto Download' custom field unset"

	tests := []struct {
		name     string
		summary  *ScanSummary
		wantWarn string // when set, the log must contain this; when empty, nothing may be logged
	}{
		{
			name:     "all unset",
			summary:  &ScanSummary{EligibilityChecked: 7, SkipBuckets: map[SkipReasonCode]int{ReasonAutoDownloadUnset: 7}},
			wantWarn: "All 7 eligibility-checked jobs had 'Auto Download' custom field unset",
		},
		{
			name: "mixed reasons",
			summary: &ScanSummary{EligibilityChecked: 7, SkipBuckets: map[SkipReasonCode]int{
				ReasonAutoDownloadUnset:    5,
				ReasonAutoDownloadDisabled: 2,
			}},
		},
		{
			name:    "nothing checked",
			summary: &ScanSummary{EligibilityChecked: 0, SkipBuckets: map[SkipReasonCode]int{ReasonAutoDownloadUnset: 0}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, buf := newTestDaemon()
			d.checkAllUnsetWarning(tt.summary)

			out := buf.String()
			if tt.wantWarn != "" {
				if !strings.Contains(out, tt.wantWarn) {
					t.Errorf("expected all-unset WARN %q, got: %s", tt.wantWarn, out)
				}
				return
			}
			if strings.Contains(out, warnPhrase) {
				t.Errorf("WARN should not fire for %s; got: %s", tt.name, out)
			}
		})
	}
}
