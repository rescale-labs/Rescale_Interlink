// Package progress provides a unified interface for progress reporting
// across CLI (progress bars) and GUI (event bus) modes.
package progress

import (
	"fmt"
	"io"
	"os"

	"github.com/schollz/progressbar/v3"

	"github.com/rescale/rescale-int/internal/events"
)

// Reporter is the interface for reporting progress in both CLI and GUI modes.
type Reporter interface {
	Start(total int64, description string)
	Update(current int64)
	Finish()
	Error(err error)
	SetDescription(desc string)
}

// CLIProgress implements progress reporting for CLI mode using progress bars.
type CLIProgress struct {
	bar *progressbar.ProgressBar
}

// NewCLIProgress creates a new CLI progress reporter.
func NewCLIProgress() *CLIProgress {
	return &CLIProgress{}
}

// Start initializes the progress bar with total size and description.
func (p *CLIProgress) Start(total int64, description string) {
	p.bar = progressbar.NewOptions64(total,
		progressbar.OptionSetDescription(description),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetWidth(50),
		progressbar.OptionThrottle(100),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprint(os.Stderr, "\n")
		}),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionSetRenderBlankState(true),
	)
}

// Update updates the progress bar to the current position.
func (p *CLIProgress) Update(current int64) {
	if p.bar != nil {
		_ = p.bar.Set64(current)
	}
}

// Finish completes the progress bar.
func (p *CLIProgress) Finish() {
	if p.bar != nil {
		_ = p.bar.Finish()
	}
}

// Error displays an error message.
func (p *CLIProgress) Error(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
	}
}

// SetDescription updates the progress bar description.
func (p *CLIProgress) SetDescription(desc string) {
	if p.bar != nil {
		p.bar.Describe(desc)
	}
}

// GUIProgress implements progress reporting for GUI mode using event bus.
type GUIProgress struct {
	eventBus *events.EventBus
	jobName  string
	total    int64
	current  int64
}

// NewGUIProgress creates a new GUI progress reporter.
func NewGUIProgress(eventBus *events.EventBus, jobName string) *GUIProgress {
	return &GUIProgress{
		eventBus: eventBus,
		jobName:  jobName,
	}
}

// Start initializes progress tracking.
func (p *GUIProgress) Start(total int64, description string) {
	p.total = total
	p.current = 0
	p.eventBus.Publish(&events.ProgressEvent{
		JobName:      p.jobName,
		Stage:        description,
		BytesTotal:   total,
		BytesCurrent: 0,
	})
}

// Update publishes progress update to event bus.
func (p *GUIProgress) Update(current int64) {
	p.current = current
	p.eventBus.Publish(&events.ProgressEvent{
		JobName:      p.jobName,
		BytesCurrent: current,
		BytesTotal:   p.total,
	})
}

// Finish publishes completion event.
func (p *GUIProgress) Finish() {
	p.eventBus.Publish(&events.ProgressEvent{
		JobName:      p.jobName,
		BytesCurrent: p.total,
		BytesTotal:   p.total,
	})
}

// Error publishes error event.
func (p *GUIProgress) Error(err error) {
	if err != nil {
		p.eventBus.Publish(&events.ErrorEvent{
			JobName: p.jobName,
			Error:   err,
		})
	}
}

// SetDescription updates the stage description.
func (p *GUIProgress) SetDescription(desc string) {
	p.eventBus.Publish(&events.ProgressEvent{
		JobName: p.jobName,
		Stage:   desc,
	})
}

// NoOpProgress is a progress reporter that does nothing (for background/silent operations).
type NoOpProgress struct{}

// NewNoOpProgress creates a new no-op progress reporter.
func NewNoOpProgress() *NoOpProgress {
	return &NoOpProgress{}
}

// Start does nothing.
func (p *NoOpProgress) Start(total int64, description string) {}

// Update does nothing.
func (p *NoOpProgress) Update(current int64) {}

// Finish does nothing.
func (p *NoOpProgress) Finish() {}

// Error does nothing.
func (p *NoOpProgress) Error(err error) {}

// SetDescription does nothing.
func (p *NoOpProgress) SetDescription(desc string) {}

// ProgressReader wraps an io.Reader to report progress.
type ProgressReader struct {
	reader   io.Reader
	reporter Reporter
	total    int64
	current  int64
}

// NewProgressReader creates a new progress-reporting reader.
func NewProgressReader(reader io.Reader, total int64, reporter Reporter) *ProgressReader {
	return &ProgressReader{
		reader:   reader,
		reporter: reporter,
		total:    total,
		current:  0,
	}
}

// Read implements io.Reader interface with progress reporting.
func (pr *ProgressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.current += int64(n)
	pr.reporter.Update(pr.current)
	return n, err
}
