# dev

A CLI for managing devcontainers and running coding agents inside them.

`dev` does not reimplement the [devcontainer standard](https://containers.dev) —
it drives the `devcontainer` CLI, and adds the things that standard leaves out: a
name for each container, a place to group them, and per-group settings that are
resolved from your secret store at launch rather than written down anywhere.

```
dev provider configure local --kind local
dev workspace init personal --provider local
dev workspace set GH_TOKEN op://Private/github/token
dev container create api --folder ~/code/api
dev container agent api --agent claude
```

## Status

Milestone 1. What works:

- the **local** provider, driving the `devcontainer` CLI against any
  Docker-compatible engine
- containers created **from a folder that ships its own `.devcontainer/`**
- workspaces, and per-workspace settings resolved from literals, the macOS
  Keychain, or the 1Password CLI
- `claude`, `opencode`, `codex` and `hermes` as agents

What does not exist yet: the Kubernetes provider, git-URL and image sources,
minted cloud credentials, IDE integration, and the UI.

## Install

```sh
make install        # builds dist/dev and copies it to ~/.local/bin/dev
```

Requires Go 1.25 or newer to build. At runtime you need:

| Tool | Why |
|---|---|
| [`devcontainer` CLI](https://github.com/devcontainers/cli) | every start, rebuild and exec goes through it |
| a Docker-compatible engine | Podman and Docker Desktop both work; `dev` talks to whichever `docker` points at |
| [`op`](https://developer.1password.com/docs/cli/) | only if you use `op://` settings |

## Concepts

**Provider** — where containers run. Only `local` exists today.

**Workspace** — a named group of containers plus the settings they launch with.
One is active at a time (`dev workspace use`), and every container command takes
`--workspace` to override it. Container names are unique *within* a workspace, so
the same project can be running twice under two names.

**Container** — a record pointing at a folder that ships its own
`.devcontainer/`. A folder without one is an error: the project owns its
container definition, and `dev` will not write one for it.

**Setting** — one environment variable for a workspace's containers, stored as a
*spec* rather than a value:

| Spec | Resolved by |
|---|---|
| `literal:https://example.com` | used as written |
| `keychain:SERVICE` | `security find-generic-password -a "$USER" -s SERVICE -w` |
| `op://vault/item/field` | `op read` |

Specs are resolved on every launch, so rotating a secret needs no restart and no
rebuild. `dev workspace show` prints the specs and never the values. Your git
`user.name` and `user.email` are passed through automatically, so the first
commit inside a container works; an explicit setting of the same name wins.

## Commands

```
dev provider configure NAME --kind local
dev provider list
dev provider remove NAME

dev workspace init NAME --provider NAME
dev workspace use NAME
dev workspace list
dev workspace set KEY SPEC
dev workspace unset KEY
dev workspace show
dev workspace remove NAME

dev container create NAME --folder PATH [--no-start]
dev container list [--all]
dev container start|stop|remove|rebuild NAME
dev container logs NAME [-f]
dev container shell NAME
dev container exec NAME -- CMD [ARGS...]
dev container agent NAME --agent claude [-- ARGS...]
```

Every container command accepts `--workspace NAME`.

Exit codes: `0` success, `1` the work failed, `2` the request was malformed,
`3` something named does not exist.

### Notes on a few of them

`create` resolves the folder to a physical path before recording it. On macOS
the engine runs in a VM and resolves paths inside it, where `/tmp` is a real
directory rather than a symlink to `/private/tmp` — an unresolved path mounts an
empty directory, silently.

`agent` starts the container if it is stopped, then checks the agent is actually
installed in the image. If it is not, `dev` offers a rebuild and points at the
project's `devcontainer.json`: installing an agent means adding it there, which
is not something this tool will do to someone else's repo.

`workspace remove` and `provider remove` refuse while anything still points at
them — a workspace holding containers (running or not), a provider named by a
workspace. Remove the containers first. Removing the active workspace leaves
none active rather than a dangling pointer.

`container remove` never touches the project folder. If the engine refuses, the record is
kept so the command can be retried; `--force` drops it anyway, which is how you
clean up after an engine that no longer exists.

## State

`~/.local/state/dev/dev.db`, a SQLite database created 0600. Set `DEV_STATE` to
put it somewhere else — worth doing when experimenting:

```sh
DEV_STATE=$(mktemp -d) dev workspace list
```

Live container status is never stored. It is read from the engine every time,
because a stored copy is stale as soon as anything happens outside `dev`.

## Development

```sh
make            # help
make build      # dist/dev
make test       # go test ./...
make lint       # gofmt, go vet, golangci-lint
make smoke      # end-to-end against a real engine (needs one)
make install    # build, then copy to ~/.local/bin
make clean
```

`make test` never touches a container engine. `make smoke` does, and skips
rather than fails when there is none.

Provider tests run against stub `devcontainer` and `docker` executables placed
on a temporary `PATH`, which is how the one invariant that breaks silently gets
checked: every `up` and `exec` for a container must pass the same
`--id-label dev.workspace=… --id-label dev.container=…` pair. Spell them
differently anywhere and the CLI looks up a container that does not exist, then
creates a second one beside it.
