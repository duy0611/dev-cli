package dcgen

import (
	"encoding/json"
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
	got, err := Render("demo", []string{"node", "yq"}, Mount{}, State{})
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

	again, err := Render("demo", []string{"yq", "node"}, Mount{}, State{})
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
	got, err := Render("bare", nil, Mount{}, State{})
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
	both, err := Render("k", []string{"kubectl", "helm"}, Mount{}, State{})
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
	only, err := Render("k", []string{"kubectl"}, Mount{}, State{})
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
	if _, err := Render("x", []string{"node", "kubctl"}, Mount{}, State{}); err == nil {
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
	cfg, err := Render("demo", want, Mount{}, State{})
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

// A folderless container has no host directory to bind-mount, so the document
// has to name a volume and the path it appears at.
//
// workspaceFolder is not optional here. Left unset, the CLI derives one from
// the basename of --workspace-folder, which for a folderless container is a
// per-invocation temporary directory: the in-container path would change on
// every command, and on k8s the PVC would mount somewhere new each time.
//
// postCreateCommand chowns the mount to the remote user. Docker creates a
// named volume owned by root, and unlike a bind mount — which the devcontainer
// CLI UID-remaps to the host user automatically — a fresh volume gets no such
// fixup, so the first write from vscode fails with EACCES.
func TestRenderWithAVolumeNamesBothFields(t *testing.T) {
	got, err := Render("scratch", nil, Mount{Volume: "dev-ws-scratch", Folder: "/workspaces/scratch"}, State{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	want := `{
  "image": "mcr.microsoft.com/devcontainers/base:1-ubuntu-24.04",
  "name": "scratch",
  "postCreateCommand": "sudo chown vscode:vscode /workspaces/scratch",
  "remoteUser": "vscode",
  "workspaceFolder": "/workspaces/scratch",
  "workspaceMount": "source=dev-ws-scratch,target=/workspaces/scratch,type=volume"
}
`
	if got != want {
		t.Errorf("Render =\n%s\nwant\n%s", got, want)
	}
}

// The zero Mount is a folder-backed container, where the CLI's own default
// bind mount is exactly what is wanted. Emitting either field there would
// override that bind mount with nothing, and the bind mount needs no chown:
// the CLI already UID-remaps it to the host user.
func TestRenderWithoutAVolumeOmitsBothFields(t *testing.T) {
	got, err := Render("demo", nil, Mount{}, State{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, key := range []string{"workspaceMount", "workspaceFolder", "postCreateCommand"} {
		if strings.Contains(got, key) {
			t.Errorf("a folder container's document carries %s:\n%s", key, got)
		}
	}
}

// rebuild --tools re-renders from scratch. A round trip that drops the mount
// would leave the next up creating a fresh empty workspace in the container
// filesystem, with the volume still there and no longer referenced.
func TestToolsOfSurvivesAVolumeDocument(t *testing.T) {
	cfg, err := Render("scratch", []string{"yq"}, Mount{Volume: "dev-ws-scratch", Folder: "/workspaces/scratch"}, State{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got, err := ToolsOf(cfg)
	if err != nil {
		t.Fatalf("ToolsOf: %v", err)
	}
	if !slices.Equal(got, []string{"yq"}) {
		t.Errorf("ToolsOf = %v, want [yq]", got)
	}
}

// A container that persists state names the volume, points every agent at it,
// and chowns it — a volume is root-owned on creation exactly as the workspace
// one is.
func TestRenderWithStateNamesMountAndEnv(t *testing.T) {
	got, err := Render("api", nil, Mount{}, State{Volume: "dev-ws-api-state"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	want := `{
  "containerEnv": {
    "CLAUDE_CONFIG_DIR": "/var/dev-state/claude",
    "CODEX_HOME": "/var/dev-state/codex",
    "GIT_CONFIG_GLOBAL": "/var/dev-state/gitconfig",
    "HERMES_HOME": "/var/dev-state/hermes"
  },
  "image": "mcr.microsoft.com/devcontainers/base:1-ubuntu-24.04",
  "mounts": [
    "source=dev-ws-api-state,target=/var/dev-state,type=volume"
  ],
  "name": "api",
  "postCreateCommand": "sudo chown vscode:vscode /var/dev-state",
  "remoteUser": "vscode"
}
`
	if got != want {
		t.Errorf("Render =\n%s\nwant\n%s", got, want)
	}
}

// The zero State is a container that does not persist anything. A stray mounts
// or containerEnv key there would create a volume nobody asked for and nothing
// would ever remove it, since Remove only looks when the column says to.
func TestRenderWithoutStateOmitsMountAndEnv(t *testing.T) {
	got, err := Render("demo", nil, Mount{}, State{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, key := range []string{"mounts", "containerEnv", "dev-state"} {
		if strings.Contains(got, key) {
			t.Errorf("a container with no state carries %s:\n%s", key, got)
		}
	}
}

// postCreateCommand is a single string, and a folderless container that also
// persists state has two directories to claim. Assigning the key twice would
// keep only the last, and whichever volume lost would be unwritable — the
// failure arrives as EACCES from the agent, far from this line.
func TestRenderChownsBothVolumes(t *testing.T) {
	got, err := Render("scratch", nil,
		Mount{Volume: "dev-ws-scratch", Folder: "/workspaces/scratch"},
		State{Volume: "dev-ws-scratch-state"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	var doc struct {
		PostCreate string `json:"postCreateCommand"`
	}
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("unmarshalling the rendered document: %v", err)
	}
	for _, dir := range []string{"/workspaces/scratch", StateDir} {
		if !strings.Contains(doc.PostCreate, dir) {
			t.Errorf("postCreateCommand %q does not chown %s", doc.PostCreate, dir)
		}
	}
	// && rather than ;: a chown that fails should stop there rather than be
	// hidden by the next one succeeding.
	if !strings.Contains(doc.PostCreate, " && ") {
		t.Errorf("postCreateCommand %q does not join its commands with &&", doc.PostCreate)
	}
}

// The reverse map reads features and must not be confused by the new keys, or
// `rebuild --tools +x` on a state-persisting container would drop every tool it
// already had.
func TestToolsOfSurvivesAStateDocument(t *testing.T) {
	cfg, err := Render("api", []string{"yq"}, Mount{}, State{Volume: "dev-ws-api-state"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got, err := ToolsOf(cfg)
	if err != nil {
		t.Fatalf("ToolsOf: %v", err)
	}
	if !slices.Equal(got, []string{"yq"}) {
		t.Errorf("ToolsOf = %v, want [yq]", got)
	}
}

// codex is in the agent registry, so a generated container must be able to
// install it. A catalog entry is only half-wired until ToolsOf maps it back:
// without that, `rebuild --tools +yq` silently drops codex from a container
// that had it.
func TestCodexRoundTrips(t *testing.T) {
	cfg, err := Render("api", []string{"codex"}, Mount{}, State{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got, err := ToolsOf(cfg)
	if err != nil {
		t.Fatalf("ToolsOf: %v", err)
	}
	if !slices.Equal(got, []string{"codex"}) {
		t.Errorf("ToolsOf = %v, want [codex]", got)
	}
}
