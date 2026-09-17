package cli

import (
	"bytes"
	"strings"
	"testing"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/model"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/provider/k8s"
)

// newTestApp returns an app backed by a throwaway database.
//
// DEV_STATE is what store.OpenDefault honours, so a test never touches the
// operator's own state. Stdin under `go test` is /dev/null, which isTerminal
// rejects, so these exercise the scripted path and never block on a prompt.
func newTestApp(t *testing.T) (*app, *bytes.Buffer) {
	t.Helper()
	t.Setenv("DEV_STATE", t.TempDir())

	var out bytes.Buffer
	a := &app{out: &out}
	t.Cleanup(a.close)
	return a, &out
}

func TestConfigureRefusesAKindChange(t *testing.T) {
	a, _ := newTestApp(t)

	if err := runProviderConfigure(a, "prod", "local", k8s.Config{}); err != nil {
		t.Fatalf("first configure: %v", err)
	}

	err := runProviderConfigure(a, "prod", "k8s", k8s.Config{Registry: "ghcr.io/me"})
	if err == nil {
		t.Fatal("configure changed the kind of an existing provider")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
	// The way out has to be in the message: there is no --force, so an
	// operator who is not told to remove it has nothing to try.
	if !strings.Contains(err.Error(), "dev provider remove prod") {
		t.Errorf("error %q does not say how to change the kind", err)
	}

	// Refused means unchanged, not half-applied.
	st, err := a.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	p, err := st.GetProvider("prod")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if p.Kind != model.KindLocal {
		t.Errorf("kind = %q, want local", p.Kind)
	}
}

func TestConfigureIsIdempotentForTheSameKind(t *testing.T) {
	a, _ := newTestApp(t)

	for i := range 2 {
		if err := runProviderConfigure(a, "prod", "local", k8s.Config{}); err != nil {
			t.Fatalf("configure %d: %v", i, err)
		}
	}
}

func TestConfigureRejectsAnUnknownKind(t *testing.T) {
	a, _ := newTestApp(t)

	// Checked before the existing provider is consulted, or correcting a typo
	// would be reported as a kind change rather than as a bad kind.
	if err := runProviderConfigure(a, "prod", "local", k8s.Config{}); err != nil {
		t.Fatalf("first configure: %v", err)
	}
	err := runProviderConfigure(a, "prod", "kubernetes", k8s.Config{})
	if err == nil {
		t.Fatal("configure accepted an unknown kind")
	}
	if !strings.Contains(err.Error(), "unknown provider kind") {
		t.Errorf("error = %q, want it to name the unknown kind", err)
	}
}

func TestConfigureKeepsUnflaggedK8sSettings(t *testing.T) {
	a, _ := newTestApp(t)

	first := k8s.Config{
		Context:         "prod-cluster",
		Namespace:       "sandboxes",
		Registry:        "ghcr.io/me",
		Platform:        "linux/arm64",
		StorageSize:     "50Gi",
		ImagePullSecret: "ghcr",
	}
	if err := runProviderConfigure(a, "prod", "k8s", first); err != nil {
		t.Fatalf("first configure: %v", err)
	}

	// One setting changed from a script. Everything else was configured once
	// and must survive: re-stating all eight to move one is how a registry
	// gets dropped.
	if err := runProviderConfigure(a, "prod", "k8s", k8s.Config{StorageSize: "80Gi"}); err != nil {
		t.Fatalf("second configure: %v", err)
	}

	got := storedK8sConfig(t, a, "prod")
	want := first
	want.StorageSize = "80Gi"
	if got != want {
		t.Errorf("config = %+v, want %+v", got, want)
	}
}

func TestConfigureClearsAnOptionalSettingOnADash(t *testing.T) {
	a, _ := newTestApp(t)

	if err := runProviderConfigure(a, "prod", "k8s", k8s.Config{
		Registry:        "ghcr.io/me",
		ImagePullSecret: "ghcr",
	}); err != nil {
		t.Fatalf("first configure: %v", err)
	}

	// An empty flag is indistinguishable from an absent one now that absent
	// means "keep", so without a marker a pull secret could never be removed.
	if err := runProviderConfigure(a, "prod", "k8s", k8s.Config{ImagePullSecret: "-"}); err != nil {
		t.Fatalf("second configure: %v", err)
	}
	if got := storedK8sConfig(t, a, "prod").ImagePullSecret; got != "" {
		t.Errorf("imagePullSecret = %q, want it cleared", got)
	}
}

func TestPromptK8sDefaultsToTheStoredConfig(t *testing.T) {
	stored := k8s.Config{
		Context:         "prod-cluster",
		Namespace:       "sandboxes",
		Registry:        "ghcr.io/me",
		Platform:        "linux/arm64",
		StorageSize:     "50Gi",
		StorageClass:    "fast",
		ServiceAccount:  "builder",
		ImagePullSecret: "ghcr",
	}

	// Return pressed at every question. Re-running configure to look at the
	// settings must not rewrite them, and the hardcoded defaults must not win
	// over what this provider was configured with.
	p := newPrompter(strings.NewReader(strings.Repeat("\n", 8)), &bytes.Buffer{})
	got, err := promptK8s(t.Context(), p, k8s.Config{}, stored)
	if err != nil {
		t.Fatalf("promptK8s: %v", err)
	}
	if got != stored {
		t.Errorf("config = %+v, want it unchanged: %+v", got, stored)
	}
}

func TestPromptK8sPrefersAFlagOverTheStoredValue(t *testing.T) {
	stored := k8s.Config{
		Context:     "prod-cluster",
		Namespace:   "sandboxes",
		Registry:    "ghcr.io/me",
		Platform:    "linux/arm64",
		StorageSize: "50Gi",
	}

	var out bytes.Buffer
	p := newPrompter(strings.NewReader(strings.Repeat("\n", 8)), &out)
	got, err := promptK8s(t.Context(), p, k8s.Config{Registry: "ghcr.io/other"}, stored)
	if err != nil {
		t.Fatalf("promptK8s: %v", err)
	}
	if got.Registry != "ghcr.io/other" {
		t.Errorf("registry = %q, want the flag to win", got.Registry)
	}
	// A flag is an answer already; asking about it anyway makes the flags
	// pointless.
	if strings.Contains(out.String(), "registry") {
		t.Errorf("prompted for a setting the flag supplied: %q", out.String())
	}
}

func TestPromptK8sClearsAnOptionalSettingOnADash(t *testing.T) {
	stored := k8s.Config{
		Namespace:       "sandboxes",
		Registry:        "ghcr.io/me",
		Platform:        "linux/arm64",
		StorageSize:     "50Gi",
		ImagePullSecret: "ghcr",
	}

	// Context, namespace, registry, platform, size, class, account, secret.
	answers := "\n\n\n\n\n\n\n-\n"
	p := newPrompter(strings.NewReader(answers), &bytes.Buffer{})
	got, err := promptK8s(t.Context(), p, k8s.Config{}, stored)
	if err != nil {
		t.Fatalf("promptK8s: %v", err)
	}
	if got.ImagePullSecret != "" {
		t.Errorf("imagePullSecret = %q, want it cleared", got.ImagePullSecret)
	}
}

func storedK8sConfig(t *testing.T, a *app, name string) k8s.Config {
	t.Helper()
	st, err := a.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	p, err := st.GetProvider(name)
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	cfg, err := k8s.ParseConfig(p.Config)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	return cfg
}
