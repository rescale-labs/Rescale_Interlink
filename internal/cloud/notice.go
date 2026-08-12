package cloud

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/progress"
)

// NoticeFromAttempt is the retry number from which a retry is reported to the
// user. A single retry is routine on any network and reporting it would be
// noise; from the second one the transfer is visibly struggling and silence is
// what makes an upload look like a hang.
const NoticeFromAttempt = 2

// RetryEvent describes one retry of a cloud storage operation.
type RetryEvent struct {
	// Operation is the provider-level operation being retried, e.g. "UploadPart 7".
	Operation string
	// Attempt is the 1-based retry number (1 = first retry, i.e. second try).
	Attempt int
	// MaxAttempts is the total number of tries allowed.
	MaxAttempts int
	// Cause is the error class that triggered the retry: network, retryable,
	// credential (see http.ErrorTypeName).
	Cause string
	// Err is the failure that triggered this retry.
	Err error
	// NextDelay is how long the caller will wait before trying again.
	NextDelay time.Duration
}

// Notice returns the one-line message for this retry, or "" when the attempt is
// too early to be worth reporting. Callers that render their own output use this
// so the "when to speak up" rule lives in one place.
func (e RetryEvent) Notice() string {
	if e.Attempt < NoticeFromAttempt {
		return ""
	}
	return fmt.Sprintf("⟳ Retrying %s (attempt %d/%d, %s): %v — waiting %s",
		e.Operation, e.Attempt, e.MaxAttempts, e.Cause, e.Err, e.NextDelay.Round(100*time.Millisecond))
}

// RetryObserver carries a caller's retry-visibility hooks down to the provider
// clients, which is where retries actually happen. Both fields are optional.
type RetryObserver struct {
	// Writer receives the default retry line. When nil it goes to stderr.
	// Ignored when OnRetry is set.
	Writer io.Writer

	// OnRetry, when set, takes over presentation: the caller gets every retry
	// (including the first, so a progress bar can show "(retry 1)") and decides
	// what to display. Called on the transfer's goroutine — it must not block.
	OnRetry func(RetryEvent)
}

// Notify reports a retry. With no caller hook it writes the default line and
// publishes to the event bus so the GUI Activity log shows it.
func (o RetryObserver) Notify(ev RetryEvent) {
	if o.OnRetry != nil {
		o.OnRetry(ev)
		return
	}

	msg := ev.Notice()
	if msg == "" {
		return
	}

	// With no writer from the caller, go through whatever progress display is
	// live rather than straight to stderr. Compat mode draws its own bars and
	// passes no writer, and a raw stderr write lands inside mpb's frame — the
	// notice would fix one silence by shredding the bars instead.
	w := o.Writer
	if w == nil {
		w = progress.SinkWriter(os.Stderr)
	}
	fmt.Fprintf(w, "%s\n", msg)

	if eb := globalEventBus.Load(); eb != nil {
		eb.PublishLog(events.WarnLevel, msg, "transfer", "", nil)
	}
}

// RetryObserverSetter is implemented by providers that can route retry notices
// back to the caller. Used through a type assertion so the CloudTransfer
// interface stays unchanged.
type RetryObserverSetter interface {
	SetRetryObserver(RetryObserver)
}
