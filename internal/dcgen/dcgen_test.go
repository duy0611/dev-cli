package dcgen

import (
	"slices"
	"strings"
	"testing"
)

// The catalog is the user-visible contract of `dev container tools`, and a
// typo in a feature reference only shows up as a failed image build minutes
// later. These assert the shape, not the taste.
func TestCatalogEntriesAreWellFormed(t *testing.T) {
	for _, tool := range Catalog() {
		if tool.ID == "" || tool.Summary == "" {
			t.Errorf("%+v: every entry needs an id and a summary", tool)
		}
		if (tool.Feature == "") == (tool.Apt == "") {
			t.Errorf("%s: exactly one of Feature and Apt must be set", tool.ID)
		}
		if tool.Apt != "" && tool.Options != nil {
			t.Errorf("%s: an apt tool has nowhere to put options", tool.ID)
		}
	}
}

func TestLookupFindsAndRejects(t *testing.T) {
	if _, ok := Lookup("node"); !ok {
		t.Error("Lookup(node) found nothing")
	}
	if _, ok := Lookup("nope"); ok {
		t.Error("Lookup(nope) found something")
	}
}

// Sorted, because it is printed as a list and the order must not depend on
// Go's map iteration.
func TestCatalogIsSorted(t *testing.T) {
	got := Catalog()
	for i := 1; i < len(got); i++ {
		if got[i-1].ID >= got[i].ID {
			t.Fatalf("catalog is not sorted: %s before %s", got[i-1].ID, got[i].ID)
		}
	}
}

// opencode installs through an npm feature, so an image without a node runtime
// fails at install time rather than at render time.
func TestOpencodeDependsOnNode(t *testing.T) {
	tool, ok := Lookup("opencode")
	if !ok {
		t.Fatal("no opencode in the catalog")
	}
	if len(tool.Requires) != 1 || tool.Requires[0] != "node" {
		t.Errorf("opencode.Requires = %v, want [node]", tool.Requires)
	}
}

// The stored string is compared and committed, so it has to be byte-stable:
// sorted keys, two-space indent, one trailing newline.
func TestRenderIsExactAndStable(t *testing.T) {
	got, err := Render("demo", []string{"node", "yq"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	want := `{
  "features": {
    "ghcr.io/devcontainers-extra/features/apt-get-packages:1": {
      "packages": "yq"
    },
    "ghcr.io/devcontainers/features/node:1": {}
  },
  "image": "mcr.microsoft.com/devcontainers/base:1-ubuntu-24.04",
  "name": "demo",
  "remoteUser": "vscode"
}
`
	if got != want {
		t.Errorf("Render =\n%s\nwant\n%s", got, want)
	}

	again, err := Render("demo", []string{"yq", "node"})
	if err != nil {
		t.Fatalf("Render again: %v", err)
	}
	if again != got {
		t.Error("Render depends on the order of its input")
	}
}

// A bare Ubuntu box is a reasonable thing to ask for, and an empty features
// object would be noise in the stored document.
func TestRenderWithNoToolsOmitsFeatures(t *testing.T) {
	got, err := Render("bare", nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(got, "features") {
		t.Errorf("Render with no tools emitted a features key:\n%s", got)
	}
}

// kubectl and helm come from one feature. Emitting it twice is impossible in a
// JSON object, so the failure mode is the second overwriting the first and
// quietly disabling a tool the operator asked for.
func TestKubectlAndHelmShareOneFeature(t *testing.T) {
	both, err := Render("k", []string{"kubectl", "helm"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Count(both, kubeFeature) != 1 {
		t.Errorf("the shared feature appears more than once:\n%s", both)
	}
	if strings.Contains(both, `"helm": "none"`) {
		t.Error("helm was asked for and disabled")
	}

	// Asking for one must not install the other: the feature defaults all
	// three on, so the ones not selected have to be turned off explicitly.
	only, err := Render("k", []string{"kubectl"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(only, `"helm": "none"`) {
		t.Errorf("kubectl alone installed helm too:\n%s", only)
	}
	if !strings.Contains(only, `"minikube": "none"`) {
		t.Errorf("minikube is never asked for and must be off:\n%s", only)
	}
}

func TestRenderRejectsAnUnknownTool(t *testing.T) {
	if _, err := Render("x", []string{"node", "kubctl"}); err == nil {
		t.Fatal("Render accepted a misspelled tool")
	} else if !strings.Contains(err.Error(), "kubctl") {
		t.Errorf("error %q does not name the offending id", err)
	}
}

func TestResolveAddsRequirements(t *testing.T) {
	got, err := Resolve([]string{"opencode"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := []string{"node", "opencode"}
	if !slices.Equal(got, want) {
		t.Errorf("Resolve = %v, want %v", got, want)
	}
}

func TestResolveDeduplicates(t *testing.T) {
	got, err := Resolve([]string{"yq", "yq", "node"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !slices.Equal(got, []string{"node", "yq"}) {
		t.Errorf("Resolve = %v, want [node yq]", got)
	}
}

// The tool set is derived from the stored document rather than stored beside
// it, so `rebuild --tools +x` has to be able to read back what it wrote.
func TestToolsOfRoundTrips(t *testing.T) {
	want := []string{"claude-code", "helm", "kubectl", "node", "opencode", "yq"}
	cfg, err := Render("demo", want)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got, err := ToolsOf(cfg)
	if err != nil {
		t.Fatalf("ToolsOf: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("ToolsOf = %v, want %v", got, want)
	}
}

// A hand-edited document is still a document dev has to work with, so an
// unrecognised feature is ignored rather than treated as an error.
func TestToolsOfIgnoresUnknownFeatures(t *testing.T) {
	const cfg = `{
  "features": {
    "ghcr.io/someone/else:1": {},
    "ghcr.io/devcontainers/features/node:1": {}
  }
}`
	got, err := ToolsOf(cfg)
	if err != nil {
		t.Fatalf("ToolsOf: %v", err)
	}
	if !slices.Equal(got, []string{"node"}) {
		t.Errorf("ToolsOf = %v, want [node]", got)
	}
}

func TestToolsOfRejectsBrokenJSON(t *testing.T) {
	if _, err := ToolsOf("{not json"); err == nil {
		t.Fatal("ToolsOf accepted a document that is not JSON")
	}
}
