# Sync pushes settings, not files

`dev container sync` copies the host folder into a remote container. That is
the wrong thing for it to do, and on the only provider that implements it the
command is a loaded gun: a k8s container is where an agent works for hours, and
`sync` overwrites its workspace from the host with no diff, no confirmation and
no way back.

After this change `sync` reconciles one thing — the workspace's settings — and
the file copy survives only as the internal step that seeds a new pod.

## What each provider does today

Settings are resolved per invocation, never stored as values (invariant 3).
Every command that launches a process calls `a.containerEnv` and hands the
result to the provider: `create` and `start` through `a.start`
(`internal/cli/container.go:372`), `start-agent` at `internal/cli/agent.go:59`,
and `shell`, `exec` and `rebuild` at their own call sites. Nothing is cached at
create time on either provider.

Where they differ is what happens to a value once it is inside the container.

| | local | k8s |
|---|---|---|
| a process `dev` launches | `--remote-env`, per invocation | `env K=V` prefix on the exec, per invocation |
| the container's own environment | there isn't one to speak of | the Secret, via `envFrom`, read at pod start |
| `sync` | not implemented; exits 2 | tars the host folder into the pod |

## The local provider has no gap to close

A local container's processes are all started by `dev`, and each one gets the
settings resolved at that moment. There is no second copy anywhere that could
drift.

The tempting thing to add is a file — `/etc/profile.d/dev-env.sh` — so that a
shell opened by some other route sees the settings too. That is rejected. The
settings hold tokens, and a file holding tokens inside a container is the thing
`docs/specs/2026-09-19-ssh-agent-relay.md` exists to avoid: these containers run
programs that execute code on their own, so nothing worth stealing should rest
in one. `--remote-env` is briefly visible in the host process list, which is a
cost already accepted and written down; a file on the container's disk that
outlives the command is a different and worse trade.

So local implements `Sync` and does nothing. See "An honest no-op" below for why
that is not the lie the interface was built to prevent.

## The gap on k8s is narrower than it looks, and real

Updating the Secret does not change a running pod. `envFrom` is read by the
kubelet when the container starts; only a Secret mounted as a *volume* gets
live-updated, and that would mean credential files inside the container, which
the section above rules out. Meanwhile an `exec` already carries current values
in its `env` prefix, and `create`, `start` and `rebuild` already re-apply the
whole manifest with a freshly built Secret.

What is left is the case nobody drives: **a pod that restarts without `dev`.**
An eviction, an OOM kill, a node drain, someone's `kubectl rollout restart` —
the ReplicaSet makes a new pod, and that pod reads the Secret *as it stands in
the cluster*. If a token was rotated three commands ago and no `dev` command has
re-applied the manifest since, the replacement pod comes up holding the old one,
and the failure surfaces as an agent that was working and now gets 401s.

`sync` is how the cluster's copy is made current without stopping anything. That
is the whole claim. It is worth a command because the alternative — `stop` then
`start` — destroys the running work, which is the thing the operator is trying
to protect.

## The interface becomes a postcondition

```go
// Syncer is implemented by providers that keep a copy of the workspace's
// settings somewhere outside the process dev launches.
type Syncer interface {
	Sync(ctx context.Context, c model.Container, env []provider.EnvVar) error
}
```

The contract is a state, not an action: *after Sync returns, the container's
stored copy of the settings matches the workspace.* A provider storing no copy
satisfies it by doing nothing.

`env` is a parameter rather than something the provider resolves, for the same
reason `Up` and `Rebuild` take it: turning a spec into a value needs the secret
backend, and that is a host concern. The provider never learns where a value
came from.

The interface stays optional and discovered by type assertion. Not because both
providers do not implement it — after this they both do — but for the reason
`AgentForwarder` is also optional: a provider added later should be able to run
a container before it owes this.

## An honest no-op

The old comment said a no-op `Sync` on local would be a lie. Under the old
contract it would have been: `Sync` meant "copy the files", and a provider that
copied nothing while reporting success would be claiming something false.

Under the new contract it is not. "The container's copy of the settings matches
the workspace" is *true* of a local container, continuously, because there is no
copy to diverge. Returning nil reports a fact.

The command still says what happened rather than printing nothing, so an
operator who ran it deliberately learns why there was nothing to do:

```
❯ dev container sync api
dev: a local container takes its settings on every command; nothing to push
```

Exit 0. A script that syncs every container in a workspace should not have to
know which provider each one is on.

## Only the Secret is applied

`Sync` on k8s builds the same object the manifest does — `buildSecret`, with the
devcontainer's own `containerEnv` underneath the workspace settings so a setting
of the same name still wins — and applies that single object.

**Re-applying the Deployment would be the bug this command is meant to avoid.**
The apply is server-side, and a server-side apply drops fields the applier no
longer sets. An apply built without a `rebuiltAt` value clears the
`dev.rebuilt-at` annotation on the pod template; the template changes; the
`Recreate` strategy tears the pod down. The command whose entire purpose is to
leave running work alone would kill it. The Secret is applied on its own, and
the comment in the code names this failure.

That the apply drops absent fields is what makes an *unset* setting propagate:
`dev workspace unset API_URL` followed by `sync` removes the key from the
Secret, rather than leaving a value the workspace no longer has.

The folderless refusal goes away. A scratch container has settings like any
other, and the old error — "container … has no folder to sync from" — was about
files. `requireRunning` stays: a stopped container gets a complete manifest from
`start` anyway, so there is nothing `sync` could add that `start` will not do a
moment later.

`Sync` prints the caveat itself, on stderr, the way the provider already prints
`dev: building …`:

```
dev: the pod keeps the environment it started with; anything already running
dev: there sees the new values only after stop and start
```

Saying it every time is deliberate. An operator who syncs a rotated token and
then watches the running agent keep failing needs to be told why at the moment
they act, not to find it in a document later.

## The file copy becomes internal

The tar stream is not deleted; it is the only way a new pod gets the project at
all. `Sync` is renamed `seedWorkspace` in the same file, unexported, with its
one caller left where it is — `Up`, on first create only
(`internal/provider/k8s/k8s.go:120`).

Being unreachable from the CLI is what makes it safe: it can now only run
against a workspace that has just been provisioned and is empty. The
`SourceFolder` guard stays, downgraded from a refusal the operator can trigger
to an assertion about a caller. `tarDir` is untouched.

## Command surface

```
dev container sync NAME

Push the workspace's settings into a container.
```

`runContainerSync` drops the `SourceNone` guard, resolves the environment with
`a.containerEnv` — which also supplies the state-volume variables, so `sync`
and `up` agree on what the container's environment is — and passes it through.
The `!ok` branch stays for a provider that implements neither.

Output names the keys and never the values, the same rule `workspace show`
follows:

```
❯ dev container sync api
synced 4 settings to api: ANTHROPIC_API_KEY, API_URL, GH_TOKEN, GIT_CONFIG_GLOBAL
```

## Testing

Unit, against the existing stub-executable harnesses:

- **k8s `Sync` applies a Secret and no Deployment.** The regression that matters:
  a Deployment in that apply means a pod restart, which means destroyed work.
  Asserted on the manifest handed to the stub kubectl.
- **k8s `Sync` accepts a folderless container**, where it used to refuse.
- **k8s `Sync` carries an unset key away**, by applying a Secret whose
  `stringData` lacks it.
- **local `Sync` satisfies `Syncer`, returns nil, and executes nothing.** The
  stub `PATH` records calls, so "no binary was run" is checkable rather than
  assumed.
- **`seedWorkspace` keeps the `tarDir` coverage** it had as `Sync`, including
  `-p` and the workspace folder it unpacks into.

Smoke (`test/smoke/k8s_test.go`), where a real cluster is available: change a
setting, run `sync`, and read the Secret back with `kubectl get secret -o
jsonpath`. Asserting through `dev container exec -- printenv` would pass whether
or not `sync` did anything at all, because exec injects the value per
invocation — the test has to look at the cluster's copy, since the cluster's
copy is the entire subject.

## Documentation

`docs/USAGE.md`: the folderless note (l.93) loses its "sync exits 2" claim; the
rotation section (l.155) gains the pod-restart reason for running `sync`; the
command reference (l.490) and the explanation of what `sync` is for (l.550) are
rewritten; the two error messages in the troubleshooting list (l.326, l.330) go,
one replaced by the local no-op line.

`CLAUDE.md`: invariant 3's note about rotation on k8s now points at `sync` as
the way to make the cluster current, and invariant 9 loses "`container sync`
refuses a folderless container on both providers" — it no longer does, and the
sentence's real subject, that there is no host tree to push, is now expressed by
`seedWorkspace` being internal.
