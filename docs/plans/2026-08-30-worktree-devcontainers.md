# Worktree-Aware Devcontainers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a git worktree usable from both a host Claude session and a dcx container by mirroring host absolute paths into the container for worktree instances, and wire `dcws` to Herdr's worktree commands.

**Architecture:** `dcx` detects a linked worktree on the host via `git rev-parse --git-common-dir` and records the main repo in `instance.json`. `lib/render.sh` then emits a second mount geometry: `workspaceMount` and `workspaceFolder` use the real host path instead of `/workspace`, and the main repo is co-mounted at its own host path so the worktree's gitdir link resolves. Normal instances are byte-for-byte unchanged. `dcws` gains `--worktree`/`--rm-worktree`, which call `herdr worktree create|open|remove` and then hand the checkout path to `dcx`.

**Tech Stack:** bash 3.2, jq, git, the `devcontainer` CLI, podman (build) / docker (run), Herdr CLI.

**Spec:** `docs/specs/2026-08-30-worktree-devcontainer-design.md`

## Global Constraints

- **macOS ships bash 3.2.** No associative arrays, no `${var,,}`, no `readarray`. Under `set -u` an empty array needs the `${arr[@]+"${arr[@]}"}` guard.
- **BSD `date`/`readlink`.** No GNU `date -d`, no dependable `readlink -f`.
- Under `set -e`, `[ cond ] && action` **exits the script** when false. Trailing `|| true` on those lines is load-bearing.
- **Every non-obvious line carries a comment explaining *why*, naming the failure it prevents.** This codebase is mostly hard-won container trivia; an uncommented workaround reads as removable.
- `bin/*` do arg parsing and orchestration; logic belongs in `lib/`. Libs start with `# shellcheck shell=bash` and are sourced, never executed.
- **New shell files must be added to `SH_FILES` in the Makefile** or they are not linted.
- `shellcheck -S warning -x --source-path=lib --source-path=.` must pass, as must `bash -n`.
- Host git must be **≥ 2.31** for `rev-parse --path-format=absolute`. No git 2.48 dependency anywhere — path mirroring deliberately avoids `--relative-paths`, because the container ships git 2.47.3.
- **Invariant 7 applies:** use `pwd -P` / `dcx_abspath` for anything that becomes a bind mount. The podman machine resolves the string inside the VM.
- **Invariant 5 applies:** `git config --global --add safe.directory` must run *after* any gitconfig copy.
- `dcx_meta` is `jq -r "$2 // empty"`, so a **missing key yields the empty string**. Old `instance.json` files without `worktree_main` therefore read as `""` and take the normal geometry with no migration.
- Instance names become volume names and a directory: `dcx_check_name` allows only `[A-Za-z0-9._-]`.

---

### Task 1: Extract the render check into a script

Pure refactor, no behavior change. The `lint-render` recipe is a backslash-continued blob inside the Makefile, which makes it both unlintable and impractical to add cases to. Task 2 needs three cases instead of one, so this comes first and can be reviewed on its own.

**Files:**
- Create: `test/render-check.sh`
- Modify: `Makefile:33-38` (`SH_FILES`), and the `lint-render` recipe

**Interfaces:**
- Consumes: `lib/common.sh`, `lib/instance.sh`, `lib/env.sh`, `lib/render.sh`; `dcx_instance_init`, `dcx_instance_write_meta`, `dcx_render_devcontainer`
- Produces: `test/render-check.sh`, run with no arguments, exit 0 on success. Task 2 adds cases to it.

- [ ] **Step 1: Create the script with today's single case**

Create `test/render-check.sh`:

```bash
#!/usr/bin/env bash
# Renders a devcontainer.json for every profile and validates it. Run via
# `make lint-render`.
#
# This is the fast feedback loop for lib/render.sh: it exercises the real
# generator against a temp DCX_STATE, so a regression is caught without
# building an image or starting a container.
set -euo pipefail

REPO="$(cd -P "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

# shellcheck source=lib/common.sh
. lib/common.sh
# shellcheck source=lib/instance.sh
. lib/instance.sh
# shellcheck source=lib/env.sh
. lib/env.sh
# shellcheck source=lib/render.sh
. lib/render.sh

K8S_JSON='{"context":"c","namespace":"n","serviceaccount":"s"}'
GCP_JSON='{"project":"p","serviceaccount":""}'
AWS_JSON='{"role_arn":"r","region":"eu-west-1"}'

fail=0
bad() { printf '    \033[31mFAIL\033[0m %s\n' "$1"; fail=1; }

DCX_STATE="$(mktemp -d)"
export DCX_STATE
trap 'rm -rf "$DCX_STATE"' EXIT

rendered() { # <instance>
  printf '%s\n' "$DCX_STATE/instances/$1/.devcontainer/devcontainer.json"
}

# <instance> <profile> <workspace> [worktree-main]
render_case() {
  rm -rf "${DCX_STATE:?}/instances/$1"
  dcx_instance_init "$1"
  dcx_instance_write_meta "$1" "$2" "$3" litellm false false \
    "$K8S_JSON" "$GCP_JSON" "$AWS_JSON" "${4:-}"
  dcx_render_devcontainer "$1"
}

for p in $DCX_PROFILES; do
  render_case lintcheck "$p" /tmp
  jq -e '.image and .workspaceMount and .containerEnv.CLAUDE_CONFIG_DIR' \
    "$(rendered lintcheck)" >/dev/null || bad "profile $p"
  printf '    %s OK\n' "$p"
done

exit "$fail"
```

- [ ] **Step 2: Make it executable and register it for linting**

```bash
chmod +x test/render-check.sh
```

In `Makefile`, extend `SH_FILES` (lines 33-38) so the new file is shellchecked — an unregistered shell file is silently unlinted:

```make
SH_FILES := bin/dcx bin/dcclaude bin/dcws bin/dccred install.sh \
            $(wildcard lib/*.sh) \
            images/shared/install-plugins.sh images/shared/post-create.sh \
            images/shared/dcx-shim images/shared/dcx-credcheck \
            images/shared/dcx-enable-signing \
            test/render-check.sh test/smoke-base.sh \
            $(wildcard skills/*/templates/*.sh)
```

`test/smoke-base.sh` is added in the same breath because it was never registered either, and Task 7 modifies it.

- [ ] **Step 3: Replace the lint-render recipe**

Replace the whole `lint-render:` recipe with:

```make
lint-render:
	@echo "==> devcontainer.json render"
	@test/render-check.sh
```

- [ ] **Step 4: Run it and confirm identical behavior**

Run: `make lint-render`
Expected: `==> devcontainer.json render` then `base OK`, `k8s OK`, `cloud OK`, `full OK`, exit 0.

- [ ] **Step 5: Run the shell linters**

Run: `make lint-shell`
Expected: PASS. If shellcheck flags the `. lib/*.sh` sources, the `# shellcheck source=` directives above are what silence it; do not add `disable=SC1091`.

- [ ] **Step 6: Commit**

```bash
git add test/render-check.sh Makefile
git commit -m "refactor: extract lint-render into test/render-check.sh"
```

---

### Task 2: Worktree mount geometry in render.sh

**Files:**
- Modify: `lib/instance.sh:23-40` (`dcx_instance_write_meta`)
- Modify: `lib/render.sh:55-80` (the jq block)
- Modify: `test/render-check.sh` (add two cases)
- Modify: `CLAUDE.md` ("The one insight that keeps `dcx` small")
- Modify: `docs/design.md:58`, `docs/design.md:326-327`

**Interfaces:**
- Consumes: `dcx_meta <instance> '.worktree_main'` → absolute host path of the main repo, or `""`
- Produces: `dcx_instance_write_meta` takes an optional **10th** positional argument, `worktree-main` (absolute path, default `""`). `instance.json` gains a `worktree_main` string field. Task 3 writes it; nothing else reads it.

- [ ] **Step 1: Write the failing render cases**

In `test/render-check.sh`, insert before the final `exit "$fail"`:

```bash
# --- worktree geometry --------------------------------------------------------
#
# A linked worktree's .git is a file pointing at <main>/.git/worktrees/<n>, so
# the container must see both trees at the SAME absolute paths the host uses.
# Mounting the checkout at /workspace instead leaves that link dangling and
# makes `git worktree list` disagree across the boundary.

MAIN=/Users/someone/Projects/proj
SIBLING=/Users/someone/.herdr/worktrees/proj/feature-x
NESTED="$MAIN/.claude/worktrees/feature-x"

render_case wtsibling base "$SIBLING" "$MAIN"
jq -e --arg ws "$SIBLING" --arg main "$MAIN" '
  .workspaceFolder == $ws
  and .workspaceMount == ("source=" + $ws + ",target=" + $ws + ",type=bind,consistency=delegated")
  and (.mounts | any(. == ("source=" + $main + ",target=" + $main + ",type=bind,consistency=delegated")))
' "$(rendered wtsibling)" >/dev/null || bad "sibling worktree geometry"
printf '    sibling worktree OK\n'

# Nested checkout: workspaceFolder may be a subdirectory of workspaceMount, so
# one bind covers both trees. A second bind would overlap the first.
render_case wtnested base "$NESTED" "$MAIN"
jq -e --arg ws "$NESTED" --arg main "$MAIN" '
  .workspaceFolder == $ws
  and .workspaceMount == ("source=" + $main + ",target=" + $main + ",type=bind,consistency=delegated")
  and (.mounts | any(startswith("source=" + $main + ",target=" + $main)) | not)
' "$(rendered wtnested)" >/dev/null || bad "nested worktree geometry"
printf '    nested worktree OK\n'

# Regression guard: an instance with no worktree_main - including every
# instance.json written before this field existed, which dcx_meta reads as ""
# because of its `// empty` - must still land on /workspace.
render_case wtnone base /tmp
jq -e '.workspaceFolder == "/workspace"
  and .workspaceMount == "source=/tmp,target=/workspace,type=bind,consistency=delegated"' \
  "$(rendered wtnone)" >/dev/null || bad "normal geometry unchanged"
printf '    normal geometry OK\n'
```

- [ ] **Step 2: Run it to verify it fails**

Run: `make lint-render`
Expected: FAIL — `sibling worktree geometry` and `nested worktree geometry` both report FAIL, `normal geometry OK` passes. Exit status 1.

- [ ] **Step 3: Add the 10th parameter to write_meta**

In `lib/instance.sh`, change the comment line and the jq call:

```bash
# Create or overwrite instance.json. Selections are passed as pre-built JSON
# objects (or 'null') so this stays one jq invocation.
#
# worktree_main is the absolute host path of the main repo when the workspace is
# a linked worktree, empty otherwise. Optional and last so the nine-argument
# callers that predate it keep working; dcx_meta's `// empty` means an
# instance.json written before this field existed reads as "" too.
dcx_instance_write_meta() { # <name> <profile> <workspace> <auth> <gitconfig> <sign> <k8s-json> <gcp-json> <aws-json> [worktree-main]
  local name="$1"
  jq -n \
    --arg name "$name" \
    --arg profile "$2" \
    --arg workspace "$3" \
    --arg auth "$4" \
    --argjson gitconfig "$5" \
    --argjson sign "$6" \
    --argjson k8s "$7" \
    --argjson gcp "$8" \
    --argjson aws "$9" \
    --arg worktree_main "${10:-}" \
    --arg created "$(date -u +%FT%TZ)" \
    '{name:$name, profile:$profile, workspace:$workspace, auth:$auth,
      gitconfig:$gitconfig, sign:$sign, k8s:$k8s, gcp:$gcp, aws:$aws,
      worktree_main:$worktree_main, created:$created}' \
    > "$(dcx_meta_file "$name")"
}
```

`${10:-}` rather than `$10`: under `set -u` an unset tenth positional would abort, and every existing caller passes nine.

- [ ] **Step 4: Implement the geometry in render.sh**

In `lib/render.sh`, after `env_file="$(dcx_env_file "$name")"` (line 17), add:

```bash
  # Mount geometry. Normal instances land at /workspace, unchanged.
  #
  # A worktree instance instead mirrors host absolute paths into the container.
  # Its .git is a file pointing at <main>/.git/worktrees/<n>, an absolute path
  # written by the host, so unless both trees sit at their host paths on both
  # sides that link dangles - and a host-side `git worktree prune` reaps the
  # container's checkout, because from the host the recorded path does not exist.
  local wt_main mount_src mount_tgt ws_folder extra_mounts
  wt_main="$(dcx_meta "$name" '.worktree_main')"
  extra_mounts='[]'
  if [ -n "$wt_main" ]; then
    ws_folder="$workspace"
    case "$workspace" in
      "$wt_main"/*)
        # Nested checkout. workspaceFolder may be a subdirectory of
        # workspaceMount, so one bind covers both trees; a second would overlap.
        mount_src="$wt_main" ;;
      *)
        # Sibling checkout, which is where Herdr puts them by default. The main
        # repo needs its own bind, read-write: commits from the worktree write
        # objects to <main>/.git/objects and refs to <main>/.git/worktrees/<n>.
        mount_src="$workspace"
        extra_mounts="$(jq -n --arg m "$wt_main" \
          '[("source=" + $m + ",target=" + $m + ",type=bind,consistency=delegated")]')" ;;
    esac
    mount_tgt="$mount_src"
  else
    mount_src="$workspace"
    mount_tgt="/workspace"
    ws_folder="/workspace"
  fi
```

Then in the final `jq -n` call, replace the `--arg workspace "$workspace" \` line with:

```bash
    --arg mountsrc  "$mount_src" \
    --arg mounttgt  "$mount_tgt" \
    --arg wsfolder  "$ws_folder" \
    --argjson extra "$extra_mounts" \
```

and replace the two body lines:

```jq
      workspaceMount: ("source=" + $mountsrc + ",target=" + $mounttgt + ",type=bind,consistency=delegated"),
      workspaceFolder: $wsfolder,
```

and close the `mounts` array over the extras:

```jq
      mounts: ([
        ("source=" + $volc  + ",target=/home/node/.claude,type=volume"),
        ("source=" + $volh  + ",target=/commandhistory,type=volume"),
        ("source=" + $creds + ",target=/run/dcx-creds,type=bind,readonly"),
        ("source=" + $share + ",target=/run/dcx-share,type=bind,readonly")
      ] + $extra),
```

- [ ] **Step 5: Run the render check to verify it passes**

Run: `make lint-render`
Expected: all four profiles OK, plus `sibling worktree OK`, `nested worktree OK`, `normal geometry OK`. Exit 0.

- [ ] **Step 6: Run the shell linters**

Run: `make lint-shell`
Expected: PASS.

- [ ] **Step 7: Update the docs this invalidates**

In `CLAUDE.md`, under "The one insight that keeps `dcx` small", the text says the two modes differ only in how `$folder` is derived. Append after the profile-mode bullet:

```markdown
One thing besides `$folder` now varies: **mount geometry**. When the workspace
is a linked git worktree, `lib/render.sh` mirrors host absolute paths into the
container instead of using `/workspace`, and co-mounts the main repo. That is
the only exception to "everything after `$folder` is generic", and it exists
because worktree metadata stores absolute paths that must agree on both sides.
```

In `docs/design.md:58`, extend the workspace row of the table:

```markdown
| Workspace | Bind-mount read-write at `/workspace`; a linked worktree instead mounts at its host path, with the main repo co-mounted |
```

In `docs/design.md:326-327`, add a sentence under the JSON sample noting that a worktree instance substitutes the host path for `/workspace` in both `workspaceMount`'s target and `workspaceFolder`, and appends a bind for the main repo.

- [ ] **Step 8: Commit**

```bash
git add lib/instance.sh lib/render.sh test/render-check.sh CLAUDE.md docs/design.md
git commit -m "feat: mirror host paths for worktree instances in render.sh"
```

---

### Task 3: dcx detects a linked worktree

**Files:**
- Modify: `bin/dcx:148-151` and `bin/dcx:207-210` (the `dcx_instance_write_meta` call)
- Modify: `test/render-check.sh` is NOT touched here; detection is host-side and tested in Task 7

**Interfaces:**
- Consumes: `dcx_abspath` from `lib/common.sh`; `dcx_instance_write_meta`'s 10th argument from Task 2
- Produces: shell variable `worktree_main` in `bin/dcx`, empty or an absolute host path; persisted to `instance.json`

- [ ] **Step 1: Add detection after the workspace is resolved**

In `bin/dcx`, immediately after line 149-150:

```bash
  workspace="$(dcx_abspath "${folder:-$PWD}")" || die "not a directory: ${folder:-$PWD}"

  # A linked worktree keeps its .git as a FILE pointing into
  # <main>/.git/worktrees/<n>. That path is absolute and host-written, so both
  # trees have to be bind-mounted at their host paths or the link dangles inside
  # the container. Detected rather than flagged: this then also covers worktrees
  # made by hand or from the Herdr sidebar, and adds no dcx option that would
  # have to be threaded through dcclaude and dcws.
  #
  # --path-format=absolute needs git >= 2.31. Failure is not fatal: a workspace
  # that is not a git repo at all is a perfectly ordinary sandbox.
  worktree_main=""
  wt_common="$(git -C "$workspace" rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
  case "$wt_common" in
    ''|"$workspace"/.git) ;;   # not a repo, or the main checkout itself
    *) worktree_main="$(dcx_abspath "$(dirname "$wt_common")")" || worktree_main="" ;;
  esac

  [ -n "$instance" ] || instance="$(basename "$workspace")"
```

- [ ] **Step 2: Pass it through at create time**

In `bin/dcx`, extend the `dcx_instance_write_meta` call (line 207):

```bash
    dcx_instance_write_meta "$instance" "$profile" "$workspace" "$auth" \
      "$([ "$want_gitconfig" -eq 1 ] && echo true || echo false)" \
      "$([ "$want_sign" -eq 1 ] && echo true || echo false)" \
      "$k8s_json" "$gcp_json" "$aws_json" "$worktree_main"
```

- [ ] **Step 3: Report it, so a surprising mount is visible at create**

Change the create-time info line (line 173) to name the co-mount when there is one:

```bash
    info "creating instance '$instance' (profile $profile) for $workspace"
    [ -n "$worktree_main" ] && info "  linked worktree; co-mounting main repo $worktree_main" || true
```

The trailing `|| true` is load-bearing under `set -e`.

- [ ] **Step 4: Verify detection by hand against a real worktree**

```bash
tmp="$(mktemp -d)"; cd "$tmp"
git init -q main && cd main && git commit -q --allow-empty -m init
git worktree add -q "$tmp/wt" -b probe
cd "$tmp/wt"
git rev-parse --path-format=absolute --git-common-dir
```

Expected: prints `<tmp>/main/.git`, whose parent is the main repo — the value `worktree_main` must take. Then from `$tmp/main`, the same command prints `<tmp>/main/.git`, which equals `"$workspace"/.git` and must yield an empty `worktree_main`.

Clean up: `cd "$tmp/main" && git worktree remove "$tmp/wt" && cd / && rm -rf "$tmp"`.

- [ ] **Step 5: Run the shell linters**

Run: `make lint-shell`
Expected: PASS. Note `wt_common` and `worktree_main` are new globals in a script that does not use `local` at top level; that matches the surrounding style.

- [ ] **Step 6: Commit**

```bash
git add bin/dcx
git commit -m "feat: detect linked worktrees and record the main repo"
```

---

### Task 4: post-create trust and mount guard

**Files:**
- Modify: `images/shared/post-create.sh:76-81`

**Interfaces:**
- Consumes: nothing from earlier tasks at runtime; runs inside the container
- Produces: no shell interface; a `git config --global safe.directory` entry per tree, and a hard failure when the main repo bind is missing

- [ ] **Step 1: Replace the safe.directory block**

`post-create.sh` has no `die` helper and does not source `lib/common.sh` — the libs are not in the image — so this is written longhand. Replace lines 79-81:

```bash
# Two entries, not one: git ownership-checks the working tree AND the gitdir it
# links into, so a worktree instance fails on the main repo without both.
wt_common="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
for d in /workspace "$PWD" ${wt_common:+"$(dirname "$wt_common")"}; do
  [ -d "$d" ] && git config --global --add safe.directory "$d" || true
done

# An unshared host path does not fail the bind - the podman machine mounts an
# empty directory inside the VM instead, silently. Only a linked worktree can
# hit this (its .git is a file); a plain checkout or a non-git workspace must
# not trip it.
if [ -f .git ] && ! [ -e "$(git rev-parse --git-common-dir 2>/dev/null || echo /nonexistent)" ]; then
  echo "post-create: main repo not mounted - check podman machine host shares" >&2
  exit 1
fi
```

This stays where the old block was, after the gitconfig copy, per invariant 5.

- [ ] **Step 2: Run the shell linters**

Run: `make lint-shell`
Expected: PASS. shellcheck may flag SC2086 on `${wt_common:+...}` — that word-splitting is intentional (empty must contribute no argument), so leave the expansion as written and confirm shellcheck at `-S warning` does not raise it. If it does, add a `# shellcheck disable=SC2086` line directly above the `for` with a comment saying why.

- [ ] **Step 3: Rebuild the base image**

Run: `make build base`
Expected: build succeeds. `post-create.sh` is baked in, so a rebuild is required before Task 7 can exercise it.

- [ ] **Step 4: Commit**

```bash
git add images/shared/post-create.sh
git commit -m "feat: trust both worktree trees and fail loudly on a missing bind"
```

---

### Task 5: dcws --worktree

**Files:**
- Modify: `bin/dcws` — usage block (lines 19-40), arg loop (lines 71-82), folder resolution (lines 84-87), workspace creation (line 152)

**Interfaces:**
- Consumes: `dcx`'s auto-detection from Task 3 — dcws passes the checkout path as `-f` and nothing else
- Produces: `dcws --worktree <branch> [--base <ref>]`; shell function `dcws_slugify <string>` → `[A-Za-z0-9._-]`-only string, reused by Task 6

- [ ] **Step 1: Add the flags to the arg loop**

Initialize alongside the other defaults (near line 62):

```bash
branch=""
base=""
```

Add to the `case` in the arg loop:

```bash
    --worktree)   [ $# -ge 2 ] || die "--worktree needs a branch"; branch="$2"; shift 2 ;;
    --base)       [ $# -ge 2 ] || die "--base needs a ref"; base="$2"; shift 2 ;;
```

- [ ] **Step 2: Add the slug helper**

Place it above the arg loop, next to the other helpers:

```bash
# Instance names become volume names and a directory, so a branch like
# feat/foo cannot be one verbatim. Computed here rather than read back from
# Herdr's own directory slug: that rule is not part of its documented contract,
# and --rm-worktree has to derive the same name later without asking Herdr.
dcws_slugify() { printf '%s' "$1" | tr -c 'A-Za-z0-9._-' '-'; }
```

`printf` without a newline matters: `tr -c` would otherwise translate the trailing newline into a `-`.

- [ ] **Step 3: Create or open the worktree, before launch_args is built**

Immediately after `folder="$(cd "$folder" && pwd -P)"` (line 87), insert:

```bash
# --- worktree mode ------------------------------------------------------------
#
# herdr worktree create makes the checkout AND the workspace with its root pane,
# returning the same .result.workspace / .result.root_pane the plain
# `workspace create` path returns. So this replaces that call rather than
# adding to it - creating both would leave an orphan workspace.
#
# This has to run before launch_args is built, because every pane runs
# `dcx -f <checkout>`, not `dcx -f <repo>`.
wt_created=""
if [ -n "$branch" ]; then
  need_herdr
  repo="$(basename "$folder")"
  [ -n "$instance" ] || instance="$(dcws_slugify "${repo}-${branch}")"
  [ -n "$name" ] || name="$instance"

  # Existing workspace wins: the focus/relaunch path below handles it, and
  # calling create again would fail on an existing checkout.
  if [ -z "$(herdr_workspace_id "$name")" ]; then
    create_args=(--cwd "$folder" --branch "$branch" --label "$name" --no-focus)
    [ -n "$base" ] && create_args+=(--base "$base") || true
    # Create when the branch has no checkout yet, open when it does. That is
    # Herdr's own split; trying create on an existing checkout is an error.
    wt_created="$(herdr worktree create "${create_args[@]}" 2>/dev/null)" \
      || wt_created="$(herdr worktree open --cwd "$folder" --branch "$branch" \
                         --label "$name" --no-focus)"

    # The field name for the checkout path is not in Herdr's published docs.
    # Try the record, then fall back to git, which is authoritative anyway.
    wt="$(printf '%s' "$wt_created" | jq -r '.result.worktree.path // empty')"
  else
    wt=""
  fi
  [ -n "${wt:-}" ] || wt="$(git -C "$folder" worktree list --porcelain \
    | awk -v b="refs/heads/$branch" '/^worktree /{p=$2} /^branch /{if ($2==b) print p}' \
    | head -1)"
  [ -n "$wt" ] || die "could not determine the checkout path for branch $branch"
  folder="$wt"
fi
```

- [ ] **Step 4: Extract the two helpers this leans on**

`herdr workspace list` is called in three places now, so give it a name. Place both above the worktree block:

```bash
need_herdr() {
  herdr workspace list >/dev/null 2>&1 \
    || die "no Herdr server reachable - run 'herdr' first"
}
herdr_workspace_id() { # <label>
  herdr workspace list | jq -r --arg n "$1" \
    '.result.workspaces[] | select(.label == $n) | .workspace_id' | head -1
}
```

Then replace line 113 with `need_herdr` and line 115-116's assignment with:

```bash
existing="$(herdr_workspace_id "$name")"
```

- [ ] **Step 5: Reuse the workspace worktree mode already created**

Replace line 152:

```bash
# In worktree mode the workspace already exists - herdr worktree create made it
# along with the checkout. Creating a second one here would orphan the first.
if [ -n "$wt_created" ]; then
  created="$wt_created"
else
  created="$(herdr workspace create --cwd "$folder" --label "$name" --no-focus)"
fi
```

- [ ] **Step 6: Document the flags in the usage block**

Add to `bin/dcws`'s usage heredoc:

```
  --worktree BRANCH  Create (or open) a git worktree for BRANCH via Herdr and
                     run the panes against the checkout instead of the repo.
                     The container mirrors host paths for worktrees, so the same
                     checkout is usable from a host session at the same path.
  --base REF         Branch from REF instead of HEAD. Only with --worktree.
```

- [ ] **Step 7: Reject --base without --worktree**

After the arg loop:

```bash
[ -n "$base" ] && [ -z "$branch" ] && die "--base only makes sense with --worktree" || true
```

- [ ] **Step 8: Run the shell linters**

Run: `make lint-shell`
Expected: PASS. `create_args` is always non-empty when used, so it needs no `${arr[@]+...}` guard; leave the guard off rather than adding a misleading one.

- [ ] **Step 9: Commit**

```bash
git add bin/dcws
git commit -m "feat: dcws --worktree creates a Herdr worktree and containers it"
```

---

### Task 6: dcws --rm-worktree

**Files:**
- Modify: `bin/dcws` — usage block, arg loop, and a teardown branch that exits before any workspace work

**Interfaces:**
- Consumes: `dcws_slugify`, `need_herdr`, `herdr_workspace_id` from Task 5
- Produces: `dcws --rm-worktree <branch> [--force]`

- [ ] **Step 1: Add the flags**

Initialize with the others:

```bash
rm_branch=""
force=0
```

In the `case`:

```bash
    --rm-worktree) [ $# -ge 2 ] || die "--rm-worktree needs a branch"; rm_branch="$2"; shift 2 ;;
    --force)       force=1; shift ;;
```

- [ ] **Step 2: Add the teardown branch**

After `folder="$(cd "$folder" && pwd -P)"` and **before** the worktree-create block from Task 5:

```bash
# --- worktree teardown ---------------------------------------------------------
#
# Order matters. The container holds a bind mount on the checkout, and
# `git worktree remove` against a directory a running container is writing to
# leaves a half-removed worktree that neither side will clean up. So the
# instance dies first, then Herdr removes the checkout.
#
# The branch survives: that is herdr worktree remove's documented behavior, and
# inheriting it beats surprising anyone with a second policy.
if [ -n "$rm_branch" ]; then
  need_herdr
  repo="$(basename "$folder")"
  [ -n "$instance" ] || instance="$(dcws_slugify "${repo}-${rm_branch}")"
  [ -n "$name" ] || name="$instance"

  dcx --rm "$instance" >/dev/null 2>&1 || true
  ws_id="$(herdr_workspace_id "$name")"
  if [ -n "$ws_id" ]; then
    rm_args=(--workspace "$ws_id")
    [ "$force" -eq 1 ] && rm_args+=(--force) || true
    herdr worktree remove "${rm_args[@]}"
    printf 'dcws: removed worktree %s (branch %s kept)\n' "$name" "$rm_branch"
  else
    printf 'dcws: no Herdr workspace labelled %s; instance removed if it existed\n' "$name"
  fi
  exit 0
fi
```

`dcx --rm` is tolerated failing: an instance that was never created is not an error here, and `dcx --rm` dies on an unknown name.

- [ ] **Step 3: Reject --force without --rm-worktree**

Beside the `--base` guard from Task 5:

```bash
[ "$force" -eq 1 ] && [ -z "$rm_branch" ] && die "--force only makes sense with --rm-worktree" || true
```

- [ ] **Step 4: Document the flags**

Add to the usage heredoc:

```
  --rm-worktree BRANCH  Remove the instance for BRANCH's worktree, then the
                        checkout itself, in that order. The branch is kept.
  --force               Remove even when the checkout is dirty. Only with
                        --rm-worktree; passed through to Herdr's own gate.
```

- [ ] **Step 5: Run the shell linters**

Run: `make lint-shell`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add bin/dcws
git commit -m "feat: dcws --rm-worktree tears down instance then checkout"
```

---

### Task 7: Smoke test the worktree geometry

**Files:**
- Modify: `test/smoke-base.sh` — a new section after the existing workspace assertions, before the signing section

**Interfaces:**
- Consumes: everything from Tasks 2-4
- Produces: no shell interface; four assertions in `make test`

- [ ] **Step 1: Add the worktree section**

Insert after the "container writes land on the host" assertion (around line 87), before the signing section. Add `WT_NAME=dcx-smoketest-wt` beside the other name variables, and `"$DCX" --rm "$WT_NAME" >/dev/null 2>&1 || true` to `cleanup`.

```bash
# --- worktree ------------------------------------------------------------------
#
# The whole feature is one string equality: the checkout's absolute path must be
# the same inside the container as on the host. When it is not, worktree
# metadata written on one side names a path the other does not have, and a
# host-side `git worktree prune` reaps the container's checkout.
#
# A sibling layout is used deliberately - that is where Herdr puts checkouts,
# and it is the case that needs the second bind.
printf '\n==> worktree\n'
WT_REPO="$WS/wtrepo"
WT_PATH="$WS/wtcheckout"
git init -q "$WT_REPO"
git -C "$WT_REPO" -c user.email=smoke@test -c user.name=smoke commit -q --allow-empty -m init
git -C "$WT_REPO" worktree add -q "$WT_PATH" -b smoke-wt

"$DCX" --rm "$WT_NAME" >/dev/null 2>&1 || true
"$DCX" -p base --as "$WT_NAME" -f "$WT_PATH" -- true
runw() { "$DCX" -p base --as "$WT_NAME" -f "$WT_PATH" -- bash -lc "$1" 2>&1; }

check "worktree path matches the host" "$WT_PATH" "$(runw 'git rev-parse --show-toplevel')"
check "main repo is co-mounted"        "$WT_REPO/.git" \
      "$(runw 'git rev-parse --path-format=absolute --git-common-dir')"

# Byte-identical, not merely "both non-empty": divergence here is exactly the
# bug, and it is invisible unless the two outputs are compared directly.
host_list="$(git -C "$WT_PATH" worktree list)"
cont_list="$(runw 'git worktree list')"
[ "$host_list" = "$cont_list" ] \
  && ok "git worktree list agrees host and container" \
  || bad "git worktree list agrees host and container (host: $host_list / container: $cont_list)"

# A commit from the container must reach the MAIN repo's object store, which is
# the read-write half of the co-mount doing its job.
sha="$(runw 'git -c user.email=smoke@test -c user.name=smoke commit -q --allow-empty -m wtprobe && git rev-parse HEAD' | tail -1)"
git -C "$WT_REPO" cat-file -e "$sha" 2>/dev/null \
  && ok "container commit lands in the main object store" \
  || bad "container commit lands in the main object store (sha: $sha)"

# The isolation invariant, re-asserted under the second mount geometry.
wtlabel="$(docker ps --filter "label=devcontainer.local_folder=$HOME/.local/state/dcx/instances/$WT_NAME" -q)"
[ -n "$wtlabel" ] && ok "worktree instance label is the state dir" \
                  || bad "worktree instance label is the state dir"
```

- [ ] **Step 2: Run the smoke test**

Run: `./test/smoke-base.sh`
Expected: every existing assertion still PASS, plus five new PASS lines under `==> worktree`. Exit 0.

If `worktree path matches the host` fails with a `/workspace` value, Task 3's detection did not fire — check `jq .worktree_main ~/.local/state/dcx/instances/dcx-smoketest-wt/instance.json`.

If `main repo is co-mounted` fails with the post-create error `main repo not mounted`, the podman machine does not share `$HOME`; that is the guard from Task 4 doing its job, not a test bug.

- [ ] **Step 3: Run the full lint**

Run: `make lint`
Expected: all stages pass, including `lint-shell` over the newly registered `test/smoke-base.sh`.

- [ ] **Step 4: Commit**

```bash
git add test/smoke-base.sh
git commit -m "test: assert host and container agree on worktree paths"
```

---

### Task 8: README

**Files:**
- Modify: `README.md` — the `dcws` usage examples (around line 71) and the Herdr row of the dependency table (line 25)

**Interfaces:**
- Consumes: the flags from Tasks 5 and 6
- Produces: nothing consumed by other tasks

- [ ] **Step 1: Add the examples**

Beside the existing `dcws -p k8s --as sk8s-debug` line:

```markdown
dcws --worktree feat/thing                  # ...on a Herdr worktree of feat/thing
dcws --worktree feat/thing --base main      # ...branched from main rather than HEAD
dcws --rm-worktree feat/thing               # instance first, then the checkout
```

- [ ] **Step 2: Note the path behavior**

Add a short paragraph under the `dcws` section:

```markdown
Worktree instances mirror host absolute paths into the container rather than
mounting at `/workspace`, and co-mount the main repo. That is what lets one
checkout be driven from a host session and a container session interchangeably:
`git worktree list` prints the same thing on both sides, so neither prunes the
other's checkout. Normal instances are unaffected.
```

- [ ] **Step 3: Update the Herdr dependency row**

Line 25 lists what Herdr is needed for. Add worktree commands to it, and mark them hard for `dcws --worktree`.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: document dcws worktree flags"
```

---

## Self-Review

**Spec coverage.** Detection → Task 3. Mount geometry table (all three rows) → Task 2. Read-write main bind → Task 2 Step 4. Silent-failure guard → Task 4. `safe.directory` for both trees → Task 4. `dcws --worktree`/`--base` → Task 5. Naming as `<repo>-<slug>` → Task 5 Step 3. Idempotency via create-then-open → Task 5 Step 3. Teardown ordering → Task 6. All four test assertions → Task 7 (which adds a fifth, the label check). README / design.md / CLAUDE.md → Tasks 8 and 2. The spec's open question about Herdr's `worktree` record field name is carried into Task 5 Step 3 with the documented `git worktree list --porcelain` fallback, so the task is executable either way.

**Interfaces.** `dcx_instance_write_meta` gains one optional trailing argument in Task 2 and is called with it in Task 3; `test/render-check.sh` passes it as `"${4:-}"` from Task 1's `render_case`, which is why Task 1 writes that wrapper before Task 2 needs it. `dcws_slugify`, `need_herdr`, and `herdr_workspace_id` are defined in Task 5 and reused in Task 6 — Task 6 cannot land first.

**Non-goals held.** No `worktree.removed` listener, no git upgrade, no relative-path rewriting, no change to non-worktree instances, and no attempt at SSH auth for `git fetch` inside the container.
