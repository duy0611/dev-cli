#!/usr/bin/env bash
# Smoke test for the base profile. Run via `make test`.
#
# Creates a real instance, asserts the things that have actually broken during
# development, then tears it down. The base profile is used because it needs no
# cloud credentials and therefore no picker input, so this runs unattended.
#
# Non-obvious assertions, and why each one is here:
#   - plugins load           the marketplace cache is keyed to absolute paths,
#                            so a mis-staged bake fails with 'cache-miss'
#   - no locale warning      the image must generate en_US.UTF-8, because
#                            devcontainer exec forwards the host's LC_ALL
#   - workspace is writable  the bind must reach the host project, and podman
#                            must map the uid; a wrong path silently mounts an
#                            empty directory inside the VM instead
#   - label is the state dir all instance isolation depends on it
set -euo pipefail

REPO="$(cd -P "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DCX="$REPO/bin/dcx"
NAME=dcx-smoketest
WS="$(mktemp -d "$HOME/.dcx-smoketest.XXXXXX")"

pass=0 fail=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=$((fail+1)); }
check() { # <description> <expected-substring> <actual>
  case "$3" in *"$2"*) ok "$1" ;; *) bad "$1 (got: $(printf '%s' "$3" | head -c 120))" ;; esac
}

cleanup() {
  printf '\n==> teardown\n'
  "$DCX" --rm "$NAME" >/dev/null 2>&1 || true
  rm -rf "$WS"
}
trap cleanup EXIT

printf '==> creating instance %s\n' "$NAME"
echo "smoke-test-marker" > "$WS/marker.txt"
# A fresh instance every run: a stale one would hide exactly the create-time
# bugs this is meant to catch.
"$DCX" --rm "$NAME" >/dev/null 2>&1 || true
"$DCX" -p base --as "$NAME" -f "$WS" -- true

printf '\n==> assertions\n'

run() { "$DCX" -p base --as "$NAME" -f "$WS" -- bash -lc "$1" 2>&1; }

check "claude is installed"        "Claude Code"  "$(run 'claude --version')"
check "plugins loaded from seed"   "superpowers"  "$(run 'claude plugin list')"
check "no marketplace cache-miss"  "NO_CACHE_MISS" \
      "$(run 'claude plugin list 2>&1 | grep -q cache-miss && echo CACHE_MISS || echo NO_CACHE_MISS')"
check "workspace bind reaches host" "smoke-test-marker" "$(run 'cat /workspace/marker.txt')"
check "workspace is writable"      "WRITE_OK" \
      "$(run 'echo x > /workspace/.probe && echo WRITE_OK')"
check "runs as non-root"           "uid=1000" "$(run 'id')"
check "instance name is exported"  "$NAME"    "$(run 'echo $DCX_INSTANCE')"
check "LiteLLM base url is set"    "litellm"  "$(run 'echo $ANTHROPIC_BASE_URL')"
check "git identity present"       "@"        "$(run 'git config --get user.email')"

# The warning went to stderr on every command before the locale was generated.
locale_out="$(LC_ALL=en_US.UTF-8 LANG=en_US.UTF-8 run 'echo LOCALE_DONE')"
case "$locale_out" in
  *"cannot change locale"*) bad "no locale warning" ;;
  *LOCALE_DONE*)            ok  "no locale warning" ;;
  *)                        bad "no locale warning (unexpected: $locale_out)" ;;
esac

# Host-side: the label must be the state dir, not the project. Everything about
# running two instances on one project rests on this.
label="$(docker ps --filter "label=devcontainer.local_folder=$HOME/.local/state/dcx/instances/$NAME" -q)"
[ -n "$label" ] && ok "container label is the state dir" || bad "container label is the state dir"

# The host must see what the container wrote.
[ -f "$WS/.probe" ] && ok "container writes land on the host" || bad "container writes land on the host"

printf '\n==> %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
