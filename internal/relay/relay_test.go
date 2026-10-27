package relay

import (
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// connect starts a relay joined to a host end over in-memory pipes and returns
// the path of its socket.
func connect(t *testing.T, agentSocket string) string {
	t.Helper()

	// Short dir: unix socket paths are capped near 100 bytes, and t.TempDir can
	// exceed that.
	dir, err := os.MkdirTemp("", "rly")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")

	serveAt(t, socket, agentSocket)
	waitForSocket(t, socket)
	return socket
}

// serveAt runs Serve on socket joined to Host over in-memory pipes. stop ends
// the session by closing the container's input, as the host does; it is
// idempotent.
func serveAt(t *testing.T, socket, agentSocket string) (served <-chan struct{}, stop func()) {
	t.Helper()
	toHost, hostIn := io.Pipe()       // container stdout -> host
	hostOut, toContainer := io.Pipe() // host stdout -> container

	done := make(chan struct{})
	var host sync.WaitGroup
	host.Add(1)
	go func() { defer close(done); _ = Serve(socket, hostOut, hostIn) }()
	go func() { defer host.Done(); _ = Host(agentSocket, toHost, toContainer) }()

	var once sync.Once
	stop = func() {
		once.Do(func() {
			_ = toContainer.Close()
			_ = hostIn.Close()
			<-done
			host.Wait()
		})
	}
	t.Cleanup(stop)
	return done, stop
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("socket %s never appeared", path)
}

// echoServer stands in for the agent by echoing what it is sent.
func echoServer(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "echo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return path
}

func TestRelayRoundTrip(t *testing.T) {
	socket := connect(t, echoServer(t))

	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	want := "hello through the relay"
	if _, err := conn.Write([]byte(want)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if got := string(buf); got != want {
		t.Errorf("round trip = %q, want %q", got, want)
	}
}

// TestRelayConcurrent checks that concurrent connections do not cross on the
// shared stream.
func TestRelayConcurrent(t *testing.T) {
	socket := connect(t, echoServer(t))

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := net.Dial("unix", socket)
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = conn.Close() }()

			// Distinct content, larger than one read, so crossed channels are
			// detectable.
			want := strings.Repeat(string(rune('a'+i)), 4096)
			if _, err := conn.Write([]byte(want)); err != nil {
				t.Error(err)
				return
			}
			buf := make([]byte, len(want))
			if _, err := io.ReadFull(conn, buf); err != nil {
				t.Error(err)
				return
			}
			if string(buf) != want {
				t.Errorf("connection %d got crossed content", i)
			}
		}()
	}
	wg.Wait()
}

// A client is closed with EOF when the agent is gone, rather than left to hang.
func TestRelayAgentUnreachable(t *testing.T) {
	socket := connect(t, filepath.Join(t.TempDir(), "absent"))

	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte("anyone there?")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("expected a clean EOF, got %v", err)
	}
}

// TestRelayWithRealAgent reaches a real ssh-agent through the relay.
func TestRelayWithRealAgent(t *testing.T) {
	agentSocket := startAgent(t)
	addKey(t, agentSocket, "relay-test")

	socket := connect(t, agentSocket)

	list := exec.Command("ssh-add", "-l")
	list.Env = append(os.Environ(), "SSH_AUTH_SOCK="+socket)
	out, err := list.CombinedOutput()
	if err != nil {
		t.Fatalf("ssh-add -l through the relay: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "relay-test") {
		t.Errorf("relayed agent did not list the key; got:\n%s", out)
	}
}

// The old relay's exit must not remove a replacement's socket: Go unlinks a
// listener's path on close by default.
func TestServeLeavesAReplacementInPlace(t *testing.T) {
	dir, err := os.MkdirTemp("", "rly")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	agent := echoServer(t)

	_, stopOld := serveAt(t, socket, agent)
	waitForSocket(t, socket)
	first, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}

	serveAt(t, socket, agent)
	waitFor(t, func() bool {
		fi, err := os.Stat(socket)
		return err == nil && !os.SameFile(fi, first)
	}, "the replacement never took the path")

	stopOld()

	if err := roundTrip(socket, "still reachable"); err != nil {
		t.Fatalf("the old relay's exit broke the replacement: %v", err)
	}
}

// A relay whose path now leads elsewhere stands down rather than wait forever.
func TestServeStandsDownWhenReplaced(t *testing.T) {
	old := ownershipInterval
	ownershipInterval = 10 * time.Millisecond
	t.Cleanup(func() { ownershipInterval = old })

	dir, err := os.MkdirTemp("", "rly")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	agent := echoServer(t)

	servedOld, _ := serveAt(t, socket, agent)
	waitForSocket(t, socket)
	serveAt(t, socket, agent)

	select {
	case <-servedOld:
	case <-time.After(5 * time.Second):
		t.Fatal("the replaced relay kept running")
	}
	if err := roundTrip(socket, "the replacement answers"); err != nil {
		t.Fatal(err)
	}
}

// A relay whose socket was removed stands down, so the host can respawn it.
func TestServeStandsDownWhenSocketDeleted(t *testing.T) {
	old := ownershipInterval
	ownershipInterval = 10 * time.Millisecond
	t.Cleanup(func() { ownershipInterval = old })

	dir, err := os.MkdirTemp("", "rly")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")

	served, _ := serveAt(t, socket, echoServer(t))
	waitForSocket(t, socket)
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}

	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("a relay with no socket kept running")
	}
}
