package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/models"
)

// credentialsAPI is a counting fake of POST /api/v3/credentials/. Like the
// platform, it answers every request with a credential of its own, names dir as
// the requester's own folder when dir is set, and for Azure adds a blob SAS for
// each requested path outside that folder.
type credentialsAPI struct {
	storageType string
	dir         string
	requests    atomic.Int32
	onRequest   func() // runs before each answer, when set
}

func (f *credentialsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v3/credentials/" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	n := f.requests.Add(1)
	if f.onRequest != nil {
		f.onRequest()
	}
	var req models.CredentialsRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	resp := map[string]any{"storageType": f.storageType}
	if f.dir != "" {
		resp["storageDir"] = f.dir
	}
	if f.storageType == "AzureStorage" {
		resp["sasToken"] = fmt.Sprintf("sig=FAKESIG-%d", n)
		var paths []map[string]any
		for _, p := range req.Paths {
			if p.PathParts.Container != f.dir {
				paths = append(paths, map[string]any{"pathParts": p.PathParts, "sasToken": fmt.Sprintf("sig=FAKESIG-%d-blob", n)})
			}
		}
		resp["paths"] = paths
	} else {
		resp["accessKey"] = fmt.Sprintf("key-%d", n)
		resp["secretKey"] = "SECRET"
		resp["sessionToken"] = "token"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// newManager is a fresh Manager whose API is fake.
func newManager(t *testing.T, fake *credentialsAPI) *Manager {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	// NewClientForTest: httptest URLs are not on the platform URL allowlist.
	client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test-key", ProxyMode: "no-proxy"})

	globalManagerMu.Lock()
	globalManager = nil
	globalManagerMu.Unlock()
	return GetManager(client)
}

// newTestManagerWithServer is newManager on an S3 storage, for the tests of the
// default-storage credentials, with the count of requests made.
func newTestManagerWithServer(t *testing.T) (*Manager, *atomic.Int32) {
	t.Helper()
	fake := &credentialsAPI{storageType: "S3Storage"}
	return newManager(t, fake), &fake.requests
}

func s3File(storageID, path string) *models.CloudFile {
	return &models.CloudFile{
		Storage:   &models.CloudFileStorage{ID: storageID, StorageType: "S3Storage"},
		PathParts: &models.CloudFilePathParts{Container: "example-bucket", Path: path},
	}
}

func azureFile(container, path string) *models.CloudFile {
	return &models.CloudFile{
		Storage:   &models.CloudFileStorage{ID: "azure-storage", StorageType: "AzureStorage"},
		PathParts: &models.CloudFilePathParts{Container: container, Path: path},
	}
}

// getS3 is GetS3CredentialsForStorage that fails the test on an error.
func getS3(t *testing.T, mgr *Manager, file *models.CloudFile) *models.S3Credentials {
	t.Helper()
	creds, err := mgr.GetS3CredentialsForStorage(context.Background(), file)
	if err != nil {
		t.Fatalf("credentials for %s: %v", file.PathParts.Path, err)
	}
	return creds
}

// ageStorageCreds makes every cached storage credential older by d.
func ageStorageCreds(mgr *Manager, d time.Duration) {
	mgr.storageMu.Lock()
	defer mgr.storageMu.Unlock()
	for _, e := range mgr.storageCreds {
		e.fetched = e.fetched.Add(-d)
	}
}

// storageCredsCached counts the storage credentials the manager holds.
func storageCredsCached(mgr *Manager) int {
	mgr.storageMu.RLock()
	defer mgr.storageMu.RUnlock()
	return len(mgr.storageCreds)
}

// Every credential for a storage covers the requester's whole folder there, so
// files in that folder share one, fetched once per refresh interval.
func TestOwnFilesShareOneCredentialPerStorage(t *testing.T) {
	fake := &credentialsAPI{storageType: "S3Storage", dir: "user/user_me"}
	mgr := newManager(t, fake)

	for i := range 5 {
		getS3(t, mgr, s3File("storage-1", fmt.Sprintf("user/user_me/output/job_%d/run1/out.dat", i)))
	}
	if n := fake.requests.Load(); n != 1 {
		t.Fatalf("%d requests for 5 files in the requester's own folder, want 1", n)
	}

	getS3(t, mgr, s3File("storage-2", "user/user_me/input.dat"))
	if n := fake.requests.Load(); n != 2 {
		t.Fatalf("%d requests after a file on a second storage, want 2: one per storage", n)
	}

	ageStorageCreds(mgr, constants.GlobalCredentialRefreshInterval+time.Second)
	for i := range 3 {
		getS3(t, mgr, s3File("storage-1", fmt.Sprintf("user/user_me/output/job_9/run1/out%d.dat", i)))
	}
	if n := fake.requests.Load(); n != 3 {
		t.Errorf("%d requests after the refresh interval, want 3: one more for the storage", n)
	}
}

// Another user's file is granted on its own, so each one is requested once.
func TestOtherUsersFilesAreRequestedOneByOne(t *testing.T) {
	fake := &credentialsAPI{storageType: "S3Storage", dir: "user/user_me"}
	mgr := newManager(t, fake)

	paths := []string{"user/user_xyz/output/job_1/run1/a.dat", "user/user_xyz/output/job_1/run1/b.dat", "user/user_xyz/in.dat"}
	for range 2 {
		for _, path := range paths {
			getS3(t, mgr, s3File("storage-1", path))
		}
	}
	if n := fake.requests.Load(); n != int32(len(paths)) {
		t.Errorf("%d requests for %d other-user files asked for twice, want one per file", n, len(paths))
	}
}

// The requester's folder is decided by where a file is, as the platform decides
// it, and not by who owns it: a file the requester owns in another user's folder
// is granted on its own, and a colleague's file in the requester's folder is not.
func TestOwnFolderIsDecidedByLocation(t *testing.T) {
	fake := &credentialsAPI{storageType: "S3Storage", dir: "user/user_me"}
	mgr := newManager(t, fake)
	getS3(t, mgr, s3File("storage-1", "user/user_me/first.dat"))

	for _, tc := range []struct {
		name, path, owner string
		own               bool
	}{
		{"a folder whose name starts with the requester's", "user/user_me2/b.dat", "", false},
		{"the requester's file in another user's folder", "user/user_def/cloned/c.dat", "requester", false},
		{"another user's file in the requester's folder", "user/user_me/shared/d.dat", "another-user", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := s3File("storage-1", tc.path)
			file.Owner = tc.owner
			before := fake.requests.Load()
			getS3(t, mgr, file)
			if fetched := fake.requests.Load() != before; fetched == tc.own {
				t.Errorf("fetched a credential of its own: %v, want %v", fetched, !tc.own)
			}
		})
	}
}

// Without a storageDir in the response nothing can be shared: every path keeps
// a credential of its own, as before.
func TestMissingStorageDirKeepsPerPathCredentials(t *testing.T) {
	fake := &credentialsAPI{storageType: "S3Storage"}
	mgr := newManager(t, fake)

	for _, path := range []string{"user/user_me/a.dat", "user/user_me/b.dat", "user/user_me/a.dat"} {
		getS3(t, mgr, s3File("storage-1", path))
	}
	if n := fake.requests.Load(); n != 2 {
		t.Errorf("%d requests for two paths, one asked for twice, want 2", n)
	}
}

// A fetch no longer blocks other lookups: requests for different paths are in
// flight together, bounded only by the rate limiter.
func TestLookupsOfDifferentPathsRunTogether(t *testing.T) {
	const workers = 4
	arrived := make(chan struct{}, workers)
	release := make(chan struct{})
	var once sync.Once
	releaseAll := func() { once.Do(func() { close(release) }) }

	// Each request is held until all of them are in, which serialized lookups
	// never reach.
	fake := &credentialsAPI{storageType: "S3Storage", dir: "user/user_me", onRequest: func() {
		arrived <- struct{}{}
		<-release
	}}
	mgr := newManager(t, fake)
	t.Cleanup(releaseAll) // before the server closes, which waits for its handlers

	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			_, _ = mgr.GetS3CredentialsForStorage(context.Background(), s3File("storage-1", fmt.Sprintf("user/user_xyz/%d.dat", i)))
		})
	}
	for i := range workers {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d requests were in flight at once", i, workers)
		}
	}
	releaseAll()
	wg.Wait()
}

// Lookups that one response answers share its request.
func TestLookupsOfOneKeyShareOneRequest(t *testing.T) {
	fake := &credentialsAPI{storageType: "S3Storage", dir: "user/user_me"}
	mgr := newManager(t, fake)
	getS3(t, mgr, s3File("storage-1", "user/user_me/first.dat"))
	ageStorageCreds(mgr, constants.GlobalCredentialRefreshInterval+time.Second)

	for _, tc := range []struct {
		name string
		path func(int) string
	}{
		{"files in the requester's folder", func(i int) string { return fmt.Sprintf("user/user_me/%d.dat", i) }},
		{"one file in another user's folder", func(int) string { return "user/user_xyz/one.dat" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := fake.requests.Load()
			var wg sync.WaitGroup
			for i := range 8 {
				wg.Go(func() {
					if _, err := mgr.GetS3CredentialsForStorage(context.Background(), s3File("storage-1", tc.path(i))); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			if n := fake.requests.Load() - before; n != 1 {
				t.Errorf("8 concurrent lookups made %d requests, want 1", n)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A lookup that gives up does not fail the lookups waiting for its request:
// they ask again. The transport is in memory, so that synctest can tell when
// each lookup is blocked.
func TestAbandonedFetchIsRetriedByItsWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var requests atomic.Int32
		defer func(rt http.RoundTripper) { http.DefaultTransport = rt }(http.DefaultTransport)
		http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if requests.Add(1) == 1 { // held until its requester gives up
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			body := `{"storageType":"S3Storage","storageDir":"user/user_me","accessKey":"key"}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})
		globalManagerMu.Lock()
		globalManager = nil
		globalManagerMu.Unlock()
		mgr := GetManager(api.NewClientForTest(&config.Config{APIBaseURL: "http://platform.test", APIKey: "test-key", ProxyMode: "no-proxy"}))
		file := s3File("storage-1", "user/user_xyz/a.dat")

		ctx, giveUp := context.WithCancel(context.Background())
		first := make(chan error, 1)
		go func() { _, err := mgr.GetS3CredentialsForStorage(ctx, file); first <- err }()
		synctest.Wait()
		waiter := make(chan error, 1)
		go func() { _, err := mgr.GetS3CredentialsForStorage(context.Background(), file); waiter <- err }()
		synctest.Wait()
		giveUp()

		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Errorf("the lookup that gave up returned %v, want its cancellation", err)
		}
		if err := <-waiter; err != nil {
			t.Errorf("the waiting lookup failed with the one that gave up: %v", err)
		}
		if n := requests.Load(); n != 2 {
			t.Errorf("%d requests, want the abandoned one and the waiter's own", n)
		}
	})
}

// A laptop's monotonic clock stops while it sleeps, and a credential can expire
// meanwhile, so the storage entries are aged on the wall clock, as the default
// credentials are in EnsureFresh: a stored time carries no monotonic reading.
func TestStorageCredentialsAgeOnTheWallClock(t *testing.T) {
	mgr := newManager(t, &credentialsAPI{storageType: "S3Storage", dir: "user/user_me"})
	getS3(t, mgr, s3File("storage-1", "user/user_me/a.dat"))
	getS3(t, mgr, s3File("storage-1", "user/user_xyz/b.dat"))

	mgr.storageMu.RLock()
	defer mgr.storageMu.RUnlock()
	for key, e := range mgr.storageCreds {
		if e.fetched != e.fetched.Round(0) {
			t.Errorf("%s was stored at %v, which carries a monotonic reading", key, e.fetched)
		}
	}
}

// Credentials past their refresh interval go when a new one is stored, so a
// long-lived process does not keep one for every file it ever downloaded.
func TestStaleStorageCredentialsAreDropped(t *testing.T) {
	fake := &credentialsAPI{storageType: "S3Storage", dir: "user/user_me"}
	mgr := newManager(t, fake)
	for i := range 3 {
		getS3(t, mgr, s3File("storage-1", fmt.Sprintf("user/user_xyz/%d.dat", i)))
	}
	ageStorageCreds(mgr, constants.GlobalCredentialRefreshInterval+time.Second)

	getS3(t, mgr, s3File("storage-1", "user/user_xyz/new.dat"))
	if n := storageCredsCached(mgr); n != 1 {
		t.Errorf("%d credentials cached, want only the new one", n)
	}
}

// A credential the storage rejected is dropped once, whichever files share it:
// the next lookup fetches a replacement, and a late report of the same rejection
// leaves the replacement alone.
func TestRejectedStorageCredentialIsDroppedOnce(t *testing.T) {
	fake := &credentialsAPI{storageType: "S3Storage", dir: "user/user_me"}
	mgr := newManager(t, fake)
	own := getS3(t, mgr, s3File("storage-1", "user/user_me/a.dat"))
	other := getS3(t, mgr, s3File("storage-1", "user/user_xyz/b.dat"))

	if !mgr.InvalidateS3Credentials(own) {
		t.Fatal("the rejected credential of the requester's folder was not dropped")
	}
	if replacement := getS3(t, mgr, s3File("storage-1", "user/user_me/c.dat")); replacement == own {
		t.Error("the rejected credential was served again")
	}
	if mgr.InvalidateS3Credentials(own) {
		t.Error("a late report of the same rejection dropped the replacement")
	}
	if getS3(t, mgr, s3File("storage-1", "user/user_xyz/b.dat")) != other {
		t.Error("another file's credential went with the rejected one")
	}
	if !mgr.InvalidateS3Credentials(other) {
		t.Fatal("the rejected credential of another user's file was not dropped")
	}
	getS3(t, mgr, s3File("storage-1", "user/user_xyz/b.dat"))
	if n := fake.requests.Load(); n != 4 {
		t.Errorf("%d requests, want 4: two, then one replacement for each rejection", n)
	}
}

// Azure has the same split: the requester's own container is its storageDir,
// and a blob anywhere else is granted on its own, matched by container and path.
func TestAzureOwnContainerSharesOneCredential(t *testing.T) {
	fake := &credentialsAPI{storageType: "AzureStorage", dir: "owncontainer"}
	mgr := newManager(t, fake)
	get := func(file *models.CloudFile) *models.AzureCredentials {
		t.Helper()
		creds, err := mgr.GetAzureCredentialsForStorage(context.Background(), file)
		if err != nil {
			t.Fatalf("credentials for %s/%s: %v", file.PathParts.Container, file.PathParts.Path, err)
		}
		return creds
	}

	for _, blob := range []string{"a.dat", "output/job_1/b.dat", "c.dat"} {
		get(azureFile("owncontainer", blob))
	}
	if n := fake.requests.Load(); n != 1 {
		t.Fatalf("%d requests for 3 blobs in the requester's container, want 1", n)
	}

	first := get(azureFile("othercontainer", "a.dat"))
	second := get(azureFile("thirdcontainer", "a.dat"))
	if n := fake.requests.Load(); n != 3 {
		t.Fatalf("%d requests, want one more for each blob in another container", n)
	}
	if first == second || len(first.Paths) != 1 || first.Paths[0].PathParts.Container != "othercontainer" {
		t.Error("blobs of the same name in two containers shared a credential")
	}

	if !mgr.InvalidateAzureCredentials(first) {
		t.Fatal("the rejected blob SAS was not dropped")
	}
	get(azureFile("othercontainer", "a.dat"))
	get(azureFile("thirdcontainer", "a.dat"))
	if n := fake.requests.Load(); n != 4 {
		t.Errorf("%d requests, want 4: the rejected blob's credential alone replaced", n)
	}
}

func TestEnsureFresh_FreshS3Credentials(t *testing.T) {
	mgr, callCount := newTestManagerWithServer(t)

	// Seed fresh S3 credentials
	mgr.mu.Lock()
	mgr.s3Credentials = &models.S3Credentials{AccessKeyID: "test"}
	mgr.lastCredsRefresh = time.Now()
	mgr.mu.Unlock()

	err := mgr.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if callCount.Load() != 0 {
		t.Errorf("Expected 0 API calls for fresh S3 creds, got %d", callCount.Load())
	}
}

func TestEnsureFresh_FreshAzureOnlyCredentials(t *testing.T) {
	mgr, callCount := newTestManagerWithServer(t)

	// Seed fresh Azure-only credentials (s3 is nil, azure populated)
	mgr.mu.Lock()
	mgr.s3Credentials = nil
	mgr.azureCredentials = &models.AzureCredentials{SASToken: "test-sas"}
	mgr.lastCredsRefresh = time.Now()
	mgr.mu.Unlock()

	err := mgr.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if callCount.Load() != 0 {
		t.Errorf("Expected 0 API calls for fresh Azure-only creds, got %d", callCount.Load())
	}
}

func TestEnsureFresh_StaleCredentials(t *testing.T) {
	mgr, callCount := newTestManagerWithServer(t)

	// Seed stale credentials (older than CredentialFreshnessThreshold)
	mgr.mu.Lock()
	mgr.s3Credentials = &models.S3Credentials{AccessKeyID: "old"}
	mgr.lastCredsRefresh = time.Now().Add(-(constants.CredentialFreshnessThreshold + time.Minute))
	mgr.mu.Unlock()

	err := mgr.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if callCount.Load() != 1 {
		t.Errorf("Expected 1 API call for stale creds, got %d", callCount.Load())
	}

	// Verify credentials were updated
	mgr.mu.RLock()
	if mgr.s3Credentials == nil {
		t.Error("s3Credentials should be populated after refresh")
	}
	if mgr.s3Credentials.AccessKeyID != "key-1" {
		t.Errorf("s3Credentials.AccessKeyID = %q, want %q", mgr.s3Credentials.AccessKeyID, "key-1")
	}
	mgr.mu.RUnlock()
}

func TestEnsureFresh_NoCredentials(t *testing.T) {
	mgr, callCount := newTestManagerWithServer(t)

	// No credentials at all (zero-value lastCredsRefresh, nil creds)
	err := mgr.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if callCount.Load() != 1 {
		t.Errorf("Expected 1 API call for no creds, got %d", callCount.Load())
	}
}

func TestEnsureFresh_ConcurrentCallsSingleAPICall(t *testing.T) {
	mgr, callCount := newTestManagerWithServer(t)

	// Seed stale credentials so EnsureFresh needs to refresh
	mgr.mu.Lock()
	mgr.s3Credentials = &models.S3Credentials{AccessKeyID: "stale"}
	mgr.lastCredsRefresh = time.Now().Add(-(constants.CredentialFreshnessThreshold + time.Minute))
	mgr.mu.Unlock()

	// Launch 10 concurrent EnsureFresh calls
	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make([]error, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			errs[idx] = mgr.EnsureFresh(context.Background())
		}(i)
	}
	wg.Wait()

	// All should succeed
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: EnsureFresh error: %v", i, err)
		}
	}

	// Double-checked locking should result in exactly 1 API call
	calls := callCount.Load()
	if calls != 1 {
		t.Errorf("Expected exactly 1 API call from %d concurrent EnsureFresh, got %d", goroutines, calls)
	}
}

func TestEnsureFresh_WallClockDetectsStaleAfterSimulatedSleep(t *testing.T) {
	mgr, callCount := newTestManagerWithServer(t)

	// Simulate post-sleep scenario: lastCredsRefresh was set 9 minutes ago (wall-clock).
	// This is past the 8-minute CredentialFreshnessThreshold.
	mgr.mu.Lock()
	mgr.s3Credentials = &models.S3Credentials{AccessKeyID: "pre-sleep"}
	mgr.lastCredsRefresh = time.Now().Add(-9 * time.Minute)
	mgr.mu.Unlock()

	err := mgr.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if callCount.Load() != 1 {
		t.Errorf("Expected 1 API call for 9-min-old creds (threshold=8m), got %d", callCount.Load())
	}
}

func TestEnsureFresh_RepeatedCallsNoCaching(t *testing.T) {
	mgr, callCount := newTestManagerWithServer(t)

	// First call: no creds, should refresh
	err := mgr.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("First EnsureFresh: %v", err)
	}
	if callCount.Load() != 1 {
		t.Errorf("After first call: expected 1 API call, got %d", callCount.Load())
	}

	// Second call: creds now fresh, should NOT refresh
	err = mgr.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("Second EnsureFresh: %v", err)
	}
	if callCount.Load() != 1 {
		t.Errorf("After second call: expected still 1 API call (cached), got %d", callCount.Load())
	}
}

// TestInvalidateS3CredentialsDropsOnlyTheRejectedGeneration pins what makes a
// burst of failing parts cost one replacement: the invalidation names the
// credential that was rejected, so whoever reports the same rejection after a
// replacement has arrived cannot throw the replacement away.
func TestInvalidateS3CredentialsDropsOnlyTheRejectedGeneration(t *testing.T) {
	mgr, callCount := newTestManagerWithServer(t)

	ctx := context.Background()
	rejected, err := mgr.GetS3Credentials(ctx)
	if err != nil {
		t.Fatalf("GetS3Credentials: %v", err)
	}
	if _, err := mgr.GetS3Credentials(ctx); err != nil {
		t.Fatalf("GetS3Credentials: %v", err)
	}
	if callCount.Load() != 1 {
		t.Fatalf("setup fetched %d times, want 1 (the second call is cached)", callCount.Load())
	}

	if !mgr.InvalidateS3Credentials(rejected) {
		t.Fatal("the credential that was served was not recognised as the one to drop")
	}

	replacement, err := mgr.GetS3Credentials(ctx)
	if err != nil {
		t.Fatalf("GetS3Credentials after invalidation: %v", err)
	}
	if callCount.Load() != 2 {
		t.Errorf("fetched %d times, want a replacement fetch after the rejection", callCount.Load())
	}
	if replacement == rejected {
		t.Error("the rejected credential was served again")
	}

	// A second part reporting the same rejection must not discard the
	// replacement that has already been fetched.
	if mgr.InvalidateS3Credentials(rejected) {
		t.Error("a stale rejection dropped the replacement credential")
	}
	if _, err := mgr.GetS3Credentials(ctx); err != nil {
		t.Fatalf("GetS3Credentials: %v", err)
	}
	if callCount.Load() != 2 {
		t.Errorf("fetched %d times, want the replacement to still be cached", callCount.Load())
	}
}
