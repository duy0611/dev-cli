# shellcheck shell=bash
# devcontainer.env — the values only the host can supply.
#
# Regenerated on every launch by `dccred env`, which the generated
# devcontainer.json wires up as initializeCommand. That means the LiteLLM token
# refreshes whether the container is started by dcx or opened in VS Code, and
# neither path can start with a stale one.

: "${DCX_LITELLM_URL:=https://litellm.litellm-superidp.supermetrics.dev}"
: "${DCX_KEYCHAIN_LITELLM_SERVICE:=ANTHROPIC_AUTH_TOKEN}"
: "${DCX_KEYCHAIN_OAUTH_SERVICE:=Claude Code-credentials}"

dcx_keychain_get() { # <service>
  command -v security >/dev/null 2>&1 || return 1
  security find-generic-password -a "$USER" -s "$1" -w 2>/dev/null
}

dcx_write_env_file() { # <instance>
  local name="$1" auth out token
  auth="$(dcx_meta "$name" '.auth')"
  out="$(dcx_env_file "$name")"

  : >"$out"
  chmod 600 "$out"

  # --- LiteLLM token ---------------------------------------------------------

  if [ "$auth" = "litellm" ]; then
    token="$(dcx_keychain_get "$DCX_KEYCHAIN_LITELLM_SERVICE" || true)"
    token="${token:-${ANTHROPIC_AUTH_TOKEN:-}}"

    if [ -n "$token" ]; then
      # Deliberately unquoted: --env-file treats quotes as literal characters,
      # so KEY="value" would send the quotes as part of the token.
      printf 'ANTHROPIC_AUTH_TOKEN=%s\n' "$token" >>"$out"
    else
      # Omitted rather than written empty: an empty ANTHROPIC_AUTH_TOKEN is sent
      # as a literal "Bearer " header, which fails more confusingly than no
      # token at all.
      warn "no ANTHROPIC_AUTH_TOKEN in the Keychain or environment."
      warn "Claude in this container will not authenticate to LiteLLM. Add it with:"
      warn "  security add-generic-password -a \"\$USER\" -s $DCX_KEYCHAIN_LITELLM_SERVICE -w"
    fi
  fi

  # --- git identity ----------------------------------------------------------

  # Passed through rather than relying on the gitconfig snapshot, which is
  # opt-in: without an identity the first commit inside fails with "Author
  # identity unknown".
  #
  # Identity only. The host's user.signingkey is a GPG key id the container has
  # no secret for, and forwarding commit.gpgsign along with it used to turn on
  # signing in every sandbox — including ones launched without --sign — so every
  # commit died with "No secret key". post-create decides signing on its own,
  # from what is actually present in /run/dcx-creds.
  _dcx_env_git() { # <VAR> <git-key>
    local v; v="$(git config --get "$2" 2>/dev/null || true)"
    [ -n "$v" ] && printf '%s=%s\n' "$1" "$v" >>"$out" || true
  }
  _dcx_env_git DEVCONTAINER_GIT_NAME  user.name
  _dcx_env_git DEVCONTAINER_GIT_EMAIL user.email
  unset -f _dcx_env_git
}

# --- opt-in host material ----------------------------------------------------

# Copied into share/, which is bind-mounted read-only at /run/dcx-share.
dcx_stage_gitconfig() { # <instance>
  local share; share="$(dcx_share_dir "$1")"
  [ -f "$HOME/.gitconfig" ] || { warn "no ~/.gitconfig to snapshot"; return 0; }
  cp "$HOME/.gitconfig" "$share/gitconfig-host"
  info "snapshotted ~/.gitconfig"
}

# Commit signing. Copied into creds/, not share/: this is real private key
# material, and creds/ is the 0700 directory the read-only /run/dcx-creds mount
# points at.
#
# SSH signing rather than GPG because it is the only design that ports. GPG
# would need the host gpg-agent socket forwarded, and a devcontainer mount path
# resolves inside the podman VM, where virtiofs cannot pass a unix socket; every
# workaround is hypervisor-specific. A key file behind a fixed path is the same
# model kubectl/gcloud/aws already use here, and it works unchanged wherever the
# container runs.
#
# Both halves are staged: `ssh-keygen -Y sign` looks for <key>.pub next to the
# private key.
dcx_stage_signing() { # <instance>
  local creds key; creds="$(dcx_creds_dir "$1")"; key="$(dcx_signing_key)"
  [ -f "$key" ] || die "no sandbox signing key yet; create one with: dccred signing-key"
  dcx_write_secret "$creds/sandbox-signing"     <"$key"
  dcx_write_secret "$creds/sandbox-signing.pub" <"$key.pub"
  [ -f "$key.expiry" ] && cp "$key.expiry" "$creds/signing.expiry" || true
  info "staged the sandbox signing key"
}
