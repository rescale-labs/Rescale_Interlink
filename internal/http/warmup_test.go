package http

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
)

// Every transfer worker warms a Basic proxy up at once, whatever proxy_warmup
// says, and a failure they all meet is logged once.
func TestWarmupProxyIfNeededLogsAFailureOnce(t *testing.T) {
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer proxy.Close()
	u, _ := url.Parse(proxy.URL)
	port, _ := strconv.Atoi(u.Port())

	var logged bytes.Buffer
	defer func(w interface{ Write([]byte) (int, error) }) { log.SetOutput(w) }(log.Writer())
	log.SetOutput(&logged)
	warmupFailure = ""

	// Only a Basic proxy is warmed up: no other mode sends it anything.
	WarmupProxyIfNeeded(context.Background(), nil)
	for _, mode := range []string{"", "no-proxy", "system", "ntlm"} {
		WarmupProxyIfNeeded(context.Background(), &config.Config{ProxyMode: mode, ProxyHost: u.Hostname(), ProxyPort: port, APIBaseURL: proxy.URL})
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("%d warmup requests outside Basic mode, want none", n)
	}

	cfg := &config.Config{ProxyMode: "basic", ProxyHost: u.Hostname(), ProxyPort: port, APIBaseURL: proxy.URL}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			WarmupProxyIfNeeded(context.Background(), cfg)
		}()
	}
	wg.Wait()

	if n := requests.Load(); n != 5 {
		t.Errorf("%d warmup requests, want one per worker (5)", n)
	}
	if n := strings.Count(logged.String(), "warmup warning"); n != 1 {
		t.Errorf("logged the failure %d times, want once:\n%s", n, logged.String())
	}
}
