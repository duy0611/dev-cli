package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/provider/k8s"
)

func touch(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	return f.Close()
}

// startTestAgent runs a real ssh-agent holding one key, and returns its socket.
// Real rather than stubbed: the point of these tests is the value handed to
// git, and only a real agent produces a real key.
func startTestAgent(t *testing.T) string {
	t.Helper()
	for _, bin := range []string{"ssh-agent", "ssh-add", "ssh-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}

	// Short path: a unix socket path is capped near 100 bytes by the kernel,
	// and t.TempDir() under a long test name can exceed it.
	dir, err := os.MkdirTemp("", "cliag")
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

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	key := filepath.Join(dir, "id_ed25519")
	if out, err := exec.Command("ssh-keygen",
		"-t", "ed25519", "-N", "", "-C", "cli-test", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	add := exec.Command("ssh-add", key)
	add.Env = append(os.Environ(), "SSH_AUTH_SOCK="+socket)
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("ssh-add: %v\n%s", err, out)
	}
	return socket
}

// notForwarder is a provider that cannot carry the agent, standing in for one
// added later that has not implemented the optional interface.
type notForwarder struct{ provider.Provider }

func TestForwardAgentOffIsAPassthrough(t *testing.T) {
	a, _ := newTestApp(t)
	in := []provider.EnvVar{{Key: "FOO", Value: "bar"}}

	t2 := &target{
		workspace: model.Workspace{Name: "ws", SSHForward: false},
		provider:  notForwarder{},
	}

	out, stop, err := a.forwardAgent(context.Background(), t2, in)
	if err != nil {
		t.Fatalf("forwardAgent: %v", err)
	}
	// A stop that is safe to call is what lets every caller defer it without
	// checking whether forwarding was on.
	stop()

	if len(out) != len(in) || out[0] != in[0] {
		t.Errorf("environment changed with forwarding off: %v", out)
	}
	for _, e := range out {
		if e.Key == sshAuthSockKey {
			t.Error("SSH_AUTH_SOCK was set with forwarding off")
		}
	}
}

// TestForwardAgentNoHostAgent covers the host having no agent: reported by the
// name of the variable to set, because the alternative is a permission-denied
// from git much later that says nothing about the host.
func TestForwardAgentNoHostAgent(t *testing.T) {
	a, _ := newTestApp(t)
	t.Setenv("SSH_AUTH_SOCK", "")

	t2 := &target{
		workspace: model.Workspace{Name: "ws", SSHForward: true},
		provider:  notForwarder{},
	}

	_, _, err := a.forwardAgent(context.Background(), t2, nil)
	if err == nil {
		t.Fatal("expected an error when the host has no ssh agent")
	}
	if !strings.Contains(err.Error(), sshAuthSockKey) {
		t.Errorf("error should name %s, got: %v", sshAuthSockKey, err)
	}
}

// TestWorkspaceInitStoresSSHForward covers the flag reaching the database.
// Without the round trip a workspace would report forwarding on and every
// command would quietly run without it.
func TestWorkspaceInitStoresSSHForward(t *testing.T) {
	a, out := newTestApp(t)
	if err := runProviderConfigure(a, "p", "local", k8s.Config{}); err != nil {
		t.Fatal(err)
	}

	if err := runWorkspaceInit(a, "on", "p", true); err != nil {
		t.Fatal(err)
	}
	if err := runWorkspaceInit(a, "off", "p", false); err != nil {
		t.Fatal(err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"on": true, "off": false} {
		ws, err := st.GetWorkspace(name)
		if err != nil {
			t.Fatal(err)
		}
		if ws.SSHForward != want {
			t.Errorf("workspace %s: SSHForward = %v, want %v", name, ws.SSHForward, want)
		}
	}

	// Said out loud at creation, because it is the kind of setting an operator
	// should not have to run a second command to discover.
	if !strings.Contains(out.String(), "ssh agent forwarding is on") {
		t.Errorf("init did not report forwarding; got:\n%s", out.String())
	}
}

// TestSigningEnvPairsAreContiguous covers the trap in git's environment-based
// configuration: GIT_CONFIG_COUNT must equal the number of pairs, numbered from
// zero with no gaps. A gap is a fatal git error, not a skipped entry — so every
// command in the container would fail, not just the signing.
func TestSigningEnvPairsAreContiguous(t *testing.T) {
	socket := startTestAgent(t)
	env := signingEnv(socket)
	if len(env) == 0 {
		t.Fatal("no signing configuration was produced")
	}

	byKey := map[string]string{}
	for _, e := range env {
		byKey[e.Key] = e.Value
	}

	count := byKey["GIT_CONFIG_COUNT"]
	n, err := strconv.Atoi(count)
	if err != nil {
		t.Fatalf("GIT_CONFIG_COUNT is %q, not a number", count)
	}
	for i := range n {
		if _, ok := byKey[fmt.Sprintf("GIT_CONFIG_KEY_%d", i)]; !ok {
			t.Errorf("GIT_CONFIG_KEY_%d is missing", i)
		}
		if _, ok := byKey[fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)]; !ok {
			t.Errorf("GIT_CONFIG_VALUE_%d is missing", i)
		}
	}
	// And nothing beyond the count, which git would ignore while the operator
	// assumed it applied.
	if _, ok := byKey[fmt.Sprintf("GIT_CONFIG_KEY_%d", n)]; ok {
		t.Errorf("GIT_CONFIG_KEY_%d is set but the count is %d", n, n)
	}
}

// TestSigningEnvUsesKeyPrefix covers the form git needs. Without key:: it reads
// user.signingkey as a *filename* and fails looking for a file that is not
// there.
func TestSigningEnvUsesKeyPrefix(t *testing.T) {
	socket := startTestAgent(t)

	var signingKey string
	env := signingEnv(socket)
	for i, e := range env {
		if e.Value == "user.signingkey" && i+1 < len(env) {
			signingKey = env[i+1].Value
		}
	}
	if signingKey == "" {
		t.Fatal("user.signingkey was not configured")
	}
	if !strings.HasPrefix(signingKey, "key::") {
		t.Errorf("user.signingkey lacks the key:: prefix: %q", signingKey)
	}
}

// TestSigningEnvWithoutAgent covers a socket that answers nothing: signing is
// skipped rather than configured with an empty key, which git would reject on
// every commit.
func TestSigningEnvWithoutAgent(t *testing.T) {
	if env := signingEnv(t.TempDir() + "/absent.sock"); env != nil {
		t.Errorf("signing was configured without an agent: %v", env)
	}
}

// TestForwardAgentUnsupportedProvider covers a provider that does not implement
// the optional interface. Refused rather than skipped: the workspace asked for
// the agent, and carrying on without it would fail later and elsewhere.
func TestForwardAgentUnsupportedProvider(t *testing.T) {
	a, _ := newTestApp(t)
	// A socket that exists, so the failure is about the provider rather than
	// about the host.
	sock := t.TempDir() + "/agent.sock"
	if err := touch(sock); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", sock)

	t2 := &target{
		workspace: model.Workspace{Name: "ws", ProviderName: "somekind", SSHForward: true},
		provider:  notForwarder{},
	}

	_, _, err := a.forwardAgent(context.Background(), t2, nil)
	if err == nil {
		t.Fatal("expected an error from a provider that cannot forward")
	}
	for _, want := range []string{"ws", "somekind"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q, got: %v", want, err)
		}
	}
}
