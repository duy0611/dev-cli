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
