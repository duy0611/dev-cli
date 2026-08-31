# shellcheck shell=bash
# Instance state: the directory, instance.json, and the named volumes.
#
# An instance is the unit of isolation. For a profile instance its state dir
# doubles as the --workspace-folder handed to the devcontainer CLI, which is
# what makes the devcontainer.local_folder label unique per instance and lets
# dcx's existing liveness filter tell two sandboxes on the same project apart.
#
# A project instance is a record only: the repo ships its own .devcontainer/, so
# the workspace folder is the repo and dcx owns no config, no volumes and no
# credentials for it. dcx_local_folder is what keeps both kinds behind one
# lookup.

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

# The path handed to `devcontainer --workspace-folder`, which is also the
# container's devcontainer.local_folder label. Derived rather than stored, so
# instance.json keeps its shape:
#
#   profile instance   the state dir, holding the generated devcontainer.json.
#                      Unique per instance, which is what isolates two sandboxes
#                      on one project.
#   project instance   the repo itself, because its own .devcontainer/ is the
#                      one being used. Not unique per instance by construction —
#                      one repo is one container — which is why registering a
#                      second name against the same repo is refused in dcx.
dcx_local_folder() { # <name>
  if dcx_is_project_instance "$1"; then
    dcx_meta "$1" '.workspace'
  else
    dcx_instance_dir "$1"
  fi
}

# Container id for an instance, empty when not running.
dcx_instance_container() { # <name>
  docker ps -q --filter "label=devcontainer.local_folder=$(dcx_local_folder "$1")" 2>/dev/null
}

dcx_instance_container_any() { # <name>, running or not
  docker ps -aq --filter "label=devcontainer.local_folder=$(dcx_local_folder "$1")" 2>/dev/null
}

# Record a repo that ships its own .devcontainer/. No creds/ or share/ dir, no
# volumes, no generated config: dcx owns none of those here, and creating empty
# ones would advertise credentials this instance can never carry.
dcx_register_project() { # <name> <workspace>
  mkdir -p "$(dcx_instance_dir "$1")"
  dcx_instance_write_meta "$1" project "$2" "" false false null null null ""
}

# Removes the container, the volumes and the state dir. Safe for a project
# instance without a special case: it owns no volumes (the rm below is already
# tolerant of that) and the state dir is under DCX_STATE, never the repo.
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
