# Reviewing an agent's changes with `dev diff`

## Problem

An agent in a guarded container edits the operator's folder directly, and the
operator reviews the result in VS Code or with `git diff`. Every part they
question or want changed means copying the snippet, switching to the agent's
session, pasting it, and explaining which file and line it came from. A review
with ten remarks is ten round trips, each losing the context the diff had.

## Goal

The operator reads the whole diff once, writes comments against the lines they
are about, and hands every comment to the agent in one step. The agent answers
or fixes each one in its own session.

Success: in a Herdr pane, `dev diff edit api` opens the container's
uncommitted changes in the operator's editor; they add three `>>` comments,
save and close. They quit the agent and run `dev diff complete api`, and the
agent's conversation reopens with a single message listing the three comments,
each with its file, line and hunk, and starts working through them.

## Commands

```
dev diff edit NAME [--workspace W]
dev diff complete NAME [--workspace W] [--agent ID] [--new-session] [--print]
```

`diff` is a new noun, `internal/cli/diff.go`. There is no `dev diff view`: with
no agent replies to show, `edit` already displays the diff and comments, and
`complete --print` displays the review. It can come back if replies or a
browser view are ever built.

## Scope of the first version

- **Guarded containers only.** Their work is uncommitted changes in a folder on
  the host, so the diff is read by host git with no container involved.
  `legacy` containers share their folder the same way and are accepted
  (builder's call). Isolated and folderless containers exit 2:
  `dev diff works on guarded containers; NAME is isolated`.
- **Every folder of the container.** A guarded container can have several
  folders (`--folder` repeated); the diff covers each repository, so nothing
  the agent changed escapes review. Files are labelled by folder when there is
  more than one (builder's call on the exact label).
- **The container need not be running** for either command until `complete`
  launches the agent, which starts it as `start-agent` does.

## What "the diff" is

Per folder, the working tree against `HEAD`, run from the host:

- tracked changes, staged or not: `git diff HEAD`;
- untracked files that are not ignored (`git ls-files --others
  --exclude-standard`), each shown as a new file. `dev` must not stage them to
  get there (`git add -N` writes the operator's index); diff each against
  `/dev/null` with `--no-index` instead, or an equivalent that writes nothing.
- Binary files appear as git reports them (`Binary files … differ`) and can be
  commented on at file level.
- An untracked file over a size limit (my call: 256 KiB) is shown as its
  `diff --git` header and one `#` line giving its size, not its content, so a
  stray build artifact or log does not swamp the review file. It stays
  visible and can be commented on at file level, which keeps "nothing escapes
  review" true. Tracked files are always shown in full. The argv limit below
  is unaffected: `complete` sends only the commented hunks, not the whole diff.

Host git here reads a working tree the agent wrote. Run it the way
`internal/gitguard` runs git (`-c core.fsmonitor=false`) and with
`--no-ext-diff --no-textconv`, so a `.gitattributes` the agent edited cannot
select a diff driver or textconv program from the operator's global config.
`.git` itself is read-only to the container (invariant 12), so its config is
trustworthy. Nothing is written into the project folder (invariant 9).

The logic belongs in a new package (`internal/review`, builder's choice of
name), with `internal/cli/diff.go` only resolving the container and printing.

## `dev diff edit`

1. Collect the diff. An empty diff prints `no changes in NAME` and exits 0
   without opening an editor.
2. Render the review file into a temporary directory on the host, never in the
   project.
3. Open it in the editor: `$VISUAL`, else `$EDITOR`, else `vi`, run through
   `sh -c` as git does, so `EDITOR="code --wait"` works. Wait for it to exit.
4. Parse the saved file, validate it, and store the comments as the draft.

### The review file

Plain unified diff, as git prints it, with two kinds of extra line:

- `#` at column 0: `dev`'s own notes — a short how-to at the top, the previous
  round's comments, and "changed since your last review" markers. Ignored when
  read back.
- `>>` at column 0: the operator's comments. Consecutive `>>` lines form one
  comment. A single space after `>>` is stripped.

No line of a unified diff starts with `#` or `>>` (diff content lines start
with space, `+`, `-` or `\`), so both are unambiguous.

```
# Write comments on lines starting with ">>", under the line they are about.
# A comment above the first file is about the whole change.
#
# Last review (sent 2026-10-19 14:32):
#   internal/cli/diff.go:42  why shell out here instead of reusing gitwt?
#   internal/cli/diff.go:45  wrap this error with the command name

>> overall: please keep the new package free of cobra imports

diff --git a/internal/cli/diff.go b/internal/cli/diff.go
# changed since your last review
@@ -40,6 +40,9 @@ func runDiff(...)
+	out, err := exec.Command("git", "diff").Output()
>> why shell out here instead of reusing gitwt?
+	if err != nil {
+		return err
```

### Where a comment is anchored

- Under a hunk line: that line. Its line number is the new file's for `+` and
  context lines, the old file's for `-` lines (reported as a removed line).
- Under a file's header, before its first hunk: the file as a whole.

A hunk is identified by its file, its body, and which occurrence of that body
it is within the file (my call), so two identical hunks in one file are never
confused.
- Above the first file: the change as a whole.

Each stored comment keeps its file, the line's number, kind and text, and the
full text of its hunk, which is what `complete` sends.

### Validation

With every `#` and `>>` line removed, the file must equal the diff that was
written. If it does not (the operator edited a diff line), nothing is saved;
at a terminal `dev` says so and offers to reopen the file with their text
intact, and otherwise exits 1 leaving the file's path in the message so no
comment is lost.

### Running `edit` again

- **Draft comments come back.** Before anything is sent, `edit` regenerates
  the diff and re-inserts the stored draft comments, so a review can be spread
  over several sittings. Saving replaces the draft with what is in the file,
  so deleting a `>>` block deletes the comment.
- **A comment whose line moved** is re-anchored: under the same line text in
  the same hunk if that hunk still exists unchanged, otherwise at the top of
  its file with a `>>` note giving the line it was on, otherwise (file gone)
  above the first file. A comment is never dropped silently (builder's call on
  the matching rule, within these outcomes).
- **After `complete`, a fresh round.** The draft is empty, the full current
  diff is shown, the sent comments appear in the `#` header, and each hunk not
  present in the diff snapshot taken at `complete` gets a `# changed since your
  last review` line. A hunk is "present" when the same file has a hunk with the
  same body; `@@` line numbers are ignored, since an edit above shifts them.

## `dev diff complete`

1. Load the draft. No comments: exit 2, `no comments to send; run dev diff edit
   NAME first`.
2. Collect the current diff and check each comment's hunk is still in it. Any
   that are not (the agent kept working after the review) are listed and the
   command exits 1, telling the operator to run `edit` to re-anchor them.
   Sending a comment about code that no longer exists would confuse the agent.
3. Render the review message (below).
4. Deliver it, by one of two modes.
5. Mark the round sent: record the sent comments, the time, and the diff
   snapshot used for the next round's markers; clear the draft.

### Delivery: hand it to the agent (default)

`dev` starts the agent in the container exactly as `container start-agent`
does — starting the container, applying owed agent config, Herdr naming, the
guarded-run checks, the audit record — with the review as its first message,
continuing the agent's most recent session. Factor the shared part out of
`runContainerStartAgent` rather than duplicating it.

The operator quits the running agent first and runs `complete` in the same
pane; they land back in that conversation with the review sent. `dev` does not
type into a running session: it does not own that terminal.

If they forget, two processes would share one conversation. Before launching,
`dev` checks the container for a running process of the agent's binary (my
call: `pgrep -x BINARY` through `Provider.Exec`, treating a missing `pgrep` as
"none found"). If one is running: at a terminal, ask whether to continue
anyway; otherwise exit 1 naming the agent. `--print` skips the check.

- `--agent` picks the agent: `claude` (default), `opencode` or `hermes`, as on
  `start-agent`.
- `--new-session` starts a fresh session instead of continuing. The review
  carries its own context, so this works almost as well.
- Supported agents in the first version, from their documentation
  (2026-10-19), as the argv after the binary:

  | Agent | Continue last session | New session |
  |---|---|---|
  | `claude` | `--continue PROMPT` | `PROMPT` |
  | `opencode` | `--continue --prompt PROMPT` | `--prompt PROMPT` |
  | `hermes` | `chat --continue --query PROMPT` | `chat --query PROMPT` |

  Hermes submits `--query` as the first turn and stays interactive only on a
  real TTY, which `start-agent`'s exec gives it; without one it answers and
  exits. Neither OpenCode's nor Hermes's documentation shows the continue flag
  and the prompt flag together, so the builder verifies each combination
  against the real CLI (AGENTS.md: check flags against the real CLI) before
  relying on it. A combination that does not work drops to the new-session
  form for that agent, with a one-line notice when `complete` runs, and the
  table in `USAGE.md` says so. Hermes's bare `--continue` is per terminal by
  default (`session.terminal_continue`); the builder checks what it resumes
  when started through `exec`, and records the answer in `USAGE.md`.
- The argv is recorded on `agent.Agent` as a function of the prompt and
  whether to continue, nil for an agent that cannot take an initial prompt.
  `codex` gets none in the first version and is refused, exit 2, pointing to
  `--print`.
- The round is marked sent once the agent process has been started, not when
  it exits, since the operator may work with it for an hour. A failure before
  the start (container will not come up, agent missing) leaves the draft as it
  was.
- The message travels as one argv element. Linux caps one at 128 KiB
  (`MAX_ARG_STRLEN`). A review larger than about 100 KiB exits 1 suggesting
  `--print` or fewer comments (builder's call on the exact threshold). Writing
  it to a file in the container instead was considered and set aside: the
  review would then reach the conversation only as a tool read.

"Most recent session" is the agent's own notion. If the operator ran two
sessions in one container, it may continue the wrong one; `--new-session` or
`--print` are the way round it. Recording which session `dev` started is out
of scope for now.

### Delivery: `--print`

Prints the review message to stdout and nothing else, for the operator to
paste into a session that is still open or into an agent `dev` does not know.
The round is marked sent, since printing it is how the operator sends it
(builder's call). `--print` with `--agent` or `--new-session` is exit 2.

### The review message

The same text in both modes, readable with no other context:

- a short instruction: these are review comments on your uncommitted changes in
  this container; for each, answer the question or change the code; afterwards
  list each comment with what you did (answered / changed / disagreed, and
  why). It must also say not to commit, since the container is guarded and the
  operator commits.
- the general comments, if any;
- each other comment, in diff order, numbered: the path and line (or "whole
  file"), the hunk with the commented line marked, and the comment text.

Exact wording is the builder's, kept short.

## Storage

Draft and last-sent state live in SQLite through `internal/store`, a new
migration (portable SQL, invariant 5), keyed to the container and removed with
it by cascade. Per container: the draft comments, and for the last sent round
its comments, send time, and diff snapshot. History beyond the last round is
not kept. Shape of the tables is the builder's.

## Errors

| Case | Exit | Message |
|---|---|---|
| Container does not exist | 3 | the existing "no such container" error |
| Container is isolated or folderless | 2 | `dev diff works on guarded containers; NAME is MODE` |
| No changes | 0 | `no changes in NAME` (`edit`); `complete` with no draft hits the next row |
| No comments in the draft | 2 | `no comments to send; run dev diff edit NAME first` |
| Commented hunk no longer in the diff | 1 | lists them, says to run `dev diff edit NAME` |
| Saved file changed a diff line | 1 (or reopen at a terminal) | says which line, gives the file's path |
| Editor exits non-zero | 1 | nothing saved, the file's path given |
| Agent takes no initial prompt | 2 | names the agent, suggests `--print` |
| Agent already running, not at a terminal | 1 | names the agent, says to quit it first |
| Review too large for argv | 1 | suggests `--print` |
| `--print` with `--agent`/`--new-session` | 2 | names the conflicting flags |

## Testing

Unit tests, no engine (`make test`):

- `internal/review`: render and parse round-trip; `>>` grouping and anchoring
  (hunk line, file header, top); `+`/`-`/context line numbers; a modified diff
  line is refused; `#` lines ignored; re-anchoring each outcome; "changed since"
  markers ignoring `@@` numbers; review message contents.
- Diff collection against a real temporary git repository: tracked, staged,
  untracked, ignored-excluded, binary; the operator's index is unchanged
  afterwards; a `.gitattributes` naming a diff driver does not run it.
- `internal/cli`: `edit` with a stub editor script that appends a comment;
  `complete --print` output and the sent round; `complete` with the existing
  stub provider asserting each supported agent's argv, continuing and under
  `--new-session`, and `codex` refused; refusal of isolated containers; the
  error table's exit codes.
- `internal/store`: draft and sent round persist and cascade, temp-file
  database.

A smoke test is optional: the agent launch is `start-agent`'s, already covered.

## Documentation

- `docs/USAGE.md`: a walkthrough, "Review an agent's changes", after "Run an
  agent on a project", and `diff edit` / `diff complete` in the command
  reference.
- `Long` help on both commands.

## Out of scope

- Isolated containers (which commits to diff against is its own decision).
- Agent replies stored by `dev`, and `dev diff view`.
- A full-screen terminal UI, a browser page, a VS Code extension. All could
  write to the same comment store later.
- Typing into a running agent session (tmux or Herdr send-keys).
- Recording which agent session to continue.
- History of past review rounds.
