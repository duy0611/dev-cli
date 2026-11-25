---
name: dev-setup
description: "Use when setting up the dev CLI for a project: configuring a local or k8s (experimental) provider, creating or choosing a workspace, creating a devcontainer for a repo, or keeping the dev setup in a sibling directory outside the project repo."
---

# Setting up `dev` for a project

`dev` manages devcontainers and runs coding agents in them. Operator reference:
`docs/USAGE.md`; guarded vs isolated: `docs/SECURITY.md`.

## Ground rules

- `dev` prompts only at a terminal. In a non-terminal run (you, as an agent)
  pass **every** flag, or the command fails naming the missing one.
- **Ask the user** for values instead of guessing: provider kind, and for k8s (experimental)
  the context, namespace and registry; the project folder; the tools; the
  container name. Offer defaults, but wait for the answer.
- Never write a secret into a file. Use `dev workspace set KEY SPEC`
  (`keychain:`, `keyring:`, `op://`, `literal:`).
- To try commands without touching the operator's database:
  `DEV_STATE=$(mktemp -d) dev ...`.

## 1. Prerequisites

Needed on PATH: `dev`, `devcontainer`, `docker` (or a compatible engine), and
`kubectl` for the k8s (experimental) provider or `--kube-token`. Check, then reuse what exists:

```sh
dev provider list
dev workspace list
```

If a suitable provider and workspace are already listed, skip steps 3–4.

## 2. Choose a layout

**(a) The project's own `.devcontainer/`.** If the repo ships one, `dev
container create` uses it as is and never edits it. Its optional
`.devcontainer/agents.yaml` is applied automatically.

**(b) A sibling of the project folder, `<project>-dev/`**, holding `agents.yaml`,
a `devcontainer.json` and notes, so nothing is written to the project repo:

```sh
mkdir -p <project>-dev
cp <directory holding this SKILL.md>/templates/devcontainer.json <project>-dev/devcontainer.json   # then edit
cp <directory holding this SKILL.md>/templates/agents.yaml <project>-dev/agents.yaml               # then edit
dev container create <name> --folder <project> \
  --config <project>-dev/devcontainer.json --agent-config <project>-dev/agents.yaml
```

`--config` names the sibling's `devcontainer.json` (or `.devcontainer.json`);
the project stays the folder and the container is built from that file. The
path is stored, so `rebuild` re-reads it. `--config` is refused with
`--generate`, `--no-folder` or `--tools`, and wins, with a warning, over a
`.devcontainer/` the project does have. `agents.yaml` paths such as `./skills/x`
resolve relative to that file, so skills can live in the sibling directory too.

For a catalog-only container instead, skip the `devcontainer.json` and use
`--generate --tools node,gh,claude-code`; the generated configuration is stored
in dev's database (`DEV_STATE`, default `~/.local/state/dev`), not in the
project folder.

`--generate` against a folder that already ships a config is an error.

## 3. Provider

Ask which kind. See `reference/local.md` or `reference/k8s.md` (k8s is experimental).

- **local** (stable): `dev provider configure local --kind local`
- **k8s (EXPERIMENTAL)**: settings and objects may change without a migration.
  Needs namespace, registry and more; see `reference/k8s.md`.

Mention the experimental status whenever both are offered side by side.

## 4. Workspace

A workspace groups containers and holds their settings.

```sh
dev workspace init <ws> --provider <provider>   # add --ssh-forward to push from containers
dev workspace use <ws>                          # the first one is active already
dev workspace set ANTHROPIC_API_KEY keyring:anthropic   # value specs only
dev workspace show <ws>
```

Reuse an existing workspace if one fits; `--from OTHER` copies provider and
settings. Container commands take `--workspace NAME` to override the active one.

## 5. Create, verify, run an agent

```sh
dev container tools                      # catalog: node, gh, go, python, claude-code, ...
dev container create <name> --folder <path> [--generate --tools a,b | --config <file>] \
  [--agent-config <file>] [--mode guarded|unprotected|isolated]
dev container config show <name>         # verify what it is built from
dev container list
dev container start-agent <name> --agent claude   # claude|codex|hermes|opencode
dev container shell <name>
```

- Default mode is `guarded` on local (folder live, `.git` read-only) and
  `isolated` on k8s (experimental). Do not pick `unprotected` without the user asking; see
  `docs/SECURITY.md`.
- `start-agent` and `shell` are interactive: give those commands to the user to
  run, and verify yourself with `dev container exec <name> -- <cmd>`.
- `start-agent` needs the agent installed in the image (`--tools claude-code`
  for a generated config; in the project's `devcontainer.json` otherwise).
- Change tools later: `dev container rebuild <name> --tools +helm,-yq`
  (generated containers only).
- First create builds the image and can take minutes.
- Remove: `dev container remove <name>` (`--keep-state` keeps agent state, local
  only). The project folder is never touched.

## 6. Guarded-mode git note

In guarded mode `git commit`, `add`, `stash`, `checkout -b` and `fetch` fail
with `Read-only file system`. Tell the user to add this to the project's
`AGENTS.md` or `CLAUDE.md` (in layout b, that is the one write to the repo, so
ask first):

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

## 7. Troubleshooting

| Symptom | Fix |
|---|---|
| `no active workspace; run: dev workspace use NAME` | `dev workspace use NAME` or pass `--workspace` |
| `no devcontainer config in ...` | the folder ships none: add `--generate --tools ...` |
| `would mount everything under it` | run from home or `/`; pass `--folder PATH` |
| `--generate would shadow it` | the folder has its own config; drop `--generate` |
| `Read-only file system` on git | guarded mode working as designed; step 6 |
| agent not installed | add it to tools (generated) or `devcontainer.json`, then `dev container rebuild <name>` |
| `agents.yaml: X is not installed ...; skipping` | add that agent to the image, or ignore |
| `workspaceMount`/`workspaceFolder` refused | guarded mode forbids them; remove from `devcontainer.json` |
| k8s (experimental) `ImagePullBackOff` | create a pull secret, `dev provider configure P --image-pull-secret NAME` |

More: `docs/USAGE.md`, section "Troubleshooting".
