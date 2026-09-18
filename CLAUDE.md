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
it skips rather than fails without them. Its Kubernetes half is gated
separately, on `DEV_SMOKE_K8S_CONTEXT` and `DEV_SMOKE_REGISTRY` both being set
(`DEV_SMOKE_K8S_NAMESPACE` and `DEV_SMOKE_K8S_PULL_SECRET` are optional), so a
host with an engine still skips it until told which cluster to use.

`DEV_STATE` relocates the database, which is how a command is run without
touching the operator's own state:

```sh
DEV_STATE=$(mktemp -d) dist/dev workspace list
```

## Architecture

```
cmd/dev/              main; exits with the code cli.Execute returns
internal/cli/         one file per noun; arg parsing and orchestration only
internal/store/       SQLite, all persistence; schema in migrations/*.sql
internal/model/       plain persisted types, no behaviour
internal/provider/    Provider interface + registry
internal/provider/local/  devcontainer CLI and docker adapter
internal/provider/k8s/    devcontainer CLI, kubectl, manifests, lifecycle
internal/secret/      spec -> value: literal, keychain, op
internal/env/         assembles the env a container launches with
internal/agent/       which agents exist and how to invoke them
internal/dcconfig/    locating a project's .devcontainer config
internal/dcgen/       generating one: the tool catalog, render, reverse map
internal/xpath/       physical paths, name and env-key validation
test/smoke/           end-to-end, behind the `smoke` build tag
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

8. **A provider's kind never changes.** `existingProvider` in
   `internal/cli/provider.go` refuses it; the row is not rewritten. Workspaces
   go on naming the provider and the containers under them stay in the engine
   they were created in, so a flip leaves records pointing at an engine that has
   never heard of them — `container list` answers confidently and wrongly, and
   `remove` can never reach the real container. Remove and recreate instead:
   `provider remove` already refuses while a workspace names it, which is what
   makes that route safe without a second copy of the rule.

9. **Do not write into a project's folder.** `dev` never creates or edits a
   `devcontainer.json` inside a project, and a missing agent is reported as
   something to add to the project, not something `dev` installs. A folder that
   ships its own configuration always wins. A folder with none can still be
   run: `--generate` renders a base Ubuntu configuration from the
   `internal/dcgen` catalog and stores it on the container row, where it
   cascades away with the workspace and travels to Postgres with the rest of
   the schema. The devcontainer CLI only takes a path, so that configuration is
   materialised to a temporary file per invocation and passed as `--config` —
   which is why every `model.Container` reaching a provider has a usable
   `ConfigPath` and neither provider knows where it came from.

   A container need not have a folder at all: `--no-folder` records
   `SourceKind` as `none` with an empty `Source`, always generates its
   configuration, and mounts a volume `dev` owns — a docker named volume called
   `dev-<workspace>-<container>` on local, the existing PVC on k8s. The
   generated document names both `workspaceMount` and `workspaceFolder`,
   because without the second the CLI would derive the in-container path from
   the temporary directory `materialise` creates and it would change every
   invocation. That temporary directory is also what such a container passes as
   `--workspace-folder`, which is why neither provider needs to know what a
   folderless container is to run one — Up, Exec, Stop, Status and Logs are
   unchanged. The three places that do branch on `SourceKind` do it to remove
   the volume and to refuse or skip a sync: the local provider removes the
   volume in `Remove`, matching k8s deleting its PVC (`rebuild` keeps it on
   both); and `container sync` refuses a folderless container on both
   providers, since there is no host tree to push.

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
- **The builder is chosen, not assumed** (`builder.go`). The CLI gates
  `--platform` and `--push` on `<docker-path> buildx version` printing a semver,
  so `selectBuilder` probes with that exact rule. docker+buildx does both in one
  step; podman satisfies the probe and honours `--platform` but has no `--push`,
  so it pushes separately; plain docker can only build for this host. Prefer
  docker+buildx over podman — Features under rootless podman fail on the bind
  mount the generated Dockerfile uses (devcontainers/cli#548).
- **`imagePullPolicy: Always`, and `rebuild` bumps a pod annotation.** The tag
  is always `:latest`, so nothing else makes a rebuild visible.
- **`Recreate`, not `RollingUpdate`.** The PVC is ReadWriteOnce; two pods would
  leave the new one unschedulable.
- **The keep-alive command traps SIGTERM, and `stop` waits.** The command is
  PID 1, and PID 1 ignores signals it has no handler for, so without the trap
  every stop waits out the full grace period before the kubelet SIGKILLs. The
  sleep is backgrounded with `wait`, or the trap could not run until it
  finished. `kubectl scale` also returns before the pod is gone, so `Stop`
  polls until no pod carries the labels — `docker stop` does not lie about this
  and neither should this.
- **The lifecycle marker is a file on the volume**, not an annotation. The
  Deployment is re-applied on every start and a server-side apply drops fields
  it no longer sets, so an annotation clears itself.
- **Every kubectl call carries `--context` and `--namespace`**, built in one
  place. A call that reaches the wrong cluster does not fail; it succeeds
  somewhere else.
- **`pipeInto` streams through `cmd.StdinPipe`, not an `io.Pipe` on
  `cmd.Stdin`.** os/exec drains an `io.Reader` from a goroutine of its own, and
  that goroutine gives up when the command exits — leaving the producer blocked
  on a pipe with no reader, so a pod that dies mid-sync hangs instead of
  failing. A real pipe gives EPIPE. An EPIPE with a clean exit is still an
  error: kubectl that took none of the archive did not complete a copy.
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
- **Prompt only at a terminal.** `provider configure` asks for whatever its
  flags left empty, but `isTerminal(os.Stdin)` guards the prompting: a scripted
  run has to fail naming the missing flag rather than block on a question
  nobody will see. Every prompted value has a flag, and a flag already given is
  never asked about again. Both paths fall back to the stored provider, so
  re-running `configure` changes only what it was told to; `k8sSettings` is the
  one list they walk, and `-` is the only way to empty an optional setting now
  that an absent flag means "keep".
- **Tests for external tools use stub executables on a temporary `PATH`**, not
  an interface with a mock. See `internal/provider/local/local_test.go`. The
  k8s harness *replaces* `PATH` with its stub directory instead of preceding
  the host's: the builder probes docker and then podman, so on a machine that
  has podman a leaked host binary becomes the one under test — it answered the
  BuildKit probe and pushed to a real registry from a unit test. A test that
  wants a host binary asks for it by name, as `requireRealCLI` does.
- **A unit test may not need the network either.** The real devcontainer CLI
  asks the engine for an image's metadata and, told nothing, fetches it from
  the registry instead — so a stub docker that answers `inspect --type image`
  with silence makes `make test` fail wherever a registry is unreachable or
  rate-limiting, with a `SyntaxError` from inside the CLI's own JavaScript.
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
