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

// connect joins a container end and a host end with in-memory pipes, standing
// in for whatever exec channel a provider would have established.
//
// Returns the path of the socket the container end is listening on.
func connect(t *testing.T, agentSocket string) string {
	t.Helper()

	// Short directory: a unix socket path is capped near 100 bytes by the
	// kernel, and a t.TempDir() under a long test name can exceed it. This is
	// the same reason the real thing puts its socket under /tmp.
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

// serveAt joins a container end listening on socket to a host end, with
// in-memory pipes standing in for the exec channel. served closes when Serve
// returns; stop ends the session the way the host really does, by closing the
// container's input. Safe to call twice, and called at cleanup regardless.
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

// echoServer stands in for the agent: it returns whatever it is sent, which is
// enough to prove bytes survive the round trip intact.
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

// TestRelayConcurrent is the reason the stream is framed at all: `git fetch
// --all` opens one agent connection per remote, and unframed they would
// interleave into a corrupt protocol.
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

			// Distinct per connection, and long enough to span several reads,
			// so a crossed channel shows up as wrong content rather than by
			// luck.
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

// TestRelayAgentUnreachable covers the agent socket going away: the client must
// be told, not left waiting on a connection nothing will answer.
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
	// EOF rather than a hang is the whole point.
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("expected a clean EOF, got %v", err)
	}
}

// TestRelayWithRealAgent is the test that matters: a real ssh-agent, reached
// through the relay by the real ssh-add, which speaks the actual protocol.
func TestRelayWithRealAgent(t *testing.T) {
	for _, bin := range []string{"ssh-agent", "ssh-add", "ssh-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}

	dir, err := os.MkdirTemp("", "agent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	agentSocket := filepath.Join(dir, "a")
	agent := exec.Command("ssh-agent", "-D", "-a", agentSocket)
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = agent.Process.Kill()
		_, _ = agent.Process.Wait()
	})
	waitForSocket(t, agentSocket)

	key := filepath.Join(dir, "id_ed25519")
	if out, err := exec.Command("ssh-keygen",
		"-t", "ed25519", "-N", "", "-C", "relay-test", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}

	add := exec.Command("ssh-add", key)
	add.Env = append(os.Environ(), "SSH_AUTH_SOCK="+agentSocket)
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("ssh-add: %v\n%s", err, out)
	}

	// Now reach that agent through the relay instead of directly.
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

// TestServeLeavesAReplacementInPlace is the hazard respawning creates: the old
// relay can outlive its stream, and when it finally exits it must not take the
// replacement's socket with it — Go's listener unlinks its path on close by
// default, whoever that path belongs to by then.
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

// TestServeStandsDownWhenReplaced covers the other half: a relay whose path
// now leads elsewhere can never be reached again, and without this it would
// wait on a dead stream for the life of the container.
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

// TestServeStandsDownWhenSocketDeleted: a relay whose socket was removed is
// unreachable, and exiting is what lets the host notice and put a new one up.
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
