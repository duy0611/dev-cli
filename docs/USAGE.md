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
  - [Push to git from inside a container](#push-to-git-from-inside-a-container)
  - [Keep an agent's plugins across a rebuild](#keep-an-agents-plugins-across-a-rebuild)
  - [Declare a project's agent setup](#declare-a-projects-agent-setup)
  - [Commit a devcontainer.json that also works under dev](#commit-a-devcontainerjson-that-also-works-under-dev)
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
dev container start-agent api --agent claude
```

`create` reads the project's own configuration and never edits it. The first
run builds the image, which can take minutes; later starts are seconds.

`start-agent` starts the container if it is stopped, then checks the agent is
installed in the image. If it is not, `dev` says so and points at the project's
`devcontainer.json` — installing an agent means adding it there, which this tool
will not do to someone else's repository.

Anything after `--` goes to the agent:

```sh
dev container start-agent api --agent claude -- --help
```

To get a shell instead:

```sh
dev container shell api
```

### Work a branch in its own checkout

A worktree gives a branch its own directory, so an agent can work on it without
disturbing what you have open. `dev worktree` makes the checkout and the
container together, and removes them together.

```
❯ dev worktree create fix-header --branch fix-header --path ~/wt/fix-header --generate
worktree fix-header on branch fix-header at ~/wt/fix-header
container fix-header is running
```

Run it from inside the repository, or name one with `--repo`. `--path` has no
default: nothing appears on your disk in a place you did not choose.

git works inside the container. A worktree's `.git` is a file pointing at an
absolute path in the repository, so `dev` bind-mounts the repository at that
same path — commits you make inside are commits the repository can see, sharing
one object store.

```
❯ dev container start-agent fix-header
```

When the branch is done:

```
❯ dev worktree remove fix-header
worktree fix-header removed; branch fix-header kept
```

The branch stays. git refuses to remove a checkout with uncommitted work in it,
and so does this — pass `--force` when you mean it.

```
❯ dev worktree list
WORKSPACE  NAME        BRANCH      CHECKOUT  PATH
default    fix-header  fix-header  ok        ~/wt/fix-header
```

`CHECKOUT` is read from git every time. `gone` means the checkout was removed
outside `dev`; `dev worktree remove` still cleans up the container and the
record.

Local providers only. A pod has no host bind mounts, so the paths a worktree
depends on cannot resolve there.

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

Nothing on your host is mounted, and nothing needs to be: `dev` never copies
your files into a container, folderless or not.

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

`config show` answers the same question for a container whose project ships its
own `devcontainer.json`, and there the answer is not the file on disk: `dev`
merges its state mount in on every invocation, so the document the container is
built from exists only for the length of one command. The path it came from goes
to stderr and the document to stdout, so `config show NAME | jq` still receives
JSON and nothing else.

```
❯ dev container config show api
dev: merged from ~/code/api/.devcontainer/devcontainer.json
{
  "image": "mcr.microsoft.com/devcontainers/base:1-ubuntu-24.04",
  "mounts": [
    "source=dev-<workspace>-api-state,target=/var/dev-state,type=volume"
  ],
  ...
}
```

A container created with `--no-persist-state` has no merge to show, and says so:
what the CLI receives really is the project's file, unchanged.

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

After rotating, run `sync` on your k8s containers:

```sh
dev container sync api
```

That refreshes the Secret in the cluster without touching the running pod. It
matters for the restart you did not ask for — an eviction, an OOM kill, a node
drain — where the ReplicaSet builds a replacement pod from the Secret as it
currently stands. Without a `sync`, that pod comes back holding the token you
rotated away, and the agent that was working starts getting 401s.

On the local provider `sync` has nothing to do and says so: every command
resolves the settings afresh, so there is no copy anywhere that could be stale.

`dev workspace show` prints the specs and never the resolved values:

```sh
dev workspace show personal
dev workspace unset API_URL
```

Your git `user.name` and `user.email` are passed through automatically, so the
first commit inside a container works. An explicit setting of the same name
wins.

### Push to git from inside a container

A container has no credentials of its own, so `git fetch` in one fails with
`Permission denied (publickey)`. Turn on agent forwarding and it can reach the
ssh agent already running on your machine:

```sh
dev workspace init personal --provider local --ssh-forward
```

Nothing is copied into the container. An ssh agent signs on request and never
hands the key over, so `dev` carries the *conversation* rather than the key:
there is nothing in the container to steal, even if something in it turns
hostile.

Commits are signed the same way. Since git 2.34 an ssh key can sign a commit,
so the agent that authenticates the push signs the commit too — no GPG, no
second key, and the signing happens on your machine:

```sh
dev container exec api -- git fetch
dev container exec api -- git commit -m 'signed by the agent on your laptop'
```

Four things to know:

- **It lasts for one command.** The agent is reachable while a `dev` command is
  running and no longer. A container you reach some other way — `docker exec`,
  say — has no relay and no git access.
- **Your host needs an agent with a key in it.** `ssh-add -l` should list one.
  If `SSH_AUTH_SOCK` is not set, `dev` says so by name rather than letting git
  fail later for reasons that look unrelated.
- **Signing uses the agent's first key.** An agent holding several gives no
  indication which one you meant. If the first is not the key your forge knows,
  commits arrive unverified — reorder your `ssh-add` calls, or start a fresh
  agent holding only the key you want.
- **Local verification is not set up.** `git log --show-signature` reports that
  `gpg.ssh.allowedSignersFile` is unset. The commit is signed and your forge
  verifies it; only local checking needs a file mapping emails to keys, which
  `dev` cannot write for you.

Both providers support this. On k8s the relay rides `kubectl exec` rather than a
local socket, so it works the same way from a pod.

### Keep an agent's plugins across a rebuild

A rebuild replaces the container filesystem, and on the local provider that is
where an agent keeps its configuration. Plugins, marketplaces, MCP servers and
anything you set with `git config --global` go with it:

```
❯ dev container exec api -- claude plugin marketplace list
No marketplaces configured.
```

New containers put that configuration on a volume instead, so there is nothing
to turn on:

```sh
dev container create api --folder ~/code/api
```

`--no-persist-state` opts out, and containers created before this existed stay
as they were — `dev container list` shows which is which under STATE.

It covers Claude Code, Codex, Hermes and opencode, each pointed at its own
directory on the volume by the variable it documents for the purpose —
`CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `HERMES_HOME`, `OPENCODE_CONFIG_DIR` — plus
a global gitconfig at `GIT_CONFIG_GLOBAL`. Set any of those as a workspace
setting and yours wins.

**Credentials do not go on the volume.** Agents read those from the workspace's
settings, resolved on every command:

```sh
dev workspace set ANTHROPIC_API_KEY keychain:anthropic
```

That is deliberate: a container runs programs that execute code on their own, so
nothing worth stealing should rest in one. The volume holds configuration. If
you log in interactively inside the container instead, the token that login
writes *will* persist there — that is your call to make, not something `dev`
does for you.

It is fixed at create, because a rebuild must not be able to change what a
container is mounted on. To change it, `remove` and `create` again. The volume
is removed with the container and survives everything else, including
`rebuild`.

One asymmetry worth knowing: for **opencode**, `OPENCODE_CONFIG_DIR` carries its
agents, commands, modes, plugins, skills and themes, but its `auth.json` lives
elsewhere and is not on the volume — so an opencode login does not survive a
rebuild, where a Claude Code one would. That is the tools differing, not `dev`
treating them differently.

On k8s all of this already worked — the pod's home directory lives on the PVC —
and a third subPath there carries the same paths, so the two providers behave
identically.

### Declare a project's agent setup

The state volume keeps what you install in one container across its rebuilds.
It does nothing for the next container. To stop typing the same `claude plugin
install` in every new one, declare it in the project:

```yaml
# .devcontainer/agents.yaml
version: 1

skills:                       # every installed agent gets these
  - path: ./skills/our-conventions          # relative to this file
  - git: https://github.com/obra/superpowers
    ref: v6.4.1
    path: skills/brainstorming

mcp:                          # and these
  context7:
    command: npx
    args: ["-y", "@upstash/context7-mcp"]
    env:
      CONTEXT7_API_KEY: ${CONTEXT7_API_KEY}

claude:
  marketplaces:
    claude-plugins-official: anthropics/claude-plugins-official
  plugins: [superpowers@claude-plugins-official]
opencode:
  plugins: [opencode-wakatime]
```

```sh
dev workspace set CONTEXT7_API_KEY keychain:context7
dev container create api --folder ~/code/api
```

`create` applies the file once the container is running — Claude Code through
its own CLI, opencode and hermes by merging into `opencode.json` and
`config.yaml` — and `rebuild` applies it again. `start` does not: like a
`devcontainer.json`, an edit waits for a rebuild. After `create --no-start`, the
first `start` applies it.

- **Top level vs. per agent.** `skills` and `mcp` at the top reach every agent
  installed in the container; each agent's own section adds more. `plugins`
  exists only under `claude` and `opencode`, `marketplaces` only under `claude`.
  hermes has no plugins. A per-agent MCP server with a top-level server's name
  replaces it for that agent.
- **`${NAME}` is a reference, never a value.** The file is committed, so `dev`
  never substitutes it; each agent resolves it when it starts, from the
  workspace's settings. A reference no setting defines is a warning.
- **An agent the container lacks is skipped with a warning**, so one file works
  across containers with different tools. A step that fails — a plugin that
  does not exist, a marketplace that cannot be reached — fails the command; the
  container stays running and the next `start` retries.
- **Nothing is removed.** A declared entry or skill directory is replaced; a
  plugin you drop from the file stays installed until the container is
  recreated. Things you added by hand are left alone.
- **`dev` only reads the file.** It never writes into the project.

[`docs/agents.sample.yaml`](agents.sample.yaml) lists every key the file
accepts, with a comment on each; copy it and delete what you do not need.

`--agent-config PATH` applies another file instead — one in your dotfiles, say,
which also gives a `--no-folder` container something to apply — and
`--no-agent-config` applies none. The choice is fixed at create, so `rebuild`
reads the same file without being told again.

Both providers support this; on k8s (experimental) the steps run through
`kubectl exec`.

### Commit a devcontainer.json that also works under dev

A project's own `.devcontainer/devcontainer.json` can name a mount at
`/var/dev-state` — a plain `source=dev-cli-state` volume is enough — and stay
correct for VS Code, which never sees anything else. While `dev` drives, it
reads that file, replaces whatever is mounted at `/var/dev-state` with the
per-container volume (`dev-<workspace>-<container>-state`), and passes the
merged result on both `up` and `exec`; nothing about the replacement is
stored, so editing the project file takes effect on the next command. A
project mounting something of its own elsewhere is untouched — only
`/var/dev-state` is ever replaced. (For a `dockerComposeFile` project, see
`--no-persist-state` under [`create`](#container) — the mount does not
reliably apply there.)

Plainly: **a container opened directly in VS Code gets no workspace
settings.** A workspace setting is stored as a spec — `keychain:NAME`,
`op://…` — and resolved to a value fresh on every `dev` command; no field in
a `devcontainer.json` can call that resolver. Opened outside `dev`, the
container starts, the agents have their state volume, and the secrets are
simply absent.

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

Your files are a **copy**, not a mount. `create` streams the folder in once, and
that is the only time `dev` writes to the container's workspace. From then on
the container's copy is the live one — an agent has been working in it, and
nothing you run from the host will overwrite that in either direction.

Getting later changes across is yours to arrange, the way it would be between
any two machines: commit and push from one, pull on the other.

```sh
dev container exec api -- git pull
```

`dev container sync api` pushes your workspace *settings*, not your files. See
[Add a secret and rotate it](#add-a-secret-and-rotate-it).

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

**`a local container takes its settings on every command; nothing to push`** —
`sync` on the local provider. Not an error: it exits 0, and the settings are
already current in every process `dev` starts.

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

**`agents.yaml: hermes is not installed in this container; skipping it`** —
the file declares something for an agent the image does not have. Add the agent
to the project's `devcontainer.json` (or `--tools hermes` for a generated one)
and rebuild, or ignore it if this container is not meant to run that agent.

**`claude: marketplace NAME: the command succeeded but the change is not
visible afterwards`** — the key under `marketplaces` is not the name the
marketplace gives itself. `dev container exec NAME -- claude plugin marketplace
list` shows the real one; use it as the key.

**My agent's plugins are gone after a rebuild** — the container does not
persist its state, which means it predates the volume becoming the default, or
it was created with `--no-persist-state`. `dev container list` says which under
STATE. It is fixed at create, so turning it on means `remove` and `create`
again; that destroys the container's work, so move anything you need off it
first.

**My opencode login is gone after a rebuild, but my plugins are not** —
expected. `OPENCODE_CONFIG_DIR` moves opencode's configuration onto the volume;
its
`auth.json` sits outside that directory and stays in the container filesystem.
Credentials are meant to come from workspace settings, not from the volume.

**`no SSH agent on this host: SSH_AUTH_SOCK is not set`** — the workspace
forwards the agent and there is nothing to forward. Start one and add a key:
`eval "$(ssh-agent -s)" && ssh-add`. Reported here rather than left to fail
inside the container, where it surfaces as `error fetching identities:
communication with agent failed` and says nothing about your machine.

**`Permission denied (publickey)` even with `--ssh-forward`** — check the agent
is reachable from inside: `dev container exec NAME -- ssh-add -l` should list
your keys. If it does, the key it lists is not one the forge accepts. If it
lists nothing, the agent on your machine is empty — `ssh-add` there first.

**git works under `dev` but not in a shell I opened another way** — expected.
The relay lives for the length of a `dev` command, so a shell started with
`docker exec` or `kubectl exec` has no agent to reach. Use `dev container shell`.

**`the ssh agent relay stopped unexpectedly; commits will not sign`** — the
relay died while the command that started it was still running, so anything the
container signs from here on fails. It is not recoverable in place: the agent
was handed its `SSH_AUTH_SOCK` when it started and a new relay would listen on a
different path, so restart the `dev` command. Reported when it happens rather
than left for the next commit to discover, because the failure is otherwise
silent — the relay and your agent are siblings, and neither notices the other
going away.

**My commits arrive unverified** — signing uses the agent's *first* key, and an
agent holding several gives no indication which you meant. Check with
`ssh-add -L | head -1`; if that is not the key your forge knows, reorder your
`ssh-add` calls or run an agent holding only that key.

**`gpg.ssh.allowedSignersFile needs to be configured`** — from
`git log --show-signature`, and not a failure: the commit is signed and your
forge will verify it. Only *local* verification needs a file mapping emails to
public keys, which `dev` does not write because the mapping is yours to decide.

**`GIT_CONFIG_COUNT is set for this workspace; not configuring commit signing`**
— you have set git's environment-based configuration yourself. Both would arrive
as the same variables and the last would win, so `dev` leaves yours alone rather
than silently replacing it. Add `gpg.format`, `user.signingkey` and
`commit.gpgsign` to your own pairs if you want signing too.

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
dev workspace init NAME --provider NAME [--ssh-forward]
dev workspace use NAME
dev workspace list
dev workspace set KEY SPEC
dev workspace unset KEY
dev workspace show NAME
dev workspace remove NAME
```

`--ssh-forward` lets containers reach the ssh agent on your machine, for the
length of each `dev` command, and signs commits with its first key. See
[Push to git from inside a container](#push-to-git-from-inside-a-container).
It is fixed at `init`: there is no flag to change it afterwards, so a workspace
that needs it later is a new workspace.

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
dev container create NAME --folder PATH [--generate] [--tools LIST] [--no-persist-state]
                         [--agent-config PATH | --no-agent-config] [--no-start]
dev container create NAME --no-folder [--tools LIST] [--no-persist-state]
                         [--agent-config PATH | --no-agent-config] [--no-start]
dev container list [--all]
dev container start NAME
dev container stop NAME
dev container remove NAME [--force]
dev container rebuild NAME [--no-cache] [--tools LIST]
dev container logs NAME [-f]
dev container shell NAME
dev container exec NAME -- CMD [ARGS...]
dev container start-agent NAME [--agent claude|codex|hermes|opencode] [-- ARGS...]
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
| `--no-persist-state` | do not give the container a volume for its agents' configuration |
| `--agent-config` | apply this agents.yaml instead of the project's `.devcontainer/agents.yaml` |
| `--no-agent-config` | apply no agents.yaml, even if the project has one |
| `--no-start` | record the container without starting it |

The folder is resolved to a physical path before it is stored or mounted. On
macOS the engine runs in a VM and resolves paths inside it, where `/tmp` is a
real directory rather than a symlink to `/private/tmp` — an unresolved path
mounts an empty directory, silently.

A container keeps its agents' configuration on a volume unless
`--no-persist-state` says otherwise. Either way it is fixed at create: a rebuild
must not be able to change what a container is mounted on, so changing your mind
is `remove` then `create` — which is explicit about destroying what was on the
volume.

Containers created before this existed keep their old behaviour. The volume they
never had is not conjured up by an upgrade; `dev container list` shows them as
`off`.

A project whose `devcontainer.json` names `dockerComposeFile` does not
reliably get the state volume: the devcontainer CLI treats `mounts` under
compose as the compose file's business, and implementations differ on whether
they apply it anyway. `create` still creates the container — everything else
works — but warns on stderr so a silently missing `/var/dev-state` is not a
surprise at the next rebuild. Add the volume to your compose file yourself, or
pass `--no-persist-state`.

A project's `.devcontainer/agents.yaml` is applied once the container is
running, and again on every `rebuild`; see
[Declare a project's agent setup](#declare-a-projects-agent-setup). A malformed
file is exit 2 before anything is created; an `--agent-config` file that does
not exist is exit 3, at create or at a later rebuild.

**`list`** reads live status from the engine every time; a stored copy would be
wrong the moment anything happened outside `dev`. A `?` means the engine could
not be reached. The SOURCE column is `-` for a folderless container, and STATE
says whether its agents' configuration is on a volume.

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

**`start-agent`** defaults to `--agent claude`. The agent must already be
installed in the image.

**`sync`** pushes the workspace's settings into a container and never touches
your files. On k8s it refreshes the Secret the pod is built from, leaving the
running pod alone; on local it exits 0 having done nothing, because every
command already resolves the settings afresh. The container has to be running
either way.

**`tools`** lists the catalog, marking each entry official or community.
**`config show`** prints the configuration a container is built from: the
generated document, or for a project-owned container the merged one, with the
path it came from on stderr.

### worktree

```
dev worktree create NAME --branch B --path P [--repo R] [--base REF]
                         [--generate] [--tools LIST] [--no-persist-state]
                         [--agent-config PATH | --no-agent-config]
                         [--no-start] [--no-herdr] [--workspace W]
dev worktree list [--all] [--workspace W]
dev worktree remove NAME [--force] [--workspace W]
```

`NAME` names both the checkout's record and the container: one name, so there
is nothing extra to remember when the two are removed together.

**`create`**

| Flag | Meaning |
|---|---|
| `--repo` | repository to add the worktree to (default: the one holding the working directory) |
| `--branch` | branch to check out or create |
| `--path` | where to create the checkout |
| `--base` | start point for a new branch |
| `--no-herdr` | do not register the checkout with Herdr |
| `--no-start` | record the container without starting it |
| `--generate` | generate a base Ubuntu configuration when the checkout ships none |
| `--tools` | comma-separated tools to install in a generated container (see: dev container tools) |
| `--no-persist-state` | do not give the container a volume for its agents' configuration |
| `--agent-config` | apply this agents.yaml instead of the checkout's `.devcontainer/agents.yaml` |
| `--no-agent-config` | apply no agents.yaml, even if the checkout has one |

`--branch` and `--path` are required; `--path` has no default, so nothing
appears on disk somewhere you did not choose. `--base` only has an effect when
the branch does not already exist — passing it for one that does is a usage
error, not a silent no-op.

Local providers only: a workspace on the k8s provider is refused, since a pod
has no host bind mounts and the two absolute paths a worktree depends on
cannot resolve there.

A checkout of a repository that ships its own `.devcontainer` keeps it
untouched — `dev` never writes into a project's folder — and its bind mounts
are merged into the document at invocation time instead, the same way the
agent-state mount is for any other project-owned container.

**`list`** reads the checkouts git still knows about, live, every time. The
CHECKOUT column reads `ok` or `gone`; `gone` means the checkout was removed
outside `dev`, and `dev worktree remove` still cleans up the container and the
record either way.

**`remove`** takes the container from the engine first, then asks git to
remove the checkout, then drops the record. git refuses a checkout holding
modified or untracked files; that refusal leaves the record intact so the
command is retryable, and `--force` passes through to git. The branch is never
deleted — the checkout is scaffolding, the branch is the work.

`dev container remove` on a worktree-backed container still works: it removes
the container and warns that the checkout at its path was left behind, naming
`dev worktree remove` as what would have taken both.

### Environment

| Variable | Meaning |
|---|---|
| `DEV_STATE` | where the SQLite database lives; default `~/.local/state/dev/dev.db` |
| `DOCKER_HOST` | read by `docker`, which `dev` shells out to; how you point at Podman |
| `SSH_AUTH_SOCK` | your ssh agent, read on the host when a workspace uses `--ssh-forward`. Inside the container it names the relay instead |
| `DEV_SMOKE_K8S_CONTEXT` | turns on the Kubernetes half of `make smoke` |
| `DEV_SMOKE_REGISTRY` | the other half of that switch; both must be set |
| `DEV_SMOKE_K8S_NAMESPACE` | optional, default `default` |
| `DEV_SMOKE_K8S_PULL_SECRET` | optional; only matters when the pushed image is private |
