package http

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestExecuteWithRetry_Success verifies basic success case returns nil on first attempt.
func TestExecuteWithRetry_Success(t *testing.T) {
	ctx := context.Background()
	cfg := Config{
		MaxRetries:   3,
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
	}

	calls := 0
	err := ExecuteWithRetry(ctx, cfg, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

// TestExecuteWithRetry_FatalError verifies no retry on fatal errors.
func TestExecuteWithRetry_FatalError(t *testing.T) {
	ctx := context.Background()
	cfg := Config{
		MaxRetries:   5,
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
	}

	calls := 0
	err := ExecuteWithRetry(ctx, cfg, func() error {
		calls++
		return fmt.Errorf("400 bad request")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retry on fatal), got %d", calls)
	}
}

// TestExecuteWithRetry_ContextCancelledDuringSleep verifies retry returns quickly when context cancelled.
func TestExecuteWithRetry_ContextCancelledDuringSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := Config{
		MaxRetries:   5,
		InitialDelay: 5 * time.Second, // Long backoff to ensure we'd be sleeping
		MaxDelay:     30 * time.Second,
	}

	calls := 0
	start := time.Now()

	// Cancel context after a short delay (while retry is sleeping)
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := ExecuteWithRetry(ctx, cfg, func() error {
		calls++
		return fmt.Errorf("connection reset") // Network error, triggers backoff
	})

	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Should return quickly (within ~200ms), not wait for full backoff
	if elapsed > 1*time.Second {
		t.Errorf("expected quick return after context cancel, but took %v", elapsed)
	}

	// Should have attempted at least once
	if calls < 1 {
		t.Errorf("expected at least 1 call, got %d", calls)
	}
}

// TestExecuteWithRetry_InsufficientDeadline verifies early exit when deadline < backoff.
func TestExecuteWithRetry_InsufficientDeadline(t *testing.T) {
	// Set a deadline that's shorter than any reasonable backoff
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	cfg := Config{
		MaxRetries:   5,
		InitialDelay: 5 * time.Second, // Backoff will exceed deadline
		MaxDelay:     30 * time.Second,
	}

	calls := 0
	start := time.Now()

	err := ExecuteWithRetry(ctx, cfg, func() error {
		calls++
		return fmt.Errorf("timeout") // Network error, triggers backoff
	})

	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Should return quickly, not wait for full backoff
	if elapsed > 1*time.Second {
		t.Errorf("expected quick return due to insufficient deadline, but took %v", elapsed)
	}

	// Should have attempted at least once
	if calls < 1 {
		t.Errorf("expected at least 1 call, got %d", calls)
	}
}
