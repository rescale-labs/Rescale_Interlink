package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"

	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/events"
)

// --- Classifier tests ---

func TestClassify_BasicError(t *testing.T) {
	err := errors.New("upload failed: connection refused")
	c := Classify(err, CategoryTransfer, "folder_upload", "s3")
	if c == nil {
		t.Fatal("expected non-nil ClassifiedError")
	}
	if c.ErrorID == "" {
		t.Error("expected non-empty ErrorID")
	}
	if c.Category != CategoryTransfer {
		t.Errorf("expected category %q, got %q", CategoryTransfer, c.Category)
	}
	if c.ErrorClass != ClassNetwork {
		t.Errorf("expected class %q, got %q", ClassNetwork, c.ErrorClass)
	}
	if c.Operation != "folder_upload" {
		t.Errorf("expected operation %q, got %q", "folder_upload", c.Operation)
	}
}

func TestClassify_AuthError_IsCritical(t *testing.T) {
	err := errors.New("401 unauthorized")
	c := Classify(err, CategoryAuth, "test_connection", "")
	if c.Severity != SeverityCritical {
		t.Errorf("expected severity %q, got %q", SeverityCritical, c.Severity)
	}
	if c.ErrorClass != ClassAuth {
		t.Errorf("expected class %q, got %q", ClassAuth, c.ErrorClass)
	}
}

func TestClassify_Nil(t *testing.T) {
	c := Classify(nil, CategoryTransfer, "op", "")
	if c != nil {
		t.Error("expected nil for nil error")
	}
}

// s3InternalError is a genuine S3 500 on uploading a file in dir.
func s3InternalError(dir string) string {
	return "failed to upload /Users/jd/sweep/" + dir + "/in.dat: operation error S3: PutObject, https response error " +
		"StatusCode: 500, RequestID: 8Q4NVDC1TMR1JS4Q, HostID: 7Zk0c2VkXhQ=, api error InternalError: " +
		"We encountered an internal error. Please try again."
}

// conflictUnder is an API 409 on registering a file uploaded from dir.
func conflictUnder(dir string) string {
	return "failed to upload /Users/jd/sweep/" + dir + `/in.dat: register file failed: status 409: {"detail": "Conflict."}`
}

// s3ResponseError stands in for smithy-go's ResponseError, which every S3 SDK
// error wraps and which carries its status as a value.
type s3ResponseError struct {
	status int
	text   string
}

func (e s3ResponseError) Error() string       { return e.text }
func (e s3ResponseError) HTTPStatusCode() int { return e.status }

func TestClassifyErrorClass(t *testing.T) {
	tests := []struct {
		msg  string
		want ErrorClass
	}{
		{"HTTP 401 Unauthorized", ClassAuth},
		{"403 Forbidden access", ClassAuth},
		{"context deadline exceeded", ClassTimeout},
		{"dial tcp: connection refused", ClassNetwork},
		{"no space left on device", ClassDiskSpace},
		// EDQUOT is spelled "disk quota exceeded" on Linux and "disc quota
		// exceeded" on macOS/BSD. Both are the user's disk, not a Rescale failure.
		{"write /net/home/f.dat: disk quota exceeded", ClassDiskSpace},
		{"write /net/home/f.dat: disc quota exceeded", ClassDiskSpace},
		{"HTTP 400 Bad Request", ClassClientError},
		{"status 404: not found", ClassClientError},
		{"HTTP 500 Internal Server Error", ClassServerError},
		{"502 Bad Gateway", ClassServerError},
		{"503 Service Unavailable", ClassServerError},
		{"some unknown error", ClassInternal},

		// Local filesystem refusals — the user's own machine, not a Rescale failure.
		{"open /Users/x/Downloads/out/f.dat: permission denied", ClassLocalFS},
		{"open /Volumes/gone/f.dat: no such file or directory", ClassLocalFS},
		{"write /mnt/ro/f.dat: read-only file system", ClassLocalFS},
		// fd exhaustion is usually our own leak — deliberately NOT LocalFS.
		{"open /tmp/f.dat: too many open files", ClassInternal},
		{"open /tmp/aaaa...: file name too long", ClassLocalFS},
		// A bare number in a path is not a status code.
		{"open /data/run400/f.dat: permission denied", ClassLocalFS},
		// HTTP 403 is still auth, not a local permission problem.
		{"403 Forbidden: permission denied by policy", ClassAuth},

		// Paths are full of status-like digits and words; only a stated status counts.
		{"read /Users/jd/sweep/Run_503: is a directory", ClassLocalFS},
		{"open /Users/jd/sweep/Run_503/in.dat/x: not a directory", ClassLocalFS},
		{"mkdir /Users/jd/sweep/Run_503: file exists", ClassLocalFS},
		// macOS privacy protection on Documents, Desktop and Downloads.
		{"open /Users/jd/Documents/Run_503/in.dat: operation not permitted", ClassLocalFS},
		{"read /Users/jd/sweep/Run_404/in.dat: input/output error", ClassInternal},
		{`open C:\sweep\Run_503\in.dat: The system cannot find the file specified.`, ClassLocalFS},
		{`open C:\sweep\Run_7\in.dat: Access is denied.`, ClassLocalFS},
		{`write C:\sweep\Run_7\out.tar: There is not enough space on the disk.`, ClassDiskSpace},
		{s3InternalError("Run_404"), ClassServerError},
		{s3InternalError("network_model"), ClassServerError},
		{"failed to stage block 0: PUT https://acct.blob.core.windows.net/c/Run_404/in.dat\n---\n" +
			"RESPONSE 503: 503 The server is busy.\nERROR CODE: ServerBusy", ClassServerError},
		{"failed to upload /tmp/uid-503/a.bin: failed to acquire upload lock: cannot inspect the existing " +
			"upload lock of /tmp/uid-503/a.bin: open /tmp/uid-503/a.bin.upload.lock: input/output error", ClassInternal},
		// A status is read only from a clause of its own, never from inside a path.
		{"open /tmp/status503/input: permission denied", ClassLocalFS},
		{`open C:\runs\HTTP 503\in.dat: Access is denied.`, ClassLocalFS},
		{"read /Users/jd/Run 404 Not Found/in.dat: input/output error", ClassInternal},
		{"open /Users/jd/frequency response 500 Hz/in.dat: permission denied", ClassLocalFS},
		{"could not acquire upload lock for /tmp/status404/input: it kept being retaken", ClassInternal},
		{s3InternalError("status404"), ClassServerError},
		// A stated status with no class of its own is not left to a path's words.
		{conflictUnder("network_model"), ClassInternal},
		// A 504 is a timeout where its response says so, as S3's and Azure's do; its path never makes it one.
		{"operation error S3: PutObject, https response error StatusCode: 504, RequestID: 8Q4NVDC1TMR1JS4Q, " +
			"HostID: 7Zk0c2VkXhQ=, api error GatewayTimeout: Gateway Timeout", ClassTimeout},
		{"failed to stage block 0: PUT https://acct.blob.core.windows.net/c/run_1/in.dat\n---\n" +
			"RESPONSE 504: 504 Gateway Timeout\nERROR CODE UNAVAILABLE", ClassTimeout},
		{"failed to upload /Users/jd/sweep/timeout_study/in.dat: register file failed: status 504: " +
			"<html><head><title>504 Gateway Time-out</title></head></html>", ClassInternal},
	}
	for _, tt := range tests {
		got := ClassifyErrorClass(errors.New(tt.msg))
		if got != tt.want {
			t.Errorf("ClassifyErrorClass(%q) = %q, want %q", tt.msg, got, tt.want)
		}
	}

	// A response that says it timed out is a timeout under any path, and a path
	// never makes one: S3's RequestTimeout (a 400), an S3 body that timed out
	// after its 200, and the API's 408 and 504.
	for msg, want := range map[string]ErrorClass{
		"operation error S3: UploadPart, https response error StatusCode: 400, RequestID: 8Q4NVDC1TMR1JS4Q, HostID: 7Zk0c2VkXhQ=, " +
			"api error RequestTimeout: Your socket connection to the server was not read from or written to within the timeout period.": ClassTimeout,
		"operation error S3: CompleteMultipartUpload, https response error StatusCode: 200, RequestID: 8Q4NVDC1TMR1JS4Q, " +
			"HostID: 7Zk0c2VkXhQ=, context deadline exceeded": ClassTimeout,
		"register file failed: status 408: 408 Request Timeout":      ClassTimeout,
		"register file failed: status 504: upstream request timeout": ClassTimeout,
		"operation error S3: CreateMultipartUpload, https response error StatusCode: 200, RequestID: 8Q4NVDC1TMR1JS4Q, " +
			"HostID: 7Zk0c2VkXhQ=, deserialization failed, failed to decode response body, unexpected EOF": ClassInternal,
		`register file failed: status 400: {"name": ["This field is required."]}`: ClassClientError,
	} {
		for _, at := range []string{"", "failed to upload /Users/jd/sweep/network_model/in.dat: ", "failed to upload /Users/jd/sweep/timeout_study/in.dat: "} {
			if got := ClassifyErrorClass(errors.New(at + msg)); got != want {
				t.Errorf("ClassifyErrorClass(%q) = %q, want %q", at+msg, got, want)
			}
		}
	}
}

func TestIsReportable(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		category ErrorCategory
		want     bool
	}{
		// Not reportable: nil, cancellation, transient
		{"nil error", nil, CategoryTransfer, false},
		{"context.Canceled", context.Canceled, CategoryTransfer, false},
		{"contains context canceled", errors.New("context canceled during upload"), CategoryTransfer, false},
		{"rate limit 429", errors.New("429 Too Many Requests"), CategoryTransfer, false},
		{"daemon stopped", errors.New("daemon stopped during download"), CategoryTransfer, false},

		// Not reportable: user-fixable errors
		{"auth 401", errors.New("API request failed with status 401: invalid token"), CategoryAuth, false},
		{"auth 403", errors.New("403 Forbidden"), CategoryTransfer, false},
		{"network error", errors.New("connection refused"), CategoryTransfer, false},
		{"dns error", errors.New("no such host api.rescale.com"), CategoryTransfer, false},
		{"timeout", errors.New("context deadline exceeded"), CategoryTransfer, false},
		{"Azure 504", &azcore.ResponseError{StatusCode: 504, ErrorCode: "GatewayTimeout"}, CategoryTransfer, false},
		{"disk space", errors.New("no space left on device"), CategoryTransfer, false},
		{"client 400", errors.New("API returned 400 bad request"), CategoryTransfer, false},
		{"client 404", errors.New("status 404: file not found"), CategoryTransfer, false},
		{"local fs permission", errors.New("open /Users/x/Downloads/f.dat: permission denied"), CategoryTransfer, false},
		{"local fs missing path", errors.New("open /Volumes/gone/f.dat: no such file or directory"), CategoryTransfer, false},
		{"local fs read-only", errors.New("write /mnt/ro/f.dat: read-only file system"), CategoryTransfer, false},
		{"fd exhaustion stays reportable", errors.New("open /tmp/f.dat: too many open files"), CategoryTransfer, true},
		{"local fs name too long", errors.New("open /tmp/x: file name too long"), CategoryTransfer, false},

		// Reportable: server errors and unclassified internal errors
		{"server 500", errors.New("API returned 500 internal server error"), CategoryTransfer, true},
		{"server 500 under Run_429", errors.New(s3InternalError("Run_429")), CategoryTransfer, true},
		// The status an SDK error carries decides, where the text states none, and only its own words say it timed out.
		{"Azure 503 under network_model/", fmt.Errorf("failed to upload /Users/jd/sweep/network_model/in.dat: %w",
			&azcore.ResponseError{StatusCode: 503, ErrorCode: "ServerBusy"}), CategoryTransfer, true},
		{"S3 503 under network_model/", fmt.Errorf("failed to upload /Users/jd/sweep/network_model/in.dat: %w",
			s3ResponseError{503, "api error SlowDown: Please reduce your request rate"}), CategoryTransfer, true},
		{"Azure 504 under timeout_study/", fmt.Errorf("failed to upload /Users/jd/sweep/timeout_study/in.dat: %w",
			&azcore.ResponseError{StatusCode: 504}), CategoryTransfer, true},
		{"server 502", errors.New("502 bad gateway"), CategoryTransfer, true},
		{"server 503", errors.New("503 service unavailable"), CategoryTransfer, true},
		{"unclassified error", errors.New("some unexpected error"), CategoryTransfer, true},
		{"batch wipeout", errors.New("batch download failed: 5/5 transfers failed"), CategoryTransfer, true},
		{"pipeline failure", errors.New("pipeline completed with 3/3 jobs failed"), CategoryPURPipeline, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsReportable(tt.err, tt.category)
			if got != tt.want {
				t.Errorf("IsReportable(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// An S3 SDK error's own text, never the path wrapped around it, says whether its
// response timed out. RequestTimeout (a 400) and a body that timed out after its
// 200 are timeouts and not reported; a 200 that failed otherwise is reported.
func TestClassify_S3ResponseError(t *testing.T) {
	for _, tt := range []struct {
		err    s3ResponseError
		want   ErrorClass
		report bool
	}{
		{s3ResponseError{400, "api error RequestTimeout: Your socket connection to the server was not read from or " +
			"written to within the timeout period."}, ClassTimeout, false},
		{s3ResponseError{200, "deserialization failed, failed to decode response body, context deadline exceeded"}, ClassTimeout, false},
		{s3ResponseError{200, "deserialization failed, failed to decode response body, unexpected EOF"}, ClassInternal, true},
	} {
		err := fmt.Errorf("failed to upload /Users/jd/sweep/timeout_study/in.dat: %w", tt.err)
		if class, report := Classify(err, CategoryTransfer, "files upload", "").ErrorClass, IsReportable(err, CategoryTransfer); class != tt.want || report != tt.report {
			t.Errorf("classified %s, reportable %v; want %s, reportable %v: %v", class, report, tt.want, tt.report, err)
		}
	}
}

// An upload refused by another transfer's lock is the user's to act on, however
// the refusal is worded.
func TestUploadLockRefusalIsNotReportable(t *testing.T) {
	src := filepath.Join(t.TempDir(), "data.bin")
	held, err := state.AcquireUploadLock(src)
	if err != nil {
		t.Fatalf("acquire the upload lock: %v", err)
	}
	defer state.ReleaseUploadLock(held)
	lock, refusal := state.AcquireUploadLock(src)
	if refusal == nil {
		state.ReleaseUploadLock(lock)
		t.Fatal("acquired an upload lock another transfer holds")
	}

	err = fmt.Errorf("failed to upload %s: S3Storage upload failed: failed to acquire upload lock: %w", src, refusal)
	if class := Classify(err, CategoryTransfer, "files upload", "").ErrorClass; class != ClassLocalFS || IsReportable(err, CategoryTransfer) {
		t.Errorf("classified %s, reportable %v; want %s, not reportable: %v", class, IsReportable(err, CategoryTransfer), ClassLocalFS, err)
	}
}

// --- Redactor tests ---

func TestRedactError(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(string) bool
	}{
		{
			"hex token stripped",
			"failed with token abc123def456789012345678 during upload",
			func(s string) bool { return strings.Contains(s, "[REDACTED]") && !strings.Contains(s, "abc123def") },
		},
		{
			"URL query params stripped",
			"GET https://storage.blob.core.windows.net/container/file?sig=abc123&se=2024-01-01",
			func(s string) bool { return strings.Contains(s, "?[REDACTED]") && !strings.Contains(s, "sig=") },
		},
		{
			"email stripped",
			"authenticated as user@example.com",
			func(s string) bool { return strings.Contains(s, "[EMAIL]") && !strings.Contains(s, "user@example.com") },
		},
		{
			"bearer token stripped",
			"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9",
			func(s string) bool { return strings.Contains(s, "[REDACTED]") },
		},
		{
			"home path stripped",
			"file not found at /Users/john/Documents/secret.txt",
			func(s string) bool { return strings.Contains(s, "[HOME]") && !strings.Contains(s, "/Users/john") },
		},
		{
			"clean error unchanged",
			"connection refused",
			func(s string) bool { return s == "connection refused" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactError(tt.input)
			if !tt.check(got) {
				t.Errorf("RedactError(%q) = %q, did not pass check", tt.input, got)
			}
		})
	}
}

func TestRedactSecrets(t *testing.T) {
	// Each credential leaves both RedactSecrets' text and a report's; the rest
	// of the text stays readable.
	redacted := []struct {
		input         string
		secrets, kept []string
	}{
		{ // Azure's own text for a failed StageBlock under an account SAS
			`Put "https://acct.blob.core.windows.net/c/data.bin?blockid=YmxvY2stMDAwMDAw&comp=block&se=2026-09-24T00%3A00%3A00Z` +
				`&sig=FAKESIGNATURE%2Fx%3D&sp=rwdlac&spr=https&srt=co&ss=b&sv=2025-11-05": connect: connection refused`,
			[]string{"FAKESIGNATURE"},
			[]string{`?blockid=YmxvY2stMDAwMDAw&comp=block&se=REDACTED&sig=REDACTED&sp=REDACTED&spr=REDACTED&srt=REDACTED&ss=REDACTED&sv=REDACTED": connect`},
		},
		{ // a user delegation SAS
			`?se=2026-09-24T00%3A00%3A00Z&sig=aBc%2Fd%3D&ske=2026-09-24T00%3A00%3A00Z&skoid=FAKEOID&sks=b&skt=2026-09-23T00%3A00%3A00Z` +
				`&sktid=FAKETENANT&skv=2025-11-05&sp=r&spr=https&sr=b&st=2026-09-23T00%3A00%3A00Z&sv=2025-11-05`,
			[]string{"aBc", "FAKEOID", "FAKETENANT"},
			[]string{"?se=REDACTED&sig=REDACTED&ske=REDACTED&skoid=REDACTED&sks=REDACTED&skt=REDACTED&sktid=REDACTED&skv=REDACTED&sp=REDACTED" +
				"&spr=REDACTED&sr=REDACTED&st=REDACTED&sv=REDACTED"},
		},
		{ // an S3 presigned GET, as Go's HTTP client reports it
			`Get "https://b.s3.us-east-1.amazonaws.com/k?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=ASIAPROBE%2F20260923%2Fus-east-1` +
				`%2Fs3%2Faws4_request&X-Amz-Date=20260923T231809Z&X-Amz-Expires=900&X-Amz-Security-Token=FAKESESSIONTOKEN&X-Amz-SignedHeaders=host` +
				`&x-id=GetObject&X-Amz-Signature=c34fe397ed88fb4e": connect: connection refused`,
			[]string{"ASIAPROBE", "FAKESESSIONTOKEN", "c34fe397ed88fb4e"},
			[]string{`&x-id=GetObject&X-Amz-Signature=REDACTED": connect`},
		},
		{`{"sasToken":"sv=2025-11-05&sig=aBc%2Fd%3D&sp=r"}`, []string{"aBc"}, []string{`&sig=REDACTED&sp=REDACTED"}`}},
		// a proxy's error page echoing the URL, which the SDK's error quotes, and a
		// CLI log field: the value stops at the escape
		{`<p>Blocked URL: https://a.blob.core.windows.net/c/f?se=2026-09-24&amp;sig=aBc%3D&amp;sp=r</p>`,
			[]string{"aBc"}, []string{`?se=REDACTED&amp;sig=REDACTED&amp;sp=REDACTED</p>`}},
		{`error="Put \"https://a.blob.core.windows.net/c/f?comp=block&sig=aBc&sv=2025-11-05\": EOF"`,
			[]string{"aBc"}, []string{`&sig=REDACTED&sv=REDACTED\": EOF"`}},
		// Squid's error page mails the request line percent-encoded; others escape "&" by number
		{`<a href="mailto:webmaster?subject=CacheErrorInfo%20-%20ERR_ACCESS_DENIED&body=GET%20%2Fc%2Ff%3Fse%3D2026-09-24%26sig%3DaBc%252Fd%26sp%3Dr%20HTTP%2F1.1">`,
			[]string{"aBc"}, []string{`?subject=CacheErrorInfo%20-%20ERR_ACCESS_DENIED&body=GET%20%2Fc%2Ff%3Fse%3DREDACTED">`}},
		{`<p>https://a.blob.core.windows.net/c/f?se=2026-09-24&#38;sig=aBc%3D&#38;sp=r</p>`,
			[]string{"aBc"}, []string{`?se=REDACTED&#38;sig=REDACTED&#38;sp=REDACTED</p>`}},
		// the credentials endpoint's own fields, as an error page can echo them
		{`{"storageType":"S3Storage","accessKey":"FAKEACCESS","secretKey":"FAKESECRET","sessionToken":"FAKESESSION"}`,
			[]string{"FAKEACCESS", "FAKESECRET", "FAKESESSION"},
			[]string{`{"storageType":"S3Storage","accessKey":"REDACTED","secretKey":"REDACTED","sessionToken":"REDACTED"}`}},
		{"DefaultEndpointsProtocol=https;AccountName=myaccount;AccountKey=abc123secret456+base64==;EndpointSuffix=core.windows.net",
			[]string{"abc123secret456"}, []string{"AccountKey=REDACTED", "AccountName=myaccount"}},
		// credentials without a digit, which prose tells apart by where they stand
		{"Authorization: Bearer abcdef", []string{"abcdef"}, []string{"Authorization: Bearer REDACTED"}},
		{"Token abcdef", []string{"abcdef"}, []string{"Token REDACTED"}},
		{"map[Authorization:[Token abcdef] Content-Type:[application/json]]", []string{"abcdef"},
			[]string{"map[Authorization:[Token REDACTED] Content-Type:[application/json]]"}},
		{"Proxy-Authorization: Basic dXNlcjpwYXNz", []string{"dXNlcjpwYXNz"}, []string{"Proxy-Authorization: Basic REDACTED"}},
		// any scheme, the whole value, and a standalone credential in any case
		{"Authorization: Negotiate abcdef", []string{"abcdef"}, []string{"Authorization: Negotiate REDACTED"}},
		{"Authorization: AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260923/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=fe5f80f7",
			[]string{"AKIDEXAMPLE", "fe5f80f7"}, []string{"Authorization: AWS4-HMAC-SHA256 REDACTED"}},
		// quoted parameters, and the header escaped or in a list, which keeps its structure
		{`Authorization: Digest username="u", realm="r", response="FAKEDIGEST"`, []string{"FAKEDIGEST"}, []string{`Authorization: Digest REDACTED`}},
		{`{"headers":{"Authorization":"Digest username=\"u\", response=\"FAKEDIGEST\""},"code":"AuthorizationFailure"}`, []string{"FAKEDIGEST"},
			[]string{`{"headers":{"Authorization":"Digest REDACTED"},"code":"AuthorizationFailure"}`}},
		{`msg="{\"Authorization\": \"Negotiate abcdef\"}"`, []string{"abcdef"}, []string{`msg="{\"Authorization\": \"Negotiate REDACTED\"}"`}},
		{`Authorization: ["Basic abcdef"] <Message>token abcdef</Message>`, []string{"abcdef"},
			[]string{`Authorization: ["Basic REDACTED"] <Message>token REDACTED</Message>`}},
		{"bearer abcdef", []string{"abcdef"}, []string{"bearer REDACTED"}},
		{`{"auth":"Bearer abcdef"}`, []string{"abcdef"}, []string{`{"auth":"Bearer REDACTED"}`}},
		{"token abcdef", []string{"abcdef"}, []string{"token REDACTED"}},
		{"credentials error: AKIAIOSFODNN7EXAMPLE used for request", []string{"AKIAIOSFODNN7EXAMPLE"}, []string{"[REDACTED_AWS_KEY]"}},
	}
	for _, tt := range redacted {
		got := RedactSecrets(tt.input)
		for _, secret := range tt.secrets {
			if strings.Contains(got, secret) || strings.Contains(RedactError(tt.input), secret) {
				t.Errorf("RedactSecrets(%q) = %q; want %q out of it and out of a report", tt.input, got, secret)
			}
		}
		for _, want := range tt.kept {
			if !strings.Contains(got, want) {
				t.Errorf("RedactSecrets(%q) = %q, want it to contain %q", tt.input, got, want)
			}
		}
	}

	// Exactly the credential goes: a quoted key keeps its quotes; an escaped
	// Digest parameter, one with spaces around "=" and each value of a list go;
	// and the field that follows stays.
	for _, tt := range [][2]string{
		{`AWS_SECRET_ACCESS_KEY="FAKEKEY" secret_key='FAKEKEY' AccountKey="FAKEKEY"`, `AWS_SECRET_ACCESS_KEY="REDACTED" secret_key='REDACTED' AccountKey="REDACTED"`},
		{`msg="session_token=\"FAKEKEY\"" {"message":"secret_key=\"FAKEKEY\""}`, `msg="session_token=\"REDACTED\"" {"message":"secret_key=\"REDACTED\""}`},
		{`error="{\"SecretKey\": \"FAKEKEY\", \"SESSION_TOKEN\":\"FAKEKEY\"}" {'accessKey': 'FAKEKEY'}`,
			`error="{\"SecretKey\": \"REDACTED\", \"SESSION_TOKEN\":\"REDACTED\"}" {'accessKey': 'REDACTED'}`},
		// a JSON credential field's whole value, escapes and punctuation too, at
		// any depth of escaping
		{`{"secretKey":"\u0046AKESECRET","sessionToken":"FAKE/SESSION\/TAIL"}`, `{"secretKey":"REDACTED","sessionToken":"REDACTED"}`},
		{`{"accessKey":"FAKE KEY, WITH \"QUOTES\"; AND } ] <&>","code":"x"}`, `{"accessKey":"REDACTED","code":"x"}`},
		{`{"SeCrEtKeY": "FAKE", "SecretAccessKey":"FAKE", "aws_session_token" : "FAKE"}`,
			`{"SeCrEtKeY": "REDACTED", "SecretAccessKey":"REDACTED", "aws_session_token" : "REDACTED"}`},
		{`{"error":"{\"secretKey\":\"FAKE\\\"KEY\\\\\",\"code\":\"x\"}"}`, `{"error":"{\"secretKey\":\"REDACTED\",\"code\":\"x\"}"}`},
		{`{"log":"{\"error\":\"{\\\"sessionToken\\\":\\\"FAKE\\\\\\\"TOKEN\\\"}\"}"}`,
			`{"log":"{\"error\":\"{\\\"sessionToken\\\":\\\"REDACTED\\\"}\"}"}`},
		// escaped whitespace before the value; a ' value ends at the string around it
		{`{"error":"{\"secretKey\":\n\"FAKESECRET\"}"}`, `{"error":"{\"secretKey\":\n\"REDACTED\"}"}`},
		{`{"log":"{\"error\":\"{\\\"secretKey\\\":\\r\\n\\t\\\"FAKESECRET\\\"}\"}"}`,
			`{"log":"{\"error\":\"{\\\"secretKey\\\":\\r\\n\\t\\\"REDACTED\\\"}\"}"}`},
		{`{"message":"'secretKey':'unterminated"}`, `{"message":"'secretKey':'REDACTED"}`},
		{`{"log":"{\"message\":\"'secretKey':'unterminated\"}"}`, `{"log":"{\"message\":\"'secretKey':'REDACTED\"}"}`},
		{`error="Authorization: Digest username=\"u\", response=\"FAKEDIGEST\"" status=401`, `error="Authorization: Digest REDACTED" status=401`},
		{`{"message":"Authorization: Digest username=\"u\", response=\"FAKEDIGEST\"","code":"AuthorizationFailure"}`, `{"message":"Authorization: Digest REDACTED","code":"AuthorizationFailure"}`},
		{`Authorization: Digest username = "u", response = "FAKEDIGEST"`, `Authorization: Digest REDACTED`},
		{`{"Authorization":["Basic FAKEONE","Basic FAKETWO"],"code":"AuthorizationFailure"}`, `{"Authorization":["Basic REDACTED","Basic REDACTED"],"code":"AuthorizationFailure"}`},
		{`{'Authorization': ['Negotiate FAKEONE', 'Basic FAKETWO']}`, `{'Authorization': ['Negotiate REDACTED', 'Basic REDACTED']}`},
		{`msg="{\"Authorization\":[\"Basic FAKEONE\",\"Basic FAKETWO\"]}"`, `msg="{\"Authorization\":[\"Basic REDACTED\",\"Basic REDACTED\"]}"`},
		// as json.Indent prints a list, which azcore's errors do, and with CRLF
		{"{\n  \"Authorization\": [\n    \"Basic FAKEONE\",\n    \"NTLM FAKETWO\"\n  ],\n  \"code\": \"AuthorizationFailure\"\n}",
			"{\n  \"Authorization\": [\n    \"Basic REDACTED\",\n    \"NTLM REDACTED\"\n  ],\n  \"code\": \"AuthorizationFailure\"\n}"},
		{"{\r\n\"Authorization\": [\r\n\t\"NTLM FAKEONE\",\r\n\t\"Basic FAKETWO\"\r\n],\r\n\"code\": \"AuthorizationFailure\"}",
			"{\r\n\"Authorization\": [\r\n\t\"NTLM REDACTED\",\r\n\t\"Basic REDACTED\"\r\n],\r\n\"code\": \"AuthorizationFailure\"}"},
		{`error="Authorization: Basic FAKEPADDED==" status="401"`, `error="Authorization: Basic REDACTED" status="401"`},
		{`{"error":{"message":"Authorization: Basic FAKEPADDED==","code":"AuthorizationFailure"}}`, `{"error":{"message":"Authorization: Basic REDACTED","code":"AuthorizationFailure"}}`},
	} {
		if got := RedactSecrets(tt[0]); got != tt[1] || json.Valid([]byte(tt[0])) && !json.Valid([]byte(got)) {
			t.Errorf("RedactSecrets(%q) = %q, want %q", tt[0], got, tt[1])
		}
	}

	// Text without a credential reads as written: job names made of parameters,
	// the words "token" and "Authorization", and the S3 SDK's own error, whose
	// URL carries none.
	for _, clean := range []string{
		"connection timeout after 30 seconds",
		"✗ mass=5_1_phase=2_case=3: response=500 thickness=0.5",
		"--config /tmp/cfg/Token has the name of the token file, which holds the API key; give the configuration file another name",
		"failed to read token file: open /tmp/cfg/token: permission denied",
		"the API token 5 of 7 expired",
		"Authorization unavailable: owner SID not captured at daemon startup",
		"Token file could not be read",
		"token file could not be read",
		"bearer of bad news",
		"access key: missing for this storage",
		`{"mysecretKey":"results.dat","field":"secretKey","errors":{"accessKey":["required"]}}`,
		`{"detail":"Token is invalid or expired"}`,
		`operation error S3: PutObject, exceeded maximum number of attempts, 1, https response error StatusCode: 0, RequestID: , ` +
			`HostID: , request send failed, Put "https://b.s3.us-east-1.amazonaws.com/user/in.dat?x-id=PutObject": connect: connection refused`,
	} {
		if got := RedactSecrets(clean); got != clean {
			t.Errorf("RedactSecrets(%q) = %q, want it unchanged", clean, got)
		}
	}
}

func TestRedactTimelineEntry_LogEvent(t *testing.T) {
	e := &events.LogEvent{
		BaseEvent: events.BaseEvent{EventType: events.EventLog, Time: time.Now()},
		Level:     events.ErrorLevel,
		Message:   "failed to connect to host",
		Stage:     "upload",
	}
	entry := RedactTimelineEntry(e, 0)
	if entry.Type != "log" {
		t.Errorf("expected type 'log', got %q", entry.Type)
	}
	if !strings.Contains(entry.Summary, "ERROR") {
		t.Errorf("expected summary to contain 'ERROR', got %q", entry.Summary)
	}
}

func TestRedactTimeline_EntryLimit(t *testing.T) {
	tests := []struct {
		name  string
		count int
		want  int
	}{
		{"more events than the limit", 30, 20},
		{"fewer events than the limit", 5, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawEvents := make([]events.Event, tt.count)
			for i := range rawEvents {
				rawEvents[i] = &events.LogEvent{
					BaseEvent: events.BaseEvent{EventType: events.EventLog, Time: time.Now()},
					Level:     events.InfoLevel,
					Message:   "test message",
				}
			}

			entries := RedactTimeline(rawEvents, 20)
			if len(entries) != tt.want {
				t.Errorf("expected %d entries, got %d", tt.want, len(entries))
			}
		})
	}
}

// --- Builder tests ---

func TestBuilder_Build(t *testing.T) {
	b := NewBuilder("gui")
	classified := &ClassifiedError{
		ErrorID:      "test-id",
		Category:     CategoryTransfer,
		Severity:     SeverityError,
		Operation:    "folder_upload",
		Backend:      "s3",
		ErrorMessage: "connection refused",
		ErrorClass:   ClassNetwork,
	}
	timeline := []events.SanitizedTimelineEntry{
		{Timestamp: "2024-01-01T00:00:00Z", Type: "log", Summary: "test"},
	}

	report := b.Build(classified, timeline, "I was uploading files")
	if report == nil {
		t.Fatal("expected non-nil report")
	}
	if report.ReportVersion != "1.0" {
		t.Errorf("expected version 1.0, got %q", report.ReportVersion)
	}
	if report.Mode != "gui" {
		t.Errorf("expected mode 'gui', got %q", report.Mode)
	}
	if report.ErrorID != "test-id" {
		t.Errorf("expected errorID 'test-id', got %q", report.ErrorID)
	}
	if report.Category != "transfer" {
		t.Errorf("expected category 'transfer', got %q", report.Category)
	}
	if len(report.Timeline) != 1 {
		t.Errorf("expected 1 timeline entry, got %d", len(report.Timeline))
	}
	if report.UserNote != "I was uploading files" {
		t.Errorf("expected user note, got %q", report.UserNote)
	}
}

func TestBuilder_NilClassified(t *testing.T) {
	b := NewBuilder("cli")
	report := b.Build(nil, nil, "")
	if report != nil {
		t.Error("expected nil report for nil classified error")
	}
}

func TestBuilder_EmptyTimeline(t *testing.T) {
	b := NewBuilder("cli")
	classified := &ClassifiedError{
		ErrorID:      "test-id",
		Category:     CategoryTransfer,
		Severity:     SeverityError,
		Operation:    "file_download",
		ErrorMessage: "some error",
		ErrorClass:   ClassInternal,
	}
	report := b.Build(classified, nil, "")
	if report == nil {
		t.Fatal("expected non-nil report")
	}
	if report.Timeline != nil {
		t.Errorf("expected nil timeline, got %v", report.Timeline)
	}
}

// --- Reporter tests ---

func TestReporter_Report_Reportable(t *testing.T) {
	eb := events.NewEventBus(100)
	defer eb.Close()

	// Subscribe to capture the published event
	ch := eb.Subscribe(events.EventReportableError)

	r := NewReporter(eb)
	// Use a server error (5xx) — user-fixable errors like "connection refused" are no longer reportable
	errorID := r.Report(errors.New("API returned 500 internal server error"), CategoryTransfer, "folder_upload", "s3")
	if errorID == "" {
		t.Fatal("expected non-empty errorID for reportable error")
	}

	// Verify event was published
	select {
	case event := <-ch:
		re, ok := event.(*events.ReportableErrorEvent)
		if !ok {
			t.Fatalf("expected ReportableErrorEvent, got %T", event)
		}
		if re.ErrorID != errorID {
			t.Errorf("expected errorID %q, got %q", errorID, re.ErrorID)
		}
		if re.Category != "transfer" {
			t.Errorf("expected category 'transfer', got %q", re.Category)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ReportableErrorEvent")
	}
}

func TestReporter_Report_UserFixableNotReportable(t *testing.T) {
	eb := events.NewEventBus(100)
	defer eb.Close()

	r := NewReporter(eb)

	// Auth, network, timeout, disk space, client errors — user can fix these
	tests := []struct {
		name string
		err  error
	}{
		{"auth", errors.New("401 unauthorized")},
		{"network", errors.New("connection refused")},
		{"timeout", errors.New("context deadline exceeded")},
		{"disk", errors.New("no space left on device")},
		{"client 404", errors.New("status 404 not found")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errorID := r.Report(tt.err, CategoryTransfer, "folder_upload", "s3")
			if errorID != "" {
				t.Errorf("expected empty errorID for user-fixable %s error, got %q", tt.name, errorID)
			}
		})
	}
}

func TestReporter_Report_NotReportable(t *testing.T) {
	eb := events.NewEventBus(100)
	defer eb.Close()

	r := NewReporter(eb)
	errorID := r.Report(context.Canceled, CategoryTransfer, "folder_upload", "s3")
	if errorID != "" {
		t.Errorf("expected empty errorID for non-reportable error, got %q", errorID)
	}
}

func TestClassifyAndPublish(t *testing.T) {
	eb := events.NewEventBus(100)
	defer eb.Close()

	ch := eb.Subscribe(events.EventReportableError)

	errorID := ClassifyAndPublish(eb, errors.New("500 internal server error"), CategoryPURPipeline, "run", "")
	if errorID == "" {
		t.Fatal("expected non-empty errorID")
	}

	select {
	case event := <-ch:
		re := event.(*events.ReportableErrorEvent)
		if re.ErrorClass != "server_error" {
			t.Errorf("expected error class 'server_error', got %q", re.ErrorClass)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestClassifyAndPublish_NilBus(t *testing.T) {
	errorID := ClassifyAndPublish(nil, errors.New("error"), CategoryTransfer, "op", "")
	if errorID != "" {
		t.Errorf("expected empty errorID with nil bus, got %q", errorID)
	}
}

// --- Transport tests ---

func TestFileTransport_Save(t *testing.T) {
	report := &ErrorReport{
		ReportVersion: "1.0",
		ErrorID:       "test-id",
		Category:      "transfer",
		ErrorMessage:  "test error",
	}

	dir := t.TempDir()
	path := dir + "/report.json"

	ft := &FileTransport{}
	if err := ft.Save(report, path); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !strings.Contains(string(data), `"errorID": "test-id"`) {
		t.Errorf("report does not contain expected errorID, got: %s", string(data))
	}
	if !strings.Contains(string(data), `"reportVersion": "1.0"`) {
		t.Error("report does not contain version")
	}
}

func TestFormatTextSummary(t *testing.T) {
	report := &ErrorReport{
		Severity:     "error",
		Category:     "transfer",
		Operation:    "folder_upload",
		ErrorClass:   "network",
		ErrorMessage: "connection refused",
		ErrorID:      "abc-123",
	}
	summary := FormatTextSummary(report)
	if !strings.Contains(summary, "transfer") {
		t.Errorf("expected 'transfer' in summary, got: %s", summary)
	}
	if !strings.Contains(summary, "connection refused") {
		t.Errorf("expected error message in summary, got: %s", summary)
	}
	if !strings.Contains(summary, "abc-123") {
		t.Errorf("expected error ID in summary, got: %s", summary)
	}
}

// --- CLI helper tests ---

func TestHandleCLIError_Nil(t *testing.T) {
	path := HandleCLIError(nil, "cli", "upload", "")
	if path != "" {
		t.Errorf("expected empty path for nil error, got %q", path)
	}
}

func TestHandleCLIError_NotReportable(t *testing.T) {
	path := HandleCLIError(context.Canceled, "cli", "upload", "")
	if path != "" {
		t.Errorf("expected empty path for non-reportable error, got %q", path)
	}
}

// --- CLI usage error filter tests ---

func TestIsCLIUsageError(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		// Cobra parse errors — user typos
		{"unknown flag", "unknown flag: --token", true},
		{"unknown command", `unknown command "folder" for "rescale-int"`, true},
		{"unknown shorthand", "unknown shorthand flag: 'x' in -x", true},
		{"required flag", `required flag "api-key" not set`, true},
		{"invalid argument", `invalid argument "abc" for "--limit"`, true},
		{"bad flag syntax", "bad flag syntax: --foo=", true},
		{"flag needs argument", "flag needs an argument: --output", true},
		{"arg count", "accepts 1 arg(s), received 0", true},

		// Local path validation errors — user gave bad path
		{"file not found", "file not found: /nonexistent/path/file.txt", true},
		{"dir stat error", "failed to access directory: stat /bad/path: no such file or directory", true},

		// User-initiated cancellation at app level
		{"upload cancelled", "cannot skip root folder with --skip-folder-conflicts - upload cancelled", true},
		{"download cancelled", "operation cancelled by user - download cancelled", true},

		// Validation errors — bad IDs, no matching files
		{"no valid files download", "no valid files to download", true},
		{"no valid files upload", "no valid files to upload", true},
		{"no files found", "no files found matching criteria", true},

		// Refusals of the flags given, which name them first
		{"config is the token", "--config /tmp/cfg/Token has the name of the token file, which holds the API key; give the configuration file another name", true},
		{"conflicting flags", "only one of --overwrite, --skip, or --resume can be specified", true},
		{"one flag twice", "use either --job-id or --id, not both: they are the same flag, so passing both discards one of the values", true},
		{"flags that exclude", "cannot use both --ids and --jobs-csv", true},

		// Real errors — should NOT be filtered
		{"failure naming a flag", "failed to locate folder for XyZ: get file info failed: status 503: unavailable (use --permanent to delete by ID without trashing)", false},
		{"server error", "API returned 500 internal server error", false},
		{"auth error", "401 unauthorized", false},
		{"network error", "connection refused", false},
		{"batch failure", "batch download failed: 5/5 transfers failed", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isCLIUsageError(tt.msg)
			if got != tt.want {
				t.Errorf("isCLIUsageError(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

func TestCategoryFromOperation(t *testing.T) {
	tests := []struct {
		operation string
		want      ErrorCategory
	}{
		{"rescale-int pur run", CategoryPURPipeline},
		{"rescale-int pur submit-existing", CategoryPURPipeline},
		{"rescale-int jobs submit", CategoryJobCreate},
		{"rescale-int jobs download", CategoryJobCreate},
		{"rescale-int folders upload-dir", CategoryTransfer},
		{"rescale-int files download", CategoryTransfer},
		{"job_download", CategoryJobCreate},
		{"", CategoryTransfer},
	}
	for _, tt := range tests {
		t.Run(tt.operation, func(t *testing.T) {
			got := categoryFromOperation(tt.operation)
			if got != tt.want {
				t.Errorf("categoryFromOperation(%q) = %q, want %q", tt.operation, got, tt.want)
			}
		})
	}
}

// Nothing else prunes the report directory. A repeating failure writes one file
// per occurrence, so the writer has to cap what it keeps — and it must keep the
// newest ones.
func TestPruneOldReportsKeepsNewest(t *testing.T) {
	dir := t.TempDir()

	base := time.Now().Add(-100 * time.Hour)
	var newest, oldest string
	for i := 0; i < 10; i++ {
		path := filepath.Join(dir, fmt.Sprintf("report-file-%02d.json", i))
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		mod := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
		if i == 0 {
			oldest = path
		}
		newest = path
	}

	// An unrelated file must survive: the cap applies to reports only.
	other := filepath.Join(dir, "keep-me.txt")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", other, err)
	}

	pruneOldReports(dir, 4)

	remaining, err := filepath.Glob(filepath.Join(dir, "report-*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(remaining) != 4 {
		t.Errorf("kept %d reports, want 4: %v", len(remaining), remaining)
	}
	if _, err := os.Stat(newest); err != nil {
		t.Errorf("newest report was pruned: %v", err)
	}
	if _, err := os.Stat(oldest); !os.IsNotExist(err) {
		t.Error("oldest report should have been pruned")
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("non-report file was removed: %v", err)
	}

	// Under the cap, nothing is touched.
	pruneOldReports(dir, 100)
	if again, _ := filepath.Glob(filepath.Join(dir, "report-*.json")); len(again) != 4 {
		t.Errorf("prune under the cap changed the directory: %v", again)
	}
	// A non-positive cap is a no-op, not "delete everything".
	pruneOldReports(dir, 0)
	if again, _ := filepath.Glob(filepath.Join(dir, "report-*.json")); len(again) != 4 {
		t.Errorf("prune with keep=0 deleted files: %v", again)
	}
}

// TestIsCLIUsageError_MissingFlags covers commands that validate their own
// required flags: "--job-id (or --id) is required" is a keyboard mistake, not
// something to raise a diagnostic report about.
func TestIsCLIUsageError_MissingFlags(t *testing.T) {
	usage := []string{
		"--job-id (or --id) is required",
		"at least one --fileid is required",
		"--name is required",
		`required flag(s) "job-id" not set`,
		"cannot prompt for a file conflict: no interactive terminal (stdin is not a TTY) — decide up front with --continue-on-error",
		"config init requires an interactive terminal; to configure non-interactively, set RESCALE_API_KEY",
		"deleting files needs confirmation but stdin is not a terminal — re-run with --confirm",
		"upload aborted by user",
	}
	for _, msg := range usage {
		if !isCLIUsageError(msg) {
			t.Errorf("expected a usage error: %q", msg)
		}
	}

	notUsage := []string{
		"failed to upload file: 500 internal server error",
		"download failed for \"a.dat\": storage unreachable",
		"retries exhausted after 1m30s (limit 1m30s, 6 attempt(s)): 503 service unavailable",
	}
	for _, msg := range notUsage {
		if isCLIUsageError(msg) {
			t.Errorf("expected a real failure, not a usage error: %q", msg)
		}
	}
}

// TestIsAggregateFailure verifies batch roll-ups do not raise their own report:
// the individual failures were already shown item by item.
func TestIsAggregateFailure(t *testing.T) {
	aggregates := []string{
		"3 file(s) failed to upload",
		"pipeline failed: 2 of 10 job(s) failed",
		"2 deletion(s) failed",
		"some files failed to download",
	}
	for _, msg := range aggregates {
		if !isAggregateFailure(msg) {
			t.Errorf("expected an aggregate summary: %q", msg)
		}
	}

	if isAggregateFailure("failed to list job files: 503 service unavailable") {
		t.Error("a single concrete failure must stay reportable")
	}
}

// TestIsCLIUsageError_RequiredNeedsFlagMarker keeps "is required" from swallowing
// internal invariants and API error bodies — those are precisely what a
// diagnostic report is for.
func TestIsCLIUsageError_RequiredNeedsFlagMarker(t *testing.T) {
	notUsage := []string{
		"storageInfo is required",
		"apiClient is required",
		"failed to create provider: storageInfo is required",
		`failed to create job: status 400: {"detail":"name is required"}`,
		"either FileID or FileInfo is required",
	}
	for _, msg := range notUsage {
		if isCLIUsageError(msg) {
			t.Errorf("internal/API failure misread as a usage error: %q", msg)
		}
	}

	usage := []string{
		"--job-id (or --id) is required",
		"at least one --fileid is required",
		"--folder-id is required",
	}
	for _, msg := range usage {
		if !isCLIUsageError(msg) {
			t.Errorf("expected a usage error: %q", msg)
		}
	}
}

// TestAggregateSuppressionIsSymmetric pins the shape that matters: files upload's
// own error must be reported whether one file failed or several. A loose
// "file(s) failed" match made two failures silent while one reported normally.
func TestAggregateSuppressionIsSymmetric(t *testing.T) {
	one := "failed to upload /tmp/a.dat: 500 internal server error"
	many := "upload failed: 3 file(s) failed (first error: 500 internal server error)"

	if isAggregateFailure(one) {
		t.Errorf("single-failure error must stay reportable: %q", one)
	}
	if isAggregateFailure(many) {
		t.Errorf("files upload's primary error must stay reportable: %q", many)
	}

	// The compat twin uses the same wording.
	if isAggregateFailure("upload failed: 2 file(s) failed (first error: EOF)") {
		t.Error("compat upload's primary error must stay reportable")
	}

	// True roll-ups, where each failure was already listed for the user.
	rollups := []string{
		"3 file(s) failed to upload",
		"pipeline failed: 2 of 10 job(s) failed",
		"2 deletion(s) failed",
		"some files failed to download",
	}
	for _, msg := range rollups {
		if !isAggregateFailure(msg) {
			t.Errorf("expected a roll-up: %q", msg)
		}
	}
}
