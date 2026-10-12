package k8s

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// withBuildx installs a docker whose `buildx version` prints a semver, as the
// CLI's probe needs.
func withBuildx(t *testing.T, s *stubs) {
	t.Helper()
	s.installScript(t, dockerBin, `case "$1 $2" in
  "buildx version") echo "github.com/docker/buildx v0.17.1 ffa4ba5" ;;
esac`)
}

// withoutBuildx installs a docker whose `buildx version` fails, as a bare
// docker does.
func withoutBuildx(t *testing.T, s *stubs) {
	t.Helper()
	s.installScript(t, dockerBin, `case "$1" in
  buildx) exit 1 ;;
esac`)
}

// withPodman installs a podman whose `buildx version` prints a version, enough
// for the CLI's probe.
func withPodman(t *testing.T, s *stubs) {
	t.Helper()
	s.installScript(t, podmanBin, `case "$1 $2" in
  "buildx version") echo "podman version 5.3.1" ;;
esac`)
}

func TestBuildAndPushPassesPlatformAndPush(t *testing.T) {
	s := newStubs(t)
	s.install(t, devcontainerBin, "", 0)
	withBuildx(t, s)

	err := buildAndPush(context.Background(), "/projects/api", "",
		"reg.example/dev/ws-api:latest", "linux/amd64", false, io.Discard)
	if err != nil {
		t.Fatalf("buildAndPush: %v", err)
	}

	argv := s.argv(t, devcontainerBin)
	if argv[0] != "build" {
		t.Errorf("argv[0] = %q, want build", argv[0])
	}
	for _, want := range [][]string{
		{"--workspace-folder", "/projects/api"},
		{"--image-name", "reg.example/dev/ws-api:latest"},
		// Nodes are amd64; an arm64 image crash-loops with "exec format error".
		{"--platform", "linux/amd64"},
	} {
		if !hasPair(argv, want[0], want[1]) {
			t.Errorf("argv %v is missing %v", argv, want)
		}
	}
	if !contains(argv, "--push") {
		t.Errorf("argv %v does not push; the cluster would have nothing to pull", argv)
	}
	if contains(argv, "--no-cache") {
		t.Errorf("argv %v disabled the cache without being asked", argv)
	}
}

func TestBuildAndPushNoCache(t *testing.T) {
	s := newStubs(t)
	s.install(t, devcontainerBin, "", 0)
	withBuildx(t, s)

	if err := buildAndPush(context.Background(), "/p", "", "img", "linux/amd64", true, io.Discard); err != nil {
		t.Fatalf("buildAndPush: %v", err)
	}
	if !contains(s.argv(t, devcontainerBin), "--no-cache") {
		t.Error("--no-cache was requested but not passed")
	}
}

func TestBuildAndPushReportsFailure(t *testing.T) {
	s := newStubs(t)
	s.install(t, devcontainerBin, "", 1)
	withBuildx(t, s)

	err := buildAndPush(context.Background(), "/p", "", "img", "linux/amd64", false, io.Discard)
	if err == nil {
		t.Fatal("buildAndPush succeeded against a failing CLI")
	}
	if !strings.Contains(err.Error(), "img") {
		t.Errorf("error %q does not name the image", err)
	}
}

// Without BuildKit the CLI refuses --platform and --push, so the push is done
// with docker.
func TestBuildAndPushWithoutBuildx(t *testing.T) {
	s := newStubs(t)
	s.installScript(t, devcontainerBin, "")
	withoutBuildx(t, s)

	err := buildAndPush(context.Background(), "/p", "", "reg.example/x:latest",
		"linux/"+hostArch, false, io.Discard)
	if err != nil {
		t.Fatalf("buildAndPush: %v", err)
	}

	build := s.calls(t, devcontainerBin)
	if anyBuildCall(build, "--platform") || anyBuildCall(build, "--push") {
		t.Errorf("passed flags the CLI refuses without BuildKit: %v", build)
	}
	if !anyBuildCall(s.calls(t, dockerBin), "push reg.example/x:latest") {
		t.Errorf("the image was built but never pushed: %v", s.calls(t, dockerBin))
	}
}

// A wrong-architecture image crash-loops with "exec format error", far from the
// build.
func TestBuildAndPushRefusesCrossArchWithoutBuildx(t *testing.T) {
	s := newStubs(t)
	s.installScript(t, devcontainerBin, "")
	withoutBuildx(t, s)

	other := "amd64"
	if hostArch == "amd64" {
		other = "arm64"
	}

	err := buildAndPush(context.Background(), "/p", "", "img", "linux/"+other, false, io.Discard)
	if err == nil {
		t.Fatal("buildAndPush built for the wrong architecture without complaint")
	}
	if !strings.Contains(err.Error(), "buildx") || !strings.Contains(err.Error(), hostArch) {
		t.Errorf("error %q does not say how to proceed", err)
	}
}

func anyBuildCall(calls []string, substr string) bool {
	for _, c := range calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// podman passes the CLI's BuildKit probe but has no --push, so it pushes
// separately.
func TestBuildAndPushPrefersPodmanOverAHostArchBuild(t *testing.T) {
	s := newStubs(t)
	s.installScript(t, devcontainerBin, "")
	withoutBuildx(t, s)
	withPodman(t, s)

	other := "amd64"
	if hostArch == "amd64" {
		other = "arm64"
	}

	err := buildAndPush(context.Background(), "/p", "", "reg.example/x:latest",
		"linux/"+other, false, io.Discard)
	if err != nil {
		t.Fatalf("buildAndPush: %v", err)
	}

	build := s.calls(t, devcontainerBin)
	if !anyBuildCall(build, "--docker-path podman") {
		t.Errorf("the CLI was not pointed at podman: %v", build)
	}
	if !anyBuildCall(build, "--platform linux/"+other) {
		t.Errorf("the platform was dropped: %v", build)
	}
	if anyBuildCall(build, "--push") {
		t.Errorf("passed --push, which podman build does not have: %v", build)
	}
	if !anyBuildCall(s.calls(t, podmanBin), "push reg.example/x:latest") {
		t.Errorf("podman never pushed the image: %v", s.calls(t, podmanBin))
	}
}

// Rootless podman fails to build Features on the Dockerfile's bind mount
// (devcontainers/cli#548).
func TestBuildAndPushPrefersDockerBuildxOverPodman(t *testing.T) {
	s := newStubs(t)
	s.installScript(t, devcontainerBin, "")
	withBuildx(t, s)
	withPodman(t, s)

	if err := buildAndPush(context.Background(), "/p", "", "img", "linux/amd64", false, io.Discard); err != nil {
		t.Fatalf("buildAndPush: %v", err)
	}
	build := s.calls(t, devcontainerBin)
	if anyBuildCall(build, "--docker-path") {
		t.Errorf("chose podman with buildx available: %v", build)
	}
	if !anyBuildCall(build, "--push") {
		t.Errorf("did not push in one step: %v", build)
	}
}

// A version-free answer fails the CLI's probe too, so --platform would be
// refused.
func TestPodmanWithoutAVersionIsNotABuildKitBuilder(t *testing.T) {
	s := newStubs(t)
	withoutBuildx(t, s)
	s.installScript(t, podmanBin, `case "$1 $2" in
  "buildx version") echo "unknown command" ;;
esac`)

	if hasBuildx(context.Background(), podmanBin) {
		t.Error("treated a version-free answer as BuildKit support")
	}
}

// --- read-configuration ---------------------------------------------------------

// fixture writes a project whose devcontainer.json has the given body.
func fixture(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".devcontainer"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, ".devcontainer", "devcontainer.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return dir
}

// requireRealCLI skips without the devcontainer CLI and stubs docker.
// The CLI's merged output is undocumented, so these tests read the real thing.
func requireRealCLI(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(devcontainerBin); err != nil {
		t.Skipf("%s is not on PATH", devcontainerBin)
	}

	host := os.Getenv("PATH")
	s := newStubs(t)
	// The host PATH follows the stub dir: the CLI is a node program and needs
	// node.
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+host)

	// With no inspect answer the CLI fetches image metadata from the registry,
	// which fails offline.
	s.installScript(t, dockerBin, `case "$1 $2" in
  "inspect --type")
    printf '%s' '[{"Id":"sha256:0000000000000000000000000000000000000000000000000000000000000000","Config":{"Env":[],"Labels":{},"User":""},"Architecture":"arm64","Os":"linux"}]'
    ;;
esac`)
}

func TestReadConfigurationAgainstTheRealCLI(t *testing.T) {
	requireRealCLI(t)

	dir := fixture(t, `{
	  "name": "fixture",
	  "image": "docker.io/library/alpine:3.20",
	  "remoteUser": "node",
	  "workspaceFolder": "/workspaces/fixture",
	  "containerEnv": { "FROM_CONTAINER_ENV": "1" },
	  "remoteEnv": { "FROM_REMOTE_ENV": "2" },
	  "onCreateCommand": "echo on",
	  "postCreateCommand": ["sh", "-c", "echo post"],
	  "postStartCommand": { "a": "echo a", "b": ["sh","-c","echo b"] }
	}`)

	dev, lc, err := readConfiguration(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("readConfiguration: %v", err)
	}

	if dev.WorkspaceFolder != "/workspaces/fixture" {
		t.Errorf("WorkspaceFolder = %q", dev.WorkspaceFolder)
	}
	if dev.RemoteUser != "node" {
		t.Errorf("RemoteUser = %q", dev.RemoteUser)
	}
	if dev.ContainerEnv["FROM_CONTAINER_ENV"] != "1" {
		t.Errorf("containerEnv did not survive: %v", dev.ContainerEnv)
	}
	if dev.ContainerEnv["FROM_REMOTE_ENV"] != "2" {
		t.Errorf("remoteEnv did not survive: %v", dev.ContainerEnv)
	}

	if len(lc.OnCreate) != 1 || !strings.Contains(string(lc.OnCreate[0]), "echo on") {
		t.Errorf("OnCreate = %v", lc.OnCreate)
	}
	if len(lc.PostCreate) != 1 || !strings.Contains(string(lc.PostCreate[0]), "echo post") {
		t.Errorf("PostCreate = %v", lc.PostCreate)
	}
	if len(lc.PostStart) != 1 || !strings.Contains(string(lc.PostStart[0]), "echo a") {
		t.Errorf("PostStart = %v", lc.PostStart)
	}
}

// The CLI reports workspaceFolder as null when unset, so this checks its own
// default.
func TestReadConfigurationDefaultsTheWorkspaceFolder(t *testing.T) {
	requireRealCLI(t)

	dir := fixture(t, `{"name":"nows","image":"docker.io/library/alpine:3.20"}`)

	dev, _, err := readConfiguration(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("readConfiguration: %v", err)
	}
	want := "/workspaces/" + filepath.Base(dir)
	if dev.WorkspaceFolder != want {
		t.Errorf("WorkspaceFolder = %q, want the CLI's own default %q", dev.WorkspaceFolder, want)
	}
}

// The CLI runs `docker ps` even to read a configuration; name docker rather
// than relay ENOENT.
func TestReadConfigurationNamesMissingDocker(t *testing.T) {
	s := newStubs(t)
	s.install(t, devcontainerBin, "{}", 0) // present, so docker is what is missing

	_, _, err := readConfiguration(context.Background(), "/projects/api", "")
	if err == nil {
		t.Fatal("readConfiguration succeeded with no docker on PATH")
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Errorf("error %q does not name docker", err)
	}
}

func TestMergeEnvPrefersRemoteEnv(t *testing.T) {
	got := mergeEnv(
		map[string]string{"A": "container", "B": "container"},
		map[string]string{"A": "remote"},
	)
	if got["A"] != "remote" {
		t.Errorf("A = %q, want remoteEnv to win", got["A"])
	}
	if got["B"] != "container" {
		t.Errorf("B = %q, want the containerEnv value kept", got["B"])
	}
}

// podman exits 125 on an unauthenticated registry; the error must name the
// login command.
func TestPushRejectionNamesTheLoginCommand(t *testing.T) {
	s := newStubs(t)
	s.installScript(t, devcontainerBin, "")
	withoutBuildx(t, s)
	s.installScript(t, podmanBin, `case "$1 $2" in
  "buildx version") echo "podman version 5.3.1" ;;
  "push "*)
    echo 'Error: trying to reuse blob sha256:08bc at destination: Requesting bearer token: invalid status code from registry 403 (Forbidden)' >&2
    exit 125
    ;;
esac`)

	err := buildAndPush(context.Background(), "/p", "", "ghcr.io/duy0611/x:latest",
		"linux/"+hostArch, false, io.Discard)
	if err == nil {
		t.Fatal("buildAndPush succeeded against a registry that refused the push")
	}
	for _, want := range []string{"podman login", "ghcr.io"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestPushFailureQuotesTheRegistry(t *testing.T) {
	s := newStubs(t)
	s.installScript(t, devcontainerBin, "")
	withoutBuildx(t, s)
	s.installScript(t, podmanBin, `case "$1 $2" in
  "buildx version") echo "podman version 5.3.1" ;;
  "push "*) echo 'Error: name unknown: repository does not exist' >&2; exit 125 ;;
esac`)

	err := buildAndPush(context.Background(), "/p", "", "ghcr.io/duy0611/x:latest",
		"linux/"+hostArch, false, io.Discard)
	if err == nil {
		t.Fatal("buildAndPush succeeded against a failing push")
	}
	if strings.Contains(err.Error(), "login") {
		t.Errorf("error %q blames credentials for something else", err)
	}
	if !strings.Contains(err.Error(), "repository does not exist") {
		t.Errorf("error %q drops the registry's explanation", err)
	}
}

func TestRegistryHost(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/duy0611/x:latest":        "ghcr.io",
		"europe-docker.pkg.dev/p/dev/x:1": "europe-docker.pkg.dev",
		"localhost:5000/x":                "localhost:5000",
		"localhost/x":                     "localhost",
		// A Docker Hub namespace, not a host: `docker login` takes no argument.
		"duy0611/x:latest": "",
		"alpine":           "",
	}
	for image, want := range cases {
		if got := registryHost(image); got != want {
			t.Errorf("registryHost(%q) = %q, want %q", image, got, want)
		}
	}
}

// A generated configuration lives in a temp dir, so --config must be passed to
// both calls.
func TestReadConfigurationPassesTheConfigPath(t *testing.T) {
	s := newStubs(t)
	s.install(t, devcontainerBin, `{"configuration":{},"mergedConfiguration":{}}`, 0)
	s.install(t, dockerBin, "", 0)

	const cfg = "/tmp/dev-config-x/.devcontainer/devcontainer.json"
	if _, _, err := readConfiguration(context.Background(), "/projects/api", cfg); err != nil {
		t.Fatalf("readConfiguration: %v", err)
	}

	argv := s.argv(t, devcontainerBin)
	if !hasPair(argv, "--config", cfg) {
		t.Errorf("read-configuration argv %v is missing --config %s", argv, cfg)
	}
}

func TestBuildAndPushPassesTheConfigPath(t *testing.T) {
	s := newStubs(t)
	s.install(t, devcontainerBin, "", 0)
	withBuildx(t, s)

	const cfg = "/tmp/dev-config-x/.devcontainer/devcontainer.json"
	err := buildAndPush(context.Background(), "/projects/api", cfg,
		"reg.example/dev/ws-api:latest", "linux/amd64", false, io.Discard)
	if err != nil {
		t.Fatalf("buildAndPush: %v", err)
	}

	argv := s.argv(t, devcontainerBin)
	if !hasPair(argv, "--config", cfg) {
		t.Errorf("build argv %v is missing --config %s", argv, cfg)
	}
}
