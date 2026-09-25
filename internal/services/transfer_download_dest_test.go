package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/transfer"
)

// The file browser builds Dest as the chosen folder plus the server's file
// name. The service applies the same name rule as every other download
// surface, holds Dest to ending in that name, and refuses a link there.
func TestGUIDownloadKeepsTheFileInTheChosenFolder(t *testing.T) {
	eventBus := events.NewEventBus(100)
	defer eventBus.Close()
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{MaxConcurrent: 1})

	dir := t.TempDir()
	// A link to a folder outside, named like the file: following it, as a
	// folder to append the name to, wrote the file outside the chosen folder.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "linked.txt")); err != nil {
		t.Skipf("cannot make a symbolic link here: %v", err)
	}
	tests := []struct {
		name, dest string
		refused    bool
	}{
		{"../escaped.txt", dir + "/../escaped.txt", true},
		{"report.txt", filepath.Join(dir, "other.txt"), true},
		{"linked.txt", filepath.Join(dir, "linked.txt"), true},
		{"report.txt", filepath.Join(dir, "report.txt"), false},
		{"report.txt", dir, false}, // a folder: the service appends the name
	}
	for _, tt := range tests {
		req := TransferRequest{Type: TransferTypeDownload, Source: "FAKEID", Dest: tt.dest, Name: tt.name}
		taskID := ts.registerDownloadTask(req)
		attempt, owned := ts.queue.BeginAttempt(taskID, transfer.NoAttempt)
		if !owned || !ts.queue.Activate(taskID) {
			t.Fatalf("%s: could not start the task", tt.name)
		}

		// No API client: a download that gets past the destination check stops
		// at DownloadFile's first check, before anything touches the network.
		ts.runDownload(taskExecution{
			ctx: context.Background(), taskCtx: context.Background(),
			req: req, taskID: taskID, attempt: attempt, fileName: tt.name, workerCount: 1,
		})

		task, _ := ts.queue.GetTask(taskID)
		if task.Error == nil {
			t.Fatalf("%s -> %s: the task did not fail", tt.name, tt.dest)
		}
		refused := !strings.Contains(task.Error.Error(), "API client is required")
		if refused != tt.refused {
			t.Errorf("%s -> %s: refused = %v (%v), want %v", tt.name, tt.dest, refused, task.Error, tt.refused)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escaped.txt")); !os.IsNotExist(err) {
		t.Errorf("a file appeared outside the chosen folder: %v", err)
	}
}

// A row recorded for a name refused before any transfer, the empty one
// included, fails again on retry without starting: a retry took the file ID
// for the missing name and fetched the file under it.
func TestRetryOfARefusedRowStartsNoTransfer(t *testing.T) {
	eventBus := events.NewEventBus(100)
	defer eventBus.Close()
	client := api.NewClientForTest(&config.Config{APIBaseURL: "http://127.0.0.1:1", APIKey: "test"})
	ts := NewTransferService(client, eventBus, TransferServiceConfig{MaxConcurrent: 1})
	ts.RecordRefusedDownload("BATCH1", "FOLDER", "", "FAKEID", t.TempDir(), errors.New("file at the top level not downloaded: filename cannot be empty"))
	tasks := ts.queue.GetTasks()
	if len(tasks) != 1 || tasks[0].State != transfer.TaskFailed {
		t.Fatalf("recorded %d rows, want one failed row", len(tasks))
	}
	if _, err := ts.queue.Retry(tasks[0].ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	var task transfer.TransferTask
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if task, _ = ts.queue.GetTask(tasks[0].ID); task.State == transfer.TaskFailed {
			break
		}
	}
	if task.State != transfer.TaskFailed || !task.StartedAt.IsZero() || task.Error == nil || !strings.Contains(task.Error.Error(), "filename cannot be empty") {
		t.Errorf("after a retry the row is %s, started %v, error %v; want it failed again for its name without starting", task.State, !task.StartedAt.IsZero(), task.Error)
	}
}
