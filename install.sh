#!/usr/bin/env bash
# install.sh - symlink bin/* onto PATH, build images, load the watcher.
#
#   ./install.sh              symlink the commands
#   ./install.sh --images     build all four images (slow: base is ~1.7 GB)
#   ./install.sh --watch      load the dccred watcher via launchd
#   ./install.sh --all        all three
set -euo pipefail

REPO="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="${DCX_BIN_DIR:-$HOME/.local/bin}"
# Label doubles as the filename in launchd/, so the two must stay in sync with
# the <key>Label</key> string inside the plist itself.
PLIST_LABEL="dev.dcx.dccred-watch"
PLIST_DEST="$HOME/Library/LaunchAgents/$PLIST_LABEL.plist"

die()  { printf 'install: %s\n' "$1" >&2; exit 1; }
info() { printf 'install: %s\n' "$1"; }

do_links=1 do_images=0 do_watch=0
while [ $# -gt 0 ]; do
  case "$1" in
    --images) do_images=1; shift ;;
    --watch)  do_watch=1; shift ;;
    --all)    do_images=1; do_watch=1; shift ;;
    --no-links) do_links=0; shift ;;
    -h|--help) sed -n '2,8p' "$0"; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

# --- symlinks ----------------------------------------------------------------

if [ "$do_links" -eq 1 ]; then
  mkdir -p "$BIN"
  for f in "$REPO"/bin/*; do
    name="$(basename "$f")"
    dest="$BIN/$name"
    # Replacing a real file (the pre-repo loose copies) is the point of the
    # takeover, but say so rather than doing it silently.
    if [ -e "$dest" ] && [ ! -L "$dest" ]; then
      backup="$dest.pre-dcx-repo"
      mv "$dest" "$backup"
      info "moved existing $name aside -> $backup"
    fi
    ln -sfn "$f" "$dest"
    chmod +x "$f"
    info "linked $name -> $f"
  done

  case ":$PATH:" in
    *":$BIN:"*) ;;
    *) info "WARNING: $BIN is not on your PATH" ;;
  esac
fi

# --- images ------------------------------------------------------------------

if [ "$do_images" -eq 1 ]; then
  # podman first, docker second. The backend is a podman machine either way, but
  # reaching it through the docker CLI drops to the deprecated classic builder
  # when the buildx plugin is absent. `podman build` is buildah, needs no
  # plugin, and writes to the same local storage, so `FROM localhost/dcx-base`
  # still resolves. Override with DCX_RUNTIME=docker.
  RUNTIME="${DCX_RUNTIME:-}"
  if [ -z "$RUNTIME" ]; then
    if   command -v podman >/dev/null 2>&1; then RUNTIME=podman
    elif command -v docker >/dev/null 2>&1; then RUNTIME=docker
    else die "podman or docker is required"
    fi
  else
    command -v "$RUNTIME" >/dev/null 2>&1 || die "DCX_RUNTIME=$RUNTIME not found"
  fi
  info "building with $RUNTIME"
  cd "$REPO"

  info "building dcx-base (installs Claude plugins; takes a while)"
  "$RUNTIME" build -f images/base/Containerfile -t localhost/dcx-base:latest .

  info "building dcx-k8s"
  "$RUNTIME" build -f images/k8s/Containerfile -t localhost/dcx-k8s:latest .

  info "building dcx-cloud"
  "$RUNTIME" build -f images/cloud/Containerfile \
    --build-arg BASE=localhost/dcx-base:latest -t localhost/dcx-cloud:latest .

  # full is exactly k8s plus the cloud layer, so it reuses the same Containerfile.
  info "building dcx-full"
  "$RUNTIME" build -f images/cloud/Containerfile \
    --build-arg BASE=localhost/dcx-k8s:latest -t localhost/dcx-full:latest .

  info "images built:"
  "$RUNTIME" images --format '  {{.Repository}}:{{.Tag}}  {{.Size}}' | grep dcx- || true
fi

# --- watcher -----------------------------------------------------------------

if [ "$do_watch" -eq 1 ]; then
  mkdir -p "$(dirname "$PLIST_DEST")"
  sed "s|@BIN@|$BIN|g; s|@HOME@|$HOME|g" \
    "$REPO/launchd/$PLIST_LABEL.plist" > "$PLIST_DEST"
  launchctl unload "$PLIST_DEST" 2>/dev/null || true
  launchctl load "$PLIST_DEST"
  info "loaded $PLIST_LABEL (logs: $HOME/Library/Logs/dccred-watch.log)"
fi

info "done"
