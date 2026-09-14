//go:build smoke

package smoke

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The k8s smoke test needs a cluster and somewhere to push images, so it is
// gated on being told where both are. `make smoke` on a laptop with neither
// still runs the local one.
const (
	envContext   = "DEV_SMOKE_K8S_CONTEXT"
	envRegistry  = "DEV_SMOKE_REGISTRY"
	envNamespace = "DEV_SMOKE_K8S_NAMESPACE" // optional; defaults to "default"
)

// postCreate writes a file the test looks for, which is how "the lifecycle
// commands ran" is checked rather than assumed. It writes into the home
// directory on purpose: that is the half of the volume whose persistence is
// easy to get wrong.
const k8sFixtureConfig = `{
  "name": "dev-smoke-k8s",
  "image": "docker.io/library/alpine:3.20",
  "postCreateCommand": "touch $HOME/post-create-ran"
}`

const (
	k8sWorkspace = "dev-smoke-k8s-ws"
	k8sContainer = "dev-smoke-k8s"
	k8sProvider  = "dev-smoke-k8s-provider"
)

func TestK8sSmoke(t *testing.T) {
	kubeContext := os.Getenv(envContext)
	registry := os.Getenv(envRegistry)
	if kubeContext == "" || registry == "" {
		t.Skipf("set %s and %s to run the Kubernetes smoke test", envContext, envRegistry)
	}
	namespace := os.Getenv(envNamespace)
	if namespace == "" {
		namespace = "default"
	}
	// docker, because the image is still built on the host by the devcontainer
	// CLI; kubectl, because everything after that is a kubectl call.
	requireBinaries(t, "devcontainer", "docker", "kubectl")

	t.Setenv("DEV_STATE", t.TempDir())
	bin := buildBinary(t)
	project := k8sFixtureProject(t)

	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}
	kubectl := func(args ...string) string {
		t.Helper()
		full := append([]string{"--context", kubeContext, "--namespace", namespace}, args...)
		out, err := exec.Command("kubectl", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("kubectl %s: %v\n%s", strings.Join(full, " "), err, out)
		}
		return string(out)
	}

	// Even a failed assertion must not leave a Deployment and a volume behind.
	t.Cleanup(func() {
		_ = exec.Command(bin, "container", "remove", k8sContainer, "--force").Run()
	})

	dev("provider", "configure", k8sProvider,
		"--kind", "k8s",
		"--context", kubeContext,
		"--namespace", namespace,
		"--registry", registry)
	dev("workspace", "init", k8sWorkspace, "--provider", k8sProvider)
	dev("workspace", "set", "GREETING", "literal:hello")

	t.Log("creating; this builds an image, pushes it, and waits for a pod")
	dev("container", "create", k8sContainer, "--folder", project)

	// All three objects, found by the labels every lookup relies on.
	for _, kind := range []string{"deployment", "persistentvolumeclaim", "secret"} {
		out := kubectl("get", kind, "-l", "dev.container="+slugOf(k8sContainer), "-o", "name")
		if strings.TrimSpace(out) == "" {
			t.Errorf("no %s carries the dev.container label", kind)
		}
	}

	// Reaches the pod through the Secret's envFrom.
	if got := strings.TrimSpace(dev("container", "exec", k8sContainer, "--", "printenv", "GREETING")); got != "hello" {
		t.Errorf("GREETING = %q, want %q", got, "hello")
	}

	// The host folder was streamed into the volume on create.
	if out := dev("container", "exec", k8sContainer, "--", "ls"); !strings.Contains(out, "hello.txt") {
		t.Errorf("the synced file is missing from the workspace:\n%s", out)
	}

	// postCreate ran, and wrote into the home half of the volume.
	dev("container", "exec", k8sContainer, "--", "test", "-f", "/root/post-create-ran")

	dev("container", "stop", k8sContainer)
	if out := kubectl("get", "pods", "-l", "dev.container="+slugOf(k8sContainer), "-o", "name"); strings.TrimSpace(out) != "" {
		t.Errorf("pods survived a stop:\n%s", out)
	}

	dev("container", "start", k8sContainer)
	// The volume outlived the pod: this is what the second subPath buys, and
	// what makes "postCreate runs once" true rather than a claim.
	dev("container", "exec", k8sContainer, "--", "test", "-f", "/root/post-create-ran")
	dev("container", "exec", k8sContainer, "--", "test", "-f", "hello.txt")

	dev("container", "remove", k8sContainer)
	for _, kind := range []string{"deployment", "persistentvolumeclaim", "secret"} {
		out := kubectl("get", kind, "-l", "dev.container="+slugOf(k8sContainer), "-o", "name")
		if strings.TrimSpace(out) != "" {
			t.Errorf("remove left a %s behind:\n%s", kind, out)
		}
	}
}

func k8sFixtureProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".devcontainer"), 0o755); err != nil {
		t.Fatalf("creating the fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".devcontainer", "devcontainer.json"),
		[]byte(k8sFixtureConfig), 0o600); err != nil {
		t.Fatalf("writing the fixture config: %v", err)
	}
	// Something recognisable to look for on the other side of the sync.
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatalf("writing the fixture file: %v", err)
	}
	return dir
}

// slugOf mirrors the provider's label value for a container name.
//
// Duplicated rather than imported: this test drives the built binary from
// outside, and importing the package would let a wrong slug agree with itself.
// Both names here are already lowercase and hyphenated, so only the hash is
// interesting.
func slugOf(name string) string {
	out, err := exec.Command("sh", "-c",
		`printf '%s' `+shellQuote(name)+` | sha256sum | cut -c1-6`).Output()
	if err != nil {
		return name
	}
	return name + "-" + strings.TrimSpace(string(out))
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
