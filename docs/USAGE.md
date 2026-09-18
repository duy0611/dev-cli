# Using dev

Walkthroughs for the jobs this tool exists to do, then a reference for every
command. For what `dev` *is* and how it fits together, read the
[README](../README.md) first.

- [Walkthroughs](#walkthroughs)
  - [First setup](#first-setup)
  - [Run an agent on a project](#run-an-agent-on-a-project)
  - [A scratch sandbox with no project](#a-scratch-sandbox-with-no-project)
  - [A project that ships no devcontainer config](#a-project-that-ships-no-devcontainer-config)
  - [Change what a generated container installs](#change-what-a-generated-container-installs)
  - [Add a secret and rotate it](#add-a-secret-and-rotate-it)
  - [Run a workspace in Kubernetes](#run-a-workspace-in-kubernetes)
  - [Clean up](#clean-up)
- [Troubleshooting](#troubleshooting)
- [Command reference](#command-reference)

## Walkthroughs

### First setup

A provider says where containers run; a workspace groups them and holds their
settings. You need one of each before anything else works.

```sh
dev provider configure local --kind local
dev workspace init personal --provider local
```

The first workspace becomes the active one, so nothing needs `dev workspace
use` yet. Check:

```sh
dev provider list
dev workspace list
```

### Run an agent on a project

The common case: a repository that already has a `.devcontainer/` directory.

```sh
dev container create api --folder ~/code/api
dev container agent api --agent claude
```

`create` reads the project's own configuration and never edits it. The first
run builds the image, which can take minutes; later starts are seconds.

`agent` starts the container if it is stopped, then checks the agent is
installed in the image. If it is not, `dev` says so and points at the project's
`devcontainer.json` — installing an agent means adding it there, which this tool
will not do to someone else's repository.

Anything after `--` goes to the agent:

```sh
dev container agent api --agent claude -- --help
```

To get a shell instead:

```sh
dev container shell api
```

### A scratch sandbox with no project

No repository, no host directory — somewhere to let an agent clone, scaffold or
experiment.

```sh
dev container create scratch --no-folder --tools node,gh,claude-code
dev container shell scratch
```

The work lives in `/workspaces/scratch`, backed by a volume `dev` creates and
removes with the container. It survives `stop` and `start` and it survives a
`rebuild`; only `dev container remove` destroys it.

At a terminal you can leave `--tools` off and pick from a list:

```sh
dev container create scratch --no-folder
```

In a script, no `--tools` means a bare Ubuntu image — a scripted run never
blocks on a question nobody is there to answer.

Nothing on your host is mounted, so there is no host directory to sync from:
`dev container sync` on a folderless container exits 2.

### A project that ships no devcontainer config

A checkout with no `.devcontainer/` is not a dead end. `--generate` renders a
base Ubuntu configuration from a catalog of tools and keeps it in dev's own
database — the project folder is still never written to.

```sh
dev container tools                       # what the catalog offers
dev container create tmp --folder ~/code/tmp --generate --tools node,yq
```

At a terminal, `create` offers this rather than failing, so `--generate` is
optional there. In a script it is required, and its absence is an error naming
the flag.

Unlike a folderless container, this one *does* bind-mount your folder: the
generated configuration only supplies what the project lacks.

A folder that ships its own configuration always wins. `--generate` against one
is an error rather than a silent shadowing:

```
dev: ~/code/api already has a devcontainer config; --generate would shadow it
```

To see what was generated:

```sh
dev container config show tmp
```

### Change what a generated container installs

`rebuild --tools` takes either a replacement list or `+`/`-` changes, never a
mix of the two:

```sh
dev container rebuild scratch --tools +helm,-yq       # adjust
dev container rebuild scratch --tools node,gh         # replace outright
```

The current set is read back out of the stored document, so a configuration you
edited by hand is still something this understands. A feature reference the
catalog does not know is left alone rather than dropped.

This only applies to containers `dev` generated. For a project-owned one, edit
the project's `devcontainer.json` and run `dev container rebuild NAME`.

### Add a secret and rotate it

A setting is one environment variable for every container in a workspace,
stored as a *spec* saying where the value comes from — never as the value:

```sh
dev workspace set GH_TOKEN op://Private/github/token
dev workspace set ANTHROPIC_API_KEY keychain:anthropic
dev workspace set API_URL literal:https://api.example.invalid
```

Specs are resolved on every invocation, so rotating the secret in 1Password or
the Keychain needs no rebuild and no restart — the next command picks it up. On
the k8s provider that holds for anything you `exec`, but the pod's own
environment comes from a Secret read at pod start, so a process already running
there keeps the old value until `dev container stop` and `start`.

`dev workspace show` prints the specs and never the resolved values:

```sh
dev workspace show personal
dev workspace unset API_URL
```

Your git `user.name` and `user.email` are passed through automatically, so the
first commit inside a container works. An explicit setting of the same name
wins.

### Run a workspace in Kubernetes

> The k8s provider is **experimental**. It works, but its configuration and
> behaviour may change without a migration path. See
> [the README](../README.md#the-k8s-provider-experimental) for what it does and
> the parts that bite.

```sh
dev provider configure prod --kind k8s      # prompts, defaulting from kubeconfig
dev workspace init cloud --provider prod
dev workspace use cloud
dev container create api --folder ~/code/api
```

Two things to do before the first create: log the builder in to your registry
(`docker login ghcr.io`), and, if the image will be private, create a pull
secret in the namespace and name it with `--image-pull-secret`.

Your files are a **copy**, not a mount. `create` streams the folder in; after
that, nothing goes up unless you ask:

```sh
dev container sync api
```

Nothing ever comes back down. Once an agent is working in the container, its
copy is the live one.

### Clean up

```sh
dev container remove scratch        # engine object and record, volume included
dev workspace remove personal       # refused while it still holds containers
dev provider remove local           # refused while a workspace still names it
```

The refusals are the point: removing a workspace that still holds containers
would leave them running on the engine with nothing that knows their names.
Remove the containers first.

`container remove --force` drops the record even when the engine removal fails,
which is how you clean up after an engine that no longer exists.

## Troubleshooting

**`no active workspace; run: dev workspace use NAME`** — every container command
acts on one workspace. Set the active one, or pass `--workspace NAME`.

**`give --folder PATH, or --no-folder for a container with no host folder`** —
`create` will not guess. A forgotten `--folder` would otherwise build an empty
sandbox, which you notice only when the agent cannot find your code.

**`no devcontainer config in …`** — the folder ships none. Add `--generate`, or
run it at a terminal and accept the offer.

**`provider … mounts the folder directly; there is nothing to sync`** — `sync`
is for providers whose container holds a *copy* of your files. A local container
is looking at the same files you are.

**`container … has no folder to sync from`** — a folderless container has no
host tree. Nothing to push.

**A rebuild did not pick up my new image (k8s)** — it should: `rebuild` bumps an
annotation on the pod template, because the tag never changes and nothing else
would make a new image visible. If it genuinely did not, check the push
succeeded.

**`exec format error` in a k8s pod** — an arm64 image landed on an amd64 node.
The build targets `linux/amd64` by default; if your nodes are arm64, set
`--platform linux/arm64` on the provider.

**`ImagePullBackOff`** — the nodes cannot read the registry. Create a pull secret
in the namespace and name it with `--image-pull-secret`.

**Permission denied writing into a folderless container's workspace** — should
not happen: the generated configuration chowns the volume to the remote user
once, at create. If you see it, the `postCreateCommand` did not run — check
`dev container logs NAME`.

**Everything looks wrong and I want to start over without losing my real state**
— point `DEV_STATE` somewhere else:

```sh
DEV_STATE=$(mktemp -d) dev workspace list
```

## Command reference

Every container and workspace-scoped command accepts `--workspace NAME`,
defaulting to the active workspace.

Exit codes: `0` success, `1` the work failed, `2` the request was malformed,
`3` something named does not exist.

### provider

```
dev provider configure NAME --kind local
dev provider configure NAME --kind k8s [--context CTX] [--namespace NS]
                                       [--registry PREFIX] [--platform linux/amd64]
                                       [--storage-size 20Gi] [--storage-class SC]
                                       [--service-account SA] [--image-pull-secret NAME]
dev provider list
dev provider remove NAME
```

| Flag | Applies to | Meaning |
|---|---|---|
| `--kind` | both | `local` or `k8s`. Cannot change after creation. |
| `--context` | k8s | kubeconfig context; default is the current one |
| `--namespace` | k8s | must already exist; `dev` creates no namespaces |
| `--registry` | k8s | prefix images are pushed to and pulled from; required |
| `--platform` | k8s | what to build for; default `linux/amd64` |
| `--storage-size` | k8s | PVC size per container; default `20Gi` |
| `--storage-class` | k8s | optional; default is the cluster's |
| `--service-account` | k8s | optional; default is the namespace's |
| `--image-pull-secret` | k8s | optional; needed when the image is private |

`configure` changes only the settings you name and leaves the rest as they were.
Pass `-` as the value of an optional k8s setting to clear it. At a terminal it
prompts for whatever the flags left empty; run from a script, a missing required
setting is an error naming the flag rather than a question nobody will see.

The kind cannot change. Workspaces go on naming the provider and their
containers stay in the engine they were created in, so a flip would leave
records pointing at an engine that has never heard of them. Remove and configure
again instead — `provider remove` refuses while a workspace names it, which is
what makes that route safe.

### workspace

```
dev workspace init NAME --provider NAME
dev workspace use NAME
dev workspace list
dev workspace set KEY SPEC
dev workspace unset KEY
dev workspace show NAME
dev workspace remove NAME
```

| Spec | Resolved by |
|---|---|
| `literal:https://example.invalid` | used as written |
| `keychain:SERVICE` | `security find-generic-password -a "$USER" -s SERVICE -w` |
| `op://vault/item/field` | `op read` |

`show` names its workspace outright rather than defaulting to the active one:
it is the command you read before trusting what a container will launch with,
and it should not depend on a pointer set somewhere else. It prints specs, never
values.

`remove` is refused while the workspace holds containers, running or not.
Removing the active workspace leaves none active rather than a dangling pointer.

### container

```
dev container create NAME --folder PATH [--generate] [--tools LIST] [--no-start]
dev container create NAME --no-folder [--tools LIST] [--no-start]
dev container list [--all]
dev container start NAME
dev container stop NAME
dev container remove NAME [--force]
dev container rebuild NAME [--no-cache] [--tools LIST]
dev container logs NAME [-f]
dev container shell NAME
dev container exec NAME -- CMD [ARGS...]
dev container agent NAME [--agent claude|codex|hermes|opencode] [-- ARGS...]
dev container sync NAME
dev container tools
dev container config show NAME
```

**`create`** takes `--folder PATH` or `--no-folder`, never both and never
neither.

| Flag | Meaning |
|---|---|
| `--folder` | host folder holding the project; bind-mounted on local, copied in on k8s |
| `--no-folder` | no host directory at all; work lives in a volume `dev` owns |
| `--generate` | render a base Ubuntu configuration when the folder ships none |
| `--tools` | comma-separated catalog tools for a generated container |
| `--no-start` | record the container without starting it |

The folder is resolved to a physical path before it is stored or mounted. On
macOS the engine runs in a VM and resolves paths inside it, where `/tmp` is a
real directory rather than a symlink to `/private/tmp` — an unresolved path
mounts an empty directory, silently.

**`list`** reads live status from the engine every time; a stored copy would be
wrong the moment anything happened outside `dev`. A `?` means the engine could
not be reached. The SOURCE column is `-` for a folderless container.

**`start`** creates the container if the engine has none, which is what makes
`create --no-start` followed by `start` work.

**`remove`** never touches the project folder. For a folderless container it
also deletes the volume, matching the k8s provider deleting its PVC. If the
engine refuses, the record is kept so the command can be retried; `--force`
drops it anyway.

**`rebuild`** recreates the container from its configuration. There is no
`devcontainer rebuild` — it is `up --remove-existing-container` underneath.
`--no-cache` rebuilds the image without the layer cache. `--tools` takes a
replacement list or `+`/`-` changes, but not both in one invocation, and applies
only to a container `dev` generated.

A rebuild keeps the volume on both providers. To start from an empty workspace,
`remove` and `create` — which is explicit about destroying the contents.

**`exec`** needs the `--`; everything after it belongs to the command being run,
not to `dev`.

**`agent`** defaults to `claude`. The agent must already be installed in the
image.

**`sync`** exists only for providers whose container holds a copy of your files,
so it is k8s-only in practice. On a local container it exits 2 saying the folder
is already mounted; on a folderless container it exits 2 saying there is no
folder to sync from.

**`tools`** lists the catalog, marking each entry official or community.
**`config show`** prints the configuration `dev` generated for a container, and
errors on one that uses the project's own.

### Environment

| Variable | Meaning |
|---|---|
| `DEV_STATE` | where the SQLite database lives; default `~/.local/state/dev/dev.db` |
| `DOCKER_HOST` | read by `docker`, which `dev` shells out to; how you point at Podman |
| `DEV_SMOKE_K8S_CONTEXT` | turns on the Kubernetes half of `make smoke` |
| `DEV_SMOKE_REGISTRY` | the other half of that switch; both must be set |
| `DEV_SMOKE_K8S_NAMESPACE` | optional, default `default` |
| `DEV_SMOKE_K8S_PULL_SECRET` | optional; only matters when the pushed image is private |
