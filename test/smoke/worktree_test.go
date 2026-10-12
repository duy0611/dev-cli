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
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "f", ".gitignore")
	gitRun(t, repo, "commit", "-qm", "init")
	// An ignored file the manifest asks for, so the copy is proven to land
	// somewhere the container can read — the checkout is a bind mount, and a
	// file written to it on the host has to be what the container sees.
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("SMOKE=carried\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".worktreeinclude"), []byte(".env\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "wt")
	// The container first: worktree remove refuses while one is on the
	// checkout.
	t.Cleanup(func() {
		_ = exec.Command(bin, "container", "remove", name, "--workspace", ws, "--force").Run()
		_ = exec.Command(bin, "worktree", "remove", name, "--workspace", ws, "--force").Run()
	})

	dev("provider", "configure", "dev-smoke-worktree-local", "--kind", "local")
	dev("workspace", "init", ws, "--provider", "dev-smoke-worktree-local")

	dev("worktree", "create", name, "--workspace", ws,
		"--repo", repo, "--branch", "feat", "--path", path, "--no-herdr")
	t.Log("creating a container on the worktree; the first run pulls a base image")
	dev("container", "create", name, "--workspace", ws, "--folder", path,
		"--generate", "--tools", "yq")

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

	// The claim: git works inside, read-only. The container is guarded by
	// default, so it reads the repository through the worktree's .git file and
	// the repository mounted at its own path — and a commit fails, because the
	// worktree's index lives under the read-only common directory.
	if out := dev("container", "exec", name, "--workspace", ws, "--",
		"sh", "-c", "cd "+resolved+" && git log --oneline -1"); strings.TrimSpace(out) == "" {
		t.Error("git log inside the worktree container printed nothing")
	}
	if out, err := exec.Command(bin, "container", "exec", name, "--workspace", ws, "--",
		"sh", "-c", "cd "+resolved+" && echo inside > made-inside && git add made-inside").CombinedOutput(); err == nil {
		t.Errorf("git add succeeded inside a guarded worktree container:\n%s", out)
	} else if !strings.Contains(string(out), "Read-only file system") {
		t.Errorf("git add failed for a reason other than the read-only repository:\n%s", out)
	}
	_ = os.Remove(filepath.Join(path, "made-inside"))

	env := dev("container", "exec", name, "--workspace", ws, "--",
		"cat", filepath.Join(resolved, ".env"))
	if !strings.Contains(env, "SMOKE=carried") {
		t.Errorf("the carried .env is not readable inside the container: %q", env)
	}

	// A commit made on the host is one the container reads at once, through
	// the same read-only mount.
	gitIn := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=h@example.com", "-c", "user.name=host"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitIn(path, "commit", "-q", "--allow-empty", "-m", "from the host")
	if out := dev("container", "exec", name, "--workspace", ws, "--",
		"sh", "-c", "cd "+resolved+" && git log --format=%s -1"); !strings.Contains(out, "from the host") {
		t.Errorf("the container does not see the host's commit: %q", out)
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

	// Refused while the container is on it, then removed once it is not.
	if out, err := exec.Command(bin, "worktree", "remove", name, "--workspace", ws).CombinedOutput(); err == nil {
		t.Errorf("worktree remove succeeded with a container still on it:\n%s", out)
	}
	dev("container", "remove", name, "--workspace", ws)
	// No --force, deliberately: everything carried over is ignored, and git
	// removes ignored files with the checkout. A copy that blocked removal
	// would turn every seeded worktree into one that needs forcing.
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
