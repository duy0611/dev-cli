# CLAUDE.md

Guidance for Claude Code (claude.ai/code) working in this repository.

## What this is

`dev`, a Go CLI that manages devcontainers and runs coding agents inside them.
One binary, a SQLite database, and two external binaries it shells out to. No
CI, one operator, unpublished.

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
blank-imports `internal/provider/local` to trigger it. Adding the Kubernetes
provider is a new package plus one blank import — not an edit to the factory.

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
   is also why a rotated secret needs no restart.

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

## Conventions

- **Shell out, do not reimplement.** The local provider drives the
  `devcontainer` CLI and `docker`; the Kubernetes one will drive `kubectl`. No
  Docker Go SDK, no devcontainer-spec parser.
- **Check flags against the real CLI before using them.** `devcontainer` has no
  `rebuild` subcommand (it is `up --remove-existing-container`), and
  `--secrets-file` exists on `up` but not on `exec`, which is why both paths use
  `--remote-env` and accept that values are briefly visible in the host process
  list.
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
