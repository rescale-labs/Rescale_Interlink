package services

import (
	"testing"

	"github.com/rescale/rescale-int/internal/events"
)

func TestNewTransferService(t *testing.T) {
	eventBus := events.NewEventBus(100)

	// Test with default config
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{})
	if ts == nil {
		t.Fatal("NewTransferService returned nil")
	}

	// Queue should be initialized
	if ts.queue == nil {
		t.Error("Queue not initialized")
	}

	// Semaphore should be initialized with default capacity (MaxMaxConcurrent=20)
	// v4.8.0: Default changed from 5 to 20 for adaptive concurrency
	if cap(ts.semaphore) != 20 {
		t.Errorf("Semaphore capacity = %d, want 20", cap(ts.semaphore))
	}
}

func TestNewTransferServiceWithCustomConcurrency(t *testing.T) {
	eventBus := events.NewEventBus(100)

	ts := NewTransferService(nil, eventBus, TransferServiceConfig{
		MaxConcurrent: 10,
	})
	if ts == nil {
		t.Fatal("NewTransferService returned nil")
	}

	if cap(ts.semaphore) != 10 {
		t.Errorf("Semaphore capacity = %d, want 10", cap(ts.semaphore))
	}
}

func TestTransferStats(t *testing.T) {
	eventBus := events.NewEventBus(100)
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{})

	stats := ts.GetStats()
	if stats.Total() != 0 {
		t.Errorf("Initial stats total = %d, want 0", stats.Total())
	}
}

func TestGetQueue(t *testing.T) {
	eventBus := events.NewEventBus(100)
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{})

	queue := ts.GetQueue()
	if queue == nil {
		t.Error("GetQueue returned nil")
	}
}

func TestGetSemaphore(t *testing.T) {
	eventBus := events.NewEventBus(100)
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{
		MaxConcurrent: 3,
	})

	sem := ts.GetSemaphore()
	if sem == nil {
		t.Error("GetSemaphore returned nil")
	}
	if cap(sem) != 3 {
		t.Errorf("Semaphore capacity = %d, want 3", cap(sem))
	}
}

func TestClearCompleted(t *testing.T) {
	eventBus := events.NewEventBus(100)
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{})

	// Should not panic with empty queue
	ts.ClearCompleted()

	stats := ts.GetStats()
	if stats.Total() != 0 {
		t.Errorf("Stats total after clear = %d, want 0", stats.Total())
	}
}

func TestCancelAllEmpty(t *testing.T) {
	eventBus := events.NewEventBus(100)
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{})

	// Should not panic with empty queue
	ts.CancelAll()
}
