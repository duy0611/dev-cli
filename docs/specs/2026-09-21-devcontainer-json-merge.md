# Merging dev's mounts into a project's devcontainer.json

Commit a `.devcontainer/devcontainer.json` to this repository that carries
`dev`'s own state block — a `mounts` entry at `/var/dev-state`, the five
`containerEnv` variables, a `postCreateCommand` chown — and `dev` stops working
on its own repository:

```
❯ dev container start c1
docker: Error response from daemon: duplicate mount destination /var/dev-state
```

`internal/provider/local/local.go:80` passes `--mount ...target=/var/dev-state`
for every project-owned container that persists state. The project file now
names the same target, the flag arrives anyway, and docker refuses the whole
run. This is the failure invariant 10 already records, arriving from the one
direction that invariant did not anticipate: not a generated document colliding
with the flag, but a project's own document doing it.

The goal is one committed `devcontainer.json` that works when VS Code opens it
directly **and** when `dev` drives it, with `dev` merging its own settings in at
invocation time rather than the operator hand-writing them into the project.

## The mechanism

`dev` reads the project's `devcontainer.json`, overlays its own `mounts` entry,
writes the result to a temporary file, and passes `--override-config` on `up`
and on `exec`.

Verified against the devcontainer CLI 0.88.0:

- `--override-config` exists on **`up`, `exec` and `read-configuration`**, and
  **not** on `build`. `--mount` exists on `up` only.
- An override file in `/tmp` keeps the **project's** path anchoring: a project
  with `"build": {"dockerfile": "Dockerfile"}` built successfully from an
  override in an unrelated directory, and `configFilePath` came back as the
  project's own path. Confirmed for `.devcontainer/devcontainer.json`, root-form
  `.devcontainer.json`, and a `dockerComposeFile` project.
- `--override-config` **replaces**, it does not deep-merge: a project's
  `features` and `name` both vanished when the override omitted them. `dev` must
  do the merge itself.
- `--config` and `--override-config` together are accepted on `up`.
- End to end: a JSONC project file merged to a temporary override started with
  both the project's mount and `dev`'s state mount, and a following `exec` with
  the same override saw `/var/dev-state` mounted and `CLAUDE_CONFIG_DIR` set.

Recorded here because none of it is derivable from `--help` and re-deriving it
costs an engine, a registry and an afternoon.

**The up/exec asymmetry disappears.** `--mount` exists on `up` and not on
`exec`, which is why invariant 10 has to warn that it must never enter
`execArgs`. `--override-config` exists on both, so the two paths carry the same
flag and the warning becomes unnecessary rather than better-enforced. The class
of bug invariant 1 exists to prevent — two commands disagreeing about which
container they mean — loses one of its remaining footholds.

## Mounts only

The overlay rewrites exactly one field.

Environment already reaches both commands through `--remote-env`, which
`remoteEnvArgs` builds for `up` and `exec` alike, so merging `containerEnv`
would be a second route to a destination that already has one — and invariant 10
is emphatic that two mechanisms for one outcome is how a mount gets dropped.
Features, image, build, name and every other field round-trip untouched.

The narrow scope is what makes the merge faithful to a document `dev` does not
own. `dev` is not a devcontainer-spec parser, which the Conventions rule
forbids; it reads one array and writes it back.

`--config` is still passed alongside `--override-config`. Both are accepted, and
`--config` names the exact file `dcconfig` resolved, which is what preserves the
project's path anchoring for a relative `dockerfile`.

## Replace, not stand down

When the project's document already names a mount at `/var/dev-state`, the
overlay **drops that entry and appends `dev`'s own**. `dev` owns that path
whenever `dev` is driving.

The alternative was to stand down — detect the project's mount and write no
override at all, leaving the project's volume in place. It reads as the more
respectful rule and costs more than it returns:

- `Remove` deletes `StateVolumeName(...)` unconditionally. A container whose
  volume `dev` did not name leaves that volume behind at removal, and `dev`
  cannot delete what it cannot name. With `dev worktree`
  (`docs/specs/2026-09-21-git-worktree-containers.md`) making many containers
  per folder the normal case, that is one orphaned volume per worktree,
  accumulating with no command that collects them.
- `claimStateDir` would need a guard, so that a project claiming the mount also
  takes responsibility for the chown.
- `container create` would need an advisory explaining which of the two regimes
  the operator had landed in.

Replacing deletes all three. `Remove` stays unconditional and stays correct,
`claimStateDir` is untouched, and there is one regime rather than two.

What it costs: a project cannot keep its own mount at `/var/dev-state` while
`dev` drives it. `/var/dev-state` is `dev`'s own path, named by `dev`, documented
as `dev`'s — nothing else has a reason to be there. A project that wants a volume
of its own mounts it somewhere else, and the overlay preserves it.

**The committed file needs no trick.** VS Code never sees the override, so
`.devcontainer/devcontainer.json` can name a plain `source=dev-cli-state` and be
correct standalone; under `dev`, that entry is replaced by
`dev-<workspace>-<container>-state` and the per-container volume is the one that
gets mounted. `${devcontainerId}` would also have worked — the spec lists
`mounts` among the properties supporting it — but it is not needed once `dev`
replaces rather than defers, and a fixed name is the one a reader can predict.

## Per invocation, never stored

The merge runs on every command that starts or enters a container. Nothing is
cached, and nothing reaches the database.

A project-owned container picks up edits to its own `devcontainer.json` today
because `dev` passes `--config <project path>` and the CLI reads the live file. A
merge stored at create would freeze a snapshot: add a feature, bump an image,
run `dev container rebuild`, and get the document as it stood weeks ago with no
error to explain it. That is invariant 4's reasoning — never store what the
world owns — applied to a file `dev` does not own.

`GeneratedConfig` is the wrong home for a second reason.
`internal/cli/container.go:177-180` holds that exactly one of `ConfigPath` and
`GeneratedConfig` is set, and `rewriteGeneratedTools` re-renders
`GeneratedConfig` from the tool list — so a project-derived copy parked there
would be destroyed by the next `rebuild --tools`, which is precisely the failure
invariant 10 records for the state mount.

The cost is one small file read and a JSON round-trip per invocation, against a
CLI that shells out to `docker ps` to read a file.

## Where it happens

`materialise` in `internal/cli/resolve.go` is already the one place a container
gets a config path a child process can open, and already owns the temporary
directory and its cleanup. It currently returns early for a project-owned
container; it gains a second branch.

A new field on `model.Container`, **not persisted** and so needing no migration —
per-invocation, exactly like the `ConfigPath` `materialise` already fabricates:

```go
OverrideConfigPath string // a merged config for this invocation; local only
```

The branch runs when `GeneratedConfig == ""`, `PersistState` is set, and
`ConfigPath != ""`. It reads that file, calls `dcgen.Overlay` with
`local.StateVolumeName(...)` — the same helper `stateFor`
(`internal/cli/generate.go:44-49`) uses, so the name has one spelling — and
writes the result into the temporary directory that already exists.

A generated container is untouched: its document already names the volume in its
own `mounts`, which is the mechanism invariant 10 describes and this change does
not disturb.

## Parsing a file dev did not write

`devcontainer.json` is JSONC. Comments and trailing commas are legal, the CLI
accepts them, VS Code's own templates ship them, and `encoding/json` rejects
both.

`github.com/tailscale/hujson` normalises the syntax before `encoding/json` sees
it. It is the repository's first dependency that is not strictly load-bearing,
and it earns that by being correct on the cases a hand-rolled stripper gets
wrong — a `//` or `/*` inside a string literal, an escaped quote before one:

```json
"postCreateCommand": "echo // not a comment"
```

A scanner that misreads that line corrupts a project's configuration silently,
on every `up`. This is a syntax normaliser for one field, not the spec parser
the Conventions rule forbids.

The document is unmarshalled into `map[string]json.RawMessage` so that every
field `dev` does not touch round-trips byte for byte. Only `mounts` is decoded
further.

**Both mount spellings.** The CLI accepts a `mounts` entry as a string
(`"source=x,target=/y,type=volume"`) or as an object
(`{"source":"x","target":"/y","type":"volume"}`), and target detection has to
recognise either — a project using the object form must still have its
`/var/dev-state` entry replaced rather than duplicated, which would return the
`duplicate mount destination` failure this change exists to remove. `dev` writes
the string form.

## Failing without trapping the container

`dev` now parses a file it previously only handed over by path, so a file it
cannot parse is a new failure mode. An unparseable `devcontainer.json` must not
start a container without its state volume — that is silent data loss at the
next rebuild — so the commands that start or enter a container fail, naming the
file and the parse error.

But `materialise` runs for all eleven `resolve` callers, and most of them do not
need the override. `Stop`, `Remove`, `Status` and `Logs` find their container
through docker label filters and never read `ConfigPath` at all. A fatal parse in
`materialise` would make a syntax error in a project's file leave the container
**un-removable by `dev`**, sending the operator to `docker` directly to clean up
after a tool that refused to.

So the error is recorded rather than returned. `materialise` puts it on the
`target`, and the paths that drive `up` or `exec` surface it and exit 1:

```
❯ dev container start c1
dev: reading .devcontainer/devcontainer.json: invalid character '}' looking for
     beginning of object key string
```

while `stop`, `remove`, `logs`, `status` and `list` carry on. Checking the
recorded error is a step a future command can forget, so a test asserts `remove`
succeeds against a container whose project file does not parse — the guarantee
is that a broken file never strands a container in the engine.

## Docker Compose cannot carry the mount

A project whose `devcontainer.json` names `dockerComposeFile` does not reliably
get a `mounts` entry applied. The spec lists `mounts` as a general,
"cross-orchestrator" property, but the VS Code documentation directs Compose
users to put volumes in the compose file instead, implementations differ on
whether they inject it, and `devcontainers/spec#106` records the inconsistency as
known and unresolved.

The consequence is the quiet kind: `/var/dev-state` is not mounted, agents write
to the container filesystem, the state disappears at the next rebuild, and
nothing anywhere reports an error.

**This is not a regression.** Today's `--mount` flag has exactly the same problem
on a compose project; the overlay neither causes it nor fixes it. What changes is
that `dev` is now parsing the document anyway, so it can see `dockerComposeFile`
for free and say so:

```
❯ dev container create c1 --folder .
dev: this project uses docker compose; the devcontainer CLI will not apply
     dev's /var/dev-state mount. Agent state will not survive a rebuild. Add
     the volume to your compose file, or pass --no-persist-state.
created container c1
```

The container is still created and still runs — everything except state
persistence works — so refusing would withhold a working container over one
degraded feature. The warning is advisory and nothing is stored: a project can
gain or lose its compose file later, so runtime re-derives it.

## The provider

`up` (`internal/provider/local/local.go:51`) appends `--override-config` when the
field is set and **loses the `--mount` branch entirely**. `execArgs` (line 157)
appends the same flag. That is the whole change to the argument lists.

`claimStateDir` (line 115) is unchanged. The volume is still created root-owned
and still gets no UID remapping, so a project-owned container still needs the
chown over the exec channel. With the claim rule gone there is no case where the
project takes responsibility for it instead.

`Remove` (line 181) is unchanged, and is now unconditionally right: `dev` named
the volume, so `dev` can delete it, for every state-persisting container.

Kubernetes is untouched. It ignores `mounts` entirely — `manifest.go:66-78`
builds PVC subPaths from the `PersistState` column — and `devcontainer build` has
no `--override-config`, which `internal/provider/k8s/build.go:160-165` already
records. That path keeps `--config` as it stands.

## The boundary this does not cross

A VS Code-launched container never receives workspace settings. Invariant 3
stores specs (`keychain:NAME`, `op://…`) and resolves them per invocation in
`internal/secret`; no field in a `devcontainer.json` can call that resolver.

So the honest description of the committed file is: the container starts, the
agents have their state volume, and the secrets are absent. The documentation
says that rather than implying parity.

## This repository's own file

`.devcontainer/devcontainer.json` keeps `containerEnv`, `mounts`,
`postCreateCommand` and `source=dev-cli-state` as committed. Opened directly in
VS Code it works. Driven by `dev`, the overlay replaces that one mount entry with
the per-container volume and everything else stands.

## Testing

Everything below runs under `make test` against the existing stub-executable
harnesses. Nothing new needs a container engine.

- `internal/dcgen`: `Overlay` adds a mount to a document that has none and
  preserves `features`, `build`, `name` and `image` byte for byte; it replaces an
  existing `/var/dev-state` entry in both the string and the object spelling,
  leaving exactly one; it preserves a project's mounts at other targets; it
  accepts JSONC with a `//` comment and a trailing comma; a `//` inside a string
  literal survives; a malformed document returns an error rather than a partial
  result. The compose detector reports `dockerComposeFile` and does not report a
  document without one.
- `internal/cli`: `materialise` writes an override for a project-owned
  state-persisting container and none for a generated one, for one that does not
  persist state, or for one with no `ConfigPath`; an unparseable project file
  records the error rather than returning it, and `remove` succeeds against that
  container; `create` warns for a compose project and not otherwise.
- `internal/provider/local`: `up` and `exec` carry the **same**
  `--override-config` value — in the shape of the existing test asserting the two
  agree on id labels, and for the same reason; `up` no longer carries `--mount`
  at all; neither flag appears for a container that does not persist state;
  `--config` is still passed alongside. The four existing `--mount` assertions
  (lines 447-503) are rewritten rather than deleted: each covered a real
  distinction and each still has one to make.
- `internal/store`: unchanged. `OverrideConfigPath` is not persisted and there is
  no migration.

Against a real engine, which is what proves the opening complaint is fixed:

```sh
DEV_STATE=$(mktemp -d) dist/dev workspace create t
DEV_STATE=… dist/dev container create c1 --folder $PWD
DEV_STATE=… dist/dev container start c1
DEV_STATE=… dist/dev container exec c1 -- sh -c 'echo $CLAUDE_CONFIG_DIR; mount | grep dev-state'
```

Expect `/var/dev-state/claude` and a mount line, with no `duplicate mount
destination` — against this repository's own folder, the one that currently
fails. Confirm `docker volume ls` shows `dev-t-c1-state` rather than
`dev-cli-state`, and that `dev container remove c1` deletes it.

Then add a `"features"` entry to the project's `devcontainer.json` and run
`dev container rebuild`: the feature must be present, proving nothing was cached
at create. Finally open the repository in VS Code with "Reopen in Container" and
confirm it starts with `/var/dev-state` mounted and no `dev` process involved.

## Documentation

- `CLAUDE.md` invariant 10: the "`--mount` on `up` and never on `exec`"
  paragraph is replaced by the override mechanism and the replace rule. The
  `duplicate mount destination` hazard stays — it is still what makes the
  generated-document branch necessary — restated as a collision `dev` now
  resolves rather than risks.
- `docs/USAGE.md`: a short section on shipping a `devcontainer.json` that works
  under both VS Code and `dev`; that `dev` replaces any `/var/dev-state` mount
  while it drives; that a compose project does not get the volume; and plainly,
  that a VS Code-launched container gets no workspace settings.

## Files

| Path | Change |
| --- | --- |
| `internal/dcgen/overlay.go` | new: `Overlay`, both mount spellings, JSONC, compose detection |
| `internal/dcgen/overlay_test.go` | new |
| `internal/model/model.go` | `OverrideConfigPath`, unpersisted — no migration |
| `internal/cli/resolve.go` | overlay inside `materialise`; deferred parse error on `target` |
| `internal/cli/container.go` | surface the deferred error on start/rebuild/exec; compose warning at `create` |
| `internal/provider/local/local.go` | `--override-config` on up and exec; drop `--mount` |
| `internal/provider/local/local_test.go` | rewrite the four `--mount` assertions |
| `CLAUDE.md`, `docs/USAGE.md` | as above |
| `go.mod` | `github.com/tailscale/hujson` |

`.devcontainer/devcontainer.json` is unchanged, which is the point.
