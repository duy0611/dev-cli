//go:build smoke

package smoke

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const platformImage = "docker.io/library/alpine:3.20"

// A container is built for the engine's architecture even when the local tag of
// its base image names the other one, as a k8s cross-build leaves it.
func TestSmokeBuildsForTheEnginePlatform(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")

	// An operator's own platform would win, and is not what this tests.
	t.Setenv("DOCKER_DEFAULT_PLATFORM", "")

	t.Setenv("DEV_STATE", t.TempDir())
	bin := buildBinary(t)
	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	out, err := exec.Command("docker", "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}").Output()
	if err != nil {
		t.Fatalf("reading the engine platform: %v", err)
	}
	engine := strings.TrimSpace(string(out))
	// The other platform, and what uname -m prints on the engine's own.
	var other, want string
	switch engine {
	case "linux/amd64":
		other, want = "linux/arm64", "x86_64"
	case "linux/arm64":
		other, want = "linux/amd64", "aarch64"
	default:
		t.Skipf("the engine runs %q; this test knows linux/amd64 and linux/arm64", engine)
	}

	const (
		name = "dev-smoke-platform"
		ws   = "dev-smoke-platform-ws"
	)
	// Registered first so it runs last, after the container is gone.
	t.Cleanup(func() {
		_ = exec.Command("docker", "pull", "--platform", engine, platformImage).Run()
	})
	t.Cleanup(func() {
		_ = exec.Command(bin, "container", "remove", name, "--force").Run()
	})

	if out, err := exec.Command("docker", "pull", "--platform", other, platformImage).CombinedOutput(); err != nil {
		t.Fatalf("pulling %s for %s: %v\n%s", platformImage, other, err, out)
	}

	project := t.TempDir()
	dc := filepath.Join(project, ".devcontainer")
	if err := os.MkdirAll(dc, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"Dockerfile":        "FROM " + platformImage + "\nRUN uname -m > /arch\n",
		"devcontainer.json": `{"name": "dev-smoke-platform", "build": {"dockerfile": "Dockerfile"}}`,
	}
	for f, content := range files {
		if err := os.WriteFile(filepath.Join(dc, f), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	dev("provider", "configure", "dev-smoke-platform-local", "--kind", "local")
	dev("workspace", "init", ws, "--provider", "dev-smoke-platform-local")

	t.Logf("creating a container with %s tagged for %s on a %s engine", platformImage, other, engine)
	dev("container", "create", name, "--folder", project)

	if got := strings.TrimSpace(dev("container", "exec", name, "--", "cat", "/arch")); got != want {
		t.Errorf("the image was built for %q, want %q", got, want)
	}
}
