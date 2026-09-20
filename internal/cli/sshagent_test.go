package cli

import (
	"context"
	"os"
	"strings"
	"testing"

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
