package gitguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// No signing, no global config: a test repository must not depend on the
	// operator's git setup, nor on an ssh agent being reachable.
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func repo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", ".")
	return dir
}

func TestLayoutOfARepositoryRoot(t *testing.T) {
	root := repo(t)
	l, ok := LayoutOf(root)
	if !ok {
		t.Fatal("no layout for a repository")
	}
	if l.GitDir != filepath.Join(root, ".git") {
		t.Errorf("GitDir = %q", l.GitDir)
	}
	if want := "/workspaces/" + filepath.Base(root) + "/.git"; l.ContainerGitDir != want {
		t.Errorf("ContainerGitDir = %q, want %q", l.ContainerGitDir, want)
	}
}

// The CLI mounts the repository's root, not the folder it was handed, when the
// folder is inside one (mount-workspace-git-root, on by default) — so .git
// lands under the root's basename, not the folder's.
func TestLayoutOfASubfolderFollowsTheGitRoot(t *testing.T) {
	root := repo(t)
	sub := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	l, ok := LayoutOf(sub)
	if !ok {
		t.Fatal("no layout for a subfolder of a repository")
	}
	if want := "/workspaces/" + filepath.Base(root) + "/.git"; l.ContainerGitDir != want {
		t.Errorf("ContainerGitDir = %q, want %q", l.ContainerGitDir, want)
	}
}

func TestLayoutOfANonRepositoryIsNone(t *testing.T) {
	if _, ok := LayoutOf(t.TempDir()); ok {
		t.Error("a plain folder has a layout")
	}
}

// A worktree checkout's .git is a file. The CLI's own layout logic does not
// apply to dev's worktree containers — they mount the checkout at its host
// path — so LayoutOf declines and the worktree route is separate.
func TestLayoutOfAWorktreeCheckoutIsNone(t *testing.T) {
	root := repo(t)
	git(t, root, "-c", "user.name=a", "-c", "user.email=a@b", "commit", "-q", "--allow-empty", "-m", "i")
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, root, "worktree", "add", "-q", wt, "-b", "feat")
	if _, ok := LayoutOf(wt); ok {
		t.Error("a worktree checkout has a CLI layout")
	}
}

func TestMountsOfAnOrdinaryCheckout(t *testing.T) {
	root := repo(t)
	l, _ := LayoutOf(root)
	got := Mounts(l, "/state/hooks/ws/api")
	want := []string{
		"source=" + l.GitDir + ",target=" + l.ContainerGitDir + ",type=bind",
		"source=" + filepath.Join(l.GitDir, "config") + ",target=" + l.ContainerGitDir + "/config,type=bind,readonly",
		"source=/state/hooks/ws/api,target=" + l.ContainerGitDir + "/hooks,type=bind",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Mounts =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// A worktree container's config and hooks are in git's common directory,
// mounted at its own host path, and its own .git file and administration files
// are redirects that must not be rewritable.
func TestWorktreeMounts(t *testing.T) {
	root := repo(t)
	git(t, root, "-c", "user.name=a", "-c", "user.email=a@b", "commit", "-q", "--allow-empty", "-m", "i")
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, root, "worktree", "add", "-q", wt, "-b", "feat")
	common := filepath.Join(root, ".git")

	got, err := WorktreeMounts(common, wt, "/state/hooks/ws/feat")
	if err != nil {
		t.Fatal(err)
	}
	admin := filepath.Join(common, "worktrees", "wt")
	for _, want := range []string{
		"source=" + common + "/config,target=" + common + "/config,type=bind,readonly",
		"source=/state/hooks/ws/feat,target=" + common + "/hooks,type=bind",
		"source=" + wt + "/.git,target=" + wt + "/.git,type=bind,readonly",
		"source=" + admin + "/commondir,target=" + admin + "/commondir,type=bind,readonly",
		"source=" + admin + "/gitdir,target=" + admin + "/gitdir,type=bind,readonly",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %s\nin %s", want, strings.Join(got, "\n   "))
		}
	}
}

// With extensions.worktreeConfig, a worktree's config.worktree is read by host
// git too, so it is a route of its own.
func TestWorktreeMountsCoverConfigWorktree(t *testing.T) {
	root := repo(t)
	git(t, root, "-c", "user.name=a", "-c", "user.email=a@b", "commit", "-q", "--allow-empty", "-m", "i")
	git(t, root, "config", "extensions.worktreeConfig", "true")
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, root, "worktree", "add", "-q", wt, "-b", "feat")
	admin := filepath.Join(root, ".git", "worktrees", "wt")
	if err := os.WriteFile(filepath.Join(admin, "config.worktree"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := WorktreeMounts(filepath.Join(root, ".git"), wt, "/h")
	if err != nil {
		t.Fatal(err)
	}
	want := "source=" + admin + "/config.worktree,target=" + admin + "/config.worktree,type=bind,readonly"
	if !slices.Contains(got, want) {
		t.Errorf("config.worktree not mounted: %v", got)
	}
}

func TestSeedHooksCopiesAndEmptiesFirst(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "pre-commit"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "post-checkout"), []byte("agent wrote this"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SeedHooks(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "post-checkout")); !os.IsNotExist(err) {
		t.Error("what the agent installed survived a re-seed")
	}
	info, err := os.Stat(filepath.Join(dst, "pre-commit"))
	if err != nil {
		t.Fatalf("hook not copied: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("hook lost its executable bit: %v", info.Mode())
	}
}

// The destination is agent-controlled. Seeding must never be led outside it,
// and must never follow a link the agent planted.
func TestSeedHooksNeverFollowsSymlinks(t *testing.T) {
	src, dst, outside := t.TempDir(), t.TempDir(), t.TempDir()
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	// In the destination: a link the agent planted, pointing outside.
	if err := os.Symlink(outside, filepath.Join(dst, "escape")); err != nil {
		t.Fatal(err)
	}
	// In the source: a symlinked hook, copied as a link rather than read.
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "pre-push")); err != nil {
		t.Fatal(err)
	}
	if err := SeedHooks(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(victim); err != nil || string(b) != "keep" {
		t.Errorf("emptying the destination reached outside it: %q, %v", b, err)
	}
	target, err := os.Readlink(filepath.Join(dst, "pre-push"))
	if err != nil || target != "/etc/passwd" {
		t.Errorf("symlinked hook not copied as a link: %q, %v", target, err)
	}
}

func TestSeedHooksFromAMissingDirectoryLeavesItEmpty(t *testing.T) {
	dst := t.TempDir()
	if err := SeedHooks(filepath.Join(t.TempDir(), "nope"), dst); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dst)
	if len(entries) != 0 {
		t.Errorf("dst not empty: %v", entries)
	}
}

// The fallback for layouts mounts cannot reach: a fingerprint of everything
// under .git that host git would execute, compared after the command.
func TestSnapshotNoticesEachRoute(t *testing.T) {
	cases := map[string]func(gitdir string) error{
		"config": func(g string) error {
			return os.WriteFile(filepath.Join(g, "config"), []byte("[core]\n\tfsmonitor = evil\n"), 0o644)
		},
		"new hook": func(g string) error {
			return os.WriteFile(filepath.Join(g, "hooks", "pre-commit"), []byte("evil"), 0o755)
		},
		"commondir": func(g string) error {
			return os.WriteFile(filepath.Join(g, "commondir"), []byte("/evil\n"), 0o644)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			root := repo(t)
			gitdir := filepath.Join(root, ".git")
			before, err := Snapshot(gitdir)
			if err != nil {
				t.Fatal(err)
			}
			if err := change(gitdir); err != nil {
				t.Fatal(err)
			}
			after, _ := Snapshot(gitdir)
			if got := Changed(before, after); len(got) == 0 {
				t.Error("change not noticed")
			}
		})
	}
}

func TestSnapshotIgnoresWhatGitWritesInNormalUse(t *testing.T) {
	root := repo(t)
	gitdir := filepath.Join(root, ".git")
	before, _ := Snapshot(gitdir)
	git(t, root, "-c", "user.name=a", "-c", "user.email=a@b", "commit", "-q", "--allow-empty", "-m", "x")
	after, _ := Snapshot(gitdir)
	if got := Changed(before, after); len(got) != 0 {
		t.Errorf("a commit was reported as a change: %v", got)
	}
}

// commondir is checked on its own too: in an ordinary checkout nothing can be
// mounted there, since a mount needs a target and an empty commondir breaks
// host git outright.
func TestCommondirPlanted(t *testing.T) {
	root := repo(t)
	gitdir := filepath.Join(root, ".git")
	if CommondirPlanted(gitdir) {
		t.Fatal("a fresh repository reports a commondir")
	}
	if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("/evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !CommondirPlanted(gitdir) {
		t.Error("planted commondir not reported")
	}
}
