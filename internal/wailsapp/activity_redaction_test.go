package wailsapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/reporting"
	"github.com/rescale/rescale-int/internal/services"
)

// An Azure transport error and an S3 presigned GET quote their URL,
// credentials and all.
var credentialErrors = []string{
	`failed to stage block 0: Put "https://acct.blob.core.windows.net/container/file.bin?comp=block&sig=SECRETSIGNATURE%3D` +
		`&sp=rwdlac": dial tcp 192.0.2.1:443: connect: connection refused`,
	`Get "https://b.s3.amazonaws.com/k?X-Amz-Credential=SECRETKEY%2F20260923&X-Amz-Signature=SECRETSIGNATURE": EOF`,
}

// Whatever the GUI shows of an error carries no credential: the Activity tab's
// events, the job rows and run status it polls, its standard log on stderr, and
// the Activity export, which is the user's file.
func TestGUICarriesNoCredentials(t *testing.T) {
	a := appWithFinishedRun(t, "failed", "failed")
	st := a.engine.GetState()
	for i, text := range credentialErrors {
		js := st.GetState(i + 1)
		js.ErrorMessage = text
		if err := st.UpdateState(js); err != nil {
			t.Fatalf("UpdateState: %v", err)
		}
	}
	// A configured URL the connection test refuses, which a pasted link can sign.
	a.config = &config.Config{APIKey: "k", APIBaseURL: "https://acct.blob.core.windows.net/c/f?sp=r&sig=SECRETSIGNATURE"}
	connection := a.TestConnection().Error
	if !strings.Contains(connection, "invalid platform URL") {
		t.Errorf("the connection test says %q, want why it failed", connection)
	}
	shown := []string{a.GetRunStatus().Error, connection}
	for _, row := range a.GetJobRows() {
		shown = append(shown, row.Error)
	}

	// The standard log on stderr, and App.log's terminal line and interlink.log.
	out, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	logFile := &lumberjack.Logger{Filename: filepath.Join(t.TempDir(), "interlink.log")}
	defer logFile.Close()
	defer func(l *lumberjack.Logger, on bool) { fileLogger, fileLoggingEnabled = l, on }(fileLogger, fileLoggingEnabled)
	fileLogger, fileLoggingEnabled = logFile, true
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = out, out
	tee := stdLogTee(nil)

	for _, text := range credentialErrors {
		cause := errors.New(text)
		logged := logEventToDTO(&events.LogEvent{Message: "upload failed: " + text, Error: cause})
		shown = append(shown, logged.Message, logged.Error, transferEventToDTO(&events.TransferEvent{Error: cause}).Error,
			stateChangeEventToDTO(&events.StateChangeEvent{ErrorMessage: text}).ErrorMessage,
			errorEventToDTO(&events.ErrorEvent{Error: cause}).Message,
			transferTaskToDTO(services.TransferTask{Error: cause}).Error,
			enumerationEventToDTO(&events.EnumerationEvent{Error: text}).Error,
			scanProgressEventToDTO(&events.ScanProgressEvent{Error: text}).Error)
		fmt.Fprintln(tee, "upload failed: "+text)
		a.log("ERROR", "upload", "upload failed: "+text)
	}
	os.Stdout, os.Stderr = origOut, origErr
	printed, _ := os.ReadFile(out.Name())
	written, _ := os.ReadFile(logFile.Filename)
	for _, text := range append(shown, strings.Split(strings.TrimSpace(string(printed)+string(written)), "\n")...) {
		if strings.Contains(text, "SECRET") || !strings.Contains(text, "=REDACTED") {
			t.Errorf("the GUI shows %q, want the credentials redacted", text)
		}
	}
	// The GUI's own logger (a failed single-job upload, config errors) too.
	if wailsLogger == nil || wailsLogger.Output() != reporting.RedactWriter(os.Stderr) {
		t.Error("the GUI's logger writes to stderr without redacting credentials")
	}

	testLogger(t)
	path := filepath.Join(t.TempDir(), "activity.log")
	defer enablePortal(nil, nil, nil, func(string, string, string, []runtime.FileFilter) (string, error) { return path, nil }, nil)()
	if _, err := (&App{ctx: context.Background()}).SaveLogExport("[ERROR] " + strings.Join(credentialErrors, "\n[ERROR] ") + "\n"); err != nil {
		t.Fatalf("SaveLogExport: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "SECRET") {
		t.Errorf("the export holds %q (%v), want the credentials redacted", data, err)
	}
	if info, err := os.Stat(path); err == nil && goruntime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("the export has mode %v, want 0600", info.Mode().Perm())
	}
}

// A folder upload that cannot start says why, in its result and its row on the
// Transfers tab, quoting the API's answer, which a proxy's page can fill with
// signed URLs and credentials.
func TestFolderUploadErrorsCarryNoCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, strings.Join(credentialErrors, "\n")+"\nAuthorization: Token SECRETTOKEN")
	}))
	defer server.Close()
	defer func(f func(*App) *api.Client) { folderUploadAPI = f }(folderUploadAPI)
	client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})
	folderUploadAPI = func(*App) *api.Client { return client }
	a, eng := appWithEngine(t)
	scans := eng.Events().Subscribe(events.EventEnumerationCompleted)

	result := a.StartFolderUpload(t.TempDir(), "parent", nil)
	var scan EnumerationEventDTO
	select {
	case ev := <-scans:
		scan = enumerationEventToDTO(ev.(*events.EnumerationEvent))
	default:
		t.Fatal("the folder upload stopped without its row on the Transfers tab")
	}
	for _, text := range []string{result.Error, scan.Error} {
		if !strings.Contains(text, "Destination folder not found") || strings.Contains(text, "SECRET") {
			t.Errorf("the folder upload showed %q, want why it stopped without credentials", text)
		}
	}
}
