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

Milestone 2. What works:

- the **local** provider, driving the `devcontainer` CLI against any
  Docker-compatible engine
- the **k8s** provider, running containers in a cluster as Deployments
- containers created **from a folder that ships its own `.devcontainer/`**
- workspaces, and per-workspace settings resolved from literals, the macOS
  Keychain, or the 1Password CLI
- `claude`, `opencode`, `codex` and `hermes` as agents

What does not exist yet: git-URL and image sources, minted cloud credentials,
IDE integration, and the UI.

## Install

```sh
make install        # builds dist/dev and copies it to ~/.local/bin/dev
```

Requires Go 1.25 or newer to build. At runtime you need:

| Tool | Why |
|---|---|
| [`devcontainer` CLI](https://github.com/devcontainers/cli) | reads configurations and builds images, on both providers |
| a Docker-compatible engine | Podman and Docker Desktop both work; `dev` talks to whichever `docker` points at. Needed for the k8s provider too, because that is where the image is built |
| `kubectl` | only for the k8s provider |
| [`op`](https://developer.1password.com/docs/cli/) | only if you use `op://` settings |

## Concepts

**Provider** — where containers run: `local` on your own engine, `k8s` in a
cluster.

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

## The k8s provider

```sh
dev provider configure prod --kind k8s     # prompts, defaulting from kubeconfig
dev workspace init cloud --provider prod
dev container create api --folder ~/code/api
```

Every setting it asks for is also a flag, and a flag given on the command line
is not asked about again. The prompting happens only when stdin is a terminal:
run it from a script and a missing setting is an error naming the flag, rather
than a question nobody is there to answer. Re-running it on an existing provider
defaults every question to what that provider already has, so pressing return
through all eight changes nothing.

The devcontainer CLI speaks Docker only, so it cannot start a pod. `dev` uses it
for the two things it alone can do — reading the merged configuration, and
building an image from your Features and Dockerfile — and drives `kubectl` for
everything else. The image is built **on your machine** and pushed to the
registry you configured, so a Docker-compatible engine is still required.

A container is three objects in the namespace: a Deployment, a
PersistentVolumeClaim and a Secret, all labelled `dev.workspace` and
`dev.container`.

| `dev` | Kubernetes |
|---|---|
| `create` | build, push, apply, wait, sync, run the create-time lifecycle commands |
| `start` | scale to 1, run `postStartCommand` |
| `stop` | scale to 0 — the volume, and everything on it, survives |
| `remove` | delete all three objects, volume included |
| `rebuild` | build and push again, recreate the pod, re-run the create-time commands |
| `shell` / `exec` / `agent` | `kubectl exec` |
| `sync` | stream the host folder in as a tar |

### Before the first create

The builder has to be logged in to the registry, and the **cluster** has to be
able to pull from it. They are separate credentials:

```sh
podman login ghcr.io -u USER      # or: docker login ghcr.io
```

A push to a registry that has never seen a credential fails with a 403 on the
token request; `dev` recognises that and prints the login command.

If the resulting package is private, the nodes need a pull secret of their own —
create one in the namespace and name it with `--image-pull-secret`, or the pod
sits in `ImagePullBackOff` and `create` fails waiting for it.

### Things worth knowing

**Your files are a copy, not a mount.** `create` streams the folder into the
volume; after that, `dev container sync NAME` is the only thing that sends more.
Nothing comes back down. Once an agent is working in the container, its copy is
the live one — which is why nothing overwrites it on a timer.

**Lifecycle commands are run by `dev`.** The CLI would normally run them at
`up`, which never happens here. `onCreateCommand`, `updateContentCommand` and
`postCreateCommand` run once per image; `postStartCommand` runs on every start;
`postAttachCommand` never runs, because nothing attaches. "Once" is a marker
file on the volume, so a stop and start does not reinstall everything, and a
rebuild does.

**Both the workspace and the home directory live on the volume.** Without that,
scaling to zero would throw away anything `postCreate` wrote to `~`.

**The build targets `linux/amd64` by default.** Your Mac is arm64 and your nodes
probably are not; an arm64 image on an amd64 node crash-loops with `exec format
error`. Change it with `--platform` if your nodes are arm64.

**Cross-architecture builds need buildx or podman.** The devcontainer CLI
refuses `--platform` and `--push` without BuildKit, so `dev` picks whichever
builder the host has:

| Host has | How it builds |
|---|---|
| docker with buildx | cross-builds and pushes in one step |
| podman | cross-builds through `--docker-path podman`, then `podman push` — `podman build` has no `--push` |
| docker without buildx | this host's architecture only, then `docker push` |

The last row cannot honour a cross-architecture request, so it stops and says so
rather than producing an image that crash-loops on the node. docker+buildx is
preferred over podman when both are present: it is one step, and building
Features under rootless podman is known to fail on the bind mount the generated
Dockerfile uses ([devcontainers/cli#548](https://github.com/devcontainers/cli/issues/548)).

**A rotated secret needs a restart to reach the pod's own environment.** The
Secret is read by `envFrom` at pod start. Commands you exec afterwards do get
the current value, since `dev` passes the environment on each call — but a
process already running in the pod keeps the old one until `dev container stop`
and `start`.

## Commands

```
dev provider configure NAME --kind local
dev provider configure NAME --kind k8s [--context CTX] [--namespace NS]
                                       [--registry PREFIX] [--platform linux/amd64]
                                       [--storage-size 20Gi] [--storage-class SC]
                                       [--service-account SA] [--image-pull-secret NAME]
dev provider list
dev provider remove NAME

dev workspace init NAME --provider NAME
dev workspace use NAME
dev workspace list
dev workspace set KEY SPEC
dev workspace unset KEY
dev workspace show NAME
dev workspace remove NAME

dev container create NAME --folder PATH [--no-start]
dev container list [--all]
dev container start|stop NAME
dev container remove NAME [--force]
dev container rebuild NAME [--no-cache]
dev container logs NAME [-f]
dev container shell NAME
dev container exec NAME -- CMD [ARGS...]
dev container agent NAME --agent claude [-- ARGS...]
dev container sync NAME                       # k8s only
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

`sync` exists only for providers whose container holds a copy of your files. On
a local container it exits 2 saying the folder is already mounted.

`provider configure` changes the settings you name and leaves the rest as they
were, so moving one does not mean restating the other seven; pass `-` as the
value of an optional k8s setting to clear it. The kind is the one thing that
cannot change. Workspaces go on naming the provider and their containers stay in
the engine they were created in, so a flip would leave records pointing at an
engine that has never heard of them — `container list` would answer confidently
and wrongly, and `container remove` could never reach the real container. Remove
the provider and configure it again instead; `provider remove` refuses while a
workspace still names it, which is what keeps that from happening quietly.

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
rather than fails when there is none. The Kubernetes half of the smoke test is
gated on being told where to run:

```sh
DEV_SMOKE_K8S_CONTEXT=my-cluster \
DEV_SMOKE_REGISTRY=europe-docker.pkg.dev/my-project/dev \
  make smoke
```

`DEV_SMOKE_K8S_CONTEXT` and `DEV_SMOKE_REGISTRY` are what turn it on.
`DEV_SMOKE_K8S_NAMESPACE` (default `default`) and `DEV_SMOKE_K8S_PULL_SECRET`
are optional — the latter only matters when the pushed image is private.

Provider tests run against stub `devcontainer` and `docker` executables on a
`PATH` that holds nothing else, which is how the one invariant that breaks
silently gets checked: every `up` and `exec` for a container must pass the same
`--id-label dev.workspace=… --id-label dev.container=…` pair. Spell them
differently anywhere and the CLI looks up a container that does not exist, then
creates a second one beside it.
