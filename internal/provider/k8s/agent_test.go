package k8s

import (
	"context"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/provider"
)

// A failed assertion would stop forwarding the agent without any error.
func TestProviderImplementsAgentForwarder(t *testing.T) {
	var p any = testProvider()
	if _, ok := p.(provider.AgentForwarder); !ok {
		t.Fatal("k8s provider does not implement provider.AgentForwarder")
	}
}

// agentStubs answers the configuration read and reports the container's
// architecture.
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
	// The CLI runs docker even to read a configuration, so docker must be on
	// PATH.
	withBuildx(t, s)
	return s
}

// TestForwardAgentTargetsTheDeployment checks that exec reaches the right
// object. Every kubectl call must carry --context and --namespace, or it
// succeeds elsewhere.
func TestForwardAgentTargetsTheDeployment(t *testing.T) {
	s := agentStubs(t, "riscv64")
	c := k8sContainer(t)

	// An unsupported architecture stops the session after the uname, which is
	// all this test needs.
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

// kubectl exec runs as the image's USER, not remoteUser, so the relay must
// switch itself.
func TestForwardAgentRunsAsRemoteUser(t *testing.T) {
	s := agentStubs(t, "riscv64")

	_, _ = testProvider().ForwardAgent(context.Background(), k8sContainer(t), "/tmp/agent.sock")

	calls := kubectlCalls(t, s)
	// Commands are quoted twice inside su -c, so match the user name only.
	if !anyCall(calls, "node") {
		t.Errorf("the relay does not switch to the remote user: %v", calls)
	}
}

// A missing kubectl is reported by name, not as a forwarding failure.
func TestForwardAgentRequiresKubectl(t *testing.T) {
	// Replace PATH rather than prepend, or a host kubectl answers.
	t.Setenv("PATH", t.TempDir())

	_, err := testProvider().ForwardAgent(context.Background(), k8sContainer(t), "/tmp/agent.sock")
	if err == nil {
		t.Fatal("expected an error when kubectl is absent")
	}
	if !strings.Contains(err.Error(), kubectlBin) {
		t.Errorf("error should name the missing binary, got: %v", err)
	}
}
