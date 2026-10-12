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

// TestSmokeWorktree checks git works inside a worktree container. Only a real
// container can show git resolving the two absolute host paths; unit tests
// check the mount arguments alone.
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
	// An ignored file the manifest carries, to show a host write reaches the
	// bind-mounted checkout.
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("SMOKE=carried\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".worktreeinclude"), []byte(".env\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "wt")
	// The container goes first; worktree remove refuses while one is on the
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

	// Resolve the path before comparing: on macOS /var is a symlink to
	// /private/var, and the container has no such symlink.
	resolved, err := xpath.Resolve(path)
	if err != nil {
		t.Fatalf("resolving the checkout path: %v", err)
	}

	// Git reads inside but cannot commit: the worktree's index lives under the
	// read-only common directory.
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

	// A host commit is visible inside at once, through the same mount.
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

	// The registration must survive: `git gc --auto` prunes a worktree whose
	// backlink does not resolve, which the second bind mount prevents. Compare
	// against the resolved path, which is what git wrote to the backlink.
	list := gitOut(t, repo, "worktree", "list")
	if !strings.Contains(list, resolved) {
		t.Errorf("the worktree registration was pruned away:\n%s", list)
	}

	// Refused while the container is on it, allowed once it is removed.
	if out, err := exec.Command(bin, "worktree", "remove", name, "--workspace", ws).CombinedOutput(); err == nil {
		t.Errorf("worktree remove succeeded with a container still on it:\n%s", out)
	}
	dev("container", "remove", name, "--workspace", ws)
	// No --force: carried files are ignored, so git removes them with the
	// checkout.
	dev("worktree", "remove", name, "--workspace", ws)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("worktree remove left the checkout at %s behind", path)
	}
	list = gitOut(t, repo, "worktree", "list")
	if strings.Contains(list, resolved) {
		t.Errorf("worktree remove did not deregister the checkout:\n%s", list)
	}
	// The branch is kept; it holds the work.
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
