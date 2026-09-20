# Git credentials for containers

**Status:** proposal, for review. Nothing here is built yet.

## The problem

Run `git fetch` inside a container `dev` made, and it fails:

```
git@github.com: Permission denied (publickey)
```

The container has no way to prove who you are. No SSH key, no token, nothing.
And `dev` has no feature to give it one. There are two unused columns in the
database, `ssh_forward` and `gpg_forward` (`internal/model/model.go:31-32`),
which were meant for exactly this — but nothing reads them. The feature was
planned in `docs/specs/2026-09-14-dev-cli.md` and never built.

We can't fix it by editing the project's `.devcontainer/devcontainer.json`,
because `dev` never writes into a project folder. That's a deliberate rule.

## What makes this harder than it looks

The obvious fix is to put a token or an SSH key inside the container. That
works, and it's what most people do.

The problem is what these containers are *for*. They run coding agents —
programs that execute code, install packages, and act on their own. If anything
in the container turns hostile, or a dependency you installed is malicious, then
**anything stored in that container is readable**. A token in an environment
variable is one `cat /proc/self/environ` away.

So the real question isn't "how do we get a credential in there". It's "how much
damage can someone do if they get it".

| Approach | Can it be stolen? | What it unlocks |
|---|---|---|
| Personal access token (PAT) | Yes | Every repo the token covers, until revoked |
| GitHub App token | Yes | One repo, expires after 1 hour |
| Deploy key | Yes | One repo, never expires |
| **Agent relay** | **No** | — |

A **personal access token** (PAT) is a password-like string that stands in for
your GitHub account. It works everywhere, forever, until you notice and revoke
it.

A **GitHub App token** can be locked to one repository and dies after an hour —
better, but an hour is shorter than an agent session, and renewing it needs a
live connection back to your machine anyway.

A **deploy key** is an SSH key attached to one repository instead of your
account. Small blast radius, but it never expires, and GitHub won't let you
reuse one key across repositories.

## The plan: relay the SSH agent

You almost certainly already run an **SSH agent** — the background program that
holds your unlocked SSH keys so you don't retype your passphrase all day. Its
key property: it will *use* a key on request, but it will never *hand the key
over*. It's a signing service, not a keyring you can read.

So we let the container talk to your agent, without giving it anything to keep.

```
  inside the container              on your machine
  ────────────────────              ───────────────
  git needs to authenticate
        │
        ▼
  a socket the helper made
        │
        ▼
  small helper  ───── relays ────▶  your ssh-agent
                                       │
                                       ├─ signs with your key
        ◀────── signature ─────────────┘

  the key itself never moves
```

There is nothing to steal. Even with full control of the container, an attacker
can't extract the key — they can only ask the agent to sign things, and only
while the relay is running.

**This also handles commit signing, with no extra machinery.** Since git 2.34
you can sign commits with an SSH key instead of GPG (`gpg.format=ssh`), and the
signing goes through the same agent. One mechanism covers both authentication
and signing. We are explicitly *not* doing GPG agent forwarding: it's the
fiddliest part of this whole area, and SSH signing makes it unnecessary.

## The important detail: relay, don't mount

There are two ways to connect the container to your agent, and picking the right
one decides how hard this is.

**The obvious way — bind-mount the agent's socket into the container — is the
wrong one.** It drags in every platform difference there is: macOS needs a magic
path Docker Desktop invents (`/run/host-services/ssh-auth.sock`) because
mounting your real socket fails with `operation not supported`; podman, colima
and Rancher Desktop have no such path at all; the mounted socket arrives owned
by root when containers run as `vscode`; and because mounts are fixed when a
container is created, toggling the feature would require a rebuild.

**The right way is a relay**, and it's what the VS Code Dev Containers extension
actually does — which is why it works on a Mac with podman. A helper process in
the container creates a *brand new* socket and streams traffic back to the host
over the same exec channel `dev` already uses to run commands.

The proof is in VS Code's own logs:

```
SSH_AUTH_SOCK in container (/tmp/vscode-ssh-auth-<uuid>.sock)
forwarded to local host (\\.\pipe\openssh-ssh-agent)
```

The host side there is a **Windows named pipe**. You cannot bind-mount a Windows
named pipe into a Linux container — so no mount is involved. It's a userspace
relay, end to end.

Choosing the relay makes four problems disappear at once:

- **Engine-agnostic.** Nothing depends on Docker Desktop's magic paths, so
  podman, colima and Rancher Desktop work. Under rootless podman a bind-mounted
  socket can be broken by SELinux and user namespaces anyway, so the relay is
  the *more* reliable option there, not a compromise.
- **No ownership problem.** The helper creates the socket, so it belongs to the
  right user already. No `chown` step.
- **No rebuild.** Nothing is mounted, so turning the feature on or off takes
  effect on the next command — consistent with how the rest of `dev` behaves.
- **It works on Kubernetes.** A pod has no host socket to mount, but it does
  have an exec channel, so the same relay works there. Both providers, one
  mechanism.

The cost is that a helper program has to run inside the container. That's a real
departure — `dev` currently puts nothing in containers — and it's the one thing
this design asks you to accept.

## When it's running

The relay lives exactly as long as the `dev` command that started it.

`dev container agent` and `dev container shell` already run in the foreground
for as long as you're using them, so for normal work this needs **no background
service** — which matters, because `dev` deliberately has no daemon. A quick
`dev container exec -- git fetch` starts the relay, runs git, stops the relay.

**Two consequences worth knowing:**

- If you attach to the container some other way — `docker exec` directly, say —
  git won't work, because no relay is running. That's the design working, but
  it will surprise someone.
- While a session *is* live, anything in the container can use the relay, not
  just git. Same trade-off SSH agent forwarding always makes, and it's why
  ending the session matters: the window is the session, not forever.

## More than one session at once

Two tmux panes, both running `dev container shell` against the same container,
is normal and has to keep working when one exits.

Give each `dev` command its own socket, named uniquely — which is exactly what
VS Code does with `vscode-ssh-auth-<uuid>.sock`. Each session's git is told its
own socket path through environment variables set per invocation, so the panes
never collide, and closing one leaves the other untouched.

Stale sockets from crashed sessions will accumulate in `/tmp` (VS Code has this
problem too), so sweep them on container start.

## What actually gets built

**1. The relay.** A small helper in the container that creates a socket and
streams traffic to the host, plus the host side that connects to your real
`$SSH_AUTH_SOCK`. The SSH agent protocol is just request/response over a stream,
so this is plumbing rather than protocol work — nothing needs to understand what
the messages mean.

**It has to be a compiled binary, not a script.** The base image
(`mcr.microsoft.com/devcontainers/base:1-ubuntu-24.04`) was checked directly and
has no `socat`, no `nc`, and no `ncat` — so the obvious shell one-liner is not
available. It does have `python3`, but depending on that would make the feature
work on this image and fail on any project shipping an alpine, distroless, or
minimal language image. Since this must work for projects that bring their own
`devcontainer.json`, the helper cannot depend on what happens to be installed.

So: a tiny Go program, cross-compiled for `linux/amd64` and `linux/arm64`,
embedded in the `dev` binary with `go:embed`, and streamed into the container at
session start. Detect the container's architecture, copy the right one to a
per-session path under `/tmp`, run it, delete it when the session ends. This is
what DevPod and Coder both do, and now the reason why is clear.

Adding `socat` through `dcgen` was considered and rejected: it would only work
for containers `dev` generates, and a project's own configuration cannot be
modified (invariant #9).

**2. Per-provider attach.** An optional interface discovered by type assertion,
the same way `Syncer` is (`internal/provider/provider.go:60-67`). Unlike
`Syncer`, **both providers implement this one**:

- **local** — the devcontainer CLI's `exec`.
- **k8s** — `kubectl exec`, which already gives a two-way pipe.
  `internal/provider/k8s/kubectl.go:88` (`stream`) already wires up input,
  output and error. Do *not* use `pipeInto` (`kubectl.go:132`); it only goes one
  direction, and its comment documents a real hang worth reading first.

**3. Tell git to use it.** Set `SSH_AUTH_SOCK` plus the signing config
(`gpg.format=ssh`, your public key) through the `GIT_CONFIG_COUNT` /
`GIT_CONFIG_KEY_0` / `GIT_CONFIG_VALUE_0` environment variables. A real git
feature since 2.31; the base image has 2.49. This configures git without writing
a single file into the container.

One trap: `GIT_CONFIG_COUNT` must exactly match the number of pairs, numbered
from 0 with no gaps. A missing number is a fatal git error, not a skipped entry.
Build the whole list in one place rather than incrementing a counter.

**4. A flag to turn it on.** The `ssh_forward` column already exists and is
already read and written by `internal/store/workspace.go` — it needs a flag on
`workspace init` and something to actually read it.

`gpg_forward` should be deleted. We're not building GPG forwarding, and a column
nobody reads is a promise the tool doesn't keep.

**5. Fail clearly when there's no agent.** If `SSH_AUTH_SOCK` isn't set on the
host, say so by name. VS Code's equivalent is "ssh-agent: SSH_AUTH_SOCK not set
on local host", and the failure it causes otherwise — `error fetching
identities: communication with agent failed`, from inside the container — tells
you nothing about the real cause.

## Prior art

All three comparable tools relay rather than mount:

- **VS Code Dev Containers** — the relay described above, plus the same
  technique for gpg-agent and X11.
- **DevPod** (`loft-sh/devpod`) — points git at its own binary, relaying over an
  SSH tunnel; for signing it installs `devpod-ssh-signature` so "the private SSH
  key never enters the container".
- **Coder** (`coder/coder`) — sets `GIT_ASKPASS` to its own binary, fetching a
  fresh token per request. Their docs warn about exactly the failure we're
  avoiding: a token baked into the environment expires and breaks.

One thing to do differently: DevPod's in-container listener is an HTTP port with
no documented authentication, so anything in the container can call it. Use a
Unix socket — a file, with file permissions. **Never a localhost TCP port**: it
sounds equivalent and isn't, since any other program on your machine could
connect, which would be worse than the PAT we're replacing.

## Suggested order

Each step is useful on its own and reviewable separately:

1. **The relay, local provider only.** `git fetch` works end to end.
2. **Kubernetes.** The same relay over `kubectl exec`.
3. **Commit signing**, both providers.

## How we'll know it works

- `make lint && make test` after every step.
- Unit tests use fake executables on a temporary `PATH`, never mocks — see
  `internal/provider/local/local_test.go`. The k8s tests *replace* `PATH` rather
  than adding to it, so a real binary can't sneak into a test.
- Check the relay socket is removed when the command exits, **including on
  Ctrl-C**. A leftover socket is a channel nobody is watching.
- Check two sessions at once: two `dev container shell` sessions against one
  container, exit one, confirm git still works in the other.
- Check a missing host agent fails with a message naming `SSH_AUTH_SOCK`.
- Check `GIT_CONFIG_COUNT` matches the number of pairs.
- By hand, which is what started all this:

```sh
dist/dev container exec dev-cli -- ssh-add -l          # agent reachable?
dist/dev container exec dev-cli -- git fetch
dist/dev container exec dev-cli -- git push --dry-run
dist/dev container exec dev-cli -- git commit --allow-empty -m 'signing test'
dist/dev container exec dev-cli -- git log --show-signature -1
```

- And the negative case: open a shell in the container *without* going through
  `dev`, and confirm git fails. That failure is the feature.
- Smoke tests: create → fetch → sign → stop on local, and the same on k8s behind
  the existing `DEV_SMOKE_K8S_CONTEXT` / `DEV_SMOKE_REGISTRY` gate.

## Docs to update

- `docs/USAGE.md` — required. A command isn't finished until USAGE matches it.
  Cover the new flag, the session-scoped lifetime, and the "no `dev`, no git"
  consequence.
- `docs/specs/2026-09-19-git-credentials.md` — the reasoning: why blast radius
  drove the decision, why a relay rather than a bind mount (with the Windows
  named pipe as the proof), why SSH signing instead of GPG forwarding, and why a
  Unix socket rather than a port.

## Open questions for review

1. ~~**Is injecting a helper into the container acceptable?**~~ Accepted.
2. ~~**Is the session-scoped limitation acceptable?**~~ Git stops working in a
   container you've attached to by other means. Multiple concurrent `dev`
   sessions are handled; this is about attaching *without* `dev`. Accepted
3. ~~**Should k8s land in the same change or follow later?**~~ The relay works there
   for the same reason it works locally, so there's no longer a technical reason
   to split them — only scope. Handle now.
