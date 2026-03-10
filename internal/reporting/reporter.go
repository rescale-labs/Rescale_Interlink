package reporting

import (
	"time"

	"github.com/rescale/rescale-int/internal/events"
)

// Reporter is a convenience wrapper for the GUI path.
// It holds a Builder and an EventBus reference for timeline snapshots.
type Reporter struct {
	builder  *Builder
	eventBus *events.EventBus
}

// NewReporter creates a Reporter for GUI use.
func NewReporter(eventBus *events.EventBus) *Reporter {
	return &Reporter{
		builder:  NewBuilder("gui"),
		eventBus: eventBus,
	}
}

// Report classifies an error and, if reportable, snapshots the timeline
// from the ring buffer, redacts it, and publishes a ReportableErrorEvent
// carrying the full classified error data + timeline.
// Returns the errorID if reportable, "" if not.
func (r *Reporter) Report(err error, category ErrorCategory, operation, backend string) string {
	if !IsReportable(err, category) {
		return ""
	}

	classified := Classify(err, category, operation, backend)
	if classified == nil {
		return ""
	}

	// Snapshot and redact timeline from ring buffer
	var timeline []events.SanitizedTimelineEntry
	if r.eventBus != nil {
		raw := r.eventBus.RecentEvents()
		timeline = RedactTimeline(raw, 20)
	}

	// Publish the event with full context
	if r.eventBus != nil {
		r.eventBus.Publish(&events.ReportableErrorEvent{
			BaseEvent: events.BaseEvent{
				EventType: events.EventReportableError,
				Time:      time.Now(),
			},
			ErrorID:      classified.ErrorID,
			Category:     string(classified.Category),
			Severity:     string(classified.Severity),
			Operation:    classified.Operation,
			Backend:      classified.Backend,
			ErrorMessage: classified.ErrorMessage,
			ErrorClass:   string(classified.ErrorClass),
			Timeline:     timeline,
		})
	}

	return classified.ErrorID
}

// ClassifyAndPublish is a standalone function for lower layers (Engine, TransferService)
// that don't have a Reporter reference. It snapshots the ring buffer, redacts,
// classifies, and publishes the ReportableErrorEvent. Same behavior as Reporter.Report()
// but takes an explicit *events.EventBus.
func ClassifyAndPublish(eventBus *events.EventBus, err error, category ErrorCategory, operation, backend string) string {
	if eventBus == nil || !IsReportable(err, category) {
		return ""
	}

	classified := Classify(err, category, operation, backend)
	if classified == nil {
		return ""
	}

	raw := eventBus.RecentEvents()
	timeline := RedactTimeline(raw, 20)

	eventBus.Publish(&events.ReportableErrorEvent{
		BaseEvent: events.BaseEvent{
			EventType: events.EventReportableError,
			Time:      time.Now(),
		},
		ErrorID:      classified.ErrorID,
		Category:     string(classified.Category),
		Severity:     string(classified.Severity),
		Operation:    classified.Operation,
		Backend:      classified.Backend,
		ErrorMessage: classified.ErrorMessage,
		ErrorClass:   string(classified.ErrorClass),
		Timeline:     timeline,
	})

	return classified.ErrorID
}
