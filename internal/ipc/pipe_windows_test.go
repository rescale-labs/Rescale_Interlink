//go:build windows

package ipc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func TestIsPipeInUse_NoPipe(t *testing.T) {
	// Ensure no daemon is running by checking if pipe exists
	// If another test left a daemon running, skip this test
	if IsPipeInUse() {
		t.Skip("Pipe is in use (daemon may be running), cannot test absent pipe scenario")
	}

	// Test should return false for absent pipe
	if IsPipeInUse() {
		t.Error("IsPipeInUse should return false when no pipe exists")
	}
}

func TestWindowsErrorCodes(t *testing.T) {
	// Verify our constants match Windows error codes
	if ERROR_FILE_NOT_FOUND != syscall.Errno(2) {
		t.Errorf("ERROR_FILE_NOT_FOUND mismatch: got %d, want 2", ERROR_FILE_NOT_FOUND)
	}
	if ERROR_PIPE_BUSY != syscall.Errno(231) {
		t.Errorf("ERROR_PIPE_BUSY mismatch: got %d, want 231", ERROR_PIPE_BUSY)
	}
	if ERROR_ACCESS_DENIED != syscall.Errno(5) {
		t.Errorf("ERROR_ACCESS_DENIED mismatch: got %d, want 5", ERROR_ACCESS_DENIED)
	}
}

func TestIsPipeInUse_ConsistentResults(t *testing.T) {
	// Call IsPipeInUse multiple times and verify consistent results
	// This helps catch any state-dependent bugs
	result1 := IsPipeInUse()
	result2 := IsPipeInUse()
	result3 := IsPipeInUse()

	if result1 != result2 || result2 != result3 {
		t.Errorf("IsPipeInUse returned inconsistent results: %v, %v, %v", result1, result2, result3)
	}
}

// withSID stands sid in for this process's SID until the test ends.
func withSID(t *testing.T, sid func() (string, error)) {
	orig := currentUserSID
	currentUserSID = sid
	t.Cleanup(func() { currentUserSID = orig })
}

// withPipeOwner stands owner in for the owner of every pipe dialed until the
// test ends: no test can create a pipe as another user.
func withPipeOwner(t *testing.T, owner string) {
	orig := pipeOwnerSID
	pipeOwnerSID = func(net.Conn) (string, error) { return owner, nil }
	t.Cleanup(func() { pipeOwnerSID = orig })
}

// testPipe is a pipe name of the test's own.
func testPipe(t *testing.T) string {
	return fmt.Sprintf(`\\.\pipe\rescale-ipc-test-%d-%s`, os.Getpid(), t.Name())
}

// The daemon listens on its user's pipe with the real DACL, and the same
// user's client reaches it there; a second daemon of that user is refused.
func TestServer_ListensOnThisUsersPipe(t *testing.T) {
	// A base of the test's own: other packages' tests, run at the same time,
	// take this user's real daemon pipe for a sign that a daemon runs.
	orig := pipeBase
	pipeBase = fmt.Sprintf("rescale-ipc-test-%d", os.Getpid())
	t.Cleanup(func() { pipeBase = orig })
	sid, err := currentUserSID()
	if err != nil {
		t.Fatalf("currentUserSID: %v", err)
	}
	name, _ := pipeNameFor(pipeBase, sid)
	srv := newSubprocessModeServerForTest(&capturingHandler{})
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	if got := srv.GetSocketPath(); got != name {
		t.Errorf("server pipe = %q, want %q", got, name)
	}
	if !IsPipeInUse() {
		t.Error("IsPipeInUse does not see this user's daemon")
	}
	if _, err := NewClient().GetStatus(context.Background()); err != nil {
		t.Errorf("the same user's client cannot reach the daemon: %v", err)
	}
	if err := newSubprocessModeServerForTest(&capturingHandler{}).Start(); err == nil {
		t.Error("a second daemon of the same user started")
	}

	conn, err := winio.DialPipe(name, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", name, err)
	}
	defer conn.Close()
	handle, ok := findHandleRecursive(reflect.ValueOf(conn), 0)
	if !ok {
		t.Fatal("no handle on the pipe connection")
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetSecurityInfo: %v", err)
	}
	if owner, _, err := sd.Owner(); err != nil || owner.String() != sid {
		t.Errorf("pipe owner = %v, %v; want %s", owner, err, sid)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("DACL: %v", err)
	}
	var trustees []string
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatalf("GetAce %d: %v", i, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Errorf("ACE %d has type %d, want an allow entry", i, ace.Header.AceType)
		}
		trustees = append(trustees, (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String())
	}
	if want := []string{sid, "S-1-5-18"}; !reflect.DeepEqual(trustees, want) {
		t.Errorf("pipe DACL admits %v, want %v (its user and LocalSystem)", trustees, want)
	}
}

// Every caller finds the daemon by the pipe named for its user's SID.
func TestEveryPath_UsesThePipeNamedForTheUser(t *testing.T) {
	withSID(t, func() (string, error) { return testSID, nil })
	withPipeOwner(t, testSID)
	name, _ := pipeNameFor(pipeBase, testSID)
	if IsPipeInUse() {
		t.Fatalf("%s is in use before the test listens on it", name)
	}
	l, err := winio.ListenPipe(name, nil)
	if err != nil {
		t.Fatalf("listen on %s: %v", name, err)
	}
	defer l.Close()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if _, err := bufio.NewReader(conn).ReadBytes('\n'); err != nil {
					return
				}
				data, _ := NewStatusResponse(&StatusData{Version: "test"}).Encode()
				conn.Write(append(data, '\n'))
			}()
		}
	}()

	if _, err := NewClient().GetStatus(context.Background()); err != nil {
		t.Errorf("the client did not reach %s: %v", name, err)
	}
	if !IsPipeInUse() {
		t.Errorf("IsPipeInUse does not look at %s", name)
	}
	if err := newSubprocessModeServerForTest(&capturingHandler{}).Start(); err == nil {
		t.Errorf("Start went ahead while %s was in use", name)
	}
}

// Without a SID there is no pipe to name, and nothing falls back to the name
// every user once shared, where an earlier version's daemon may still listen.
func TestNoSID_NoPipe(t *testing.T) {
	withSID(t, func() (string, error) { return "", errors.New("no process token") })
	if l, err := winio.ListenPipe(`\\.\pipe\rescale-interlink`, nil); err == nil {
		defer l.Close()
	}

	srv := newSubprocessModeServerForTest(&capturingHandler{})
	if err := srv.Start(); !errors.Is(err, errNoSID) {
		if err == nil {
			srv.Stop()
		}
		t.Errorf("Start without a SID = %v, want a refusal", err)
	}
	if IsPipeInUse() {
		t.Error("IsPipeInUse looked at a pipe it cannot have named")
	}
	client := NewClient()
	client.SetTimeout(time.Second)
	if _, err := client.GetStatus(context.Background()); !errors.Is(err, errNoSID) {
		t.Errorf("client without a SID = %v, want a refusal", err)
	}
}

// Listening creates the pipe or fails: it never joins a pipe of that name
// someone else created first, whose clients it would then share.
func TestListenUserPipe_DoesNotJoinAnExistingPipe(t *testing.T) {
	name := testPipe(t)
	squatter, err := winio.ListenPipe(name, nil)
	if err != nil {
		t.Fatalf("listen on %s: %v", name, err)
	}
	defer squatter.Close()
	if l, err := ListenUserPipe(name, 4096); err == nil {
		l.Close()
		t.Fatalf("ListenUserPipe joined %s, which already existed", name)
	}
}

// A client uses only a pipe its own user created, and sends nothing to one
// another user created under the same name.
func TestDialUserPipe_RefusesAnotherUsersPipe(t *testing.T) {
	name := testPipe(t)
	l, err := ListenUserPipe(name, 4096)
	if err != nil {
		t.Fatalf("ListenUserPipe: %v", err)
	}
	defer l.Close()
	received := make(chan int, 2)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			n, _ := io.Copy(io.Discard, conn)
			conn.Close()
			received <- int(n)
		}
	}()

	// The real owner lookup finds this user on this user's pipe.
	conn, err := DialUserPipe(context.Background(), name)
	if err != nil {
		t.Fatalf("DialUserPipe on this user's pipe: %v", err)
	}
	conn.Close()
	<-received

	withPipeOwner(t, otherTestSID)
	if conn, err := DialUserPipe(context.Background(), name); !errors.Is(err, errNotOurPipe) {
		if err == nil {
			conn.Close()
		}
		t.Fatalf("DialUserPipe on another user's pipe = %v, want a refusal", err)
	}
	if n := <-received; n != 0 {
		t.Errorf("the client sent %d bytes to another user's pipe", n)
	}
}
