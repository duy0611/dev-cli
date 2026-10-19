# Stopping idle containers

## Problem

A container keeps running after the operator walks away from it: the shell
closed, the editor disconnected, the agent finished. It goes on holding memory
and CPU on the laptop or in the cluster, and whatever runs inside it keeps
running with nobody watching. A process an agent was tricked into starting,
left in the background, has all the time it wants.

## Goal

A container no one has been connected to for 30 minutes is stopped. Stopping is
`dev container stop`: the container, its volumes and its work all stay, and
anything that starts it brings it back.

The operator runs nothing and configures nothing. The check lives *inside* the
container, started by `dev` when it starts the container; nothing is installed
on the host, no launchd agent, no systemd unit, no daemon of any kind.

Success: the operator closes `dev container shell api` at 14:00, closes the
laptop and does nothing else. At about 14:30 the watchdog inside sees no
session, stops the container, and leaves a marker. When the operator next
starts it, `dev` records an `idle-stop` event in the audit log with the time and
idle minutes. A container with an editor attached all afternoon is never
touched.

## Non-goals

- Removing anything. Idle stop never deletes a container, volume or row.
- Watching CPU, network volume or file activity. "Idle" is about connections,
  not load.
- Containers `dev` does not know about. Only rows in the database get a
  watchdog.
- Kubernetes, in this version. See below.
- Anything persistent on the host: no timer, no daemon, no unit file.

## Anti-goals

- **Something inside the container keeping it alive.** A process left behind
  by an exec that has ended — a detached `tmux`, a `nohup` job, an agent whose
  client went away — must not hold the container up.
- **Stopping a container under someone's hands.** A live connection that has
  been quiet for hours still keeps it up.
- **A host-side component.** The operator's machine gains no service, timer or
  file.

## What counts as a session

Prior art agrees on the definition. Coder counts active sessions — VS Code
remote, JetBrains gateway, terminal, SSH — with binary connection counters and
no keystroke detection; an open but quiet editor is active, and background
jobs and tmux are explicitly not. Gitpod and DevPod likewise key off whether a
user is connected. `dev` takes the same line.

A session is a process that entered the container from outside through the
engine's exec — `dev container shell`, `exec`, `start-agent`, `dev diff
complete`, the ssh relay, VS Code's remote server while its client is attached,
or a plain `docker exec`.

### How the watchdog sees sessions

From `/proc` inside the container, which is the one view a process inside
cannot fake. On Linux a process whose parent lives outside the container's PID
namespace reports `ppid 0`: that is the init and every exec'd process. A child
a session starts has its parent inside the container, and a process orphaned
when its exec ended is reparented to PID 1. So:

- a session is a process with `ppid 0`, other than PID 1 and other than a
  zombie;
- its children are not counted separately;
- the watchdog itself is reparented to PID 1 when the exec that started it
  ends, so it never counts as a session.

A process inside cannot manufacture `ppid 0`: that needs a parent outside the
namespace. This is why the check does not run on the host: it is more precise
than asking the engine for parent PIDs, and it does not depend on how Docker,
Docker Desktop or a podman machine report them.

One approximation to state plainly: a VS Code server that outlives its window
keeps its exec, so it reads as a session until it exits. The error is in the
safe direction (the container stays up a little longer); the opposite error
would kill work. VS Code's server shuts itself down within minutes of its last
client.

## The watchdog

A small POSIX shell script, embedded in the binary (`internal/idle`) and copied
into the container through `Provider.Exec` on stdin. It is written to a
writable directory the container owns (`/tmp`, or the state volume when one is
mounted), made executable, and started detached — `setsid` when present,
`nohup` with every stream redirected otherwise — so it outlives the exec that
started it.

Every minute it counts sessions. After 30 minutes with none it:

1. writes a marker next to itself holding the time and the idle minutes;
2. sends `TERM` to PID 1, and `KILL` if PID 1 is still there ten seconds later,
   which ends the container exactly as `docker stop` would, leaving its
   filesystem and volumes in place.

PID 1 is root, so the watchdog needs root. `dev` runs it directly when the
remote user is root and under `sudo -n` otherwise. A container with neither —
a non-root remote user and no sudo — gets a warning at start saying idle
stopping is unavailable there, and no watchdog. The warning is repeated at most
once per container per run.

A pidfile makes it one instance per container: a second copy exits at once if
the first is alive.

### When it is started and repaired

`dev` starts the watchdog after every `Up`, whichever command caused it:
`create`, `start`, `shell`, `exec`, `start-agent`, `rebuild`, `diff complete`.
A container started some other way (`docker start` behind `dev`'s back) gets no
watchdog until the next `dev` command starts one; this is documented.

Every `dev` run also checks, best-effort and in parallel with a short timeout,
that each running container's watchdog is alive and restarts it if not. That
check never stops anything itself; it only repairs. It lives on the host for
the length of the command and leaves nothing behind.

A session that starts between the watchdog's last check and the stop is killed
by it. My call: that window is accepted rather than closed with a lock every
exec path would have to take. It is at most a minute, once per 30 minutes of
idling.

### Why this is best-effort

An agent running as root, or with passwordless sudo, can kill the watchdog, and
the container then runs until the next `dev` run repairs it. The operator
accepted this (2026-10-19): the alternative was a host timer whose decision the
container cannot touch, at the cost of installing a service on the operator's
machine. In every ordinary case — including a non-root malicious process — the
stop happens. `docs/SECURITY.md` says this plainly so no one reads it as
containment.

### Kubernetes

Skipped. A pod whose PID 1 dies is restarted by the kubelet rather than
stopped, so the watchdog would be a restart loop. k8s containers get no
watchdog, and the repair pass ignores them.

## Opting out

`dev container create --no-idle-stop` records `idle_stop = 0` on the row.
Migration `0011` adds the column, `NOT NULL DEFAULT 1`, so containers that
predate the feature get it. The column is fixed at create, like the mode; a
container that must run unattended — a long build, a service under test — is
created with the flag. No watchdog is started, and a running one is stopped.

## The marker

The marker is the only trace a stop leaves. It holds the stop time and the idle
minutes, and it survives a stop and start because the container's filesystem
does. The next time `dev` starts that container it reads the marker, records
`idle-stop` in the audit log with those values, and deletes it. A container
removed before that loses the record, which is acceptable: there is nothing
left to explain.

## What the operator sees

Nothing, until a container is stopped. Then:

- `dev container list` shows it stopped (the engine says so; invariant 4);
- the next start is the moment `dev audit` gains the `idle-stop` record;
- `dev container create --no-idle-stop` is the one flag in the feature, and
  `docs/USAGE.md` documents it with the session definition below.

No command, no status line, no new noun.

## Docs

`docs/USAGE.md` gets a section on idle stopping: that it is on, what counts as
a session including the detached-process consequence, the root requirement,
`--no-idle-stop`, and that a container started outside `dev` has no watchdog
until `dev` touches it. `docs/SECURITY.md` gets the best-effort paragraph from
above. `docs/ARCHITECTURE.md` gets the reasoning for judging sessions from
inside the container, beside the other invariants that concern what the
container can reach.

## Testing

- `internal/idle`: the session parser against a fixture `/proc` tree (the
  script takes the proc root and the PID to signal from the environment), with
  init only, an exec'd process with children, an orphan reparented to 1, a
  zombie, and the watchdog itself.
- The stop path: against a stub killer binary, `TERM` then `KILL` after the
  grace period, and the marker written before either.
- Provider path: `Ensure` copies the script through a stub `devcontainer` on a
  temporary `PATH`, starts it detached once, and does nothing on a second call
  while the pidfile is alive.
- Store: the `idle_stop` column, its default for existing rows, and the create
  flag.
- Smoke: start a container, `docker exec` a `sleep`, assert one session; let it
  end, assert none; start a background process from an exec that then exits,
  assert none; with a 1-minute threshold, assert the watchdog stops the
  container and the marker is read on the next start.
