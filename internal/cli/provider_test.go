package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider/k8s"
)

// newTestApp returns an app backed by a throwaway database.
func newTestApp(t *testing.T) (*app, *bytes.Buffer) {
	t.Helper()
	t.Setenv("DEV_STATE", t.TempDir())
	// Skips the local provider's platform probe, which would otherwise ask
	// whatever docker the host has, since most stubs here replace only
	// devcontainer.
	t.Setenv("DOCKER_DEFAULT_PLATFORM", "linux/amd64")

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
	if !strings.Contains(err.Error(), "dev provider remove prod") {
		t.Errorf("error %q does not say how to change the kind", err)
	}

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

	// Without a marker a pull secret could never be cleared: an empty flag and
	// an absent one are the same.
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
