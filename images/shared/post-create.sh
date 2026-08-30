#!/usr/bin/env bash
# Installed as /usr/local/bin/dcx-post-create. Runs INSIDE the container, once,
# after it is created (postCreateCommand).
#
# Inputs, all optional, all supplied by the launcher:
#   env  DEVCONTAINER_GIT_NAME / _EMAIL
#   file /run/dcx-share/gitconfig-host       (only with --gitconfig)
#   file /run/dcx-creds/sandbox-signing[.pub] (only with --sign)
set -euo pipefail

config_dir="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
share="/run/dcx-share"

# --- Claude config volume ----------------------------------------------------

# A named volume whose mount point does not already exist in the image is
# created by the runtime as an empty root:root directory. The mount then
# shadows whatever the image put there, $config_dir is unwritable, and every
# `claude plugin` call fails identically with an error that names the
# marketplace rather than the permission.
#
# Only fires on first create, while the volume is still empty; -R would get
# expensive once it holds a few hundred MB of plugins.
if [ ! -w "$config_dir" ]; then
  sudo chown "$(id -un):$(id -gn)" "$config_dir"
  echo "post-create: took ownership of $config_dir (root-owned volume mount)"
fi

# --- plugins -----------------------------------------------------------------

# Plugins were installed at build time, at this exact path, then staged to
# /opt/claude-seed so the volume mount could not shadow them. Copy them back to
# the path they were installed at: Claude's marketplace cache stores absolute
# paths, so any other destination yields 'cache-miss' at load.
#
# Guarded on plugins/ rather than a marker file: the volume's own emptiness is
# the real condition, and it cannot drift out of sync the way a marker can.
if [ -d /opt/claude-seed ] && [ ! -e "$config_dir/plugins" ]; then
  cp -a /opt/claude-seed/. "$config_dir"/
  echo "post-create: seeded plugins from /opt/claude-seed"
fi

# --- gitconfig ---------------------------------------------------------------

# Copied rather than mounted: the gh rewrite below has to happen somewhere, and
# it must not be your host file.
if [ -f "$share/gitconfig-host" ]; then
  cp "$share/gitconfig-host" "$HOME/.gitconfig"

  # Host helpers point at /opt/homebrew/bin/gh, which does not exist here. gh
  # does, at a different path, so rewrite rather than drop: the helper then
  # resolves and reports "not logged in" if used before `gh auth login`.
  sed -i 's|!/opt/homebrew/bin/gh |!gh |g' "$HOME/.gitconfig"

  aliases=$(git config --global --get-regexp '^alias\.' 2>/dev/null | wc -l | tr -d ' ')
  echo "post-create: installed host gitconfig (${aliases} aliases)"
fi

# --- git identity ------------------------------------------------------------

# After the gitconfig copy so these win. Without them the first commit fails
# with "Author identity unknown".
#
# The trailing `|| true` is load-bearing under `set -e`: with an unset variable
# the `[ -n ] && git config` list returns 1, which would abort the script here
# rather than skip the line.
[ -n "${DEVCONTAINER_GIT_NAME:-}" ] && git config --global user.name "$DEVCONTAINER_GIT_NAME" || true
[ -n "${DEVCONTAINER_GIT_EMAIL:-}" ] && git config --global user.email "$DEVCONTAINER_GIT_EMAIL" || true

# --- git repo trust ----------------------------------------------------------

# Must come AFTER the gitconfig copy, which would otherwise overwrite it.
#
# Defensive: this podman machine is rootful and virtiofs maps the host uid to
# the container user, so the bind-mounted repo does pass git's ownership check
# here. Under a rootless machine, or a different sharing backend, it does not,
# and every git command dies with "detected dubious ownership". Costs nothing
# to keep.
if [ -d /workspace ]; then
  git config --global --add safe.directory /workspace
fi

# --- commit signing ------------------------------------------------------------

# Delegated so `dcx --sign` can apply the same config to an already-running
# instance over devcontainer exec, without a container recreate. Keeping one
# copy is the point: two would drift, and the failure mode is a sandbox that
# signs with a key git cannot verify against.
#
# Must come AFTER the gitconfig copy for the same reason safe.directory does:
# a host snapshot that sets commit.gpgsign would otherwise win, and the host's
# GPG key is not here.
/usr/local/bin/dcx-enable-signing

echo "post-create: done"
