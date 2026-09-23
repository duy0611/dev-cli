# Declared agent configuration

Create a container, and before an agent in it is useful the operator types the
same thing every time:

```
❯ dev container exec api -- claude plugin marketplace add anthropics/claude-plugins-official
❯ dev container exec api -- claude plugin install superpowers@claude-plugins-official
❯ dev container exec api -- claude mcp add-json --scope user context7 '{…}'
```

The state volume keeps that work across a rebuild of *one* container. It does
nothing for the next container, which starts with an empty volume, and nothing
for opencode or hermes, which have their own spelling of the same ideas.

This lets a project declare its agents' skills, MCP servers and plugins in
`.devcontainer/agents.yaml`, and has `dev` apply that declaration inside the
container on `create` and on `rebuild`.

The set is per project, not per operator or per workspace: two repositories
under one workspace want different skills, and the repository is what a
colleague clones.

## What is already true

- **Every agent already has a directory on the state volume** —
  `CLAUDE_CONFIG_DIR`, `OPENCODE_CONFIG_DIR`, `HERMES_HOME`
  (`internal/dcgen/state.go`). Applying a declaration writes there, so what it
  installs outlives a rebuild on its own; re-applying on rebuild is for
  containers without the volume and for picking up edits to the file.
- **`Provider.Exec` works on both providers.** Everything here runs inside the
  container through it, so neither provider changes and k8s needs nothing of
  its own.
- **Invariant 9 forbids writing into a project's folder.** This design reads
  `agents.yaml` and never writes it. There is no command that edits it.

## What the three agents accept

Checked against each vendor's documentation on 2026-09-23.

| | Plugins | Skills | MCP servers |
|---|---|---|---|
| Claude Code | marketplace plugins; `enabledPlugins` in `settings.json` enables but does **not** install, so `claude plugin marketplace add` and `claude plugin install --scope user` are required | `SKILL.md` under `$CLAUDE_CONFIG_DIR/skills/<name>/` | `claude mcp add-json --scope user` |
| opencode | npm packages in the `plugin` array of `opencode.json` | `SKILL.md` under `$OPENCODE_CONFIG_DIR/skills/<name>/` | `mcp` key in `opencode.json`; no CLI |
| hermes | none | `SKILL.md` under `$HERMES_HOME/skills/<name>/` | `mcp_servers` in `$HERMES_HOME/config.yaml`; no CLI |

Three consequences shape the file:

- **Skills are the one portable layer.** The format is shared; only the
  directory differs.
- **MCP is one concept spelled three ways.** A stdio server (command, args,
  env) or a remote one (url, headers) translates mechanically.
- **Plugins do not abstract.** A Claude marketplace plugin and an opencode npm
  package have nothing in common, and hermes has neither. They stay per agent.

## The file

```yaml
version: 1

# Applied to every agent installed in the container.
skills:
  - git: https://github.com/obra/superpowers
    ref: v6.4.1                    # optional; default branch otherwise
    path: skills/brainstorming     # optional; directory holding SKILL.md
  - path: ./skills/our-conventions # local, relative to this file

mcp:
  context7:
    command: npx
    args: ["-y", "@upstash/context7-mcp"]
    env:
      CONTEXT7_API_KEY: ${CONTEXT7_API_KEY}
  sentry:
    url: https://mcp.sentry.dev/mcp
    headers:
      Authorization: Bearer ${SENTRY_TOKEN}

# Per agent: additions to the shared lists, and what only that agent has.
claude:
  marketplaces:
    claude-plugins-official: github:anthropics/claude-plugins-official
  plugins:
    - superpowers@claude-plugins-official
  skills: []
  mcp: {}
opencode:
  plugins: [opencode-wakatime]
  skills: []
  mcp: {}
hermes:
  skills: []
  mcp: {}
```

Rules:

- **Two layers, additive.** An agent receives the top-level `skills` and `mcp`
  plus its own section's. A per-agent MCP server with the same name as a
  top-level one replaces it for that agent; that is the only override.
- **`plugins` exists only where the agent has plugins**, and `marketplaces`
  only under `claude`. `hermes.plugins` is an unknown key.
- **Strict.** `version` is required and must be `1`. Unknown keys, an MCP entry
  with both or neither of `command` and `url`, a skill with both or neither of
  `git` and `path`, and a skill name that is not a valid directory name are all
  rejected with exit 2, naming the file and the key. A typo is caught at
  `create`, not discovered as a plugin that never arrived.
- **A skill's name** is the last element of its `path` (for `git`, the
  subdirectory if given, else the repository name). Two skills with one name for
  the same agent are rejected.
- **`path:` skills are read on the host**, relative to the file's directory,
  resolved with `xpath.Resolve`, and streamed into the container as a tar over
  exec stdin. That is what lets `--agent-config ~/dotfiles/agents.yaml` carry
  skills sitting next to it, and a `--no-folder` container use them.
- **`git:` skills are cloned inside the container** with `git clone --depth 1`
  (and `--branch ref` when given). The base image has git; a project image
  without it fails that step, which names the missing binary.

### Secrets

The file is committed, so it never holds a value. Invariant 3 extends to it.

`${NAME}` is a reference. `dev` does not substitute it; each adapter rewrites it
into its agent's own environment-reference syntax, and the agent resolves it
when it starts, from the environment `dev` launches it with — which already
comes from workspace settings. So:

```sh
dev workspace set CONTEXT7_API_KEY keychain:context7
```

puts no token in the file, the database, or the state volume, and rotating it
needs no rebuild. A `${NAME}` no workspace setting defines is a warning at
apply time, not an error: the operator may set it afterwards.

`${NAME}` is recognised only in MCP `env` values, `headers` values, `args` and
`url`. Anywhere else it is literal text.

## Choosing the file

| `create` flags | Stored in `containers.agent_config` | File read |
|---|---|---|
| none | `''` | `<folder>/.devcontainer/agents.yaml` if it exists, else nothing |
| `--agent-config PATH` | the physical path | `PATH`; missing is exit 3 |
| `--no-agent-config` | `none` | nothing |

`--agent-config` and `--no-agent-config` together are exit 2. A `--no-folder`
container with neither reads nothing.

Fixed at create, like `persist_state`, so `rebuild` reads the same file without
the operator repeating a flag. Only the choice is stored, never the contents:
an edit takes effect on the next rebuild. Existing rows read as `''`, which is
safe — nothing is applied to them until they are rebuilt, and then only if the
project has a file.

The default path follows the resolved folder, so a worktree container reads the
checkout's own `.devcontainer/agents.yaml`, as it already reads the checkout's
`devcontainer.json`.

## When it is applied

On `create` after `Up` succeeds, and on `rebuild` after `Rebuild` succeeds.
Never on `start`, `up` or `exec` — the same contract as `devcontainer.json`:
editing the file changes nothing until a rebuild.

`create --no-start` cannot apply anything, because there is no running
container to exec into. It sets `containers.agent_config_pending = 1` instead,
and the next `start` applies the file and clears the flag. A failed apply also
leaves the flag set, so the next `start` retries it. The flag records work `dev`
owes the container, not the container's state, so invariant 4 is untouched.

## Applying

For each agent in the registry that has a section or would receive a shared
item:

1. **Probe** — `command -v <binary>` through `Exec`. Missing: warn on stderr
   (`agents.yaml: hermes is not installed in this container; skipping its
   section`) and go to the next agent. A shared file must work across containers
   with different tool sets.
2. **Steps** — run the adapter's steps in order, stopping at the first failure.

A failed step exits 1, naming the step and relaying its stderr. The container is
left running for inspection.

### The adapter

`internal/agent` gains one adapter per registry entry:

```go
type Configurer interface {
	Probe() []string
	Steps(v agentcfg.AgentView) ([]Step, error)
}

type Step struct {
	Desc  string    // "claude: install superpowers@claude-plugins-official"
	Cmd   []string
	Stdin io.Reader // a local skill's tar or a merged config; nil otherwise
	// Capture is set on a read step; its stdout feeds the next step's merge.
	Capture bool
}
```

Steps are data, so adapters are tested without a container. Adding an agent is
one adapter; nothing in `internal/cli` names one.

- **claude** — `claude plugin marketplace add` per marketplace, skipping any
  already listed by `claude plugin marketplace list`; `claude plugin install
  --scope user` per plugin; `claude mcp add-json --scope user NAME JSON` per
  server, preceded by `claude mcp remove --scope user NAME` so a changed
  definition replaces the old one.
- **opencode** — read `$OPENCODE_CONFIG_DIR/opencode.json` (absent is an empty
  object), add each declared package to the `plugin` array if absent, set each
  declared server under `mcp` by name, write it back. Per entry, not per key:
  a server or plugin the operator added by hand is left where it is, exactly as
  `claude mcp add-json` leaves other servers alone. Every other key round-trips
  untouched, the rule `--override-config` already follows.
- **hermes** — the same over `$HERMES_HOME/config.yaml`, setting declared
  servers under `mcp_servers` by name.
- **skills, all agents** — each skill directory under the agent's skills
  directory is replaced wholesale; directories `dev` did not declare are left
  alone. A git skill is cloned once per apply into a temporary directory in the
  container and copied into each agent.

Every step is safe to repeat, because `rebuild` re-runs all of them over a
state volume that already holds the previous result.

The file-merging steps need the config directories, which are only fixed when
the container persists state. Without the state volume the adapter uses the
agent's default location (`~/.claude`, `~/.config/opencode`, `~/.hermes`).

## Components

- **`internal/agentcfg`** (new) — parses and validates `agents.yaml` into a
  `Spec`, and projects it per agent into an `AgentView` with the layers merged.
  No exec, no cobra, no filesystem beyond reading the file and its local
  skills. Adds `go.yaml.in/yaml/v3`, decoding with `KnownFields(true)`; the
  hermes merge needs a YAML library regardless.
- **`internal/agent`** — the `Configurer` per agent, and the opencode/hermes
  merge functions.
- **`internal/cli`** — flag handling on `create`, resolving the file, the
  apply loop over `Provider.Exec`, and the pending flag on `start`.
- **Migration `0006_agent_config.sql`**:

  ```sql
  ALTER TABLE containers ADD COLUMN agent_config TEXT NOT NULL DEFAULT '';
  ALTER TABLE containers ADD COLUMN agent_config_pending INTEGER NOT NULL DEFAULT 0;
  ```

  Literal defaults, portable to Postgres.

## Errors

| Situation | Result |
|---|---|
| Malformed file, unknown key, invalid entry | exit 2, before any container work |
| `--agent-config` and `--no-agent-config` together | exit 2 |
| `--agent-config PATH` does not exist | exit 3 |
| Agent not installed in the container | warning, agent skipped |
| A step fails | exit 1 naming the step; container left running; pending flag stays set |
| `${NAME}` with no matching workspace setting | warning |

## Testing

- **`agentcfg`** — table tests: valid files, each rejection, layer merge,
  per-agent MCP override, skill naming and duplicates, `${NAME}` recognition.
- **Adapters** — golden `[]Step` per agent; merge tests proving unowned keys
  survive, in the style of `internal/dcgen/overlay_test.go`; env-reference
  rewriting per agent.
- **CLI** — stub executables on `PATH`: a probe miss warns and skips; a failing
  step exits 1 and leaves the flag set; `create --no-start` then `start`
  applies once; `rebuild` re-reads the stored path; flag conflicts exit 2.
- **Smoke** — a generated container with `claude-code` and one local skill:
  the skill is in place and `claude plugin list` shows the declared plugin.

## Documentation

- **`docs/USAGE.md`** — a walkthrough, "Declare a project's agent setup"; the
  two flags under `create`; a reference for the file format.
- **`CLAUDE.md`** — under invariant 9: `dev` reads `agents.yaml` and never
  writes it, and applies it inside the container through `Exec`.

## Out of scope

- Removing what a line no longer declares. Application is additive: a named
  entry or skill directory the file declares is replaced, and nothing else is
  touched, so a plugin dropped from the file stays installed until the
  container is recreated.
- Codex. It is in the agent registry but has no adapter; a `codex:` section is
  an unknown key. It gets one when there is a reason to write it.
- A command that writes or edits `agents.yaml` (invariant 9).
- Applying on `start` or on demand. A later `container sync-agents` could be
  added if rebuild turns out too heavy a way to pick up an edit.

## To confirm while planning

1. Whether opencode installs `plugin` packages itself at startup or needs a
   step to install them.
2. hermes's syntax for an environment reference in `config.yaml`.
3. Whether `claude plugin install` of an installed plugin exits 0.
