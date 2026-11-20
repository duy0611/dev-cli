# A Chief of Staff agent team, with model tiers

Builds on `2026-10-26-agent-team-design.md`; where the two disagree, this one
wins. Experimental, like the rest of `start-agent-team`: roles, flags, the
worker prompts, the report format and the tier settings may change without a
migration.

## Intent

The operator wants `start-agent-team` to follow a Chief of Staff pattern: one
lead that decomposes, routes, verifies and escalates, and a set of specialist
workers, each with its own mindset and hard limits, so that they audit each
other's work instead of echoing it. Today workers get only the task text, there
are three roles, the lead quietly takes over a failing task, and every worker
model must be named exactly on each launch.

Success looks like:

```sh
dev container team tier set opencode small=deepseek/deepseek-v4.1-flash \
  medium=moonshot/kimi-k3 large=anthropic/claude-opus-5-5   # once

dev container start-agent-team api --lead claude --lead-model large --workers opencode \
  --worker researcher --worker architect --worker coder --worker tester --worker reviewer
```

The lead plans with the operator, then runs research → design → build → test →
review through the workers present, sends failures back with reproductions,
and stops to ask the operator when a task still falls short after two retries.

## Goals

- Five worker roles: `researcher`, `architect`, `coder`, `tester`, `reviewer`.
- Every worker runs with a role persona and hard boundary, and ends with a
  fixed-shape report.
- The lead is a Chief of Staff: it never does leaf work that changes files, and
  escalates instead of taking over.
- Model tiers `small`, `medium`, `large`, with per-role defaults, saved
  per-tool mappings, and a one-off override.

## Non-goals

- Enforcing the report format (dispatch passes it through; see Approaches).
- Locking the lead's tools.
- Path-scoped edit locks (tester to test files, coder away from assertions):
  neither tool supports them; the persona is the fence.
- More than one worker per role, custom roles, mixing worker tools in a team.

## Anti-goals

- A lead that silently finishes failing work itself.
- Coder and tester editing the tree at the same time.
- A literal model name that stops working because it collides with a tier.

## Approaches

Common to all: five roles, personas, report format, escalation, one editor at a
time.

- **A. Personas plus per-role tool profiles (chosen).** Lead keeps its tools
  and is told never to do leaf work; workers get the tool locks each tool can
  enforce; reports pass through untouched. Path-level rules rest on the
  persona alone.
- **B. A, plus locking the lead's edit tools.** Rejected: the lead needs the
  shell to call `dev-team`, so the lock has a hole, and it blocks the operator
  asking for a one-line fix.
- **C. A, plus dispatch parsing and enforcing the report.** Rejected for now:
  a formatting slip would fail good work and burn a retry. Can be added later.

The operator chose A.

## Roles

| Role | Mandate | Mindset | Hard boundary | Lock (claude / opencode) |
|---|---|---|---|---|
| `researcher` | Gather facts: code, docs, constraints | Skeptical analyst; cites every claim, flags unknowns as open questions | No edits | edit tools removed / edit, write, shell, subagent, execute denied (as today) |
| `architect` | Turn requirements and research into an atomic spec: files, interfaces, inputs, outputs, error states, ordered checklist | Minimalist systems thinker | Specs only, returned in its report; never writes files | same as researcher |
| `coder` | Implement the spec, test-first: writes and runs its own unit tests | Disciplined, literal builder; flags spec flaws instead of inventing workarounds | Never self-approves; never weakens or changes a test it did not write in this task; no unrelated refactors | none (as today) |
| `tester` | Break the implementation: add adversarial tests against the spec, not the code, on top of the coder's own; run the suite | Adversarial; hunts nulls, boundaries, malformed input, races | Never edits non-test source; reports reproducible failures | none: it needs edit and shell; the persona is the fence |
| `reviewer` | Audit logic, security, maintainability, fit to intent | Strict principal engineer | No edits; findings split into blockers and nits; never blocks on style | same as researcher (as today) |

A team is a lead plus any one to five of these, each at most once.

**One editor at a time.** `coder` and `tester` both edit, so the existing
single-coder gate becomes a single-editor gate: a `start` of either is refused
while the other (or another of the same role) is running, naming the running
task. `researcher`, `architect` and `reviewer` may run alongside anything.

## Worker personas

Each worker gets a role prompt on top of the task text, through the tool's own
channel: Claude `--append-system-prompt`; opencode the agent's prompt in the
`OPENCODE_CONFIG_CONTENT` agent definition that already carries its
permissions. The task text stays on stdin. Neither the prompt nor the task ever
lands in the operator's working tree.

The prompt states the role's mandate, mindset, hard boundary, and the report
format. The table above is the content; the wording is the builder's.

### Report format

Every persona ends with: finish with one fenced JSON block, the last thing in
the answer:

```json
{
  "status": "done | failed | blocked",
  "summary": "what was done or found, briefly",
  "artifacts": ["paths changed or produced, or spec/doc sections"],
  "blockers": ["what must change before this can ship, each self-contained"],
  "nits": ["optional polish (reviewer only)"]
}
```

Tester blockers each carry exact reproduction steps. Researcher blockers are
open questions for the lead. Dispatch passes the final answer through
unchanged; it does not parse or validate the block. (JSON over labelled lines:
my call, following the operator's pattern.)

## The lead: Chief of Staff

The standing instruction is rebuilt from the roles present. In substance:

- You are the Chief of Staff: pragmatic, scope-obsessed. You decompose,
  route, verify against acceptance criteria, and escalate. Cut scope creep.
- The operator approves the plan with you; that is the only approval point.
  An architect's spec refines the approved plan and needs no further
  approval, unless it would change what was approved: then ask.
- Once the operator approves a spec or plan, never do leaf work yourself:
  break it into bounded tasks with acceptance criteria and hand each to a
  worker.
- The flow, through the roles present: researcher → architect → coder →
  tester → reviewer. Hand the tester the spec and acceptance criteria, not
  the coder's account of what it did.
- **Missing roles.** Reading and thinking work with no worker (research,
  design, review) you do yourself; you may use your own subagents for it.
  Implementing with no coder you never do: stop and ask the operator. With
  no tester, the coder's own test-first unit tests stand; nothing is asked. Only the roles present are named; the
  instruction gives the rule for absent roles without listing them.
- "Use these workers, not your built-in subagents" stays, for the roles
  present.
- A tester failure or a reviewer blocker goes back to the coder as a retry of
  the coder's task, with the failures or blockers on stdin. Reviewer nits do
  not trigger a retry.
- A coder that reports `blocked` on a spec flaw: fix the spec (through the
  architect if present); if the fix changes the approved scope, ask the
  operator. The re-run is a retry of the coder's task and counts as an
  attempt.
- A tester, reviewer, researcher or architect task that fails or hits its
  limits is retried on the same worker, under the same cap.
- Researcher open questions: answer them from the approved plan or the code;
  if neither settles one, ask the operator rather than invent a requirement.
- A worker answer with no JSON block, or a malformed one: judge from the
  prose. A missing block from a tester or reviewer is never a pass.
- **At most two retries per task.** If the third attempt still falls short,
  stop and ask the operator what to do. Never take the work over on your
  own. If the operator tells you to do it yourself, record it with
  `dev-team takeover ID --reason "..."`.
- Read the JSON block at the end of each worker's answer for its status and
  blockers.
- The dispatch commands, and the bounded-wait rule, as today.

The retry cap needs no new counting: tester and reviewer findings re-run the
coder's task through `start coder --retry ID`, whose attempts dispatch already
records.

## Model tiers

### Selecting

- `--worker ROLE` with no `=` uses the role's default tier:

  | Role | Default tier |
  |---|---|
  | researcher | small |
  | architect | large |
  | coder | medium |
  | tester | medium |
  | reviewer | large |

- `--worker ROLE=VALUE`: if `VALUE` is `small`, `medium` or `large` it is a
  tier; anything else is a literal model passed through as today.
  `ROLE=` with an empty value stays a usage error.
- `--lead-model` takes a tier or a literal; omitted, the lead uses its
  tool's default as today.
- Tier names: `small`, `medium`, `large` (my call: the operator said `high`;
  `large` pairs with the other two).

### Resolving

A tier resolves for a tool (the workers' tool for `--worker`, the lead's tool
for `--lead-model`), first match wins:

1. `--tier TIER=MODEL`, repeatable, one per tier. Applies to the workers' tool
   only (my call: lead and workers may be different tools, and a one-off lead
   model is just a literal `--lead-model`).
2. The saved mapping for that tool.
3. The built-in mapping: claude `small=haiku`, `medium=sonnet`, `large=opus`
   (Claude's own aliases, which track the newest models). opencode has none:
   its models depend on the providers the operator configured.

An unresolved tier is a usage error, exit 2, before anything starts, naming
both fixes, e.g.: `tier "medium" has no model for opencode: run dev container
team tier set opencode medium=MODEL, or pass --tier medium=MODEL`.

### Saved mappings

New subcommands under the existing `dev container team`:

```
dev container team tier set TOOL TIER=MODEL [TIER=MODEL...]
dev container team tier unset TOOL TIER [TIER...]
dev container team tier list [TOOL]
```

- `TOOL` is `claude` or `opencode`; `TIER` one of the three. Anything else is
  a usage error.
- `list` shows every tool × tier with its model and source (`saved`,
  `built-in`, or none), so the operator sees what a launch would pick.
- Stored in the SQLite database via a new migration; `internal/store` owns
  the table and queries. Not per project, not per workspace.
- Each `set` and `unset` goes in the audit log like other state changes.

### Recording

At launch, `dev` prints each role's tier and resolved model (and the lead's)
before the session starts. `config.json` records, per role and for the lead,
what was asked (tier or literal) and the model used. Its shape is the
builder's call; the run folder is experimental and needs no migration.

## Surfaces that change

- `internal/team`: roles, validation (1 to 5 workers), the editor gate,
  per-role locks, persona prompts for both worker adapters, the standing
  instruction, tier resolution.
- `internal/cli/team.go`: `--worker` parsing and help, `--tier`,
  `--lead-model` resolution, the `tier` subcommands, launch print-out.
- `cmd/dev-team`: the role list in its usage and errors. The embedded binary
  is rebuilt with `make team`.
- `internal/store` + migration: the saved tiers.
- `docs/USAGE.md`: walkthrough and reference for the new roles, the report
  format, escalation, tiers and `team tier`. Experimental marking kept.
- `docs/specs/2026-10-26-agent-team-design.md` is not edited; this spec
  supersedes its Roles, standing-instruction and `--worker` sections.

## Left to the builder

- The exact wording of each persona and of the standing instruction, within
  the content above.
- The opencode agent key that carries a role prompt: check it against the
  real opencode CLI before relying on it (convention: check flags against the
  real CLI). Claude's `--disallowedTools` is variadic and must stay last in
  argv, after `--append-system-prompt`.
- The table layout, the `config.json` shape, and the print-out format.
- Tests per the repo conventions: stub executables, store tests on a temp
  file, test first.
