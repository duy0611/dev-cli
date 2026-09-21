# devcontainer.json merge — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `dev` merges its state mount into a project's own `devcontainer.json` at
invocation time and passes the result as `--override-config` on `up` and `exec`,
so a repository can commit a `devcontainer.json` that works under both VS Code
and `dev`.

**Architecture:** A new `dcgen.Overlay` rewrites one field (`mounts`) of a
project's document, replacing any entry targeting `/var/dev-state` with `dev`'s
own volume. `materialise` calls it per invocation and writes the result to a
temporary file; the local provider passes `--override-config` on both `up` and
`exec` and loses its `--mount` branch entirely. Nothing is persisted and no
migration is needed.

**Tech Stack:** Go 1.26, `github.com/tailscale/hujson` (new), the devcontainer
CLI 0.88.0, docker. Tests use the existing stub-executable-on-a-temp-`PATH`
harnesses; nothing new needs an engine.

**Spec:** `docs/specs/2026-09-21-devcontainer-json-merge.md`

## Global Constraints

- Module is `github.com/duy0611/dev-cli`. Go 1.26.0.
- **Commits carry no `Co-Authored-By: Claude ...` trailer** and no other
  tooling-attribution line. `.githooks/commit-msg` rejects one. This overrides
  any harness default asking for it. Run `make hooks` once per clone.
- `make lint && make test` must pass after every task. `make lint` runs gofmt
  (fails on any diff), `go vet`, and golangci-lint.
- `make test` never touches a container engine. Neither `devcontainer` nor
  `docker` is installed in this environment; every test here uses stubs.
- The state directory is `dcgen.StateDir` = `/var/dev-state`. Never spell it
  literally in Go code outside `internal/dcgen/state.go`.
- The state volume name is `local.StateVolumeName(workspace, container)` =
  `dev-<workspace>-<container>-state`. One spelling only.
- Exit codes are interface: `2` malformed request, `3` named thing absent, `1`
  otherwise. An overlay failure is `1` — return a plain error, not
  `usageError`/`notFoundErrorf`.
- Every non-obvious line carries a comment explaining **why**, naming the
  failure it prevents. Match the density of the surrounding file.
- `dev` never writes into a project's folder. Everything here writes to
  `os.MkdirTemp` only.
- No new spelling of a name that already exists elsewhere; no aliases and no
  "used to be X" comments.

---

## File Structure

| Path | Responsibility |
| --- | --- |
| `internal/dcgen/overlay.go` | **new.** Parse a project's JSONC document, rewrite `mounts`, re-marshal. Detect `dockerComposeFile`. Pure: no I/O, no knowledge of containers. |
| `internal/dcgen/overlay_test.go` | **new.** Table-driven tests for the above. |
| `internal/model/model.go` | `OverrideConfigPath` field on `Container`, unpersisted. |
| `internal/provider/provider.go` | `ConfigOverrider` optional interface. |
| `internal/provider/local/local.go` | Implement `ConfigOverrider`; `--override-config` on `up` and `execArgs`; delete the `--mount` branch. |
| `internal/cli/errors.go` | `overlayError` type, so `resolve` can tell a deferrable failure from a fatal one. |
| `internal/cli/resolve.go` | Overlay inside `materialise`; `overrideErr` on `target`; `requireOverride`. |
| `internal/cli/container.go` | Call `requireOverride` on the paths that drive up/exec; compose warning at `create`. |
| `internal/cli/agent.go` | Call `requireOverride`. |

Task order keeps `make test` green at every commit. Tasks 1–3 add the machinery
while `--mount` still runs; task 4 flips the provider over in one commit.

---

### Task 1: `dcgen.Overlay` — parse, replace, re-marshal

**Files:**
- Create: `internal/dcgen/overlay.go`
- Create: `internal/dcgen/overlay_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: `dcgen.StateDir` and `dcgen.State` (already exist in
  `internal/dcgen/state.go:12` and `internal/dcgen/render.go:70`). `State` has
  one field, `Volume string`.
- Produces:
  - `func Overlay(projectConfig []byte, state State) ([]byte, error)`
  - `func UsesCompose(projectConfig []byte) (bool, error)`

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/tailscale/hujson@latest
go mod tidy
```

If the module proxy is unreachable, stop and report it — do not hand-roll a
comment stripper. The spec rejects that explicitly: a scanner that misreads
`"postCreateCommand": "echo // not a comment"` corrupts a project's config
silently on every `up`.

- [ ] **Step 2: Write the failing tests**

Create `internal/dcgen/overlay_test.go`:

```go
package dcgen

import (
	"encoding/json"
	"strings"
	"testing"
)

// mountsOfJSON pulls the "mounts" array out of a rendered document as strings,
// for assertions. An object-form entry comes back as its compact JSON.
func mountsOfJSON(t *testing.T, doc []byte) []string {
	t.Helper()
	var parsed struct {
		Mounts []json.RawMessage `json:"mounts"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v\n%s", err, doc)
	}
	out := make([]string, 0, len(parsed.Mounts))
	for _, raw := range parsed.Mounts {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			out = append(out, s)
			continue
		}
		out = append(out, string(raw))
	}
	return out
}

// The ordinary case: a project that never heard of dev gets dev's mount added
// and everything else back unchanged.
func TestOverlayAddsTheStateMount(t *testing.T) {
	in := []byte(`{
	  "name": "demo",
	  "image": "ubuntu",
	  "features": {"ghcr.io/devcontainers/features/go:1": {}}
	}`)

	out, err := Overlay(in, State{Volume: "dev-ws-c1-state"})
	if err != nil {
		t.Fatalf("Overlay: %v", err)
	}

	want := "source=dev-ws-c1-state,target=/var/dev-state,type=volume"
	if got := mountsOfJSON(t, out); len(got) != 1 || got[0] != want {
		t.Errorf("mounts = %v, want [%s]", got, want)
	}

	// Every field dev does not touch must survive. --override-config replaces
	// rather than deep-merges, so a field dropped here is a field the container
	// loses.
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"name", "image", "features"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("Overlay dropped %q: %s", key, out)
		}
	}
}

// A project's own mounts at other targets are none of dev's business.
func TestOverlayPreservesOtherMounts(t *testing.T) {
	in := []byte(`{"mounts": ["source=cache,target=/cache,type=volume"]}`)

	out, err := Overlay(in, State{Volume: "dev-ws-c1-state"})
	if err != nil {
		t.Fatalf("Overlay: %v", err)
	}

	got := mountsOfJSON(t, out)
	if len(got) != 2 {
		t.Fatalf("mounts = %v, want 2 entries", got)
	}
	if got[0] != "source=cache,target=/cache,type=volume" {
		t.Errorf("project mount not preserved first: %v", got)
	}
	if !strings.Contains(got[1], "dev-ws-c1-state") {
		t.Errorf("dev mount not appended last: %v", got)
	}
}

// The replace rule, string spelling. Leaving both would make docker refuse the
// whole run with "duplicate mount destination" — the failure this change exists
// to remove.
func TestOverlayReplacesAStringStateMount(t *testing.T) {
	in := []byte(`{"mounts": ["source=dev-cli-state,target=/var/dev-state,type=volume"]}`)

	out, err := Overlay(in, State{Volume: "dev-ws-c1-state"})
	if err != nil {
		t.Fatalf("Overlay: %v", err)
	}

	got := mountsOfJSON(t, out)
	if len(got) != 1 {
		t.Fatalf("mounts = %v, want exactly 1 entry", got)
	}
	if strings.Contains(got[0], "dev-cli-state") {
		t.Errorf("project's state mount survived: %v", got)
	}
	if !strings.Contains(got[0], "dev-ws-c1-state") {
		t.Errorf("dev's state mount missing: %v", got)
	}
}

// The replace rule, object spelling. The CLI accepts both, so target detection
// must read both or an object-form project doubles the mount.
func TestOverlayReplacesAnObjectStateMount(t *testing.T) {
	in := []byte(`{"mounts": [{"source": "dev-cli-state", "target": "/var/dev-state", "type": "volume"}]}`)

	out, err := Overlay(in, State{Volume: "dev-ws-c1-state"})
	if err != nil {
		t.Fatalf("Overlay: %v", err)
	}

	got := mountsOfJSON(t, out)
	if len(got) != 1 {
		t.Fatalf("mounts = %v, want exactly 1 entry", got)
	}
	if strings.Contains(got[0], "dev-cli-state") {
		t.Errorf("project's state mount survived: %v", got)
	}
}

// devcontainer.json is JSONC. VS Code's own templates ship comments, and
// encoding/json rejects both of these.
func TestOverlayAcceptsJSONC(t *testing.T) {
	in := []byte(`// what this container is
{
  "name": "demo", // trailing comment
  "mounts": [
    "source=cache,target=/cache,type=volume",
  ],
}`)

	out, err := Overlay(in, State{Volume: "dev-ws-c1-state"})
	if err != nil {
		t.Fatalf("Overlay: %v", err)
	}
	if len(mountsOfJSON(t, out)) != 2 {
		t.Errorf("mounts = %v, want 2", mountsOfJSON(t, out))
	}
}

// The case a hand-rolled comment stripper gets wrong. Corrupting this line
// would change what the container runs, silently, on every up.
func TestOverlayKeepsCommentMarkersInsideStrings(t *testing.T) {
	in := []byte(`{"postCreateCommand": "echo // not a comment"}`)

	out, err := Overlay(in, State{Volume: "dev-ws-c1-state"})
	if err != nil {
		t.Fatalf("Overlay: %v", err)
	}

	var parsed struct {
		PostCreate string `json:"postCreateCommand"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.PostCreate != "echo // not a comment" {
		t.Errorf("postCreateCommand = %q, want the string intact", parsed.PostCreate)
	}
}

// dev parses a file it did not write, so it must report a file it cannot parse
// rather than return a partial document.
func TestOverlayRejectsAMalformedDocument(t *testing.T) {
	if _, err := Overlay([]byte(`{"name":}`), State{Volume: "v"}); err == nil {
		t.Error("Overlay accepted a malformed document")
	}
}

// A compose project cannot carry the mount, so create warns. Detection is free
// here because the document is already parsed.
func TestUsesCompose(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"compose", `{"dockerComposeFile": "docker-compose.yml", "service": "app"}`, true},
		{"compose list", `{"dockerComposeFile": ["a.yml", "b.yml"]}`, true},
		{"image", `{"image": "ubuntu"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UsesCompose([]byte(tc.in))
			if err != nil {
				t.Fatalf("UsesCompose: %v", err)
			}
			if got != tc.want {
				t.Errorf("UsesCompose = %v, want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/dcgen/ -run 'Overlay|UsesCompose' -v`
Expected: FAIL to build — `undefined: Overlay`, `undefined: UsesCompose`.

- [ ] **Step 4: Write the implementation**

Create `internal/dcgen/overlay.go`:

```go
package dcgen

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tailscale/hujson"
)

// Overlay merges dev's state mount into a project's own devcontainer.json.
//
// The devcontainer CLI's --override-config *replaces* the document rather than
// deep-merging it, so everything the project declared has to come back out
// again. That is why the document is decoded into json.RawMessage values:
// every field dev does not touch round-trips byte for byte, and dev stays out
// of the business of understanding a spec it does not implement.
//
// Exactly one field is rewritten. Environment already reaches both up and exec
// through --remote-env, so merging containerEnv would be a second route to a
// destination that already has one — and two mechanisms for one outcome is how
// a mount gets dropped.
//
// dev owns StateDir whenever dev is driving: an existing mount at that target is
// replaced, not kept beside. Leaving both would make docker refuse the whole run
// with "duplicate mount destination", and standing down instead would leave
// `container remove` unable to delete a volume dev did not name.
func Overlay(projectConfig []byte, state State) ([]byte, error) {
	doc, err := parseConfig(projectConfig)
	if err != nil {
		return nil, err
	}

	var existing []json.RawMessage
	if raw, ok := doc["mounts"]; ok {
		if err := json.Unmarshal(raw, &existing); err != nil {
			return nil, fmt.Errorf("reading the project's mounts: %w", err)
		}
	}

	// Kept in the project's own order, with dev's appended last, so the result
	// is deterministic and a diff between two invocations is empty.
	kept := make([]json.RawMessage, 0, len(existing)+1)
	for _, entry := range existing {
		target, err := mountTarget(entry)
		if err != nil {
			return nil, err
		}
		if target == StateDir {
			continue // replaced below
		}
		kept = append(kept, entry)
	}

	// The string spelling, matching what Render writes, so the two places dev
	// names this volume name it the same way.
	own, err := json.Marshal(fmt.Sprintf("source=%s,target=%s,type=volume", state.Volume, StateDir))
	if err != nil {
		return nil, fmt.Errorf("rendering the state mount: %w", err)
	}
	kept = append(kept, own)

	mounts, err := json.Marshal(kept)
	if err != nil {
		return nil, fmt.Errorf("rendering the merged mounts: %w", err)
	}
	doc["mounts"] = mounts

	// Indented because this file is what an operator is pointed at when a
	// container starts wrong, and two spaces cost nothing in a temporary file.
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rendering the merged configuration: %w", err)
	}
	return out, nil
}

// UsesCompose reports whether a project's document names a compose file.
//
// A `mounts` entry is not reliably applied for a compose project: the spec
// lists the property as cross-orchestrator, but mounts there belong in the
// compose file and implementations differ on whether they inject it
// (devcontainers/spec#106). The container still runs; its state volume is
// simply absent, with nothing reporting an error. The caller warns.
func UsesCompose(projectConfig []byte) (bool, error) {
	doc, err := parseConfig(projectConfig)
	if err != nil {
		return false, err
	}
	_, ok := doc["dockerComposeFile"]
	return ok, nil
}

// parseConfig decodes a devcontainer.json into its top-level fields.
//
// devcontainer.json is JSONC: comments and trailing commas are legal, the CLI
// accepts them and VS Code's own templates ship them, while encoding/json
// rejects both. hujson.Standardize normalises the syntax and nothing else — it
// is correct on a // or /* inside a string literal, where a hand-rolled
// stripper would silently corrupt the line.
func parseConfig(projectConfig []byte) (map[string]json.RawMessage, error) {
	std, err := hujson.Standardize(projectConfig)
	if err != nil {
		return nil, fmt.Errorf("reading the project's devcontainer.json: %w", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(std, &doc); err != nil {
		return nil, fmt.Errorf("reading the project's devcontainer.json: %w", err)
	}
	return doc, nil
}

// mountTarget returns where a mounts entry lands inside the container.
//
// Both spellings, because the CLI accepts both: the string form
// "source=x,target=/y,type=volume" and the object form
// {"source":"x","target":"/y"}. Reading only one would leave an object-form
// project with dev's mount added beside its own rather than replacing it, which
// is the duplicate-destination failure again.
func mountTarget(entry json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(entry, &s); err == nil {
		for _, field := range strings.Split(s, ",") {
			// target= is the documented spelling; docker's own --mount also
			// accepts destination= and dst= for the same thing.
			for _, key := range []string{"target=", "destination=", "dst="} {
				if strings.HasPrefix(field, key) {
					return strings.TrimPrefix(field, key), nil
				}
			}
		}
		return "", nil // a mount with no target is the CLI's problem, not dev's
	}

	var obj struct {
		Target      string `json:"target"`
		Destination string `json:"destination"`
	}
	if err := json.Unmarshal(entry, &obj); err != nil {
		return "", fmt.Errorf("reading a mounts entry: %w", err)
	}
	if obj.Target != "" {
		return obj.Target, nil
	}
	return obj.Destination, nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/dcgen/ -v`
Expected: PASS, including the pre-existing `dcgen` tests.

- [ ] **Step 6: Lint and commit**

```bash
make lint && make test
git add internal/dcgen/overlay.go internal/dcgen/overlay_test.go go.mod go.sum
git commit -m "feat(dcgen): merge dev's state mount into a project's devcontainer.json"
```

---

### Task 2: The unpersisted field and the optional interface

**Files:**
- Modify: `internal/model/model.go:62-83` (the `Container` struct)
- Modify: `internal/provider/provider.go`
- Modify: `internal/provider/local/local.go` (add the marker method)
- Test: `internal/provider/local/local_test.go`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces:
  - `model.Container.OverrideConfigPath string`
  - `provider.ConfigOverrider` interface with method `OverridesConfig()`
  - `(*local.Provider).OverridesConfig()`

Why an interface rather than a `model.Kind` check: the Kubernetes provider
builds its own pod spec and ignores a document's `mounts` entirely, so a merged
document is work with no effect there — and a project file that does not parse
must not block a k8s command over a document k8s never reads. Discovery by type
assertion is how `Syncer` and `AgentForwarder` already do this.

- [ ] **Step 1: Write the failing test**

Append to `internal/provider/local/local_test.go`:

```go
// The overlay is local-only. k8s builds its own pod spec and ignores a
// document's mounts, so it must not be handed a merged document — and a
// project file that does not parse must not block a k8s command.
func TestLocalProviderOverridesConfig(t *testing.T) {
	if _, ok := any(&Provider{}).(provider.ConfigOverrider); !ok {
		t.Error("local provider does not implement provider.ConfigOverrider")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/provider/local/ -run TestLocalProviderOverridesConfig -v`
Expected: FAIL to build — `undefined: provider.ConfigOverrider`.

- [ ] **Step 3: Add the field**

In `internal/model/model.go`, inside `type Container struct`, after
`PersistState` (line 81) and before `CreatedAt`:

```go
	// OverrideConfigPath is a merged devcontainer.json built for the length of
	// one invocation: the project's own document with dev's state mount in it.
	//
	// Not persisted, and so needing no migration. A stored merge would freeze a
	// snapshot of a file dev does not own — add a feature to the project's
	// devcontainer.json and a rebuild would silently use the document as it
	// stood at create. That is invariant 4's reasoning applied to a file on the
	// other side of the fence. Set by materialise, read by the local provider.
	OverrideConfigPath string
```

- [ ] **Step 4: Add the interface**

In `internal/provider/provider.go`, beside the other optional interfaces:

```go
// ConfigOverrider is a provider that consumes a merged devcontainer.json,
// through model.Container's OverrideConfigPath.
//
// Optional and discovered by type assertion, for the reason Syncer is: the
// Kubernetes provider builds its own pod spec and ignores a document's mounts
// entirely, so merging one would be work with no effect — and worse, a project
// file that does not parse would fail a k8s command over a document k8s never
// reads.
//
// The method reports nothing; its presence is the answer.
type ConfigOverrider interface {
	OverridesConfig()
}
```

- [ ] **Step 5: Implement it**

In `internal/provider/local/local.go`, after the `Provider` declaration and its
`init` (around line 37):

```go
// OverridesConfig marks this provider as one that takes a merged
// devcontainer.json. See provider.ConfigOverrider.
func (p *Provider) OverridesConfig() {}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `make lint && make test`
Expected: PASS. Nothing reads the new field yet.

- [ ] **Step 7: Commit**

```bash
git add internal/model/model.go internal/provider/provider.go internal/provider/local/local.go internal/provider/local/local_test.go
git commit -m "feat(provider): declare which providers consume a merged config"
```

---

### Task 3: Build the override in `materialise`, defer its failures

**Files:**
- Modify: `internal/cli/resolve.go:20-127`
- Modify: `internal/cli/errors.go`
- Modify: `internal/cli/container.go:830` (`a.start`), and the four paths that
  drive up or exec
- Modify: `internal/cli/agent.go:53`
- Test: `internal/cli/resolve_test.go` (create if absent)

**Interfaces:**
- Consumes: `dcgen.Overlay` (Task 1), `model.Container.OverrideConfigPath` and
  `provider.ConfigOverrider` (Task 2), the existing
  `local.StateVolumeName(workspace, container string) string`.
- Produces:
  - `func materialise(c model.Container, overrides bool) (model.Container, func(), error)`
    — **signature change**, both call sites updated
  - `target.overrideErr error` and `func (t *target) requireOverride() error`
  - `func overridesConfig(p provider.Provider) bool`

Why the failure is deferred: `materialise` runs for all eleven `resolve`
callers, but `Stop`, `Remove`, `Status` and `Logs` find their container through
docker label filters and never read `ConfigPath`. A fatal parse here would make
a syntax error in a project's file leave the container **un-removable by `dev`**.

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/resolve_test.go`:

```go
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
)

// projectContainer is a container whose project ships its own devcontainer.json
// at path, and which persists agent state.
func projectContainer(path string) model.Container {
	return model.Container{
		Name:          "c1",
		WorkspaceName: "ws",
		SourceKind:    model.SourceFolder,
		Source:        "/tmp/project",
		ConfigPath:    path,
		PersistState:  true,
	}
}

// writeProjectConfig puts a devcontainer.json on disk and returns its path.
func writeProjectConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "devcontainer.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the project config: %v", err)
	}
	return path
}

func TestMaterialiseBuildsAnOverrideForAProjectOwnedContainer(t *testing.T) {
	path := writeProjectConfig(t, `{"name":"demo","image":"ubuntu"}`)

	c, cleanup, err := materialise(projectContainer(path), true)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanup()

	if c.OverrideConfigPath == "" {
		t.Fatal("materialise built no override")
	}
	body, err := os.ReadFile(c.OverrideConfigPath)
	if err != nil {
		t.Fatalf("reading the override: %v", err)
	}
	if !strings.Contains(string(body), "dev-ws-c1-state") {
		t.Errorf("override does not name the state volume: %s", body)
	}

	// The project's own path still reaches the CLI as --config, which is what
	// keeps a relative "dockerfile" anchored to the project.
	if c.ConfigPath != path {
		t.Errorf("ConfigPath = %q, want the project's own %q", c.ConfigPath, path)
	}

	// The temp file goes with the invocation; nothing survives release.
	cleanup()
	if _, err := os.Stat(c.OverrideConfigPath); !os.IsNotExist(err) {
		t.Errorf("cleanup left the override behind: %v", err)
	}
}

// A generated document already names the volume in its own mounts. Adding an
// override too would be two mechanisms for one outcome.
func TestMaterialiseBuildsNoOverrideForAGeneratedContainer(t *testing.T) {
	c := projectContainer("")
	c.GeneratedConfig = `{"name":"demo"}`

	got, cleanup, err := materialise(c, true)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanup()

	if got.OverrideConfigPath != "" {
		t.Errorf("OverrideConfigPath = %q, want empty for a generated container",
			got.OverrideConfigPath)
	}
}

func TestMaterialiseBuildsNoOverrideWithoutPersistState(t *testing.T) {
	c := projectContainer(writeProjectConfig(t, `{"name":"demo"}`))
	c.PersistState = false

	got, cleanup, err := materialise(c, true)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanup()

	if got.OverrideConfigPath != "" {
		t.Errorf("OverrideConfigPath = %q, want empty when state is off",
			got.OverrideConfigPath)
	}
}

// k8s ignores a document's mounts, so it is never handed an override.
func TestMaterialiseBuildsNoOverrideWhenTheProviderIgnoresIt(t *testing.T) {
	c := projectContainer(writeProjectConfig(t, `{"name":"demo"}`))

	got, cleanup, err := materialise(c, false)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanup()

	if got.OverrideConfigPath != "" {
		t.Errorf("OverrideConfigPath = %q, want empty for a provider that ignores it",
			got.OverrideConfigPath)
	}
}

// The deferred failure. A project file dev cannot parse must not strand the
// container in the engine: stop, remove, logs and status never read it.
func TestMaterialiseDefersAParseFailure(t *testing.T) {
	path := writeProjectConfig(t, `{"name":}`)

	c, cleanup, err := materialise(projectContainer(path), true)
	defer cleanup()

	if err == nil {
		t.Fatal("materialise accepted a malformed project config")
	}
	if !isOverlayError(err) {
		t.Errorf("error is not deferrable: %v", err)
	}
	// The container is still usable for the commands that do not need the
	// override — Remove finds it by docker label, not by config.
	if c.Name != "c1" {
		t.Errorf("materialise lost the container on a parse failure: %+v", c)
	}
}

// The merge runs per invocation, so an edit to the project's file between two
// commands is picked up by the second. A stored merge would freeze a snapshot.
func TestMaterialiseRereadsTheProjectConfig(t *testing.T) {
	path := writeProjectConfig(t, `{"name":"demo"}`)

	first, cleanupFirst, err := materialise(projectContainer(path), true)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanupFirst()

	if err := os.WriteFile(path, []byte(`{"name":"demo","image":"alpine"}`), 0o600); err != nil {
		t.Fatalf("editing the project config: %v", err)
	}

	second, cleanupSecond, err := materialise(projectContainer(path), true)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanupSecond()

	body, err := os.ReadFile(second.OverrideConfigPath)
	if err != nil {
		t.Fatalf("reading the override: %v", err)
	}
	var parsed struct {
		Image string `json:"image"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Image != "alpine" {
		t.Errorf("second override did not pick up the edit: %s", body)
	}
	_ = first
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/cli/ -run Materialise -v`
Expected: FAIL to build — `too many arguments in call to materialise`,
`undefined: isOverlayError`.

- [ ] **Step 3: Add the deferrable error type**

In `internal/cli/errors.go`:

```go
// overlayError is a failure to build the merged devcontainer.json.
//
// Distinguishable from any other failure because it is deferrable: materialise
// runs for every container command, but stop, remove, logs and status find
// their container through docker label filters and never read a config at all.
// Failing those too would leave a syntax error in a project's file holding a
// container hostage in the engine, with dev refusing to clean up after itself.
//
// Exit 1, like any other runtime failure: the request was well-formed and the
// container exists.
type overlayError struct{ err error }

func (e *overlayError) Error() string { return e.err.Error() }
func (e *overlayError) Unwrap() error { return e.err }

// isOverlayError reports whether err is a deferrable overlay failure.
func isOverlayError(err error) bool {
	var oe *overlayError
	return errors.As(err, &oe)
}
```

Add `"errors"` to that file's imports if it is not already there.

- [ ] **Step 4: Rewrite `materialise` and `target`**

In `internal/cli/resolve.go`, add to the `target` struct (after `cleanup`):

```go
	// overrideErr is a failure to build the merged devcontainer.json, held
	// rather than returned. The commands that drive up or exec surface it
	// through requireOverride; the ones that do not need it carry on, so a
	// project file that does not parse never strands a container in the engine.
	overrideErr error
```

Add beside `release`:

```go
// requireOverride reports a deferred overlay failure.
//
// Called by every path that drives up or exec, and by no other: those are the
// commands that need the merged document, and starting a container without it
// would silently leave the agents' state volume unmounted.
func (t *target) requireOverride() error {
	return t.overrideErr
}

// overridesConfig reports whether this provider consumes a merged config.
func overridesConfig(p provider.Provider) bool {
	_, ok := p.(provider.ConfigOverrider)
	return ok
}
```

Replace the `materialise` call inside `resolve` (currently line 70):

```go
	// Done here rather than in each command: every provider call needs a
	// container whose ConfigPath a child process can open, and a command that
	// forgot would fail only for generated containers.
	c, cleanup, err := materialise(c, overridesConfig(p))
	if err != nil {
		if !isOverlayError(err) {
			return nil, err
		}
		// Deferred, not fatal. See target.overrideErr.
		return &target{container: c, workspace: ws, provider: p, cleanup: cleanup,
			overrideErr: err}, nil
	}
	return &target{container: c, workspace: ws, provider: p, cleanup: cleanup}, nil
```

Replace the head of `materialise` (currently lines 91-94) with:

```go
// overrides says whether this container's provider consumes a merged
// devcontainer.json. False for Kubernetes, which builds its own pod spec.
func materialise(c model.Container, overrides bool) (model.Container, func(), error) {
	if c.GeneratedConfig == "" {
		return overlayProjectConfig(c, overrides)
	}
```

and add, after `materialise` ends:

```go
// overlayProjectConfig merges dev's state mount into a project's own
// devcontainer.json for the length of one invocation.
//
// Per invocation rather than stored: a project-owned container picks up edits
// to its own devcontainer.json today because the CLI reads the live file, and a
// merge cached at create would freeze a snapshot — add a feature, rebuild, and
// get the document as it stood weeks ago with no error to explain it.
// GeneratedConfig is the wrong home for a second reason: rewriteGeneratedTools
// re-renders it from the tool list and would destroy a project-derived copy.
//
// Nothing is written for a container that does not persist state, has no config
// of its own, or runs on a provider that ignores a document's mounts.
func overlayProjectConfig(c model.Container, overrides bool) (model.Container, func(), error) {
	if !overrides || !c.PersistState || c.ConfigPath == "" {
		return c, func() {}, nil
	}

	raw, err := os.ReadFile(c.ConfigPath)
	if err != nil {
		return c, func() {}, &overlayError{fmt.Errorf("reading %s: %w", c.ConfigPath, err)}
	}
	// The same helper the generated document uses, so the volume has one
	// spelling across create, up and remove.
	merged, err := dcgen.Overlay(raw, dcgen.State{
		Volume: local.StateVolumeName(c.WorkspaceName, c.Name),
	})
	if err != nil {
		return c, func() {}, &overlayError{fmt.Errorf("reading %s: %w", c.ConfigPath, err)}
	}

	dir, err := os.MkdirTemp("", "dev-override-")
	if err != nil {
		return c, func() {}, fmt.Errorf("preparing the merged config: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	// Flat rather than inside a .devcontainer directory: --override-config takes
	// a document, while --config is what names the project's own file and keeps
	// its path anchoring. 0600 for the reason the generated config uses it.
	path := filepath.Join(dir, "devcontainer.json")
	if err := os.WriteFile(path, merged, 0o600); err != nil {
		cleanup()
		return c, func() {}, fmt.Errorf("writing the merged config: %w", err)
	}

	c.OverrideConfigPath = path
	return c, cleanup, nil
}
```

Add `"github.com/duy0611/dev-cli/internal/provider/local"` to the imports of
`resolve.go`. (`internal/cli/generate.go` already imports it, so there is no
cycle.)

- [ ] **Step 5: Update the second `materialise` call site**

`a.start` in `internal/cli/container.go:830` calls `materialise` directly — the
create path builds its own container value and never goes through `resolve`.
`start` always drives `Up`, so it needs the override and fails hard:

```go
	// The create path builds its own container value and never goes through
	// resolve, so a generated configuration has to become a file here too — and
	// the merged one has to be built here too. Fatal rather than deferred: this
	// function exists to start the container, and starting it without the
	// merged document would leave the state volume silently unmounted.
	c, cleanup, err := materialise(c, overridesConfig(p))
	if err != nil {
		return err
	}
	defer cleanup()
```

- [ ] **Step 6: Call `requireOverride` on the four paths that need it**

Add `if err := t.requireOverride(); err != nil { return err }` immediately after
`defer t.release()` in each of:

| File | Function | Why |
| --- | --- | --- |
| `internal/cli/container.go:948` | `newContainerRebuildCmd`'s `RunE` | drives `Rebuild`, which is `up` |
| `internal/cli/container.go:1079` | `runContainerShell` | drives `exec` |
| `internal/cli/container.go:1157` | `runContainerExec` | drives `exec` |
| `internal/cli/agent.go:53` | `runContainerStartAgent` | drives `Up` and `exec` |

Do **not** add it to `runContainerStop`, `runContainerRemove`,
`newContainerLogsCmd`, `runContainerConfigShow`, `runContainerSync`, or
`rewriteGeneratedTools`. Those either never read a config or, in sync's case,
exist only on k8s, which never has an override to fail at.

`newContainerStartCmd` needs no call either: it hands off to `a.start`, which
fails hard on the same condition.

- [ ] **Step 7: Run the tests to verify they pass**

Run: `make lint && make test`
Expected: PASS. `--mount` is still passed by the provider, so container
behaviour has not changed yet.

- [ ] **Step 8: Commit**

```bash
git add internal/cli/
git commit -m "feat(cli): merge the project's devcontainer.json per invocation"
```

---

### Task 4: Pass `--override-config`, drop `--mount`

**Files:**
- Modify: `internal/provider/local/local.go:52-96` (`up`) and `:157-168` (`execArgs`)
- Modify: `internal/provider/local/local_test.go:447-503`

**Interfaces:**
- Consumes: `model.Container.OverrideConfigPath` (Task 2), set by `materialise`
  (Task 3).
- Produces: no new symbols. `up` and `execArgs` both carry
  `--override-config <path>` when the field is set.

This is the commit that changes behaviour. `--override-config` exists on **both**
`up` and `exec`, which is what removes the asymmetry invariant 10 currently has
to warn about.

- [ ] **Step 1: Rewrite the four `--mount` tests**

In `internal/provider/local/local_test.go`, replace lines 447-503 — the four
tests `TestUpMountsTheStateVolumeForAProjectOwnedContainer`,
`TestUpDoesNotMountStateForAGeneratedContainer`,
`TestUpDoesNotMountStateWhenOff` and `TestExecNeverPassesMount` — with:

```go
// overrideContainer is a project-owned, state-persisting container whose merged
// configuration materialise has already written.
func overrideContainer() model.Container {
	c := stateContainer()
	c.ConfigPath = "/project/.devcontainer/devcontainer.json"
	c.OverrideConfigPath = "/tmp/dev-override-x/devcontainer.json"
	return c
}

// The merged document reaches the CLI, and --config still names the project's
// own file: both are accepted together, and --config is what keeps a relative
// "dockerfile" anchored to the project rather than to the temporary directory.
func TestUpPassesTheOverrideConfig(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	c := overrideContainer()
	if err := (&Provider{}).Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	if !contains(argv, []string{"--override-config", c.OverrideConfigPath}) {
		t.Errorf("argv %v is missing the override config", argv)
	}
	if !contains(argv, []string{"--config", c.ConfigPath}) {
		t.Errorf("argv %v dropped the project's own --config", argv)
	}
}

// up and exec must carry the *same* merged document. This is the same shape of
// mistake as the id labels: two commands that disagree reach two different
// containers, or one reaches a container with no state volume, and nothing
// reports an error. --override-config exists on both, unlike --mount, which is
// what lets the two paths finally agree.
func TestUpAndExecAgreeOnTheOverrideConfig(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	c := overrideContainer()
	if err := (&Provider{}).Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	upArgs := f.argv(t, devcontainerBin)
	execArgs := (&Provider{}).execArgs(c, []string{"true"})

	want := []string{"--override-config", c.OverrideConfigPath}
	if !contains(upArgs, want) {
		t.Errorf("up argv %v is missing %v", upArgs, want)
	}
	if !contains(execArgs, want) {
		t.Errorf("exec argv %v is missing %v", execArgs, want)
	}
}

// --mount is gone. It existed on up and not on exec, which is the asymmetry the
// override removes; leaving it would also make docker refuse the run with
// "duplicate mount destination" now that the merged document names the volume.
func TestUpNeverPassesMount(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	if err := (&Provider{}).Up(context.Background(), overrideContainer(), nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if contains(f.argv(t, devcontainerBin), []string{"--mount"}) {
		t.Errorf("up still passes --mount: %v", f.argv(t, devcontainerBin))
	}
}

// A generated document already names the volume in its own mounts, so
// materialise builds no override for it and neither flag appears.
func TestUpPassesNoOverrideForAGeneratedContainer(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	c := stateContainer()
	c.GeneratedConfig = `{"name":"demo","mounts":["source=dev-ws-demo-state,target=/var/dev-state,type=volume"]}`
	if err := (&Provider{}).Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	for _, flag := range []string{"--override-config", "--mount"} {
		if contains(argv, []string{flag}) {
			t.Errorf("up passed %s for a generated container: %v", flag, argv)
		}
	}
}

// Nothing to merge, nothing to pass.
func TestUpPassesNoOverrideWhenStateIsOff(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	if err := (&Provider{}).Up(context.Background(), testContainer(), nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if contains(f.argv(t, devcontainerBin), []string{"--override-config"}) {
		t.Errorf("Up passed an override for a container that persists no state: %v",
			f.argv(t, devcontainerBin))
	}
}

func TestExecNeverPassesMount(t *testing.T) {
	argv := (&Provider{}).execArgs(overrideContainer(), []string{"true"})
	if contains(argv, []string{"--mount"}) {
		t.Errorf("exec argv carries --mount: %v", argv)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/provider/local/ -run 'Override|Mount' -v`
Expected: FAIL — `TestUpPassesTheOverrideConfig` reports a missing
`--override-config`, and `TestUpNeverPassesMount` reports that `up` still
passes `--mount`.

- [ ] **Step 3: Change `up`**

In `internal/provider/local/local.go`, **delete** the `--mount` block at lines
69-83 (the comment and the `if c.PersistState && c.GeneratedConfig == ""`
branch), and add after the `--config` block:

```go
	// The project's own document with dev's state mount merged into it, built
	// per invocation by materialise. --override-config *replaces* the document
	// rather than deep-merging it, which is why the merge happens in dev rather
	// than being left to the CLI.
	//
	// Passed here and in execArgs both: unlike --mount, which existed only on
	// up, this flag exists on both commands — so the two paths finally describe
	// the same container rather than one of them quietly omitting the volume.
	// --config stays alongside; both are accepted, and it is what keeps a
	// relative "dockerfile" anchored to the project.
	if c.OverrideConfigPath != "" {
		args = append(args, "--override-config", c.OverrideConfigPath)
	}
```

- [ ] **Step 4: Change `execArgs`**

In the same file, after the `--config` block inside `execArgs`:

```go
	// The same merged document up was given. A container started with the state
	// volume and exec'd without it would see an empty /var/dev-state, with
	// nothing reporting an error.
	if c.OverrideConfigPath != "" {
		args = append(args, "--override-config", c.OverrideConfigPath)
	}
```

- [ ] **Step 5: Update the `claimStateDir` comment**

`claimStateDir` is unchanged in behaviour — the volume is still created
root-owned and still gets no UID remapping. Its doc comment references the
mechanism, so correct that one sentence: the volume now arrives through the
merged document rather than through `--mount`, and there is no case where the
project takes responsibility for the chown instead.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `make lint && make test`
Expected: PASS, including the pre-existing test that `up` and `exec` agree on
id labels.

- [ ] **Step 7: Commit**

```bash
git add internal/provider/local/
git commit -m "feat(local): pass the merged config on up and exec, drop --mount"
```

---

### Task 5: Warn at `create` for a compose project

**Files:**
- Modify: `internal/cli/container.go` (the `create` path, around lines 139-190)
- Test: `internal/cli/container_test.go` (or the existing `generate_test.go`
  harness, whichever the create tests already live in)

**Interfaces:**
- Consumes: `dcgen.UsesCompose` (Task 1), the existing `warnf(a *app, format
  string, args ...any)` from `internal/cli/print.go:35`.
- Produces: `func composeWarning(configPath string, persistState bool) string`
  — the message, or `""` when there is nothing to say.

A compose project's `mounts` entry is not reliably applied. The container is
still created and still runs — everything except state persistence works — so
this warns rather than refusing. Advisory only: nothing is stored, because a
project can gain or lose a compose file later and runtime re-derives it.

**Why a function that returns the message rather than one that prints it:**
`warnf` writes to `os.Stderr` (`internal/cli/print.go:36`), which no test in
`internal/cli` captures. Returning the string makes the decision testable
without redirecting a global; `create` does the printing.

- [ ] **Step 1: Write the failing tests**

Add to `internal/cli/generate_test.go`, which is where the `create` tests
already live. `newTestApp(t) (*app, *bytes.Buffer)` is in
`internal/cli/provider_test.go:17`; the create path is called directly as
`runContainerCreate(t.Context(), a, "", "demo", folder, createOpts{})`.

```go
// writeProjectConfigIn puts a .devcontainer/devcontainer.json in dir.
func writeProjectConfigIn(t *testing.T, dir, body string) {
	t.Helper()
	nested := filepath.Join(dir, ".devcontainer")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "devcontainer.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing the project config: %v", err)
	}
}

// A compose project cannot carry the state mount, and nothing reports an error
// when it silently does not: the agents write to the container filesystem and
// lose it at the next rebuild.
func TestComposeWarningForAComposeProject(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfigIn(t, dir, `{"dockerComposeFile":"docker-compose.yml","service":"app"}`)
	path := filepath.Join(dir, ".devcontainer", "devcontainer.json")

	got := composeWarning(path, true)
	if got == "" {
		t.Fatal("no warning for a compose project")
	}
	// The way out has to be in the message, in the house style of the
	// kind-change error: an operator who is not told what to do has nothing to
	// try.
	for _, want := range []string{"docker compose", dcgen.StateDir, "--no-persist-state"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning %q does not mention %q", got, want)
		}
	}
}

func TestComposeWarningSilentForAnImageProject(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfigIn(t, dir, `{"image":"ubuntu"}`)
	path := filepath.Join(dir, ".devcontainer", "devcontainer.json")

	if got := composeWarning(path, true); got != "" {
		t.Errorf("warned about a non-compose project: %q", got)
	}
}

// Nothing to lose, nothing to warn about.
func TestComposeWarningSilentWithoutPersistState(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfigIn(t, dir, `{"dockerComposeFile":"docker-compose.yml"}`)
	path := filepath.Join(dir, ".devcontainer", "devcontainer.json")

	if got := composeWarning(path, false); got != "" {
		t.Errorf("warned for a container that persists no state: %q", got)
	}
}

// A generated container has no project config, and an unreadable one is the
// business of the commands that need it.
func TestComposeWarningSilentWithoutAConfig(t *testing.T) {
	if got := composeWarning("", true); got != "" {
		t.Errorf("warned with no config path: %q", got)
	}
	if got := composeWarning("/nonexistent/devcontainer.json", true); got != "" {
		t.Errorf("warned for an unreadable config: %q", got)
	}
}

// Still created: the warning is advisory, not a refusal.
func TestCreateSucceedsForAComposeProject(t *testing.T) {
	a, _ := newTestApp(t)
	folder := t.TempDir()
	writeProjectConfigIn(t, folder, `{"dockerComposeFile":"docker-compose.yml","service":"app"}`)

	if err := runContainerCreate(t.Context(), a, "", "demo", folder,
		createOpts{noStart: true}); err != nil {
		t.Fatalf("create refused a compose project: %v", err)
	}
}
```

Check `createOpts`' field for skipping the start — the plan assumes `noStart`;
read the struct at `internal/cli/container.go:58` and use whatever it is called.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/cli/ -run Compose -v`
Expected: FAIL to build — `undefined: composeWarning`.

- [ ] **Step 3: Implement it**

In `internal/cli/container.go`, beside the other create helpers:

```go
// composeWarning says why a compose project will not get its state volume, or
// returns "" when there is nothing to say.
//
// `mounts` is documented as a cross-orchestrator property, but mounts under
// compose belong in the compose file and implementations differ on whether they
// inject it (devcontainers/spec#106). The container still runs; its state
// volume is simply absent, and nothing anywhere reports an error — which is the
// only reason this is worth a line of output.
//
// Returns the message rather than printing it so the decision can be tested:
// warnf writes to stderr, which no test here captures.
//
// A config it cannot read or parse is silent. That is reported by the commands
// that actually need the document, and refusing here would be a second, earlier
// answer to the same question.
func composeWarning(configPath string, persistState bool) string {
	if configPath == "" || !persistState {
		return ""
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	compose, err := dcgen.UsesCompose(raw)
	if err != nil || !compose {
		return ""
	}
	return fmt.Sprintf("this project uses docker compose; the devcontainer CLI will not "+
		"apply dev's %s mount. Agent state will not survive a rebuild. Add the volume "+
		"to your compose file, or pass --no-persist-state.", dcgen.StateDir)
}
```

- [ ] **Step 4: Call it from `create`**

In `runContainerCreate` (`internal/cli/container.go:105`), after `configPath` is
resolved and before the container row is built:

```go
	// Advisory, and nothing is stored: the project can gain or lose a compose
	// file later, so every invocation re-derives this.
	if msg := composeWarning(configPath, !opts.noPersistState); msg != "" {
		warnf(a, "%s", msg)
	}
```

- [ ] **Step 4: Run them to verify they pass**

Run: `make lint && make test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/
git commit -m "feat(cli): warn that a compose project cannot carry the state mount"
```

---

### Task 6: Documentation

**Files:**
- Modify: `CLAUDE.md` (invariant 10)
- Modify: `docs/USAGE.md`

**Interfaces:**
- Consumes: everything above. Produces no code.

A change to a command's surface is not finished until `docs/USAGE.md` matches
it. Invariant 10 currently documents a mechanism that no longer exists.

- [ ] **Step 1: Rewrite the two-mechanisms paragraph in invariant 10**

In `CLAUDE.md`, invariant 10's final mechanism paragraph currently reads that a
project-owned container gets the volume from `devcontainer up --mount`, which
exists on `up` and not on `exec`, so it must never enter `execArgs`. Replace it
with the current rule:

- A generated document carries `mounts` and `containerEnv`, as before.
- A project-owned container gets the volume from a **merged document**: `dev`
  reads the project's `devcontainer.json`, replaces any `mounts` entry targeting
  `/var/dev-state` with its own, writes a temporary file and passes
  `--override-config` on **both** `up` and `exec`. The up/exec asymmetry is gone.
- `--override-config` replaces rather than deep-merges, which is why `dev` does
  the merge itself and why every untouched field must round-trip.
- The merge is per invocation and never stored, for invariant 4's reason applied
  to a file `dev` does not own.
- `dev` owns `/var/dev-state` while it drives: an existing mount there is
  replaced, so `Remove` stays unconditionally correct. Standing down instead
  would orphan one volume per container.
- The `duplicate mount destination` hazard stays in the text — it is still what
  makes the generated-document branch necessary — restated as a collision `dev`
  now resolves rather than risks.
- A compose project does not get the volume; `create` warns.
- Variables still ride `--remote-env` and so reach both commands. k8s ignores all
  of it and grows a PVC subPath instead.

- [ ] **Step 2: Add the USAGE.md section**

Near the existing state-persistence material (around `docs/USAGE.md:249` and
`:369`), add a short section covering:

- Shipping a `devcontainer.json` that works under both VS Code and `dev`.
- While `dev` drives, it replaces any mount at `/var/dev-state` with its own
  per-container volume, so a name committed in the project file is what VS Code
  uses and nothing else.
- A compose project does not get the volume; add it to the compose file or pass
  `--no-persist-state`.
- Plainly: **a VS Code-launched container gets no workspace settings.** Invariant
  3 resolves specs per invocation in `internal/secret`, and no field in a
  `devcontainer.json` can call that resolver. The container starts, the agents
  have their state volume, and the secrets are absent.

- [ ] **Step 3: Verify and commit**

```bash
make lint && make test
git add CLAUDE.md docs/USAGE.md
git commit -m "docs: record the merged-config mechanism and its boundary"
```

---

## Verification against a real engine

Not part of `make test` and not runnable in this environment — neither
`devcontainer` nor `docker` is installed here. Run this on a host that has both
before calling the work done, and report the actual output rather than the
expected output.

```sh
DEV_STATE=$(mktemp -d) dist/dev workspace create t
DEV_STATE=… dist/dev container create c1 --folder $PWD
DEV_STATE=… dist/dev container start c1
DEV_STATE=… dist/dev container exec c1 -- sh -c 'echo $CLAUDE_CONFIG_DIR; mount | grep dev-state'
```

1. Expect `/var/dev-state/claude` and a mount line, with **no**
   `duplicate mount destination` — run against this repository's own folder,
   which is the case that fails today.
2. `docker volume ls` shows `dev-t-c1-state`, not `dev-cli-state`.
3. `dev container remove c1` deletes it.
4. Add a `"features"` entry to the project's `devcontainer.json`, run
   `dev container rebuild c1`, and confirm the feature is present — proving
   nothing was cached at create.
5. Open the repository in VS Code with "Reopen in Container": it starts with
   `/var/dev-state` mounted and no `dev` process involved.

`.devcontainer/devcontainer.json` is **not** edited by this work. That it keeps
working unchanged, under both tools, is the point.

---

## Self-review

**Spec coverage.** Every section of `docs/specs/2026-09-21-devcontainer-json-merge.md`
maps to a task: the mechanism and the CLI facts → Tasks 1 and 4; mounts-only
scope → Task 1; replace-not-stand-down → Task 1; per-invocation → Task 3;
where it happens → Task 3; JSONC parsing → Task 1; deferred failure → Task 3;
compose → Tasks 1 and 5; the provider → Task 4; the boundary and this
repository's own file → Task 6. The spec's "Files" table lists
`internal/cli/container.go` for both the deferred-error surface and the compose
warning; those are Tasks 3 and 5 respectively.

**Placeholders.** None: every code step carries the actual code, every test step
the actual test, every run step the exact command and expected result.

**Type consistency.** `Overlay(projectConfig []byte, state State) ([]byte, error)`
and `UsesCompose([]byte) (bool, error)` are defined in Task 1 and used with those
signatures in Tasks 3 and 5. `materialise(c model.Container, overrides bool)` is
changed in Task 3 and both call sites — `resolve` and `a.start` — are updated in
the same task. `OverrideConfigPath` is added in Task 2, written in Task 3, read
in Task 4. `overridesConfig`/`isOverlayError`/`requireOverride` are each defined
once, in Task 3. `local.StateVolumeName` and `dcgen.StateDir` are pre-existing
and spelled one way throughout.

**Verified against the tree** while reviewing, rather than assumed:
`newTestApp` is `internal/cli/provider_test.go:17` and returns
`(*app, *bytes.Buffer)`; the create tests call `runContainerCreate` directly
(`internal/cli/generate_test.go:53`); `warnf` writes to `os.Stderr`
(`internal/cli/print.go:36`), which is why Task 5 tests a function that returns
the message; `contains` is `internal/provider/local/local_test.go:116`;
`Syncer` and `AgentForwarder` are at `internal/provider/provider.go:75` and
`:91`, which is the pattern Task 2 follows; `internal/cli/errors.go` already
imports `errors`.

**Known soft spots for the implementer.** Two, both flagged where they bite:
`createOpts`' skip-start field name in Task 5 must be read off
`internal/cli/container.go:58` rather than assumed; and `a.start` materialises a
second time for a container that `resolve` already materialised, which is
pre-existing behaviour left alone here.
