#!/usr/bin/env bash
# Runs INSIDE the container, once, after it is created (postCreateCommand).
#
# Inputs, all optional, all supplied by init.sh through the env file:
#   DEVCONTAINER_GIT_NAME / _EMAIL / _SIGNINGKEY
#
# Ordering matters and is not arbitrary — see the comment on each section.
set -euo pipefail

here="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
config_dir="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
manifest="$here/claude-plugins.txt"

log() { printf 'post-create: %s\n' "$1"; }

# --- volume ownership --------------------------------------------------------

# A named volume whose mount point does not already exist in the image is
# created by the runtime as an empty root:root directory. The mount then
# shadows whatever the image put there, $config_dir is unwritable, and every
# `claude plugin` call fails identically with an error that names the
# marketplace rather than the permission.
#
# Non-recursive and first-create only: -R would get expensive once the volume
# holds a few hundred MB of plugins.
take_ownership() { # <dir>
  [ -d "$1" ] || mkdir -p "$1"
  if [ ! -w "$1" ]; then
    sudo chown "$(id -un):$(id -gn)" "$1"
    log "took ownership of $1 (root-owned volume mount)"
  fi
}

take_ownership "$config_dir"

# HISTFILE points here from containerEnv; the shell cannot create it if the
# directory it lives in is root-owned.
if [ -n "${HISTFILE:-}" ]; then
  take_ownership "$(dirname "$HISTFILE")"
  [ -e "$HISTFILE" ] || touch "$HISTFILE"
fi

# --- Claude plugins ----------------------------------------------------------

# Installed here rather than baked into an image, which is what makes this
# config work on a plain `mcr.microsoft.com/devcontainers/base` with nothing
# built locally. The cost is that first create needs network and takes a
# couple of minutes.
#
# Installing at $config_dir — the volume's own mount point — is load-bearing.
# Claude's marketplace cache stores absolute paths, so a plugin tree that ends
# up anywhere other than where it was installed fails to load with
# 'Failed to load marketplace: cache-miss'.
#
# Guarded on plugins/ rather than a marker file: the volume's own emptiness is
# the real condition, and it cannot drift out of sync the way a marker can.
if [ -f "$manifest" ] && [ ! -e "$config_dir/plugins" ]; then
  log "installing Claude plugins from $(basename "$manifest") (first create only)"
  export CLAUDE_CONFIG_DIR="$config_dir"

  # Unlike the build-time version this is modelled on, one failure does NOT
  # abort the rest. At build time a half-populated image is worse than none, so
  # it fails hard. Here, an unreachable marketplace must not also cost you the
  # git setup and dependency install below.
  while read -r kind name source; do
    case "$kind" in
      marketplace)
        # -y is not accepted here; stdin simply is not a TTY, and the command
        # does not prompt.
        if claude plugin marketplace add "$source" >/dev/null 2>&1; then
          log "  + marketplace $name"
        else
          log "  ! marketplace $name failed ($source)"
        fi
        ;;
      plugin)
        # -y because stdin is never a TTY in this context.
        if claude plugin install "$name" -y >/dev/null 2>&1; then
          log "  + plugin $name"
        else
          log "  ! plugin $name failed"
        fi
        ;;
      *)
        log "  ! unknown directive '$kind' in $(basename "$manifest")"
        ;;
    esac
  done < <(grep -vE '^[[:space:]]*(#|$)' "$manifest")
fi

# --- git ---------------------------------------------------------------------

# The trailing `|| true` is load-bearing under `set -e`: with an unset variable
# the `[ -n ] && git config` list returns 1, which would abort the script here
# rather than skip the line.
[ -n "${DEVCONTAINER_GIT_NAME:-}" ]  && git config --global user.name  "$DEVCONTAINER_GIT_NAME"  || true
[ -n "${DEVCONTAINER_GIT_EMAIL:-}" ] && git config --global user.email "$DEVCONTAINER_GIT_EMAIL" || true

# No signing key reaches this container — the host agent socket cannot be
# forwarded through a devcontainer mount — so signing is off explicitly. Left
# unset, a host that signs by default would make every commit in here fail with
# "No secret key".
git config --global commit.gpgsign false

# Must come after any gitconfig write, which would otherwise overwrite it.
# Without it the bind-mounted repo fails git's ownership check and every git
# command dies with "detected dubious ownership".
if [ -d "$PWD/.git" ] || git -C "$PWD" rev-parse --git-dir >/dev/null 2>&1; then
  git config --global --add safe.directory "$PWD"
fi

# --- project dependencies ----------------------------------------------------

# Last on purpose: a failing dependency install must not cost you the Claude
# and git setup above, so this reports and continues rather than aborting.
#
# Every command carries an explicit `|| return 1`. `set -e` does NOT apply
# inside a function invoked as an `if` condition, so without it a failed
# install would fall through to the `return 0` below and be reported as
# success — the exact failure this section exists to surface.
install_deps() {
  if [ -f go.mod ]; then
    log "go mod download"
    go mod download || return 1
    return 0
  fi

  if [ -f pyproject.toml ] || [ -f requirements.txt ]; then
    if [ -f uv.lock ]; then
      if ! command -v uv >/dev/null 2>&1; then
        log "installing uv"
        curl -LsSf https://astral.sh/uv/install.sh | sh || return 1
        # The installer writes its own shell-rc line for interactive use; this
        # export is for the rest of THIS script.
        export PATH="$HOME/.local/bin:$PATH"
      fi
      log "uv sync"
      uv sync || return 1
    elif [ -f poetry.lock ]; then
      command -v poetry >/dev/null 2>&1 || pip install --user poetry || return 1
      log "poetry install"
      poetry install || return 1
    elif [ -f requirements.txt ]; then
      log "pip install -r requirements.txt"
      pip install --user -r requirements.txt || return 1
    else
      log "pip install -e ."
      pip install --user -e . || return 1
    fi
    return 0
  fi

  if [ -f package.json ]; then
    # corepack ships with Node and pins the package manager from
    # package.json#packageManager when it is set.
    corepack enable >/dev/null 2>&1 || true
    if [ -f pnpm-lock.yaml ]; then
      log "pnpm install"
      pnpm install --frozen-lockfile || return 1
    elif [ -f yarn.lock ]; then
      log "yarn install"
      yarn install --immutable || return 1
    elif [ -f package-lock.json ]; then
      log "npm ci"
      npm ci || return 1
    else
      log "npm install"
      npm install || return 1
    fi
    return 0
  fi

  log "no recognised dependency manifest; skipping dependency install"
}

if ! install_deps; then
  log "! dependency install failed — the container is still usable; fix and rerun by hand"
fi

log "done"
