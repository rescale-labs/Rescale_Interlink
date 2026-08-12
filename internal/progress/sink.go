package progress

import (
	"io"
	"sync"
)

// While progress bars are live, mpb owns the terminal: it redraws its frame on a
// timer and anything written straight to the same terminal lands in the middle of
// that frame, which is what turns one progress bar into a screenful of half-drawn
// ones. mpb's Progress is itself an io.Writer that interleaves a line above the
// bars safely, so log output has somewhere correct to go — it just has to find
// it. These functions are that lookup: the active UI registers itself, and
// writers built by SinkWriter follow it without the log setup having to know
// whether a transfer is running.
var (
	sinkMu  sync.Mutex
	logSink io.Writer
)

// SetLogSink registers w as the writer that log output should pass through for as
// long as bars are live. Called by the upload and download UIs.
func SetLogSink(w io.Writer) {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	logSink = w
}

// ClearLogSink removes w if it is still the active sink. The writer is passed
// back so a UI finishing late cannot clear a newer UI's sink.
func ClearLogSink(w io.Writer) {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	if logSink == w {
		logSink = nil
	}
}

// LogSink returns the writer for the active progress display, or nil when no
// bars are live.
func LogSink() io.Writer {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	return logSink
}

// SinkWriter returns a writer that sends each write to the active progress
// display when there is one and to fallback otherwise. It resolves the sink per
// write, so it can be installed once (log.SetOutput) and will follow progress
// bars as they come and go.
func SinkWriter(fallback io.Writer) io.Writer {
	return sinkRouter{fallback: fallback}
}

type sinkRouter struct {
	fallback io.Writer
}

func (s sinkRouter) Write(p []byte) (int, error) {
	if w := LogSink(); w != nil {
		return w.Write(p)
	}
	if s.fallback == nil {
		return len(p), nil
	}
	return s.fallback.Write(p)
}
