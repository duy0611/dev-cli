# A devcontainer.json outside the project

Status: implemented. The two open questions below concern the devcontainer
CLI's own behaviour and are covered by `TestSmokeExternalConfig`.

## Problem

Some projects do not ship a `devcontainer.json`, and their owners do not want
one committed: a repository that is not theirs, or a team that has not adopted
devcontainers. The operator keeps the file in a sibling directory instead
(`~/work/api-devcontainer/devcontainer.json` beside `~/work/api`). `dev` cannot
use it. `folderSource` calls `dcconfig.Find(source)`, which looks only at
`<folder>/.devcontainer/devcontainer.json` and `<folder>/.devcontainer.json`.
The only config-free route is `--generate`, which offers the tool catalog and
nothing else: no custom image, Dockerfile, Features options or lifecycle hooks.

## Goal

`dev container create NAME --folder PROJECT --config PATH` creates a container
whose project is `PROJECT` and whose devcontainer configuration is the file at
`PATH`, wherever that is.

Success: with `PATH` outside `PROJECT`, `create` succeeds, `dev container
config show` prints that file, and `rebuild` re-reads it.

## Decisions

- **No schema change.** `model.Container.ConfigPath` is already an absolute path
  that every consumer passes as `--config` beside `--workspace-folder
  c.Source`: both providers' `up`, `run-user-commands` and `read-configuration`
  (`local.go`, `config.go`; k8s `readConfiguration`), `overlayProjectConfig`
  (which keeps `--config` so the CLI anchors a relative `dockerfile`), the git
  guard's reads and `checkGuarded`. Nothing assumes it sits under `Source`.
  Only `create` cannot produce such a row.
- **A flag, not a setting.** The path is per container and stored on the row, so
  a later `rebuild` needs no flag.
- **The explicit flag wins.** A project's own config is shadowed, with a
  warning that names the shadowed file. Refusing instead would make the flag
  useless for the repository that half-adopted devcontainers.
- **The file keeps its CLI name.** The devcontainer CLI reads the layout around
  the file and rejects other names (`materialise` writes generated documents as
  `.devcontainer/devcontainer.json` for this reason), so only `devcontainer.json`
  and `.devcontainer.json` are accepted.

## Behaviour

```
dev container create NAME --folder PROJECT --config PATH
```

`createOpts` gains `configFile string`. `runContainerCreate` resolves it with
`xpath.ResolveFile` (absolute, symlinks followed, regular file required), as
`--agent-config` does, and passes it to `folderSource`. There, a non-empty
`configFile` replaces the `dcconfig.Find` call and its result becomes
`ConfigPath`. The remaining branches (compose warning, copy planning,
`checkGuarded`) already take `configPath` and need no change.

### What a project's own file does

If `dcconfig.Find(source)` also succeeds, `create` warns `--config PATH is used;
<found> in PROJECT is ignored` and carries on. Nothing is written to the project.

### What does not move

The project's `.devcontainer/agents.yaml` is still looked up under `Source` by
`agentConfigFile`; `--agent-config` remains the way to point elsewhere.

### `--from`

A project-owned source has its `ConfigPath` read by `applyFrom` only as a
marker (`fromProjectOwned`); the new container otherwise finds its own folder's
config. That is wrong when the source's `ConfigPath` is not where
`dcconfig.Find(source)` looks: the copy would find nothing, or the wrong file,
in its folder. Rule: when `--config` is absent and the source's `ConfigPath`
differs from what `Find` returns for its `Source`, the new container inherits
it. A path outside the source's folder is kept as is; one inside it (say
`.devcontainer/python/devcontainer.json`) is rebased onto the new folder, and a
missing file there is exit 3. `--config` overrides either. An inherited external
config shadows the new folder's own `.devcontainer` with the usual warning. The
`--copy image` compose check already reads both containers' paths and is
unchanged.

### Errors

All run before anything is written.

| Case | Exit | Message |
|---|---|---|
| `--config` with `--generate`, `--no-folder` or `--tools` | 2 | `--config supplies the configuration; --generate, --no-folder and --tools have nothing to do` |
| File missing or not a regular file | 3 | The `ResolveFile` error, naming the path |
| Base name not `devcontainer.json` or `.devcontainer.json` | 2 | `--config must name a devcontainer.json or .devcontainer.json file` |

`--tools` with `--from` stays valid only where `--from` supplies a generated
config; with `--config` it is refused as above.

## Guarded mode

The config lives outside the mount, so the agent cannot edit it. An in-project
`.devcontainer/devcontainer.json` is writable unless guarded mode covers it;
this file is out of reach by construction. The git guard still applies to
`PROJECT/.git`: `checkGuarded` runs on `source`, which stays the project.

## Alternatives rejected

- **The sibling directory as the primary folder, the project as an extra
  `--folder`.** `--folder` makes the first the primary, whose configuration the
  container is built from, so the config is found, but the working directory is
  the sibling, not the project, and the k8s provider supports one folder only.
- **`workspaceMount` in the file pointing at `../project`.** `Source` would be
  the sibling directory, so `isGitCheckout(source)` is false and `checkGuarded`
  returns early: the project's `.git` is mounted writable and unguarded.

## Open questions

Both are answered on a real engine by `TestSmokeExternalConfig`
(`test/smoke/externalconfig_test.go`); `USAGE.md` states the answers.

1. With `--config` outside `--workspace-folder`, what anchors a relative
   `build.dockerfile` and `build.context`: the config's directory (the spec's
   answer) or the workspace folder? `build.context` normally resolves against
   the config's directory; confirm the CLI does so here too.
2. What `${localWorkspaceFolder}` expands to: expected `--workspace-folder`,
   hence the project. A file that relies on it must keep working.

If either differs, `USAGE.md` documents it and the flag still ships.

## Kubernetes

The k8s provider is experimental. It reads the config through
`readConfiguration(ctx, c.Source, c.ConfigPath)`, so the same row works, but
`buildAndPush` runs the host build with that path and inherits whatever the
answers above are. Nothing k8s-specific is added.

## Testing

Unit tests on the stub-executable harness (stub `devcontainer` on a temporary
`PATH`; no engine, no network) in `internal/cli/container_test.go`:

- `TestCreateWithConfigOutsideTheProject`: `ConfigPath` is the resolved file,
  `Source` the project, and the stub's `up` receives `--config` and
  `--workspace-folder` as stored.
- `TestCreateConfigShadowsTheProjectFileWithAWarning`: warning names the file.
- `TestCreateConfigRefusals`: `--generate`, `--no-folder`, `--tools` (exit 2),
  a wrong base name (exit 2), a missing file (exit 3); no row written.
- `TestCreateFromInheritsAnExternalConfig`, and `--config` overriding it.
- `TestCreateConfigGuardedStillChecksTheProjectGit`.

`test/smoke/smoke_test.go`: a project with no config and a sibling file using a
relative `build.dockerfile`; `create --config`, then `exec` to prove the
build used the sibling's Dockerfile. This settles open question 1 on a real
engine.

## Documentation

`docs/USAGE.md`: `--config PATH` under `container create`, and a walkthrough,
"Use a devcontainer.json kept outside the project", including the guarded-mode
benefit. The `Long` help gains a paragraph.

## Out of scope

- Changing a stored container's `ConfigPath` after create.
- Searching parent or sibling directories for a config automatically.
- `worktree create --config`.
