# Agent memory across `dev` containers

A new isolated container starts with an agent that remembers nothing. Often
that is right; sometimes what an earlier session learned would save the next
one an hour. This looks at what `dev` does with agent memory today and what the
industry ([survey](2026-10-14-agent-memory-industry-survey.md)) suggests it
should do. It is research, not a design: nothing here is decided.

## What happens today

- Every container gets a state volume, `dev-<ws>-<ctr>-state`, mounted at
  `/var/dev-state` unless created with `--no-persist-state`.
  `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `HERMES_HOME` and `OPENCODE_CONFIG_DIR`
  point into it (`internal/dcgen/state.go`), so agent memory and transcripts
  land there without `dev` knowing they exist —
  `/var/dev-state/claude/projects/<slug>/memory/` for Claude.
- The volume survives a rebuild and is deleted with the container: `docker
  volume rm` on local, the PVC on k8s.
- `create --from` copies the `PersistState` flag, not the volume's contents.
- Isolated containers are seeded once, on first `up`, from the working tree —
  uncommitted, untracked and ignored files included — plus a bundle of the
  repository's branches. The root `.git` and host git config stay behind.
- The only way out of an isolated container is `dev container fetch`: a git
  bundle, fetched with `transfer.fsckObjects`, into `refs/remotes/NAME/*`.
  Nothing extracts agent state.
- No spec covers memory, transcripts, or sharing between containers.
  `docs/specs/2026-09-21-persistent-agent-state.md` mentions an agent's
  accumulated history only in passing.

So memory survives a rebuild, dies with `remove`, and never moves sideways.

## What the industry points to

- Only memory committed to the repository reaches sandboxes on every platform
  surveyed. User-scoped and auto memory stays where it was written unless
  something seeds it in.
- Where a shared store exists, the careful platforms gate writes (Copilot:
  write access, citation checks, 28-day expiry) or route them through git
  (Letta, container-use).
- A shared live store is a known way for one compromised agent to reach every
  other one. Memory is read as instructions.

That last point bites hardest here. Isolated mode exists because the agent
inside is not trusted with the host; a memory file it writes is exactly as
trustworthy as a commit it makes, and `dev` already refuses to take commits
without a fetch the operator runs.

## Options

### 1. Committed files, through `fetch` (no code)

Agents write what is worth keeping to `AGENTS.md`, `docs/`, or a progress file
in the Anthropic style, and commit it. It comes out with `dev container fetch`
and the operator reviews it with the rest of the branch.

A gitignored notes file also works in one direction: the seed copies ignored
files, so a new isolated container sees it without anything being committed.

This is what every platform relies on, it fits the isolated trust model as it
stands, and it costs nothing. It does not capture what the agent stores on its
own — Claude's auto memory, Codex's memories.

### 2. Seed memory into a new container

Something like `create --memory-from <container|dir>`, copying memory onto the
state volume before the first agent runs. Host to container is the safe
direction: it is the same shape as the workspace seed.

Open questions:

- Claude names the project directory after the workspace path. If two
  containers mount at different paths the slug differs, and a copied memory
  directory is never read. `CLAUDE_CODE_PROJECT_DIR_NAME` should fix this by
  pinning the name; check it against the real CLI before relying on it.
- Which agents' memory to carry, and where each keeps it. Codex's
  `memories/` sits under `CODEX_HOME`; the others need checking.
- Memory only, or settings and transcripts as well. Settings are already
  rebuilt from `agents.yaml`; transcripts are large and path-bound.

### 3. Export memory out of a container, for review

Something like `dev container memory export NAME`, copying the memory
directories to a staging area on the host. The result is untrusted, as a
fetched bundle is: the operator diffs it and promotes what is good into a
library that option 2 seeds from.

Together, 2 and 3 are Copilot's write gate done locally: memory moves between
sandboxes only through the operator.

Open questions:

- Path validation on the way out — symlinks, `..`, file count and size — as the
  memory-tool docs require of any application storing model-written files.
- Whether `container remove` should refuse while memory is unexported, as it
  does for unfetched branches. Probably not: most memory is disposable, and a
  refusal nobody acts on teaches `--force`.

### Not recommended

- **One state volume shared by parallel containers.** One compromised agent
  poisons all of them, concurrent writers interleave transcripts, and it
  undoes isolated mode.
- **Carrying transcripts to resume a session elsewhere.** Path-bound, large, and
  it carries every piece of context the old session had, injected content
  included. A progress file says the same in a page the operator can read.
- **An MCP memory service.** It needs a network path out of the sandbox and a
  service to run, for what files and git already do.

## Suggested order

Start with option 1: write the convention into the project's `AGENTS.md` and
see how far it goes. If agent-private memory turns out to matter, design 2 and
3 together in `docs/specs/`, starting by checking `CLAUDE_CODE_PROJECT_DIR_NAME`
and where each supported agent keeps its memory.
