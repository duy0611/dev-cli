# shellcheck shell=bash
# Instance state: the directory, instance.json, and the named volumes.
#
# An instance is the unit of isolation. Its state dir doubles as the
# --workspace-folder handed to the devcontainer CLI, which is what makes the
# devcontainer.local_folder label unique per instance and lets dcx's existing
# liveness filter tell two sandboxes on the same project apart.

dcx_volume_claude()  { printf 'dcx-claude-%s\n' "$1"; }
dcx_volume_history() { printf 'dcx-history-%s\n' "$1"; }

dcx_instance_init() { # <name>
  local name="$1" dir
  dir="$(dcx_instance_dir "$name")"
  mkdir -p "$dir/.devcontainer" "$dir/creds" "$dir/share"
  # Tokens live here. The bind mount into the container is read-only, but the
  # host side should not be world-readable either.
  chmod 700 "$dir/creds"
}

# Create or overwrite instance.json. Selections are passed as pre-built JSON
# objects (or 'null') so this stays one jq invocation.
dcx_instance_write_meta() { # <name> <profile> <workspace> <auth> <gitconfig> <gpg> <k8s-json> <gcp-json> <aws-json>
  local name="$1"
  jq -n \
    --arg name "$name" \
    --arg profile "$2" \
    --arg workspace "$3" \
    --arg auth "$4" \
    --argjson gitconfig "$5" \
    --argjson gpg "$6" \
    --argjson k8s "$7" \
    --argjson gcp "$8" \
    --argjson aws "$9" \
    --arg created "$(date -u +%FT%TZ)" \
    '{name:$name, profile:$profile, workspace:$workspace, auth:$auth,
      gitconfig:$gitconfig, gpg:$gpg, k8s:$k8s, gcp:$gcp, aws:$aws,
      created:$created}' \
    > "$(dcx_meta_file "$name")"
}

# Merge a patch into instance.json. Read-modify-write; callers are interactive,
# so no locking.
dcx_instance_patch() { # <name> <json-patch>
  local f tmp
  f="$(dcx_meta_file "$1")"
  tmp="$(mktemp "${f}.XXXXXX")"
  jq --argjson patch "$2" '. * $patch' "$f" > "$tmp" && mv -f "$tmp" "$f"
}

dcx_instance_volumes_exist() { # <name>
  docker volume inspect "$(dcx_volume_claude "$1")" >/dev/null 2>&1
}

dcx_instance_create_volumes() { # <name>
  docker volume create "$(dcx_volume_claude "$1")"  >/dev/null
  docker volume create "$(dcx_volume_history "$1")" >/dev/null
}

# Container id for an instance, empty when not running. Same filter dcx has
# always used; it works unchanged because the state dir is the local_folder.
dcx_instance_container() { # <name>
  docker ps -q --filter "label=devcontainer.local_folder=$(dcx_instance_dir "$1")" 2>/dev/null
}

dcx_instance_container_any() { # <name>, running or not
  docker ps -aq --filter "label=devcontainer.local_folder=$(dcx_instance_dir "$1")" 2>/dev/null
}

dcx_instance_rm() { # <name>
  local name="$1" cid
  cid="$(dcx_instance_container_any "$name")"
  if [ -n "$cid" ]; then
    # shellcheck disable=SC2086
    docker rm -f $cid >/dev/null
    info "removed container(s) for $name"
  fi
  docker volume rm "$(dcx_volume_claude "$name")"  >/dev/null 2>&1 && info "removed volume $(dcx_volume_claude "$name")"  || true
  docker volume rm "$(dcx_volume_history "$name")" >/dev/null 2>&1 && info "removed volume $(dcx_volume_history "$name")" || true
  rm -rf "$(dcx_instance_dir "$name")"
  info "removed state for $name"
}
