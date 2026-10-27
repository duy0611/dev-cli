package relay

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// waitForAgent waits until the agent at socket answers. The path existing is
// not enough: bind(2) creates it a moment before listen(2) makes it accept, and
// a dial landing in that gap is refused — which is how this flaked on macOS.
func waitForAgent(t *testing.T, socket string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("unix", socket, time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ssh agent at %s never accepted a connection: %v", socket, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The socket file appears at bind(2); only listen(2) makes it an agent.
func TestWaitForAgentWaitsForListen(t *testing.T) {
	// Short path: unix socket paths are capped near 100 bytes.
	dir, err := os.MkdirTemp("", "bindwait")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "a")

	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: socket}); err != nil {
		t.Fatal(err)
	}

	// Bound but not listening: the state ssh-agent passes through on the way up.
	// The signal is sent before listen(2) so a successful dial, which cannot
	// happen before it, is proof it was seen rather than a race with it.
	listening := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(listening)
		_ = syscall.Listen(fd, 1)
	}()

	waitForAgent(t, socket)

	select {
	case <-listening:
	default:
		t.Fatal("waitForAgent returned before the agent listened")
	}
}

// startAgent runs a real ssh-agent and returns its socket.
func startAgent(t *testing.T) string {
	t.Helper()
	for _, bin := range []string{"ssh-agent", "ssh-add", "ssh-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}

	dir, err := os.MkdirTemp("", "idagent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	socket := filepath.Join(dir, "a")
	agent := exec.Command("ssh-agent", "-D", "-a", socket)
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = agent.Process.Kill()
		_, _ = agent.Process.Wait()
	})
	waitForAgent(t, socket)
	return socket
}

// addKey generates a key and loads it into the agent, returning its public half
// as ssh-add reports it.
func addKey(t *testing.T, socket, comment string) string {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519")
	if out, err := exec.Command("ssh-keygen",
		"-t", "ed25519", "-N", "", "-C", comment, "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}

	add := exec.Command("ssh-add", key)
	add.Env = append(os.Environ(), "SSH_AUTH_SOCK="+socket)
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("ssh-add: %v\n%s", err, out)
	}

	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(pub))
}

// TestSigningKeyMatchesSSHAdd checks the key handed to git is the agent's key.
func TestSigningKeyMatchesSSHAdd(t *testing.T) {
	socket := startAgent(t)
	pub := addKey(t, socket, "signing-test")

	got, err := SigningKey(socket)
	if err != nil {
		t.Fatalf("SigningKey: %v", err)
	}

	// Without key:: git reads the value as a filename.
	if !strings.HasPrefix(got, "key::") {
		t.Errorf("signing key lacks the key:: prefix: %q", got)
	}

	// The comment is not compared; the agent need not hold it.
	fields := strings.Fields(strings.TrimPrefix(got, "key::"))
	wantFields := strings.Fields(pub)
	if len(fields) < 2 || len(wantFields) < 2 {
		t.Fatalf("unexpected key shapes: got %q, ssh-keygen %q", got, pub)
	}
	if fields[0] != wantFields[0] || fields[1] != wantFields[1] {
		t.Errorf("signing key does not match the agent's:\n got %s %s\nwant %s %s",
			fields[0], fields[1], wantFields[0], wantFields[1])
	}
}

// With several keys loaded, the first one is returned.
func TestSigningKeyPicksTheFirst(t *testing.T) {
	socket := startAgent(t)
	first := addKey(t, socket, "first-key")
	second := addKey(t, socket, "second-key")

	got, err := SigningKey(socket)
	if err != nil {
		t.Fatalf("SigningKey: %v", err)
	}

	blob := strings.Fields(strings.TrimPrefix(got, "key::"))[1]
	switch blob {
	case strings.Fields(first)[1]:
	case strings.Fields(second)[1]:
		t.Error("SigningKey returned the second key, not the first")
	default:
		t.Errorf("SigningKey returned a key the agent does not hold: %q", got)
	}
}

// An agent with no keys is reported with the command that fixes it.
func TestSigningKeyEmptyAgent(t *testing.T) {
	socket := startAgent(t)

	_, err := SigningKey(socket)
	if err == nil {
		t.Fatal("expected an error from an agent with no keys")
	}
	if !strings.Contains(err.Error(), "ssh-add") {
		t.Errorf("error should name the command that fixes it, got: %v", err)
	}
}

func TestSigningKeyNoAgent(t *testing.T) {
	_, err := SigningKey(filepath.Join(t.TempDir(), "absent.sock"))
	if err == nil {
		t.Fatal("expected an error when the agent socket is absent")
	}
}
