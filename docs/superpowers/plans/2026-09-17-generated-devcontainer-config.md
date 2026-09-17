# Generated Devcontainer Configuration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `dev container create` work on a folder that ships no `.devcontainer` configuration, by generating a minimal Ubuntu one from a catalog of tools and storing it in the database.

**Architecture:** A new `internal/dcgen` package owns the tool catalog and renders a `devcontainer.json` string. That string is stored on the container row in a new `generated_config` column, never on disk. Before calling a provider, `internal/cli` materialises it to a temporary file, so every `model.Container` reaching a provider carries a real `ConfigPath` and both providers pass `--config` unconditionally with no knowledge of where it came from.

**Tech Stack:** Go 1.26, cobra, modernc.org/sqlite, `golang.org/x/term` (already direct dependencies — this plan adds no new module).

**Spec:** `docs/specs/2026-09-17-generated-devcontainer-config.md`

## Global Constraints

- **No new Go module dependencies.** The picker is hand-rolled on `golang.org/x/term`, already in `go.mod`.
- **Migrations are portable SQL.** No `AUTOINCREMENT`, no `datetime('now')` default, no SQLite-only affinity. The same file replays against Postgres.
- **Exit codes:** `2` malformed request, `3` named thing does not exist, `1` otherwise. Use `usageErrorf` / `notFoundErrorf` from `internal/cli/errors.go`, and `exactArgs`/`noArgs`/`minArgs` from `internal/cli/args.go` — never cobra's own `Args` validators.
- **Both `--id-label` values on every `up` and `exec`,** built only in `internal/provider/local/label.go`. Nothing in this plan changes that.
- **Resolve folders with `xpath.Resolve` before storing or mounting.** Unchanged by this plan; do not add a second path to storage.
- **`dev` never writes into a project folder.** A folder that ships its own configuration always wins.
- **Store tests use a temp file, never `:memory:`** — that database is per-connection and the pool hands migration and query to different connections. `t.Setenv("DEV_STATE", t.TempDir())` is the pattern.
- **Tests for external tools use stub executables on a temporary `PATH`.** See `internal/provider/local/local_test.go`.
- **Every non-obvious line carries a comment saying *why*,** usually naming the failure it prevents. Match the surrounding density — this codebase comments heavily and an uncommented workaround reads as removable.
- **No `Co-Authored-By` trailer** and no other tooling-attribution line on any commit.
- **Base image is exactly** `mcr.microsoft.com/devcontainers/base:1-ubuntu-24.04`.
- **Verify with** `make lint && make test` before every commit.

### Verified feature references

Every reference and option name below was read from the published feature metadata on `ghcr.io` on 2026-09-17. Do not substitute or "correct" them.

| Catalog id | Reference | Options |
|---|---|---|
| `node` | `ghcr.io/devcontainers/features/node:1` | none |
| `python` | `ghcr.io/devcontainers/features/python:1` | none |
| `gh` | `ghcr.io/devcontainers/features/github-cli:1` | none |
| `kubectl` | `ghcr.io/devcontainers/features/kubectl-helm-minikube:1` | see below |
| `helm` | the same reference | see below |
| `aws` | `ghcr.io/devcontainers/features/aws-cli:1` | none |
| `claude-code` | `ghcr.io/anthropics/devcontainer-features/claude-code:1` | none |
| `hermes` | `ghcr.io/devcontainer-community/devcontainer-features/hermes-agent.nousresearch.com:1` | none |
| `opencode` | `ghcr.io/devcontainers-extra/features/npm-package:1` | `{"package": "opencode-ai"}` |
| `gcloud` | `ghcr.io/dhoeric/features/google-cloud-cli:1` | none |
| `jq` | apt package `jq` | — |
| `yq` | apt package `yq` | — |

- `kubectl-helm-minikube` takes `version` (kubectl), `helm`, `minikube`, each a string whose value `"none"` skips that tool. `minikube` is always `"none"`; `helm` is `"none"` unless `helm` was selected; `version` is `"none"` unless `kubectl` was selected.
- `apt-get-packages` takes `packages` as a **comma-separated string**, not an array.
- `npm-package` takes `package` as a string, and needs a node runtime, so selecting `opencode` implies `node`.

## File Structure

**Created:**

- `internal/store/migrations/0002_generated_config.sql` — the column.
- `internal/dcgen/catalog.go` — the `Tool` type and the catalog table. No I/O.
- `internal/dcgen/render.go` — `Render` and `ToolsOf`, the two directions between a tool set and a JSON document.
- `internal/dcgen/dcgen_test.go` — golden render, round-trip, option edge cases.
- `internal/cli/picker.go` — `decodeKey`, `multiSelect`, and the raw-mode wrapper.
- `internal/cli/picker_test.go` — key decoding table, pipe-driven selection.
- `internal/cli/generate.go` — the create-time generate flow: parsing `--tools`, the guards, the prompt, and the `+`/`-` diff parser. Kept out of `container.go`, which is already 515 lines.
- `internal/cli/generate_test.go`
- `internal/cli/config.go` — `dev container tools` and `dev container config show`.

**Modified:**

- `internal/model/model.go` — add `GeneratedConfig` to `Container`.
- `internal/store/container.go` — the new column in insert, both selects, and `scanContainer`.
- `internal/store/container_test.go` (or the existing store test file) — round-trip and cascade.
- `internal/cli/container.go` — the create flow's `ErrNoConfig` branch, `--generate`/`--tools` flags on `create` and `rebuild`, the long help.
- `internal/cli/resolve.go` — the materialise helper and its use in `resolve`.
- `internal/provider/local/local.go` — `--config` on `up` and `exec`.
- `internal/provider/k8s/build.go` — `--config` on `read-configuration` and `build`.
- `internal/provider/k8s/k8s.go`, `internal/provider/k8s/sync.go` — pass the config path through to `readConfiguration`.
- `test/smoke/smoke_test.go` — a generated-container case.
- `CLAUDE.md` — invariant 9.

---

### Task 1: Settle `--config` versus `--override-config`

The spec's one unverified assumption. Everything downstream depends on it, so it goes first and is a spike, not a feature: the deliverable is an answer recorded in the spec.

**Files:**
- Modify: `docs/specs/2026-09-17-generated-devcontainer-config.md` (the "Unverified" section)

- [ ] **Step 1: Check for a usable engine**

```bash
which devcontainer docker podman
```

If `devcontainer` and one engine are present, continue. If not, **stop and report to the user**: this task cannot be completed on this machine, and the remaining tasks proceed on the spec's assumption that `--config` works. Do not guess.

- [ ] **Step 2: Build the probe**

```bash
cd "$(mktemp -d)" && mkdir proj cfg
cat > cfg/devcontainer.json <<'EOF'
{"name":"probe","image":"docker.io/library/alpine:3.20"}
EOF
pwd
```

- [ ] **Step 3: Run `up` with `--config` against the folder that has no configuration**

```bash
devcontainer up --workspace-folder ./proj --config ./cfg/devcontainer.json \
  --id-label dev.probe=1 --id-label dev.probe.name=probe
```

Expected on success: the CLI builds/pulls and reports a running container.
Expected on failure: an error saying a `devcontainer.json` could not be found.

- [ ] **Step 4: If step 3 failed, run the same thing with `--override-config`**

```bash
devcontainer up --workspace-folder ./proj --override-config ./cfg/devcontainer.json \
  --id-label dev.probe=1 --id-label dev.probe.name=probe
```

- [ ] **Step 5: Clean up**

```bash
docker rm -f "$(docker ps -aq --filter label=dev.probe=1)" 2>/dev/null || true
```

- [ ] **Step 6: Record the answer in the spec**

Replace the "Unverified: `--config` versus `--override-config`" section with what actually happened, including the command run and its outcome. If `--config` worked, the rest of the plan stands as written. If only `--override-config` worked, note that Tasks 8 and 9 must use `--override-config` on `up`, `exec` and `read-configuration`, and `--config` on `build` (which has no `--override-config` flag), and **tell the user before continuing**.

- [ ] **Step 7: Commit**

```bash
git add docs/specs/2026-09-17-generated-devcontainer-config.md
git commit -m "docs: record how the devcontainer CLI takes an external config"
```

---

### Task 2: The `generated_config` column

**Files:**
- Create: `internal/store/migrations/0002_generated_config.sql`
- Modify: `internal/model/model.go:62`
- Modify: `internal/store/container.go:14-48` (insert and both selects), `internal/store/container.go:74` (`scanContainer`)
- Test: `internal/store/store_test.go` (the existing store test file; create `internal/store/container_test.go` if there is no natural home)

**Interfaces:**
- Produces: `model.Container.GeneratedConfig string` — empty when the project owns the configuration, the rendered JSON otherwise. Every later task reads or writes this field.

- [ ] **Step 1: Write the failing test**

In the store test file:

```go
// A generated container carries its configuration in the row rather than on
// disk, so the round-trip has to preserve it exactly: it is the input to the
// devcontainer CLI on every later invocation.
func TestContainerRoundTripsAGeneratedConfig(t *testing.T) {
	s := openTest(t)
	seed(t, s)

	const cfg = "{\n  \"name\": \"demo\"\n}"
	want := model.Container{
		Name:            "demo",
		WorkspaceName:   "ws",
		SourceKind:      model.SourceFolder,
		Source:          "/tmp/demo",
		ConfigPath:      "",
		GeneratedConfig: cfg,
	}
	if err := s.CreateContainer(want); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}

	got, err := s.GetContainer("ws", "demo")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if got.GeneratedConfig != cfg {
		t.Errorf("GeneratedConfig = %q, want %q", got.GeneratedConfig, cfg)
	}
	if got.ConfigPath != "" {
		t.Errorf("ConfigPath = %q, want empty for a generated container", got.ConfigPath)
	}

	list, err := s.ListContainers("ws")
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(list) != 1 || list[0].GeneratedConfig != cfg {
		t.Errorf("ListContainers lost the generated config: %+v", list)
	}
}
```

`openTest` and `seed` are the existing fixtures in `internal/store/store_test.go`; `seed` creates provider `local` and workspace `ws`.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/store/ -run TestContainerRoundTripsAGeneratedConfig -v`
Expected: FAIL to compile — `unknown field GeneratedConfig in struct literal`.

- [ ] **Step 3: Write the migration**

`internal/store/migrations/0002_generated_config.sql`:

```sql
-- The devcontainer configuration dev generated for a container, empty when the
-- project ships its own. Stored rather than written to disk so it cascades away
-- with the workspace and travels to Postgres with the rest of the schema.
--
-- A literal default rather than an expression, and NOT NULL, so the same file
-- replays against Postgres unchanged.
ALTER TABLE containers ADD COLUMN generated_config TEXT NOT NULL DEFAULT '';
```

- [ ] **Step 4: Add the field**

In `internal/model/model.go`, inside `Container`, after `ConfigPath`:

```go
	// GeneratedConfig is the devcontainer.json dev wrote for this container,
	// empty when the project ships its own. It lives here rather than on disk
	// so that deleting the workspace takes it too, and so a cloud provider
	// inherits it with the row. The devcontainer CLI only accepts a path, so
	// it is materialised to a temporary file per invocation.
	GeneratedConfig string
```

- [ ] **Step 5: Thread it through the store**

In `internal/store/container.go`, add `generated_config` to the `INSERT` column list and `c.GeneratedConfig` to its arguments; add it to the `SELECT` list in both `GetContainer` and `ListContainers`, in the same position (after `config_path`, before `created_at`); and add `&c.GeneratedConfig` to `scanContainer`'s `sc.Scan` call in that position.

- [ ] **Step 6: Run the test**

Run: `go test ./internal/store/ -run TestContainerRoundTripsAGeneratedConfig -v`
Expected: PASS

- [ ] **Step 7: Run the whole suite and the linter**

Run: `make lint && make test`
Expected: all pass. A failure in another store test means a `SELECT` list and `scanContainer` disagree about column order.

- [ ] **Step 8: Commit**

```bash
git add internal/store internal/model
git commit -m "feat(store): record a generated devcontainer config on the container row"
```

---

### Task 3: The catalog

**Files:**
- Create: `internal/dcgen/catalog.go`
- Test: `internal/dcgen/dcgen_test.go`

**Interfaces:**
- Produces:
  - `type Tool struct { ID, Summary string; Official bool; Feature string; Options map[string]any; Apt string }`
  - `func Catalog() []Tool` — every tool, sorted by ID.
  - `func Lookup(id string) (Tool, bool)`
  - `const BaseImage = "mcr.microsoft.com/devcontainers/base:1-ubuntu-24.04"`
  - `const aptFeature`, `const kubeFeature` — referenced by Task 4.

- [ ] **Step 1: Write the failing test**

`internal/dcgen/dcgen_test.go`:

```go
package dcgen

import "testing"

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
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/dcgen/ -v`
Expected: FAIL — the package does not exist.

- [ ] **Step 3: Write the catalog**

`internal/dcgen/catalog.go`:

```go
// Package dcgen generates a devcontainer configuration for a folder that ships
// none, from a fixed catalog of tools.
//
// It renders JSON and nothing else: no database, no cobra, no filesystem. The
// installing is done by devcontainer Features, so this package never writes a
// shell script and never learns how a tool is packaged for a distribution.
package dcgen

import "sort"

// BaseImage is what a generated container is built from.
//
// Pinned to the Ubuntu release rather than the floating `:ubuntu` tag: that tag
// moves to the next LTS eventually, which would change the distribution under a
// container that had been working for months. The `1-` prefix still takes the
// image's own security rebuilds within that release.
const BaseImage = "mcr.microsoft.com/devcontainers/base:1-ubuntu-24.04"

// Feature references shared by more than one catalog entry, named because the
// render and the reverse map both have to spell them identically.
const (
	// kubeFeature installs kubectl, helm and minikube from one feature, each
	// gated by its own version option where "none" means "skip this one".
	kubeFeature = "ghcr.io/devcontainers/features/kubectl-helm-minikube:1"
	// aptFeature installs plain distribution packages, for the tools no
	// feature publishes at a reference that resolves.
	aptFeature = "ghcr.io/devcontainers-extra/features/apt-get-packages:1"
	// npmFeature installs one global npm package. It needs a node runtime,
	// which is why every tool using it declares Requires.
	npmFeature = "ghcr.io/devcontainers-extra/features/npm-package:1"
)

// Tool is one installable entry in the catalog.
type Tool struct {
	ID      string
	Summary string
	// Official marks a feature published by the devcontainers project or by
	// the vendor of the tool itself. Everything else is a community image the
	// operator is choosing to trust, and `dev container tools` says so.
	Official bool
	// Feature is the OCI reference. Empty when the tool is an apt package.
	Feature string
	Options map[string]any
	// Apt is the package name, set only when Feature is empty. These collect
	// into one apt-get-packages feature, so a dozen packages do not become a
	// dozen image layers.
	Apt string
	// Requires names catalog entries this one cannot work without. They are
	// added to the selection, because the alternative is an image build that
	// fails several minutes in with a message about npm.
	Requires []string
}

// catalog is the whole set. Adding a tool is a line here and nothing else.
//
// Every reference was checked against ghcr.io on 2026-09-17; an option name
// that does not exist is accepted silently by the devcontainer CLI and then
// does nothing, so these are copied from each feature's published metadata
// rather than guessed.
var catalog = []Tool{
	{ID: "aws", Summary: "AWS CLI", Official: true,
		Feature: "ghcr.io/devcontainers/features/aws-cli:1"},
	{ID: "claude-code", Summary: "Claude Code", Official: true,
		Feature: "ghcr.io/anthropics/devcontainer-features/claude-code:1"},
	{ID: "gcloud", Summary: "Google Cloud CLI",
		Feature: "ghcr.io/dhoeric/features/google-cloud-cli:1"},
	{ID: "gh", Summary: "GitHub CLI", Official: true,
		Feature: "ghcr.io/devcontainers/features/github-cli:1"},
	{ID: "helm", Summary: "Helm", Official: true, Feature: kubeFeature},
	{ID: "hermes", Summary: "Hermes agent",
		Feature: "ghcr.io/devcontainer-community/devcontainer-features/hermes-agent.nousresearch.com:1"},
	{ID: "jq", Summary: "jq", Apt: "jq"},
	{ID: "kubectl", Summary: "kubectl", Official: true, Feature: kubeFeature},
	{ID: "node", Summary: "Node.js", Official: true,
		Feature: "ghcr.io/devcontainers/features/node:1"},
	{ID: "opencode", Summary: "OpenCode agent", Feature: npmFeature,
		Options: map[string]any{"package": "opencode-ai"}, Requires: []string{"node"}},
	{ID: "python", Summary: "Python", Official: true,
		Feature: "ghcr.io/devcontainers/features/python:1"},
	{ID: "yq", Summary: "yq", Apt: "yq"},
}

// Catalog returns every tool, sorted by id. Sorted because it is printed as a
// list and read by a human looking for a name.
func Catalog() []Tool {
	out := make([]Tool, len(catalog))
	copy(out, catalog)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Lookup finds one tool by id.
func Lookup(id string) (Tool, bool) {
	for _, t := range catalog {
		if t.ID == id {
			return t, true
		}
	}
	return Tool{}, false
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/dcgen/ -v`
Expected: PASS

- [ ] **Step 5: Lint and commit**

```bash
make lint && make test
git add internal/dcgen
git commit -m "feat(dcgen): add the tool catalog for generated configurations"
```

---

### Task 4: Render and reverse-map

**Files:**
- Create: `internal/dcgen/render.go`
- Test: `internal/dcgen/dcgen_test.go` (append)

**Interfaces:**
- Consumes: `Tool`, `Catalog`, `Lookup`, `BaseImage`, `kubeFeature`, `aptFeature` from Task 3.
- Produces:
  - `func Render(name string, toolIDs []string) (string, error)` — the JSON document, indented two spaces, keys sorted. Errors on an unknown id.
  - `func Resolve(toolIDs []string) ([]string, error)` — validates, de-duplicates, adds `Requires`, sorts. Called by the CLI so it can report what it added.
  - `func ToolsOf(config string) ([]string, error)` — the reverse: catalog ids present in a stored document, sorted.

- [ ] **Step 1: Write the failing tests**

Append to `internal/dcgen/dcgen_test.go`:

```go
// The stored string is compared and committed, so it has to be byte-stable:
// sorted keys, two-space indent, one trailing newline.
func TestRenderIsExactAndStable(t *testing.T) {
	got, err := Render("demo", []string{"node", "jq", "yq"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	want := `{
  "features": {
    "ghcr.io/devcontainers-extra/features/apt-get-packages:1": {
      "packages": "jq,yq"
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

	again, err := Render("demo", []string{"yq", "node", "jq"})
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
	got, err := Resolve([]string{"jq", "jq", "node"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !slices.Equal(got, []string{"jq", "node"}) {
		t.Errorf("Resolve = %v, want [jq node]", got)
	}
}

// The tool set is derived from the stored document rather than stored beside
// it, so `rebuild --tools +x` has to be able to read back what it wrote.
func TestToolsOfRoundTrips(t *testing.T) {
	want := []string{"claude-code", "helm", "jq", "kubectl", "node", "opencode"}
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
```

Add `"slices"` and `"strings"` to the test file's imports.

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/dcgen/ -v`
Expected: FAIL — `undefined: Render`, `undefined: Resolve`, `undefined: ToolsOf`.

- [ ] **Step 3: Write the renderer**

`internal/dcgen/render.go`:

```go
package dcgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Resolve validates a selection, adds anything the chosen tools require, and
// returns it sorted and deduplicated.
//
// Separate from Render because the CLI wants to tell the operator what it added
// on their behalf, which it cannot do if the addition happens inside the
// renderer.
func Resolve(toolIDs []string) ([]string, error) {
	seen := map[string]bool{}
	var add func(id string) error
	add = func(id string) error {
		if seen[id] {
			return nil
		}
		tool, ok := Lookup(id)
		if !ok {
			return fmt.Errorf("unknown tool %q", id)
		}
		seen[id] = true
		for _, req := range tool.Requires {
			if err := add(req); err != nil {
				return err
			}
		}
		return nil
	}

	for _, id := range toolIDs {
		if err := add(strings.TrimSpace(id)); err != nil {
			return nil, err
		}
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	// Sorted, because the map above has no order and the result reaches a
	// rendered document that gets compared against its stored predecessor.
	slices.Sort(out)
	return out, nil
}

// Render produces the devcontainer.json for a selection of tools.
func Render(name string, toolIDs []string) (string, error) {
	ids, err := Resolve(toolIDs)
	if err != nil {
		return "", err
	}

	doc := map[string]any{
		"name":  name,
		"image": BaseImage,
		// The base image's non-root user. Named explicitly so a feature that
		// installs into a home directory installs into the one the operator
		// will be sitting in.
		"remoteUser": "vscode",
	}
	if features := featuresFor(ids); len(features) > 0 {
		doc["features"] = features
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	// encoding/json sorts map keys, which is what makes the output stable
	// enough to compare against the stored copy and to assert on in a test.
	if err := enc.Encode(doc); err != nil {
		return "", fmt.Errorf("rendering the configuration: %w", err)
	}
	return buf.String(), nil
}

// featuresFor builds the features object for an already-resolved selection.
func featuresFor(ids []string) map[string]any {
	features := map[string]any{}
	var apt []string

	for _, id := range ids {
		tool, ok := Lookup(id)
		if !ok {
			continue // Resolve already rejected these
		}
		switch {
		case tool.Apt != "":
			apt = append(apt, tool.Apt)
		case tool.Feature == kubeFeature:
			// Handled once, below: three tools share this reference and a JSON
			// object cannot hold it twice.
		default:
			opts := map[string]any{}
			for k, v := range tool.Options {
				opts[k] = v
			}
			features[tool.Feature] = opts
		}
	}

	if slices.Contains(ids, "kubectl") || slices.Contains(ids, "helm") {
		// Every tool this feature offers defaults to being installed, so the
		// ones not asked for are switched off by name. "none" is the value its
		// install script tests for.
		features[kubeFeature] = map[string]any{
			"version":  noneUnless(slices.Contains(ids, "kubectl")),
			"helm":     noneUnless(slices.Contains(ids, "helm")),
			"minikube": "none", // nothing in the catalog offers it
		}
	}

	if len(apt) > 0 {
		// A comma-separated string, not an array: that is what this feature's
		// published option schema declares.
		features[aptFeature] = map[string]any{"packages": strings.Join(apt, ",")}
	}
	return features
}

func noneUnless(wanted bool) string {
	if wanted {
		return "latest"
	}
	return "none"
}

// ToolsOf reports which catalog tools a stored configuration installs.
//
// Derived rather than stored beside the document, so that a configuration
// edited by hand is still something `rebuild --tools` can reason about. A
// feature reference the catalog does not know is ignored: it belongs to
// whoever added it, and dropping it would be destroying their edit.
func ToolsOf(config string) ([]string, error) {
	var doc struct {
		Features map[string]json.RawMessage `json:"features"`
	}
	if err := json.Unmarshal([]byte(config), &doc); err != nil {
		return nil, fmt.Errorf("reading the stored configuration: %w", err)
	}

	var out []string
	for ref, raw := range doc.Features {
		switch ref {
		case kubeFeature:
			var opts struct {
				Version string `json:"version"`
				Helm    string `json:"helm"`
			}
			// Absent means the feature's own default, which installs it.
			opts.Version, opts.Helm = "latest", "latest"
			if err := json.Unmarshal(raw, &opts); err != nil {
				return nil, fmt.Errorf("reading the stored configuration: %w", err)
			}
			if opts.Version != "none" {
				out = append(out, "kubectl")
			}
			if opts.Helm != "none" {
				out = append(out, "helm")
			}
		case aptFeature:
			var opts struct {
				Packages string `json:"packages"`
			}
			if err := json.Unmarshal(raw, &opts); err != nil {
				return nil, fmt.Errorf("reading the stored configuration: %w", err)
			}
			for _, pkg := range strings.Split(opts.Packages, ",") {
				pkg = strings.TrimSpace(pkg)
				for _, tool := range catalog {
					if tool.Apt == pkg && pkg != "" {
						out = append(out, tool.ID)
					}
				}
			}
		default:
			for _, tool := range catalog {
				if tool.Feature == ref && tool.Feature != "" {
					out = append(out, tool.ID)
				}
			}
		}
	}

	slices.Sort(out)
	return slices.Compact(out), nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/dcgen/ -v`
Expected: PASS. If `TestRenderIsExactAndStable` fails on whitespace, compare the strings byte for byte — `json.Encoder.Encode` appends a newline, which the expected value includes.

- [ ] **Step 5: Lint and commit**

```bash
make lint && make test
git add internal/dcgen
git commit -m "feat(dcgen): render a devcontainer config and read its tools back"
```

---

### Task 5: The picker

**Files:**
- Create: `internal/cli/picker.go`
- Test: `internal/cli/picker_test.go`

**Interfaces:**
- Produces:
  - `type pickItem struct { ID, Summary string; Note string }` — `Note` is the `official`/`community` label.
  - `func multiSelect(items []pickItem, in io.Reader, out io.Writer) ([]string, error)` — returns the selected ids in catalog order. Returns `errPickCancelled` on `Ctrl-C`.
  - `func pickTools(items []pickItem) ([]string, error)` — the raw-mode wrapper around `multiSelect` on `os.Stdin`/`os.Stderr`.
  - `var errPickCancelled = errors.New("cancelled")`

- [ ] **Step 1: Write the failing test**

`internal/cli/picker_test.go`:

```go
package cli

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
)

// Escape sequences arrive as whole reads most of the time and split across two
// reads sometimes, which is the bug this table exists to prevent: a decoder
// that assumes three bytes are present treats a lone ESC as an arrow key.
func TestDecodeKey(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want key
		n    int
	}{
		{"up", "\x1b[A", keyUp, 3},
		{"down", "\x1b[B", keyDown, 3},
		{"space toggles", " ", keyToggle, 1},
		{"return accepts", "\r", keyAccept, 1},
		{"newline accepts too", "\n", keyAccept, 1},
		{"ctrl-c cancels", "\x03", keyCancel, 1},
		{"q cancels", "q", keyCancel, 1},
		{"partial escape waits", "\x1b", keyNone, 0},
		{"partial bracket waits", "\x1b[", keyNone, 0},
		{"unknown byte is skipped", "z", keyNone, 1},
		{"nothing to read", "", keyNone, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, n := decodeKey([]byte(tt.in))
			if got != tt.want || n != tt.n {
				t.Errorf("decodeKey(%q) = (%v, %d), want (%v, %d)", tt.in, got, n, tt.want, tt.n)
			}
		})
	}
}

func testItems() []pickItem {
	return []pickItem{
		{ID: "node", Summary: "Node.js", Note: "official"},
		{ID: "gh", Summary: "GitHub CLI", Note: "official"},
		{ID: "jq", Summary: "jq", Note: "community"},
	}
}

func TestMultiSelectTogglesAndAccepts(t *testing.T) {
	// Toggle the first, move down twice, toggle the third, accept.
	in := strings.NewReader(" \x1b[B\x1b[B \r")
	got, err := multiSelect(testItems(), in, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("multiSelect: %v", err)
	}
	if !slices.Equal(got, []string{"node", "jq"}) {
		t.Errorf("selected %v, want [node jq]", got)
	}
}

// Selecting nothing is a real answer: a bare Ubuntu box.
func TestMultiSelectAcceptsAnEmptySelection(t *testing.T) {
	got, err := multiSelect(testItems(), strings.NewReader("\r"), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("multiSelect: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("selected %v, want nothing", got)
	}
}

func TestMultiSelectTogglesOff(t *testing.T) {
	got, err := multiSelect(testItems(), strings.NewReader("  \r"), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("multiSelect: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("selected %v after toggling the same item twice, want nothing", got)
	}
}

func TestMultiSelectCancels(t *testing.T) {
	_, err := multiSelect(testItems(), strings.NewReader(" \x03"), &bytes.Buffer{})
	if !errors.Is(err, errPickCancelled) {
		t.Errorf("err = %v, want errPickCancelled", err)
	}
}

// The cursor must not walk off either end: an index out of range here is a
// panic in the middle of an interactive prompt.
func TestMultiSelectClampsTheCursor(t *testing.T) {
	// Up past the top, then toggle: still the first item.
	got, err := multiSelect(testItems(), strings.NewReader("\x1b[A\x1b[A \r"), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("multiSelect: %v", err)
	}
	if !slices.Equal(got, []string{"node"}) {
		t.Errorf("selected %v, want [node]", got)
	}

	// Down past the bottom, then toggle: still the last item.
	got, err = multiSelect(testItems(), strings.NewReader("\x1b[B\x1b[B\x1b[B\x1b[B \r"), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("multiSelect: %v", err)
	}
	if !slices.Equal(got, []string{"jq"}) {
		t.Errorf("selected %v, want [jq]", got)
	}
}

// End of input without an accept is the operator's terminal going away. It must
// not be read as "they accepted an empty list" and silently build something.
func TestMultiSelectTreatsEOFAsCancellation(t *testing.T) {
	_, err := multiSelect(testItems(), strings.NewReader(" "), &bytes.Buffer{})
	if !errors.Is(err, errPickCancelled) {
		t.Errorf("err = %v, want errPickCancelled", err)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/cli/ -run 'TestDecodeKey|TestMultiSelect' -v`
Expected: FAIL — `undefined: decodeKey`, `undefined: multiSelect`.

- [ ] **Step 3: Write the picker**

`internal/cli/picker.go`:

```go
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"golang.org/x/term"
)

// errPickCancelled is returned when the operator abandons the picker. The
// caller must write nothing: a half-answered question is not an answer.
var errPickCancelled = errors.New("cancelled")

// pickItem is one row in the picker.
type pickItem struct {
	ID      string
	Summary string
	// Note qualifies the row — "official" or "community" — so that pulling a
	// third-party image is a visible choice rather than a default.
	Note string
}

type key int

const (
	keyNone key = iota
	keyUp
	keyDown
	keyToggle
	keyAccept
	keyCancel
)

// decodeKey reads one key from the front of buf, returning how many bytes it
// consumed.
//
// Returning (keyNone, 0) means "not enough bytes yet, read more": an arrow key
// is three bytes and a terminal is free to deliver them across separate reads,
// so treating a lone ESC as a complete key would turn a cursor move into a
// cancel.
func decodeKey(buf []byte) (key, int) {
	if len(buf) == 0 {
		return keyNone, 0
	}
	switch buf[0] {
	case 0x1b:
		if len(buf) < 3 {
			return keyNone, 0 // incomplete escape sequence
		}
		if buf[1] == '[' {
			switch buf[2] {
			case 'A':
				return keyUp, 3
			case 'B':
				return keyDown, 3
			}
		}
		return keyNone, 3 // some other escape sequence; discard it whole
	case ' ':
		return keyToggle, 1
	case '\r', '\n':
		return keyAccept, 1
	case 0x03, 'q': // Ctrl-C
		return keyCancel, 1
	}
	return keyNone, 1
}

// multiSelect runs the picker over the given streams.
//
// Streams rather than os.Stdin and os.Stdout, the way prompter takes them, so
// the whole interaction can be driven from a test over a pipe. Putting the
// terminal into raw mode is the caller's job; see pickTools.
func multiSelect(items []pickItem, in io.Reader, out io.Writer) ([]string, error) {
	chosen := make([]bool, len(items))
	cursor := 0

	fmt.Fprintf(out, "tools (\u2191\u2193 move, space toggle, enter accept, ^C cancel)\r\n\r\n")
	draw(out, items, chosen, cursor, false)

	var buf []byte
	readBuf := make([]byte, 16)
	for {
		n, err := in.Read(readBuf)
		if n > 0 {
			buf = append(buf, readBuf[:n]...)
		}
		if n == 0 && err != nil {
			// The input ended without an accept. That is the terminal going
			// away, not a considered empty selection, so nothing is built.
			return nil, errPickCancelled
		}

		for {
			k, consumed := decodeKey(buf)
			if consumed == 0 {
				break // wait for more bytes
			}
			buf = buf[consumed:]

			switch k {
			case keyUp:
				if cursor > 0 {
					cursor--
				}
			case keyDown:
				if cursor < len(items)-1 {
					cursor++
				}
			case keyToggle:
				chosen[cursor] = !chosen[cursor]
			case keyCancel:
				draw(out, items, chosen, cursor, true)
				return nil, errPickCancelled
			case keyAccept:
				draw(out, items, chosen, cursor, true)
				var picked []string
				for i, ok := range chosen {
					if ok {
						picked = append(picked, items[i].ID)
					}
				}
				return picked, nil
			}
			draw(out, items, chosen, cursor, false)
		}
	}
}

// draw rewrites the list in place.
//
// Cursor-up over the rows it printed last time, rather than clearing the
// screen: the question above the list and whatever the operator was reading
// before it stay where they are. final leaves the list on screen without the
// cursor marker, as a record of what was chosen.
func draw(out io.Writer, items []pickItem, chosen []bool, cursor int, final bool) {
	var b strings.Builder
	for i, it := range items {
		marker := " "
		if i == cursor && !final {
			marker = ">"
		}
		box := " "
		if chosen[i] {
			box = "x"
		}
		// \r\n rather than \n: in raw mode the terminal does not translate a
		// newline into a carriage return, so every line would step right.
		fmt.Fprintf(&b, "%s [%s] %-12s %s (%s)\r\n", marker, box, it.ID, it.Summary, it.Note)
	}
	if !final {
		// Park the cursor back at the top of the list for the next redraw.
		fmt.Fprintf(&b, "\x1b[%dA", len(items))
	}
	io.WriteString(out, b.String())
}

// pickTools runs the picker on the real terminal.
//
// The prompt goes to stderr so that a command whose stdout is being captured
// does not have the question land in the captured output.
func pickTools(items []pickItem) ([]string, error) {
	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, fmt.Errorf("putting the terminal into raw mode: %w", err)
	}
	// Restored even on a panic: leaving a terminal in raw mode makes the
	// operator's shell unusable until they blindly type `reset`.
	defer term.Restore(fd, state)

	return multiSelect(items, os.Stdin, os.Stderr)
}

// catalogItems adapts the tool catalog to picker rows.
func catalogItems(tools []dcgen.Tool, preselect []string) []pickItem {
	items := make([]pickItem, 0, len(tools))
	for _, t := range tools {
		note := "community"
		if t.Official {
			note = "official"
		}
		items = append(items, pickItem{ID: t.ID, Summary: t.Summary, Note: note})
	}
	_ = slices.Clip(preselect)
	return items
}
```

Drop `catalogItems`' unused `preselect` parameter and the `slices` import if the compiler flags them — Task 6 is the only caller and passes nothing. Add the `dcgen` import: `"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/dcgen"`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/cli/ -run 'TestDecodeKey|TestMultiSelect' -v`
Expected: PASS

- [ ] **Step 5: Lint and commit**

```bash
make lint && make test
git add internal/cli/picker.go internal/cli/picker_test.go
git commit -m "feat(cli): add a terminal multi-select for choosing tools"
```

---

### Task 6: The create flow

**Files:**
- Create: `internal/cli/generate.go`, `internal/cli/generate_test.go`
- Modify: `internal/cli/container.go:39-105` (`newContainerCreateCmd` and `runContainerCreate`)

**Interfaces:**
- Consumes: `dcgen.Render`, `dcgen.Resolve`, `dcgen.Catalog`, `pickTools`, `catalogItems`, `errPickCancelled`, `isTerminal`, `model.Container.GeneratedConfig`.
- Produces:
  - `func parseToolList(s string) []string` — splits and trims a comma-separated flag value; empty string yields nil.
  - `func generatedConfigFor(name string, generate bool, tools []string, in *os.File, out io.Writer) (string, error)` — the whole decision: returns the rendered JSON, or `""` with an error when the operator declined or the run is not interactive.

- [ ] **Step 1: Write the failing test**

`internal/cli/generate_test.go`:

```go
package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseToolList(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"node", []string{"node"}},
		{"node,jq", []string{"node", "jq"}},
		{" node , jq ", []string{"node", "jq"}},
		{"node,,jq", []string{"node", "jq"}},
	}
	for _, tt := range tests {
		if got := parseToolList(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("parseToolList(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// Under `go test` stdin is /dev/null, which isTerminal rejects — so these
// exercise the scripted path, which is the one that must never block.
func TestCreateWithoutAConfigStillFails(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	err := runContainerCreate(t.Context(), a, "", "demo", t.TempDir(), createOpts{})
	if err == nil {
		t.Fatal("create succeeded on a folder with no devcontainer config")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
	// The error has to teach the way forward; there is no other discovery path.
	if !strings.Contains(err.Error(), "--generate") {
		t.Errorf("error %q does not mention --generate", err)
	}
}

func TestCreateGeneratesAConfig(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	folder := t.TempDir()
	opts := createOpts{generate: true, tools: []string{"jq"}, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "demo", folder, opts); err != nil {
		t.Fatalf("create: %v", err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	c, err := st.GetContainer("ws", "demo")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if !strings.Contains(c.GeneratedConfig, "apt-get-packages") {
		t.Errorf("stored config does not install jq:\n%s", c.GeneratedConfig)
	}
	if c.ConfigPath != "" {
		t.Errorf("ConfigPath = %q, want empty for a generated container", c.ConfigPath)
	}
}

// The project owns its container definition. Shadowing a configuration that is
// right there is the worst of both behaviours.
func TestGenerateRefusesAFolderThatHasAConfig(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	folder := t.TempDir()
	if err := os.MkdirAll(filepath.Join(folder, ".devcontainer"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(folder, ".devcontainer", "devcontainer.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	opts := createOpts{generate: true, noStart: true}
	err := runContainerCreate(t.Context(), a, "", "demo", folder, opts)
	if err == nil {
		t.Fatal("--generate shadowed the project's own configuration")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
}

func TestToolsWithoutGenerateIsRejected(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{tools: []string{"jq"}, noStart: true}
	err := runContainerCreate(t.Context(), a, "", "demo", t.TempDir(), opts)
	if err == nil {
		t.Fatal("--tools was accepted without --generate")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
}

func TestGenerateRejectsAnUnknownTool(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{generate: true, tools: []string{"kubctl"}, noStart: true}
	err := runContainerCreate(t.Context(), a, "", "demo", t.TempDir(), opts)
	if err == nil {
		t.Fatal("an unknown tool was accepted")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
	if !strings.Contains(err.Error(), "kubctl") {
		t.Errorf("error %q does not name the offending tool", err)
	}
}
```

Add a `seedWorkspace` helper to this file if the package has none — it needs a provider and a workspace named `ws`, set active:

```go
// seedWorkspace gives a test app a usable provider and workspace, since every
// container command resolves one before it does anything else.
func seedWorkspace(t *testing.T, a *app) {
	t.Helper()
	if err := runProviderConfigure(a, "p", "local", k8s.Config{}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if err := runWorkspaceInit(a, "ws", "p"); err != nil {
		t.Fatalf("workspace init: %v", err)
	}
}
```

Both signatures are real: `runProviderConfigure(a *app, name, kind string, flags k8s.Config) error` and `runWorkspaceInit(a *app, name, provider string) error`. `runWorkspaceInit` makes the first workspace active on its own, so no `workspace use` call is needed. Import `internal/provider/k8s` for the empty `k8s.Config{}`, as `provider_test.go` already does.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/cli/ -run 'TestParseToolList|TestCreate|TestGenerate|TestTools' -v`
Expected: FAIL — `undefined: parseToolList`, `undefined: createOpts`.

- [ ] **Step 3: Write the generate flow**

`internal/cli/generate.go`:

```go
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/dcgen"
)

// parseToolList splits a --tools value. Empty entries are dropped rather than
// rejected, so a trailing comma is not a usage error.
func parseToolList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// generatedConfigFor decides whether to generate a configuration and renders it.
//
// Three paths, matching `provider configure`: told to, so do it; not told but
// at a terminal, so ask; neither, so fail naming the flag. The scripted run
// must never block on a question nobody will see.
func generatedConfigFor(name string, generate bool, tools []string, in *os.File, out io.Writer) (string, error) {
	if !generate {
		if !isTerminal(in) {
			return "", nil // the caller reports the missing configuration
		}
		if !confirm(in, out, "generate a base Ubuntu devcontainer?") {
			return "", nil
		}
		picked, err := pickTools(catalogItems(dcgen.Catalog()))
		if err != nil {
			if errors.Is(err, errPickCancelled) {
				// Nothing is written: a cancelled question is not an answer.
				return "", errors.New("cancelled")
			}
			return "", err
		}
		tools = picked
	}

	resolved, err := dcgen.Resolve(tools)
	if err != nil {
		return "", usageError(err)
	}
	// Say what was added on the operator's behalf. Silently installing a tool
	// they did not ask for is the kind of surprise that gets noticed months
	// later, in an image nobody can explain.
	for _, id := range resolved {
		if !slices.Contains(tools, id) {
			fmt.Fprintf(out, "adding %s, which the selection requires\n", id)
		}
	}

	config, err := dcgen.Render(name, resolved)
	if err != nil {
		return "", usageError(err)
	}
	return config, nil
}
```

- [ ] **Step 4: Rework the create command**

In `internal/cli/container.go`, replace the flag variables and `runContainerCreate` signature with an options struct, so the growing list of flags does not become six positional parameters:

```go
// createOpts is what `container create` was asked for beyond the name.
type createOpts struct {
	noStart  bool
	generate bool
	tools    []string
}
```

`newContainerCreateCmd` gains:

```go
	cmd.Flags().BoolVar(&generate, "generate", false,
		"generate a base Ubuntu configuration when the folder ships none")
	cmd.Flags().StringVar(&toolList, "tools", "",
		"comma-separated tools to install in a generated container (see: dev container tools)")
```

and builds `createOpts{noStart: noStart, generate: generate, tools: parseToolList(toolList)}`.

Update the `Long` help, which currently says the folder must ship its own configuration:

```go
		Long: "Create a container from a folder and start it.\n\n" +
			"A folder that ships its own .devcontainer configuration is used as it\n" +
			"is; dev never edits it. For a folder with none, --generate builds a\n" +
			"base Ubuntu configuration from the tools --tools names, and keeps it\n" +
			"in dev's own database rather than writing into the project.",
```

Then in `runContainerCreate`, replace the `dcconfig.Find` error path:

```go
	if len(opts.tools) > 0 && !opts.generate {
		return usageErrorf("--tools only applies with --generate")
	}

	configPath, err := dcconfig.Find(source)
	switch {
	case err == nil && opts.generate:
		// The project ships one. Generating a second would shadow the
		// definition the project owns, with no way to tell from the outside
		// which one built the container.
		return usageErrorf("%s already has a devcontainer config; --generate would shadow it", xpath.Shorten(source))
	case err != nil && !errors.Is(err, dcconfig.ErrNoConfig):
		return usageError(err)
	}

	var generated string
	if errors.Is(err, dcconfig.ErrNoConfig) {
		generated, gerr := generatedConfigFor(name, opts.generate, opts.tools, os.Stdin, a.out)
		if gerr != nil {
			return gerr
		}
		if generated == "" {
			return usageErrorf("%w (--generate builds a base Ubuntu one; dev container tools lists what it can add)", err)
		}
		configPath = ""
		_ = generated
	}
```

Take care with the shadowing in that block — declare `generated` once with `var` and assign with `=`, not `:=`, or the outer variable stays empty and the container is stored with no configuration at all. Then set `GeneratedConfig: generated` on the `model.Container` literal.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/cli/ -run 'TestParseToolList|TestCreate|TestGenerate|TestTools' -v`
Expected: PASS

- [ ] **Step 6: Lint, full suite, commit**

```bash
make lint && make test
git add internal/cli
git commit -m "feat(cli): generate a devcontainer config for a folder that ships none"
```

---

### Task 7: `container tools` and `container config show`

**Files:**
- Create: `internal/cli/config.go`
- Modify: `internal/cli/container.go:19-36` (`newContainerCmd`'s `AddCommand` list)
- Test: `internal/cli/generate_test.go` (append)

**Interfaces:**
- Consumes: `dcgen.Catalog`, `a.table`, `header`, `row`, `a.resolve`.
- Produces: `newContainerToolsCmd(a *app) *cobra.Command`, `newContainerConfigCmd(a *app) *cobra.Command`.

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/generate_test.go`:

```go
func TestContainerToolsListsTheCatalog(t *testing.T) {
	a, out := newTestApp(t)
	if err := runContainerTools(a); err != nil {
		t.Fatalf("tools: %v", err)
	}
	got := out.String()
	for _, want := range []string{"node", "claude-code", "official", "community"} {
		if !strings.Contains(got, want) {
			t.Errorf("tools output is missing %q:\n%s", want, got)
		}
	}
}

// With the configuration in the database rather than on disk, this is the only
// way to see what a container was built from.
func TestConfigShowPrintsTheStoredConfig(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{generate: true, tools: []string{"jq"}, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "demo", t.TempDir(), opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	out.Reset()

	if err := runContainerConfigShow(a, "", "demo"); err != nil {
		t.Fatalf("config show: %v", err)
	}
	if !strings.Contains(out.String(), "apt-get-packages") {
		t.Errorf("config show printed nothing useful:\n%s", out.String())
	}
}

// A project-owned container has no stored configuration, and saying "" would
// look like an empty one rather than a different kind of container.
func TestConfigShowOnAProjectOwnedContainer(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	folder := t.TempDir()
	if err := os.MkdirAll(filepath.Join(folder, ".devcontainer"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	cfg := filepath.Join(folder, ".devcontainer", "devcontainer.json")
	if err := os.WriteFile(cfg, []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := runContainerCreate(t.Context(), a, "", "owned", folder, createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}

	err := runContainerConfigShow(a, "", "owned")
	if err == nil {
		t.Fatal("config show invented a configuration for a project-owned container")
	}
	if !strings.Contains(err.Error(), cfg) {
		t.Errorf("error %q does not point at the project's own config", err)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/cli/ -run 'TestContainerTools|TestConfigShow' -v`
Expected: FAIL — `undefined: runContainerTools`.

- [ ] **Step 3: Write the commands**

`internal/cli/config.go`:

```go
package cli

import (
	"io"

	"github.com/spf13/cobra"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/dcgen"
)

func newContainerToolsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "tools",
		Short: "List the tools a generated container can install",
		Args:  noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerTools(a)
		},
	}
}

func runContainerTools(a *app) error {
	return a.table(func(w io.Writer) {
		header(w, "TOOL", "SUMMARY", "SOURCE")
		for _, t := range dcgen.Catalog() {
			// Whether a feature is published by the devcontainers project (or
			// the tool's own vendor) is the operator's business: the rest are
			// community images they are choosing to trust.
			source := "community"
			if t.Official {
				source = "official"
			}
			row(w, t.ID, t.Summary, source)
		}
	})
}

func newContainerConfigCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect a generated container's configuration",
	}

	var workspace string
	show := &cobra.Command{
		Use:   "show NAME",
		Short: "Print the devcontainer config dev generated for a container",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerConfigShow(a, workspace, args[0])
		},
	}
	addWorkspaceFlag(show, &workspace)

	cmd.AddCommand(show)
	return cmd
}

func runContainerConfigShow(a *app, workspace, name string) error {
	t, err := a.resolve(workspace, name)
	if err != nil {
		return err
	}
	if t.container.GeneratedConfig == "" {
		// Not an empty configuration: a different kind of container, whose
		// configuration is a file the operator can already open.
		return usageErrorf("container %s uses the project's own config: %s",
			name, t.container.ConfigPath)
	}
	a.printf("%s", t.container.GeneratedConfig)
	return nil
}
```

Add both to `newContainerCmd`'s `AddCommand` list in `internal/cli/container.go`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/cli/ -run 'TestContainerTools|TestConfigShow' -v`
Expected: PASS

- [ ] **Step 5: Lint and commit**

```bash
make lint && make test
git add internal/cli
git commit -m "feat(cli): add container tools and container config show"
```

---

### Task 8: Materialise the configuration and pass `--config` (local)

**Files:**
- Modify: `internal/cli/resolve.go` (add the materialise helper, call it from `resolve`)
- Modify: `internal/cli/container.go:215-237` (`a.start`, which builds a container without going through `resolve`)
- Modify: `internal/provider/local/local.go:56` (`up`), `internal/provider/local/local.go:87` (`exec`)
- Test: `internal/cli/generate_test.go` (append), `internal/provider/local/local_test.go` (append)

**Interfaces:**
- Produces: `func materialise(c model.Container) (model.Container, func(), error)` — returns the container with a usable `ConfigPath` and a cleanup function that is always safe to call. For a project-owned container it returns the input and a no-op.

> If Task 1 found that `--config` does not work for a folder with no configuration of its own, use `--override-config` in this task instead, and say so in the commit message.

- [ ] **Step 1: Write the failing tests**

In `internal/provider/local/local_test.go`, following the existing stub-PATH pattern in that file:

```go
// The config path is passed explicitly rather than left to the CLI's own
// lookup, because a generated configuration lives in a temporary directory the
// CLI would never find, and because being explicit costs nothing for a
// project-owned one.
func TestUpPassesTheConfigPath(t *testing.T) {
	f := newFakePath(t)
	f.install(t, "devcontainer", "", 0)

	c := model.Container{
		Name: "demo", WorkspaceName: "ws",
		SourceKind: model.SourceFolder,
		Source:     t.TempDir(),
		ConfigPath: "/somewhere/.devcontainer/devcontainer.json",
	}
	if err := (&Provider{}).Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	argv := f.argv(t, "devcontainer")
	if !hasFlagValue(argv, "--config", c.ConfigPath) {
		t.Errorf("up argv missing --config %s: %v", c.ConfigPath, argv)
	}
}

func TestExecPassesTheConfigPath(t *testing.T) {
	f := newFakePath(t)
	f.install(t, "devcontainer", "", 0)

	c := model.Container{
		Name: "demo", WorkspaceName: "ws",
		SourceKind: model.SourceFolder,
		Source:     t.TempDir(),
		ConfigPath: "/somewhere/.devcontainer/devcontainer.json",
	}
	err := (&Provider{}).Exec(context.Background(), c, []string{"true"},
		provider.ExecOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	argv := f.argv(t, "devcontainer")
	if !hasFlagValue(argv, "--config", c.ConfigPath) {
		t.Errorf("exec argv missing --config %s: %v", c.ConfigPath, argv)
	}
}

// hasFlagValue reports whether argv contains flag immediately followed by value.
func hasFlagValue(argv []string, flag, value string) bool {
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}
```

If `local_test.go` already has an equivalent of `hasFlagValue`, use that instead of adding a second one.

In `internal/cli/generate_test.go`:

```go
// The devcontainer CLI only accepts a path, so a configuration that lives in
// the database has to become a file for the length of one invocation — and
// stop being one afterwards.
func TestMaterialiseWritesAndCleansUp(t *testing.T) {
	c := model.Container{Name: "demo", GeneratedConfig: "{\"name\":\"demo\"}\n"}

	got, cleanup, err := materialise(c)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	if got.ConfigPath == "" {
		t.Fatal("materialise produced no config path")
	}
	if filepath.Base(got.ConfigPath) != "devcontainer.json" ||
		filepath.Base(filepath.Dir(got.ConfigPath)) != ".devcontainer" {
		// The CLI infers things from the layout around the file, so the
		// temporary copy mirrors what a project would look like.
		t.Errorf("unexpected layout: %s", got.ConfigPath)
	}
	body, err := os.ReadFile(got.ConfigPath)
	if err != nil {
		t.Fatalf("reading the materialised config: %v", err)
	}
	if string(body) != c.GeneratedConfig {
		t.Errorf("materialised %q, want %q", body, c.GeneratedConfig)
	}

	cleanup()
	if _, err := os.Stat(got.ConfigPath); !os.IsNotExist(err) {
		t.Errorf("cleanup left the file behind: %v", err)
	}
}

func TestMaterialiseLeavesAProjectOwnedContainerAlone(t *testing.T) {
	c := model.Container{Name: "demo", ConfigPath: "/proj/.devcontainer/devcontainer.json"}

	got, cleanup, err := materialise(c)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanup() // must be safe to call even when nothing was written
	if got.ConfigPath != c.ConfigPath {
		t.Errorf("ConfigPath = %q, want it untouched", got.ConfigPath)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/cli/ ./internal/provider/local/ -run 'TestMaterialise|PassesTheConfigPath' -v`
Expected: FAIL — `undefined: materialise`, and the provider tests fail because `--config` is not in the argv.

- [ ] **Step 3: Write `materialise`**

Append to `internal/cli/resolve.go`:

```go
// materialise gives a container a config path a child process can open.
//
// A generated configuration lives in the database, and the devcontainer CLI
// takes a path and nothing else — so it becomes a file for the length of one
// invocation. Written into a .devcontainer directory because the CLI reads the
// layout around the file, not just the file.
//
// The returned cleanup is always safe to call, including for a project-owned
// container where nothing was written.
func materialise(c model.Container) (model.Container, func(), error) {
	if c.GeneratedConfig == "" {
		return c, func() {}, nil
	}

	dir, err := os.MkdirTemp("", "dev-config-")
	if err != nil {
		return c, func() {}, fmt.Errorf("preparing the generated config: %w", err)
	}
	cleanup := func() { os.RemoveAll(dir) }

	// 0700 on the directory and 0600 on the file: the configuration says what
	// the operator is building, and on a shared host that is nobody else's
	// business.
	nested := filepath.Join(dir, ".devcontainer")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		cleanup()
		return c, func() {}, fmt.Errorf("preparing the generated config: %w", err)
	}
	path := filepath.Join(nested, "devcontainer.json")
	if err := os.WriteFile(path, []byte(c.GeneratedConfig), 0o600); err != nil {
		cleanup()
		return c, func() {}, fmt.Errorf("writing the generated config: %w", err)
	}

	c.ConfigPath = path
	return c, cleanup, nil
}
```

Add `"fmt"`, `"os"` and `"path/filepath"` to that file's imports.

- [ ] **Step 4: Call it**

In `resolve`, after the container is loaded, materialise it and hand the cleanup back on the target:

```go
	c, cleanup, err := materialise(c)
	if err != nil {
		return nil, err
	}
	return &target{container: c, workspace: ws, provider: p, cleanup: cleanup}, nil
```

Add `cleanup func()` to the `target` struct with a comment saying why, plus:

```go
// release removes anything resolve wrote for this container. Safe on a nil
// cleanup, so every caller can defer it without checking.
func (t *target) release() {
	if t != nil && t.cleanup != nil {
		t.cleanup()
	}
}
```

Then add `defer t.release()` to every command function in `internal/cli/container.go` and `internal/cli/agent.go` that calls `a.resolve` — grep for `a.resolve(` and cover each one. Also materialise in `a.start` (`container.go:215`), which builds its own container value on the create path and never goes through `resolve`:

```go
	c, cleanup, err := materialise(c)
	if err != nil {
		return err
	}
	defer cleanup()
```

- [ ] **Step 5: Pass the flag in the local provider**

In `internal/provider/local/local.go`, in both `up` and `Exec`, after the `--workspace-folder` argument:

```go
	// The config path is passed rather than inferred. A generated one lives in
	// a temporary directory the CLI's own lookup would never reach, and for a
	// project-owned one this is the path dcconfig already resolved.
	if c.ConfigPath != "" {
		args = append(args, "--config", c.ConfigPath)
	}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/cli/ ./internal/provider/local/ -v`
Expected: PASS. An existing local test asserting an exact argv may need the two new arguments added to its expectation.

- [ ] **Step 7: Lint, full suite, commit**

```bash
make lint && make test
git add internal/cli internal/provider/local
git commit -m "feat: materialise a generated config and pass it to the devcontainer CLI"
```

---

### Task 9: The k8s provider

**Files:**
- Modify: `internal/provider/k8s/build.go:42` (`readConfiguration`), `internal/provider/k8s/build.go:146` (`buildAndPush`)
- Modify: `internal/provider/k8s/k8s.go:85`, `internal/provider/k8s/k8s.go:132`, `internal/provider/k8s/sync.go:26` (the three call sites)
- Test: `internal/provider/k8s/build_test.go`

**Interfaces:**
- Consumes: `model.Container.ConfigPath`, already materialised by Task 8.
- Produces: `readConfiguration(ctx, folder, configPath string)` and `buildAndPush(ctx, folder, configPath, image, platform string, noCache bool, progress io.Writer)`.

- [ ] **Step 1: Write the failing test**

In `internal/provider/k8s/build_test.go`, following the stub harness already in that package — note that it *replaces* `PATH` rather than prepending, because the builder probes docker and then podman and a leaked host binary becomes the one under test:

```go
// The k8s provider reads the configuration and builds the image through the
// same CLI, so both have to be told where the configuration is: a generated one
// is in a temporary directory, not in the folder.
func TestReadConfigurationPassesTheConfigPath(t *testing.T) {
	s := newStubs(t)
	s.install(t, devcontainerBin, `{"configuration":{},"mergedConfiguration":{}}`, 0)
	s.install(t, dockerBin, "", 0)

	const cfg = "/tmp/dev-config-x/.devcontainer/devcontainer.json"
	if _, _, err := readConfiguration(context.Background(), t.TempDir(), cfg); err != nil {
		t.Fatalf("readConfiguration: %v", err)
	}

	argv := s.argv(t, devcontainerBin)
	if !hasFlagValue(argv, "--config", cfg) {
		t.Errorf("read-configuration argv missing --config %s: %v", cfg, argv)
	}
}

func TestBuildPassesTheConfigPath(t *testing.T) {
	s := newStubs(t)
	withBuildx(t, s)
	s.install(t, devcontainerBin, "", 0)

	const cfg = "/tmp/dev-config-x/.devcontainer/devcontainer.json"
	err := buildAndPush(context.Background(), "/p", cfg, "img", "linux/amd64", false, io.Discard)
	if err != nil {
		t.Fatalf("buildAndPush: %v", err)
	}
	if !hasFlagValue(s.argv(t, devcontainerBin), "--config", cfg) {
		t.Errorf("build argv missing --config %s: %v", s.argv(t, devcontainerBin), cfg)
	}
}
```

`newStubs`, `install`, `argv` and `withBuildx` are the real helpers in this package — `newStubs` *replaces* `PATH` rather than prepending, for the reason its own comment gives. Add `hasFlagValue` here too if the package has no equivalent. `devcontainerBin` and `dockerBin` are the existing binary-name constants.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/provider/k8s/ -run TestReadConfigurationPassesTheConfigPath -v`
Expected: FAIL — too many arguments to `readConfiguration`.

- [ ] **Step 3: Thread the path through**

Add a `configPath string` parameter to `readConfiguration` and to `buildAndPush`, and in each, after the `--workspace-folder` argument:

```go
	// Passed rather than inferred: a generated configuration lives in a
	// temporary directory, and the CLI's own lookup only searches the folder.
	if configPath != "" {
		args = append(args, "--config", configPath)
	}
```

`build` has no `--override-config`, only `--config` — so if Task 1 found that `up` needs `--override-config`, `buildAndPush` still uses `--config` and the difference is deliberate. Comment it where it happens.

Update the three production call sites (`k8s.go:85`, `k8s.go:132`, `sync.go:26`) to pass `c.ConfigPath`. The existing tests call both functions too — three `readConfiguration` calls and three `buildAndPush` calls in `build_test.go`, plus any in `k8s_test.go` and `sync_test.go`. Pass `""` in each, which is what a project-owned container has and which must keep behaving exactly as before; the compiler finds them all.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/provider/k8s/ -v`
Expected: PASS

- [ ] **Step 5: Lint, full suite, commit**

```bash
make lint && make test
git add internal/provider/k8s
git commit -m "feat(k8s): pass the config path to read-configuration and build"
```

---

### Task 10: `rebuild --tools`

**Files:**
- Modify: `internal/cli/container.go:310-338` (`newContainerRebuildCmd`)
- Modify: `internal/cli/generate.go` (the diff parser)
- Modify: `internal/store/container.go` (an update for the config column)
- Test: `internal/cli/generate_test.go` (append)

**Interfaces:**
- Consumes: `dcgen.ToolsOf`, `dcgen.Render`, `materialise`.
- Produces:
  - `func applyToolDiff(current, spec []string) ([]string, error)` — `+x`/`-x` entries adjust `current`; bare entries replace it; mixing the two is an error.
  - `func (s *Store) UpdateContainerConfig(workspace, name, config string) error`

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/generate_test.go`:

```go
func TestApplyToolDiff(t *testing.T) {
	current := []string{"helm", "node"}

	tests := []struct {
		name string
		spec []string
		want []string
	}{
		{"add", []string{"+jq"}, []string{"helm", "jq", "node"}},
		{"remove", []string{"-helm"}, []string{"node"}},
		{"both", []string{"+jq", "-helm"}, []string{"jq", "node"}},
		{"replace", []string{"jq", "yq"}, []string{"jq", "yq"}},
		{"remove what is absent", []string{"-yq"}, []string{"helm", "node"}},
		{"add what is present", []string{"+node"}, []string{"helm", "node"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyToolDiff(current, tt.spec)
			if err != nil {
				t.Fatalf("applyToolDiff: %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("applyToolDiff(%v, %v) = %v, want %v", current, tt.spec, got, tt.want)
			}
		})
	}
}

// "node,+jq" reads as one intent and means another, so it is refused rather
// than guessed at.
func TestApplyToolDiffRejectsMixedForms(t *testing.T) {
	_, err := applyToolDiff([]string{"node"}, []string{"node", "+jq"})
	if err == nil {
		t.Fatal("a mixed diff and replacement was accepted")
	}
}

func TestRebuildToolsRewritesTheStoredConfig(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{generate: true, tools: []string{"jq"}, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "demo", t.TempDir(), opts); err != nil {
		t.Fatalf("create: %v", err)
	}

	// The rebuild itself needs an engine; this asserts the rewrite, which is
	// the part that belongs to dev.
	if err := rewriteGeneratedTools(a, "", "demo", []string{"+yq"}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	st, _ := a.store()
	c, err := st.GetContainer("ws", "demo")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	tools, err := dcgen.ToolsOf(c.GeneratedConfig)
	if err != nil {
		t.Fatalf("ToolsOf: %v", err)
	}
	if !slices.Equal(tools, []string{"jq", "yq"}) {
		t.Errorf("tools = %v, want [jq yq]", tools)
	}
}

// A project's configuration is not dev's to rewrite.
func TestRebuildToolsRefusesAProjectOwnedContainer(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	folder := t.TempDir()
	if err := os.MkdirAll(filepath.Join(folder, ".devcontainer"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folder, ".devcontainer", "devcontainer.json"),
		[]byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := runContainerCreate(t.Context(), a, "", "owned", folder, createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}

	err := rewriteGeneratedTools(a, "", "owned", []string{"+jq"})
	if err == nil {
		t.Fatal("--tools rewrote a project-owned container")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/cli/ -run 'TestApplyToolDiff|TestRebuildTools' -v`
Expected: FAIL — `undefined: applyToolDiff`, `undefined: rewriteGeneratedTools`.

- [ ] **Step 3: Write the diff parser and the rewrite**

Append to `internal/cli/generate.go`:

```go
// applyToolDiff works out the new tool set.
//
// Every entry prefixed + or - adjusts the current set; entries with no prefix
// replace it wholesale. Mixing the two forms is refused rather than guessed at:
// "node,+jq" reads as one intent and means another.
func applyToolDiff(current, spec []string) ([]string, error) {
	var diffs, plain int
	for _, s := range spec {
		if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
			diffs++
		} else {
			plain++
		}
	}
	if diffs > 0 && plain > 0 {
		return nil, usageErrorf("--tools takes either a list or +/- changes, not both")
	}
	if diffs == 0 {
		return dcgen.Resolve(spec)
	}

	set := map[string]bool{}
	for _, id := range current {
		set[id] = true
	}
	for _, s := range spec {
		id := s[1:]
		if id == "" {
			return nil, usageErrorf("--tools: %q names no tool", s)
		}
		if s[0] == '+' {
			set[id] = true
		} else {
			delete(set, id)
		}
	}

	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return dcgen.Resolve(out)
}

// rewriteGeneratedTools changes which tools a generated container installs.
//
// Reads the current set back out of the stored document rather than from a
// column of its own, so a configuration edited by hand is still something this
// can reason about.
func rewriteGeneratedTools(a *app, workspace, name string, spec []string) error {
	t, err := a.resolve(workspace, name)
	if err != nil {
		return err
	}
	defer t.release()

	if t.container.GeneratedConfig == "" {
		return usageErrorf("container %s uses the project's own config; edit %s instead",
			name, t.container.ConfigPath)
	}

	current, err := dcgen.ToolsOf(t.container.GeneratedConfig)
	if err != nil {
		return err
	}
	next, err := applyToolDiff(current, spec)
	if err != nil {
		return err
	}
	config, err := dcgen.Render(name, next)
	if err != nil {
		return usageError(err)
	}

	st, err := a.store()
	if err != nil {
		return err
	}
	return st.UpdateContainerConfig(t.workspace.Name, name, config)
}
```

In `internal/store/container.go`:

```go
// UpdateContainerConfig replaces a container's generated configuration.
func (s *Store) UpdateContainerConfig(workspace, name, config string) error {
	res, err := s.db.Exec(
		`UPDATE containers SET generated_config = ?
		 WHERE workspace_name = ? AND name = ?`, config, workspace, name)
	if err != nil {
		return fmt.Errorf("updating container %s: %w", name, err)
	}
	return requireOneRow(res, ErrNotFound)
}
```

- [ ] **Step 4: Wire it to the command**

In `newContainerRebuildCmd`, add the flag and call the rewrite before resolving for the rebuild — the rewrite has to land in the database first, or `resolve` materialises the old configuration:

```go
	cmd.Flags().StringVar(&toolList, "tools", "",
		"change a generated container's tools, e.g. +jq,-helm (see: dev container tools)")
```

and at the top of `RunE`:

```go
			if tools := parseToolList(toolList); len(tools) > 0 {
				// Before resolve, so the rebuild materialises the new
				// configuration rather than the one being replaced.
				if err := rewriteGeneratedTools(a, workspace, args[0], tools); err != nil {
					return err
				}
			}
```

Add `defer t.release()` after the existing `a.resolve` call in that function.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/cli/ -v`
Expected: PASS

- [ ] **Step 6: Lint, full suite, commit**

```bash
make lint && make test
git add internal/cli internal/store
git commit -m "feat(cli): change a generated container's tools with rebuild --tools"
```

---

### Task 11: Cascade test, smoke test, and documentation

**Files:**
- Modify: `internal/store/store_test.go` (the cascade)
- Modify: `test/smoke/smoke_test.go`
- Modify: `CLAUDE.md` (invariant 9)

- [ ] **Step 1: Write the cascade test**

The whole reason the configuration is in the database rather than on disk:

```go
// Storing the configuration on the row is what makes this true: a file under
// the state directory would outlive the workspace that owned it.
func TestDeletingAWorkspaceTakesTheGeneratedConfig(t *testing.T) {
	s := openTest(t)
	seed(t, s)

	c := model.Container{
		Name: "demo", WorkspaceName: "ws",
		SourceKind: model.SourceFolder, Source: "/tmp/demo",
		GeneratedConfig: "{\"name\":\"demo\"}\n",
	}
	if err := s.CreateContainer(c); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	if err := s.DeleteWorkspace("ws"); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}

	list, err := s.ListContainers("")
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("the container outlived its workspace: %+v", list)
	}
}
```

`openTest` and `seed` are the existing fixtures in that file, and `DeleteWorkspace` is the real method name.

- [ ] **Step 2: Run it**

Run: `go test ./internal/store/ -run TestDeletingAWorkspaceTakesTheGeneratedConfig -v`
Expected: PASS — the cascade already exists; this pins it to the new column.

- [ ] **Step 3: Add the smoke test**

In `test/smoke/smoke_test.go`, a second test function alongside `TestSmoke`:

```go
// A folder with no configuration of its own is the whole point of --generate,
// so the fixture is a bare directory.
func TestSmokeGeneratedConfig(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")

	state := t.TempDir()
	t.Setenv("DEV_STATE", state)

	bin := buildBinary(t)
	project := t.TempDir() // deliberately empty

	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	const name = "dev-smoke-gen"
	t.Cleanup(func() {
		_ = exec.Command(bin, "container", "remove", name, "--force").Run()
	})

	dev("provider", "configure", "dev-smoke-gen-local", "--kind", "local")
	dev("workspace", "init", "dev-smoke-gen-ws", "--provider", "dev-smoke-gen-local")

	t.Log("creating a generated container; the first run pulls a base image and installs features")
	dev("container", "create", name, "--folder", project, "--generate", "--tools", "jq")

	// The tool the operator asked for has to actually be in the container:
	// a feature reference that resolves is not the same as one that installs.
	if out := dev("container", "exec", name, "--", "jq", "--version"); !strings.Contains(out, "jq") {
		t.Errorf("jq is not installed in the generated container: %q", out)
	}

	// The project folder is dev's to read, never to write.
	entries, err := os.ReadDir(project)
	if err != nil {
		t.Fatalf("reading the project folder: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("dev wrote into the project folder: %v", entries)
	}

	if out := dev("container", "config", "show", name); !strings.Contains(out, "apt-get-packages") {
		t.Errorf("config show did not print the stored configuration:\n%s", out)
	}

	dev("container", "remove", name)
}
```

The feature install takes several minutes on a cold cache; the existing `run` helper's ten-minute timeout covers it.

- [ ] **Step 4: Run the smoke test if an engine is available**

Run: `make smoke`
Expected: PASS, or a skip on a host with no engine. A skip is an acceptable outcome here — note it in the commit message rather than claiming the test passed.

- [ ] **Step 5: Update CLAUDE.md**

Replace invariant 9:

```markdown
9. **Do not write into a project's folder.** `dev` never creates or edits a
   `devcontainer.json` inside a project, and a missing agent is reported as
   something to add to the project, not something `dev` installs. A folder that
   ships its own configuration always wins. A folder with none can still be run:
   `--generate` renders a base Ubuntu configuration from the `internal/dcgen`
   catalog and stores it on the container row, where it cascades away with the
   workspace and travels to Postgres with the rest of the schema. The
   devcontainer CLI only takes a path, so that configuration is materialised to
   a temporary file per invocation and passed as `--config` — which is why every
   `model.Container` reaching a provider has a usable `ConfigPath` and neither
   provider knows where it came from.
```

Add to the architecture listing, after the `internal/dcconfig` line:

```
internal/dcgen/       generating a devcontainer config: the tool catalog, render
```

- [ ] **Step 6: Lint, full suite, commit**

```bash
make lint && make test
git add internal/store test/smoke CLAUDE.md
git commit -m "test: cover the cascade and a generated container end to end"
```

---

## Self-Review

**Spec coverage:**

| Spec section | Task |
|---|---|
| Data model, `generated_config` column | 2 |
| Catalog, verified references | 3 |
| Render, sorted keys, shared kubectl feature, apt string, empty selection | 4 |
| `opencode` requires `node` | 3 (declaration), 4 (`Resolve`), 6 (reporting) |
| Reverse map / `ToolsOf` | 4 |
| Create flow: flag, prompt, non-tty error | 6 |
| Guards: config exists, `--tools` without `--generate` | 6 |
| `container tools`, `container config show` | 7 |
| Picker: `decodeKey`, `multiSelect`, raw wrapper, cancel | 5 |
| Mutation: `+`/`-` diff, refusal on project-owned | 10 |
| Execution: materialise, `--config` everywhere | 8 (local), 9 (k8s) |
| Unverified `--config` question | 1 |
| Testing section | folded into each task, plus 11 |
| Documentation: CLAUDE.md, create long help | 11, 6 |

No gaps.

**Type consistency:** `createOpts` is defined in Task 6 and used in Tasks 6, 7 and 10. `materialise` is defined in Task 8 and used in 8 and 10. `target.cleanup`/`release` are introduced in Task 8 and used in 10. `dcgen.Resolve` is defined in Task 4 and used in 4, 6 and 10. `pickItem`/`catalogItems` are defined in Task 5 and used in 6. `kubeFeature`/`aptFeature` are defined in Task 3 and used in 4 and its tests.

**Helper names, all verified against the repository:** `openTest` / `seed` (`internal/store/store_test.go`), `newTestApp` (`internal/cli/provider_test.go`), `runProviderConfigure` / `runWorkspaceInit` (which makes the first workspace active on its own), `newFakePath` / `install` / `argv` (`internal/provider/local/local_test.go`), `newStubs` / `install` / `argv` / `withBuildx` and the `devcontainerBin` / `dockerBin` constants (`internal/provider/k8s`), and `requireBinaries` / `buildBinary` / `run` (`test/smoke/smoke_test.go`). The only helper this plan adds is `hasFlagValue`, in Tasks 8 and 9, and each says to reuse an existing equivalent if the package already has one.
