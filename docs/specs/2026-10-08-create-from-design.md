# Creating a container like an existing one

## Problem

A workspace often wants a second container with the same environment as one it
already has — the same tools, the same agent setup — but pointed at different
work: another folder, a scratch volume, or another branch. Today that means
reading the first container's tool list out of `dev container config show` and
retyping it as `--tools`, along with whatever `--no-persist-state` and
`--agent-config` it was created with. Easy to get subtly wrong, and the two
containers then drift for no reason anyone chose.

## Goal

`--from SOURCE` on `container create` and `worktree create` starts a new
container with the environment of an existing generated container in the same
workspace, and otherwise behaves exactly as `--generate --tools <list>` would.

Success: `dev container create scratch --from api --no-folder` gives a
container whose tools match `api`'s, with its own volumes, and nothing about
`api` changes.

## Decisions

- **Only a generated source can be cloned.** A container whose project ships its
  own `devcontainer.json` is refused. Pointing the new container at another
  project's file leaves a relative `dockerfile` or `build.context` anchored to
  that project and breaks when it moves; snapshotting the file into the new row
  freezes a document `dev` does not own (invariant 4's reasoning, as in
  `OverrideConfigPath`). Either can be added later as an explicit opt-in.
- **What is copied is the tool list, never the document.** `dcgen.Render` bakes
  the container's name, its workspace volume (`dev-<ws>-<container>`), its state
  volume and any worktree binds into the document. A byte copy would mount the
  source's volumes into the new container. The new document is rendered from
  `dcgen.ToolsOf(source.GeneratedConfig)` with the new container's own name and
  mounts — the same route `rebuild --tools` takes.
- **A flag, not a command or a template.** `container clone` would duplicate
  every create flag for no new capability; a workspace-level template is a new
  noun and a migration for reuse nobody has asked for.
- **No record of the clone.** After create the new container is an ordinary
  generated container. No migration, no `cloned_from` column.
- **No shared image tag.** Sharing a tag across containers would let `rebuild`
  on one silently change the other. The image is reused only as far as the
  build cache reuses it (see *Image*).

## Behaviour

```
dev container create NAME --from SOURCE (--folder PATH | --no-folder)
dev worktree  create NAME --from SOURCE --branch B --path P [--repo R]
```

The source is looked up in the command's workspace (`--workspace` or the
default). It is read and never written.

### What the new container inherits

| Field | With `--from` |
|---|---|
| Tool list | Copied from the source. `--tools` may adjust it with `+id`/`-id` changes only. |
| `PersistState` | Copied. `--no-persist-state` overrides. |
| `AgentConfig` | An explicit path or `none` is copied. `""` (the project's own `.devcontainer/agents.yaml`) stays `""` and so means the *new* folder's file. `--agent-config` / `--no-agent-config` override. |
| Workspace mount, state volume, worktree binds | Never copied. Derived for the new container exactly as for any other. |

A plain `--tools` list is refused rather than allowed to replace the copied set:
`applyToolDiff` treats a plain list as a replacement, so `--from api --tools go`
would silently discard everything `--from` exists to carry.

`--generate` is accepted alongside `--from` and is redundant, as it is with
`--no-folder`. The picker never opens: `--from` always supplies the tools.

### Validation order

All of these run before anything is written:

1. The command's existing name and folder checks, unchanged.
2. `--from` with a plain `--tools` list → exit 2.
3. Source missing from the workspace → exit 3.
4. Source has no `GeneratedConfig` → exit 2.
5. Tools read with `ToolsOf`, adjusted with `applyToolDiff`.
6. Target folder ships its own `.devcontainer` → exit 2, the existing "would
   shadow it" error.

For `worktree create`, steps 2–5 run before `gitwt.Add`, so a missing or
project-owned source leaves no checkout and no new branch. Step 6 can only run
after the checkout exists, since until then there is no tree for
`dcconfig.Find` to read; a refusal there rolls the checkout back through the
existing `rollback`, as `--generate` already does.

### Errors

| Case | Exit | Message |
|---|---|---|
| `--from` with a plain `--tools` list | 2 | `with --from, --tools takes +/- changes to SOURCE's tools` |
| Source does not exist | 3 | `no such container: SOURCE (workspace W)` |
| Source uses its project's config | 2 | `container SOURCE uses its project's own config; --from copies only generated ones` |
| Source's stored document has no readable tool list | 1 | The `ToolsOf` error, naming the source. A corrupt row, not a malformed request. |
| NAME equals SOURCE | 2 | The existing "already exists" error; no new code. |
| Target ships its own `.devcontainer` | 2 | The existing "would shadow it" error. |

### Worktrees

`createWorktreeRows` already renders with `worktreeMount(repo, path)` for the
new checkout, so a clone gets its own two bind mounts (invariant 11) whatever
the source was. A worktree-backed source's binds never reach the new document;
a plain source cloned into a worktree gains them. `requireLocalProvider` still
applies.

### Image

- **Local:** the same tool list renders the same Features, so the devcontainer
  CLI's build is expected to be served from Docker's layer cache. The new
  container still gets its own image tag.
- **k8s:** the tag is per container (`<registry>/<ws>-<name>:latest`), so a
  build and push still happen; the host build is cached and the registry
  already holds the layers. Only `container create` applies, since worktrees are
  local-only.

The layer-cache claim is verified by hand during implementation and reported,
not asserted in a test: a timing or log-scraping assertion would be flaky in CI.
`USAGE.md` states only what that check confirms.

## Implementation shape

One helper in `internal/cli/generate.go`:

```go
// applyFrom rewrites opts so the create that follows renders from source's
// tools and inherits its persisted choices.
func applyFrom(a *app, wsName, from string, opts *createOpts) error
```

It performs validation steps 2–5 and sets `opts.generate`, `opts.tools`,
`opts.noPersistState` and `opts.agentConfig` / `opts.noAgentConfig` according to
the inheritance table, leaving any flag the operator gave in charge. A `from`
field joins `createOpts`, so both commands carry it the same way.

- `runContainerCreate` calls it immediately after `a.workspaceName`, before
  `folderSource` or the no-folder branch. Its existing `agentConfigChoice` call
  runs before the workspace is known, so it stays where it is to report a bad
  flag first, and the value stored on the row is computed again after
  `applyFrom` from the rewritten `opts`. A source's `none` becomes
  `opts.noAgentConfig`; a stored path becomes `opts.agentConfig`, so a file that
  has since gone fails with the existing exit 3.
- `runWorktreeCreate` calls it immediately after `a.workspaceName`, before
  `gitwt.Add`. `createWorktreeRows` is unchanged.

The existing check that `--tools` needs `--generate` must see `--from` as
satisfying it.

No new package, no migration, no provider change.

## Testing

Unit tests on the existing stub harness, so no container engine is involved.

`internal/cli/generate_test.go`:

- `TestCreateFromCopiesTheSourceTools`: the new row's `ToolsOf` equals the
  source's, and its document names the new container's own volumes and does not
  contain the source's name.
- `TestCreateFromAppliesAToolDiff`: `+node` adds, `-go` removes.
- `TestCreateFromRejectsAPlainToolList`: exit 2.
- `TestCreateFromAMissingSource`: exit 3.
- `TestCreateFromAProjectOwnedSource`: exit 2, no row written.
- `TestCreateFromInheritsPersistAndAgentConfig`: explicit path and `none`
  copied, `""` kept, both overrides honoured.
- `TestCreateFromRefusesAFolderWithAConfig`: exit 2.
- `TestCreateFromLeavesTheSourceAlone`: source row unchanged.

`internal/cli/worktree_test.go`:

- `TestWorktreeCreateFromCopiesTools`: source's tools and the new checkout's two
  bind mounts.
- `TestWorktreeCreateFromAMissingSourceMakesNoCheckout`: exit 3, `--path` absent,
  branch not created.
- `TestWorktreeCreateFromAWorktreeSourceDropsItsBinds`: the source checkout's
  path does not appear in the new document.

`test/smoke/smoke_test.go`: create a generated container, create another
`--from` it, and `exec` in the second to confirm one of the source's tools is
on `PATH`.

## Documentation

`docs/USAGE.md`:

- Command reference: `--from SOURCE` under `container create` and
  `worktree create`.
- A walkthrough, "Start another container like an existing one", after "Change
  what a generated container installs": both commands, the `+/-` form, and what
  never carries over.

The `Long` help of both commands gains a paragraph on `--from`.

## Out of scope

- Cloning a project-owned configuration.
- Sharing an image tag, or skipping the build, across containers.
- Copying workspace files, agent state, or a worktree's ignored files from the
  source. `--from` copies an environment, not work.
