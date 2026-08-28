# devcontainer-claude-setup — profile sandboxes for the dc* toolchain

Repo: `https://git.supermetrics.com/duy-nguyen/devcontainer-claude-setup`
Clone to: `~/Projects/Github/devcontainer-claude-setup`

## Context

`dcx`, `dcclaude`, and `dcws` already run Claude Code inside a project's
devcontainer and surface it as a Herdr workspace. They only work when the project
ships a `.devcontainer/` — `dcx` dies with `no .devcontainer found above $PWD`
otherwise. Most folders don't ship one, so most Claude sessions still run on the
host with full access to the filesystem and to every credential in `~/.kube`,
`~/.config/gcloud`, and `~/.aws`.

This adds a **profile** mode: when no `.devcontainer` is found, launch a prebuilt
sandbox image instead of failing. Cloud-capable profiles get **freshly minted,
short-lived, narrowly scoped** credentials — never a copy of the host's.

`~/Projects/Github/my-home-lab/.devcontainer/` is the reference implementation for
everything container-side: plugin installation, volume ownership, git identity,
LiteLLM auth. Its hard-won details are ported rather than rediscovered.

The three scripts are currently unversioned loose files in `~/.local/bin`. The repo
takes ownership of them, and the profile layer sits alongside sharing their code.

### What the sandbox protects

| Protected | Not protected |
|---|---|
| Host filesystem outside the project | Project files (bind-mounted read-write) |
| Host `~/.kube`, `~/.config/gcloud`, `~/.aws` | LiteLLM token (or OAuth cred, if opted in) |
| Host shell and processes | Network egress (open, by choice) |
| Other instances' state (per-instance volumes) | |

## Scope

**P1 — this plan.** Repo takeover, profile mode in `dcx`, four images with baked
plugins, `dccred`, Herdr wiring. Ships one hand-applied stub RBAC ServiceAccount on
a stage cluster, enough to demonstrate mint → inject → expire → refresh.

**P2 — deferred, own brainstorm.** Real RBAC tiering per cluster, gitops-managed,
prod coverage.

## Decisions

| Area | Decision |
|---|---|
| CLI shape | Extend `dcx`/`dcclaude`/`dcws` with `-p/--profile` + `--as`; new `dccred` for the credential lifecycle |
| Repo scope | Owns all four commands; `install.sh` symlinks into `~/.local/bin` |
| Engine | `DOCKER_HOST` already points `docker` at the podman socket — **no `--docker-path` needed**; keep using `docker`/`devcontainer` exactly as `dcx` does today |
| Profiles | `base`, `k8s`, `gcp`, `full` |
| Plugins | Baked into the image at build. No picker, no per-instance variation |
| Claude auth | LiteLLM by default; `--auth oauth` opts into seeding the OAuth credential |
| Git | Identity + `safe.directory` always; `--gitconfig` and `--gpg` opt in |
| Scope selection | Interactive fzf picker on first launch of an instance; recorded and reused after |
| Instance identity | Explicit name via `--as`, defaulting to `basename $PWD` |
| Persistence | Named volumes per instance; survive restart, isolated between instances |
| Workspace | Bind-mount read-write at `/workspace` |
| Egress | Open. No firewall |
| k8s creds | Minted SA tokens — one code path for EKS and GKE |
| GCP creds | Impersonated SA access token (1h hard cap) |
| AWS creds | `sts assume-role` session credentials |
| Expiry | Blocking shim message + `herdr notification show`; refresh is manual from the host |

### Why minted SA tokens

`kubectl create token` is a Kubernetes API call, so one code path serves `aws-cen-*`
(EKS) and `gcp-cen-*` (GKE). Once minted, the container's kubeconfig carries a bare
token and needs **no `exec` credential plugin** — no `aws eks get-token`, no
`gke-gcloud-auth-plugin`. The `aws` and `gcloud` CLIs are in the images for direct
use, not for cluster auth.

## Container invariants

Ported from `my-home-lab`. Each of these was expensive to discover; violating any of
them produces a confusing failure a long way from its cause.

1. **Never mount the host `~/.claude`.** Its marketplace cache is keyed to host
   paths, so the container fails with `Failed to load marketplace: cache-miss`.
   Plugins are installed, never copied from the host.
2. **Take ownership of the volume mount before touching it.** A named volume whose
   mount point doesn't exist in the image is created `root:root`. The mount then
   shadows whatever the image put there, `$CLAUDE_CONFIG_DIR` is unwritable, and
   every `claude plugin` call fails identically. `sudo chown` first, non-recursive,
   only on first create.
3. **`remoteUser` must be non-root.** Claude Code refuses
   `--dangerously-skip-permissions` as root, and a root toolchain leaves root-owned
   files in the bind-mounted repo on the host.
4. **`git config --global --add safe.directory` after any gitconfig copy.** The
   bind-mounted repo fails git's ownership check; every git command, commit
   included, dies with `detected dubious ownership`. A gitconfig copy overwrites the
   setting, so ordering matters.
5. **`claude plugin install -y`** — stdin is never a TTY in these contexts.

## Repository layout

```
devcontainer-claude-setup/
  install.sh                   # symlink bin/* into ~/.local/bin, load launchd plist
  bin/
    dcx                        # existing + profile mode
    dcclaude                   # existing + flag passthrough
    dcws                       # existing + profile mode
    dccred                     # new: mint | refresh | status | pick | env | watch
  lib/
    common.sh                  # die/usage, symlink-resolving lib loader, state paths
    instance.sh                # state dir, instance.json, volume lifecycle
    pick.sh                    # fzf pickers: profile / context / namespace / project / SA / role
    mint.sh                    # mint_k8s, mint_gcp, mint_aws
    env.sh                     # devcontainer.env: LiteLLM token, git identity
    render.sh                  # devcontainer.json generation
    seed.sh                    # OAuth credential seeding (--auth oauth only)
  images/
    base/Containerfile
    base/claude-plugins.txt
    k8s/Containerfile
    k8s/claude-plugins.txt
    gcp/Containerfile
    gcp/claude-plugins.txt
    full/Containerfile
    full/claude-plugins.txt
    shared/
      install-plugins.sh       # build-time; consumes claude-plugins.txt
      post-create.sh           # runtime; seed volume, git setup
      dcx-shim                 # argv[0]-dispatching wrapper for kubectl/gcloud/aws
      dcx-credcheck            # expiry reader
  rbac/stub-readonly.yaml
  launchd/dev.dcx.dccred-watch.plist
  README.md
```

Scripts are symlinked into `~/.local/bin`, so `lib/` resolution must walk the
symlink — macOS ships bash 3.2 and `readlink -f` is not dependable. `common.sh`
gets a small resolve loop. Keep the existing bash-3.2 accommodations (the
`${arr[@]+"${arr[@]}"}` guard in `dcclaude`).

## Changes to `dcx`

The insight that keeps this small: `dcx` already computes one variable, `$folder`,
and everything after it — the `docker ps` liveness check, `devcontainer up`,
`devcontainer exec` — is generic. Profile mode only changes **how `$folder` is
derived**.

- **Project mode** (today): `$folder` = the repo holding `.devcontainer/`.
- **Profile mode** (new): `$folder` = the instance's state dir, which holds a
  generated `.devcontainer/devcontainer.json` whose `workspaceMount` points at the
  real project path.

Because the state dir is unique per instance, `devcontainer.local_folder` is unique
per instance too, so `dcx`'s existing liveness filter distinguishes instances with
no change. Two instances can target the same project without colliding.

New flags, threaded identically through `dcclaude` and `dcws`:

```
-p, --profile PROFILE    base | k8s | gcp | full
    --as NAME            instance name (default: basename of the workspace folder)
    --auth MODE          litellm (default) | oauth
    --gitconfig          snapshot the host ~/.gitconfig into the instance
    --gpg                attempt GPG signing setup (see caveat below)
```

Resolution order becomes:

1. `-p` given → profile mode.
2. No `-p`, `.devcontainer` found above `$PWD` → project mode, unchanged.
3. Neither → fzf profile picker instead of today's `die`. Cancelling still exits 1.

On a **new** instance, profile mode runs the scope pickers, mints, renders the
config. On an **existing** instance it reuses the recorded selections and skips
straight to `devcontainer up`. The `--auth`/`--gitconfig`/`--gpg` choices are
recorded in `instance.json` at create and need not be repeated.

## State

```
~/.local/state/dcx/instances/<name>/
  instance.json                     # profile, workspace path, selections, flags, mint times
  devcontainer.env                  # 0600; LiteLLM token + git identity
  .devcontainer/devcontainer.json   # generated
  gitconfig-host                    # only with --gitconfig
  gpg-public-key.asc                # only with --gpg
  creds/                            # 0700
    kubeconfig.yaml
    k8s.token         k8s.expiry
    gcp.token         gcp.expiry
    aws-credentials   aws.expiry
```

Volumes: `dcx-claude-<name>` → `/home/node/.claude`, `dcx-history-<name>` →
`/commandhistory`.

## Images and plugin baking

`images/base/Containerfile` forks
`~/Projects/Github/claude-code/.devcontainer/Dockerfile` — reuse its base image
(bumped from `node:20` to `node:24-trixie`, since Node 20 went EOL on
2026-04-30), `node` user, zsh/powerlevel10k, `git-delta`, `gh`,
`/commandhistory` persistence.
**Drop** `init-firewall.sh`, `NET_ADMIN`, `NET_RAW`, and the `iptables`/`ipset`
packages; egress is open by decision.

| Image | Adds |
|---|---|
| `localhost/dcx-base` | (nothing beyond the fork) |
| `localhost/dcx-k8s` | `kubectl`, `helm`, `k9s`, `awscli` v2, shims |
| `localhost/dcx-gcp` | Google Cloud SDK, shims |
| `localhost/dcx-full` | both toolchains, shims |

Real binaries move to `/usr/local/bin/real/`; `/usr/local/bin/{kubectl,gcloud,aws}`
become symlinks to `dcx-shim`.

### Baking plugins past the volume shadow

Plugins must be installed (invariant 1) and `$CLAUDE_CONFIG_DIR` is a volume mount
that shadows whatever the image left there. The way through is to install at the
**final** path, then stage the result aside:

```dockerfile
# build time, as the node user
ENV CLAUDE_CONFIG_DIR=/home/node/.claude
COPY claude-plugins.txt /tmp/
RUN install-plugins.sh /tmp/claude-plugins.txt \
 && mv /home/node/.claude /opt/claude-seed \
 && mkdir -p /home/node/.claude
```

Installing at `/home/node/.claude` and copying back to the same path keeps the
path-keyed marketplace cache valid. `post-create.sh` then, on first create only:

```bash
[ -w "$config_dir" ] || sudo chown "$(id -un):$(id -gn)" "$config_dir"   # invariant 2
[ -e "$config_dir/plugins" ] || cp -a /opt/claude-seed/. "$config_dir"/
```

Result: container start is instant and needs no network. Changing the plugin set
means editing `images/<profile>/claude-plugins.txt` and rebuilding, which is the
accepted trade.

`install-plugins.sh` reuses the manifest format and the loop from
`my-home-lab/.devcontainer/post-create.sh` — `marketplace <name> <src>` /
`plugin <name>@<marketplace>`, `-y` on install, one failure doesn't stop the rest.
Difference: at build time it should **fail the build** on error rather than warn,
since a half-populated image is worse than no image.

**GHE marketplaces cannot be baked.** They need an SSH agent or `gh auth login`,
neither of which exists during `docker build`. The manifests carry public
marketplaces only; anything from GHE stays a manual `claude plugin install` inside a
running container.

## Claude auth

**LiteLLM (default).** Matches `my-home-lab`. `dccred env <name>` reads the token
from the macOS Keychain (`security find-generic-password -a "$USER" -s
ANTHROPIC_AUTH_TOKEN -w`) and writes `devcontainer.env` mode `0600`. The generated
config wires it in via `initializeCommand` and `runArgs: --env-file`, so the token
refreshes on every launch and works whether started by `dcx` or opened in VS Code.

Values are written unquoted — `--env-file` treats quotes as literal characters. An
absent token is omitted entirely rather than written empty, because an empty
`ANTHROPIC_AUTH_TOKEN` sends a literal `Bearer ` header and fails more confusingly
than having none.

**OAuth (`--auth oauth`).** Claude Code stores its credential in the macOS Keychain
under `Claude Code-credentials`, not a file, so a `~/.claude` copy would miss it. On
volume create only: read it, write `credentials.json` mode `0600` into the state
dir, one-shot `docker run` copies it into the volume as
`/home/node/.claude/.credentials.json` owned `node:node` mode `600`, then delete the
staged file. The container is Linux and uses the file-based store, so the value
round-trips. Skips `ANTHROPIC_BASE_URL` entirely.

## Git setup

**Always.** `dccred env` passes `DEVCONTAINER_GIT_NAME` / `_EMAIL` through from
`git config --get`; `post-create.sh` applies them and runs
`git config --global --add safe.directory "$PWD"`. Without the identity, the first
commit dies with `Author identity unknown`; without `safe.directory`, every git
command dies with `dubious ownership`.

**`--gitconfig`.** Snapshots host `~/.gitconfig` into the state dir; post-create
copies it to `$HOME/.gitconfig` and rewrites `!/opt/homebrew/bin/gh ` to `!gh ` so
the credential helpers resolve. Copied, not mounted, so the rewrite never touches
the host file. Must run **before** the `safe.directory` line.

**`--gpg` — opt-in and unproven.** In `my-home-lab` this works because **VS Code
forwards the host `gpg-agent` socket**. `dcx` uses plain `devcontainer exec`, where
nothing forwards it, and the host socket cannot be bind-mounted through the podman
VM — virtiofs does not pass unix sockets. So the public-key import and
`gpgconf --kill gpg-agent` steps port cleanly, but the signing path itself probably
does not.

Build it, then **verify it early** (step 6). If signing fails, drop the flag rather
than carrying dead code: commit from the host, or commit unsigned from the sandbox
and sign on the host afterwards.

## Generated devcontainer.json

```json
{
  "name": "dcx-<name>",
  "image": "localhost/dcx-<profile>:latest",
  "workspaceMount": "source=<abs-project-path>,target=/workspace,type=bind,consistency=delegated",
  "workspaceFolder": "/workspace",
  "remoteUser": "node",
  "initializeCommand": "dccred env <name>",
  "runArgs": ["--env-file", "<state>/instances/<name>/devcontainer.env"],
  "mounts": [
    "source=dcx-claude-<name>,target=/home/node/.claude,type=volume",
    "source=dcx-history-<name>,target=/commandhistory,type=volume",
    "source=<state>/instances/<name>/creds,target=/run/dcx-creds,type=bind,readonly"
  ],
  "containerEnv": {
    "CLAUDE_CONFIG_DIR": "/home/node/.claude",
    "DCX_INSTANCE": "<name>",
    "ANTHROPIC_BASE_URL": "https://litellm.litellm-superidp.supermetrics.dev",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "claude-opus-5",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "claude-sonnet-5",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "claude-haiku-4-5",
    "CLAUDE_CODE_SUBAGENT_MODEL": "claude-haiku-4-5",
    "CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS": "1",
    "KUBECONFIG": "/run/dcx-creds/kubeconfig.yaml",
    "CLOUDSDK_AUTH_ACCESS_TOKEN_FILE": "/run/dcx-creds/gcp.token",
    "CLOUDSDK_CORE_PROJECT": "<project>",
    "AWS_SHARED_CREDENTIALS_FILE": "/run/dcx-creds/aws-credentials",
    "AWS_REGION": "eu-west-1"
  },
  "postCreateCommand": "/usr/local/bin/dcx-post-create"
}
```

Cloud env vars are emitted only for profiles that use them; the `ANTHROPIC_*` block
only under `--auth litellm`.

Every credential is reached through a **file path**, never a baked-in env value.
`kubectl` reads `users[].user.tokenFile`, `gcloud` reads
`CLOUDSDK_AUTH_ACCESS_TOKEN_FILE`, `aws` reads `AWS_SHARED_CREDENTIALS_FILE`. All
three re-read per invocation, so a host-side rewrite lands with no restart and no
signal.

## `dccred`

Credential handling lives outside the launch path so it is independently runnable
and testable.

| Command | Does |
|---|---|
| `dccred mint <name>` | Mint all cloud credentials for the recorded selections |
| `dccred refresh <name>` | Alias for `mint` — what the shim tells you to run |
| `dccred env <name>` | Regenerate `devcontainer.env` (LiteLLM token, git identity). Run by `initializeCommand` |
| `dccred status [name]` | Table of instances, profiles, and TTL remaining |
| `dccred pick <name>` | Re-run the scope pickers, update selections, re-mint |
| `dccred watch` | Poll all instances every 60s, fire a Herdr notification on staleness |

Minting runs on the host against the operator's existing sessions. Each writes a
value file plus a sibling `<name>.expiry` holding a unix timestamp, mode `0600` in a
`0700` directory, written to a temp file and `mv`'d into place so a container reading
mid-refresh never sees a partial token.

**k8s** — `kubectl --context <ctx> create token <sa> -n <ns> --duration=1h`.
Kubeconfig from `kubectl config view --raw --minify --context <ctx>`, keeping the
cluster block (`server`, `certificate-authority-data`) and replacing the user block
entirely with `tokenFile: /run/dcx-creds/k8s.token`. That replacement is what strips
the `exec` plugin.

**GCP** — `gcloud auth print-access-token --impersonate-service-account=<sa>`. 1h hard
cap; 12h only with `constraints/iam.allowServiceAccountCredentialLifetimeExtension`,
assumed unset at Supermetrics. With no SA selected it falls back to the operator's own
token — still short-lived, but **not** privilege-reduced, and the picker says so at
selection time.

**AWS** — `aws sts assume-role --role-arn <role> --role-session-name dcx-<name>
--duration-seconds 3600`, rendered as a `[default]` ini block.

When minting fails, `dccred` surfaces the underlying `gcloud`/`aws`/`kubectl` error
verbatim rather than a wrapper message. This failure mode is real — see prerequisites.

## Expiry UX

No per-container daemon.

1. A command runs in the container. The shim reads the matching `.expiry` first.
2. Live → `exec /usr/local/bin/real/<tool> "$@"`. Zero friction.
3. Stale → print and `exit 1`:

```
dcx: k8s token for instance 'sk8s-debug' expired 3m ago.
     Run on the host:  dccred refresh sk8s-debug
```

4. `dccred watch` — **one** process for all instances, not one per container — polls
   every 60s and fires on the transition to stale:

```
herdr notification show "dcx: k8s token expired" \
  --body "instance sk8s-debug — run: dccred refresh sk8s-debug" \
  --sound request --position top-right
```

   So it reaches you even when the pane isn't focused. Loaded via
   `launchd/dev.dcx.dccred-watch.plist` with `KeepAlive`.
5. `dccred refresh <name>` re-mints and atomically replaces the files. The next
   invocation works. The container is never restarted and the Claude session is
   never lost.

## Herdr wiring

`dcclaude` already exports `HERDR_AGENT=claude` so Herdr classifies the pane behind
the `devcontainer exec` wrapper. Profile mode changes nothing there — it only passes
the new flags through to `dcx`.

`dcws` gains the same flags. In profile mode it skips the
`[ -f "$folder/.devcontainer/devcontainer.json" ]` precondition, defaults the
workspace label to the instance name rather than the folder basename, and labels
panes by instance so several sandboxes on one project stay distinguishable:

| Pane | Runs |
|---|---|
| `shell:<name>` | `dcx -p <profile> --as <name>` |
| `claude:<name>` | `dcclaude -p <profile> --as <name>` |

The existing workspace-exists / `-r` relaunch logic and `pane_busy` check carry over
untouched.

## P1 stub RBAC

`rbac/stub-readonly.yaml` — one `ServiceAccount` plus a `ClusterRoleBinding` to the
built-in `view` role, applied **by hand** to `gcp-cen-st-sk8s` (stage). Its only job
is to make the mint → inject → expire → refresh loop demonstrable. Not
gitops-managed, not prod, superseded wholesale by P2.

## Prerequisites

**Host `gcloud` needs reauth before any of this works.** A probe during design failed
with `(gcloud.auth.docker-helper) Reauthentication failed`. Run `gcloud auth login`
first — it blocks GCP minting and, because `~/.docker/config.json` wires `gcloud` as
a credential helper for `*.gcr.io` and `europe-docker.pkg.dev`, it can also break
image pulls.

## Verification

Steps 2 and 6 are gates: both can invalidate design decisions, so run them before
building past them.

1. **Repo takeover is a no-op** — run `install.sh`, then `dcx` in
   `~/Projects/DevOps/sm-secret-cli-go`. Behaves exactly as before; the two
   containers already running (`stoic_chebyshev`, `competent_hellman`) are reused,
   not rebuilt.
2. **Gate — label assumption** — `docker inspect` a profile-mode container and
   confirm `devcontainer.local_folder` equals the **state dir**, not the project
   path. All instance isolation rests on this.
3. **Build** — `install.sh --images`; `docker images | grep dcx-` shows four.
4. **Plugins baked** — `dcx -p base --as scratch`, then `claude plugin list` inside.
   Shows the manifest's plugins. Time the create: it should not hit the network.
   `ls /opt/claude-seed` still present in the image.
5. **LiteLLM auth** — Claude starts in that container and answers a prompt. Confirm
   `ANTHROPIC_BASE_URL` is the gateway and no OAuth login is requested.
6. **Gate — GPG** — `dcx -p base --as gpgtest --gpg` in a git repo, then
   `git commit -S --allow-empty` inside. If it fails with `No secret key`, the flag
   is unworkable on the `dcx` path; delete it from the design rather than shipping
   it broken.
7. **Git identity** — `git commit --allow-empty` inside (unsigned) succeeds and is
   authored as you. No `dubious ownership`.
8. **Persistence** — write a memory file, exit, relaunch same name. Still there.
9. **Isolation** — `dcx -p base --as other`. Empty state; `scratch`'s memory absent.
10. **Two instances, one project** — launch `--as a` and `--as b` from the same
    folder. Both run concurrently; neither hijacks the other's container.
11. **GKE + reduced privilege** — apply the stub RBAC to `gcp-cen-st-sk8s`, then
    `dcx -p k8s --as stagetest`. Inside: `kubectl get pods -n <ns>` succeeds,
    `kubectl delete pod <x>` is **denied**. The denial is the actual proof — success
    alone would also happen with the operator's own credentials.
12. **No exec plugin** — `grep -c exec /run/dcx-creds/kubeconfig.yaml` returns `0`.
13. **EKS** — repeat 11 against `aws-cen-st-sk8s`. Same code path, no changes.
14. **Expiry, shim, notification** — mint with `--duration=10m` and wait. `kubectl`
    prints the refresh instruction and exits 1; a Herdr notification fires from
    `dccred watch`. Then `dccred refresh stagetest` on the host, and in the *same*
    container session `kubectl get pods` works again with no restart.
15. **GCP** — `dcx -p gcp --as g1`; `gcloud storage ls` succeeds against the chosen
    project and only that project.
16. **OAuth opt-in** — `dcx -p base --as oa --auth oauth`; Claude runs without a
    login prompt and `ANTHROPIC_BASE_URL` is unset.
17. **Combined** — `dcx -p full --as both`; both toolchains and both credentials work.
18. **Herdr workspace** — `dcws -p k8s --as sk8s-debug`. Workspace labelled
    `sk8s-debug` with panes `shell:sk8s-debug` and `claude:sk8s-debug`; the Claude
    pane is detected as an agent (`herdr agent list`). Kill the Claude pane's
    process, run `dcws -r -p k8s --as sk8s-debug`, confirm only the dead pane
    relaunches.
19. **Teardown** — `dcx --rm scratch` removes container, volumes, and state dir;
    `docker volume ls | grep scratch` returns nothing.
