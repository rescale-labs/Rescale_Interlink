package events

import (
	"testing"
	"time"
)

func TestEventBus_PublishSubscribe(t *testing.T) {
	bus := NewEventBus(10)
	defer bus.Close()

	// Subscribe to progress events
	ch := bus.Subscribe(EventProgress)

	// Publish a progress event
	testEvent := &ProgressEvent{
		BaseEvent: BaseEvent{
			EventType: EventProgress,
			Time:      time.Now(),
		},
		JobName:  "test-job",
		Stage:    "tar",
		Progress: 0.5,
		Message:  "Test message",
	}

	bus.Publish(testEvent)

	// Receive the event
	select {
	case received := <-ch:
		progress, ok := received.(*ProgressEvent)
		if !ok {
			t.Fatal("Expected ProgressEvent")
		}
		if progress.JobName != "test-job" {
			t.Errorf("Expected job name 'test-job', got '%s'", progress.JobName)
		}
		if progress.Progress != 0.5 {
			t.Errorf("Expected progress 0.5, got %f", progress.Progress)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Timeout waiting for event")
	}
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	bus := NewEventBus(10)
	defer bus.Close()

	// Create multiple subscribers
	ch1 := bus.Subscribe(EventLog)
	ch2 := bus.Subscribe(EventLog)

	// Publish a log event
	testEvent := &LogEvent{
		BaseEvent: BaseEvent{
			EventType: EventLog,
			Time:      time.Now(),
		},
		Level:   InfoLevel,
		Message: "Test log",
		Stage:   "test",
	}

	bus.Publish(testEvent)

	// Both subscribers should receive it
	received1 := false
	received2 := false

	select {
	case <-ch1:
		received1 = true
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case <-ch2:
		received2 = true
	case <-time.After(100 * time.Millisecond):
	}

	if !received1 || !received2 {
		t.Error("Not all subscribers received the event")
	}
}

func TestEventBus_DifferentEventTypes(t *testing.T) {
	bus := NewEventBus(10)
	defer bus.Close()

	// Subscribe to different event types
	progressCh := bus.Subscribe(EventProgress)
	logCh := bus.Subscribe(EventLog)

	// Publish progress event
	bus.Publish(&ProgressEvent{
		BaseEvent: BaseEvent{EventType: EventProgress, Time: time.Now()},
		JobName:   "test",
	})

	// Only progress subscriber should receive it
	select {
	case <-progressCh:
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("Progress subscriber didn't receive event")
	}

	// Log subscriber should not receive it
	select {
	case <-logCh:
		t.Error("Log subscriber received wrong event type")
	case <-time.After(50 * time.Millisecond):
		// Expected - timeout means no event
	}
}

func TestEventBus_SubscribeAll(t *testing.T) {
	bus := NewEventBus(10)
	defer bus.Close()

	// Subscribe to all events
	allCh := bus.SubscribeAll()

	// Publish different event types
	bus.Publish(&ProgressEvent{
		BaseEvent: BaseEvent{EventType: EventProgress, Time: time.Now()},
	})

	bus.Publish(&LogEvent{
		BaseEvent: BaseEvent{EventType: EventLog, Time: time.Now()},
	})

	// Should receive both
	count := 0
	for i := 0; i < 2; i++ {
		select {
		case <-allCh:
			count++
		case <-time.After(100 * time.Millisecond):
			break
		}
	}

	if count != 2 {
		t.Errorf("Expected to receive 2 events, got %d", count)
	}
}

func TestEventBus_NonBlocking(t *testing.T) {
	bus := NewEventBus(2) // Small buffer
	defer bus.Close()

	ch := bus.Subscribe(EventProgress)

	// Fill the buffer
	for i := 0; i < 10; i++ {
		bus.Publish(&ProgressEvent{
			BaseEvent: BaseEvent{EventType: EventProgress, Time: time.Now()},
			JobName:   "test",
		})
	}

	// Should not block - excess events are dropped
	// Test passes if we get here without deadlock

	// Drain some events
	count := 0
	for {
		select {
		case <-ch:
			count++
		case <-time.After(10 * time.Millisecond):
			goto done
		}
	}
done:

	if count == 0 {
		t.Error("Should have received at least some events")
	}
}

func TestEventBus_Close(t *testing.T) {
	bus := NewEventBus(10)

	ch := bus.Subscribe(EventProgress)

	bus.Close()

	// Channel should be closed
	_, ok := <-ch
	if ok {
		t.Error("Channel should be closed after bus.Close()")
	}

	// Publishing after close should not panic
	bus.Publish(&ProgressEvent{
		BaseEvent: BaseEvent{EventType: EventProgress, Time: time.Now()},
	})
}

func TestLogLevel_String(t *testing.T) {
	tests := []struct {
		level    LogLevel
		expected string
	}{
		{DebugLevel, "DEBUG"},
		{InfoLevel, "INFO"},
		{WarnLevel, "WARN"},
		{ErrorLevel, "ERROR"},
	}

	for _, tt := range tests {
		if got := tt.level.String(); got != tt.expected {
			t.Errorf("Level %d: expected %s, got %s", tt.level, tt.expected, got)
		}
	}
}

func TestConvenienceMethods(t *testing.T) {
	bus := NewEventBus(10)
	defer bus.Close()

	logCh := bus.Subscribe(EventLog)
	progressCh := bus.Subscribe(EventProgress)
	stateCh := bus.Subscribe(EventStateChange)

	// Test PublishLog
	bus.PublishLog(InfoLevel, "test message", "test-stage", "test-job", nil)

	select {
	case event := <-logCh:
		log, ok := event.(*LogEvent)
		if !ok {
			t.Fatal("Expected LogEvent")
		}
		if log.Message != "test message" {
			t.Errorf("Expected 'test message', got '%s'", log.Message)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Timeout waiting for log event")
	}

	// Test PublishProgress
	bus.PublishProgress("job1", "upload", 0.75, "uploading")

	select {
	case event := <-progressCh:
		progress, ok := event.(*ProgressEvent)
		if !ok {
			t.Fatal("Expected ProgressEvent")
		}
		if progress.Progress != 0.75 {
			t.Errorf("Expected progress 0.75, got %f", progress.Progress)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Timeout waiting for progress event")
	}

	// Test PublishStateChange
	bus.PublishStateChange("job1", "pending", "running", "tar", "job-123", "")

	select {
	case event := <-stateCh:
		state, ok := event.(*StateChangeEvent)
		if !ok {
			t.Fatal("Expected StateChangeEvent")
		}
		if state.NewStatus != "running" {
			t.Errorf("Expected new status 'running', got '%s'", state.NewStatus)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Timeout waiting for state change event")
	}
}
