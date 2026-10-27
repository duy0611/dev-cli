# Running an agent team with `dev container start-agent-team`

Experimental, like `dev diff`: the command, its flags, the in-container
dispatch interface and the team folder may change without a migration.

## Problem

The operator wants a big model (Opus, Kimi) to do the planning, spec writing
and reviewing, and a cheaper model (Haiku, DeepSeek flash, sometimes Sonnet or
Kimi) to do the implementing. The point is cost. Today this is done by
prompting: start one agent, then tell it "launch an opencode agent to implement
this" or "spawn a Claude agent to review the change", and have it write tasks
into a markdown file. Every part of that is clunky:

- the setup is re-explained in every session;
- the model for each worker is decided in a prompt, each time;
- workers the agent started itself are invisible to `dev` and to Herdr;
- workers go quiet, and the orchestrator loses track of what is finished.

Collisions between workers are not the problem: the usual team is one editor
and one reviewer.

## Goal

One command starts an orchestrator ("lead") session that already knows it has
a team: which roles exist, which model each runs on, and how to hand work to
them. Nothing about the team is explained in a prompt.

Success: the operator runs

```sh
dev container start-agent-team api \
  --lead claude --lead-model opus \
  --workers opencode \
  --worker coder=deepseek/deepseek-v4.1-flash \
  --worker reviewer=anthropic/claude-sonnet-5-5
```

and lands in an ordinary interactive Claude session on Opus, registered in
Herdr as `start-agent` does today. They brainstorm a spec with it, approve it
(sometimes write the plan themselves), and walk away. The lead splits the plan
into tasks, hands each to the `coder` (opencode on DeepSeek), sends each result
to the `reviewer` (opencode on Sonnet), and loops until done. When the operator
comes back, they review the code on the host as usual, and the team folder
shows every task, who did it, what it cost, and any task the lead had to take
over.

## How a session runs

1. **Launch.** Interactive lead session, exactly like `start-agent` (same
   container start, env, ssh relay, kube token, Herdr naming, audit record),
   plus the team wiring below. No worker runs yet.
2. **Planning.** The operator and the lead work interactively. No goal is
   given on the command line; it arrives later in conversation.
3. **Execution.** After the operator approves a spec or plan, the lead hands
   tasks to workers. Each hand-off runs one worker **headless, to completion**,
   and returns its result to the lead. Workers are created per task, on
   demand, and exit when the task ends.
4. **Review.** Unchanged: the operator reviews on the host; `dev diff` and the
   guarded-mode commit flow work as before.

Workers never talk to the operator. Everything goes through the lead.

### Why synchronous hand-off, not a mailbox

Research (Claude Code agent teams, MCP Agent Mail, tmux runners) shows the
common cross-tool channel is a shared mailbox, but an idle agent does not wake
on mail, and the operator runs teams unattended. Each tool's own delegation
(Claude subagents, opencode `task`, Hermes `delegate_task`) is call-and-return,
which is why it is reliable. This design makes the cross-tool hand-off
call-and-return too. Native delegation alone was rejected because it cannot
mix tools (a Claude lead cannot run opencode workers natively).

## Command

```
dev container start-agent-team NAME
    --lead claude|opencode   [--lead-model MODEL]
    --workers claude|opencode
    --worker ROLE=MODEL             (repeat, 1 to 3 times)
    [--worker-max-turns N] [--worker-timeout DURATION]
    [--agent-name NAME] [--workspace W] [--kube-token …] [--ssh-agent|--no-ssh-agent]
    [-- LEAD-ARGS...]
```

- Flags every time; no saved team in `agents.yaml`. Teams change case by case.
- `--lead` and `--workers` name tools from `internal/agent`; only `claude`
  and `opencode` are accepted (`hermes` and `codex` are refused; see Scope).
  The lead and workers may be different tools; all workers are the same tool.
- `--lead-model` is optional; without it the lead uses its tool's default.
- `--worker ROLE=MODEL`: `ROLE` is one of the three built-in roles; each at
  most once; an unknown or repeated role is a usage error. `MODEL` is passed
  through in the worker tool's own syntax (`opencode`: `provider/model`;
  `claude`: alias or full ID). `dev` does not
  validate model names. Provider API keys come from existing `dev workspace
  set` settings.
- Flags follow the repo convention: a scripted run missing a required flag
  fails naming it; nothing is prompted that has a flag.

`docs/USAGE.md` gains a walkthrough and a reference entry, marked experimental.

## Roles

Three built-in roles, no custom roles, no per-role descriptions:

| Role | Does | May edit files |
|---|---|---|
| `coder` | implements a task; writes and runs its tests | yes |
| `reviewer` | reviews a finished change, reports problems | no |
| `researcher` | reads code or the web, reports back | no |

A team is a lead plus any one to three of these. A role that is missing is
handled however the lead normally would: itself, or with its tool's own
subagents; `dev` stays out of that. The lead's own subagents keep whatever
model its tool gives them; `dev` does not repin them.

Read-only roles are told not to edit. Where the worker tool can lock this
cheaply, it is also locked (Claude `--disallowedTools` for edit/write tools,
opencode agent `permission.edit: deny`); where it cannot, the instruction
alone stands (builder's call on the exact mechanism per tool).

Only one `coder`: parallel edits, collisions and worktrees are out of scope.
The lead may run `researcher` while `coder` works, since only one edits.

## The standing instruction

At launch the lead receives a standing instruction, built from the roles
actually present, delivered through each tool's own channel (e.g. Claude
`--append-system-prompt`; builder picks the opencode equivalent,
such as opencode's `instructions` config via an env-supplied config). It is never written into the operator's working
tree. A lead tool with no such channel is refused as a lead.
It says, in substance:

- You plan, write specs and review. Once the operator approves a spec or plan,
  do not implement it yourself: break it into tasks with acceptance criteria
  and hand each to a worker.
- Your team: each present role, its model, what it does. Use these workers for
  coding, reviewing and research, not your built-in subagents — those run at
  your price.
- After a `coder` task: send the change to `reviewer` if present, and run the
  acceptance checks yourself.
- If a task falls short, send it back to the same worker with the specific
  problems (failing tests, review findings). **At most two retries.** If the
  third attempt still falls short, implement it yourself and record the
  takeover.
- How to hand off and read results (the dispatch interface below).

The "lead doesn't code" rule is soft by the operator's choice: it is an
instruction, made visible in the team folder, not enforced.

## Dispatch

`dev` ships a small in-container dispatch program, delivered the way the ssh
relay binary is (`internal/relaybin`). The lead uses it to run a task on a
role. For each task it:

1. writes the task text to the team folder;
2. runs the worker tool headless with the role's pinned model, auto-approved
   permissions, the per-task limits, and streaming output;
3. tees the stream to the task's log;
4. on exit, writes the result, status, session id and cost (when the tool
   reports it), and returns the worker's final answer to the lead.

Worker invocations (verified against the CLIs in the dev container on
2026-10-26, except where noted):

| Tool | Headless run | Model | Permissions | Limits | Stream |
|---|---|---|---|---|---|
| claude | `claude -p TASK` | `--model` | `--permission-mode auto` | no turn flag (dispatch counts turns in the stream); `--max-budget-usd` | `--output-format stream-json` |
| opencode | `opencode run TASK` | `-m provider/model` | `--auto` | agent `steps` (unverified how to set per run) | `--format json` |

Workers' permissions are pre-approved because nobody is there to answer a
prompt. The container's guarded or isolated protections are the fence, as for
any agent today.

Dispatch is a Go binary built and embedded like `cmd/dev-relay`, for the same
architectures, and holds the stream parsers for both tools (writing this
up, my call).

**Left to the builder:** how the lead calls dispatch — an MCP server it is
configured with at launch, or a shell command described in the standing
instruction. Either must cope with tasks that run longer than the lead's tool
call timeout (Claude's Bash and MCP calls time out), e.g. start-then-wait with
a task id, or a blocking wait with a bounded poll. It must work identically for
both lead tools, and must not depend on `agents.yaml` (which applies on
create/rebuild, not per launch).

## Guardrails against cost

The operator's worst outcomes are a worker looping and burning tokens, and the
lead doing the coding itself.

- **Per-task hard limits:** a turn cap (tool's own flag) and a wall-clock
  timeout enforced by dispatch, which kills the worker. Defaults chosen by the
  builder; `--worker-max-turns` and `--worker-timeout` override them.
- **No dollar cap** in this version: only Claude has one (`--max-budget-usd`),
  and turns plus wall-clock work for both (my call).
- **A cut-off task returns a plain failure** to the lead — "hit the turn
  limit" / "timed out" — with the tail of its log, and counts as an attempt.
- **Cost per task** recorded when the tool reports it.
- **Lead edits are visible:** the team folder records which files each worker
  changed; edits outside worker runs (other than specs and plans) are the
  lead's.

## Watching progress

Works with or without Herdr, like every `dev` command.

- **Live log per task** in the team folder, from the worker's stream.
- **From the host (required in the first version):** a command such as `dev container team logs NAME
  [--follow]` renders the active (or a given) task's log as readable lines —
  tool calls, edits, messages — not raw JSON. A listing of tasks with role,
  status, attempts, cost and takeovers belongs alongside it (builder names the
  subcommands).
- **Herdr, nice to have:** if Herdr can open a pane on request, each worker
  run gets a registered pane tailing its log. Not required; Herdr is not
  available inside the container, so this would be driven from the host.
- **Taking over afterwards:** each task records its session id so the
  operator can resume a worker's session interactively (`claude --resume ID`;
  opencode `run --session ID`). Read-only while it runs.

## Team folder

One folder per team run holding: the team's configuration, one entry per task
(task text, attempts, log, result, status, session id, cost, files changed),
and the takeover record. Location is the builder's call, with constraints: not
in the operator's git working tree (it would pollute their diff and commits),
readable from the host through the provider's exec, and present in guarded
containers where `.git` is read-only.

Calls made while writing this (the operator may overrule):

- **In the container, no new host mount.** On the persisted state volume
  when the container has one, so it survives a rebuild; otherwise a rebuild
  loses it, which is acceptable for an experimental feature. No change to the
  mount invariants.
- **One run per launch.** Each `start-agent-team` creates a new run folder
  keyed by container and launch time; the list command shows past runs. A
  lead resumed in the same session (`--continue`) keeps reading the same run
  folder through the dispatch interface.
- **A lead that dies mid-task** does not stop the worker: it runs to
  completion or its limits and writes its result. A result nobody collected
  stays in the folder, marked uncollected.
- **Files changed are best-effort:** `git status` before and after each
  worker run (reading works in guarded containers), falling back to the
  worker's stream where git is absent. Attribution of lead edits is
  best-effort too; it is a visibility aid, not an audit.

## Scope

- **Claude Code and opencode only,** as lead and as worker. Hermes is out of
  this version: it already orchestrates and delegates on its own
  (`delegate_task`), and its unattended approval mode could not be set from
  the command line. It stays available through `start-agent`.
- Local provider is the target. The k8s provider is experimental; the command
  should work there if dispatch delivery does, but it is not required.
- Guarded, isolated and unprotected containers all work; nothing here touches
  git. Guarded containers cannot create worktrees, which is fine with one
  coder.

## Non-goals

- Workers talking to the operator, or to each other directly.
- More than one worker per role, parallel coders, worktrees per worker.
- Custom roles or per-role descriptions.
- Saved team presets in `agents.yaml`.
- Repinning the lead's built-in subagents.
- A cross-agent protocol (A2A, ACP, MCP mailbox). Considered and rejected
  above.

## Anti-goals

- A worker that loops unbounded. Every task has a hard stop.
- A team that only works with Herdr.
- A design that needs the team explained in a prompt.
