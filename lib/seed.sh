# shellcheck shell=bash
# OAuth credential seeding. Only used with --auth oauth; the default LiteLLM
# path needs none of this.

# Claude Code on macOS keeps its credential in the Keychain, not on disk, so
# copying ~/.claude would miss it entirely. The container is Linux and uses the
# file-based store, so the value round-trips as .credentials.json.
#
# Every instance seeded this way holds a copy of your real token. That is the
# accepted trade for the flag; the LiteLLM default avoids it.
dcx_seed_oauth() { # <instance>
  local name="$1" cred vol tmp
  cred="$(dcx_keychain_get "$DCX_KEYCHAIN_OAUTH_SERVICE" || true)"
  if [ -z "$cred" ]; then
    warn "no Claude OAuth credential in the Keychain (service: $DCX_KEYCHAIN_OAUTH_SERVICE)."
    warn "Claude will prompt for login on first run in this container."
    return 0
  fi

  vol="$(dcx_volume_claude "$name")"
  tmp="$(dcx_instance_dir "$name")/.credentials.json.seed"
  printf '%s' "$cred" | dcx_write_secret "$tmp"

  # A one-shot container is the only way to write into a named volume before
  # the real container exists. uid 1000 is `node` in our images.
  docker run --rm \
    -v "$vol:/dest" \
    -v "$tmp:/seed/credentials.json:ro" \
    docker.io/library/alpine:latest \
    sh -c 'cp /seed/credentials.json /dest/.credentials.json &&
           chown 1000:1000 /dest/.credentials.json &&
           chmod 600 /dest/.credentials.json' >/dev/null

  rm -f "$tmp"
  info "seeded Claude OAuth credential into $vol"
}
