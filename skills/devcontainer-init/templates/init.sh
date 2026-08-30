#!/usr/bin/env bash
# Runs on the HOST, before the container starts (initializeCommand).
#
# Its only job is to write .devcontainer/.env, which devcontainer.json passes
# to the runtime via `runArgs: --env-file`. Everything in here is a value only
# the host can supply: the Claude token and your git identity.
#
# Regenerated on every launch, so the token cannot go stale and the same file
# serves `dcx`, a bare `devcontainer up`, and VS Code alike.
#
# Portability notes:
#   - macOS Keychain is tried first, then the host environment, so this works
#     on Linux and WSL where `security` does not exist.
#   - Nothing about any particular Claude gateway is hardcoded. ANTHROPIC_BASE_URL
#     is passed through only if you already set it; unset means Claude uses its
#     normal login.
#   - macOS ships bash 3.2, so no associative arrays and no ${var,,}.
set -euo pipefail

here="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
out="$here/.env"

log() { printf 'init: %s\n' "$1"; }

# Always create the file, even when there is nothing to put in it: `--env-file`
# pointing at a missing path is a hard runtime error, not a warning.
: >"$out"
chmod 600 "$out"

# Values are written UNQUOTED on purpose. --env-file treats quotes as literal
# characters, so KEY="value" would send the quotes as part of the value.
emit() { # <KEY> <value>
  [ -n "$2" ] || return 0
  printf '%s=%s\n' "$1" "$2" >>"$out"
}

# --- Claude credential -------------------------------------------------------

# $USER is not guaranteed to be exported into the environment the devcontainer
# CLI runs initializeCommand in, and this script runs under `set -u`.
keychain_get() { # <service>
  command -v security >/dev/null 2>&1 || return 1
  security find-generic-password -a "${USER:-$(id -un)}" -s "$1" -w 2>/dev/null
}

token="$(keychain_get ANTHROPIC_AUTH_TOKEN || true)"
token="${token:-${ANTHROPIC_AUTH_TOKEN:-}}"

if [ -n "$token" ]; then
  emit ANTHROPIC_AUTH_TOKEN "$token"
else
  # An API key is the other common shape. Only one of the two is emitted.
  api_key="$(keychain_get ANTHROPIC_API_KEY || true)"
  api_key="${api_key:-${ANTHROPIC_API_KEY:-}}"
  emit ANTHROPIC_API_KEY "$api_key"
fi

# Omitted rather than written empty: an empty token is sent as a literal
# "Bearer " header, which fails more confusingly than having no token at all.
if [ -z "$token" ] && [ -z "${api_key:-}" ]; then
  log "no Claude credential found on the host."
  log "Claude in the container will prompt for login on first run."
  log "To avoid that, set ANTHROPIC_AUTH_TOKEN (or ANTHROPIC_API_KEY) in your"
  log "environment, or on macOS add it to the Keychain:"
  log "  security add-generic-password -a \"\$USER\" -s ANTHROPIC_AUTH_TOKEN -w"
fi

# Only when you already use a gateway. No default is baked in.
emit ANTHROPIC_BASE_URL "${ANTHROPIC_BASE_URL:-}"

# --- git identity ------------------------------------------------------------

# Passed through explicitly rather than by copying ~/.gitconfig: without an
# identity the first commit inside the container fails with "Author identity
# unknown", and a whole-file copy drags in host-only paths.
emit_git() { # <VAR> <git-config-key>
  emit "$1" "$(git config --get "$2" 2>/dev/null || true)"
}
#
# Identity only, deliberately: the host's signing key is not in the container,
# so passing user.signingkey through would only produce commits that fail to
# sign. post-create turns signing off inside for the same reason.
emit_git DEVCONTAINER_GIT_NAME  user.name
emit_git DEVCONTAINER_GIT_EMAIL user.email

log "wrote $(basename "$out")"
