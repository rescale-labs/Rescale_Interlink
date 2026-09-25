package http

import (
	"context"
	"io"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/config"
)

// connCountingServer answers every request with an empty 200 and counts the
// connections it has accepted and those still open.
func connCountingServer(t *testing.T) (server *httptest.Server, accepted, open *atomic.Int32) {
	t.Helper()
	accepted, open = new(atomic.Int32), new(atomic.Int32)
	server = httptest.NewUnstartedServer(nethttp.HandlerFunc(func(nethttp.ResponseWriter, *nethttp.Request) {}))
	server.Config.ConnState = func(_ net.Conn, state nethttp.ConnState) {
		switch state {
		case nethttp.StateNew:
			accepted.Add(1)
			open.Add(1)
		case nethttp.StateClosed, nethttp.StateHijacked:
			open.Add(-1)
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	return server, accepted, open
}

// TestTransferClientsShareOneConnectionPool: S3 and Azure clients are built per
// file, and each used to get a transport of its own — a connection pool per
// file whose idle connections outlived it, registered for sleep/wake cleanup
// and never released. Ten files meant ten connections to one host.
func TestTransferClientsShareOneConnectionPool(t *testing.T) {
	server, accepted, _ := connCountingServer(t)
	cfg := &config.Config{ProxyMode: "no-proxy"}
	for range 10 {
		client, err := CreateOptimizedClient(cfg)
		if err != nil {
			t.Fatalf("CreateOptimizedClient: %v", err)
		}
		resp, err := client.Get(server.URL)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if n := accepted.Load(); n != 1 {
		t.Errorf("ten per-file clients opened %d connections to one host, want 1", n)
	}
}

// TestWarmupProxyConnectionClosesItsConnection: each warm-up built a transport
// of its own and left its connection to the proxy open, with no idle timeout to
// close it — a connection and two goroutines per credential refresh.
func TestWarmupProxyConnectionClosesItsConnection(t *testing.T) {
	proxy, _, open := connCountingServer(t)
	host, port, _ := net.SplitHostPort(proxy.Listener.Addr().String())
	portNumber, _ := strconv.Atoi(port)
	cfg := &config.Config{ProxyMode: "basic", ProxyHost: host, ProxyPort: portNumber, APIBaseURL: "http://platform.example.invalid"}
	for range 20 {
		if err := WarmupProxyConnection(context.Background(), cfg); err != nil {
			t.Fatalf("WarmupProxyConnection: %v", err)
		}
	}
	// The client closes its end before returning; the proxy only sees that on
	// its next read, so the count is given a moment to settle.
	for deadline := time.Now().Add(5 * time.Second); open.Load() > 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if n := open.Load(); n != 0 {
		t.Errorf("%d connections to the proxy still open after 20 warm-ups", n)
	}
}

// TestSharedClientStillWarmsUpWhenAsked: the shared client is keyed by the
// proxy settings, so one built without a warm-up answered a later request that
// asked for one, and that warm-up never ran.
func TestSharedClientStillWarmsUpWhenAsked(t *testing.T) {
	proxy, accepted, _ := connCountingServer(t)
	host, port, _ := net.SplitHostPort(proxy.Listener.Addr().String())
	portNumber, _ := strconv.Atoi(port)
	cfg := config.Config{ProxyMode: "basic", ProxyHost: host, ProxyPort: portNumber, ProxyUser: "user", ProxyPassword: "SECRET",
		APIBaseURL: "http://platform.example.invalid"}
	for _, warmup := range []bool{false, true} {
		cfg.ProxyWarmup = warmup
		if _, err := CreateOptimizedClient(&cfg); err != nil {
			t.Fatalf("CreateOptimizedClient (warm-up %v): %v", warmup, err)
		}
	}
	if n := accepted.Load(); n != 1 {
		t.Errorf("the proxy saw %d connection(s), want the one warm-up that was asked for", n)
	}
}
