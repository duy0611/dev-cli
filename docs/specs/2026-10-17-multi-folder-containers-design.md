# Multi-folder containers

A container can be created on more than one host folder, so an agent works
across several repositories at once — `workspace/something/repo-1` and
`workspace/something/repo-2` — in either mode, with each repository guarded or
isolated as a single-folder container's is.

## Problem

A container has one folder. To give an agent two repositories today, the
operator points `--folder` at their common parent, and in guarded mode that
breaks the mode's promise silently:

- `gitguard.LayoutOf` finds a repository only by walking *up* from the folder.
  A parent that is not itself a repository has none, so `GitGuard` is false,
  `checkGuarded` returns early, and nothing is mounted read-only.
- `repo-1/.git` and `repo-2/.git` are then ordinary writable files. The
  container can set `core.fsmonitor` or plant a hook in either, and host git —
  the IDE's git status included — runs it. `container list` still says
  `guarded`.

The same holds when the parent is a repository holding independent child
repositories that are not submodules.

Isolated mode has no answer either: `internal/seed` treats a nested `.git` as
ordinary content, so each child arrives with the host's config and hooks, and
`dev container fetch` knows only the root repository, so work committed in a
child has no way home.

## Goal

The operator names each folder. Each is mounted, or copied, and guarded exactly
as a single folder is; a folder not named is not in the container at all. No
directory scan, no depth limit, and nothing appearing later that the guard has
to catch up with.

## Decisions

| Decision | Choice |
|---|---|
| How folders are given | `--folder` repeated on `container create` |
| Which one drives the container | the first: its `.devcontainer`, or a generated config; the devcontainer CLI's `--workspace-folder` |
| Where an extra folder appears | `/workspaces/<basename>`; a worktree at its own host path (invariant 11) |
| Two folders with one in-container name | refused at create or rebuild, naming both |
| Modes | both; each folder guarded or isolated as a single folder is |
| Isolated layout | one volume per folder, each at `/workspaces/<basename>` |
| Changing the set, guarded | `rebuild --add-folder` / `--remove-folder` |
| Changing the set, isolated | never: recreate the container |
| Changing the primary | never: recreate the container |
| Extra folders that are not repositories | allowed, nothing to guard |
| `--from` | inherits tools and settings, not folders |
| Legacy containers | no extra folders |
| Kubernetes | one folder only; more is refused |
| `agents.yaml` | read from the primary only |

## 1. Command surface

```
dev container create NAME [--folder PATH]... [--mode guarded|isolated] ...
dev container rebuild NAME [--add-folder PATH]... [--remove-folder PATH]... ...
```

- The first `--folder` is the primary; the order of the rest does not matter.
  With no `--folder`, the current directory is the primary, as today.
- `rebuild --add-folder` and `--remove-folder` are each repeatable, and are
  separate flags rather than `+`/`-` prefixes as `--tools` has, because `+` is
  a legal first character of a path.
- `container list` shows the primary followed by `+N` when there are extra
  folders. `inspect` lists every folder with its container path.

### Validation

At create, and at rebuild for the resulting set, before anything changes:

- Each path is resolved to its physical path (invariant 2) and must be an
  existing directory.
- No folder twice, and no folder inside another folder's mount. A nested pair
  mounts the same files twice, and in guarded mode puts the inner repository's
  `.git` under the outer mount. For a guarded primary the mount is its whole
  git root, so an extra folder elsewhere in that repository is refused too.
- In-container paths are unique. `--folder a/api --folder b/api`, or an extra
  folder named like the primary's mount, is refused naming both folders.
- `--no-folder` with `--folder` stays a usage error.
- On k8s, more than one folder is exit 2, naming the provider as experimental.
- `--remove-folder` of the primary, or `--add-folder` of a path already in the
  set, is exit 2.
- `--add-folder` or `--remove-folder` on an isolated container is exit 2:
  `container NAME is isolated; its folders are fixed, recreate it to change them`.
  On a legacy container, exit 2 naming the mode.

Every refusal is exit 2 (invariant 7).

## 2. Storage

Migration `0009_container_folders.sql`, portable SQL (invariant 5):

```sql
CREATE TABLE container_folders (
  workspace_name TEXT NOT NULL,
  container_name TEXT NOT NULL,
  host_path      TEXT NOT NULL,
  container_path TEXT NOT NULL,
  PRIMARY KEY (workspace_name, container_name, host_path),
  FOREIGN KEY (workspace_name, container_name)
    REFERENCES containers(workspace_name, name) ON DELETE CASCADE
);

ALTER TABLE containers ADD COLUMN workspace_path TEXT NOT NULL DEFAULT '';
UPDATE containers SET workspace_path = '/workspaces/' || name
  WHERE mode = 'isolated';
```

- The primary stays in `containers.source`, so every reader of `Source` is
  unchanged. `container_folders` holds only the extra folders.
- `container_path` is stored, not derived, so a host rename after the folder
  was added cannot move it.
- `workspace_path` is where an isolated primary's volume is mounted:
  `/workspaces/<basename>` for a container created from here on, and the
  backfilled `/workspaces/<name>` for one that exists, so `fetch`, `remove` and
  `rebuild` find its volume where it already is. It is `''` for every other
  mode, whose primary the devcontainer CLI places.
- `model.Container` gains `Folders []model.Folder` (`HostPath`,
  `ContainerPath`) and `WorkspacePath`, loaded with the row.
- `rebuild` writes the table only after the provider's rebuild succeeds, so a
  failed rebuild leaves the record matching the container that still runs.

## 3. Where each folder appears

| | Primary | Extra folder |
|---|---|---|
| Guarded | unchanged: `/workspaces/<git root basename>`, the devcontainer CLI's rule | `/workspaces/<basename>`; a worktree at its own host path |
| Isolated, created from here on | `/workspaces/<basename>` | `/workspaces/<basename>` |
| Isolated, existing | `/workspaces/<container name>`, unchanged | none |

The same paths in both modes for a new container, so one set of agent
instructions serves either.

An extra folder that is inside a repository but not at its root is mounted as
given: the agent sees its files and not the repository's history, since that
`.git` lies outside the mount. The primary keeps the devcontainer CLI's rule of
mounting the whole git root.

## 4. Guarded

### Mounts

Each extra folder is a writable bind mount at its container path, followed by
its guard mounts; a mount inside another comes after it. Both reach the
container by the routes the git guard's mounts already take: `dcgen.AppendMounts`
for a generated document, `dcgen.OverlayMounts` in the `--override-config` merge
for a project's, on `up` and `exec` alike (invariants 10 and 11).

### Guarding each folder

`planGuardedGitDir` runs once per folder, primary first, and the results join
into one `gitGuardPlan`:

| The folder is | Guard |
|---|---|
| a repository root | `ReadOnlyMounts` at `<container path>/.git` |
| a worktree checkout | `ReadOnlyWorktreeMounts` on its common directory, the checkout at its own host path; `checkGuarded` warns, naming the folder |
| inside a repository, not at its root | none: the `.git` is not mounted |
| not a repository | none |

A folder with `.gitmodules` gets the existing submodule warning, naming the
folder. `LayoutOf` takes the container root as a parameter rather than assuming
`/workspaces/<basename>`.

### The working tree

`FindTree` runs once per guarded repository with every folder's mount as a
root, since repo-1's `core.hooksPath` or `include.path` can name a file in
repo-2. Each guarded `.git` counts as shielded for every search. The findings
are deduplicated by host path and go to `TreeBinds`, `checkTreeMounts` and
`probeHeldFiles` unchanged.

### Trust and messages

- `trustMountedRepository` adds a `safe.directory` entry for each guarded
  repository's container path.
- `create` prints `guarded: the agent can read these repositories' history but
  not commit; you commit and push`, naming them when there is more than one.
- The relay rules are unchanged: `--ssh-agent` is exit 2, `--ssh-forward` is
  skipped with one line.

### A folder gone from the host

Before `up`, each stored extra folder must exist. A missing one refuses the
command with exit 1, naming it and `dev container rebuild NAME --remove-folder PATH`, rather
than leaving the engine to fail the mount. A missing primary behaves as today.

### `rebuild --add-folder` / `--remove-folder`

The validation in section 1 runs on the resulting set, then the plan is made
from it, then the provider rebuilds, then the table is written. An added
folder's guard mounts are in the rebuilt container from its first command.
Removing a folder drops its mounts; its work is already on the host.

## 5. Isolated

### Volumes

| Folder | Volume | Mounted at |
|---|---|---|
| primary | `dev-<workspace>-<container>`, as today | `workspace_path` |
| extra | `dev-<workspace>-<container>-f-<hash>` | its container path |

`<hash>` is the first 8 hex digits of the SHA-256 of the folder's host path:
stable, and valid in a volume name whatever the folder is called. `-f-` keeps
the names apart from `-state`. `OverlayIsolatedWorkspace` handles the primary
as now; the extra volumes go through `AppendMounts` or `OverlayMounts`.

### Seeding

Only on first create, as today (`seeding := id == ""` in `local.up`).
`seedWorkspace` loops over the folders, primary first, and runs today's three
steps for each — claim the mount point, `seed.Archive` into `tar -x`,
`seed.Script` — at that folder's path. `internal/seed` is unchanged. A folder
that fails fails the `up`, naming it; the half-made container is left for
`remove`, as today.

### `dev container fetch NAME`

For each folder that is a repository, its branches come home into its own host
repository as `refs/remotes/NAME/*`, exactly as for one folder today. The
`isRemote` refusal is checked in every repository before anything is fetched. A
folder that is not a repository is skipped with one line. Output is one
`fetched into <folder>: <refs>` line per folder; the audit record carries the
refs per folder.

### `dev container remove NAME`

`unexported` runs per folder, against its own volume path and host repository.
Findings are grouped by folder, so the message names which repository holds the
work. `--force` skips the check as today. On success, `dropFetchedRefs` runs in
each host repository, and `Provider.Remove` deletes every extra volume with the
primary and state volumes.

### `rebuild`

Keeps every volume. `--add-folder` and `--remove-folder` are refused (section 1).

## 6. Edges

- **k8s:** one folder only (section 1). Single-folder k8s is unchanged.
- **`--from SOURCE`:** inherits tools, state and `agents.yaml` settings as
  today, never folders; this command's `--folder` flags decide them.
- **Legacy containers:** no extra folders; they keep the single-file guard they
  were created with (invariant 12).
- **`agents.yaml`:** read from the primary only, as `agentcfg` does today.
- **Workspace settings:** unchanged; they apply to the container, not a folder.

## Testing

Test first. Unit tests need no engine and no network.

- **Store:** `container_folders` round-trips, cascades on container delete, and
  migration 0009 backfills `workspace_path` for isolated rows only — on a temp
  file, never `:memory:`.
- **Validation:** duplicates, nesting, name collisions (extra with extra, extra
  with the primary's mount), k8s with two folders, `--add-folder` on isolated
  and legacy, `--remove-folder` of the primary.
- **Guard planning:** a temp tree with a repository, a worktree and a plain
  folder as extras; the mount strings and their order; `trustMountedRepository`
  directories.
- **Tree search:** repo-1's `core.hooksPath` naming a directory in repo-2 is
  found and held read-only.
- **Overlay:** a project config gets every folder's mounts, after the
  workspace, in order.
- **Isolated:** seed, fetch and remove loop per folder, with stub `docker` and
  `devcontainer` on a temporary `PATH`; volume names; the remove message
  grouped by folder.
- **Smoke, guarded:** two repository folders; writing each `.git/config` from
  inside fails with `EROFS`; an edit inside appears on the host;
  `rebuild --add-folder` a third and its `.git` is read-only.
- **Smoke, isolated:** two folders; a commit inside each; `fetch` puts each
  repository's branch under its own `refs/remotes/NAME/*`; `remove` refuses
  until both are merged.

## Documentation

- `docs/USAGE.md`: repeated `--folder`, `rebuild --add-folder`/`--remove-folder`,
  the collision rule, where folders appear in each mode, per-folder `fetch` and
  `remove`, `agents.yaml` from the primary.
- `docs/SECURITY.md`: guarded's promise is per folder; the worktree and
  submodule gaps name their folder; a repository the agent creates is still a
  gap; a folder not named is not in the container.
- `docs/ARCHITECTURE.md`: invariant 12 gains multiple folders — one read-only
  `.git` mount per guarded repository, one volume per isolated folder, stored
  container paths. Invariant 11 notes a worktree extra folder is at its own
  path too.

## Order

1. Migration, model and store: `container_folders`, `workspace_path`.
   Behaviour unchanged.
2. `create --folder` repeated, validation, `list` and `inspect`; extra folders
   refused in every mode until their step lands.
3. Guarded: mounts, per-folder guard, tree search, trust, the missing-folder
   check.
4. `rebuild --add-folder` / `--remove-folder` for guarded.
5. Isolated: `workspace_path` for new containers, per-folder volumes, seeding,
   `fetch`, `remove`.
6. Documentation.

Each step leaves `make lint && make test` green and is its own commit.

## Not in this change

- discovering child repositories under a folder;
- changing an isolated container's folders, or any container's primary,
  without recreating it;
- seeding an isolated folder on rebuild;
- naming an extra folder's mount (`--folder PATH:NAME`);
- more than one folder on k8s.
