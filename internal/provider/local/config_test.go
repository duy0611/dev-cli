package local

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

var _ provider.ConfigReader = (*Provider)(nil)

func TestMergedConfigAsksForTheMergedFormWithTheOverride(t *testing.T) {
	f := newFakePath(t)
	f.install(t, "devcontainer", `{"configuration":{},"mergedConfiguration":{"privileged":true}}`, 0)

	c := model.Container{Name: "api", Source: "/src/api",
		ConfigPath: "/src/api/.devcontainer/devcontainer.json", OverrideConfigPath: "/tmp/o.json"}
	merged, compose, err := (&Provider{}).MergedConfig(t.Context(), c)
	if err != nil {
		t.Fatalf("MergedConfig: %v", err)
	}
	if string(merged) != `{"privileged":true}` {
		t.Errorf("merged = %s", merged)
	}
	if compose != nil {
		t.Errorf("compose = %v, want none", compose)
	}

	argv := f.argv(t, "devcontainer")
	for _, want := range []string{"read-configuration", "--include-merged-configuration",
		"--config", "--override-config", "/tmp/o.json"} {
		if !slices.Contains(argv, want) {
			t.Errorf("argv %v lacks %q", argv, want)
		}
	}
	// No id label: the CLI would find the existing container and report the
	// merged form of the image being replaced, not the one about to be built.
	if slices.Contains(argv, "--id-label") {
		t.Errorf("argv %v names a container; it must read the configuration afresh", argv)
	}
}

func TestMergedConfigResolvesComposeFilesAgainstTheConfigDirectory(t *testing.T) {
	f := newFakePath(t)
	f.install(t, "devcontainer",
		`{"mergedConfiguration":{"dockerComposeFile":["../compose.yml","/abs/other.yml"]}}`, 0)

	c := model.Container{Name: "api", Source: "/src/api",
		ConfigPath: "/src/api/.devcontainer/devcontainer.json"}
	_, compose, err := (&Provider{}).MergedConfig(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Clean("/src/api/compose.yml"), "/abs/other.yml"}
	if !slices.Equal(compose, want) {
		t.Errorf("compose = %v, want %v", compose, want)
	}
}

func TestMergedConfigFailureQuotesTheCLI(t *testing.T) {
	f := newFakePath(t)
	f.install(t, "devcontainer", "", 1)
	_, _, err := (&Provider{}).MergedConfig(t.Context(), model.Container{Name: "api", Source: "/s"})
	if err == nil || !strings.Contains(err.Error(), "container api") {
		t.Errorf("err = %v", err)
	}
}

// Silence in the merged field is a configuration the guard cannot check, not
// one with nothing to report.
func TestMergedConfigWithNoMergedFieldIsAnError(t *testing.T) {
	f := newFakePath(t)
	f.install(t, "devcontainer", `{"configuration":{}}`, 0)
	if _, _, err := (&Provider{}).MergedConfig(t.Context(), model.Container{Name: "api", Source: "/s"}); err == nil {
		t.Error("no merged configuration was accepted")
	}
}
