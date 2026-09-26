package azure

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/providers/testsupport"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// credsWithPath builds credentials holding one per-file SAS entry for path.
func credsWithPath(containerSAS, path, perFileSAS string) *models.AzureCredentials {
	return &models.AzureCredentials{
		SASToken: containerSAS,
		Paths: []models.AzureCredentialPath{
			{
				Path:      path,
				PathParts: &models.CloudFilePathParts{Container: "example-container", Path: path},
				SASToken:  perFileSAS,
			},
		},
	}
}

func TestBuildSASURL(t *testing.T) {
	tests := []struct {
		name        string
		accountName string
		storageAcct string
		creds       *models.AzureCredentials
		fileInfo    *models.CloudFile
		wantErr     string   // when set, buildSASURL must fail and name this
		wantExact   string   // when set, the URL must match exactly
		wantSubstr  []string // fragments the URL must contain
		wantAbsent  []string // fragments the URL must not contain
	}{
		{
			name:        "account name from connection settings",
			accountName: "myaccount",
			creds:       &models.AzureCredentials{SASToken: "sv=2021-06-08&ss=b&sig=abc"},
			wantExact:   "https://myaccount.blob.core.windows.net/?sv=2021-06-08&ss=b&sig=abc",
		},
		{
			name:        "falls back to StorageAccount",
			storageAcct: "legacyaccount",
			creds:       &models.AzureCredentials{SASToken: "sv=2021-06-08&sig=def"},
			wantSubstr:  []string{"legacyaccount.blob.core.windows.net"},
		},
		{
			name:    "no account name at all",
			creds:   &models.AzureCredentials{SASToken: "sv=2021-06-08&sig=ghi"},
			wantErr: "account name not found",
		},
		{
			// The SDK parses the URL for each request, and a parse error quotes it.
			name:        "a token no URL can hold",
			accountName: "myaccount",
			creds:       &models.AzureCredentials{SASToken: "sv=2021-06-08&sig=jkl\n"},
			wantErr:     "invalid control character",
		},
		{
			// A shared job's file carries its own SAS; the container-level one
			// would not grant access to it.
			name:        "per-file SAS wins for a shared file",
			accountName: "sharedaccount",
			creds:       credsWithPath("container-level-sas", "user/abc/shared-output.dat", "per-file-sas-for-shared"),
			fileInfo: &models.CloudFile{
				PathParts: &models.CloudFilePathParts{Container: "example-container", Path: "user/abc/shared-output.dat"},
			},
			wantSubstr: []string{"per-file-sas-for-shared"},
			wantAbsent: []string{"container-level-sas"},
		},
		{
			name:        "falls back to container SAS when no path matches",
			accountName: "myaccount",
			creds:       credsWithPath("container-level-sas", "user/abc/different-file.dat", "per-file-sas-other"),
			fileInfo: &models.CloudFile{
				PathParts: &models.CloudFilePathParts{Container: "example-container", Path: "user/abc/wanted-file.dat"},
			},
			wantSubstr: []string{"container-level-sas"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storageInfo := &models.StorageInfo{
				ConnectionSettings: models.ConnectionSettings{
					AccountName:    tt.accountName,
					StorageAccount: tt.storageAcct,
				},
			}

			var url string
			var err error
			if tt.fileInfo != nil {
				url, err = buildSASURL(storageInfo, tt.creds, tt.fileInfo)
			} else {
				url, err = buildSASURL(storageInfo, tt.creds)
			}

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("buildSASURL() = %q, want error", url)
				}
				if !strings.Contains(err.Error(), tt.wantErr) || strings.Contains(err.Error(), "sig=") {
					t.Errorf("buildSASURL() error = %q, want mention of %q and no SAS", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildSASURL() error = %v", err)
			}
			if tt.wantExact != "" && url != tt.wantExact {
				t.Errorf("buildSASURL() = %q, want %q", url, tt.wantExact)
			}
			for _, want := range tt.wantSubstr {
				if !strings.Contains(url, want) {
					t.Errorf("buildSASURL() = %q, should contain %q", url, want)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(url, absent) {
					t.Errorf("buildSASURL() = %q, should not contain %q", url, absent)
				}
			}
		})
	}
}

func TestGetPerFileSASToken(t *testing.T) {
	tests := []struct {
		name    string
		creds   *models.AzureCredentials
		lookup  string
		wantSAS string
	}{
		{
			name:    "path match returns the per-file token",
			creds:   credsWithPath("container-level-sas", "user/abc/file1.dat", "per-file-sas-token"),
			lookup:  "user/abc/file1.dat",
			wantSAS: "per-file-sas-token",
		},
		{
			name:    "no match falls back to the container token",
			creds:   credsWithPath("container-level-sas", "user/abc/other.dat", "per-file-sas-token"),
			lookup:  "user/abc/wanted.dat",
			wantSAS: "container-level-sas",
		},
		{
			name:    "empty path list falls back to the container token",
			creds:   &models.AzureCredentials{SASToken: "container-level-sas", Paths: []models.AzureCredentialPath{}},
			lookup:  "user/abc/file1.dat",
			wantSAS: "container-level-sas",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetPerFileSASToken(tt.creds, tt.lookup); got != tt.wantSAS {
				t.Errorf("GetPerFileSASToken() = %q, want %q", got, tt.wantSAS)
			}
		})
	}
}

// newCountingAzureCredentialsAPI is the credential endpoint with a counter and a
// different SAS token per response, so a test can see how many replacements the
// storage credential actually went through.
func newCountingAzureCredentialsAPI(t *testing.T) (*api.Client, *atomic.Int32) {
	t.Helper()

	var fetches atomic.Int32
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		generation := fetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"storageType":"AzureStorage","sasToken":"sv=2021-06-08&sig=test-%d"}`, generation)
	}))
	t.Cleanup(server.Close)

	return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), &fetches
}

// newCredentialTestAzureClient is an AzureClient whose blob endpoint is a stub:
// these tests drive RetryWithBackoff directly, so the only traffic that matters
// is the credential fetching.
func newCredentialTestAzureClient(t *testing.T, apiClient *api.Client) *AzureClient {
	t.Helper()
	server := httptest.NewTLSServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
	}))
	t.Cleanup(server.Close)

	return &AzureClient{
		storageInfo: &models.StorageInfo{
			StorageType: "AzureStorage",
			ConnectionSettings: models.ConnectionSettings{
				Container:   testContainer,
				AccountName: testAccount,
			},
		},
		credManager: credentials.GetManager(apiClient),
		apiClient:   apiClient,
		httpClient:  testsupport.RedirectingHTTPClient(server.Listener.Addr().String()),
	}
}

// TestLateRejectionLeavesTheReplacementCredentialInPlace is N6 on Azure. The
// retry wrapper invalidated whatever SAS token was current when the rejection
// came back, not the one the attempt actually ran on: a block rejected on
// generation G, released after G had been replaced by H, dropped the healthy H.
func TestLateRejectionLeavesTheReplacementCredentialInPlace(t *testing.T) {
	apiClient, fetches := newCountingAzureCredentialsAPI(t)
	azureClient := newCredentialTestAzureClient(t, apiClient)
	ctx := context.Background()

	// Generation G: what the attempt below runs on.
	if err := azureClient.EnsureFreshCredentials(ctx); err != nil {
		t.Fatalf("failed to install the first credential: %v", err)
	}
	generationG := azureClient.appliedCreds

	replacement := generationG
	rejected := false
	err := azureClient.RetryWithBackoff(ctx, "StageBlock 1", func() error {
		if rejected {
			return nil
		}
		rejected = true

		// While this attempt is in flight, another one replaces G with H.
		azureClient.credManager.InvalidateAzureCredentials(generationG)
		if err := azureClient.EnsureFreshCredentials(ctx); err != nil {
			t.Fatalf("failed to install the replacement credential: %v", err)
		}
		replacement = azureClient.appliedCreds

		// Only now does the service's rejection of generation G arrive.
		return fmt.Errorf("PUT https://testaccount.blob.core.windows.net/: 403 Server failed to authenticate the request, ERROR CODE: AuthenticationFailed")
	})
	if err != nil {
		t.Fatalf("the upload did not recover from a rejected credential: %v", err)
	}
	if replacement == generationG {
		t.Fatal("the test never installed a replacement credential")
	}

	if got := fetches.Load(); got != 2 {
		t.Errorf("credentials were fetched %d time(s), want 2: a rejection of the generation the attempt used must not drop its replacement", got)
	}
	if azureClient.appliedCreds != replacement {
		t.Error("the retry rebuilt the client around a credential other than the replacement that was already installed")
	}
}

type roundTripFunc func(*nethttp.Request) (*nethttp.Response, error)

func (f roundTripFunc) RoundTrip(r *nethttp.Request) (*nethttp.Response, error) { return f(r) }

// A transport failure quotes its request URL, and a blob URL's query is its SAS
// token. The SDK retries, logs and returns that error, inside the retry loop or
// outside it (GetEncryptedSize's byte range), and none of them may carry it.
// Nor may the SDK's error for a proxy's refusal, which quotes the page, and the
// page quotes the request; a blob's bytes stay the blob's, whatever they read.
func TestTransportErrorsCarryNoSAS(t *testing.T) {
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"storageType":"AzureStorage","sasToken":"se=2026-09-24T00%3A00%3A00Z&sig=SECRETSIGNATURE%3D&sp=r&sv=2025-11-05"}`)
	}))
	t.Cleanup(server.Close)
	apiClient := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})
	const content = "https://a.blob.core.windows.net/c/f?sv=2025-11-05&sig=KEPT%3D"
	blob := strings.NewReader(content)
	// Refusals, which the SDK's error quotes: a proxy's page quoting the request,
	// ones past a MiB, and errors holding a storage key before their code.
	refusals := map[string]string{
		"denied.bin": `<?xml version="1.0" encoding="utf-8"?><Error><Code>AuthorizationFailure</Code><Message>Blocked: URL</Message></Error>`,
		"long.bin":   `<Error><Code>AuthorizationFailure</Code><Message>` + strings.Repeat("Blocked. ", 1<<18) + `</Message></Error>`,
		"key.json":   `{"error":{"message":"secret_key=SECRETSIGNATURE","code":"AuthorizationFailure"}}`,
		"key.xml":    `<Error><Message>AccountKey=SECRETSIGNATURE</Message><Code>AuthorizationFailure</Code></Error>`,
	}
	gets := 0
	azureClient := &AzureClient{
		storageInfo: &models.StorageInfo{ConnectionSettings: models.ConnectionSettings{Container: testContainer, AccountName: testAccount}},
		credManager: credentials.GetManager(apiClient),
		apiClient:   apiClient,
		httpClient: &nethttp.Client{Transport: roundTripFunc(func(r *nethttp.Request) (*nethttp.Response, error) {
			switch {
			case r.Method == nethttp.MethodHead: // no ETag, so GetEncryptedSize reads the version off a byte range
				return &nethttp.Response{StatusCode: 200, Header: nethttp.Header{"Content-Length": {"4096"}}, Body: nethttp.NoBody, Request: r}, nil
			case refusals[path.Base(r.URL.Path)] != "":
				page := strings.Replace(refusals[path.Base(r.URL.Path)], "URL", r.URL.String(), 1)
				return &nethttp.Response{StatusCode: 403, Status: "403 Blocked " + r.URL.String(), Header: nethttp.Header{}, Body: io.NopCloser(strings.NewReader(page)), Request: r}, nil
			case strings.HasPrefix(path.Base(r.URL.Path), "stall"): // a page that stops arriving until the request ends, as net/http's does
				stalled, _ := io.Pipe()
				context.AfterFunc(r.Context(), func() { stalled.CloseWithError(r.Context().Err()) })
				page := strings.NewReader(`<Error><Code>AuthorizationFailure</Code><Message>Blocked: ` + r.URL.String())
				return &nethttp.Response{StatusCode: 403, Header: nethttp.Header{}, Body: struct {
					io.Reader
					io.Closer
				}{io.MultiReader(page, stalled), stalled}, Request: r}, nil
			case r.Method == nethttp.MethodPut || strings.HasSuffix(r.URL.Path, "/rewritten.bin"): // a success status the SDK does not expect
				page := "<html><body>Blocked by policy: " + r.URL.String() + "</body></html>"
				return &nethttp.Response{StatusCode: map[string]int{"PUT": 200, "GET": 203}[r.Method], Header: nethttp.Header{}, Body: io.NopCloser(strings.NewReader(page)), Request: r}, nil
			case strings.HasSuffix(r.URL.Path, "/whole.bin"):
				return &nethttp.Response{StatusCode: 200, Status: "200 Blocked " + r.URL.String(), Header: nethttp.Header{}, Body: io.NopCloser(strings.NewReader("blob")), Request: r}, nil
			case strings.HasSuffix(r.URL.Path, "/kept.bin"):
				return &nethttp.Response{StatusCode: 206, Status: "206 Blocked " + r.URL.String(), Header: nethttp.Header{}, Body: io.NopCloser(blob), Request: r}, nil
			case strings.HasSuffix(r.URL.Path, "/moved.bin"): // net/http quotes a Location it cannot parse
				return &nethttp.Response{StatusCode: 307, Header: nethttp.Header{"Location": {"https://example.invalid/%zz?" + r.URL.RawQuery}}, Body: nethttp.NoBody, Request: r}, nil
			case strings.HasSuffix(r.URL.Path, "/list.bin"): // GetBlockList's answer passes untouched, and the SDK quotes a 206 it does not expect
				return &nethttp.Response{StatusCode: 206, Header: nethttp.Header{}, Body: io.NopCloser(strings.NewReader("Blocked: " + r.URL.String())), Request: r}, nil
			case strings.HasSuffix(r.URL.Path, "/size.bin"): // and a Size it cannot parse
				page := "<BlockList><UncommittedBlocks><Block><Name>YQ==</Name><Size>" + strings.ReplaceAll(r.URL.String(), "&", "&amp;") + "</Size></Block></UncommittedBlocks></BlockList>"
				return &nethttp.Response{StatusCode: 200, Header: nethttp.Header{}, Body: io.NopCloser(strings.NewReader(page)), Request: r}, nil
			case strings.HasSuffix(r.URL.Path, "/slow.bin"):
				return nil, context.DeadlineExceeded
			case strings.HasSuffix(r.URL.Path, "/cut.bin"): // a refusal whose page times out
				cut := &url.Error{Op: "Get", URL: r.URL.String(), Err: context.DeadlineExceeded}
				return &nethttp.Response{StatusCode: 503, Header: nethttp.Header{}, Body: io.NopCloser(iotest.ErrReader(cut)), Request: r}, nil
			}
			if gets++; gets == 1 {
				return nil, errors.New("read tcp 192.0.2.2:51234->192.0.2.1:443: read: connection reset by peer") // retried
			}
			return nil, errors.New("proxyconnect tcp: Proxy Authentication Required") // not retried
		})},
	}
	var notices []string
	azureClient.retryObserver = cloud.RetryObserver{OnRetry: func(ev cloud.RetryEvent) { notices = append(notices, ev.Err.Error()) }}
	// One try per SDK call: the SDK's own backoff takes seconds and changes no text.
	ctx := policy.WithRetryOptions(context.Background(), policy.RetryOptions{MaxRetries: -1})

	_, streamErr := azureClient.DownloadStream(ctx, "user/job/data.bin", nil)
	provider := &Provider{storageInfo: azureClient.storageInfo, apiClient: apiClient, azureClient: azureClient}
	_, _, sizeErr := provider.GetEncryptedSize(ctx, "user/job/data.bin")
	if len(notices) != 1 || streamErr == nil || sizeErr == nil {
		t.Fatalf("got retries %q and errors %v, %v: want one retry and two failures", notices, streamErr, sizeErr)
	}
	for _, text := range append(notices, streamErr.Error(), sizeErr.Error()) {
		if strings.Contains(text, "SECRETSIGNATURE") || !strings.Contains(text, "data.bin\": ") {
			t.Errorf("reported %q, want the blob named without its SAS", text)
		}
	}
	for _, name := range []string{"list.bin", "size.bin"} {
		if _, err := stagedBlocksExist(ctx, azureClient, "user/job/"+name); err == nil || strings.Contains(err.Error(), "SECRETSIGNATURE") {
			t.Errorf("%s: checking the staged blocks returned %v, want an error without the SAS", name, err)
		}
	}
	// Only a download's 200 or 206 carries the blob; StageBlock expects 201.
	var respErr *azcore.ResponseError
	block := azureClient.Client().ServiceClient().NewContainerClient(testContainer).NewBlockBlobClient("user/job/staged.bin")
	_, stagedErr := block.StageBlock(ctx, "YmxvY2stMDAwMDAw", streaming.NopCloser(strings.NewReader("data")), nil)
	_, rewrittenErr := azureClient.DownloadRangeOnce(ctx, "user/job/rewritten.bin", 0, 1, "")
	for _, err := range []error{stagedErr, rewrittenErr} {
		if !errors.As(err, &respErr) || respErr.StatusCode/100 != 2 || strings.Contains(err.Error(), "SECRETSIGNATURE") {
			t.Errorf("a page refusing the request with a success status returned %v, want the SDK's error without the SAS", err)
		}
	}
	var keptRaw, wholeRaw *nethttp.Response // what the SDK holds of each answer
	kept, err := azureClient.DownloadRangeOnce(policy.WithCaptureResponse(ctx, &keptRaw), "user/job/kept.bin", 0, int64(len(content)), "")
	if err != nil || blob.Len() != len(content) {
		t.Fatalf("DownloadRangeOnce: %v, with %d of the blob's bytes read before its caller asked", err, len(content)-blob.Len())
	}
	if got, err := io.ReadAll(kept.Body); err != nil || string(got) != content {
		t.Errorf("downloaded %q (%v), want the blob's bytes %q", got, err, content)
	}
	// The SDK logs these answers' reason phrases.
	_, wholeErr := azureClient.DownloadRangeOnce(policy.WithCaptureResponse(ctx, &wholeRaw), "user/job/whole.bin", 0, 4, "")
	for _, raw := range []*nethttp.Response{keptRaw, wholeRaw} {
		if wholeErr != nil || raw == nil || !strings.Contains(raw.Status, "&sig=REDACTED&") {
			t.Errorf("a download's answer reached the SDK as %+v (%v), want its reason phrase without the SAS", raw, wholeErr)
		}
	}
	// The whole text goes, and what the retry classifier reads stays.
	_, movedErr := azureClient.DownloadRangeOnce(ctx, "user/job/moved.bin", 0, 1, "")
	if movedErr == nil || strings.Contains(movedErr.Error(), "SECRETSIGNATURE") || !strings.Contains(movedErr.Error(), "&sig=REDACTED&") {
		t.Errorf("a redirect to a signed URL it could not parse returned %v, want it without the SAS", movedErr)
	}
	var netErr net.Error
	var temporary interface{ Temporary() bool }
	for _, name := range []string{"slow.bin", "cut.bin"} {
		_, err := azureClient.DownloadRangeOnce(ctx, "user/job/"+name, 0, 1, "")
		if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &netErr) || !netErr.Timeout() ||
			!errors.As(err, &temporary) || !temporary.Temporary() || strings.Contains(err.Error(), "SECRETSIGNATURE") {
			t.Errorf("%s: a timed-out request returned %v, want a temporary timeout that errors.Is finds, without the SAS", name, err)
		}
	}
	// A refusal keeps its status, error code and page, less its credentials; the
	// page is cut at a MiB, and one that stalls ends where it stalled.
	defer func(wait time.Duration) { errorBodyWait = wait }(errorBodyWait)
	errorBodyWait = 0
	stallCtx, cancel := context.WithTimeout(ctx, time.Minute) // a MiB takes seconds to redact under -race
	defer cancel()
	for name, want := range map[string]string{
		"denied.bin":  "&sig=REDACTED&sp=REDACTED&sv=REDACTED</Message></Error>",
		"long.bin":    "<Message>Blocked. Blocked. ",
		"stalled.bin": "&sig=REDACTED&sp=REDACTED&sv=REDACTED",
		"key.json":    `"message": "secret_key=REDACTED",`, // as the SDK indents it
		"key.xml":     "<Error><Message>AccountKey=REDACTED</Message><Code>AuthorizationFailure</Code></Error>",
	} {
		_, err := azureClient.DownloadRangeOnce(stallCtx, "user/job/"+name, 0, 1, "")
		if !errors.As(err, &respErr) || respErr.StatusCode != 403 || respErr.ErrorCode != "AuthorizationFailure" ||
			len(err.Error()) > 1<<20+1<<10 || strings.Contains(err.Error(), "SECRETSIGNATURE") || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: a refusal returned %.400v, want a 403 AuthorizationFailure without its credentials, in a MiB", name, err)
		}
	}
}

// With AZURE_SDK_GO_LOGGING=all, read once at start-up, the SDK logs each
// response and error to stderr. An error quotes, indented, the page of a status
// the transport passes on untouched, such as GetBlockList's 206.
func TestSDKLogCarriesNoCredentials(t *testing.T) {
	if os.Getenv("AZURE_SDK_GO_LOGGING") != "" {
		client, err := newBlobClient("https://"+testAccount+".blob.core.windows.net/?sv=2025-11-05&sig=SECRETSIGNATURE", &nethttp.Client{
			Transport: roundTripFunc(func(r *nethttp.Request) (*nethttp.Response, error) {
				page := `{"Authorization":["Basic FAKEONE","NTLM FAKETWO"],"url":"` + r.URL.String() + `"}`
				return &nethttp.Response{StatusCode: 206, Status: "206 Blocked " + r.URL.String(), Header: nethttp.Header{},
					Body: io.NopCloser(strings.NewReader(page)), Request: r}, nil
			})})
		if err != nil {
			t.Fatal(err)
		}
		client.ServiceClient().NewContainerClient(testContainer).NewBlockBlobClient("list.bin").GetBlockList(context.Background(), blockblob.BlockListTypeAll, nil)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSDKLogCarriesNoCredentials$")
	cmd.Env = append(os.Environ(), "AZURE_SDK_GO_LOGGING=all")
	out, err := cmd.CombinedOutput()
	if err != nil || strings.Contains(string(out), "SECRETSIGNATURE") || strings.Contains(string(out), "FAKE") ||
		!strings.Contains(string(out), "] ResponseError: GET https://") || !strings.Contains(string(out), "RESPONSE 206: 206 Blocked https://") ||
		!strings.Contains(string(out), "\"NTLM REDACTED\"\n  ],\n  \"url\": ") {
		t.Errorf("with AZURE_SDK_GO_LOGGING=all the SDK printed (%v):\n%s", err, out)
	}
}

// A client that refuses a redirect returns the redirect's response with its
// error, and that response's reason phrase can quote the signed URL too.
func TestRefusedRedirectKeepsItsErrorWithoutTheSAS(t *testing.T) {
	refused := errors.New("redirect refused")
	client := &nethttp.Client{CheckRedirect: func(*nethttp.Request, []*nethttp.Request) error { return refused },
		Transport: roundTripFunc(func(r *nethttp.Request) (*nethttp.Response, error) {
			return &nethttp.Response{StatusCode: 302, Status: "302 Moved " + r.URL.String(), Header: nethttp.Header{"Location": {r.URL.String()}},
				Body: nethttp.NoBody, Request: r}, nil
		})}
	req, _ := nethttp.NewRequest(nethttp.MethodGet, "https://example.invalid/c/f?sv=2025-11-05&sig=SECRETSIGNATURE", nil)
	resp, err := sasFreeTransport{client}.Do(req)
	if !errors.Is(err, refused) || resp == nil || strings.Contains(err.Error()+resp.Status, "SECRETSIGNATURE") || !strings.Contains(resp.Status, "&sig=REDACTED") {
		t.Errorf("a refused redirect returned %v and %+v, want the refusal and a reason phrase without the SAS", err, resp)
	}
}

type closeCounter struct {
	io.Reader
	closed int
}

func (c *closeCounter) Close() error { c.closed++; return nil }

// The stall timer can fire just as the read ends, and Timer.Stop does not wait
// for its callback, which can then run after the SDK has replaced the body. It
// closes the page it timed, never what took its place.
func TestLateStallTimerClosesOnlyThePage(t *testing.T) {
	var late func()
	defer func(f func(time.Duration, func()) *time.Timer) { afterFunc = f }(afterFunc)
	afterFunc = func(_ time.Duration, f func()) *time.Timer {
		late = f
		expired := time.NewTimer(0)
		<-expired.C // so Stop reports that the callback has started
		return expired
	}
	page := &closeCounter{Reader: strings.NewReader("<Error><Code>AuthorizationFailure</Code></Error>")}
	client := &nethttp.Client{Transport: roundTripFunc(func(r *nethttp.Request) (*nethttp.Response, error) {
		return &nethttp.Response{StatusCode: 403, Header: nethttp.Header{}, Body: page, Request: r}, nil
	})}
	req, _ := nethttp.NewRequest(nethttp.MethodGet, "https://example.invalid/c/f", nil)
	resp, err := sasFreeTransport{client}.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	replaced := &closeCounter{Reader: resp.Body} // as azcore replaces a body it has read
	resp.Body = replaced
	late()
	if replaced.closed != 0 || page.closed != 2 {
		t.Errorf("the page was closed %d time(s) and its replacement %d, want the page twice, by the read and the callback, and the replacement never",
			page.closed, replaced.closed)
	}
}
