package daemon

import (
	"bytes"
	"errors"
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

func TestEmitScanSummary_EmptyScan(t *testing.T) {
	d, buf := newTestDaemon()

	s := &ScanSummary{
		TotalScanned:     0,
		SkipBuckets:      map[SkipReasonCode]int{},
		DownloadOutcomes: map[string]int{},
	}
	d.emitScanSummary(s, 500*time.Millisecond, false, nil)

	out := buf.String()
	if !strings.Contains(out, "scanned=0") {
		t.Errorf("summary missing scanned=0: %s", out)
	}
	if !strings.Contains(out, "downloaded=0") {
		t.Errorf("summary missing downloaded=0: %s", out)
	}
	if !strings.Contains(out, "silent-skipped=0 (none)") {
		t.Errorf("summary missing silent-skipped=0 (none): %s", out)
	}
	if !strings.Contains(out, "logged-skipped=0 (none)") {
		t.Errorf("summary missing logged-skipped=0 (none): %s", out)
	}
}

func TestEmitScanSummary_MixedReasonsAndOutcomes(t *testing.T) {
	d, buf := newTestDaemon()

	s := &ScanSummary{
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
	}
	d.emitScanSummary(s, 2*time.Second, false, nil)

	out := buf.String()
	for _, want := range []string{
		"scanned=20",
		"eligibility-checked=12",
		"downloaded=3",
		"failed=2",
		"partial=1",
		"list-failed=1",
		"not_completed=5",
		"already_downloaded_local=3",
		"auto_download_unset=4",
		"has_downloaded_tag=2",
		"conditional_missing_tag=1",
		"duration=2.0s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q: %s", want, out)
		}
	}

	// Plan 3: ReasonHasDownloadedTag is now silent (tag-first semantics
	// make it the common case every poll). Silent = 5+3+4+2 = 14,
	// logged = 1 (conditional_missing_tag only).
	if !strings.Contains(out, "silent-skipped=14") {
		t.Errorf("summary silent-skipped total incorrect: %s", out)
	}
	if !strings.Contains(out, "logged-skipped=1") {
		t.Errorf("summary logged-skipped total incorrect: %s", out)
	}
}

func TestEmitScanSummary_Interrupted(t *testing.T) {
	d, buf := newTestDaemon()

	s := &ScanSummary{
		TotalScanned: 10,
		SkipBuckets:  map[SkipReasonCode]int{ReasonNotCompleted: 2},
		DownloadOutcomes: map[string]int{
			string(OutcomeInterrupted): 1,
		},
	}
	d.emitScanSummary(s, 1*time.Second, true, nil)

	out := buf.String()
	if !strings.Contains(out, "interrupted=true") {
		t.Errorf("summary missing interrupted=true: %s", out)
	}
	if !strings.Contains(out, "interrupted-jobs=1") {
		t.Errorf("summary missing interrupted-jobs=1: %s", out)
	}
}

// A poll that fails before the job loop must still emit the one canonical
// summary line, with an error tag rather than a second bespoke format.
func TestEmitScanSummary_ScanError(t *testing.T) {
	d, buf := newTestDaemon()

	s := &ScanSummary{
		SkipBuckets:      map[SkipReasonCode]int{},
		DownloadOutcomes: map[string]int{},
	}
	d.emitScanSummary(s, 3*time.Second, false, errors.New("list jobs failed: 503"))

	out := buf.String()
	for _, want := range []string{
		"Poll complete: scanned=0",
		"eligibility-checked=0",
		"downloaded=0",
		"no_files=0",
		"failed=0",
		"silent-skipped=0 (none)",
		"logged-skipped=0 (none)",
		"error=list jobs failed: 503",
		"duration=3.0s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scan-error summary missing %q: %s", want, out)
		}
	}
}

// no_files must not be folded into the downloaded count: a completed job with
// an empty output set is not a download.
func TestEmitScanSummary_NoFilesIsSeparateFromDownloaded(t *testing.T) {
	d, buf := newTestDaemon()

	s := &ScanSummary{
		TotalScanned:       4,
		EligibilityChecked: 4,
		SkipBuckets:        map[SkipReasonCode]int{},
		DownloadOutcomes: map[string]int{
			string(OutcomeDownloaded): 1,
			string(OutcomeNoFiles):    3,
		},
	}
	d.emitScanSummary(s, time.Second, false, nil)

	out := buf.String()
	if !strings.Contains(out, "downloaded=1") {
		t.Errorf("summary should report downloaded=1: %s", out)
	}
	if !strings.Contains(out, "no_files=3") {
		t.Errorf("summary should report no_files=3: %s", out)
	}
}

// The scan error must survive until the next completed scan clears it: that is
// what lets a status query distinguish a healthy daemon from one that is alive
// but failing every poll.
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

func TestCheckAllUnsetWarning_Fires(t *testing.T) {
	d, buf := newTestDaemon()

	s := &ScanSummary{
		EligibilityChecked: 7,
		SkipBuckets:        map[SkipReasonCode]int{ReasonAutoDownloadUnset: 7},
	}
	d.checkAllUnsetWarning(s)

	if !strings.Contains(buf.String(), "All 7 eligibility-checked jobs had 'Auto Download' custom field unset") {
		t.Errorf("expected all-unset WARN, got: %s", buf.String())
	}
}

func TestCheckAllUnsetWarning_DoesNotFireWhenMixed(t *testing.T) {
	d, buf := newTestDaemon()

	s := &ScanSummary{
		EligibilityChecked: 7,
		SkipBuckets: map[SkipReasonCode]int{
			ReasonAutoDownloadUnset:    5,
			ReasonAutoDownloadDisabled: 2,
		},
	}
	d.checkAllUnsetWarning(s)

	if strings.Contains(buf.String(), "had 'Auto Download' custom field unset") {
		t.Errorf("WARN should not fire when not all unset; got: %s", buf.String())
	}
}

func TestCheckAllUnsetWarning_DoesNotFireWhenNoneChecked(t *testing.T) {
	d, buf := newTestDaemon()

	s := &ScanSummary{
		EligibilityChecked: 0,
		SkipBuckets:        map[SkipReasonCode]int{ReasonAutoDownloadUnset: 0},
	}
	d.checkAllUnsetWarning(s)

	if buf.Len() != 0 {
		t.Errorf("WARN should not fire when EligibilityChecked=0; got: %s", buf.String())
	}
}
