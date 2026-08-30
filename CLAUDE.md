# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Bash tooling that runs Claude Code inside containers on macOS + Podman. Four
commands (`dcx`, `dcclaude`, `dcws`, `dccred`) symlinked into `~/.local/bin`,
a shared `lib/`, and four container images. No compiled code, no package
manager, no CI.

## Commands

```sh
make                 # help (prints the header comment of the Makefile)
make lint            # shellcheck + bash -n, yamllint, plutil, render check, skills, hadolint
make test            # smoke-test the base profile end to end (builds dcx-base first)
make build           # all four images
make build k8s       # one image (base | k8s | gcp | full); its deps build first
make k8s             # same, without the `build` word
make install         # ./install.sh (symlinks only)
make clean           # remove the smoke test's instance only
```

There is no unit-test framework and no way to run "a single test". `make test`
runs `test/smoke-base.sh`, a sequence of `check` assertions against one real
container; run it directly (`./test/smoke-base.sh`) to skip the image rebuild.
Individual lint stages are separate targets: `lint-shell`, `lint-yaml`,
`lint-plist`, `lint-render`, `lint-skills`, `lint-docker`.

`make lint` is the fast feedback loop — `lint-render` actually generates a
`devcontainer.json` for every profile in a temp dir and validates it, so it
catches `lib/render.sh` regressions without touching Docker.

### Runtime split: podman builds, docker runs

Image **builds** prefer `podman` (buildah; no buildx plugin needed) and fall
back to `docker`. Running instances go through `docker`, because that is what
the `devcontainer` CLI speaks — `DOCKER_HOST` already points it at the podman
socket. Both write the same local storage, so `FROM localhost/dcx-base`
resolves either way. Override with `DCX_RUNTIME=docker` (honoured by both the
Makefile and `install.sh`) or `make build DOCKER=docker`.

## Architecture

### The one insight that keeps `dcx` small

`dcx` computes a single variable, `$folder`, and everything after it — the
`docker ps` liveness filter, `devcontainer up`, `devcontainer exec` — is
generic. The two modes only differ in how `$folder` is derived:

- **project mode**: `$folder` = the repo holding `.devcontainer/` (original behaviour).
- **profile mode**: `$folder` = the instance's state dir under
  `~/.local/state/dcx/instances/<name>/`, which holds a *generated*
  `.devcontainer/devcontainer.json` whose `workspaceMount` points at the real
  project.

One thing besides `$folder` now varies: **mount geometry**. When the workspace
is a linked git worktree, `lib/render.sh` mirrors host absolute paths into the
container instead of using `/workspace`, and co-mounts the main repo. That is
the only exception to "everything after `$folder` is generic", and it exists
because worktree metadata stores absolute paths that must agree on both sides.

Because the state dir is unique per instance, the container's
`devcontainer.local_folder` label is unique per instance, so the existing
liveness filter distinguishes two sandboxes on the same project with no extra
code. **This label assumption is load-bearing for all instance isolation** and
is asserted in the smoke test.

Mode selection order: `-p` given → profile; else `.devcontainer` found above
`$PWD` → project; else the profile picker.

### Layer map

| Layer | Files | Role |
|---|---|---|
| Commands | `bin/dcx`, `bin/dcclaude`, `bin/dcws`, `bin/dccred` | Arg parsing + orchestration only |
| Shared libs | `lib/*.sh` | Sourced, never executed; all real logic |
| Build-time | `images/*/Containerfile`, `images/shared/install-plugins.sh` | Bake tools + Claude plugins |
| Run-time (in container) | `images/shared/post-create.sh`, `dcx-shim`, `dcx-credcheck` | Volume/git setup, expiry gate |
| Skills | `skills/*/SKILL.md` + `templates/` | Claude Code skills shipped with the repo |

`lib/` split: `common.sh` (die/warn/need, state paths, profile predicates,
atomic secret write) · `instance.sh` (state dir, `instance.json`, volumes) ·
`pick.sh` (fzf-or-numbered-menu pickers) · `mint.sh` (k8s/gcp/aws minting) ·
`env.sh` (`devcontainer.env`, Keychain, opt-in host material) · `render.sh`
(devcontainer.json generation) · `seed.sh` (OAuth seeding).

`dcclaude` and `dcws` are thin: they forward the same flags to `dcx` and add
Herdr wiring. Any new `dcx` flag must be threaded through both.

### Credential flow

Minting runs **on the host** against the operator's own sessions and writes into
`<state>/instances/<name>/creds/` (mode 0700), bind-mounted read-only at
`/run/dcx-creds`. The container holds nothing capable of minting anything — by
design, nothing auto-refreshes.

Every credential is reached through a **file path**, never a baked-in env value,
so a host-side refresh lands with no container restart:
`kubectl` → `users[].user.tokenFile`, `gcloud` →
`CLOUDSDK_AUTH_ACCESS_TOKEN_FILE`, `aws` → `AWS_SHARED_CREDENTIALS_FILE`, `git`
→ `user.signingkey` as a path. Each value file gets a `<kind>.expiry` sidecar
(unix timestamp, written last); `dcx-shim` reads it and blocks before the API
does. Preserve both properties in any change here.

Commit signing (`--sign`) is the same model deliberately: an SSH key at
`/run/dcx-creds/sandbox-signing`, shared across instances and generated by
`dccred signing-key`. GPG signing is **not** available and should not be
reintroduced — it needs the host `gpg-agent` socket, a devcontainer mount path
resolves inside the podman VM, and virtiofs cannot carry a unix socket. Signing
is the only credential whose expiry warns rather than blocks: refusing to commit
over a rotation date costs more than the stale key does.

Writes go through `dcx_write_secret` (temp file + `mv`), so a container reading
mid-refresh never sees a partial token.

k8s uses `kubectl create token` — a Kubernetes API call, so **one code path
serves EKS and GKE**. The generated kubeconfig is built from scratch, not
edited: replacing the user block wholesale is what strips the `exec` credential
plugin, which is why the images need neither `aws eks get-token` nor
`gke-gcloud-auth-plugin`.

### Image chain

`base` → `k8s`, `base` → `gcp`, `k8s` → `full`. `gcp` and `full` share
`images/gcp/Containerfile`, parameterised on `--build-arg BASE`; there is no
second copy to drift. Make targets encode the dependency so a stale base cannot
silently persist into a derived image.

Real cloud binaries live in `/usr/local/bin/real/`; `/usr/local/bin/{kubectl,
helm,k9s,aws,gcloud,gsutil,bq}` are symlinks to `dcx-shim`, which dispatches on
`argv[0]`.

## Invariants — violating these produces failures far from their cause

1. **Never mount the host `~/.claude`.** Claude's marketplace cache is keyed to
   absolute host paths → `Failed to load marketplace: cache-miss`. Plugins are
   installed, never copied from the host.
2. **Plugins must be installed at `/home/node/.claude`, staged to
   `/opt/claude-seed`, and copied back to the same path.** Same path-keyed
   cache. The volume mount would otherwise shadow them.
3. **Take ownership of the Claude config volume before touching it.** A named
   volume whose mount point doesn't exist in the image is created `root:root`;
   `sudo chown` first, non-recursive, first-create only.
4. **`remoteUser` must be non-root.** Claude refuses
   `--dangerously-skip-permissions` as root, and root leaves root-owned files in
   the bind-mounted repo.
5. **`git config --global --add safe.directory` must run *after* any gitconfig
   copy**, which would otherwise overwrite it.
6. **`claude plugin install -y`** — stdin is never a TTY in these contexts.
7. **Use `pwd -P` / `dcx_abspath` for anything that becomes a bind mount.** The
   podman machine resolves the string inside the VM: handing it `/tmp/x` mounts
   the VM's empty `/tmp/x`, silently, instead of the host's `/private/tmp/x`.

## Shell conventions

- **macOS ships bash 3.2.** No associative arrays, no `${var,,}`, no
  `readarray`. Under `set -u` an empty array needs the
  `${arr[@]+"${arr[@]}"}` guard (see `dcclaude`, `dcws`).
- **BSD `date`/`readlink`.** No GNU `date -d` (see `dcx_iso_to_epoch`); no
  dependable `readlink -f`, which is why `common.sh` and each `bin/` script walk
  the symlink chain by hand to find `lib/`.
- Under `set -e`, `[ cond ] && action` **exits the script** when the condition
  is false. The trailing `|| true` on those lines is load-bearing, not sloppy;
  shellcheck runs at `-S warning` partly to keep SC2015 quiet about it.
- Every non-obvious line carries a comment explaining *why*, usually naming the
  failure it prevents. Match that density — this codebase is mostly hard-won
  container trivia, and an uncommented workaround here reads as removable.
- `bin/*` do arg parsing and orchestration; logic belongs in `lib/`. Libs start
  with `# shellcheck shell=bash` and are sourced, never executed.
- New shell files must be added to `SH_FILES` in the Makefile or they are not
  linted.

## Skills

`skills/devcontainer-init/` generates a portable `.devcontainer/` for a
Python/Node/Go project. Note the deliberate inversion: it targets
`mcr.microsoft.com/devcontainers/base:ubuntu` and installs Claude plugins at
**create** time, where this repo's own images target `dcx-base` and bake them
at **build** time. Both are correct for their context — the skill's output must
work for someone who has never built these images, so it trades instant start
for portability, and the `/opt/claude-seed` staging dance becomes unnecessary
because there is no build step to shadow.

Skill template shell files are picked up by the `skills/*/templates/*.sh`
wildcard in `SH_FILES`, so they are shellchecked like everything else. Keep them
placeholder-free and complete — per-project behaviour belongs in runtime
detection, not text substitution.

## Docs

`docs/design.md` is the original design doc: full rationale, decision table,
and a numbered verification plan. Treat it as the record of *why*, not as
current-state truth — read the code for that.
