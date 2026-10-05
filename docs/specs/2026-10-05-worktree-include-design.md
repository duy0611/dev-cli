# Carrying ignored files into a new worktree

## Problem

`dev worktree create` runs `git worktree add`, so the new checkout holds only
tracked files. Everything a working tree depends on that git ignores —
`node_modules/`, `.venv/`, `.env`, local config — is missing, and the operator
(or the agent) reinstalls dependencies from cold and recreates env files before
the worktree is usable. That reinstall is the slow part of starting work on a
branch.

## Goal

A new worktree starts with the ignored files the operator chose, copied from
the checkout `create` was run from, so the install that follows — run by hand
or by the agent, inside the container, after it is up — fetches only what
changed. A stale or wrong-platform library (a darwin build in a Linux
container) is acceptable: re-running install repairs it quickly.

Success: a project with a `.worktreeinclude` gets a worktree whose `.env` is in
place and whose `node_modules/` is already there, with no change to how
`create` and `remove` otherwise behave.

## Decisions

- **Copy into a fresh checkout; no pool.** Treehouse
  (github.com/kunchenguid/treehouse) solves the same problem by keeping a pool
  of worktrees and resetting them for reuse. That keeps dependencies warm with
  no copy, but the first worktree is still cold, `.env` files still need
  seeding on top, and `remove` would have to become "return to pool" with
  acquire, reset, lease and prune logic, pool paths `dev` chooses, and
  untracked residue from the previous task. Since a re-install is cheap here,
  the goal is "install is quick", not "install is unnecessary", and copying
  meets it without changing what a worktree is.
- **Manifest file plus an additive flag.** `.worktreeinclude` at the root of
  the source checkout, in gitignore syntax — the same name and format treehouse
  uses — plus `--include-file PATH`, whose patterns are added on top.
- **Source is the checkout `create` runs from**: the one holding the working
  directory, or the one `--repo` names.
- **No setup hook.** The operator or the agent runs the install after the
  container is up.

## Command surface

```
dev worktree create NAME --branch B --path P [--include-file PATH] ...
```

| `.worktreeinclude` | `--include-file` | Patterns used |
|---|---|---|
| absent | absent | none — nothing copied, behaviour identical to today |
| present | absent | the repository's |
| absent | given | the flag's |
| present | given | both: the repository's first, the flag's after |

Both files go to git as `--exclude-from`, in that order, so on a conflict the
later file wins. A `!pattern` in `--include-file` therefore drops an entry the
repository's file lists, for that one create — the only way to narrow the
project's list without editing a project file.

Example manifest:

```gitignore
.env*
node_modules/
!.env.production
```

`dev` reads `.worktreeinclude` and never writes it, as with `agents.yaml`
(invariant 9). The only place it writes is the checkout it just created.

Output gains one line when anything was copied:

```
worktree fix-header on branch fix-header at ~/wt/fix-header
copied 3 ignored paths from ~/src/app (.env, .env.local, node_modules/)
container fix-header is running
```

Nothing is stored in the database: `remove` deletes the whole checkout, so no
inventory of copied files is needed.

## Selection

A path is copied when all three hold:

1. it matches the combined patterns;
2. it is untracked and ignored in the source checkout;
3. it is not tracked in the new checkout — the new branch may commit a file the
   source branch ignores, and overwriting it would silently change the
   branch's content.

Rule 2 is what makes a broad pattern such as `*` safe: it cannot reach tracked
files or `.git`.

Git does the matching; there is no gitignore parser in Go. Every listing is
`git ls-files -z`, one entry per file:

- `--others --ignored --exclude-from=<manifest>…` in the source lists the files
  the patterns match;
- `--others --ignored --exclude-standard` in the source lists the files its own
  rules ignore;
- `--cached` in the destination lists what the new checkout tracks.

The intersection of the first two, minus the third (and anything beneath a
tracked file, or above one), is the set of files to copy. They are then
gathered back into the highest directories every file of which was chosen —
counted against `--cached --others` in the source — and that do not hold a
tracked file in the destination, so a `node_modules/` of 100k files is one `cp`
invocation, while a directory the new checkout already has is copied into file
by file rather than nested.

`--directory` is not used: it collapses a directory only when everything under
it is ignored, so two listings group the same tree differently and cannot be
intersected.

## Flow inside `worktree create`

1. Validate flags. Read `--include-file` if given; missing or unreadable is a
   usage error (exit 2), before anything exists. Find the source checkout with
   `git rev-parse --show-toplevel` from the working directory or `--repo`.
2. `git worktree add`, then `xpath.Resolve`, as today.
3. **Copy.**
4. `createWorktreeRows`, then `start`, as today.

The copy precedes the rows so `dcconfig.Find` sees the finished checkout: a
project that gitignores `.devcontainer/` and lists it in the manifest gets that
configuration found and used. `dev` copied only what the operator named and
created or edited no `devcontainer.json`, so invariant 9 holds.

### Failure

- **A copy fails** (disk full, permission denied): the checkout is rolled back
  as a bad devcontainer config rolls it back today, and the command exits 1
  naming the failing path. A checkout with half a `node_modules` and no `.env`
  that looks finished is worse than a clean error.
- **No source checkout** — a bare repository, run from outside any of its
  worktrees: without `--include-file`, nothing is copied (there is no
  `.worktreeinclude` to read either); with it, a usage error: "no checkout to
  copy from; run from inside a worktree of this repository".

### Copying

Shell out to `cp`, one invocation per entry, after `os.MkdirAll` for the
parent of a nested entry such as `apps/web/.env`:

- Linux: `cp -a --reflink=auto SRC DST` — a clone on btrfs or xfs, a normal
  copy elsewhere.
- macOS: `cp -c -Rp SRC DST` — an APFS clone; if `cp -c` refuses, retry with
  `cp -Rp`. Its behaviour off APFS is to be checked against the real tool
  before it is relied on.

Symlinks are copied as symlinks, never followed. A relative link (pnpm's inside
`node_modules/`) resolves within the new checkout; an absolute one still points
where it did, possibly into the source checkout.

Not fixed up: a copied `.venv/` records the source checkout's path in
`pyvenv.cfg` and its scripts' shebangs, and runs the source's interpreter until
recreated (`uv sync`, `python -m venv --clear`). Build caches such as `.next/`
embed paths but rebuild themselves.

## Code layout

- `internal/gitwt`: the `ls-files` wrappers and the toplevel lookup.
- `internal/wtseed` (new): reads the manifests, computes the copy list, runs
  the copies.
- `internal/cli/worktree.go`: the flag, validation, one call into `wtseed`
  between the add and the rows, rollback on error, the output line. Nothing
  more, per "resolve inputs, call a package, print".

## Testing

Real `git` in temporary repositories (the existing symlink-free `tempDir`
helper); no container engine, so `make test` stays engine-free.

- `internal/gitwt`: ignored listing with a directory as one entry; matching
  with several `--exclude-from` files in order; tracked listing.
- `internal/wtseed`:
  - selection: matched-and-ignored copied; tracked-in-source skipped;
    tracked-in-destination skipped (a branch committing a file the source
    ignores); unmatched skipped;
  - merging: repository only, flag only, both, a flag `!pattern` dropping a
    repository entry;
  - copying: nested parents created, directory copied whole, symlink preserved,
    file mode kept;
  - an unreadable source fails, naming the path.
- `internal/cli/worktree_test.go`, with `--no-start`:
  - `.env` and `node_modules/` arrive and the "copied" line prints;
  - no manifest and no flag: nothing copied, output unchanged;
  - a missing `--include-file` exits 2 and leaves no checkout;
  - a failed copy rolls the checkout back and leaves no rows;
  - a gitignored `.devcontainer/` in the manifest is found by `dcconfig.Find`;
  - a bare repository with no checkout plus `--include-file` exits 2.
- `test/smoke/worktree_test.go`: a copied `.env` is readable inside the running
  container.

## Documentation

- `docs/USAGE.md`: in the worktree walkthrough, a short part on carrying
  ignored files over with a `.worktreeinclude` example, stating that install is
  re-run afterwards and that a `.venv/` must be recreated; in the `worktree
  create` reference, the `--include-file` row and the table above.
- `CLAUDE.md`: under invariant 9, one sentence — `.worktreeinclude` is read and
  never written, as `agents.yaml` is.

## Out of scope

A worktree pool; copy-on-write sharing of tracked files; a setup hook; anything
on the k8s provider, which still refuses worktrees.
