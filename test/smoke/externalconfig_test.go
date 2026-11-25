//go:build smoke

package smoke

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A devcontainer.json kept in a sibling directory configures a project that
// ships none. This also settles, on a real engine, what the devcontainer CLI
// anchors a relative build.dockerfile and build.context to, and what
// ${localWorkspaceFolder} expands to, when --config lies outside
// --workspace-folder.
func TestSmokeExternalConfig(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")

	t.Setenv("DEV_STATE", t.TempDir())
	bin := buildBinary(t)
	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	const (
		name = "dev-smoke-extcfg"
		ws   = "dev-smoke-extcfg-ws"
	)
	t.Cleanup(func() {
		_ = exec.Command(bin, "container", "remove", name, "--force").Run()
		_ = exec.Command("docker", "volume", "rm", "--force", "dev-"+ws+"-"+name+"-state").Run()
	})

	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(parent, "project")
	sibling := filepath.Join(parent, "project-dev")
	for path, body := range map[string]string{
		filepath.Join(project, "marker.txt"): "from-the-project\n",
		// Relative dockerfile and context, so the build proves which directory
		// they are read from. The marker files exist only beside the config.
		filepath.Join(sibling, "devcontainer.json"): `{
  "build": {"dockerfile": "Dockerfile", "context": "."},
  "containerEnv": {"SMOKE_LOCAL_WORKSPACE": "${localWorkspaceFolder}"}
}
`,
		filepath.Join(sibling, "Dockerfile"):         "FROM ubuntu:24.04\nCOPY context-marker.txt /etc/smoke-context-marker\n",
		filepath.Join(sibling, "context-marker.txt"): "from-the-sibling\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	dev("provider", "configure", "dev-smoke-extcfg-local", "--kind", "local")
	dev("workspace", "init", ws, "--provider", "dev-smoke-extcfg-local")

	t.Log("creating with --config; the first run pulls a base image")
	dev("container", "create", name, "--folder", project, "--config", filepath.Join(sibling, "devcontainer.json"))

	exec1 := func(script string) string {
		t.Helper()
		return strings.TrimSpace(dev("container", "exec", name, "--", "sh", "-c", script))
	}
	if got := exec1("cat /etc/smoke-context-marker"); got != "from-the-sibling" {
		t.Errorf("build context marker = %q; the build did not use the sibling's Dockerfile and context", got)
	}
	if got := exec1("cat /workspaces/project/marker.txt"); got != "from-the-project" {
		t.Errorf("project marker = %q; the project is not mounted", got)
	}
	if got := exec1(`printf %s "$SMOKE_LOCAL_WORKSPACE"`); got != project {
		t.Errorf("${localWorkspaceFolder} = %q, want the project %q", got, project)
	}

	if out := dev("container", "config", "show", name); !strings.Contains(out, "SMOKE_LOCAL_WORKSPACE") {
		t.Errorf("config show did not print the sibling's file:\n%s", out)
	}

	entries, err := os.ReadDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "marker.txt" {
		t.Errorf("dev wrote into the project: %v", entries)
	}

	t.Log("rebuilding; the stored path is read again")
	dev("container", "rebuild", name)
	if got := exec1("cat /etc/smoke-context-marker"); got != "from-the-sibling" {
		t.Errorf("after rebuild, build context marker = %q", got)
	}
}
