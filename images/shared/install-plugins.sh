#!/usr/bin/env bash
# Runs at IMAGE BUILD time. Installs Claude Code plugins from a manifest.
#
# Manifest format, tab or space separated:
#   marketplace <name> <github-repo-or-git-url>
#   plugin      <name>@<marketplace>
# Blank lines and # comments are ignored. Marketplaces must appear before the
# plugins that come from them.
#
# Unlike the runtime version this is modelled on
# (my-home-lab/.devcontainer/post-create.sh), a failure here FAILS THE BUILD. A
# half-populated image is worse than no image: the failure would otherwise only
# surface later, inside a container, as a plugin that silently isn't there.
#
# GHE marketplaces cannot be installed here. They need an SSH agent or a
# `gh auth login`, and neither exists during a build. Keep manifests to public
# sources; anything from GHE stays a manual install in a running container.
set -euo pipefail

manifest="${1:?usage: install-plugins.sh <manifest>}"
[ -f "$manifest" ] || { echo "install-plugins: no such manifest: $manifest" >&2; exit 1; }

# Must match the path the plugins will finally live at. Claude's marketplace
# cache stores absolute paths, so installing under one path and copying to
# another produces 'Failed to load marketplace: cache-miss' at runtime. The
# Containerfile installs here, then stages the whole directory to
# /opt/claude-seed; post-create copies it back to this same path.
: "${CLAUDE_CONFIG_DIR:?install-plugins: CLAUDE_CONFIG_DIR must be set}"

echo "install-plugins: using manifest $manifest"
echo "install-plugins: CLAUDE_CONFIG_DIR=$CLAUDE_CONFIG_DIR"

while read -r kind name source; do
  case "$kind" in
    marketplace)
      echo "  + marketplace $name ($source)"
      claude plugin marketplace add "$source" >/dev/null
      ;;
    plugin)
      # -y because stdin is not a TTY during a build.
      echo "  + plugin $name"
      claude plugin install "$name" -y >/dev/null
      ;;
    *)
      echo "install-plugins: unknown directive '$kind' in $manifest" >&2
      exit 1
      ;;
  esac
done < <(grep -vE '^[[:space:]]*(#|$)' "$manifest")

echo "install-plugins: done"
