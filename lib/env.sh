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
  _dcx_env_git() { # <VAR> <git-key>
    local v; v="$(git config --get "$2" 2>/dev/null || true)"
    [ -n "$v" ] && printf '%s=%s\n' "$1" "$v" >>"$out" || true
  }
  _dcx_env_git DEVCONTAINER_GIT_NAME       user.name
  _dcx_env_git DEVCONTAINER_GIT_EMAIL      user.email
  _dcx_env_git DEVCONTAINER_GIT_SIGNINGKEY user.signingkey
  _dcx_env_git DEVCONTAINER_GIT_GPGSIGN    commit.gpgsign
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

# Public half only. Not secret — publishing it is the point of a public key —
# but exported at run time rather than committed so each person gets their own.
dcx_stage_gpg() { # <instance>
  local share key; share="$(dcx_share_dir "$1")"
  key="$(git config --get user.signingkey 2>/dev/null || true)"
  [ -n "$key" ] || { warn "no user.signingkey on the host; skipping GPG"; return 0; }
  if command -v gpg >/dev/null 2>&1 && gpg --export --armor "$key" >"$share/gpg-public-key.asc" 2>/dev/null \
     && [ -s "$share/gpg-public-key.asc" ]; then
    info "exported GPG public key $key"
  else
    rm -f "$share/gpg-public-key.asc"
    warn "could not export the public key for $key; signing inside will fail"
  fi
}
