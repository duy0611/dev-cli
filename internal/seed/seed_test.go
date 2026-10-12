package seed

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@b", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@b")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// names lists the entries of a tar stream.
func names(t *testing.T, b []byte) []string {
	t.Helper()
	var out []string
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, h.Name)
	}
	sort.Strings(out)
	return out
}

// The working tree goes in as on the host, and the repository as a bundle,
// never as .git.
func TestArchiveHasTheTreeAndABundleButNoGitDirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main", ".")
	write(t, filepath.Join(dir, "f.txt"), "one\n")
	gitIn(t, dir, "add", "f.txt")
	gitIn(t, dir, "commit", "-qm", "init")
	write(t, filepath.Join(dir, "f.txt"), "one\nwip\n")
	write(t, filepath.Join(dir, "untracked.txt"), "new\n")

	var buf bytes.Buffer
	plan, err := Archive(t.Context(), dir, &buf)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	got := names(t, buf.Bytes())
	for _, want := range []string{"f.txt", "untracked.txt", BundleName} {
		if !contains(got, want) {
			t.Errorf("archive %v lacks %s", got, want)
		}
	}
	for _, n := range got {
		if n == ".git" || strings.HasPrefix(n, ".git/") {
			t.Errorf("archive carries %s; the repository goes in as a bundle", n)
		}
	}
	if plan.Branch != "main" {
		t.Errorf("branch = %q, want main", plan.Branch)
	}
}

// A folder that is not a repository is copied as files, with no bundle.
func TestArchiveOfAPlainFolder(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.txt"), "a")
	var buf bytes.Buffer
	plan, err := Archive(t.Context(), dir, &buf)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if got := names(t, buf.Bytes()); len(got) != 1 || got[0] != "a.txt" {
		t.Errorf("archive = %v, want just a.txt", got)
	}
	if plan.Repository {
		t.Error("a plain folder was planned as a repository")
	}
}

// The seed script, run with host sh, rebuilds the branch, origin and working
// tree.
func TestUnpackedArchiveIsTheSameRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	src := t.TempDir()
	gitIn(t, src, "init", "-q", "-b", "main", ".")
	write(t, filepath.Join(src, "f.txt"), "one\n")
	gitIn(t, src, "add", "f.txt")
	gitIn(t, src, "commit", "-qm", "init")
	gitIn(t, src, "checkout", "-q", "-b", "feat")
	gitIn(t, src, "remote", "add", "origin", "git@github.com:example/app.git")
	write(t, filepath.Join(src, "f.txt"), "one\nwip\n")
	write(t, filepath.Join(src, "new.txt"), "new\n")

	var buf bytes.Buffer
	plan, err := Archive(t.Context(), src, &buf)
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	untar := exec.Command("tar", "-x", "-p", "-C", dst)
	untar.Stdin = &buf
	if out, err := untar.CombinedOutput(); err != nil {
		t.Fatalf("tar: %v\n%s", err, out)
	}
	script := exec.Command("sh", append([]string{"-c", Script}, plan.ScriptArgs(dst)...)...)
	if out, err := script.CombinedOutput(); err != nil {
		t.Fatalf("seed script: %v\n%s", err, out)
	}

	if got := gitIn(t, dst, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat" {
		t.Errorf("branch = %q, want feat", got)
	}
	if got := gitIn(t, dst, "remote", "get-url", "origin"); got != "git@github.com:example/app.git" {
		t.Errorf("origin = %q, want the folder's", got)
	}
	status := gitIn(t, dst, "status", "--porcelain")
	// gitIn trims, so the leading space of " M" is lost.
	if !strings.Contains(status, "M f.txt") || !strings.Contains(status, "?? new.txt") {
		t.Errorf("status = %q, want f.txt modified and new.txt untracked", status)
	}
	if _, err := os.Stat(filepath.Join(dst, BundleName)); !os.IsNotExist(err) {
		t.Error("the bundle was left in the workspace")
	}
	if info, err := os.Stat(filepath.Join(dst, ".git")); err != nil || !info.IsDir() {
		t.Errorf("the seeded .git is not a directory: %v", err)
	}
}

// A worktree folder seeds to a standalone repository, not a .git file.
func TestArchiveOfAWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main", ".")
	write(t, filepath.Join(repo, "f.txt"), "one\n")
	gitIn(t, repo, "add", "f.txt")
	gitIn(t, repo, "commit", "-qm", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "task", wt)

	var buf bytes.Buffer
	plan, err := Archive(t.Context(), wt, &buf)
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	untar := exec.Command("tar", "-x", "-p", "-C", dst)
	untar.Stdin = &buf
	if out, err := untar.CombinedOutput(); err != nil {
		t.Fatalf("tar: %v\n%s", err, out)
	}
	script := exec.Command("sh", append([]string{"-c", Script}, plan.ScriptArgs(dst)...)...)
	if out, err := script.CombinedOutput(); err != nil {
		t.Fatalf("seed script: %v\n%s", err, out)
	}
	if got := gitIn(t, dst, "rev-parse", "--abbrev-ref", "HEAD"); got != "task" {
		t.Errorf("branch = %q, want task", got)
	}
	if info, err := os.Stat(filepath.Join(dst, ".git")); err != nil || !info.IsDir() {
		t.Errorf("the seeded .git is not a directory: %v", err)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
