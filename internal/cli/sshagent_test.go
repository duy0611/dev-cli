package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

// waitForTestAgent waits until the agent at socket answers. The path existing
// is not enough: bind(2) creates it a moment before listen(2) makes it accept,
// and a dial landing in that gap is refused.
func waitForTestAgent(t *testing.T, socket string) {
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

// startTestAgent runs a real ssh-agent holding one key and returns its socket.
func startTestAgent(t *testing.T) string {
	t.Helper()
	for _, bin := range []string{"ssh-agent", "ssh-add", "ssh-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}

	// Short path: unix socket paths are capped near 100 bytes.
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
	waitForTestAgent(t, socket)

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

// notForwarder is a provider without the optional AgentForwarder interface.
type notForwarder struct{ provider.Provider }

func TestForwardAgentOffIsAPassthrough(t *testing.T) {
	a, _ := newTestApp(t)
	in := []provider.EnvVar{{Key: "FOO", Value: "bar"}}

	t2 := &target{
		workspace: model.Workspace{Name: "ws", SSHForward: false},
		provider:  notForwarder{},
	}

	out, stop, err := a.forwardAgent(context.Background(), t2, nil, in)
	if err != nil {
		t.Fatalf("forwardAgent: %v", err)
	}
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

// The no-agent error names SSH_AUTH_SOCK, not a later permission-denied.
func TestForwardAgentNoHostAgent(t *testing.T) {
	a, _ := newTestApp(t)
	t.Setenv("SSH_AUTH_SOCK", "")

	t2 := &target{
		workspace: model.Workspace{Name: "ws", SSHForward: true},
		provider:  notForwarder{},
	}

	_, _, err := a.forwardAgent(context.Background(), t2, nil, nil)
	if err == nil {
		t.Fatal("expected an error when the host has no ssh agent")
	}
	if !strings.Contains(err.Error(), sshAuthSockKey) {
		t.Errorf("error should name %s, got: %v", sshAuthSockKey, err)
	}
}

func TestForwardAgentOverrideOff(t *testing.T) {
	a, _ := newTestApp(t)
	in := []provider.EnvVar{{Key: "FOO", Value: "bar"}}
	off := false

	t2 := &target{
		workspace: model.Workspace{Name: "ws", SSHForward: true},
		provider:  notForwarder{},
	}

	out, stop, err := a.forwardAgent(context.Background(), t2, &off, in)
	if err != nil {
		t.Fatalf("forwardAgent: %v", err)
	}
	stop()
	if len(out) != len(in) || out[0] != in[0] {
		t.Errorf("environment changed with --no-ssh-agent: %v", out)
	}
}

func TestForwardAgentOverrideOn(t *testing.T) {
	a, _ := newTestApp(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	on := true

	t2 := &target{
		workspace: model.Workspace{Name: "ws", SSHForward: false},
		provider:  notForwarder{},
	}

	_, _, err := a.forwardAgent(context.Background(), t2, &on, nil)
	if err == nil {
		t.Fatal("expected --ssh-agent to attempt forwarding and fail without a host agent")
	}
	if !strings.Contains(err.Error(), sshAuthSockKey) {
		t.Errorf("error should name %s, got: %v", sshAuthSockKey, err)
	}
}

func TestSSHAgentFlagsOverride(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags sshAgentFlags
		want  *bool
	}{
		{"neither", sshAgentFlags{}, nil},
		{"on", sshAgentFlags{on: true}, ptr(true)},
		{"off", sshAgentFlags{off: true}, ptr(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.flags.override()
			if err != nil {
				t.Fatal(err)
			}
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Errorf("override() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Checked before the container lookup, so a missing one does not exit 3 first.
func TestSSHAgentFlagsTogetherExitTwo(t *testing.T) {
	for _, argv := range [][]string{
		{"container", "shell", "api", "--ssh-agent", "--no-ssh-agent"},
		{"container", "exec", "api", "--ssh-agent", "--no-ssh-agent", "--", "true"},
		{"container", "start-agent", "api", "--ssh-agent", "--no-ssh-agent"},
	} {
		code, out := execute(t, argv...)
		if code != exitUsage {
			t.Errorf("%v: exit %d, want %d\n%s", argv, code, exitUsage, out)
		}
	}
}

func ptr[T any](v T) *T { return &v }

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

	if !strings.Contains(out.String(), "ssh agent forwarding is on") {
		t.Errorf("init did not report forwarding; got:\n%s", out.String())
	}
}

// GIT_CONFIG_COUNT must match the pairs exactly: a gap is fatal to git, not
// skipped.
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
	if _, ok := byKey[fmt.Sprintf("GIT_CONFIG_KEY_%d", n)]; ok {
		t.Errorf("GIT_CONFIG_KEY_%d is set but the count is %d", n, n)
	}
}

// key:: is needed: without it git reads user.signingkey as a filename.
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

// An empty key would fail every commit, so signing is skipped.
func TestSigningEnvWithoutAgent(t *testing.T) {
	if env := signingEnv(t.TempDir() + "/absent.sock"); env != nil {
		t.Errorf("signing was configured without an agent: %v", env)
	}
}

func TestForwardAgentUnsupportedProvider(t *testing.T) {
	a, _ := newTestApp(t)
	sock := t.TempDir() + "/agent.sock"
	if err := touch(sock); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", sock)

	t2 := &target{
		workspace: model.Workspace{Name: "ws", ProviderName: "somekind", SSHForward: true},
		provider:  notForwarder{},
	}

	_, _, err := a.forwardAgent(context.Background(), t2, nil, nil)
	if err == nil {
		t.Fatal("expected an error from a provider that cannot forward")
	}
	for _, want := range []string{"ws", "somekind"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q, got: %v", want, err)
		}
	}
}

// GIT_CONFIG_GLOBAL shares the GIT_CONFIG_ prefix; matching it would wrongly
// skip signing.
func TestStateGitConfigDoesNotSuppressSigning(t *testing.T) {
	environ := []provider.EnvVar{{Key: "GIT_CONFIG_GLOBAL", Value: "/var/dev-state/gitconfig"}}
	if hasGitConfigEnv(environ) {
		t.Error("GIT_CONFIG_GLOBAL was read as the operator configuring git themselves")
	}
}

func TestOperatorGitConfigSuppressesSigning(t *testing.T) {
	for _, key := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"} {
		if !hasGitConfigEnv([]provider.EnvVar{{Key: key, Value: "x"}}) {
			t.Errorf("%s was not read as the operator configuring git themselves", key)
		}
	}
}

func TestRelayDropsFields(t *testing.T) {
	if got := relayDrops(nil); got != nil {
		t.Errorf("no drops = %v, want nil", got)
	}

	at := time.Date(2026, 10, 9, 20, 14, 0, 0, time.UTC)
	back := at.Add(3 * time.Second)
	got := relayDrops([]provider.AgentDrop{
		{At: at, Restored: back},
		{At: at.Add(time.Hour), Err: "read |0: file already closed"},
	})
	want := []map[string]any{
		{"at": "2026-10-09T20:14:00Z", "restored": "2026-10-09T20:14:03Z"},
		{"at": "2026-10-09T21:14:00Z", "error": "read |0: file already closed"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("relayDrops = %v, want %v", got, want)
	}
}

// Guarded refuses --ssh-agent: a relay could still push existing commits.
func TestForwardAgentRefusedOnAGuardedContainer(t *testing.T) {
	a, _ := newTestApp(t)
	on := true
	t2 := &target{
		workspace: model.Workspace{Name: "ws"},
		container: model.Container{Name: "api", Mode: model.ModeGuarded},
		provider:  notForwarder{},
	}

	_, _, err := a.forwardAgent(context.Background(), t2, &on, nil)
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
	if !strings.Contains(err.Error(), "guarded") {
		t.Errorf("error %q does not name the mode", err)
	}
}

func TestWorkspaceForwardingSkipsAGuardedContainer(t *testing.T) {
	a, _ := newTestApp(t)
	in := []provider.EnvVar{{Key: "FOO", Value: "bar"}}
	t2 := &target{
		workspace: model.Workspace{Name: "ws", SSHForward: true},
		container: model.Container{Name: "api", Mode: model.ModeGuarded},
		provider:  notForwarder{},
	}

	out, stop, err := a.forwardAgent(context.Background(), t2, nil, in)
	if err != nil {
		t.Fatalf("forwardAgent: %v", err)
	}
	stop()
	for _, e := range out {
		if e.Key == sshAuthSockKey {
			t.Error("SSH_AUTH_SOCK was set for a guarded container")
		}
	}
	if agentForwarded(t2, nil) {
		t.Error("the audit record would say the agent was forwarded")
	}
}

type fakeSession struct{}

func (fakeSession) Socket() string              { return "/run/dev/agent.sock" }
func (fakeSession) Close() error                { return nil }
func (fakeSession) End() string                 { return "closed" }
func (fakeSession) Drops() []provider.AgentDrop { return nil }

type fakeForwarder struct{ provider.Provider }

func (fakeForwarder) ForwardAgent(context.Context, model.Container, string) (provider.AgentSession, error) {
	return fakeSession{}, nil
}

func TestSSHAgentIsForwardedIntoAnUnprotectedContainer(t *testing.T) {
	a, _ := newTestApp(t)
	sock := t.TempDir() + "/agent.sock"
	if err := touch(sock); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", sock)
	on := true
	t2 := &target{
		workspace: model.Workspace{Name: "ws"},
		container: model.Container{Name: "api", Mode: model.ModeUnprotected},
		provider:  fakeForwarder{},
	}

	env, stop, err := a.forwardAgent(context.Background(), t2, &on, nil)
	if err != nil {
		t.Fatalf("forwardAgent: %v", err)
	}
	stop()
	set := false
	for _, e := range env {
		set = set || e.Key == sshAuthSockKey
	}
	if !set || !agentForwarded(t2, &on) {
		t.Errorf("the agent was not forwarded: %v", env)
	}
}
