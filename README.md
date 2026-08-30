# devcontainer-claude-setup

Run Claude Code inside containers on macOS + Podman.

Two ways in, one command family:

- A project that ships a `.devcontainer/` is used as-is — the original `dcx`
  behaviour, unchanged.
- A project that doesn't gets a **prebuilt sandbox image** chosen by profile,
  with short-lived, narrowly scoped cloud credentials minted on the host.

```
dcx        shell or command inside a container
dcclaude   Claude Code inside a container, visible to Herdr
dcws       Herdr workspace with a shell pane and a Claude pane
dccred     credential lifecycle: mint, refresh, status, watch
```

## Prerequisites

| Requirement | Why | Needed by |
|---|---|---|
| [Podman](https://podman.io) (or Docker) | Builds and runs the images. Podman is the default; `DOCKER_HOST` points the docker CLI at its socket. | everything |
| [`devcontainer` CLI](https://github.com/devcontainers/cli) | `dcx` shells out to `devcontainer up` / `devcontainer exec` — it is the whole run path. | everything |
| [Herdr](https://github.com/herdrdev/herdr) | Workspace/pane orchestration, git worktree checkouts, and the expiry notifications. | `dcws` (hard, including `--worktree`), `dccred watch` (notifications only) |
| `jq` | JSON everywhere: `devcontainer.json` rendering, `instance.json`, credential parsing. | everything |
| [`fzf`](https://github.com/junegunn/fzf) | Nicer pickers. Optional — without it they fall back to a numbered menu. | optional |

```sh
brew install podman jq fzf
npm install -g @devcontainers/cli
podman machine init && podman machine start
```

`dcws` refuses to run without a reachable Herdr server (`no Herdr server
reachable - run 'herdr' first`). `dcclaude` only sets `HERDR_AGENT=claude` so
Herdr classifies the pane, and works fine without Herdr installed.

## Install

```sh
./install.sh          # symlink bin/* into ~/.local/bin
./install.sh --images # build the four images (slow; base is ~1.7 GB)
./install.sh --watch  # load the expiry watcher via launchd
./install.sh --all    # all three
```

An existing non-symlink `~/.local/bin/dcx` is moved to `dcx.pre-dcx-repo`
rather than overwritten.

Image *builds* prefer `podman` when it is on PATH and fall back to `docker`,
because the docker CLI drops to its deprecated classic builder without the
buildx plugin. Both write to the same local storage. Set `DCX_RUNTIME=docker`
to force the old behaviour; it applies to `make build` and `install.sh
--images` alike. Running instances still go through `docker`, since that is
what the `devcontainer` CLI speaks.

## Profiles

| Profile | Tooling | Credentials |
|---|---|---|
| `base` | Claude Code, git, gh | none |
| `k8s`  | kubectl, helm, k9s | minted k8s SA token |
| `cloud`  | Google Cloud SDK, aws CLI | impersonated GCP SA access token, assumed AWS role |
| `full` | both | both |

Both provider credentials sit in `cloud`. `k8s` deliberately carries none:
cluster auth is a minted ServiceAccount token, so nothing in that image reads a
GCP or AWS credential. Want a cluster *and* a cloud provider? That is `full`.

```sh
dcx -p base  --as scratch                   # sandbox, no cloud access
dcx -p k8s   --as sk8s-debug                # pick cluster/namespace/SA, then shell
dcx -p cloud --as gcp-debug                 # pick GCP project/SA and AWS role
dcclaude -p k8s --as sk8s-debug             # Claude in that sandbox
dcws -p k8s --as sk8s-debug                 # ...as a Herdr workspace
dcws --worktree feat/thing                  # ...on a Herdr worktree of feat/thing
dcws --worktree feat/thing --base main      # ...branched from main rather than HEAD
dcws --rm-worktree feat/thing               # instance first, then the checkout
dcx -p k8s  --as sk8s-debug -- kubectl get pods
dcx --list
dcx --rm scratch
```

Scope is picked **once**, when the instance is created, and reused on every
later launch. Re-pick with `dccred pick NAME`.

Worktree instances mirror host absolute paths into the container rather than
mounting at `/workspace`, and co-mount the main repo. That is what lets one
checkout be driven from a host session and a container session interchangeably:
`git worktree list` prints the same thing on both sides, so neither prunes the
other's checkout. Normal instances are unaffected. `dcx` detects a linked
worktree on its own, so this also covers checkouts made by hand or from the
Herdr sidebar — not only ones `dcws --worktree` created.

**Run `--worktree` from the main repo, not from inside a checkout.** It names
the instance `<basename FOLDER>-<branch>`, and FOLDER defaults to `$PWD` — from
the repo root that is the repo name, from inside a checkout it is the
checkout's own directory name. Pass the repo explicitly
(`dcws --worktree feat/thing /path/to/repo`) or `--as` the instance name if you
need to run it from elsewhere.

`--rm-worktree` has no such constraint: it finds the instance by the checkout
path recorded in `instance.json`, so it works from anywhere in the repo or any
of its worktrees. It also reports what it removed — if no instance matched, it
says so instead of claiming success, which is how orphaned containers and
volumes used to go unnoticed.

## Instances

An instance is the unit of isolation: its own Claude config volume, its own
shell history, its own credentials. Name them with `--as`; the default is the
folder's basename.

Two instances can point at the same project and run side by side. That works
because an instance's state directory — not the project — is what gets handed
to `devcontainer up`, which makes the `devcontainer.local_folder` label unique
per instance.

State lives in `~/.local/state/dcx/instances/<name>/`.

## Credentials

Cloud credentials are **minted**, never copied from your host.

- **Kubernetes** — `kubectl create token` against a ServiceAccount you pick.
  It is a Kubernetes API call, so the same path serves EKS and GKE. The
  generated kubeconfig carries a bare token and **no `exec` credential
  plugin**, so the container needs neither `aws eks get-token` nor
  `gke-gcloud-auth-plugin`.
- **GCP** — `gcloud auth print-access-token --impersonate-service-account`.
  Capped at 1h. Declining to pick a service account falls back to your own
  identity: short-lived, but *not* privilege-reduced. The picker says so.
- **AWS** — `sts assume-role`, written as an ini file.

Every credential is read from a **file path**, never a baked-in env value, so a
refresh on the host lands without restarting the container:

| Tool | Reads |
|---|---|
| kubectl | `users[].user.tokenFile` |
| gcloud | `CLOUDSDK_AUTH_ACCESS_TOKEN_FILE` |
| aws | `AWS_SHARED_CREDENTIALS_FILE` |

### When a token expires

Nothing auto-refreshes: the container holds no credential capable of minting
another, which is the point.

```
$ kubectl get pods
dcx: k8s token expired 3m ago for instance sk8s-debug.
     Run on the HOST:  dccred refresh sk8s-debug
```

`dccred watch` (one process for all instances, loaded by `install.sh --watch`)
fires a Herdr notification on the transition, so you see it even when the pane
isn't focused. `dccred status` shows time remaining.

A refresh does not restart the container or interrupt a Claude session.

## Claude auth

`--auth litellm` (default) puts `ANTHROPIC_BASE_URL` and a token from the macOS
Keychain (`ANTHROPIC_AUTH_TOKEN`) into the container. The token is re-read on
every launch via `initializeCommand`, so it can't go stale.

`--auth oauth` instead copies your Claude Code OAuth credential from the
Keychain into the instance volume. Convenient, but every instance then holds a
copy of your real token.

## Plugins

Baked into the image at build time from `images/base/claude-plugins.txt`.
Container start is instant and needs no network. Change the set by editing the
manifest and rebuilding.

They cannot simply be copied in: Claude's marketplace cache stores absolute
paths, so a plugin tree installed anywhere but its final home fails with
`Failed to load marketplace: cache-miss`. The build installs them at
`/home/node/.claude`, moves the tree to `/opt/claude-seed` so the volume mount
cannot shadow it, and `dcx-post-create` copies it back to the same path.

**Public marketplaces only.** GHE sources need an SSH agent or `gh auth login`,
neither of which exists during a build.

## Git

Identity and `safe.directory` are always set up, so commits work out of the box.
`--gitconfig` additionally snapshots your `~/.gitconfig` (rewriting the
Homebrew-path `gh` credential helpers).

`--sign` signs commits made inside the container, using **SSH signing** with a
dedicated sandbox key rather than your GPG key. One-time setup:

```sh
dccred signing-key                          # generates ~/.local/state/dcx/signing/
gh ssh-key add --type signing --title dcx-sandbox \
  ~/.local/state/dcx/signing/sandbox-signing.pub
dcx -p base --as scratch --sign
```

The key is staged into the instance's `creds/` dir and reached at
`/run/dcx-creds/sandbox-signing`, exactly like the cloud credentials, so a
rotation (`dccred signing-key --rotate`) lands without recreating anything.

**Why not GPG.** Signing with your real key needs the host `gpg-agent` socket
forwarded into the container. A devcontainer mount path is resolved inside the
Podman VM, and virtiofs cannot carry a unix socket; every workaround (a socat
TCP relay, `ssh -R` into the machine) is tied to a local hypervisor and would
have to be rebuilt for a remote container host. A key file behind a fixed path
is the only design that ports. `--gpg` is kept as a deprecated alias for
`--sign`; the old behaviour only ever exported the *public* key, so it could
never sign at all.

**Treat the key as sandbox-only.** It sits inside a container with open egress
running Claude with `--dangerously-skip-permissions`, so assume it can leak.
Register it as a **signing** key and never as an authentication key: the loss is
then forged commit signatures — recoverable by deleting one key from the forge —
and not repository write access or SSH login. It carries a 90-day rotation
sidecar (`DCX_SIGNING_TTL_DAYS`), which `dccred signing-key` reports.

Without `--sign`, `commit.gpgsign` is forced **off** inside the container. Your
host almost certainly signs by default, and inheriting that setting made every
commit in every sandbox fail with `No secret key`.

## What the sandbox protects

| Protected | Not protected |
|---|---|
| Host filesystem outside the project | Project files (bind-mounted read-write) |
| Host `~/.kube`, `~/.config/gcloud`, `~/.aws` | LiteLLM token, or OAuth cred with `--auth oauth` |
| Host shell and processes | Network egress (open, by design) |
| Other instances' state | |

## Skills

`skills/devcontainer-init/` is a Claude Code skill that writes a
`.devcontainer/` for a Python, Node.js, or Go project — `devcontainer.json`,
an `initializeCommand`, a `postCreateCommand`, and an editable copy of the
Claude plugin manifest. Ask Claude to "add a devcontainer to this project".

Its output is deliberately **not** built on this repo's images. It targets
`mcr.microsoft.com/devcontainers/base:ubuntu` with devcontainer Features, so it
works for someone who has never run `make build`, and it runs under `dcx`
project mode, a bare `devcontainer up`, or VS Code alike. Plugins install on
first container create rather than being baked, which trades instant start for
that portability. Cloud credentials stay out of scope — that remains
`dcx -p k8s --as NAME`.

It refuses to touch a project that already has a devcontainer.

## Development

```sh
make              # help
make lint         # shellcheck, yamllint, plutil, render check, skills, hadolint
make test         # smoke-test the base profile end to end
make build        # all four images
make build k8s    # one image (base | k8s | cloud | full); deps built first
make k8s          # same, without the `build` word
make install      # ./install.sh
make clean        # remove the smoke test's instance only
```

Image targets encode the real dependencies — `k8s` and `cloud` are built `FROM
base`, `full` `FROM k8s` — so `make build cloud` rebuilds `base` first. That is a
few seconds when nothing changed, and it stops a stale base from silently
persisting into a derived image.

`make lint` renders a `devcontainer.json` for every profile into a throwaway
directory and validates it, rather than trusting the `jq` in `lib/render.sh` by
eye. `make test` creates a real instance and asserts the things that have
actually broken here: plugins loading from the seed without a cache-miss, the
workspace bind reaching the host in both directions, no locale warning, and the
container label being the state dir.

hadolint runs from its own image and is skipped, not failed, when it cannot be
fetched. Suppressed rules are listed with reasons at the top of `lint-docker`.

## Design

See [docs/design.md](docs/design.md) for the full rationale, the container
invariants, and the verification plan.
