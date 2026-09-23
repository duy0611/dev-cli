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
    claude-plugins-official: anthropics/claude-plugins-official
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
- **A marketplace's key is its own name**, the one `claude plugin marketplace
  list` reports; the value is passed as-is to `claude plugin marketplace add`
  (`owner/repo`, a git URL, a path). A key that does not match what the source
  calls itself fails the step rather than re-adding it on every rebuild.
- **Strict.** `version` is required and must be `1`. Unknown keys, an MCP entry
  with both or neither of `command` and `url`, a skill with neither `git` nor
  `path` (with `git`, `path` is the subdirectory holding `SKILL.md`), a `path`
  that climbs out of its repository, a local skill with no `SKILL.md`, and a
  skill name that is not a valid directory name are all
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

`create` records `containers.agent_config_pending = 1` whenever the container
has a file to apply, and `start` applies a pending declaration and clears the flag — so the
start inside `create` applies it, and after `create --no-start`, which has no
running container to exec into, the first later `start` does. `start-agent`
honours the flag the same way. Every apply sets the flag before its first step
and clears it after its last, so a failed or interrupted apply leaves it set
and the next `start` retries it. The flag records work `dev`
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
type Agent struct {
	ID, Binary string
	Args       []string
	// Nil for an agent agents.yaml has no section for.
	Configure func(v agentcfg.View) ([]Step, error)
}

type Step struct {
	Desc  string   // "claude: install superpowers@claude-plugins-official"
	Cmd   []string
	Stdin []byte   // a local skill's tar
	// Check runs before Cmd and Done reads its stdout: true skips the step.
	// It runs again after, and false then fails the step.
	Check []string
	Done  func(stdout []byte) bool
	// File and Edit make the step a read-modify-write of one file.
	File string
	Edit func(current []byte) ([]byte, error)
}
```

The probe is `command -v <Binary>`, the check `start-agent` already makes.

Steps are data, so adapters are tested without a container. Adding an agent is
one adapter; nothing in `internal/cli` names one.

- **claude** — `claude plugin marketplace add` per marketplace, skipped when
  `claude plugin marketplace list --json` already names it and verified by the
  same list afterwards; `claude plugin install
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
  alone. A git skill is cloned into a temporary directory in the container for
  each agent that receives it — a shallow clone per agent is cheaper than the
  cross-agent state sharing one would need.

Every step is safe to repeat, because `rebuild` re-runs all of them over a
state volume that already holds the previous result.

Every directory is named as a shell default —
`${CLAUDE_CONFIG_DIR:-$HOME/.claude}`, `${OPENCODE_CONFIG_DIR:-$HOME/.config/opencode}`,
`${HERMES_HOME:-$HOME/.hermes}` — so a container without the state volume gets
each agent's default location with no branch in `dev`.

## Components

- **`internal/agentcfg`** (new) — parses and validates `agents.yaml` into a
  `Spec`, and projects it per agent into an `AgentView` with the layers merged.
  No exec, no cobra, no filesystem beyond reading the file and its local
  skills. Adds `go.yaml.in/yaml/v3`, decoding with `KnownFields(true)`; the
  hermes merge needs a YAML library regardless.
- **`internal/agent`** — `Configure` per agent, and the opencode/hermes
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

## Confirmed while planning

1. opencode installs the packages in `plugin` itself at startup (with Bun,
   into its cache), so writing the name is the whole job. Its environment
   reference is `{env:NAME}`; a stdio server is `{"type": "local", "command":
   [argv], "environment": {}}`, a remote one `{"type": "remote", "url", "headers"}`.
2. hermes expands `${NAME}` in `config.yaml` at load, so references pass
   through unchanged.
3. `claude plugin install` of an installed plugin exits 0. `claude mcp
   add-json` fails on an existing name, hence remove-then-add. `claude plugin
   marketplace add` of a known marketplace is undocumented, hence the list
   check. `claude plugin install` is run without `-y`: a plugin whose
   marketplace declares a command to run at install is refused unattended
   rather than run, and the step's error says so.
