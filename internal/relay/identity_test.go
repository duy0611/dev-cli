package relay

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// startAgent runs a real ssh-agent and returns its socket. The agent protocol
// is worth testing against the real implementation rather than a stub: the
// wire format is the whole content of identity.go.
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
	waitForSocket(t, socket)
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

// TestSigningKeyMatchesSSHAdd is the test that matters: the value handed to git
// must be the same key material ssh-add reports, in the form git parses.
func TestSigningKeyMatchesSSHAdd(t *testing.T) {
	socket := startAgent(t)
	pub := addKey(t, socket, "signing-test")

	got, err := SigningKey(socket)
	if err != nil {
		t.Fatalf("SigningKey: %v", err)
	}

	// Without key:: git reads the value as a filename and fails looking for a
	// key file that does not exist.
	if !strings.HasPrefix(got, "key::") {
		t.Errorf("signing key lacks the key:: prefix: %q", got)
	}

	// The algorithm and base64 blob must match the generated key exactly. The
	// comment is not part of what is compared: ssh-add reports the one from the
	// file, and the agent does not have to.
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

// TestSigningKeyPicksTheFirst pins the documented behaviour for an agent
// holding several keys. Not obviously the right key — it is the one the
// operator loaded first — but it must at least be a key the agent actually has.
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
		// Expected.
	case strings.Fields(second)[1]:
		t.Error("SigningKey returned the second key, not the first")
	default:
		t.Errorf("SigningKey returned a key the agent does not hold: %q", got)
	}
}

// TestSigningKeyEmptyAgent covers an agent holding nothing: reported with the
// command that fixes it, rather than as an empty value git would later reject.
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

// TestSigningKeyNoAgent covers the socket not being there at all.
func TestSigningKeyNoAgent(t *testing.T) {
	_, err := SigningKey(filepath.Join(t.TempDir(), "absent.sock"))
	if err == nil {
		t.Fatal("expected an error when the agent socket is absent")
	}
}
