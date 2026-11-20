# Chief of Staff Agent Team Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Five worker roles with personas and per-role tool locks, a Chief of Staff lead that escalates instead of taking over, and model tiers (`small`/`medium`/`large`) with saved per-tool mappings.

**Architecture:** Roles, personas, the editor gate, the standing instruction and tier resolution live in `internal/team`; saved tiers in `internal/store` behind a new migration; `internal/cli/team.go` parses flags, resolves tiers and adds `dev container team tier`. `cmd/dev-team` only learns the new role names.

**Tech Stack:** Go, cobra, SQLite (migrations replay against Postgres), stub executables for tool tests.

**Spec:** `docs/specs/2026-11-20-chief-of-staff-team-design.md`

## Global Constraints

- Roles, in this order everywhere: `researcher`, `architect`, `coder`, `tester`, `reviewer`. 1 to 5 workers, each role at most once.
- Tiers: `small`, `medium`, `large`. Claude built-ins: `small=haiku`, `medium=sonnet`, `large=opus`. opencode: none.
- Default tiers: researcher small, architect large, coder medium, tester medium, reviewer large.
- Read-only (locked) roles: researcher, architect, reviewer. Editor roles (gate + no lock): coder, tester.
- Report block keys: `status` (`done | failed | blocked`), `summary`, `artifacts`, `blockers`, `nits` (reviewer only).
- Experimental: no migration of run folders; old `config.json` must still load.
- Repo rules from `AGENTS.md`: test first, stub executables not mocks, store tests on a temp file, comments only for the non-obvious why, `make lint && make test` before every commit, Conventional Commit subjects, no tool-attribution trailers. Commit as the operator: `GIT_AUTHOR_NAME="Duy Nguyen" GIT_COMMITTER_NAME="Duy Nguyen" git commit ...`. If commit fails with `Read-only file system`, leave changes uncommitted and say so.

## Review Focus

1. **Tier typed in another case** (`--worker coder=Medium`): a person expects the tier. `ParseTier` is case-insensitive. Test in Task 2.
2. **Resuming an older run** (`--continue` on a run whose `config.json` predates `tiers`): dispatch must still load it. Test in Task 2.
3. **Lead and workers on different tools:** `--tier` must not change the lead's model. Test in Task 6.
4. **`team tier set` with a bad tool or tier** (`hermes`, `huge`, `medium=`): usage error, nothing stored. Test in Task 6.
5. **A dead tester's stale running record** must not block a coder forever. Test in Task 1.

---

### Task 1: Five roles and the single-editor gate

**Files:**
- Modify: `internal/team/config.go`, `internal/team/dispatch.go` (gate, ~lines 107-146, 197-230), `cmd/dev-team/main.go:141-145`
- Test: `internal/team/config_test.go`, `internal/team/dispatch_test.go`, `cmd/dev-team/main_test.go`

**Interfaces:**
- Produces: `RoleArchitect Role = "architect"`, `RoleTester Role = "tester"`; `func AllRoles() []Role` (the fixed order); `func (r Role) Valid() bool` (replaces unexported `valid`); `func (r Role) ReadOnly() bool` (researcher, architect, reviewer); `func (r Role) Edits() bool` (coder, tester). `Config.Roles()` keeps its signature, new order.

- [ ] **Step 1: Write the failing tests**
  - `TestRolesOrder`: config with all five → `[researcher architect coder tester reviewer]`.
  - `TestValidateWorkerCount`: 5 workers valid; 0 → error containing `want 1 to 5`.
  - `TestParseWorkers`: `architect=x` and `tester=x` accepted; unknown role still refused.
  - `TestRoleLocks`: `ReadOnly()` true exactly for researcher, architect, reviewer; `Edits()` true exactly for coder, tester.
  - `TestTesterRefusedWhileCoderRuns` and `TestCoderRefusedWhileTesterRuns`: error is `a coder is already running as task t1; wait for it to finish` (role of the running task, its id).
  - `TestReviewerAlongsideTesterAllowed`, `TestArchitectAlongsideCoderAllowed`.
  - `TestDeadTesterDoesNotBlockCoder`: a running tester record whose pid is dead is marked failed and the coder starts.
  - dev-team: `start architect` and `start tester` accepted; unknown role message is `unknown role "x": want researcher, architect, coder, tester or reviewer`.
- [ ] **Step 2: Run** `go test ./internal/team/ ./cmd/dev-team/` — expect FAIL (undefined roles, wrong order).
- [ ] **Step 3: Implement.** Rename `runningCoder` → `runningEditor(olderThan string)`, matching `task.Role.Edits()`; both gate checks run when `role.Edits()`; `coderRunningError` → `editorRunningError(t Task)` formatting `a %s is already running as task %s; wait for it to finish`. Validate's count message `team has %d workers, want 1 to 5`. dev-team uses `role.Valid()` and builds its message from `AllRoles()`.
- [ ] **Step 4: Run** `make lint && make test` — PASS.
- [ ] **Step 5: Commit** `feat(team): add architect and tester roles behind a single-editor gate`

### Task 2: Tier resolution

**Files:**
- Create: `internal/team/tier.go`, `internal/team/tier_test.go`
- Modify: `internal/team/config.go` (ParseWorkers bare role; Config/Tool fields)
- Test: `internal/team/config_test.go`

**Interfaces:**
- Consumes: `Role`, `AllRoles()` from Task 1.
- Produces:
  ```go
  type Tier string
  const (TierSmall Tier = "small"; TierMedium Tier = "medium"; TierLarge Tier = "large")
  func AllTiers() []Tier
  func ParseTier(s string) (Tier, bool)                 // case-insensitive
  func DefaultTier(r Role) Tier
  func BuiltinModel(tool string, t Tier) (string, bool)
  func ParseTierOverrides(specs []string) (map[Tier]string, error) // --tier TIER=MODEL
  type TierSource string // "flag", "saved", "built-in"
  type Resolver struct {
      Tool      string
      Overrides map[Tier]string // from --tier; nil for the lead
      Saved     map[Tier]string
  }
  // Resolve returns the model for a --worker/--lead-model value. A non-tier
  // value comes back unchanged with tier "" and source "".
  func (r Resolver) Resolve(value string) (model string, tier Tier, source TierSource, err error)
  ```
  `ParseWorkers` keeps its signature; a bare `ROLE` maps to `string(DefaultTier(role))`. `Config` gains `Tiers map[Role]Tier \`json:"tiers,omitempty"\``; `Tool` gains `Tier Tier \`json:"tier,omitempty"\``. `Config.Models` stays the resolved model per role (dispatch reads it unchanged).

- [ ] **Step 1: Write the failing tests**
  - `TestParseTier`: `small`, `MEDIUM`, `Large` parse; `huge`, `""` don't.
  - `TestDefaultTier`: the five defaults from Global Constraints.
  - `TestBuiltinModel`: claude three built-ins; opencode none.
  - `TestResolveOrder`: flag beats saved beats built-in, with the source reported.
  - `TestResolveLiteralPassesThrough`: `claude-sonnet-5-5` and `openrouter/a=b` come back unchanged, tier `""`.
  - `TestResolveUnmapped`: opencode `medium`, no saved/override → error exactly `tier "medium" has no model for opencode: run dev container team tier set opencode medium=MODEL, or pass --tier medium=MODEL`; with `Overrides == nil` the `, or pass --tier medium=MODEL` part is absent.
  - `TestParseTierOverrides`: `medium=x` ok; `huge=x`, `medium=`, `medium`, repeated `medium` → errors naming the value.
  - `TestParseWorkersBareRole`: `coder` → `medium`, `architect` → `large`; `coder=` still an error.
  - `TestConfigLoadsWithoutTiers`: unmarshal a pre-change `config.json` (three roles, no `tiers`) and `Validate()` passes.
- [ ] **Step 2: Run** `go test ./internal/team/` — expect FAIL.
- [ ] **Step 3: Implement** in `tier.go` as specified.
- [ ] **Step 4: Run** `make lint && make test` — PASS.
- [ ] **Step 5: Commit** `feat(team): resolve worker models from tiers`

### Task 3: Saved tiers in the store

**Files:**
- Create: `internal/store/migrations/0012_team_tiers.sql`, `internal/store/teamtier.go`, `internal/store/teamtier_test.go`

**Interfaces:**
- Produces (plain strings; the store does not import `team`):
  ```go
  func (s *Store) SetTeamTier(tool, tier, model string) error   // upsert
  func (s *Store) UnsetTeamTier(tool, tier string) error        // ErrNotFound when absent
  func (s *Store) ListTeamTiers(tool string) (map[string]string, error) // tier -> model
  ```
- Migration: `CREATE TABLE team_tiers (tool TEXT NOT NULL, tier TEXT NOT NULL, model TEXT NOT NULL, PRIMARY KEY (tool, tier));` with the header comment style of `0011_buzz_binary.sql` (no SQLite-only syntax).

- [ ] **Step 1: Write the failing tests** (temp-file store, as `store_test.go` does): set then list; set again replaces; tools are separate; unset removes; unset absent → `ErrNotFound`; list of an unknown tool → empty map, no error.
- [ ] **Step 2: Run** `go test ./internal/store/` — expect FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint && make test` — PASS.
- [ ] **Step 5: Commit** `feat(store): save team model tiers per tool`

### Task 4: Worker personas and per-role locks

**Files:**
- Create: `internal/team/persona.go`, `internal/team/persona_test.go`
- Modify: `internal/team/claude.go:164-187`, `internal/team/opencode.go:30-75`
- Test: `internal/team/claude_test.go`, `internal/team/opencode_test.go`

**Interfaces:**
- Consumes: `Role.ReadOnly()` from Task 1.
- Produces: `func RolePrompt(r Role) string` — mandate, mindset, hard boundary and report format per the spec's Roles table and "Report format" section.

- [ ] **Step 1: Verify the opencode prompt key.** Find the v2 agent field that carries a system prompt (`opencode` 2.3.0 is installed; read its config schema or run it with `OPENCODE_CONFIG_CONTENT` containing an agent with a marker prompt and confirm the marker reaches the model's system prompt, e.g. via `--print-logs --log-level debug`). Record the finding in one line in the commit body. If no such field exists, prepend the persona to the task text on stdin for opencode only, separated by a blank line and `---`.
- [ ] **Step 2: Write the failing tests**
  - `TestRolePromptReportFormat`: every role's prompt contains `"status"`, `"summary"`, `"artifacts"`, `"blockers"`, and `done | failed | blocked`; only reviewer's contains `"nits"`.
  - `TestRolePromptBoundaries`: coder contains `test-first` and `never weaken or change a test you did not write`; tester contains `reproduction` and `never edit non-test source`; architect contains `never write files`; researcher and reviewer contain `never edit`; reviewer contains `blockers` and `nits` and `style`.
  - Claude: every role's argv has `--append-system-prompt` followed by `RolePrompt(role)`; for researcher, architect, reviewer `--disallowedTools Edit Write MultiEdit NotebookEdit` are the last argv elements; coder and tester have no `--disallowedTools`.
  - opencode: architect's agent has the five deny permissions (as reviewer today); tester and coder have none; every role's config carries the prompt (or, on the fallback, stdin starts with it).
- [ ] **Step 3: Run** `go test ./internal/team/` — expect FAIL.
- [ ] **Step 4: Implement.** Replace the `RoleReviewer || RoleResearcher` checks with `w.Role.ReadOnly()`.
- [ ] **Step 5: Run** `make lint && make test` — PASS.
- [ ] **Step 6: Commit** `feat(team): give each worker a role persona and report format`

### Task 5: The Chief of Staff standing instruction

**Files:**
- Modify: `internal/team/instruction.go`
- Test: `internal/team/instruction_test.go`

**Interfaces:**
- Consumes: roles from Task 1. `Instruction(c Config, dispatch string) string` keeps its signature.
- `roleDuty` and `roleWork` gain architect (`writes the spec from the approved plan, never edits.` / `design`) and tester (`tries to break a change with tests against the spec, never fixes source.` / `testing`).

- [ ] **Step 1: Write the failing tests** (substring assertions, rewrite existing ones that pin the old wording):
  - Says `Chief of Staff`; `only approval point`; an architect spec needs no further approval `unless it would change what was approved`.
  - Flow line names only present roles, in order, joined by ` → ` (coder+tester team → `coder → tester`).
  - Missing-role rule present in every team: research, design or review with no worker → do it yourself; implementing with no coder → `stop and ask the operator`; no tester → the coder's own tests stand.
  - `At most two retries`; after that `stop and ask the operator`; contains `Never take the work over on your own`; takeover command still listed with `only if the operator tells you`.
  - Tester gets `the spec and acceptance criteria, not the coder's account`; tester failures and reviewer blockers go back as a coder retry; `nits` do not.
  - Coder `blocked` → fix the spec; researcher open questions; missing JSON block `is never a pass`.
  - Old phrases gone: `implement it yourself and record the takeover`, `run the acceptance checks yourself` when a tester is present.
  - Existing: names only present roles with their models; dispatch commands and the wait-loop sentence unchanged.
- [ ] **Step 2: Run** `go test ./internal/team/ -run Instruction` — expect FAIL.
- [ ] **Step 3: Implement.** Without a tester and with a coder, the lead still runs the acceptance checks itself.
- [ ] **Step 4: Run** `make lint && make test` — PASS.
- [ ] **Step 5: Commit** `feat(team): make the lead a chief of staff that escalates`

### Task 6: CLI — tiers at launch and `dev container team tier`

**Files:**
- Modify: `internal/cli/team.go`
- Test: `internal/cli/team_test.go`

**Interfaces:**
- Consumes: Task 2's `Resolver`, `ParseTierOverrides`, `ParseTier`, `AllTiers`; Task 3's store methods; `team.AllRoles()`.
- Produces: `teamConfig(o teamOpts, saved func(tool string) (map[string]string, error)) (team.Config, error)`; `teamOpts.tierSpecs []string` (`--tier`, repeatable); subcommands `tier set TOOL TIER=MODEL...`, `tier unset TOOL TIER...`, `tier list [TOOL]` under `newContainerTeamCmd`.

- [ ] **Step 1: Write the failing tests**
  - `TestTeamConfigBareRoleUsesDefaultTier`: claude workers, `--worker coder` → `Models[coder]=="sonnet"`, `Tiers[coder]=="medium"`.
  - `TestTeamConfigTierFlagBeatsSaved`, `TestTeamConfigSavedBeatsBuiltin`, `TestTeamConfigLiteralPassesThrough`.
  - `TestTeamConfigOpencodeUnmappedTier`: usage error (exit 2) with Task 2's exact message.
  - `TestTeamConfigLeadModelTier`: `--lead claude --lead-model large` → `Lead.Model=="opus"`, `Lead.Tier=="large"`.
  - `TestTeamConfigTierFlagSkipsLead`: `--lead claude --lead-model medium --workers opencode --tier medium=x/y --worker coder` → lead `sonnet`, coder `x/y`.
  - `TestTeamConfigFiveWorkers` accepted; a sixth (repeat) refused; `--worker` missing → `give one to five`.
  - `TestTeamTierSetListUnset` (temp `DEV_STATE`): set two tiers for opencode, `list` shows tool, tier, model, source for all six rows (`saved`, `built-in`, `-`), unset one, list again; audit has `team-tier-set` / `team-tier-unset` with `tool`, `tier`, `model` fields.
  - `TestTeamTierRejectsBadInput`: tool `hermes`, tier `huge`, `medium=`, `medium` (no `=`) → usage errors; nothing stored.
  - Launch print-out: one line per role and the lead, `ROLE: TIER → MODEL (SOURCE)` or `ROLE: MODEL` for a literal.
- [ ] **Step 2: Run** `go test ./internal/cli/ -run 'TeamConfig|TeamTier'` — expect FAIL.
- [ ] **Step 3: Implement.** Help text for `--worker`: `one worker as ROLE or ROLE=MODEL; ROLE is researcher, architect, coder, tester or reviewer; MODEL is a tier (small, medium, large) or a model name; ROLE alone uses its default tier; repeat for up to five`. `--lead-model`: `the lead's model or tier (default: the tool's own)`. `--tier`: `TIER=MODEL for the workers' tool, this launch only; repeatable`. Print the resolution with `a.printf` before `launchAgent`. Mark the `tier` commands experimental in `Long`, like `list` and `logs`.
- [ ] **Step 4: Run** `make lint && make test` — PASS.
- [ ] **Step 5: Commit** `feat(cli): pick team models by tier and save tiers per tool`

### Task 7: Operator docs

**Files:**
- Modify: `docs/USAGE.md` (walkthrough "Run an agent team (experimental)" ~line 79; reference `--worker` row ~line 1948; `container team` reference)

- [ ] **Step 1: Update the walkthrough:** five roles table (mandate, boundary, what is locked vs instruction-only), one-editor rule, the report block, escalation after two retries (no automatic takeover), missing-role rule, tiers with defaults and built-ins, `team tier set` example for opencode, the launch print-out. Keep the experimental marking and "local provider is the stable one" wording.
- [ ] **Step 2: Update the reference:** `--worker` (ROLE alone, tiers, up to five), `--lead-model` (tier), `--tier`, and `container team tier set|unset|list`.
- [ ] **Step 3: Check** every flag and subcommand named in USAGE exists in `dist/dev ... --help` (`make build`).
- [ ] **Step 4: Commit** `docs(usage): document the chief of staff team and model tiers`
