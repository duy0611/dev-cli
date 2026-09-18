# Folderless Containers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `dev container create NAME --no-folder` build a devcontainer with no host folder, whose work lives in a docker named volume on the local provider and in the existing PVC on k8s.

**Architecture:** Neither provider learns what a folderless container is. A new `model.SourceNone` kind stores an empty `Source`; `dcgen.Render` adds `workspaceFolder` and `workspaceMount` to the generated configuration; `materialise` in `internal/cli/resolve.go` sets `Source` to the temporary directory it already creates for the config. The providers receive a `model.Container` they can already handle.

**Tech Stack:** Go 1.x, cobra, SQLite (`modernc.org/sqlite`), the `devcontainer` CLI and `docker`/`kubectl` driven as subprocesses. Tests use stub executables on a temporary `PATH`, never mocks.

**Spec:** `docs/specs/2026-09-18-folderless-containers.md`

## Global Constraints

- Volume name format, exactly: `dev-<workspace>-<container>`. Built in `internal/provider/local/label.go` and nowhere else.
- In-container workspace path for a folderless container, exactly: `/workspaces/<container name>`.
- `workspaceMount` string, exactly: `source=<volume>,target=<workspaceFolder>,type=volume`.
- Exit codes: `2` for a malformed request (`usageErrorf`), `3` for something named that does not exist (`notFoundErrorf`). Never cobra's own arg validators — use `exactArgs`/`noArgs`/`minArgs`.
- No migration. `source TEXT NOT NULL` already accepts `""`.
- `dev` never writes into a project folder.
- Every non-obvious line carries a comment saying *why*, naming the failure it prevents. Match the density of the surrounding code.
- Commits carry no `Co-Authored-By` trailer and no tooling-attribution line.
- `make lint && make test` must pass at the end of every task. `make lint` fails on any gofmt diff.
- `make test` must never reach for a container engine or the network.
- No renaming courtesies: this project is unpublished with one operator. No aliases, no deprecation notes.

---

### Task 1: The `none` source kind

Adds the model value and its storage round-trip. Nothing reads it yet.

**Files:**
- Modify: `internal/model/model.go:44-62`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Produces: `model.SourceNone SourceKind = "none"`. A folderless container is `model.Container{SourceKind: model.SourceNone, Source: ""}`.

- [ ] **Step 1: Write the failing test**

Append to `internal/store/store_test.go`:

```go
// A folderless container stores an empty source. The column is NOT NULL, not
// non-empty, and nothing on the way through may turn "" into a default.
func TestContainerRoundTripsAnEmptySource(t *testing.T) {
	s := openTest(t)
	seed(t, s)

	want := model.Container{
		Name: "scratch", WorkspaceName: "ws",
		SourceKind:      model.SourceNone,
		Source:          "",
		GeneratedConfig: `{"name":"scratch"}`,
	}
	if err := s.CreateContainer(want); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}

	got, err := s.GetContainer("ws", "scratch")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if got.SourceKind != model.SourceNone {
		t.Errorf("SourceKind = %q, want %q", got.SourceKind, model.SourceNone)
	}
	if got.Source != "" {
		t.Errorf("Source = %q, want empty", got.Source)
	}
}
```

`openTest` and `seed` are the existing helpers at the top of that file: a store on a temp file (never `:memory:`, which is per-connection), and a provider plus a workspace named `ws`. Do not add new helpers.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/store/ -run TestContainerRoundTripsAnEmptySource -v`
Expected: FAIL to compile — `undefined: model.SourceNone`.

- [ ] **Step 3: Add the model value**

In `internal/model/model.go`, inside the existing `SourceKind` const block:

```go
const (
	// SourceFolder is a directory on the host that ships its own
	// .devcontainer configuration.
	SourceFolder SourceKind = "folder"
	// SourceNone is a container with no host folder at all. Its work lives in
	// a volume the provider owns — a docker named volume on local, the PVC on
	// k8s — because there is no host directory to bind-mount.
	SourceNone SourceKind = "none"
)
```

And update the `Source` field comment on `model.Container`:

```go
	Source string // absolute physical host path when SourceKind is folder, empty when none
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/store/ -run TestContainerRoundTripsAnEmptySource -v`
Expected: PASS

- [ ] **Step 5: Run the full check**

Run: `make lint && make test`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add internal/model/model.go internal/store/store_test.go
git commit -m "feat(model): add the none source kind"
```

---

### Task 2: Render a volume mount into the generated configuration

`dcgen.Render` gains a mount parameter. This is the task that produces the JSON both providers read.

**Files:**
- Modify: `internal/dcgen/render.go:52-77`
- Modify: `internal/cli/generate.go:64` and `internal/cli/generate.go:148` (call sites, to keep the build green)
- Test: `internal/dcgen/dcgen_test.go`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces:

```go
// Mount says where a generated container keeps its work. The zero value means
// the devcontainer CLI's own default, which is a bind mount of the folder.
type Mount struct {
	// Volume is the name of a volume to mount instead of a host folder. Empty
	// for a folder-backed container.
	Volume string
	// Folder is the path the volume is mounted at inside the container.
	Folder string
}

func Render(name string, toolIDs []string, mount Mount) (string, error)
```

  Later tasks call `dcgen.Render(name, tools, dcgen.Mount{})` for a folder container and `dcgen.Render(name, tools, mount)` for a folderless one.

- [ ] **Step 1: Write the failing tests**

Append to `internal/dcgen/dcgen_test.go`:

```go
// A folderless container has no host directory to bind-mount, so the document
// has to name a volume and the path it appears at.
//
// workspaceFolder is not optional here. Left unset, the CLI derives one from
// the basename of --workspace-folder, which for a folderless container is a
// per-invocation temporary directory: the in-container path would change on
// every command, and on k8s the PVC would mount somewhere new each time.
func TestRenderWithAVolumeNamesBothFields(t *testing.T) {
	got, err := Render("scratch", nil, Mount{Volume: "dev-ws-scratch", Folder: "/workspaces/scratch"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	want := `{
  "image": "mcr.microsoft.com/devcontainers/base:1-ubuntu-24.04",
  "name": "scratch",
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
// override that bind mount with nothing.
func TestRenderWithoutAVolumeOmitsBothFields(t *testing.T) {
	got, err := Render("demo", nil, Mount{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, key := range []string{"workspaceMount", "workspaceFolder"} {
		if strings.Contains(got, key) {
			t.Errorf("a folder container's document carries %s:\n%s", key, got)
		}
	}
}

// rebuild --tools re-renders from scratch. A round trip that drops the mount
// would leave the next up creating a fresh empty workspace in the container
// filesystem, with the volume still there and no longer referenced.
func TestToolsOfSurvivesAVolumeDocument(t *testing.T) {
	cfg, err := Render("scratch", []string{"yq"}, Mount{Volume: "dev-ws-scratch", Folder: "/workspaces/scratch"})
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dcgen/ -run 'TestRenderWith|TestToolsOfSurvives' -v`
Expected: FAIL to compile — `undefined: Mount` and "too many arguments in call to Render".

- [ ] **Step 3: Implement the parameter**

In `internal/dcgen/render.go`, above `Render`:

```go
// Mount says where a generated container keeps its work.
//
// The zero value means the devcontainer CLI's own default: a bind mount of the
// workspace folder, which is what a folder-backed container wants. A
// folderless one has no host directory to bind, so it names a volume instead.
type Mount struct {
	// Volume is the name of a volume to mount. Empty for a folder container.
	Volume string
	// Folder is where the volume appears inside the container.
	Folder string
}
```

Then change `Render`'s signature and body:

```go
func Render(name string, toolIDs []string, mount Mount) (string, error) {
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
	if mount.Volume != "" {
		// Both, never one. workspaceMount alone leaves the CLI deriving the
		// in-container path from the host directory's basename, which for a
		// folderless container is a temporary directory with a different name
		// every invocation.
		doc["workspaceFolder"] = mount.Folder
		doc["workspaceMount"] = fmt.Sprintf("source=%s,target=%s,type=volume", mount.Volume, mount.Folder)
	}
	if features := featuresFor(ids); len(features) > 0 {
		doc["features"] = features
	}
	...
```

Leave the rest of the function as it is. `fmt` is already imported.

- [ ] **Step 4: Fix the two call sites so the package builds**

In `internal/cli/generate.go`, both `dcgen.Render(...)` calls take a third argument. For now pass the zero value at both — Task 4 gives them the real one:

```go
	config, err := dcgen.Render(name, resolved, dcgen.Mount{})
```

```go
	config, err := dcgen.Render(name, next, dcgen.Mount{})
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/dcgen/ -v`
Expected: PASS, including the pre-existing `TestRenderIsExactAndStable` — the zero `Mount` must not change a folder container's document by one byte.

- [ ] **Step 6: Run the full check**

Run: `make lint && make test`
Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add internal/dcgen/ internal/cli/generate.go
git commit -m "feat(dcgen): render a volume mount for a folderless container"
```

---

### Task 3: The volume name

One function, one spelling. Isolated from Task 2 because it lives in the local provider and Task 4 and Task 6 both call it.

**Files:**
- Modify: `internal/provider/local/label.go`
- Test: `internal/provider/local/label_test.go` (create)

**Interfaces:**
- Produces: `func VolumeName(workspace, container string) string`, returning `dev-<workspace>-<container>`. Exported because `internal/cli` calls it when rendering the configuration.

- [ ] **Step 1: Write the failing test**

Create `internal/provider/local/label_test.go`:

```go
package local

import "testing"

// The volume outlives the container and is removed by name. A second spelling
// anywhere would leak it: `docker volume rm` would miss, and the next create
// would mount a volume nothing else refers to.
func TestVolumeName(t *testing.T) {
	if got, want := VolumeName("ws", "scratch"), "dev-ws-scratch"; got != want {
		t.Errorf("VolumeName = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/provider/local/ -run TestVolumeName -v`
Expected: FAIL to compile — `undefined: VolumeName`.

- [ ] **Step 3: Implement it**

Append to `internal/provider/local/label.go`:

```go
// VolumeName is the docker volume a folderless container keeps its work in.
//
// Here rather than beside the caller, and for the same reason as the id
// labels: the name is written at create, read at up, and passed to `docker
// volume rm` at remove. A second spelling in any one of those leaks the volume
// or mounts an empty one.
//
// xpath.ValidateName already restricts both names to letters, digits, dot,
// underscore and hyphen, which is a subset of what docker accepts for a volume
// name, so no escaping is needed here.
func VolumeName(workspace, container string) string {
	return fmt.Sprintf("dev-%s-%s", workspace, container)
}
```

`fmt` is already imported by that file.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/provider/local/ -run TestVolumeName -v`
Expected: PASS

- [ ] **Step 5: Run the full check**

Run: `make lint && make test`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add internal/provider/local/label.go internal/provider/local/label_test.go
git commit -m "feat(local): name the volume a folderless container uses"
```

---

### Task 4: `create --no-folder`

The command surface, the stored row, and the rendered configuration wired together. After this task a folderless container can be created with `--no-start`; starting one is Task 5.

**Files:**
- Modify: `internal/cli/container.go:44-160` (`createOpts`, `newContainerCreateCmd`, `runContainerCreate`)
- Modify: `internal/cli/generate.go:32-68` (`generatedConfigFor`), `:120-155` (`rewriteGeneratedTools`)
- Test: `internal/cli/generate_test.go`

**Interfaces:**
- Consumes: `model.SourceNone` (Task 1), `dcgen.Mount` and the three-argument `dcgen.Render` (Task 2), `local.VolumeName` (Task 3).
- Produces:
  - `createOpts` gains `noFolder bool`.
  - `func folderlessMount(workspace, container string) dcgen.Mount` in `internal/cli/generate.go`, returning `dcgen.Mount{Volume: local.VolumeName(workspace, container), Folder: "/workspaces/" + container}`.
  - `generatedConfigFor` gains a trailing `mount dcgen.Mount` parameter.
  - `runContainerCreate` keeps its signature; `opts.noFolder` carries the new mode.

- [ ] **Step 1: Write the failing tests**

Append to `internal/cli/generate_test.go`:

```go
// Omitting both is the likeliest typo, and guessing either way is expensive:
// an empty sandbox is only noticed when the agent cannot find the code.
func TestCreateWithNeitherFolderNorNoFolderIsRejected(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	err := runContainerCreate(t.Context(), a, "", "scratch", "", createOpts{noStart: true})
	if err == nil {
		t.Fatal("create succeeded with neither --folder nor --no-folder")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
	for _, want := range []string{"--folder", "--no-folder"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

func TestCreateWithBothFolderAndNoFolderIsRejected(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{noFolder: true, noStart: true}
	err := runContainerCreate(t.Context(), a, "", "scratch", t.TempDir(), opts)
	if err == nil {
		t.Fatal("create accepted both --folder and --no-folder")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
}

// A folderless container is always a generated one: there is no project to
// ship a configuration. The document must carry the volume, and the row must
// record that there is no source.
func TestCreateNoFolderStoresAVolumeBackedConfig(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{noFolder: true, tools: []string{"yq"}, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create: %v", err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	c, err := st.GetContainer("ws", "scratch")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if c.SourceKind != model.SourceNone {
		t.Errorf("SourceKind = %q, want %q", c.SourceKind, model.SourceNone)
	}
	if c.Source != "" {
		t.Errorf("Source = %q, want empty", c.Source)
	}
	if !strings.Contains(c.GeneratedConfig, `"source=dev-ws-scratch,target=/workspaces/scratch,type=volume"`) {
		t.Errorf("stored config does not mount the volume:\n%s", c.GeneratedConfig)
	}
	if !strings.Contains(c.GeneratedConfig, `"workspaceFolder": "/workspaces/scratch"`) {
		t.Errorf("stored config does not pin the workspace folder:\n%s", c.GeneratedConfig)
	}
}

// --no-folder needs no --generate: there is no project configuration for a
// generated one to shadow, which is the only thing --generate guards against.
// A scripted run must not block on the picker either.
func TestCreateNoFolderNeedsNoGenerateFlag(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{noFolder: true, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create: %v", err)
	}
}

// rebuild --tools re-renders the whole document. Dropping the mount here would
// be invisible until the next up, which would start a container with an empty
// workspace and the operator's work still in an unreferenced volume.
func TestRebuildToolsKeepsTheVolumeMount(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{noFolder: true, tools: []string{"yq"}, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := rewriteGeneratedTools(a, "", "scratch", []string{"+node"}); err != nil {
		t.Fatalf("rewriteGeneratedTools: %v", err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	c, err := st.GetContainer("ws", "scratch")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if !strings.Contains(c.GeneratedConfig, "workspaceMount") {
		t.Errorf("rebuild --tools dropped the volume mount:\n%s", c.GeneratedConfig)
	}
	if !strings.Contains(c.GeneratedConfig, "node") {
		t.Errorf("rebuild --tools did not add node:\n%s", c.GeneratedConfig)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestCreateWith|TestCreateNoFolder|TestRebuildToolsKeeps' -v`
Expected: FAIL to compile — `unknown field noFolder in struct literal`.

- [ ] **Step 3: Add the mount helper**

In `internal/cli/generate.go`, add the import of the local provider package and this function:

```go
// folderlessMount describes where a folderless container keeps its work.
//
// The container name is the in-container path as well as half the volume name,
// so that two containers in one workspace never share a workspace directory
// and `docker volume ls` reads as the container list.
func folderlessMount(workspace, container string) dcgen.Mount {
	return dcgen.Mount{
		Volume: local.VolumeName(workspace, container),
		Folder: "/workspaces/" + container,
	}
}
```

The import is `"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/provider/local"`. Note `internal/cli/root.go` already blank-imports this package, so this only changes it to a named import in one more file — it does not add a dependency the CLI did not already have.

- [ ] **Step 4: Thread the mount through the two renderers**

In `generatedConfigFor`, add a trailing parameter and pass it on:

```go
func generatedConfigFor(name string, generate bool, tools []string, mount dcgen.Mount, in *os.File, out io.Writer) (string, error) {
```

```go
	config, err := dcgen.Render(name, resolved, mount)
```

In `rewriteGeneratedTools`, derive the mount from the container being rewritten rather than from a flag, so a folder container keeps the zero value:

```go
	// Derived from the stored row, not from a flag: a rebuild must not be able
	// to change what a container is mounted on. A folder container renders the
	// zero Mount, which is the CLI's own bind mount.
	var mount dcgen.Mount
	if t.container.SourceKind == model.SourceNone {
		mount = folderlessMount(t.workspace.Name, name)
	}
	config, err := dcgen.Render(name, next, mount)
```

`generate.go` does not import `model` yet — add `"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/model"` alongside the `local` import from Step 3.

- [ ] **Step 5: Add the flag and the create path**

In `internal/cli/container.go`, add the field to `createOpts`:

```go
type createOpts struct {
	noStart  bool
	noFolder bool
	generate bool
	tools    []string
}
```

Register the flag in `newContainerCreateCmd`, and widen the help:

```go
	cmd.Flags().BoolVar(&opts.noFolder, "no-folder", false,
		"create a container with no host folder; its work lives in a volume dev owns")
```

```go
		Short: "Create a container and start it",
		Long: "Create a container and start it.\n\n" +
			"With --folder, a project that ships its own .devcontainer configuration\n" +
			"is used as it is; dev never edits it. For a folder with none, --generate\n" +
			"builds a base Ubuntu configuration from the tools --tools names.\n\n" +
			"With --no-folder there is no host directory at all: the container's work\n" +
			"lives in a volume dev creates and removes with it. That configuration is\n" +
			"always generated, and kept in dev's own database.",
```

Then rewrite the head of `runContainerCreate`, up to and including the point where `generated` is decided. The existing folder branch is unchanged below the split:

```go
func runContainerCreate(ctx context.Context, a *app, workspace, name, folder string, opts createOpts) error {
	if err := xpath.ValidateName(name); err != nil {
		return usageError(err)
	}
	switch {
	case folder == "" && !opts.noFolder:
		// Guessing either way is expensive: a forgotten --folder would build an
		// empty sandbox, which is only noticed when the agent cannot find the
		// project.
		return usageErrorf("give --folder PATH, or --no-folder for a container with no host folder")
	case folder != "" && opts.noFolder:
		return usageErrorf("--folder and --no-folder contradict each other")
	}
	if len(opts.tools) > 0 && !opts.generate && !opts.noFolder {
		return usageErrorf("--tools only applies with --generate")
	}

	wsName, err := a.workspaceName(workspace)
	if err != nil {
		return err
	}

	var (
		source     string
		configPath string
		generated  string
	)
	if opts.noFolder {
		// No project, so nothing can ship a configuration and there is nothing
		// for --generate to shadow: a folderless container is always generated.
		generated, err = generatedConfigFor(name, true, opts.tools,
			folderlessMount(wsName, name), os.Stdin, a.out)
		if err != nil {
			return err
		}
	} else {
		source, configPath, generated, err = folderSource(name, folder, opts, a)
		if err != nil {
			return err
		}
	}
	...
```

Move the existing folder logic — `xpath.Resolve`, `dcconfig.Find`, the `--generate` shadow check, and the `ErrNoConfig` branch — into a helper below `runContainerCreate`, unchanged except for returning its three values:

```go
// folderSource works out what a --folder container is built from: the resolved
// path, the project's own config if it has one, and a generated document if it
// does not.
func folderSource(name, folder string, opts createOpts, a *app) (source, configPath, generated string, err error) {
	// Physical path, before anything stores or mounts it: the engine resolves
	// the string inside its VM on macOS, where /tmp is a real directory rather
	// than a symlink to /private/tmp, so an unresolved path mounts an empty
	// directory with no error to show for it.
	source, err = xpath.Resolve(folder)
	if err != nil {
		return "", "", "", usageError(err)
	}
	configPath, err = dcconfig.Find(source)
	switch {
	case err == nil && opts.generate:
		// The project ships one. Generating a second would shadow the
		// definition the project owns, with no way to tell from the outside
		// which of the two built the container.
		return "", "", "", usageErrorf("%s already has a devcontainer config; --generate would shadow it",
			xpath.Shorten(source))
	case err != nil && !errors.Is(err, dcconfig.ErrNoConfig):
		return "", "", "", usageError(err)
	}

	if errors.Is(err, dcconfig.ErrNoConfig) {
		// A folder with no configuration of its own is not the end of the
		// road: dev can render one, and keeps it in the database rather than
		// writing into a project it does not own. The zero Mount leaves the
		// CLI's own bind mount of the folder in place.
		generated, err = generatedConfigFor(name, opts.generate, opts.tools, dcgen.Mount{}, os.Stdin, a.out)
		if err != nil {
			return "", "", "", err
		}
		if generated == "" {
			return "", "", "", usageErrorf("no devcontainer config in %s "+
				"(--generate builds a base Ubuntu one; dev container tools lists what it can add)",
				source)
		}
		configPath = ""
	}
	return source, configPath, generated, nil
}
```

Then the row and the report:

```go
	kind := model.SourceFolder
	if opts.noFolder {
		kind = model.SourceNone
	}
	c := model.Container{
		Name:          name,
		WorkspaceName: wsName,
		SourceKind:    kind,
		Source:        source,
		ConfigPath:    configPath,
		// Exactly one of these two is set: a project-owned container has a
		// path, a generated one has the document itself.
		GeneratedConfig: generated,
	}
	st, err := a.store()
	if err != nil {
		return err
	}
	err = st.CreateContainer(c)
	if errors.Is(err, store.ErrExists) {
		return usageErrorf("container %s already exists in workspace %s", name, wsName)
	}
	if err != nil {
		return err
	}
	if opts.noFolder {
		a.printf("container %s (workspace %s) with no folder\n", name, wsName)
	} else {
		a.printf("container %s (workspace %s) from %s\n", name, wsName, xpath.Shorten(source))
	}
```

Add `dcgen` to `container.go`'s imports.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/cli/ -v`
Expected: PASS, including every pre-existing test in the package — the folder path must behave exactly as before.

- [ ] **Step 7: Run the full check**

Run: `make lint && make test`
Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add internal/cli/
git commit -m "feat(cli): create a container with no host folder"
```

---

### Task 5: Give a folderless container a workspace folder to run in

One assignment in `materialise` is what lets both providers run a folderless container unchanged.

**Files:**
- Modify: `internal/cli/resolve.go:74-110` (`materialise`)
- Test: `internal/cli/resolve_test.go` (create)

**Interfaces:**
- Consumes: `model.SourceNone` (Task 1).
- Produces: after `materialise`, a `model.Container` with `SourceKind == model.SourceNone` has a non-empty `Source` pointing at a directory that exists, and `ConfigPath` inside it.

- [ ] **Step 1: Write the failing test**

Create `internal/cli/resolve_test.go`:

```go
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/model"
)

// Every provider call needs a --workspace-folder that exists: the devcontainer
// CLI takes one on up, exec and read-configuration alike. A folderless
// container has no host directory, so it borrows the temporary one its
// configuration is already written into.
func TestMaterialiseGivesAFolderlessContainerAFolder(t *testing.T) {
	c := model.Container{
		Name: "scratch", WorkspaceName: "ws",
		SourceKind:      model.SourceNone,
		GeneratedConfig: `{"name":"scratch"}`,
	}

	got, cleanup, err := materialise(c)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanup()

	if got.Source == "" {
		t.Fatal("Source is still empty; the devcontainer CLI would be given no workspace folder")
	}
	if info, err := os.Stat(got.Source); err != nil || !info.IsDir() {
		t.Fatalf("Source %q is not a directory: %v", got.Source, err)
	}
	// The config has to be inside the folder that is handed to the CLI, or the
	// CLI reads the layout around a file that is somewhere else entirely.
	if !strings.HasPrefix(got.ConfigPath, got.Source+string(filepath.Separator)) {
		t.Errorf("ConfigPath %q is not inside Source %q", got.ConfigPath, got.Source)
	}

	cleanup()
	if _, err := os.Stat(got.Source); !os.IsNotExist(err) {
		t.Errorf("cleanup left %s behind", got.Source)
	}
}

// A folder container's source is the project, and nothing may move it.
func TestMaterialiseLeavesAFolderSourceAlone(t *testing.T) {
	dir := t.TempDir()
	c := model.Container{
		Name: "demo", WorkspaceName: "ws",
		SourceKind:      model.SourceFolder,
		Source:          dir,
		GeneratedConfig: `{"name":"demo"}`,
	}

	got, cleanup, err := materialise(c)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanup()

	if got.Source != dir {
		t.Errorf("Source = %q, want the project folder %q", got.Source, dir)
	}
}
```

- [ ] **Step 2: Run the tests to verify the first fails**

Run: `go test ./internal/cli/ -run TestMaterialise -v`
Expected: `TestMaterialiseGivesAFolderlessContainerAFolder` FAILs with "Source is still empty"; `TestMaterialiseLeavesAFolderSourceAlone` PASSes.

- [ ] **Step 3: Implement it**

In `internal/cli/resolve.go`, inside `materialise`, after `c.ConfigPath = path` and before the return:

```go
	// A folderless container has no host directory, but the devcontainer CLI
	// takes --workspace-folder on every invocation and the k8s provider uses
	// it as a build context. The temporary directory holding the configuration
	// serves as both: it exists, it is empty, and the generated document names
	// an explicit workspaceFolder so nothing downstream depends on its name.
	if c.SourceKind == model.SourceNone {
		c.Source = dir
	}

	c.ConfigPath = path
	return c, cleanup, nil
```

Extend the doc comment above `materialise` with a sentence saying it also supplies the workspace folder for a folderless container. `model` is already imported.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/cli/ -run TestMaterialise -v`
Expected: PASS

- [ ] **Step 5: Run the full check**

Run: `make lint && make test`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/resolve.go internal/cli/resolve_test.go
git commit -m "feat(cli): give a folderless container a workspace folder"
```

---

### Task 6: Remove the volume with the container

**Files:**
- Modify: `internal/provider/local/local.go` (`Remove`)
- Test: `internal/provider/local/local_test.go`

**Interfaces:**
- Consumes: `VolumeName` (Task 3), `model.SourceNone` (Task 1).
- Produces: nothing later tasks call.

- [ ] **Step 1: Write the failing tests**

Append to `internal/provider/local/local_test.go`:

```go
func folderlessContainer() model.Container {
	return model.Container{
		Name:          "scratch",
		WorkspaceName: "ws",
		SourceKind:    model.SourceNone,
		Source:        "/tmp/dev-config-x",
		ConfigPath:    "/tmp/dev-config-x/.devcontainer/devcontainer.json",
	}
}

// docker rm does not touch a named volume: it is external to the container, so
// without this it survives every remove and accumulates until a disk fills.
func TestRemoveDeletesTheVolumeOfAFolderlessContainer(t *testing.T) {
	f := newFakePath(t)
	f.install(t, dockerBin, "abc123\n", 0)

	if err := (&Provider{}).Remove(context.Background(), folderlessContainer()); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !contains(f.argv(t, dockerBin), []string{"volume", "rm", "dev-ws-scratch"}) {
		t.Errorf("Remove did not delete the volume; last docker call was %v", f.argv(t, dockerBin))
	}
}

// A folder container's work is on the host and there is no volume to remove.
// Asking docker to remove one would fail on every remove.
func TestRemoveDoesNotDeleteAVolumeForAFolderContainer(t *testing.T) {
	f := newFakePath(t)
	f.install(t, dockerBin, "abc123\n", 0)

	if err := (&Provider{}).Remove(context.Background(), testContainer()); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if contains(f.argv(t, dockerBin), []string{"volume"}) {
		t.Errorf("Remove touched a volume for a folder container: %v", f.argv(t, dockerBin))
	}
}
```

Note the stub records only the *last* call it received, so the volume assertion works because `volume rm` is the final docker invocation `Remove` makes. Keep it that way.

- [ ] **Step 2: Run the tests to verify the first fails**

Run: `go test ./internal/provider/local/ -run TestRemove -v`
Expected: `TestRemoveDeletesTheVolumeOfAFolderlessContainer` FAILs — the last docker call is `rm -f abc123`.

- [ ] **Step 3: Implement it**

Replace `Remove` in `internal/provider/local/local.go`:

```go
func (p *Provider) Remove(ctx context.Context, c model.Container) error {
	id, err := p.containerID(ctx, c)
	if err != nil {
		return err
	}
	if id != "" {
		if err := runDocker(ctx, "rm", "-f", id); err != nil {
			return err
		}
	}

	// The volume is named in workspaceMount rather than created by the
	// container, so `docker rm` leaves it behind. Removing it here is what
	// makes `container remove` mean the container is gone, and matches the k8s
	// provider deleting its PVC. Last, because the volume cannot be removed
	// while a container still references it.
	if c.SourceKind == model.SourceNone {
		name := VolumeName(c.WorkspaceName, c.Name)
		fmt.Fprintf(os.Stderr, "dev: removing volume %s\n", name)
		// --force: a folderless container that was never started has no
		// volume, and "no such volume" is not a failure to report.
		return runDocker(ctx, "volume", "rm", "--force", name)
	}
	return nil
}
```

`fmt` and `os` are already imported.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/provider/local/ -v`
Expected: PASS

- [ ] **Step 5: Run the full check**

Run: `make lint && make test`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add internal/provider/local/
git commit -m "feat(local): remove a folderless container's volume with it"
```

---

### Task 7: k8s refuses to sync a folderless container

**Files:**
- Modify: `internal/provider/k8s/sync.go:25-40` (`Sync`)
- Modify: `internal/provider/k8s/k8s.go:113-120` (the `firstCreate` branch in `apply`)
- Modify: `internal/cli/container.go` (`runContainerSync`)
- Test: `internal/provider/k8s/sync_test.go`, `internal/provider/k8s/k8s_test.go`

**Interfaces:**
- Consumes: `model.SourceNone` (Task 1).
- Produces: nothing later tasks call.

- [ ] **Step 1: Write the failing tests**

Append to `internal/provider/k8s/sync_test.go`:

```go
// There is no host tree to push. Copying nothing over a workspace an agent is
// working in would be worse than refusing.
func TestSyncRefusesAFolderlessContainer(t *testing.T) {
	clusterStubs(t, true)

	c := model.Container{Name: "scratch", WorkspaceName: "ws", SourceKind: model.SourceNone}
	err := testProvider().Sync(context.Background(), c)
	if err == nil {
		t.Fatal("Sync accepted a container with no folder")
	}
	if !strings.Contains(err.Error(), "no folder") {
		t.Errorf("error %q does not say why", err)
	}
}
```

Append to `internal/provider/k8s/k8s_test.go`:

```go
// The first create syncs the host folder in before postCreate runs. A
// folderless container has none, and the sync would fail the create.
func TestUpSkipsTheFirstCreateSyncWithoutAFolder(t *testing.T) {
	s := clusterStubs(t, false)

	c := model.Container{Name: "scratch", WorkspaceName: "ws", SourceKind: model.SourceNone, Source: t.TempDir()}
	if err := testProvider().Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	for _, call := range kubectlCalls(t, s) {
		if strings.Contains(call, "tar -x") {
			t.Errorf("Up synced a folderless container: %q", call)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/provider/k8s/ -run 'TestSyncRefuses|TestUpSkipsTheFirstCreateSync' -v`
Expected: both FAIL — `Sync` succeeds, and `Up` issues a tar.

- [ ] **Step 3: Move the guard to the front of `Sync`**

In `internal/provider/k8s/sync.go`, the existing kind check sits *after* `readConfiguration`. Move it above, so the refusal does not depend on a cluster round trip:

```go
func (p *Provider) Sync(ctx context.Context, c model.Container) error {
	if c.SourceKind != model.SourceFolder {
		// Checked before reading the configuration: a refusal that needs a
		// cluster to produce it is a refusal that fails for the wrong reason
		// when the cluster is unreachable.
		return fmt.Errorf("container %s has no folder to sync from", c.Name)
	}
	dev, _, err := readConfiguration(ctx, c.Source, c.ConfigPath)
	if err != nil {
		return err
	}
	...
```

- [ ] **Step 4: Skip the first-create sync**

In `internal/provider/k8s/k8s.go`, in `apply`:

```go
	// Before the lifecycle commands, not after: postCreate is usually "install
	// what this project needs", and it needs the project to be there. A
	// folderless container has no host tree, and Sync refuses one.
	if opts.firstCreate && c.SourceKind == model.SourceFolder {
		if err := p.Sync(ctx, c); err != nil {
			return err
		}
	}
```

- [ ] **Step 5: Give the CLI the same refusal**

In `internal/cli/container.go`, in `runContainerSync`, before the `Syncer` assertion:

```go
	if t.container.SourceKind == model.SourceNone {
		return usageErrorf("container %s has no folder to sync from", name)
	}
```

Placed first so the message names the real reason rather than the provider's.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/provider/k8s/ ./internal/cli/ -v`
Expected: PASS

- [ ] **Step 7: Run the full check**

Run: `make lint && make test`
Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add internal/provider/k8s/ internal/cli/container.go
git commit -m "feat(k8s): refuse to sync a container with no folder"
```

---

### Task 8: `container list` prints a dash for no source

**Files:**
- Modify: `internal/cli/container.go:230-240` (`runContainerList`)
- Test: `internal/cli/generate_test.go`

**Interfaces:**
- Consumes: `model.SourceNone` (Task 1), `createOpts.noFolder` (Task 4).
- Produces: nothing.

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/generate_test.go`:

```go
// An empty SOURCE column reads as a bug in the table. A dash says "there is
// none" rather than "something went wrong printing it".
func TestListShowsADashForAFolderlessContainer(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{noFolder: true, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	out.Reset()

	if err := runContainerList(t.Context(), a, "", false); err != nil {
		t.Fatalf("list: %v", err)
	}
	line := ""
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.Contains(l, "scratch") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("scratch is not in the list:\n%s", out.String())
	}
	if !strings.HasSuffix(strings.TrimSpace(line), "-") {
		t.Errorf("SOURCE column is not a dash: %q", line)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cli/ -run TestListShowsADash -v`
Expected: FAIL — the row ends with an empty column.

- [ ] **Step 3: Implement it**

In `runContainerList`, replace the row call with a small helper defined beside `statusOf`:

```go
	// An empty cell reads as a printing bug rather than as "there is no
	// folder".
	sourceOf := func(c model.Container) string {
		if c.SourceKind == model.SourceNone {
			return "-"
		}
		return xpath.Shorten(c.Source)
	}
```

```go
			row(w, c.WorkspaceName, c.Name, statusOf(c), sourceOf(c))
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/cli/ -run TestListShowsADash -v`
Expected: PASS

- [ ] **Step 5: Run the full check**

Run: `make lint && make test`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/container.go internal/cli/generate_test.go
git commit -m "feat(cli): print a dash for a container with no folder"
```

---

### Task 9: End-to-end smoke test

The only task that needs a real engine. It proves the one thing unit tests cannot: that the volume actually persists work across a stop and a start.

**Files:**
- Modify: `test/smoke/smoke_test.go`

**Interfaces:**
- Consumes: everything above, through the built binary.

- [ ] **Step 1: Write the test**

Append to `test/smoke/smoke_test.go`:

```go
// The claim a folderless container rests on: work survives a stop and a start,
// because it is in a volume rather than in the container filesystem.
func TestSmokeFolderless(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")

	state := t.TempDir()
	t.Setenv("DEV_STATE", state)

	bin := buildBinary(t)
	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	const (
		name = "dev-smoke-none"
		ws   = "dev-smoke-none-ws"
	)
	t.Cleanup(func() {
		_ = exec.Command(bin, "container", "remove", name, "--force").Run()
		// The volume outlives a failed remove, and the next run would mount
		// the previous run's files.
		_ = exec.Command("docker", "volume", "rm", "--force", "dev-"+ws+"-"+name).Run()
	})

	dev("provider", "configure", "dev-smoke-none-local", "--kind", "local")
	dev("workspace", "init", ws, "--provider", "dev-smoke-none-local")

	t.Log("creating a folderless container; the first run pulls a base image")
	dev("container", "create", name, "--no-folder")

	dev("container", "exec", name, "--", "sh", "-c", "echo persisted > /workspaces/"+name+"/marker")

	dev("container", "stop", name)
	dev("container", "start", name)

	got := strings.TrimSpace(dev("container", "exec", name, "--", "cat", "/workspaces/"+name+"/marker"))
	if got != "persisted" {
		t.Errorf("the workspace did not survive a stop and start: marker = %q, want %q", got, "persisted")
	}

	// The volume is dev's to remove. Left behind, it accumulates silently.
	volume := "dev-" + ws + "-" + name
	dev("container", "remove", name)
	if out := dockerVolumes(t, volume); len(out) != 0 {
		t.Errorf("remove left the volume behind: %v", out)
	}
}

// dockerVolumes returns the volume names matching a name filter.
func dockerVolumes(t *testing.T, name string) []string {
	t.Helper()
	out, err := exec.Command("docker", "volume", "ls", "-q", "--filter", "name="+name).Output()
	if err != nil {
		t.Fatalf("docker volume ls: %v", err)
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names
}
```

`requireBinaries`, `buildBinary`, `run` and `dockerPS` are the existing helpers in that file. `dockerVolumes` is new because `dockerPS` runs `docker ps` specifically; match its shape and place it beside it.

- [ ] **Step 2: Run it**

Run: `make smoke`
Expected: PASS on a host with `devcontainer`, `docker` and network. It skips rather than fails without them — if it skips, say so plainly rather than reporting a pass.

- [ ] **Step 3: Run the full check**

Run: `make lint && make test`
Expected: all pass. `make test` must not have picked up the smoke test — it is behind the `smoke` build tag.

- [ ] **Step 4: Commit**

```bash
git add test/smoke/smoke_test.go
git commit -m "test: cover a folderless container end to end"
```

---

### Task 10: Document the new shape

**Files:**
- Modify: `CLAUDE.md` (invariant 9)

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Extend invariant 9**

Invariant 9 in `CLAUDE.md` currently ends with the sentence about every `model.Container` reaching a provider having a usable `ConfigPath`. Add to that invariant:

```markdown
   A container need not have a folder at all: `--no-folder` records
   `SourceKind` as `none` with an empty `Source`, always generates its
   configuration, and mounts a volume `dev` owns — a docker named volume called
   `dev-<workspace>-<container>` on local, the existing PVC on k8s. The
   generated document names both `workspaceMount` and `workspaceFolder`,
   because without the second the CLI would derive the in-container path from
   the temporary directory `materialise` creates and it would change every
   invocation. That temporary directory is also what such a container passes as
   `--workspace-folder`, which is why neither provider knows what a folderless
   container is. The local provider removes the volume in `Remove`, matching
   k8s deleting its PVC; `rebuild` keeps it on both.
```

- [ ] **Step 2: Verify the claims**

Read back the invariant against the code you wrote. Every sentence must be true of what is on disk — in particular the volume name format, and that `rebuild` touches no volume on either provider.

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: record how a folderless container is built"
```

---

## Spec coverage

| Spec section | Task |
|---|---|
| Data model — `SourceNone`, no migration | 1 |
| Generated configuration — `workspaceFolder` + `workspaceMount` | 2 |
| Volume name, one spelling | 3 |
| `rewriteGeneratedTools` keeps the mount | 4 |
| Command surface — `--no-folder`, both/neither rejected | 4 |
| Execution — `materialise` supplies the folder | 5 |
| Removal — the volume goes with the container | 6 |
| `sync` refuses; k8s skips the first-create sync | 7 |
| `list` prints a dash | 8 |
| Testing — dcgen, cli, local, k8s, smoke | 2, 4, 5, 6, 7, 8, 9 |
| Original design record edited | already committed with the spec |
