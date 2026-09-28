# A short-lived Kubernetes token in a container

An agent working in a container sometimes needs to read a cluster: the pods a
change is about, the events behind a failing rollout. Today the only way to give
it `kubectl` access is to copy a kubeconfig in by hand, which in practice means
the operator's own long-lived credentials sitting on the container's disk.

After this change `shell`, `exec` and `start-agent` take `--kube-token`. With it,
`dev` asks which context, namespace and ServiceAccount to use and for how long,
mints a token for that ServiceAccount on the host, writes a kubeconfig holding it
into the container, and runs the command with `KUBECONFIG` pointing there.
Refreshing an expired token is the same flag on a no-op:

```sh
dev container exec NAME --kube-token -- true
```

This is the first stage. Nothing is stored: every `--kube-token` asks all four
questions again.

## Why a ServiceAccount token

Three identities were considered.

| | Scope | Works on |
|---|---|---|
| **ServiceAccount token** (`kubectl create token`) | whatever RBAC grants that account | both providers |
| The operator's own host identity | everything the operator can do | both providers |
| The pod's own ServiceAccount | the cluster the pod runs in | k8s provider only |

The ServiceAccount token wins. Its reach is set by RBAC the operator writes on
purpose — a read-only role in one namespace is the expected case — and its
lifetime is a flag on the TokenRequest API rather than something `dev` has to
police. Handing an agent the operator's own identity is the thing this feature
exists to avoid, and tying it to the k8s provider would make it experimental
for no reason: the cluster queried and the cluster the container runs in are
unrelated.

The feature does not depend on the k8s provider. It works identically for a
container on either, because the only thing it needs from the provider is
`Exec`.

## A credential that rests in the container, deliberately

`docs/specs/2026-09-19-ssh-agent-relay.md` and
`docs/specs/2026-09-21-sync-settings.md` both reject credential files inside a
container, and this feature writes one. The difference is what the file is
worth to whoever takes it:

- **It is short-lived.** The token expires on its own — an hour by default —
  with no revocation step.
- **It is scoped.** It reaches what one ServiceAccount's RBAC allows, which the
  operator chose at the prompt, not everything the operator can do.
- **It is asked for.** No container gets one without `--kube-token` on the
  command that put it there.

The alternative that keeps nothing in the container — a relay the container's
kubeconfig calls through an `exec:` credential plugin, as the ssh relay does for
signing — was rejected. It needs a helper in every image, and it works only
while a `dev` command holds the relay open, which fails the case this feature
is for: an agent left running for hours, whose session outlives any one
command's channel.

The file is written outside `/var/dev-state`. That volume survives a rebuild and
invariant 10 keeps credentials off it; a token file has no business outliving
the container's filesystem.

## What the operator sees

```
$ dev container shell api --kube-token
context: (fzf list of kubectl config get-contexts -o name)
namespace: (fzf list of namespaces in that context)
service account: (fzf list of service accounts in that namespace)
duration, empty for kubectl's default (1h): 4h
dev: token for team-a/reader on prod-eu expires at 19:42 (4h0m)
vscode@api:/workspaces/api$ kubectl get pods
```

The four questions, in order:

1. **Context** — from `kubectl config get-contexts -o name`, the host's current
   context listed first.
2. **Namespace** — from `kubectl --context C get namespaces -o name`, that
   context's default namespace listed first.
3. **ServiceAccount** — from
   `kubectl --context C -n NS get serviceaccounts -o name`.
4. **Duration** — free text. The label says that an empty answer is kubectl's
   own default, which is 1h; empty passes no `--duration` at all. Checked with
   `time.ParseDuration` before anything is minted, and asked again if it does
   not parse.

When `fzf` is on the host's `PATH`, questions 1–3 are fzf lists: the choices go
in on its stdin, it draws on `/dev/tty`, and the pick comes back on its stdout.
Without fzf each is a free-text prompt through the existing `prompter`, with the
choices listed above it. A list that comes back empty or fails — RBAC commonly
forbids listing ServiceAccounts or namespaces — warns on stderr and falls back
to free text for that question: the operator may know the name without being
allowed to list it.

Everything `dev` says goes to stderr, so the output of an `exec` is not mixed
with it.

## The duration and the cluster's cap

1h is kubectl's default, not a ceiling. `--duration 4h` asks for four hours, and
on most clusters gets it.

The ceiling is the API server's `--service-account-max-token-expiration`.
Upstream sets none; a managed cluster may. A server with a lower cap does not
refuse the request — it issues a shorter token than was asked for, and kubectl
prints it without comment. Left alone, the operator finds out when `kubectl`
starts answering `Unauthorized` halfway through a session.

So `dev` reads the token's real expiry. The token is a JWT; its `exp` claim is
decoded locally, without verifying the signature — `dev` is reporting what it
was handed, not deciding whether to trust it. The expiry is always printed. When
it falls short of the request by more than a minute, a warning follows:

```
dev: asked for 4h0m, the cluster issued 1h0m; its maximum is lower
```

A token whose expiry cannot be read — not a JWT, or no `exp` — warns that the
expiry is unknown and carries on. The token itself may still be good.

A cluster that caps below what the operator needs has two ways out: raise the
cap where the operator runs the control plane, or refresh during the session.
The second is what the design already relies on.

## The kubeconfig

`internal/kubetoken.Mint` runs two commands on the host:

```sh
kubectl --context C -n NS create token SA [--duration D]
kubectl --context C config view --minify --flatten -o json
```

`--duration` is passed only when the operator gave one, so an empty answer
really is kubectl's default rather than `dev`'s idea of it. `--flatten` inlines
the cluster's CA data, because a `certificate-authority` path on the host means
nothing inside the container.

From the second command `Mint` takes the cluster's `server`, its CA data and
`insecure-skip-tls-verify` if set, and assembles a self-contained kubeconfig:
one cluster, one user carrying `token:`, one context naming both with the
namespace set, and `current-context` pointing at it. The host user's
credentials — client certificates, exec plugins — are never copied: the token is
the only credential in the file.

The token never appears in a command-line argument, an environment variable,
an error message or a log line. It exists in `dev`'s memory and in the file.

## Into the container

The kubeconfig goes in through `Provider.Exec`, with the document on stdin:

```sh
sh -c 'umask 077 && mkdir -p /tmp/dev-kube && cat > /tmp/dev-kube/config.tmp && mv /tmp/dev-kube/config.tmp /tmp/dev-kube/config'
```

- **Through `Exec`**, which both providers already have, so there is no
  provider-specific code and no new optional interface. It runs as the
  container's remote user, so the file is theirs.
- **On stdin**, never as an argument: an argument shows in the host's and the
  container's process lists.
- **umask 077**, so the directory and the file are the remote user's alone.
- **Written then renamed.** `kubectl` in a running session reads the file on
  every call; a rename is atomic, so a read during a refresh sees the old
  kubeconfig or the new one, never half of either.

A failed write fails the command before it runs. A session started without the
token it asked for would fail later with an error that names nothing `dev` did.

After the write, `dev` runs `command -v kubectl` in the container. If there is
none it warns — the token is written, but nothing there can use it — and says to
add kubectl to the project, or `--tools kubectl` for a generated configuration.
A warning, not a failure: another client in the container may read the file.
`dev` does not install it (invariant 9).

## `KUBECONFIG` only with the flag

The command `dev` launches gets `KUBECONFIG=/tmp/dev-kube/config` appended to
its environment, last, so it wins over a workspace setting of the same name: the
flag on this command is the more specific request.

It is added only when `--kube-token` is given. A plain `shell` afterwards does
not get it, even though the file is still there. That keeps the feature opt-in,
and on a k8s-provider pod it keeps a plain session on the pod's own in-cluster
credentials rather than quietly switching it to the last token minted.

A session that did start with the flag keeps the variable for its life, which is
what makes the refresh work: `exec NAME --kube-token -- true` overwrites the
file that session's `KUBECONFIG` already names.

### The last mint wins

There is one file per container. A later `--kube-token` that picks a different
ServiceAccount changes the identity of every session already pointing at the
file — including upgrading a read-only session to whatever the new account can
do. Per-session files would prevent that, and would also make it impossible to
refresh a running session, which is the thing asked for. One file it is, and
`docs/USAGE.md` says so plainly.

## The order of things

Questions come early; anything that needs a running container comes late. No
one is asked a question after waiting through a slow `up`, and nothing is
written into a container that is not running.

| Step | `shell`, `exec` | `start-agent` |
|---|---|---|
| 1. Resolve, `requireOverride`, terminal check | ✓ | ✓ |
| 2. Container state | `requireRunning` — refuse if stopped | after step 3 |
| 3. **Ask** context, namespace, SA, duration | ✓ | ✓, **before** `Up` |
| 4. `Up` if stopped, owed `agents.yaml` apply, `ensureAgentPresent` | — | ✓, as today |
| 5. **Mint**, print expiry | ✓ | ✓ |
| 6. **Write** the kubeconfig, check for kubectl | ✓ | ✓ |
| 7. `forwardAgent`, append `KUBECONFIG`, `Exec` | ✓ | ✓ |

`start-agent` asks before `Up` so that a host-side failure — no kubectl on the
host, no contexts, a cancelled pick — does not start a container for nothing.
It mints after `Up` so the token's lifetime starts with the session, not before
an image pull. A mint that fails after `start-agent` has started the container
leaves it running, as any failure after `Up` does today; re-running the command
is immediate.

Without the flag all three commands behave exactly as they do now.

## Where the code goes

**`internal/kubetoken`** — the work, no prompting. Shells out to the host's
`kubectl`, every call carrying `--context`.

- `Contexts(ctx) (names []string, current string, err error)`
- `Namespaces(ctx, kubeContext) (names []string, def string, err error)`
- `ServiceAccounts(ctx, kubeContext, namespace) ([]string, error)`
- `Mint(ctx, Request) (Token, error)` — `Request` holds the context, namespace,
  ServiceAccount and duration (zero meaning kubectl's default). `Token` holds the
  kubeconfig bytes and the expiry, zero when unreadable.

The `-o name` output's `namespace/` and `serviceaccount/` prefixes are stripped
here, so the CLI deals in plain names.

**`internal/cli/kubetoken.go`** — orchestration.

- `askKubeToken(ctx) (kubetoken.Request, error)` — steps 1 and 3: the terminal
  check, the four questions, the fzf-or-prompter choice.
- `(a *app) injectKubeToken(ctx, t, req) (provider.EnvVar, error)` — steps 5 and
  6: mint, report the expiry, write, check for kubectl, return the `KUBECONFIG`
  variable.
- `choose(label, items, first)` — fzf when `exec.LookPath("fzf")` finds it, the
  prompter otherwise.

Each of the three commands gains a `--kube-token` bool and calls the two
functions at its own points in the table above.

## Errors

Exit codes follow invariant 7.

| Situation | Behaviour | Exit |
|---|---|---|
| `--kube-token` and stdin is not a terminal | "--kube-token asks which context, namespace and service account to use, and needs a terminal" | 2 |
| No `kubectl` on the host | named as a missing host tool, before any question | 1 |
| No contexts in the kubeconfig | refused: there is nothing to pick | 1 |
| Namespace or SA list empty or failing | warning, free text for that question | — |
| Cancel: Esc or ^C in fzf (exit 130), EOF at a prompt | `errPickCancelled`; nothing written, nothing run | 1 |
| Duration does not parse | asked again | — |
| `create token` fails — missing SA, forbidden | kubectl's stderr, prefixed with the context, namespace and SA | 1 |
| Issued expiry shorter than requested | warning, carry on | — |
| Expiry unreadable | warning, carry on | — |
| Write into the container fails | fail before the command runs | 1 |
| No kubectl in the container | warning, carry on | — |

## Testing

All under `make test`: no engine, no cluster.

**`internal/kubetoken`**, against a stub `kubectl` on a temporary `PATH` that
*replaces* the host's, as the k8s harness does, so a host kubectl can never be
the one answering:

- the three listings parse `-o name` output and strip prefixes; the stub asserts
  every call carries `--context`, and that the namespace and SA listings carry
  the right ones;
- `Mint` passes `--duration` only when one is set;
- the assembled kubeconfig parses, and holds exactly one cluster, user and
  context, the token, the inline CA, the namespace — and none of the host
  user's credentials from the stubbed `config view`;
- expiry decoding: a well-formed JWT, one with no `exp`, a token that is not a
  JWT; the short-token warning fires beyond the tolerance and not within it.

**`internal/cli`**:

- `choose` runs a stub `fzf` when one is on `PATH` and the prompter otherwise;
  fzf's exit 130 becomes a cancel; an empty list falls back to free text;
- with a fake provider: the kubeconfig reaches `Exec` on stdin and never in its
  arguments; `KUBECONFIG` is in the command's environment only with the flag;
  `start-agent` asks before `Up`; `shell` and `exec` on a stopped container
  fail before asking;
- `--kube-token` without a terminal exits 2.

No smoke test in this stage. Minting needs a real cluster with a ServiceAccount
in it, which the smoke environment does not promise, and the write into the
container is the `Exec` path `agents.yaml` already exercises there.

## Docs

`docs/USAGE.md` gains the flag on `shell`, `exec` and `start-agent`, and a
walkthrough, "Query a cluster from inside a container": creating a read-only
ServiceAccount and binding, the four questions, the refresh idiom, the
duration and the cluster's cap, and the last-mint-wins caveat. It says the
feature works the same on both providers and does not depend on the
experimental k8s one.

## Later

Out of this stage, and deliberately so:

- **Stored answers.** The context, namespace, SA and duration on the container
  row, with flags that override and update them, and `--kube-token` asking only
  for what is missing. That is where a non-terminal run becomes possible.
- **A named refresh command**, if `exec … -- true` proves hard to remember.
- **Minting on `start`**, for a session opened some other way than through `dev`.
