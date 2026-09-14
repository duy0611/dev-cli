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

// withBuildx installs a docker whose `buildx version` succeeds.
func withBuildx(t *testing.T, s *stubs) {
	t.Helper()
	s.install(t, dockerBin, "", 0)
}

// withoutBuildx installs a docker that fails `buildx version`, as Podman and a
// bare docker CLI do, while still accepting a push.
func withoutBuildx(t *testing.T, s *stubs) {
	t.Helper()
	s.installScript(t, dockerBin, `case "$1" in
  buildx) exit 1 ;;
esac`)
}

func TestBuildAndPushPassesPlatformAndPush(t *testing.T) {
	s := newStubs(t)
	s.install(t, devcontainerBin, "", 0)
	withBuildx(t, s)

	err := buildAndPush(context.Background(), "/projects/api",
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
		// The host is arm64 and the nodes are not. Without this the pod
		// crash-loops with "exec format error".
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

	if err := buildAndPush(context.Background(), "/p", "img", "linux/amd64", true, io.Discard); err != nil {
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

	err := buildAndPush(context.Background(), "/p", "img", "linux/amd64", false, io.Discard)
	if err == nil {
		t.Fatal("buildAndPush succeeded against a failing CLI")
	}
	if !strings.Contains(err.Error(), "img") {
		t.Errorf("error %q does not name the image", err)
	}
}

// The CLI rejects --platform and --push outright without BuildKit, so without
// buildx the image is built locally and pushed with docker afterwards.
func TestBuildAndPushWithoutBuildx(t *testing.T) {
	s := newStubs(t)
	s.installScript(t, devcontainerBin, "")
	withoutBuildx(t, s)

	err := buildAndPush(context.Background(), "/p", "reg.example/x:latest",
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

// Producing the wrong architecture silently is the worst outcome available: the
// pod crash-loops with "exec format error", which says nothing about the build.
func TestBuildAndPushRefusesCrossArchWithoutBuildx(t *testing.T) {
	s := newStubs(t)
	s.installScript(t, devcontainerBin, "")
	withoutBuildx(t, s)

	other := "amd64"
	if hostArch == "amd64" {
		other = "arm64"
	}

	err := buildAndPush(context.Background(), "/p", "img", "linux/"+other, false, io.Discard)
	if err == nil {
		t.Fatal("buildAndPush built for the wrong architecture without complaint")
	}
	// The message has to carry both ways out, or it is just a refusal.
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

// requireRealCLI skips unless the devcontainer CLI is installed, and puts a
// stub docker in front of whatever else is on PATH.
//
// These tests read the CLI's real output rather than a recorded copy, because
// the shape is undocumented and is not what it looks like from the outside: the
// merged configuration uses plural names holding arrays, and reports a null
// workspaceFolder when the project does not set one.
//
// The stub keeps that hermetic. Reading a configuration makes the CLI run
// `docker ps` to look for an existing container, and "no container" is exactly
// the answer these tests want — so a docker that exits 0 saying nothing is
// better here than a real one.
func requireRealCLI(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(devcontainerBin); err != nil {
		t.Skipf("%s is not on PATH", devcontainerBin)
	}
	s := newStubs(t)
	s.install(t, dockerBin, "", 0)
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

	dev, lc, err := readConfiguration(context.Background(), dir)
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

	// Each of the three legal shapes reaches the right bucket, still encoded.
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

// The merged configuration reports workspaceFolder as null when the project
// does not set one, which is the common case.
func TestReadConfigurationDefaultsTheWorkspaceFolder(t *testing.T) {
	requireRealCLI(t)

	dir := fixture(t, `{"name":"nows","image":"docker.io/library/alpine:3.20"}`)

	dev, _, err := readConfiguration(context.Background(), dir)
	if err != nil {
		t.Fatalf("readConfiguration: %v", err)
	}
	want := "/workspaces/" + filepath.Base(dir)
	if dev.WorkspaceFolder != want {
		t.Errorf("WorkspaceFolder = %q, want the CLI's own default %q", dev.WorkspaceFolder, want)
	}
}

// The CLI shells out to `docker ps` even to read a configuration, and fails
// with "spawn docker ENOENT" otherwise. Naming the tool beats relaying that.
func TestReadConfigurationNamesMissingDocker(t *testing.T) {
	s := newStubs(t)
	s.install(t, devcontainerBin, "{}", 0) // present, so docker is what is missing

	_, _, err := readConfiguration(context.Background(), "/projects/api")
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
