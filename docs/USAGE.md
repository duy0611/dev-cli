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
  - [Which credentials to give a container](#which-credentials-to-give-a-container)
  - [Push to git from inside a container](#push-to-git-from-inside-a-container)
  - [What a container can do to your git](#what-a-container-can-do-to-your-git)
  - [Changing a container's mode](#changing-a-containers-mode)
  - [Keep an agent's plugins across a rebuild](#keep-an-agents-plugins-across-a-rebuild)
  - [Declare a project's agent setup](#declare-a-projects-agent-setup)
  - [Commit a devcontainer.json that also works under dev](#commit-a-devcontainerjson-that-also-works-under-dev)
  - [Query a cluster from inside a container](#query-a-cluster-from-inside-a-container)
  - [Run a workspace in Kubernetes](#run-a-workspace-in-kubernetes)
  - [See what ran, and when](#see-what-ran-and-when)
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
disturbing what you have open. `dev worktree` makes the checkout; a container
on it is an ordinary `dev container create`, run from inside it.

```
❯ dev worktree create fix-header --branch fix-header --path ~/wt/fix-header
worktree fix-header on branch fix-header at ~/wt/fix-header
run a container on it: cd ~/wt/fix-header && dev container create fix-header
❯ cd ~/wt/fix-header
❯ dev container create fix-header --generate
❯ dev container start-agent fix-header
```

Run `worktree create` from inside the repository, or name one with `--repo`.
`--path` has no default: nothing appears on your disk in a place you did not
choose.

git works inside the container. A worktree's `.git` is a file pointing at an
absolute path in the repository, so `container create` asks git where that
repository is and bind-mounts it at the same path — commits you make inside are
commits the repository can see, sharing one object store. It does this for any
worktree, including one made with plain `git worktree add`.

A fresh checkout holds only what git tracks, so the `.env` files and
dependency trees your project ignores are missing. List the ones worth carrying
over in a `.worktreeinclude` at the root of the checkout you run `create` from,
in `.gitignore` syntax:

```gitignore
.env*
node_modules/
!.env.production
```

`create` then copies the ignored files it matches from that checkout into the
new one:

```
❯ dev worktree create fix-header --branch fix-header --path ~/wt/fix-header
worktree fix-header on branch fix-header at ~/wt/fix-header
copied 3 ignored paths from ~/src/app (.env, .env.local, node_modules/)
run a container on it: cd ~/wt/fix-header && dev container create fix-header
```

On APFS and on btrfs or xfs the copy is a clone, costing neither time nor disk
until a file changes; elsewhere it is a real copy. Run the project's install
once the container is up — it fetches only what differs, and repairs a library
built for the host rather than the container. A copied Python `.venv` still
names the original checkout's path, so recreate it (`uv sync`,
`python -m venv --clear`) rather than reuse it.

When the branch is done, remove the container, then the checkout:

```
❯ dev container remove fix-header
❯ dev worktree remove fix-header
worktree fix-header removed; branch fix-header kept
```

`worktree remove` refuses while a container is still on the checkout. The
branch stays. git refuses to remove a checkout with uncommitted work in it, and
so does this — pass `--force` when you mean it.

```
❯ dev worktree list
WORKSPACE  NAME        BRANCH      CHECKOUT  PATH             CONTAINERS
default    fix-header  fix-header  ok        ~/wt/fix-header  fix-header
```

`CHECKOUT` is read from git every time. `gone` means the checkout was removed
outside `dev`; `dev worktree remove` still drops the record. `CONTAINERS` names
the containers whose folder is the checkout.

A container on a worktree needs a local provider. A pod has no host bind
mounts, so the paths a worktree depends on cannot resolve there.

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

### Start another container like an existing one

`--from` starts a new container from the setup of one you already have in the
workspace, so you do not retype its tools:

```sh
dev container create scratch --from api --no-folder
dev container create other   --from api --folder ~/src/other
dev container create fix     --from api --folder ~/wt/api-fix   # a worktree checkout
```

It copies three things from the source: its tool list, whether it keeps agent
state on a volume, and which agents.yaml it applies. `--tools` then takes only
`+`/`-` changes to that list (`--tools +helm,-yq`), since a replacement list
would throw away what `--from` copied. `--no-persist-state`, `--agent-config` and
`--no-agent-config` override what is inherited. A source that applies its own
project's agents.yaml passes that on as "the new folder's own agents.yaml", not
as the source's file.

These are never copied:

- **Volumes and mounts.** The configuration is rendered afresh for the new
  container, so it gets its own workspace volume, its own state volume and, for a
  worktree, bind mounts for its own checkout. The two containers share nothing
  on disk.
- **Work.** Files, agent state and history stay with the source.
- **A project's own configuration.** Only a container `dev` generated can be a
  source. One built from a project's `devcontainer.json` is refused (exit 2),
  because that file belongs to its project. Point `--folder` at a project that
  ships the same file instead. A target folder that ships a configuration of its
  own is refused too, as with `--generate`.

The source is only read, it does not need to be running, and its later changes
do not reach the clone. After create, the new container is an ordinary generated
container. On local the same tools give the same image layers, so the build is
expected to come from Docker's cache. Each container still gets its own image.

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

Your git `user.name` and `user.email` are passed through as `GIT_AUTHOR_NAME`,
`GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_NAME` and `GIT_COMMITTER_EMAIL`, so the first
commit inside a container works with no gitconfig there. They take precedence
over a `git config user.name` set inside the container; to commit as someone
else, set those variables on the workspace, since an explicit setting of the
same name wins.

### Which credentials to give a container

Anything a container can read, an agent in it can send anywhere: assume a
setting's value will leak one day, and choose settings that make that cheap.
`dev` keeps values out of its database and its audit log, but inside the
container they are ordinary environment variables — that is deliberate, and
what follows is how to live with it.

- **Model API keys:** one key per workspace, never your personal key or one
  shared with anything else. Put a spend limit on it at the issuer — an
  Anthropic workspace spend limit, an OpenRouter per-key `limit` — so a leaked
  key costs a capped amount. Anthropic keys do not expire, so revocable and
  capped is the most there is.
- **GitHub:** prefer the ssh relay (see
  [Push to git from inside a container](#push-to-git-from-inside-a-container)),
  and feed it an ssh agent that holds only your GitHub key. The relay forwards
  every key your agent holds, so a container could otherwise sign for any host
  your personal key reaches:

  ```sh
  eval "$(ssh-agent -s)"          # a fresh agent, in this shell only
  ssh-add ~/.ssh/github_ed25519   # the GitHub key and nothing else
  dev container start-agent api --ssh-agent   # relays this agent, not your usual one
  ```

  When a token is needed instead, use a fine-grained personal access token
  scoped to the one repository, with the shortest expiry that fits.
- **Kubernetes:** `--kube-token`, minted for a ServiceAccount bound to the
  narrowest role that does the job — never a copied kubeconfig. On the k8s
  provider, leave the pod's own ServiceAccount unbound.
- **Logging in inside the container:** `claude /login` and `opencode auth login`
  store a long-lived token on the container's state volume. Prefer a workspace
  setting: it is revocable at the issuer and resolved fresh on every command.

When a key may have leaked, revoke it at the issuer first, then repoint the
setting at its replacement — `dev workspace set NAME <new spec>`, or rotate the
value behind the spec. Local containers pick it up on their next command; k8s
containers need `dev container sync NAME`. `dev audit` says which containers
ran with the workspace, and when.

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

Five things to know:

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
- **github.com is already trusted.** Every `start` and `rebuild` adds
  github.com's host key to the container's `~/.ssh/known_hosts`, so the first
  push does not fail on `Host key verification failed`. The keys come from
  `ssh-keyscan` inside the container, and only those matching
  [GitHub's published fingerprints](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/githubs-ssh-key-fingerprints)
  are written; a scan that matches none writes nothing. A failure — no network,
  no `ssh-keyscan` or `ssh-keygen` in the image, an unrecognised key — is a
  warning, and the container starts anyway. Other hosts are yours to add.
- **Local verification is not set up.** `git log --show-signature` reports that
  `gpg.ssh.allowedSignersFile` is unset. The commit is signed and your forge
  verifies it; only local checking needs a file mapping emails to keys, which
  `dev` cannot write for you.

The workspace setting is a default, not a lock. `--ssh-agent` forwards the
agent for one command in a workspace that does not, and `--no-ssh-agent` keeps
it out of one command in a workspace that does — an agent you are about to
leave unattended, say:

```sh
dev container exec api --ssh-agent -- git push
dev container start-agent api --no-ssh-agent
```

Both providers support this. On k8s the relay rides `kubectl exec` rather than a
local socket, so it works the same way from a pod.

### Work across several repositories at once

Name each folder with `--folder`; the first is the **primary**:

```sh
dev container create both --folder ~/code/something/repo-1 --folder ~/code/something/repo-2
```

The primary is the project: its `.devcontainer`, or a generated configuration,
builds the container, and its `.devcontainer/agents.yaml` is the one applied.
Every other folder appears at `/workspaces/<its folder name>` — a git worktree
checkout at its own host path, as a primary one is — and has the mode
the primary has (see
[What a container can do to your git](#what-a-container-can-do-to-your-git)).
In guarded mode each repository's `.git` is read-only; in unprotected mode it is
writable; in isolated mode each folder is copied into a volume of its own. A
folder you do not name is not in the container at all.

A new container puts its primary at `/workspaces/<its folder name>` in every
mode, so one set of agent instructions serves all three.

`create` refuses, exit 2, a set it cannot mount cleanly: a folder given twice,
a folder inside another — for a guarded primary that includes anywhere else in
its repository, which is already mounted whole — and two folders that would
appear at one path, such as `--folder a/api --folder b/api`. Rename one, or
leave one out.

`container list` shows the primary followed by `+N`.

`dev container rebuild NAME --mode MODE` changes a local container's mode; see
[Changing a container's mode](#changing-a-containers-mode). A guarded or
unprotected container's extra folders can change on a rebuild; each flag is
repeatable:

```sh
dev container rebuild both --add-folder ~/code/something/repo-3
dev container rebuild both --remove-folder ~/code/something/repo-2
```

An added folder gets the container's mode from the rebuilt container's first
command; a removed one's work is already on your host. If an extra folder
disappears from your host, `start` refuses, exit 1, naming it and the
`--remove-folder` that drops it. An isolated container's folders, and any
container's primary, are fixed: recreate the container to change them. In
isolated mode `fetch` brings each repository's branches home into that
repository, and `remove` checks each one, naming the folder that still holds
work.

The experimental k8s provider takes one folder; more is exit 2.

### What a container can do to your git

A container's workspace is your checkout, bind-mounted, so anything running in
it can write anywhere in it — including `.git`. Host git runs programs named
there: `core.fsmonitor` in `.git/config` runs on every `git status`, which your
editor runs every few seconds, and hooks run on commit, checkout and push.
[SECURITY.md](SECURITY.md) explains the risk and how to choose a mode; this
section is what each mode does.

**Guarded** (`--mode guarded`, the default on the local provider) mounts `.git`
read-only, whole. Nothing under it can be written from inside — not its config,
not its hooks, not a planted `commondir`, under any spelling of the name — and
your own git on the host is unaffected: you can commit, push, set upstreams and
change configuration while the container runs.

What works inside: `git status`, `diff`, `log`, `blame`, anything that only
reads. What does not: `commit`, `add`, `stash`, creating a branch, `fetch` and
`pull`, all of which write to `.git` and fail with `Read-only file system`. You
review the agent's edits in your checkout and commit them yourself. Tell the
agent so in your project's agent instructions, or it will keep trying. The
project's `AGENTS.md` or `CLAUDE.md` is in the container in every mode, so one
passage can serve all by telling the agent how to recognise the guarded
case:

```markdown
## Git in the dev container

This container may be guarded, with `.git` mounted read-only:
- `git status`, `diff` and `log` always work.
- If `git commit` (or `add`, `stash`, `checkout -b`, `fetch`) fails with
  `Read-only file system`, the container is guarded. Do not retry or work
  around it: leave the changes uncommitted and say so in your summary.
  The operator reviews and commits them from the host.
- Otherwise commit on a branch, and push if asked. If the container is
  isolated, the operator brings the work home with `dev container fetch`.
```

Go by the error rather than a probe: in a worktree `.git` is a file, and
`test -w` calls a read-only mount writable as root.

A guarded container has no use for the ssh agent, so it gets none:
`--ssh-agent` is refused, and a workspace created with `--ssh-forward` does not
forward into it.

For a git worktree, the repository's common directory is mounted read-only at
its own path instead.

Host git can also be pointed *into* the working tree: a `core.hooksPath` such as
`.githooks` or `.husky`, or an `include.path` naming a file in the checkout.
Those are ordinary files the container can edit, and host git runs them. So
before every `start`, `exec`, `shell`, `start-agent` and `rebuild`, `dev` asks
your host git what it would run from inside the workspace — your global config
counts too — and:

- mounts each one read-only inside the container, with every directory between
  it and the workspace root pinned so it cannot be renamed away. The agent can
  still read and run the hooks, but not change them.
- refuses a path no mount can hold, naming it and the setting: one that does
  not exist yet (`core.hooksPath .husky` before husky has created it), or one
  reached through a symlink inside the workspace, which the container could
  repoint. Create the directory, or point the setting at the real path.
- refuses a running or stopped container created before the setting appeared —
  you ran `make hooks` on the host after `create` — because mounts are fixed
  when a container is created. `dev container rebuild <name>` gives it them.
- refuses one where a file held this way has become writable from inside. A
  read-only mount on a single file is lifted when the file is saved on the host
  — git and most editors save by renaming a new file over the old — and stays
  lifted until the container restarts. `dev container stop` and `start` put it
  back. Directories are not affected.

None of this is decided at create, since the setting usually arrives later.
Nor does it count as a configuration change for `rebuild`'s drift check: the
mounts follow your host git config, not the project. A hooks directory edited
on the host is seen inside at once, as any file in the workspace is; the
container just cannot write it.

Setting `core.hooksPath` to a directory in the checkout still means your host
runs whatever the branch you check out puts there. Copying hooks into
`.git/hooks` instead — this repository's `make hooks` does — makes installing a
changed hook a step you take after reading it.

What guarded does not cover:

- **A submodule's `.git` file.** It sits in the working tree and points your git
  at the submodule's repository; the container can repoint it. `create` warns
  when a folder has submodules, naming it when there are several.
- **A worktree checkout's `.git` file**, for the same reason. `create` warns
  when a folder is a worktree.
- **A nested repository** the agent creates in the working tree, which your
  editor may find and run git in.
- **A project whose `devcontainer.json` names its own `workspaceMount` or
  `workspaceFolder`.** That puts `.git` where `dev` cannot mount it, so `create`
  refuses it in guarded mode.
- **Files that run only when you run them** — `.envrc`, `.vscode/tasks.json`, a
  `Makefile`, `package.json` scripts, a hook manager's config in the tree
  (lefthook's `lefthook.yml`, pre-commit's `.pre-commit-config.yaml`). The agent
  edits those like any other file. Review before running workspace code.

**Unprotected** (`--mode unprotected`, local provider only) mounts your folder
live with nothing over `.git`: it is writable, and the agent can commit,
branch, stash and fetch in it, with your checkout showing each change as it
happens. It gives up the guard. Anything running in the container can write
`core.fsmonitor`, a hook or any other setting that names a program, and host git
runs it the next time you use git in the folder. `create --mode unprotected`
and a rebuild into it print a warning that says so and names
`rebuild --mode guarded` as the way back.

None of guarded's checks run: no hooks-path or `include.path` mounts, no probe
before each command, and no refusal of a project that sets its own
`workspaceMount`, whose mount is used as the project wrote it. The ssh agent is
allowed, as in isolated: `--ssh-agent` and `--ssh-forward` work, so the agent
can push and sign. A worktree folder's common directory is mounted writable at
its own path so the checkout's `.git` file resolves. `fetch` refuses an
unprotected container, since its work is already in your folder. `container
list` shows `unprotected` in `MODE`. [SECURITY.md](SECURITY.md) says when to
choose it.

**Isolated** (`--mode isolated`) never shares your folder with the container.
On first create, `dev` copies it into a volume the container owns: the working
tree as it is — uncommitted changes, untracked and ignored files, so `.env` and
`node_modules` come along — and the repository rebuilt from a git bundle, with
the branch you have checked out and your `origin` URL. Nothing of your `.git`
itself is copied, so neither its config nor its hooks reach the container. The
project's lifecycle commands (`postCreateCommand` and the rest) run after the
copy, so an install step finds the project there. The image needs git for this.

The agent then commits, branches and rebases as it would anywhere, and pushes
and signs through the ssh relay. Bring its work home with
[`dev container fetch`](#container):

```sh
dev container fetch api
git log --oneline main..api/agent-work   # review before you merge
```

Nothing the container does reaches your folder or your git, so isolated mode
covers everything guarded does not. What it costs: you do not see the agent's
edits on your machine until you fetch, and the volume is the only copy of its
work until then. `dev container remove` refuses while the container holds
changes nobody committed, or a branch that is not merged into one of your
branches or pushed to a remote: a fetched `api/<branch>` alone does not count.
Once the check passes, `remove` deletes the `api/*` refs `fetch` wrote.
`--force` removes the container anyway and keeps those refs. `rebuild` keeps
the volume.

**Containers created before modes** keep the guard they were created with:
`.git` writable, with `.git/config` read-only as a single file and `.git/hooks`
a copy only the container sees. That guard has two known gaps — a git command
on your host that writes repository configuration (`push -u`, `remote add`, your
editor publishing a branch) lifts it until the container restarts, and on
macOS a write to `.git/CONFIG` gets past it — and, for a project that moves its
workspace, it only fingerprints `.git` around each command. Recreate such a
container in a mode.

### Changing a container's mode

`dev container rebuild NAME --mode MODE` recreates a local container in another
mode. Each allowed change, and what it keeps:

| From \ To | guarded | unprotected | isolated |
|---|---|---|---|
| guarded | no-op | yes | yes, one way |
| unprotected | yes | no-op | yes, one way |
| isolated | no | no | no-op |

Asking for the current mode changes nothing about the mode and the rebuild goes
ahead. Everything else is exit 2 and touches nothing: leaving isolated (bring
the work home with `dev container fetch` and create a new container), any change
on a container created before modes or with `--no-folder`, `guarded` or
`unprotected` on the k8s provider, and `--mode guarded` on a project that sets
its own `workspaceMount`. Every folder is placed again for the new mode and the
set is checked as `create` checks it, so a set the new mode refuses is exit 2.
`--mode` combines with `--add-folder` and `--remove-folder`.

The common use is to open a guarded container up for a while, then close it:

```sh
dev container rebuild api --mode unprotected   # open up for a stack of PRs
dev container rebuild api --mode guarded       # guarded again
```

Switching to guarded protects from then on and undoes nothing: whatever the
container wrote to `.git` while it was unprotected is still there. Read
`.git/config` and the hooks before you trust the folder again. A rebuild
replaces the container, so a running agent session stops; `start-agent` resumes
where the agent keeps its own state.

A switch into isolated copies each folder into a volume as it is at that moment,
uncommitted and untracked files included, exactly as the first create of an
isolated container does, then runs the lifecycle commands. If `up` or the copy
fails, `dev` removes the new container and volumes, returns the original error
and keeps the old mode: rerun the command, or `rebuild` for the old container
back. If only a lifecycle command fails, the container is already isolated and
its volume holds the copy, so the mode is recorded as isolated. A volume left
from an earlier isolated life is exit 1, naming it.

The mode is recorded only after the rebuild succeeds, never by `start` or
`exec`. The audit log's `rebuild` record carries `mode_from` and `mode_to`.

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
    paths:                                  # or path: for a single skill
      - skills/brainstorming
      - skills/writing-plans

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
- **One repository, several skills.** `paths` lists subdirectories of a git
  skill, each named after its last segment; `path` takes one. The repository is
  cloned once per agent, and skills from the same repository and `ref` share
  that clone even across entries. If any path lacks a `SKILL.md`, none of that
  repository's skills is replaced.
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

### Query a cluster from inside a container

An agent sometimes needs to read a cluster — the pods behind a change, the
events behind a failing rollout. `--kube-token` gives it `kubectl` access as a
ServiceAccount you choose, with a token that expires on its own.

This works the same on both providers and has nothing to do with the
experimental k8s provider: the cluster you query and the engine the container
runs in are unrelated.

Make the ServiceAccount first, with only the access you mean to give. A
read-only one in a single namespace:

```sh
kubectl -n team-a create serviceaccount reader
kubectl -n team-a create rolebinding reader-view --clusterrole view --serviceaccount team-a:reader
```

Then pass the flag to `shell`, `exec` or `start-agent`:

```
$ dev container start-agent api --kube-token
context: prod-eu
namespace: team-a
service account: reader
duration, empty for kubectl's default (1h): 4h
dev: token for team-a/reader on prod-eu expires at 19:42 (4h0m0s)
```

With `fzf` installed on the host, the first three questions are fzf lists of
what your kubeconfig and the cluster offer. Without it, the choices are listed
and you type one. A list the cluster will not give you — RBAC often forbids
listing ServiceAccounts — becomes a free-text question, since you may know the
name without being allowed to list it. The questions are asked every time.

`dev` mints the token on the host with `kubectl create token`, writes a
kubeconfig holding it to `/tmp/dev-kube/config` in the container, and runs the
command with `KUBECONFIG` pointing there. The kubeconfig carries the token and
nothing else of yours: your own client certificates and exec plugins never leave
the host. The container needs `kubectl`; `dev` warns when it has none, and does
not install it.

**Refreshing.** When the token expires, `kubectl` in the container starts
answering `Unauthorized`. Mint a new one from the host:

```sh
dev container exec api --kube-token -- true
```

`kubectl` rereads its kubeconfig on every call, so the session already running
— the agent you started four hours ago — picks up the new token on its next
call, without a restart.

**The duration.** 1h is kubectl's default, not a limit. The cluster's limit is
the API server's `--service-account-max-token-expiration`; upstream sets none,
but a managed cluster may, and a cluster that caps below your request issues a
shorter token without refusing. `dev` reads the token's real expiry and says so:

```
dev: asked for 4h0m0s, the cluster issued 1h0m0s; its maximum is lower
```

**The last mint wins.** There is one kubeconfig per container. A later
`--kube-token` that picks a different ServiceAccount changes the identity of
every session already using it — a read-only session becomes whatever the new
account can do. Pick the same account when you refresh.

Only commands run with the flag get `KUBECONFIG`. A plain `shell` afterwards
does not, though the file is still there; on a k8s-provider pod that keeps it on
the pod's own in-cluster credentials. Those are the token of the provider's
`serviceAccount` — the namespace's `default` account when unset, which holds no
permissions unless someone has bound some to it. Leave that account unbound,
and `--kube-token` stays the only way a container gains cluster access.

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

### See what ran, and when

Every container `dev` creates, rebuilds, starts, stops or removes, every command
it runs in one, every kube token it mints and every ssh agent relay it opens is
written to an audit log on the host, beside the database. No container can
reach it, so an agent cannot edit the record of what it did.

```sh
dev audit                                # everything, oldest first
dev audit --container api --since 24h    # one container, the last day
dev audit --event exec,start-agent       # only commands that ran
dev audit --json | jq 'select(.exit_code != 0)'
```

```
2026-10-08 14:02:11  personal/api  create       folder
2026-10-08 14:02:40  personal/api  start
2026-10-08 14:03:05  personal/api  kube-token   sa reader in team-a (dev-cluster), expires 15:03
2026-10-08 16:15:52  personal/api  start-agent  claude, exit 0, after 2h12m47s
2026-10-08 16:15:52  personal/api  relay        clean after 2h12m47s
2026-10-09 10:13:09  personal/api  relay        clean after 22h41m32s, recovered from 1 drop
```

It records names, never values: the command you typed, setting names, the
ServiceAccount a token was minted for — never a setting's value, an environment
variable's value, or a token. It is the place to start when deciding what to
revoke after a container has had something it should not.

A removed container's history stays: the log outlives `container remove` and
`workspace remove`, and `dev audit --container NAME` reads it for a container
that no longer exists. A command that runs for a long time is recorded once it
ends, so a session cut off by the host going down leaves no record of itself.

What the log cannot see is what an agent did *inside* a session — that is the
agent's own transcript.

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

**`… would mount everything under it`** — `create` was run from your home
directory or `/`, or given one as `--folder`. With no `--folder`, `create` uses
the current directory, so `cd` into the project first, or pass `--folder PATH`.

**`no devcontainer config in …`** — the folder ships none. Add `--generate`, or
run it at a terminal and accept the offer.

**`a local container takes its settings on every command; nothing to push`** —
`sync` on the local provider. Not an error: it exits 0, and the settings are
already current in every process `dev` starts.

**A rebuild did not pick up my new image (k8s)** — it should: `rebuild` bumps an
annotation on the pod template, because the tag never changes and nothing else
would make a new image visible. If it genuinely did not, check the push
succeeded.

**`image platform (linux/amd64) does not match the expected platform
(linux/arm64)`, or `uname -m` prints `x86_64` on Apple Silicon** — a local
container built from an amd64 image under emulation, typically after a k8s build
moved the base image's tag. `create` and `rebuild` now build for the engine's
own platform, so a rebuild fixes it. If you set `DOCKER_DEFAULT_PLATFORM`
yourself, `dev` passes it through and you get the platform you asked for.

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

**`the ssh agent relay stopped unexpectedly and could not be restarted; commits
will not sign`** — the relay's exec channel collapsed while the command was still
running, and no replacement came up within five minutes. `dev` restarts a
dropped relay by itself, on the same socket path, so the `SSH_AUTH_SOCK` your
agent was started with keeps working and nothing needs restarting. A git command
that runs in the gap fails, and the next one works. The usual cause is a laptop
sleeping through a long session or Docker Desktop restarting, and both normally
recover on the first retry. This message means the engine stayed unreachable:
check that the container is still running, then restart the `dev` command.
`dev audit --event relay` shows every drop, as `recovered from 1 drop` or
`down since 2026-10-09 23:14`.

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
                                       [--storage-size 1Gi] [--storage-class SC]
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
| `--storage-size` | k8s | PVC size per container; default `1Gi` |
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
dev workspace init NAME --from WORKSPACE [--provider NAME] [--ssh-forward]
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
It is fixed at `init`: there is no flag to change it afterwards. A single
command can override it with `--ssh-agent` or `--no-ssh-agent`; a workspace that
needs the other default for good is a new workspace.

`--from` starts a workspace as a copy of another: the same provider, the same
`--ssh-forward` choice, and every setting. `--provider` and `--ssh-forward`
given alongside it override what is copied, which is how to put one set of
settings on a second provider:

```sh
dev workspace init staging --from personal --provider prod
```

The provider is shared, not duplicated — a later `provider configure` changes
it for both workspaces. Settings are copied as specs, never resolved, and after
the copy each workspace's settings change independently. Containers are not
copied.

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
dev container create NAME [--folder PATH]...
                         [--mode guarded|unprotected|isolated] [--generate] [--tools LIST] [--no-persist-state]
                         [--agent-config PATH | --no-agent-config] [--no-start]
                         [--allow-privileged]
dev container create NAME --no-folder [--tools LIST] [--no-persist-state]
                         [--agent-config PATH | --no-agent-config] [--no-start]
dev container create NAME --from SOURCE [--folder PATH | --no-folder] [--tools +ID,-ID]
                         [--no-persist-state] [--agent-config PATH | --no-agent-config] [--no-start]
dev container list [--all] [--timeout DURATION]
dev container start NAME
dev container stop NAME
dev container remove NAME [--force]
dev container rebuild NAME [--no-cache] [--tools LIST] [--accept-config]
                          [--add-folder PATH]... [--remove-folder PATH]...
                          [--mode guarded|unprotected|isolated]
dev container logs NAME [-f]
dev container shell NAME [--kube-token] [--ssh-agent|--no-ssh-agent]
dev container exec NAME [--kube-token] [--ssh-agent|--no-ssh-agent] -- CMD [ARGS...]
dev container start-agent NAME [--agent claude|codex|hermes|opencode] [--agent-name NAME] [--kube-token] [--ssh-agent|--no-ssh-agent] [-- ARGS...]
dev container sync NAME
dev container fetch NAME
dev container tools
dev container config show NAME
```

**`create`** takes `--folder PATH` or `--no-folder`, never both. With neither,
the folder is the current directory, and `create` prints which one it used.
The host's root and your home directory are refused as a folder: either would
mount everything under it. `--folder` may be repeated: the first is the
primary, and the rest are extra folders (see
[Work across several repositories at once](#work-across-several-repositories-at-once)).

| Flag | Meaning |
|---|---|
| `--folder` | host folder holding the project; bind-mounted on local, copied in on k8s. Repeatable: the first is the primary, each other appears at `/workspaces/<basename>`. Default: the current directory |
| `--mode` | `guarded`: the folder live, `.git` read-only; `unprotected`: the folder live, `.git` writable (local only); `isolated`: a copy the agent owns. Default `guarded` on local, `isolated` on k8s. Change it later with `rebuild --mode` |
| `--no-folder` | no host directory at all; work lives in a volume `dev` owns |
| `--generate` | render a base Ubuntu configuration when the folder ships none |
| `--tools` | comma-separated catalog tools for a generated container |
| `--no-persist-state` | do not give the container a volume for its agents' configuration |
| `--agent-config` | apply this agents.yaml instead of the project's `.devcontainer/agents.yaml` |
| `--no-agent-config` | apply no agents.yaml, even if the project has one |
| `--from` | start from another generated container's tools and settings in this workspace |
| `--no-start` | record the container without starting it |

The mode is chosen at create and changed only by `rebuild --mode`; see
[What a container can do to your git](#what-a-container-can-do-to-your-git). A
`--no-folder` container takes no mode. `--mode guarded` or `--mode unprotected`
on a k8s workspace is exit 2, since a pod cannot mount your folder live. A
local `create --mode unprotected` prints a warning naming
`rebuild --mode guarded`.

Extra folders are refused, exit 2, when one is given twice, lies inside another
folder or inside a guarded primary's repository, or would appear at the same
container path as another — the message names both. More than one `--folder`
on the experimental k8s provider, or alongside `--no-folder`, is exit 2 too.
`--from` never carries folders: this command's `--folder` flags decide them.

With `--from`, `--generate` is implied and `--tools` takes only `+`/`-` changes.
A source that does not exist is exit 3. A source built from a project's own
configuration, or a plain `--tools` list, is exit 2. See
[Start another container like an existing one](#start-another-container-like-an-existing-one).

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

A project-owned configuration that asks its engine for the host is refused,
exit 2, before anything is created: privileged mode (from the project or from a
feature such as `docker-in-docker`), a mount of the engine socket, a host
namespace (`--pid=host`, `--network=host`, `--ipc=host`, `--userns=host`,
`--uts=host`), a `--device`, or a bind mount of `/` or your home directory or
anything above it. For a compose project, every service in its compose files is
checked the same way. The refusal names each finding and where it came from.
`--allow-privileged` lets it through; it is the operator's decision, fixed at
create, and nothing in the project can grant it. `start` and `rebuild` check
again, so a configuration edited to ask for the host later is refused then,
while the old container still exists — recreate it with the flag if you mean
it. `capAdd` and `securityOpt` are not refused: they widen what root can do
inside the container, not the way out of it. Generated configurations are
never checked, since they hold nothing privileged. The local provider only: the
experimental k8s provider builds its own pod spec and ignores all of these.

**`list`** reads live status from the engine every time; a stored copy would be
wrong the moment anything happened outside `dev`. A `?` means the engine could
not be reached, or did not answer within `--timeout` (default `10s`, any Go
duration such as `30s` or `2m`). The timeout is per workspace, so one
unreachable cluster costs one wait, not one per container, and leaves the other
workspaces' statuses alone. Either way the list still prints and exits 0. The SOURCE column is `-` for a folderless container, and the primary
followed by `+N` for one with extra folders. CONFIG
is `generated` when `dev` rendered the configuration (`--generate` or
`--no-folder`) and `project` when the folder ships its own, STATE says
whether its agents' configuration is on a volume, and HOST says whether the
container may ask its engine for the host — `allowed` for one created with
`--allow-privileged` or before that check existed, `refused` otherwise. MODE is
`guarded` or `isolated` (see
[What a container can do to your git](#what-a-container-can-do-to-your-git)),
`legacy` for a container created before modes, and `-` for a folderless one.
A legacy container that was guarded prints a warning on every `start`, `exec`,
`shell` and `start-agent`, since its guard can lapse; recreate it in a mode.

**`start`** creates the container if the engine has none, which is what makes
`create --no-start` followed by `start` work.

**`remove`** never touches the project folder's files. For a folderless
container it also deletes the volume, matching the k8s provider deleting its
PVC. For an isolated one it first checks that nothing would be lost (see
[What a container can do to your git](#what-a-container-can-do-to-your-git)), then deletes the `NAME/*` refs `fetch` wrote. If the
engine refuses, the record is kept so the command can be retried; `--force`
drops it anyway.

**`rebuild`** recreates the container from its configuration. There is no
`devcontainer rebuild` — it is `up --remove-existing-container` underneath.
`--no-cache` rebuilds the image without the layer cache. `--tools` takes a
replacement list or `+`/`-` changes, but not both in one invocation, and applies
only to a container `dev` generated.

A rebuild keeps the volume on both providers. To start from an empty workspace,
`remove` and `create` — which is explicit about destroying the contents.

`--mode` changes the container's mode along the edges in [Changing a container's
mode](#changing-a-containers-mode); the new mode is written only once the
rebuild succeeds. `--add-folder` and `--remove-folder`, each repeatable, change
a guarded or unprotected container's extra folders. The resulting set is checked
as `create` checks it before anything changes, and the record is written only
once the rebuild succeeds. `--remove-folder` of the primary, `--add-folder` of a
folder already there, and either flag on an isolated or legacy container are
exit 2; a folder gone from the host can still be removed by the path it was
added with.

A rebuild of a project-owned container first compares its configuration with
the one the container was last built from — the merged configuration, its
Dockerfile when it builds one, and its compose files — and refuses, exit 2, when
it changed, naming the fields and files that did. `initializeCommand` runs on
your host and a Dockerfile `RUN` line runs at build, so a change you did not
make is worth reading before it runs. `--accept-config` rebuilds with it and
records it as the new baseline; the next rebuild of the same configuration needs
no flag. A container with no recorded configuration — one created before this
check, or with `--no-start` and never started — records one at its next start or
rebuild rather than refusing. Generated configurations are `dev`'s own and are
never compared. The local provider only.

**`exec`** needs the `--`; everything after it belongs to the command being run,
not to `dev`.

**`start-agent`** defaults to `--agent claude`. The agent must already be
installed in the image. Run inside a Herdr pane, it names the agent
(`herdr agent rename`) with `--agent-name`, or else after the container,
lowercased with `.` turned into `-` and cut to 32 characters; a container name
that does not start with a letter leaves the agent unnamed. `--agent-name` must
be what Herdr accepts — a lowercase letter, then up to 31 of `a-z`, `0-9`, `_`
and `-` — or it is exit 2, before anything starts. Outside a Herdr pane it does
nothing.

**`--kube-token`**, on `shell`, `exec` and `start-agent`, asks for a context,
namespace, ServiceAccount and duration, mints a token for that account on the
host, writes a kubeconfig holding it to `/tmp/dev-kube/config` in the container
and sets `KUBECONFIG` for the command. It needs a terminal — without one it is
exit 2 — and a host `kubectl`. `shell` and `exec` refuse a stopped container
before asking anything; `start-agent` asks before it starts one. See
[Query a cluster from inside a container](#query-a-cluster-from-inside-a-container).

**`--ssh-agent`** and **`--no-ssh-agent`**, on `shell`, `exec` and
`start-agent`, override the workspace's `--ssh-forward` for that one command:
the first forwards the agent even if the workspace does not, the second keeps
it out even if the workspace forwards it. With neither, the workspace decides.
Giving both is exit 2. See
[Push to git from inside a container](#push-to-git-from-inside-a-container).

**`sync`** pushes the workspace's settings into a container and never touches
your files. On k8s it refreshes the Secret the pod is built from, leaving the
running pod alone; on local it exits 0 having done nothing, because every
command already resolves the settings afresh. The container has to be running
either way.

**`fetch`** brings an isolated container's branches into the folder it was
created from, as `NAME/<branch>` remote-tracking refs — nothing local changes,
and you decide what to merge. They travel as a git bundle, so only commit data
crosses, checked as it arrives. Uncommitted work stays in the container. A
guarded or folderless container has nothing to fetch (exit 2), and a folder that
has gone is exit 1. With extra folders, each repository's branches go into that
repository, one `fetched into` line each; a folder that is not a repository is
skipped with a line saying so, and a git remote named `NAME` in any of them
refuses the fetch before anything is fetched.

**`remove`** on an isolated container first asks it what it holds: commits on
any branch your folder lacks, or uncommitted changes. Either refuses the
removal (exit 2), naming them — and, with extra folders, which folder holds
each; `fetch` and commit inside first, or `--force`. It deletes every folder's
volume.

**`tools`** lists the catalog, marking each entry official or community.
**`config show`** prints the configuration a container is built from: the
generated document, or for a project-owned container the merged one, with the
path it came from on stderr.

### worktree

```
dev worktree create NAME --branch B --path P [--repo R] [--base REF]
                         [--include-file PATH] [--no-herdr] [--workspace W]
dev worktree list [--all] [--workspace W]
dev worktree remove NAME [--force] [--workspace W]
```

`worktree` makes and removes git worktree checkouts. A container on one is made
with `dev container create` from inside the checkout, or with `--folder`.

**`create`**

| Flag | Meaning |
|---|---|
| `--repo` | repository to add the worktree to (default: the one holding the working directory) |
| `--branch` | branch to check out or create |
| `--path` | where to create the checkout |
| `--base` | start point for a new branch |
| `--include-file` | more patterns of ignored files to copy in, on top of the source's `.worktreeinclude` |
| `--no-herdr` | do not register the checkout with Herdr |

`--branch` and `--path` are required; `--path` has no default, so nothing
appears on disk somewhere you did not choose. `--base` only has an effect when
the branch does not already exist — passing it for one that does is a usage
error, not a silent no-op. A name already taken in the workspace is refused
before git runs.

Ignored files are copied in from the *source* checkout: the one holding the
working directory — from inside a linked worktree, that worktree — or the one
`--repo` names. The patterns come from its `.worktreeinclude` and from
`--include-file`:

| `.worktreeinclude` | `--include-file` | Copied |
|---|---|---|
| absent | absent | nothing |
| present | absent | what the file lists |
| absent | given | what the flag's file lists |
| present | given | both, the flag's patterns applied after the file's |

Applied after, so a `!pattern` in the flag's file drops an entry the project's
file lists, for that one create. A path is copied only when the patterns match
it, the source's own ignore rules ignore it, and the new checkout does not
track it — so a broad pattern cannot reach tracked files or `.git`, and a file
the new branch commits is never overwritten. Symlinks are copied as symlinks.
A missing or unreadable `--include-file` is a usage error before anything is
created; so is passing one from a bare repository, which has no checkout to
copy from. A copy that fails removes the checkout again. Nothing about the copy
is recorded, and `remove` needs no `--force` on its account, since git removes
ignored files with the checkout. A project that ignores its `.devcontainer/` and
lists it gets that configuration copied too, so a container made there uses it.

**A container on a worktree.** `dev container create` on a worktree checkout
asks git for the repository and bind-mounts it at its own host path, alongside
the checkout at its own. A checkout of a repository that ships its own
`.devcontainer` keeps it untouched — `dev` never writes into a project's folder
— and those mounts are merged into the document at invocation time instead, the
same way the agent-state mount is for any other project-owned container. It
needs a local provider.

**`list`** reads the checkouts git still knows about, live, every time. The
CHECKOUT column reads `ok` or `gone`; `gone` means the checkout was removed
outside `dev`, and `dev worktree remove` still drops the record. CONTAINERS
names the containers whose folder is the checkout, or `-`.

**`remove`** refuses while a container is on the checkout, naming it: remove it
first with `dev container remove`. Then it asks git to remove the checkout, then
drops the record. git refuses a checkout holding modified or untracked files;
that refusal leaves the record intact so the command is retryable, and `--force`
passes through to git. The branch is never deleted — the checkout is
scaffolding, the branch is the work.

`dev container remove` on a container on a worktree removes the container and
leaves the checkout and its record. `dev workspace remove` refuses while the
workspace holds worktrees, as it does for containers.

### audit

```
dev audit [--workspace W] [--container C] [--event E]... [--since D] [--json]
```

Prints the audit log oldest first, one record per line: time, workspace and
container, event, and a summary. See
[See what ran, and when](#see-what-ran-and-when).

| Flag | |
|---|---|
| `--workspace W` | only records for this workspace. Does **not** default to the active workspace: the log is the view across everything |
| `--container C` | only records for this container |
| `--event E` | only these events; repeatable or comma-separated |
| `--since D` | a duration back from now (`24h`, `30m`) or an RFC 3339 time |
| `--json` | the matching records as raw JSON lines, for `jq` |

Events: `create`, `rebuild`, `start`, `stop`, `remove`, `exec`, `shell`,
`start-agent`, `kube-token`, `relay`, `sync`, `refused` for a command a guard
stopped (with the reason — `escape`, `drift` or `git` — and what it found), and `accept-config` for a rebuild
told to take a changed configuration (with what changed).

Names are not looked up: a workspace or container that no longer exists still
has history, so an unknown name prints nothing and exits `0` rather than `3`.
No log yet also prints nothing. A damaged line is skipped with a warning naming
its line number. `--since` that is neither form exits `2`.

The file is `audit.jsonl` in the state directory, mode `0600`. A record that
cannot be written warns once and never fails the command it belongs to.

### Environment

| Variable | Meaning |
|---|---|
| `DEV_STATE` | the state directory: the SQLite database `dev.db` and the audit log `audit.jsonl`; default `~/.local/state/dev/` |
| `DOCKER_HOST` | read by `docker`, which `dev` shells out to; how you point at Podman |
| `SSH_AUTH_SOCK` | your ssh agent, read on the host when a workspace uses `--ssh-forward`. Inside the container it names the relay instead |
| `KUBECONFIG` | set inside the container, for a command run with `--kube-token`, to the kubeconfig holding the minted token |
| `DEV_SMOKE_K8S_CONTEXT` | turns on the Kubernetes half of `make smoke` |
| `DEV_SMOKE_REGISTRY` | the other half of that switch; both must be set |
| `DEV_SMOKE_K8S_NAMESPACE` | optional, default `default` |
| `DEV_SMOKE_K8S_PULL_SECRET` | optional; only matters when the pushed image is private |
