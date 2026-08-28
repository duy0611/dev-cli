---
name: devcontainer-init
description: Generate a portable .devcontainer/ (devcontainer.json + initializeCommand + postCreateCommand + Claude plugin manifest) for a Python, Node.js, or Go project, so Claude Code runs in a container with the project's own toolchain. Use when the user asks to add a devcontainer, containerize a project for Claude, set up a dev container, sandbox a repo, make a project work with dcx/dcclaude, or says "this project has no .devcontainer". Also use for questions about which base image or devcontainer Features a project should use.
---

# devcontainer-init

Writes a `.devcontainer/` that runs Claude Code plus the project's language
toolchain in a container. The output works with `dcx` (project mode), a bare
`devcontainer up`, and VS Code — nothing in it depends on this repo's images
being built locally.

## Refuse if a devcontainer already exists

**First action, before anything else.** Check the target project for
`.devcontainer/devcontainer.json` or `.devcontainer.json`.

If either exists: print what you found and stop. Write nothing, back nothing
up, merge nothing. Say that the skill declines to touch an existing config, and
offer to review or extend it by hand as a separate request.

## Steps

1. **Check for an existing devcontainer.** Refuse as above.

2. **Detect the language** from marker files in the project root:
   `go.mod` → Go · `pyproject.toml` / `requirements.txt` / `setup.py` → Python ·
   `package.json` → Node.

   More than one match, or none, means ask which toolchain the container is
   for. Do not guess on a monorepo.

3. **Pin the version from the project, not from memory.** Read it out of the
   repo rather than picking a default:

   | Language | Read from | Fallback |
   |---|---|---|
   | Go | the `go` directive in `go.mod` | ask |
   | Python | `requires-python` in `pyproject.toml`, or `.python-version` | ask |
   | Node | `engines.node` in `package.json`, or `.nvmrc` | `24` |

   Never emit a bare `{}` for a Feature. A Feature with no `version` resolves
   to whatever its own default is, which drifts.

4. **Copy the templates** from this skill's `templates/` into
   `<project>/.devcontainer/`:

   ```
   devcontainer.json
   init.sh            chmod +x
   post-create.sh     chmod +x
   ```

   Both shell scripts are complete and need no editing — all their per-project
   behaviour is runtime detection.

5. **Copy the plugin manifest.** From this repo, at
   `<skill-dir>/../../images/base/claude-plugins.txt`, into
   `<project>/.devcontainer/claude-plugins.txt`. It is copied rather than
   referenced so each project can edit its own set. Tell the user it is theirs
   to trim — every plugin costs container-create time on first run.

6. **Edit `devcontainer.json`:**
   - Replace `PROJECT_NAME` with the project's directory name.
   - Add the language Feature to the `features` object (table below).
   - For a Node project, set the existing `features/node:1` version from step 3
     instead of adding a second entry.
   - Add a short comment above each Feature saying why that version. Match the
     house style: explain the *why*, name the failure avoided.

7. **Add `.devcontainer/.env` to the project `.gitignore`.** It is written at
   mode 0600 and holds the Claude token. Create `.gitignore` if absent.

8. **Verify, then report.** See below. Do not claim it works without running
   something.

## Language Features

Added to the `features` object alongside the node and claude-code entries the
template already carries.

| Language | Feature | Notes |
|---|---|---|
| Go | `ghcr.io/devcontainers/features/go:1` | `{ "version": "<from go.mod>" }` |
| Python | `ghcr.io/devcontainers/features/python:1` | `{ "version": "<from pyproject>" }`. `post-create.sh` installs `uv` itself when it sees `uv.lock` — the Feature supplies only the interpreter |
| Node | `ghcr.io/devcontainers/features/node:1` | already present; set its `version` |

`post-create.sh` picks the dependency command from the lockfile
(`uv.lock` → `uv sync`, `poetry.lock` → `poetry install`, `pnpm-lock.yaml` →
`pnpm install --frozen-lockfile`, and so on). No template change needed for
that.

**A Feature that ships a CLI you also want gated by an expiry shim will fight
this repo's `dcx-shim`.** Features install to `/usr/local/bin/<tool>`, which is
where `images/k8s/Containerfile:54` puts the symlink to the shim. If a project
adds `kubectl-helm-minikube` and is then run under `dcx` profile mode, the
Feature wins and expiry gating disappears silently. Say so in a comment if you
ever add such a Feature here.

## Anything a Feature cannot install

Add a `Containerfile` next to `devcontainer.json` and swap `"image"` for:

```json
"build": { "dockerfile": "Containerfile", "context": ".." }
```

Reach for this only when a Feature genuinely cannot do the job — a private apt
repo, a vendored toolchain, CGO system libraries. The Feature path is the
default because it needs no local build and stays pullable for anyone.

## Faster start for people who have this repo built

Mention this once, as a comment in the emitted `devcontainer.json`, not as the
default:

```jsonc
// Faster first create if you have this repo's images built (`make base`):
//   "image": "localhost/dcx-base:latest",
//   "remoteUser": "node",        // dcx-base's user is node, not vscode
//   then drop the node + claude-code features, and update
//   CLAUDE_CONFIG_DIR / the volume target to /home/node/.claude
//
// dcx-base has the plugins baked at /opt/claude-seed, so container start is
// instant and needs no network. The cost is that the config then only works
// for someone who has built this repo's images.
```

It is three coupled edits, not one — say that, so nobody changes only the
`image` line and hits a `vscode`-vs-`node` home directory mismatch.

## Verification

Run these and show the real output. State plainly if any fails.

```sh
# the emitted JSON parses
jq -e . <project>/.devcontainer/devcontainer.json >/dev/null

# the scripts are valid shell
bash -n <project>/.devcontainer/init.sh <project>/.devcontainer/post-create.sh

# the host half runs and produces an env file
<project>/.devcontainer/init.sh && ls -l <project>/.devcontainer/.env
```

Then the real test, which needs a container build and takes a few minutes on
first run:

```sh
cd <project> && dcx -- <toolchain version command>   # go version | python -V | node -v
```

First create downloads Features and installs the Claude plugins over the
network. Tell the user to expect that, and that later starts reuse the built
image and the populated volume.

## Design notes

Kept short here; the reasoning that produced these choices lives in
`docs/design.md` and `CLAUDE.md`.

- **Plugins install at create time, into the volume's own mount point.** The
  `/opt/claude-seed` staging dance in `images/base/Containerfile:99-111` exists
  only because that image bakes plugins at *build* time into a path a volume
  later shadows. With no build step there is nothing to shadow, so installing
  directly at `$CLAUDE_CONFIG_DIR` keeps the marketplace cache's absolute paths
  correct with no staging.
- **`${devcontainerId}`** names the volumes. It is a CLI-provided stable hash of
  the config, so two projects with the same directory name do not share a Claude
  volume — and nothing collides with `dcx-claude-<name>` from
  `lib/instance.sh:9`.
- **No gateway URL is hardcoded.** `lib/env.sh:9` bakes in a Supermetrics
  LiteLLM endpoint; this skill's `init.sh` deliberately does not. It passes
  `ANTHROPIC_BASE_URL` through only when the host already sets it.
- **`init.sh` tries the macOS Keychain, then the environment**, so the same
  file works on Linux and WSL.
- **`.env` is always created, even empty.** `--env-file` pointing at a missing
  path is a hard runtime error.
- **No cloud credentials.** Minted, scoped k8s/GCP/AWS credentials remain the
  job of `dcx -p k8s --as <name>` profile mode, which mounts `/run/dcx-creds`
  and gates the CLIs behind expiry shims. Nothing here replicates that.
