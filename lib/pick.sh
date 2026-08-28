# shellcheck shell=bash
# fzf pickers for instance scope. Run on the host, at instance create time
# only; the choices land in instance.json and are reused on every later launch.

# One wrapper so every picker looks the same and cancelling is always a
# non-zero exit the caller turns into a die().
#
# fzf is used when present but is NOT a requirement: it is not installed by
# default on macOS, and making the whole profile flow depend on a brew package
# would be a poor trade for a menu. The fallback is a numbered list.
_dcx_fzf() { # <prompt> [extra fzf args...]
  local prompt="$1"; shift

  if command -v fzf >/dev/null 2>&1; then
    fzf --height=40% --reverse --prompt="$prompt " --no-multi "$@"
    return
  fi

  local lines=() line i reply
  while IFS= read -r line; do
    [ -n "$line" ] && lines+=("$line")
  done
  [ ${#lines[@]} -gt 0 ] || return 1

  # Prompt and list go to stderr: stdout is the selection, which the caller
  # captures in a $( ).
  i=1
  for line in ${lines[@]+"${lines[@]}"}; do
    printf '%3d) %s\n' "$i" "$line" >&2
    i=$(( i + 1 ))
  done
  printf '%s ' "$prompt" >&2

  # /dev/tty, not stdin: stdin is the pipe feeding the list.
  read -r reply < /dev/tty || return 1
  case "$reply" in
    ''|*[!0-9]*) return 1 ;;
  esac
  [ "$reply" -ge 1 ] && [ "$reply" -le ${#lines[@]} ] || return 1
  printf '%s\n' "${lines[$(( reply - 1 ))]}"
}

dcx_pick_profile() {
  local choice
  choice="$(printf '%s\n' \
      "base   claude only, no cloud credentials" \
      "k8s    kubectl, helm, k9s, aws  (minted SA token)" \
      "gcp    gcloud SDK               (impersonated SA token)" \
      "full   both toolchains, both credentials" \
    | _dcx_fzf "profile>" )" || return 1
  [ -n "$choice" ] || return 1
  printf '%s\n' "${choice%% *}"
}

dcx_pick_kube_context() {
  local ctx
  ctx="$(kubectl config get-contexts -o name 2>/dev/null | sort | _dcx_fzf "cluster>")" || return 1
  [ -n "$ctx" ] || return 1
  printf '%s\n' "$ctx"
}

dcx_pick_namespace() { # <context>
  local ns
  # Listing namespaces uses your own credentials, which is fine: this runs on
  # the host, before anything is minted.
  ns="$(kubectl --context "$1" get ns -o name 2>/dev/null | sed 's|^namespace/||' | sort \
        | _dcx_fzf "namespace>")" || return 1
  [ -n "$ns" ] || return 1
  printf '%s\n' "$ns"
}

dcx_pick_serviceaccount() { # <context> <namespace>
  local sa
  sa="$(kubectl --context "$1" -n "$2" get sa -o name 2>/dev/null | sed 's|^serviceaccount/||' | sort \
        | _dcx_fzf "serviceaccount>")" || return 1
  [ -n "$sa" ] || return 1
  printf '%s\n' "$sa"
}

dcx_pick_gcp_project() {
  local p
  p="$(gcloud projects list --format='value(projectId)' 2>/dev/null | sort | _dcx_fzf "gcp project>")" || return 1
  [ -n "$p" ] || return 1
  printf '%s\n' "$p"
}

# Impersonation target. The empty choice is offered deliberately and labelled
# for what it is: convenient, short-lived, and NOT privilege-reduced.
dcx_pick_gcp_sa() { # <project>
  local sa
  sa="$( { printf '(none — use your own identity, NOT privilege-reduced)\n'
           gcloud iam service-accounts list --project "$1" --format='value(email)' 2>/dev/null | sort
         } | _dcx_fzf "impersonate>")" || return 1
  case "$sa" in
    ''|'(none'*) printf '\n' ;;
    *) printf '%s\n' "$sa" ;;
  esac
}

dcx_pick_aws_role() {
  local role
  role="$( { printf '(none — no AWS credentials in the container)\n'
             # Roles you have written down beat roles you can list: iam:ListRoles
             # is usually denied, and the useful roles are cross-account anyway.
             { [ -f "$HOME/.config/dcx/aws-roles" ] && grep -vE '^[[:space:]]*(#|$)' "$HOME/.config/dcx/aws-roles"; } || true
             aws configure list-profiles 2>/dev/null | sed 's/^/profile:/'
           } | _dcx_fzf "aws role>")" || return 1
  case "$role" in
    ''|'(none'*) printf '\n' ;;
    *) printf '%s\n' "$role" ;;
  esac
}
