# shellcheck shell=bash
# Shared helpers. Sourced by bin/*, never executed.
#
# macOS ships bash 3.2, so: no associative arrays, no ${var,,}, no readarray,
# and an empty array under `set -u` needs the ${arr[@]+"${arr[@]}"} guard.

# --- output ------------------------------------------------------------------

: "${DCX_PROG:=dcx}"

die()  { printf '%s: %s\n' "$DCX_PROG" "$1" >&2; exit "${2:-1}"; }
warn() { printf '%s: %s\n' "$DCX_PROG" "$1" >&2; }
info() { printf '%s: %s\n' "$DCX_PROG" "$1"; }

need() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is required but not on PATH"
}

# --- repo root ---------------------------------------------------------------

# bin/* are symlinked into ~/.local/bin by install.sh, so $0 is a symlink and
# dirname $0 is the wrong place to look for lib/. Walk the chain by hand:
# BSD readlink has no -f on older macOS, and coreutils may not be installed.
dcx_repo_root() {
  local src="$1" dir
  while [ -L "$src" ]; do
    dir="$(cd -P "$(dirname "$src")" && pwd)"
    src="$(readlink "$src")"
    # A relative link resolves against the directory holding the link.
    case "$src" in
      /*) ;;
      *) src="$dir/$src" ;;
    esac
  done
  dir="$(cd -P "$(dirname "$src")" && pwd)"
  # bin/ -> repo root
  (cd "$dir/.." && pwd -P)
}

# --- state -------------------------------------------------------------------

: "${DCX_STATE:=$HOME/.local/state/dcx}"

dcx_instances_dir() { printf '%s/instances\n' "$DCX_STATE"; }
dcx_instance_dir()  { printf '%s/instances/%s\n' "$DCX_STATE" "$1"; }
dcx_creds_dir()     { printf '%s/instances/%s/creds\n' "$DCX_STATE" "$1"; }
dcx_share_dir()     { printf '%s/instances/%s/share\n' "$DCX_STATE" "$1"; }
dcx_meta_file()     { printf '%s/instances/%s/instance.json\n' "$DCX_STATE" "$1"; }
dcx_env_file()      { printf '%s/instances/%s/devcontainer.env\n' "$DCX_STATE" "$1"; }

# The commit-signing key is shared by every instance, so it lives beside
# instances/ rather than inside one. Deliberately not in ~/.ssh: this is a
# sandbox-only, signing-only key, and keeping it out of the login key directory
# is what stops it from ever being offered for authentication.
dcx_signing_dir()   { printf '%s/signing\n' "$DCX_STATE"; }
dcx_signing_key()   { printf '%s/signing/sandbox-signing\n' "$DCX_STATE"; }

dcx_instance_exists() { [ -f "$(dcx_meta_file "$1")" ]; }

dcx_list_instances() {
  local d
  [ -d "$(dcx_instances_dir)" ] || return 0
  for d in "$(dcx_instances_dir)"/*/; do
    [ -f "$d/instance.json" ] || continue
    basename "$d"
  done
}

# Read one field out of instance.json.
dcx_meta() { # <instance> <jq-path>
  jq -r "$2 // empty" "$(dcx_meta_file "$1")" 2>/dev/null
}

# --- profiles ----------------------------------------------------------------

DCX_PROFILES="base k8s cloud full"

dcx_valid_profile() {
  case " $DCX_PROFILES " in
    *" $1 "*) return 0 ;;
    *) return 1 ;;
  esac
}

# A repo that ships its own .devcontainer/ is recorded with profile "project" so
# it shows up in --list and can be torn down with --rm. Deliberately not in
# DCX_PROFILES: there is no dcx-project image, so `-p project` must stay an
# error, and every dcx_profile_has_* predicate below must keep returning false
# for it — that is what makes minting a no-op for these instances without a
# single extra guard in lib/mint.sh.
dcx_is_project_instance() { [ "$(dcx_meta "$1" '.profile')" = project ]; }

# Which credential kinds a profile carries. Note that k8s does NOT carry an AWS
# session: cluster auth is a minted ServiceAccount token, so nothing in that
# image ever reads an AWS credential. Both provider credentials live together in
# `cloud`, which is also the only image holding gcloud and the aws CLI.
dcx_profile_has_k8s() { case "$1" in k8s|full) return 0 ;; *) return 1 ;; esac; }
dcx_profile_has_gcp() { case "$1" in cloud|full) return 0 ;; *) return 1 ;; esac; }
dcx_profile_has_aws() { case "$1" in cloud|full) return 0 ;; *) return 1 ;; esac; }

dcx_image() { printf 'localhost/dcx-%s:latest\n' "$1"; }

# --- misc --------------------------------------------------------------------

# Physical path. Load-bearing for bind mounts: the podman machine shares /Users
# and /private, and resolves whatever string it is given inside the VM. Handing
# it /tmp/x mounts the VM's own empty /tmp/x instead of the host's
# /private/tmp/x, silently and with no error.
dcx_abspath() { (cd "$1" 2>/dev/null && pwd -P); }

# Instance names become volume names and a directory, so keep them boring.
dcx_check_name() {
  case "$1" in
    ''|*[!A-Za-z0-9._-]*) die "invalid instance name '$1' (use letters, digits, . _ -)" ;;
  esac
}

# Write a file atomically, 0600, from stdin. Containers read these files on
# every invocation; a partial read of a token is a confusing failure.
dcx_write_secret() { # <path>
  local dest="$1" tmp
  tmp="$(mktemp "${dest}.XXXXXX")"
  chmod 600 "$tmp"
  cat >"$tmp"
  mv -f "$tmp" "$dest"
}
