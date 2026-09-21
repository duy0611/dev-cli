# Git worktree containers

Working two branches of one repository at once means two checkouts. Today that
is four commands and a path the operator has to invent:

```
git worktree add ~/wt/app/fix-header -b fix-header
dev container create fix-header --folder ~/wt/app/fix-header
dev container start-agent fix-header
# ...and later, remember which checkout belonged to which container
```

The last step is where it goes wrong. `dev container remove fix-header` takes
the container and leaves the checkout, and nothing records that the two were
ever related. A week later the operator has a directory of stale worktrees and
no way to tell which are finished.

This adds one noun that owns both:

```
dev worktree create fix-header --branch fix-header --path ~/wt/fix-header
dev worktree remove fix-header
```

`create` adds the checkout, registers it with Herdr when Herdr is running,
creates the container and starts it. `remove` reverses exactly that, in reverse
order, and never touches the branch.

## The gitdir problem

A worktree's `.git` is not a directory. It is a file holding one line:

```
gitdir: /home/u/src/app/.git/worktrees/fix-header
```

An absolute path, outside the checkout. Bind-mount the checkout alone into a
container and every git command inside it fails, because that path does not
exist there.

The repository side has a matching pointer. `<repo>/.git/worktrees/<name>/gitdir`
holds the absolute path of the checkout's `.git` file, and the two are read
together: git follows the first to find the administrative directory and the
second to confirm the checkout is still where it was.

That second pointer is the dangerous one. `git worktree prune` deletes an
administrative directory whose backlink names a path that does not exist:

```
$ mv ../wt ../wt-moved
$ git worktree prune -n -v
Removing worktrees/wt: gitdir file points to non-existent location
```

and `git gc --auto` runs `worktree prune` as part of its work. Auto-gc fires
from ordinary commands once enough loose objects accumulate. So a container that
can see `<repo>/.git` but not the checkout at its host path is not merely
missing a mount — an agent committing in a loop inside it will eventually prune
the worktree's registration out of the repository, from the inside, with no
error to show for it.

Both pointers are absolute host paths, so both are satisfied the same way: mount
each at the identical path it has on the host.

| host path | container path | why |
|---|---|---|
| `<repo>/.git` | the same | resolves the checkout's `gitdir:` line; shares the object store |
| `<worktree>` | the same | satisfies the admin backlink, so `prune` leaves it alone |

The checkout is mounted once, not twice: for a generated document
`workspaceFolder` and `workspaceMount` name the host path directly, so the
second row of that table *is* the workspace mount rather than an extra one.
Naming them explicitly is what makes that work — left alone, the devcontainer
CLI bind-mounts the folder at `/workspaces/<basename>`, which is not the path
the backlink holds.

Rewriting the `.git` file to a container-local path was the alternative and is
worse. It makes the checkout's pointer agree with the container and the
repository's backlink disagree with both, which is the pruning case above
rather than a fix for it, and it writes into a file the host's git owns.

`safe.directory` needs nothing: the devcontainer CLI UID-remaps a bind mount to
the host user, so the checkout and `.git` arrive owned by the user the container
runs as. This is the same remapping the state volume does *not* get, which is
why that one needs a chown and these do not.

### Why not mount the whole repository

Mounting `<repo>` and pointing `workspaceFolder` at the checkout inside it would
resolve everything with one mount and no path trickery. It is rejected because
the container then sees the main checkout and every sibling worktree. An agent
told to work on one branch can edit another, which is the isolation a worktree
exists to provide. The two-mount arrangement gives the container exactly one
working tree and the object store behind it.

## Two mechanisms, one outcome

The same split the state volume already lives with, for the same reason.

A container `dev` generated gets both mounts in its document:

```json
"workspaceFolder": "/home/u/wt/fix-header",
"workspaceMount": "source=/home/u/wt/fix-header,target=/home/u/wt/fix-header,type=bind",
"mounts": ["source=/home/u/src/app/.git,target=/home/u/src/app/.git,type=bind"]
```

A project that ships its own `.devcontainer` cannot, because `dev` never writes
into a project folder — invariant 9. That container gets the `.git` bind from
`devcontainer up --mount`, which exists on `up` and **not** on `exec`, so it must
never enter `execArgs` where it would be a flag error on every command run inside
the container.

For that container the checkout needs a `--mount` of its own as well. The CLI
bind-mounts `--workspace-folder`, but at `/workspaces/<basename>` rather than at
the host path, and the backlink names the host path. A generated document avoids
the second mount by naming `workspaceMount` itself; a project-owned one cannot,
so it takes two `--mount` flags and accepts the checkout appearing at both
paths.

Exactly one route per container, never both. Docker refuses the whole run with
`duplicate mount destination` when one target arrives twice, so `up` passes
`--mount` for the `.git` bind only when there is no generated document that has
already named it. The state volume's history is the warning here: the first
version of that passed `--mount` unconditionally and every generated container
failed to start, and the unit tests missed it because they covered the
project-owned case where the flag belongs.

One consequence worth saying plainly. For a project-owned container the working
tree appears at two paths — the CLI's `/workspaces/<basename>` and the host path
the backlink needs. They are the same files. The host path exists for git; the
CLI's path is where the operator and the agent will be sitting. A generated
container has one path for both, which is the better arrangement and the reason
`--generate` is worth reaching for here.

### dcgen renders a list

`render.go` assigns `doc["mounts"]` outright for the state volume. A worktree
container can need both that volume and the `.git` bind, so the field becomes a
collected slice — the same shape `postCreate` already uses in that function, and
for the same reason: assigning twice would silently keep only the last.

The slice is appended to in a fixed order and never sorted, because the order is
already deterministic and the byte-stability test that makes the stored document
comparable depends on it staying that way.

## Surface

```
dev worktree create NAME --branch B --path P [--repo R] [--base REF]
                         [--generate] [--tools ...] [--no-persist-state]
                         [--no-start] [--no-herdr] [--workspace W]
dev worktree list [--all]
dev worktree remove NAME [--force] [--workspace W]
```

`NAME` names the worktree record and the container both. One unit, one name: a
second name to remember would be the bookkeeping this command exists to remove.
It is validated by `xpath.ValidateName` like any container name.

`--branch` and `--path` are required, and `--path` has no default. A default
would have to invent a directory somewhere — under `DEV_STATE`, or beside the
repository — and both put directories on the operator's disk in a place they did
not choose. Requiring it costs one flag and means nothing ever appears
somewhere surprising. If a default is wanted later, `DEV_STATE/worktrees/` is
the place for it, since that is already a directory `dev` owns.

`--repo` defaults to the repository containing the working directory, found with
`git rev-parse --show-toplevel`. From inside a worktree that returns the
worktree, so the repository is taken from `--path-format=absolute
--git-common-dir` and its parent: creating a worktree from inside another
worktree of the same repository then works, which is the case that would
otherwise fail confusingly.

`--base` names the start point when the branch does not exist yet. An existing
local branch is checked out as it is, and `--base` with an existing branch is a
usage error rather than a silent no-op.

The pass-through flags mean on `worktree create` exactly what they mean on
`container create`, and reach the same code.

### What create does

1. Resolve `--repo` and `--path`. The repository must exist and be a repository;
   the path must *not* exist, since `git worktree add` refuses an existing
   directory (`fatal: '../wt' already exists`).
2. `git worktree add [-b BRANCH [BASE] | BRANCH] PATH`. Failures surface
   verbatim — `'feat' is already used by worktree at …` says more than anything
   this could write.
3. Resolve the created path with `xpath.Resolve`. This happens *after* the add,
   not before: `git worktree add` records the resolved path in its backlink
   (given a symlinked path it stores the real one), and the mount has to match
   what git wrote, not what the operator typed.
4. Register with Herdr, best-effort. See below.
5. Create the container record and the worktree row in one transaction, then
   start the container unless `--no-start`.

A failure after step 2 rolls the checkout back with `git worktree remove
--force` before returning, so a half-made worktree is not left behind by a
command that reported an error. The rollback is skipped once the container is
started — at that point the operator has something worth keeping, and the error
names what to clean up instead.

### What remove does

Container first, then git, then the row. The order is what makes a refusal safe:

```
$ dev worktree remove fix-header
fatal: '/home/u/wt/fix-header' contains modified or untracked files, use --force to delete it
```

`git worktree remove` refuses a dirty checkout, and that refusal is surfaced as
it is. `--force` passes through to git. Because the row is written last, a
refusal leaves the record intact and the command retryable — the same shape
`container remove` already has when the engine refuses.

The container is removed before git is asked, so a `--force` removal of a dirty
checkout is not racing an agent still writing into it.

**The branch is never deleted.** Herdr's own worktree removal makes the same
promise, and so does git's. The branch is the work; the checkout is scaffolding.
There is no `--delete-branch`: an operator who wants the branch gone can say so
to git, where the consequences are theirs and legible.

## Herdr

Herdr has worktree commands of its own. `dev` does not use them for the git
work: `git worktree add` is one call with no daemon in the way, and a feature
this central should not stop working when an optional tool is not installed.
Herdr gets the checkout *after* it exists, so it appears in the sidebar grouped
under the repository's workspace.

Availability is two checks, not one: the binary on `PATH`, and `herdr status
server` exiting 0. An installed Herdr whose daemon is not running would
otherwise turn every `worktree create` into a hung or failing call.

| step | call |
|---|---|
| after `git worktree add` | `herdr worktree open --path <resolved>` |
| during `worktree remove` | `herdr worktree remove --workspace <id> --force` |

Both are best-effort. A Herdr failure warns through `warnf` and the `dev`
command carries on: Herdr is a view onto the checkout, and a view that failed to
open is not a reason to refuse the checkout. `--no-herdr` skips the probe
entirely, for a host where Herdr is installed but should be left alone.

The workspace ID Herdr returns is stored on the worktree row, because
`herdr worktree remove` takes an ID and there is nothing to derive it from
later. An empty column means Herdr was absent or declined, and removal skips it.

The removal call is Herdr state only; `dev` still runs the git removal itself.
This is the division Herdr's own documentation draws between `workspace close`
and `worktree remove`, and following it means neither tool is surprised by the
other.

## Schema

Migration `0005_worktrees.sql`:

```sql
CREATE TABLE worktrees (
  workspace_name  TEXT NOT NULL,
  container_name  TEXT NOT NULL,
  repo            TEXT NOT NULL,
  branch          TEXT NOT NULL,
  path            TEXT NOT NULL,
  herdr_workspace TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  PRIMARY KEY (workspace_name, container_name),
  FOREIGN KEY (workspace_name, container_name)
    REFERENCES containers(workspace_name, name) ON DELETE CASCADE
);
```

A table rather than columns on `containers`. Most containers are not
worktree-backed, and four mostly-empty columns on the table every command reads
is the wrong trade for one join in one command. `model.Container` stays what it
is.

The cascade is what makes `dev container remove` safe to leave alone: it cannot
leave a row describing a container that is gone. It depends on
`PRAGMA foreign_keys = ON`, which invariant 6 already requires and a test
already asserts.

`path` duplicates what `containers.source` holds for the same row. That is
deliberate: `source` is what the container is mounted from and `path` is what
git was told to create, and keeping them separate means a future container whose
source diverges from its checkout does not corrupt the removal. They are equal
today and a test says so.

Portable SQL: literal defaults, `TEXT` timestamps written by the application,
no `AUTOINCREMENT`. It replays against Postgres with the rest.

An empty `herdr_workspace` rather than a nullable column, matching how the
schema already treats "nothing to say" elsewhere and keeping the scan free of
`sql.NullString`.

## Provider scope

`dev worktree` is local-only. A k8s workspace gets a usage error (exit 2) naming
the reason.

Kubernetes has no host bind mounts. The provider streams the workspace in as a
tar, which would deliver the checkout's files and a `.git` file pointing at a
path that does not exist in the pod. Streaming `<repo>/.git` in as well would
make git work and create a second object store that drifts from the host the
moment either side commits, with nothing to push it back — a much larger feature
wearing this one's name.

Refusing is the honest answer while that is true. The k8s provider is
experimental and this is recorded as a limitation, not a defect.

## New packages

`internal/gitwt` shells out to `git`, per the shell-out convention: no go-git,
no repository parsing.

| function | wraps |
|---|---|
| `Root(dir)` | `rev-parse --path-format=absolute --git-common-dir`, then its parent |
| `Add(repo, path, branch, base)` | `worktree add` |
| `Remove(repo, path, force)` | `worktree remove` |
| `List(repo)` | `worktree list --porcelain` |

`List` exists for `dev worktree list`, which reports whether the checkout git
knows about still matches the row — the one place a stored path can go stale,
since an operator can always run `git worktree remove` themselves.

`internal/herdr` is `Available`, `Open` and `Close`, as above.

`internal/cli/worktree.go` parses, orchestrates and prints. `container.go` is
already 716 lines and the container-creation body is extracted so both callers
share it rather than copied — the extraction is the only change `container.go`
needs.

## One loose end, named rather than hidden

`dev container remove NAME` on a worktree-backed container removes the container
and cascades the row away, leaving the checkout on disk with nothing recording
it.

Container commands stay as they are, so the guard is a warning rather than a
refusal: `container remove` looks for a worktree row, and when it finds one
prints the leftover path and the command that would have cleaned it up, then
proceeds. No new flag, no new failure mode, and the operator learns what
happened at the moment it happens.

Refusing was considered and rejected: it makes `container remove` fail for a
container it can perfectly well remove, and an operator who deliberately wants
the container gone and the checkout kept has no way to say so.

## Testing

Everything runs under `make test` against the stub-executable harness, with
`DEV_STATE` pointed at a temporary directory. Stub `git` and `herdr` on a
temporary `PATH`, replacing it rather than preceding it, as the k8s harness
already does and for the reason recorded there.

- `internal/gitwt`: `Root` returns the repository from inside a worktree, not
  the worktree; `Add` passes `-b` only for a new branch; a dirty `Remove`
  surfaces git's own message. These use real `git` via `requireRealCLI` — git is
  cheap, hermetic and needs no network, so stubbing it would only test the stub.
- `internal/dcgen`: a worktree render carries the `.git` bind in `mounts` and
  the checkout in `workspaceMount`; a container that is both worktree-backed and
  state-persisting carries both mounts and chowns only the state directory; the
  output stays byte-stable.
- `internal/provider/local`: `up` passes both `--mount` flags on a project-owned
  worktree container and neither on a generated one; `execArgs` never carries
  one; a worktree container that also persists state passes three, with no
  target repeated.
- `internal/cli`: `worktree create` writes both rows; a k8s workspace exits 2;
  `worktree remove` keeps the row when git refuses; `container remove` warns and
  proceeds; Herdr absent is not an error, and `--no-herdr` makes no call at all.
- `internal/store`: migration test in the shape of `migrate_gpg_test.go` — seed
  the older schema, migrate, delete a container, confirm the worktree row went
  with it.
- `test/smoke`: create a worktree container against a real engine, commit inside
  it, confirm the commit is visible from the host repository and that
  `git worktree list` there still shows the checkout. That last assertion is the
  pruning case, and it is the only test that can prove it.

The smoke test is what proves git actually works inside the container. The unit
tests prove the arguments are built correctly, which is a different claim.

## Documentation

`docs/USAGE.md` gains a walkthrough between "Run an agent on a project" and "A
scratch sandbox with no project", and a `### worktree` section in the command
reference. `CLAUDE.md` gains the gitdir invariant — both mounts at their host
paths, and why the second one is not optional.
