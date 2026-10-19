# Unprotected Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an `unprotected` container mode (folder live, `.git` writable, relay allowed) and let `dev container rebuild --mode` move a container guarded ↔ unprotected, or from either into isolated.

**Architecture:** The mode stays a free-text column; a new store method rewrites the mode-derived columns together after a successful rebuild. `rebuild` builds the target container in memory for the new mode (mode, workspace path, worktree repo, folder placement, generated document), runs the existing checks against it, rebuilds, then persists. A switch into isolated is the one recreate that seeds, signalled explicitly through a new `provider.RebuildOpts`.

**Tech Stack:** Go, cobra, SQLite, devcontainer CLI and docker via stub executables in tests.

**Spec:** `docs/specs/2026-10-19-unprotected-mode-design.md`

## Global Constraints

- Mode value: `unprotected` (`model.ModeUnprotected`). Local provider only; k8s refuses `guarded` and `unprotected` with exit 2.
- Allowed changes: guarded→unprotected, guarded→isolated, unprotected→guarded, unprotected→isolated. Out of isolated, and any change on a `legacy` or `none` container: exit 2, nothing touched.
- `--mode` equal to the current mode changes nothing about the mode; the rebuild goes ahead, and never seeds.
- The mode and its derived columns are written only after a successful rebuild. The one exception: a switch into isolated whose seed succeeded and whose lifecycle hooks then failed is written as isolated.
- No check of `.git` on unprotected→guarded. No confirmation prompt.
- Audit: the `rebuild` record carries `mode_from` and `mode_to` when the mode changed.
- Exit codes per invariant 7: usage/refusal 2, failure 1.
- No migration: `mode` is already `TEXT`.
- Conventional Commit subjects, no tooling-attribution trailer. `make lint && make test` green after every task.
- Comments: why, not what (AGENTS.md).

## Review Focus

1. **A worktree container switched into isolated must lose its host bind mounts.** `overlayProjectConfig` and `rewriteGeneratedTools` add the worktree's binds whenever `WorktreeRepo != ""`; the switch must clear it, or the "isolated" container still reaches the host's repository. Test in Task 4.
2. **An unprotected container must not fall into the legacy guard.** `planGitDir` sends any non-guarded container with `GitGuard` set to `planLegacyGitDir`; `create` sets `GitGuard` for unprotected, so without an explicit branch it gets single-file mounts and a hooks copy. Test in Task 2.
3. **`rebuild --mode isolated` on an isolated container must never seed.** Seeding unpacks over the agent's work. Test in Task 4.
4. **A failed rebuild leaves the row exactly as it was**: mode, workspace path, worktree repo, folders and generated document. Test in Task 4.
5. **An unprotected container's worktree extra folder has its common directory writable**, or commits in that folder fail with `Read-only file system` while the primary works. Test in Task 2.

---

### Task 1: The mode value and the store write

**Files:**
- Modify: `internal/model/model.go:54-66,89-90` (constant; drop "Fixed at create" from the `Mode` type and field comments)
- Modify: `internal/store/container.go` (new method after `SetConfigDigest`)
- Test: `internal/store/store_test.go`

**Interfaces:**
- Produces: `model.ModeUnprotected Mode = "unprotected"`, doc comment "mounts the folder live with its .git writable".
- Produces: `func (s *Store) SetContainerMode(c model.Container) error` — in one transaction, for `(c.WorkspaceName, c.Name)`, writes `mode`, `workspace_path`, `worktree_repo`, `generated_config`, and replaces the `container_folders` rows with `c.Folders` (reuse `insertFolders`). `ErrNotFound` when no row.

- [ ] **Step 1: Write the failing tests**

```go
func TestSetContainerModeRewritesTheModeColumns(t *testing.T)
// create a guarded container with WorktreeRepo "/r", one folder {HostPath:"/a", ContainerPath:"/workspaces/a", WorktreeRepo:"/ra"},
// GeneratedConfig "old"; SetContainerMode with Mode isolated, WorkspacePath "/workspaces/x", WorktreeRepo "",
// Folders [{"/a","/workspaces/a",""}], GeneratedConfig "new"; GetContainer: each field equals what was written.

func TestSetContainerModeOnAMissingContainer(t *testing.T)
// errors.Is(err, ErrNotFound)
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/store -run SetContainerMode`
Expected: FAIL, `s.SetContainerMode undefined`.

- [ ] **Step 3: Add the constant and `SetContainerMode`**

Mirror `SetContainerFolders`' transaction shape: existence check, `UPDATE`, delete and re-insert folders, commit.

- [ ] **Step 4: Run them to see them pass**

Run: `go test ./internal/store ./internal/model`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/model/model.go internal/store/container.go internal/store/store_test.go
git commit -m "feat(store): record a container's change of mode"
```

---

### Task 2: Creating an unprotected container

**Files:**
- Modify: `internal/cli/container.go` (`modeFor` :348-376, `allowExtraFolders` :425-436, create flag help :123-124, a warning after create)
- Modify: `internal/cli/gitguard.go:132-151` (`planGitDir`)
- Modify: `internal/cli/folders.go` (`folderMounts` :92-112, `requireFolders` :143-156)
- Test: `internal/cli/mode_test.go`, `internal/cli/sshagent_test.go`

**Interfaces:**
- Consumes: `model.ModeUnprotected` (Task 1).
- Produces: `func warnUnprotected(a *app, name string)` in `internal/cli/container.go`, printing through `warnf`:
  `"container %s is unprotected: it can change what git on your machine runs; dev container rebuild %s --mode guarded closes it again"`.
  Task 4 calls it after a rebuild into unprotected.

Behaviour, from the spec section 1:
- `modeFor`: `unprotected` accepted on local, exit 2 on k8s with the same shape of message as guarded's; the unknown-mode message lists `guarded, unprotected or isolated`.
- `allowExtraFolders`: unprotected takes extra folders.
- `planGitDir`: an unprotected container returns an empty plan and no scopes, before the `GitGuard` check (Review Focus 2).
- `folderMounts`: an unprotected folder is a plain writable bind at `f.ContainerPath`; a worktree folder adds a writable bind of `f.WorktreeRepo` at its own path (`source=R,target=R,type=bind`).
- `requireFolders`: applies to unprotected as to guarded (a bind of a gone folder fails in the engine).
- `create` with unprotected skips `checkGuarded` and `warnGuardGaps`, and calls `warnUnprotected` once the row is written.
- `GitGuard` at create is unchanged: unprotected gets it as guarded would, so a later switch to guarded finds the primary in `guardedRepos`.
- The relay needs no change: `agentForwarded` and `forwardAgent` test only for guarded. The test pins that.

- [ ] **Step 1: Write the failing tests**

```go
func TestCreateUnprotectedStoresTheMode(t *testing.T)
// local workspace, createOpts{noStart: true, mode: model.ModeUnprotected}: containerRow(...).Mode == ModeUnprotected

func TestK8sRefusesUnprotected(t *testing.T)
// as TestK8sDefaultsToIsolatedAndRefusesGuarded: exitCodeOf(err) == exitUsage

func TestUnprotectedMountsNothingOverGit(t *testing.T)
// gitRepoWithConfig(`{"image":"ubuntu"}`), create unprotected with stubCapturingDevcontainer:
// !hasMountAt(mounts, "/workspaces/<base>/.git"), !hasMountAt(..."/.git/config"), hooksCopyDir("ws","api") empty or missing.

func TestUnprotectedProjectThatMovesTheWorkspaceIsAccepted(t *testing.T)
// the fixture of TestGuardedRefusesAProjectThatMovesTheWorkspace, with mode unprotected: err == nil

func TestUnprotectedWorktreeFolderIsWritable(t *testing.T)   // Review Focus 5
// primary repo plus an extra folder that is a `git worktree add` checkout, mode unprotected, generated:
// hasMount(mounts, extra.WorktreeRepo, false) and no read-only mount at that target.

func TestUnprotectedCreateWarns(t *testing.T)
// capture stderr: contains "is unprotected" and "--mode guarded"

func TestListShowsUnprotected(t *testing.T)
// as TestListShowsTheMode: the MODE column reads "unprotected"

func TestFetchRefusesUnprotected(t *testing.T)
// exit 2; requireIsolated already refuses it, the test pins it
```

In `sshagent_test.go`:

```go
func TestSSHAgentIsForwardedIntoAnUnprotectedContainer(t *testing.T)
// as the existing isolated forwarding test, with an unprotected row: no usage error, SSH_AUTH_SOCK set.
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/cli -run 'Unprotected'`
Expected: FAIL, `unknown mode "unprotected"` (exit 2) on create.

- [ ] **Step 3: Implement the behaviour listed above**

- [ ] **Step 4: Run the package**

Run: `go test ./internal/cli`
Expected: PASS, including every existing guarded and isolated test.

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): an unprotected mode, the folder live with .git writable"
```

---

### Task 3: A rebuild that seeds, in the providers

**Files:**
- Modify: `internal/provider/provider.go:35-38`
- Modify: `internal/provider/local/local.go:47-118`
- Modify: `internal/provider/k8s/k8s.go:56-62`
- Modify: callers `internal/cli/container.go:984`, `internal/cli/agent.go:187`
- Test: `internal/provider/local/local_test.go`, `internal/provider/k8s/*_test.go` (whichever holds the Rebuild test)

**Interfaces:**
- Produces, in `internal/provider`:

```go
// RebuildOpts are the choices a rebuild takes beyond the container itself.
type RebuildOpts struct {
	NoCache bool
	// Seed copies the folders into fresh workspace volumes, as a first create
	// does. Only a switch into isolated sets it.
	Seed bool
}

// LifecycleError is a lifecycle hook failing after a seeded rebuild: the
// volumes now hold the container's copy, so the mode has changed.
type LifecycleError struct{ Err error }
```

  with `Error()` and `Unwrap()`.
- `Rebuild(ctx context.Context, c model.Container, env []EnvVar, opts RebuildOpts) error` replaces the `noCache bool` parameter on the interface and both providers.
- Local, when `opts.Seed`:
  1. Before `up`: refuse (plain error, exit 1) if `docker volume inspect` finds `VolumeName(c.WorkspaceName, c.Name)` or any `FolderVolumeName(...)` for `c.Folders`, naming the volume.
  2. `up --remove-existing-container --skip-post-create`, then `seedWorkspace`.
  3. If `up` or the seed fails: `docker rm --force` the container (by `containerID`), `docker volume rm --force` the workspace and folder volumes, never the state volume; return the original error.
  4. If `runUserCommands` fails: return `&provider.LifecycleError{Err: err}`.
  `seeding` in `up` becomes: `opts.Seed`, or the existing first-create test when not recreating. A recreate without `Seed` never seeds (Review Focus 3).
- k8s: `opts.Seed` returns an error (`"a k8s container is isolated from create; nothing to seed"`); it cannot occur because the CLI refuses the switch there.

- [ ] **Step 1: Write the failing tests**

```go
func TestRebuildWithSeedSeedsBeforeTheLifecycleHooks(t *testing.T)
// isolatedContainer, docker stub answering "" to volume inspect: verbs == up exec exec exec run-user-commands,
// calls[0] has "--remove-existing-container" and "--skip-post-create".

func TestRebuildWithoutSeedNeverSeeds(t *testing.T)
// isolatedContainer, docker stub with no container: Rebuild(..., RebuildOpts{}) makes only an "up" call, no "--skip-post-create".

func TestRebuildWithSeedRefusesAnExistingVolume(t *testing.T)
// docker stub answering volume inspect with success: error names VolumeName("ws","demo"); no devcontainer call.

func TestRebuildWithSeedCleansUpAFailedSeed(t *testing.T)
// devcontainer stub failing on the "tar" exec: error returned; docker calls include "rm --force <id>" and
// "volume rm --force dev-ws-demo"; none names StateVolumeName.

func TestRebuildWithSeedReportsAFailedHookAsALifecycleError(t *testing.T)
// devcontainer stub failing on run-user-commands: errors.As(err, *provider.LifecycleError); no docker rm.
```

Update `TestRebuildAsksForRecreate` and `TestRebuildWithoutNoCache` to the new signature.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/provider/...`
Expected: FAIL to compile, `undefined: provider.RebuildOpts`.

- [ ] **Step 3: Implement as specified above; update the two CLI callers to `provider.RebuildOpts{NoCache: noCache}` and `provider.RebuildOpts{}`**

- [ ] **Step 4: Run everything**

Run: `make lint && make test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/provider internal/cli/container.go internal/cli/agent.go
git commit -m "feat(provider): a rebuild that seeds a fresh isolated workspace"
```

---

### Task 4: `rebuild --mode`

**Files:**
- Create: `internal/cli/modechange.go` (the validation and the retarget)
- Modify: `internal/cli/container.go` (`newContainerRebuildCmd` :907-1040, `retargetFolders` :1045-1072 folded into the new retarget)
- Modify: `internal/cli/folders.go:168-176` (`changeFolders` accepts unprotected)
- Modify: `internal/cli/generate.go:221-275` (extract the render so rebuild can re-render in memory)
- Test: `internal/cli/modechange_test.go`

**Interfaces:**
- Consumes: `Store.SetContainerMode` (Task 1); `warnUnprotected` (Task 2); `provider.RebuildOpts`, `provider.LifecycleError` (Task 3).
- Produces:

```go
// checkModeChange refuses a change of mode rebuild cannot make. from == to is allowed.
func checkModeChange(name string, from, to model.Mode, kind model.ProviderKind) error

// withMode returns c as it would be in mode to: Mode, WorkspacePath
// (isolatedPath(c.Source) for isolated, "" otherwise), WorktreeRepo ("" for
// isolated, kept otherwise), every folder placed again with placeFolder, and a
// generated document re-rendered for the new mount. c itself is untouched.
func withMode(ctx context.Context, c model.Container, to model.Mode) (model.Container, error)

// renderGenerated re-renders c's generated document with tools, deriving the
// mount and state from c's columns.
func renderGenerated(c model.Container, tools []string) (string, error)
```

  `renderGenerated` is the body of `rewriteGeneratedTools` from "Derived from the rows" to `dcgen.Render`; `rewriteGeneratedTools` calls it. `withMode` calls it with `dcgen.ToolsOf(c.GeneratedConfig)` when `GeneratedConfig != ""`.
- `checkModeChange` messages (exit 2, via `usageErrorf`):
  - out of isolated: `"container %s is isolated; it cannot leave isolated mode. Bring its work home with dev container fetch %s, then create a new container"`
  - legacy or none: `"container %s is %s; only a guarded or unprotected container can change mode"` (`displayMode`)
  - guarded/unprotected on k8s: the same messages `modeFor` gives.
  - unknown mode: the same message `modeFor` gives.

Rebuild flow with `--mode` given and different from the row's mode, from the spec section 2:
1. `checkModeChange`.
2. Retarget (one function for `--mode` and the folder flags): load the row, apply `withMode`, then `changeFolders`; into guarded run `checkGuarded(a, c.Source, c.ConfigPath, c.WorktreeRepo, len(c.Folders) > 0)` and `warnGuardGaps` per folder; `checkFolders`; build the target with `a.target`.
3. The existing drift and escape checks, unchanged.
4. `Rebuild` with `RebuildOpts{NoCache: noCache, Seed: to == isolated}`.
5. On success, or on `*provider.LifecycleError` when seeding: `SetContainerMode(c)`. Then audit `rebuild` with `mode_from`, `mode_to` added to the existing fields. Into unprotected: `warnUnprotected`. A `LifecycleError` is returned after the row is written and the record made.
6. `SetContainerFolders` is no longer called separately when a mode change happened: `SetContainerMode` carries the folders.

Without a mode change, the existing path is unchanged except that `--add-folder`/`--remove-folder` accept an unprotected container.

- [ ] **Step 1: Write the failing tests**

```go
func TestCheckModeChange(t *testing.T)
// table over every (from, to) in {guarded, unprotected, isolated, legacy, none} × {guarded, unprotected, isolated}
// on local: nil exactly for the four allowed edges and from == to on guarded/unprotected/isolated; exit 2 otherwise.
// On k8s: isolated→isolated nil; ...→guarded and ...→unprotected exit 2.

func TestRebuildGuardedToUnprotected(t *testing.T)
// create guarded (gitRepoWithConfig), `container rebuild api --mode unprotected` with stubCapturingDevcontainer:
// no mount at /workspaces/<base>/.git; row Mode unprotected; auditEntries has a rebuild with mode_from guarded, mode_to unprotected.

func TestRebuildUnprotectedToGuarded(t *testing.T)
// the reverse: read-only .git mount present; row Mode guarded.

func TestRebuildIntoGuardedRefusesAMovedWorkspace(t *testing.T)
// unprotected container on the moved-workspace fixture: exit 2; row still unprotected; no devcontainer up call.

func TestRebuildRefusesLeavingIsolated(t *testing.T)
// isolated container, --mode guarded: exit 2, message contains "dev container fetch"; row unchanged.

func TestRebuildIsolatedToIsolatedNeverSeeds(t *testing.T)   // Review Focus 3
// isolated container, --mode isolated: the up call has no "--skip-post-create".

func TestRebuildGuardedToIsolatedSeeds(t *testing.T)
// generated guarded container: the up call has "--skip-post-create" and "--remove-existing-container";
// row Mode isolated, WorkspacePath "/workspaces/<base>"; stored GeneratedConfig names a volume workspaceMount.

func TestRebuildWorktreeToIsolatedDropsTheHostBinds(t *testing.T)   // Review Focus 1
// guarded container on a worktree checkout, --mode isolated: no mount whose source is the worktree or its
// common directory; row WorktreeRepo "".

func TestFailedModeRebuildLeavesTheRow(t *testing.T)   // Review Focus 4
// generated guarded container; stubDevcontainer(t, "--remove-existing-container"); --mode unprotected fails;
// containerRow equals the row before the call in Mode, WorkspacePath, WorktreeRepo, Folders, GeneratedConfig.

func TestLifecycleFailureIntoIsolatedStillRecordsTheMode(t *testing.T)
// devcontainer stub failing on run-user-commands: rebuild returns an error; row Mode isolated.

func TestRebuildIntoUnprotectedWarns(t *testing.T)
// stderr contains "is unprotected".

func TestRebuildAddsAFolderToAnUnprotectedContainer(t *testing.T)
// as TestRebuildAddsAndRemovesFolders on an unprotected container: succeeds; the added folder's .git has no read-only mount.
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/cli -run 'ModeChange|RebuildGuarded|RebuildUnprotected|RebuildInto|RebuildRefusesLeaving|RebuildIsolated|RebuildWorktree|FailedModeRebuild|LifecycleFailure|RebuildAddsAFolderToAnUnprotected'`
Expected: FAIL, `unknown flag: --mode` and `undefined: checkModeChange`.

- [ ] **Step 3: Implement as specified above; add `--mode` to rebuild with the help text `"change the container's mode: guarded, unprotected, or isolated (one way)"`, and extend the command's `Long` with the allowed changes**

- [ ] **Step 4: Run everything**

Run: `make lint && make test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): rebuild --mode changes a container's mode"
```

---

### Task 5: Documentation

**Files:**
- Modify: `docs/USAGE.md` (flag table :1168 and :1178-1181; guarded section :491-524; isolated :580-606; rebuild's reference; "What a container can do to your git")
- Modify: `docs/SECURITY.md` (:9-11 "It cannot be changed later"; the modes table :44-52; a new "Unprotected" section after "Isolated"; "Your ssh agent" :119 covers unprotected)
- Modify: `docs/ARCHITECTURE.md` invariant 12 (:253-272)
- Modify: `AGENTS.md` "Git in the dev container"

Content, from the spec's "What changes elsewhere":
- USAGE: the third mode; `rebuild --mode` with the table of allowed changes; the workflow (open a guarded container up for a stack of PRs, close it again); `MODE` shows `unprotected`; switching into isolated copies the folder as it is then, uncommitted changes included.
- SECURITY: an unprotected column that says plainly it gives up the protection ("Agent can change what your git runs: yes"); switching back to guarded protects from then on and undoes nothing the container wrote; k8s has no unprotected.
- ARCHITECTURE invariant 12: a mode is guarded, unprotected or isolated; it changes only through `rebuild --mode`, only along the allowed edges, and is written only after a successful rebuild (invariant 10's reason, kept); a guarded `.git` is still one read-only directory mount.
- AGENTS.md: "If `git commit` fails with `Read-only file system`, the container is guarded … Otherwise commit on a branch, and push if asked. If the container is isolated, the operator brings the work home with `dev container fetch`."
- The 2026-10-12 modes spec is left as written.

- [ ] **Step 1: Make the edits**

- [ ] **Step 2: Check the command surface matches**

Run: `make build && DEV_STATE=$(mktemp -d) dist/dev container rebuild --help && DEV_STATE=$(mktemp -d) dist/dev container create --help`
Expected: `--mode` on both, help text matching what USAGE.md says.

- [ ] **Step 3: Commit**

```bash
git add docs AGENTS.md
git commit -m "docs: the unprotected mode and changing a container's mode"
```

---

### Task 6: Smoke

**Files:**
- Modify: `test/smoke/hardening_test.go` (new tests beside `TestSmokeGitGuard` and `TestSmokeIsolated`, reusing their fixtures and `buildBinary`)

- [ ] **Step 1: Write the smoke tests**

```go
func TestSmokeUnprotectedRoundTrip(t *testing.T)
// create guarded; `dev container exec NAME -- git commit --allow-empty -m x` fails with "Read-only file system";
// rebuild --mode unprotected; the same commit succeeds and its subject is in host `git log -1`;
// rebuild --mode guarded; the commit fails again, and writing .git/CONFIG and .git/hooks/x inside fails.

func TestSmokeGuardedToIsolated(t *testing.T)
// guarded container on a repo with an uncommitted change and an untracked file; rebuild --mode isolated;
// both are in the container; commit inside; `dev container fetch NAME` brings the branch home;
// rebuild --mode guarded exits 2 and the container still runs.
```

- [ ] **Step 2: Run them where an engine exists**

Run: `make smoke` (or push and read the CI smoke job's logs)
Expected: PASS; skip without an engine.

- [ ] **Step 3: Commit**

```bash
git add test/smoke
git commit -m "test(smoke): switching between guarded, unprotected and isolated"
```
