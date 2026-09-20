package k8s

import (
	"context"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/provider"
)

// TestProviderImplementsAgentForwarder pins the type assertion callers use.
// A provider that stopped satisfying the interface would fail nowhere at
// compile time — the caller would just stop forwarding the agent.
func TestProviderImplementsAgentForwarder(t *testing.T) {
	var p any = testProvider()
	if _, ok := p.(provider.AgentForwarder); !ok {
		t.Fatal("k8s provider does not implement provider.AgentForwarder")
	}
}

// agentStubs answers the configuration read and lets kubectl report the
// container's architecture.
func agentStubs(t *testing.T, arch string) *stubs {
	t.Helper()
	s := newStubs(t)
	s.installKubectl(t, `case "$*" in
  *uname*) echo `+arch+` ;;
esac`)
	s.installScript(t, devcontainerBin, `case "$1" in
  read-configuration)
    echo '{"mergedConfiguration":{"workspaceFolder":"/workspaces/api","remoteUser":"node","containerEnv":{},"remoteEnv":{}}}'
    ;;
esac`)
	// The configuration read needs a docker on PATH even though it only reads a
	// file: the devcontainer CLI shells out to the engine regardless.
	withBuildx(t, s)
	return s
}

// TestForwardAgentTargetsTheDeployment covers the exec reaching the right
// object in the right cluster. Every kubectl call has to carry --context and
// --namespace: one that does not will not fail, it will succeed somewhere else.
func TestForwardAgentTargetsTheDeployment(t *testing.T) {
	s := agentStubs(t, "riscv64")
	c := k8sContainer(t)

	// An architecture nothing is embedded for stops the session immediately
	// after the uname, which is all this test needs: the argv that carried it.
	_, err := testProvider().ForwardAgent(context.Background(), c, "/tmp/agent.sock")
	if err == nil {
		t.Fatal("expected an error for an unsupported architecture")
	}
	if !strings.Contains(err.Error(), "riscv64") {
		t.Errorf("error should name the architecture, got: %v", err)
	}

	calls := kubectlCalls(t, s)
	if !anyCall(calls, "uname") {
		t.Fatalf("the architecture was never read: %v", calls)
	}
	for _, want := range []string{"--context", "--namespace", "deployment/"} {
		if !anyCall(calls, want) {
			t.Errorf("no kubectl call carried %q: %v", want, calls)
		}
	}
}

// TestForwardAgentRunsAsRemoteUser covers the relay running as the user the
// configuration names. kubectl exec runs as the image's own USER, and the
// devcontainer spec's remoteUser is applied by the CLI at exec time, which
// never happens here — so a relay left as root would write its socket somewhere
// the operator's own shell cannot use it.
func TestForwardAgentRunsAsRemoteUser(t *testing.T) {
	s := agentStubs(t, "riscv64")

	_, _ = testProvider().ForwardAgent(context.Background(), k8sContainer(t), "/tmp/agent.sock")

	calls := kubectlCalls(t, s)
	// The command is shell-quoted and then quoted again inside su -c, so match
	// the user rather than any particular spelling of the wrapper.
	if !anyCall(calls, "node") {
		t.Errorf("the relay does not switch to the remote user: %v", calls)
	}
}

// TestForwardAgentRequiresKubectl covers the binary being absent: reported as a
// missing tool rather than as a failure to forward.
func TestForwardAgentRequiresKubectl(t *testing.T) {
	// A stub directory with nothing in it. Replacing PATH rather than
	// prepending, or a real kubectl on this machine would answer.
	t.Setenv("PATH", t.TempDir())

	_, err := testProvider().ForwardAgent(context.Background(), k8sContainer(t), "/tmp/agent.sock")
	if err == nil {
		t.Fatal("expected an error when kubectl is absent")
	}
	if !strings.Contains(err.Error(), kubectlBin) {
		t.Errorf("error should name the missing binary, got: %v", err)
	}
}
