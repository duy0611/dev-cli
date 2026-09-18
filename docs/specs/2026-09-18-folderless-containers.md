# Containers without a folder

`dev container create` requires `--folder`:

```
❯ dist/dev container create scratch
dev: --folder is required
```

Every container is therefore tied to a directory on the host, and the local
provider bind-mounts that directory. This adds a second shape: a container
created with no folder at all, whose work lives in a volume the provider owns
rather than on the host.

The case is a sandbox with nothing to start from — a place to run an agent that
will clone, scaffold, or experiment on its own, without first picking a host
directory for it to write into. On a shared or remote engine there may be no
sensible host directory to pick.

The original design record listed three sources: a folder, a GitHub URL, or a
container image. Only the folder was ever built. The other two are removed from
the record rather than carried as unbuilt intent; a folderless container covers
the ground they were reaching for, and a URL source can be reconsidered on its
own merits later. `docs/specs/2026-09-14-dev-cli.md` is edited to say so, as
part of this change.

## The shape of the change

Neither provider learns what a folderless container is.

The devcontainer CLI takes `--workspace-folder` on every invocation, and the
k8s provider needs one for `read-configuration` and as a build context. A
folderless container still hands both a path — the temporary directory that
already exists to hold its materialised configuration. What differs is only
what is mounted there, and that is a line in the generated `devcontainer.json`.

So the change is concentrated in three places: the generated configuration
gains a volume mount, `materialise` supplies the workspace folder, and the CLI
grows a flag. The providers see a `model.Container` they can already handle.

## Data model

`model.SourceKind` gains a second value:

```go
// SourceNone is a container with no host folder. Its work lives in a volume
// the provider owns: a docker named volume on local, the PVC on k8s.
SourceNone SourceKind = "none"
```

A folderless container stores `Source: ""`. No migration: `source TEXT NOT
NULL` accepts the empty string, and `source_kind` is already a free-text
column. `Container.Source`'s comment becomes "absolute physical host path when
SourceKind is folder, empty when none".

`SourceKind` is now load-bearing rather than decorative — it was a single-valued
enum until this change, and three call sites branch on it.

## The generated configuration

A folderless container has no project to ship a configuration, so it is always
a generated one. `dcgen.Render` gains a parameter describing the mount, and for
a folderless container emits two more fields:

```json
{
  "name": "scratch",
  "image": "…",
  "remoteUser": "vscode",
  "workspaceFolder": "/workspaces/scratch",
  "workspaceMount": "source=dev-default-scratch,target=/workspaces/scratch,type=volume"
}
```

Both fields are required, not just the mount. Without an explicit
`workspaceFolder` the CLI derives one from the basename of
`--workspace-folder`, which here is a per-invocation temporary directory: the
in-container path would be `/workspaces/dev-config-8471` and would change on
every command. On k8s it would move the PVC's mount point each time, which
loses the workspace.

The volume name is `dev-<workspace>-<container>`, built in
`internal/provider/local/label.go` beside the id-labels and for the same
reason: a name with two spellings is a resource that leaks. Workspace and
container names are already restricted to letters, digits, `.`, `_` and `-`
by `xpath.ValidateName`, which is a subset of what docker accepts for a volume
name.

`rewriteGeneratedTools` re-renders the document from scratch when
`rebuild --tools` changes a selection. It must pass the same mount parameter,
or a rebuild would quietly drop the `workspaceMount` from an existing container
and the next `up` would create a fresh empty workspace in the container
filesystem. This is the sharpest edge in the change and gets a test of its own.

k8s ignores `workspaceMount`: it builds its own pod spec and mounts the PVC at
`DevConfig.WorkspaceFolder`. The mount line is therefore local-only in effect,
while `workspaceFolder` matters to both providers.

## Execution

`materialise` in `internal/cli/resolve.go` already writes
`<tmp>/.devcontainer/devcontainer.json` per invocation and sets `ConfigPath` to
it. For a folderless container it also sets `Source` to `<tmp>`.

That single assignment is what lets both providers work unmodified. Local's
`up` and `exec` get a `--workspace-folder` that exists and is empty; k8s's
`read-configuration` gets one too, and its build context is a directory holding
only the configuration — which is all a folderless build needs, since there is
no project to copy in.

Every path to a provider goes through `materialise`: `resolve` for an existing
container, `app.start` for the create path. There is no second place to fix.

The temporary directory is not persistent and does not need to be. Nothing
downstream keys on the host path — the container is identified by its
id-labels, not by where it was started from (invariant 1) — and the volume
that holds the actual work is named after the workspace and container.

## Command surface

`container create NAME` takes `--folder PATH` or `--no-folder`:

- neither: exit 2, naming both choices
- both: exit 2
- `--no-folder`: a generated configuration, always

`--generate` is redundant with `--no-folder` and accepted silently: there is no
project configuration for a generated one to shadow, which is the only thing
`--generate` guards against. At a terminal, `--no-folder` without `--tools`
still opens the tool picker; a scripted run without `--tools` gets the base
Ubuntu image with no tools added.

`container sync` on a folderless container exits 2 — "container X has no folder
to sync from". There is no host tree to push, and the command already refuses
on the local provider for a comparable reason. The k8s `Up` path skips its
`firstCreate` sync for the same reason.

`container list` prints `-` in the SOURCE column. `create` reports
`container scratch (workspace default) with no folder`.

## Removal

The named volume is external to the container, so `docker rm -f` does not touch
it. The local provider's `Remove` deletes it explicitly, for a folderless
container only, printing the name as it goes.

This matches k8s, whose `Remove` already deletes the PVC on the reasoning that
`dev container remove` means the container is gone and a claim nothing
references is a bill nobody expects. `--force` keeps its meaning: a volume
removal that fails is a warning, and the record is dropped anyway.

`rebuild` does not touch the volume, on either provider. A rebuild changes the
image, not the work: `up --remove-existing-container` recreates the container
around the same named volume, and k8s keeps its PVC across a rebuild. A clean
workspace is `remove` followed by `create`, which is explicit about destroying
the contents. There is no `rebuild --clean`; it would be a second destructive
path to reach a state one command already reaches.

`$HOME` inside a local container remains container-filesystem-only, for
folderless and folder containers alike — a rebuild loses what `postCreate`
wrote to `~`. That is today's behaviour and this change does not alter it. k8s
persists home because scaling to zero forced the question.

## Testing

Everything below runs against the existing stub-executable harness; nothing new
needs a container engine.

- `internal/dcgen`: a folderless render carries `workspaceFolder` and
  `workspaceMount`; a folder render carries neither; `ToolsOf` round-trips a
  document that has them.
- `internal/cli`: `create` with neither flag exits 2; with both exits 2;
  `rebuild --tools` on a folderless container preserves `workspaceMount`.
- `internal/provider/local`: `up` for a folderless container passes the
  materialised directory as `--workspace-folder` and both id-labels; `Remove`
  issues `docker volume rm` with the expected name, and does not for a folder
  container.
- `internal/provider/k8s`: `Sync` refuses a folderless container; `Up` on first
  create skips the sync.
- `test/smoke`: create a folderless container, write a file into the workspace,
  stop it, start it, and confirm the file is still there.
