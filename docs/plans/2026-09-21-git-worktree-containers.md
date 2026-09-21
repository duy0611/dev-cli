# Git worktree containers — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `dev worktree create|list|remove`, one command that owns a git worktree checkout and the container running on it, so neither can be left behind without the other.

**Architecture:** A new `internal/gitwt` package shells out to `git`, a new `internal/herdr` package best-effort registers the checkout with Herdr, and a new `worktrees` table links one checkout to one container with `ON DELETE CASCADE`. The container gets two bind mounts at their identical host paths — the git common directory and the checkout — because a worktree's `.git` is a file holding an absolute host path, and the repository holds an absolute backlink to the checkout that `git gc --auto` prunes when it does not resolve.

**Tech Stack:** Go 1.x, cobra, modernc.org/sqlite, the `git` CLI, the `devcontainer` CLI, `docker`. No go-git, no Docker SDK — the repo shells out.

**Spec:** `docs/specs/2026-09-21-git-worktree-containers.md`

## Global Constraints

Copied from `CLAUDE.md` and the spec. Every task's requirements implicitly include these.

- **Exit codes are interface.** `2` malformed request, `3` named thing does not exist, `1` otherwise. Use `usageError`/`usageErrorf`/`notFoundErrorf` from `internal/cli/errors.go`, and `exactArgs`/`noArgs`/`minArgs` from `internal/cli/args.go` — never cobra's `Args` helpers directly.
- **Migrations are portable SQL.** No `AUTOINCREMENT`, no `datetime('now')` defaults, literal defaults only, timestamps written by the application. The same files replay against Postgres.
- **Resolve host paths with `xpath.Resolve` before storing or mounting.** Never a raw path string.
- **`--mount` is an `up` flag only.** The devcontainer CLI does not accept it on `exec`; it must never enter `execArgs`.
- **Never both mount routes for one target.** Docker refuses the run with `duplicate mount destination`. A generated document names its mounts; only a project-owned container gets `--mount`.
- **Do not write into a project's folder.** No creating or editing a `devcontainer.json` under a repository or checkout.
- **Shell out, do not reimplement.** `git` via `os/exec`.
- **Every non-obvious line carries a comment saying *why*,** usually naming the failure it prevents. Match the surrounding density — this repo comments heavily.
- **No `Co-Authored-By: Claude` trailer** and no other tooling-attribution line on any commit. `.githooks/commit-msg` rejects them; run `make hooks` once in a fresh clone.
- **Tests never touch a container engine.** `make test` must pass with no docker, no devcontainer CLI, no network.
- **Store tests use a temp file, never `:memory:`.**
- After every task: `make lint && make test` both pass before the commit.

## File structure

| File | Responsibility |
|---|---|
| `internal/gitwt/gitwt.go` (create) | Every `git` call: `CommonDir`, `Add`, `Remove`, `List`. The only place this repo runs git. |
| `internal/gitwt/gitwt_test.go` (create) | Tests against real `git` — cheap, hermetic, no network. |
| `internal/herdr/herdr.go` (create) | `Available`, `Open`, `Close`. Best-effort; never returns an error that should stop a `dev` command. |
| `internal/herdr/herdr_test.go` (create) | Stub `herdr` on a temp PATH. |
| `internal/store/migrations/0005_worktrees.sql` (create) | The `worktrees` table. |
| `internal/store/worktree.go` (create) | `CreateWorktree`, `GetWorktree`, `ListWorktrees`, `DeleteWorktree`. |
| `internal/store/worktree_test.go` (create) | Round-trip and cascade. |
| `internal/store/migrate_worktree_test.go` (create) | Upgrade from the 0004 schema. |
| `internal/model/model.go` (modify) | Add the `Worktree` type. |
| `internal/dcgen/render.go` (modify) | `Mount.Bind` field; `mounts` becomes a collected slice. |
| `internal/provider/local/local.go` (modify) | `up` passes the worktree `--mount` flags for a project-owned container. |
| `internal/cli/worktree.go` (create) | The `dev worktree` command tree: parse, orchestrate, print. |
| `internal/cli/worktree_test.go` (create) | Command-level behaviour. |
| `internal/cli/generate.go` (modify) | `worktreeMount` helper; `rewriteGeneratedTools` preserves the `.git` mount. |
| `internal/cli/container.go` (modify) | `container remove` warns about a leftover checkout. |
| `internal/cli/root.go` (modify) | Register `newWorktreeCmd`. |
| `docs/USAGE.md` (modify) | Walkthrough plus command reference. |
| `CLAUDE.md` (modify) | The gitdir invariant. |
| `test/smoke/worktree_test.go` (create) | End-to-end against a real engine, behind the `smoke` tag. |

Tasks are ordered so each one compiles and tests green on its own. Tasks 1–3 are leaves with no dependencies on each other and could be done in any order; 4 onward build upward.

---

### Task 1: `internal/gitwt` — the git wrapper

**Files:**
- Create: `internal/gitwt/gitwt.go`
- Test: `internal/gitwt/gitwt_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `var ErrNotRepo = errors.New("not a git repository")`
  - `func CommonDir(ctx context.Context, dir string) (string, error)` — absolute path of the git common directory; wraps `ErrNotRepo` when `dir` is not in a repository.
  - `func Add(ctx context.Context, repo, path, branch, base string) error`
  - `func Remove(ctx context.Context, repo, path string, force bool) error`
  - `func List(ctx context.Context, repo string) ([]Entry, error)` with `type Entry struct{ Path, Branch string }`

- [ ] **Step 1: Write the failing tests**

Create `internal/gitwt/gitwt_test.go`:

```go
package gitwt

import (
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
)

// requireGit skips when git is missing. Real git rather than a stub: it is
// cheap, hermetic and needs no network, so a stub would only test the stub —
// and the behaviour under test here *is* git's behaviour.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// initRepo builds a repository with one commit and returns its root.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q", ".")
	// Identity is set per-repo: the host running the test may have none, and
	// `git commit` refuses without one.
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "f")
	run(t, dir, "commit", "-qm", "init")
	return dir
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestCommonDirFindsTheRepository(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)

	got, err := CommonDir(t.Context(), repo)
	if err != nil {
		t.Fatalf("CommonDir: %v", err)
	}
	want := filepath.Join(repo, ".git")
	if got != want {
		t.Errorf("CommonDir = %q, want %q", got, want)
	}
}

// A subdirectory resolves upward, the way every other git command does.
func TestCommonDirResolvesUpward(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := CommonDir(t.Context(), sub)
	if err != nil {
		t.Fatalf("CommonDir: %v", err)
	}
	if got != filepath.Join(repo, ".git") {
		t.Errorf("CommonDir = %q, want the repository's .git", got)
	}
}

// From inside a worktree the answer is the repository, not the worktree. This
// is why --show-toplevel is not used: it would answer the worktree, and
// creating a worktree from inside another worktree would then point at the
// wrong place.
func TestCommonDirFromInsideAWorktree(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	run(t, repo, "worktree", "add", "-q", wt, "-b", "feat")

	got, err := CommonDir(t.Context(), wt)
	if err != nil {
		t.Fatalf("CommonDir: %v", err)
	}
	if got != filepath.Join(repo, ".git") {
		t.Errorf("CommonDir = %q, want the repository's .git", got)
	}
}

// A bare repository answers with itself. Its common dir has no .git beneath
// anything, which is why the mount source is defined as this directory rather
// than as <repo>/.git.
func TestCommonDirOnABareRepository(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	bare := filepath.Join(dir, "app.git")
	run(t, dir, "init", "-q", "--bare", bare)

	got, err := CommonDir(t.Context(), bare)
	if err != nil {
		t.Fatalf("CommonDir: %v", err)
	}
	if got != bare {
		t.Errorf("CommonDir = %q, want %q", got, bare)
	}
}

func TestCommonDirOutsideARepository(t *testing.T) {
	requireGit(t)

	_, err := CommonDir(t.Context(), t.TempDir())
	if !errors.Is(err, ErrNotRepo) {
		t.Errorf("err = %v, want ErrNotRepo", err)
	}
}

func TestAddCreatesANewBranch(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")

	if err := Add(t.Context(), repo, path, "feat", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	entries, err := List(t.Context(), repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !hasEntry(entries, path, "feat") {
		t.Errorf("List = %v, want an entry for %s on feat", entries, path)
	}
}

// An existing branch is checked out rather than recreated: `git worktree add -b`
// on a branch that exists fails, so the -b must not be passed.
func TestAddChecksOutAnExistingBranch(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	run(t, repo, "branch", "existing")
	path := filepath.Join(t.TempDir(), "wt")

	if err := Add(t.Context(), repo, path, "existing", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
}

// An empty repository is not an error: `git worktree add -b` infers --orphan
// there, which is the right answer for a repository just initialised.
func TestAddOnARepositoryWithNoCommits(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	run(t, repo, "init", "-q", ".")
	path := filepath.Join(t.TempDir(), "wt")

	if err := Add(t.Context(), repo, path, "feat", ""); err != nil {
		t.Errorf("Add on an empty repository: %v", err)
	}
}

func TestRemoveRefusesADirtyCheckout(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	if err := Add(t.Context(), repo, path, "feat", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "dirty"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Remove(t.Context(), repo, path, false)
	if err == nil {
		t.Fatal("Remove succeeded on a dirty checkout")
	}
	// Git's own wording reaches the operator. It says more than anything this
	// could write, and it names the flag that gets past it.
	if !strings.Contains(err.Error(), "use --force") {
		t.Errorf("error %q does not carry git's own message", err)
	}

	if err := Remove(t.Context(), repo, path, true); err != nil {
		t.Errorf("Remove --force: %v", err)
	}
}

// The branch outlives the checkout, which is the promise the command makes.
func TestRemoveKeepsTheBranch(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	if err := Add(t.Context(), repo, path, "feat", ""); err != nil {
		t.Fatal(err)
	}
	if err := Remove(t.Context(), repo, path, false); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "branch", "--list", "feat")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "feat") {
		t.Error("Remove deleted the branch")
	}
}

func hasEntry(entries []Entry, path, branch string) bool {
	for _, e := range entries {
		if e.Path == path && e.Branch == branch {
			return true
		}
	}
	return false
}
```

Add `"os"` and `"strings"` to the import block.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/gitwt/...`
Expected: FAIL — the package does not exist (`no Go files` or undefined symbols).

- [ ] **Step 3: Write the implementation**

Create `internal/gitwt/gitwt.go`:

```go
// Package gitwt drives git's worktree commands.
//
// Shelling out rather than go-git, for the reason the rest of this tool shells
// out: git's own behaviour is the behaviour wanted, including its error
// messages, and a reimplementation would be a second opinion about a format
// git owns.
package gitwt

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const gitBin = "git"

// ErrNotRepo is returned when a directory is not inside a git repository.
var ErrNotRepo = errors.New("not a git repository")

// Entry is one checkout as `git worktree list` reports it.
type Entry struct {
	Path   string
	Branch string // short name, empty for a detached HEAD
}

// CommonDir returns the absolute path of the repository's common directory —
// the directory holding the object store and the worktree administration.
//
// This answers two questions with one call, deliberately. It says whether dir
// is in a repository at all, and it is the directory that must be bind-mounted
// into a container: a worktree's .git file holds `gitdir: <common>/worktrees/
// <name>`, an absolute host path. A separate IsRepo could disagree with it.
//
// --git-common-dir rather than --show-toplevel: from inside a worktree the
// latter answers the worktree, so creating a worktree from inside another one
// would register it against the wrong place. It is also correct for a bare
// repository, which has no working tree to be the top level of.
func CommonDir(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, gitBin,
		"rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		// Git's message names ".git" rather than the directory the operator
		// was standing in, so it is dropped and the caller writes its own.
		return "", fmt.Errorf("%w: %s", ErrNotRepo, dir)
	}
	return strings.TrimSpace(string(out)), nil
}

// Add creates a checkout at path.
//
// branch is checked out when it already exists and created otherwise, because
// `git worktree add -b` fails outright on a branch that exists. base names the
// start point for a new branch and is ignored for an existing one — the caller
// rejects that combination before getting here.
func Add(ctx context.Context, repo, path, branch, base string) error {
	args := []string{"worktree", "add", "--quiet"}
	if branchExists(ctx, repo, branch) {
		args = append(args, path, branch)
	} else {
		args = append(args, path, "-b", branch)
		if base != "" {
			args = append(args, base)
		}
	}
	return runGit(ctx, repo, args...)
}

// Remove deletes the checkout and its administrative directory.
//
// The branch is never touched; that is git's behaviour and the command's
// promise. force passes through to git, which refuses a checkout holding
// modified or untracked files without it.
func Remove(ctx context.Context, repo, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	return runGit(ctx, repo, append(args, path)...)
}

// List reports the repository's checkouts, the main one included.
func List(ctx context.Context, repo string) ([]Entry, error) {
	cmd := exec.CommandContext(ctx, gitBin, "worktree", "list", "--porcelain")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing worktrees: %w", gitError(err))
	}

	// The porcelain format is one blank-line-separated record per checkout,
	// each a sequence of "key value" lines. Parsed rather than the plain
	// format because that one pads with spaces and cannot be split safely.
	var (
		entries []Entry
		cur     Entry
	)
	flush := func() {
		if cur.Path != "" {
			entries = append(entries, cur)
		}
		cur = Entry{}
	}
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			// Reported as a full ref; the short name is what a human reads.
			cur.Branch = strings.TrimPrefix(
				strings.TrimPrefix(line, "branch "), "refs/heads/")
		}
	}
	flush()
	return entries, nil
}

// branchExists reports whether the repository already has this local branch.
//
// --verify on the full ref, not `git branch --list`: the latter prints nothing
// and exits 0 for a branch that does not exist, so its exit code says nothing.
func branchExists(ctx context.Context, repo, branch string) bool {
	cmd := exec.CommandContext(ctx, gitBin,
		"rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = repo
	return cmd.Run() == nil
}

func runGit(ctx context.Context, repo string, args ...string) error {
	cmd := exec.CommandContext(ctx, gitBin, args...)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %s", args[1], strings.TrimSpace(string(out)))
	}
	return nil
}

// gitError turns an ExitError into something carrying git's own stderr, which
// is more useful than "exit status 128".
func gitError(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return errors.New(strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/gitwt/... -v`
Expected: PASS, all nine tests.

- [ ] **Step 5: Lint and commit**

```bash
make lint && make test
git add internal/gitwt
git commit -m "feat: gitwt drives git's worktree commands"
```

---

### Task 2: `internal/herdr` — best-effort registration

**Files:**
- Create: `internal/herdr/herdr.go`
- Test: `internal/herdr/herdr_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `func Available(ctx context.Context) bool`
  - `func Open(ctx context.Context, path string) (string, error)` — returns the Herdr workspace ID
  - `func Close(ctx context.Context, workspaceID string) error`

- [ ] **Step 1: Write the failing tests**

Create `internal/herdr/herdr_test.go`:

```go
package herdr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHerdr writes a stub `herdr` onto a PATH that *replaces* the host's.
//
// Replaced rather than prepended, as the k8s harness does and for the same
// reason: a host with a real herdr installed would otherwise have its daemon
// probed, and its sidebar written to, from a unit test.
func fakeHerdr(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	log := filepath.Join(dir, "herdr.argv")
	body := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + log + "'; done\n" + script
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return log
}

func TestAvailableWhenTheDaemonAnswers(t *testing.T) {
	fakeHerdr(t, "exit 0\n")
	if !Available(t.Context()) {
		t.Error("Available = false with a daemon that answers")
	}
}

// Installed but not running is the case that matters: without the status probe
// every worktree create would hang or fail on a machine where herdr is present
// and the daemon is not.
func TestUnavailableWhenTheDaemonIsDown(t *testing.T) {
	fakeHerdr(t, "exit 1\n")
	if Available(t.Context()) {
		t.Error("Available = true with a daemon that does not answer")
	}
}

func TestUnavailableWhenNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if Available(t.Context()) {
		t.Error("Available = true with no herdr on PATH")
	}
}

func TestOpenReturnsTheWorkspaceID(t *testing.T) {
	log := fakeHerdr(t, `printf '{"workspace_id":"7f2a"}\n'`+"\nexit 0\n")

	id, err := Open(t.Context(), "/home/u/wt/feat")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if id != "7f2a" {
		t.Errorf("id = %q, want 7f2a", id)
	}
	argv := readLog(t, log)
	for _, want := range []string{"worktree", "open", "--path", "/home/u/wt/feat"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q is missing %q", argv, want)
		}
	}
}

// Output herdr does not promise is not an error: the ID is a convenience for
// the removal path, and a checkout that opened is worth keeping either way.
func TestOpenToleratesUnparsableOutput(t *testing.T) {
	fakeHerdr(t, "printf 'opened\\n'\nexit 0\n")

	id, err := Open(t.Context(), "/home/u/wt/feat")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if id != "" {
		t.Errorf("id = %q, want empty", id)
	}
}

func TestCloseRemovesTheWorkspace(t *testing.T) {
	log := fakeHerdr(t, "exit 0\n")

	if err := Close(t.Context(), "7f2a"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	argv := readLog(t, log)
	for _, want := range []string{"worktree", "remove", "--workspace", "7f2a", "--force"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q is missing %q", argv, want)
		}
	}
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("herdr was never called: %v", err)
	}
	return string(b)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/herdr/...`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the implementation**

Create `internal/herdr/herdr.go`:

```go
// Package herdr registers a checkout with Herdr, when Herdr is running.
//
// Everything here is optional. Herdr is a view onto a checkout that exists
// either way, so a failure to open that view is warned about and never stops
// the command that made the checkout.
package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

const herdrBin = "herdr"

// Available reports whether Herdr can be asked to do anything.
//
// Two checks, not one. The binary being on PATH says nothing about the daemon:
// an installed Herdr whose server is not running would turn every call into a
// hang or a failure, on a path where neither is the operator's problem.
func Available(ctx context.Context) bool {
	if _, err := exec.LookPath(herdrBin); err != nil {
		return false
	}
	return exec.CommandContext(ctx, herdrBin, "status", "server").Run() == nil
}

// Open registers an existing checkout as a Herdr workspace and returns its ID.
//
// The checkout is made by git first and handed over afterwards, rather than
// letting `herdr worktree create` do both: the git work must not depend on an
// optional tool.
//
// An unparsable answer is not an error. The ID is only needed to close the
// workspace later, and a missing one costs a stale entry in a sidebar — far
// less than failing a command whose real work already succeeded.
func Open(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, herdrBin,
		"worktree", "open", "--path", path).Output()
	if err != nil {
		return "", fmt.Errorf("herdr worktree open: %w", err)
	}

	var res struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal([]byte(firstJSONLine(string(out))), &res); err != nil {
		return "", nil
	}
	return res.WorkspaceID, nil
}

// Close removes the Herdr workspace. Herdr state only: the checkout is deleted
// by git, which is the division Herdr's own commands draw.
//
// --force because the checkout is being deleted regardless, so a prompt about
// it would be a question with one answer.
func Close(ctx context.Context, workspaceID string) error {
	if workspaceID == "" {
		return nil // herdr was absent or declined when this was created
	}
	if out, err := exec.CommandContext(ctx, herdrBin,
		"worktree", "remove", "--workspace", workspaceID, "--force").
		CombinedOutput(); err != nil {
		return fmt.Errorf("herdr worktree remove: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// firstJSONLine returns the first line that looks like a JSON object, so a
// progress line printed before the result does not break the decode.
func firstJSONLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "{") {
			return line
		}
	}
	return ""
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/herdr/... -v`
Expected: PASS, all six tests.

- [ ] **Step 5: Lint and commit**

```bash
make lint && make test
git add internal/herdr
git commit -m "feat: herdr registers a checkout when the daemon is running"
```

---

### Task 3: the `worktrees` table

**Files:**
- Create: `internal/store/migrations/0005_worktrees.sql`
- Create: `internal/store/worktree.go`
- Create: `internal/store/worktree_test.go`
- Create: `internal/store/migrate_worktree_test.go`
- Modify: `internal/model/model.go` (append the `Worktree` type)

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `model.Worktree{WorkspaceName, ContainerName, Repo, Branch, Path, HerdrWorkspace string; CreatedAt time.Time}`
  - `func (s *Store) CreateWorktree(w model.Worktree) error`
  - `func (s *Store) GetWorktree(workspace, container string) (model.Worktree, error)` — `ErrNotFound` when there is none
  - `func (s *Store) ListWorktrees(workspace string) ([]model.Worktree, error)` — every workspace when `workspace` is empty
  - `func (s *Store) DeleteWorktree(workspace, container string) error`

- [ ] **Step 1: Write the failing tests**

Create `internal/store/worktree_test.go`:

```go
package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
)

// seedContainer gives a worktree row something to hang off, since the foreign
// key is what the cascade depends on.
func seedContainer(t *testing.T, s *Store) {
	t.Helper()
	if err := s.CreateProvider(model.Provider{Name: "p", Kind: model.KindLocal, Config: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateWorkspace(model.Workspace{Name: "ws", ProviderName: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateContainer(model.Container{
		Name: "feat", WorkspaceName: "ws",
		SourceKind: model.SourceFolder, Source: "/wt/feat",
	}); err != nil {
		t.Fatal(err)
	}
}

func testWorktree() model.Worktree {
	return model.Worktree{
		WorkspaceName:  "ws",
		ContainerName:  "feat",
		Repo:           "/src/app/.git",
		Branch:         "feat",
		Path:           "/wt/feat",
		HerdrWorkspace: "7f2a",
	}
}

func TestWorktreeRoundTrip(t *testing.T) {
	s := openTestStore(t)
	seedContainer(t, s)

	if err := s.CreateWorktree(testWorktree()); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	got, err := s.GetWorktree("ws", "feat")
	if err != nil {
		t.Fatalf("GetWorktree: %v", err)
	}
	if got.Repo != "/src/app/.git" || got.Branch != "feat" ||
		got.Path != "/wt/feat" || got.HerdrWorkspace != "7f2a" {
		t.Errorf("GetWorktree = %+v, want the seeded row", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero; it is written by the application")
	}
}

func TestGetWorktreeMissing(t *testing.T) {
	s := openTestStore(t)
	seedContainer(t, s)

	_, err := s.GetWorktree("ws", "feat")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// The cascade is what makes `container remove` safe to leave alone: it can
// never leave a row describing a container that is gone. It depends on
// PRAGMA foreign_keys = ON, which is invariant 6.
func TestDeletingTheContainerTakesTheWorktree(t *testing.T) {
	s := openTestStore(t)
	seedContainer(t, s)
	if err := s.CreateWorktree(testWorktree()); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteContainer("ws", "feat"); err != nil {
		t.Fatalf("DeleteContainer: %v", err)
	}
	if _, err := s.GetWorktree("ws", "feat"); !errors.Is(err, ErrNotFound) {
		t.Errorf("worktree survived the container: %v", err)
	}
}

// And so does deleting the workspace, two cascades deep.
func TestDeletingTheWorkspaceTakesTheWorktree(t *testing.T) {
	s := openTestStore(t)
	seedContainer(t, s)
	if err := s.CreateWorktree(testWorktree()); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteWorkspace("ws"); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	if _, err := s.GetWorktree("ws", "feat"); !errors.Is(err, ErrNotFound) {
		t.Errorf("worktree survived the workspace: %v", err)
	}
}

func TestListWorktrees(t *testing.T) {
	s := openTestStore(t)
	seedContainer(t, s)
	if err := s.CreateWorktree(testWorktree()); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListWorktrees("")
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("ListWorktrees returned %d rows, want 1", len(all))
	}
	scoped, err := s.ListWorktrees("ws")
	if err != nil {
		t.Fatalf("ListWorktrees(ws): %v", err)
	}
	if len(scoped) != 1 {
		t.Errorf("ListWorktrees(ws) returned %d rows, want 1", len(scoped))
	}
	none, err := s.ListWorktrees("other")
	if err != nil {
		t.Fatalf("ListWorktrees(other): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("ListWorktrees(other) returned %d rows, want 0", len(none))
	}
}

func TestDeleteWorktreeMissing(t *testing.T) {
	s := openTestStore(t)
	seedContainer(t, s)

	if err := s.DeleteWorktree("ws", "feat"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// A temp file, never :memory: — that database is per-connection and the pool
// would hand the migration to one connection and the query to another.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "dev.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
```

Before writing this file, check `internal/store/store_test.go` for an existing
helper that opens a temp-file store. If one exists, use it and drop
`openTestStore` from this file rather than defining a second one.

Create `internal/store/migrate_worktree_test.go`:

```go
package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
)

// TestMigrationAddsWorktrees covers upgrading a database created before the
// worktrees table existed.
//
// The cascade is the part worth testing across a migration: a FOREIGN KEY
// declared in a table created later still has to fire, and SQLite only honours
// it with PRAGMA foreign_keys = ON, which Open sets.
func TestMigrationAddsWorktrees(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.db")

	// The schema as it stood after 0004, written out rather than replayed from
	// the embedded files: the point is to start from the schema as it was.
	pre := `
CREATE TABLE providers (
  name TEXT PRIMARY KEY, kind TEXT NOT NULL,
  config TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL
);
CREATE TABLE workspaces (
  name TEXT PRIMARY KEY,
  provider_name TEXT NOT NULL REFERENCES providers(name),
  ssh_forward INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL
);
CREATE TABLE workspace_settings (
  workspace_name TEXT NOT NULL REFERENCES workspaces(name) ON DELETE CASCADE,
  key TEXT NOT NULL, spec TEXT NOT NULL,
  PRIMARY KEY (workspace_name, key)
);
CREATE TABLE containers (
  name TEXT NOT NULL,
  workspace_name TEXT NOT NULL REFERENCES workspaces(name) ON DELETE CASCADE,
  source_kind TEXT NOT NULL, source TEXT NOT NULL, config_path TEXT NOT NULL,
  generated_config TEXT NOT NULL DEFAULT '',
  persist_state INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  PRIMARY KEY (workspace_name, name)
);
CREATE TABLE app_state (key TEXT PRIMARY KEY, value TEXT NOT NULL);`

	seedRaw(t, path, pre,
		"0001_init.sql", "0002_generated_config.sql",
		"0003_drop_gpg_forward.sql", "0004_persist_state.sql")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	seedContainer(t, s)
	if err := s.CreateWorktree(testWorktree()); err != nil {
		t.Fatalf("CreateWorktree after migrating: %v", err)
	}
	if err := s.DeleteContainer("ws", "feat"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetWorktree("ws", "feat"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the cascade did not fire after migrating: %v", err)
	}
	_ = model.Worktree{} // keeps the import honest if the assertions change
}
```

Check the exact column list of the 0004-era `containers` table against
`migrations/0001_init.sql` plus `0002`/`0004` before running; if `generated_config`
was declared differently, copy that declaration rather than the one above.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/... -run Worktree`
Expected: FAIL — `model.Worktree` undefined, `CreateWorktree` undefined.

- [ ] **Step 3: Write the migration**

Create `internal/store/migrations/0005_worktrees.sql`:

```sql
-- One git worktree checkout, and the container running on it.
--
-- A table rather than columns on containers: most containers are not
-- worktree-backed, and four mostly-empty columns on the table every command
-- reads is the wrong trade for one join in one command.
--
-- The cascade is what makes `container remove` safe to leave alone — it cannot
-- leave a row describing a container that is gone. It needs
-- PRAGMA foreign_keys = ON, which the store sets on every connection.
--
-- repo is the git *common directory*, not the repository root. They differ for
-- a bare repository, and this is the directory the checkout's .git file points
-- into, so it is the one that gets bind-mounted.
--
-- path duplicates containers.source for the same row today. Deliberate: source
-- is what the container is mounted from, path is what git was told to create,
-- and keeping them apart means a container whose source later diverges from its
-- checkout cannot corrupt the removal.
--
-- An empty herdr_workspace rather than a nullable column, matching how the rest
-- of this schema spells "nothing to say" and keeping the scan free of
-- sql.NullString. Empty means Herdr was absent or declined.
--
-- Portable SQL: literal default, TEXT timestamp written by the application.
CREATE TABLE worktrees (
  workspace_name  TEXT NOT NULL,
  container_name  TEXT NOT NULL,
  repo            TEXT NOT NULL,
  branch          TEXT NOT NULL,
  path            TEXT NOT NULL,
  herdr_workspace TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  PRIMARY KEY (workspace_name, container_name),
  FOREIGN KEY (workspace_name, container_name)
    REFERENCES containers(workspace_name, name) ON DELETE CASCADE
);
```

- [ ] **Step 4: Add the model type**

Append to `internal/model/model.go`:

```go
// Worktree is a git worktree checkout that a container was created on.
//
// One per container at most, and it is deleted with the container: the pair is
// created together by `dev worktree create` and the whole point of recording
// the link is that neither can be left behind without the other.
type Worktree struct {
	WorkspaceName string
	ContainerName string
	// Repo is git's common directory — the directory holding the object store
	// and the worktree administration. Not the repository root: the two differ
	// for a bare repository, and this is the path the checkout's .git file
	// points into, so it is what gets bind-mounted into the container.
	Repo   string
	Branch string
	// Path is the checkout, resolved. Equal to the container's Source today,
	// and kept separately because the two answer different questions.
	Path string
	// HerdrWorkspace is the id Herdr gave this checkout, empty when Herdr was
	// not running or declined. Only used to close that workspace on removal.
	HerdrWorkspace string
	CreatedAt      time.Time
}
```

- [ ] **Step 5: Write the store methods**

Create `internal/store/worktree.go`:

```go
package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/duy0611/dev-cli/internal/model"
)

// CreateWorktree records a checkout against its container.
func (s *Store) CreateWorktree(w model.Worktree) error {
	_, err := s.db.Exec(
		`INSERT INTO worktrees
		   (workspace_name, container_name, repo, branch, path, herdr_workspace, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		w.WorkspaceName, w.ContainerName, w.Repo, w.Branch, w.Path,
		w.HerdrWorkspace, nowString())
	if err != nil {
		if isUniqueViolation(err) {
			return ErrExists
		}
		return fmt.Errorf("recording the worktree for %s: %w", w.ContainerName, err)
	}
	return nil
}

// GetWorktree returns the checkout a container was created on, or ErrNotFound
// when the container is not worktree-backed.
func (s *Store) GetWorktree(workspace, container string) (model.Worktree, error) {
	row := s.db.QueryRow(
		`SELECT workspace_name, container_name, repo, branch, path, herdr_workspace, created_at
		 FROM worktrees WHERE workspace_name = ? AND container_name = ?`,
		workspace, container)
	return scanWorktree(row)
}

// ListWorktrees returns one workspace's checkouts, or every workspace's when
// workspace is empty.
func (s *Store) ListWorktrees(workspace string) ([]model.Worktree, error) {
	query := `SELECT workspace_name, container_name, repo, branch, path, herdr_workspace, created_at
	          FROM worktrees`
	args := []any{}
	if workspace != "" {
		query += ` WHERE workspace_name = ?`
		args = append(args, workspace)
	}
	query += ` ORDER BY workspace_name, container_name`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing worktrees: %w", err)
	}
	defer rows.Close()

	var out []model.Worktree
	for rows.Next() {
		w, err := scanWorktree(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// DeleteWorktree forgets a checkout. The container row is untouched: removing
// the container is a separate step, and the cascade covers the other order.
func (s *Store) DeleteWorktree(workspace, container string) error {
	res, err := s.db.Exec(
		`DELETE FROM worktrees WHERE workspace_name = ? AND container_name = ?`,
		workspace, container)
	if err != nil {
		return fmt.Errorf("deleting the worktree for %s: %w", container, err)
	}
	return requireOneRow(res, ErrNotFound)
}

func scanWorktree(sc scanner) (model.Worktree, error) {
	var (
		w         model.Worktree
		createdAt string
	)
	if err := sc.Scan(&w.WorkspaceName, &w.ContainerName, &w.Repo, &w.Branch,
		&w.Path, &w.HerdrWorkspace, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Worktree{}, ErrNotFound
		}
		return model.Worktree{}, fmt.Errorf("reading the worktree: %w", err)
	}

	t, err := parseTime(createdAt)
	if err != nil {
		return model.Worktree{}, fmt.Errorf("reading the worktree for %s: bad created_at %q: %w",
			w.ContainerName, createdAt, err)
	}
	w.CreatedAt = t
	return w, nil
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/store/... -v -run 'Worktree|Migration'`
Expected: PASS. If `TestDeletingTheWorkspaceTakesTheWorkspace` fails, the
two-level cascade is not firing — check that `containers` declares
`ON DELETE CASCADE` on its workspace FK, which `0001_init.sql` does.

- [ ] **Step 7: Lint and commit**

```bash
make lint && make test
git add internal/store internal/model
git commit -m "feat: record a container's git worktree"
```

---

### Task 4: `dcgen` renders a bind mount

**Files:**
- Modify: `internal/dcgen/render.go:53-73` (the `Mount` type), `internal/dcgen/render.go:74-130` (`Render`)
- Test: `internal/dcgen/render_test.go` (append)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `dcgen.Mount` gains two fields —
  `Bind string` (host path bind-mounted at the identical path in the container, empty for none)
  and `Host string` (host path to use as the workspace, empty for the CLI's default).
  `Render(name string, toolIDs []string, mount Mount, state State) (string, error)` keeps its signature.

- [ ] **Step 1: Write the failing tests**

Append to `internal/dcgen/render_test.go` (check the file's existing helpers
first and reuse them rather than redefining):

```go
// A worktree container names the checkout as its workspace at the host path,
// and bind-mounts git's common directory at the identical path beside it.
//
// The identical path is the whole trick: the checkout's .git file holds an
// absolute `gitdir:` pointing into that directory, and the repository holds an
// absolute backlink to the checkout. Both are host paths, and `git gc --auto`
// prunes the worktree's registration when the backlink does not resolve — from
// inside the container, silently.
func TestRenderWorktreeMounts(t *testing.T) {
	got, err := Render("feat", nil, Mount{
		Host: "/home/u/wt/feat",
		Bind: "/home/u/src/app/.git",
	}, State{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	var doc struct {
		WorkspaceFolder string   `json:"workspaceFolder"`
		WorkspaceMount  string   `json:"workspaceMount"`
		Mounts          []string `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("unmarshalling the rendered document: %v", err)
	}

	if doc.WorkspaceFolder != "/home/u/wt/feat" {
		t.Errorf("workspaceFolder = %q, want the checkout's host path", doc.WorkspaceFolder)
	}
	wantWS := "source=/home/u/wt/feat,target=/home/u/wt/feat,type=bind"
	if doc.WorkspaceMount != wantWS {
		t.Errorf("workspaceMount = %q, want %q", doc.WorkspaceMount, wantWS)
	}
	wantGit := "source=/home/u/src/app/.git,target=/home/u/src/app/.git,type=bind"
	if !slices.Contains(doc.Mounts, wantGit) {
		t.Errorf("mounts = %v, want it to hold %q", doc.Mounts, wantGit)
	}
}

// Both mounts at once, no target repeated: docker refuses the whole run with
// "duplicate mount destination" if one arrives twice.
func TestRenderWorktreeWithState(t *testing.T) {
	got, err := Render("feat", nil, Mount{
		Host: "/home/u/wt/feat",
		Bind: "/home/u/src/app/.git",
	}, State{Volume: "dev-ws-feat-state"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	var doc struct {
		Mounts            []string `json:"mounts"`
		PostCreateCommand string   `json:"postCreateCommand"`
	}
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Mounts) != 2 {
		t.Fatalf("mounts = %v, want two entries", doc.Mounts)
	}
	seen := map[string]bool{}
	for _, m := range doc.Mounts {
		_, target, _ := strings.Cut(m, "target=")
		target, _, _ = strings.Cut(target, ",")
		if seen[target] {
			t.Errorf("mounts repeat the target %q; docker refuses that run", target)
		}
		seen[target] = true
	}
	// The state volume is chowned and the bind mounts are not: the devcontainer
	// CLI UID-remaps a bind mount to the host user already, and a recursive
	// chown over a repository would be slow and pointless.
	if !strings.Contains(doc.PostCreateCommand, StateDir) {
		t.Errorf("postCreateCommand %q does not chown the state volume", doc.PostCreateCommand)
	}
	if strings.Contains(doc.PostCreateCommand, "/home/u/wt/feat") {
		t.Errorf("postCreateCommand %q chowns a bind mount", doc.PostCreateCommand)
	}
}

// A folderless container is unchanged by any of this.
func TestRenderFolderlessIsUnchanged(t *testing.T) {
	got, err := Render("scratch", nil,
		Mount{Volume: "dev-ws-scratch", Folder: "/workspaces/scratch"}, State{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "source=dev-ws-scratch,target=/workspaces/scratch,type=volume") {
		t.Errorf("rendered document lost the workspace volume:\n%s", got)
	}
}

// Byte-stable output is what makes the stored copy comparable and lets
// `rebuild --tools` tell a real change from a re-render.
func TestRenderWorktreeIsStable(t *testing.T) {
	m := Mount{Host: "/home/u/wt/feat", Bind: "/home/u/src/app/.git"}
	first, err := Render("feat", []string{"yq"}, m, State{Volume: "v"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := Render("feat", []string{"yq"}, m, State{Volume: "v"})
		if err != nil {
			t.Fatal(err)
		}
		if again != first {
			t.Fatal("Render is not byte-stable across calls")
		}
	}
}
```

Ensure `encoding/json`, `slices` and `strings` are imported in that test file.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dcgen/... -run Worktree`
Expected: FAIL — `Mount` has no field `Host` / `Bind`.

- [ ] **Step 3: Extend the Mount type**

Replace the `Mount` struct in `internal/dcgen/render.go` with:

```go
// Mount says where a generated container keeps its work.
//
// The zero value means the devcontainer CLI's own default: a bind mount of the
// workspace folder at /workspaces/<basename>, which is what an ordinary folder
// container wants.
type Mount struct {
	// Volume is the name of a volume to mount at Folder. Set for a folderless
	// container, which has no host directory to bind.
	Volume string
	// Folder is where Volume appears inside the container.
	Folder string
	// Host is a host directory to mount as the workspace at its own path,
	// rather than at the CLI's /workspaces/<basename>. Set for a worktree
	// checkout, whose registration in the repository is an absolute host path:
	// mounted anywhere else, `git gc --auto` inside the container prunes the
	// worktree away. Mutually exclusive with Volume.
	Host string
	// Bind is a host directory to mount at the identical path in the
	// container, beside the workspace. Set to git's common directory for a
	// worktree, whose .git file holds `gitdir: <common>/worktrees/<name>` —
	// an absolute host path that resolves only if the directory is there.
	Bind string
}
```

- [ ] **Step 4: Render it**

In `Render`, replace the `mount.Volume` block and the `state.Volume` block with:

```go
	// Collected rather than assigned. postCreateCommand is a single string and
	// mounts is a single array, so a container needing two of either would
	// silently keep only the last if each branch assigned directly.
	var (
		postCreate []string
		mounts     []string
	)

	switch {
	case mount.Volume != "":
		// Both, never one. workspaceMount alone leaves the CLI deriving the
		// in-container path from the host directory's basename, which for a
		// folderless container is a temporary directory with a different name
		// every invocation.
		doc["workspaceFolder"] = mount.Folder
		doc["workspaceMount"] = fmt.Sprintf("source=%s,target=%s,type=volume", mount.Volume, mount.Folder)
		// A volume is created root-owned and, unlike a bind mount, gets no UID
		// remapping from the CLI, so the remote user's first write fails.
		postCreate = append(postCreate, chown(mount.Folder))
	case mount.Host != "":
		// The same path on both sides. Left to itself the CLI would mount this
		// at /workspaces/<basename>, and the repository's backlink to this
		// checkout is an absolute host path — `git worktree prune`, which
		// `git gc --auto` runs, deletes a registration whose backlink does not
		// resolve. No chown: the CLI UID-remaps a bind mount already.
		doc["workspaceFolder"] = mount.Host
		doc["workspaceMount"] = fmt.Sprintf("source=%s,target=%s,type=bind", mount.Host, mount.Host)
	}

	if mount.Bind != "" {
		// Identical path again, and for the matching half of the same problem:
		// the checkout's .git file points here with an absolute host path.
		mounts = append(mounts, fmt.Sprintf("source=%s,target=%s,type=bind", mount.Bind, mount.Bind))
	}

	if state.Volume != "" {
		// A volume rather than a bind mount, and so root-owned on creation for
		// the same reason the workspace volume is: the chown below is what
		// makes the remote user's first write succeed.
		mounts = append(mounts, fmt.Sprintf("source=%s,target=%s,type=volume", state.Volume, StateDir))
		// A map, so encoding/json sorts the keys and the output stays stable.
		doc["containerEnv"] = StateEnv()
		postCreate = append(postCreate, chown(StateDir))
	}

	// Appended in a fixed order and never sorted: the order is already
	// deterministic, and the byte-stability test that makes the stored copy
	// comparable depends on it staying that way.
	if len(mounts) > 0 {
		doc["mounts"] = mounts
	}
	if len(postCreate) > 0 {
		// Joined with && rather than ; so that a failure stops there instead of
		// being hidden by the next command's success.
		doc["postCreateCommand"] = strings.Join(postCreate, " && ")
	}
```

Delete the old `var postCreate []string` declaration above, now folded into the
new `var` block.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/dcgen/... -v`
Expected: PASS, including every pre-existing test. A failure in an older test
means the folderless or state path changed shape — compare against the `switch`
above, which must keep both behaving exactly as before.

- [ ] **Step 6: Lint and commit**

```bash
make lint && make test
git add internal/dcgen
git commit -m "feat: dcgen renders a worktree's two bind mounts"
```

---

### Task 5: the local provider mounts a project-owned worktree

**Files:**
- Modify: `internal/provider/local/local.go:71-84` (the `--mount` block in `up`)
- Test: `internal/provider/local/local_test.go` (append)

**Interfaces:**
- Consumes: nothing from earlier tasks — `model.Container` is not extended.
- Produces, in `internal/provider/local/label.go`:
  - `func WithWorktree(ctx context.Context, commonDir string) context.Context` — exported, because `internal/cli` calls it.
  - `func worktreeCommonDir(ctx context.Context) string` — unexported, read inside `up`.
  - `func worktreeMountArgs(commonDir, workspace string) []string` — unexported; the `--mount` flag pairs for a project-owned worktree container, nil when `commonDir` is empty.

- [ ] **Step 1: Write the failing tests**

Append to `internal/provider/local/local_test.go`:

```go
// A project-owned worktree container gets both binds from the CLI's --mount,
// because dev may not write into the project's devcontainer.json (invariant 9).
func TestUpMountsAWorktreeForAProjectOwnedContainer(t *testing.T) {
	f := newFakePath(t)
	f.install(t, "devcontainer", "", 0)
	f.install(t, "docker", "", 0)

	c := testContainer()
	c.Source = "/home/u/wt/feat"
	c.ConfigPath = "/home/u/wt/feat/.devcontainer/devcontainer.json"

	p := &Provider{}
	if err := p.Up(withWorktree(t.Context(), "/home/u/src/app/.git"), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	argv := f.argv(t, "devcontainer")
	for _, want := range [][]string{
		{"--mount", "type=bind,source=/home/u/src/app/.git,target=/home/u/src/app/.git"},
		{"--mount", "type=bind,source=/home/u/wt/feat,target=/home/u/wt/feat"},
	} {
		if !contains(argv, want) {
			t.Errorf("up argv %v is missing %v", argv, want)
		}
	}
}

// A generated document already names both mounts, and docker refuses the whole
// run with "duplicate mount destination" when one target arrives twice.
func TestUpDoesNotMountAWorktreeForAGeneratedContainer(t *testing.T) {
	f := newFakePath(t)
	f.install(t, "devcontainer", "", 0)
	f.install(t, "docker", "", 0)

	c := testContainer()
	c.Source = "/home/u/wt/feat"
	c.ConfigPath = "/tmp/dev-config-x/.devcontainer/devcontainer.json"
	c.GeneratedConfig = `{"name":"feat"}`

	p := &Provider{}
	if err := p.Up(withWorktree(t.Context(), "/home/u/src/app/.git"), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	for _, argv := range f.argvAll(t, "devcontainer") {
		for i, a := range argv {
			if a == "--mount" && i+1 < len(argv) && strings.Contains(argv[i+1], "/src/app/.git") {
				t.Errorf("up passed --mount for a generated worktree container: %v", argv)
			}
		}
	}
}

// --mount is an `up` flag. On exec it is an unknown flag, which would be a
// usage error on every command run inside the container.
func TestExecNeverCarriesAWorktreeMount(t *testing.T) {
	c := testContainer()
	c.Source = "/home/u/wt/feat"

	argv := execArgs(withWorktree(t.Context(), "/home/u/src/app/.git"), c, []string{"true"}, provider.ExecOpts{})
	if slices.Contains(argv, "--mount") {
		t.Errorf("exec argv carries --mount: %v", argv)
	}
}
```

`withWorktree` and the exact `execArgs` signature come from Step 3 — write the
tests against them and let them fail until then. Check `execArgs`'s real
signature in `local.go` before writing the third test and match it.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/provider/local/... -run Worktree`
Expected: FAIL — `withWorktree` undefined.

- [ ] **Step 3: Carry the bind on the context**

The provider interface takes `model.Container`, and adding a worktree field to
that type would put a local-provider concern on a shared model that k8s also
reads. Carry it on the context instead — the same request-scoped shape the
codebase already uses for cancellation.

Add to `internal/provider/local/label.go`:

```go
// worktreeKey carries a worktree's git common directory through the provider
// call.
//
// On the context rather than on model.Container: the container row is shared
// with the k8s provider and with the store, and this is a local-provider
// mechanism that k8s explicitly does not implement. A request-scoped value for
// a request-scoped fact.
type worktreeKey struct{}

// WithWorktree marks a provider call as acting on a worktree checkout whose
// repository lives at commonDir.
func WithWorktree(ctx context.Context, commonDir string) context.Context {
	return context.WithValue(ctx, worktreeKey{}, commonDir)
}

// worktreeCommonDir returns the git common directory for this call, empty when
// the container is not worktree-backed.
func worktreeCommonDir(ctx context.Context) string {
	dir, _ := ctx.Value(worktreeKey{}).(string)
	return dir
}

// worktreeMountArgs renders the two bind mounts a project-owned worktree
// container needs, both at their identical host paths.
//
// Two, not one. The common directory resolves the checkout's `gitdir:` line;
// the checkout at its own path satisfies the repository's backlink, which
// `git worktree prune` — run by `git gc --auto` — deletes the registration for
// when it does not resolve. The devcontainer CLI would otherwise mount the
// checkout at /workspaces/<basename> only.
//
// Only for a project-owned container: a generated document names both mounts
// itself, and docker refuses the whole run with "duplicate mount destination"
// if either arrives twice.
func worktreeMountArgs(commonDir, workspace string) []string {
	if commonDir == "" {
		return nil
	}
	return []string{
		"--mount", fmt.Sprintf("type=bind,source=%s,target=%s", commonDir, commonDir),
		"--mount", fmt.Sprintf("type=bind,source=%s,target=%s", workspace, workspace),
	}
}
```

Add `"context"` to that file's imports.

In the test file, add the shim the tests call:

```go
func withWorktree(ctx context.Context, commonDir string) context.Context {
	return WithWorktree(ctx, commonDir)
}
```

- [ ] **Step 4: Pass the flags from `up`**

In `internal/provider/local/local.go`, immediately after the existing
`if c.PersistState && c.GeneratedConfig == ""` block, add:

```go
	// Same rule as the state volume above, and the same failure if it is
	// broken: a generated document already names both worktree binds, and a
	// second copy makes docker refuse the run.
	//
	// On up and never on exec, for the same reason: the devcontainer CLI
	// accepts --mount only here.
	if c.GeneratedConfig == "" {
		args = append(args, worktreeMountArgs(worktreeCommonDir(ctx), c.Source)...)
	}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/provider/local/... -v`
Expected: PASS, including every pre-existing test.

- [ ] **Step 6: Lint and commit**

```bash
make lint && make test
git add internal/provider/local
git commit -m "feat: local provider binds a worktree's repository"
```

---

### Task 6: `dev worktree create`

**Files:**
- Create: `internal/cli/worktree.go`
- Create: `internal/cli/worktree_test.go`
- Modify: `internal/cli/root.go:38-42` (register the command)
- Modify: `internal/cli/generate.go` (add `worktreeMount`)
- Modify: `internal/cli/resolve.go:129-144` area (add `worktreeContext`)

**Interfaces:**
- Consumes: `gitwt.CommonDir`/`Add`/`Remove`, `herdr.Available`/`Open`, `store.CreateWorktree`/`GetWorktree`, `dcgen.Mount{Host,Bind}`, `local.WithWorktree`.
- Produces:
  - `func newWorktreeCmd(a *app) *cobra.Command`
  - `func runWorktreeCreate(ctx context.Context, a *app, workspace, name string, opts worktreeOpts) error`
  - `func worktreeMount(repo, path string) dcgen.Mount`
  - `func (a *app) worktreeContext(ctx context.Context, workspace, container string) context.Context`
  - `type worktreeOpts struct { repo, branch, path, base string; noHerdr bool; create createOpts }`

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/worktree_test.go`:

```go
package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider/k8s"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// initRepo builds a repository with one commit and returns its root.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "."},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "f"}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// noHerdr keeps a host that has herdr installed from being asked about a
// checkout a unit test made.
func baseOpts(repo, path string) worktreeOpts {
	return worktreeOpts{
		repo: repo, branch: "feat", path: path, noHerdr: true,
		create: createOpts{generate: true, noStart: true, tools: []string{"yq"}},
	}
}

func TestWorktreeCreateWritesBothRows(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")

	if err := runWorktreeCreate(t.Context(), a, "", "feat", baseOpts(repo, path)); err != nil {
		t.Fatalf("worktree create: %v", err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "feat")
	if err != nil {
		t.Fatalf("container row: %v", err)
	}
	w, err := st.GetWorktree("ws", "feat")
	if err != nil {
		t.Fatalf("worktree row: %v", err)
	}
	if w.Branch != "feat" {
		t.Errorf("branch = %q, want feat", w.Branch)
	}
	// The stored path is what git actually created, resolved — not what was
	// typed. git records the resolved path in its backlink, and the mount has
	// to match what git wrote.
	if w.Path != c.Source {
		t.Errorf("worktree path %q and container source %q disagree", w.Path, c.Source)
	}
	// The repository is the common directory, which is what gets mounted.
	if !strings.HasSuffix(w.Repo, ".git") {
		t.Errorf("repo = %q, want git's common directory", w.Repo)
	}
	// The generated document carries both mounts, or git will not work inside.
	if !strings.Contains(c.GeneratedConfig, w.Repo) {
		t.Errorf("generated config does not bind the repository:\n%s", c.GeneratedConfig)
	}
}

// A checkout of a repository that ships its own .devcontainer takes the
// project's config untouched — dev may not write into it (invariant 9) — so it
// gets no generated document and the mounts come from `up --mount` instead.
func TestWorktreeCreateUsesTheProjectsOwnConfig(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)

	// Committed, so the checkout of the branch carries it.
	dc := filepath.Join(repo, ".devcontainer")
	if err := os.MkdirAll(dc, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dc, "devcontainer.json"),
		[]byte(`{"name":"project","image":"ubuntu"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "add devcontainer"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	opts := baseOpts(repo, filepath.Join(t.TempDir(), "wt"))
	opts.create.generate = false // the project ships one
	opts.create.tools = nil
	if err := runWorktreeCreate(t.Context(), a, "", "feat", opts); err != nil {
		t.Fatalf("worktree create: %v", err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "feat")
	if err != nil {
		t.Fatal(err)
	}
	if c.GeneratedConfig != "" {
		t.Errorf("a project-owned checkout got a generated document:\n%s", c.GeneratedConfig)
	}
	if c.ConfigPath == "" {
		t.Error("the project's own config was not recorded")
	}
	// The worktree row is still written: it is what `up` reads to build the
	// --mount flags, and what `remove` reads to find the checkout.
	if _, err := st.GetWorktree("ws", "feat"); err != nil {
		t.Errorf("no worktree row for a project-owned checkout: %v", err)
	}
}

// --generate over a project that ships its own config would shadow it, with no
// way to tell from outside which of the two built the container.
func TestWorktreeCreateRefusesToShadowAProjectConfig(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)

	dc := filepath.Join(repo, ".devcontainer")
	if err := os.MkdirAll(dc, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dc, "devcontainer.json"),
		[]byte(`{"name":"project","image":"ubuntu"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "add devcontainer"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	path := filepath.Join(t.TempDir(), "wt")
	err := runWorktreeCreate(t.Context(), a, "", "feat", baseOpts(repo, path))
	if err == nil {
		t.Fatal("--generate was accepted over a project's own config")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
	// And the checkout was rolled back, since the create failed.
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("the checkout at %s outlived a failed create", path)
	}
}

// The check the operator will hit most often.
func TestWorktreeCreateOutsideARepository(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := baseOpts("", filepath.Join(t.TempDir(), "wt"))
	// A directory that is not a repository, standing in for the working one.
	opts.repo = t.TempDir()

	err := runWorktreeCreate(t.Context(), a, "", "feat", opts)
	if err == nil {
		t.Fatal("worktree create succeeded outside a repository")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
	// The message has to name the way forward; git's own says ".git", which is
	// not a directory the operator chose.
	if !strings.Contains(err.Error(), "--repo") {
		t.Errorf("error %q does not mention --repo", err)
	}
}

// Nothing is written when the repository check fails.
func TestWorktreeCreateOutsideARepositoryWritesNothing(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := baseOpts(t.TempDir(), filepath.Join(t.TempDir(), "wt"))
	_ = runWorktreeCreate(t.Context(), a, "", "feat", opts)

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetContainer("ws", "feat"); err == nil {
		t.Error("a container row was written for a failed create")
	}
}

// An existing path is git's error, and it must arrive before anything is made.
func TestWorktreeCreateRefusesAnExistingPath(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := t.TempDir() // exists

	err := runWorktreeCreate(t.Context(), a, "", "feat", baseOpts(repo, path))
	if err == nil {
		t.Fatal("worktree create succeeded onto an existing directory")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
}

// --base with an existing branch reads as one intent and means another, so it
// is refused rather than ignored.
func TestWorktreeCreateRejectsBaseWithAnExistingBranch(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	cmd := exec.Command("git", "branch", "existing")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v\n%s", err, out)
	}

	opts := baseOpts(repo, filepath.Join(t.TempDir(), "wt"))
	opts.branch = "existing"
	opts.base = "HEAD"

	err := runWorktreeCreate(t.Context(), a, "", "feat", opts)
	if err == nil {
		t.Fatal("worktree create accepted --base for an existing branch")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
}

// Kubernetes has no host bind mounts, so the two pointers a worktree depends on
// cannot resolve there. Refusing is honest; seeding the files would look like
// it worked.
func TestWorktreeCreateRefusesK8s(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	if err := runProviderConfigure(a, "kp", "k8s", k8s.Config{Context: "c", Registry: "r"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if err := runWorkspaceInit(a, "kws", "kp", false); err != nil {
		t.Fatalf("workspace init: %v", err)
	}
	repo := initRepo(t)

	err := runWorktreeCreate(t.Context(), a, "kws", "feat",
		baseOpts(repo, filepath.Join(t.TempDir(), "wt")))
	if err == nil {
		t.Fatal("worktree create succeeded on a k8s workspace")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
	if !strings.Contains(err.Error(), "local") {
		t.Errorf("error %q does not say the provider must be local", err)
	}
}

// A failed create leaves no checkout behind.
func TestWorktreeCreateRollsBackTheCheckout(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")

	// A container of this name already exists, so the record write fails after
	// the checkout has been made.
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateContainer(model.Container{
		Name: "feat", WorkspaceName: "ws",
		SourceKind: model.SourceFolder, Source: "/elsewhere",
	}); err != nil {
		t.Fatal(err)
	}

	if err := runWorktreeCreate(t.Context(), a, "", "feat", baseOpts(repo, path)); err == nil {
		t.Fatal("worktree create succeeded with the name taken")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the checkout at %s outlived a failed create", path)
	}
}
```

`runProviderConfigure(a, name, kind string, flags k8s.Config) error` and
`runWorkspaceInit(a, name, provider string, sshForward bool) error` are the
real signatures; `seedWorkspace` in `generate_test.go` already calls both.

One thing this task does **not** need to change: `local.Provider.Remove`
branches on `SourceKind == model.SourceNone` before removing the workspace
volume, and a worktree container is `SourceFolder` — so it removes the state
volume and never the checkout. Verify that still holds rather than assuming it;
a worktree container that deleted its own checkout on `container remove` would
be the worst bug this feature could have.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cli/... -run Worktree`
Expected: FAIL — `runWorktreeCreate` undefined.

- [ ] **Step 3: Add the two helpers**

Append to `internal/cli/generate.go`:

```go
// worktreeMount describes how a worktree container is mounted.
//
// Two bind mounts, each at the identical host path. The checkout's .git file
// holds `gitdir: <repo>/worktrees/<name>` and the repository holds a backlink
// to the checkout — both absolute host paths, and `git gc --auto` prunes the
// registration when the backlink does not resolve.
func worktreeMount(repo, path string) dcgen.Mount {
	return dcgen.Mount{Host: path, Bind: repo}
}
```

Append to `internal/cli/resolve.go`:

```go
// worktreeContext marks a provider call as acting on a worktree checkout, so
// the local provider can pass the bind mounts a project-owned container needs.
//
// A lookup miss is not an error: most containers are not worktree-backed, and
// the context is simply left as it was.
func (a *app) worktreeContext(ctx context.Context, workspace, container string) context.Context {
	st, err := a.store()
	if err != nil {
		return ctx
	}
	w, err := st.GetWorktree(workspace, container)
	if err != nil {
		return ctx
	}
	return local.WithWorktree(ctx, w.Repo)
}
```

Add `"github.com/duy0611/dev-cli/internal/provider/local"` to `resolve.go`'s
imports if it is not already there.

- [ ] **Step 4: Write the command**

Create `internal/cli/worktree.go`:

```go
package cli

import (
	"context"
	"errors"
	"os"

	"github.com/duy0611/dev-cli/internal/dcconfig"
	"github.com/duy0611/dev-cli/internal/dcgen"
	"github.com/duy0611/dev-cli/internal/gitwt"
	"github.com/duy0611/dev-cli/internal/herdr"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/store"
	"github.com/duy0611/dev-cli/internal/xpath"
	"github.com/spf13/cobra"
)

func newWorktreeCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktree",
		Short: "Run a container on a git worktree checkout",
		// A group, not a command: see newContainerCmd for why both are needed.
		Args: noArgs(),
		RunE: groupRunE,
	}
	cmd.AddCommand(
		newWorktreeCreateCmd(a),
		newWorktreeListCmd(a),
		newWorktreeRemoveCmd(a),
	)
	return cmd
}

// worktreeOpts is what `worktree create` was asked for beyond the name. The
// container half is a createOpts so the two commands cannot drift.
type worktreeOpts struct {
	repo    string
	branch  string
	path    string
	base    string
	noHerdr bool
	create  createOpts
}

func newWorktreeCreateCmd(a *app) *cobra.Command {
	var (
		workspace string
		toolList  string
		opts      worktreeOpts
	)

	cmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Add a git worktree and run a container on it",
		Long: "Add a git worktree and run a container on it.\n\n" +
			"NAME names both the checkout's record and the container. The branch is\n" +
			"created from --base when it does not exist, and checked out as it is when\n" +
			"it does; --path says where, and nothing is written anywhere else.\n\n" +
			"git works inside the container: the repository is bind-mounted at its own\n" +
			"host path, which is what the checkout's .git file points at.\n\n" +
			"Local providers only. Kubernetes has no host bind mounts, so the paths a\n" +
			"worktree depends on cannot resolve there.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.create.tools = parseToolList(toolList)
			return runWorktreeCreate(cmd.Context(), a, workspace, args[0], opts)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().StringVar(&opts.repo, "repo", "",
		"repository to add the worktree to (default: the one holding the working directory)")
	cmd.Flags().StringVar(&opts.branch, "branch", "", "branch to check out or create")
	cmd.Flags().StringVar(&opts.path, "path", "", "where to create the checkout")
	cmd.Flags().StringVar(&opts.base, "base", "", "start point for a new branch")
	cmd.Flags().BoolVar(&opts.noHerdr, "no-herdr", false, "do not register the checkout with Herdr")
	cmd.Flags().BoolVar(&opts.create.noStart, "no-start", false, "record the container without starting it")
	cmd.Flags().BoolVar(&opts.create.generate, "generate", false,
		"generate a base Ubuntu configuration when the checkout ships none")
	cmd.Flags().StringVar(&toolList, "tools", "",
		"comma-separated tools to install in a generated container (see: dev container tools)")
	cmd.Flags().BoolVar(&opts.create.noPersistState, "no-persist-state", false,
		"do not give the container a volume for its agents' configuration")
	return cmd
}

func runWorktreeCreate(ctx context.Context, a *app, workspace, name string, opts worktreeOpts) error {
	if err := xpath.ValidateName(name); err != nil {
		return usageError(err)
	}
	if opts.branch == "" {
		return usageErrorf("--branch is required")
	}
	if opts.path == "" {
		// No default. One would have to invent a directory on the operator's
		// disk, and a checkout appearing somewhere they did not choose is worse
		// than one more flag.
		return usageErrorf("--path is required; it says where the checkout goes")
	}

	// First, before the store is opened or anything is created: this is the
	// most common way the command will be typed wrong, and every later step's
	// error would be more confusing than this one.
	//
	// The resolved repository is what is checked, not the working directory as
	// such — with --repo given, cwd is never consulted.
	dir := opts.repo
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return err
		}
	}
	repo, err := gitwt.CommonDir(ctx, dir)
	if err != nil {
		if errors.Is(err, gitwt.ErrNotRepo) {
			// Git's own message names ".git" rather than the directory the
			// operator was standing in, which is why this one is written.
			return usageErrorf("not a git repository: %s "+
				"(run dev worktree create from a repository, or pass --repo)", dir)
		}
		return err
	}

	wsName, err := a.workspaceName(workspace)
	if err != nil {
		return err
	}
	if err := a.requireLocalProvider(wsName); err != nil {
		return err
	}

	// Before the checkout, so a mistake costs nothing: git refuses an existing
	// directory anyway, and finding that out after the add would mean a
	// rollback for a failure that was knowable first.
	if _, err := os.Stat(opts.path); err == nil {
		return usageErrorf("%s already exists; --path must name a directory to create",
			xpath.Shorten(opts.path))
	}

	if err := gitwt.Add(ctx, repo, opts.path, opts.branch, opts.base); err != nil {
		return usageError(err)
	}

	// Resolved *after* the add, not before: git records the resolved path in
	// the repository's backlink — given a symlinked path it stores the real one
	// — and the mount has to match what git wrote, not what was typed.
	path, err := xpath.Resolve(opts.path)
	if err != nil {
		rollback(ctx, a, repo, opts.path)
		return usageError(err)
	}

	herdrWS := ""
	if !opts.noHerdr && herdr.Available(ctx) {
		// Best-effort throughout: Herdr is a view onto a checkout that exists
		// either way, so a failed view is not a failed command.
		if herdrWS, err = herdr.Open(ctx, path); err != nil {
			warnf(a, "%v (the checkout is fine; it is not in the Herdr sidebar)", err)
		}
	}

	if err := a.createWorktreeRows(ctx, wsName, name, repo, path, herdrWS, opts); err != nil {
		rollback(ctx, a, repo, path)
		return err
	}

	a.printf("worktree %s on branch %s at %s\n", name, opts.branch, xpath.Shorten(path))

	if opts.create.noStart {
		return nil
	}
	st, err := a.store()
	if err != nil {
		return err
	}
	c, err := st.GetContainer(wsName, name)
	if err != nil {
		return err
	}
	// Past this point the checkout is not rolled back: the operator has a
	// container worth keeping, and a start failure is something to retry rather
	// than something to undo.
	return a.start(a.worktreeContext(ctx, wsName, name), wsName, c)
}

// createWorktreeRows writes the container and the worktree that names it.
//
// The container first, because the worktree's foreign key points at it. Not one
// transaction: the store's methods each own their statement, and the cascade
// means a worktree row cannot outlive its container even if this returns
// halfway.
func (a *app) createWorktreeRows(ctx context.Context, wsName, name, repo, path, herdrWS string, opts worktreeOpts) error {
	configPath, err := dcconfig.Find(path)
	switch {
	case err == nil && opts.create.generate:
		return usageErrorf("%s already has a devcontainer config; --generate would shadow it",
			xpath.Shorten(path))
	case err != nil && !errors.Is(err, dcconfig.ErrNoConfig):
		return usageError(err)
	}

	generated := ""
	if errors.Is(err, dcconfig.ErrNoConfig) {
		generated, err = generatedConfigFor(name, opts.create.generate, opts.create.tools,
			worktreeMount(repo, path),
			stateFor(!opts.create.noPersistState, wsName, name), os.Stdin, a.out)
		if err != nil {
			return err
		}
		if generated == "" {
			return usageErrorf("no devcontainer config in %s "+
				"(--generate builds a base Ubuntu one; dev container tools lists what it can add)",
				xpath.Shorten(path))
		}
		configPath = ""
	}

	st, err := a.store()
	if err != nil {
		return err
	}
	c := model.Container{
		Name:            name,
		WorkspaceName:   wsName,
		SourceKind:      model.SourceFolder,
		Source:          path,
		ConfigPath:      configPath,
		GeneratedConfig: generated,
		PersistState:    !opts.create.noPersistState,
	}
	if err := st.CreateContainer(c); err != nil {
		if errors.Is(err, store.ErrExists) {
			return usageErrorf("container %s already exists in workspace %s", name, wsName)
		}
		return err
	}
	return st.CreateWorktree(model.Worktree{
		WorkspaceName:  wsName,
		ContainerName:  name,
		Repo:           repo,
		Branch:         opts.branch,
		Path:           path,
		HerdrWorkspace: herdrWS,
	})
}

// rollback removes a checkout a failed create had already made, so a command
// that reported an error leaves nothing behind.
//
// --force, because the checkout is seconds old and holds only what git put
// there. A failure here is warned about and swallowed: the command is already
// returning an error, and a second one would bury the first.
func rollback(ctx context.Context, a *app, repo, path string) {
	if err := gitwt.Remove(ctx, repo, path, true); err != nil {
		warnf(a, "could not remove the half-made checkout at %s: %v", xpath.Shorten(path), err)
	}
}

// requireLocalProvider refuses a workspace whose provider is not local.
//
// Kubernetes has no host bind mounts, so neither of the two absolute paths a
// worktree depends on can resolve in a pod. Seeding the files would deliver a
// checkout whose every git command fails, which looks like it worked.
func (a *app) requireLocalProvider(workspace string) error {
	st, err := a.store()
	if err != nil {
		return err
	}
	ws, err := st.GetWorkspace(workspace)
	if err != nil {
		return err
	}
	rec, err := st.GetProvider(ws.ProviderName)
	if err != nil {
		return err
	}
	if rec.Kind != model.KindLocal {
		return usageErrorf("workspace %s runs on a %s provider; worktrees need a local one "+
			"(a pod has no host bind mounts, so the repository cannot be reached)",
			workspace, rec.Kind)
	}
	return nil
}
```

`newWorktreeListCmd` and `newWorktreeRemoveCmd` are Task 7. To keep this task
compiling on its own, add them as minimal stubs now and fill them in there:

```go
func newWorktreeListCmd(a *app) *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List worktree containers",
		Args: noArgs(), RunE: func(cmd *cobra.Command, args []string) error {
			return usageErrorf("not implemented yet")
		}}
}

func newWorktreeRemoveCmd(a *app) *cobra.Command {
	return &cobra.Command{Use: "remove NAME", Short: "Remove a worktree and its container",
		Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			return usageErrorf("not implemented yet")
		}}
}
```

- [ ] **Step 5: Register the command**

In `internal/cli/root.go`, add `newWorktreeCmd(a),` to the `root.AddCommand`
call after `newContainerCmd(a),`.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/cli/... -v -run Worktree`
Expected: PASS, all nine tests.

- [ ] **Step 7: Lint and commit**

```bash
make lint && make test
git add internal/cli
git commit -m "feat: dev worktree create adds a checkout and runs a container on it"
```

---

### Task 7: `dev worktree list` and `remove`

**Files:**
- Modify: `internal/cli/worktree.go` (replace the two stubs)
- Modify: `internal/cli/worktree_test.go` (append)

**Interfaces:**
- Consumes: everything from Task 6, plus `gitwt.List`, `herdr.Close`, `store.ListWorktrees`/`DeleteWorktree`.
- Produces: `func runWorktreeRemove(ctx context.Context, a *app, workspace, name string, force bool) error`

- [ ] **Step 1: Write the failing tests**

Append to `internal/cli/worktree_test.go`:

```go
func TestWorktreeRemoveTakesBoth(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	if err := runWorktreeCreate(t.Context(), a, "", "feat", baseOpts(repo, path)); err != nil {
		t.Fatal(err)
	}

	if err := runWorktreeRemove(t.Context(), a, "", "feat", false); err != nil {
		t.Fatalf("worktree remove: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the checkout at %s survived remove", path)
	}
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetContainer("ws", "feat"); err == nil {
		t.Error("the container row survived remove")
	}
	if _, err := st.GetWorktree("ws", "feat"); err == nil {
		t.Error("the worktree row survived remove")
	}
}

// The branch is the work; the checkout is scaffolding.
func TestWorktreeRemoveKeepsTheBranch(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	if err := runWorktreeCreate(t.Context(), a, "", "feat",
		baseOpts(repo, filepath.Join(t.TempDir(), "wt"))); err != nil {
		t.Fatal(err)
	}
	if err := runWorktreeRemove(t.Context(), a, "", "feat", false); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "branch", "--list", "feat")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "feat") {
		t.Error("remove deleted the branch")
	}
}

// The row is written last, so a refusal leaves the record intact and the
// command retryable.
func TestWorktreeRemoveKeepsTheRowWhenGitRefuses(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	if err := runWorktreeCreate(t.Context(), a, "", "feat", baseOpts(repo, path)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "unsaved"), []byte("work"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runWorktreeRemove(t.Context(), a, "", "feat", false); err == nil {
		t.Fatal("remove succeeded on a dirty checkout")
	}
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetWorktree("ws", "feat"); err != nil {
		t.Errorf("the worktree row was dropped despite the refusal: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the dirty checkout was destroyed: %v", err)
	}

	if err := runWorktreeRemove(t.Context(), a, "", "feat", true); err != nil {
		t.Errorf("remove --force on a dirty checkout: %v", err)
	}
}

func TestWorktreeRemoveUnknown(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	err := runWorktreeRemove(t.Context(), a, "", "nope", false)
	if got := exitCodeOf(err); got != exitNotFound {
		t.Errorf("exit code = %d, want %d", got, exitNotFound)
	}
}

// A container that is not worktree-backed is not a worktree to remove.
func TestWorktreeRemoveRefusesAPlainContainer(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateContainer(model.Container{
		Name: "plain", WorkspaceName: "ws",
		SourceKind: model.SourceFolder, Source: "/projects/plain",
	}); err != nil {
		t.Fatal(err)
	}

	err = runWorktreeRemove(t.Context(), a, "", "plain", false)
	if got := exitCodeOf(err); got != exitNotFound {
		t.Errorf("exit code = %d, want %d", got, exitNotFound)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cli/... -run WorktreeRemove`
Expected: FAIL — `runWorktreeRemove` undefined.

- [ ] **Step 3: Replace the two stubs**

In `internal/cli/worktree.go`, replace `newWorktreeListCmd` and
`newWorktreeRemoveCmd` with:

```go
// --- list ---------------------------------------------------------------------

func newWorktreeListCmd(a *app) *cobra.Command {
	var (
		workspace string
		all       bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List worktree containers and their checkouts",
		Args:  noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorktreeList(cmd.Context(), a, workspace, all)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().BoolVar(&all, "all", false, "list every workspace's worktrees")
	return cmd
}

func runWorktreeList(ctx context.Context, a *app, workspace string, all bool) error {
	st, err := a.store()
	if err != nil {
		return err
	}

	scope := ""
	if !all {
		if scope, err = a.workspaceName(workspace); err != nil {
			return err
		}
	}

	rows, err := st.ListWorktrees(scope)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		a.printf("no worktrees; run: dev worktree create NAME --branch B --path P\n")
		return nil
	}

	// Whether git still knows about a checkout is read from git every time,
	// for the reason live container status is: an operator can run
	// `git worktree remove` themselves, and a stored answer would be wrong
	// from that moment on.
	known := map[string]bool{}
	checked := map[string]bool{}
	live := func(w model.Worktree) string {
		if !checked[w.Repo] {
			checked[w.Repo] = true
			entries, err := gitwt.List(ctx, w.Repo)
			if err != nil {
				return "?"
			}
			for _, e := range entries {
				known[e.Path] = true
			}
		}
		if known[w.Path] {
			return "ok"
		}
		return "gone"
	}

	return a.table(func(out io.Writer) {
		header(out, "WORKSPACE", "NAME", "BRANCH", "CHECKOUT", "PATH")
		for _, wt := range rows {
			row(out, wt.WorkspaceName, wt.ContainerName, wt.Branch,
				live(wt), xpath.Shorten(wt.Path))
		}
	})
}

// --- remove -------------------------------------------------------------------

func newWorktreeRemoveCmd(a *app) *cobra.Command {
	var (
		workspace string
		force     bool
	)

	cmd := &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove a worktree's container and its checkout",
		Long: "Remove a worktree's container and its checkout.\n\n" +
			"The container goes first, then the checkout. git refuses a checkout\n" +
			"holding modified or untracked files, and that refusal leaves the record\n" +
			"in place so the command can be retried; --force passes it through.\n\n" +
			"The branch is never deleted.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorktreeRemove(cmd.Context(), a, workspace, args[0], force)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().BoolVar(&force, "force", false, "remove the checkout even with uncommitted work in it")
	return cmd
}

func runWorktreeRemove(ctx context.Context, a *app, workspace, name string, force bool) error {
	wsName, err := a.workspaceName(workspace)
	if err != nil {
		return err
	}
	st, err := a.store()
	if err != nil {
		return err
	}

	w, err := st.GetWorktree(wsName, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFoundErrorf("no such worktree: %s (workspace %s); "+
				"dev container remove takes a container that has no checkout", name, wsName)
		}
		return err
	}

	// The container first, so a --force removal of a dirty checkout is not
	// racing an agent still writing into it.
	t, err := a.resolve(workspace, name)
	if err != nil {
		return err
	}
	defer t.release()
	if err := t.provider.Remove(a.worktreeContext(ctx, wsName, name), t.container); err != nil {
		return err
	}
	if err := st.DeleteContainer(wsName, name); err != nil {
		return err
	}

	// Then git. A refusal here has already cost the container, which is
	// replaceable; the checkout it is protecting is not.
	if err := gitwt.Remove(ctx, w.Repo, w.Path, force); err != nil {
		return usageError(err)
	}

	// Herdr last and best-effort, for the same reason as on create.
	if err := herdr.Close(ctx, w.HerdrWorkspace); err != nil {
		warnf(a, "%v (the checkout is gone; its Herdr workspace may linger)", err)
	}

	// The row is already gone — DeleteContainer cascaded it away — so there is
	// nothing left to delete here. Said out loud because its absence looks
	// like an omission.
	a.printf("worktree %s removed; branch %s kept\n", name, w.Branch)
	return nil
}
```

Add `"io"` to the imports. `row` and `header` come from `print.go`; the loop
variable is named `wt` rather than `row` so it does not shadow that helper.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/cli/... -v -run Worktree`
Expected: PASS, all fourteen tests.

- [ ] **Step 5: Lint and commit**

```bash
make lint && make test
git add internal/cli
git commit -m "feat: dev worktree list and remove"
```

---

### Task 8: the mount survives `rebuild --tools`

**Files:**
- Modify: `internal/cli/generate.go:148-196` (`rewriteGeneratedTools`)
- Modify: `internal/cli/container.go:244-278` (the `rebuild` and `start` paths pass the worktree context)
- Test: `internal/cli/worktree_test.go` (append)

**Interfaces:**
- Consumes: `worktreeMount`, `a.worktreeContext`, `store.GetWorktree`.
- Produces: nothing new.

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/worktree_test.go`:

```go
// rewriteGeneratedTools re-renders the whole document from the tool list and
// the stored row, so anything not derivable from those is destroyed by the next
// `rebuild --tools`. That is the trap invariant 10 records for the state
// volume, and the .git bind is a third instance of it: dropped here, the next
// up starts a container whose git does not work, with no warning.
func TestRebuildToolsKeepsTheWorktreeMount(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)

	if err := runWorktreeCreate(t.Context(), a, "", "feat",
		baseOpts(repo, filepath.Join(t.TempDir(), "wt"))); err != nil {
		t.Fatal(err)
	}
	if err := rewriteGeneratedTools(a, "", "feat", []string{"+jq"}); err != nil {
		t.Fatalf("rebuild --tools: %v", err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "feat")
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.GetWorktree("ws", "feat")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.GeneratedConfig, w.Repo) {
		t.Errorf("rebuild --tools dropped the repository bind:\n%s", c.GeneratedConfig)
	}
	if !strings.Contains(c.GeneratedConfig, `"workspaceFolder": "`+w.Path+`"`) {
		t.Errorf("rebuild --tools dropped the checkout's host path:\n%s", c.GeneratedConfig)
	}
	// And the tool change actually happened, or the test proves nothing.
	if !strings.Contains(c.GeneratedConfig, "jq") {
		t.Errorf("rebuild --tools did not add jq:\n%s", c.GeneratedConfig)
	}
}

// And none is invented for a container that never had one.
func TestRebuildToolsInventsNoWorktreeMount(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	folder := t.TempDir()
	if err := runContainerCreate(t.Context(), a, "", "plain", folder,
		createOpts{generate: true, noStart: true, tools: []string{"yq"}}); err != nil {
		t.Fatal(err)
	}
	if err := rewriteGeneratedTools(a, "", "plain", []string{"+jq"}); err != nil {
		t.Fatalf("rebuild --tools: %v", err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "plain")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.GeneratedConfig, "workspaceMount") {
		t.Errorf("a plain container grew a workspaceMount:\n%s", c.GeneratedConfig)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cli/... -run RebuildTools`
Expected: FAIL — the rendered document has lost the bind and the host path.

- [ ] **Step 3: Derive the mount from the row**

In `rewriteGeneratedTools`, replace the mount-deriving block with:

```go
	// Derived from the stored rows, not from a flag: a rebuild must not be able
	// to change what a container is mounted on. This re-renders the whole
	// document, so any mount not rebuilt here is destroyed — the failure
	// invariant 10 records for the state volume, and the same one for a
	// worktree's binds.
	var mount dcgen.Mount
	switch {
	case t.container.SourceKind == model.SourceNone:
		mount = folderlessMount(t.workspace.Name, name)
	default:
		st0, err := a.store()
		if err != nil {
			return err
		}
		// Not re-derived from the checkout on disk: one the operator has
		// already deleted by hand would answer nothing, and the rebuild would
		// then quietly produce a container whose git does not work.
		if w, err := st0.GetWorktree(t.workspace.Name, name); err == nil {
			mount = worktreeMount(w.Repo, w.Path)
		}
	}
```

- [ ] **Step 4: Pass the worktree context on rebuild and start**

In `internal/cli/container.go`, in `newContainerRebuildCmd`'s `RunE`, replace
the `t.provider.Rebuild(cmd.Context(), ...)` call with:

```go
			// The bind mounts a project-owned worktree container needs are
			// passed by the provider from this context; without it a rebuild
			// starts a container whose git does not work.
			ctx := a.worktreeContext(cmd.Context(), t.workspace.Name, t.container.Name)
			if err := t.provider.Rebuild(ctx, t.container, environ, noCache); err != nil {
```

In `newContainerStartCmd`'s `RunE`, replace the `a.start(cmd.Context(), ...)`
call with:

```go
			return a.start(a.worktreeContext(cmd.Context(), t.workspace.Name, t.container.Name),
				t.workspace.Name, t.container)
```

Then grep for every other `provider.Up`, `provider.Rebuild` and `provider.Exec`
call in `internal/cli` and give each the same treatment — `start-agent`,
`shell`, `exec` and `sync` all reach a container that may be worktree-backed.
`Exec` does not use the mounts, but passing the context uniformly is what keeps
a later reader from having to work out which calls do.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/cli/... -v`
Expected: PASS, everything.

- [ ] **Step 6: Lint and commit**

```bash
make lint && make test
git add internal/cli
git commit -m "fix: rebuild --tools keeps a worktree's mounts"
```

---

### Task 9: `container remove` warns about a leftover checkout

**Files:**
- Modify: `internal/cli/container.go:441-463` (`runContainerRemove`)
- Test: `internal/cli/worktree_test.go` (append)

**Interfaces:**
- Consumes: `store.GetWorktree`.
- Produces: nothing new.

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/worktree_test.go`:

```go
// `container remove` on a worktree-backed container takes the container and
// leaves the checkout, because the cascade drops the row that recorded it. A
// warning rather than a refusal: an operator who wants the container gone and
// the checkout kept has no other way to say so, and the warning is what stops
// the checkout becoming an orphan nobody can explain.
func TestContainerRemoveWarnsAboutTheCheckout(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	if err := runWorktreeCreate(t.Context(), a, "", "feat", baseOpts(repo, path)); err != nil {
		t.Fatal(err)
	}

	warnings := captureStderr(t, func() {
		if err := runContainerRemove(t.Context(), a, "", "feat", true); err != nil {
			t.Fatalf("container remove: %v", err)
		}
	})

	if !strings.Contains(warnings, path) {
		t.Errorf("warning %q does not name the leftover checkout", warnings)
	}
	if !strings.Contains(warnings, "worktree remove") {
		t.Errorf("warning %q does not name the command that cleans it up", warnings)
	}
	// The checkout is still there: the warning is a warning, not an action.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("container remove deleted the checkout: %v", err)
	}
}
```

Add the helper at the bottom of the same file:

```go
// captureStderr collects what warnf writes, which goes to os.Stderr directly
// rather than through the app's writer — it is deliberately kept out of a
// piped list.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()

	fn()
	w.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
```

Add `"bytes"` to the imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cli/... -run ContainerRemoveWarns`
Expected: FAIL — no warning naming the checkout.

- [ ] **Step 3: Warn before deleting the row**

In `runContainerRemove`, immediately before the `st.DeleteContainer` call, add:

```go
	// Before the delete, because the cascade takes the worktree row with it and
	// there would be nothing left to read afterwards.
	//
	// A warning rather than a refusal: `container remove` can perfectly well
	// remove this container, and an operator who wants the container gone and
	// the checkout kept has no other way to say so. What they must not have is
	// a checkout on disk that nothing records.
	if w, err := st.GetWorktree(t.workspace.Name, name); err == nil {
		warnf(a, "%s was a worktree; the checkout at %s stays on disk "+
			"(dev worktree remove %s would have taken both)",
			name, xpath.Shorten(w.Path), name)
	}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/cli/... -v -run ContainerRemove`
Expected: PASS.

- [ ] **Step 5: Lint and commit**

```bash
make lint && make test
git add internal/cli
git commit -m "feat: container remove names a worktree it leaves behind"
```

---

### Task 10: smoke test

**Files:**
- Create: `test/smoke/worktree_test.go`

**Interfaces:**
- Consumes: the whole command surface, through the built binary the existing smoke harness drives.
- Produces: nothing.

Read `test/smoke/smoke_test.go` first and reuse its harness — how it builds the
binary, sets `DEV_STATE`, skips without an engine, and cleans up. The code below
names those helpers generically; match the real ones.

- [ ] **Step 1: Write the test**

Create `test/smoke/worktree_test.go`:

```go
//go:build smoke

package smoke

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorktreeGitWorksInsideTheContainer is the only test that can prove the
// point of this feature. The unit tests prove the mount arguments are built
// correctly, which is a different claim: git resolving two absolute host paths
// from inside a container is not something a stub can answer.
func TestWorktreeGitWorksInsideTheContainer(t *testing.T) {
	env := setupSmoke(t) // the existing harness: binary, DEV_STATE, engine check

	repo := t.TempDir()
	gitRun(t, repo, "init", "-q", ".")
	gitRun(t, repo, "config", "user.email", "smoke@example.com")
	gitRun(t, repo, "config", "user.name", "smoke")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "f")
	gitRun(t, repo, "commit", "-qm", "init")

	path := filepath.Join(t.TempDir(), "wt")
	env.run(t, "worktree", "create", "feat",
		"--repo", repo, "--branch", "feat", "--path", path,
		"--generate", "--tools", "git", "--no-herdr")
	t.Cleanup(func() { env.run(t, "worktree", "remove", "feat", "--force") })

	// The claim: git works inside. A commit made in the container is a commit
	// the host repository can see, because the object store is one bind mount.
	env.run(t, "container", "exec", "feat", "--",
		"sh", "-c", "cd "+path+" && echo inside > made-inside && "+
			"git add made-inside && "+
			"git -c user.email=c@example.com -c user.name=container commit -qm 'from the container'")

	out := gitOut(t, repo, "log", "--all", "--format=%s")
	if !strings.Contains(out, "from the container") {
		t.Errorf("the host repository cannot see the container's commit:\n%s", out)
	}

	// And the registration survived. `git gc --auto` runs `worktree prune`,
	// which deletes a worktree whose backlink does not resolve — so a container
	// missing the second bind mount would destroy this from the inside.
	list := gitOut(t, repo, "worktree", "list")
	if !strings.Contains(list, path) {
		t.Errorf("the worktree registration was pruned away:\n%s", list)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
```

- [ ] **Step 2: Confirm it is excluded from `make test`**

Run: `make test`
Expected: PASS, and the smoke file is not compiled — the `smoke` build tag
keeps it out.

- [ ] **Step 3: Run it if an engine is available**

Run: `make smoke`
Expected: PASS, or SKIP on a host with no engine or no devcontainer CLI. If it
fails with `not a git repository` from inside the container, the second bind
mount is missing — that is Task 5.

- [ ] **Step 4: Commit**

```bash
make lint && make test
git add test/smoke
git commit -m "test: smoke test that git works inside a worktree container"
```

---

### Task 11: documentation

**Files:**
- Modify: `docs/USAGE.md` (new walkthrough after "Run an agent on a project"; new `### worktree` in the command reference)
- Modify: `CLAUDE.md` (new invariant)

**Interfaces:** none.

- [ ] **Step 1: Add the walkthrough**

Insert into `docs/USAGE.md` after the "Run an agent on a project" section,
matching the surrounding prose style — second person, a real transcript, no
bullet lists of flags:

````markdown
### Work a branch in its own checkout

A worktree gives a branch its own directory, so an agent can work on it without
disturbing what you have open. `dev worktree` makes the checkout and the
container together, and removes them together.

```
❯ dev worktree create fix-header --branch fix-header --path ~/wt/fix-header --generate
worktree fix-header on branch fix-header at ~/wt/fix-header
container fix-header is running
```

Run it from inside the repository, or name one with `--repo`. `--path` has no
default: nothing appears on your disk in a place you did not choose.

git works inside the container. A worktree's `.git` is a file pointing at an
absolute path in the repository, so `dev` bind-mounts the repository at that
same path — commits you make inside are commits the repository can see, sharing
one object store.

```
❯ dev container start-agent fix-header
```

When the branch is done:

```
❯ dev worktree remove fix-header
worktree fix-header removed; branch fix-header kept
```

The branch stays. git refuses to remove a checkout with uncommitted work in it,
and so does this — pass `--force` when you mean it.

```
❯ dev worktree list
WORKSPACE  NAME        BRANCH      CHECKOUT  PATH
default    fix-header  fix-header  ok        ~/wt/fix-header
```

`CHECKOUT` is read from git every time. `gone` means the checkout was removed
outside `dev`; `dev worktree remove` still cleans up the container and the
record.

Local providers only. A pod has no host bind mounts, so the paths a worktree
depends on cannot resolve there.
````

- [ ] **Step 2: Add the command reference**

Insert a `### worktree` section into the command reference, after `### container`,
in the same format the neighbouring sections use — every command, every flag.
Document `create` (`--repo`, `--branch`, `--path`, `--base`, `--no-herdr`,
`--generate`, `--tools`, `--no-start`, `--no-persist-state`, `--workspace`),
`list` (`--all`, `--workspace`), and `remove` (`--force`, `--workspace`). Copy
each flag's one-line description verbatim from the `cmd.Flags()` calls in
`internal/cli/worktree.go` so the two cannot drift.

- [ ] **Step 3: Add the invariant**

Append to the invariants list in `CLAUDE.md`, as item 11:

````markdown
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

    Two routes as ever, and never both: a generated document names both mounts,
    a project-owned container gets them from `devcontainer up --mount` — `up`
    only, never `execArgs`. And both are rebuilt from the `worktrees` row in
    `rewriteGeneratedTools`, because that function re-renders the whole document
    and drops anything it cannot derive, exactly as in invariant 10.

    k8s refuses worktrees outright: a pod has no host bind mounts, and a
    streamed copy of the object store would drift from the host with nothing to
    push it back.
````

- [ ] **Step 4: Check the docs against the code**

Run: `dist/dev worktree create --help` and compare every flag against the
reference section. A change to a command's surface is not finished until
`docs/USAGE.md` matches it.

```bash
make build
DEV_STATE=$(mktemp -d) dist/dev worktree create --help
DEV_STATE=$(mktemp -d) dist/dev worktree remove --help
DEV_STATE=$(mktemp -d) dist/dev worktree list --help
```

- [ ] **Step 5: Commit**

```bash
make lint && make test
git add docs/USAGE.md CLAUDE.md
git commit -m "docs: dev worktree"
```

---

## Verification

After Task 11, confirm the whole thing from a clean state:

```bash
make lint && make test
make build

# A real repository, a real checkout, no engine needed up to `create --no-start`.
export DEV_STATE=$(mktemp -d)
dist/dev provider configure local --kind local
dist/dev workspace init demo --provider local
cd /path/to/some/repo
dist/dev worktree create scratch --branch scratch --path /tmp/wt-scratch \
  --generate --tools git --no-start --no-herdr
dist/dev worktree list
dist/dev worktree remove scratch
git worktree list   # the checkout is gone
git branch --list scratch   # the branch is not
```

Then `make smoke` on a host with an engine, which is what proves git works
inside the container.
