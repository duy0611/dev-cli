# Architecture

How `dev` is put together and why: the invariants the code depends on, the
Kubernetes provider's sharp edges, and how CI and releases work. `AGENTS.md`
holds the working rules; this file holds the reasoning behind them. The design
record for each feature is in `docs/specs/`.

## CI and releases

CI runs `make lint && make test` on push and pull request, on Linux amd64 and
macOS arm64, with `make smoke` as a second job beside it on Linux amd64 and
arm64 — macOS runners have no nested virtualisation, so no engine starts there.
A change touching only Markdown or `docs/plans`, `docs/specs` skips both, and
`ci_success_check` is the one job branch protection requires: it passes when
they passed or were skipped, and fails otherwise. Releases are cut by release-please: every push to main
updates an open release PR from the Conventional Commit subjects since the last
tag, and merging that PR tags the release, publishes it with the changelog, and
builds `dev` for darwin and linux on both architectures into it. Nobody tags or
drafts a release by hand (`.github/workflows/`). CI installs tools from
`mise.toml` and calls the Makefile targets rather than restating them, so a
green tick means what a clean local run means. The smoke job runs the local
provider against the runner's Docker; its Kubernetes half skips there, since CI
has no cluster or registry to name.

## Structure

`internal/cli` orchestrates; the work lives in the packages it calls. A command
function that grows logic beyond "resolve inputs, call a package, print" is in
the wrong file.

Providers register themselves by kind in `init`, and `internal/cli/root.go`
blank-imports each one to trigger it. Adding a provider is a new package plus one
blank import — not an edit to the factory. That held when k8s was added: the only
changes outside the new package were the blank import, `--kind k8s` becoming
valid, and one new command (`container sync`).

`Syncer` is an optional interface, discovered by type assertion. It promises a
postcondition — after `Sync`, the container's copy of the workspace's settings
matches the workspace — so the local provider satisfies it by doing nothing:
every command resolves the settings afresh, and no copy exists there to drift.
It is optional for the reason `AgentForwarder` is, not because one provider
cannot implement it.

`IdleStopper` is optional in the same way, and only the local provider has it:
`Ensure` copies the watchdog into the container and starts it once. The k8s
provider omits it, since a pod whose PID 1 dies is restarted, so a watchdog
would be a restart loop. Sessions are judged from `/proc` inside the container,
never from the host and never from anything a process can claim: a process
whose parent is outside the PID namespace reports `ppid 0`, which only the
engine's exec can produce, and it does not depend on how Docker Desktop or a
podman machine report parent PIDs. PID 1 receives only signals it has a handler
for, so the watchdog sends `TERM`, and if init survives it kills every other
process with `kill -KILL -1` and records `reaped` rather than `stopped`.

## Invariants

Violating one of these produces a failure far from its cause. Code comments
refer to them by number, so the numbers are stable.

1. **Pass both `--id-label` values on every `up` and `exec`.** Given none, the
   devcontainer CLI infers one from `--workspace-folder`, so two containers
   created from one folder become the same container. Given a *different* set,
   it looks up a container that does not exist and creates a second one beside
   it — no error either way. They are built in exactly one place,
   `internal/provider/local/label.go`, and a test asserts `up` and `exec` agree.

2. **Resolve folders to physical paths before storing or mounting them.** The
   engine is VM-backed on macOS and resolves the path string *inside* that VM,
   where `/tmp` is a real directory rather than a symlink to `/private/tmp`.
   Handing it an unresolved path mounts an empty directory, silently.
   `xpath.Resolve` is the only way in.

   The same VM does not remap a bind mount's ownership to the container user,
   as Docker on Linux does, so git inside refuses the checkout as dubious
   ownership. `trustMountedRepository` adds the mounted repository to
   `safe.directory` in the container's global git config at every start and
   rebuild.

3. **A setting's spec is never a value.** The database stores
   `keychain:NAME` or `op://…`; resolution happens per invocation, in
   `internal/secret`. That is what keeps tokens out of the database, out of
   `workspace show`, and out of any backup of either. Resolving per invocation
   is also why a rotated secret needs no restart — fully true on local; on k8s
   an exec'd command sees the new value immediately, but the pod's own
   environment comes from `envFrom` and is fixed until a stop and start.

   `container sync` is what makes the cluster's copy current without one. It
   applies the Secret alone, never the Deployment: the apply is server-side and
   drops fields it no longer sets, so an apply without `rebuiltAt` clears that
   annotation, changes the pod template, and lets `Recreate` destroy the work
   the command exists to protect. The running pod keeps its environment; the
   replacement the cluster builds after an eviction or a drain does not.

4. **Never store live container status.** It is read from the engine every
   time. A stored copy is wrong the moment anything happens outside `dev`.

5. **Migrations are portable SQL.** No `AUTOINCREMENT`, no `datetime('now')`
   defaults, timestamps written by the application. The same files are meant to
   replay against Postgres when the cloud provider lands.

6. **`PRAGMA foreign_keys = ON` and `MaxOpenConns(1)`.** SQLite defaults foreign
   keys off, which would skip the cascade that deleting a workspace depends on.
   It also takes one writer, so a connection pool turns lock contention into
   errors. Both are asserted or relied on by tests.

7. **Exit codes are part of the interface.** `2` for a malformed request, `3`
   for something named that does not exist, `1` otherwise. Cobra reports a bad
   flag or a wrong argument count as a plain error, which would exit 1 — hence
   the wrappers in `internal/cli/args.go` and the `SetFlagErrorFunc` in
   `root.go`. Use `exactArgs`/`noArgs`/`minArgs`, never cobra's directly.

8. **A provider's kind never changes.** `existingProvider` in
   `internal/cli/provider.go` refuses it; the row is not rewritten. Workspaces
   go on naming the provider and the containers under them stay in the engine
   they were created in, so a flip leaves records pointing at an engine that has
   never heard of them — `container list` answers confidently and wrongly, and
   `remove` can never reach the real container. Remove and recreate instead:
   `provider remove` already refuses while a workspace names it, which is what
   makes that route safe without a second copy of the rule.

9. **Do not write into a project's folder.** `dev` never creates or edits a
   `devcontainer.json` inside a project, and a missing agent is reported as
   something to add to the project, not something `dev` installs. A folder that
   ships its own configuration always wins. A folder with none can still be
   run: `--generate` renders a base Ubuntu configuration from the
   `internal/dcgen` catalog and stores it on the container row, where it
   cascades away with the workspace and travels to Postgres with the rest of
   the schema. The devcontainer CLI only takes a path, so that configuration is
   materialised to a temporary file per invocation and passed as `--config` —
   which is why every `model.Container` reaching a provider has a usable
   `ConfigPath` and neither provider knows where it came from.

   A container need not have a folder at all: `--no-folder` records
   `SourceKind` as `none` with an empty `Source`, always generates its
   configuration, and mounts a volume `dev` owns — a docker named volume called
   `dev-<workspace>-<container>` on local, the existing PVC on k8s. The
   generated document names both `workspaceMount` and `workspaceFolder`,
   because without the second the CLI would derive the in-container path from
   the temporary directory `materialise` creates and it would change every
   invocation. That temporary directory is also what such a container passes as
   `--workspace-folder`, which is why neither provider needs to know what a
   folderless container is to run one — Up, Exec, Stop, Status and Logs are
   unchanged. The places that do branch on `SourceKind` do it to remove the
   volume and to skip the initial copy: the local provider removes the volume
   in `Remove`, matching k8s deleting its PVC (`rebuild` keeps it on both); and
   k8s `Up` calls `seedWorkspace` only for a folder-backed container, since
   there is no host tree to stream otherwise.

   `seedWorkspace` is unexported and called from exactly one place on each
   provider — `Up`, on first create. That is what keeps it safe: it unpacks a
   tar over the workspace, so reaching it from the CLI would hand the operator
   a way to destroy whatever an agent had half-done. `container sync` pushes
   settings and never files. The local provider's seeds an isolated container
   (invariant 12) and has `up --skip-post-create` hold the lifecycle commands
   back until it has, then runs them with `run-user-commands`; a recreate
   keeps the volume and is never seeded.

   The generated document also carries a `postCreateCommand` that chowns the
   mount to `vscode`. Docker creates a named volume owned by root, and unlike a
   bind mount — which the devcontainer CLI UID-remaps to the host user itself —
   a volume gets no such fixup, so the first write from the non-root remote
   user fails with permission denied. One `sudo chown` after create is enough,
   since the volume's ownership then persists across every later start; k8s
   runs the same command but it is a no-op there, because the pod's `fsGroup`
   already makes the PVC group-writable.

   `.devcontainer/agents.yaml` is the same rule from the other side: `dev`
   reads it and never writes it, and applies what it declares *inside* the
   container, through `Provider.Exec`, onto the state volume — never into the
   project. `create` validates it before the row exists, the row records only
   which file (`agent_config`) and whether an apply is owed
   (`agent_config_pending`), and `rebuild` loads it before recreating the
   container, so a file that has gone or stopped parsing fails while the old
   container still exists.

   `.worktreeinclude` is the same rule again: `worktree create` reads it from
   the source checkout and never writes it, and the only place it copies into
   is the checkout it has just made.

10. **A container's agent state is a column, not a document field.** Every new
    container mounts a volume at `/var/dev-state` unless `--no-persist-state`,
    with `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `HERMES_HOME`,
    `OPENCODE_CONFIG_DIR` and `GIT_CONFIG_GLOBAL` pointing into it, so plugins
    and settings outlive a rebuild. Each moves one tool's own directory; `XDG_*`
    would have moved every XDG-aware program in the container. The column
    defaults to 0 while `create` defaults it to 1: the migration's default
    speaks for rows that already exist, and those have no volume to mount.

    It lives on `containers` because `rewriteGeneratedTools` re-renders the
    whole generated document from the tool list and `SourceKind` — a mount
    recorded only in the JSON is dropped by the next `rebuild --tools`, and the
    next `up` then starts a container with no volume and no warning. Fixed at
    create for the same reason the workspace mount is.

    Two mechanisms, one outcome, and never both at once: a generated document
    carries `mounts` and `containerEnv`. A project-owned container gets the
    volume from a merged document instead: `dev` reads the project's
    `devcontainer.json`, replaces any `mounts` entry targeting `/var/dev-state`
    with its own, writes the result to a temporary file and passes
    `--override-config` on both `up` and `exec`. One flag on both commands, so
    there is no asymmetry for a later `exec` to fall through.
    `--override-config` replaces rather than deep-merges, which is why `dev`
    does the merge itself and why every field it does not touch has to
    round-trip untouched. The merge is per invocation and never stored, for
    invariant 4's reason applied to a file `dev` does not own. `dev` owns
    `/var/dev-state` while it drives: an existing mount there is replaced, not
    stood down next to, so `Remove` stays unconditionally correct instead of
    orphaning a volume it can no longer name. That replacement is also what
    still prevents `duplicate mount destination` — the collision is real, `dev`
    now resolves it instead of risking it. A project whose `devcontainer.json`
    names `dockerComposeFile` does not reliably get the mount applied at all;
    `create` warns rather than silently losing state at the next rebuild. The
    variables ride `--remote-env` and so reach both commands. k8s ignores all
    of it and grows a PVC subPath instead.

    Credentials stay out. Agents authenticate from workspace settings resolved
    per invocation, which is what keeps the ssh relay's "nothing worth stealing
    rests in the container" true of this volume too.

11. **A worktree container mounts two host paths, each at its own path.** A
    worktree's `.git` is a file holding `gitdir: <common>/worktrees/<name>`, and
    the repository holds a backlink to the checkout — both absolute host paths.
    The common directory is bind-mounted so the first resolves; the checkout is
    bind-mounted *at its own path* so the second does. The second is the one
    that looks optional and is not: `git gc --auto` runs `git worktree prune`,
    which deletes a registration whose backlink does not resolve, so a container
    with only the first mount lets an agent prune its own worktree away from the
    inside, silently.

    The mount source is git's **common directory**, never `<repo>/.git`. They
    differ for a bare repository, which takes worktrees like any other.

    Two routes as ever, and never both: a generated document names both mounts
    directly in `mounts`. A project-owned container gets them the same way it
    gets the state mount — `dcgen.OverlayWorktree` merges both bind entries into
    a copy of the project's `devcontainer.json`, and the result reaches the
    container through `--override-config`, on `up` and `exec` alike (see
    invariant 10; there is no separate `--mount` route to keep off `execArgs`,
    because none of this uses `--mount`). `model.Container.WorktreeRepo` carries
    the common directory into that merge. It is a column, read from git by
    `container create` (`gitwt.WorktreeRepo`) and fixed then like the workspace
    mount, so a worktree made by plain `git worktree add` is mounted as
    correctly as one `dev worktree create` made; it is empty for any container
    whose folder is not a linked worktree. Both mounts are rebuilt from it in
    `rewriteGeneratedTools`, because that function re-renders the whole
    generated document and drops anything it cannot derive, exactly as in
    invariant 10.

    `dev worktree create` makes the checkout and records it; it makes no
    container. The `worktrees` table belongs to the workspace, not to a
    container: `worktree remove` refuses while a container's folder is the
    checkout, and `workspace remove` refuses while worktrees exist, so a
    checkout on disk is never left with no row naming it.

    An extra folder that is a worktree checkout is the same rule again: in
    guarded mode it is bind-mounted at its own host path, and its common
    directory, read from git at create and stored on its `container_folders`
    row, is mounted read-only at its own path.

    k8s refuses a container on a worktree outright: a pod has no host bind
    mounts, and a streamed copy of the object store would drift from the host
    with nothing to push it back.

12. **A container's mode is guarded or isolated, and a guarded `.git` is one
    read-only directory mount.** The mode is a column, fixed at create and kept
    by `rebuild` for invariant 10's reason; `legacy` is what migration 0008
    wrote for every row that predates modes, and those keep the single-file
    guard they were made with. Nothing else ever writes `legacy`.

    Never a read-only mount on a single file inside a writable `.git`. git
    replaces its files by writing a lock and renaming it over the original,
    and since Linux 3.18 renaming over a file that is a mount point in another
    mount namespace detaches the mount — so `.git/config` became writable the
    moment the host wrote its config, at once on Linux and about 1.5 s later
    on a macOS podman machine, while `inspect` went on reporting it read-only.
    On a case-insensitive host `.git/CONFIG` also went around it. git never
    replaces `.git` itself, and a case variant of a directory name resolved to
    the mount in every test, so guarded mounts all of `.git` read-only
    (`gitguard.ReadOnlyMounts`, `ReadOnlyWorktreeMounts`) — at the cost of the
    agent's commits, which is the mode's trade, and of the ssh relay, which a
    container that cannot commit has no use for. The smoke tests in PR #28
    are the evidence.

    What guarded still holds read-only as single files — a file an
    `include.path` names in the working tree — is probed from inside the
    container before each command by opening it for append and writing
    nothing (`probeHeldFiles`). Not `test -w`: busybox's decides from mode bits
    and calls a read-only mount writable as root. Not `inspect`, which reports
    the mount as configured.

    Isolated copies the folder into the workspace volume and never mounts it.
    The repository crosses as a git bundle (`internal/seed`), never as `.git`'s
    files, so the host's config and hooks stay on the host and a worktree's
    `.git` file, which names a host path, is never copied. `dev container
    fetch` brings branches back the same way, into remote-tracking refs, with
    `transfer.fsckObjects`; host git reads commit data from the container and
    nothing else. `remove` refuses while the volume holds work the folder
    lacks, since it is the only copy.

    A container can have extra folders beyond its primary, which stays in
    `containers.source` so nothing that reads it changes. They live in
    `container_folders`, each with its container path stored rather than
    derived, so renaming the folder on the host cannot move it inside. Guarded
    mounts one read-only `.git` per repository among them, at the folder's
    container path; the tree search runs once per repository with every
    folder's mount as a root, since one repository's config can name a file in
    another. Isolated gives each folder a volume of its own, named from a hash
    of the host path, and seeds, fetches and checks each one separately. The
    folder mounts reach the container by the git guard's route — appended to a
    generated document, merged into a project's — and are kept out of the
    configuration digest, so adding a folder is not drift. `rebuild` writes the
    new set only once the provider's rebuild succeeds. An isolated primary's
    volume is mounted at the stored `workspace_path`: `/workspaces/<basename>`
    for a container created since, `/workspaces/<name>` backfilled for the ones
    before, so their volume stays where it is.

## Kubernetes provider (experimental)

The devcontainer CLI speaks Docker only. It is used for reading the merged
configuration and building the image; `kubectl` does everything else. The image
is built on the host and pushed, so a k8s provider still needs Docker locally.

Experimental means the settings in `Config`, the three objects, and the way
lifecycle commands are driven can all still change. It does not license a
sloppy change: the points below are what make the provider work at all, and
breaking one produces a failure far from its cause whatever the label says.

- **Two subPaths on the PVC, workspace and home — three when the container
  persists state.** Scaling to zero destroys the container filesystem. Without
  the home mount, anything `postCreate` wrote to `~` disappears on every stop
  and "postCreate runs once" is false. The third carries the agents'
  configuration and exists only when `persist_state` is set; the overlap guard
  in `buildManifest` checks every pair of mount points, because a third one is a
  third chance to shadow another.
- **`--platform` is explicit, defaulting to `linux/amd64`.** An arm64 image on
  an amd64 node crash-loops with `exec format error`.
- **The builder is chosen, not assumed** (`builder.go`). The CLI gates
  `--platform` and `--push` on `<docker-path> buildx version` printing a semver,
  so `selectBuilder` probes with that exact rule. docker+buildx does both in one
  step; podman satisfies the probe and honours `--platform` but has no `--push`,
  so it pushes separately; plain docker can only build for this host. Prefer
  docker+buildx over podman — Features under rootless podman fail on the bind
  mount the generated Dockerfile uses (devcontainers/cli#548).
- **`imagePullPolicy: Always`, and `rebuild` bumps a pod annotation.** The tag
  is always `:latest`, so nothing else makes a rebuild visible.
- **`Recreate`, not `RollingUpdate`.** The PVC is ReadWriteOnce; two pods would
  leave the new one unschedulable.
- **The keep-alive command traps SIGTERM, and `stop` waits.** The command is
  PID 1, and PID 1 ignores signals it has no handler for, so without the trap
  every stop waits out the full grace period before the kubelet SIGKILLs. The
  sleep is backgrounded with `wait`, or the trap could not run until it
  finished. `kubectl scale` also returns before the pod is gone, so `Stop`
  polls until no pod carries the labels — `docker stop` does not lie about this
  and neither should this.
- **The lifecycle marker is a file on the volume**, not an annotation. The
  Deployment is re-applied on every start and a server-side apply drops fields
  it no longer sets, so an annotation clears itself.
- **Every kubectl call carries `--context` and `--namespace`**, built in one
  place. A call that reaches the wrong cluster does not fail; it succeeds
  somewhere else.
- **`pipeInto` streams through `cmd.StdinPipe`, not an `io.Pipe` on
  `cmd.Stdin`.** os/exec drains an `io.Reader` from a goroutine of its own, and
  that goroutine gives up when the command exits — leaving the producer blocked
  on a pipe with no reader, so a pod that dies mid-sync hangs instead of
  failing. A real pipe gives EPIPE. An EPIPE with a clean exit is still an
  error: kubectl that took none of the archive did not complete a copy.
- **Commands are shell-quoted, then quoted again inside `su -c`.** A test stub
  matching `'test' '-f'` will never fire — match the path instead. This cost
  three wrong tests before it was noticed.

## The commit-msg hook

`.githooks/commit-msg` rejects a tooling-attribution line as well as a
non-conventional subject, because the attribution rule had already been written
in the agent instructions when a commit carrying the trailer went in anyway: a
harness reminder asked for it, and the harness default was followed. A rule worth
stating is worth enforcing somewhere that does not depend on being read
correctly. `make hooks` copies it into `.git/hooks`, which no clone inherits,
so a fresh checkout runs it first — a hook that is quietly absent is worse than
none, since the rule looks handled. A copy, not `core.hooksPath`: host git
would otherwise run the hook from the working tree, which a container's agent
can edit. The installed hook warns when `.githooks` has moved on; review the
change, then run `make hooks` again.

The hook allows a `Co-Authored-By:` naming a person. It is attribution to
tooling that is refused, not co-authorship.

## Tooling and testing, in detail

The longer form of conventions summarised in `AGENTS.md`.

- **Shell out, do not reimplement.** The local provider drives the
  `devcontainer` CLI and `docker`; the Kubernetes one drives the same CLI and
  `kubectl`. No Docker Go SDK, no client-go, no devcontainer-spec parser.
- **Check flags and output against the real CLI before using them.**
  `devcontainer` has no `rebuild` subcommand (it is
  `up --remove-existing-container`); `--secrets-file` exists on `up` but not on
  `exec`, which is why both local paths use `--remote-env` and accept that
  values are briefly visible in the host process list; and
  `read-configuration` emits *plural* merged fields (`onCreateCommands`),
  reports a null `workspaceFolder` when unset, and shells out to `docker ps`
  even to read a file.
- **The local provider builds for the engine's platform.** `up` runs
  `devcontainer up` with `DOCKER_DEFAULT_PLATFORM` set from
  `docker version --format '{{.Server.Os}}/{{.Server.Arch}}'`. An engine keeps
  one image per tag, and a k8s cross-build for `linux/amd64` moves a shared base
  tag onto amd64; without a platform, every later local build on an arm64
  engine runs `FROM` it under emulation, with nothing failing. The variable,
  not `--platform`: the devcontainer CLI refuses `--platform` without BuildKit,
  and a docker CLI without buildx is common, while the docker CLI honours the
  variable on `build`, `run` and `compose` either way. The engine's platform,
  not `runtime.GOARCH`, because a remote `DOCKER_HOST` or an Intel Mac driving
  an arm64 machine makes dev's own architecture the wrong one. An operator's own
  `DOCKER_DEFAULT_PLATFORM` is left alone and skips the probe; a failed probe
  runs `up` unchanged.
- **Prompt only at a terminal.** `provider configure` asks for whatever its
  flags left empty, but `isTerminal(os.Stdin)` guards the prompting: a scripted
  run has to fail naming the missing flag rather than block on a question
  nobody will see. Every prompted value has a flag, and a flag already given is
  never asked about again. Both paths fall back to the stored provider, so
  re-running `configure` changes only what it was told to; `k8sSettings` is the
  one list they walk, and `-` is the only way to empty an optional setting now
  that an absent flag means "keep".
- **Tests for external tools use stub executables on a temporary `PATH`**, not
  an interface with a mock. See `internal/provider/local/local_test.go`. The
  k8s harness *replaces* `PATH` with its stub directory instead of preceding
  the host's: the builder probes docker and then podman, so on a machine that
  has podman a leaked host binary becomes the one under test — it answered the
  BuildKit probe and pushed to a real registry from a unit test. A test that
  wants a host binary asks for it by name, as `requireRealCLI` does.
- **A unit test may not need the network either.** The real devcontainer CLI
  asks the engine for an image's metadata and, told nothing, fetches it from
  the registry instead — so a stub docker that answers `inspect --type image`
  with silence makes `make test` fail wherever a registry is unreachable or
  rate-limiting, with a `SyntaxError` from inside the CLI's own JavaScript.
- **Store tests use a temp file, never `:memory:`** — that database is
  per-connection, and the pool hands the migration to one connection and the
  query to another.
