# Agent hardening

The build half of `docs/plans/2026-10-08-agent-hardening.md`. The plan's
catalogue is the record of what was considered and why most of it is not built;
this spec does not repeat it. Where the two differ, this spec is current.

## Problem

Three ways a fooled agent reaches past its container, and one way nobody can
tell afterwards what happened:

1. **Host git runs what the agent wrote.** The workspace is a writable bind
   mount, and host git — including the IDE's `git status`, every few seconds —
   executes programs named in files under `.git/`. One
   `git config core.fsmonitor '<cmd>'` inside the container is code on the
   operator's host.
2. **A project configuration can ask for the host.** `privileged`, the engine
   socket, host namespaces, a bind mount of `/` or `$HOME` — from the project's
   `devcontainer.json`, its `runArgs`, or any feature it names — are applied
   without question.
3. **`.devcontainer/` changes between `create` and `rebuild` unseen.**
   `initializeCommand` runs on the host, and a Dockerfile `RUN` runs in the
   engine with whatever the build is given.
4. **No record.** Nothing says what ran in which container, when, or which
   kube token was minted for it — which is what deciding what to revoke needs.

## Goal

A container created after this lands cannot make host git run a program of its
choosing through any route this spec names, cannot start with a configuration
that reaches the host unless the operator said so by flag, and cannot have its
configuration change under a `rebuild` without the operator saying so by flag.
Every command `dev` runs against a container leaves a line in a log on the host.

Nothing here changes what an agent can do *inside* its container. Root, `sudo`,
the network and the workspace's credentials are as they were; the plan explains
why.

## Decisions

- **Hardened by default; relaxed by flag, never by file.** `--allow-privileged`
  and `--accept-config` are operator flags. Nothing in the project can turn a
  guard off, because the project is a file the agent can edit.
- **New rows only.** Each guard is recorded on the container row at `create`.
  Migration defaults keep existing rows exactly as they behave today; a
  container is guarded once it is recreated. No adoption path.
- **Refuse, never strip.** A configuration that asks for the host is refused
  with what it asked for. Silently removing `privileged` would leave
  docker-in-docker broken in a way that reads as a docker bug.
- **The guard overrides the project.** A project's own `devcontainer.json`
  still wins over anything `dev` would generate; it does not win over these.
- **No writing into the project folder** (invariant 9), with one stated
  exception: a missing `.git/hooks` directory is created empty, because a bind
  mount needs a target and the alternative is leaving the route open. It is
  what `git init` would have created.
- **The local provider is where the git guard and escape guard live.** A k8s
  pod has no host bind mounts — its workspace is a copy on a PVC that host git
  never reads — and builds its own pod spec, ignoring the configuration's
  `privileged`, `runArgs` and `mounts`. The drift check and the audit log apply
  to both providers.

## 1. Git guard

### What host git executes

Tested against git 2.49, each route below ran a command on `git status` from
the host:

| Route | Agent writes | Notes |
|---|---|---|
| `.git/config` | `core.fsmonitor`, `core.hooksPath`, `core.sshCommand`, `core.pager`, `diff.*.textconv`, filters, `credential.helper` | the direct route |
| `.git/hooks/*` | a hook script | runs on commit, checkout, merge, push |
| `.git/commondir` | a new file naming any directory | git then reads `<that dir>/config`. Not in the plan. |
| A worktree checkout's `.git` file | `gitdir: <any dir>` | git then reads that directory's `commondir` and `config`. Not in the plan. |
| A nested repository registered as a gitlink | `git init sub && git config core.fsmonitor … && git update-index --add --cacheinfo 160000,…,sub` | host `git status` recurses into it with no `.gitmodules` entry |
| `.git/config.worktree`, `<common>/worktrees/*/config.worktree` | `core.fsmonitor` | read only when `extensions.worktreeConfig` is set, which is a main-config write |

### Mounts

All bind mounts, through the
routes the state and worktree mounts already use: `mounts` in a generated
document, the merged `--override-config` for a project-owned one, on `up` and
`exec` alike (invariants 10 and 11).

| Target | Mount | Why |
|---|---|---|
| `<gitdir>/config` | host file, read-only | git writes config by lock-and-rename; a rename onto a bind-mounted file fails with `EBUSY`, so a writable copy behaves exactly like read-only. Tested. |
| `<gitdir>/hooks` | a container-only copy, writable | a directory: writes inside it succeed and land where host git never looks |
| `<gitdir>` itself | host directory, writable, bound onto itself | a mount point cannot be renamed or removed (`EBUSY`, tested), so the agent cannot swap `.git` for a directory it built |
| worktree checkout's `.git` file | host file, read-only | closes the `gitdir:` redirect |
| `<common>/worktrees/<name>/commondir` and `gitdir` | host files, read-only | closes the same redirect one level down |
| `config.worktree`, where it exists | host file, read-only | only when the repository already uses `extensions.worktreeConfig` |

`<gitdir>` is the folder's `.git` for an ordinary checkout and the common
directory for a worktree container (whose `config` and `hooks` live there).
A folderless container has no host `.git` and gets none of this, nor does a
folder that was not a git repository at create.

**Where `.git` lands.** A worktree container mounts the checkout and the
common directory at their own host paths (invariant 11), so every target is
the host path. Every other container gets the devcontainer CLI's default
layout, which `gitguard.LayoutOf` mirrors: the CLI mounts the repository's
root rather than the folder it was handed (`mount-workspace-git-root`, default
on), found by walking up to the first `.git/config`, at
`/workspaces/<basename of the root>` — so the targets are under that path. A
project whose `devcontainer.json` names its own `workspaceMount` or
`workspaceFolder` replaces that rule, and `.git` lands somewhere dev cannot
predict; it gets no mounts, and instead `.git` is fingerprinted (config, every
hook, `commondir`, `config.worktree`) before each `exec`, `shell` and
`start-agent` and compared after, with a change reported as exit 1 and a
`refused` audit record. That is detection with a gap, like `commondir`'s, and
`USAGE.md` says so.

### What mounts cannot close

Two routes create **new** files, and a bind mount needs an existing target.
Docker creates a missing target on the host, inside the bind-mounted `.git` —
and an empty `.git/commondir` breaks host git outright (tested: `fatal: failed
to read …/commondir`). So:

- **`.git/commondir` in an ordinary checkout** is checked, not mounted. `dev`
  looks for it on the host before every command that drives the container and
  after every `exec`, `shell` and `start-agent` returns, and on finding one
  refuses to continue, prints the file's content, and writes an audit record.
  This is detection with a gap — an IDE's `git status` in the meantime still
  runs it — and the spec says so rather than calling it closed.
  Checked at those two points only, deliberately: a host-side file watch for
  the length of a session would narrow the gap, at the cost of a watcher per
  session, and was judged not worth it.
- **Nested repositories registered as gitlinks** are not closed by `dev`. The
  index must stay writable for commits to work, so the agent can always add a
  gitlink; the nested repository's own config is anywhere in the working tree.
  The answer is host-side and documented in `USAGE.md`: `git config --global
  diff.ignoreSubmodules all` stops `status` recursing into submodules (tested),
  at the cost of submodule status in the operator's own repositories. `dev`
  neither detects nor refuses gitlinks — refusing would also refuse a
  legitimate submodule the agent was asked to add.

Both are listed as residual risk in `USAGE.md`, beside the routes the plan
already left open: hooks managed in the working tree (`.husky/`, lefthook,
pre-commit), `.envrc`, `.vscode/tasks.json`, `Makefile`, `package.json`
scripts. Review before running workspace code on the host.

### The hooks copy

A host directory, `<DEV_STATE dir>/hooks/<workspace>/<container>/`, created
`0700`, resolved with `xpath.Resolve` (invariant 2) and bind-mounted at
`<gitdir>/hooks`.

- **Seeded from the project's `<gitdir>/hooks` at `create` and at every
  `rebuild`**: the directory is emptied and the project's hooks copied in, so
  the container runs what the operator runs on the host and anything the agent
  installed is discarded at `rebuild`. Host to container only; nothing is ever
  copied back.
- **Copied without following symlinks**, and emptied with `os.RemoveAll`, which
  does not follow them either. The agent controls this directory's contents;
  `dev` never executes anything in it, and must never be led outside it.
- **Removed with the container**, in `container remove` and the workspace
  cascade.
- **On the host rather than a docker volume** so that seeding happens before
  `up`, without an exec: lifecycle commands that commit already see the hooks.

### What the agent sees

| Operation | Result |
|---|---|
| `fetch`, `pull`, `commit`, `push origin <branch>`, `checkout -b` | work |
| `push -u`, `checkout -b x origin/y` | the push or branch succeeds; recording the upstream prints `could not write config file`; exit 0 |
| `remote add`, `git config <key>` | fail |
| `git config --global` | works — `GIT_CONFIG_GLOBAL` is on the state volume, which host git never reads |
| husky / `core.hooksPath` installers | fail inside the container; the operator runs them once on the host, and the container sees the result through the read-only mount |

### Recorded

`git_guard` on the row: `1` from `create` for a folder container, `0` for
existing rows and folderless containers. `rewriteGeneratedTools` re-derives the
mounts from it, as it does the state and worktree mounts (invariant 10).

## 2. Escape guard

At `create`, at every `start`, and at `rebuild`, read the merged
configuration (`read-configuration --include-merged-configuration`, the call
k8s already makes in `internal/provider/k8s/build.go`; no `--id-label`, so the
CLI resolves features afresh rather than reading the image being replaced) and
refuse, unless the row has
`allow_privileged`, when it asks for any of:

| Asks for | Seen in |
|---|---|
| `privileged: true` | the document, `runArgs` `--privileged`, any feature (the CLI ORs it across features: `docker-in-docker` sets it) |
| the engine socket | any mount whose source ends in `docker.sock` or `podman.sock`, or names `/var/run/docker.sock` / `/run/podman/` |
| host namespaces | `runArgs` `--pid=host`, `--network=host` (`--net=host`), `--ipc=host`, `--userns=host`, `--uts=host` |
| the host filesystem | a bind mount whose source is `/`, the operator's home directory, or a parent of either — on Docker Desktop and OrbStack `/Users` is shared into the engine's VM, so this reaches `~/.ssh` |
| a device | `runArgs` `--device` |

Allowed, because they widen what root can do inside the container, not the way
out of it: `capAdd` (including the Go feature's `SYS_PTRACE`), `securityOpt`
(including its `seccomp=unconfined`), `init`, ordinary `sudo`.

Merged, because an override document cannot remove what a feature adds. The
refusal names each finding and where it came from, and ends with the flag:

```
dev: container api asks for access to the host:
  privileged: true            (feature ghcr.io/devcontainers/features/docker-in-docker)
  mount /var/run/docker.sock  (devcontainer.json mounts)
pass --allow-privileged to create it anyway
exit 2
```

A `dockerComposeFile` project is checked on the merged configuration plus each
service's `privileged`, `volumes`, `network_mode`, `pid` and `devices` from the
compose files `read-configuration` reports. A compose file the check cannot
parse is a refusal, not a pass.

k8s: not applied. The pod spec is `dev`'s own and ignores all of the above.

### Recorded

`allow_privileged` on the row. Existing rows get `1` — they keep doing what
they do — and `create` writes `0` unless the flag is given. Only `create` sets
it: a `rebuild` that newly asks for the host fails, and the operator recreates
the container with the flag, which is the moment to think about it.

## 3. Drift check

### What is digested

SHA-256 over a canonical encoding of:

- the merged configuration from `read-configuration`, with the fields that
  change without anything changing removed (the CLI's own resolved paths);
- when the configuration builds an image, the Dockerfile's bytes
  (`build.dockerfile`, resolved against the config's directory);
- for a compose project, each compose file's bytes.

Merged rather than file bytes, because what matters is what will run, and a
feature or a referenced file changes that without the project's
`devcontainer.json` changing. A generated configuration is `dev`'s own and is
exempt: its digest is `''` and never checked.

### Behaviour

- `create` stores the digest on the row.
- `rebuild` computes it before touching the container. Equal: proceeds.
  Different: refuses, exit 2, listing the changed top-level fields of the
  merged configuration and naming a changed Dockerfile or compose file, with
  `pass --accept-config to rebuild with it`.
- `rebuild --accept-config` proceeds and stores the new digest. The audit
  record carries both digests and the changed fields.
- A row whose digest is `''` (existing rows) records the digest at its first
  `rebuild` without refusing. That is the "new rows only" rule: an existing
  container is guarded from its next rebuild on.
- `start` does not compare. Starting an existing container does not apply its
  configuration; only `rebuild` and the first `up` do. It does record a digest
  for a row with none, which is how `create --no-start` gets one.
- `create --no-start` reads nothing and records no digest: recording a container
  needs no engine. The escape check and the digest both happen at its first
  `start`.

### Interaction

The drift check runs before the escape guard, and both before
`loadAgentConfig`, so that every refusal happens while the old container still
exists — the same ordering `rebuild` already uses for `agents.yaml`.

## 4. Kubernetes pod

In `buildDeployment`, always: `securityContext.seccompProfile: {type:
RuntimeDefault}` on the pod. Docker applies its default profile to every local
container; a pod runs unconfined unless it asks.

The ServiceAccount token stays mounted. It is the token of the provider's
`serviceAccount`, the namespace's `default` account when unset, and that
account holds no permissions unless someone binds some to it — so the token is
only worth what the operator chose to grant. `USAGE.md` says so: leave the
account unbound and `--kube-token` stays the only route to cluster access.

No column. The provider is experimental; every Deployment changes on its next
start and `Recreate` handles it.

## 5. Audit log

### File

`audit.jsonl` in the database's directory — `filepath.Dir(store.DefaultPath())`,
so `$DEV_STATE` or `~/.local/state/dev/` — opened `O_APPEND|O_CREATE|O_WRONLY`,
mode `0600`. A package of its own, `internal/audit`, so the CLI calls
`audit.Record(event)` and knows nothing about the format.

### Records

One JSON object per line. Common fields:

| Field | |
|---|---|
| `time` | UTC, RFC 3339 with nanoseconds |
| `event` | below |
| `workspace`, `container` | when the event has them |
| `dev` | `dev`'s version |
| `user` | host user name |

| `event` | Extra fields |
|---|---|
| `create` | `provider_kind`, `source_kind`, `generated`, `persist_state`, `git_guard`, `allow_privileged`, `config_digest` |
| `rebuild` | `config_digest`, and with `--accept-config`: `previous_digest`, `changed` |
| `remove`, `start`, `stop` | — |
| `exec`, `shell`, `start-agent` | `argv`, `agent` (start-agent), `ssh_agent`, `kube_token`, `started`, `ended`, `exit_code` |
| `kube-token` | `context`, `namespace`, `service_account`, `requested`, `expires` |
| `relay` | `started`, `ended`, `end` (`clean`, `killed`, `unexpected`) |
| `sync` | `settings` — names only |
| `refused` | `reason` (`escape`, `drift`, `git`), `findings` |
| `accept-config` | `previous_digest`, `config_digest`, `changed` |

`argv` is the command the operator gave, which may contain anything they
typed. Never environment values, never resolved settings, never a token.

### Rules

- **Names, never values.** A test runs every command path with a sentinel
  secret and asserts it never reaches the file.
- **One `write(2)` per record** of a line under `PIPE_BUF` where possible, with
  `O_APPEND`, so concurrent `dev` processes do not interleave. A longer record
  (a long `argv`) is truncated to fit, with `"truncated": true`.
- **A failed write warns once on stderr and does not change the exit code.**
- **Long commands write one record at the end**, with `started` and `ended`, so
  a session killed with the host leaves no record. Accepted: a start record and
  an end record would double the file for the rare case.

### `dev audit`

```
dev audit [--workspace W] [--container C] [--event E]... [--since D] [--json]
```

- Prints records oldest first, one per line, as
  `TIME  WORKSPACE/CONTAINER  EVENT  SUMMARY`, where the summary is
  per-event (`exit 0 after 2h13m`, `asked for privileged`, `sa reader, expires
  15:04`).
- `--since` takes a duration (`24h`) or an RFC 3339 time.
- `--json` prints the matching raw lines unchanged, for `jq`.
- **Does not resolve names.** A workspace or container that no longer exists
  still has history, so an unknown name is not `3`; it matches nothing and
  prints nothing, exit 0. `--workspace` does not default to the active one: the
  log is the one place where everything should be visible at once.
- A missing file prints nothing, exit 0. A line that does not parse is skipped
  with one warning naming its line number.
- Exit 2 for a malformed `--since`.

## Schema

`0007_hardening.sql`, portable (invariant 5):

```sql
-- git_guard and allow_privileged are fixed at create: what a container is
-- mounted on and what it may ask of the host must not change under a rebuild
-- (invariant 10's reason). The defaults speak for rows that already exist:
-- they were created unguarded and keep behaving as they did until recreated.
-- config_digest '' means "not yet recorded": generated containers, and
-- existing rows until their next rebuild. config_digest_fields holds a hash
-- per field and per file of the same configuration, so a refusal can name
-- what changed without dev keeping a copy of the document.
ALTER TABLE containers ADD COLUMN git_guard INTEGER NOT NULL DEFAULT 0;
ALTER TABLE containers ADD COLUMN allow_privileged INTEGER NOT NULL DEFAULT 1;
ALTER TABLE containers ADD COLUMN config_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE containers ADD COLUMN config_digest_fields TEXT NOT NULL DEFAULT '';
```

`model.Container` gains `GitGuard`, `AllowPrivileged`, `ConfigDigest` and
`ConfigDigestFields`, plus `GitGuardMounts`, derived per invocation and never
stored.

## Command surface

| Command | Change |
|---|---|
| `container create`, `worktree create` | `--allow-privileged`; refuses per §2; records `git_guard`, `config_digest` |
| `container rebuild` | `--accept-config`; refuses per §2 and §3; re-seeds hooks |
| `container list` | `HOST` and `GIT` columns (there is no `container show`) |
| `container start` | re-checks §2; records a digest for a row with none; seeds the hooks copy |
| `container remove`, `worktree remove` | remove the hooks copy (a workspace cannot be removed while it holds containers) |
| `exec`, `shell`, `start-agent` | `commondir` check before and after, and the fingerprint fallback (§1); an audit record |
| `audit` | new (§5) |

`create --from` copies neither flag from its source: each is the operator's
decision for the container in front of them.

`docs/USAGE.md` gains walkthroughs for the git guard, the credential model and
the audit log, and every changed command's reference entry.

## Testing

Unit (`make test`, no engine, stub executables per the conventions):

- `dcgen`: generated and overlaid documents carry exactly the §1 mounts for an
  ordinary checkout, a worktree, a bare-repository worktree, a repository with
  `extensions.worktreeConfig`, and none for folderless or non-git folders; an
  existing project mount at any of those targets is replaced, not duplicated.
- Hooks copy: seeding empties first, copies regular files, copies a symlink as
  a symlink, and never writes outside its directory when the source contains a
  symlink to `..`.
- Escape guard: one fixture per row of §2's table, from the document, from
  `runArgs`, from a stubbed feature in merged output, and from a compose file;
  the Go feature's `SYS_PTRACE` and `seccomp=unconfined` pass.
- Drift: identical merged output → equal digest; a reordered-but-equal map →
  equal; a changed feature option, Dockerfile byte or compose file → different,
  with the right `changed` list; `''` rows record without refusing.
- Audit: record shape per event; sentinel secret never written; concurrent
  writers produce whole lines; `dev audit` filtering, `--since` both forms,
  unknown names exit 0, malformed line skipped.
- k8s manifest: the seccomp profile
  present on every Deployment.
- Exit codes: every refusal is 2.

Smoke (`make smoke`, real engine — Linux amd64 and arm64 in CI):

- Inside a guarded container: `git config core.fsmonitor x` fails;
  `git push -u` to a local bare remote succeeds with the documented message;
  a hook written inside does not appear in the host's `.git/hooks`; `mv .git`
  fails; `rebuild` restores the project's hooks.
- Writing `.git/commondir` inside makes the next `dev exec` refuse.
- A project config with `privileged: true` is refused at `create` and created
  with `--allow-privileged`.
- Editing the project's `devcontainer.json` makes `rebuild` refuse, and
  `--accept-config` proceeds.
- `audit.jsonl` holds the records for the run and no resolved setting value.
- On a Docker Desktop or OrbStack engine (manual, not CI): a file bind mount
  behaves as on Linux.

## Order

Each its own branch and PR; together they make the plan's build list.

1. k8s pod (§4) — independent, smallest.
2. Audit log and `dev audit` (§5) — the other guards write `refused` records
   into it, so it lands before them.
3. Schema and the escape guard (§2), with the shared merged-configuration
   reader.
4. Drift check (§3), on that reader.
5. Git guard (§1).
6. `USAGE.md` hardening section and the credential model, completed with each
   PR above and finished here.

## Resolved in review

1. **Gitlinks:** the documented host-side `diff.ignoreSubmodules all` is
   enough. `dev` neither detects nor refuses them.
2. **`commondir` detection:** checked before and after each command only; no
   session-long file watch.

## Changed during implementation

Where building it turned up something this spec had wrong:

- **The ServiceAccount token stays mounted on k8s.** Dropped in review: the
  token is the provider's `serviceAccount`, the namespace `default` when unset,
  which holds no permissions unless bound. `USAGE.md` says to leave it unbound.
- **Where `.git` lands is the CLI's git-root rule, not the folder.** See §1,
  "Where `.git` lands". Projects that name their own workspace mount get the
  fingerprint fallback instead of mounts.
- **The escape check also runs at `start`.** `create --no-start` records a
  container without an engine, so the first `start` is where its configuration
  is first read; and checking every start costs one CLI call while telling a
  first start from a later one would need the engine's answer first.
- **`config_digest_fields`.** The stored digest alone could say that a
  configuration changed but not what; a hash per field lets the refusal name
  the fields without dev keeping a copy of the document.
- **No `container show`.** It does not exist; `container list` gained `HOST`
  and `GIT` columns instead.
- **Git guard mounts are added per invocation, not stored**, even in a
  generated document — they depend on where `.git` is today, and storing them
  would give `rewriteGeneratedTools` one more thing to re-derive.
