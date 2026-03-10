// Package reporting provides safe serious-error reporting for Rescale Interlink.
// v4.8.7: Plan 3 (6A-6E) — error classification, redaction, report building,
// and transport for GUI/CLI/daemon error reporting.
package reporting

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// ErrorCategory classifies the domain of an error.
type ErrorCategory string

const (
	CategoryTransfer    ErrorCategory = "transfer"
	CategoryJobCreate   ErrorCategory = "job_create"
	CategoryJobSubmit   ErrorCategory = "job_submit"
	CategoryPURPipeline ErrorCategory = "pur_pipeline"
	CategoryAuth        ErrorCategory = "auth"
)

// Severity indicates the impact level of a classified error.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityError    Severity = "error"
)

// ErrorClass describes the technical nature of the error.
type ErrorClass string

const (
	ClassNetwork     ErrorClass = "network"
	ClassAuth        ErrorClass = "auth"
	ClassDiskSpace   ErrorClass = "disk_space"
	ClassClientError ErrorClass = "client_error" // 4xx — user gave bad input (wrong ID, bad params)
	ClassServerError ErrorClass = "server_error" // 5xx — server-side failure
	ClassInternal    ErrorClass = "internal"
	ClassTimeout     ErrorClass = "timeout"
)

// ClassifiedError holds a fully classified error ready for report building.
type ClassifiedError struct {
	ErrorID      string
	Category     ErrorCategory
	Severity     Severity
	Operation    string
	Backend      string
	ErrorMessage string
	ErrorClass   ErrorClass
}

// Classify inspects an error and returns a ClassifiedError with redacted message.
// The operation and backend are caller-supplied context.
func Classify(err error, category ErrorCategory, operation, backend string) *ClassifiedError {
	if err == nil {
		return nil
	}

	msg := err.Error()
	class := classifyErrorClass(msg)

	severity := SeverityError
	if category == CategoryAuth || class == ClassAuth {
		severity = SeverityCritical
	}

	return &ClassifiedError{
		ErrorID:      uuid.New().String(),
		Category:     category,
		Severity:     severity,
		Operation:    operation,
		Backend:      backend,
		ErrorMessage: RedactError(msg),
		ErrorClass:   class,
	}
}

// IsReportable decides whether an error warrants a user-visible report.
//
// Philosophy: a report is for "I did everything right and something broke."
// If the user can read the error message and fix it themselves (wrong credentials,
// network down, bad ID, disk full), no report is needed — Interlink already tells
// them what went wrong. Reports are reserved for server-side failures (5xx),
// unclassified internal errors, and batch/pipeline wipeouts where something
// genuinely broke.
func IsReportable(err error, category ErrorCategory) bool {
	if err == nil {
		return false
	}

	// User cancellation is never reportable
	if err == context.Canceled {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "context canceled") || strings.Contains(msg, "operation was canceled") {
		return false
	}

	// Rate limit 429 is transient
	if strings.Contains(msg, "429") || strings.Contains(msg, "rate limit") {
		return false
	}

	// "Daemon stopped" is user-initiated
	if strings.Contains(msg, "daemon stopped") {
		return false
	}

	// Classify the error to filter user-fixable problems.
	// Only server errors (5xx) and unclassified internal errors are reportable.
	class := classifyErrorClass(msg)
	switch class {
	case ClassAuth: // wrong/expired credentials — user can fix
		return false
	case ClassNetwork: // connectivity issue — user's network
		return false
	case ClassTimeout: // connectivity/latency — user's network
		return false
	case ClassDiskSpace: // user needs to free space
		return false
	case ClassClientError: // 400/404 — bad input (wrong ID, bad params)
		return false
	}

	// ClassServerError (5xx) and ClassInternal (unclassified) are reportable —
	// these represent genuine failures the user can't fix themselves.
	return true
}

// classifyErrorClass maps error message patterns to an ErrorClass.
// Mirrors the pattern matching in translateAPIError (file_bindings.go)
// and classifyError (transferStore.ts).
func classifyErrorClass(msg string) ErrorClass {
	lower := strings.ToLower(msg)

	switch {
	case strings.Contains(lower, "401") || strings.Contains(lower, "unauthorized"):
		return ClassAuth
	case strings.Contains(lower, "403") || strings.Contains(lower, "forbidden"):
		return ClassAuth
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded"):
		return ClassTimeout
	case strings.Contains(lower, "connection refused") || strings.Contains(lower, "no such host") ||
		strings.Contains(lower, "network") || strings.Contains(lower, "dns"):
		return ClassNetwork
	case strings.Contains(lower, "no space left") || strings.Contains(lower, "disk quota"):
		return ClassDiskSpace
	case strings.Contains(lower, "400") || strings.Contains(lower, "404"):
		return ClassClientError
	case strings.Contains(lower, "500") || strings.Contains(lower, "502") ||
		strings.Contains(lower, "503") || strings.Contains(lower, "internal server"):
		return ClassServerError
	default:
		return ClassInternal
	}
}
