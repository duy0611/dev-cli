package local

import (
	"context"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// TestProviderImplementsAgentForwarder pins the type assertion callers use. A
// provider that silently stopped satisfying the interface would not fail to
// compile anywhere — the caller would just stop forwarding the agent.
func TestProviderImplementsAgentForwarder(t *testing.T) {
	var p any = &Provider{}
	if _, ok := p.(provider.AgentForwarder); !ok {
		t.Fatal("local provider does not implement provider.AgentForwarder")
	}
}

// TestForwardAgentUsesIdLabels covers invariant 1 for the relay's own exec.
// The relay has to reach the same container as every other command, and the id
// labels are what decide that: given a different set the CLI would look up a
// container that does not exist and create a second one beside it, with no
// error either way.
func TestForwardAgentUsesIdLabels(t *testing.T) {
	f := newFakePath(t)
	// `uname -m` is the first thing a session runs. Answering with an
	// architecture nothing is embedded for stops the session right after that
	// call, which is all this test needs: the argv of the exec that carried it.
	f.install(t, devcontainerBin, "riscv64\n", 0)

	c := model.Container{
		Name:          "api",
		WorkspaceName: "personal",
		SourceKind:    model.SourceFolder,
		Source:        "/tmp/project",
	}

	_, err := (&Provider{}).ForwardAgent(context.Background(), c, "/tmp/agent.sock")
	if err == nil {
		t.Fatal("expected an error for an unsupported architecture")
	}
	// Named rather than a generic failure, so an operator on an architecture
	// with no relay is told which one.
	if !strings.Contains(err.Error(), "riscv64") {
		t.Errorf("error should name the architecture, got: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"--id-label",
		"exec",
		"--workspace-folder",
		"uname",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("exec argv missing %q; got: %s", want, joined)
		}
	}

	// Both labels, not one: the pair is what identifies a container uniquely.
	if n := strings.Count(joined, "--id-label"); n != 2 {
		t.Errorf("expected 2 --id-label arguments, got %d in: %s", n, joined)
	}
}

// TestForwardAgentRequiresCLI covers the binary being absent: reported as a
// missing tool rather than as a failure to forward.
func TestForwardAgentRequiresCLI(t *testing.T) {
	// An empty stub directory, so the devcontainer CLI is not found. Replacing
	// PATH rather than prepending, or a real CLI on this machine would answer.
	t.Setenv("PATH", t.TempDir())

	_, err := (&Provider{}).ForwardAgent(context.Background(), model.Container{
		Name: "api", WorkspaceName: "personal", Source: "/tmp/project",
	}, "/tmp/agent.sock")
	if err == nil {
		t.Fatal("expected an error when the devcontainer CLI is absent")
	}
	if !strings.Contains(err.Error(), devcontainerBin) {
		t.Errorf("error should name the missing binary, got: %v", err)
	}
}
