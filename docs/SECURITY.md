# Security: what a container can make the host run

This file records the known gaps in the git guard, the evidence for each, and
every option considered to close them, with what each one leaves open. Nothing
here is fixed yet: it is the record a decision is meant to be taken from.

Recorded 2026-10-10, with option F added 2026-10-11. The reproductions are
smoke tests on the `gitguard-host-config-write` branch (PR #28). They were run
on the operator's Mac under a podman machine, and by CI on Linux amd64 and
arm64 under Docker, except where a section says otherwise.

## The risk

A container's workspace is a bind mount of the operator's checkout, so the
container can write anything in it, `.git` included. Host git then executes
what it finds there. VS Code runs `git status` every few seconds without being
asked, so one planted setting is code running on the host as the operator.

A planted setting does not need to be a hook or `core.fsmonitor`. Each of
these runs a command during ordinary use:

| Setting | Runs during |
|---|---|
| `core.fsmonitor` | `status`, `diff`, any index refresh |
| `core.hooksPath`, hooks in `.git/hooks` | `commit`, `checkout`, `merge`, `push` |
| `filter.<name>.clean` / `.smudge` / `.process` | `status`, `add`, `checkout`, for paths `.gitattributes` assigns to the filter |
| `diff.external`, `diff.<name>.textconv` | `diff`, `log -p`, VS Code's diff view |
| `core.pager` | almost anything that prints, at a terminal |
| `core.sshCommand` | `fetch`, `pull`, `push` |
| `credential.helper` | `fetch` and `push` over HTTPS |
| `core.editor`, `sequence.editor` | `commit`, `rebase -i` |
| `gpg.program`, `gpg.ssh.program` | signed commits, `log --show-signature` |
| `include.path`, `includeIf` | any command; points git at another config file |
| `commondir`, a worktree's `gitdir` | any command; points git at another repository's config |

The container can write `.gitattributes` and `.git/info/attributes` as well,
so it can assign a filter to whichever paths it likes.

Verified while recording this: a plain `git status` ran a `filter.x.clean`
command from the repository's config even with `core.hooksPath=/dev/null` and
`core.fsmonitor=false` forced on through `GIT_CONFIG_COUNT`. Turning off hooks
and fsmonitor closes two of these routes and leaves the rest open.

Two threat models give different answers below, so every option is judged
against both:

- **An agent that makes mistakes:** it runs `husky install` or
  `git config core.hooksPath …` with no intent to escape.
- **A deliberate or prompt-injected agent:** it looks for the gap and waits
  for it.

## How the guard works

For an ordinary checkout, `gitguard.Mounts` asks for three mounts:

- `.git` bound onto itself, writable, so it cannot be renamed away;
- `.git/config` as a **read-only single-file** mount;
- `.git/hooks` as a container-only **directory**, a copy reseeded from the
  project's hooks at create and at every `rebuild`.

A worktree container gets read-only single-file mounts on the repository's
shared `config`, on the checkout's `.git` file, and on `commondir`, `gitdir`
and `config.worktree` where they exist. Working-tree paths that host git runs
are held read-only: a `core.hooksPath` directory as a directory, a file that
`include.path` names as a single file, with every parent directory pinned.

Two checks run with every `exec`, `shell` and `start-agent`:

- a planted `commondir` refuses the command;
- for a project that names its own workspace mount, all of `.git` is
  fingerprinted before the command and compared after.

## Known gaps in the guard

### 1. Replacing a guarded file on the host removes its mount

Host git never edits its config in place. It writes `config.lock` and renames
it over `config`. Since Linux 3.18
([`8ed936b`](https://github.com/torvalds/linux/commit/8ed936b5671bfb33d89bc60bdcc7cf0470ba52fe),
[LWN](https://lwn.net/Articles/595414/), `mount_namespaces(7)`), renaming over
a file that is a mount point in another mount namespace detaches that mount.
The container then sees what was underneath it: the writable `.git` mount,
which reaches the host's new file.

Engine options cannot fix this. Recursive read-only mounts (`rro`) concern
mounts nested beneath, and propagation settings concern which mounts are
shared. Neither restores a mount the kernel has detached. Docker and podman
are equally affected.

Host commands that replace `.git/config`:

- `git config <key> <value>` and `--unset` (`--global` writes `~/.gitconfig`,
  which is outside the checkout);
- setting an upstream: `git push -u`, `git branch -u`, or a plain push of a
  new branch under `push.autoSetupRemote = true`;
- creating a branch from a remote one, which sets up tracking:
  `git checkout -b x origin/x`, or `git checkout x` when only `origin/x`
  exists;
- renaming or deleting a branch that has tracking;
- `git remote add | remove | rename | set-url`, and
  `git submodule init` or `update --init`;
- tools that call any of these: `gh pr checkout`, husky's install, and VS Code
  publishing a branch. VS Code also writes `branch.*.vscode-merge-base` on its
  own; this repository's config holds fourteen of them.

None of these break the guard: commands that only read config (`status`,
`log`, `diff`, `fetch`, `pull`, `commit`, `merge`, `rebase`, a push to an
existing upstream), an in-place edit on the host, and anything run inside the
container. Inside, the file is a mount point and git's rename onto it fails.

When it breaks:

| | Linux, Docker (CI) | macOS, podman machine |
|---|---|---|
| Straight after the host rename | mount gone, file writable | reads fail with `No such file`, not writable |
| ~1–1.5 s | — | writable, mount still listed in `mountinfo` |
| ~2.1 s | — | mount gone from `mountinfo` |
| Until | the container is stopped and started | the same |

Nothing reports the break:

- `dev container list` still shows `guarded`;
- `podman inspect` still shows `"RW": false`, which is the configuration at
  create, not the live mount;
- `checkTreeMounts` (`internal/cli/gitguard.go`) reads that same inspect
  output, so it passes;
- for an ordinary checkout the guard compares nothing before and after a
  command except `commondir`, so a write made after the break goes unreported.

Which mounts are exposed. Only single files break; a directory is never
replaced, and editing a file inside a directory leaves the directory's mount
alone.

| Mount | Kind | Exposed |
|---|---|---|
| `.git/config` | file, read-only | yes |
| worktree: shared `config`, `config.worktree` | file, read-only | yes; `git config --worktree` replaces the second |
| worktree: the checkout's `.git` file, `commondir`, `gitdir` | file, read-only | yes, when the host rewrites them (`git worktree repair`, a move) |
| a file `include.path` names in the working tree | file, read-only | yes; an editor's safe save renames over it |
| `.git` onto itself, parents pinned in the tree | directory, writable | no |
| `.git/hooks` copy | directory | no; it is not the host's hooks directory at all |
| a `core.hooksPath` directory | directory, read-only | no, by the same rule; not tested directly |

A read-only single-file mount *not* nested over a writable mount of its own
directory never became writable in any case. After a rename the container
reads the stale file on Linux and gets `No such file` on macOS. This is the
common Docker "edited on the host, the container did not notice" behaviour,
not a security problem.

Evidence:

- `TestSmokeGitGuardSurvivesAHostConfigWrite` fails on all three platforms;
- `TestSmokeReadOnlyFileMount` covers both layouts under an in-place save and
  a rename;
- the operator's own `dev-cli-local` container showed it live: no
  `.git/config` entry in `mountinfo`, a writable file, and a `push -u` from
  inside that recorded its upstream.

The same failure has been reported elsewhere, for example
[kravlab/hostrunner#20](https://github.com/kravlab/hostrunner/issues/20).

### 2. On macOS, a case variant of a guarded file name reaches the host file

The guard's mounts sit on exact paths. Inside the container Linux treats
`.git/CONFIG` as a name of its own, so it misses the read-only mount. On the
host, APFS ignores case and resolves the name to the real `.git/config`.

| Probe from inside a guarded container | Result |
|---|---|
| `.git/config` (control) | refused, read-only |
| **`.git/CONFIG`** | **written into the host's `.git/config`** |
| `.GIT/config` | refused, read-only |
| `.git/HOOKS/…`, `.GIT/hooks/…` | written into the container's hooks copy, not the host's hooks |
| `mv .GIT .GIT.old` | refused, `Resource busy` |

Every directory variant resolved to the guard's mount; the file variant
missed. That fits Linux keeping one dentry per directory, so a variant name
for a directory lands on the mount point, while a file may have several
dentries. This explanation is unverified.

What follows from it:

- Every read-only single-file mount in the guard can be bypassed this way on a
  case-insensitive host, at any time, with no host action at all.
- Every guarded container on such a Mac has been exposed since creation.
- Re-applying mounts (option C) does nothing here: the mount is in place, and
  the write goes around it.
- Accidents are still stopped. git writes the lowercase name, through
  `config.lock` renamed onto the mount point, and that rename fails.

Evidence: `TestSmokeGitGuardCaseVariants`, run on the operator's Mac. It
skips on a case-sensitive host, including CI. The same bypass was reported
against Docker Desktop in
[AI-Progress-Agent/claude-container#11](https://github.com/AI-Progress-Agent/claude-container/issues/11).

### 3. A nested repository in the shared working tree

The container can create a repository anywhere in the working tree, for
example `sub/.git` with a planted config. VS Code detects repositories in
workspace subfolders and in the parent folders of an open file, and runs git
in them. Host git then reads a config the container wrote in full.

The guard looks only at what host git runs for the checkout's own repository.
No mount can cover every path where a new `.git` might appear. This affects
every design that shares a live working tree with the host: the current guard
and options A, B, C and E below.

Not tested. It was found while recording this file, and depends on VS Code's
`git.autoRepositoryDetection` and `git.repositoryScanMaxDepth` defaults.

### 4. Editor tooling runs project code

Opening an agent-touched folder as a trusted VS Code workspace runs code from
it:

- the ESLint extension loads eslint from the project's `node_modules`;
- rust-analyzer runs build scripts;
- a `.venv` interpreter is used;
- `.vscode/tasks.json` defines tasks.

The git guard never covered this, and it applies to the current shared
checkout. Restricted Mode blocks most of it.

### 5. Detection on the host loses the race

Any check `dev` runs on the host after a write competes with VS Code's
background `git status`, which can run a planted setting first. Detection
limits damage and leaves an audit trail. It does not prevent.

### 6. Checks that report the wrong state

- **`test -w` is unreliable.** busybox's `test`, the one in Alpine, decides
  from owner and mode bits and, as root, calls every file writable. It
  reported a working read-only mount as writable. The reliable probe opens the
  file for appending and writes nothing, `( : >> FILE )`. `open(2)` fails with
  `EROFS` on a read-only mount, and the content and mtime stay unchanged.
- **`/proc/self/mountinfo` lags on macOS:** the mount stays listed for about
  half a second after the file has become writable.
- **`podman inspect` / `docker inspect` and the `GIT` column** report what was
  configured at create.

## Options

### A. Check before and after each command (detection)

- **Before:** open every guarded file for write inside the container, without
  writing. If one opens, refuse and ask for a stop and start.
- **After:** compare the host's repository config with a copy taken before the
  command. Ignore `branch.*.remote`, `branch.*.merge` and
  `branch.*.vscode-merge-base`, and report and record every other change.

| | |
|---|---|
| Closes | a command starting with the guard broken; an unreported change made during a command |
| Leaves open | gaps 2–5; a host write mid-session; the race in gap 5 |
| Accidents | caught, after the fact |
| Deliberate agent | not stopped |
| Costs | cheap; no privileges; host git untouched |

### B. Watch during long sessions (detection)

A host-side watcher on the guarded files for the length of each `exec` or
`start-agent`. It warns, records, or ends the session on a change outside the
allowlist.

| | |
|---|---|
| Closes | finds a change mid-session rather than at the end |
| Leaves open | everything A leaves; still detection, and loses the race in gap 5 |
| Costs | cannot tell the operator's own host change from the container's; cannot restore the file without writing into the project (invariant 9) |

### C. Re-apply the read-only mount (prevention, narrowed)

Before every `exec`, `shell` and `start-agent`, `dev` probes each guarded file
from inside the container. If one opens for write, it re-applies that file's
read-only mount. A watcher could also re-apply as soon as a guarded file is
replaced on the host.

Spike, `TestSmokeReapplyReadOnlyMount`, two rounds on each platform:

| | macOS, podman machine | Linux, Docker |
|---|---|---|
| `exec --privileged -u 0` running `mount` | refused, `Permission denied` | refused, `permission denied (are you root?)` |
| Privileged helper container, `nsenter -t 1 -m` into the target | **works**, plain `ro` | **works**, plain `ro` |
| 5 s after the re-apply | read-only; reads the host's current content | the same |
| The container's root tries `umount` or a writable remount | refused | refused |

Only the helper works: an exec keeps the container's own confinement, whatever
privileges it is given. The helper is a separate container and keeps its own
profile. The re-applied mount survived the stale mount's cleanup on macOS,
which was the main uncertainty there.

| | |
|---|---|
| Closes | the guard staying broken after a host write: every command starts protected |
| Leaves open | gap 2, case variants on macOS, entirely; gaps 3–5; a host write mid-session, from the rename on Linux and until the watcher reacts at best; a process the agent left running between commands |
| Accidents | stopped, together with A's comparison |
| Deliberate agent | not stopped: a background loop writes within milliseconds of a host write |
| Costs | a privileged helper container started by `dev`, never by the agent, but a new privileged operation on the engine; one container start whenever a mount needs re-applying, unmeasured but probably about a second; the helper image needs `nsenter` (busybox has it); a write probe for every guarded file, not only `.git/config`; `checkTreeMounts` replaced with the probe |

### D. The container owns its repository

`--folder` copies the folder, `.git` included, into a volume `dev` owns,
instead of mounting it. This is what the k8s provider already does
(`seedWorkspace`), and folderless containers already use the volume. The k8s
provider is experimental; the local provider is the stable one.

| | |
|---|---|
| Closes | every route through git: gaps 1–3 and 5, on both platforms; no mount behaviour relied on; nothing to check per command |
| Leaves open | gap 4 for whatever the operator opens on the host; the review boundary below |
| Accidents and deliberate agents | both stopped at the host-git boundary |
| Costs | no live edits in the host checkout; a slow first copy for large folders (a clone plus uncommitted changes plus `.worktreeinclude` files would be faster); new host commits reach the container only by pull or bundle; worktree containers need rethinking; the guard's mount layer is no longer needed for these containers |

The review boundary: anything the agent commits is untrusted code, including
tracked hook scripts. With `core.hooksPath` pointing into the working tree, as
husky's `.husky/` does, checking out or merging the agent's branch runs the
agent's hooks. `git log -p` and `git diff main..agent/x` run none of them, so
review with those first. `transfer.fsckObjects=true` makes git reject
malformed objects on fetch. This repository installs its hooks as a copy in
`.git/hooks` (`make hooks`), which avoids the hooks part.

#### Seeing the agent's work under D

| Option | Live | On the Mac's disk | New code | Still exposed |
|---|---|---|---|---|
| Attach VS Code to the container (Dev Containers, works with podman) | yes | no | none | the VS Code server sits inside the container, which the host window trusts for opening URLs and forwarding ports |
| Fetch commits: the agent pushes to origin through the ssh relay; or `dev container fetch NAME` as a git bundle; or a `git-remote-dev` helper so `git fetch agent` works natively | no | after checkout | none / small / medium | the review boundary above |
| A live read-only copy outside any repository, every `.git` dropped at any depth and in any case | ~1 s | yes, read-only | largest: a sync process | gap 4, unless opened in Restricted Mode; large directories such as `node_modules` |
| On demand: `dev container diff NAME` (control characters stripped) or `dev container export NAME DIR` | no | yes | small | as the copy |

Ruled out: a `dev`-owned host folder mounted in as the working tree. Edits
would be live with no sync, but gap 3 applies in full.

#### Durability under D

The working tree lives on the volume, not in the container.

| Event | Uncommitted work |
|---|---|
| A crash, OOM kill, `podman stop`, a podman machine restart, a Mac reboot | kept, except writes not yet on disk; a file being saved can be truncated, and git can leave an `index.lock` |
| `dev container stop` / `start` / `rebuild` | kept; rebuild keeps the volume |
| **`dev container remove`** | **deleted**, with the volume (`internal/provider/local/local.go`) |
| `podman volume rm` or `prune`, `podman machine rm` or `reset` | **deleted** |

The shared folder survives the last two rows, because the work is also on the
Mac's disk. D gives that up. The mitigations considered:

- **Refuse to remove unexported work:** `remove` checks for uncommitted
  changes and unfetched commits and refuses unless `--force`. Folderless
  containers would benefit today.
- **Rescue uncommitted work:** `dev container fetch NAME --wip` runs
  `git stash create` inside the container, which records uncommitted changes
  as a commit without touching the working tree, and fetches that commit in
  the same bundle.
- **Automatic snapshots** at the end of every command. A deletion then loses
  at most the last command's work; the cost is a few seconds per command and
  history to tidy.
- **The agent commits often and pushes** through the ssh relay.

The durability table is from reading the code and general volume behaviour.
`TestSmokeFolderless` and `TestSmokePersistState` exercise stop, start and
rebuild; nothing kills a container mid-write.

### E. Shadow `.git` with a directory `dev` owns

Mount a `dev`-owned directory over `.git` holding the container's own
`config`, `hooks`, `HEAD` and `index`. Borrow the host's objects read-only
through `alternates`; new commits go to the container's own objects.

| | |
|---|---|
| Closes | gap 1: only directories are mounted; probably gap 2, since directory variants resolved to mounts, though not tested for this layout |
| Leaves open | gaps 3 and 4: the working tree is still shared live |
| Costs | the container's `HEAD` and index diverge from the host's over one working tree, so host `git status` looks wrong after the agent commits; commits reach the host only by fetch; relies on git internals (`packed-refs`, `gc`, `alternates`), submodules, LFS and `worktrees/`; unverified, would need its own spike |

### F. Mount all of `.git` read-only

Mount the checkout writable and its `.git` directory read-only over it. A
directory mount holds where the single-file mounts fail: git replaces files
*inside* `.git` but never the directory itself, and a case variant of a
directory name resolved to the mount in every test.

This is not what `dev` does today, so the spikes drive the engine directly.

**Ordinary checkout.** `TestSmokeReadOnlyGitDir` passed on all three
platforms:

| Check | Result |
|---|---|
| `git status`, `log`, `diff` inside | work |
| `git add`, `commit` inside | refused: `index.lock` on a read-only filesystem |
| Writes into `.git` from inside: `config`, `info/attributes`, a new hook, `commondir`, `git config` | refused, before any host write and after each of five |
| On macOS, `.git/CONFIG`, `.GIT/config`, `.git/HOOKS/<new>` | refused |
| Host `git config`, `branch`, `branch -u`, `add`, `commit` while the container runs | all work |
| `.git/config` writable in the 5 s after each host write | never |
| The container sees the host's new commit | yes |

**Worktree and submodule.** `TestSmokeReadOnlyGitDirWorktreeAndSubmodule`,
run on the operator's Mac only:

| Check | Result |
|---|---|
| `status`, `log` inside the worktree and inside the submodule | work |
| Commits in either | refused |
| Under the read-only `.git`: the shared `config`, the worktree's `config.worktree` and `commondir`, the submodule's `config` (under `.git/modules`) and a new hook there | refused every time |
| Case variants of those (`modules/sub/CONFIG`, `worktrees/wt/CONFIG.WORKTREE`) | refused |
| Host git in the superproject, the worktree and the submodule | all work |
| **The submodule's `.git` file** | **writable from the start**: it sits in the writable workspace with nothing over it |
| **The worktree checkout's `.git` file**, read-only on its own as the guard mounts it today | held, then **writable 0.1 s after the host rewrote it**, as `git worktree repair` would: gap 1 again |
| **`.GIT` in either directory**, on macOS | **reached the host's `.git` file**: gap 2 again |

A `.git` file holds one line, `gitdir: <path>`, and host git follows it. A
container that rewrites it can point host git at a directory it controls,
with a config of its own, the next time anyone runs git in that submodule or
worktree on the host. That is the same redirection as a planted `commondir`.
The read-only `.git` covers every git directory under it, but not the files
that point at them from the writable tree.

| | |
|---|---|
| Closes | gaps 1 and 2 for everything under `.git`, on every platform tested; every setting in the risk table, since the container can write no config or hooks |
| Leaves open | a submodule's `.git` file, writable outright; a worktree checkout's `.git` file, by a host rewrite and by a case variant on macOS; gaps 3 and 4, as for any shared working tree |
| Accidents | stopped |
| Deliberate agent | stopped for an ordinary checkout without submodules; not stopped through a submodule's or a worktree's `.git` file |
| Costs | the agent cannot commit, stash, or create a branch: the operator commits from the host, or the work leaves the container another way; agents expect to commit and would need telling, in the project's agent instructions |

Covering the `.git` files would need what failed elsewhere: a single-file
mount, which breaks on a host rewrite and, on macOS, to a case variant. So
for repositories with submodules and for worktree containers, F needs A's
after-command comparison extended to every `.git` file, or D.

`.gitattributes` is still writable, since it lives in the working tree. It
can assign paths to a filter or a textconv, but the command those run is
defined in config, which the container can no longer write. It runs only
what a filter the operator's own global config defines would run.

Not tested: a nested repository the agent creates in the working tree
(gap 3); Git LFS; `git submodule absorbgitdirs` and other host commands that
rewrite a submodule's `.git` file.

### Rejected

- **The macOS immutable flag (`chflags uchg`)** for the length of a session.
  It stops both the rename and the case variant. It also stops the operator's
  own host git from writing config (`push -u`, `remote add`, VS Code's
  writes), which is unacceptable. Its Linux counterpart, `chattr +i`, needs
  `CAP_LINUX_IMMUTABLE`.
- **Host git overrides only**, as in
  [sandcastle#1014](https://github.com/mattpocock/sandcastle/pull/1014):
  `core.hooksPath=/dev/null` and `core.fsmonitor=false` through
  `GIT_CONFIG_COUNT`. They cover only the git that `dev` itself runs, not
  VS Code's or the shell's, and the other settings in the table still run. A
  global `~/.gitconfig` cannot apply them, since repository config wins. Worth
  keeping as a cheap extra layer on `dev`'s own host git calls, not as a fix.
- **Making host git ignore repository config:** git has no setting for it, and
  a wrapper cannot reach every git process on the host (VS Code ships its own
  git path, `gh` calls git directly).
- **Proxying the agent's git to the host:** agents use too much of git to
  allow-list, and it is a large new attack surface.
- **Other single-file mount layouts:** any single-file mount on the shared
  path fails as in gap 1. A read-only `.git` *directory* is option F, at the
  cost of the agent's commits.
- **File permissions:** git writes by renaming files inside `.git`, so the
  directory must stay writable.

## Comparison

| | Live edits on the host | Gap 1, host rename | Gap 2, case variant on macOS | Gap 3, nested repository | Deliberate agent | New privilege |
|---|---|---|---|---|---|---|
| The guard as it is | yes | open until restart | open | open | not stopped | none |
| A | yes | refused next command; mid-command write reported | open | open | not stopped | none |
| A + B | yes | reported mid-session | open | open | not stopped | none |
| A + C | yes | re-applied next command, or by the watcher | open | open | not stopped | privileged helper |
| D | no; attach, fetch or copy | closed | closed | closed | stopped at host git | none |
| E | yes | closed | probably closed, untested | open | partly | none |
| F | yes; the agent cannot commit | closed under `.git`; a worktree's `.git` file open | closed under `.git`; `.GIT` in a submodule or worktree open | open | stopped for an ordinary checkout; not through a submodule's or worktree's `.git` file | none |

## What to do now, with the guard as it is

- **Read a guarded repository's config without running anything from it:**
  run `git config --local --list` on the host, and look for any of the
  settings in the risk table that you did not set.
- **After a host command that writes config**, put the mount back. Restarting
  the container does it, which a start setting up mounts afresh should
  guarantee, though no test confirms it. Without a restart, the spike's
  method does it, run on the host:

  ```sh
  id=$(podman ps -q --filter label=dev.workspace=WORKSPACE --filter label=dev.container=CONTAINER)
  podman run --rm --privileged --pid=container:"$id" docker.io/library/alpine:3.20 \
    nsenter -t 1 -m -- sh -c 'mount -o bind FILE FILE && mount -o remount,bind,ro FILE'
  ```

  `FILE` is the path inside the container, for example
  `/workspaces/dev-cli/.git/config`. Then confirm it from inside:
  `( : >> FILE ) 2>/dev/null && echo writable || echo read-only`.
- **Run config-writing host git commands while the container is stopped**
  where you can.
- **Open agent-touched folders in Restricted Mode** unless you trust what is in
  them.
- **`docs/USAGE.md`, "What a container can do to your git", overstates the
  guard:** it says `.git/config` is read-only inside the container, which gaps
  1 and 2 show is not always so. It should be corrected along with whichever
  fix is chosen.

## Open questions

- Which threat model the guard serves. That decides between A + C, which keeps
  live edits, D, and F, which keeps live edits at the cost of the agent's
  commits.
- Under F, how the agent's work becomes commits: the operator commits from the
  host, or something else does.
- Under F, how submodule and worktree `.git` files are covered.
- Whether a restart restores the mount. Expected, not tested.
- Gap 3 against real VS Code. Untested.
- Option E as a whole. Untested.
- The helper container's start time. Unmeasured.
- A container killed mid-write under D. Untested.

## References

- [vfs: Lazily remove mounts on unlinked files and directories, torvalds/linux@8ed936b](https://github.com/torvalds/linux/commit/8ed936b5671bfb33d89bc60bdcc7cf0470ba52fe)
- [Detaching mounts on unlink, LWN](https://lwn.net/Articles/595414/) and [Mount point removal and renaming, LWN](https://lwn.net/Articles/570338/)
- [A read-only bind of .git/config is removed when host git rewrites the file, kravlab/hostrunner#20](https://github.com/kravlab/hostrunner/issues/20)
- [A file written inside as .git/CONFIG gets past the read-only .git/config mount, AI-Progress-Agent/claude-container#11](https://github.com/AI-Progress-Agent/claude-container/issues/11)
- [Worktree sandboxes mount the host .git read-write, mattpocock/sandcastle#1010](https://github.com/mattpocock/sandcastle/issues/1010) and [PR #1014](https://github.com/mattpocock/sandcastle/pull/1014)
- [A Git Config Key Ran Code in Seven Coding Agents, grith](https://grith.ai/blog/git-config-key-2022-fix-coding-agents)
- [Per-file bind mounts pin a deleted inode, NousResearch/hermes-agent#132790](https://github.com/NousResearch/hermes-agent/issues/132790)
