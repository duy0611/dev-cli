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

## Install

```sh
./install.sh          # symlink bin/* into ~/.local/bin
./install.sh --images # build the four images (slow; base is ~1.7 GB)
./install.sh --watch  # load the expiry watcher via launchd
./install.sh --all    # all three
```

An existing non-symlink `~/.local/bin/dcx` is moved to `dcx.pre-dcx-repo`
rather than overwritten.

Requires `docker` (pointed at Podman via `DOCKER_HOST`), the `devcontainer`
CLI, and `jq`. `fzf` is used for the pickers when present; without it they fall
back to a numbered menu.

## Profiles

| Profile | Tooling | Credentials |
|---|---|---|
| `base` | Claude Code, git, gh | none |
| `k8s`  | kubectl, helm, k9s, aws | minted k8s SA token, AWS session |
| `gcp`  | Google Cloud SDK | impersonated SA access token |
| `full` | both | both |

```sh
dcx -p base --as scratch                    # sandbox, no cloud access
dcx -p k8s  --as sk8s-debug                 # pick cluster/namespace/SA, then shell
dcclaude -p k8s --as sk8s-debug             # Claude in that sandbox
dcws -p k8s --as sk8s-debug                 # ...as a Herdr workspace
dcx -p k8s  --as sk8s-debug -- kubectl get pods
dcx --list
dcx --rm scratch
```

Scope is picked **once**, when the instance is created, and reused on every
later launch. Re-pick with `dccred pick NAME`.

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

`--gpg` is **opt-in and unproven**. Signing needs the host `gpg-agent` socket
forwarded; VS Code does that, plain `devcontainer exec` does not, and virtiofs
cannot pass a unix socket through the Podman VM. Expect `No secret key` on the
`dcx` path and commit unsigned, signing on the host afterwards.

## What the sandbox protects

| Protected | Not protected |
|---|---|
| Host filesystem outside the project | Project files (bind-mounted read-write) |
| Host `~/.kube`, `~/.config/gcloud`, `~/.aws` | LiteLLM token, or OAuth cred with `--auth oauth` |
| Host shell and processes | Network egress (open, by design) |
| Other instances' state | |

## Design

See [docs/design.md](docs/design.md) for the full rationale, the container
invariants, and the verification plan.
