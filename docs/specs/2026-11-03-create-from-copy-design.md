# Creating a container from an existing one: image and state

Extends `2026-10-08-create-from-design.md`. Two items on that design's "Out of
scope" list are now in scope: skipping the build by reusing an image, and
copying agent state. A third changes: a project-owned source is now accepted.

## Problem

`dev container create NAME --from SOURCE` copies SOURCE's tool list and its
persist-state and agents.yaml choices, then runs a full `devcontainer up`. Two
costs remain:

- **The build.** Even when Docker's layer cache serves every layer, the
  devcontainer CLI still walks the build and Feature install. The result is an
  image identical to one already on disk.
- **The empty state volume.** The new `/var/dev-state` starts empty. Agent
  logins, settings, plugins, MCP servers, Claude history and memory are all set
  up again by hand.

The typical case is a git worktree. The operator has a main container on a
repository and wants a container on a new worktree of that same repository,
quickly, with the same image and the same agent state.

## Goal

```
dev worktree create feat-x --branch feat-x --path ../feat-x
dev container create feat-x --folder ../feat-x --from web --copy image,state
```

This gives `feat-x` a running container on `web`'s exact image with no build,
and a state volume that starts as a copy of `web`'s. `web` is read and never
written.

## Non-goals

- Copying workspace files, uncommitted work, or a worktree's ignored files.
  `.worktreeinclude` already covers the last of these.
- Remapping Claude's per-project history and memory to the new working folder.
  Claude keys them by path, so a worktree at a different path starts as a new
  project in Claude's eyes. The copied data is present under the source's path
  and is left as it is. The operator accepted this.
- `--copy` on the k8s provider (see *Modes*).
- `--from` or `--copy` on `dev worktree create`. The earlier spec described
  `worktree create --from`, but it was never built and is not built here. The
  two-command flow above is the supported route.
- An interactive prompt asking what to copy. The operator rejected it. Plain
  `--from` keeps meaning "settings only", so a script and a terminal behave the
  same.

## Anti-goals

These would make the feature a failure even if it worked:

- A rebuild of one container silently changing the other's image.
- State or an image an agent could have tampered with, flowing into or out of
  an isolated container.
- A failed `--copy` leaving a half-made container, image tag or volume behind,
  or touching the source.
- Plain `--from` changing behaviour for anyone who does not pass `--copy`.

## Surface

```
dev container create NAME --from SOURCE [--copy image|state|image,state] ...
```

- `--copy LIST` takes a comma-separated list. Valid entries are `image` and
  `state`. Duplicates are harmless. An empty or unknown entry is exit 2.
- `--copy` without `--from` is exit 2.
- Without `--copy`, `--from` behaves exactly as today, apart from the
  project-owned source change below.

## Sources

The source is looked up in the command's workspace, as today. It can be either
of two kinds.

**A generated source** (has `GeneratedConfig`) works as today. Its tool list is
copied and `+/-` diffs apply. The target folder must not have its own
`.devcontainer`: this is the existing "would shadow it" error.

**A project-owned source** (no `GeneratedConfig`) is new. The new container
uses **its own folder's** `.devcontainer`, found and resolved exactly as a plain
`create` would. The source supplies only:

- `PersistState`, which `--no-persist-state` overrides.
- `AgentConfig`, following the existing inheritance table. `""` means the new
  folder's own agents.yaml.
- With `--copy`, the image and/or the state.

If the new folder has no `.devcontainer`, the command fails with exit 2 and
says that the source uses its project's config and the target folder has none.
Any `--tools` together with a project-owned source is exit 2. Pointing at
another folder's devcontainer.json was rejected for two reasons: it would break
when that folder moves, and it would ignore config changes on the worktree's
branch. The earlier spec's reasoning still applies.

A `--no-folder` target together with a project-owned source is exit 2, since
there is no folder whose config to use.

Mixing kinds is not possible. A generated source with a target folder that has
its own `.devcontainer` is refused ("would shadow it"). A project-owned source
with a target folder that has none is refused (above). So the config match below
only ever compares generated with generated, or project-owned with
project-owned.

## Modes

`--from` is unchanged: it is allowed in every mode, on local and k8s.

`--copy` is refused with exit 2 when **either** the source or the new
container is `isolated`. An isolated agent has free rein over its image's
running container and its state volume, and could have planted hooks, plugins,
MCP commands or git config there. Nothing should flow out of it into a guarded
container, and an isolated container's threat model assumes it starts from
what dev controls. `guarded`, `unprotected`, legacy (pre-mode) and `--no-folder`
containers are allowed.

On k8s every container is isolated, so `--copy` is refused there by the same
rule. A pre-mode k8s row may not carry `Mode == isolated`, so `--copy` also
requires the local provider explicitly: the existing `requireLocalProvider`,
checked after the mode check so the usual message names isolation. `--from`
itself keeps working on k8s, including with a project-owned source; the k8s
build path must be checked for that case.

## `--copy image`

**Meaning.** The new container is created from the image SOURCE's container was
created from. This is the image ID from `docker inspect` on SOURCE's container,
found by its `dev.workspace`/`dev.container` labels. It is **not** a
`docker commit` of the running container's filesystem. Nothing is built.

**Snapshot.** dev builds a new image for the new container, for example
tagged `dev-<ws>-<name>:from-image`; the exact name is left to the builder. It
is made from SOURCE's image ID with `docker create` and `docker commit`, with
the `devcontainer.metadata` label keeping the Features' entries and the base
image's own (such as `remoteUser`) and dropping SOURCE's own config entry, but only
when it is the last entry, has no id and carries mounts, lifecycle commands or
privileges; harmless keys of an entry without those stay. The devcontainer CLI
also bakes the source config's `containerEnv` into the image it builds as image
`ENV`, so a project-owned source's `containerEnv` values carry into the copy
wherever the copy's config does not set the same keys (generated configs set
identical env, so there it has no effect). A source whose config builds nothing
(no `features`, `build` or `dockerFile`) is tagged as it is: the CLI used the
base image as-is and put the source's entry only on the container's label, so
the image's last id-less entry belongs to the base and is kept.
The snapshot is the new container's own image, not a bridge: it lives until
`dev container remove`, and a rebuild replaces it. SOURCE can be removed and its
image pruned at any time. From the first creation nothing ties the two
containers together: a rebuild of either leaves the other's image alone.

**How the build is skipped.** For the first creation only, the config passed to
`devcontainer up` is rewritten:

- `image` becomes the snapshot tag;
- `features`, `build`, `dockerFile` and `context` are removed.

Feature-contributed settings (`containerEnv`, lifecycle hooks, capabilities)
come back from the snapshot image's `devcontainer.metadata` label, as for any
image the devcontainer CLI built. The builder confirms this against the real CLI
before relying on it.

- For a generated config, the rewritten document is what `materialise` writes
  for that one invocation.
- For a project-owned config, it goes through the existing `--override-config`
  route, which replaces rather than merges.

In neither case is it stored: `GeneratedConfig` and `ConfigPath` stay exactly
what they would be without `--copy`.

The rewrite applies only to the first start: when the snapshot tag exists and
the engine has no container for the row. That means the first start after
`create`, including a later `start` after `--no-start`. Once the first start
has used it, whether the `up` succeeded or failed after creating the
container, the tag is renamed to `:copied`, so the rewrite never applies again.
A later start of the existing container just starts it; if the container is
gone, a start builds from the config. The `:copied` tag only keeps the image
referenced until remove or rebuild. A rebuild goes through the normal path, building afresh and replacing
the snapshot.

**What still runs.** The new container's own `onCreateCommand`,
`updateContentCommand` and `postCreateCommand` still run on first start. They
must: they chown the new, root-owned workspace and state volumes, and the
devcontainer CLI keys them per container. Mounts, labels, `containerEnv` and
`remoteUser` come from the new container's config, as for any create. The
devcontainer CLI may still build its thin UID-adjustment layer on Linux. That
is acceptable, since it is fast and is not the build being skipped.

**Config must match.** The image is only correct if the new container's config
would have produced it:

- *Generated source:* `--tools` with `--copy image` is exit 2, whatever its form,
  because the tools are baked into the image. The target cannot have its own
  `.devcontainer`, but its document is rendered with the current catalog, which
  may differ from what SOURCE's image was built from (a dev upgrade). So SOURCE's stored `GeneratedConfig` and the new
  container's rendered one are digested (`guard.DigestOf`) and compared on the
  same image-deciding entries (`guard.ImageDiff`). A mismatch is exit 2 with the
  same advice to drop `--copy image` and build. This cannot see a failed
  `rebuild --tools`, which rewrites SOURCE's row before building, so both
  documents list the new tools while SOURCE's image has the old ones; the
  Feature check below covers that.
- *Features in the image* (both kinds of source): the Feature ids in SOURCE's
  image's `devcontainer.metadata` label (`Copier.ImageFeatureIDs`, on the source
  image ID) must include every key of the target configuration's `features`
  (generated: the rendered document; project-owned: the target's merged config).
  Local `./` and `../` keys are skipped, since the label may record their ids
  differently. Any missing is exit 2 naming them, with the same advice. Extra
  Features in the image are not refused.
- *Project-owned source:* compare only what decides the image. The full
  `ConfigDigest` cannot be compared, because it covers the merged config **with**
  dev's per-container overlays: the state volume name and the worktree binds
  differ between any two containers, so it would never match.
  - SOURCE side: the per-field hashes in its stored `ConfigDigestFields` record
    the configuration SOURCE was actually built from. That stays true even if
    its project files were edited since, which is the right reference.
  - Target side: the new folder's config is digested the same way
    (`guard.DigestOf` on the merged config, Dockerfile and compose files).
  - Compared: only the image-deciding entries, i.e. the fields `image`,
    `build`, `dockerFile`, `context` and `features` (as they appear in the
    merged config; the builder confirms the spellings against
    `read-configuration`), plus the Dockerfile's file hash. A missing entry on
    one side and present on the other counts as a difference.
  - A mismatch is exit 2. The error names the differing entries and tells the
    operator to drop `--copy image` and build.
  - If SOURCE has no recorded digest (it was never started, or predates the
    check), the command is exit 2 with the same advice. No migration and no new
    stored data are needed.

**Refused cases** (all exit 2 unless noted):

- SOURCE has no container in the engine (created with `--no-start`, or removed
  out from under dev). The error tells the operator to start it once, or to
  drop `--copy image`.
- SOURCE's image no longer exists locally.
- A source or target config that uses `dockerComposeFile`. Compose has one
  image per service, and dev does not own that mapping.

## `--copy state`

**Meaning.** Before the new container first starts, its state volume is created
and filled with a copy of SOURCE's state volume. Ownership and modes are
preserved: the copy is root-run and archive-style. Everything comes along,
including logins (tokens are duplicated, so revoking one copy does not revoke
the other), Claude history and memory under their original paths, plugins, MCP
config, `gitconfig` (including `safe.directory` entries for SOURCE's paths) and
`dev-team` run folders. Nothing is filtered or rewritten. The operator chose
this.

**Running source.** This is allowed. If SOURCE is running, the copy is a
snapshot of a moving target, and a file being written at that moment may come
out torn. When SOURCE is running, `create` prints one line on stderr saying so
and suggesting `dev container stop SOURCE` for a clean copy. dev never stops
the source itself.

**How.** A short-lived helper container mounts SOURCE's state volume read-only
and the new volume read-write, and copies. It must:

- Use an image already present locally, never a pull. SOURCE's image, which the
  previous step confirmed exists, is the natural choice.
- Work under both docker and podman (podman machine on macOS included), through
  the same engine binary the local provider already uses.

The rest is left to the builder.

**Refused cases** (exit 2):

- SOURCE or the new container has `PersistState` off. Passing
  `--no-persist-state` together with `--copy state` is a contradiction, so it is
  exit 2.
- SOURCE's state volume does not exist.
- A source or target config that uses `dockerComposeFile`. The state volume is
  unreliable under compose (see USAGE.md, persistent state).

## Order and rollback

Every validation step runs before anything is created, in roughly this order:

1. The existing name, folder and `--from` checks.
2. The `--copy` parse, after the source lookup, so a missing source is exit 3
   whatever `--copy` says. `--copy` without `--from` is exit 2 at once.
3. Mode checks.
4. Persist-state and compose checks.
5. Engine lookups: SOURCE's container, image and state volume.
6. The target check: first that no container row of that name exists (the usual
   "already exists" error), then that the new container's state volume and
   snapshot tag do not. A leftover is exit 2, naming `docker volume rm` and
   `docker rmi`.
7. The config match for `image`.

Then:

1. Write the row (as today).
2. If `image`: create the snapshot.
3. If `state`: create the new state volume and copy into it.
4. Start the container (unless `--no-start`). With `image`, start from the
   snapshot.

If step 2, 3 or 4 fails, everything created so far is removed, under a
context that outlives Ctrl-C or SIGTERM and with a timeout: first the
container and its volumes through the provider's remove, then the snapshot image
and the new state volume, then the row. When the copy fails because the state
volume appeared after the target check, that volume is not this run's: rollback
removes the row and the snapshot tag and leaves the volume. Removal goes first because the engine
refuses a volume a container still uses. If the container cannot be removed, the
row is kept and the error tells the operator to run `dev container remove NAME`.
With `--no-start`, the snapshot and the filled volume remain until the first
`start`, which uses them.

`dev container remove` deletes the snapshot image along with the volumes.
Removing SOURCE never affects the copy.

## Recording

The audit entry for `create` records `--copy` alongside `--from`. The row keeps
no link to SOURCE and there is no migration, as before. Whether the row needs
anything at all to know a snapshot tag exists, or whether `start` and `remove`
can derive the tag name from the container's name and check whether it exists,
is left to the builder. The second is preferred.

## Errors

Every refusal is exit 2 unless the existing code says otherwise. A missing
SOURCE row stays exit 3. Each message names the flag and the reason, and where
one exists, the way forward ("drop `--copy image`", "stop SOURCE first",
"isolated containers cannot share an image or state").

## Testing

- **Unit tests** use the existing stub-executable harness (stub `docker` and
  `devcontainer` on a temporary `PATH`). They cover:
  - every refusal in this document;
  - the snapshot tag and volume copy invocations;
  - rollback on a failed copy;
  - the snapshot image kept after the first start, and deleted by `remove`;
  - the first-start config rewrite, and its absence on later starts;
  - the project-owned config match ignoring dev's overlays (two worktrees of
    one repo match, an edited Dockerfile does not);
  - plain `--from` unchanged;
  - project-owned `--from` resolving the target folder's own config.
- **Smoke:** create a generated container and write a marker file into its
  `/var/dev-state`. Create another with `--from X --copy image,state`, then
  check three things: no build step ran (assert the image ID matches the
  source's, not timing), the marker is present, and a `rebuild` of the source
  leaves the copy's image ID unchanged.

## Documentation

- `docs/USAGE.md`:
  - `--copy` in the `container create` reference.
  - The "Start another container like an existing one" walkthrough rewritten
    around the worktree flow above.
  - What `image` and `state` each carry.
  - The isolated/k8s refusal.
  - The torn-copy caveat.
  - The duplicated-token note.
  - The `--from` project-owned source change.
- `docs/SECURITY.md`: one paragraph on why `--copy` is refused for isolated
  containers.
- `docs/specs/2026-10-08-create-from-design.md` is left as the historical
  record. This spec supersedes its "Only a generated source", "No shared image
  tag" and "Out of scope" items.

## Calls made during design, not by the operator

- Using the target folder's own `.devcontainer` for a project-owned source.
  This was suggested and the operator accepted it.
- Refusing `--copy image` when configs differ, rather than warning.
- Refusing `--copy` for compose projects.
- Allowing a running source for `--copy state`, with a warning.
- The snapshot being the new container's own image until remove or rebuild, with only SOURCE's own config entry filtered out.
- The first-start config rewrite (image swapped, build fields dropped), never
  stored.
- The project-owned config match comparing only image-deciding entries from
  SOURCE's stored per-field record.
- `--copy` also requiring the local provider, as a backstop for pre-mode k8s
  rows.
- Treating legacy and `--no-folder` containers as allowed for `--copy`.
- Leaving `worktree create --from` unbuilt.
