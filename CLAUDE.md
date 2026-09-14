# CLAUDE.md

Guidance for Claude Code (claude.ai/code) working in this repository.

## What this is

`dev`, a Go CLI that manages devcontainers and runs coding agents inside them.
One binary, a SQLite database, and the external binaries it shells out to:
`devcontainer` and `docker` always, `kubectl` for the k8s provider. No CI, one
operator, unpublished.

The design record is `docs/specs/2026-09-14-dev-cli.md`. Read it for intent;
read the code for current state.

## Commands

```sh
make            # help
make build      # dist/dev
make test       # go test ./... — never touches a container engine
make lint       # gofmt (fails on any diff), go vet, golangci-lint
make smoke      # go test -tags smoke ./test/smoke/... — needs a real engine
make install    # build, then copy to ~/.local/bin/dev
make clean
```

`make lint && make test` is the fast loop and runs anywhere Go does. `make
smoke` needs the `devcontainer` CLI, an engine, and network for an image pull;
it skips rather than fails without them.

## Architecture

```
cmd/dev/              main; returns an exit code, never calls os.Exit itself
internal/cli/         one file per noun; arg parsing and orchestration only
internal/store/       SQLite, embedded migrations, all persistence
internal/model/       plain persisted types, no behaviour
internal/provider/    Provider interface + registry
internal/provider/local/  devcontainer CLI and docker adapter
internal/provider/k8s/    devcontainer CLI, kubectl, manifests, lifecycle
internal/secret/      spec -> value: literal, keychain, op
internal/env/         assembles the env a container launches with
internal/agent/       which agents exist and how to invoke them
internal/dcconfig/    locating a project's .devcontainer config
internal/xpath/       physical paths, name and env-key validation
```

`internal/cli` orchestrates; the work lives in the packages it calls. A command
function that grows logic beyond "resolve inputs, call a package, print" is in
the wrong file.

Providers register themselves by kind in `init`, and `internal/cli/root.go`
blank-imports each one to trigger it. Adding a provider is a new package plus one
blank import — not an edit to the factory. That held when k8s was added: the only
changes outside the new package were the blank import, `--kind k8s` becoming
valid, and one new command (`container sync`).

`Syncer` is an optional interface, discovered by type assertion. The local
provider bind-mounts the folder, so a no-op `Sync` there would be a lie.

## Invariants — violating these produces failures far from their cause

1. **Pass both `--id-label` values on every `up` and `exec`.** Given none, the
   devcontainer CLI infers one from `--workspace-folder`, so two containers
   created from one folder become the same container. Given a *different* set,
   it looks up a container that does not exist and creates a second one beside
   it — no error either way. They are built in exactly one place,
   `internal/provider/local/label.go`, and a test asserts `up` and `exec` agree.

2. **Resolve folders to physical paths before storing or mounting them.** The
   engine is VM-backed on macOS and resolves the path string *inside* that VM,
   where `/tmp` is a real directory rather than a symlink to `/private/tmp`.
   Handing it an unresolved path mounts an empty directory, silently.
   `xpath.Resolve` is the only way in.

3. **A setting's spec is never a value.** The database stores
   `keychain:NAME` or `op://…`; resolution happens per invocation, in
   `internal/secret`. That is what keeps tokens out of the database, out of
   `workspace show`, and out of any backup of either. Resolving per invocation
   is also why a rotated secret needs no restart — fully true on local; on k8s
   an exec'd command sees the new value immediately, but the pod's own
   environment comes from `envFrom` and is fixed until a stop and start.

4. **Never store live container status.** It is read from the engine every
   time. A stored copy is wrong the moment anything happens outside `dev`.

5. **Migrations are portable SQL.** No `AUTOINCREMENT`, no `datetime('now')`
   defaults, timestamps written by the application. The same files are meant to
   replay against Postgres when the cloud provider lands.

6. **`PRAGMA foreign_keys = ON` and `MaxOpenConns(1)`.** SQLite defaults foreign
   keys off, which would skip the cascade that deleting a workspace depends on.
   It also takes one writer, so a connection pool turns lock contention into
   errors. Both are asserted or relied on by tests.

7. **Exit codes are part of the interface.** `2` for a malformed request, `3`
   for something named that does not exist, `1` otherwise. Cobra reports a bad
   flag or a wrong argument count as a plain error, which would exit 1 — hence
   the wrappers in `internal/cli/args.go` and the `SetFlagErrorFunc` in
   `root.go`. Use `exactArgs`/`noArgs`/`minArgs`, never cobra's directly.

8. **Do not edit a project's `devcontainer.json`.** A folder without one is an
   error, and a missing agent is reported as something to add to the project,
   not something `dev` installs. The project owns its container definition.

## Kubernetes provider

The devcontainer CLI speaks Docker only. It is used for reading the merged
configuration and building the image; `kubectl` does everything else. The image
is built on the host and pushed, so a k8s provider still needs Docker locally.

Beyond the invariants above, these are the parts that bite:

- **Two subPaths on the PVC, workspace and home.** Scaling to zero destroys the
  container filesystem. Without the home mount, anything `postCreate` wrote to
  `~` disappears on every stop and "postCreate runs once" is false.
- **`--platform` is explicit, defaulting to `linux/amd64`.** An arm64 image on
  an amd64 node crash-loops with `exec format error`.
- **`imagePullPolicy: Always`, and `rebuild` bumps a pod annotation.** The tag
  is always `:latest`, so nothing else makes a rebuild visible.
- **`Recreate`, not `RollingUpdate`.** The PVC is ReadWriteOnce; two pods would
  leave the new one unschedulable.
- **The lifecycle marker is a file on the volume**, not an annotation. The
  Deployment is re-applied on every start and a server-side apply drops fields
  it no longer sets, so an annotation clears itself.
- **Every kubectl call carries `--context` and `--namespace`**, built in one
  place. A call that reaches the wrong cluster does not fail; it succeeds
  somewhere else.
- **Commands are shell-quoted, then quoted again inside `su -c`.** A test stub
  matching `'test' '-f'` will never fire — match the path instead. This cost
  three wrong tests before it was noticed.

## Conventions

- **Shell out, do not reimplement.** The local provider drives the
  `devcontainer` CLI and `docker`; the Kubernetes one drives the same CLI and
  `kubectl`. No Docker Go SDK, no client-go, no devcontainer-spec parser.
- **Check flags and output against the real CLI before using them.**
  `devcontainer` has no `rebuild` subcommand (it is
  `up --remove-existing-container`); `--secrets-file` exists on `up` but not on
  `exec`, which is why both local paths use `--remote-env` and accept that
  values are briefly visible in the host process list; and
  `read-configuration` emits *plural* merged fields (`onCreateCommands`),
  reports a null `workspaceFolder` when unset, and shells out to `docker ps`
  even to read a file.
- **Every non-obvious line carries a comment explaining *why*,** usually naming
  the failure it prevents. Match that density. Most of what is hard here is
  container and platform trivia, and an uncommented workaround reads as
  removable.
- **Tests for external tools use stub executables on a temporary `PATH`**, not
  an interface with a mock. See `internal/provider/local/local_test.go`.
- **Store tests use a temp file, never `:memory:`** — that database is
  per-connection, and the pool hands the migration to one connection and the
  query to another.
- Unpublished, one operator, no external users. A rename is just a rename: no
  aliases, no deprecation notes, no "used to be X" in comments or docs. State
  the current rule; git holds the history.

## Commits

No `Co-Authored-By: Claude ...` trailer, and no other tooling-attribution line.
One operator, so the history reads as their own authorship. This overrides any
harness default that asks for it.
