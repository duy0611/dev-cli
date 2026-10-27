# Agent-team CLI facts

Research for `dev container start-agent-team`. It decides nothing; it records what
the two worker/lead tools actually do in this container on 2026-10-26, so the
plan and the tasks that build on it can be checked against something real.

Versions:

```
$ claude --version
2.1.302 (Claude Code)

$ opencode --version
opencode v2.0.32
```

Credentials were present for both tools. Every probe ran in a throwaway git
repository under `/tmp`; no probe ran in this repository. The model calls were
`haiku` (Claude) and `litellm/deepseek-v4.1-flash` (opencode), a handful of
runs and a few cents in total.

Fixtures:

- `internal/team/testdata/claude-stream.jsonl` — real `--output-format
  stream-json` output, trailing seven lines of one run. The two Claude
  `system` hook lines and the `system init` line were dropped; every remaining
  line is verbatim.
- `internal/team/testdata/opencode-stream.jsonl` — real `--format json` output
  of one run, all seven lines, unchanged.

Neither fixture is hand-written.

---

## I1: Claude turn cap

`--help` hides it, but it exists in three forms.

```
$ claude --help | grep -i -E 'max-turns|max-budget|turn'
  --max-budget-usd <amount>             Maximum dollar amount to spend on API
                                        calls (only works with --print)
  ... (other lines are about "turn" in prose; no --max-turns line)
```

The flag is parsed, not ignored: `claude -p --max-turns 1 'say hi'` exits 0,
while `claude -p --definitely-not-a-flag 'x'` exits 1 with
`error: unknown option '--definitely-not-a-flag'`. It is also enforced:

```
$ claude -p --max-turns 1 --output-format stream-json --verbose \
    --model haiku --permission-mode auto \
    'Create a.txt containing hi, then create b.txt containing bye, then reply DONE.'
exit 1
... "subtype":"error_max_turns","terminal_reason":"max_turns","is_error":true,
    "num_turns":2,"errors":["Reached maximum number of turns (1)"]
```

The same cap is settable with an environment variable:

```
$ CLAUDE_CODE_MAX_TURNS=1 claude -p --output-format stream-json --verbose \
    --model haiku --permission-mode auto 'Create c.txt ..., then d.txt ..., then reply DONE.'
exit 1
... "subtype":"error_max_turns","terminal_reason":"max_turns","is_error":true,
    "num_turns":2,"errors":["Reached maximum number of turns (1)"]

$ CLAUDE_CODE_MAX_TURNS=abc claude -p 'hi'
CLAUDE_CODE_MAX_TURNS must be a positive integer; got "abc"
exit 1
```

`strings` on the 2.1.302 binary confirms the variable and its validation; there
is no equivalent line in `--help`.

**Decision: dispatch counts turns; neither the flag nor the env var is used.**
The hidden surfaces are undocumented and could disappear (a removed flag would
fail every worker at start-up), the counting path is needed for opencode anyway,
and the count is the hard stop the spec promises even if the tool changes. The
env var is a safe optional backstop because an unknown variable is ignored, but
it is not required.

Turn semantics matter (see I2): `num_turns` is the number of distinct assistant
messages, not the number of `assistant` stream lines. A response with thinking
is emitted as two `assistant` lines with the same `message.id`. On a cap abort
`num_turns` includes the turn that hit the cap (2 for `--max-turns 1`), so it is
not a count of completed turns; use it only as a report of what ran.

---

## I2: Claude headless shape

`--verbose` is required:

```
$ claude -p --output-format stream-json --model haiku 'say hi'
Error: When using --print, --output-format=stream-json requires --verbose
exit 1
```

`--permission-mode auto` is accepted by `-p` and auto-approves edits and Bash:

```
$ printf 'Read seed.txt, then create note.txt ..., then reply DONE.\n' |
    claude -p --output-format stream-json --verbose --model haiku \
      --permission-mode auto
exit 0
... tool_result_meta ... "permission_decision":{"decision":"accept",
    "source":"config","reason_type":"mode"}
```

`--permission-mode bypassPermissions` is also accepted by `-p` without
`--dangerously-skip-permissions` (`claude -p --permission-mode bypassPermissions
--model haiku 'say ok'` printed `ok`). `--help` describes it as "Bypass all
permission checks", while `auto` is one of the ordinary modes; the difference
is that `bypassPermissions` removes the permission layer, and `auto` does not
claim to. A probe that wrote `$HOME/auto-probe.txt` through Bash succeeded under
`auto`, so for the workers' usual edit-and-test loop the practical difference is
small; `auto` is the safer choice and is the spec's choice.

Task text on stdin works and keeps every byte out of argv (the run above got the
whole task that way; no prompt argument was passed). `--resume ID` with `-p`
works for a retry:

```
$ printf 'Reply with exactly: RESUMED\n' |
    claude -p --resume f7771d53-d8d6-4c81-969a-39ae4219bbb9 \
      --output-format stream-json --verbose --model haiku --permission-mode auto
... {"type":"result","subtype":"success",
     "session_id":"f7771d53-d8d6-4c81-969a-39ae4219bbb9",
     "result":"RESUMED","num_turns":1,"is_error":false}
```

The session id is reused; `num_turns` is per invocation (it reset to 1), which
is what a per-task cap wants.

The live stream also carries `system` lines before these: `hook_started`,
`hook_response` (the operator's session hooks, one of them the full text of a
skill) and `init`. They are not part of the worker's answer; a parser should
render or skip them, not treat them as text. The fixture starts after them.

Event sequence of the captured run (after the `system init` line):

```
assistant   message.content[0].type = thinking   (same message.id as the next line)
assistant   message.content[0].type = tool_use   name = Read
user        message.content[0].type = tool_result
assistant   message.content[0].type = tool_use   name = Write     <- the file edit
user        message.content[0].type = tool_result
assistant   message.content[0].type = text       "DONE"
result      subtype = success                     <- the final event
```

The final `result` line carries `session_id`, `total_cost_usd`, `num_turns`,
`is_error`, `subtype`, `result`. In the fixture: session
`f7771d53-d8d6-4c81-969a-39ae4219bbb9`, cost `0.006312285`, `num_turns` 3,
`is_error` false, `subtype` `success`, result `DONE`.

Turn counting must use distinct `message.id` on top-level `assistant` lines
(`parent_tool_use_id` null), not the number of lines: the thinking and tool_use
lines above share one id, and the fixture has 4 assistant lines for 3 turns. A
subagent's messages, when forwarded, carry a non-null `parent_tool_use_id` and
must not count.

Files edited are in the assistant `tool_use` input (`file_path`) for tool names
`Write`, `Edit`, `MultiEdit`, `NotebookEdit`; the following `user`
`tool_use_result` line repeats the path (`filePath`) and names the kind
(`create`, `edit`). `Read` also produces a `tool_use_result`, with type `text`,
so tool name, not the result line alone, decides what was edited.

---

## I3: Claude read-only lock

`--disallowedTools` accepts all four names and blocks the edit tools:

```
$ printf 'Create blocked.txt ... First try the Write tool. If refused, use Bash.\n' |
    claude -p --output-format stream-json --verbose --model haiku \
      --permission-mode auto --disallowedTools Edit Write MultiEdit NotebookEdit
...
tool_use    Write {"file_path":"/tmp/claudefix/blocked.txt",...}
tool_result "<tool_use_error>Error: No such tool available: Write. Write is
             disabled for this session, in subagents as well as here.</tool_use_error>"
             is_error=true
tool_use    Bash {"command":"printf 'hi' > .../blocked.txt && cat ..."}
tool_result "hi" is_error=false
```

The Write tool was removed from the session; Bash then wrote the same file and
exited 0. The instruction is still the fence for a determined worker, as the
spec says. Passing `Edit Write MultiEdit NotebookEdit` as four argv elements
after one `--disallowedTools` is accepted (that is the form the probe used).

---

## I4: opencode step limit per run

`opencode run --help` has no step flag. Its flags are:
`--standalone`, `--server`, `--continue`, `--session`, `--fork`, `--model`,
`--agent`, `--format`, `--file`, `--title`, `--thinking`, `--auto`.

An agent's `steps` is a config field, and `OPENCODE_CONFIG_CONTENT` plus
`run --agent NAME` does supply it. The config schema
(`https://opencode.ai/config.json`) has
`agents.<name>.steps` ("Maximum number of agentic iterations before forcing
text-only response") and `agents.<name>.permissions`.

```
$ OPENCODE_CONFIG_CONTENT='{"agents":{"coder":{"steps":2,"mode":"primary"}}}' \
    opencode run --standalone --auto --format json --agent coder \
      'Read seed.txt, then create out3.txt ..., then reply DONE.'
exit 0
step_start
tool_use    read     state.status=completed
step_finish reason=tool-calls
step_start
text        "!!!! Ignore this ... (the model reacting to an injected message)"
tool_use    write    state.status=error
            state.error="Tools are disabled after the maximum agent steps"
```

With `steps` 5 the same task took three steps and created the file; with
`steps` 2 the file was not created. With `steps` 1 the run emitted a single
`step_start` and exited 0 with no text and no tool event at all. The cap is
real, but its boundary is quiet.

**What the stream shows when the limit is hit:** a `tool_use` part whose
`state.status` is `error` and `state.error` is exactly `Tools are disabled after
the maximum agent steps`; the run still exits 0 and has no terminal event naming
the limit. If the model stops without trying another tool at the boundary,
nothing in the stream distinguishes "finished at the cap" from "done". A
dispatch that relied on `steps` alone would mark a cut-off task as done.

**Decision for Task 10/5:** keep `steps` as a backstop, but let dispatch count
`step_start` events and apply `MaxTurns`; the status comes from that count. A
sensible backstop is `steps = MaxTurns + 1`, which dispatch reaches first; the
opencode limit then only matters if the supervisor dies.

---

## I5: opencode worker facts

### `run --format json` event shapes

One JSON object per line, each with a top-level `type`, `timestamp`, and
`sessionID` (capital `D`; there is no `session_id`). The observed types:

| `type` | Carries |
| --- | --- |
| `step_start` | `part.type=step-start`, `part.snapshot`; one per model step |
| `text` | `part.text`; the assistant's text |
| `tool_use` | `part.tool` (`read`, `write`, `edit`, `shell`, `execute`, `subagent`, ...), `part.state.status`, `part.state.input`, `part.state.output`, `part.state.error` |
| `step_finish` | `part.reason` (`tool-calls`, `stop`), `part.cost`, `part.tokens` |
| `error` | `error.type`, `error.message` (e.g. a missing agent) |

There is no `result` event. The final answer is the last `text` part after the
last `step_start`; if the run ended on a tool call with no text after it, there
is no final answer. The session id is on every line; cost is per step in
`step_finish.part.cost` and must be summed; tokens are per step. In the fixture
the two `step_finish` costs sum to `0.001748664`, and the session id is
`ses_e86276dc2ffeI3PY0EjSw3gBLr`.

The final `step_finish` is not guaranteed. One run with the same shape ended
after the final `text` with no closing `step_finish`; the fixture run does have
`step_finish reason=stop`. A parser must not require it, and a cost summed from
the stream can be partial when it is missing.

Turn counting for opencode is `step_start` (one per model call), which matches
the `steps` semantics.

### A task beginning with `-`

With `--`, it is safe:

```
$ opencode run --standalone --auto --format json -m litellm/deepseek-v4.1-flash \
    -- '-hello'
exit 0
text "Hello! Both hellos received. ..."      (the message reached the model)
```

Without `--`, the same argument is parsed as a flag and the run prints help and
exits 0 without a session:

```
$ opencode run --standalone --auto --format json -m litellm/deepseek-v4.1-flash \
    '-hello'
DESCRIPTION
  Run OpenCode with a message
...
exit 0
```

Task 10 must put the task after `--`.

### `--standalone` is required

Without it, `run` talks to the background service, which does not see
`OPENCODE_CONFIG_CONTENT` or the per-run config:

```
$ OPENCODE_CONFIG_CONTENT='{"agents":{"coder":{"steps":3,"mode":"primary"}}}' \
    opencode run --auto --format json --agent coder 'hi'
{"type":"error",...,"error":{"type":"unknown","message":"Agent not found: \"coder\""}}

$ OPENCODE_CONFIG_CONTENT='{"agents":{"coder":{"steps":3,"mode":"primary"}}}' \
    opencode run --standalone --auto --format json --agent coder 'hi'
step_start / text "hi" / step_finish ...
```

`--standalone` also keeps two worker runs from sharing a server and its cwd.
Task 10 and Task 11 both need it.

### Permissions: `edit: deny` is not enough

In v2 the agent's permission rules are `agents.<name>.permissions`, an ordered
array of `{action, resource, effect}`; the action is the **tool name**, not the
v1 key `bash`. Observed as a tool name: `read`, `edit`, `write`, `shell`;
`subagent` and `execute` are tool names seen in `tool_use` events. The
deprecated `agent.<name>.permission` object still exists in the schema but is
marked deprecated.

Evidence that the ruleset is enforced:

```
$ OPENCODE_CONFIG_CONTENT='{"agents":{"locked":{"mode":"primary","steps":3,
    "permissions":[{"action":"*","resource":"*","effect":"deny"}]}}}' \
    opencode run --standalone --auto --format json --agent locked 'Read seed.txt ...'
tool_use read   state.status=error
  state.error="No tool named \"read\" is currently available. ..."
```

Evidence that `edit` alone is not a lock: a reviewer agent with `edit` denied
still wrote its file with the `shell` tool. Denying `bash` did not help; the v1
name is not the v2 tool name:

```
deny bash, edit -> tool_use shell  state.status=completed
                    (review2.txt written)
```

Denying `shell`, `edit`, `write` (with `subagent` and `execute` also in the
list) stopped it:

```
$ OPENCODE_CONFIG_CONTENT='{"agents":{"reviewer":{"mode":"primary","steps":4,
    "permissions":[{"action":"shell",...deny},{"action":"edit",...deny},
                   {"action":"write",...deny},{"action":"subagent",...deny},
                   {"action":"execute",...deny}]}}}' \
    opencode run --standalone --auto --format json --agent reviewer \
      'Use the write tool ... If unavailable, try shell. If unavailable, reply BLOCKED.'
exit 0
text "BLOCKED"
(no file)
```

`--auto` is what makes the run unattended; explicit denies are still enforced
(the all-deny run is the proof). The read-only lock is therefore: put deny rules
for the edit tools and, if the role should not run commands, `shell` too. The
instruction stays the fence for anything the tool still offers (a subagent or
`execute` can write), exactly as Bash remains a hole on the Claude side.

---

## I6: opencode lead instruction channel

`OPENCODE_CONFIG_CONTENT` **merges** with the user's config. The user's config
is `/var/dev-state/opencode/opencode.json` (provider `litellm`, model,
`agents.title`); with the env var set to an unrelated small JSON object, a run
still used the `litellm` provider and the user's credentials, so the provider
and key from the user's file survived alongside the env entry.

`instructions` takes an absolute path outside the working tree:

```
$ cat /tmp/oc-instr/team-lead-instruction.md
Always begin every reply with the exact word BANANA followed by a space.

$ cd /tmp/ocprobe
$ OPENCODE_CONFIG_CONTENT='{"instructions":["/tmp/oc-instr/team-lead-instruction.md"]}' \
    opencode run --standalone --auto --format json \
      -m litellm/deepseek-v4.1-flash 'Say hi.'
text "BANANA Hi!"
```

The working tree was `/tmp/ocprobe`; the instruction file was outside it. So the
opencode lead is not refused: `OPENCODE_CONFIG_CONTENT` with an `instructions`
entry pointing at a file in the run folder is a usable channel.

Two caveats:

- Malformed content is silently ignored. `OPENCODE_CONFIG_CONTENT='not json'`
  still ran normally (`text "hi"`), and an invalid agent shape produced "Agent
  not found" rather than a config error. The Go side must generate valid JSON;
  there is no error to catch later.
- The probe used `run`. The interactive lead has not been exercised end to end
  (no TTY here); it uses the same standalone server and config loader, and must
  be launched with `--standalone`, otherwise the background service ignores the
  env config exactly as it did for `run`.

---

## I7: lead tool-call timeouts

Claude's Bash tool:

- default timeout **120000 ms** (`BASH_DEFAULT_TIMEOUT_MS` overrides it);
- default maximum **600000 ms**; `BASH_MAX_TIMEOUT_MS` raises it (the code
  returns `Math.max(env value, default)`).

The constants are in the 2.1.302 binary:

```
var a=120000,i=600000;function Tie(n=process.env){let e=n.BASH_DEFAULT_TIMEOUT_MS;
... return a}function GPe(n=process.env){... BASH_MAX_TIMEOUT_MS ...
return Math.max(i,Tie(n))}
```

Observed: a Bash call running `python3 -c "import time; time.sleep(65); ..."`
with no `timeout` parameter completed in 68.1 s and returned `SLEPT`. The Claude
bash harness does block a command written as `sleep 65; echo ...` (it suggests
`Monitor` or a background run), but an ordinary long-running command is allowed;
`dev-team wait` is an ordinary command, not a `sleep`.

opencode's shell tool has no timeout setting in the config schema (only MCP and
provider timeouts exist). Observed: `sleep 65; echo SLEPT` through the shell
tool took 73 s end to end and the `run` waited for it, returning `SLEPT`.

**Conclusion:** a shell command suffices for both leads. `DefaultWait = 60s` is
under both observed limits, and the wait command returns by itself at the bound,
so Task 6 does not need an MCP server.

---

## I8: Herdr pane on request

`herdr` is not installed in this container:

```
$ herdr --version
zsh: command not found: herdr
```

Unverifiable here; herdr absent. It stays a host-side optional follow-up, not
part of this plan.

---

## Plan adjustments

- **Task 3 (Claude worker).** argv is
  `claude -p --output-format stream-json --verbose --model M --permission-mode auto`;
  no `--max-turns` (I1 decision). Task text on stdin. Reviewer/researcher add
  `--disallowedTools Edit Write MultiEdit NotebookEdit`; a retry adds
  `--resume ID`. Parse the final `result` for `result`, `session_id`,
  `total_cost_usd`, `num_turns`, `is_error`, `subtype`; edited paths from
  assistant `tool_use` names (`Write`/`Edit`/`MultiEdit`/`NotebookEdit`) and
  their `file_path`, not from `Read`'s result. Ignore `system` and thinking
  lines. The fixture has 7 lines, 4 assistant lines and 3 turns: **Turns must be
  distinct `message.id` on top-level assistant lines**, not the line count.
  `Outcome().Turns` needs to be live enough for Task 5 to kill at the cap; a
  result-only counter is too late.
- **Task 5 (dispatch).** `DefaultWait = 60s` stands (I7). Turn cap:
  Claude = distinct assistant `message.id` with `parent_tool_use_id` null;
  opencode = `step_start` count. Kill on exceeding `MaxTurns`; the path is the
  same for both tools. The Claude stub in `TestSuperviseTurnLimit` should emit a
  thinking line and a tool_use line with the same message id so a line-counting
  counter fails the test. A cut-off opencode task does not need a stream error
  to be recognised: the count is authoritative.
- **Task 6 (binary and command interface).** No MCP server; the lead runs a
  shell command. `wait` is bounded at 60 s, under Claude's 120 s Bash default
  (and the 68 s ordinary command observed) and under opencode's unbounded shell.
  Keep the start/wait/result shape.
- **Task 10 (opencode worker).** argv:
  `opencode run --standalone --auto --format json -m MODEL [--agent NAME] [-s SESSION] -- TASK`.
  `--standalone` is mandatory (I5); the task is the single element after `--`.
  The per-run config goes in `OPENCODE_CONFIG_CONTENT` as
  `{"agents":{"NAME":{"mode":"primary","steps":..., "permissions":[...]}}}`;
  use the new `agents`/`permissions` shape, not the deprecated `agent`/`permission`
  object. Read-only roles deny at least `edit`, `write`, and `shell` (the v2
  action names are tool names; `bash` is not one), optionally `subagent` and
  `execute`; `edit` alone leaves the shell and subagent holes. `steps` works but
  is silent (exit 0, no terminal event), so dispatch counts `step_start`;
  `steps = MaxTurns + 1` is a sensible backstop. Parser: top-level `type`,
  `sessionID` (capital D), `part.tool`, `part.state`, `part.text`; cost is the
  sum of `step_finish.part.cost`; the final `step_finish` may be missing, so do
  not require it; the final answer is the last text; edited paths are
  `tool_use` `input.path` for `write`/`edit` (and `apply_patch` text paths), with
  the git fallback for shell edits.
- **Task 11 (opencode lead).** Not refused: `OPENCODE_CONFIG_CONTENT` merges with
  the user's config and `instructions` accepts an absolute path outside the
  tree. Keep the `path` argument for the instruction file, put its absolute path
  in the config, and launch the lead with `--standalone` (without it the
  background service ignores the env config). Generate valid JSON: invalid
  content is silently ignored.
- **Task 13 (logs).** The opencode renderer has no `result` event; render
  `step_start`, `text`, `tool_use` and `step_finish`, and take the final answer
  from the last text.
- **Spec wording.** The dispatch table's "no turn flag" is true of `--help` but
  not of the binary: 2.1.302 accepts a hidden `--max-turns` and
  `CLAUDE_CODE_MAX_TURNS`. The chosen mechanism remains dispatch-side counting;
  if the spec is corrected, say "no documented turn flag; dispatch counts".
