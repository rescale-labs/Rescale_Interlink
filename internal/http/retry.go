package http

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"strings"
	"time"
)

// ErrorType represents different classes of errors for retry strategy
type ErrorType int

const (
	// ErrorTypeSuccess indicates operation succeeded
	ErrorTypeSuccess ErrorType = iota
	// ErrorTypeCredential indicates authentication/authorization failure (403, expired token)
	ErrorTypeCredential
	// ErrorTypeNetwork indicates network/connection issues (timeouts, connection refused, etc.)
	ErrorTypeNetwork
	// ErrorTypeRetryable indicates server errors that can be retried (500, 502, 503, throttling)
	ErrorTypeRetryable
	// ErrorTypeFatal indicates client errors that should not be retried (400, 404, invalid request)
	ErrorTypeFatal
)

// Config holds retry parameters for ExecuteWithRetry
type Config struct {
	// MaxRetries is the maximum number of retry attempts (default: 10)
	MaxRetries int
	// InitialDelay is the base delay for exponential backoff (default: 200ms)
	InitialDelay time.Duration
	// MaxDelay is the maximum delay between retries (default: 15s)
	MaxDelay time.Duration
	// CredentialRefresh is an optional function to refresh credentials before each attempt
	CredentialRefresh func(context.Context) error
	// OnRetry is an optional callback invoked before each retry attempt
	OnRetry func(attempt int, err error, errorType ErrorType)
}

// ClassifyError determines the error type for retry strategy
// This error classification is based on extensive testing with S3/Azure uploads and downloads
func ClassifyError(err error) ErrorType {
	if err == nil {
		return ErrorTypeSuccess
	}

	// v4.5.4: Type-based checks for common error types (more robust than string matching)
	// User cancellation should NOT be retried (avoids wasted backoff delay)
	if errors.Is(err, context.Canceled) {
		return ErrorTypeFatal
	}
	// Timeouts ARE retryable
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorTypeNetwork
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ErrorTypeNetwork
	}

	errStr := strings.ToLower(err.Error())

	// v4.5.4: Proxy authentication failures - don't retry (must check before generic network errors)
	if strings.Contains(errStr, "407") ||
		strings.Contains(errStr, "proxy authentication required") ||
		strings.Contains(errStr, "proxyauthenticationrequired") {
		return ErrorTypeFatal
	}

	// Credential-related errors - need token/credential refresh
	// Includes both AWS (expired token) and Azure (authentication failed, invalid SAS) errors
	if strings.Contains(errStr, "expired") ||
		strings.Contains(errStr, "invalid token") ||
		strings.Contains(errStr, "expiredtoken") ||
		strings.Contains(errStr, "403") ||
		strings.Contains(errStr, "unauthorized") ||
		strings.Contains(errStr, "authentication failed") ||
		strings.Contains(errStr, "authenticationfailed") ||
		strings.Contains(errStr, "invalid sas") ||
		strings.Contains(errStr, "sas token") ||
		strings.Contains(errStr, "signature not valid") ||
		strings.Contains(errStr, "authorization failure") {
		return ErrorTypeCredential
	}

	// Network errors - retryable with backoff
	// v4.5.4: Added proxy/connection errors that occur mid-transfer
	if strings.Contains(errStr, "tls handshake timeout") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "i/o timeout") ||
		strings.Contains(errStr, "eof") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "use of closed network connection") ||
		strings.Contains(errStr, "server closed idle connection") ||
		strings.Contains(errStr, "proxyconnect tcp") ||
		strings.Contains(errStr, "stream error") ||
		strings.Contains(errStr, "http2: server sent goaway") {
		return ErrorTypeNetwork
	}

	// AWS/Azure retryable errors - server issues, rate limiting
	// Includes both AWS (RequestTimeout, InternalError) and Azure (ServerBusy, OperationTimeout) errors
	if strings.Contains(errStr, "requesttimeout") ||
		strings.Contains(errStr, "internalerror") ||
		strings.Contains(errStr, "serviceunavailable") ||
		strings.Contains(errStr, "slowdown") ||
		strings.Contains(errStr, "throttl") ||
		strings.Contains(errStr, "429") ||
		strings.Contains(errStr, "500") ||
		strings.Contains(errStr, "502") ||
		strings.Contains(errStr, "503") ||
		strings.Contains(errStr, "504") ||
		strings.Contains(errStr, "server busy") ||
		strings.Contains(errStr, "serverbusy") ||
		strings.Contains(errStr, "operationtimeout") ||
		strings.Contains(errStr, "operation timeout") ||
		strings.Contains(errStr, "service unavailable") {
		return ErrorTypeRetryable
	}

	// Client errors - don't retry (bad request, not found, etc.)
	if strings.Contains(errStr, "400") ||
		strings.Contains(errStr, "404") ||
		strings.Contains(errStr, "invalid") {
		return ErrorTypeFatal
	}

	// Unknown errors - treat as fatal to avoid infinite retries on unexpected errors
	return ErrorTypeFatal
}

// CalculateBackoff returns exponential backoff duration with full jitter
// Full jitter prevents thundering herd problem when many clients retry simultaneously
//
// Formula: random(0, min(maxDelay, initialDelay * 2^attempt))
func CalculateBackoff(attempt int, initialDelay, maxDelay time.Duration) time.Duration {
	if attempt <= 0 {
		return 0
	}

	// Exponential: 2^attempt * initialDelay
	base := time.Duration(1<<uint(attempt)) * initialDelay

	// Cap at maxDelay
	if base > maxDelay {
		base = maxDelay
	}

	// Full jitter: random value between 0 and base
	// This spreads out retry attempts to avoid synchronized retries
	return time.Duration(rand.Int63n(int64(base)))
}

// ExecuteWithRetry runs an operation with intelligent retry logic
//
// Retry strategy:
//   - Credential errors: Refresh credentials and retry immediately
//   - Network/Retryable errors: Exponential backoff with full jitter
//   - Fatal errors: Return immediately without retry
//   - Context cancellation: Return immediately
//
// The function will make up to config.MaxRetries attempts. If all attempts fail,
// it returns an error wrapping the last failure.
func ExecuteWithRetry(ctx context.Context, config Config, operation func() error) error {
	var lastErr error

	for attempt := 0; attempt < config.MaxRetries; attempt++ {
		// Check context cancellation before each attempt
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Refresh credentials before each attempt (if provided)
		if config.CredentialRefresh != nil {
			if err := config.CredentialRefresh(ctx); err != nil {
				return fmt.Errorf("credential refresh failed: %w", err)
			}
		}

		// Execute the operation
		err := operation()
		if err == nil {
			// Log success after retries (helps diagnose intermittent issues)
			if attempt > 0 {
				log.Printf("Operation succeeded after %d retry(s)", attempt)
			}
			return nil // Success!
		}

		lastErr = err

		// Classify the error to determine retry strategy
		errType := ClassifyError(err)

		switch errType {
		case ErrorTypeSuccess:
			return nil

		case ErrorTypeFatal:
			// Don't retry fatal errors (bad request, not found, etc.)
			return err

		case ErrorTypeCredential:
			// Credential errors - force refresh and retry immediately
			if attempt < config.MaxRetries-1 {
				if config.OnRetry != nil {
					config.OnRetry(attempt+1, err, errType)
				}
				// Brief pause before credential refresh (1 second)
				time.Sleep(1 * time.Second)
				continue
			}
			return fmt.Errorf("credential error after %d attempts: %w", config.MaxRetries, err)

		case ErrorTypeNetwork, ErrorTypeRetryable:
			// Network or server errors - use exponential backoff
			if attempt < config.MaxRetries-1 {
				backoff := CalculateBackoff(attempt, config.InitialDelay, config.MaxDelay)
				if config.OnRetry != nil {
					config.OnRetry(attempt+1, err, errType)
				}
				time.Sleep(backoff)
				continue
			}
		}
	}

	return fmt.Errorf("operation failed after %d attempts: %w", config.MaxRetries, lastErr)
}

// ErrorTypeName returns a human-readable name for an ErrorType
func ErrorTypeName(errType ErrorType) string {
	switch errType {
	case ErrorTypeSuccess:
		return "success"
	case ErrorTypeCredential:
		return "credential"
	case ErrorTypeNetwork:
		return "network"
	case ErrorTypeRetryable:
		return "retryable"
	case ErrorTypeFatal:
		return "fatal"
	default:
		return "unknown"
	}
}
