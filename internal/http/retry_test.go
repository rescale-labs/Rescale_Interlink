package http

import (
	"context"
	"fmt"
	"net"
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

// TestClassifyError verifies all error classification paths including DNS errors.
func TestClassifyError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected ErrorType
	}{
		// DNS errors — type-based
		{"dns type-based", &net.DNSError{Err: "no such host", Name: "example.com"}, ErrorTypeNetwork},
		{"dns temporary", &net.DNSError{Err: "server misbehaving", Name: "api.rescale.com", IsTemporary: true}, ErrorTypeNetwork},

		// DNS errors — string fallbacks (SDK-wrapped)
		{"dns no such host string", fmt.Errorf("dial tcp: lookup prod-rescale-platform.s3.us-east-1.amazonaws.com: no such host"), ErrorTypeNetwork},
		{"dns temporary failure string", fmt.Errorf("temporary failure in name resolution"), ErrorTypeNetwork},
		{"dns server misbehaving string", fmt.Errorf("lookup api.rescale.com: server misbehaving"), ErrorTypeNetwork},
		{"dns nodename macOS string", fmt.Errorf("nodename nor servname provided, or not known"), ErrorTypeNetwork},

		// Existing network errors
		{"connection reset", fmt.Errorf("connection reset by peer"), ErrorTypeNetwork},
		{"timeout", fmt.Errorf("i/o timeout"), ErrorTypeNetwork},
		{"eof", fmt.Errorf("unexpected eof"), ErrorTypeNetwork},
		{"connection refused", fmt.Errorf("connection refused"), ErrorTypeNetwork},
		{"broken pipe", fmt.Errorf("broken pipe"), ErrorTypeNetwork},
		{"tls handshake timeout", fmt.Errorf("tls handshake timeout"), ErrorTypeNetwork},
		{"context deadline", context.DeadlineExceeded, ErrorTypeNetwork},
		{"net.Error timeout", &net.OpError{Err: &timeoutErr{}}, ErrorTypeNetwork},

		// Credential errors
		{"403 forbidden", fmt.Errorf("403 forbidden"), ErrorTypeCredential},
		{"expired token", fmt.Errorf("token expired"), ErrorTypeCredential},

		// Retryable errors
		{"500 server error", fmt.Errorf("500 internal server error"), ErrorTypeRetryable},
		{"503 unavailable", fmt.Errorf("503 service unavailable"), ErrorTypeRetryable},
		{"throttling", fmt.Errorf("request throttled"), ErrorTypeRetryable},

		// Fatal errors
		{"400 bad request", fmt.Errorf("400 bad request"), ErrorTypeFatal},
		{"context canceled", context.Canceled, ErrorTypeFatal},
		{"proxy 407", fmt.Errorf("407 proxy authentication required"), ErrorTypeFatal},
		{"unknown error", fmt.Errorf("something completely unexpected"), ErrorTypeFatal},

		// nil
		{"nil error", nil, ErrorTypeSuccess},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyError(tt.err)
			if got != tt.expected {
				t.Errorf("ClassifyError(%v) = %v (%s), want %v (%s)",
					tt.err, got, ErrorTypeName(got), tt.expected, ErrorTypeName(tt.expected))
			}
		})
	}
}

// timeoutErr implements net.Error with Timeout() = true for testing.
type timeoutErr struct{}

func (e *timeoutErr) Error() string   { return "i/o timeout" }
func (e *timeoutErr) Timeout() bool   { return true }
func (e *timeoutErr) Temporary() bool { return true }

// TestExecuteWithRetry_DNSError verifies DNS errors re-enter the retry flow.
func TestExecuteWithRetry_DNSError(t *testing.T) {
	ctx := context.Background()
	cfg := Config{
		MaxRetries:   5,
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
	}

	calls := 0
	err := ExecuteWithRetry(ctx, cfg, func() error {
		calls++
		if calls == 1 {
			return &net.DNSError{Err: "no such host", Name: "prod-rescale-platform.s3.us-east-1.amazonaws.com"}
		}
		return nil // Success on second attempt
	})

	if err != nil {
		t.Fatalf("expected nil error after DNS retry, got: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (initial + 1 retry), got %d", calls)
	}
}

// TestExecuteWithRetry_DNSErrorString verifies string-wrapped DNS errors also retry.
func TestExecuteWithRetry_DNSErrorString(t *testing.T) {
	ctx := context.Background()
	cfg := Config{
		MaxRetries:   5,
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
	}

	calls := 0
	err := ExecuteWithRetry(ctx, cfg, func() error {
		calls++
		if calls == 1 {
			return fmt.Errorf("dial tcp: lookup api.rescale.com: no such host")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("expected nil error after DNS string retry, got: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (initial + 1 retry), got %d", calls)
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
