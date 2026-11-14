# Serving a container's agent over ACP with `dev container acp`

## Problem

The operator runs coding agents inside dev containers, where the harness, the
sandbox and the credentials live. To work with several of them from one place,
and let them see each other's messages, they want the agents in a Buzz channel:
@mention one from Buzz Desktop and it does the work in its container and answers
there. Buzz drives agents over the Agent Client Protocol (ACP) by spawning a
local process and speaking JSON-RPC on its stdin and stdout. Nothing today turns
an agent inside a container into such a process.

Findings this design rests on are in
`docs/research/2026-11-13-acp-buzz-facts.md`; a throwaway prototype of the
command proved the path end to end on 2026-11-13.

## Goal

A few long-lived agents, one or several per container, each a normal Buzz
Desktop agent whose harness is `dev container acp`. Buzz Desktop decides
whether an agent runs; the container is where it runs.

Success: against a relay the operator hosts at a real address, they run
`dev container acp api --agent claude --print-harness`, paste the result into
Desktop's custom harness form, create an agent on it and start it. An @mention
in a channel gets an answer posted by the agent, which ran its tools inside
`api` with `api`'s credentials. A second agent on another container, in the
same channel, can be @mentioned by the first and answers.

## Scope

- **Buzz v0 only:** Desktop 0.5.x and buzz-acp from `block/buzz`, on a
  self-hosted relay. Other ACP clients may well work, since the command speaks
  plain ACP, but nothing is built or tested for them.
- **Experimental**, as `dev diff` is: commands, flags and output may change
  without a migration. Say so in `docs/USAGE.md` and the command's help.
- **Agents:** claude, opencode, codex and hermes, the agents `internal/agent`
  already knows.

## Non-goals

- Buzz 1.0 (`block/buzz-app`) and `*.communities.buzz.xyz` communities. They
  need federated sign-in, a Buzz plugin and an MCP tunnel; see the research
  file.
- Managing agents' lifecycle: no daemon, no registry of agents, no
  `dev agent list`. Buzz Desktop is the only record of what runs.
- Agent-to-agent protocols (A2A), or moving `dev-team` workers onto ACP.
- ACP's HTTP or WebSocket transport.
- The client's MCP servers, filesystem or terminal reaching the agent.
- A relay on the host's loopback (`ws://127.0.0.1:...`). Operator decision
  2026-11-14: relays are self-hosted at a real address, and a loopback relay
  is unsupported. `BUZZ_RELAY_URL` passes unchanged and must be reachable from
  inside the container.

## Anti-goals

- Creating a container. `acp` starts a stopped one and refuses one that was
  never created, exactly as `start-agent` does.
- Changing the address the agent uses to reach the relay. The relay chooses
  the community from the `Host` it is reached as; a rewritten hostname lands
  in no community (or the wrong one).
- Sending the container any host variable the operator did not ask for. In
  the prototype a `BUZZ_*` pattern shipped the relay's private key and S3
  secret into the container.
- Anything but protocol on stdout.

## Lifecycle

- Starting the agent in Desktop makes buzz-acp spawn `dev container acp`,
  which brings the container up if it is stopped, then runs the agent's ACP
  adapter in it. Stopping the agent in Desktop closes stdin; the adapter exits
  and so does `dev`.
- Every agent is its own `devcontainer exec` (or `kubectl exec`); agents on one
  container share nothing but the container.
- Stopping the container kills the adapter; buzz-acp restarts it, and the
  restart starts the container again. **Accepted:** to stop a container for
  good, stop its agents in Desktop first. Document it in `docs/USAGE.md`.
  A warning from `dev container stop` when an `acp` process is attached is the
  builder's call; skip it if detection is not reliable.
- A rebuild kills running agents; buzz-acp's restart brings them back once the
  container is up. No warning is needed.

## Command

```
dev container acp [NAME] [--agent ID] [--workspace W] [--pass-env NAME]...
                       [--integration NAME]
                       [--kube-token] [--ssh-agent | --no-ssh-agent]
                       [--trace FILE] [--print-harness] [-- COMMAND [ARGS...]]
```

A new file `internal/cli/acp.go` under the `container` noun; logic beyond
argument handling and orchestration lives in a new package `internal/acp`.

### Launch

The command reuses `start-agent`'s launch path (`launchAgent`): env, kube
token, up if stopped, owed agent config, presence check, ssh relay, guarded
run, audit (event `acp`, argv only). Differences:

- No TTY: the adapter's stdin and stdout are the bridge's pipes, stderr is
  the operator's stderr.
- No Herdr naming; there is no pane.
- Everything `dev` prints goes to stderr, including the presence check's and
  the kube-token flow's output. `confirm` already answers no without a
  terminal; a missing adapter fails naming it and the container's config,
  with no rebuild offer.
- The adapter's stdin must be a real pipe file (`os.Pipe`), not an
  `io.Reader`: `os/exec` otherwise waits on a copying goroutine until the
  client next writes, long after a crashed adapter is gone. (The prototype
  hit this; it is the same family as the `StdinPipe` note in
  `docs/ARCHITECTURE.md`.)

### Which adapter

`agent.Agent` gains the argv and environment that serve it over ACP:

| `--agent` | argv | env |
|---|---|---|
| claude | `claude-agent-acp` | `CLAUDE_AGENT_ACP_PROTOCOL=v1` |
| opencode | `opencode acp` | |
| codex | `codex-acp` | |
| hermes | `hermes acp` | |

The `claude` protocol pin (`CLAUDE_AGENT_ACP_PROTOCOL=v1`) is not part of
the registry: it belongs to the `buzz` integration, because buzz-acp and
`claude-agent-acp` implement different drafts of protocol v2; drop it when
they agree. A `COMMAND` after `--`
replaces the argv (the env still applies), for trying an adapter dev-cli does
not know.

The presence check looks for the adapter's executable, not the agent's.

### The bridge

`internal/acp` copies newline-delimited JSON between the client and the
adapter, rewriting as it goes. A line that is not a JSON object passes
through untouched. Numbers must survive exactly (decode with `UseNumber`).

Client to agent:

- `initialize`: delete `clientCapabilities.fs`, `.terminal` and
  `.auth.terminal`. Deleted, not set false: omitted is "unsupported" in every
  protocol version, and the v2 drafts disagree on the shape of a present
  capability.
- `session/new`, `session/load`, `session/resume`:
  - `cwd` maps from host to container. A `cwd` no folder maps (buzz-acp sends
    its own working directory) becomes the container's workspace folder, with
    one line on stderr.
  - `mcpServers` is replaced by `[]`, with a line on stderr naming how many
    were dropped.
  - `additionalDirectories` entries map, or are dropped with a line.
- Every string value: host folder paths become container paths, matched as
  whole paths so `/x/app` does not touch `/x/apple`, inside prose and
  `file://` URIs alike.

Agent to client: the reverse path mapping on every string value.

Folder pairs come from the container: the primary folder's host path and
where it appears inside, plus every `model.Folder`. The prototype asked the
container for its working directory (`pwd` through exec); deriving it from
what dev-cli already knows (the guarded workspace folder,
`WorkspaceFolderOf` for isolated) is better if it is reliable, and is the
builder's call. Folderless containers have no pairs; every session starts in
the workspace folder.

### What reaches the agent

- By default nothing. With `--integration buzz`, exactly `BUZZ_PRIVATE_KEY`,
  `BUZZ_AUTH_TAG`, `BUZZ_RELAY_URL` and `BUZZ_OUTPUT_FORMAT`, when set in
  `dev`'s environment. buzz-acp sets them for its agent and the agent's `buzz`
  CLI needs them to reply.
- `--pass-env NAME` adds a variable; repeatable. Exact names only, no
  patterns.
- Values pass unchanged. In particular a relay URL is never rewritten (see
  anti-goals).
- They must not appear on the host's process list. The local provider passes
  env with `--remote-env`, which does (ARCHITECTURE.md ~430); use another
  channel. My call: write them through a separate exec's stdin into a file
  readable only by the container user, have the adapter's launch read and
  delete it, then exec the adapter. The builder may choose another channel
  that meets the same bar: not on the host's process list, not left behind
  after the adapter starts, not readable by other users in the container.
- The agent's key is the agent's own Buzz identity; holding it lets the agent
  act as itself. `docs/SECURITY.md` should say so in a sentence.

### `--print-harness`

Prints, and does nothing else, a POSIX shell script. With NAME it is a fixed
script that `exec`s this `dev` binary (its absolute path, quoted) with
`container acp NAME --workspace W --agent ID` and any `--integration`,
`--pass-env`, `--trace` and ssh flags given; it checks that the container
exists. It sets no `PATH`: Buzz Desktop starts harnesses with the login-shell
`PATH` (operator-verified 2026-11-14).

Without NAME it is a generic script for clients that set per-agent environment
variables, as Buzz Desktop does. It reads `DEV_ACP_CONTAINER` and
`DEV_ACP_WORKSPACE` (exit 2 with a message when unset) and optionally
`DEV_ACP_AGENT` and `DEV_ACP_TRACE`, then `exec`s `dev container acp` with
them, plus `--integration`, `--pass-env` and ssh flags given to
`--print-harness`. `--agent` and `--trace` given without NAME set the defaults
used when those variables are unset (`claude` and `/tmp/acp-$DEV_ACP_CONTAINER.log`
otherwise). It resolves and touches no container.

`docs/USAGE.md` shows the Desktop steps, including that the **ACP command**
stays Buzz ACP and the harness goes in the **agent runtime**.

## Integrations

Decision, 2026-11-14: `dev container acp` is a client-neutral ACP agent by
default. Behaviour specific to one client sits behind `--integration NAME`, a
single value validated against a small table in `internal/cli/acp.go`; only
`buzz` exists, and another is a new table entry. `buzz` adds:

- the default pass list (`BUZZ_PRIVATE_KEY`, `BUZZ_AUTH_TAG`, `BUZZ_RELAY_URL`,
  `BUZZ_OUTPUT_FORMAT`);
- the `CLAUDE_AGENT_ACP_PROTOCOL=v1` pin for the `claude` agent; other agents
  get none;
- the warning when no `buzz` CLI is on the container's `PATH`.

Without `--integration`, only `--pass-env` names are passed (by the same private
channel), nothing is pinned and no `buzz` check runs.

### Modes and providers

- Guarded, isolated, unprotected: all work; the bridge runs inside
  `guardedRun` like every other agent launch. Guarded containers do not get
  the ssh agent, as today.
- k8s (experimental): launch and bridge work through `kubectl exec -i`.

## What the image needs

The adapters and the `buzz` CLI must be on the container's `PATH`.

- For generated configs, add catalog entries in `internal/dcgen` for
  `claude-agent-acp` and `codex-acp` (both npm packages). `opencode acp` ships
  with opencode; `hermes acp` needs hermes's `acp` extra (see the deviation
  below). Constraint: a features map holds one entry per
  reference, and `opencode` already uses `npmFeature` with its `package`
  option, so two more npm tools cannot simply reuse it. How to get around
  that (another published feature, the node feature's own options, or
  something else that keeps the catalog script-free) is the builder's call.
- Project configs add the adapters themselves; the presence check names what
  is missing.

**Deviation:** the hermes feature has no option to install hermes's `acp` extra
(its only option is `version`, unused), so the catalog does not install it.
`hermes acp` works only if the upstream installer includes it.

### The `buzz` CLI: a binary the operator provides

`block/buzz` publishes no Linux build of the CLI, so the operator builds one
per architecture (for example in a `rust` container) and tells dev-cli where
it is. **Decided by the operator:** dev-cli puts that binary in the
container; it does not build it.

- A setting names a host file per architecture (`linux/arm64`,
  `linux/amd64`). Where the setting lives (a workspace setting, as ssh
  forwarding is, or a global one) and its command surface are the builder's
  call; it follows the prompt-only-at-a-terminal rule like every other value.
- **My call on the mechanism:** a read-only bind mount of the file matching
  the container's architecture at `/usr/local/bin/buzz`, added the way
  dev-cli already adds mounts (generated config, or the override config for a
  project config). It reaches generated and project configs alike and leaves
  the image untouched. The catalog stays Features-only.
- Like any mount it takes effect when the container is next created or
  rebuilt; changing the setting says so.
- A configured file that is missing, or not a Linux ELF for the container's
  architecture, is an error at create/rebuild naming the file.
- Isolated containers get the same mount; it is one read-only file, not a
  path into the operator's tree. k8s has no host mounts: there the binary
  must be in the image, and the setting is refused for a k8s workspace with
  that explanation.
- With `--integration buzz`, a missing `buzz` CLI is a warning on stderr, not a failure (my call): the
  adapter starts, and the operator learns why replies would not arrive. It is
  not fatal because only Buzz needs it; another ACP client would not.

## Testing

- `internal/acp`: unit tests for every rewrite above (capabilities, cwd
  mapping and fallback, MCP drop, additional directories, both directions,
  whole-path matching, non-JSON passthrough, large ids).
- `internal/cli`: the fake provider (`kubetoken_test.go`) with an `execFn`
  that plays an ACP agent: a full bridge through `launchAgent`, an adapter
  that dies while the client is connected (the command must return), stdout
  carrying nothing but protocol, the env allowlist, and `--print-harness`.
- The `buzz` binary setting: the mount appears in the generated and the
  override config for the matching architecture, a missing or wrong-arch file
  is refused, a k8s workspace is refused.
- Stub executables for anything that shells out, per the conventions.
- Smoke (optional, needs an engine): `acp` against a container with
  `opencode acp`, one `initialize` round trip.

## Docs

- `docs/USAGE.md`: the command, its flags, the Desktop setup, the lifecycle
  (stop agents before stopping the container), the `buzz` binary setting
  with a recipe for building the Linux binaries, experimental status.
- `docs/SECURITY.md`: the agent's key lives in the container.
- `AGENTS.md`: list `acp` beside `dev diff` as experimental.

## Left to the builder

The exact stderr wording; whether `dev container stop` warns about attached
agents; how folder pairs are derived; the env channel, within the stated bar;
the exact shape of `--print-harness` output;
the npm-feature workaround; where the `buzz` binary setting lives and its
command surface.
