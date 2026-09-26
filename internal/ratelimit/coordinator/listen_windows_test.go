//go:build windows

package coordinator

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/user"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/ipc"
)

// testEndpoint returns a named pipe of the test's own: the Windows client dials
// a pipe, never a Unix socket.
func testEndpoint(t *testing.T) string {
	return fmt.Sprintf(`\\.\pipe\rescale-coordinator-test-%d-%s`, os.Getpid(), t.Name())
}

// listenTest listens where the client dials on this platform: a pipe of this
// user's, as Listen makes it, since the client uses no other.
func listenTest(endpoint string) (net.Listener, error) {
	return ipc.ListenUserPipe(endpoint, 65536)
}

// Each signed-in user has a coordinator of their own, as on macOS and Linux,
// on a pipe named for the user.
func TestSocketPath_OnePerUser(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	if path, err := SocketPath(); err != nil || path != `\\.\pipe\rescale-ratelimit-coordinator-`+u.Uid {
		t.Errorf("SocketPath = %q, %v; want this user's pipe, named for SID %s", path, err, u.Uid)
	}
}

// The coordinator's pipe serves this user's clients, and a second coordinator
// of the same user cannot join it.
func TestListen_ServesThisUsersClient(t *testing.T) {
	// A base of the test's own, not this user's real coordinator pipe, which
	// other packages' tests may be using.
	orig := pipeBase
	pipeBase = fmt.Sprintf("rescale-coordinator-test-%d", os.Getpid())
	t.Cleanup(func() { pipeBase = orig })
	l, err := Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	srv := NewServer()
	srv.Start(l)
	defer srv.Stop()

	if err := NewClient().Ping(context.Background()); err != nil {
		t.Errorf("this user's client cannot reach the coordinator: %v", err)
	}
	if l2, err := Listen(); err == nil {
		l2.Close()
		t.Error("a second coordinator of the same user listened")
	}
}

// Without a profile folder the coordinator cannot keep its PID file, so none is
// started: each retry would spawn one that fails, and wait for it.
func TestEnsureCoordinator_NoProfileNoSpawn(t *testing.T) {
	if os.Getenv("RESCALE_COORDINATOR_CHILD") != "" {
		t.Skip("this test binary was spawned as a coordinator; it must not spawn another")
	}
	orig := pipeBase
	pipeBase = fmt.Sprintf("rescale-coordinator-test-%d", os.Getpid()) // no coordinator there
	t.Cleanup(func() { pipeBase = orig })
	t.Setenv("USERPROFILE", "")
	if _, err := EnsureCoordinator(); err == nil || !strings.Contains(err.Error(), "home folder") {
		t.Errorf("EnsureCoordinator with no profile folder = %v, want a refusal naming it", err)
	}
}
