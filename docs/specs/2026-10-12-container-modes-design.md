# Container modes: guarded and isolated

A container is created in one of two modes, and the mode decides how much of
the operator's checkout it can reach. The evidence that drove this — smoke
tests on Linux and on a macOS podman machine — is in PR #28 and summarised in
`docs/SECURITY.md`. This spec does not repeat it.

## Problem

The git guard (`docs/specs/2026-10-08-agent-hardening-design.md`, section 1)
keeps `.git` writable and holds a few files inside it read-only with
single-file bind mounts. Two things defeat those mounts:

1. **A host rename removes them.** Host git writes `config` by renaming
   `config.lock` over it, and since Linux 3.18 renaming over a file that is a
   mount point in another mount namespace detaches the mount. Every host command
   that writes repository config — `push -u`, `branch -u`, `remote add`,
   VS Code's own writes — leaves the container with a writable `.git/config`
   until it restarts. Immediately on Linux, about 1.5 s later on macOS.
2. **On macOS a case variant goes around them.** Inside the container
   `.git/CONFIG` is a name of its own and misses the read-only mount; APFS on
   the host resolves it to the real `.git/config`. No host action needed.

Nothing reports either: `container list` says `guarded`, and `inspect` reports
the configuration at create, not the live mount.

## Goal

Two modes, each of which keeps its promise against a deliberate agent and not
only a careless one, and says plainly what it does not cover:

- **guarded** — the operator's folder is mounted live, so edits appear on the
  host as they happen; `.git` is read-only, so the agent can read history but
  cannot change what host git runs. The operator commits and pushes.
- **isolated** — the folder is copied into a volume `dev` owns and never
  mounted. The agent has a whole repository of its own and can commit, sign and
  push; the operator brings its branches home with `dev container fetch`.

## Decisions

| Decision | Choice |
|---|---|
| How a mode is chosen | `--mode guarded\|isolated` on `container create` |
| Default | `guarded` on local; `isolated` on k8s, where guarded is impossible |
| Changing it | never: fixed at create, kept by `rebuild` |
| Guarded's `.git` | one read-only directory mount; the single-file mounts and the hooks copy go |
| Relay in guarded mode | refused: nothing inside needs it, and it would let the container push what the operator committed |
| Isolated's copy | a standalone repository, made from a bundle plus the working tree |
| Getting isolated work out | `dev container fetch NAME`, a git bundle into `refs/remotes/<container>/*` |
| Removing isolated work | refused while anything is unfetched, unless `--force` |
| `dev worktree create` | makes the git worktree only; the container comes from `container create` in it |
| `--folder` | defaults to the current directory |
| Existing containers | unchanged; shown as `legacy` |
| Kubernetes | isolated only, which is what it already does |
| A third mode keeping today's guard | not built: it cannot keep a guard's promise |

## 1. The mode

`containers` gains a `mode` column: `guarded`, `isolated`, `legacy`, or `none`.

- `create` writes `guarded` or `isolated` for a folder, `none` for
  `--no-folder`.
- `legacy` is what the migration writes for every row that exists, folder or
  not, with `none` for folderless rows — see Schema.
- Nothing else writes the column. `rebuild` keeps it, as it keeps
  `persist_state` and `git_guard`, for the same reason: what a container is
  mounted on must not change under it (invariant 10's reason).

`--mode` with `--no-folder` is a usage error (exit 2): a folderless container
has no host checkout for a mode to describe.

The `git_guard` column stays and keeps its meaning for legacy rows only. New
rows write `git_guard = 0`; the mode says everything it used to.

On the k8s provider, `create` refuses `--mode guarded` (exit 2): a pod has no
host bind mounts, so a live folder is impossible there. `--mode isolated` is
what k8s already does — `seedWorkspace` streams the folder into the PVC — and
its default on k8s is `isolated`, not `guarded`, so `dev container create`
works on both providers without a flag. The k8s provider is experimental; the
local provider is the stable one.

## 2. Guarded

### Mounts

For a folder whose `.git` is a directory, one entry, in devcontainer.json's
string form:

```
source=<repo>/.git,target=<container repo>/.git,type=bind,readonly
```

It replaces all three of `gitguard.Mounts`. The two routes are as before
(invariant 10): a generated document names it in `mounts`; a project-owned one
gets it merged into a copy of its `devcontainer.json` and passed with
`--override-config` on `up` and `exec` alike.

Why a directory holds where the files did not: host git replaces files *inside*
`.git` and never `.git` itself, so nothing renames over the mount point; and on
macOS a case variant of a directory name (`.GIT`, `.git/HOOKS`) resolved to the
mount in every test, while a case variant of a file name did not.

For a folder that is a git worktree (a `.git` *file*): the worktree's common
directory is mounted read-only at its own host path, and the checkout at its
own host path, as invariant 11 requires. The checkout's `.git` file stays in the
writable tree — see "What guarded does not cover".

For a folder that is not a repository: no `.git` mount. `create` notes it once.

### What goes away

With `.git` read-only, these have nothing left to do for a guarded container:

- the read-only single-file mounts on `config`, a worktree's `.git` file,
  `commondir`, `gitdir`, `config.worktree`;
- the hooks copy under the state directory, its seeding at create and rebuild,
  and `removeHooksCopy`;
- the `commondir` check before and after every command, and the fingerprint
  fallback for a project that names its own workspace mount.

They stay in the code for legacy containers until no legacy row is left, and
are deleted then. A later change removes them; this one stops new containers
using them.

The project's own `workspaceMount` case changes too. A project that names its
own workspace mount puts `.git` wherever it likes, so no mount can be aimed at
it. Today that falls back to fingerprinting. Under guarded, `create` refuses
such a project (exit 2), naming the field and pointing at `--mode isolated`:
a fingerprint is detection, and guarded is a promise of prevention.

### What stays

The working-tree half of the guard is unchanged: host git can be pointed
*into* the working tree by a `core.hooksPath` or an `include.path`, and
`gitguard.FindTree` still finds those on every command and holds them read-only
— directories as directories, files with every parent pinned. A file held this
way is a single-file mount and breaks on a host rename as `.git/config` did;
that is recorded under "What guarded does not cover", not fixed here.

`checkTreeMounts` stops trusting `inspect`, which reports the mount configured
at create rather than the live one. It probes each held path from inside the
container by opening it for append and writing nothing — `( : >> PATH )` —
which fails with `EROFS` on a read-only mount and changes nothing otherwise.
Not `test -w`: busybox's `test` decides from mode bits and, as root, calls a
read-only mount writable.

### No relay

A guarded container cannot commit, branch, stash, fetch or pull, since each
writes to `.git`. What it could still do with the operator's ssh agent is push
commits that already exist — commits the operator made on the host — which is
the one thing the operator did not ask it to do.

So for a guarded container:

- `--ssh-agent` on `exec`, `shell` or `start-agent` is a usage error (exit 2),
  naming the mode.
- A workspace with `--ssh-forward` does not forward into it; the command prints
  one line saying so, since the operator set forwarding on and may wonder why
  `git` cannot reach the remote.
- No signing configuration is passed: there is nothing to sign.

A project that needs a private dependency over ssh (`npm install` from a
private repository, `go mod download` of a private module) needs isolated mode,
or a token in a workspace setting.

### What the agent sees

A guarded container's agent will try to commit; agents expect to. git's own
refusal — `Unable to create '.git/index.lock': Read-only file system` — is
clear enough to a person and less so to an agent. `create` and `start-agent`
print one line naming the mode and that commits are the operator's; the
project's agent instructions are where to tell the agent itself, and
`docs/USAGE.md` says so. `dev` does not write that instruction into the project
(invariant 9).

### What guarded does not cover

Each is documented in `docs/SECURITY.md` and printed where it applies:

- **A submodule's `.git` file.** It sits in the writable working tree and holds
  `gitdir: ../.git/modules/<name>`, which host git follows. A container that
  rewrites it points host git at a config of its own. `create` warns when the
  folder has `.gitmodules`, naming isolated mode.
- **A worktree checkout's `.git` file.** Same redirection. `create` warns when
  the folder is a worktree.
- **A nested repository** the agent creates anywhere in the working tree, which
  an editor may detect and run git in.
- **Editor tooling** that runs code from the folder when it is opened as a
  trusted workspace.
- **A held working-tree file** (`include.path` target) after a host rename, as
  above.

## 3. Isolated

### Seeding

The folder is never mounted. The container's workspace is a volume `dev` owns:
a docker named volume `dev-<workspace>-<container>` on local, as a
`--no-folder` container has, and the existing PVC on k8s.

What goes into it is a standalone repository plus the working tree, never the
host's `.git` as files. A worktree's `.git` file names a host path that does
not exist in the container, and a copied `.git` directory carries the host's
config and hooks into a repository the agent then owns — harmless to the host,
but not the operator's settings to hand over. So:

1. On the host, `git bundle create - --branches` from the folder.
2. In the container, `git clone` the bundle into the workspace, check out the
   branch the folder has checked out (`symbolic-ref --short HEAD`, or the
   commit when detached), and set `origin` to the folder's `origin` URL when it
   has one.
3. Stream the working tree, excluding `.git`, over the result — uncommitted
   changes and untracked files arrive as they are on the host. Ignored files
   arrive too, which is how `node_modules` and `.env` reach the agent; k8s does
   this today with `tarDir`.

Checked with plain git: a worktree folder seeds to a repository with a `.git`
directory, the folder's branch checked out, its `origin`, and its uncommitted
and untracked files reported by `git status` as they were on the host.

Seeding happens once, on first create, exactly as `seedWorkspace` does on k8s:
it unpacks over the workspace, so reaching it from anywhere else would destroy
what the agent has half-done. `rebuild` keeps the volume. A folder that is not a
repository seeds the working tree alone.

On local this is new code in `internal/provider/local`, reusing the k8s
provider's archive walk rather than the host's `tar`, whose flags differ between
BSD and GNU.

### The relay

Unchanged. `--ssh-agent`, `--ssh-forward` and signing work as they do today:
the agent commits, signs through the relay, and pushes over ssh.

### `dev container fetch NAME`

Brings the container's branches into the folder it was created from:

1. In the container, `git bundle create - --branches`, streamed out over
   `Provider.Exec`.
2. On the host, in the source folder,
   `git -c transfer.fsckObjects=true fetch <bundle> 'refs/heads/*:refs/remotes/<container>/*'`.

The host fetches commit data only. Nothing from the container's config or hooks
is read: a bundle is passive data, and `fsckObjects` rejects malformed objects.
The branches land as remote-tracking refs, never over a local branch, so the
operator decides what to merge.

`fetch` refuses a guarded or legacy container (exit 2): their work is already
in the operator's folder. It refuses a container whose source folder has gone
(exit 1), naming the path.

Checked with plain git: a branch committed in the copy, and a second branch
created there, both arrive under `refs/remotes/<container>/`.

What `fetch` does not carry: uncommitted work in the container. The agent
commits, or the operator runs `git stash` and commits inside. A `--wip` that
captures it is deferred.

### Removing

`dev container remove NAME` on an isolated container first asks the container's
repository two questions:

- uncommitted changes: `git status --porcelain` is empty;
- unfetched commits: every branch tip is present in the source folder
  (`git cat-file -e <tip>^{commit}` on the host).

Either one failing refuses the removal (exit 2), listing the branches and
whether there are uncommitted changes, and naming `dev container fetch`.
`--force` removes anyway. A container that is not running is started for the
check; one that cannot be reached — an engine gone — refuses unless `--force`,
since the check could not be made.

A container seeded from a folder that was not a repository has no repository
to ask. `remove` refuses it (exit 2) without `--force`, saying its files are
not tracked anywhere and go with the volume: there is no cheap way to tell what
changed, and refusing is the side that cannot lose work.

## 4. Worktrees

`dev worktree create` makes the git worktree and nothing else:

```
dev worktree create task-1 --branch task-1 --base main --path ../app-task-1
cd ../app-task-1
dev container create task-1                     # guarded, on this folder
```

It keeps `--branch`, `--base`, `--path`, `--repo`, `--include-file`,
`--no-herdr` and the `.worktreeinclude` copy, and still registers the checkout
with Herdr. It loses every flag that described a container: `--no-start`,
`--generate`, `--tools`, `--no-persist-state`, `--agent-config`,
`--no-agent-config`, `--from`, `--allow-privileged`. All of them exist on
`container create`.

### `container create` on a worktree folder

The worktree mounts now come from the folder, not from a row that names the
container. `create` asks git: when the folder's `.git` is a file,
`git rev-parse --git-common-dir` gives the repository's common directory, which
is recorded on the container row (`worktree_repo`) and rebuilt into the mounts
on every command as today. A worktree made by `git worktree add`, by Herdr, or
by `dev worktree create` is handled the same way.

`a.worktreeRepo` reads the column instead of the `worktrees` table, and
`rewriteGeneratedTools` takes it from there too.

In guarded mode the common directory is mounted read-only (section 2). In
isolated mode nothing is mounted; the seed reads the folder on the host.

### `worktrees` table

A worktree no longer belongs to a container:

- keyed by `(workspace_name, name)`, with no foreign key to `containers`;
- `worktree list` shows each worktree and the containers whose `source` is its
  path, rather than one container name;
- `worktree remove` refuses (exit 2) while a container's `source` is the path,
  naming the containers, as `workspace remove` refuses while it holds
  containers. It never removes a container. Then git removes the checkout —
  refusing a dirty one without `--force`, as today — then the row, then Herdr.
- `container remove` on a container whose source is a worktree no longer
  warns: the checkout is the worktree's, and `worktree list` still shows it.
- `workspace remove` refuses (exit 2) while the workspace has worktree rows,
  naming them, as it already refuses while it has containers. The rows would
  otherwise go by cascade and leave checkouts on disk that nothing records.

## 5. `--folder` defaults to the current directory

`dev container create NAME` with neither `--folder` nor `--no-folder` uses the
current directory. Before anything is created it prints:

```
using ~/Projects/app (the current directory)
```

It refuses (exit 2) when that directory, resolved, is `/` or the operator's
home directory: a forgotten `cd` would otherwise mount everything under it. The
same refusal applies to an explicit `--folder`. `guard.Escape` already refuses a
project configuration that mounts either; this applies the rule to `dev`'s own
mount. `--folder` overrides; `--no-folder` is unchanged.

## 6. `container list`

The `GIT` column is replaced by `MODE`: `guarded`, `isolated`, `legacy`, or `-`
for a folderless container. `start`, `exec`, `shell` and `start-agent` print one
line on a legacy container whose `git_guard` is set:

```
dev: api predates container modes; its git guard can lapse — recreate it with --mode guarded or --mode isolated
```

A legacy container keeps working exactly as today, gaps included.

## Schema

Migration `0008_container_modes.sql`, portable SQL (invariant 5):

```sql
-- What a container's mode is; fixed at create. Existing rows are 'legacy' —
-- created under the single-file git guard — or 'none' when folderless, and
-- keep behaving exactly as they did.
ALTER TABLE containers ADD COLUMN mode TEXT NOT NULL DEFAULT 'legacy';
UPDATE containers SET mode = 'none' WHERE source_kind = 'none';

-- A worktree's common directory, for a container whose folder is a worktree;
-- '' otherwise. Read from git at create, so a worktree made outside dev is
-- mounted correctly too.
ALTER TABLE containers ADD COLUMN worktree_repo TEXT NOT NULL DEFAULT '';
UPDATE containers SET worktree_repo =
  (SELECT repo FROM worktrees
    WHERE worktrees.workspace_name = containers.workspace_name
      AND worktrees.container_name = containers.name)
  WHERE EXISTS (SELECT 1 FROM worktrees
    WHERE worktrees.workspace_name = containers.workspace_name
      AND worktrees.container_name = containers.name);

-- A worktree outlives any container on it, so the table loses its foreign key
-- and names itself. Rebuilt rather than altered: dropping a foreign key is not
-- portable.
CREATE TABLE worktrees_new (
  workspace_name  TEXT NOT NULL REFERENCES workspaces(name) ON DELETE CASCADE,
  name            TEXT NOT NULL,
  repo            TEXT NOT NULL,
  branch          TEXT NOT NULL,
  path            TEXT NOT NULL,
  herdr_workspace TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  PRIMARY KEY (workspace_name, name)
);
INSERT INTO worktrees_new
  SELECT workspace_name, container_name, repo, branch, path, herdr_workspace, created_at
  FROM worktrees;
DROP TABLE worktrees;
ALTER TABLE worktrees_new RENAME TO worktrees;
```

The new table cascades from `workspaces`, which only matters as a backstop:
`workspace remove` refuses while worktree rows exist (section 4).

`model.Container` gains `Mode` and `WorktreeRepo` becomes persisted (today it
is set per invocation from the `worktrees` table). `model.Worktree` renames
`ContainerName` to `Name`.

## Command surface

```
dev container create NAME [--mode guarded|isolated] [--folder PATH | --no-folder] ...
dev container fetch NAME [--workspace W]
dev container remove NAME [--force]          # refuses unfetched isolated work
dev worktree create NAME --branch B --path P [--base REF] [--repo R]
                         [--include-file F] [--no-herdr] [--workspace W]
dev worktree remove NAME [--force]           # refuses while a container uses it
```

Exit codes follow invariant 7: a contradictory or impossible request is 2, a
missing name is 3, a failure is 1.

## Testing

Unit (`make test`, stub executables on a temporary `PATH`, no engine):

- the `mode` column round-trips; the migration marks existing rows `legacy` and
  folderless ones `none`; `worktree_repo` is carried from the old `worktrees`
  rows; the new `worktrees` table has no foreign key to `containers`;
- `--mode` with `--no-folder`, `--mode guarded` on k8s, an unknown mode, and a
  folder of `/` or home are each exit 2;
- `--folder` absent uses the working directory and prints it;
- a guarded container's generated document carries exactly one `.git` mount,
  read-only, and none of the single-file mounts; the merged document for a
  project-owned one the same;
- a guarded project naming its own `workspaceMount` is refused;
- `--ssh-agent` on a guarded container is exit 2; `--ssh-forward` does not
  forward into one;
- `container create` on a worktree folder records `worktree_repo` from git;
- `worktree create` makes no container row; `worktree remove` refuses while a
  container's source is its path;
- `fetch` refuses guarded and legacy containers;
- `remove` refuses an isolated container with unfetched branch tips or
  uncommitted changes, and `--force` removes it.

Smoke (`make smoke`, real engine):

- guarded: the `.git` mount refuses writes by any name, before and after host
  git writes its config, branches and a commit; host git works throughout;
  `git status`, `log` and `diff` work inside and `commit` fails. This is
  `TestSmokeReadOnlyGitDir` from PR #28, moved onto `dev` itself rather than the
  engine directly.
- isolated: create from a folder with uncommitted and untracked files; they are
  there in the container; the agent commits; `fetch` brings the branch home;
  `remove` refuses before the fetch and succeeds after it.
- isolated from a worktree folder: the container's `.git` is a directory and
  `git log` works.
- legacy: a container created before the migration still starts.

## Documentation

- `docs/USAGE.md`: the two modes and how to choose, the guarded workflow
  (review live, commit from the host), the isolated workflow (`fetch`), the
  `--folder` default, `worktree create` without a container, the `MODE` column.
  "What a container can do to your git" is rewritten: it currently says
  `.git/config` is read-only inside, which is not always so for legacy
  containers.
- `docs/SECURITY.md`: rewritten for a reader new to `dev` — see that file.
- `CLAUDE.md`: invariant 11 and the k8s note describe worktree containers as
  `worktree create` making them; both change. A new invariant states that a
  guarded `.git` is one directory mount and never single-file mounts, and why.

## Order

1. Migration, model and store: `mode`, persisted `worktree_repo`, the
   `worktrees` table. Behaviour unchanged; every new row is still created as
   today, marked `legacy`.
2. `--folder` default and the home and `/` refusal.
3. `worktree create` without a container; `container create` detecting a
   worktree folder; `worktree remove` and `list` on the new table.
4. Guarded: the `.git` mount, the relay refusal, the `checkTreeMounts` probe,
   the `workspaceMount` refusal, the warnings. `create` writes `guarded` from
   here on.
5. Isolated on local: the volume, seeding, `fetch`, the remove refusal. On k8s:
   `mode` recorded, `guarded` refused.
6. `container list` `MODE` and the legacy warning.
7. Documentation.

Each step leaves `make lint && make test` green and is its own commit.

## Not in this change

- removing the legacy guard's code: it stays until no legacy row is left;
- re-applying a broken mount, comparing config after each command, or watching
  the host during a session;
- a `--wip` capture of uncommitted isolated work;
- today's guard as a third mode;
- an isolated container seeded from something other than its folder's checked
  out branch — branching is `worktree create`'s job.
