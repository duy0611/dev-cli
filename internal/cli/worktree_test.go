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

// baseOpts is a worktree create that always keeps herdr out of a unit test:
// a host that has herdr installed should not have its daemon probed or its
// sidebar written to just because a test made a checkout.
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
// gets no generated document, and its two bind mounts are merged into a
// per-invocation override instead (dcgen.OverlayWorktree).
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
	// The worktree row is still written: it is what materialise reads to build
	// the merged override, and what `remove` reads to find the checkout.
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

// One thing this task does not need to change: local.Provider.Remove branches
// on SourceKind == model.SourceNone before removing the workspace volume, and
// a worktree container is SourceFolder — so it removes the state volume and
// never the checkout. A worktree container that deleted its own checkout on
// `container remove` would be the worst bug this feature could have.
func TestWorktreeContainerIsNotRemovedAsFolderless(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	if err := runWorktreeCreate(t.Context(), a, "", "feat", baseOpts(repo, path)); err != nil {
		t.Fatal(err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "feat")
	if err != nil {
		t.Fatal(err)
	}
	if c.SourceKind != model.SourceFolder {
		t.Errorf("SourceKind = %q, want folder", c.SourceKind)
	}
}

func TestWorktreeRemoveTakesBoth(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	if err := runWorktreeCreate(t.Context(), a, "", "feat", baseOpts(repo, path)); err != nil {
		t.Fatal(err)
	}

	// --force, because make test runs with no engine on PATH: what is being
	// asserted is that the container and worktree rows are gone together, not
	// what docker would have said.
	if err := runWorktreeRemove(t.Context(), a, "", "feat", true); err != nil {
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
	// --force for the reason above: no engine on PATH under make test.
	if err := runWorktreeRemove(t.Context(), a, "", "feat", true); err != nil {
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

// stubDocker puts a fake docker on PATH that answers every call with success
// and no output, so the local provider's Remove reaches the point of asking
// git — this is the only test in this file that needs the container removal
// itself to succeed, in order to isolate git's own refusal.
func stubDocker(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
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
	stubDocker(t)

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
