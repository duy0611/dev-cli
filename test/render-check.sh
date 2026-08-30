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
ok()  { printf '    %s OK\n' "$1"; }

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

# <description> <rendered-file> <jq-filter> [jq-args...]. Reports exactly one
# line either way; a bare `jq ... || bad` would print the OK line on failure too.
expect() {
  local desc="$1" file="$2" filter="$3"
  shift 3
  if jq -e ${1+"$@"} "$filter" "$file" >/dev/null; then ok "$desc"; else bad "$desc"; fi
}

for p in $DCX_PROFILES; do
  render_case lintcheck "$p" /tmp
  expect "$p" "$(rendered lintcheck)" \
    '.image and .workspaceMount and .containerEnv.CLAUDE_CONFIG_DIR'
done

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
expect "sibling worktree" "$(rendered wtsibling)" '
  .workspaceFolder == $ws
  and .workspaceMount == ("source=" + $ws + ",target=" + $ws + ",type=bind,consistency=delegated")
  and (.mounts | any(. == ("source=" + $main + ",target=" + $main + ",type=bind,consistency=delegated")))
' --arg ws "$SIBLING" --arg main "$MAIN"

# Nested checkout: workspaceFolder may be a subdirectory of workspaceMount, so
# one bind covers both trees. A second bind would overlap the first.
render_case wtnested base "$NESTED" "$MAIN"
expect "nested worktree" "$(rendered wtnested)" '
  .workspaceFolder == $ws
  and .workspaceMount == ("source=" + $main + ",target=" + $main + ",type=bind,consistency=delegated")
  and (.mounts | any(startswith("source=" + $main + ",target=" + $main)) | not)
' --arg ws "$NESTED" --arg main "$MAIN"

# Regression guard: an instance with no worktree_main - including every
# instance.json written before this field existed, which dcx_meta reads as ""
# because of its `// empty` - must still land on /workspace.
render_case wtnone base /tmp
expect "normal geometry" "$(rendered wtnone)" \
  '.workspaceFolder == "/workspace"
   and .workspaceMount == "source=/tmp,target=/workspace,type=bind,consistency=delegated"'

exit "$fail"
