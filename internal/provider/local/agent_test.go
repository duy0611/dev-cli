package local

import (
	"context"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// TestProviderImplementsAgentForwarder checks the type assertion callers use.
func TestProviderImplementsAgentForwarder(t *testing.T) {
	var p any = &Provider{}
	if _, ok := p.(provider.AgentForwarder); !ok {
		t.Fatal("local provider does not implement provider.AgentForwarder")
	}
}

// TestForwardAgentUsesIdLabels checks the relay's exec carries the id-labels
// (invariant 1).
func TestForwardAgentUsesIdLabels(t *testing.T) {
	f := newFakePath(t)
	// `uname -m` runs first; an unsupported architecture stops the session
	// there.
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

	// Both labels, since the pair identifies the container.
	if n := strings.Count(joined, "--id-label"); n != 2 {
		t.Errorf("expected 2 --id-label arguments, got %d in: %s", n, joined)
	}
}

// TestForwardAgentRequiresCLI checks a missing devcontainer CLI is reported by
// name.
func TestForwardAgentRequiresCLI(t *testing.T) {
	// Replaces PATH rather than prepending, so a real CLI cannot answer.
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
