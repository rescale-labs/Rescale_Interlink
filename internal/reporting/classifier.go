// Package reporting provides safe serious-error reporting for Rescale Interlink.
package reporting

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/google/uuid"

	"github.com/rescale/rescale-int/internal/cloud/state"
)

// ErrorCategory classifies the domain of an error.
type ErrorCategory string

const (
	CategoryTransfer    ErrorCategory = "transfer"
	CategoryJobCreate   ErrorCategory = "job_create"
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
	ClassLocalFS     ErrorClass = "local_fs" // local filesystem refused the operation (permissions, missing path, another transfer's upload lock)
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
	if cause := reportedError(err, category); cause != nil {
		err = cause // a batch's report is on the error that warrants one
	}

	msg := err.Error()
	class := ClassifyErrorClass(err)

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
	if err = reportedError(err, category); err == nil {
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
	if status, _ := StatusOf(err); status == 429 || strings.Contains(msg, "rate limit") {
		return false
	}

	// "Daemon stopped" is user-initiated
	if strings.Contains(msg, "daemon stopped") {
		return false
	}

	// Classify the error to filter user-fixable problems.
	// Only server errors (5xx) and unclassified internal errors are reportable.
	class := ClassifyErrorClass(err)
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
	case ClassLocalFS: // local machine refused the write/read — user can fix the path or permissions
		return false
	}

	// ClassServerError (5xx) and ClassInternal (unclassified) are reportable —
	// these represent genuine failures the user can't fix themselves.
	return true
}

type batchError struct {
	msg  string
	errs []error
}

func (e batchError) Error() string   { return e.msg }
func (e batchError) Unwrap() []error { return e.errs }

// BatchError is the error of a batch in which several items failed. It reads
// as msg and holds each item's error, which a report weighs one by one: a lock
// refusal or a cancel among them hides no failure, whichever came first.
func BatchError(msg string, errs []error) error { return batchError{msg, errs} }

// reportedError is err, or for a BatchError the first of its errors that
// warrants a report on its own (nil when none does).
func reportedError(err error, category ErrorCategory) error {
	var batch batchError
	if !errors.As(err, &batch) {
		return err
	}
	for _, e := range batch.errs {
		if IsReportable(e, category) {
			return reportedError(e, category)
		}
	}
	return nil
}

// ClassifyErrorClass maps an error to an ErrorClass. An upload refused by
// another transfer's lock is the user's to act on, whatever its text says.
func ClassifyErrorClass(err error) ErrorClass {
	if errors.Is(err, state.ErrUploadLocked) {
		return ClassLocalFS
	}
	status, response := StatusOf(err)
	return classifyMessage(err.Error(), status, response)
}

// StatusOf returns the HTTP status of the response err reports (0 for none)
// and the text reporting it. An S3 or Azure SDK error carries both itself, free
// of any path wrapped around it; for any other error both are read from a status
// its text states, never from digits in a path or name.
func StatusOf(err error) (int, string) {
	var s3Err interface{ HTTPStatusCode() int } // smithy-go's ResponseError, behind every S3 response error
	if errors.As(err, &s3Err) {
		return s3Err.HTTPStatusCode(), s3Err.(error).Error()
	}
	var azureErr *azcore.ResponseError
	if errors.As(err, &azureErr) {
		return azureErr.StatusCode, azureErr.Error()
	}
	return httpStatus(err.Error()), err.Error()
}

// statusPattern finds the HTTP status a message states: a code after "status",
// "StatusCode", "HTTP" or "RESPONSE" and a separator, or one ahead of its reason
// phrase, opening after a space or punctuation and closing at punctuation or a
// line's end, as every status formatted here or by the SDKs does. A code inside
// a path or name runs on instead: "/tmp/status503/", "frequency response 500 Hz/".
var statusPattern = regexp.MustCompile(`(?i)(?:^|[\s(\["',;:>])(?:` +
	`(?:status(?:\s*code)?|http(?:/[\d.]+)?|response)(?:\s*[:=]\s*|\s+)([1-5]\d\d)|` +
	`([1-5]\d\d) (?:bad request|unauthorized|forbidden|not found|too many requests|internal server error|bad gateway|service unavailable)` +
	`)(?:$|[\r\n)\]"',;:<]|\.(?:\s|$))`)

// httpStatus returns the HTTP status msg states, or 0 if it states none.
func httpStatus(msg string) int {
	if m := statusPattern.FindStringSubmatch(msg); m != nil {
		code, _ := strconv.Atoi(m[1] + m[2])
		return code
	}
	return 0
}

// classifyMessage maps an error message, the HTTP status of the response it
// reports (0 for none) and the text reporting that response to an ErrorClass.
func classifyMessage(msg string, status int, response string) ErrorClass {
	// Whether the response says it timed out: its own words run from the status
	// its text states, so a path, name or URL ahead of that never counts.
	said := strings.ToLower(response)
	if loc := statusPattern.FindStringIndex(said); loc != nil {
		said = said[loc[0]:]
	}
	timedOut := strings.Contains(said, "timeout") || strings.Contains(said, "deadline exceeded")
	switch status {
	case 0: // no status: the words below decide
	case 401, 403:
		return ClassAuth
	case 400, 404:
		if timedOut { // S3's RequestTimeout, for a connection left idle, is a 400
			return ClassTimeout
		}
		return ClassClientError
	case 500, 502, 503:
		return ClassServerError
	default: // a status with no class of its own, such as 504, 409 or a 200 whose body then failed; the words below could be a path's
		if timedOut {
			return ClassTimeout
		}
		return ClassInternal
	}

	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "unauthorized") || strings.Contains(lower, "forbidden"):
		return ClassAuth
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded"):
		return ClassTimeout
	case strings.Contains(lower, "connection refused") || strings.Contains(lower, "no such host") ||
		strings.Contains(lower, "network") || strings.Contains(lower, "dns"):
		return ClassNetwork
	// "disc quota" is the macOS/BSD spelling of EDQUOT; Linux says "disk quota".
	case strings.Contains(lower, "no space left") || strings.Contains(lower, "disk quota") ||
		strings.Contains(lower, "disc quota") || strings.Contains(lower, "not enough space on the disk"):
		return ClassDiskSpace
	// Local filesystem refusals, in Unix wording and then Windows'. These come
	// from the user's own machine — a directory macOS privacy protection guards,
	// a path that disappeared mid-transfer, a read-only volume — so they are
	// never a Rescale failure worth a report. Deliberately absent: "too many
	// open files" (fd exhaustion is usually our own descriptor leak) and I/O
	// errors, which stay reportable.
	case strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "operation not permitted") ||
		strings.Contains(lower, "no such file or directory") ||
		strings.Contains(lower, "read-only file system") ||
		strings.Contains(lower, "file name too long") ||
		strings.Contains(lower, "is a directory") ||
		strings.Contains(lower, "not a directory") ||
		strings.Contains(lower, "file exists") ||
		strings.Contains(lower, "the system cannot find the") ||
		strings.Contains(lower, "access is denied"):
		return ClassLocalFS
	case strings.Contains(lower, "internal server"):
		return ClassServerError
	default:
		return ClassInternal
	}
}
