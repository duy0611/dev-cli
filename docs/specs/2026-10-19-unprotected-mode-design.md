# Unprotected mode, and changing a container's mode

A third mode, `unprotected`, mounts the operator's folder live with `.git`
writable, so the agent can commit, branch, rebase and push while the operator
watches its edits on the host. `dev container rebuild --mode` moves an existing
container between modes, so the operator can open a guarded container up for a
stretch of work and close it again without losing it.

This changes two decisions in `docs/specs/2026-10-12-container-modes-design.md`:
"Changing it: never" and "A third mode keeping today's guard: not built". The
rest of that spec stands.

## Problem

A guarded container's agent cannot commit. That is the mode's trade, and for
most work it is the right one: the operator reviews live and commits from the
host.

It breaks down when a change grows big enough to land as a stack of smaller
PRs. The agent then needs to commit, branch, rebase and push many times while
it works, and the operator has to do each of those by hand from the host.

Today's only way out is a new isolated container. That costs twice:

- the operator loses the live view of the files, since an isolated container
  works on its own copy and its work is only visible after `dev container
  fetch`;
- creating a container in the middle of the work means stopping, recreating
  and starting the agent over.

A second repository for the agent over the same live folder was considered and
rejected: two repositories sharing one working tree each see the other's
commits as uncommitted changes, and a `checkout` or `rebase` on either side
rewrites the files under the other. If the agent needs its own repository, the
operator uses isolated mode.

## Intent

The operator chooses, knowingly and for one container, to give up guarded's
protection so the agent can run git as it would anywhere else. `dev` makes that
quick and says plainly what it costs. It does not try to make it safe.

## Goals

- An `unprotected` mode: folder live, `.git` writable, the ssh relay allowed.
- `dev container rebuild NAME --mode MODE` changes the mode of an existing
  container, keeping everything a rebuild keeps today.
- The allowed changes:

  | From \ To | guarded | unprotected | isolated |
  |---|---|---|---|
  | guarded | – | yes | yes |
  | unprotected | yes | – | yes |
  | isolated | no | no | – |

## Non-goals

- Making unprotected safe, or detecting what the agent did while it was
  unprotected.
- Checking `.git` when switching from unprotected back to guarded. This is the
  operator's decision: if an unprotected container was hijacked, the host is
  already exposed, and going back to guarded does not change that. Guarded's
  promise holds from the switch onward, never backwards.
- Leaving isolated mode. An isolated container's work lives in its own copy;
  the way out stays `dev container fetch`, then a new container.
- Unprotected on Kubernetes. A pod cannot mount a live folder, which is the
  whole point of the mode.

## Anti-goals

- **Unprotected passing for guarded.** Wherever a container's mode is shown
  (`container list`, `inspect`), it reads `unprotected`. Nothing describes an
  unprotected container as guarded or partly guarded.
- **Silent switching.** Every mode change is printed and goes in the audit log
  with the old and new mode.
- **Losing work on a switch.** A switch to isolated copies the folder as it is
  at that moment, uncommitted changes included. A failed rebuild leaves the
  stored mode matching the container that still runs.

## Decisions

| Decision | Choice |
|---|---|
| Name | `unprotected` |
| How it is chosen | `--mode unprotected` on `container create`, and `--mode` on `container rebuild` |
| Default | unchanged: guarded on local, isolated on k8s |
| Providers | local only; k8s refuses it (exit 2), as it refuses guarded |
| Unprotected's `.git` | not mounted separately: it is part of the folder's bind mount, writable |
| Relay in unprotected | allowed, exactly as in isolated: `--ssh-agent`, `--ssh-forward`, signing |
| Back to guarded | no check of `.git`; the operator's call, documented |
| Out of isolated | refused (exit 2), pointing at `fetch` and a new container |
| Legacy and folderless | `rebuild --mode` refused (exit 2) |
| Schema | no migration: `mode` is already free text |

## 1. Unprotected

### Mounts

The folder is bind-mounted live, as guarded mounts it, with nothing over
`.git`. For a folder that is a git worktree, the checkout is mounted at its
host path as invariant 11 requires, and its common directory is mounted
**writable** at its own host path, so the checkout's `.git` file resolves and
commits work. Extra folders get the same treatment, folder by folder.

Neither half of the guard runs: no `.git` mount, no `FindTree` search and no
held working-tree files, no `probeHeldFiles` before each command. With `.git`
itself writable, holding an `include.path` target read-only protects nothing.

`checkGuarded`'s refusal of a project that sets its own `workspaceMount` does
not apply: there is nothing to aim a mount at. The project's mount is used as
the project wrote it, so whether the folder is live is the project's choice.
*Builder's call.* Its submodule and worktree warnings do not apply either;
they describe gaps in a guard that is not there.

### The relay

Allowed, as in isolated. The guarded refusal in `forwardAgent` and
`agentForwarded` stays for guarded only. Nothing about the relay is stored on a
container: `--ssh-agent` is per command and `--ssh-forward` is per workspace.
So a switch into guarded needs no cleanup; from the next command, guarded's
existing refusal and notice apply. `SECURITY.md`'s advice — an ssh agent
holding only the key it needs — applies to unprotected as it does to isolated.

### What the operator sees

- `create --mode unprotected` and a `rebuild` into it print one line saying the
  container can now change what git on the host runs, and naming
  `rebuild --mode guarded` as the way back.
- `fetch` refuses an unprotected container (exit 2), as it refuses guarded:
  its work is already in the folder.
- `remove` makes no check, as for guarded.
- `container list` shows `unprotected` in `MODE`.

*Builder's call:* no confirmation prompt. The flag is the operator's
deliberate act, and a prompt would need its own flag for scripted runs.

## 2. Changing mode: `rebuild --mode`

```
dev container rebuild NAME --mode guarded|unprotected|isolated [other rebuild flags]
```

`--mode` equal to the current mode is a no-op for the mode and the rebuild goes
ahead. A disallowed change is exit 2 and touches nothing: the message names the
two modes and the way to get there (for isolated: `fetch`, then a new
container).

Refused (exit 2) before anything is rebuilt:

- out of isolated, to anything;
- a `legacy` or folderless (`none`) container;
- `guarded` or `unprotected` on the k8s provider;
- into `guarded` when the project sets its own `workspaceMount` — the same
  refusal `create` makes, through the same `checkGuarded` call, which also
  prints the submodule and worktree warnings;
- a folder set the new mode refuses. Where an extra folder appears depends on
  the mode (`placeFolder`, `folders.ContainerPath`), so every folder is placed
  again for the new mode and `checkFolders` runs on the result. *Builder's
  call.*

The order, so a refusal or a failure leaves the old container and its record
consistent:

1. Validate the change as above.
2. Plan and materialise the configuration for the **new** mode, as
   `--add-folder` does today with `retargetFolders`: the rebuilt container must
   have the new mode's mounts from its first command.
3. Run the existing drift and escape checks.
4. Rebuild.
5. Only after success, write the mode (`store.SetMode`, new) and record
   `rebuild` in the audit log with `mode_from` and `mode_to`. For a switch
   into isolated, "success" is the seed, not the lifecycle hooks; see below.

`--mode` combines with `--add-folder`/`--remove-folder`, which today accept
only guarded containers; they accept unprotected ones too.

### Into isolated

The container's workspace changes from the bind-mounted folder to a volume
`dev` owns, so this rebuild seeds: each folder is copied into its volume
exactly as on first create — a repository rebuilt from a bundle plus the
working tree, uncommitted and untracked files included — and the lifecycle
hooks are run after seeding as they are on first create.

The local provider today seeds only when `up` finds no container, and never on
a recreate. A switch into isolated is the one recreate that seeds; it needs an
explicit signal from the CLI rather than a guess in the provider, since a
seeding recreate on a container that is already isolated would destroy agent
work.

If a volume of the container's workspace name already exists — left from an
earlier isolated life — the rebuild refuses (exit 1) naming it, rather than
seeding over or reusing it. *Builder's call:* the safe side; it should not
happen, since leaving isolated is refused.

A failure on the way into isolated (*builder's call*):

- **`up` or seeding fails.** Nothing has been done in the copy yet, and the
  host folder still holds everything. `dev` removes the new container and the
  volumes it created, leaves the row in the old mode, and says so. The operator
  reruns `rebuild --mode isolated`, or `rebuild` to get the old container back.
- **Seeding succeeds, a lifecycle hook fails.** The container is isolated and
  its volume is the copy from now on, so the mode is written as `isolated`
  before the hooks run. The hook failure is reported as on first create.

A failure on the way between guarded and unprotected is any rebuild failure:
the row keeps the old mode, and the container may be gone, as it may be after
any failed rebuild today. Rerunning `rebuild --mode` retries.

### Out of guarded or unprotected, into the other

Only the mounts change. The bind-mounted folder, the state volume, the
container's settings, folders and agent config are kept, as by any rebuild.

The rebuild replaces the container, so anything running in it — an agent
session included — stops, as on any rebuild. `start-agent` afterwards resumes
where the agent keeps its own state.

## 3. What changes elsewhere

### Code

- `model`: `ModeUnprotected`; the "Fixed at create" comments go.
- `store`: `SetMode(workspace, name, mode)`.
- `cli`: `modeFor` accepts `unprotected` (refused on k8s); `create` stores it;
  `rebuild` gains `--mode`; `planGitDir`, `forwardAgent`, `agentForwarded`,
  `folders`, `fetch`'s `requireIsolated` message and `displayMode` learn the
  new mode.
- `provider/local`: an explicit "seed on this recreate" for a switch into
  isolated.

Every existing `c.Mode == ModeIsolated` / `ModeGuarded` branch needs a
decision for unprotected; the builder lists them and checks each one. The
safe default where a branch is about protection is "behaves like guarded's
folder handling, minus the guard".

### Documentation

- `docs/USAGE.md`: the third mode, `rebuild --mode` and the table of allowed
  changes, the workflow (open up for a stack of PRs, close again), `MODE`
  showing `unprotected`, the unprotected line in "What a container can do to
  your git".
- `docs/SECURITY.md`: "It cannot be changed later" goes. The modes table gains
  an unprotected column that says plainly it gives up the protection. A
  section says what switching back to guarded does and does not mean: guarded
  from the switch on, nothing undone. The ssh-agent advice covers unprotected.
- `docs/ARCHITECTURE.md`: invariant 12 is rewritten — a mode is guarded,
  unprotected or isolated; it changes only through `rebuild --mode` and only
  along the allowed edges; a guarded `.git` is still one read-only directory
  mount. Invariant 10's "mounted on must not change under it" is kept by
  writing the mode only after a successful rebuild, never by `start` or
  `exec`.
- `docs/specs/2026-10-12-container-modes-design.md` is left as the record of
  its decision; this spec supersedes the two rows named at the top.
- `AGENTS.md`, "Git in the dev container": a commit that works no longer means
  isolated. The rule becomes: if `git commit` fails with `Read-only file
  system`, guarded; otherwise commit on a branch and push if asked. Only an
  isolated container's work is brought home with `dev container fetch`.

## Testing

Unit (stub executables, no engine):

- `create --mode unprotected` stores the mode; on k8s it is exit 2;
- an unprotected container's generated and merged documents carry no `.git`
  mount; a worktree folder's common directory is mounted writable;
- `--ssh-agent` works on an unprotected container;
- `rebuild --mode` follows the table: each allowed edge stores the new mode
  after a successful rebuild; each refused edge, legacy and folderless are
  exit 2 and leave the row untouched; a failed rebuild leaves the old mode;
- into guarded with a project `workspaceMount` is refused;
- into isolated passes the seed signal to the provider; a recreate without it
  never seeds;
- the audit record names both modes;
- `fetch` refuses unprotected.

Smoke (real engine):

- guarded → unprotected: `git commit` inside succeeds and shows in host
  `git log`; → guarded: `git commit` inside fails with `Read-only file system`
  and `.git` refuses writes by any name.
- guarded → isolated: uncommitted and untracked files from the host are in the
  volume; the agent commits; `fetch` brings the branch home.
- isolated → guarded: exit 2, container unchanged.

## Left to the builder

- Flag help, message wording and where the unprotected warning line prints.
- How the seed signal reaches the provider (a field on the container value, a
  rebuild option, a separate method).
- The order of steps within a commit series; each leaves
  `make lint && make test` green.
