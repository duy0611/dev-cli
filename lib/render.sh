# shellcheck shell=bash
# Generates the instance's devcontainer.json.
#
# It lives in the instance state dir, not the project, so the project stays
# clean and the devcontainer.local_folder label ends up unique per instance.
# workspaceMount is what points the container at the real project directory.

: "${DCX_AWS_REGION:=eu-west-1}"

dcx_render_devcontainer() { # <instance>
  local name="$1" dir profile workspace auth image env_file
  dir="$(dcx_instance_dir "$name")"
  profile="$(dcx_meta "$name" '.profile')"
  workspace="$(dcx_meta "$name" '.workspace')"
  auth="$(dcx_meta "$name" '.auth')"
  image="$(dcx_image "$profile")"
  env_file="$(dcx_env_file "$name")"

  # Mount geometry. Normal instances land at /workspace, unchanged.
  #
  # A worktree instance instead mirrors host absolute paths into the container.
  # Its .git is a file pointing at <main>/.git/worktrees/<n>, an absolute path
  # written by the host, so unless both trees sit at their host paths on both
  # sides that link dangles - and a host-side `git worktree prune` reaps the
  # container's checkout, because from the host the recorded path does not exist.
  local wt_main mount_src mount_tgt ws_folder extra_mounts
  wt_main="$(dcx_meta "$name" '.worktree_main')"
  extra_mounts='[]'
  if [ -n "$wt_main" ]; then
    ws_folder="$workspace"
    case "$workspace" in
      "$wt_main"/*)
        # Nested checkout. workspaceFolder may be a subdirectory of
        # workspaceMount, so one bind covers both trees; a second would overlap.
        mount_src="$wt_main" ;;
      *)
        # Sibling checkout, which is where Herdr puts them by default. The main
        # repo needs its own bind, read-write: commits from the worktree write
        # objects to <main>/.git/objects and refs to <main>/.git/worktrees/<n>.
        mount_src="$workspace"
        extra_mounts="$(jq -n --arg m "$wt_main" \
          '[("source=" + $m + ",target=" + $m + ",type=bind,consistency=delegated")]')" ;;
    esac
    mount_tgt="$mount_src"
  else
    mount_src="$workspace"
    mount_tgt="/workspace"
    ws_folder="/workspace"
  fi

  # containerEnv is assembled per profile: a base container has no business
  # carrying KUBECONFIG, and an --auth oauth container must not see
  # ANTHROPIC_BASE_URL or it would talk to the gateway with the wrong credential.
  local envjson
  envjson="$(jq -n \
    --arg cfg   "/home/node/.claude" \
    --arg inst  "$name" \
    '{CLAUDE_CONFIG_DIR:$cfg, DCX_INSTANCE:$inst}')"

  if [ "$auth" = "litellm" ]; then
    envjson="$(printf '%s' "$envjson" | jq \
      --arg url "$DCX_LITELLM_URL" \
      '. + {ANTHROPIC_BASE_URL:$url,
            ANTHROPIC_DEFAULT_OPUS_MODEL:"claude-opus-5",
            ANTHROPIC_DEFAULT_SONNET_MODEL:"claude-sonnet-5",
            ANTHROPIC_DEFAULT_HAIKU_MODEL:"claude-haiku-4-5",
            CLAUDE_CODE_SUBAGENT_MODEL:"claude-haiku-4-5",
            CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS:"1"}')"
  fi

  if dcx_profile_has_k8s "$profile"; then
    envjson="$(printf '%s' "$envjson" | jq '. + {KUBECONFIG:"/run/dcx-creds/kubeconfig.yaml"}')"
  fi
  if dcx_profile_has_gcp "$profile"; then
    envjson="$(printf '%s' "$envjson" | jq \
      --arg p "$(dcx_meta "$name" '.gcp.project')" \
      '. + {CLOUDSDK_AUTH_ACCESS_TOKEN_FILE:"/run/dcx-creds/gcp.token"}
         + (if $p == "" then {} else {CLOUDSDK_CORE_PROJECT:$p} end)')"
  fi
  if dcx_profile_has_aws "$profile"; then
    envjson="$(printf '%s' "$envjson" | jq \
      --arg r "$(dcx_meta "$name" '.aws.region' || true)" \
      --arg d "$DCX_AWS_REGION" \
      '. + {AWS_SHARED_CREDENTIALS_FILE:"/run/dcx-creds/aws-credentials",
            AWS_REGION:(if $r == "" then $d else $r end)}')"
  fi

  jq -n \
    --arg name      "$name" \
    --arg image     "$image" \
    --arg mountsrc  "$mount_src" \
    --arg mounttgt  "$mount_tgt" \
    --arg wsfolder  "$ws_folder" \
    --argjson extra "$extra_mounts" \
    --arg envfile   "$env_file" \
    --arg creds     "$(dcx_creds_dir "$name")" \
    --arg share     "$(dcx_share_dir "$name")" \
    --arg volc      "$(dcx_volume_claude "$name")" \
    --arg volh      "$(dcx_volume_history "$name")" \
    --argjson env   "$envjson" \
    '{
      name: ("dcx-" + $name),
      image: $image,
      workspaceMount: ("source=" + $mountsrc + ",target=" + $mounttgt + ",type=bind,consistency=delegated"),
      workspaceFolder: $wsfolder,
      remoteUser: "node",
      initializeCommand: ("dccred env " + $name),
      runArgs: ["--env-file", $envfile],
      mounts: ([
        ("source=" + $volc  + ",target=/home/node/.claude,type=volume"),
        ("source=" + $volh  + ",target=/commandhistory,type=volume"),
        ("source=" + $creds + ",target=/run/dcx-creds,type=bind,readonly"),
        ("source=" + $share + ",target=/run/dcx-share,type=bind,readonly")
      ] + $extra),
      containerEnv: $env,
      postCreateCommand: "/usr/local/bin/dcx-post-create"
    }' > "$dir/.devcontainer/devcontainer.json"
}
