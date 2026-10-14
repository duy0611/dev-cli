# Agent memory across sandboxes: industry survey

How coding agents keep memory, and how platforms that run many agents in
ephemeral or parallel sandboxes carry it from one run to the next. Surveyed
2026-10-14. What this means for `dev` is in
[2026-10-14-agent-memory-for-dev.md](2026-10-14-agent-memory-for-dev.md).

Claims marked **[unverified]** come from search snippets or third-party
write-ups and were not confirmed against the vendor's own documentation.

## Where each agent keeps memory

| Agent | In the repository | Per user or machine |
|---|---|---|
| Claude Code | `CLAUDE.md`, `.claude/CLAUDE.md`, `.claude/rules/` | `~/.claude/CLAUDE.md`; auto memory in `~/.claude/projects/<project>/memory/` (a `MEMORY.md` index plus topic files, index capped at 200 lines / 25 KB); transcripts in `~/.claude/projects/<project>/<session-id>.jsonl`. `CLAUDE_CONFIG_DIR` moves all of it. |
| Codex CLI | `AGENTS.md`, root down to the working directory | `~/.codex/AGENTS.md`; generated memories in `~/.codex/memories/`, under `CODEX_HOME` |
| Gemini CLI | `GEMINI.md` and per-directory copies | `~/.gemini/GEMINI.md`; where `save_memory` writes changed between versions **[unverified]** |
| Cursor | `.cursor/rules/*.mdc`, `AGENTS.md` | Memories: sources disagree on whether the feature still exists **[unverified]** |
| Devin | Playbooks | Knowledge, reportedly deprecated in favour of Skills **[unverified]** |
| GitHub Copilot | Repository memory (below) | User preferences |
| Cline | `memory-bank/` Markdown, a documented convention | none |
| Aider | `CONVENTIONS.md` via `--read` | `.aider.chat.history.md`, not restored by default |

Claude's project key comes from the repository, so worktrees and
subdirectories of one repository share auto memory. Claude Code's docs are
explicit that memory "is loaded as context, not enforced configuration".

Anthropic's API memory tool (`memory_20250818`) is client-side: the model
issues file operations against `/memories` and the application decides where
they land. Its docs require the application to block path traversal, strip
sensitive data and cap file sizes.

## How sandbox platforms handle it

- **Claude Code cloud sessions.** The repository's `CLAUDE.md` arrives with the
  clone; user `CLAUDE.md`, user skills, user MCP config and auto memory do not.
  The documented workaround is a setup script that writes `~/.claude/CLAUDE.md`.
  Nothing flows back.
- **Claude Code sessions.** `CLAUDE_CODE_PROJECT_DIR_NAME` (from v2.1.234) pins
  the project directory name under `<config>/projects/`, independent of the
  working directory's path. Resuming one session in two terminals without
  forking interleaves both into one transcript. Transcripts are cleaned after
  30 days by default (`cleanupPeriodDays`).
- **Docker sandboxes.** The host's `~/.claude` is not visible; only project
  config is. Work leaves through a `sandbox-<name>` git remote. What persists
  between runs is not documented.
- **GitHub Copilot coding agent.** Copilot Memory, below.
- **Dagger container-use.** One branch and worktree per agent environment,
  auto-committed; the branch is what gets reviewed.
- **Letta Code.** "Context Repositories" (February 2026): memory is a git
  repository. Reflection runs in its own worktree and merges back; parallel
  subagents write in separate worktrees and merge through git.
- **Conductor, Crystal, Claude Squad, uzi, Vibe Kanban.** Worktrees and tmux for
  isolation, no shared memory. Claude Squad describes itself as "isolation, not
  coordination".
- **E2B, Modal, Daytona.** Filesystem persistence through pause/resume,
  snapshots, or volumes several sandboxes can mount. Memory is whatever happens
  to be on the filesystem **[unverified details]**.
- **Cursor background agents / Automations.** Per-automation memory, on by
  default, with Cursor warning that it persists across runs and is risky with
  untrusted input **[unverified]**.

## Patterns

### Memory as committed files

The one channel every platform carries: `AGENTS.md`, `CLAUDE.md`, `docs/`,
architecture decision records, Cline's memory bank. Changes arrive as diffs
someone reviews. Codex's documentation puts it plainly: keep required team
guidance in `AGENTS.md` or checked-in docs, and treat memories as "a helpful
recall layer".

### Repository memory with validation

GitHub Copilot Memory is the most developed answer for agents that cannot be
trusted with a shared store:

- repository facts are visible only in that repository, and only users with
  write access can create them;
- each fact is stored with citations into the code, which Copilot re-checks
  against the current branch before using it;
- facts unused for 28 days are deleted;
- user preferences stay with the user who made them.

### Handoff and progress files

Anthropic's harness for long-running agents: a first session writes `init.sh`,
`claude-progress.txt` and a feature list; every later session starts by reading
the progress file and `git log`, and ends with a commit and an updated progress
file. The feature list is JSON because the model is "less likely to
inappropriately change or overwrite JSON files compared to Markdown files".
The pattern is for sessions in sequence, not in parallel. Amp's `/handoff`
(a new thread seeded from the current one) is the interactive version.

### Git-backed memory for parallel writers

Letta's Context Repositories and container-use both give each agent its own
branch or worktree and merge through git. These are the only concrete answers
found to concurrent writes.

### Consolidation

Letta's sleep-time compute (`/sleeptime` in Letta Code) distils a session into
durable memory as a separate step. Anthropic's memory-tool docs recommend
periodically deleting stale files.

### External memory over MCP

- **mem0 / OpenMemory**: vector store (Qdrant) with add, search, list and
  delete tools. The local version was reportedly archived in July 2026
  **[unverified]**.
- **Zep Graphiti**: bi-temporal knowledge graph, `group_id` per tenant; the
  heaviest to run.
- **basic-memory**: Markdown files plus a SQLite index; the files can be kept in
  git.

All of them need a network path out of the sandbox.

### A shared agent-home volume

Pointing several containers' `CLAUDE_CONFIG_DIR` at one volume works
mechanically, but no vendor documents locking, transcripts interleave, and no
platform surveyed recommends it.

## Security

- OWASP's Top 10 for Agentic Applications (December 2025) lists memory and
  context poisoning as ASI06 **[unverified]**.
- NVIDIA's AI Red Team reported a compromised Go library writing a malicious
  `AGENTS.md` when it detected Codex; Cisco's "MemoryTrap" (April 2026)
  reportedly overwrote project memory files and global hooks config through a
  clone-and-install flow **[unverified]**.
- A poisoned memory file outlives the skill or dependency that wrote it, and
  spreads to everyone who pulls it.

The defences in use: review memory changes like code, record where each memory
came from, keep install scripts and dependencies from writing memory files,
expire what goes unused, and keep credentials out of memory. A store that one
sandbox writes and others read is a trust boundary, and the platforms that take
it seriously put a human or a permission check on the write side.

## Sources

- Claude Code: [memory](https://code.claude.com/docs/en/memory),
  [sessions](https://code.claude.com/docs/en/sessions),
  [cloud environments](https://code.claude.com/docs/en/cloud-environments)
- [Anthropic memory tool](https://platform.claude.com/docs/en/agents-and-tools/tool-use/memory-tool)
- [Effective harnesses for long-running agents](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)
- [GitHub Copilot Memory](https://docs.github.com/en/copilot/concepts/agents/copilot-memory),
  [on by default for Pro and Pro+](https://github.blog/changelog/2026-03-04-copilot-memory-now-on-by-default-for-pro-and-pro-users-in-public-preview/)
- [Codex memories](https://learn.chatgpt.com/docs/customization/memories?surface=app)
- [Docker sandboxes: Claude Code](https://docs.docker.com/ai/sandboxes/agents/claude-code/)
- [Letta Context Repositories](https://www.letta.com/blog/context-repositories/),
  [Letta memory configuration](https://docs.letta.com/configuration/memory),
  [Letta's next phase](https://www.letta.com/blog/our-next-phase/)
- [Dagger container-use](https://github.com/dagger/container-use)
- [Graphiti](https://github.com/getzep/graphiti),
  [basic-memory](https://github.com/basicmachines-co/basic-memory),
  [OpenMemory](https://mem0.ai/openmemory)
- [E2B persistence](https://e2b.dev/docs/sandbox/persistence)
