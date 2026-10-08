# Agent hardening plan

Status: **planned, nothing landed.** The build half is specified in
`docs/specs/2026-10-08-agent-hardening-design.md`, which supersedes this file
wherever the two differ — writing it turned up two escape routes this plan
missed (see the spec's "Git guard").

The containers `dev` makes are called sandboxes in the first spec and are not
one. An agent running in a container today can become root with `sudo`, reach
any host on the internet, read every API key the workspace resolves, sign with
every key in the operator's ssh agent while a session is live, and write files
the host will later execute. None of that is a bug in any one feature; nothing
was ever asked to stop it.

This plan started from Lens's "How to run Hermes Agent securely" (October
2026), whose six steps — contain the runtime, control egress, give the agent
its own identity, keep credentials short-lived, keep an audit trail, cap the
blast radius — are generic and apply to Claude, Hermes and OpenCode alike. The
article stops at the container boundary. The worst problems here are on the
other side of it.

The plan has two halves. **The catalogue** lists every guardrail the research
turned up, what it would cost, and whether it is taken. **The build** is the
short list that survives. Most of the catalogue is deliberately not built, and
the reasons are the point of writing it down: the next person to read the
Lens article should find their idea already weighed here, not re-propose it.

## Context: what these containers are for

Development and sandbox environments, with **dev-only credentials** —
workspace API keys, a repo-scoped token, a kube token for a dev cluster. No
production secret is placed in a container. Agents run **unattended** most of
the time, so a control that stops the agent mid-task gets switched off and
protects nothing.

That shapes everything below. Two different things need protecting, and they
deserve very different effort:

- **The operator's host.** Real keys, real sessions, the personal ssh key, and
  everything else the operator can reach. Code running there is the worst
  outcome, and the guardrails against it are worth real friction.
- **The container and what it holds.** Dev credentials that are dedicated to
  the workspace, spend-capped and revocable in one step. A leak costs a
  rotation, so the right answer is to make leaks cheap, not impossible — the
  same stance taken for any production container.

## Threat model

The agent is assumed to be prompt-injected at some point — by a README, an
issue, a dependency's postinstall output, a web page it fetched. The question is
what a fooled agent can do, not whether it will be fooled.

| Asset | Today an agent can… | After this plan |
|---|---|---|
| The operator's host | write `.git/hooks` and `.git/config` (both run by host `git`); edit `.devcontainer/` (applied at `rebuild`, `initializeCommand` runs on the host) | host git reads neither; config drift refused at `rebuild` |
| The host, via the engine | nothing new — but a project config or feature asking for `privileged`, the engine socket, or host namespaces is applied without question | refused unless `--allow-privileged` |
| The operator's other ssh keys | sign with *any* loaded key, for any host, while a session is live | documented: relay a dedicated agent holding only the GitHub key |
| The cluster (k8s) | read the pod's auto-mounted ServiceAccount token | no token unless `--kube-token` |
| API keys | read them from its environment and send them anywhere | still can — they are dedicated, capped, revocable, and the runbook says how |
| The container | become root with `sudo` | still can; nothing inside the container depends on it not |
| Accountability | nothing records what ran | append-only audit log on the host |

What `dev` already does well, and must keep doing: settings are stored as specs
and resolved per invocation (invariant 3); the ssh agent is relayed per command
rather than a key copied in; kube tokens are minted on the host, short-lived,
and never carry the operator's own credentials; agents are launched with no
permission or autonomy flags.

Accepted residual risk, stated plainly because nothing below removes it: with
egress open and root available, a key in the container can leave it. The answer
is that the key is worth little, not that it cannot leave. Also out of scope: a
malicious operator, a compromised host, kernel escapes from a shared-kernel
container, and anything the operator runs on the host from workspace files the
agent wrote (see 1.1, "what this does not cover").

## Principles

1. **Protect the host first and hardest.** A container boundary is worth
   nothing if the agent can write a file the host runs.
2. **Make leaked dev credentials cheap rather than impossible.** Dedicated,
   capped, revocable, with a runbook. Engineering credentials out of the
   container is weighed and rejected below.
3. **No in-container control while the agent can become root.** Firewall rules,
   managed-settings files and approval policies are all undone by `sudo`. So
   either drop root first, or build none of them. This plan builds none of
   them, and so keeps root.
4. **Never block the unattended agent.** Hardening acts on what can escape the
   container, not on what the agent is allowed to try inside it.
5. **Hardened by default; the escape hatch is explicit.** A control that must
   be turned on is a control nobody turns on. Each relaxation is a named flag,
   given by the operator, never by a file in the project — a file the agent can
   edit.
6. **The guard overrides the project.** A project's own `devcontainer.json`
   still wins over anything `dev` would generate, but not over the escape guard.
7. **New rows only.** Anything that must survive `rebuild --tools` is a column
   fixed at `create` (invariant 10's reason). Migration defaults keep existing
   rows as they are; a container is hardened when it is recreated.
8. **The existing rules still hold.** No writing into the project folder
   (invariant 9): overlays and `--override-config` change what the container
   sees, never the files. The local provider stays the stable one; k8s follows.

## The catalogue

Every guardrail considered, grouped by the article's six steps plus the two the
article does not cover. **Build** items are specified in the next section.

### Host escape (not in the article)

| Guardrail | Protects | Friction | Decision |
|---|---|---|---|
| Container-only `.git/config` (read-only) and `.git/hooks` (a copy) | host git running agent-chosen programs | small: `push -u` and `checkout -b x origin/y` cannot record tracking; `remote add` fails | **build — 1.1** |
| `.git/config` and `.git/hooks` both as writable copies | same | none in theory | **rejected**: git writes config by lock-and-rename, and renaming onto a bind-mounted file fails with `EBUSY`. Tested: a copy behaves exactly like read-only for git. Only `hooks/`, a directory, can be a copy. |
| Protect `.git/hooks` only | same | none | **rejected**: `git config core.hooksPath` points git at any agent-writable directory, and `core.fsmonitor` runs a command on every `git status` with no hook involved — the IDE runs that every few seconds |
| Read-only `.devcontainer/` | `initializeCommand` on the host; `runArgs`/`mounts` at `rebuild` | the agent cannot help edit the container's configuration | **rejected** in favour of 1.2: a deliberate change is common, and a prompt at `rebuild` catches every route by which the file changed, not only the agent's |
| Config drift check at `rebuild` (`--accept-config`) | same | one flag when the config was changed on purpose | **build — 1.2** |
| Escape guard on the merged config | `privileged`, engine socket, host namespaces, host root or home mounted | only docker-in-docker users, who pass `--allow-privileged` | **build — 1.3** |
| Read-only overlays on `.husky/`, `.envrc`, `Makefile`, `package.json`, `.vscode/` … | the operator running workspace files on the host | large: these are the project the agent is editing | **rejected**: unbounded, and protecting them prevents the work. The operator reviews before running workspace code on the host; stated in `USAGE.md`. |

### 1. Runtime containment

| Guardrail | Protects | Friction | Decision |
|---|---|---|---|
| `no-new-privileges` + `--cap-drop=ALL` (local), `--allow-sudo` escape hatch | in-container controls from root | large: no `apt install`; project lifecycle commands using `sudo` fail; the Go feature (`SYS_PTRACE`, `seccomp=unconfined`) refused; `dev`'s own `sudo chown` of the state volume must move host-side; compose projects cannot be shown to comply | **dropped**: its only job was to protect in-container controls (egress firewall, agent policy), and those are deferred. Root in an unprivileged container is still contained; the escape guard covers what is not. |
| k8s: `automountServiceAccountToken: false` | the cluster | none — `--kube-token` is the explicit route | **build — 2** |
| k8s: `seccompProfile: RuntimeDefault` | the node kernel | none in practice; docker already applies its default profile | **build — 2** |
| k8s: `runAsNonRoot`, `allowPrivilegeEscalation: false`, capability drop | in-container controls | breaks images whose remote user is not uid 1000; same reasoning as local | **dropped** |
| Read-only root filesystem | persistence inside the container | large: every tool writing outside a tmpfs breaks | **rejected** |
| Rootless engine, userns-remap | the host from container root | operator's engine choice, not `dev`'s | **out of scope**; mentioned in `USAGE.md` |
| Runtime class: gVisor (local), Kata/gVisor `runtimeClassName` (k8s) | kernel escapes | gVisor does not run on OrbStack or Colima; needs cluster support | **deferred**: one manifest field on k8s when someone wants it |
| Docker Sandboxes / microVM per container | kernel escapes | replaces the engine `dev` drives | **rejected** for now; revisit if it becomes a devcontainer-compatible engine |

### 2. Egress control

| Guardrail | Protects | Friction | Decision |
|---|---|---|---|
| Internal docker network + allowlisting CONNECT proxy; `HTTP(S)_PROXY`, `NODE_USE_ENV_PROXY=1` | exfiltration of anything in the container | large: every unlisted host an unattended agent needs is a stuck task; git over ssh needs a `ProxyCommand` | **deferred**: credentials are dev-only and revocable. Revisit if the containers ever hold anything that cannot be rotated. |
| Anthropic's `init-firewall.sh` (iptables + ipset) | same | needs `NET_ADMIN`; resolves domains once at start; allows ssh to any host; trusts the host /24 | **rejected even when egress is picked up**: with root available the agent flushes the rules, and its integrity rests on a sudoers entry this base image does not have |
| k8s default-deny `NetworkPolicy` + proxy Deployment | same | many CNIs (flannel, kindnet) accept and ignore policies | **deferred** with the above |
| Claude Code sandbox (`/sandbox`, bubblewrap) | Claude's shell commands | needs `enableWeakerNestedSandbox` in a container, which Anthropic documents as considerably weaker; Claude only | **rejected** as a `dev` feature; an operator may enable it in their own Claude settings |

### 3. Identity

| Guardrail | Protects | Friction | Decision |
|---|---|---|---|
| `--kube-token`: host-minted, short-lived ServiceAccount token, no operator credentials | the cluster | — | **exists** |
| Dedicated ssh agent holding only the GitHub key, pointed at by `SSH_AUTH_SOCK` when running `dev` | the operator's other keys (prod hosts) | one `ssh-agent` per session; no code | **build — docs, 4** |
| Relay-side key filtering (answer identities and sign for the workspace's key only; refuse add/remove/lock) | same | modest code in the relay | **deferred**: the dedicated agent above gives the same result for free |
| Per-workspace deploy key as the agent's identity | attribution, scope | key management per repo | **documented option**, not built |

### 4. Short-lived credentials

| Guardrail | Protects | Friction | Decision |
|---|---|---|---|
| Credential relay: agent gets a placeholder and a local base URL, `dev` injects the key host-side over `dev-relay` | API keys never in the container | every model request rides the `dev-relay` exec stream | **rejected**: that stream has been seen to drop mid-session (`.claude/plans/2026-09-21-relay-dies-mid-session.md`). Losing commit signing is an annoyance; losing the model mid-task is not acceptable. |
| Claude Code `sandbox.credentials` masking | same, Claude's shell only | Claude only; the Claude process still holds the key; needs the weakened nested sandbox | **rejected** |
| LLM gateway with virtual keys (LiteLLM) | per-key budgets | one more service holding every upstream key; LiteLLM had a pre-auth SQL injection exposing them in 2026 (CVE-2026-42208) | **rejected** as a dependency |
| Dedicated, capped, revocable keys per workspace + revocation runbook | the cost of a leak | none — documentation | **build — docs, 4** |
| Remove `/tmp/dev-kube/config` when the command exits | a stale kube token | small | **deferred**: the token is short-lived, so the file is an expired credential |

### 5. Audit trail

| Guardrail | Protects | Friction | Decision |
|---|---|---|---|
| Append-only JSONL log on the host, beside the database | knowing what to revoke, and when | none | **build — 3** |
| Claude OpenTelemetry export (`CLAUDE_CODE_ENABLE_TELEMETRY=1`, `OTEL_LOG_TOOL_DETAILS=1`) | what happened inside a session | a collector to run | **documented opt-in** through workspace settings; `dev` does not wire it |

### 6. Blast radius

| Guardrail | Protects | Friction | Decision |
|---|---|---|---|
| Spend caps at the issuer (Anthropic workspace limit, OpenRouter per-key `limit`) | the operator's wallet | none | **build — docs, 4** |
| Memory, CPU, PID limits (`runArgs`: `--memory`, `--cpus`, `--pids-limit`; k8s `resources`) | the host's capacity | tuning | **deferred**: dev and sandbox use, overuse is tolerable. `runArgs` is the route when picked up; `hostRequirements` is only a minimum. |
| Root-owned agent policy (Claude `managed-settings.json`, Hermes `approvals`, OpenCode `permission`) | what the agent attempts | blocks unattended agents; Hermes and OpenCode read config only from the agent-writable state volume; undone by root | **deferred** |
| Relay binary digest check | a swapped relay in agent-writable `/tmp` | none | **dropped**: while a session is live, any process can already use `/tmp/.dev-agent-*.sock`. A swapped binary gains what the open relay already gives. |

## The build

Five items: four in code, one in documentation. Only 1.1 changes anything an
agent experiences.

### 1.1 Container-only `.git/config` and `.git/hooks`

Host git runs programs named in two places an agent can write:

- **`.git/config`** — `core.fsmonitor` (run on every `git status`; IDEs run it
  continuously), `core.hooksPath`, `core.sshCommand`, `core.pager`,
  `diff.*.textconv`, filter drivers, `credential.helper`.
- **`.git/hooks/`** — run on commit, checkout, merge, push.

Mount both over the workspace through the routes the state and worktree mounts
already use — `mounts` in a generated document, the merged `--override-config`
for a project-owned one, on `up` and `exec` alike (invariants 10 and 11):

- **`.git/config` read-only, bind-mounted from the host file itself.** A copy
  would not help: git writes config by writing `config.lock` and renaming it
  over `config`, and a rename onto a bind-mounted file fails with `EBUSY`.
  Tested on Linux with git 2.x:

  | Operation | With `.git/config` read-only |
  |---|---|
  | `fetch`, `pull`, `push origin <branch>`, `commit`, `checkout -b` | work |
  | `push -u` | push succeeds; recording the upstream prints an error; exit 0 |
  | `checkout -b x origin/y` | branch created; recording tracking prints an error; exit 0 |
  | `remote add`, `git config <key>` | fail |

  `git config --global` keeps working, writing `GIT_CONFIG_GLOBAL` on the state
  volume, which host git never reads.

- **`.git/hooks/` as a copy**: a directory on the state volume (or a docker
  volume `dev` owns) mounted over the real one. Writes inside a directory mount
  succeed, so hook installers keep working, and land where host git never looks.
  The copy is **seeded from the project's `.git/hooks`**, so the container
  runs the same hooks the operator gets on the host. Seeding copies host to
  container only; nothing ever flows back. The phase spec decides when it
  re-seeds (at `create` only, or again at `rebuild`) — at `rebuild` would
  discard whatever the agent installed, which is arguably the point.

- **Worktree containers.** Both paths live under the git common directory,
  which is mounted in full (`render.go:131`). Mount `<common>/config` and
  `<common>/hooks` the same way. If the repository has
  `extensions.worktreeConfig` set, each `<common>/worktrees/*/config.worktree`
  can set `core.fsmonitor` too and must be read-only as well; the agent cannot
  turn the extension on, since that is a write to the main config.
  `HEAD`, `index` and refs stay writable, or commits stop working.

- **Folderless containers** have no host `.git` and need nothing.

To settle in the phase spec:

- A missing `.git/hooks` (it is optional) must not fail the mount, and `dev` may
  not create it in the project.
- Docker file mounts on macOS engines behave as on Linux — the smoke run
  confirms it rather than assuming.
- The error an agent sees is git's own; `USAGE.md` explains it so it reads as
  intended rather than broken.

Husky, and any installer that sets `core.hooksPath`, cannot do so from inside
the container. Accepted: the operator runs the install once on the host
(`npx husky`), which writes the setting to the real `.git/config`, and the
container sees it through the read-only mount. `USAGE.md` says so.

What this does not cover, stated in `USAGE.md`: hooks managed inside the
working tree (`.husky/`, lefthook, pre-commit), `.envrc`, `.vscode/tasks.json`,
`Makefile`, `package.json` scripts. They run when the operator runs them on the
host, and the agent can edit them like any other file. Review before running
workspace code on the host.

### 1.2 Config drift check at `rebuild`

`.devcontainer/` stays writable. Instead, record a digest of the project's
merged configuration at `create` (a column), compare it at `rebuild`, and
refuse with the changed fields unless `--accept-config` is given, which records
the new digest. A generated configuration is `dev`'s own and is exempt.

The merged configuration, not the file bytes: what matters is what will run,
and features and referenced files contribute to it. When the configuration
builds from a Dockerfile (`build.dockerfile`), its content is part of the
digest too: a `RUN` line is as much "what will run" as `initializeCommand`.
A compose project's compose files are included on the same reasoning.

### 1.3 Escape guard

Refuse at `create` and at `rebuild`, unless `--allow-privileged` (a column),
any configuration whose **merged** form
(`read-configuration --include-merged-configuration`) asks for:

- `privileged: true` (in the document, in `runArgs`, or from a feature);
- a mount of the engine socket (`/var/run/docker.sock`, `docker.sock` anywhere);
- `--pid=host`, `--network=host`, `--ipc=host`, `--userns=host`;
- a bind mount of `/`, the operator's home directory, or a parent of either.
  On Docker Desktop and OrbStack, `/Users` is shared into the engine's VM, so
  such a mount reaches `~/.ssh` and every credential on the host.

Merged, because features contribute too and an override cannot remove what a
feature adds: `docker-in-docker` declares `privileged: true`. Refused rather
than stripped: silently removing it would leave a project that breaks in
confusing ways. Ordinary `sudo`, `capAdd`, and the Go feature's `SYS_PTRACE`
and `seccomp=unconfined` stay allowed — they widen what root can do inside the
container, not the way out of it.

A `dockerComposeFile` project is checked on what `read-configuration` reports,
plus the services' `privileged`, `volumes` and `network_mode` read from the
compose file; anything the check cannot read is reported, not assumed safe.

### 2 Kubernetes pod

In `buildManifest`, always:

- `automountServiceAccountToken: false`.
- Pod `securityContext.seccompProfile: {type: RuntimeDefault}`.

Every existing Deployment changes on its next start; `Recreate` handles it, and
the experimental provider owes no migration.

### 3 Audit log

`audit.jsonl` in the database's directory — `$DEV_STATE`, otherwise
`~/.local/state/dev/` — created `0600` in the `0700` directory `store.Open`
already makes. On the host, out of the agent's reach. Not SQLite: it is a log,
it grows, and it must outlive `workspace remove` and its cascade.

| Event | Fields beyond the common ones |
|---|---|
| `create`, `rebuild`, `remove`, `up`, `stop` | provider kind, `allow_privileged`, `persist_state`, config digest |
| `exec`, `shell`, `start-agent` | argv (never env values), agent id, start, end, exit code |
| kube token mint | context, namespace, ServiceAccount, requested and issued expiry — never the token |
| ssh relay session | start, end, how it ended (clean, killed, unexpected stop) |
| `container sync` | setting names pushed — never values |
| escape hatches | `--allow-privileged`, `--accept-config`, and what they accepted |

Common fields: UTC RFC 3339 time, event, workspace, container, `dev` version,
host user.

- **Setting names, never values.** Inherits invariant 3; a test asserts no
  resolved value reaches the file.
- **One `write` per record, `O_APPEND`**, so concurrent `dev` processes never
  interleave lines.
- **A failed write warns and does not fail the command.** It is accountability,
  not enforcement; a full disk must not stop an agent mid-task.
- **No rotation yet**: a few hundred bytes per event.
- **Read with `dev audit`**: newest last, filtered by `--workspace`,
  `--container`, `--event` and `--since`, with `--json` to emit the raw lines
  for `jq`. A new command, so it lands with its `USAGE.md` section and its exit
  codes (invariant 7: `3` for a named workspace or container that has no
  records is wrong — a removed container still has history — so an unknown name
  simply prints nothing).

What it cannot see is what the agent did inside a session; that is the agent's
transcript or Claude's OpenTelemetry export (catalogue, step 5).

### 4 Credential model, in `USAGE.md`

- **LLM keys**: one per workspace, never personal or shared; a spend limit at
  the issuer (Anthropic workspace limit, OpenRouter per-key `limit`). Anthropic
  keys do not expire, so revocable-and-capped is what is achievable there.
- **GitHub**: the ssh relay, fed by a **dedicated ssh-agent holding only the
  GitHub key** — `ssh-agent`, `ssh-add` that key, run `dev` with that
  `SSH_AUTH_SOCK`. The relay forwards every key the agent holds; with dev-only
  credentials the operator's personal key is the one production credential in
  the picture. Otherwise a fine-grained PAT, repository-scoped, shortest expiry
  that fits.
- **Kubernetes**: `--kube-token` with the narrowest role; never a copied
  kubeconfig.
- **Interactive logins inside the container** (`claude /login`,
  `opencode auth login`) persist a long-lived token on the state volume. Prefer
  a workspace setting: revocable at the issuer, resolved fresh each command.
- **Revocation runbook**: which console, which key, then repoint the setting's
  spec — local picks it up on the next command, k8s needs `container sync`.
- **Workspace files on the host**: review before running them (1.1).

## Order

| # | Item | Size |
|---|---|---|
| 1 | 2 k8s: SA token off, seccomp | S |
| 2 | 1.1 container-only `.git/config` and `.git/hooks` | M |
| 3 | 1.3 escape guard | M |
| 4 | 1.2 config drift check | S |
| 5 | 3 audit log | S–M |
| 6 | 4 credential model docs | S |

1.3 and 1.2 share reading the merged configuration and land in that order. Each
item is its own branch and spec, lands with `make lint && make test`, extends
`make smoke` where container behaviour changes, and updates `docs/USAGE.md` in
the same change.

## Decisions

From review of the first two drafts:

1. Hardened by default; escape hatches are explicit operator flags.
2. The guard applies over a project's own configuration.
3. Existing containers are hardened only when recreated.
4. Keys stay in the container; the credential relay is rejected.
5. No-sudo floor, egress control, agent policy, resource limits, ssh key
   filtering, runtime class: not built now, for the reasons in the catalogue.
6. `.devcontainer/` stays writable; drift is caught at `rebuild`.
7. `.git/config` read-only, `.git/hooks` a container-only copy.
8. The hooks copy is seeded from the project's `.git/hooks`.
9. Husky and other `core.hooksPath` installers are run once on the host.
10. A Dockerfile (and compose files), when present, are part of the drift
    digest.
11. The audit log is read with a `dev audit` command.

12. The hooks copy is re-seeded from the project at both `create` and
    `rebuild`; whatever the agent installed is discarded at `rebuild`.
