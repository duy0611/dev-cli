# Worktree-aware devcontainers

Date: 2026-08-30
Status: approved, not yet implemented

## Problem

A git worktree created inside a dcx container cannot be reused by a Claude
session on the host, and vice versa. Two separate causes:

1. **Path disagreement.** Worktree metadata stores absolute paths. The
   container sees the project at `/workspace`; the host sees it at
   `/Users/<user>/Projects/...`. A worktree registered on one side names a
   path that does not exist on the other, so `git worktree list` there shows a
   dangling entry and `git worktree prune` reaps it.

2. **Missing mounts.** Herdr places worktrees at
   `<worktrees.directory>/<repo>/<branch-slug>`, default `~/.herdr/worktrees`
   — outside the project. The container's single bind covers neither the
   checkout nor `<main-repo>/.git/worktrees/<name>`, which the checkout's
   `.git` file points into.

The second cause is the harder one: even with identical paths, an unmounted
checkout is invisible.

## Non-solutions considered

**Relative worktree paths.** `git worktree add --relative-paths` and
`worktree.useRelativePaths` (git 2.48) exist for exactly this class of
problem. Rejected: the container ships git 2.47.3 (node:24-trixie), and
relative paths only help when both trees are mounted with identical relative
geometry, which a sibling layout does not provide.

**Mounting the host home directory.** One bind of `/Users/<user>` covers the
repo, the worktrees directory, and everything else. Rejected: it hands the
sandbox the entire home directory, which is contrary to the premise of this
repo.

**Relocating checkouts inside the repo** via `herdr worktree create --path`.
Would let the existing single bind cover both trees. Rejected: worktrees
created from the Herdr sidebar still land in the configured default, outside
the mount, so the fix would only hold for checkouts dcws itself created.

## Approach

Mirror host absolute paths into the container for worktree instances only.
Normal instances are untouched.

### Detection

`bin/dcx`, after resolving `workspace`:

```sh
common="$(git -C "$workspace" rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
```

If `$common` resolves outside `$workspace`, the workspace is a linked
worktree and the main repo is `dcx_abspath "$(dirname "$common")"`. Recorded
in `instance.json` as `worktree_main`, null for every normal instance so
existing metadata stays valid.

Auto-detection rather than an explicit flag: it adds no dcx option to thread
through `dcclaude` and `dcws`, and it covers worktrees made by hand or from
the Herdr sidebar, not only ones dcws created.

### Mount geometry

`lib/render.sh` selects on `worktree_main`:

| Case | workspaceMount | workspaceFolder | Extra mount |
|---|---|---|---|
| Normal (`worktree_main` null) | `source=$ws,target=/workspace` | `/workspace` | — |
| Worktree, sibling checkout | `source=$ws,target=$ws` | `$ws` | `source=$main,target=$main` |
| Worktree nested under the repo | `source=$main,target=$main` | `$ws` | — |

The nested case falls out for free: `workspaceFolder` may be a subdirectory of
`workspaceMount`, so one bind covers both trees and a second would overlap.
Worth supporting because Claude Code's own `EnterWorktree` places worktrees at
`.claude/worktrees/`.

The main-repo bind is read-write. Commits from the worktree write objects to
`<main>/.git/objects` and refs to `<main>/.git/worktrees/<name>`; read-only
would leave the sandbox unable to commit.

Instance isolation is unaffected. The `devcontainer.local_folder` label is
derived from the instance state dir, not the workspace target.

### Silent-failure guard

Invariant 7 applies twice here, because `~/.herdr/worktrees` is a second host
path the podman machine must share. An unshared path binds as an empty
directory rather than failing. `images/shared/post-create.sh` asserts the link
resolves:

```sh
# Only for a linked worktree: a plain checkout has .git as a directory, and a
# non-git workspace must not trip this at all.
if [ -f .git ] && ! [ -e "$(git rev-parse --git-common-dir 2>/dev/null || echo /nonexistent)" ]; then
  echo "post-create: main repo not mounted - check podman machine host shares" >&2
  exit 1
fi
```

`post-create.sh` has no `die` helper and runs under `set -euo pipefail`, so
this is written out longhand rather than borrowing `lib/common.sh`, which is
not present in the container.

### post-create safe.directory

The current block hardcodes `/workspace`. A worktree needs two entries: git
ownership-checks the working tree and the linked gitdir separately.

```sh
main="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
for d in /workspace "$PWD" ${main:+"$(dirname "$main")"}; do
  [ -d "$d" ] && git config --global --add safe.directory "$d" || true
done
```

Still placed after the gitconfig copy, per invariant 5.

## dcws integration

### Create

`dcws --worktree <branch> [--base <ref>]` replaces the workspace-create call:

```sh
created="$(herdr worktree create --cwd "$folder" --branch "$branch" \
             ${base:+--base "$base"} --label "$name" --no-focus)"
```

`worktree.create` returns `workspace`, `tab`, `root_pane`, and `worktree`. The
existing code already reads `workspace` and `root_pane`, so every pane rename,
split, and run below it is untouched. The checkout path comes from the
`worktree` record.

**Open question for implementation:** the field name of the path inside the
`worktree` record is not in Herdr's published docs. Read it at implementation
time; fall back to `git worktree list --porcelain` filtered by branch.

`launch_args` then uses `-f "$worktree_path"`. dcx auto-detects the main repo
from there.

### Naming

dcx's default instance name is `basename "$workspace"`, which for
`~/.herdr/worktrees/<repo>/<branch-slug>` is already the branch slug — but
that collides across repositories, where branch `main` in two projects would
claim one instance. Default to `<repo>-<slug>`. `--as` still overrides.

### Idempotency

dcws already focuses an existing workspace matched by label. Worktree mode
adds one case behind it: label absent but checkout present on disk calls
`herdr worktree open --branch <b>` instead of `create`, matching Herdr's own
create/open split.

### Teardown

`dcws --rm-worktree <branch> [--force]`, in order:

1. `dcx --rm <instance>` — container, both volumes, state dir
2. `herdr worktree remove --workspace <id> [--force]`

Container first is load-bearing: it holds a bind mount on the checkout, and
`git worktree remove` against a directory a running container is writing to
produces a half-removed worktree. `--force` passes through to Herdr's own
dirty-checkout gate rather than adding a second one.

The branch survives removal. That is Herdr's documented behavior, inherited
rather than overridden.

## Tests

`test/smoke-base.sh` gains a worktree case: build a throwaway repo and a
sibling worktree on the host, bring up an instance on the worktree, then
assert

- `git rev-parse --show-toplevel` in the container equals the host worktree
  path as a string — that equality is the whole feature
- `git worktree list` is byte-identical on host and in container
- a commit made in the container lands in the main repo's object store
- `devcontainer.local_folder` is still unique per instance, confirming the
  isolation invariant holds under the second mount geometry

## Documentation

- `README.md`: `--worktree` and `--rm-worktree`.
- `docs/design.md`: third row in the mount table (line 58) and the alternate
  generated JSON (line 326).
- `CLAUDE.md`: "The one insight that keeps `dcx` small" currently states that
  the two modes differ only in how `$folder` is derived. Worktree mode makes
  mount geometry vary as well. Leaving the claim unqualified would send the
  next reader past the bug.

## Failure modes

- **Unshared podman host path** binds as an empty directory. Caught by the
  guard above.
- **git version**: no 2.48 dependency. Path mirroring sidesteps
  `--relative-paths`; container git 2.47.3 is sufficient.
- **Two containers on one main repo**: safe. Each worktree's index lives at
  `<main>/.git/worktrees/<n>/index`, so staging is isolated; only
  worktree-admin commands contend, and the object store is concurrency-safe.
- **Host paths inside the container** expose the host username in every path.
  Cosmetic, but a visible change from `/workspace`.

## Non-goals

- No listener on Herdr's `worktree.removed` event. Removals initiated from the
  sidebar leave an orphaned instance until `dcx --rm`.
- No change to non-worktree instances.
- No git upgrade and no relative-path rewriting.
- **SSH authentication for `git fetch` inside the container remains
  unsolved.** This design does not address it. It does make the workaround
  natural: with host and container agreeing on paths, fetching on the host
  lands directly in the container's view.
