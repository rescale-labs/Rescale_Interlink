package wailsapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/events"
)

// logFileForTest points interlink.log at a fresh file for one test.
func logFileForTest(t *testing.T) string {
	t.Helper()
	logFile := &lumberjack.Logger{Filename: filepath.Join(t.TempDir(), "interlink.log")}
	orig, on := fileLogger, fileLoggingEnabled
	fileLogger, fileLoggingEnabled = logFile, true
	t.Cleanup(func() { logFile.Close(); fileLogger, fileLoggingEnabled = orig, on })
	return logFile.Filename
}

// appWithBridge is an App whose running event bridge hands the frontend's
// events to emit. The test stops the bridge.
func appWithBridge(t *testing.T, emit func(context.Context, string, ...interface{})) *App {
	t.Helper()
	orig := eventsEmit
	eventsEmit = emit
	t.Cleanup(func() { eventsEmit = orig })
	eng, err := core.NewEngine(nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	a := &App{engine: eng, eventBridge: NewEventBridge(context.Background(), eng.Events())}
	if err := a.eventBridge.Start(); err != nil {
		t.Fatal(err)
	}
	return a
}

// "Save logs to file" keeps what the Activity tab shows: a retry and a failure
// the transfer layer publishes reach interlink.log once each, redacted, and a
// message App.log writes is not written twice.
func TestLogFileGetsEveryLogEventOnce(t *testing.T) {
	path := logFileForTest(t)
	forwarded := make(chan struct{}, 8)
	a := appWithBridge(t, func(_ context.Context, name string, _ ...interface{}) {
		if name == "interlink:log" {
			forwarded <- struct{}{}
		}
	})
	cloud.SetEventBus(a.engine.Events())
	defer cloud.SetEventBus(nil)

	cloud.RetryObserver{Writer: io.Discard}.Notify(cloud.RetryEvent{
		Operation: "StageBlock 0", Attempt: 2, MaxAttempts: 10, Cause: "network",
		Err: errors.New("connection refused"), NextDelay: time.Second,
	})
	a.engine.Events().PublishLog(events.ErrorLevel,
		`Upload failed: Put "https://acct.blob.core.windows.net/c/f?sig=FAKESIG": EOF`, "transfer", "", nil)
	a.logInfo("upload", "Starting upload of data.bin")
	for range 3 {
		select {
		case <-forwarded:
		case <-time.After(5 * time.Second):
			t.Fatal("the event bridge did not forward the log events")
		}
	}
	a.eventBridge.Stop()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	written := string(data)
	for _, want := range []string{"[WARN] transfer: ⟳ Retrying StageBlock 0 (attempt 2/10", "[ERROR] transfer: Upload failed:", "[INFO] upload: Starting upload of data.bin"} {
		if n := strings.Count(written, want); n != 1 {
			t.Errorf("interlink.log holds %q %d times, want once:\n%s", want, n, written)
		}
	}
	if strings.Contains(written, "FAKESIG") {
		t.Errorf("interlink.log holds a credential:\n%s", written)
	}
}

// A shutdown with log events still queued loses none of them from
// interlink.log, and writes none twice: App.log's lines are in the file as
// soon as they are logged, even those the event bus had no room for, and the
// bridge writes what the rest of the app left queued.
func TestLogFileKeepsABurstAtShutdown(t *testing.T) {
	path := logFileForTest(t)
	a := appWithBridge(t, func(context.Context, string, ...interface{}) { time.Sleep(time.Millisecond) })
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	defer func(orig *os.File) { os.Stdout = orig }(os.Stdout)
	os.Stdout = devNull // App.log prints every line too

	const fromBus, fromApp = 400, 1500 // together more than the bus buffers for the bridge
	for i := range fromBus {
		a.engine.Events().PublishLog(events.InfoLevel, fmt.Sprintf("bus-%04d", i), "transfer", "", nil)
	}
	for i := range fromApp {
		a.logInfo("upload", fmt.Sprintf("app-%04d", i))
	}
	a.eventBridge.Stop()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	written := string(data)
	for source, n := range map[string]int{"bus": fromBus, "app": fromApp} {
		missing, doubled := 0, 0
		for i := range n {
			switch strings.Count(written, fmt.Sprintf("%s-%04d\n", source, i)) {
			case 0:
				missing++
			case 1:
			default:
				doubled++
			}
		}
		if missing+doubled > 0 {
			t.Errorf("of %d lines from %s, interlink.log misses %d and holds %d twice", n, source, missing, doubled)
		}
	}
}
