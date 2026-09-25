package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/iotest"

	inthttp "github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/reporting"
)

// An error page can quote a signed URL or a key, and every API error that
// carries the page's text reaches a terminal, a log, the daemon's state file or
// the GUI.
func TestAPIErrorsQuoteNoCredentials(t *testing.T) {
	const page = `{"detail":"upload to https://acct.blob.core.windows.net/c/f?sv=2020-10-02&sig=FAKESIG failed","key":"AccountKey=FAKEKEY;","secretKey":"FAKESECRET"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, page)
	}))
	defer server.Close()
	c := newTestClient(t, server.URL)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"ListJobFiles", func() error { _, err := c.ListJobFiles(ctx, "job1"); return err }},
		{"retry excerpt", func() error { return errorString(bodyExcerpt(io.NopCloser(strings.NewReader(page)))) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("want an error")
			}
			if msg := err.Error(); strings.Contains(msg, "FAKE") || !strings.Contains(msg, "sig=REDACTED") {
				t.Errorf("error quotes a credential: %s", msg)
			}
		})
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

// A transport error quotes the URL it failed on, which a redirect can make a
// signed one, or a Location net/http could not parse; so does a request the
// client cannot make, such as a next page's; a failed read of a response can
// quote one too. Each is redacted where the client makes it, and still
// classified as it was.
func TestAPITransportErrorsQuoteNoCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "moved"):
			w.Header().Set("Location", "https://example.invalid/%zz?sv=2020-10-02&sig=FAKESIG")
			w.WriteHeader(http.StatusFound)
		case strings.Contains(r.URL.Path, "paged"):
			io.WriteString(w, `{"results":[],"next":"https://example.invalid/api/v2/jobs/paged/files/%zz/?sig=FAKESIG"}`)
		default:
			http.Redirect(w, r, "http://127.0.0.1:1/signed?sv=2020-10-02&sig=FAKESIG", http.StatusFound)
		}
	}))
	defer server.Close()
	c := newTestClient(t, server.URL)
	readErr := &url.Error{Op: "read", URL: "https://acct.blob.core.windows.net/c/f?sv=2020-10-02&sig=FAKESIG", Err: errors.New("connection reset by peer")}
	failedRead := newTestClient(t, server.URL)
	failedRead.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(iotest.ErrReader(readErr))}, nil
	})}

	if reporting.RedactedError(nil) != nil {
		t.Error("RedactedError(nil) is not nil")
	}
	if err := reporting.RedactedError(reporting.RedactedError(readErr)); errors.Unwrap(err) != readErr {
		t.Errorf("a redacted error is wrapped again: %#v", err)
	}

	// The client's error, and the one it returned before: its cause, which it
	// must still reach, behind the same words.
	listed := func(c *Client, jobID, before string) (error, error) {
		_, err := c.ListJobFiles(context.Background(), jobID)
		var urlErr *url.Error
		if !errors.As(err, &urlErr) {
			t.Fatalf("%s: %v, want a transport error", jobID, err)
		}
		return err, fmt.Errorf(before, urlErr)
	}
	for _, tc := range []struct {
		name string
		errs func() (error, error)
	}{
		{"a redirect to an unreachable signed URL", func() (error, error) { return listed(c, "redirect", "request failed: %w") }},
		{"a Location it cannot parse", func() (error, error) { return listed(c, "moved", "request failed: %w") }},
		{"a next page it cannot request", func() (error, error) { return listed(c, "paged", "failed to create request: %w") }},
		{"a failed read of a successful response", func() (error, error) {
			return listed(failedRead, "job1", "failed to decode job files response: %w")
		}},
		{"a failed read of an error page", func() (error, error) {
			return errors.New(readResponseBody(io.NopCloser(iotest.ErrReader(readErr)))), fmt.Errorf("(failed to read response body: %s)", readErr)
		}},
		// the shared wrapper answers for a network error anywhere in its chain
		{"a timeout", func() (error, error) {
			return reporting.RedactedError(stallErr{timeout: true}), stallErr{timeout: true}
		}},
		{"a wrapped timeout", func() (error, error) {
			raw := fmt.Errorf("read failed: %w", stallErr{timeout: true})
			return reporting.RedactedError(raw), raw
		}},
		{"a wrapped temporary error", func() (error, error) {
			raw := fmt.Errorf("read failed: %w", stallErr{temporary: true})
			return reporting.RedactedError(raw), raw
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err, raw := tc.errs()
			if msg := err.Error(); strings.Contains(msg, "FAKE") || !strings.Contains(msg, "sig=REDACTED") {
				t.Errorf("error quotes a credential: %s", msg)
			}
			if got, want := classification(err), classification(raw); got != want {
				t.Errorf("classified %s, want %s as before", got, want)
			}
		})
	}
}

// classification is every verdict a caller draws from an error: its retry
// class, report class and reportability, status, timeout and transport origin.
func classification(err error) string {
	status, _ := reporting.StatusOf(err)
	var netErr net.Error
	isNet := errors.As(err, &netErr)
	return fmt.Sprint(inthttp.ClassifyError(err), reporting.ClassifyErrorClass(err), reporting.IsReportable(err, reporting.CategoryTransfer),
		status, isNet && netErr.Timeout(), isNet && netErr.Temporary(), errors.As(err, new(*url.Error)), errors.As(err, new(stallErr)))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stallErr is a network error whose text names no timeout, so that only its
// methods classify it.
type stallErr struct{ timeout, temporary bool }

func (e stallErr) Error() string {
	return `read "https://acct.blob.core.windows.net/c/f?sv=2020-10-02&sig=FAKESIG": stalled`
}
func (e stallErr) Timeout() bool   { return e.timeout }
func (e stallErr) Temporary() bool { return e.temporary }
