//go:build smoke

package smoke

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/xpath"
)

// TestSmokeWorktree is the only test that can prove the point of this
// feature. The unit tests prove the mount arguments are built correctly,
// which is a different claim: git resolving two absolute host paths from
// inside a container is not something a stub can answer.
func TestSmokeWorktree(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker", "git")

	state := t.TempDir()
	t.Setenv("DEV_STATE", state)

	bin := buildBinary(t)
	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	const (
		name = "dev-smoke-worktree"
		ws   = "dev-smoke-worktree-ws"
	)

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
	t.Cleanup(func() {
		_ = exec.Command(bin, "worktree", "remove", name, "--workspace", ws, "--force").Run()
		_ = exec.Command(bin, "container", "remove", name, "--workspace", ws, "--force").Run()
	})

	dev("provider", "configure", "dev-smoke-worktree-local", "--kind", "local")
	dev("workspace", "init", ws, "--provider", "dev-smoke-worktree-local")

	t.Log("creating the worktree; the first run pulls a base image")
	dev("worktree", "create", name, "--workspace", ws,
		"--repo", repo, "--branch", "feat", "--path", path,
		"--generate", "--tools", "yq", "--no-herdr")

	// dev bind-mounts the checkout at its *resolved* path (invariant 2 and the
	// gitdir invariant both turn on this): xpath.Resolve runs after the add and
	// stores the real directory, given a symlinked one. On macOS /var is a
	// symlink to /private/var and t.TempDir() answers under /var, so asserting
	// against the path as typed would cd into a directory that exists only via
	// a symlink the container does not have — this is the same class of bug
	// invariant 2 exists to catch, here in the test rather than in dev.
	resolved, err := xpath.Resolve(path)
	if err != nil {
		t.Fatalf("resolving the checkout path: %v", err)
	}

	// The claim: git works inside. A commit made in the container is a commit
	// the host repository can see, because the object store is one bind mount.
	dev("container", "exec", name, "--workspace", ws, "--",
		"sh", "-c", "cd "+resolved+" && echo inside > made-inside && "+
			"git add made-inside && "+
			"git -c user.email=c@example.com -c user.name=container commit -qm 'from the container'")

	out := gitOut(t, repo, "log", "--all", "--format=%s")
	if !strings.Contains(out, "from the container") {
		t.Errorf("the host repository cannot see the container's commit:\n%s", out)
	}

	// And the registration survived. `git gc --auto` runs `worktree prune`,
	// which deletes a worktree whose backlink does not resolve — so a container
	// missing the second bind mount would destroy this from the inside.
	//
	// Checked against the resolved path: git's own listing reports what it
	// wrote to the backlink, which is the resolved directory, not the string
	// --path was given.
	list := gitOut(t, repo, "worktree", "list")
	if !strings.Contains(list, resolved) {
		t.Errorf("the worktree registration was pruned away:\n%s", list)
	}

	dev("worktree", "remove", name, "--workspace", ws)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("worktree remove left the checkout at %s behind", path)
	}
	list = gitOut(t, repo, "worktree", "list")
	if strings.Contains(list, resolved) {
		t.Errorf("worktree remove did not deregister the checkout:\n%s", list)
	}
	// The branch stays: it is the work, not the scaffolding.
	branches := gitOut(t, repo, "branch", "--list", "feat")
	if !strings.Contains(branches, "feat") {
		t.Error("worktree remove deleted the branch")
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
