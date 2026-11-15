# ACP and Buzz v0: findings from the prototype

Facts gathered on 2026-11-13 while prototyping `dev container acp` and driving
it from Buzz Desktop 0.5. This file decides nothing; the spec that draws on it is
`docs/specs/2026-11-13-acp-bridge-design.md`.

## Agent Client Protocol

- JSON-RPC 2.0 over stdio, one message per line; stdout carries only protocol,
  logs go to stderr. The client spawns the agent and ends it by closing stdin.
  <https://agentclientprotocol.com/protocol/v1/transports.md>
- `session/new` takes an absolute `cwd`, valid where the agent runs, and
  `mcpServers` (stdio entries carry an absolute host `command`). No path
  translation is defined.
  <https://agentclientprotocol.com/protocol/session-setup>
- Client capabilities `fs.*` and `terminal` make the agent read, write and run
  through the client, i.e. on the host. Omitted means unsupported.
- An HTTP/WebSocket transport is an RFD, not yet in the spec.
- "ACP" also names IBM's Agent Communication Protocol (merged into A2A) and an
  unrelated commerce protocol.

## Adapters, as verified inside a dev container

| Agent | ACP command | Result |
|---|---|---|
| Claude | `claude-agent-acp` (npm `@agentclientprotocol/claude-agent-acp`, 0.107.0) | initialize, session, prompt, shell tool, exit on stdin close |
| OpenCode | `opencode acp` (2.0.32) | same |
| Hermes | `hermes acp` (0.22.2, needs the `acp` extra) | same |
| Codex | `codex-acp` (npm `@agentclientprotocol/codex-acp`) | not tried |

## Through `devcontainer exec`

- With stdin and stdout as pipes, `devcontainer exec` allocates no pty and the
  stream stays clean (verified from a macOS host with podman).
- The Node-based `devcontainer` CLI throws on EPIPE when its reader closes
  early; harmless, but noisy.

## Buzz v0 (block/buzz: Desktop 0.5.x, buzz-acp)

- buzz-acp spawns the agent over stdio. `BUZZ_ACP_LAUNCH_PREFIX` (JSON array,
  absolute executable first) wraps the spawn; Desktop 0.5 also accepts custom
  harnesses (command + args, no commas in args).
- Desktop has two settings that both list ACP commands: the **ACP command**
  must be Buzz ACP; the **agent runtime** is where a custom harness goes.
  Putting a harness in the first slot runs it in place of buzz-acp.
- buzz-acp sends `session/new` with its own working directory as `cwd`, and
  `mcpServers: []` unless `BUZZ_ACP_MCP_COMMAND` is set.
- **Agents reply by running the `buzz` CLI** in their shell
  (`buzz messages send`), with `BUZZ_RELAY_URL`, `BUZZ_PRIVATE_KEY`,
  `BUZZ_AUTH_TAG`. Streamed agent text only feeds Desktop's activity view. The
  CLI has no prebuilt Linux release; it is built with cargo
  (`-p buzz-cli`).
- buzz-acp requests protocol version 2 with `clientInfo`; `claude-agent-acp`
  0.107.0 implements a different v2 draft (`info`) and rejects it with -32602.
  `CLAUDE_AGENT_ACP_PROTOCOL=v1` makes the adapter answer v1, which buzz-acp
  accepts.
- buzz-acp restarts an agent that exits unexpectedly.
- Desktop launched from the relay's `just dev` shell inherits the relay's
  `.env` (relay private key, S3 secret): a `BUZZ_*` pattern forwards them.

## The relay

- The relay maps the request `Host` to a community and fails closed for an
  unknown host (`scripts/seed-local-community.sh`). Every authority is its own
  community; there are no aliases. Reaching a loopback relay from a container
  as `host.docker.internal` therefore lands nowhere (404); keeping the
  container-side address `127.0.0.1:3000` and tunnelling it to the host works.
- Under podman machine, `host.docker.internal` and `host.containers.internal`
  resolve in the container and reach the host's loopback.
- `*.communities.buzz.xyz` communities require NIP-FI federated sign-in
  (`Nostr-Federated-Identity` header on the upgrade). buzz-acp and Desktop 0.5
  do not send it: 401 and "Upgrade Buzz to join this community".

## Buzz 1.0 (block/buzz-app), for later

- Agents are plugins; there is no custom-command harness. Bundled plugins spawn
  manifest-declared programs; the ACP client is not exported to installed
  plugins.
- Tools are served to the agent as a per-session Streamable-HTTP MCP server on
  `127.0.0.1:<port>` with a bearer token; `Host` must match and a second
  `initialize` stops it. It requires `mcpCapabilities.http`.
- Identity variables are removed from the agent's environment.
