package gitwt

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
