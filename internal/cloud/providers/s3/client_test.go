package s3

import (
	"bytes"
	"context"
	"encoding/pem"
	"fmt"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

func TestShouldUseFIPSEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected bool
	}{
		{"ITAR GovCloud", "https://itar.rescale-gov.com", true},
		{"ITAR Rescale", "https://itar.rescale.com", true},
		{"ITAR GovCloud with path", "https://itar.rescale-gov.com/api/v2/", true},
		{"ITAR case insensitive", "https://ITAR.RESCALE.COM", true},
		{"standard platform", "https://platform.rescale.com", false},
		{"EU platform", "https://eu.rescale.com", false},
		{"KR platform", "https://kr.rescale.com", false},
		{"JP platform", "https://platform.rescale.jp", false},
		{"empty string", "", false},
		{"partial match rescale-gov without itar", "https://rescale-gov.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldUseFIPSEndpoint(tt.url)
			if got != tt.expected {
				t.Errorf("shouldUseFIPSEndpoint(%q) = %v, want %v", tt.url, got, tt.expected)
			}
		})
	}
}

// TestS3ClientIgnoresTheUsersAWSEnvironment: the platform supplies the
// credentials, region and endpoint. Built with LoadDefaultConfig, the client
// also read the user's AWS environment — AWS_CA_BUNDLE failed every build of it,
// and it is rebuilt before every request; AWS_ENDPOINT_URL redirected the
// transfer to another endpoint.
func TestS3ClientIgnoresTheUsersAWSEnvironment(t *testing.T) {
	server := httptest.NewTLSServer(nethttp.NotFoundHandler())
	t.Cleanup(server.Close)
	bundle := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}

	for name, value := range map[string]string{"AWS_CA_BUNDLE": bundle, "AWS_ENDPOINT_URL": "https://example.invalid"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			storage := &models.StorageInfo{ConnectionSettings: models.ConnectionSettings{Container: testBucket, Region: "us-east-1"}}
			client, err := NewS3Client(context.Background(), storage, newFakeCredentialsAPI(t), nil, cloud.RetryObserver{})
			if err != nil {
				t.Fatalf("NewS3Client: %v", err)
			}
			if err := client.EnsureFreshCredentials(context.Background()); err != nil {
				t.Fatalf("EnsureFreshCredentials: %v", err)
			}
			if endpoint := client.Client().Options().BaseEndpoint; endpoint != nil {
				t.Errorf("the client addresses %s, want the bucket's own S3 endpoint", *endpoint)
			}
		})
	}
}

// TestS3ClientChecksumsPartsAndVerifiesResponses: LoadDefaultConfig switched on
// the SDK's CRC32 checks. Built from bare options they were off, so a part went
// out with no checksum for S3 to hold it to, and a response body was not held
// to the checksum S3 sent with it.
func TestS3ClientChecksumsPartsAndVerifiesResponses(t *testing.T) {
	var trailer atomic.Value
	server := httptest.NewTLSServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Method == nethttp.MethodPut {
			trailer.Store(r.Header.Get("X-Amz-Trailer"))
			w.Header().Set("ETag", `"part"`)
			return
		}
		w.Header().Set("x-amz-checksum-crc32", "AAAAAA==") // not the CRC32 of the body
		_, _ = io.WriteString(w, "object bytes")
	}))
	t.Cleanup(server.Close)
	client := newTestS3Client(t, server) // rebuilt by newSDKClient before the upload

	if err := uploadOnePart(context.Background(), client, 1); err != nil {
		t.Fatalf("UploadPart: %v", err)
	}
	if got, _ := trailer.Load().(string); got != "x-amz-checksum-crc32" {
		t.Errorf("the part went out with trailer %q, want its CRC32", got)
	}

	resp, err := client.Client().GetObject(context.Background(), &awss3.GetObjectInput{Bucket: aws.String(testBucket), Key: aws.String("object")})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Error("a body that does not match the CRC32 sent with it was accepted")
	}
}

// newCountingCredentialsAPI is newFakeCredentialsAPI with a counter and a
// different key per response, so a test can see how many replacements the
// storage credential actually went through.
func newCountingCredentialsAPI(t *testing.T) (*api.Client, *atomic.Int32) {
	t.Helper()

	var fetches atomic.Int32
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		generation := fetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"storageType":"S3Storage","accessKey":"test-key-%d","secretKey":"test-secret","sessionToken":"test-token"}`, generation)
	}))
	t.Cleanup(server.Close)

	return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), &fetches
}

func uploadOnePart(ctx context.Context, s3Client *S3Client, partNumber int32) error {
	body := []byte(fmt.Sprintf("part %d", partNumber))
	return s3Client.RetryWithBackoff(ctx, fmt.Sprintf("UploadPart %d", partNumber), func() error {
		_, err := s3Client.Client().UploadPart(ctx, &awss3.UploadPartInput{
			Bucket:        aws.String(testBucket),
			Key:           aws.String(testPathBase + "/object"),
			UploadId:      aws.String(testUploadID),
			PartNumber:    aws.Int32(partNumber),
			Body:          bytes.NewReader(body),
			ContentLength: aws.Int64(int64(len(body))),
		})
		return err
	})
}

// TestRetryAfterRejectedCredentialFetchesAReplacement is the F15 regression:
// the retry branch calls itself a forced refresh, but the refresh reads through
// a cache that keeps serving the same credential for ten minutes. Every attempt
// was rebuilt around the credential the backend had just rejected.
func TestRetryAfterRejectedCredentialFetchesAReplacement(t *testing.T) {
	backend, server := newFakeS3Backend(t)
	backend.rejectOncePerPart = true
	apiClient, fetches := newCountingCredentialsAPI(t)
	s3Client := newTestS3ClientWithAPI(t, server, apiClient)

	if err := uploadOnePart(context.Background(), s3Client, 1); err != nil {
		t.Fatalf("upload did not recover from a rejected credential: %v", err)
	}

	if got := fetches.Load(); got != 2 {
		t.Errorf("credentials were fetched %d time(s), want 2: the rejected one plus its replacement", got)
	}
}

// TestRejectedCredentialIsReplacedOncePerBurst pins the other half of the fix:
// a whole file's worth of parts failing on one rejected credential must cost
// one replacement, not one per part.
func TestRejectedCredentialIsReplacedOncePerBurst(t *testing.T) {
	backend, server := newFakeS3Backend(t)
	backend.rejectOncePerPart = true
	apiClient, fetches := newCountingCredentialsAPI(t)
	s3Client := newTestS3ClientWithAPI(t, server, apiClient)

	const parts = 10
	var wg sync.WaitGroup
	errs := make([]error, parts)
	wg.Add(parts)
	for i := 0; i < parts; i++ {
		go func(idx int) {
			defer wg.Done()
			errs[idx] = uploadOnePart(context.Background(), s3Client, int32(idx+1))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("part %d did not recover from a rejected credential: %v", i+1, err)
		}
	}

	if got := fetches.Load(); got != 2 {
		t.Errorf("credentials were fetched %d time(s), want 2: %d rejected parts must coalesce into one replacement", got, parts)
	}
}

// TestLateRejectionLeavesTheReplacementCredentialInPlace is N6. The retry
// wrapper invalidated whatever credential was current when the rejection came
// back, not the one the attempt actually ran on. A part rejected on generation
// G, whose rejection is released after G has already been replaced by H,
// therefore dropped the healthy H and cost the transfer another fetch — and
// under a burst, one per late rejection.
func TestLateRejectionLeavesTheReplacementCredentialInPlace(t *testing.T) {
	_, server := newFakeS3Backend(t)
	apiClient, fetches := newCountingCredentialsAPI(t)
	s3Client := newTestS3ClientWithAPI(t, server, apiClient)
	ctx := context.Background()

	// Generation G: what the attempt below runs on.
	if err := s3Client.EnsureFreshCredentials(ctx); err != nil {
		t.Fatalf("failed to install the first credential: %v", err)
	}
	generationG := s3Client.appliedCreds

	replacement := generationG
	rejected := false
	err := s3Client.RetryWithBackoff(ctx, "UploadPart 1", func() error {
		if rejected {
			return nil
		}
		rejected = true

		// While this attempt is in flight, another one replaces G with H.
		s3Client.credManager.InvalidateS3Credentials(generationG)
		if err := s3Client.EnsureFreshCredentials(ctx); err != nil {
			t.Fatalf("failed to install the replacement credential: %v", err)
		}
		replacement = s3Client.appliedCreds

		// Only now does the backend's rejection of generation G arrive.
		return fmt.Errorf("operation error S3: UploadPart, https response error StatusCode: 403, api error AccessDenied: authentication failed")
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
	if s3Client.appliedCreds != replacement {
		t.Error("the retry rebuilt the client around a credential other than the replacement that was already installed")
	}
}
