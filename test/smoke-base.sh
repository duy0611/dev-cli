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
#   - project label is the repo the one deliberate exception: a repo with its
#                            own .devcontainer/ is registered but not rendered,
#                            so its label is the repo and every lookup has to
#                            go through dcx_local_folder
#   - commits work unsigned   the host's commit.gpgsign used to be forwarded
#                             into every instance, so every commit failed with
#                             "No secret key" in sandboxes that never asked
#                             for signing
set -euo pipefail

REPO="$(cd -P "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DCX="$REPO/bin/dcx"
DCCRED="$REPO/bin/dccred"
NAME=dcx-smoketest
SIGN_NAME=dcx-smoketest-sign
WT_NAME=dcx-smoketest-wt
PROJ_NAME=dcx-smoketest-proj
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
  "$DCX" --rm "$SIGN_NAME" >/dev/null 2>&1 || true
  "$DCX" --rm "$WT_NAME" >/dev/null 2>&1 || true
  "$DCX" --rm "$PROJ_NAME" >/dev/null 2>&1 || true
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
check "signing off without --sign" "false"    "$(run 'git config --get commit.gpgsign')"
# The regression itself: not the config value, but whether a commit completes.
check "unsigned commit succeeds"   "COMMIT_OK" \
      "$(run 'cd "$(mktemp -d)" && git init -q . && git commit -q --allow-empty -m probe && echo COMMIT_OK')"

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

# --- worktree ------------------------------------------------------------------
#
# The whole feature is one string equality: the checkout's absolute path must be
# the same inside the container as on the host. When it is not, worktree
# metadata written on one side names a path the other does not have, and a
# host-side `git worktree prune` reaps the container's checkout.
#
# A sibling layout is used deliberately - that is where Herdr puts checkouts,
# and it is the case that needs the second bind.
printf '\n==> worktree\n'
WT_REPO="$WS/wtrepo"
WT_PATH="$WS/wtcheckout"
git init -q "$WT_REPO"
git -C "$WT_REPO" -c user.email=smoke@test -c user.name=smoke commit -q --allow-empty -m init
git -C "$WT_REPO" worktree add -q "$WT_PATH" -b smoke-wt

"$DCX" --rm "$WT_NAME" >/dev/null 2>&1 || true
"$DCX" -p base --as "$WT_NAME" -f "$WT_PATH" -- true
runw() { "$DCX" -p base --as "$WT_NAME" -f "$WT_PATH" -- bash -lc "$1" 2>&1; }

check "worktree path matches the host" "$WT_PATH" "$(runw 'git rev-parse --show-toplevel')"
check "main repo is co-mounted"        "$WT_REPO/.git" \
      "$(runw 'git rev-parse --path-format=absolute --git-common-dir')"

# Byte-identical, not merely "both non-empty": divergence here is exactly the
# bug, and it is invisible unless the two outputs are compared directly.
host_list="$(git -C "$WT_PATH" worktree list)"
cont_list="$(runw 'git worktree list')"
[ "$host_list" = "$cont_list" ] \
  && ok "git worktree list agrees host and container" \
  || bad "git worktree list agrees host and container (host: $host_list / container: $cont_list)"

# A commit from the container must reach the MAIN repo's object store, which is
# the read-write half of the co-mount doing its job.
sha="$(runw 'git -c user.email=smoke@test -c user.name=smoke commit -q --allow-empty -m wtprobe && git rev-parse HEAD' | tail -1)"
git -C "$WT_REPO" cat-file -e "$sha" 2>/dev/null \
  && ok "container commit lands in the main object store" \
  || bad "container commit lands in the main object store (sha: $sha)"

# The isolation invariant, re-asserted under the second mount geometry.
wtlabel="$(docker ps --filter "label=devcontainer.local_folder=$HOME/.local/state/dcx/instances/$WT_NAME" -q)"
[ -n "$wtlabel" ] && ok "worktree instance label is the state dir" \
                  || bad "worktree instance label is the state dir"

# --- project mode ---------------------------------------------------------------
#
# A repo that ships its own .devcontainer/ is used as-is, but must still be
# registered, or it is a container the toolchain cannot name: invisible to
# --list, unremovable by --rm. The label here is the repo, not the state dir,
# which is the one place project instances diverge from the isolation rule
# asserted above.
printf '\n==> project mode\n'
PROJ_REPO="$WS/projrepo"
mkdir -p "$PROJ_REPO/.devcontainer"
cat > "$PROJ_REPO/.devcontainer/devcontainer.json" <<EOF
{
  "name": "$PROJ_NAME",
  "image": "localhost/dcx-base:latest",
  "remoteUser": "node"
}
EOF
# pwd -P, because that is what dcx records and what the label is compared with.
PROJ_REPO="$(cd "$PROJ_REPO" && pwd -P)"

"$DCX" --rm "$PROJ_NAME" >/dev/null 2>&1 || true
"$DCX" --as "$PROJ_NAME" -f "$PROJ_REPO" -- true

listing="$("$DCX" --list)"
check "project instance is listed"   "$PROJ_NAME" "$listing"
check "listed with profile project"  "project"    "$listing"
check "listed against the repo"      "$PROJ_REPO" "$listing"
check "dccred status sees it running" "running"   "$("$DCCRED" status "$PROJ_NAME")"

# The lookup that had to change: dcx_local_folder returns the repo here, so the
# liveness filter and --rm find a container whose label is not the state dir.
projlabel="$(docker ps --filter "label=devcontainer.local_folder=$PROJ_REPO" -q)"
[ -n "$projlabel" ] && ok "project label is the repo" || bad "project label is the repo"

# Minting into an instance dcx owns no creds mount for would be a silent no-op;
# `dccred pick` would go further and render a devcontainer.json nobody reads.
check "dccred mint refuses" "project instance" "$("$DCCRED" mint "$PROJ_NAME" 2>&1 || true)"
check "dccred pick refuses" "project instance" "$("$DCCRED" pick "$PROJ_NAME" 2>&1 || true)"

"$DCX" --rm "$PROJ_NAME" >/dev/null
[ -z "$(docker ps -aq --filter "label=devcontainer.local_folder=$PROJ_REPO")" ] \
  && ok "--rm removed the project container" || bad "--rm removed the project container"
[ ! -d "$HOME/.local/state/dcx/instances/$PROJ_NAME" ] \
  && ok "--rm removed the project state" || bad "--rm removed the project state"
# The repo is not ours to delete.
[ -f "$PROJ_REPO/.devcontainer/devcontainer.json" ] \
  && ok "--rm left the repo's devcontainer.json" || bad "--rm left the repo's devcontainer.json"

# --- signing ------------------------------------------------------------------
#
# A second instance, because --sign is decided at create. Skipped rather than
# failed when no sandbox key exists: `dccred signing-key` is a deliberate,
# one-time act on a machine, and generating one here would leave key material
# behind on any host that runs `make test`.
if [ -f "$HOME/.local/state/dcx/signing/sandbox-signing" ]; then
  printf '\n==> signing (--sign)\n'
  "$DCX" --rm "$SIGN_NAME" >/dev/null 2>&1 || true
  "$DCX" -p base --as "$SIGN_NAME" -f "$WS" --sign -- true
  runs() { "$DCX" -p base --as "$SIGN_NAME" -f "$WS" -- bash -lc "$1" 2>&1; }

  check "signing format is ssh"  "ssh"      "$(runs 'git config --get gpg.format')"
  check "signing key is mounted" "PRESENT"  "$(runs 'test -r /run/dcx-creds/sandbox-signing && echo PRESENT')"
  # End to end: sign a commit, then verify it with the allowed_signers file
  # post-create wrote. A key git can read but not verify against still reads as
  # broken to anyone using --show-signature.
  check "commit signs and verifies" 'Good "git" signature' \
        "$(runs 'cd "$(mktemp -d)" && git init -q . && git commit -q --allow-empty -m signed && git log --show-signature -1')"

  # --sign applied to an instance created without it, with no recreate. This is
  # the regression: the key reaches a live container through the creds/ bind
  # mount, but the git config pointing at it used to be written only by
  # post-create, so the flag silently did nothing until the container was
  # destroyed. $NAME is reused precisely because it was created unsigned and
  # asserted "false" above.
  before="$(docker ps -q --filter "label=devcontainer.local_folder=$HOME/.local/state/dcx/instances/$NAME")"
  "$DCX" -p base --as "$NAME" -f "$WS" --sign -- true >/dev/null
  after="$(docker ps -q --filter "label=devcontainer.local_folder=$HOME/.local/state/dcx/instances/$NAME")"

  check "--sign lands on a live instance" "true" "$(run 'git config --get commit.gpgsign')"
  check "--sign signs without a recreate" 'Good "git" signature' \
        "$(run 'cd "$(mktemp -d)" && git init -q . && git commit -q --allow-empty -m retro && git log --show-signature -1')"
  # The point of the change: same container throughout. A recreate would also
  # produce a signed commit, and would hide the bug.
  [ -n "$before" ] && [ "$before" = "$after" ] \
    && ok "--sign reused the running container" \
    || bad "--sign reused the running container (before=$before after=$after)"
else
  printf '\n==> signing: skipped (no key; run: %s signing-key)\n' "$DCCRED"
fi

printf '\n==> %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
