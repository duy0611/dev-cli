//go:build smoke

package smoke

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The declaration end to end: a generated container with Claude Code, a
// project that ships only an agents.yaml, and a rebuild that has to re-apply
// over what the first apply left on the state volume.
func TestSmokeAgentConfig(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")

	t.Setenv("DEV_STATE", t.TempDir())
	bin := buildBinary(t)
	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	const (
		name = "dev-smoke-agents"
		ws   = "dev-smoke-agents-ws"
	)
	t.Cleanup(func() {
		_ = exec.Command(bin, "container", "remove", name, "--force").Run()
		_ = exec.Command("docker", "volume", "rm", "--force", "dev-"+ws+"-"+name+"-state").Run()
	})

	// No devcontainer.json, so --generate applies; the agents.yaml beside
	// where one would be is found by default.
	project := t.TempDir()
	for rel, body := range map[string]string{
		".devcontainer/agents.yaml": `version: 1
skills:
  - path: ./skills/hello
mcp:
  echo:
    command: npx
    args: ["-y", "@modelcontextprotocol/server-everything"]
claude:
  marketplaces:
    claude-plugins-official: anthropics/claude-plugins-official
  plugins: [superpowers@claude-plugins-official]
`,
		".devcontainer/skills/hello/SKILL.md": "---\nname: hello\ndescription: says hello\n---\nSay hello.\n",
	} {
		p := filepath.Join(project, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	dev("provider", "configure", "dev-smoke-agents-local", "--kind", "local")
	dev("workspace", "init", ws, "--provider", "dev-smoke-agents-local")

	check := func(when string) {
		t.Helper()
		dev("container", "exec", name, "--", "sh", "-c", `test -f "$CLAUDE_CONFIG_DIR/skills/hello/SKILL.md"`)
		if out := dev("container", "exec", name, "--", "claude", "plugin", "list"); !strings.Contains(out, "superpowers") {
			t.Errorf("%s: the plugin is not installed:\n%s", when, out)
		}
		if out := dev("container", "exec", name, "--", "claude", "mcp", "list"); !strings.Contains(out, "echo") {
			t.Errorf("%s: the MCP server is not configured:\n%s", when, out)
		}
	}

	t.Log("creating; the apply runs once the container is up")
	dev("container", "create", name, "--folder", project, "--generate", "--tools", "claude-code")
	check("after create")

	t.Log("rebuilding; every step has to be safe to run over its own result")
	dev("container", "rebuild", name)
	check("after rebuild")
}
