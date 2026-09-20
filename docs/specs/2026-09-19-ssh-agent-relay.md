# SSH agent relay

`git fetch` inside a container `dev` made fails:

```
git@github.com: Permission denied (publickey)
```

The container has no credential of any kind, and `dev` had no way to give it
one. Two columns, `ssh_forward` and `gpg_forward`, were reserved for this in the
first migration and never read by anything.

This carries the host's ssh agent into a container for the length of one
command. Nothing is copied in: an agent signs on request and never surrenders
the key, so what crosses the boundary is the conversation, not the secret.

## Why not a credential in the container

These containers run coding agents — programs that execute code and install
packages on their own. Treat anything placed in one as readable.

| | Can it be taken? | What it reaches |
|---|---|---|
| Personal access token | yes | every repo in scope, until revoked |
| GitHub App token | yes | one repo, one hour |
| Deploy key | yes | one repo, forever |
| **Agent relay** | **no** | — |

The App token was the strongest of the three and still lost: one hour is shorter
than an agent session, and renewing it needs a live channel back to the host —
which is the relay, so the relay may as well carry the agent itself.

What this buys is narrow and worth stating plainly: a credential never *rests*
in the container. While a session is live anything inside it can ask the agent
to sign, exactly as with ordinary ssh agent forwarding. The window is the
command, not the container's lifetime, and nothing survives it.

## Why a relay and not a bind mount

Sharing the agent socket by mounting it is the obvious approach and the wrong
one. It fails differently on every host: macOS exposes only a synthesised
`/run/host-services/ssh-auth.sock` and refuses to mount the real socket
(`operation not supported`, because the engine will not pass a macOS socket into
its Linux VM); podman, colima and Rancher Desktop have no such path at all; the
mounted socket arrives owned by root against a container running as `vscode`;
and a mount is fixed at creation, so changing the setting would need a rebuild.

A relay has none of those problems, and it is what the VS Code Dev Containers
extension does. Its logs are the proof:

```
SSH_AUTH_SOCK in container (/tmp/vscode-ssh-auth-<uuid>.sock)
forwarded to local host (\\.\pipe\openssh-ssh-agent)
```

A Windows named pipe cannot be bind-mounted into a Linux container. DevPod and
Coder relay too — DevPod over an ssh tunnel, Coder through `GIT_ASKPASS`
pointing at its own binary.

Relaying also works on Kubernetes, where there is no host socket to mount at
all. Both providers implement `AgentForwarder`, which inverts `Syncer`: that one
only k8s can do.

## The transport

The provider's own exec channel, so nothing new has to be established and the
relay reaches the same container every other command does. On local that is
`devcontainer exec` — sharing `execArgs` with `Exec`, because the id labels
decide which container is reached and a second copy of that list would be a
second chance to get it wrong (invariant 1). On k8s it is `kubectl exec`,
through `stream` rather than `pipeInto`: the latter is one-directional, and its
comment records the hang that taught us why.

One exec channel carries several connections, so the stream is framed and
multiplexed. `git fetch --all` opens one agent connection per remote; unframed
they would interleave into a corrupt protocol. Nothing parses the agent protocol
itself — it is request-response over a stream, and both halves only copy bytes.

Both ends register a channel *before* starting the goroutine that serves it. The
first version registered inside the goroutine, and a reply that arrived before
the scheduler ran it was dropped as belonging to an unknown channel, leaving
`ssh-add` waiting forever for an answer the agent had already given. It passed
once and hung on the second run; `-race -count=3` is what found it.

## The in-container half is a binary

It has to be. The base image ships no `socat`, `nc` or `ncat`, so there is no
shell one-liner to lean on. It does have `python3`, but depending on that would
work here and fail on any project whose own devcontainer is alpine or
distroless — and this must work for a project that brings its own
configuration.

So `cmd/dev-relay` is cross-compiled for `linux/amd64` and `linux/arm64`,
embedded with `go:embed`, and streamed in per session. This is the one
concession the design makes: `dev` otherwise puts nothing inside a container.

It lives in `internal/relaybin`, separate from `internal/relay`, because
`cmd/dev-relay` imports the logic and the embed would then need the binary built
from the package being compiled to produce it. Split, nothing on the path to
building the relay depends on the embedded copy.

Its path in the container names its own content hash, so a second session finds
it already there. Otherwise every `git fetch` would push two megabytes through
the exec channel before doing any work. The copy is atomic — written to `.part`
and moved — so a session that dies mid-copy cannot leave a truncated binary the
next one would find with `test -x` and try to run.

## Lifetime

One `dev` command. `agent` and `shell` are long-running foreground processes
already, so the common case needs no daemon, which matters for a tool whose
whole design is shelling out and exiting.

Each command gets its own socket, named from random bytes. Two shells attached
to one container must both work, and a shared path would mean whichever exited
first pulled the socket out from under the other.

Teardown closes stdin first and waits before killing. A killed relay cannot run
its own cleanup, and the socket would outlive the session in a container that
outlives it too. `Execute` installs a `signal.NotifyContext` so Ctrl-C cancels
the context rather than killing the process, which is what lets the deferred
teardown run at all; a second Ctrl-C still kills.

## Signing

Since git 2.34 an ssh key can sign a commit, so the agent that authenticates the
push signs it too. That is why `gpg_forward` is deleted rather than implemented:
GPG agent forwarding is the fiddliest part of this area and buys nothing here.

Configured through `GIT_CONFIG_COUNT` / `GIT_CONFIG_KEY_n` / `GIT_CONFIG_VALUE_n`
(git 2.31+; the base image has 2.49), so nothing is written into the container
and nothing into the project's `.git/config` — which lives in the bind-mounted
folder and is covered by invariant 9. The count must equal the number of pairs,
numbered from zero with no gaps: a gap is a fatal git error rather than a
skipped entry, so the list is built in one place and counted from itself.

`user.signingkey` carries the `key::` prefix. Without it git reads the value as
a *filename* and fails looking for a file that does not exist. The key is read
from the agent by speaking the agent protocol directly rather than shelling out
to `ssh-add -L`, which is not guaranteed to be on the host's PATH and whose
output would be a second format to get wrong.

Two decisions that are deliberate rather than accidental:

- **The first key the agent holds.** An agent gives no indication which of its
  keys the operator meant. If the first is not the one the forge knows, the
  commit arrives unverified — visible, and fixable by reordering `ssh-add`.
- **No `allowedSignersFile`.** Signing works and the forge verifies it; only
  *local* verification needs a file mapping emails to keys, and that mapping is
  the operator's to decide, not something to guess from a git identity.

Signing is skipped, with a warning, when the workspace already sets
`GIT_CONFIG_*` itself. Both would arrive as `--remote-env` and the last would
win, so adding ours would silently drop theirs — and a half-applied count breaks
every git command in the container, not just the signing.

## Testing

Everything runs under `make test` with no container engine.

The relay is tested against a real `ssh-agent` with a real key, reached by the
real `ssh-add`, because the agent protocol is the whole content of that code and
a stub would only confirm the shape this code already assumes. Signing is tested
by handing real `git` exactly the environment a container would receive and
checking `%G?` reports `G` — a verified signature. The two traps above, the
`key::` prefix and the contiguous count, both produce a plausible-looking
configuration that git rejects, so the shape alone is not evidence.

The provider halves use the existing stub-executable harness. The k8s tests
replace `PATH` rather than prepending, as the rest of that package does.
