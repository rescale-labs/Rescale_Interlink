package cloud

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRetryEventNotice(t *testing.T) {
	base := RetryEvent{
		Operation:   "UploadPart 3",
		MaxAttempts: 10,
		Cause:       "retryable",
		Err:         errors.New("503 service unavailable"),
		NextDelay:   2500 * time.Millisecond,
	}

	tests := []struct {
		name     string
		attempt  int
		wantText bool
	}{
		{name: "first retry stays quiet", attempt: 1, wantText: false},
		{name: "second retry speaks up", attempt: 2, wantText: true},
		{name: "later retries speak up", attempt: 7, wantText: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := base
			ev.Attempt = tt.attempt
			msg := ev.Notice()
			if tt.wantText != (msg != "") {
				t.Fatalf("Notice() = %q, wantText %v", msg, tt.wantText)
			}
			if !tt.wantText {
				return
			}
			for _, want := range []string{"UploadPart 3", "retryable", "503 service unavailable", "2.5s"} {
				if !strings.Contains(msg, want) {
					t.Errorf("notice %q missing %q", msg, want)
				}
			}
		})
	}
}

func TestRetryObserverNotify(t *testing.T) {
	ev := RetryEvent{Operation: "PutObject", Attempt: 3, MaxAttempts: 10, Cause: "network",
		Err: errors.New("connection reset"), NextDelay: time.Second}

	t.Run("writes to the given writer", func(t *testing.T) {
		var buf bytes.Buffer
		RetryObserver{Writer: &buf}.Notify(ev)
		if !strings.Contains(buf.String(), "PutObject") {
			t.Errorf("expected the notice on the writer, got %q", buf.String())
		}
	})

	t.Run("early attempts write nothing", func(t *testing.T) {
		var buf bytes.Buffer
		early := ev
		early.Attempt = 1
		RetryObserver{Writer: &buf}.Notify(early)
		if buf.Len() != 0 {
			t.Errorf("expected no output for the first retry, got %q", buf.String())
		}
	})

	t.Run("caller hook takes over and sees every attempt", func(t *testing.T) {
		var buf bytes.Buffer
		var seen []int
		obs := RetryObserver{Writer: &buf, OnRetry: func(e RetryEvent) { seen = append(seen, e.Attempt) }}
		first := ev
		first.Attempt = 1
		obs.Notify(first)
		obs.Notify(ev)

		if len(seen) != 2 || seen[0] != 1 || seen[1] != 3 {
			t.Errorf("hook saw %v, want [1 3]", seen)
		}
		if buf.Len() != 0 {
			t.Errorf("writer must stay untouched when the caller renders: %q", buf.String())
		}
	})
}
