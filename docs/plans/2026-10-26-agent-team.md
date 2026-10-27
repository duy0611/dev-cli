# Agent Team (`dev container start-agent-team`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Per `AGENTS.md`, every `Task()` dispatch declares `model` explicitly. **The operator said no commits for this work:** there are no commit steps; leave changes uncommitted and say so in the summary.

**Goal:** One command starts an interactive lead agent (claude or opencode) that already knows its team of headless workers (coder, reviewer, researcher) and hands each task to them through an in-container dispatch program.

**Architecture:** A new Go binary `cmd/dev-team` is built per architecture, embedded (`internal/teambin`, a copy of the `relaybin` pattern) and copied into the container at launch. The lead calls it as a shell command: `start` runs a worker detached and returns a task id at once, `wait` blocks for a bounded time, so no lead tool-call timeout is hit. All state (config, tasks, logs) lives in a run folder outside the working tree. The host side (`internal/cli/team.go`) reuses `launchAgent`, then reads the run folder through `Provider.Exec` for `team list` and `team logs`.

**Tech Stack:** Go 1.26 standard library, cobra (existing). No new dependencies. Tool knowledge: `internal/agent` for the lead side, `internal/team` for the worker side (kept out of `internal/agent` so the in-container binary does not link `agentcfg`/`hujson`).

**Spec:** `docs/specs/2026-10-26-agent-team-design.md`. Read `docs/ARCHITECTURE.md` invariants 1, 2, 10 and 12 before Task 8.

## Global Constraints

- Experimental, like `dev diff`: the command, flags, dispatch interface and team folder "may change without a migration". Say so in `--help` and USAGE.md.
- Only `claude` and `opencode` accepted as `--lead` and `--workers`; `hermes` and `codex` are refused. All workers are the same tool; lead and workers may differ.
- `--worker ROLE=MODEL`, repeat 1 to 3 times; `ROLE` one of `coder`, `reviewer`, `researcher`, each at most once; unknown or repeated role is a usage error. `dev` does not validate model names.
- Flags every time; no saved team in `agents.yaml`. A scripted run missing a required flag fails naming it; nothing is prompted that has a flag.
- Standing instruction is never written into the operator's working tree; a lead tool with no channel for it is refused.
- Retry limit: at most two retries per task (three attempts), then the lead implements it and records a takeover.
- Every task has a hard stop: a turn cap and a wall-clock timeout enforced by dispatch (kills the worker). `--worker-max-turns` and `--worker-timeout` override defaults. No dollar cap.
- Cut-off task returns a plain failure ("hit the turn limit" / "timed out") with the log tail and counts as an attempt.
- Team folder: not in the git working tree, readable from the host via exec, present in guarded containers, no new host mount. On `/var/dev-state` when the container persists state, else a rebuild loses it. One run folder per launch, keyed by container and launch time; a `--continue` lead keeps its run.
- A dead lead does not stop a worker; an uncollected result is marked uncollected.
- Local provider is the target; k8s should work if delivery does but is not required.
- Comments: why only, senior-to-senior. Tests: stub executables on a temp `PATH`, no mocks, no network; store tests use a temp file. Test first. A rename is just a rename.
- `docs/USAGE.md` matches the command surface before the work is done.

## Review Focus

Failure modes the spec implies but nothing pins; each has a test in the named task.

1. Task text with leading dashes, quotes, newlines, `$(...)` or hundreds of KB reaches the worker intact (never via a shell, never as a flag). Task 3 (claude), Task 10 (opencode).
2. A worker that ignores SIGTERM or spawns children is still stopped at the timeout (process group, SIGTERM then SIGKILL). Task 5.
3. A stream with truncated or non-JSON lines, or one line over 64 KiB, does not stop the parser or the log tee. Task 3.
4. A second `coder` start while one is running is refused naming the running task; a `researcher` alongside is allowed. Task 5.
5. `git status` unavailable (no git, not a repo, guarded container) falls back to edited paths from the stream and never fails the task. Task 5.

---

## Task 0: Investigations (no product code)

Resolve every unverified claim before building on it. Record the findings in `docs/research/2026-10-26-agent-team-cli-facts.md` (research decides nothing; later tasks cite it) and capture real output as testdata fixtures for Tasks 3 and 10 (`internal/team/testdata/claude-stream.jsonl`, `opencode-stream.jsonl`). Where no API credentials exist in this container, say so and mark the fixture hand-written from documented/observed event shapes.

**Files:** Create `docs/research/2026-10-26-agent-team-cli-facts.md`, `internal/team/testdata/*`.

- [ ] **I1: claude turn cap.** `claude --help` (2.1.302) lists `--max-budget-usd` but **no `--max-turns`**, although the spec marks it verified. Check `claude -p --max-turns 1 x` (is it accepted but hidden?), settings keys and env vars. If none exists, record that dispatch enforces the turn cap itself by counting `assistant` events in the stream and killing the worker (Task 5 already needs the kill path). Decision to record: flag, or counted by dispatch.
- [ ] **I2: claude headless shape.** Confirm `-p --output-format stream-json` needs `--verbose`; confirm `--permission-mode auto` is accepted by `-p` (the spec's choice) and what unattended mode `bypassPermissions` would add; event names for assistant text, tool_use, tool_result, final `result` (fields `session_id`, `total_cost_usd`, `num_turns`, `is_error`, `subtype`); how a task is read from stdin (`claude -p` with prompt on stdin) so no task text rides argv; `--resume ID` with `-p` for a retry.
- [ ] **I3: claude read-only lock.** `--disallowedTools` names that block edits (`Edit Write MultiEdit NotebookEdit`); note Bash can still write, so the instruction stays the main fence.
- [ ] **I4: opencode step limit per run.** `opencode run --help` (v2.0.32) has no step flag. Check whether an agent's `steps` can be supplied through `OPENCODE_CONFIG_CONTENT` plus `run --agent NAME`, and what the stream shows when the limit is hit.
- [ ] **I5: opencode worker facts.** `run --format json` event shapes (text, tool, session id, cost/tokens); whether a task beginning with `-` is safe after `--`; whether `run` must use `--standalone` so a background service does not serve the wrong cwd/config; that `permission.edit: deny` in a per-run agent works with `--auto`.
- [ ] **I6: opencode lead instruction channel.** Does `OPENCODE_CONFIG_CONTENT` merge with (not replace) the user's `opencode.json`, and does `instructions` take an absolute file path outside the working tree? If not, the opencode lead is refused (spec) and Tasks 11 reduces to the refusal.
- [ ] **I7: lead tool-call timeouts.** Claude Bash tool default and maximum timeout (and `BASH_DEFAULT_TIMEOUT_MS`/`BASH_MAX_TIMEOUT_MS`), opencode bash tool timeout. Output: the `wait` bound `DefaultWait` used in Task 5 (working assumption 60s, under any default) and whether a shell command suffices for both leads. If either lead cannot run a command repeatedly without a timeout, switch Task 6 to an MCP server (`dev-team mcp`, stdio) and record it.
- [ ] **I8: Herdr pane on request.** `herdr --help` for a way to open a pane running a command. Record yes/no; if yes it becomes an optional follow-up, not in this plan.

Verification: the findings file exists, answers I1-I8 with the command run and its output, and every later task whose assumption changed has a note in "Plan adjustments" at the end of the findings file. No `make` run.

---

## Task 1: Team config, flag parsing, validation

**Files:** Create `internal/team/config.go`, `internal/team/config_test.go`.

**Interfaces:**
- Produces:
  ```go
  type Role string // RoleCoder, RoleReviewer, RoleResearcher
  type Tool struct{ ID, Model string }                  // ID: "claude" | "opencode"
  type Config struct {
      Lead      Tool
      Workers   string          // worker tool ID
      Models    map[Role]string // role -> model
      MaxTurns  int
      Timeout   time.Duration
  } // json tags; marshalled to <run>/config.json
  const DefaultMaxTurns = 30; const DefaultTimeout = 30 * time.Minute
  func ParseWorkers(specs []string) (map[Role]string, error)
  func (c Config) Validate() error // tool names, 1..3 roles, limits > 0
  func (c Config) Roles() []Role   // stable order: coder, reviewer, researcher
  ```

- [ ] **Step 1: Write the failing tests** in `config_test.go`: `TestParseWorkers` (table: `coder=a/b` ok; `coder=` empty model, `=x`, `coder`, `qa=x`, repeated `coder`, four specs all error naming the offender; model `x=y/z` keeps everything after the first `=`), `TestValidateRefusesHermesAndCodex` (error names the tool and says why, one case per tool, plus `nope`), `TestRolesOrder`.
- [ ] **Step 2:** `go test ./internal/team/ -run 'TestParseWorkers|TestValidate|TestRoles'` fails: package undefined.
- [ ] **Step 3: Implement** the interface above. Defaults for zero `MaxTurns`/`Timeout` are applied by the CLI (Task 8), not here, so `Validate` rejects zero.
- [ ] **Step 4:** same command passes; `make lint && make test` passes.

---

## Task 2: Standing instruction

**Files:** Create `internal/team/instruction.go`, `internal/team/instruction_test.go`.

**Interfaces:**
- Consumes: Task 1 `Config`.
- Produces: `func Instruction(c Config, dispatch string) string` where `dispatch` is the absolute in-container path of the dev-team binary. The run folder is not named in the text: dispatch finds it through env `DEV_TEAM_RUN` (Task 8), which keeps the text identical across `--continue`.

- [ ] **Step 1: Write the failing tests:** `TestInstructionNamesOnlyPresentRoles` (coder-only config mentions `coder` and its model, not `reviewer`/`researcher`); `TestInstructionCarriesTheRules` asserts substrings for: do not implement after approval, "not your built-in subagents", send coder output to reviewer only when reviewer present, "At most two retries", takeover command, and each dispatch command (`start ROLE`, `wait ID`, `result ID`, `takeover ID`, `list`) with the dispatch path; `TestInstructionTellsLeadToLoopOnWait` (says to call `wait` again while it prints `STATUS: running`, never to give up on a timeout of the tool call).
- [ ] **Step 2:** run, fails (undefined).
- [ ] **Step 3: Implement** with `text/template` or string building; content follows the spec's "The standing instruction" list. Task text is passed on stdin: show `dev-team start coder <<'EOF' ... EOF` in the example so the lead never puts it in argv.
- [ ] **Step 4:** pass; `make lint && make test`.

---

## Task 3: Claude worker: argv, stream parser, renderer (shared event model)

**Files:** Create `internal/team/event.go`, `internal/team/claude.go`, `internal/team/claude_test.go`; use Task 0's `testdata/claude-stream.jsonl`.

**Interfaces:**
- Produces:
  ```go
  type Event struct{ Kind, Tool, Text string } // Kind: "text", "tool", "edit", "result", "other"
  type Outcome struct {
      Final     string   // the worker's final answer
      SessionID string
      CostUSD   *float64 // nil when the tool did not report it
      Turns     int
      Edited    []string // paths the worker edited, from the stream
      Failed    bool     // the tool reported an error result
  }
  type Parser interface {
      Feed(line []byte) []Event // never errors: a bad line becomes an "other" Event
      Outcome() Outcome
  }
  type WorkerSpec struct{ Role Role; Model, SessionID string; MaxTurns int }
  func Worker(tool string, w WorkerSpec) (argv []string, parser Parser, err error) // task text goes on stdin
  func Render(e Event) string // one readable line, "" to skip
  ```

- [ ] **Step 1: Write the failing tests** (`claude_test.go`): `TestClaudeArgv` (coder: `claude -p --output-format stream-json --verbose --model M --permission-mode auto`, plus the max-turns form chosen in I1; reviewer/researcher add `--disallowedTools` from I3; `SessionID` set adds `--resume ID`; no argv element contains task text); `TestClaudeParserFixture` feeds the fixture and asserts `Final`, `SessionID`, `CostUSD`, `Turns`, `Edited`; `TestClaudeParserSurvivesGarbage` (truncated JSON, a plain-text line, a final line without newline, a 200 KiB line: no panic, an `other` event each, outcome still from the later result line); `TestRender` (tool call, edit, text, result lines; empty for noise).
- [ ] **Step 2:** `go test ./internal/team/ -run Claude` fails.
- [ ] **Step 3: Implement.** Parsing is line-at-a-time from the caller; the caller (Task 5) reads with `bufio.Reader.ReadBytes`, not `Scanner`, for the 64 KiB reason. `Worker` returns an error for any tool but claude until Task 10.
- [ ] **Step 4:** pass; `make lint && make test`.

---

## Task 4: Run folder store and task records

**Files:** Create `internal/team/store.go`, `internal/team/store_test.go`.

**Interfaces:**
- Consumes: Task 1 `Config`, Task 3 `Outcome`.
- Produces:
  ```go
  type Status string // StatusRunning, StatusDone, StatusFailed, StatusTimedOut, StatusTurnLimit
  type Task struct {
      ID, Role, Tool, Model, SessionID string
      Status Status; Attempts int; Pid int
      CostUSD *float64; Turns int
      FilesChanged []string
      StartedAt, EndedAt time.Time
      Collected, TookOver bool; TakeoverReason string
  } // json tags
  type Run struct{ Dir string }
  func Base(persistState bool) string               // "/var/dev-state/dev-team" or "/tmp/dev-team"
  func RunID(container string, at time.Time) string // "<container>-20261026T101500Z"
  func BinPath(base string) string                  // base + "/bin/dev-team"
  func (r Run) TaskDir(id string) string
  func (r Run) Config() (Config, error)
  func (r Run) NewTask(role Role, text string) (Task, error)  // ids t1, t2...; writes task.md + task.json atomically
  func (r Run) Load(id string) (Task, error)
  func (r Run) Save(t Task) error                    // temp file + rename
  func (r Run) List() ([]Task, error)                // by id order
  func ListRuns(base string) ([]string, error)       // newest first
  ```
  Layout: `<run>/config.json`, `<run>/tasks/<id>/{task.md,task.json,log.jsonl,result.md}`.

- [ ] **Step 1: Write the failing tests** on `t.TempDir()`: `TestNewTaskIDsSequential`, `TestSaveIsAtomicAndRoundTrips` (CostUSD nil stays nil), `TestListOrder`, `TestListRunsNewestFirst`, `TestBaseDependsOnPersistState`, `TestConcurrentNewTaskDistinctIDs` (two goroutines; directory create with `os.Mkdir` as the lock).
- [ ] **Step 2:** run, fails.
- [ ] **Step 3: Implement.**
- [ ] **Step 4:** pass; `make lint && make test`.

---

## Task 5: Dispatch engine (start, supervise, wait, takeover)

**Files:** Create `internal/team/dispatch.go`, `internal/team/files.go`, `internal/team/dispatch_test.go`.

**Interfaces:**
- Consumes: Tasks 1, 3, 4.
- Produces:
  ```go
  const DefaultWait = 60 * time.Second // revised by Task 0 I7
  const MaxAttempts = 3
  func (r Run) Start(role Role, text, retryOf string, self string) (Task, error)
      // validates: role present in config; one running coder; retry attempts <= MaxAttempts; spawns `self supervise ID` detached (Setsid) and returns
  func (r Run) Supervise(ctx context.Context, id string) error
      // runs the worker (argv from Worker), task text on stdin, tees stdout to log.jsonl, enforces Timeout (SIGTERM to the process group, 5s, SIGKILL) and the turn cap, snapshots git status before/after, writes result.md and task.json
  func (r Run) Wait(id string, bound time.Duration, w io.Writer) error
      // prints "STATUS: running" + last rendered log lines at the bound, or "STATUS: <terminal>" + the final answer (or failure line + log tail); sets Collected
  func (r Run) Takeover(id, reason string) error
  func gitChanged(dir string) (map[string]string, error) // porcelain status, nil error with empty map when git is absent
  ```
  Worker cwd is the process cwd of `dev-team start` (the lead runs in the project folder). `PATH`, not a fixed binary path, finds the worker tool, which is how the tests stub it.

- [ ] **Step 1: Write the failing tests** with a stub `claude` shell script on a temp `PATH` that emits fixture lines (pattern: `internal/provider/local/local_test.go`):
  - `TestSuperviseHappyPath`: status done, result.md = final answer, session id and cost recorded, log.jsonl holds every stub line verbatim, task text arrived on the stub's stdin (stub writes it to a file).
  - `TestSuperviseTimeoutKillsProcessGroup`: stub traps TERM, spawns a `sleep` child, loops; `Timeout=1s`; status `timed-out` within ~8s and no leftover child; result says "timed out" with the log tail.
  - `TestSuperviseTurnLimit`: stub emits more assistant events than `MaxTurns`; status `turn-limit`, worker killed.
  - `TestSuperviseWorkerFailsToStart`: no tool on PATH: status failed, message names the tool.
  - `TestSecondCoderRefused` (names the running task) and `TestResearcherAlongsideCoderAllowed`.
  - `TestRetryBoundedAtThreeAttempts`: the fourth start with `retryOf` is refused with a message pointing at `takeover`; a retry reuses the stored session id (argv has `--resume ID`).
  - `TestFilesChangedFromGit` (temp git repo; stub edits a file) and `TestFilesChangedFallsBackToStream` (no git on PATH; `Edited` from the stream is used; task not failed).
  - `TestWaitReturnsRunningAtBound` (bound 200ms against a slow stub) and `TestWaitMarksCollected`; `TestUncollectedAfterLeadGone` (supervise completes with no Wait: `Collected` false).
  - `TestTakeoverRecorded`.
- [ ] **Step 2:** `go test ./internal/team/ -run 'Supervise|Coder|Retry|FilesChanged|Wait|Takeover|Uncollected'` fails.
- [ ] **Step 3: Implement.** `Start` re-execs itself (`os.Executable`) for supervision so a dead lead cannot kill the worker; read the stream with `bufio.Reader.ReadBytes('\n')`; process group via `SysProcAttr{Setpgid: true}` (Linux only; this package builds for the container, tests run on Linux/macOS, so keep the syscalls to `Setpgid` and `syscall.Kill(-pgid, ...)` which both support).
- [ ] **Step 4:** pass; `make lint && make test`.

---

## Task 6: `cmd/dev-team` binary and embedding

**Files:** Create `cmd/dev-team/main.go`, `cmd/dev-team/main_test.go`, `internal/teambin/embed.go`, `internal/teambin/embed_test.go`, `internal/teambin/bin/README.md`. Modify `Makefile`, `.gitignore`, `.github/workflows/ci.yml`, `.github/workflows/release.yml`.

**Interfaces:**
- Consumes: Task 4/5 `Run` API.
- Produces: CLI `dev-team start ROLE [--retry ID]` (text on stdin, prints the task id), `wait ID [--for DURATION]`, `result ID`, `takeover ID --reason TEXT`, `list [--json] [--all-runs]`, hidden `supervise ID`; run located by env `DEV_TEAM_RUN` (absolute run dir; unset is a usage error saying so). `teambin.Binary(arch string) ([]byte, error)` and `teambin.HostArch() string` mirroring `relaybin` (own copy, same error text pattern `no dev-team binary for %s`).

- [ ] **Step 1: Write the failing tests:** `main_test.go` builds the command in-process (`run(args, stdin, stdout, stderr, env) int`) against a temp run folder and the stub-claude PATH: `start` then `wait` prints the task id then a `STATUS: done` block; missing `DEV_TEAM_RUN` exits 2 naming it; unknown role exits 2. `embed_test.go` copies `relaybin/embed_test.go` (arch spellings, unknown arch, ELF check that skips when not built).
- [ ] **Step 2:** `go test ./cmd/dev-team/ ./internal/teambin/` fails.
- [ ] **Step 3: Implement.** Keep `main` as `os.Exit(run(...))` so it is testable. Makefile: new `team` target building `cmd/dev-team` for linux/amd64 and arm64 into `internal/teambin/bin` (same flags and tmp-then-mv as `relay`); `build: relay team`; `clean` removes the two files; help text and the `.PHONY` line. `.gitignore`: ignore the binaries, keep `README.md`. CI and release: add `make team` beside every `make relay`.
- [ ] **Step 4:** `make team && make lint && make test` pass; `file internal/teambin/bin/*` shows ELF for both architectures.

---

## Task 7: Launch plumbing: lead channel and extra env

**Files:** Modify `internal/agent/agent.go`, `internal/agent/claude.go`, `internal/cli/agent.go`. Test: `internal/agent/lead_test.go` (create), `internal/cli/agent_test.go`.

**Interfaces:**
- Produces:
  ```go
  // agent.Agent gains:
  Lead func(instruction, model string) (args []string, env []provider.EnvVar, err error)
  // nil Lead: the agent cannot lead a team.
  func (a Agent) LeadCommand(instruction, model string, extra []string) (argv []string, env []provider.EnvVar, err error)
  // cli: launchOpts gains
  env []provider.EnvVar // appended to the exec env only, not to Up
  ```
  Claude's Lead: `[--model M] --append-system-prompt INSTRUCTION` (no `--model` when empty). `internal/agent` may not import `internal/provider` if that creates a cycle; if it does, return `map[string]string` and convert in the CLI.

- [ ] **Step 1: Write the failing tests:** `TestClaudeLead` (with and without model, instruction passed as one argv element, extra args after); `TestLeadCommandRefusesAgentWithoutLead` (codex/hermes error names the agent); `TestLaunchAgentExecEnvIncludesOptsEnv` in `internal/cli/agent_test.go` using the existing `seedFake`: the exec sees the extra variable and the `up` call does not.
- [ ] **Step 2:** run, fails.
- [ ] **Step 3: Implement.** In `launchAgent`, clone `environ` and append `o.env` at the single place the exec options are built.
- [ ] **Step 4:** pass; `make lint && make test`.

---

## Task 8: `dev container start-agent-team` (claude lead + claude coder slice)

**Files:** Create `internal/cli/team.go`, `internal/cli/team_test.go`. Modify `internal/cli/container.go` (register the command), `internal/team/install.go` (create).

**Interfaces:**
- Consumes: Tasks 1, 2, 4, 6, 7; `launchAgent`, `herdrAgentName`, `a.resolve`, `t.requireOverride`.
- Produces: `func runContainerStartAgentTeam(ctx, a *app, workspace, name string, o teamOpts, extra []string, kubeToken bool, sshOverride *bool) error`; `team.Install(ctx, run func(ctx, argv []string, stdin io.Reader) error, path string, bin []byte) error` (atomic copy: write `path.part`, `chmod 700`, `mv`; always runs, since the path is fixed rather than content-named and the copy is small; a deliberate 10-line twin of `relay.install`).
  Flags: `--lead`, `--lead-model`, `--workers`, `--worker` (string array), `--worker-max-turns`, `--worker-timeout`, `--agent-name`, plus `addWorkspaceFlag`, `addKubeTokenFlag`, `addSSHAgentFlags`. Audit event `start-agent-team`.
  Order inside: validate flags (usage errors, before resolving or starting anything) -> resolve target -> build `Config` and `Instruction` -> `LeadCommand` -> `launchAgent` with `onStart` that (a) reads `uname -m`, installs the binary at `team.BinPath(base)`, (b) `mkdir -p` the run dir and writes `config.json` through exec stdin. `launchOpts.env` carries `DEV_TEAM_RUN`.

- [ ] **Step 1: Write the failing tests** (`team_test.go`, fake provider from `seedFake`): `TestStartAgentTeamRefusesBeforeStarting` table for: missing `--lead` (error names it), missing `--workers`, no `--worker`, `--lead hermes`, `--workers codex`, repeated role, bad `--agent-name`; each asserts exit code `exitUsage` and that `up` is absent from `fake.log`. `TestStartAgentTeamLaunchesLead`: the last exec is `claude --model opus --append-system-prompt <text> ...` with the instruction containing the dispatch path; the install exec and the run-folder exec happen before it; `DEV_TEAM_RUN` is in the exec env; `LEAD-ARGS` after `--` follow. `TestTeamBaseFollowsPersistState` (container with `PersistState` false uses `/tmp/dev-team`). Install test with a stub runner: `TestInstallAtomicCopy`.
- [ ] **Step 2:** `go test ./internal/cli/ -run 'StartAgentTeam|TeamBase'` and `./internal/team -run Install` fail.
- [ ] **Step 3: Implement.** Defaults: `--worker-max-turns` -> `DefaultMaxTurns`, `--worker-timeout` -> `DefaultTimeout` when unset. `Short`/`Long` say Experimental. `ensureAgentPresent` already checks the lead's binary; also check the worker binary with `agentInstalled` and fail naming it and the devcontainer.json to edit.
- [ ] **Step 4:** pass; `make lint && make test`. Manual check with a stub engine is not possible here; Task 14 covers the real one.

**Vertical slice done here:** claude lead, claude coder/reviewer/researcher.

---

## Task 9: Resume keeps the run folder

**Files:** Modify `internal/cli/team.go`. Test: `internal/cli/team_test.go`.

- [ ] **Step 1: Write the failing tests:** `TestContinueReusesNewestRun` (extra args contain `--continue`, `-c`, `--resume` or `-r`: the stub exec that lists runs returns an existing id and `DEV_TEAM_RUN` ends with it; no new `mkdir`); `TestContinueWithNoRunStartsOne`; `TestNoContinueMakesNewRun` (two launches, two different ids).
- [ ] **Step 2:** fails.
- [ ] **Step 3: Implement** `resumeFlag(extra []string) bool` and a `newestRun(ctx, t, base) (string, bool)` using exec `ls -1t BASE` (busybox-safe), in `onStart`'s predecessor: the run id is needed for the env before `launchAgent`, so the lookup runs only when the container is already running and otherwise falls back to a new run.
- [ ] **Step 4:** pass; `make lint && make test`.

---

## Task 10: opencode as worker

**Files:** Create `internal/team/opencode.go`, `internal/team/opencode_test.go`. Modify `internal/team/claude.go`'s `Worker` switch (rename to `internal/team/worker.go` if the dispatch grows).

**Interfaces:**
- Consumes: Task 3 `Parser`, `WorkerSpec`; Task 0 I4/I5 findings.
- Produces: `Worker("opencode", spec)` returns `opencode run --standalone --auto --format json -m MODEL [--agent NAME] [-s SESSION]` plus any env the step limit needs (extend the return with `env []string`; update the Task 3/5 call sites), a read-only agent definition (`permission.edit: deny`) for reviewer/researcher and `steps` for the turn cap, both via `OPENCODE_CONFIG_CONTENT` per I4. The exact flags are those I5 verified; the list above is the spec's working assumption.

- [ ] **Step 1: Write the failing tests:** `TestOpencodeArgvAndEnv` (coder: no edit-deny; reviewer: edit denied in the generated config JSON; `MaxTurns` appears as `steps`; session flag only when set; the task text is the one trailing argv element after `--` so a `--help\n$(x)` task survives, covering Review Focus 1); `TestOpencodeParserFixture` (final text, session id, cost when present, edited paths); `TestOpencodeParserSurvivesGarbage`; stub-`opencode` supervise test mirroring `TestSuperviseHappyPath`.
- [ ] **Step 2:** fails.
- [ ] **Step 3: Implement.** If I4 finds no way to cap steps per run, set `MaxTurns` enforcement to dispatch-side counting for opencode too (same path as claude in I1) and note it.
- [ ] **Step 4:** pass; `make lint && make test`.

---

## Task 11: opencode as lead

**Files:** Modify `internal/agent/opencode.go`. Test: `internal/agent/lead_test.go`, `internal/cli/team_test.go`.

**Interfaces:**
- Produces: opencode `Lead` returning `env` with `OPENCODE_CONFIG_CONTENT` (an `instructions` entry pointing at a file) and `--model`/`-m` args per I6. The instruction file is written by `onStart` into the run folder (so `LeadCommand` needs the file path: add a `path string` argument if I6 requires a file; update Task 7's signature in the same change).

- [ ] **Step 1: Write the failing tests:** `TestOpencodeLead` (config JSON parses, names the instruction file, model arg when set, no model when empty); `TestStartAgentTeamOpencodeLeadWritesInstructionOutsideTree` (the write exec targets the run dir, never the container's `Source` path).
- [ ] **Step 2:** fails.
- [ ] **Step 3: Implement.** If I6 found no usable channel, instead make `Lead` nil for opencode and add `TestStartAgentTeamRefusesOpencodeLead` (usage error naming the reason); the rest of this task is then skipped.
- [ ] **Step 4:** pass; `make lint && make test`.

---

## Task 12: `dev container team list`

**Files:** Modify `internal/cli/team.go`. Test: `internal/cli/team_test.go`. Create `internal/team/list.go` (+test) for table formatting.

**Interfaces:**
- Produces: `dev container team list NAME [--workspace W] [--run ID]`; `team.FormatTasks(tasks []Task) string` columns: ID, ROLE, STATUS, ATTEMPTS, COST, TAKEOVER, UNCOLLECTED. With no `--run`, lists runs (newest first) and the newest run's tasks. The host reads via `Provider.Exec` of `dev-team list --json --all-runs` at `team.BinPath(base)` with env `DEV_TEAM_RUN` unset, so `list --all-runs` (Task 6) takes `DEV_TEAM_BASE` or a `--base` flag; add that flag in this task if Task 6 did not.

- [ ] **Step 1: Write the failing tests:** `TestFormatTasks` (cost `-` when nil, takeover shown, uncollected flagged); `TestTeamListReadsThroughExec` (stub provider returns canned JSON on stdout; the table is printed); `TestTeamListNoRuns` (clear message, exit 0); `TestTeamListNeedsRunningContainer` (stopped: exit 1 naming `start-agent-team`).
- [ ] **Step 2:** fails.
- [ ] **Step 3: Implement.**
- [ ] **Step 4:** pass; `make lint && make test`.

---

## Task 13: `dev container team logs`

**Files:** Modify `internal/cli/team.go`. Create `internal/team/logrender.go` (+test). Test: `internal/cli/team_test.go`.

**Interfaces:**
- Consumes: Task 3/10 `Parser` and `Render`; Task 4 layout.
- Produces: `dev container team logs NAME [--workspace W] [--task ID] [--run ID] [--follow]`; `team.RenderLog(tool string, r io.Reader, w io.Writer) error` (line by line through the tool's `Parser`, writing `Render` output, tolerant of a partial last line). Default task is the running one, else the most recent. `--follow` execs `tail -n +1 -F FILE` and renders as lines arrive, ending when the task is terminal (host polls `task.json` once a second) or on interrupt.

- [ ] **Step 1: Write the failing tests:** `TestRenderLogClaudeFixture` and `...OpencodeFixture` (readable lines, no raw JSON); `TestRenderLogPartialLine`; `TestTeamLogsPicksRunningTask`; `TestTeamLogsFollowEndsWhenTaskDone` (stub `tail` emits then the status flips); `TestTeamLogsUnknownTask` (exit 3 style per `docs/USAGE.md` conventions: not found).
- [ ] **Step 2:** fails.
- [ ] **Step 3: Implement.**
- [ ] **Step 4:** pass; `make lint && make test`.

---

## Task 14: Docs and smoke coverage

**Files:** Modify `docs/USAGE.md` (TOC entry, walkthrough "Run an agent team (experimental)", reference entries for `start-agent-team`, `team list`, `team logs`, the exit-code table, a note in the `audit` event list, the experimental banner), `docs/ARCHITECTURE.md` (one short section: why `internal/team` is separate from `internal/agent`, why synchronous hand-off, the team folder location, and `dev-team` as a second embedded binary), `AGENTS.md` layout block (`cmd/dev-team/`, `internal/team/`, `internal/teambin/`) and the `make` list (`make team`). Create `test/smoke/team_test.go` (build tag `smoke`).

- [ ] **Step 1: Write the failing smoke test** modelled on `test/smoke/smoke_test.go`: create a container from a generated config with a stub `claude` script installed in the image (smoke cannot rely on API keys), run `start-agent-team` with `--lead claude --workers claude --worker coder=stub`, where the stub lead script invokes `dev-team start coder`/`wait` and exits; then assert `team list` shows one done task and `team logs` prints a rendered line. Skips without an engine, like the others.
- [ ] **Step 2:** `make smoke` (or the skip message here) fails or skips for the right reason.
- [ ] **Step 3:** Write the docs; every flag, default (`DefaultMaxTurns`, `DefaultTimeout`), refusal (hermes, codex) and the "experimental" and "k8s not required" notes match the code.
- [ ] **Step 4:** `make lint && make test`; run `make smoke` where an engine exists, else read the CI smoke logs or ask the operator to run it on their Mac.

---

## Self-review

- **Spec coverage:** command and flags (1, 8), roles and read-only locks (3, 10), standing instruction and channel (2, 7, 11), dispatch and limits (5, 6), cost and files changed (3, 5, 10), team folder and resume (4, 8, 9), host logs and list (12, 13), docs and smoke (14), Herdr pane (not planned, see I8), k8s (not required; delivery uses `Provider.Exec` only).
- **Not covered by tests I can pin without credentials:** real stream shapes (Task 0 fixtures) and real tool-call timeouts (I7).
- **Plan adjustments** from Task 0 are expected in Tasks 3, 5, 6, 10 and 11; update this file when the findings land.
