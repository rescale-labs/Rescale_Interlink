package reporting

import (
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/logging"
)

// A credential's value ends where the text around it does: at a space, quote,
// escape or markup, or a query's, list's or JSON object's separator. An XML or
// JSON error body keeps its structure, and so its error code, without it.
const credentialValue = `[^\s"'\\<&,;})\]]+`

// Credentials an error's text can carry, removed by RedactSecrets.
var (
	// A parameter of a signed URL's query: each one azblob's SAS parser knows
	// (sas.NewQueryParameters, less "snapshot") and each X-Amz-* one of an S3
	// presigned URL. Only after "?" or "&" (or its JSON, HTML or percent
	// escape, as a proxy's error page quotes the request), so that a job named
	// "mass=5_1" or "phase=2" keeps its name.
	reSignedURLParam = regexp.MustCompile(`(?i)([?&]|\\u0026|&amp;|&#38;|%3F|%26)(sig|se|st|sp|sv|sr|si|ss|srt|spr|sip|sdd|ses|scid|` +
		`skoid|sktid|skt|ske|sks|skv|saoid|suoid|rsc[cdelt]|x-amz-[a-z0-9-]+)(=|%3D)` + credentialValue)
	// A key's value, quoted or not; its quotes stay.
	reAWSKey = regexp.MustCompile(`(?i)((?:access.?key|secret.?key|session.?token)=\\?["']?)` + credentialValue)
	// A JSON (or Python) field named for a credential, as the credentials
	// endpoint names them ("secretKey":"x"), up to its value's opening quote,
	// whose escapes (group 1) tell how deep in other strings the JSON sits. The
	// whole name counts: "mysecretKey" is another field. Whitespace after the
	// colon may be escaped too, as JSON inside a string prints a newline.
	reJSONKey  = regexp.MustCompile(`(?i)["'](?:aws.?)?(?:secret.?)?(?:access.?key|secret.?key|session.?token)\\*["']\s*:(?:\s|\\+[nrt])*(\\*)(["'])`)
	reAzureKey = regexp.MustCompile(`(?i)(AccountKey=\\?["']?)` + credentialValue)
	// An Authorization header's value however it is printed: the scheme stays
	// and the rest of the field goes. A quoted value (JSON, a list) ends at its
	// closing quote, one escaped in a log line at the escape, and a header's
	// ("Authorization: Digest a="b", c = \"d\"", Go's map[Authorization:[Token
	// abc]]) at the end of its line, list or quote, taking in its quoted
	// parameters, escaped or not. A quote that opens on a space or closes before
	// a word is the next field's, as status's is in
	// error="Authorization: Basic YQ==" status="401".
	// A bearer or token credential on its own is told from prose by its place,
	// in any case: it fills a whole field, from the start of a line, a quote,
	// bracket, tag or separator to the end of one, as "token abc" does and
	// "Token file could not be read" does not.
	reAuthorization = regexp.MustCompile(`(?im)(authorization["']?\s*[:=]\s*\[?\s*["']\s*(?:[a-z][\w-]*\s+)?)(?:\\.|[^"'\\\r\n])+|` +
		`(authorization\\"\s*[:=]\s*\[?\s*\\"\s*(?:[a-z][\w-]*\s+)?)[^\\\r\n]+|` +
		`(authorization\s*[:=]\s*\[?\s*(?:[a-z][\w-]*\s+)?)(?:=\s*(?:"(?:[^\s"][^"\r\n]*)?"|\\"(?:[^\s"\\][^"\\\r\n]*)?\\")\B|[^\r\n"'\[\])\\&<])+|` +
		`((?:^|["'\[(=:,>]\s*)(?:bearer|token)\s+)[\w.~+/=-]+([\r"'\])\\,;&<]|$)`)
	// Each value of an Authorization list, as JSON, Python or Go's %q prints a
	// header's, on one line or indented over several: reAuthorization takes
	// only the first.
	reAuthorizationList = regexp.MustCompile(`(?i)authorization\\?["']?\s*[:=]\s*\[[^\]]*`)
	reListValue         = regexp.MustCompile(`(?i)(\\["']\s*(?:[a-z][\w-]*\s+)?)[^"'\\\r\n]+(\\["'])|(["']\s*(?:[a-z][\w-]*\s+)?)(?:\\.|[^"'\\\r\n])+(["'])`)
	reAWSAccessKeyID    = regexp.MustCompile(`AKIA[A-Z0-9]{16}`)
)

// RedactSecrets removes credentials from s: signed-URL signatures and tokens,
// storage keys and authorization values. Error text goes through it on its way
// to the terminal, a log, the GUI or a report, because an error can quote a
// request URL, and a signed URL's query is its credential.
func RedactSecrets(s string) string {
	s = reSignedURLParam.ReplaceAllString(s, "${1}${2}${3}REDACTED")
	s = reAWSKey.ReplaceAllString(s, "${1}REDACTED")
	s = redactJSONKeys(s)
	s = reAzureKey.ReplaceAllString(s, "${1}REDACTED")
	s = reAuthorizationList.ReplaceAllStringFunc(s, func(list string) string {
		i := strings.IndexByte(list, '[')
		return list[:i] + reListValue.ReplaceAllString(list[i:], "${1}${3}REDACTED${2}${4}")
	})
	s = reAuthorization.ReplaceAllString(s, "${1}${2}${3}${4}REDACTED${5}")
	s = reAWSAccessKeyID.ReplaceAllString(s, "[REDACTED_AWS_KEY]")
	return s
}

// redactJSONKeys replaces each reJSONKey field's whole string value, escapes
// and all, and keeps its quotes. A value opened by n escapes and a quote is
// closed by the quote with n more than a multiple of 2(n+1) escapes before it
// (each level of escaping doubles them and adds one); fewer than n end the
// string the JSON sits in, as any " does a ' value's.
func redactJSONKeys(s string) string {
	var b strings.Builder
	for {
		m := reJSONKey.FindStringSubmatchIndex(s)
		if m == nil {
			return b.String() + s
		}
		n, q, i := m[3]-m[2], s[m[4]], m[1]
		for r := 0; i < len(s) && s[i] != '\n'; i++ {
			if q == '\'' && s[i] == '"' {
				i -= r
				break
			}
			if s[i] == q && (r < n || (r-n)%(2*n+2) == 0) {
				i -= min(r, n)
				break
			}
			if s[i] == '\\' {
				r++
			} else {
				r = 0
			}
		}
		b.WriteString(s[:m[1]] + "REDACTED")
		s = s[i:]
	}
}

// RedactWriter returns a writer that passes each write through RedactSecrets
// on its way to w. Each write must hold whole lines, as the standard logger's,
// zerolog's and cobra's do.
func RedactWriter(w io.Writer) io.Writer { return redactWriter{w} }

type redactWriter struct{ w io.Writer }

// IsTerminal answers for the writer underneath, so a logger writing through
// redaction still colours for a terminal and not for a file or a pipe.
func (r redactWriter) IsTerminal() bool { return logging.IsTerminal(r.w) }

func (r redactWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(r.w, RedactSecrets(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// RedactedLogger is logging.NewLogger's logger for mode, writing its lines to w
// through RedactWriter.
func RedactedLogger(mode string, w io.Writer) *logging.Logger {
	l := logging.NewLogger(mode, nil)
	l.SetOutput(RedactWriter(w))
	return l
}

// RedactedError is err without the credentials its text can quote, such as a
// redirect's URL or a Location net/http could not parse. The retry and report
// classifiers still see the cause: errors.Is and As reach it, and it answers
// Timeout and Temporary for the net.Error they would have found in its chain.
// An error it made comes back unchanged, but not one wrapping such an error:
// the wrapper's own text may quote a credential.
func RedactedError(err error) error {
	if _, done := err.(redactedError); done || err == nil {
		return err
	}
	return redactedError{err}
}

type redactedError struct{ error }

func (e redactedError) Error() string { return RedactSecrets(e.error.Error()) }
func (e redactedError) Unwrap() error { return e.error }

func (e redactedError) Timeout() bool {
	var n net.Error
	return errors.As(e.error, &n) && n.Timeout()
}

func (e redactedError) Temporary() bool {
	var n net.Error
	return errors.As(e.error, &n) && n.Temporary()
}

// Regex patterns for redaction
var (
	// Hex tokens longer than 20 chars (API keys, SAS tokens, etc.)
	hexTokenRe = regexp.MustCompile(`[0-9a-fA-F]{20,}`)
	// URL query parameters (contains sensitive tokens)
	urlQueryRe = regexp.MustCompile(`\?[^\s"']+`)
	// Email addresses
	emailRe = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
	// Bearer/authorization tokens
	bearerRe = regexp.MustCompile(`(?i)(bearer|token|key|authorization)[=:\s]+\S+`)
	// File paths with home directory
	homePathRe = regexp.MustCompile(`(/Users/[^/]+|/home/[^/]+|C:\\Users\\[^\\]+)`)
)

// RedactError strips sensitive data from an error message using an allowlist approach.
// On top of RedactSecrets it removes hex tokens, URL query params, email
// addresses, auth tokens and home directories: a report leaves the machine.
func RedactError(msg string) string {
	msg = RedactSecrets(msg)
	msg = hexTokenRe.ReplaceAllString(msg, "[REDACTED]")
	msg = urlQueryRe.ReplaceAllString(msg, "?[REDACTED]")
	msg = emailRe.ReplaceAllString(msg, "[EMAIL]")
	msg = bearerRe.ReplaceAllString(msg, "${1}=[REDACTED]")
	msg = homePathRe.ReplaceAllString(msg, "[HOME]")
	return msg
}

// RedactTimelineEntry converts an internal Event into a sanitized one-liner.
// Job names are replaced with "job-N" placeholders to avoid leaking sensitive names.
func RedactTimelineEntry(event events.Event, jobIndex int) events.SanitizedTimelineEntry {
	entry := events.SanitizedTimelineEntry{
		Timestamp: event.Timestamp().Format(time.RFC3339),
	}

	switch e := event.(type) {
	case *events.LogEvent:
		entry.Type = "log"
		entry.Summary = fmt.Sprintf("[%s] %s", e.Level.String(), RedactError(e.Message))
	case *events.ErrorEvent:
		entry.Type = "error"
		msg := ""
		if e.Error != nil {
			msg = RedactError(e.Error.Error())
		}
		entry.Summary = fmt.Sprintf("error in %s: %s", e.Stage, msg)
	case *events.StateChangeEvent:
		entry.Type = "state_change"
		entry.Summary = fmt.Sprintf("job-%d: %s → %s (%s)", jobIndex, e.OldStatus, e.NewStatus, e.Stage)
	case *events.CompleteEvent:
		entry.Type = "complete"
		entry.Summary = fmt.Sprintf("completed: %d/%d succeeded in %s", e.SuccessJobs, e.TotalJobs, e.Duration.Round(time.Second))
	case *events.TransferEvent:
		entry.Type = "transfer"
		entry.Summary = fmt.Sprintf("%s: %s %.0f%%", e.EventType, sanitizeFileName(e.Name), e.Progress*100)
		if e.Error != nil {
			entry.Summary += " error: " + RedactError(e.Error.Error())
		}
	case *events.ProgressEvent:
		entry.Type = "progress"
		entry.Summary = fmt.Sprintf("job-%d %s: %.0f%%", jobIndex, e.Stage, e.Progress*100)
	case *events.BatchProgressEvent:
		entry.Type = "batch_progress"
		entry.Summary = fmt.Sprintf("batch %s/%s: %d/%d (%.0f%%)", e.Direction, e.Label, e.Completed, e.Total, e.Progress*100)
	case *events.EnumerationEvent:
		entry.Type = "enumeration"
		entry.Summary = fmt.Sprintf("scan %s: %d files, %d folders", e.Direction, e.FilesFound, e.FoldersFound)
	default:
		entry.Type = string(event.Type())
		entry.Summary = "(event)"
	}

	// Truncate long summaries
	if len(entry.Summary) > 200 {
		entry.Summary = entry.Summary[:200] + "..."
	}

	return entry
}

// RedactTimeline batch-converts the most recent N events from a snapshot.
func RedactTimeline(rawEvents []events.Event, limit int) []events.SanitizedTimelineEntry {
	start := 0
	if len(rawEvents) > limit {
		start = len(rawEvents) - limit
	}

	jobNameIndex := make(map[string]int)
	nextIndex := 1

	entries := make([]events.SanitizedTimelineEntry, 0, len(rawEvents)-start)
	for _, event := range rawEvents[start:] {
		// Determine job index from event if applicable
		jobIdx := 0
		jobName := extractJobName(event)
		if jobName != "" {
			if idx, ok := jobNameIndex[jobName]; ok {
				jobIdx = idx
			} else {
				jobIdx = nextIndex
				jobNameIndex[jobName] = nextIndex
				nextIndex++
			}
		}
		entries = append(entries, RedactTimelineEntry(event, jobIdx))
	}

	return entries
}

// extractJobName returns the job name from an event, if applicable.
func extractJobName(event events.Event) string {
	switch e := event.(type) {
	case *events.LogEvent:
		return e.JobName
	case *events.ErrorEvent:
		return e.JobName
	case *events.StateChangeEvent:
		return e.JobName
	case *events.ProgressEvent:
		return e.JobName
	default:
		return ""
	}
}

// sanitizeFileName keeps only the basename of a file path to avoid leaking directory structure.
func sanitizeFileName(name string) string {
	// Find last separator
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' || name[i] == '\\' {
			return name[i+1:]
		}
	}
	return name
}
