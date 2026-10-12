//go:build smoke

package smoke

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The k8s smoke test needs a cluster and a registry, named by env vars; without
// both it skips.
const (
	envContext   = "DEV_SMOKE_K8S_CONTEXT"
	envRegistry  = "DEV_SMOKE_REGISTRY"
	envNamespace = "DEV_SMOKE_K8S_NAMESPACE" // optional; defaults to "default"
	// Optional; needed only for a private pushed image, else pods sit in
	// ImagePullBackOff.
	envPullSecret = "DEV_SMOKE_K8S_PULL_SECRET"
)

// postCreate writes into the home directory, the half of the PVC whose
// persistence is easy to get wrong.
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
	// The image is built on the host; kubectl does the rest.
	requireBinaries(t, "devcontainer", "kubectl")
	requireAnyBinary(t, "docker", "podman")

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

	// Runs even on assertion failure, so no Deployment or volume is left
	// behind.
	t.Cleanup(func() {
		_ = exec.Command(bin, "container", "remove", k8sContainer, "--force").Run()
	})

	configure := []string{"provider", "configure", k8sProvider,
		"--kind", "k8s",
		"--context", kubeContext,
		"--namespace", namespace,
		"--registry", registry,
	}
	// Passed only when set; an empty value would reference a secret named "".
	if pullSecret := os.Getenv(envPullSecret); pullSecret != "" {
		configure = append(configure, "--image-pull-secret", pullSecret)
	}
	dev(configure...)
	dev("workspace", "init", k8sWorkspace, "--provider", k8sProvider)
	dev("workspace", "set", "GREETING", "literal:hello")

	t.Log("creating; this builds an image, pushes it, and waits for a pod")
	dev("container", "create", k8sContainer, "--folder", project)

	// All three objects must carry the dev.container label.
	for _, kind := range []string{"deployment", "persistentvolumeclaim", "secret"} {
		out := kubectl("get", kind, "-l", "dev.container="+slugOf(k8sContainer), "-o", "name")
		if strings.TrimSpace(out) == "" {
			t.Errorf("no %s carries the dev.container label", kind)
		}
	}

	// Reaches the pod via the Secret's envFrom.
	if got := strings.TrimSpace(dev("container", "exec", k8sContainer, "--", "printenv", "GREETING")); got != "hello" {
		t.Errorf("GREETING = %q, want %q", got, "hello")
	}

	// The host folder was copied into the volume on create.
	if out := dev("container", "exec", k8sContainer, "--", "ls"); !strings.Contains(out, "hello.txt") {
		t.Errorf("the seeded file is missing from the workspace:\n%s", out)
	}

	// Read the Secret directly: `exec printenv` injects the value per
	// invocation and would pass even if sync did nothing.
	podBefore := kubectl("get", "pods", "-l", "dev.container="+slugOf(k8sContainer), "-o", "name")
	dev("workspace", "set", "GREETING", "literal:rotated")
	dev("container", "sync", k8sContainer)
	if got := secretValue(t, kubectl, "GREETING"); got != "rotated" {
		t.Errorf("the Secret carries GREETING = %q after a sync, want %q", got, "rotated")
	}
	// The same pod must survive sync; a restart would destroy an agent's
	// in-progress work.
	if podAfter := kubectl("get", "pods", "-l", "dev.container="+slugOf(k8sContainer), "-o", "name"); podAfter != podBefore {
		t.Errorf("sync replaced the pod: %q became %q", strings.TrimSpace(podBefore), strings.TrimSpace(podAfter))
	}

	// postCreate ran and wrote into the home subPath.
	dev("container", "exec", k8sContainer, "--", "test", "-f", "/root/post-create-ran")

	dev("container", "stop", k8sContainer)
	if out := kubectl("get", "pods", "-l", "dev.container="+slugOf(k8sContainer), "-o", "name"); strings.TrimSpace(out) != "" {
		t.Errorf("pods survived a stop:\n%s", out)
	}

	dev("container", "start", k8sContainer)
	// The volume outlived the pod, so postCreate is not re-run on start.
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
	// A file to find after the seed.
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatalf("writing the fixture file: %v", err)
	}
	return dir
}

// secretValue returns one decoded key from the container's Secret.
func secretValue(t *testing.T, kubectl func(...string) string, key string) string {
	t.Helper()
	out := kubectl("get", "secret", "-l", "dev.container="+slugOf(k8sContainer),
		"-o", "jsonpath={.items[0].data."+key+"}")
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("the Secret's %s is not base64 (%q): %v", key, out, err)
	}
	return string(decoded)
}

// slugOf mirrors the provider's label value for a container name. It is
// reimplemented, not imported, so a wrong slug cannot agree with itself. It is
// computed in Go, since macOS has no sha256sum.
func slugOf(name string) string {
	sum := sha256.Sum256([]byte(name))
	return name + "-" + hex.EncodeToString(sum[:])[:6]
}
