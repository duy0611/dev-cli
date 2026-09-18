//go:build smoke

// Package smoke drives the real binary against a real container engine.
//
// Behind a build tag so `make test` never reaches for an engine. Run it with
// `make smoke`, on a host that has the devcontainer CLI, a Docker-compatible
// engine, and network access for the image pull.
package smoke

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A tiny image, so the pull is seconds rather than minutes. The devcontainer
// CLI supplies its own long-running command for an image-based config, so
// alpine does not need one.
const fixtureConfig = `{
  "name": "dev-smoke",
  "image": "docker.io/library/alpine:3.20"
}`

const (
	workspaceName = "dev-smoke-ws"
	containerName = "dev-smoke"
	providerName  = "dev-smoke-local"
)

func TestSmoke(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")

	// A state directory of its own: the operator's real database is not a test
	// fixture.
	state := t.TempDir()
	t.Setenv("DEV_STATE", state)

	bin := buildBinary(t)
	project := fixtureProject(t)

	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	// Removal has to happen even when an assertion fails part way through, or
	// the next run finds a container it did not create.
	t.Cleanup(func() {
		_ = exec.Command(bin, "container", "remove", containerName, "--force").Run()
	})

	dev("provider", "configure", providerName, "--kind", "local")
	dev("workspace", "init", workspaceName, "--provider", providerName)
	dev("workspace", "set", "GREETING", "literal:hello")

	t.Log("creating the container; the first run pulls an image")
	dev("container", "create", containerName, "--folder", project)

	// Both labels, or the isolation the whole design rests on is not there.
	ids := dockerPS(t, "-q",
		"--filter", "label=dev.workspace="+workspaceName,
		"--filter", "label=dev.container="+containerName)
	if len(ids) != 1 {
		t.Fatalf("docker matched %d containers on the dev.* labels, want 1: %v", len(ids), ids)
	}

	if got := strings.TrimSpace(dev("container", "exec", containerName, "--", "printenv", "GREETING")); got != "hello" {
		t.Errorf("workspace setting did not reach the container: printenv GREETING = %q, want %q", got, "hello")
	}

	// The record is what `list` prints; the status beside it comes from the
	// engine.
	if out := dev("container", "list"); !strings.Contains(out, containerName) || !strings.Contains(out, "running") {
		t.Errorf("container list does not show a running %s:\n%s", containerName, out)
	}

	dev("container", "stop", containerName)
	if out := dev("container", "list"); strings.Contains(out, "running") {
		t.Errorf("container still reads as running after stop:\n%s", out)
	}

	dev("container", "remove", containerName)

	if ids := dockerPS(t, "-aq", "--filter", "label=dev.container="+containerName); len(ids) != 0 {
		t.Errorf("remove left %d containers behind: %v", len(ids), ids)
	}
	if out := dev("container", "list"); strings.Contains(out, containerName) {
		t.Errorf("remove left the record behind:\n%s", out)
	}
}

// --- helpers -------------------------------------------------------------------

// requireAnyBinary skips unless at least one of the alternatives is present,
// for a job either of them can do.
func requireAnyBinary(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err == nil {
			return
		}
	}
	t.Skipf("none of %v is on PATH", names)
}

func requireBinaries(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s is not on PATH; the smoke test needs a real engine", n)
		}
	}
}

// buildBinary compiles the CLI under test, so the smoke test cannot pass
// against a stale dist/dev from an earlier build.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "dev")

	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/dev")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the binary: %v\n%s", err, out)
	}
	return bin
}

func fixtureProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".devcontainer"), 0o755); err != nil {
		t.Fatalf("creating the fixture: %v", err)
	}
	path := filepath.Join(dir, ".devcontainer", "devcontainer.json")
	if err := os.WriteFile(path, []byte(fixtureConfig), 0o600); err != nil {
		t.Fatalf("writing the fixture config: %v", err)
	}
	return dir
}

func run(t *testing.T, bin string, args ...string) string {
	t.Helper()

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// An image pull on a cold cache is the slow part; everything else is
	// seconds.
	timer := time.AfterFunc(10*time.Minute, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	defer timer.Stop()

	if err := cmd.Run(); err != nil {
		t.Fatalf("dev %s: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func dockerPS(t *testing.T, args ...string) []string {
	t.Helper()

	out, err := exec.Command("docker", append([]string{"ps"}, args...)...).Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	var ids []string
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			ids = append(ids, s)
		}
	}
	return ids
}

// dockerVolumes returns the volume names matching a name filter.
func dockerVolumes(t *testing.T, name string) []string {
	t.Helper()
	out, err := exec.Command("docker", "volume", "ls", "-q", "--filter", "name="+name).Output()
	if err != nil {
		t.Fatalf("docker volume ls: %v", err)
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names
}

// A folder with no configuration of its own is the whole point of --generate,
// so the fixture is a bare directory.
func TestSmokeGeneratedConfig(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")

	state := t.TempDir()
	t.Setenv("DEV_STATE", state)

	bin := buildBinary(t)
	project := t.TempDir() // deliberately empty

	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	const name = "dev-smoke-gen"
	t.Cleanup(func() {
		_ = exec.Command(bin, "container", "remove", name, "--force").Run()
	})

	dev("provider", "configure", "dev-smoke-gen-local", "--kind", "local")
	dev("workspace", "init", "dev-smoke-gen-ws", "--provider", "dev-smoke-gen-local")

	t.Log("creating a generated container; the first run pulls a base image and installs features")
	dev("container", "create", name, "--folder", project, "--generate", "--tools", "yq")

	// The tool the operator asked for has to actually be in the container: a
	// feature reference that resolves is not the same as one that installs.
	//
	// yq rather than something the base image already ships — an assertion on a
	// tool that is present either way would pass with the feature omitted
	// entirely, and prove nothing.
	if out := dev("container", "exec", name, "--", "yq", "--version"); !strings.Contains(out, "yq") {
		t.Errorf("yq is not installed in the generated container: %q", out)
	}

	// The project folder is dev's to read, never to write.
	entries, err := os.ReadDir(project)
	if err != nil {
		t.Fatalf("reading the project folder: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("dev wrote into the project folder: %v", entries)
	}

	if out := dev("container", "config", "show", name); !strings.Contains(out, "apt-get-packages") {
		t.Errorf("config show did not print the stored configuration:\n%s", out)
	}

	dev("container", "remove", name)
}

// The claim a folderless container rests on: work survives a stop and a start,
// because it is in a volume rather than in the container filesystem.
func TestSmokeFolderless(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")

	state := t.TempDir()
	t.Setenv("DEV_STATE", state)

	bin := buildBinary(t)
	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	const (
		name = "dev-smoke-none"
		ws   = "dev-smoke-none-ws"
	)
	t.Cleanup(func() {
		_ = exec.Command(bin, "container", "remove", name, "--force").Run()
		// The volume outlives a failed remove, and the next run would mount
		// the previous run's files.
		_ = exec.Command("docker", "volume", "rm", "--force", "dev-"+ws+"-"+name).Run()
	})

	dev("provider", "configure", "dev-smoke-none-local", "--kind", "local")
	dev("workspace", "init", ws, "--provider", "dev-smoke-none-local")

	t.Log("creating a folderless container; the first run pulls a base image")
	dev("container", "create", name, "--no-folder")

	dev("container", "exec", name, "--", "sh", "-c", "echo persisted > /workspaces/"+name+"/marker")

	dev("container", "stop", name)
	dev("container", "start", name)

	got := strings.TrimSpace(dev("container", "exec", name, "--", "cat", "/workspaces/"+name+"/marker"))
	if got != "persisted" {
		t.Errorf("the workspace did not survive a stop and start: marker = %q, want %q", got, "persisted")
	}

	// The volume is dev's to remove. Left behind, it accumulates silently.
	volume := "dev-" + ws + "-" + name
	dev("container", "remove", name)
	if out := dockerVolumes(t, volume); len(out) != 0 {
		t.Errorf("remove left the volume behind: %v", out)
	}
}
